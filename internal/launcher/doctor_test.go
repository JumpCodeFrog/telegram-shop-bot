package launcher

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
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
	for _, expected := range []string{"chmod 600", "does not match WEBHOOK_URL", "3 queued", "recent delivery error"} {
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
