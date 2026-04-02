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
		botToken := rapid.StringMatching(`[a-zA-Z0-9:_-]{10,50}`).Draw(t, "botToken")
		cryptoToken := rapid.StringMatching(`[a-zA-Z0-9:_-]{0,50}`).Draw(t, "cryptoToken")
		webhookURL := rapid.StringMatching(`https?://[a-z0-9]+\.[a-z]{2,4}/[a-z0-9]*`).Draw(t, "webhookURL")
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
			os.Unsetenv("DB_PATH")
		})
		os.Setenv("BOT_TOKEN", botToken)
		os.Setenv("CRYPTOBOT_TOKEN", cryptoToken)
		os.Setenv("ADMIN_IDS", adminIDsStr)
		os.Setenv("WEBHOOK_URL", webhookURL)
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

func TestLoad_InvalidAdminIDs(t *testing.T) {
	os.Setenv("BOT_TOKEN", "test-token-123")
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
	os.Setenv("BOT_TOKEN", "tok_abc123")
	os.Setenv("CRYPTOBOT_TOKEN", "crypto_xyz")
	os.Setenv("ADMIN_IDS", "111,222,333")
	os.Setenv("WEBHOOK_URL", "https://example.com/hook")
	os.Setenv("DB_PATH", "/tmp/test.db")
	t.Cleanup(func() {
		for _, key := range []string{"BOT_TOKEN", "CRYPTOBOT_TOKEN", "ADMIN_IDS", "WEBHOOK_URL", "DB_PATH"} {
			os.Unsetenv(key)
		}
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.BotToken != "tok_abc123" {
		t.Errorf("BotToken = %q, want %q", cfg.BotToken, "tok_abc123")
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
