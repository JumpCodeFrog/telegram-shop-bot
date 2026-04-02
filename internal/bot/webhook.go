package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

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
		if err := b.order.ConfirmPayment(ctx, payload.OrderID, "crypto", payload.InvoiceID); err != nil {
			if errors.Is(err, storage.ErrOrderStatusConflict) {
				// Duplicate webhook — payment already confirmed. Ack to prevent CryptoBot retries.
				b.logger.Info("cryptobot webhook already confirmed (idempotent)", "order_id", payload.OrderID)
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

		// Look up the order to find the buyer's user ID for notification.
		order, err := b.order.GetOrder(ctx, payload.OrderID)
		if err != nil {
			b.logger.Error("cryptobot webhook: get order", "order_id", payload.OrderID, "error", err)
			// Payment is already confirmed; respond OK even if notification fails.
			w.WriteHeader(http.StatusOK)
			return
		}

		text := fmt.Sprintf("✅ Оплата заказа #%d прошла успешно!\n\nСпасибо за покупку!", payload.OrderID)
		b.send(tgbotapi.NewMessage(order.UserID, text))

		b.notifyAdmins(fmt.Sprintf("💎 Оплачен заказ #%d (Crypto) — пользователь %d — $%.2f",
			payload.OrderID, order.UserID, order.TotalUSD))

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

		// Verify secret token if configured.
		if b.cfg.TelegramWebhookSecret != "" {
			secret := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
			if secret != b.cfg.TelegramWebhookSecret {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}

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

		b.HandleUpdate(update)
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
