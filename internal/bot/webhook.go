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
			recordErr := b.order.RecordPaymentAnomaly(r.Context(), storage.PaymentAnomaly{
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

		// The webhook request IS the context: all settlement work below
		// completes before the response is written, so propagate r.Context().
		ctx := r.Context()
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

// YooKassaWebhookHandler processes YooKassa payment notifications. YooKassa
// webhooks are unsigned: the body is used ONLY to learn which payment changed;
// the authoritative state is refetched from the API before any settlement.
func (b *Bot) YooKassaWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if b.yookassa == nil || !b.yookassa.Configured() {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
		body, err := io.ReadAll(r.Body)
		if err != nil {
			b.logger.Error("yookassa webhook: read body", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		notification, err := b.yookassa.ParseWebhook(body)
		if err != nil || notification.PaymentID == "" {
			// Unparseable or factless body: quarantine a digest, ACK so YooKassa
			// does not retry garbage forever (mirrors the crypto parse path).
			// The reason distinguishes the two cases: an envelope that parsed
			// cleanly but carried no payment id is not a parse failure.
			reason := "webhook_parse_failure"
			if err == nil {
				reason = "webhook_missing_payment_id"
			}
			digest := sha256.Sum256(body)
			recordErr := b.order.RecordPaymentAnomaly(r.Context(), storage.PaymentAnomaly{
				Provider: storage.PaymentMethodYooKassa, RawPayload: fmt.Sprintf("sha256:%x", digest),
				Reason: reason,
			})
			if err == nil && notification != nil && notification.PaymentID == "" {
				w.WriteHeader(http.StatusOK) // valid envelope, no payment id: nothing to do
				return
			}
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("yookassa webhook: malformed body was not quarantined", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if notification.Event != "payment.succeeded" {
			// payment.canceled / refund.* / etc: acknowledge, no auto-settlement.
			w.WriteHeader(http.StatusOK)
			return
		}

		ctx := r.Context()
		// Authoritative refetch — the unsigned body is never trusted for money.
		p, err := b.yookassa.GetPayment(ctx, notification.PaymentID)
		if err != nil {
			b.logger.Error("yookassa webhook: refetch payment", "payment_id", notification.PaymentID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError) // retryable by YooKassa
			return
		}
		if !p.Paid || p.Status != "succeeded" {
			b.logger.Info("yookassa webhook: refetched payment is not terminal",
				"payment_id", p.ID, "status", p.Status)
			w.WriteHeader(http.StatusOK)
			return
		}
		receipt, receiptErr := p.PaymentReceipt()
		if receiptErr != nil {
			anomaly, _ := p.PaymentAnomaly("webhook_invalid_receipt")
			recordErr := b.order.RecordPaymentAnomaly(ctx, anomaly)
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("yookassa webhook: invalid receipt was not quarantined", "payment_id", p.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		outcome, err := b.order.ConfirmPaymentReceipt(ctx, receipt)
		if err != nil {
			// Same idempotent-ACK error classes as the crypto webhook:
			if errors.Is(err, storage.ErrOrderStatusConflict) || errors.Is(err, storage.ErrNotFound) ||
				errors.Is(err, storage.ErrPaymentNeedsReview) || errors.Is(err, storage.ErrPaymentIdentityConflict) ||
				errors.Is(err, storage.ErrPaymentReceiptMismatch) {
				b.logger.Info("yookassa webhook ignored (idempotent)", "payment_id", p.ID, "reason", err)
				w.WriteHeader(http.StatusOK)
				return
			}
			if errors.Is(err, storage.ErrProductOutOfStock) {
				anomaly, _ := p.PaymentAnomaly("out_of_stock_after_charge")
				if recordErr := b.order.RecordPaymentAnomaly(ctx, anomaly); recordErr == nil ||
					errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			b.logger.Error("yookassa webhook: confirm payment", "payment_id", p.ID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if b.metrics != nil {
			b.metrics.SuccessfulPayments.WithLabelValues("yookassa").Inc()
		}
		order := outcome.Order
		lang := b.userLang(ctx, order.UserID)
		b.send(tgbotapi.NewMessage(order.UserID, fmt.Sprintf(b.t(lang, "payment_success"), order.ID)))
		b.NotifyPaymentOutcome(ctx, outcome)
		b.notifyAdmins(ctx, AdminEventOrderPaid, fmt.Sprintf(b.t("en", "admin_order_paid_yookassa"),
			order.ID, order.UserID, order.TotalRUB))
		b.outWebhook.Send(service.OutboundWebhookEvent{
			Event: "order.paid", OrderID: order.ID, UserID: order.UserID,
			TotalUSD: order.TotalUSD, TotalStars: order.TotalStars,
			Method: "yookassa", PaymentID: p.ID,
		})
		w.WriteHeader(http.StatusOK)
	}
}

// StripeWebhookHandler processes Stripe event notifications. Stripe signs
// every webhook with the endpoint secret: once the signature verifies, the
// body itself is authoritative and settles the order WITHOUT any API refetch
// (the CryptoBot pattern, unlike the unsigned YooKassa flow). An invalid
// signature is unauthenticated junk: rejected with 403, recorded nowhere.
func (b *Bot) StripeWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if b.stripe == nil || !b.stripe.Configured() {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
		body, err := io.ReadAll(r.Body)
		if err != nil {
			b.logger.Error("stripe webhook: read body", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		if err := b.stripe.VerifyWebhookSignature(r.Header.Get("Stripe-Signature"), body); err != nil {
			b.logger.Error("stripe webhook: invalid signature", "error", err)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		ctx := r.Context()
		eventType, session, err := b.stripe.ParseWebhook(body)
		if err != nil {
			// The signature was valid, so a body that still fails to parse is
			// a real anomaly: quarantine a digest, ACK once it is durable.
			digest := sha256.Sum256(body)
			recordErr := b.order.RecordPaymentAnomaly(ctx, storage.PaymentAnomaly{
				Provider: storage.PaymentMethodStripe, RawPayload: fmt.Sprintf("sha256:%x", digest), Reason: "webhook_parse_failure",
			})
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("stripe webhook: signed payload was not quarantined", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if eventType != "checkout.session.completed" || session == nil {
			// Other lifecycle events and id-less envelopes: acknowledge, settle nothing.
			w.WriteHeader(http.StatusOK)
			return
		}

		receipt, receiptErr := session.PaymentReceipt()
		if receiptErr != nil {
			anomaly, _ := session.PaymentAnomaly("webhook_invalid_receipt")
			recordErr := b.order.RecordPaymentAnomaly(ctx, anomaly)
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("stripe webhook: invalid receipt was not quarantined", "session_id", session.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		outcome, err := b.order.ConfirmPaymentReceipt(ctx, receipt)
		if err != nil {
			// Same idempotent-ACK error classes as the yookassa webhook:
			if errors.Is(err, storage.ErrOrderStatusConflict) || errors.Is(err, storage.ErrNotFound) ||
				errors.Is(err, storage.ErrPaymentNeedsReview) || errors.Is(err, storage.ErrPaymentIdentityConflict) ||
				errors.Is(err, storage.ErrPaymentReceiptMismatch) {
				b.logger.Info("stripe webhook ignored (idempotent)", "session_id", session.ID, "reason", err)
				w.WriteHeader(http.StatusOK)
				return
			}
			if errors.Is(err, storage.ErrProductOutOfStock) {
				anomaly, _ := session.PaymentAnomaly("out_of_stock_after_charge")
				if recordErr := b.order.RecordPaymentAnomaly(ctx, anomaly); recordErr == nil ||
					errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			b.logger.Error("stripe webhook: confirm payment", "session_id", session.ID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if b.metrics != nil {
			b.metrics.SuccessfulPayments.WithLabelValues("stripe").Inc()
		}
		order := outcome.Order
		lang := b.userLang(ctx, order.UserID)
		b.send(tgbotapi.NewMessage(order.UserID, fmt.Sprintf(b.t(lang, "payment_success"), order.ID)))
		b.NotifyPaymentOutcome(ctx, outcome)
		b.notifyAdmins(ctx, AdminEventOrderPaid, fmt.Sprintf(b.t("en", "admin_order_paid_stripe"),
			order.ID, order.UserID, order.TotalUSD))
		b.outWebhook.Send(service.OutboundWebhookEvent{
			Event: "order.paid", OrderID: order.ID, UserID: order.UserID,
			TotalUSD: order.TotalUSD, TotalStars: order.TotalStars,
			Method: "stripe", PaymentID: session.ID,
		})
		w.WriteHeader(http.StatusOK)
	}
}

// NowpaymentsWebhookHandler processes NOWPayments IPN callbacks. NOWPayments
// signs every IPN with the endpoint's HMAC-SHA512 secret over the
// canonicalized body: once the signature verifies, the body itself is
// authoritative and settles the order WITHOUT any API refetch (the
// CryptoBot/Stripe pattern, unlike the unsigned YooKassa flow). An invalid
// signature is unauthenticated junk: rejected with 403, recorded nowhere.
func (b *Bot) NowpaymentsWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if b.nowpayments == nil || !b.nowpayments.Configured() {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
		body, err := io.ReadAll(r.Body)
		if err != nil {
			b.logger.Error("nowpayments webhook: read body", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		if err := b.nowpayments.VerifyIPNSignature(r.Header.Get("x-nowpayments-sig"), body); err != nil {
			b.logger.Error("nowpayments webhook: invalid signature", "error", err)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		ctx := r.Context()
		ipn, err := b.nowpayments.ParseIPN(body)
		if err != nil {
			// The signature was valid, so a body that still fails to parse is
			// a real anomaly: quarantine a digest, ACK once it is durable.
			digest := sha256.Sum256(body)
			recordErr := b.order.RecordPaymentAnomaly(ctx, storage.PaymentAnomaly{
				Provider: storage.PaymentMethodNowpayments, RawPayload: fmt.Sprintf("sha256:%x", digest), Reason: "webhook_parse_failure",
			})
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("nowpayments webhook: signed payload was not quarantined", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if ipn.PaymentStatus != "finished" {
			// waiting/confirming/partially_paid/failed/expired/...: normal
			// lifecycle noise — acknowledge, settle nothing, record nothing.
			w.WriteHeader(http.StatusOK)
			return
		}

		receipt, receiptErr := ipn.PaymentReceipt()
		if receiptErr != nil {
			anomaly, _ := ipn.PaymentAnomaly("webhook_invalid_receipt")
			recordErr := b.order.RecordPaymentAnomaly(ctx, anomaly)
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("nowpayments webhook: invalid receipt was not quarantined", "payment_id", ipn.PaymentID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		outcome, err := b.order.ConfirmPaymentReceipt(ctx, receipt)
		if err != nil {
			// Same idempotent-ACK error classes as the stripe webhook:
			if errors.Is(err, storage.ErrOrderStatusConflict) || errors.Is(err, storage.ErrNotFound) ||
				errors.Is(err, storage.ErrPaymentNeedsReview) || errors.Is(err, storage.ErrPaymentIdentityConflict) ||
				errors.Is(err, storage.ErrPaymentReceiptMismatch) {
				b.logger.Info("nowpayments webhook ignored (idempotent)", "payment_id", ipn.PaymentID, "reason", err)
				w.WriteHeader(http.StatusOK)
				return
			}
			if errors.Is(err, storage.ErrProductOutOfStock) {
				anomaly, _ := ipn.PaymentAnomaly("out_of_stock_after_charge")
				if recordErr := b.order.RecordPaymentAnomaly(ctx, anomaly); recordErr == nil ||
					errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			b.logger.Error("nowpayments webhook: confirm payment", "payment_id", ipn.PaymentID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if b.metrics != nil {
			b.metrics.SuccessfulPayments.WithLabelValues("nowpayments").Inc()
		}
		order := outcome.Order
		lang := b.userLang(ctx, order.UserID)
		b.send(tgbotapi.NewMessage(order.UserID, fmt.Sprintf(b.t(lang, "payment_success"), order.ID)))
		b.NotifyPaymentOutcome(ctx, outcome)
		b.notifyAdmins(ctx, AdminEventOrderPaid, fmt.Sprintf(b.t("en", "admin_order_paid_nowpayments"),
			order.ID, order.UserID, order.TotalUSD))
		b.outWebhook.Send(service.OutboundWebhookEvent{
			Event: "order.paid", OrderID: order.ID, UserID: order.UserID,
			TotalUSD: order.TotalUSD, TotalStars: order.TotalStars,
			Method: "nowpayments", PaymentID: ipn.PaymentID,
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
