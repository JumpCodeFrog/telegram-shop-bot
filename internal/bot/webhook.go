package bot

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/config"
	"shop_bot/internal/payment"
	"shop_bot/internal/service"
	"shop_bot/internal/shop"
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
			digest := sha256.Sum256(body)
			recordErr := b.order.RecordPaymentAnomaly(context.Background(), storage.PaymentAnomaly{
				Provider: storage.PaymentMethodCrypto, RawPayload: fmt.Sprintf("sha256:%x", digest), Reason: "webhook_parse_failure",
			})
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("cryptobot webhook: signed payload was not quarantined", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if payload.Status != "paid" {
			// Acknowledge non-payment updates without further processing.
			w.WriteHeader(http.StatusOK)
			return
		}

		ctx := context.Background()
		if !payload.ReceiptComplete {
			anomaly, _ := (payment.PendingInvoice{
				InvoiceID: payload.InvoiceID, Status: payload.Status, OrderID: payload.OrderID,
				Payload: payload.Payload, Asset: payload.Asset, Amount: payload.Amount,
				PaidAt: payload.PaidAt, OccurredAt: payload.OccurredAt,
			}).PaymentAnomaly("webhook_invalid_paid_invoice")
			recordErr := b.order.RecordPaymentAnomaly(ctx, anomaly)
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("cryptobot webhook: malformed signed receipt was not quarantined", "order_id", payload.OrderID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		outcome, err := b.order.ConfirmPaymentReceipt(ctx, shop.PaymentReceipt{
			OrderID: payload.OrderID, Provider: storage.PaymentMethodCrypto,
			ExternalID: payload.InvoiceID, Currency: payload.Asset,
			AmountMinor: payload.AmountMinor, Scale: 2, OccurredAt: payload.OccurredAt,
		})
		if err != nil {
			if errors.Is(err, storage.ErrProductOutOfStock) {
				recordErr := b.order.RecordUnexpectedPayment(ctx, shop.PaymentReceipt{
					OrderID: payload.OrderID, Provider: storage.PaymentMethodCrypto,
					ExternalID: payload.InvoiceID, Currency: payload.Asset,
					AmountMinor: payload.AmountMinor, Scale: 2, OccurredAt: payload.OccurredAt,
				}, "out_of_stock_after_charge")
				if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			if errors.Is(err, storage.ErrOrderStatusConflict) || errors.Is(err, storage.ErrNotFound) ||
				errors.Is(err, storage.ErrPaymentNeedsReview) || errors.Is(err, storage.ErrPaymentIdentityConflict) ||
				errors.Is(err, storage.ErrPaymentReceiptMismatch) {
				// Exact replay, terminal state, or a durably quarantined provider fact:
				// ACK so CryptoBot does not retry an event already preserved locally.
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

		// A public Telegram webhook is never an unsigned compatibility mode.
		// Configuration loading rejects this state; this handler check keeps
		// direct construction and future wiring fail-closed as defense in depth.
		if b.cfg == nil || config.ValidateTelegramWebhookSecret(b.cfg.TelegramWebhookSecret) != nil {
			b.logger.Error("telegram webhook disabled: strong secret is not configured")
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		secret := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if subtle.ConstantTimeCompare([]byte(secret), []byte(b.cfg.TelegramWebhookSecret)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
		body, err := io.ReadAll(r.Body)
		if err != nil {
			b.logger.Error("telegram webhook: read body", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		update, cleanup, err := b.decodeTelegramUpdate(body)
		if err != nil {
			handled, _, quarantineErr := b.quarantineUndecodableStarsUpdate(r.Context(), body)
			if handled {
				if quarantineErr != nil {
					b.logger.Error("telegram webhook: provider payment decode failure was not quarantined", "error", quarantineErr)
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("telegram webhook: parse update", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		defer cleanup()

		if update.Message != nil && update.Message.SuccessfulPayment != nil {
			if err := b.processSuccessfulPayment(update.Message); err != nil {
				b.logger.Error("telegram webhook: Stars payment not durably handled", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		} else {
			b.HandleUpdate(update)
		}
		w.WriteHeader(http.StatusOK)
	}
}

// quarantineUndecodableStarsUpdate catches a valid JSON Telegram envelope
// whose successful_payment fields cannot be decoded by the SDK. Only a digest
// is persisted, so malformed provider input cannot leak invoice payloads.
func (b *Bot) quarantineUndecodableStarsUpdate(ctx context.Context, raw []byte) (bool, int, error) {
	var envelope struct {
		UpdateID int `json:"update_id"`
		Message  *struct {
			SuccessfulPayment json.RawMessage `json:"successful_payment"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Message == nil ||
		len(envelope.Message.SuccessfulPayment) == 0 || string(envelope.Message.SuccessfulPayment) == "null" {
		return false, 0, nil
	}
	digest := sha256.Sum256(raw)
	err := b.order.RecordPaymentAnomaly(ctx, storage.PaymentAnomaly{
		Provider:   storage.PaymentMethodStars,
		EventKind:  storage.PaymentEventCaptured,
		RawPayload: fmt.Sprintf("telegram_update_sha256:%x", digest),
		Reason:     "stars_update_decode_failure",
	})
	if err == nil || errors.Is(err, storage.ErrPaymentNeedsReview) {
		return true, envelope.UpdateID, nil
	}
	return true, envelope.UpdateID, fmt.Errorf("persist undecodable Stars payment: %w", err)
}
