package bot

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"pgregory.net/rapid"
)

// --- helpers ---

func newMessageUpdate(userID int64) tgbotapi.Update {
	return tgbotapi.Update{
		Message: &tgbotapi.Message{
			From: &tgbotapi.User{ID: userID},
		},
	}
}

func newCallbackUpdate(userID int64) tgbotapi.Update {
	return tgbotapi.Update{
		CallbackQuery: &tgbotapi.CallbackQuery{
			From: &tgbotapi.User{ID: userID},
		},
	}
}

// --- extractUserID ---

func TestExtractUserID_Message(t *testing.T) {
	u := newMessageUpdate(123)
	if got := extractUserID(u); got != 123 {
		t.Fatalf("expected 123, got %d", got)
	}
}

func TestExtractUserID_Callback(t *testing.T) {
	u := newCallbackUpdate(456)
	if got := extractUserID(u); got != 456 {
		t.Fatalf("expected 456, got %d", got)
	}
}

func TestExtractUserID_Empty(t *testing.T) {
	u := tgbotapi.Update{}
	if got := extractUserID(u); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

// --- updateType ---

func TestUpdateType(t *testing.T) {
	tests := []struct {
		name   string
		update tgbotapi.Update
		want   string
	}{
		{"message", tgbotapi.Update{Message: &tgbotapi.Message{}}, "message"},
		{"callback", tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{}}, "callback_query"},
		{"inline", tgbotapi.Update{InlineQuery: &tgbotapi.InlineQuery{}}, "inline_query"},
		{"edited", tgbotapi.Update{EditedMessage: &tgbotapi.Message{}}, "edited_message"},
		{"channel", tgbotapi.Update{ChannelPost: &tgbotapi.Message{}}, "channel_post"},
		{"precheckout", tgbotapi.Update{PreCheckoutQuery: &tgbotapi.PreCheckoutQuery{}}, "pre_checkout_query"},
		{"shipping", tgbotapi.Update{ShippingQuery: &tgbotapi.ShippingQuery{}}, "shipping_query"},
		{"unknown", tgbotapi.Update{}, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateType(tt.update); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

// --- LoggingMiddleware ---

func TestLoggingMiddleware_LogsFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	called := false
	handler := func(u tgbotapi.Update) { called = true }

	wrapped := LoggingMiddleware(logger)(handler)
	wrapped(newMessageUpdate(42))

	if !called {
		t.Fatal("handler was not called")
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "type=message") {
		t.Fatalf("log missing type: %s", logOutput)
	}
	if !strings.Contains(logOutput, "user_id=42") {
		t.Fatalf("log missing user_id: %s", logOutput)
	}
	if !strings.Contains(logOutput, "timestamp=") {
		t.Fatalf("log missing timestamp: %s", logOutput)
	}
}

func TestLoggingMiddleware_CallbackQuery(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	handler := func(u tgbotapi.Update) {}
	wrapped := LoggingMiddleware(logger)(handler)
	wrapped(newCallbackUpdate(99))

	logOutput := buf.String()
	if !strings.Contains(logOutput, "type=callback_query") {
		t.Fatalf("log missing callback type: %s", logOutput)
	}
	if !strings.Contains(logOutput, "user_id=99") {
		t.Fatalf("log missing user_id: %s", logOutput)
	}
}

// --- RecoverMiddleware ---

func TestRecoverMiddleware_NoPanic(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	called := false
	handler := func(u tgbotapi.Update) { called = true }

	wrapped := RecoverMiddleware(logger)(handler)
	wrapped(tgbotapi.Update{})

	if !called {
		t.Fatal("handler was not called")
	}
	if buf.Len() != 0 {
		t.Fatalf("unexpected log output: %s", buf.String())
	}
}

func TestRecoverMiddleware_CatchesPanic(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	handler := func(u tgbotapi.Update) { panic("test panic") }

	wrapped := RecoverMiddleware(logger)(handler)

	// Should not panic
	wrapped(tgbotapi.Update{})

	logOutput := buf.String()
	if !strings.Contains(logOutput, "PANIC recovered") {
		t.Fatalf("log missing panic recovery: %s", logOutput)
	}
	if !strings.Contains(logOutput, "test panic") {
		t.Fatalf("log missing panic value: %s", logOutput)
	}
}

func TestRecoverMiddleware_ContinuesAfterPanic(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	callCount := 0
	handler := func(u tgbotapi.Update) {
		callCount++
		if callCount == 1 {
			panic("first call panic")
		}
	}

	wrapped := RecoverMiddleware(logger)(handler)

	// First call panics — should be recovered
	wrapped(tgbotapi.Update{})
	// Second call should work fine
	wrapped(tgbotapi.Update{})

	if callCount != 2 {
		t.Fatalf("expected 2 calls, got %d", callCount)
	}
}

// --- AdminOnly ---

func TestAdminOnly_AllowsAdmin(t *testing.T) {
	called := false
	handler := func(u tgbotapi.Update) { called = true }

	wrapped := AdminOnly([]int64{100, 200})(handler)
	wrapped(newMessageUpdate(100))

	if !called {
		t.Fatal("handler was not called for admin")
	}
}

func TestAdminOnly_BlocksNonAdmin(t *testing.T) {
	called := false
	handler := func(u tgbotapi.Update) { called = true }

	wrapped := AdminOnly([]int64{100, 200})(handler)
	wrapped(newMessageUpdate(999))

	if called {
		t.Fatal("handler was called for non-admin")
	}
}

func TestAdminOnly_BlocksZeroUserID(t *testing.T) {
	called := false
	handler := func(u tgbotapi.Update) { called = true }

	wrapped := AdminOnly([]int64{100})(handler)
	wrapped(tgbotapi.Update{}) // no user info → userID=0

	if called {
		t.Fatal("handler was called for zero user_id")
	}
}

func TestAdminOnly_EmptyAdminList(t *testing.T) {
	called := false
	handler := func(u tgbotapi.Update) { called = true }

	wrapped := AdminOnly([]int64{})(handler)
	wrapped(newMessageUpdate(42))

	if called {
		t.Fatal("handler was called with empty admin list")
	}
}

func TestAdminOnly_CallbackQuery(t *testing.T) {
	called := false
	handler := func(u tgbotapi.Update) { called = true }

	wrapped := AdminOnly([]int64{77})(handler)
	wrapped(newCallbackUpdate(77))

	if !called {
		t.Fatal("handler was not called for admin via callback")
	}
}

// --- Chain ---

func TestChain_AppliesInOrder(t *testing.T) {
	var order []string

	mw1 := func(h func(tgbotapi.Update)) func(tgbotapi.Update) {
		return func(u tgbotapi.Update) {
			order = append(order, "mw1-before")
			h(u)
			order = append(order, "mw1-after")
		}
	}
	mw2 := func(h func(tgbotapi.Update)) func(tgbotapi.Update) {
		return func(u tgbotapi.Update) {
			order = append(order, "mw2-before")
			h(u)
			order = append(order, "mw2-after")
		}
	}

	handler := func(u tgbotapi.Update) {
		order = append(order, "handler")
	}

	chained := Chain(handler, mw1, mw2)
	chained(tgbotapi.Update{})

	expected := []string{"mw1-before", "mw2-before", "handler", "mw2-after", "mw1-after"}
	if len(order) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, order)
	}
	for i := range expected {
		if order[i] != expected[i] {
			t.Fatalf("at index %d: expected %q, got %q", i, expected[i], order[i])
		}
	}
}

// --- Property-based tests (rapid) ---

// Feature: shop_bot, Property 16: Доступ к админ-командам только для администраторов
// Validates: Requirements 9.6
func TestProperty_AdminOnlyAccess(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a random admin ID list (1-10 unique positive IDs).
		adminIDs := rapid.SliceOfNDistinct(rapid.Int64Range(1, 1_000_000), 1, 10, func(id int64) int64 { return id }).Draw(t, "adminIDs")

		called := false
		handler := func(u tgbotapi.Update) { called = true }
		wrapped := AdminOnly(adminIDs)(handler)

		// Pick a random admin from the list — handler MUST be called.
		adminIdx := rapid.IntRange(0, len(adminIDs)-1).Draw(t, "adminIdx")
		adminID := adminIDs[adminIdx]

		called = false
		wrapped(newMessageUpdate(adminID))
		if !called {
			t.Fatalf("handler was NOT called for admin user_id=%d, adminIDs=%v", adminID, adminIDs)
		}

		// Also test via callback update.
		called = false
		wrapped(newCallbackUpdate(adminID))
		if !called {
			t.Fatalf("handler was NOT called for admin (callback) user_id=%d", adminID)
		}

		// Generate a user_id that is NOT in the admin list — handler MUST NOT be called.
		nonAdminID := rapid.Int64Range(1, 1_000_000).Draw(t, "nonAdminID")
		isAdmin := false
		for _, id := range adminIDs {
			if id == nonAdminID {
				isAdmin = true
				break
			}
		}
		if !isAdmin {
			called = false
			wrapped(newMessageUpdate(nonAdminID))
			if called {
				t.Fatalf("handler was called for non-admin user_id=%d, adminIDs=%v", nonAdminID, adminIDs)
			}
		}
	})
}

// Feature: shop_bot, Property 17: Логирование содержит обязательные поля
// Validates: Requirements 11.1
func TestProperty_LoggingContainsRequiredFields(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		userID := rapid.Int64Range(1, 1_000_000).Draw(t, "userID")
		useCallback := rapid.Bool().Draw(t, "useCallback")

		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))

		handler := func(u tgbotapi.Update) {}
		wrapped := LoggingMiddleware(logger)(handler)

		var update tgbotapi.Update
		var expectedType string
		if useCallback {
			update = newCallbackUpdate(userID)
			expectedType = "callback_query"
		} else {
			update = newMessageUpdate(userID)
			expectedType = "message"
		}

		wrapped(update)

		logOutput := buf.String()

		// Must contain update type.
		if !strings.Contains(logOutput, fmt.Sprintf("type=%s", expectedType)) {
			t.Fatalf("log missing type=%s: %s", expectedType, logOutput)
		}

		// Must contain user_id.
		if !strings.Contains(logOutput, fmt.Sprintf("user_id=%d", userID)) {
			t.Fatalf("log missing user_id=%d: %s", userID, logOutput)
		}

		// Must contain timestamp.
		if !strings.Contains(logOutput, "time=") {
			t.Fatalf("log missing time: %s", logOutput)
		}
	})
}

// Feature: shop_bot, Property 18: Recover middleware перехватывает паники
// Validates: Requirements 11.2
func TestProperty_RecoverMiddlewareCatchesPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		panicMsg := rapid.StringMatching(`[a-zA-Z0-9 ]{1,50}`).Draw(t, "panicMsg")

		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))

		panicHandler := func(u tgbotapi.Update) {
			panic(panicMsg)
		}
		wrapped := RecoverMiddleware(logger)(panicHandler)

		// First call — should NOT crash, panic must be caught.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("RecoverMiddleware did not catch panic: %v", r)
				}
			}()
			wrapped(tgbotapi.Update{})
		}()

		logOutput := buf.String()
		if !strings.Contains(logOutput, "PANIC recovered") {
			t.Fatalf("log missing PANIC recovered: %s", logOutput)
		}
		if !strings.Contains(logOutput, panicMsg) {
			t.Fatalf("log missing panic message %q: %s", panicMsg, logOutput)
		}

		// Second call — middleware must still work after catching a panic.
		buf.Reset()
		secondCalled := false
		normalHandler := func(u tgbotapi.Update) { secondCalled = true }
		wrappedNormal := RecoverMiddleware(logger)(normalHandler)
		wrappedNormal(tgbotapi.Update{})
		if !secondCalled {
			t.Fatal("handler was not called on subsequent invocation after panic recovery")
		}
	})
}
