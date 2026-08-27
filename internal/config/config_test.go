package config

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Feature: shop_bot, Property 1: Round-trip конфигурации
// For any set of valid environment variables (BOT_TOKEN, CRYPTOBOT_TOKEN, ADMIN_IDS, WEBHOOK_URL, DB_PATH),
// loading config via Load() must return a Config with fields equivalent to the original env values.
func TestConfigRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random valid values.
		botToken := rapid.StringMatching(`[1-9][0-9]{0,11}:[a-zA-Z0-9!@#$%^&*()_+=.-]{1,50}`).Draw(t, "botToken")
		cryptoToken := rapid.StringMatching(`[a-zA-Z0-9:_-]{0,50}`).Draw(t, "cryptoToken")
		webhookURL := rapid.StringMatching(`https?://[a-z0-9]+\.[a-z]{2,4}/[a-z0-9]*`).Draw(t, "webhookURL")
		webhookSecret := rapid.StringMatching(`[a-zA-Z0-9_-]{32,64}`).Draw(t, "webhookSecret")
		dbPath := rapid.StringMatching(`[a-zA-Z0-9/_.-]{1,30}\.db`).Draw(t, "dbPath")

		// Generate a random list of admin IDs.
		adminCount := rapid.IntRange(0, 5).Draw(t, "adminCount")
		adminIDs := make([]int64, adminCount)
		adminParts := make([]string, adminCount)
		for i := 0; i < adminCount; i++ {
			id := rapid.Int64Range(1, 999999999).Draw(t, fmt.Sprintf("adminID_%d", i))
			adminIDs[i] = id
			adminParts[i] = fmt.Sprintf("%d", id)
		}
		adminIDsStr := strings.Join(adminParts, ",")

		// Set env vars.
		t.Cleanup(func() {
			os.Unsetenv("BOT_TOKEN")
			os.Unsetenv("CRYPTOBOT_TOKEN")
			os.Unsetenv("ADMIN_IDS")
			os.Unsetenv("WEBHOOK_URL")
			os.Unsetenv("TELEGRAM_WEBHOOK_SECRET")
			os.Unsetenv("DB_PATH")
		})
		os.Setenv("BOT_TOKEN", botToken)
		os.Setenv("CRYPTOBOT_TOKEN", cryptoToken)
		os.Setenv("ADMIN_IDS", adminIDsStr)
		os.Setenv("WEBHOOK_URL", webhookURL)
		os.Setenv("TELEGRAM_WEBHOOK_SECRET", webhookSecret)
		os.Setenv("DB_PATH", dbPath)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() returned error: %v", err)
		}

		// Verify round-trip: loaded config must match the env values.
		if cfg.BotToken != botToken {
			t.Errorf("BotToken: got %q, want %q", cfg.BotToken, botToken)
		}
		if cfg.CryptoBotToken != cryptoToken {
			t.Errorf("CryptoBotToken: got %q, want %q", cfg.CryptoBotToken, cryptoToken)
		}
		if cfg.WebhookURL != webhookURL {
			t.Errorf("WebhookURL: got %q, want %q", cfg.WebhookURL, webhookURL)
		}
		if cfg.TelegramWebhookSecret != webhookSecret {
			t.Errorf("TelegramWebhookSecret did not round-trip")
		}
		if cfg.DBPath != dbPath {
			t.Errorf("DBPath: got %q, want %q", cfg.DBPath, dbPath)
		}
		if len(cfg.AdminIDs) != len(adminIDs) {
			t.Fatalf("AdminIDs length: got %d, want %d", len(cfg.AdminIDs), len(adminIDs))
		}
		for i, id := range adminIDs {
			if cfg.AdminIDs[i] != id {
				t.Errorf("AdminIDs[%d]: got %d, want %d", i, cfg.AdminIDs[i], id)
			}
		}
	})
}

// Unit tests for config loading — validates Requirements 1.1, 1.2

func TestLoad_MissingBotToken(t *testing.T) {
	// Clear all config env vars.
	for _, key := range []string{"BOT_TOKEN", "CRYPTOBOT_TOKEN", "ADMIN_IDS", "WEBHOOK_URL", "DB_PATH"} {
		os.Unsetenv(key)
	}

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BOT_TOKEN is missing, got nil")
	}
	if !strings.Contains(err.Error(), "BOT_TOKEN") {
		t.Errorf("error should mention BOT_TOKEN, got: %v", err)
	}
}

func TestValidateBotToken(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{name: "valid", token: "123456789:abcdefghijklmnopqrstuvwxyz_ABCD"},
		{name: "future alphabet and short secret", token: "1:future!token@v2"},
		{name: "trimmed", token: "  123456789:abcdefghijklmnopqrstuvwxyz_ABCD  "},
		{name: "empty", token: "", wantErr: true},
		{name: "example placeholder", token: "123456789:AAxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", wantErr: true},
		{name: "missing separator", token: "123456789abcdefghijklmnopqrstuvwxyz", wantErr: true},
		{name: "empty secret", token: "123456:", wantErr: true},
		{name: "path separator", token: "123456:secret/path", wantErr: true},
		{name: "non numeric id", token: "bot-id:abcdefghijklmnopqrstuvwxyz_ABCD", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBotToken(tt.token)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateBotToken() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTelegramWebhookURL(t *testing.T) {
	tests := map[string]string{
		"":                              "",
		"   ":                           "",
		"https://example.com":           "https://example.com/telegram-webhook",
		"https://example.com/":          "https://example.com/telegram-webhook",
		" https://example.com/base/// ": "https://example.com/base/telegram-webhook",
	}
	for input, want := range tests {
		if got := TelegramWebhookURL(input); got != want {
			t.Errorf("TelegramWebhookURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidateTelegramWebhookSecret(t *testing.T) {
	tests := []struct {
		name    string
		secret  string
		wantErr bool
	}{
		{name: "strong hex", secret: "0123456789abcdef0123456789abcdef"},
		{name: "strong Bot API alphabet", secret: "AbCdEfGhIjKlMnOpQrStUvWxYz_12345-"},
		{name: "empty", wantErr: true},
		{name: "too short", secret: "short-secret", wantErr: true},
		{name: "whitespace", secret: "0123456789abcdef0123456789abcde ", wantErr: true},
		{name: "punctuation", secret: "0123456789abcdef0123456789abcde!", wantErr: true},
		{name: "too long", secret: strings.Repeat("a", 257), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTelegramWebhookSecret(tt.secret)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateTelegramWebhookSecret() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadRequiresStrongWebhookSecretRegardlessEnvironment(t *testing.T) {
	for _, appEnv := range []string{"", "development", "test", "production"} {
		t.Run("env_"+appEnv, func(t *testing.T) {
			values := map[string]string{
				"BOT_TOKEN":   "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
				"WEBHOOK_URL": "https://public.example",
				"APP_ENV":     appEnv,
			}
			_, err := LoadFromMap(values)
			if err == nil || !strings.Contains(err.Error(), "TELEGRAM_WEBHOOK_SECRET") {
				t.Fatalf("LoadFromMap() error = %v, want webhook secret failure", err)
			}

			values["TELEGRAM_WEBHOOK_SECRET"] = "still-too-short"
			if _, err := LoadFromMap(values); err == nil {
				t.Fatal("LoadFromMap accepted a weak public-webhook secret")
			}

			values["TELEGRAM_WEBHOOK_SECRET"] = "0123456789abcdef0123456789abcdef"
			if _, err := LoadFromMap(values); err != nil {
				t.Fatalf("LoadFromMap rejected a strong webhook secret: %v", err)
			}
		})
	}
}

func TestLoadFromMapDoesNotReadProcessEnvironment(t *testing.T) {
	t.Setenv("BOT_TOKEN", "999999999:process_environment_token_ABCDE")
	values := map[string]string{
		"BOT_TOKEN": "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"ADMIN_IDS": "42",
	}

	cfg, err := LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.BotToken != values["BOT_TOKEN"] {
		t.Fatalf("BotToken = %q, want map value", cfg.BotToken)
	}
}

func TestLoadRejectsNonUserAdminIDs(t *testing.T) {
	t.Setenv("BOT_TOKEN", "123456789:abcdefghijklmnopqrstuvwxyz_ABCD")
	t.Setenv("ADMIN_IDS", "-1001234567890")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "positive Telegram user IDs") {
		t.Fatalf("Load() error = %v, want positive-user-ID error", err)
	}
}

func TestLoad_InvalidAdminIDs(t *testing.T) {
	os.Setenv("BOT_TOKEN", "123456789:abcdefghijklmnopqrstuvwxyz_ABCD")
	os.Setenv("ADMIN_IDS", "123,abc,456")
	t.Cleanup(func() {
		os.Unsetenv("BOT_TOKEN")
		os.Unsetenv("ADMIN_IDS")
	})

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for non-numeric ADMIN_IDS, got nil")
	}
	if !strings.Contains(err.Error(), "ADMIN_IDS") {
		t.Errorf("error should mention ADMIN_IDS, got: %v", err)
	}
}

func TestLoad_AllParamsValid(t *testing.T) {
	os.Setenv("BOT_TOKEN", "123456789:abcdefghijklmnopqrstuvwxyz_ABCD")
	os.Setenv("CRYPTOBOT_TOKEN", "crypto_xyz")
	os.Setenv("ADMIN_IDS", "111,222,333")
	os.Setenv("WEBHOOK_URL", "https://example.com/hook")
	os.Setenv("TELEGRAM_WEBHOOK_SECRET", "0123456789abcdef0123456789abcdef")
	os.Setenv("DB_PATH", "/tmp/test.db")
	t.Cleanup(func() {
		for _, key := range []string{"BOT_TOKEN", "CRYPTOBOT_TOKEN", "ADMIN_IDS", "WEBHOOK_URL", "TELEGRAM_WEBHOOK_SECRET", "DB_PATH"} {
			os.Unsetenv(key)
		}
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.BotToken != "123456789:abcdefghijklmnopqrstuvwxyz_ABCD" {
		t.Errorf("BotToken = %q, want %q", cfg.BotToken, "123456789:abcdefghijklmnopqrstuvwxyz_ABCD")
	}
	if cfg.CryptoBotToken != "crypto_xyz" {
		t.Errorf("CryptoBotToken = %q, want %q", cfg.CryptoBotToken, "crypto_xyz")
	}
	if cfg.WebhookURL != "https://example.com/hook" {
		t.Errorf("WebhookURL = %q, want %q", cfg.WebhookURL, "https://example.com/hook")
	}
	if cfg.DBPath != "/tmp/test.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/tmp/test.db")
	}

	wantIDs := []int64{111, 222, 333}
	if len(cfg.AdminIDs) != len(wantIDs) {
		t.Fatalf("AdminIDs length = %d, want %d", len(cfg.AdminIDs), len(wantIDs))
	}
	for i, id := range wantIDs {
		if cfg.AdminIDs[i] != id {
			t.Errorf("AdminIDs[%d] = %d, want %d", i, cfg.AdminIDs[i], id)
		}
	}
}

func TestLoad_AdminGroupAndTopics(t *testing.T) {
	t.Setenv("BOT_TOKEN", "123456789:abcdefghijklmnopqrstuvwxyz_ABCD")
	t.Setenv("ADMIN_GROUP_ID", "-1001234567890")
	t.Setenv("TOPIC_ORDERS_NEW", "5")
	t.Setenv("TOPIC_ORDERS_PAID", "7")
	t.Setenv("TOPIC_ORDERS_DELIVERED", "9")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.AdminGroupID != -1001234567890 {
		t.Errorf("AdminGroupID = %d, want -1001234567890", cfg.AdminGroupID)
	}
	if cfg.TopicOrdersNew != 5 || cfg.TopicOrdersPaid != 7 || cfg.TopicOrdersDelivered != 9 {
		t.Errorf("topics = %d/%d/%d, want 5/7/9", cfg.TopicOrdersNew, cfg.TopicOrdersPaid, cfg.TopicOrdersDelivered)
	}
}

func TestLoad_AdminGroupUnsetDefaultsToZero(t *testing.T) {
	t.Setenv("BOT_TOKEN", "123456789:abcdefghijklmnopqrstuvwxyz_ABCD")
	for _, key := range []string{"ADMIN_GROUP_ID", "TOPIC_ORDERS_NEW", "TOPIC_ORDERS_PAID", "TOPIC_ORDERS_DELIVERED"} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.AdminGroupID != 0 {
		t.Errorf("AdminGroupID = %d, want 0", cfg.AdminGroupID)
	}
	if cfg.TopicOrdersNew != 0 || cfg.TopicOrdersPaid != 0 || cfg.TopicOrdersDelivered != 0 {
		t.Errorf("topics = %d/%d/%d, want 0/0/0", cfg.TopicOrdersNew, cfg.TopicOrdersPaid, cfg.TopicOrdersDelivered)
	}
}

func TestLoad_InvalidAdminGroupID(t *testing.T) {
	t.Setenv("BOT_TOKEN", "123456789:abcdefghijklmnopqrstuvwxyz_ABCD")
	t.Setenv("ADMIN_GROUP_ID", "not-a-number")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for non-numeric ADMIN_GROUP_ID, got nil")
	}
	if !strings.Contains(err.Error(), "ADMIN_GROUP_ID") {
		t.Errorf("error should mention ADMIN_GROUP_ID, got: %v", err)
	}
}
