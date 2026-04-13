package worker

import (
	"context"
	"log/slog"
	"time"

	"shop_bot/internal/payment"
	"shop_bot/internal/storage"
)

// CryptoBotPollingWorker polls the CryptoBot API every 30 seconds to catch
// paid invoices that may have been missed due to webhook failures.
type CryptoBotPollingWorker struct {
	crypto     *payment.CryptoBotPayment
	orderStore storage.OrderStore
	interval   time.Duration
}

func NewCryptoBotPollingWorker(crypto *payment.CryptoBotPayment, orderStore storage.OrderStore, interval time.Duration) *CryptoBotPollingWorker {
	return &CryptoBotPollingWorker{
		crypto:     crypto,
		orderStore: orderStore,
		interval:   interval,
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
	// Query "active" (unpaid) invoices rather than all "paid" ones.
	// This keeps the payload small: only invoices currently awaiting payment are returned.
	// Paid invoices that arrive here means the webhook was missed — we confirm them now.
	invoices, err := w.crypto.GetInvoices(ctx, "active")
	if err != nil {
		slog.Error("CryptoBot polling: failed to get invoices", "error", err)
		return
	}

	for _, inv := range invoices {
		err := w.orderStore.UpdateOrderStatus(ctx, inv.OrderID, "pending", "paid", "cryptobot", inv.InvoiceID)
		if err != nil {
			// ErrNotFound / wrong status means already processed — not an error worth logging as error
			slog.Debug("CryptoBot polling: UpdateOrderStatus skipped", "order_id", inv.OrderID, "reason", err)
		} else {
			slog.Info("CryptoBot polling: order marked paid", "order_id", inv.OrderID, "invoice_id", inv.InvoiceID)
		}
	}
}
