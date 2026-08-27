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

// InvoiceFetcher fetches invoices from the payment provider
// (implemented by payment.CryptoBotPayment).
type InvoiceFetcher interface {
	GetInvoices(ctx context.Context, status string) ([]payment.PendingInvoice, error)
}

// windowedInvoiceFetcher is implemented by CryptoBotPayment. The explicit
// continuation prevents a backlog larger than one bounded provider window from
// pinning every poll to the same first 1000 invoices.
type windowedInvoiceFetcher interface {
	GetInvoicesWindow(ctx context.Context, status string, startOffset int) ([]payment.PendingInvoice, int, error)
}

// PaymentConfirmer confirms a paid order and reports the resulting side
// effects (implemented by shop.OrderService).
type PaymentConfirmer interface {
	ConfirmPaymentReceipt(ctx context.Context, receipt shop.PaymentReceipt) (*shop.PaymentOutcome, error)
	RecordUnexpectedPayment(ctx context.Context, receipt shop.PaymentReceipt, reason string) error
	RecordPaymentAnomaly(ctx context.Context, anomaly storage.PaymentAnomaly) error
}

// CryptoBotPollingWorker polls the CryptoBot API every 30 seconds to catch
// paid invoices that may have been missed due to webhook failures.
type CryptoBotPollingWorker struct {
	crypto         InvoiceFetcher
	orders         PaymentConfirmer
	notify         func(ctx context.Context, outcome *shop.PaymentOutcome)
	interval       time.Duration
	nextPaid       int
	paidHeadID     string
	paidBoundaryID string
}

// NewCryptoBotPollingWorker creates the polling worker. notify is invoked for
// every order the worker confirms so the bot layer can message the users; it
// may be nil.
func NewCryptoBotPollingWorker(crypto InvoiceFetcher, orders PaymentConfirmer, notify func(ctx context.Context, outcome *shop.PaymentOutcome), interval time.Duration) *CryptoBotPollingWorker {
	return &CryptoBotPollingWorker{
		crypto:   crypto,
		orders:   orders,
		notify:   notify,
		interval: interval,
	}
}

func (w *CryptoBotPollingWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("CryptoBot Polling Worker started", "interval", w.interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("CryptoBot Polling Worker stopped")
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

func (w *CryptoBotPollingWorker) poll(ctx context.Context) {
	// Query paid invoices: an invoice whose webhook was missed is no longer
	// "active", so only the paid list can contain work for us. The per-invoice
	// status check below stays as a guard — confirming an unpaid order here
	// would hand out goods for free.
	if windowed, ok := w.crypto.(windowedInvoiceFetcher); ok {
		seen := make(map[string]struct{})
		if w.nextPaid > 0 {
			// A paid invoice is inserted at the provider's head, so absolute
			// offsets can drift during a backlog scan. Refresh the bounded head on
			// every continuation poll without discarding the tail cursor.
			head, headNext, headErr := windowed.GetInvoicesWindow(ctx, "paid", 0)
			if !validPaidWindow(0, headNext, head, headErr) {
				return
			}
			headDelta := invoicesBeforeIdentity(head, w.paidHeadID)
			if headErr == nil {
				// If the paid set shrank while a continuation was outstanding, replay
				// the complete bounded head rather than assuming old rows stayed put.
				headDelta = head
			}
			if !w.processPaidInvoices(ctx, headDelta, seen) {
				return
			}
			if len(head) > 0 {
				w.paidHeadID = head[0].InvoiceID
			}
			if headErr == nil {
				// The whole current paid set now fits in the head window.
				w.nextPaid = 0
				w.paidBoundaryID = ""
				return
			}
		}

		startFrom := w.nextPaid
		if startFrom > 0 {
			// One-row overlap re-reads the prior boundary. Its stable invoice ID
			// locates the first unseen tail row after any head insertion.
			startFrom--
		}
		invoices, next, err := windowed.GetInvoicesWindow(ctx, "paid", startFrom)
		if !validPaidWindow(startFrom, next, invoices, err) {
			return
		}
		toProcess := invoices
		if w.nextPaid > 0 {
			toProcess = invoicesAfterIdentity(invoices, w.paidBoundaryID)
		} else if len(invoices) > 0 {
			w.paidHeadID = invoices[0].InvoiceID
		}
		if !w.processPaidInvoices(ctx, toProcess, seen) {
			return
		}
		if errors.Is(err, payment.ErrCryptoInvoiceWindow) {
			if next <= w.nextPaid {
				slog.Error("CryptoBot polling: non-progressing continuation",
					"offset", startFrom, "next_offset", next, "error", err)
				return
			}
			w.nextPaid = next
			w.paidBoundaryID = invoices[len(invoices)-1].InvoiceID
			slog.Warn("CryptoBot polling: processing incomplete bounded invoice window",
				"count", len(invoices), "offset", startFrom, "next_offset", next, "error", err)
		} else {
			w.nextPaid = 0
			w.paidBoundaryID = ""
		}
		return
	}

	invoices, err := w.crypto.GetInvoices(ctx, "paid")
	if err != nil {
		slog.Error("CryptoBot polling: failed to get invoices", "error", err)
		return
	}
	w.processPaidInvoices(ctx, invoices, nil)
}

func validPaidWindow(startFrom, next int, invoices []payment.PendingInvoice, err error) bool {
	if err != nil && !errors.Is(err, payment.ErrCryptoInvoiceWindow) {
		slog.Error("CryptoBot polling: failed to get invoices", "error", err)
		return false
	}
	if errors.Is(err, payment.ErrCryptoInvoiceWindow) && (next <= startFrom || len(invoices) == 0) {
		slog.Error("CryptoBot polling: non-progressing continuation",
			"offset", startFrom, "next_offset", next, "count", len(invoices), "error", err)
		return false
	}
	return true
}

func invoicesBeforeIdentity(invoices []payment.PendingInvoice, identity string) []payment.PendingInvoice {
	if identity == "" {
		return invoices
	}
	for i := range invoices {
		if invoices[i].InvoiceID == identity {
			return invoices[:i]
		}
	}
	// Head drift exceeded one full page. Processing this bounded page is safe:
	// the immutable payment ledger makes exact invoice retries idempotent.
	return invoices
}

func invoicesAfterIdentity(invoices []payment.PendingInvoice, identity string) []payment.PendingInvoice {
	if identity == "" {
		return invoices
	}
	for i := range invoices {
		if invoices[i].InvoiceID == identity {
			return invoices[i+1:]
		}
	}
	// The boundary moved outside this bounded overlap. Process rather than drop
	// the page; retries are safer than silently skipping a paid provider fact.
	return invoices
}

func (w *CryptoBotPollingWorker) processPaidInvoices(ctx context.Context, invoices []payment.PendingInvoice, seen map[string]struct{}) bool {
	windowDurable := true
	for _, inv := range invoices {
		if seen != nil && inv.InvoiceID != "" {
			if _, duplicate := seen[inv.InvoiceID]; duplicate {
				continue
			}
			seen[inv.InvoiceID] = struct{}{}
		}
		if inv.Status != "paid" {
			slog.Debug("CryptoBot polling: skipping unpaid invoice",
				"order_id", inv.OrderID, "invoice_id", inv.InvoiceID, "status", inv.Status)
			continue
		}
		receipt, receiptErr := inv.PaymentReceipt()
		if receiptErr != nil {
			anomaly, anomalyErr := inv.PaymentAnomaly("polling_invalid_paid_invoice")
			if anomalyErr == nil {
				quarantineErr := w.orders.RecordPaymentAnomaly(ctx, anomaly)
				if quarantineErr == nil || errors.Is(quarantineErr, storage.ErrPaymentNeedsReview) {
					slog.Warn("CryptoBot polling: quarantined invalid paid invoice", "order_id", inv.OrderID, "invoice_id", inv.InvoiceID)
					continue
				}
			}
			windowDurable = false
			slog.Error("CryptoBot polling: invalid paid invoice receipt was not quarantined", "order_id", inv.OrderID, "invoice_id", inv.InvoiceID)
			continue
		}
		outcome, err := w.orders.ConfirmPaymentReceipt(ctx, receipt)
		if err != nil {
			if errors.Is(err, storage.ErrProductOutOfStock) {
				recordErr := w.orders.RecordUnexpectedPayment(ctx, receipt, "out_of_stock_after_charge")
				if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
					continue
				}
			}
			if !isDurablyHandledPaymentError(err) {
				windowDurable = false
			}
			// ErrNotFound / wrong status means already processed — not an error worth logging as error
			slog.Debug("CryptoBot polling: ConfirmPayment skipped", "order_id", inv.OrderID, "reason", err)
			continue
		}
		slog.Info("CryptoBot polling: order marked paid", "order_id", inv.OrderID, "invoice_id", inv.InvoiceID)
		if w.notify != nil {
			w.notify(ctx, outcome)
		}
	}
	return windowDurable
}

func isDurablyHandledPaymentError(err error) bool {
	return errors.Is(err, storage.ErrOrderStatusConflict) || errors.Is(err, storage.ErrNotFound) ||
		errors.Is(err, storage.ErrPaymentNeedsReview) || errors.Is(err, storage.ErrPaymentIdentityConflict) ||
		errors.Is(err, storage.ErrPaymentReceiptMismatch)
}
