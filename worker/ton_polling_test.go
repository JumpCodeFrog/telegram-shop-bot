package worker

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"shop_bot/internal/payment"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

// stubTxFetcher returns a fixed transaction list, mirroring
// stubInvoiceFetcher in polling_test.go.
type stubTxFetcher struct {
	txs    []payment.TONTransaction
	err    error
	limits []int
}

func (s *stubTxFetcher) GetTransactions(_ context.Context, limit int) ([]payment.TONTransaction, error) {
	s.limits = append(s.limits, limit)
	return s.txs, s.err
}

// failingConfirmer fails every confirmation with the same error and counts
// calls, pinning that one bad transfer never aborts the batch.
type failingConfirmer struct {
	calls int
	err   error
}

func (f *failingConfirmer) ConfirmPaymentReceipt(_ context.Context, _ shop.PaymentReceipt) (*shop.PaymentOutcome, error) {
	f.calls++
	return nil, f.err
}

func (f *failingConfirmer) RecordUnexpectedPayment(_ context.Context, _ shop.PaymentReceipt, _ string) error {
	return storage.ErrPaymentNeedsReview
}

func (f *failingConfirmer) RecordPaymentAnomaly(_ context.Context, _ storage.PaymentAnomaly) error {
	return nil
}

// newTONSQLHarness builds a real SQLite-backed OrderService, the same
// fixture the shop receipt tests use, so settlement, quarantine and replay
// behaviour are exercised against the real ledger.
func newTONSQLHarness(t *testing.T) (*shop.OrderService, *storage.SQLOrderStore, *storage.DB) {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "ton-polling.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := storage.NewSQLOrderStore(db)
	svc := shop.NewOrderService(store, storage.NewCartStore(db.Conn()), storage.NewSQLProductStore(db), shop.PaymentDeps{}, slog.Default())
	return svc, store, db
}

func tonTransfer(lt, hash, comment string, nano int64) payment.TONTransaction {
	return payment.TONTransaction{
		LT: lt, Hash: hash, Source: "EQsender", ValueNano: nano,
		Comment: comment, Utime: 1720000000,
	}
}

// TestTONPollingSettlesMatchingTransfer pins the happy path: an inbound
// transfer whose comment is exactly "order-<id>" with value >= the order's
// nanoton snapshot settles the order, writes a provider-ton attempt row and
// triggers exactly one outcome notification. The poll window (latest 50
// transactions) is pinned via the recorded fetch limit.
func TestTONPollingSettlesMatchingTransfer(t *testing.T) {
	svc, store, db := newTONSQLHarness(t)
	ctx := context.Background()
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalTonNano: 1500000000, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &stubTxFetcher{txs: []payment.TONTransaction{
		tonTransfer("1720000000001", "abc123", "order-"+itoa(orderID), 1500000000),
	}}
	var notified []int64
	w := NewTONPollingWorker(fetcher, svc, func(_ context.Context, o *shop.PaymentOutcome) {
		notified = append(notified, o.Order.ID)
	}, time.Minute)

	w.poll(ctx)

	if len(notified) != 1 || notified[0] != orderID {
		t.Fatalf("notified=%v, want [%d]", notified, orderID)
	}
	if len(fetcher.limits) != 1 || fetcher.limits[0] != 50 {
		t.Fatalf("fetch limits=%v, want [50]", fetcher.limits)
	}
	order, err := store.GetOrder(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != storage.OrderStatusPaid || order.PaymentMethod != storage.PaymentMethodTON ||
		order.PaymentID != "1720000000001:abc123" {
		t.Fatalf("order=%+v", order)
	}
	var amountMinor, scale int64
	var provider, currency, status string
	if err := db.Conn().QueryRow(`
		SELECT provider, currency, amount_minor, scale, status FROM payment_attempts
		WHERE order_id=?`, orderID).Scan(&provider, &currency, &amountMinor, &scale, &status); err != nil {
		t.Fatalf("attempt row: %v", err)
	}
	if provider != "ton" || currency != "TON" || amountMinor != 1500000000 || scale != 9 || status != "succeeded" {
		t.Fatalf("attempt provider=%s currency=%s amount=%d scale=%d status=%s",
			provider, currency, amountMinor, scale, status)
	}
}

// TestTONPollingSkipsTransfersWithoutOrderReceipt verifies that foreign
// comments, malformed order comments and receipt-less transfers (zero
// value, incomplete lt:hash identity) never reach the confirmer. Binary
// dataRaw filtering happens inside the adapter and is covered there.
func TestTONPollingSkipsTransfersWithoutOrderReceipt(t *testing.T) {
	fetcher := &stubTxFetcher{txs: []payment.TONTransaction{
		tonTransfer("1", "h1", "thanks for the coffee", 1000000000), // foreign comment
		tonTransfer("2", "h2", "order-abc", 1000000000),             // malformed comment
		tonTransfer("3", "h3", "order-0", 1000000000),               // non-positive order id
		tonTransfer("4", "h4", "order-9", 0),                        // zero value
		tonTransfer("", "h5", "order-9", 1000000000),                // missing lt
		tonTransfer("6", "", "order-9", 1000000000),                 // missing hash
	}}
	conf := &recordingConfirmer{}
	notifications := 0
	w := NewTONPollingWorker(fetcher, conf, func(context.Context, *shop.PaymentOutcome) {
		notifications++
	}, time.Minute)

	w.poll(context.Background())

	if len(conf.confirmed) != 0 || len(conf.receipts) != 0 || len(conf.anomalies) != 0 {
		t.Fatalf("confirmer touched: confirmed=%v receipts=%v anomalies=%v",
			conf.confirmed, conf.receipts, conf.anomalies)
	}
	if notifications != 0 {
		t.Fatalf("notifications=%d, want 0", notifications)
	}
}

// TestTONPollingReplayAcrossPollsIsIdempotent mirrors crypto's
// conflict-does-not-notify test against the real ledger: the same transfer
// seen by two consecutive polls settles once; the replay is a durable
// ErrOrderStatusConflict no-op with no second notification and no new rows.
func TestTONPollingReplayAcrossPollsIsIdempotent(t *testing.T) {
	svc, store, db := newTONSQLHarness(t)
	ctx := context.Background()
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalTonNano: 1500000000, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx := tonTransfer("1720000000002", "def456", "order-"+itoa(orderID), 1500000000)
	fetcher := &stubTxFetcher{txs: []payment.TONTransaction{tx}}
	notifications := 0
	w := NewTONPollingWorker(fetcher, svc, func(context.Context, *shop.PaymentOutcome) {
		notifications++
	}, time.Minute)

	w.poll(ctx)
	w.poll(ctx)

	if notifications != 1 {
		t.Fatalf("notifications=%d, want exactly 1 across both polls", notifications)
	}
	var attempts, anomalies int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, orderID).Scan(&anomalies)
	order, _ := store.GetOrder(ctx, orderID)
	if attempts != 1 || anomalies != 0 || order.Status != storage.OrderStatusPaid {
		t.Fatalf("attempts=%d anomalies=%d order=%+v", attempts, anomalies, order)
	}
}

// TestTONPollingUnderpaidIsQuarantined: a transfer below the order's nanoton
// snapshot must NOT settle the order. The ledger quarantines the receipt
// (anomaly row with the actual underpaid facts) and the worker treats the
// quarantine-class error as durably handled: no notification, no retry
// storm, order left pending in needs-review.
func TestTONPollingUnderpaidIsQuarantined(t *testing.T) {
	svc, store, db := newTONSQLHarness(t)
	ctx := context.Background()
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalTonNano: 1500000000, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &stubTxFetcher{txs: []payment.TONTransaction{
		tonTransfer("1720000000003", "low789", "order-"+itoa(orderID), 1499999999),
	}}
	notifications := 0
	w := NewTONPollingWorker(fetcher, svc, func(context.Context, *shop.PaymentOutcome) {
		notifications++
	}, time.Minute)

	w.poll(ctx)

	if notifications != 0 {
		t.Fatalf("notifications=%d, want 0", notifications)
	}
	order, _ := store.GetOrder(ctx, orderID)
	if order.Status != storage.OrderStatusPending || order.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("order=%+v, want pending/needs_review", order)
	}
	var amountMinor int64
	var provider string
	if err := db.Conn().QueryRow(`
		SELECT provider, amount_minor FROM payment_anomalies
		WHERE proposed_order_id=? AND external_id='1720000000003:low789'`, orderID).Scan(&provider, &amountMinor); err != nil {
		t.Fatalf("anomaly row: %v", err)
	}
	if provider != "ton" || amountMinor != 1499999999 {
		t.Fatalf("anomaly provider=%s amount=%d", provider, amountMinor)
	}
	var attempts int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("attempts=%d, want 0", attempts)
	}
}

// TestTONPollingFetchErrorConfirmsNothing mirrors crypto's
// fetch-error test: a provider error aborts the poll round; the next tick
// retries.
func TestTONPollingFetchErrorConfirmsNothing(t *testing.T) {
	fetcher := &stubTxFetcher{err: errors.New("toncenter down")}
	conf := &recordingConfirmer{}
	w := NewTONPollingWorker(fetcher, conf, nil, time.Minute)

	w.poll(context.Background())

	if len(conf.confirmed) != 0 {
		t.Fatalf("expected no confirmations, got %v", conf.confirmed)
	}
}

// TestTONPollingSkipsUnknownOrderAndContinues: a transfer whose comment
// parses to a nonexistent order is quarantined/skipped by the ledger without
// aborting the batch — the following matching transfer still settles.
func TestTONPollingSkipsUnknownOrderAndContinues(t *testing.T) {
	svc, store, db := newTONSQLHarness(t)
	ctx := context.Background()
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalTonNano: 1500000000, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &stubTxFetcher{txs: []payment.TONTransaction{
		tonTransfer("1720000000004", "ghost", "order-999999", 1500000000),
		tonTransfer("1720000000005", "real", "order-"+itoa(orderID), 1500000000),
	}}
	var notified []int64
	w := NewTONPollingWorker(fetcher, svc, func(_ context.Context, o *shop.PaymentOutcome) {
		notified = append(notified, o.Order.ID)
	}, time.Minute)

	w.poll(ctx)

	if len(notified) != 1 || notified[0] != orderID {
		t.Fatalf("notified=%v, want [%d] (unknown order must not abort the batch)", notified, orderID)
	}
	order, _ := store.GetOrder(ctx, orderID)
	if order.Status != storage.OrderStatusPaid {
		t.Fatalf("order=%+v, want paid", order)
	}
	var ghostAnomalies int64
	_ = db.Conn().QueryRow(`
		SELECT COUNT(*) FROM payment_anomalies WHERE provider='ton' AND external_id='1720000000004:ghost'`).Scan(&ghostAnomalies)
	if ghostAnomalies != 1 {
		t.Fatalf("ghost anomalies=%d, want 1", ghostAnomalies)
	}
}

// TestTONPollingConfirmErrorDoesNotAbortBatch: an unexpected (not
// conflict/quarantine-class) confirmation error is logged and contained —
// the worker continues with the next transaction and the ticker never
// panics.
func TestTONPollingConfirmErrorDoesNotAbortBatch(t *testing.T) {
	fetcher := &stubTxFetcher{txs: []payment.TONTransaction{
		tonTransfer("1720000000006", "e1", "order-1", 1000000000),
		tonTransfer("1720000000007", "e2", "order-2", 2000000000),
	}}
	conf := &failingConfirmer{err: errors.New("database unavailable")}
	notifications := 0
	w := NewTONPollingWorker(fetcher, conf, func(context.Context, *shop.PaymentOutcome) {
		notifications++
	}, time.Minute)

	w.poll(context.Background())

	if conf.calls != 2 {
		t.Fatalf("confirm calls=%d, want 2 (batch must continue after a failure)", conf.calls)
	}
	if notifications != 0 {
		t.Fatalf("notifications=%d, want 0", notifications)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
