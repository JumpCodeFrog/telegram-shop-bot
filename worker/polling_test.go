package worker

import (
	"context"
	"errors"
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

// recordingConfirmer records which orders were confirmed.
type recordingConfirmer struct {
	confirmed []int64
	err       error
}

func (r *recordingConfirmer) ConfirmPayment(_ context.Context, orderID int64, _, _ string) (*shop.PaymentOutcome, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.confirmed = append(r.confirmed, orderID)
	return &shop.PaymentOutcome{Order: &storage.Order{ID: orderID}}, nil
}

// TestPollingSkipsUnpaidInvoices verifies C1: only invoices with status
// "paid" are confirmed; active/expired ones are skipped.
func TestPollingSkipsUnpaidInvoices(t *testing.T) {
	fetcher := stubInvoiceFetcher{invoices: []payment.PendingInvoice{
		{InvoiceID: "inv-1", Status: "active", OrderID: 1},
		{InvoiceID: "inv-2", Status: "paid", OrderID: 2},
		{InvoiceID: "inv-3", Status: "expired", OrderID: 3},
		{InvoiceID: "inv-4", Status: "paid", OrderID: 4},
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
}

// TestPollingConfirmConflictDoesNotNotify verifies that an already-confirmed
// order (idempotent replay) produces no outcome notification.
func TestPollingConfirmConflictDoesNotNotify(t *testing.T) {
	fetcher := stubInvoiceFetcher{invoices: []payment.PendingInvoice{
		{InvoiceID: "inv-1", Status: "paid", OrderID: 1},
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
