package bot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

// CryptoBotWebhookHandler returns an http.HandlerFunc that processes
// incoming CryptoBot webhook callbacks. It verifies the request signature,
// parses the payload, confirms the payment, and notifies the user.
func (b *Bot) CryptoBotWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
		body, err := io.ReadAll(r.Body)
		if err != nil {
			b.logger.Error("cryptobot webhook: read body", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		signature := r.Header.Get("crypto-pay-api-signature")

		if !b.crypto.VerifyWebhook(body, signature) {
			b.logger.Error("cryptobot webhook: invalid signature")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		payload, err := b.crypto.ParseWebhook(body)
		if err != nil {
			b.logger.Error("cryptobot webhook: parse payload", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if payload.Status != "paid" {
			// Acknowledge non-payment updates without further processing.
			w.WriteHeader(http.StatusOK)
			return
		}

		ctx := context.Background()
		outcome, err := b.order.ConfirmPayment(ctx, payload.OrderID, "crypto", payload.InvoiceID)
		if err != nil {
			if errors.Is(err, storage.ErrOrderStatusConflict) || errors.Is(err, storage.ErrNotFound) {
				// Duplicate webhook or unknown order — nothing to retry.
				// Ack with 200 so CryptoBot stops redelivering.
				b.logger.Info("cryptobot webhook ignored (idempotent)", "order_id", payload.OrderID, "reason", err)
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("cryptobot webhook: confirm payment", "order_id", payload.OrderID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if b.metrics != nil {
			b.metrics.SuccessfulPayments.WithLabelValues("crypto").Inc()
		}

		order := outcome.Order
		lang := b.userLang(ctx, order.UserID)

		text := fmt.Sprintf(b.t(lang, "payment_success"), payload.OrderID)
		b.send(tgbotapi.NewMessage(order.UserID, text))

		b.NotifyPaymentOutcome(ctx, outcome)

		b.notifyAdmins(ctx, AdminEventOrderPaid, fmt.Sprintf(b.t("en", "admin_order_paid_crypto"),
			payload.OrderID, order.UserID, order.TotalUSD))

		b.outWebhook.Send(service.OutboundWebhookEvent{
			Event:      "order.paid",
			OrderID:    payload.OrderID,
			UserID:     order.UserID,
			TotalUSD:   order.TotalUSD,
			TotalStars: order.TotalStars,
			Method:     "crypto",
			PaymentID:  payload.InvoiceID,
		})

		w.WriteHeader(http.StatusOK)
	}
}

// TelegramWebhookHandler returns an http.HandlerFunc that processes incoming
// Telegram updates delivered via webhook.
func (b *Bot) TelegramWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Verify secret token if configured. Constant-time comparison keeps
		// the secret safe from timing side channels.
		if b.cfg.TelegramWebhookSecret != "" {
			secret := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
			if subtle.ConstantTimeCompare([]byte(secret), []byte(b.cfg.TelegramWebhookSecret)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
		body, err := io.ReadAll(r.Body)
		if err != nil {
			b.logger.Error("telegram webhook: read body", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var update tgbotapi.Update
		if err := json.Unmarshal(body, &update); err != nil {
			b.logger.Error("telegram webhook: parse update", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Bot API subscription fields are invisible to tgbotapi v5, so the
		// expiration date is lifted from the raw JSON before dispatch.
		cleanup := b.stashSubscriptionExpiry(body)
		b.HandleUpdate(update)
		cleanup()
		w.WriteHeader(http.StatusOK)
	}
}

// WebhookHandler returns an http.Handler that routes all webhook endpoints.
// Mount this on your HTTP server.
func (b *Bot) WebhookHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/cryptobot-webhook", b.CryptoBotWebhookHandler())
	mux.HandleFunc("/telegram-webhook", b.TelegramWebhookHandler())
	return mux
}
