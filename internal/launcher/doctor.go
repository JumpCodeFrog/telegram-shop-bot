package launcher

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"shop_bot/internal/config"
	"shop_bot/internal/storage"
)

type CheckStatus string

const (
	CheckOK   CheckStatus = "OK"
	CheckWarn CheckStatus = "WARN"
	CheckFail CheckStatus = "FAIL"
)

type DoctorCheck struct {
	Status CheckStatus
	Label  string
	Detail string
}

type DoctorReport struct {
	Checks []DoctorCheck
}

func (r DoctorReport) ExitCode() int {
	for _, check := range r.Checks {
		if check.Status == CheckFail {
			return 1
		}
	}
	return 0
}

func (r DoctorReport) HasWarnings() bool {
	for _, check := range r.Checks {
		if check.Status == CheckWarn {
			return true
		}
	}
	return false
}

type DoctorOptions struct {
	EnvPath    string
	BaseDir    string
	Out        io.Writer
	Inspector  TelegramInspector
	LookupEnv  func(string) (string, bool)
	CheckRedis func(context.Context, string, string) error
}

func DefaultDoctorOptions() DoctorOptions {
	return DoctorOptions{
		EnvPath:    ".env",
		BaseDir:    ".",
		Out:        os.Stdout,
		Inspector:  NewTelegramClient(10 * time.Second),
		LookupEnv:  os.LookupEnv,
		CheckRedis: CheckRedis,
	}
}

func RunDoctor(ctx context.Context, opts DoctorOptions) DoctorReport {
	opts = normalizeDoctorOptions(opts)
	report := DoctorReport{}
	add := func(status CheckStatus, label, detail string) {
		report.Checks = append(report.Checks, DoctorCheck{Status: status, Label: label, Detail: detail})
	}

	values, envExists, envErr := loadEnvironment(opts.EnvPath, opts.LookupEnv)
	switch {
	case envErr != nil:
		add(CheckFail, "Configuration file", envErr.Error())
	case envExists:
		add(CheckOK, "Configuration file", opts.EnvPath)
		checkEnvPermissions(opts.EnvPath, add)
	default:
		add(CheckWarn, "Configuration file", "not found; checking process environment")
	}

	cfg, err := config.LoadFromMap(values)
	if err != nil {
		add(CheckFail, "Configuration", err.Error())
		printDoctorReport(opts.Out, report)
		return report
	}
	add(CheckOK, "Configuration", "required values are valid")
	if len(cfg.AdminIDs) == 0 {
		add(CheckWarn, "Admin access", "ADMIN_IDS is empty; admin commands will have no users")
	} else {
		add(CheckOK, "Admin access", fmt.Sprintf("%d user(s)", len(cfg.AdminIDs)))
	}

	dbPath := cfg.DBPath
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(opts.BaseDir, dbPath)
	}
	db, err := storage.New(dbPath)
	if err != nil {
		add(CheckFail, "SQLite + migrations", "database could not be opened")
	} else {
		add(CheckOK, "SQLite + migrations", cfg.DBPath)
		if err := db.Close(); err != nil {
			add(CheckWarn, "SQLite close", "database closed with an error")
		}
	}

	if _, _, err := net.SplitHostPort(cfg.RedisAddr); err != nil {
		add(CheckWarn, "Redis", "REDIS_ADDR is invalid; in-memory fallback will be used")
	} else {
		redisCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := opts.CheckRedis(redisCtx, cfg.RedisAddr, cfg.RedisPassword)
		cancel()
		if err != nil {
			add(CheckWarn, "Redis", "unavailable; in-memory fallback will be used")
		} else {
			add(CheckOK, "Redis", cfg.RedisAddr)
		}
	}

	state, err := opts.Inspector.Inspect(ctx, cfg.BotToken)
	if err != nil {
		add(CheckFail, "Telegram API", ErrTelegramCheck.Error())
	} else {
		add(CheckOK, "Telegram API", "@"+state.Identity.Username)
		if state.Identity.SupportsInlineQueries {
			add(CheckOK, "Telegram inline mode", "enabled")
		} else {
			add(CheckWarn, "Telegram inline mode", "disabled; enable it in @BotFather (/setinline) for inline catalog sharing")
		}
		checkWebhook(cfg.WebhookURL, state, add)
	}

	printDoctorReport(opts.Out, report)
	return report
}

func normalizeDoctorOptions(opts DoctorOptions) DoctorOptions {
	defaults := DefaultDoctorOptions()
	if opts.EnvPath == "" {
		opts.EnvPath = defaults.EnvPath
	}
	if opts.BaseDir == "" {
		opts.BaseDir = filepath.Dir(opts.EnvPath)
	}
	if opts.Out == nil {
		opts.Out = defaults.Out
	}
	if opts.Inspector == nil {
		opts.Inspector = defaults.Inspector
	}
	if opts.LookupEnv == nil {
		opts.LookupEnv = defaults.LookupEnv
	}
	if opts.CheckRedis == nil {
		opts.CheckRedis = defaults.CheckRedis
	}
	return opts
}

// CheckRedis validates the service protocol and password, not only whether a
// process accepts TCP connections at the configured address.
func CheckRedis(ctx context.Context, addr, password string) error {
	client := redis.NewClient(&redis.Options{Addr: addr, Password: password, MaxRetries: -1})
	defer client.Close()
	return client.Ping(ctx).Err()
}

func loadEnvironment(path string, lookup func(string) (string, bool)) (map[string]string, bool, error) {
	values := map[string]string{}
	envExists := false
	fileValues, err := godotenv.Read(path)
	switch {
	case err == nil:
		envExists = true
		for key, value := range fileValues {
			values[key] = value
		}
	case !os.IsNotExist(err):
		// Parser errors can embed the malformed line, including BOT_TOKEN.
		// Keep terminal diagnostics actionable without echoing file contents.
		return values, false, fmt.Errorf("read %s: configuration file could not be parsed", path)
	}

	for _, key := range knownEnvironmentKeys {
		if value, ok := lookup(key); ok {
			values[key] = value
		}
	}
	return values, envExists, nil
}

var knownEnvironmentKeys = []string{
	"BOT_TOKEN", "BOT_USERNAME", "ADMIN_IDS", "CRYPTOBOT_TOKEN",
	"DB_PATH", "REDIS_ADDR", "REDIS_PASSWORD", "WEBHOOK_URL",
	"TELEGRAM_WEBHOOK_SECRET", "APP_ENV", "LOG_LEVEL", "USD_TO_STARS_RATE",
	"LOCALES_DIR", "WEBAPP_URL", "OUTBOUND_WEBHOOK_URL", "OUTBOUND_WEBHOOK_SECRET",
	"ADMIN_GROUP_ID", "TOPIC_ORDERS_NEW", "TOPIC_ORDERS_PAID", "TOPIC_ORDERS_DELIVERED",
}

func checkEnvPermissions(path string, add func(CheckStatus, string, string)) {
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		add(CheckFail, "Configuration permissions", "cannot inspect file mode")
		return
	}
	if info.Mode().Perm()&0o077 != 0 {
		add(CheckWarn, "Configuration permissions", "file is shared; run chmod 600 "+path)
		return
	}
	add(CheckOK, "Configuration permissions", "private (0600)")
}

func checkWebhook(configuredURL string, state TelegramState, add func(CheckStatus, string, string)) {
	expected := config.TelegramWebhookURL(configuredURL)
	switch {
	case expected == "" && state.WebhookURL == "":
		add(CheckOK, "Telegram mode", "polling")
	case expected == "" && state.WebhookURL != "":
		add(CheckFail, "Telegram mode", "a webhook is active; clear it with Bot API deleteWebhook or set WEBHOOK_URL before polling")
	case expected == state.WebhookURL:
		add(CheckOK, "Telegram webhook", "configured and matches")
	default:
		add(CheckWarn, "Telegram webhook", "Bot API state does not match WEBHOOK_URL")
	}
	if state.PendingUpdateCount > 0 {
		add(CheckWarn, "Pending updates", fmt.Sprintf("%d queued", state.PendingUpdateCount))
	} else {
		add(CheckOK, "Pending updates", "0")
	}
	if state.LastErrorMessage != "" {
		add(CheckWarn, "Webhook delivery", "Telegram reports a recent delivery error")
	}
}

func printDoctorReport(out io.Writer, report DoctorReport) {
	fmt.Fprintln(out, "Telegram Shop Bot Doctor")
	fmt.Fprintln(out)
	for _, check := range report.Checks {
		fmt.Fprintf(out, "[%s] %s", check.Status, check.Label)
		if check.Detail != "" {
			fmt.Fprintf(out, ": %s", check.Detail)
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out)
	switch {
	case report.ExitCode() != 0:
		fmt.Fprintln(out, "Doctor found blocking failures.")
	case report.HasWarnings():
		fmt.Fprintln(out, "Doctor passed with warnings.")
	default:
		fmt.Fprintln(out, "Doctor passed.")
	}
}
