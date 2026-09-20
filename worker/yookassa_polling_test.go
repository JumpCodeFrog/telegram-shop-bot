package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"shop_bot/internal/payment"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

// yookassaListCall records one ListPayments invocation so tests can pin the
// status filter, window, cursor threading and page size.
type yookassaListCall struct {
	status       string
	createdAtGte time.Time
	cursor       string
	limit        int
}

// yookassaListPage is one canned ListPayments response.
type yookassaListPage struct {
	items      []payment.Payment
	nextCursor string
	err        error
}

// stubYooKassaLister returns canned pages in order; the last page repeats
// forever, so a single page with a non-empty cursor models an endless stream
// (the page-cap test) and one page models the normal response. Mirrors
// stubTxFetcher in ton_polling_test.go.
type stubYooKassaLister struct {
	pages []yookassaListPage
	calls []yookassaListCall
}

func (s *stubYooKassaLister) ListPayments(_ context.Context, status string, createdAtGte time.Time, cursor string, limit int) ([]payment.Payment, string, error) {
	s.calls = append(s.calls, yookassaListCall{status: status, createdAtGte: createdAtGte, cursor: cursor, limit: limit})
	if len(s.pages) == 0 {
		return nil, "", nil
	}
	page := s.pages[0]
	if len(s.pages) > 1 {
		s.pages = s.pages[1:]
	}
	return page.items, page.nextCursor, page.err
}

// yooPayment builds a succeeded RUB list item, the shape ListPayments returns
// after parsing a full YooKassa payment object.
func yooPayment(id string, orderID int64, amount string) payment.Payment {
	return payment.Payment{
		ID: id, Status: "succeeded", Paid: true,
		Amount: amount, Currency: "RUB", OrderID: orderID,
		OccurredAt: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
	}
}

// TestYooKassaPollingSettlesPendingOrder pins the happy path: a succeeded
// list item whose metadata order_id points at a pending RUB order settles it,
// writes a provider-yookassa attempt row with the payment id, decrements the
// stock exactly once and triggers exactly one outcome notification. The list
// request shape (status filter, page size, empty first cursor) is pinned via
// the recorded call.
func TestYooKassaPollingSettlesPendingOrder(t *testing.T) {
	svc, store, db := newTONSQLHarness(t)
	ctx := context.Background()
	productID := seedYooKassaProduct(t, ctx, db, 5)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalRUB: 12.50, Status: storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: productID, ProductName: "Widget", Quantity: 1, PriceUSD: 12.50}})
	if err != nil {
		t.Fatal(err)
	}
	lister := &stubYooKassaLister{pages: []yookassaListPage{{
		items: []payment.Payment{yooPayment("2490000000001", orderID, "12.50")},
	}}}
	var notified []int64
	w := NewYooKassaPollingWorker(lister, svc, func(_ context.Context, o *shop.PaymentOutcome) {
		notified = append(notified, o.Order.ID)
	}, time.Minute)

	w.poll(ctx)

	if len(notified) != 1 || notified[0] != orderID {
		t.Fatalf("notified=%v, want [%d]", notified, orderID)
	}
	if len(lister.calls) != 1 {
		t.Fatalf("list calls=%d, want 1", len(lister.calls))
	}
	call := lister.calls[0]
	if call.status != "succeeded" || call.limit != 50 || call.cursor != "" {
		t.Fatalf("call status=%q limit=%d cursor=%q, want succeeded/50/empty", call.status, call.limit, call.cursor)
	}
	order, err := store.GetOrder(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != storage.OrderStatusPaid || order.PaymentMethod != storage.PaymentMethodYooKassa ||
		order.PaymentID != "2490000000001" {
		t.Fatalf("order=%+v", order)
	}
	var amountMinor, scale int64
	var provider, externalID, currency, status string
	if err := db.Conn().QueryRow(`
		SELECT provider, external_id, currency, amount_minor, scale, status FROM payment_attempts
		WHERE order_id=?`, orderID).Scan(&provider, &externalID, &currency, &amountMinor, &scale, &status); err != nil {
		t.Fatalf("attempt row: %v", err)
	}
	if provider != "yookassa" || externalID != "2490000000001" || currency != "RUB" ||
		amountMinor != 1250 || scale != 2 || status != "succeeded" {
		t.Fatalf("attempt provider=%s external_id=%s currency=%s amount=%d scale=%d status=%s",
			provider, externalID, currency, amountMinor, scale, status)
	}
	if stock := yooKassaStock(t, ctx, db, productID); stock != 4 {
		t.Fatalf("stock=%d, want 4 (decremented exactly once at settlement)", stock)
	}
}

// seedYooKassaProduct inserts a 12.50-USD widget with the given stock and
// returns its id, the fixture the TON out-of-stock leg uses.
func seedYooKassaProduct(t *testing.T, ctx context.Context, db *storage.DB, stock int) int64 {
	t.Helper()
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('yoo-poll')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Widget', 12.50, 25, ?, 1)`, stock)
	if err != nil {
		t.Fatal(err)
	}
	productID, _ := res.LastInsertId()
	return productID
}

func yooKassaStock(t *testing.T, ctx context.Context, db *storage.DB, productID int64) int {
	t.Helper()
	var stock int
	if err := db.Conn().QueryRowContext(ctx, `SELECT stock FROM products WHERE id=?`, productID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	return stock
}

// TestYooKassaPollingReplayIsIdempotent mirrors the TON replay pin against
// the real ledger: the webhook (or a previous tick) already settled the
// order, so every re-scan of the same list item is a durable
// ErrOrderStatusConflict no-op — no second notification, no new rows.
func TestYooKassaPollingReplayIsIdempotent(t *testing.T) {
	svc, store, db := newTONSQLHarness(t)
	ctx := context.Background()
	productID := seedYooKassaProduct(t, ctx, db, 5)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalRUB: 12.50, Status: storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: productID, ProductName: "Widget", Quantity: 1, PriceUSD: 12.50}})
	if err != nil {
		t.Fatal(err)
	}
	lister := &stubYooKassaLister{pages: []yookassaListPage{{
		items: []payment.Payment{yooPayment("2490000000002", orderID, "12.50")},
	}}}
	notifications := 0
	w := NewYooKassaPollingWorker(lister, svc, func(context.Context, *shop.PaymentOutcome) {
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
	if stock := yooKassaStock(t, ctx, db, productID); stock != 4 {
		t.Fatalf("stock=%d, want 4 (decremented once by the first poll; the replay must not touch it)", stock)
	}
}

// TestYooKassaPollingSkipsNeedsReviewOrder: an order already quarantined in
// needs_review must never settle via the backup poller. The ledger answers
// the confirm with the durable ErrPaymentNeedsReview class, so the worker
// skips silently — no notification, order untouched.
func TestYooKassaPollingSkipsNeedsReviewOrder(t *testing.T) {
	svc, store, db := newTONSQLHarness(t)
	ctx := context.Background()
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalRUB: 12.50, Status: storage.OrderStatusPending,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().ExecContext(ctx,
		`UPDATE orders SET payment_state=? WHERE id=?`, storage.PaymentStateNeedsReview, orderID); err != nil {
		t.Fatal(err)
	}
	lister := &stubYooKassaLister{pages: []yookassaListPage{{
		items: []payment.Payment{yooPayment("2490000000003", orderID, "12.50")},
	}}}
	notifications := 0
	w := NewYooKassaPollingWorker(lister, svc, func(context.Context, *shop.PaymentOutcome) {
		notifications++
	}, time.Minute)

	w.poll(ctx)

	if notifications != 0 {
		t.Fatalf("notifications=%d, want 0", notifications)
	}
	order, _ := store.GetOrder(ctx, orderID)
	if order.Status != storage.OrderStatusPending || order.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("order=%+v, want pending/needs_review (quarantined orders never settle here)", order)
	}
}

// TestYooKassaPollingSkipsPaymentsWithoutOrderReceipt verifies that foreign
// or non-settleable list items never reach the confirmer. The list returns
// ALL succeeded payments in the window, so missing/unparsable order_id
// metadata, unpaid or non-RUB shapes and unparsable amounts are expected
// noise, not errors.
func TestYooKassaPollingSkipsPaymentsWithoutOrderReceipt(t *testing.T) {
	foreign := yooPayment("2490000000004", 0, "10.00") // order_id metadata absent/unparsable → OrderID 0
	unpaid := yooPayment("2490000000005", 7, "10.00")
	unpaid.Paid = false
	usd := yooPayment("2490000000006", 7, "10.00")
	usd.Currency = "USD"
	badAmount := yooPayment("2490000000007", 7, "free")
	noTime := yooPayment("2490000000008", 7, "10.00")
	noTime.OccurredAt = time.Time{}
	lister := &stubYooKassaLister{pages: []yookassaListPage{{
		items: []payment.Payment{foreign, unpaid, usd, badAmount, noTime},
	}}}
	conf := &recordingConfirmer{}
	notifications := 0
	w := NewYooKassaPollingWorker(lister, conf, func(context.Context, *shop.PaymentOutcome) {
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

// TestYooKassaPollingOutOfStockIsQuarantined mirrors the TON fix-round leg
// exactly: the buyer pays after the stock sold out, so the settlement-time
// stock decrement fails with ErrProductOutOfStock and the worker quarantines
// the receipt via RecordUnexpectedPayment (reason out_of_stock_after_charge).
// The durable evidence is a needs_review payment_attempt plus a needs_review
// captured payment_event; the order stays pending in needs_review and is
// NEVER retried — the second poll over the same item reaches
// ConfirmPaymentReceipt again, which the ledger now answers with
// ErrPaymentNeedsReview, so no second row of any kind is written.
func TestYooKassaPollingOutOfStockIsQuarantined(t *testing.T) {
	svc, store, db := newTONSQLHarness(t)
	ctx := context.Background()
	productID := seedYooKassaProduct(t, ctx, db, 1)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalRUB: 12.50, Status: storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: productID, ProductName: "Widget", Quantity: 1, PriceUSD: 12.50}})
	if err != nil {
		t.Fatal(err)
	}
	// The stock sells out between order creation and the poll.
	if _, err := db.Conn().ExecContext(ctx, `UPDATE products SET stock = 0 WHERE id = ?`, productID); err != nil {
		t.Fatal(err)
	}
	item := yooPayment("2490000000009", orderID, "12.50")
	lister := &stubYooKassaLister{pages: []yookassaListPage{{
		items: []payment.Payment{item},
	}}}
	notifications := 0
	w := NewYooKassaPollingWorker(lister, svc, func(context.Context, *shop.PaymentOutcome) {
		notifications++
	}, time.Minute)

	w.poll(ctx)
	w.poll(ctx) // re-poll over the same item: quarantine is durable, no second capture

	if notifications != 0 {
		t.Fatalf("notifications=%d, want 0", notifications)
	}
	order, _ := store.GetOrder(ctx, orderID)
	if order.Status != storage.OrderStatusPending || order.PaymentState != storage.PaymentStateNeedsReview {
		t.Fatalf("order=%+v, want pending/needs_review (money quarantined, never settled)", order)
	}
	var attempts, reviewEvents int64
	var attemptStatus, eventProvider string
	if err := db.Conn().QueryRow(`
		SELECT COUNT(*), COALESCE(MIN(status), '') FROM payment_attempts
		WHERE order_id=? AND provider='yookassa' AND external_id='2490000000009'`,
		orderID).Scan(&attempts, &attemptStatus); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || attemptStatus != storage.PaymentStateNeedsReview {
		t.Fatalf("attempts=%d status=%q, want 1 needs_review attempt after two polls", attempts, attemptStatus)
	}
	if err := db.Conn().QueryRow(`
		SELECT COUNT(*), COALESCE(MIN(provider), '') FROM payment_events
		WHERE order_id=? AND external_id='2490000000009'
		  AND event_kind='captured' AND disposition='needs_review'`,
		orderID).Scan(&reviewEvents, &eventProvider); err != nil {
		t.Fatal(err)
	}
	if reviewEvents != 1 || eventProvider != "yookassa" {
		t.Fatalf("needs_review events=%d provider=%q, want exactly 1 yookassa event after two polls", reviewEvents, eventProvider)
	}
	var anomalies int64
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, orderID).Scan(&anomalies)
	if anomalies != 0 {
		t.Fatalf("anomalies=%d, want 0 (no identity conflict; the event/attempt rows are the quarantine)", anomalies)
	}
	// Pin the re-poll observable: a repeat confirm of the quarantined fact is
	// a durable ErrPaymentNeedsReview no-op that writes nothing new.
	receipt, receiptErr := item.PaymentReceipt()
	if receiptErr != nil {
		t.Fatal(receiptErr)
	}
	if _, err := svc.ConfirmPaymentReceipt(ctx, receipt); !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("repeat confirm err=%v, want ErrPaymentNeedsReview", err)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events
		WHERE order_id=? AND external_id='2490000000009'`, orderID).Scan(&reviewEvents)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts
		WHERE order_id=? AND provider='yookassa' AND external_id='2490000000009'`, orderID).Scan(&attempts)
	if reviewEvents != 1 || attempts != 1 {
		t.Fatalf("after repeat confirm: events=%d attempts=%d, want still 1/1", reviewEvents, attempts)
	}
}

// TestYooKassaPollingListErrorConfirmsNothing mirrors the TON fetch-error
// test: a list error aborts the poll round cleanly (the next tick retries);
// nothing reaches the confirmer.
func TestYooKassaPollingListErrorConfirmsNothing(t *testing.T) {
	lister := &stubYooKassaLister{pages: []yookassaListPage{{
		err: errors.New("yookassa: parse payment list item: yookassa: invalid payment receipt"),
	}}}
	conf := &recordingConfirmer{}
	w := NewYooKassaPollingWorker(lister, conf, nil, time.Minute)

	w.poll(context.Background())

	if len(conf.confirmed) != 0 || len(conf.receipts) != 0 {
		t.Fatalf("expected no confirmations, got confirmed=%v receipts=%v", conf.confirmed, conf.receipts)
	}
}

// TestYooKassaPollingPageCap: a provider stuck handing out non-empty cursors
// must never spin the tick — exactly yookassaPollMaxPages (20) pages are
// fetched, threading each page's next_cursor into the following call.
func TestYooKassaPollingPageCap(t *testing.T) {
	pages := make([]yookassaListPage, 0, 25)
	for i := 1; i <= 25; i++ {
		pages = append(pages, yookassaListPage{nextCursor: fmt.Sprintf("c%d", i)})
	}
	lister := &stubYooKassaLister{pages: pages}
	w := NewYooKassaPollingWorker(lister, &recordingConfirmer{}, nil, time.Minute)

	w.poll(context.Background())

	if len(lister.calls) != 20 {
		t.Fatalf("list calls=%d, want exactly 20 (hard page cap per tick)", len(lister.calls))
	}
	for i, call := range lister.calls {
		wantCursor := ""
		if i > 0 {
			wantCursor = fmt.Sprintf("c%d", i)
		}
		if call.cursor != wantCursor {
			t.Fatalf("call %d cursor=%q, want %q (next_cursor threading)", i, call.cursor, wantCursor)
		}
	}
}

// TestYooKassaPollingWindowParam pins the created_at.gte window: each tick
// re-scans payments created within the last 24 hours (tolerance: a minute).
func TestYooKassaPollingWindowParam(t *testing.T) {
	lister := &stubYooKassaLister{pages: []yookassaListPage{{}}}
	w := NewYooKassaPollingWorker(lister, &recordingConfirmer{}, nil, time.Minute)

	w.poll(context.Background())

	if len(lister.calls) != 1 {
		t.Fatalf("list calls=%d, want 1", len(lister.calls))
	}
	want := time.Now().Add(-24 * time.Hour)
	if delta := lister.calls[0].createdAtGte.Sub(want); delta < -time.Minute || delta > time.Minute {
		t.Fatalf("created_at.gte=%v, want ≈ %v (now-24h, got delta %v)",
			lister.calls[0].createdAtGte, want, delta)
	}
}

// TestYooKassaPollingConfirmErrorDoesNotAbortBatch mirrors the TON
// containment test: an unexpected (not conflict/quarantine-class) confirm
// error is logged and contained — the worker continues with the next item
// and the ticker never dies.
func TestYooKassaPollingConfirmErrorDoesNotAbortBatch(t *testing.T) {
	lister := &stubYooKassaLister{pages: []yookassaListPage{{
		items: []payment.Payment{
			yooPayment("2490000000010", 1, "10.00"),
			yooPayment("2490000000011", 2, "20.00"),
		},
	}}}
	conf := &failingConfirmer{err: errors.New("database unavailable")}
	notifications := 0
	w := NewYooKassaPollingWorker(lister, conf, func(context.Context, *shop.PaymentOutcome) {
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
