package config

import (
	"errors"
	"strconv"
	"strings"
)

const maxTelegramUserID int64 = (1 << 52) - 1

const (
	minTelegramWebhookSecretBytes = 32
	maxTelegramWebhookSecretBytes = 256
)

// ValidateBotToken rejects empty/example/path-like values without freezing a
// future BotFather secret length or alphabet. Telegram getMe is authoritative.
func ValidateBotToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("is required but not set")
	}
	lower := strings.ToLower(token)
	if strings.Contains(lower, "your_token") || strings.Contains(lower, "xxxxxxxx") {
		return errors.New("still contains an example placeholder")
	}
	if strings.ContainsAny(token, "/\\\r\n\t ") {
		return errors.New("must be a single BotFather token without whitespace or path separators")
	}
	prefix, secret, ok := strings.Cut(token, ":")
	botID, err := strconv.ParseInt(prefix, 10, 64)
	if !ok || secret == "" || err != nil || botID <= 0 {
		return errors.New("must contain a positive bot ID, a colon, and a non-empty secret")
	}
	return nil
}

// ValidateAdminUserID accepts Telegram user IDs, not group or channel IDs.
func ValidateAdminUserID(id int64) error {
	if id <= 0 || id > maxTelegramUserID {
		return errors.New("must contain positive Telegram user IDs")
	}
	return nil
}

// ValidateTelegramWebhookSecret enforces a high-entropy-compatible Telegram
// secret_token. Telegram accepts A-Z, a-z, 0-9, underscore and hyphen; a
// 32-character minimum gives operators enough room for at least 128 bits of
// randomness while keeping the value compatible with the Bot API.
func ValidateTelegramWebhookSecret(secret string) error {
	if secret == "" {
		return errors.New("is required")
	}
	if len(secret) < minTelegramWebhookSecretBytes {
		return errors.New("must be at least 32 characters")
	}
	if len(secret) > maxTelegramWebhookSecretBytes {
		return errors.New("must be at most 256 characters")
	}
	for _, ch := range secret {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			continue
		}
		return errors.New("may contain only A-Z, a-z, 0-9, underscore and hyphen")
	}
	return nil
}

// TelegramWebhookURL turns the configured public base URL into the single
// Telegram callback URL used by runtime checks, registration, and smoke tools.
func TelegramWebhookURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return ""
	}
	return base + "/telegram-webhook"
}
