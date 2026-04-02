package config

import (
	"errors"
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
	RedisAddr             string
	RedisPassword         string
	TelegramWebhookSecret string
	USDToStarsRate        int
	LocalesDir            string
}

// Load reads configuration from environment variables.
// Returns an error if required fields are missing or invalid.
func Load() (*Config, error) {
	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		return nil, errors.New("BOT_TOKEN is required but not set")
	}

	adminIDs, err := parseAdminIDs(os.Getenv("ADMIN_IDS"))
	if err != nil {
		return nil, fmt.Errorf("ADMIN_IDS: %w", err)
	}

	usdToStars, err := parsePositiveInt(os.Getenv("USD_TO_STARS_RATE"), defaultUSDToStarsRate)
	if err != nil {
		return nil, fmt.Errorf("USD_TO_STARS_RATE: %w", err)
	}

	return &Config{
		BotToken:              botToken,
		BotUsername:           os.Getenv("BOT_USERNAME"),
		CryptoBotToken:        os.Getenv("CRYPTOBOT_TOKEN"),
		AdminIDs:              adminIDs,
		WebhookURL:            os.Getenv("WEBHOOK_URL"),
		DBPath:                getEnv("DB_PATH", "data/shop.db"),
		LogLevel:              getEnv("LOG_LEVEL", "info"),
		RedisAddr:             getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:         os.Getenv("REDIS_PASSWORD"),
		TelegramWebhookSecret: os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		USDToStarsRate:        usdToStars,
		LocalesDir:            getEnv("LOCALES_DIR", "locales"),
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
		ids = append(ids, id)
	}
	return ids, nil
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
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
