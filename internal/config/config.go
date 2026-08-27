package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const defaultUSDToStarsRate = 50

type Config struct {
	BotToken              string
	BotUsername           string
	CryptoBotToken        string
	AdminIDs              []int64
	WebhookURL            string
	DBPath                string
	LogLevel              string
	AppEnv                string
	RedisAddr             string
	RedisPassword         string
	TelegramWebhookSecret string
	USDToStarsRate        int
	LocalesDir            string
	OutboundWebhookURL    string
	OutboundWebhookSecret string
	AdminGroupID          int64
	TopicOrdersNew        int
	TopicOrdersPaid       int
	TopicOrdersDelivered  int
	// WebAppURL is the public HTTPS URL of the Mini App (mounted at /app/).
	// Empty disables the Mini App and its REST API entirely.
	WebAppURL string
}

// Load reads configuration from environment variables.
// Returns an error if required fields are missing or invalid.
func Load() (*Config, error) {
	return load(os.LookupEnv)
}

// LoadFromMap validates and loads configuration from values. It is used by
// diagnostics so checking a .env file does not mutate the current process.
func LoadFromMap(values map[string]string) (*Config, error) {
	return load(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
}

type lookupFunc func(string) (string, bool)

func load(lookup lookupFunc) (*Config, error) {
	botToken := strings.TrimSpace(value(lookup, "BOT_TOKEN"))
	if err := ValidateBotToken(botToken); err != nil {
		return nil, fmt.Errorf("BOT_TOKEN: %w", err)
	}

	adminIDs, err := parseAdminIDs(value(lookup, "ADMIN_IDS"))
	if err != nil {
		return nil, fmt.Errorf("ADMIN_IDS: %w", err)
	}

	usdToStars, err := parsePositiveInt(value(lookup, "USD_TO_STARS_RATE"), defaultUSDToStarsRate)
	if err != nil {
		return nil, fmt.Errorf("USD_TO_STARS_RATE: %w", err)
	}

	adminGroupID, err := parseOptionalInt64(value(lookup, "ADMIN_GROUP_ID"))
	if err != nil {
		return nil, fmt.Errorf("ADMIN_GROUP_ID: %w", err)
	}

	topicOrdersNew, err := parsePositiveInt(value(lookup, "TOPIC_ORDERS_NEW"), 0)
	if err != nil {
		return nil, fmt.Errorf("TOPIC_ORDERS_NEW: %w", err)
	}
	topicOrdersPaid, err := parsePositiveInt(value(lookup, "TOPIC_ORDERS_PAID"), 0)
	if err != nil {
		return nil, fmt.Errorf("TOPIC_ORDERS_PAID: %w", err)
	}
	topicOrdersDelivered, err := parsePositiveInt(value(lookup, "TOPIC_ORDERS_DELIVERED"), 0)
	if err != nil {
		return nil, fmt.Errorf("TOPIC_ORDERS_DELIVERED: %w", err)
	}

	webhookURL := strings.TrimSpace(value(lookup, "WEBHOOK_URL"))
	webhookSecret := strings.TrimSpace(value(lookup, "TELEGRAM_WEBHOOK_SECRET"))

	// A configured WEBHOOK_URL makes this endpoint public regardless of the
	// friendly APP_ENV label. Never expose Telegram update handling without a
	// strong shared secret that Telegram sends back on every request.
	if webhookURL != "" {
		if err := ValidateTelegramWebhookSecret(webhookSecret); err != nil {
			return nil, fmt.Errorf("TELEGRAM_WEBHOOK_SECRET: %w when WEBHOOK_URL is set", err)
		}
	}

	return &Config{
		BotToken:              botToken,
		BotUsername:           value(lookup, "BOT_USERNAME"),
		CryptoBotToken:        value(lookup, "CRYPTOBOT_TOKEN"),
		AdminIDs:              adminIDs,
		AdminGroupID:          adminGroupID,
		TopicOrdersNew:        topicOrdersNew,
		TopicOrdersPaid:       topicOrdersPaid,
		TopicOrdersDelivered:  topicOrdersDelivered,
		WebhookURL:            webhookURL,
		DBPath:                getEnv(lookup, "DB_PATH", "data/shop.db"),
		LogLevel:              getEnv(lookup, "LOG_LEVEL", "info"),
		AppEnv:                getEnv(lookup, "APP_ENV", "development"),
		RedisAddr:             getEnv(lookup, "REDIS_ADDR", "localhost:6379"),
		RedisPassword:         value(lookup, "REDIS_PASSWORD"),
		TelegramWebhookSecret: webhookSecret,
		USDToStarsRate:        usdToStars,
		LocalesDir:            getEnv(lookup, "LOCALES_DIR", "locales"),
		OutboundWebhookURL:    value(lookup, "OUTBOUND_WEBHOOK_URL"),
		OutboundWebhookSecret: value(lookup, "OUTBOUND_WEBHOOK_SECRET"),
		WebAppURL:             value(lookup, "WEBAPP_URL"),
	}, nil
}

// parseAdminIDs parses a comma-separated string of Telegram user IDs.
// Returns an error if any entry is non-numeric (empty string is allowed and yields an empty slice).
func parseAdminIDs(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []int64{}, nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid ID %q: %w", p, err)
		}
		if err := ValidateAdminUserID(id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func getEnv(lookup lookupFunc, key, defaultValue string) string {
	if current, exists := lookup(key); exists {
		return current
	}
	return defaultValue
}

func value(lookup lookupFunc, key string) string {
	current, _ := lookup(key)
	return current
}

// parseOptionalInt64 parses s as an int64 (negative values are valid: Telegram
// supergroup IDs are negative); returns 0 when s is empty.
func parseOptionalInt64(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("must be a number, got %q", s)
	}
	return n, nil
}

// parsePositiveInt parses s as a positive integer; returns defaultVal when s is empty.
func parsePositiveInt(s string, defaultVal int) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultVal, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("must be a number, got %q", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be positive, got %d", n)
	}
	return n, nil
}
