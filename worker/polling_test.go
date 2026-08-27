package worker

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"shop_bot/internal/payment"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

// stubInvoiceFetcher returns a fixed invoice list.
type stubInvoiceFetcher struct {
	invoices []payment.PendingInvoice
	err      error
}

func (s stubInvoiceFetcher) GetInvoices(_ context.Context, _ string) ([]payment.PendingInvoice, error) {
	return s.invoices, s.err
}

type windowResponse struct {
	invoices []payment.PendingInvoice
	next     int
	err      error
}

type recordingWindowFetcher struct {
	responses map[int]windowResponse
	sequence  []windowResponse
	offsets   []int
}

func (f *recordingWindowFetcher) GetInvoices(_ context.Context, _ string) ([]payment.PendingInvoice, error) {
	panic("worker should use explicit window API")
}

func (f *recordingWindowFetcher) GetInvoicesWindow(_ context.Context, _ string, startOffset int) ([]payment.PendingInvoice, int, error) {
	f.offsets = append(f.offsets, startOffset)
	if len(f.sequence) > 0 {
		response := f.sequence[0]
		f.sequence = f.sequence[1:]
		return response.invoices, response.next, response.err
	}
	response := f.responses[startOffset]
	return response.invoices, response.next, response.err
}

// recordingConfirmer records which orders were confirmed.
type recordingConfirmer struct {
	confirmed  []int64
	err        error
	receipts   []shop.PaymentReceipt
	anomalies  []storage.PaymentAnomaly
	anomalyErr error
}

func (r *recordingConfirmer) ConfirmPaymentReceipt(_ context.Context, receipt shop.PaymentReceipt) (*shop.PaymentOutcome, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.receipts = append(r.receipts, receipt)
	r.confirmed = append(r.confirmed, receipt.OrderID)
	return &shop.PaymentOutcome{Order: &storage.Order{ID: receipt.OrderID}}, nil
}

func (r *recordingConfirmer) ConfirmPayment(_ context.Context, orderID int64, _, _ string) (*shop.PaymentOutcome, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.confirmed = append(r.confirmed, orderID)
	return &shop.PaymentOutcome{Order: &storage.Order{ID: orderID}}, nil
}

func (r *recordingConfirmer) RecordUnexpectedPayment(_ context.Context, receipt shop.PaymentReceipt, _ string) error {
	r.receipts = append(r.receipts, receipt)
	return storage.ErrPaymentNeedsReview
}

func (r *recordingConfirmer) RecordPaymentAnomaly(_ context.Context, anomaly storage.PaymentAnomaly) error {
	r.anomalies = append(r.anomalies, anomaly)
	return r.anomalyErr
}

// TestPollingSkipsUnpaidInvoices verifies C1: only invoices with status
// "paid" are confirmed; active/expired ones are skipped.
func TestPollingSkipsUnpaidInvoices(t *testing.T) {
	fetcher := stubInvoiceFetcher{invoices: []payment.PendingInvoice{
		{InvoiceID: "101", Status: "active", OrderID: 1, Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "102", Status: "paid", OrderID: 2, Asset: "USDT", Amount: "2.00", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "103", Status: "expired", OrderID: 3, Asset: "USDT", Amount: "3.00", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "104", Status: "paid", OrderID: 4, Asset: "USDT", Amount: "4.00", OccurredAt: time.Unix(1700000000, 0)},
	}}
	conf := &recordingConfirmer{}
	var notified []int64
	w := NewCryptoBotPollingWorker(fetcher, conf, func(_ context.Context, o *shop.PaymentOutcome) {
		notified = append(notified, o.Order.ID)
	}, time.Minute)

	w.poll(context.Background())

	if len(conf.confirmed) != 2 || conf.confirmed[0] != 2 || conf.confirmed[1] != 4 {
		t.Fatalf("confirmed orders = %v, want [2 4]", conf.confirmed)
	}
	if len(notified) != 2 || notified[0] != 2 || notified[1] != 4 {
		t.Fatalf("notified orders = %v, want [2 4]", notified)
	}
	if len(conf.receipts) != 2 || conf.receipts[0].AmountMinor != 200 || conf.receipts[1].AmountMinor != 400 {
		t.Fatalf("receipts = %+v", conf.receipts)
	}
}

// TestPollingConfirmConflictDoesNotNotify verifies that an already-confirmed
// order (idempotent replay) produces no outcome notification.
func TestPollingConfirmConflictDoesNotNotify(t *testing.T) {
	fetcher := stubInvoiceFetcher{invoices: []payment.PendingInvoice{
		{InvoiceID: "101", Status: "paid", OrderID: 1, Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)},
	}}
	conf := &recordingConfirmer{err: storage.ErrOrderStatusConflict}
	notifications := 0
	w := NewCryptoBotPollingWorker(fetcher, conf, func(context.Context, *shop.PaymentOutcome) {
		notifications++
	}, time.Minute)

	w.poll(context.Background())

	if notifications != 0 {
		t.Fatalf("expected no notifications for conflicting confirm, got %d", notifications)
	}
}

func TestPollingRejectsWrongAssetAndAmount(t *testing.T) {
	fetcher := stubInvoiceFetcher{invoices: []payment.PendingInvoice{
		{InvoiceID: "201", Status: "paid", Payload: "1", OrderID: 1, Asset: "TON", Amount: "10.00", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "202", Status: "paid", Payload: "2", OrderID: 2, Asset: "USDT", Amount: "1.001", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "203", Status: "paid", Payload: "3", OrderID: 3, Asset: "USDT", Amount: "1e2", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "204", Status: "paid", Payload: "not-an-order", OrderID: 0, Asset: "USDT", Amount: "2.00", OccurredAt: time.Unix(1700000000, 0)},
		{InvoiceID: "205", Status: "paid", Payload: "4", OrderID: 4, Asset: "USDT", Amount: "not-a-number", OccurredAt: time.Unix(1700000000, 0)},
	}}
	conf := &recordingConfirmer{anomalyErr: storage.ErrPaymentNeedsReview}
	NewCryptoBotPollingWorker(fetcher, conf, nil, time.Minute).poll(context.Background())
	if len(conf.receipts) != 0 || len(conf.confirmed) != 0 {
		t.Fatalf("invalid receipts reached confirmer: %+v", conf.receipts)
	}
	if len(conf.anomalies) != 5 || conf.anomalies[0].ExternalID != "201" ||
		conf.anomalies[1].ExternalID != "202" || conf.anomalies[1].AmountMinor != 1001 || conf.anomalies[1].Scale != 3 ||
		conf.anomalies[2].ExternalID != "203" || conf.anomalies[2].AmountMinor != 100 || conf.anomalies[2].Scale != 0 ||
		conf.anomalies[3].ExternalID != "204" || conf.anomalies[3].ProposedOrderID != 0 ||
		conf.anomalies[4].ExternalID != "205" || conf.anomalies[4].AmountMinor != 0 || conf.anomalies[4].RawAmount != "not-a-number" {
		t.Fatalf("durable anomalies = %+v", conf.anomalies)
	}
}

func TestPollingAcknowledgesResolvedMalformedInvoiceReplay(t *testing.T) {
	fetcher := stubInvoiceFetcher{invoices: []payment.PendingInvoice{
		{InvoiceID: "resolved-malformed", Status: "paid", Payload: "bad-order", Asset: "TON", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)},
	}}
	// A resolved exact anomaly replay returns nil. It is a terminal durable
	// acknowledgement, not a storage failure that should be retried forever.
	conf := &recordingConfirmer{}
	NewCryptoBotPollingWorker(fetcher, conf, nil, time.Minute).poll(context.Background())
	if len(conf.anomalies) != 1 || len(conf.confirmed) != 0 {
		t.Fatalf("anomalies=%+v confirmed=%v", conf.anomalies, conf.confirmed)
	}
}

func TestPollingProcessesOnlyExplicitBoundedPartialWindow(t *testing.T) {
	invoice := payment.PendingInvoice{InvoiceID: "101", Status: "paid", OrderID: 1, Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)}

	t.Run("bounded window", func(t *testing.T) {
		conf := &recordingConfirmer{}
		fetcher := &recordingWindowFetcher{responses: map[int]windowResponse{
			0: {invoices: []payment.PendingInvoice{invoice}, next: 1000, err: payment.ErrCryptoInvoiceWindow},
		}}
		NewCryptoBotPollingWorker(fetcher, conf, nil, time.Minute).poll(context.Background())
		if len(conf.confirmed) != 1 || conf.confirmed[0] != 1 {
			t.Fatalf("confirmed=%v", conf.confirmed)
		}
	})

	t.Run("ambiguous provider failure", func(t *testing.T) {
		conf := &recordingConfirmer{}
		fetcher := stubInvoiceFetcher{invoices: []payment.PendingInvoice{invoice}, err: errors.New("provider down")}
		NewCryptoBotPollingWorker(fetcher, conf, nil, time.Minute).poll(context.Background())
		if len(conf.confirmed) != 0 {
			t.Fatalf("confirmed ambiguous partial response: %v", conf.confirmed)
		}
	})
}

func TestPollingAdvancesBoundedWindowThenReturnsToHead(t *testing.T) {
	first := payment.PendingInvoice{InvoiceID: "301", Status: "paid", OrderID: 1, Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)}
	second := payment.PendingInvoice{InvoiceID: "302", Status: "paid", OrderID: 2, Asset: "USDT", Amount: "2.00", OccurredAt: time.Unix(1700000000, 0)}
	fetcher := &recordingWindowFetcher{sequence: []windowResponse{
		{invoices: []payment.PendingInvoice{first}, next: 1000, err: payment.ErrCryptoInvoiceWindow},
		{invoices: []payment.PendingInvoice{first}, next: 1000, err: payment.ErrCryptoInvoiceWindow},
		{invoices: []payment.PendingInvoice{first, second}},
		{invoices: []payment.PendingInvoice{first}},
	}}
	conf := &recordingConfirmer{}
	worker := NewCryptoBotPollingWorker(fetcher, conf, nil, time.Minute)

	worker.poll(context.Background())
	worker.poll(context.Background())
	worker.poll(context.Background())

	if len(fetcher.offsets) != 4 || fetcher.offsets[0] != 0 || fetcher.offsets[1] != 0 ||
		fetcher.offsets[2] != 999 || fetcher.offsets[3] != 0 {
		t.Fatalf("offsets=%v, want [0 0 999 0]", fetcher.offsets)
	}
	if len(conf.confirmed) != 3 || conf.confirmed[0] != 1 || conf.confirmed[1] != 2 || conf.confirmed[2] != 1 {
		t.Fatalf("confirmed=%v, want [1 2 1]", conf.confirmed)
	}
}

func TestPollingMutableHeadContinuationSeesNewHeadAndShiftedOldTail(t *testing.T) {
	invoice := func(id int64) payment.PendingInvoice {
		return payment.PendingInvoice{
			InvoiceID: strconv.FormatInt(id, 10), Status: "paid", OrderID: id,
			Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0),
		}
	}
	firstWindow := make([]payment.PendingInvoice, 1000)
	for i := range firstWindow {
		firstWindow[i] = invoice(int64(2000 - i))
	}
	newHead := invoice(2001)
	shiftedHead := append([]payment.PendingInvoice{newHead}, firstWindow[:999]...)
	// After insertion, old boundary 1001 shifted from absolute 999 to 1000.
	// The overlap request starts at 999 and must resume after that identity,
	// eventually reaching old tail invoice 1000 rather than skipping it.
	shiftedTail := []payment.PendingInvoice{firstWindow[998], firstWindow[999], invoice(1000)}
	fetcher := &recordingWindowFetcher{sequence: []windowResponse{
		{invoices: firstWindow, next: 1000, err: payment.ErrCryptoInvoiceWindow},
		{invoices: shiftedHead, next: 1000, err: payment.ErrCryptoInvoiceWindow},
		{invoices: shiftedTail},
	}}
	conf := &recordingConfirmer{}
	worker := NewCryptoBotPollingWorker(fetcher, conf, nil, time.Minute)

	worker.poll(context.Background())
	worker.poll(context.Background())

	if !reflect.DeepEqual(fetcher.offsets, []int{0, 0, 999}) {
		t.Fatalf("offsets=%v, want head refresh plus overlap [0 0 999]", fetcher.offsets)
	}
	wantCount := 1002 // original 1000 + newly inserted head + old tail
	if len(conf.receipts) != wantCount {
		t.Fatalf("processed=%d, want %d", len(conf.receipts), wantCount)
	}
	counts := make(map[string]int, len(conf.receipts))
	for _, receipt := range conf.receipts {
		counts[receipt.ExternalID]++
	}
	if counts[newHead.InvoiceID] != 1 || counts["1001"] != 1 || counts["1000"] != 1 {
		t.Fatalf("new_head=%d old_boundary=%d old_tail=%d", counts[newHead.InvoiceID], counts["1001"], counts["1000"])
	}
	for id, count := range counts {
		if count != 1 {
			t.Fatalf("invoice %s processed %d times", id, count)
		}
	}
	if worker.nextPaid != 0 {
		t.Fatalf("continuation=%d, want completed scan", worker.nextPaid)
	}
}

func TestPollingDoesNotAdvanceWindowBeforeDurableHandling(t *testing.T) {
	invoice := payment.PendingInvoice{
		InvoiceID: "401", Status: "paid", OrderID: 1, Asset: "USDT", Amount: "1.00",
		OccurredAt: time.Unix(1700000000, 0),
	}
	fetcher := &recordingWindowFetcher{responses: map[int]windowResponse{
		0: {invoices: []payment.PendingInvoice{invoice}, next: 1000, err: payment.ErrCryptoInvoiceWindow},
	}}
	conf := &recordingConfirmer{err: errors.New("database unavailable")}
	worker := NewCryptoBotPollingWorker(fetcher, conf, nil, time.Minute)
	worker.poll(context.Background())
	worker.poll(context.Background())
	if len(fetcher.offsets) != 2 || fetcher.offsets[0] != 0 || fetcher.offsets[1] != 0 {
		t.Fatalf("offsets=%v, want retry [0 0]", fetcher.offsets)
	}
}

func TestPollingRejectsNonProgressingContinuation(t *testing.T) {
	invoice := payment.PendingInvoice{InvoiceID: "301", Status: "paid", OrderID: 1, Asset: "USDT", Amount: "1.00", OccurredAt: time.Unix(1700000000, 0)}
	fetcher := &recordingWindowFetcher{responses: map[int]windowResponse{
		0: {invoices: []payment.PendingInvoice{invoice}, next: 0, err: payment.ErrCryptoInvoiceWindow},
	}}
	conf := &recordingConfirmer{}
	NewCryptoBotPollingWorker(fetcher, conf, nil, time.Minute).poll(context.Background())
	if len(conf.confirmed) != 0 {
		t.Fatalf("confirmed non-progressing window: %v", conf.confirmed)
	}
}

// TestPollingFetchErrorConfirmsNothing verifies that a provider error aborts
// the polling round without any confirmations.
func TestPollingFetchErrorConfirmsNothing(t *testing.T) {
	fetcher := stubInvoiceFetcher{err: errors.New("provider down")}
	conf := &recordingConfirmer{}
	w := NewCryptoBotPollingWorker(fetcher, conf, nil, time.Minute)

	w.poll(context.Background())

	if len(conf.confirmed) != 0 {
		t.Fatalf("expected no confirmations, got %v", conf.confirmed)
	}
}
