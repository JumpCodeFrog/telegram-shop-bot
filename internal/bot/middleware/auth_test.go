package middleware

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"shop_bot/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// failingUserStore fails every Upsert; the auth middleware must not block the
// update on it, but must warn with the telegram user id.
type failingUserStore struct{}

func (failingUserStore) Upsert(ctx context.Context, user *storage.User) error {
	return errors.New("upsert boom")
}

// TestAuthWarnsOnUpsertFailure pins §6.20 R20b: the auth middleware's user
// upsert failure is logged (Warn) with the message text and the user_id,
// instead of being silently swallowed, and the chain still proceeds.
func TestAuthWarnsOnUpsertFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	nextCalled := false
	next := func(ctx context.Context, update tgbotapi.Update) {
		nextCalled = true
	}

	mw := Auth(failingUserStore{}, logger)
	handler := mw(next)
	handler(context.Background(), tgbotapi.Update{
		Message: &tgbotapi.Message{From: &tgbotapi.User{ID: 4242}},
	})

	if !nextCalled {
		t.Fatal("auth middleware blocked the chain on upsert failure")
	}
	line := buf.String()
	if !strings.Contains(line, "auth: user upsert failed") {
		t.Fatalf("warn line missing text %q, got %q", "auth: user upsert failed", line)
	}
	if !strings.Contains(line, "user_id=4242") {
		t.Fatalf("warn line missing user_id=4242, got %q", line)
	}
}
