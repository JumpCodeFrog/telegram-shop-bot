package launcher

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

	// YooKassa and Stripe credentials are diagnosed from the raw values so a
	// partial or non-HTTPS configuration still gets a labeled, actionable line
	// even though configuration loading rejects it outright below.
	checkYooKassaPayments(values, add)
	checkStripePayments(values, add)

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
	"YOOKASSA_SHOP_ID", "YOOKASSA_SECRET_KEY", "YOOKASSA_RETURN_URL", "USD_TO_RUB_RATE",
	"STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "STRIPE_RETURN_URL",
}

// checkYooKassaPayments reports the YooKassa RUB rail state: not configured,
// configured, partially configured, or configured without the USD_TO_RUB rate
// that gates the checkout button. It reuses the shared configuration
// validation so the doctor and the bot can never disagree, and it never prints
// credential values.
func checkYooKassaPayments(values map[string]string, add func(CheckStatus, string, string)) {
	shopID := strings.TrimSpace(values["YOOKASSA_SHOP_ID"])
	secretKey := strings.TrimSpace(values["YOOKASSA_SECRET_KEY"])
	returnURL := strings.TrimSpace(values["YOOKASSA_RETURN_URL"])
	if err := config.ValidateYooKassaConfig(shopID, secretKey, returnURL); err != nil {
		add(CheckFail, "YooKassa payments", err.Error())
		return
	}
	if shopID == "" {
		add(CheckOK, "YooKassa payments", "not configured")
		return
	}
	rate := strings.TrimSpace(values["USD_TO_RUB_RATE"])
	if rate == "" {
		add(CheckWarn, "YooKassa payments", "USD_TO_RUB_RATE is not set; the RUB payment button stays hidden")
		return
	}
	parsed, err := strconv.ParseFloat(rate, 64)
	if err != nil || parsed <= 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		add(CheckWarn, "YooKassa payments", "USD_TO_RUB_RATE is not a positive number; the RUB payment button stays hidden")
		return
	}
	add(CheckOK, "YooKassa payments", "configured")
}

// checkStripePayments reports the Stripe USD card rail state: not configured,
// configured, or partially/invalidly configured. It reuses the shared
// configuration validation so the doctor and the bot can never disagree, and
// it never prints credential values. Stripe is USD-native, so unlike YooKassa
// there is no exchange rate gating the checkout button.
func checkStripePayments(values map[string]string, add func(CheckStatus, string, string)) {
	secretKey := strings.TrimSpace(values["STRIPE_SECRET_KEY"])
	webhookSecret := strings.TrimSpace(values["STRIPE_WEBHOOK_SECRET"])
	returnURL := strings.TrimSpace(values["STRIPE_RETURN_URL"])
	if err := config.ValidateStripeConfig(secretKey, webhookSecret, returnURL); err != nil {
		add(CheckFail, "Stripe payments", err.Error())
		return
	}
	if secretKey == "" {
		add(CheckOK, "Stripe payments", "not configured")
		return
	}
	add(CheckOK, "Stripe payments", "configured")
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
