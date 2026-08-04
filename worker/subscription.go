package worker

import (
	"context"
	"log/slog"
	"time"

	"shop_bot/internal/storage"
)

// SubscriptionKeeper is the narrow store surface the subscription worker
// needs (implemented by storage.SQLSubscriptionStore).
type SubscriptionKeeper interface {
	ExpireOverdue(ctx context.Context) (int64, error)
	DueForReminder(ctx context.Context, within time.Duration) ([]storage.Subscription, error)
	MarkReminded(ctx context.Context, id int64) error
}

// SubscriptionWorker hourly marks overdue Stars subscriptions expired and
// sends a one-shot "expiring soon" reminder ahead of expiry.
type SubscriptionWorker struct {
	subs SubscriptionKeeper
	// notify sends the reminder to the user; a non-nil error means the
	// subscription is NOT marked reminded and will be retried next tick.
	notify       func(ctx context.Context, sub storage.Subscription) error
	interval     time.Duration
	remindWithin time.Duration
}

// NewSubscriptionWorker creates the worker. notify may be nil (reminders are
// then only marked, useful in smoke setups without a bot). The reminder window
// is 72 hours, per spec §3.5.
func NewSubscriptionWorker(subs SubscriptionKeeper, notify func(ctx context.Context, sub storage.Subscription) error, interval time.Duration) *SubscriptionWorker {
	return &SubscriptionWorker{
		subs:         subs,
		notify:       notify,
		interval:     interval,
		remindWithin: 72 * time.Hour,
	}
}

// Start runs the periodic loop until ctx is cancelled.
func (w *SubscriptionWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("Subscription Worker started", "interval", w.interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Subscription Worker stopped")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// tick performs one maintenance pass: expire overdue subscriptions, then send
// due reminders. MarkReminded runs only after a successful notify, so each
// subscription is reminded exactly once per period (Upsert-renewal clears the
// flag for the next period).
func (w *SubscriptionWorker) tick(ctx context.Context) {
	expired, err := w.subs.ExpireOverdue(ctx)
	if err != nil {
		slog.Error("subscription worker: expire overdue", "error", err)
	} else if expired > 0 {
		slog.Info("subscription worker: subscriptions expired", "count", expired)
	}

	due, err := w.subs.DueForReminder(ctx, w.remindWithin)
	if err != nil {
		slog.Error("subscription worker: due for reminder", "error", err)
		return
	}

	for _, sub := range due {
		if ctx.Err() != nil {
			return
		}
		if w.notify != nil {
			if err := w.notify(ctx, sub); err != nil {
				// Not marked reminded — retried on the next tick.
				slog.Error("subscription worker: send reminder",
					"subscription_id", sub.ID, "user_id", sub.UserID, "error", err)
				continue
			}
		}
		if err := w.subs.MarkReminded(ctx, sub.ID); err != nil {
			slog.Error("subscription worker: mark reminded", "subscription_id", sub.ID, "error", err)
		}
	}
}
