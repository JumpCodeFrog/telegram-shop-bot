package launcher

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeInspector struct {
	state TelegramState
	err   error
	seen  string
}

func (f *fakeInspector) Inspect(_ context.Context, token string) (TelegramState, error) {
	f.seen = token
	return f.state, f.err
}

func refusedRedis(context.Context, string, string) error {
	return errors.New("connection refused")
}

func TestCheckRedisRejectsPlainTCPService(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
		_ = conn.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := CheckRedis(ctx, listener.Addr().String(), "password"); err == nil {
		t.Fatal("CheckRedis() accepted a non-Redis TCP service")
	}
	<-done
}

func TestRunDoctorPassesWithOptionalRedisWarning(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	dbPath := filepath.Join(dir, "shop.db")
	content := "BOT_TOKEN=" + testToken + "\nADMIN_IDS=42\nDB_PATH=" + dbPath + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	inspector := &fakeInspector{state: TelegramState{Identity: BotIdentity{ID: 7, Username: "shop_bot"}}}
	var output bytes.Buffer

	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath:    envPath,
		Out:        &output,
		Inspector:  inspector,
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckRedis: refusedRedis,
	})
	if report.ExitCode() != 0 || !report.HasWarnings() {
		t.Fatalf("report = %+v", report)
	}
	if inspector.seen != testToken {
		t.Fatalf("inspector token = %q", inspector.seen)
	}
	if strings.Contains(output.String(), testToken) {
		t.Fatal("doctor output leaked token")
	}
	if !strings.Contains(output.String(), "[WARN] Redis") || !strings.Contains(output.String(), "[OK] Telegram API: @shop_bot") {
		t.Fatalf("unexpected output:\n%s", output.String())
	}
}

func TestRunDoctorInvalidConfigStopsBeforeSideEffects(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("BOT_TOKEN=placeholder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspector := &fakeInspector{err: errors.New("must not run")}
	var output bytes.Buffer

	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath:    envPath,
		Out:        &output,
		Inspector:  inspector,
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckRedis: refusedRedis,
	})
	if report.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", report.ExitCode())
	}
	if inspector.seen != "" {
		t.Fatal("inspector was called after invalid configuration")
	}
}

func TestRunDoctorRedactsMalformedEnvironmentLine(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	const secret = "123456789:abcdefghijklmnopqrstuvwxyz_SECRET"
	if err := os.WriteFile(envPath, []byte("BOT_TOKEN='"+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath:    envPath,
		Out:        &output,
		Inspector:  &fakeInspector{},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckRedis: refusedRedis,
	})
	if report.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", report.ExitCode())
	}
	if strings.Contains(output.String(), secret) {
		t.Fatalf("doctor leaked malformed BOT_TOKEN: %s", output.String())
	}
	if !strings.Contains(output.String(), "could not be parsed") {
		t.Fatalf("missing sanitized parse failure: %s", output.String())
	}
}

func TestRunDoctorWarnsOnSharedConfigurationAndWebhookMismatch(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	dbPath := filepath.Join(dir, "shop.db")
	content := "BOT_TOKEN=" + testToken + "\nADMIN_IDS=42\nDB_PATH=" + dbPath +
		"\nWEBHOOK_URL=https://new.example\nTELEGRAM_WEBHOOK_SECRET=0123456789abcdef0123456789abcdef\n"
	if err := os.WriteFile(envPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath: envPath,
		Out:     &output,
		Inspector: &fakeInspector{state: TelegramState{
			Identity:           BotIdentity{ID: 7, Username: "shop_bot"},
			WebhookURL:         "https://old.example/telegram-webhook",
			PendingUpdateCount: 3,
			LastErrorMessage:   "secret provider details",
		}},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckRedis: refusedRedis,
	})
	if report.ExitCode() != 0 || !report.HasWarnings() {
		t.Fatalf("report = %+v", report)
	}
	expectedDetails := []string{"does not match WEBHOOK_URL", "3 queued", "recent delivery error"}
	if runtime.GOOS != "windows" {
		expectedDetails = append(expectedDetails, "chmod 600")
	}
	for _, expected := range expectedDetails {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q in output:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "secret provider details") {
		t.Fatal("doctor printed raw provider error")
	}
}

func TestRunDoctorFailsWhenPollingWouldConflictWithActiveWebhook(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	dbPath := filepath.Join(dir, "shop.db")
	content := "BOT_TOKEN=" + testToken + "\nADMIN_IDS=42\nDB_PATH=" + dbPath + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath: envPath,
		Out:     &output,
		Inspector: &fakeInspector{state: TelegramState{
			Identity:   BotIdentity{ID: 7, Username: "shop_bot"},
			WebhookURL: "https://old.example/telegram-webhook",
		}},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckRedis: refusedRedis,
	})
	if report.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", report.ExitCode())
	}
	if !strings.Contains(output.String(), "deleteWebhook") {
		t.Fatalf("missing actionable webhook recovery: %s", output.String())
	}
}

func TestRunDoctorPassesRedisPasswordToProtocolCheck(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	dbPath := filepath.Join(dir, "shop.db")
	const password = "redis-test-password"
	content := "BOT_TOKEN=" + testToken + "\nADMIN_IDS=42\nDB_PATH=" + dbPath + "\nREDIS_ADDR=cache.example:6379\nREDIS_PASSWORD=" + password + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotAddr, gotPassword string
	var output bytes.Buffer
	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath:   envPath,
		Out:       &output,
		Inspector: &fakeInspector{state: TelegramState{Identity: BotIdentity{ID: 7, Username: "shop_bot"}}},
		LookupEnv: func(string) (string, bool) { return "", false },
		CheckRedis: func(_ context.Context, addr, suppliedPassword string) error {
			gotAddr, gotPassword = addr, suppliedPassword
			return nil
		},
	})
	if report.ExitCode() != 0 || gotAddr != "cache.example:6379" || gotPassword != password {
		t.Fatalf("report = %+v, redis = %q/%q", report, gotAddr, gotPassword)
	}
	if strings.Contains(output.String(), password) {
		t.Fatal("doctor output leaked Redis password")
	}
}

func TestRunDoctorFailsOnPartialYooKassaCredentials(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	dbPath := filepath.Join(dir, "shop.db")
	content := "BOT_TOKEN=" + testToken + "\nADMIN_IDS=42\nDB_PATH=" + dbPath + "\nYOOKASSA_SHOP_ID=123456\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath:    envPath,
		Out:        &output,
		Inspector:  &fakeInspector{},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckRedis: refusedRedis,
	})
	if report.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", report.ExitCode())
	}
	if !strings.Contains(output.String(), "[FAIL] YooKassa payments") ||
		!strings.Contains(output.String(), "must be set together") {
		t.Fatalf("missing partial credential failure:\n%s", output.String())
	}

	// Partial credentials supplied only through the process environment are
	// still diagnosed: the environment overlay must know the YooKassa keys.
	missingEnv := filepath.Join(dir, "missing.env")
	var overlayOut bytes.Buffer
	report = RunDoctor(context.Background(), DoctorOptions{
		EnvPath:   missingEnv,
		Out:       &overlayOut,
		Inspector: &fakeInspector{},
		LookupEnv: func(key string) (string, bool) {
			switch key {
			case "BOT_TOKEN":
				return testToken, true
			case "ADMIN_IDS":
				return "42", true
			case "DB_PATH":
				return dbPath, true
			case "YOOKASSA_SHOP_ID":
				return "123456", true
			}
			return "", false
		},
		CheckRedis: refusedRedis,
	})
	if report.ExitCode() != 1 || !strings.Contains(overlayOut.String(), "[FAIL] YooKassa payments") {
		t.Fatalf("overlay report = %+v, output:\n%s", report, overlayOut.String())
	}
}

func TestRunDoctorWarnsWhenYooKassaRateMissing(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	dbPath := filepath.Join(dir, "shop.db")
	content := "BOT_TOKEN=" + testToken + "\nADMIN_IDS=42\nDB_PATH=" + dbPath +
		"\nYOOKASSA_SHOP_ID=123456\nYOOKASSA_SECRET_KEY=live_secret_do_not_print\nYOOKASSA_RETURN_URL=https://shop.example.com/return\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath:    envPath,
		Out:        &output,
		Inspector:  &fakeInspector{state: TelegramState{Identity: BotIdentity{ID: 7, Username: "shop_bot"}}},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckRedis: refusedRedis,
	})
	if report.ExitCode() != 0 || !report.HasWarnings() {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(output.String(), "[WARN] YooKassa payments") ||
		!strings.Contains(output.String(), "USD_TO_RUB_RATE") ||
		!strings.Contains(output.String(), "RUB payment button stays hidden") {
		t.Fatalf("missing rate warning:\n%s", output.String())
	}
	if strings.Contains(output.String(), "live_secret_do_not_print") {
		t.Fatal("doctor output leaked YooKassa secret key")
	}

	// An explicitly non-positive rate fails configuration on top of the warning.
	zeroRatePath := filepath.Join(dir, "zero.env")
	content = content + "USD_TO_RUB_RATE=0\n"
	if err := os.WriteFile(zeroRatePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var zeroOut bytes.Buffer
	report = RunDoctor(context.Background(), DoctorOptions{
		EnvPath:    zeroRatePath,
		Out:        &zeroOut,
		Inspector:  &fakeInspector{},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckRedis: refusedRedis,
	})
	if report.ExitCode() != 1 {
		t.Fatalf("zero rate ExitCode() = %d, want 1", report.ExitCode())
	}
	if !strings.Contains(zeroOut.String(), "USD_TO_RUB_RATE") {
		t.Fatalf("zero rate report lacks actionable detail:\n%s", zeroOut.String())
	}
}

func TestRunDoctorFailsOnNonHTTPSYooKassaReturnURL(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	dbPath := filepath.Join(dir, "shop.db")
	content := "BOT_TOKEN=" + testToken + "\nADMIN_IDS=42\nDB_PATH=" + dbPath +
		"\nYOOKASSA_SHOP_ID=123456\nYOOKASSA_SECRET_KEY=live_secret_do_not_print\nYOOKASSA_RETURN_URL=http://shop.example.com/return\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath:    envPath,
		Out:        &output,
		Inspector:  &fakeInspector{},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckRedis: refusedRedis,
	})
	if report.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", report.ExitCode())
	}
	if !strings.Contains(output.String(), "[FAIL] YooKassa payments") ||
		!strings.Contains(output.String(), "YOOKASSA_RETURN_URL must be a public https:// URL") {
		t.Fatalf("missing https failure:\n%s", output.String())
	}
	if strings.Contains(output.String(), "live_secret_do_not_print") {
		t.Fatal("doctor output leaked YooKassa secret key")
	}
}

func TestRunDoctorPassesConfiguredYooKassa(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	dbPath := filepath.Join(dir, "shop.db")
	content := "BOT_TOKEN=" + testToken + "\nADMIN_IDS=42\nDB_PATH=" + dbPath +
		"\nYOOKASSA_SHOP_ID=123456\nYOOKASSA_SECRET_KEY=live_secret_do_not_print\nYOOKASSA_RETURN_URL=https://shop.example.com/return\nUSD_TO_RUB_RATE=92.5\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath:    envPath,
		Out:        &output,
		Inspector:  &fakeInspector{state: TelegramState{Identity: BotIdentity{ID: 7, Username: "shop_bot", SupportsInlineQueries: true}}},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckRedis: func(context.Context, string, string) error { return nil },
	})
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0:\n%s", report.ExitCode(), output.String())
	}
	if !strings.Contains(output.String(), "[OK] YooKassa payments: configured") {
		t.Fatalf("missing configured line:\n%s", output.String())
	}
	if strings.Contains(output.String(), "live_secret_do_not_print") {
		t.Fatal("doctor output leaked YooKassa secret key")
	}
}
