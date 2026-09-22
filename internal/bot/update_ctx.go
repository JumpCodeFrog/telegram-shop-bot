package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// updateTimeout bounds all work for one Telegram update (4.14): a single
// per-update budget, replacing 4.7's per-handler windows.
const updateTimeout = 30 * time.Second

// updateCtxKey is the unexported context key type for per-update values.
type updateCtxKey struct{}

// traceIDKey carries the per-update trace correlation id.
var traceIDKey updateCtxKey

// TraceID returns the per-update trace id stored in ctx, or "" if absent.
func TraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// newTraceID returns 8 crypto/rand bytes hex-encoded (16 chars). On rand
// failure it falls back to the zero-padded update id: ingress must never
// crash to produce a correlation id.
func newTraceID(update tgbotapi.Update) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%016x", uint64(update.UpdateID))
	}
	return hex.EncodeToString(b[:])
}

// newUpdateCtx derives the per-update context: root (nil → b.rootCtx →
// context.Background()) + trace id + the 30s per-update budget. Deriving
// from the process-lifetime root keeps 4.7's property: shutdown cancellation
// reaches in-flight handler DB work.
func (b *Bot) newUpdateCtx(root context.Context, update tgbotapi.Update) (context.Context, context.CancelFunc) {
	if root == nil {
		root = b.rootCtx
	}
	if root == nil {
		root = context.Background()
	}
	ctx := context.WithValue(root, traceIDKey, newTraceID(update))
	return context.WithTimeout(ctx, updateTimeout)
}

// loggerFor returns the bot logger with the update's trace_id bound, so all
// lines emitted while handling one update correlate. Passthrough when the
// ctx carries no trace id.
func (b *Bot) loggerFor(ctx context.Context) *slog.Logger {
	if id := TraceID(ctx); id != "" {
		return b.logger.With("trace_id", id)
	}
	return b.logger
}
