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

// tonPollWindow bounds each poll to the wallet's latest 50 transactions.
const tonPollWindow = 50

// TxFetcher fetches the watched wallet's latest on-chain transactions
// (implemented by payment.TONPayment).
type TxFetcher interface {
	GetTransactions(ctx context.Context, limit int) ([]payment.TONTransaction, error)
}

// TONPollingWorker polls the watched TON wallet every interval and settles
// inbound transfers whose comment is an exact "order-<id>" reference. TON
// has no webhook or signature, so this worker is the ONLY settlement path
// for on-chain payments.
//
// There is deliberately no cursor or pagination: every poll re-reads the
// latest tonPollWindow transactions and replays each matching transfer into
// the ledger, where repeats are no-ops (ConfirmPaymentReceipt answers an
// exact replay with storage.ErrOrderStatusConflict, and quarantined facts
// stay quarantined). Documented limitation: a backlog deeper than
// tonPollWindow transfers between two ticks leaves the older tail unsettled
// until the window covers it again; at orders-per-30s realities the window
// is ample, and windowing can be added the way the crypto worker did it if
// volumes ever prove otherwise.
type TONPollingWorker struct {
	ton      TxFetcher
	orders   PaymentConfirmer
	notify   func(ctx context.Context, outcome *shop.PaymentOutcome)
	interval time.Duration
}

// NewTONPollingWorker creates the worker. notify is invoked for every order
// the worker settles so the bot layer can message the users; it may be nil.
func NewTONPollingWorker(ton TxFetcher, orders PaymentConfirmer, notify func(ctx context.Context, outcome *shop.PaymentOutcome), interval time.Duration) *TONPollingWorker {
	return &TONPollingWorker{
		ton:      ton,
		orders:   orders,
		notify:   notify,
		interval: interval,
	}
}

func (w *TONPollingWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("TON Polling Worker started", "interval", w.interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("TON Polling Worker stopped")
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
func (w *TONPollingWorker) PollOnce(ctx context.Context) { w.poll(ctx) }

func (w *TONPollingWorker) poll(ctx context.Context) {
	transactions, err := w.ton.GetTransactions(ctx, tonPollWindow)
	if err != nil {
		// The next tick retries; a transient toncenter failure is routine.
		slog.Error("TON polling: failed to get transactions", "error", err)
		return
	}
	for _, tx := range transactions {
		// The adapter already dropped binary (dataRaw) messages and
		// unparsable values. Of the rest, only exact "order-<id>" comments
		// with a positive value and a complete lt:hash identity become
		// receipts; foreign transfers are simply not shop business.
		receipt, receiptErr := tx.PaymentReceipt()
		if receiptErr != nil {
			slog.Debug("TON polling: skipping transaction without order receipt",
				"lt", tx.LT, "hash", tx.Hash)
			continue
		}
		outcome, err := w.orders.ConfirmPaymentReceipt(ctx, receipt)
		if err != nil {
			// One transfer must never panic the ticker or abort the batch.
			// Money that arrives after the stock sold out can never settle,
			// so it is quarantined permanently (no retry); conflicts,
			// unknown orders and quarantined mismatches are durably handled
			// by the ledger (replay-safe no-ops here); anything else is
			// retried on the next tick.
			if errors.Is(err, storage.ErrProductOutOfStock) {
				recordErr := w.orders.RecordUnexpectedPayment(ctx, receipt, "out_of_stock_after_charge")
				if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
					slog.Warn("TON polling: quarantined out-of-stock settlement",
						"order_id", receipt.OrderID, "external_id", receipt.ExternalID)
					continue
				}
			}
			if isDurablyHandledPaymentError(err) {
				slog.Debug("TON polling: ConfirmPayment skipped",
					"order_id", receipt.OrderID, "reason", err)
			} else {
				slog.Error("TON polling: ConfirmPayment failed",
					"order_id", receipt.OrderID, "external_id", receipt.ExternalID, "error", err)
			}
			continue
		}
		slog.Info("TON polling: order marked paid",
			"order_id", receipt.OrderID, "external_id", receipt.ExternalID)
		if w.notify != nil {
			w.notify(ctx, outcome)
		}
	}
}
