package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"

	"shop_bot/internal/config"
)

type smokeReport struct {
	lines       []string
	hasWarnings bool
}

func expectedTelegramWebhookURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return strings.TrimRight(raw, "/") + "/telegram-webhook"
}

func buildSmokeReport(self tgbotapi.User, info tgbotapi.WebhookInfo, envWebhookURL string) smokeReport {
	report := smokeReport{}

	add := func(status, label, detail string) {
		line := fmt.Sprintf("[%s] %s", status, label)
		if detail != "" {
			line += ": " + detail
		}
		report.lines = append(report.lines, line)
		if status == "WARN" {
			report.hasWarnings = true
		}
	}

	botLabel := self.FirstName
	if self.UserName != "" {
		botLabel = "@" + self.UserName
	}
	add("OK", "Telegram getMe", fmt.Sprintf("%s (id=%d)", botLabel, self.ID))

	if info.URL == "" {
		add("OK", "Telegram webhook", "not set; polling mode visible from Bot API")
	} else {
		add("OK", "Telegram webhook", info.URL)
	}

	expectedWebhook := expectedTelegramWebhookURL(envWebhookURL)
	switch {
	case expectedWebhook == "" && info.URL != "":
		add("WARN", "WEBHOOK_URL env mismatch", "env is empty but Telegram still has a webhook configured")
	case expectedWebhook != "" && info.URL == "":
		add("WARN", "WEBHOOK_URL env mismatch", "env suggests webhook mode, but Telegram has no webhook configured")
	case expectedWebhook != "" && info.URL != expectedWebhook:
		add("WARN", "WEBHOOK_URL env mismatch", fmt.Sprintf("expected %s, got %s", expectedWebhook, info.URL))
	case expectedWebhook != "":
		add("OK", "WEBHOOK_URL env", expectedWebhook)
	}

	if info.PendingUpdateCount > 0 {
		add("WARN", "Pending updates", fmt.Sprintf("%d update(s) queued", info.PendingUpdateCount))
		if info.URL == "" {
			add("WARN", "Polling consumer", "updates are queued while webhook is not set; the bot process is likely not polling Telegram right now")
		}
	} else {
		add("OK", "Pending updates", "0")
	}

	if info.LastErrorMessage != "" {
		detail := info.LastErrorMessage
		if info.LastErrorDate > 0 {
			at := time.Unix(int64(info.LastErrorDate), 0).UTC().Format(time.RFC3339)
			detail = fmt.Sprintf("%s (at %s)", detail, at)
		}
		add("WARN", "Webhook delivery error", detail)
	}

	return report
}

func main() {
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("[FAIL] Config: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	bot, err := tgbotapi.NewBotAPIWithClient(cfg.BotToken, tgbotapi.APIEndpoint, client)
	if err != nil {
		fmt.Printf("[FAIL] Telegram getMe: %v\n", err)
		os.Exit(1)
	}

	info, err := bot.GetWebhookInfo()
	if err != nil {
		fmt.Printf("[FAIL] Telegram getWebhookInfo: %v\n", err)
		os.Exit(1)
	}

	report := buildSmokeReport(bot.Self, info, cfg.WebhookURL)

	fmt.Println("Telegram Smoke")
	fmt.Println()
	for _, line := range report.lines {
		fmt.Println(line)
	}

	fmt.Println()
	if report.hasWarnings {
		fmt.Println("Telegram smoke completed with warnings.")
		return
	}
	fmt.Println("Telegram smoke completed successfully.")
}
