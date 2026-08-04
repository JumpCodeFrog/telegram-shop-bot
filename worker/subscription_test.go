package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"shop_bot/internal/storage"
)

// fakeSubKeeper is an in-memory SubscriptionKeeper: DueForReminder returns
// the not-yet-reminded subscriptions, MarkReminded flags them.
type fakeSubKeeper struct {
	subs        []storage.Subscription
	reminded    map[int64]bool
	expireCalls int
	expireErr   error
}

func newFakeSubKeeper(subs ...storage.Subscription) *fakeSubKeeper {
	return &fakeSubKeeper{subs: subs, reminded: make(map[int64]bool)}
}

func (f *fakeSubKeeper) ExpireOverdue(_ context.Context) (int64, error) {
	f.expireCalls++
	return 0, f.expireErr
}

func (f *fakeSubKeeper) DueForReminder(_ context.Context, _ time.Duration) ([]storage.Subscription, error) {
	var due []storage.Subscription
	for _, s := range f.subs {
		if !f.reminded[s.ID] {
			due = append(due, s)
		}
	}
	return due, nil
}

func (f *fakeSubKeeper) MarkReminded(_ context.Context, id int64) error {
	f.reminded[id] = true
	return nil
}

func testSub(id int64) storage.Subscription {
	return storage.Subscription{
		ID:        id,
		UserID:    100 + id,
		ProductID: 200 + id,
		ChargeID:  "charge",
		Status:    storage.SubStatusActive,
		ExpiresAt: time.Now().Add(48 * time.Hour),
	}
}

// TestSubscriptionWorkerRemindsOnce is the core one-shot contract: a due
// subscription is notified on the first tick, marked reminded, and never
// notified again on subsequent ticks.
func TestSubscriptionWorkerRemindsOnce(t *testing.T) {
	keeper := newFakeSubKeeper(testSub(1))

	notified := 0
	w := NewSubscriptionWorker(keeper, func(_ context.Context, sub storage.Subscription) error {
		notified++
		if sub.ID != 1 {
			t.Errorf("notified subscription %d, want 1", sub.ID)
		}
		return nil
	}, time.Hour)

	w.tick(context.Background())
	w.tick(context.Background())

	if notified != 1 {
		t.Fatalf("notify called %d times, want exactly 1", notified)
	}
	if !keeper.reminded[1] {
		t.Fatal("subscription 1 was not marked reminded")
	}
	if keeper.expireCalls != 2 {
		t.Fatalf("ExpireOverdue called %d times, want 2 (once per tick)", keeper.expireCalls)
	}
}

// TestSubscriptionWorkerRetriesFailedReminder: a failed notify must NOT mark
// the subscription reminded — the next tick retries it.
func TestSubscriptionWorkerRetriesFailedReminder(t *testing.T) {
	keeper := newFakeSubKeeper(testSub(7))

	calls := 0
	w := NewSubscriptionWorker(keeper, func(_ context.Context, _ storage.Subscription) error {
		calls++
		if calls == 1 {
			return errors.New("telegram down")
		}
		return nil
	}, time.Hour)

	w.tick(context.Background())
	if keeper.reminded[7] {
		t.Fatal("subscription marked reminded despite failed notify")
	}

	w.tick(context.Background())
	if calls != 2 {
		t.Fatalf("notify called %d times, want 2 (one failure + one retry)", calls)
	}
	if !keeper.reminded[7] {
		t.Fatal("subscription not marked reminded after successful retry")
	}
}

// TestSubscriptionWorkerStopsOnCancel: Start must return promptly when its
// context is cancelled.
func TestSubscriptionWorkerStopsOnCancel(t *testing.T) {
	w := NewSubscriptionWorker(newFakeSubKeeper(), nil, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Start(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
}
