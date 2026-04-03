package bot

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"golang.org/x/time/rate"

	"shop_bot/internal/service"
)

// Middleware wraps a handler function, adding cross-cutting behavior.
type Middleware func(handler func(update tgbotapi.Update)) func(update tgbotapi.Update)

// extractUserID returns the user ID from an update, or 0 if not available.
func extractUserID(update tgbotapi.Update) int64 {
	if update.Message != nil && update.Message.From != nil {
		return update.Message.From.ID
	}
	if update.CallbackQuery != nil && update.CallbackQuery.From != nil {
		return update.CallbackQuery.From.ID
	}
	return 0
}

// updateType returns a human-readable type string for the update.
func updateType(update tgbotapi.Update) string {
	switch {
	case update.Message != nil:
		return "message"
	case update.CallbackQuery != nil:
		return "callback_query"
	case update.InlineQuery != nil:
		return "inline_query"
	case update.EditedMessage != nil:
		return "edited_message"
	case update.ChannelPost != nil:
		return "channel_post"
	case update.PreCheckoutQuery != nil:
		return "pre_checkout_query"
	case update.ShippingQuery != nil:
		return "shipping_query"
	default:
		return "unknown"
	}
}

// LoggingMiddleware logs the update type, user_id, and timestamp and tracks
// duration in metrics when the metrics service is provided.
func LoggingMiddleware(logger *slog.Logger, metrics ...*service.MetricsService) Middleware {
	var m *service.MetricsService
	if len(metrics) > 0 {
		m = metrics[0]
	}
	return func(handler func(update tgbotapi.Update)) func(update tgbotapi.Update) {
		return func(update tgbotapi.Update) {
			start := time.Now()
			userID := extractUserID(update)
			uType := updateType(update)

			handler(update)

			duration := time.Since(start)
			logger.Info("incoming update",
				"type", uType,
				"user_id", userID,
				"timestamp", start.Format(time.RFC3339Nano),
				"duration_ms", duration.Milliseconds(),
			)
			if m != nil {
				m.RequestDuration.WithLabelValues(uType).Observe(duration.Seconds())
			}
		}
	}
}

// RecoverMiddleware catches panics in the handler, logs the stack trace, and continues processing.
func RecoverMiddleware(logger *slog.Logger) Middleware {
	return func(handler func(update tgbotapi.Update)) func(update tgbotapi.Update) {
		return func(update tgbotapi.Update) {
			defer func() {
				if r := recover(); r != nil {
					stack := debug.Stack()
					logger.Error("PANIC recovered",
						"error", r,
						"stack", string(stack),
					)
				}
			}()
			handler(update)
		}
	}
}

// AdminOnly checks if the user_id from the update is in the adminIDs list.
// If not, the handler is not called.
func AdminOnly(adminIDs []int64) Middleware {
	allowed := make(map[int64]struct{}, len(adminIDs))
	for _, id := range adminIDs {
		allowed[id] = struct{}{}
	}
	return func(handler func(update tgbotapi.Update)) func(update tgbotapi.Update) {
		return func(update tgbotapi.Update) {
			userID := extractUserID(update)
			if _, ok := allowed[userID]; !ok {
				return
			}
			handler(update)
		}
	}
}

// userLimiter holds a per-user token bucket and the time it was last used.
type userLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimitMiddleware enforces a per-user token bucket rate limit.
// Each user gets a burst of burstSize requests, replenishing at r per second.
// Updates without a user ID (e.g. PreCheckoutQuery) always pass through.
// Stale entries are evicted every hour. ctx controls the cleanup goroutine.
func RateLimitMiddleware(ctx context.Context, r rate.Limit, burstSize int) Middleware {
	var mu sync.Mutex
	limiters := make(map[int64]*userLimiter)

	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-time.Hour)
				mu.Lock()
				for id, ul := range limiters {
					if ul.lastSeen.Before(cutoff) {
						delete(limiters, id)
					}
				}
				mu.Unlock()
			}
		}
	}()

	getLimiter := func(userID int64) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		ul, ok := limiters[userID]
		if !ok {
			ul = &userLimiter{limiter: rate.NewLimiter(r, burstSize)}
			limiters[userID] = ul
		}
		ul.lastSeen = time.Now()
		return ul.limiter
	}

	return func(handler func(update tgbotapi.Update)) func(update tgbotapi.Update) {
		return func(update tgbotapi.Update) {
			userID := extractUserID(update)
			if userID != 0 && !getLimiter(userID).Allow() {
				return
			}
			handler(update)
		}
	}
}

// Chain applies middlewares in order, wrapping the handler from right to left.
// The first middleware in the list is the outermost wrapper.
func Chain(handler func(update tgbotapi.Update), middlewares ...Middleware) func(update tgbotapi.Update) {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}
