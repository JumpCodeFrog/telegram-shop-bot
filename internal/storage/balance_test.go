package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
)

// seedBalanceUser inserts a bare users row and returns nothing; the balance
// store keys on telegram_id (the identity the bot/shop layers hold), while
// balance_txs.user_id keeps referencing the internal users.id.
func seedBalanceUser(t *testing.T, db *DB, telegramID int64) {
	t.Helper()
	if _, err := db.Conn().ExecContext(context.Background(),
		`INSERT INTO users (telegram_id, username) VALUES (?, ?)`,
		telegramID, fmt.Sprintf("u%d", telegramID)); err != nil {
		t.Fatal(err)
	}
}

func newBalanceTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "balance.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSQLBalanceStoreGetBalance(t *testing.T) {
	db := newBalanceTestDB(t)
	store := NewSQLBalanceStore(db.Conn())
	ctx := context.Background()

	if _, err := store.GetBalance(ctx, 424242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: err=%v, want ErrNotFound", err)
	}

	seedBalanceUser(t, db, 42)
	balance, err := store.GetBalance(ctx, 42)
	if err != nil || balance != 0 {
		t.Fatalf("fresh user balance=%v err=%v, want 0/nil", balance, err)
	}
}

func TestSQLBalanceStoreAdjustCreditAndDebit(t *testing.T) {
	db := newBalanceTestDB(t)
	store := NewSQLBalanceStore(db.Conn())
	ctx := context.Background()
	seedBalanceUser(t, db, 42)

	// Admin credit of $10.50.
	balance, err := store.AdjustBalance(ctx, 42, 10.50, "grant", 9001)
	if err != nil || balance != 10.50 {
		t.Fatalf("credit: balance=%v err=%v, want 10.50/nil", balance, err)
	}

	// System debit of $4.25 (no admin actor).
	balance, err = store.AdjustBalance(ctx, 42, -4.25, "order_payment:7", 0)
	if err != nil || balance != 6.25 {
		t.Fatalf("debit: balance=%v err=%v, want 6.25/nil", balance, err)
	}

	if got, err := store.GetBalance(ctx, 42); err != nil || got != 6.25 {
		t.Fatalf("GetBalance=%v err=%v, want 6.25/nil", got, err)
	}

	// Tx integrity: both rows carry the internal users.id (FK target), the
	// signed amount, the reason as type and the admin reference (NULL when
	// the adjustment is not operator-driven).
	var internalID int64
	if err := db.Conn().QueryRow(`SELECT id FROM users WHERE telegram_id = 42`).Scan(&internalID); err != nil {
		t.Fatal(err)
	}
	var amount float64
	var userID int64
	var txType, refID string
	if err := db.Conn().QueryRow(`
		SELECT user_id, amount_usd, type, COALESCE(ref_id, '') FROM balance_txs
		WHERE user_id = ? ORDER BY id`, internalID).Scan(&userID, &amount, &txType, &refID); err != nil {
		t.Fatal(err)
	}
	if userID != internalID || amount != 10.50 || txType != "grant" || refID != "9001" {
		t.Fatalf("credit tx row: user=%d amount=%v type=%q ref=%q", userID, amount, txType, refID)
	}
	if err := db.Conn().QueryRow(`
		SELECT user_id, amount_usd, type, COALESCE(ref_id, '') FROM balance_txs
		WHERE user_id = ? AND type = 'order_payment:7'`, internalID).Scan(&userID, &amount, &txType, &refID); err != nil {
		t.Fatal(err)
	}
	if amount != -4.25 || refID != "" {
		t.Fatalf("debit tx row: amount=%v ref=%q, want -4.25/NULL", amount, refID)
	}
}

func TestSQLBalanceStoreInsufficientFundsGuard(t *testing.T) {
	db := newBalanceTestDB(t)
	store := NewSQLBalanceStore(db.Conn())
	ctx := context.Background()
	seedBalanceUser(t, db, 42)

	if _, err := store.AdjustBalance(ctx, 42, 5.00, "grant", 9001); err != nil {
		t.Fatal(err)
	}

	// Overdraft attempt: sentinel error, balance untouched, no tx row.
	if _, err := store.AdjustBalance(ctx, 42, -5.01, "order_payment:8", 0); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("overdraft: err=%v, want ErrInsufficientFunds", err)
	}
	if got, _ := store.GetBalance(ctx, 42); got != 5.00 {
		t.Fatalf("balance after rejected overdraft = %v, want 5.00", got)
	}
	var txCount int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM balance_txs`).Scan(&txCount); err != nil {
		t.Fatal(err)
	}
	if txCount != 1 {
		t.Fatalf("balance_txs rows = %d, want 1 (only the grant)", txCount)
	}

	// An exact-zero landing is allowed: debit the full $5.00.
	balance, err := store.AdjustBalance(ctx, 42, -5.00, "order_payment:9", 0)
	if err != nil || balance != 0 {
		t.Fatalf("exact-zero debit: balance=%v err=%v, want 0/nil", balance, err)
	}
	// And now even one cent more must fail.
	if _, err := store.AdjustBalance(ctx, 42, -0.01, "order_payment:10", 0); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("one cent over zero: err=%v, want ErrInsufficientFunds", err)
	}
}

func TestSQLBalanceStoreUnknownUser(t *testing.T) {
	db := newBalanceTestDB(t)
	store := NewSQLBalanceStore(db.Conn())

	if _, err := store.AdjustBalance(context.Background(), 424242, 5.00, "grant", 9001); !errors.Is(err, ErrNotFound) {
		t.Fatalf("adjust unknown user: err=%v, want ErrNotFound", err)
	}
	var txCount int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM balance_txs`).Scan(&txCount); err != nil {
		t.Fatal(err)
	}
	if txCount != 0 {
		t.Fatalf("balance_txs rows = %d, want 0", txCount)
	}
}

func TestSQLBalanceStoreRejectsInvalidDeltas(t *testing.T) {
	db := newBalanceTestDB(t)
	store := NewSQLBalanceStore(db.Conn())
	ctx := context.Background()
	seedBalanceUser(t, db, 42)

	// Zero is rejected as meaningless (it would only write a noise tx row);
	// a sub-cent delta rounds to zero cents at the boundary and is rejected
	// for the same reason. NaN/Inf can never enter the ledger.
	for _, delta := range []float64{0, 0.001, -0.004, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := store.AdjustBalance(ctx, 42, delta, "grant", 9001); !errors.Is(err, ErrInvalidMoney) {
			t.Fatalf("delta %v: err=%v, want ErrInvalidMoney", delta, err)
		}
	}
	// The audit reason is mandatory: balance_txs is the operator trail.
	if _, err := store.AdjustBalance(ctx, 42, 1.00, "  ", 9001); !errors.Is(err, ErrInvalidMoney) {
		t.Fatalf("empty reason: err=%v, want ErrInvalidMoney", err)
	}
	if got, _ := store.GetBalance(ctx, 42); got != 0 {
		t.Fatalf("balance after rejected deltas = %v, want 0", got)
	}
	var txCount int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM balance_txs`).Scan(&txCount); err != nil {
		t.Fatal(err)
	}
	if txCount != 0 {
		t.Fatalf("balance_txs rows = %d, want 0", txCount)
	}
}

func TestSQLBalanceStoreCentRounding(t *testing.T) {
	db := newBalanceTestDB(t)
	store := NewSQLBalanceStore(db.Conn())
	ctx := context.Background()
	seedBalanceUser(t, db, 42)

	// 0.1 + 0.2 must land on exactly 0.30 — the store snaps every write to
	// integer cents, so binary float drift can never accumulate.
	if _, err := store.AdjustBalance(ctx, 42, 0.1, "a", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdjustBalance(ctx, 42, 0.2, "b", 0); err != nil {
		t.Fatal(err)
	}
	balance, err := store.GetBalance(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%.2f", balance) != "0.30" {
		t.Fatalf("0.1+0.2 balance = %.17f, want 0.30", balance)
	}

	// 1.005 rounds to whole cents at the boundary (binary 1.005 is
	// 1.00499999... → 100 cents), and the stored value stays snapped.
	balance, err = store.AdjustBalance(ctx, 42, 1.005, "c", 0)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%.2f", balance) != "1.30" {
		t.Fatalf("balance after 1.005 credit = %.17f, want 1.30", balance)
	}

	// A debit that lands exactly on the remaining cents succeeds despite
	// the float representation of the stored total.
	if _, err := store.AdjustBalance(ctx, 42, -1.30, "d", 0); err != nil {
		t.Fatalf("exact-remainder debit: %v", err)
	}
	if got, _ := store.GetBalance(ctx, 42); got != 0 {
		t.Fatalf("final balance = %.17f, want 0", got)
	}
}

// TestSQLBalanceStoreBalanceTxExists pins the idempotency probe the admin
// refund flow uses on the balance rail: a deterministic order_refund:<orderID>
// credit must be findable by its exact type string, so a re-run after a
// ledger-recording failure skips the credit instead of minting money twice.
func TestSQLBalanceStoreBalanceTxExists(t *testing.T) {
	db := newBalanceTestDB(t)
	store := NewSQLBalanceStore(db.Conn())
	ctx := context.Background()

	// An unknown user never has audit rows (the telegram_id subquery is empty).
	if exists, err := store.BalanceTxExists(ctx, 424242, "order_refund:7"); err != nil || exists {
		t.Fatalf("unknown user: exists=%v err=%v, want false/nil", exists, err)
	}

	seedBalanceUser(t, db, 42)
	if exists, err := store.BalanceTxExists(ctx, 42, "order_refund:7"); err != nil || exists {
		t.Fatalf("before credit: exists=%v err=%v, want false/nil", exists, err)
	}

	if _, err := store.AdjustBalance(ctx, 42, 12.50, "order_refund:7", 9001); err != nil {
		t.Fatal(err)
	}
	if exists, err := store.BalanceTxExists(ctx, 42, "order_refund:7"); err != nil || !exists {
		t.Fatalf("after credit: exists=%v err=%v, want true/nil", exists, err)
	}
	// Exact type match: a sibling order's refund credit must not shadow.
	if exists, err := store.BalanceTxExists(ctx, 42, "order_refund:8"); err != nil || exists {
		t.Fatalf("sibling order: exists=%v err=%v, want false/nil", exists, err)
	}
}
