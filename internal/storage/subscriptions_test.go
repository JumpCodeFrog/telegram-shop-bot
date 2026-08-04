package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// seedSub inserts a subscription through the store and returns it as listed.
// expires is relative to now; status "" means active.
func upsertSub(t *testing.T, store *SQLSubscriptionStore, userID, productID int64, chargeID, status string, expiresIn time.Duration) {
	t.Helper()
	err := store.Upsert(context.Background(), Subscription{
		UserID:    userID,
		ProductID: productID,
		ChargeID:  chargeID,
		Status:    status,
		ExpiresAt: time.Now().UTC().Add(expiresIn),
	})
	if err != nil {
		t.Fatalf("Upsert(user %d, product %d): %v", userID, productID, err)
	}
}

func TestSubUpsert_InsertThenRenew(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLSubscriptionStore(db)
	ctx := context.Background()
	userID := seedUser(t, db, 100)
	productID := seedProduct(t, db, "Sub")

	first := time.Now().UTC().Add(24 * time.Hour)
	if err := store.Upsert(ctx, Subscription{UserID: userID, ProductID: productID, ChargeID: "ch-1", ExpiresAt: first}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	subs, err := store.ListActiveByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListActiveByUser: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("got %d subscriptions, want 1", len(subs))
	}
	if subs[0].Status != SubStatusActive {
		t.Errorf("status = %q, want %q (empty status defaults to active)", subs[0].Status, SubStatusActive)
	}
	if got := subs[0].ExpiresAt.Unix(); got != first.Unix() {
		t.Errorf("ExpiresAt = %v, want %v", subs[0].ExpiresAt, first)
	}
	id := subs[0].ID

	// Simulate a sent reminder, then renew: expires_at must move forward and
	// reminded_at must reset so the next period gets its own reminder.
	if err := store.MarkReminded(ctx, id); err != nil {
		t.Fatalf("MarkReminded: %v", err)
	}
	renewed := time.Now().UTC().Add(48 * time.Hour)
	if err := store.Upsert(ctx, Subscription{UserID: userID, ProductID: productID, ChargeID: "ch-2", ExpiresAt: renewed}); err != nil {
		t.Fatalf("renewal Upsert: %v", err)
	}

	subs, err = store.ListActiveByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListActiveByUser after renewal: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("got %d subscriptions after renewal, want 1 (upsert must not duplicate)", len(subs))
	}
	if subs[0].ID != id {
		t.Errorf("renewal changed ID %d -> %d, want same row", id, subs[0].ID)
	}
	if got := subs[0].ExpiresAt.Unix(); got != renewed.Unix() {
		t.Errorf("ExpiresAt after renewal = %v, want %v", subs[0].ExpiresAt, renewed)
	}
	if subs[0].ChargeID != "ch-2" {
		t.Errorf("ChargeID = %q, want %q", subs[0].ChargeID, "ch-2")
	}
	if subs[0].RemindedAt.Valid {
		t.Errorf("RemindedAt still set after renewal, want cleared")
	}
}

func TestSubListActiveByUser_FiltersAndOrders(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLSubscriptionStore(db)
	ctx := context.Background()
	userID := seedUser(t, db, 100)
	otherUser := seedUser(t, db, 200)
	pLater := seedProduct(t, db, "Later")
	pSooner := seedProduct(t, db, "Sooner")
	pCanceled := seedProduct(t, db, "Canceled")
	pExpired := seedProduct(t, db, "Expired")

	upsertSub(t, store, userID, pLater, "ch-later", "", 48*time.Hour)
	upsertSub(t, store, userID, pSooner, "ch-sooner", "", 24*time.Hour)
	upsertSub(t, store, userID, pCanceled, "ch-canceled", SubStatusCanceled, 24*time.Hour)
	upsertSub(t, store, userID, pExpired, "ch-expired", SubStatusExpired, 24*time.Hour)
	upsertSub(t, store, otherUser, pSooner, "ch-other", "", 24*time.Hour)

	subs, err := store.ListActiveByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListActiveByUser: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("got %d subscriptions, want 2 (canceled/expired/foreign excluded): %+v", len(subs), subs)
	}
	if subs[0].ProductID != pSooner || subs[1].ProductID != pLater {
		t.Errorf("order = [%d, %d], want soonest-first [%d, %d]", subs[0].ProductID, subs[1].ProductID, pSooner, pLater)
	}
}

func TestSubSetStatusByCharge(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLSubscriptionStore(db)
	ctx := context.Background()
	userID := seedUser(t, db, 100)
	productID := seedProduct(t, db, "Sub")

	upsertSub(t, store, userID, productID, "ch-1", "", 24*time.Hour)

	if err := store.SetStatusByCharge(ctx, "ch-1", SubStatusCanceled); err != nil {
		t.Fatalf("SetStatusByCharge: %v", err)
	}
	subs, err := store.ListActiveByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListActiveByUser: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("got %d active subscriptions after cancel, want 0", len(subs))
	}

	if err := store.SetStatusByCharge(ctx, "no-such-charge", SubStatusCanceled); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetStatusByCharge(unknown) = %v, want ErrNotFound", err)
	}
}

func TestSubDueForReminder(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLSubscriptionStore(db)
	ctx := context.Background()
	userID := seedUser(t, db, 100)
	pDue := seedProduct(t, db, "Due")
	pFar := seedProduct(t, db, "Far")
	pPast := seedProduct(t, db, "Past")
	pCanceled := seedProduct(t, db, "Canceled")

	upsertSub(t, store, userID, pDue, "ch-due", "", 10*time.Hour) // inside window
	upsertSub(t, store, userID, pFar, "ch-far", "", 72*time.Hour) // outside window
	upsertSub(t, store, userID, pPast, "ch-past", "", -time.Hour) // already expired
	upsertSub(t, store, userID, pCanceled, "ch-can", SubStatusCanceled, 10*time.Hour)

	due, err := store.DueForReminder(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("DueForReminder: %v", err)
	}
	if len(due) != 1 || due[0].ProductID != pDue {
		t.Fatalf("DueForReminder = %+v, want exactly the sub expiring in 10h", due)
	}

	// After MarkReminded the same subscription must never come back.
	if err := store.MarkReminded(ctx, due[0].ID); err != nil {
		t.Fatalf("MarkReminded: %v", err)
	}
	due, err = store.DueForReminder(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("DueForReminder after MarkReminded: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("DueForReminder after MarkReminded = %+v, want empty", due)
	}
}

func TestSubExpireOverdue(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLSubscriptionStore(db)
	ctx := context.Background()
	userID := seedUser(t, db, 100)
	pOverdue := seedProduct(t, db, "Overdue")
	pAlive := seedProduct(t, db, "Alive")
	pCanceled := seedProduct(t, db, "Canceled")

	upsertSub(t, store, userID, pOverdue, "ch-over", "", -time.Hour)
	upsertSub(t, store, userID, pAlive, "ch-alive", "", 24*time.Hour)
	upsertSub(t, store, userID, pCanceled, "ch-can", SubStatusCanceled, -time.Hour) // not active: untouched

	n, err := store.ExpireOverdue(ctx)
	if err != nil {
		t.Fatalf("ExpireOverdue: %v", err)
	}
	if n != 1 {
		t.Errorf("ExpireOverdue = %d, want 1", n)
	}

	subs, err := store.ListActiveByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListActiveByUser: %v", err)
	}
	if len(subs) != 1 || subs[0].ProductID != pAlive {
		t.Errorf("active after expire = %+v, want only the future sub", subs)
	}

	// Idempotent: a second sweep finds nothing.
	n, err = store.ExpireOverdue(ctx)
	if err != nil {
		t.Fatalf("second ExpireOverdue: %v", err)
	}
	if n != 0 {
		t.Errorf("second ExpireOverdue = %d, want 0", n)
	}
}
