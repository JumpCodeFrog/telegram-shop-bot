package main

import (
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestExpectedTelegramWebhookURL(t *testing.T) {
	if got := expectedTelegramWebhookURL(""); got != "" {
		t.Fatalf("expected empty webhook URL, got %q", got)
	}

	want := "https://example.com/base/telegram-webhook"
	if got := expectedTelegramWebhookURL("https://example.com/base/"); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildSmokeReport_PollingHappyPath(t *testing.T) {
	report := buildSmokeReport(
		tgbotapi.User{ID: 42, FirstName: "SmokeBot", UserName: "smoke_bot"},
		tgbotapi.WebhookInfo{},
		"",
	)

	if report.hasWarnings {
		t.Fatal("expected no warnings for polling happy path")
	}

	joined := strings.Join(report.lines, "\n")
	if !strings.Contains(joined, "Telegram getMe: @smoke_bot (id=42)") {
		t.Fatalf("expected bot identity in report, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Pending updates: 0") {
		t.Fatalf("expected pending updates summary, got:\n%s", joined)
	}
}

func TestBuildSmokeReport_FlagsWebhookMismatchAndErrors(t *testing.T) {
	info := tgbotapi.WebhookInfo{
		URL:                "https://old.example.com/telegram-webhook",
		PendingUpdateCount: 3,
		LastErrorDate:      int(time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC).Unix()),
		LastErrorMessage:   "connection refused",
	}

	report := buildSmokeReport(
		tgbotapi.User{ID: 42, FirstName: "SmokeBot"},
		info,
		"https://new.example.com/base",
	)

	if !report.hasWarnings {
		t.Fatal("expected warnings for webhook mismatch")
	}

	joined := strings.Join(report.lines, "\n")
	if !strings.Contains(joined, "WEBHOOK_URL env mismatch") {
		t.Fatalf("expected webhook mismatch warning, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Pending updates: 3 update(s) queued") {
		t.Fatalf("expected pending updates warning, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Webhook delivery error: connection refused") {
		t.Fatalf("expected webhook error warning, got:\n%s", joined)
	}
}

func TestBuildSmokeReport_FlagsMissingPollingConsumer(t *testing.T) {
	report := buildSmokeReport(
		tgbotapi.User{ID: 42, FirstName: "SmokeBot"},
		tgbotapi.WebhookInfo{PendingUpdateCount: 2},
		"",
	)

	if !report.hasWarnings {
		t.Fatal("expected warnings for queued polling updates")
	}

	joined := strings.Join(report.lines, "\n")
	if !strings.Contains(joined, "Polling consumer") {
		t.Fatalf("expected polling consumer warning, got:\n%s", joined)
	}
}
