package worker

import (
	"context"
	"log/slog"
	"time"

	"shop_bot/internal/payment"
	"shop_bot/internal/shop"
)

// InvoiceFetcher fetches invoices from the payment provider
// (implemented by payment.CryptoBotPayment).
type InvoiceFetcher interface {
	GetInvoices(ctx context.Context, status string) ([]payment.PendingInvoice, error)
}

// PaymentConfirmer confirms a paid order and reports the resulting side
// effects (implemented by shop.OrderService).
type PaymentConfirmer interface {
	ConfirmPayment(ctx context.Context, orderID int64, method, paymentID string) (*shop.PaymentOutcome, error)
}

// CryptoBotPollingWorker polls the CryptoBot API every 30 seconds to catch
// paid invoices that may have been missed due to webhook failures.
type CryptoBotPollingWorker struct {
	crypto   InvoiceFetcher
	orders   PaymentConfirmer
	notify   func(ctx context.Context, outcome *shop.PaymentOutcome)
	interval time.Duration
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
	invoices, err := w.crypto.GetInvoices(ctx, "paid")
	if err != nil {
		slog.Error("CryptoBot polling: failed to get invoices", "error", err)
		return
	}

	for _, inv := range invoices {
		if inv.Status != "paid" {
			slog.Debug("CryptoBot polling: skipping unpaid invoice",
				"order_id", inv.OrderID, "invoice_id", inv.InvoiceID, "status", inv.Status)
			continue
		}
		outcome, err := w.orders.ConfirmPayment(ctx, inv.OrderID, "cryptobot", inv.InvoiceID)
		if err != nil {
			// ErrNotFound / wrong status means already processed — not an error worth logging as error
			slog.Debug("CryptoBot polling: ConfirmPayment skipped", "order_id", inv.OrderID, "reason", err)
			continue
		}
		slog.Info("CryptoBot polling: order marked paid", "order_id", inv.OrderID, "invoice_id", inv.InvoiceID)
		if w.notify != nil {
			w.notify(ctx, outcome)
		}
	}
}
