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

// YooKassa RUB card payments.

func TestYooKassaConfigLoadsWhenComplete(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":           "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"YOOKASSA_SHOP_ID":    "123",
		"YOOKASSA_SECRET_KEY": "live_abc",
		"YOOKASSA_RETURN_URL": "https://shop.example.com/return",
	}

	cfg, err := LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.YooKassaShopID != "123" {
		t.Errorf("YooKassaShopID = %q, want %q", cfg.YooKassaShopID, "123")
	}
	if cfg.YooKassaSecretKey != "live_abc" {
		t.Errorf("YooKassaSecretKey = %q, want %q", cfg.YooKassaSecretKey, "live_abc")
	}
	if cfg.YooKassaReturnURL != "https://shop.example.com/return" {
		t.Errorf("YooKassaReturnURL = %q, want %q", cfg.YooKassaReturnURL, "https://shop.example.com/return")
	}
}

func TestYooKassaConfigPartialCredentialsRejected(t *testing.T) {
	complete := map[string]string{
		"YOOKASSA_SHOP_ID":    "123",
		"YOOKASSA_SECRET_KEY": "live_abc",
		"YOOKASSA_RETURN_URL": "https://shop.example.com/return",
	}
	const botToken = "123456789:abcdefghijklmnopqrstuvwxyz_ABCD"

	// Any subset of the three (but not all) must fail: half-configured
	// credentials must never silently disable the provider.
	cases := map[string]map[string]string{}
	for drop := range complete {
		values := map[string]string{"BOT_TOKEN": botToken}
		for key, val := range complete {
			if key != drop {
				values[key] = val
			}
		}
		cases["missing_"+drop] = values
	}
	for only := range complete {
		values := map[string]string{"BOT_TOKEN": botToken, only: complete[only]}
		cases["only_"+only] = values
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFromMap(values)
			if err == nil {
				t.Fatal("expected error for partial YOOKASSA credentials, got nil")
			}
			if !strings.Contains(err.Error(), "YOOKASSA") {
				t.Errorf("error should mention YOOKASSA, got: %v", err)
			}
		})
	}
}

func TestYooKassaReturnURLMustBeHTTPS(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":           "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"YOOKASSA_SHOP_ID":    "123",
		"YOOKASSA_SECRET_KEY": "live_abc",
		"YOOKASSA_RETURN_URL": "http://shop.example.com/return",
	}

	_, err := LoadFromMap(values)
	if err == nil {
		t.Fatal("expected error for non-HTTPS YOOKASSA_RETURN_URL, got nil")
	}
	if !strings.Contains(err.Error(), "YOOKASSA_RETURN_URL") {
		t.Errorf("error should mention YOOKASSA_RETURN_URL, got: %v", err)
	}
}

func TestUSDToRUBRateDefaultsToZeroAndParsesFloat(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN": "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
	}

	// Unset -> 0 (RUB payments disabled).
	cfg, err := LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.USDToRUBRate != 0 {
		t.Errorf("USDToRUBRate = %v, want 0 when unset", cfg.USDToRUBRate)
	}

	// Valid float.
	values["USD_TO_RUB_RATE"] = "92.5"
	cfg, err = LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.USDToRUBRate != 92.5 {
		t.Errorf("USDToRUBRate = %v, want 92.5", cfg.USDToRUBRate)
	}

	// Invalid values must be rejected when set explicitly, including the
	// non-finite NaN/Inf spellings that ParseFloat accepts with err == nil.
	for _, raw := range []string{"abc", "-5", "0", "NaN", "Inf"} {
		values["USD_TO_RUB_RATE"] = raw
		if _, err := LoadFromMap(values); err == nil {
			t.Errorf("USD_TO_RUB_RATE = %q: expected error, got nil", raw)
		}
	}
}

func TestYooKassaWebhookURLDerivesFromBase(t *testing.T) {
	tests := map[string]string{
		"":                              "",
		"   ":                           "",
		"https://shop.example.com":      "https://shop.example.com/yookassa-webhook",
		"https://shop.example.com/":     "https://shop.example.com/yookassa-webhook",
		" https://shop.example.com/// ": "https://shop.example.com/yookassa-webhook",
	}
	for input, want := range tests {
		if got := YooKassaWebhookURL(input); got != want {
			t.Errorf("YooKassaWebhookURL(%q) = %q, want %q", input, got, want)
		}
	}
}

// Stripe USD card payments.

func TestStripeConfigLoadsWhenComplete(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":             "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"STRIPE_SECRET_KEY":     "sk_test_abc",
		"STRIPE_WEBHOOK_SECRET": "whsec_abc",
		"STRIPE_RETURN_URL":     "https://shop.example.com/return",
	}

	cfg, err := LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.StripeSecretKey != "sk_test_abc" {
		t.Errorf("StripeSecretKey = %q, want %q", cfg.StripeSecretKey, "sk_test_abc")
	}
	if cfg.StripeWebhookSecret != "whsec_abc" {
		t.Errorf("StripeWebhookSecret = %q, want %q", cfg.StripeWebhookSecret, "whsec_abc")
	}
	if cfg.StripeReturnURL != "https://shop.example.com/return" {
		t.Errorf("StripeReturnURL = %q, want %q", cfg.StripeReturnURL, "https://shop.example.com/return")
	}
}

func TestStripeConfigUnsetIsDisabled(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN": "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
	}

	cfg, err := LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.StripeSecretKey != "" || cfg.StripeWebhookSecret != "" || cfg.StripeReturnURL != "" {
		t.Errorf("Stripe config = %q/%q/%q, want all empty when unset",
			cfg.StripeSecretKey, cfg.StripeWebhookSecret, cfg.StripeReturnURL)
	}
}

func TestStripeConfigPartialCredentialsRejected(t *testing.T) {
	complete := map[string]string{
		"STRIPE_SECRET_KEY":     "sk_test_abc",
		"STRIPE_WEBHOOK_SECRET": "whsec_abc",
		"STRIPE_RETURN_URL":     "https://shop.example.com/return",
	}
	const botToken = "123456789:abcdefghijklmnopqrstuvwxyz_ABCD"

	// Any subset of the three (but not all) must fail: half-configured
	// credentials must never silently disable the provider.
	cases := map[string]map[string]string{}
	for drop := range complete {
		values := map[string]string{"BOT_TOKEN": botToken}
		for key, val := range complete {
			if key != drop {
				values[key] = val
			}
		}
		cases["missing_"+drop] = values
	}
	for only := range complete {
		values := map[string]string{"BOT_TOKEN": botToken, only: complete[only]}
		cases["only_"+only] = values
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFromMap(values)
			if err == nil {
				t.Fatal("expected error for partial STRIPE credentials, got nil")
			}
			if !strings.Contains(err.Error(), "STRIPE") {
				t.Errorf("error should mention STRIPE, got: %v", err)
			}
		})
	}
}

func TestStripeReturnURLMustBeHTTPS(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":             "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"STRIPE_SECRET_KEY":     "sk_test_abc",
		"STRIPE_WEBHOOK_SECRET": "whsec_abc",
		"STRIPE_RETURN_URL":     "http://shop.example.com/return",
	}

	_, err := LoadFromMap(values)
	if err == nil {
		t.Fatal("expected error for non-HTTPS STRIPE_RETURN_URL, got nil")
	}
	if !strings.Contains(err.Error(), "STRIPE_RETURN_URL") {
		t.Errorf("error should mention STRIPE_RETURN_URL, got: %v", err)
	}
}

func TestStripeSecretKeyMustHaveKnownPrefix(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":             "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"STRIPE_SECRET_KEY":     "not-a-stripe-key",
		"STRIPE_WEBHOOK_SECRET": "whsec_abc",
		"STRIPE_RETURN_URL":     "https://shop.example.com/return",
	}

	_, err := LoadFromMap(values)
	if err == nil {
		t.Fatal("expected error for STRIPE_SECRET_KEY without sk_live_/sk_test_ prefix, got nil")
	}
	if !strings.Contains(err.Error(), "STRIPE_SECRET_KEY") {
		t.Errorf("error should mention STRIPE_SECRET_KEY, got: %v", err)
	}

	// Both the test-mode and live-mode prefixes are accepted.
	for _, key := range []string{"sk_test_abc", "sk_live_abc"} {
		values["STRIPE_SECRET_KEY"] = key
		if _, err := LoadFromMap(values); err != nil {
			t.Errorf("LoadFromMap() rejected %q: %v", key, err)
		}
	}
}

func TestStripeWebhookSecretMustHaveKnownPrefix(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":             "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"STRIPE_SECRET_KEY":     "sk_test_abc",
		"STRIPE_WEBHOOK_SECRET": "not-a-signing-secret",
		"STRIPE_RETURN_URL":     "https://shop.example.com/return",
	}

	_, err := LoadFromMap(values)
	if err == nil {
		t.Fatal("expected error for STRIPE_WEBHOOK_SECRET without whsec_ prefix, got nil")
	}
	if !strings.Contains(err.Error(), "STRIPE_WEBHOOK_SECRET") {
		t.Errorf("error should mention STRIPE_WEBHOOK_SECRET, got: %v", err)
	}
}

func TestStripeWebhookURLDerivesFromBase(t *testing.T) {
	tests := map[string]string{
		"":                              "",
		"   ":                           "",
		"https://shop.example.com":      "https://shop.example.com/stripe-webhook",
		"https://shop.example.com/":     "https://shop.example.com/stripe-webhook",
		" https://shop.example.com/// ": "https://shop.example.com/stripe-webhook",
	}
	for input, want := range tests {
		if got := StripeWebhookURL(input); got != want {
			t.Errorf("StripeWebhookURL(%q) = %q, want %q", input, got, want)
		}
	}
}

// TON on-chain payments (polling provider — no webhook).

// tonTestAddress is a well-formed 48-char base64url TON friendly address.
const tonTestAddress = "EQCD39VS5jcptHL8vMjEXrzGaRcCVYto7HUn4bpAOg8xqB2N"

func TestTONConfigLoadsWhenComplete(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":          "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"TON_WALLET_ADDRESS": tonTestAddress,
		"USD_PER_TON":        "5.25",
		"TON_API_KEY":        "toncenter-key",
	}

	cfg, err := LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.TONWalletAddress != tonTestAddress {
		t.Errorf("TONWalletAddress = %q, want %q", cfg.TONWalletAddress, tonTestAddress)
	}
	if cfg.USDPerTON != 5.25 {
		t.Errorf("USDPerTON = %v, want 5.25", cfg.USDPerTON)
	}
	if cfg.TONAPIKey != "toncenter-key" {
		t.Errorf("TONAPIKey = %q, want %q", cfg.TONAPIKey, "toncenter-key")
	}
}

func TestTONConfigLoadsWithoutAPIKey(t *testing.T) {
	// The toncenter API key is optional: address + rate alone enable TON.
	values := map[string]string{
		"BOT_TOKEN":          "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"TON_WALLET_ADDRESS": tonTestAddress,
		"USD_PER_TON":        "5.25",
	}

	cfg, err := LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.TONWalletAddress != tonTestAddress || cfg.USDPerTON != 5.25 {
		t.Errorf("TON config = %q/%v, want %q/5.25", cfg.TONWalletAddress, cfg.USDPerTON, tonTestAddress)
	}
	if cfg.TONAPIKey != "" {
		t.Errorf("TONAPIKey = %q, want empty when unset", cfg.TONAPIKey)
	}
}

func TestTONConfigUnsetIsDisabled(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN": "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
	}

	cfg, err := LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.TONWalletAddress != "" || cfg.USDPerTON != 0 || cfg.TONAPIKey != "" {
		t.Errorf("TON config = %q/%v/%q, want all empty when unset",
			cfg.TONWalletAddress, cfg.USDPerTON, cfg.TONAPIKey)
	}
}

func TestTONAddressRequiresRate(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":          "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"TON_WALLET_ADDRESS": tonTestAddress,
	}

	_, err := LoadFromMap(values)
	if err == nil {
		t.Fatal("expected error for TON_WALLET_ADDRESS without USD_PER_TON, got nil")
	}
	if !strings.Contains(err.Error(), "USD_PER_TON") {
		t.Errorf("error should mention USD_PER_TON, got: %v", err)
	}
}

func TestTONRateRequiresAddress(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":   "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"USD_PER_TON": "5.25",
	}

	_, err := LoadFromMap(values)
	if err == nil {
		t.Fatal("expected error for USD_PER_TON without TON_WALLET_ADDRESS, got nil")
	}
	if !strings.Contains(err.Error(), "TON_WALLET_ADDRESS") {
		t.Errorf("error should mention TON_WALLET_ADDRESS, got: %v", err)
	}
}

func TestTONAPIKeyAloneRejected(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":   "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"TON_API_KEY": "toncenter-key",
	}

	_, err := LoadFromMap(values)
	if err == nil {
		t.Fatal("expected error for TON_API_KEY without address and rate, got nil")
	}
	if !strings.Contains(err.Error(), "TON_API_KEY") {
		t.Errorf("error should mention TON_API_KEY, got: %v", err)
	}
}

func TestTONAddressShape(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":          "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"TON_WALLET_ADDRESS": tonTestAddress,
		"USD_PER_TON":        "5.25",
	}

	bad := map[string]string{
		"too short":        tonTestAddress[:47],
		"too long":         tonTestAddress + "A",
		"bad charset":      tonTestAddress[:47] + "+",
		"padding rejected": tonTestAddress[:46] + "==",
	}
	for name, addr := range bad {
		t.Run(name, func(t *testing.T) {
			values["TON_WALLET_ADDRESS"] = addr
			_, err := LoadFromMap(values)
			if err == nil {
				t.Fatalf("expected error for TON_WALLET_ADDRESS %q, got nil", addr)
			}
			if !strings.Contains(err.Error(), "TON_WALLET_ADDRESS") {
				t.Errorf("error should mention TON_WALLET_ADDRESS, got: %v", err)
			}
		})
	}

	// A well-formed address passes shape validation (full ownership checks
	// are toncenter's job at polling time, not config's).
	values["TON_WALLET_ADDRESS"] = tonTestAddress
	if _, err := LoadFromMap(values); err != nil {
		t.Errorf("LoadFromMap() rejected a well-formed address: %v", err)
	}
}

func TestUSDPerTONRateGuards(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":          "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"TON_WALLET_ADDRESS": tonTestAddress,
	}

	// Non-finite and non-numeric spellings must be rejected when set
	// explicitly, exactly like USD_TO_RUB_RATE. A zero rate with an address
	// set is a configuration error, not "disabled".
	for _, raw := range []string{"abc", "-5", "0", "NaN", "Inf"} {
		values["USD_PER_TON"] = raw
		if _, err := LoadFromMap(values); err == nil {
			t.Errorf("USD_PER_TON = %q with address set: expected error, got nil", raw)
		}
	}
}

// NOWPayments crypto payments.

func TestNowpaymentsConfigLoadsWhenComplete(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":              "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"NOWPAYMENTS_API_KEY":    "np-api-key",
		"NOWPAYMENTS_IPN_SECRET": "np-ipn-secret",
		"NOWPAYMENTS_RETURN_URL": "https://shop.example.com/return",
	}

	cfg, err := LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.NowpaymentsAPIKey != "np-api-key" {
		t.Errorf("NowpaymentsAPIKey = %q, want %q", cfg.NowpaymentsAPIKey, "np-api-key")
	}
	if cfg.NowpaymentsIPNSecret != "np-ipn-secret" {
		t.Errorf("NowpaymentsIPNSecret = %q, want %q", cfg.NowpaymentsIPNSecret, "np-ipn-secret")
	}
	if cfg.NowpaymentsReturnURL != "https://shop.example.com/return" {
		t.Errorf("NowpaymentsReturnURL = %q, want %q", cfg.NowpaymentsReturnURL, "https://shop.example.com/return")
	}
}

func TestNowpaymentsConfigUnsetIsDisabled(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN": "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
	}

	cfg, err := LoadFromMap(values)
	if err != nil {
		t.Fatalf("LoadFromMap() error = %v", err)
	}
	if cfg.NowpaymentsAPIKey != "" || cfg.NowpaymentsIPNSecret != "" || cfg.NowpaymentsReturnURL != "" {
		t.Errorf("NOWPayments config = %q/%q/%q, want all empty when unset",
			cfg.NowpaymentsAPIKey, cfg.NowpaymentsIPNSecret, cfg.NowpaymentsReturnURL)
	}
}

func TestNowpaymentsConfigPartialCredentialsRejected(t *testing.T) {
	complete := map[string]string{
		"NOWPAYMENTS_API_KEY":    "np-api-key",
		"NOWPAYMENTS_IPN_SECRET": "np-ipn-secret",
		"NOWPAYMENTS_RETURN_URL": "https://shop.example.com/return",
	}
	const botToken = "123456789:abcdefghijklmnopqrstuvwxyz_ABCD"

	// Any subset of the three (but not all) must fail: half-configured
	// credentials must never silently disable the provider.
	cases := map[string]map[string]string{}
	for drop := range complete {
		values := map[string]string{"BOT_TOKEN": botToken}
		for key, val := range complete {
			if key != drop {
				values[key] = val
			}
		}
		cases["missing_"+drop] = values
	}
	for only := range complete {
		values := map[string]string{"BOT_TOKEN": botToken, only: complete[only]}
		cases["only_"+only] = values
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFromMap(values)
			if err == nil {
				t.Fatal("expected error for partial NOWPAYMENTS credentials, got nil")
			}
			if !strings.Contains(err.Error(), "NOWPAYMENTS") {
				t.Errorf("error should mention NOWPAYMENTS, got: %v", err)
			}
		})
	}
}

func TestNowpaymentsReturnURLMustBeHTTPS(t *testing.T) {
	values := map[string]string{
		"BOT_TOKEN":              "123456789:abcdefghijklmnopqrstuvwxyz_ABCD",
		"NOWPAYMENTS_API_KEY":    "np-api-key",
		"NOWPAYMENTS_IPN_SECRET": "np-ipn-secret",
		"NOWPAYMENTS_RETURN_URL": "http://shop.example.com/return",
	}

	_, err := LoadFromMap(values)
	if err == nil {
		t.Fatal("expected error for non-HTTPS NOWPAYMENTS_RETURN_URL, got nil")
	}
	if !strings.Contains(err.Error(), "NOWPAYMENTS_RETURN_URL") {
		t.Errorf("error should mention NOWPAYMENTS_RETURN_URL, got: %v", err)
	}
}

func TestNowpaymentsWebhookURLDerivesFromBase(t *testing.T) {
	tests := map[string]string{
		"":                              "",
		"   ":                           "",
		"https://shop.example.com":      "https://shop.example.com/nowpayments-webhook",
		"https://shop.example.com/":     "https://shop.example.com/nowpayments-webhook",
		" https://shop.example.com/// ": "https://shop.example.com/nowpayments-webhook",
	}
	for input, want := range tests {
		if got := NowpaymentsWebhookURL(input); got != want {
			t.Errorf("NowpaymentsWebhookURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsHTTPSURL(t *testing.T) {
	// The scheme compare is case-insensitive per RFC 3986 (an uppercase
	// HTTPS:// must be accepted); a bare scheme without a host is not a
	// usable public URL.
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"https://shop.example.com/return", true},
		{"HTTPS://shop.example.com/return", true},
		{"https://shop.example.com", true},
		{"http://shop.example.com/return", false},
		{"https://", false},
		{"https:///path-only", false},
		{"ftp://shop.example.com", false},
		{"", false},
		{"not a url", false},
	} {
		if got := isHTTPSURL(tc.raw); got != tc.want {
			t.Errorf("isHTTPSURL(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestProviderReturnURLHTTPSValidationShared(t *testing.T) {
	// Every provider return-URL validator accepts an uppercase HTTPS scheme
	// and rejects a hostless https:// — the shared isHTTPSURL contract.
	validators := map[string]func(returnURL string) error{
		"yookassa":    func(u string) error { return ValidateYooKassaConfig("123", "live_abc", u) },
		"stripe":      func(u string) error { return ValidateStripeConfig("sk_test_abc", "whsec_abc", u) },
		"nowpayments": func(u string) error { return ValidateNowpaymentsConfig("np-api-key", "np-ipn-secret", u) },
	}
	for name, validate := range validators {
		if err := validate("HTTPS://shop.example.com/return"); err != nil {
			t.Errorf("%s: uppercase HTTPS scheme rejected: %v", name, err)
		}
		if err := validate("https://"); err == nil {
			t.Errorf("%s: hostless https:// accepted", name)
		}
	}
}
