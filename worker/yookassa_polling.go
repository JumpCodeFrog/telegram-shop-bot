package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"shop_bot/internal/payment"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

// yookassaPollWindow bounds each tick to payments created within the last
// 24 hours. The webhook is the primary settlement path and lost webhooks
// surface within minutes, so a day of lookback covers any realistic outage;
// everything in it is re-confirmed every tick, where the ledger answers
// exact replays with durable no-ops (ErrOrderStatusConflict and friends).
const yookassaPollWindow = 24 * time.Hour

// yookassaPollPageSize is the per-request page size for the list call.
const yookassaPollPageSize = 50

// yookassaPollMaxPages caps one tick at 20 pages (1000 payments). A backlog
// deeper than that is an operator-scale event, and the payment-review
// surface covers it — the poller must never spin unbounded on one tick.
const yookassaPollMaxPages = 20

// YooKassaLister lists payments matching the given filters (implemented by
// *payment.YooKassaPayment). Items are full payment snapshots parsed by the
// same fail-closed parser GetPayment uses, so (*Payment).PaymentReceipt()
// builds exactly the receipt the webhook path builds.
type YooKassaLister interface {
	ListPayments(ctx context.Context, status string, createdAtGte time.Time, cursor string, limit int) ([]payment.Payment, string, error)
}

// YooKassaPollingWorker is the lost-webhook backup for RUB card payments:
// the YooKassa webhook is the primary settlement path, and this worker only
// catches payments whose notification never arrived. Each tick re-scans the
// last yookassaPollWindow of succeeded payments and replays every settleable
// one into the ledger, where repeats are no-ops — the list returns ALL
// succeeded payments in the window, so most items are already settled or are
// not bot orders at all, and that noise is expected, not an error.
type YooKassaPollingWorker struct {
	lister   YooKassaLister
	orders   PaymentConfirmer
	notify   func(ctx context.Context, outcome *shop.PaymentOutcome)
	interval time.Duration
}

// NewYooKassaPollingWorker creates the worker. notify is invoked for every
// order the worker settles so the bot layer can message the users; it may be
// nil.
func NewYooKassaPollingWorker(lister YooKassaLister, orders PaymentConfirmer, notify func(ctx context.Context, outcome *shop.PaymentOutcome), interval time.Duration) *YooKassaPollingWorker {
	return &YooKassaPollingWorker{
		lister:   lister,
		orders:   orders,
		notify:   notify,
		interval: interval,
	}
}

func (w *YooKassaPollingWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("YooKassa Polling Worker started", "interval", w.interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("YooKassa Polling Worker stopped")
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// PollOnce runs a single poll pass synchronously. Exported test seam:
// same-package tests drive the unexported poll directly, while cross-package
// E2E tests (internal/bot) need one exported entry into the exact same code
// path. Production scheduling lives in Start; nothing else calls this.
func (w *YooKassaPollingWorker) PollOnce(ctx context.Context) { w.poll(ctx) }

func (w *YooKassaPollingWorker) poll(ctx context.Context) {
	windowStart := time.Now().Add(-yookassaPollWindow)
	cursor := ""
	for page := 0; page < yookassaPollMaxPages; page++ {
		items, nextCursor, err := w.lister.ListPayments(ctx, "succeeded", windowStart, cursor, yookassaPollPageSize)
		if err != nil {
			// The next tick retries; a transient API failure is routine. The
			// error is logged with its wrapped reason because ListPayments
			// fails the whole call closed on a malformed item — a permanently
			// poisoned window is only diagnosable from that wrapped context.
			slog.Error("YooKassa polling: failed to list payments", "cursor", cursor, "error", err)
			return
		}
		for i := range items {
			w.processPayment(ctx, &items[i])
		}
		if nextCursor == "" {
			return
		}
		cursor = nextCursor
	}
	// Reaching this point means the cap was exhausted while a live cursor
	// remained: the backlog is truncated for this tick. Self-heal semantics
	// are unchanged — the next tick re-scans from cursor "" and picks the
	// tail up — but a >cap backlog is an operator-scale event per
	// yookassaPollMaxPages, so make it observable: exactly one Warn per
	// truncated tick, never one per page.
	slog.Warn("yookassa poller: page cap reached, tail deferred to next tick",
		"pages", yookassaPollMaxPages, "cursor", cursor)
}

func (w *YooKassaPollingWorker) processPayment(ctx context.Context, item *payment.Payment) {
	// The list returns every succeeded payment in the window, so items
	// without parsable order metadata (foreign payments) and non-settleable
	// shapes are EXPECTED noise on every re-scan — debug, never error.
	receipt, receiptErr := item.PaymentReceipt()
	if receiptErr != nil {
		slog.Debug("YooKassa polling: skipping payment without order receipt",
			"payment_id", item.ID)
		return
	}
	outcome, err := w.orders.ConfirmPaymentReceipt(ctx, receipt)
	if err != nil {
		// One payment must never panic the ticker or abort the batch.
		// Money that arrives after the stock sold out can never settle,
		// so it is quarantined permanently (no retry); conflicts,
		// unknown orders and quarantined mismatches are durably handled
		// by the ledger (replay-safe no-ops here); anything else is
		// retried on the next tick.
		if errors.Is(err, storage.ErrProductOutOfStock) {
			recordErr := w.orders.RecordUnexpectedPayment(ctx, receipt, "out_of_stock_after_charge")
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				slog.Warn("YooKassa polling: quarantined out-of-stock settlement",
					"order_id", receipt.OrderID, "external_id", receipt.ExternalID)
				return
			}
		}
		if isDurablyHandledPaymentError(err) {
			slog.Debug("YooKassa polling: ConfirmPayment skipped",
				"order_id", receipt.OrderID, "reason", err)
		} else {
			slog.Error("YooKassa polling: ConfirmPayment failed",
				"order_id", receipt.OrderID, "external_id", receipt.ExternalID, "error", err)
		}
		return
	}
	slog.Info("YooKassa polling: order marked paid",
		"order_id", receipt.OrderID, "external_id", receipt.ExternalID)
	if w.notify != nil {
		w.notify(ctx, outcome)
	}
}
