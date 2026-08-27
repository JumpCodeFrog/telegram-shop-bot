package bot

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/payment"
	"shop_bot/internal/service"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

func (b *Bot) onPayStars(cbID string, chatID, userID int64, msgID int, data, lang string) {
	orderID, err := parseIDFromCallback(data, "pay:stars:")
	if err != nil {
		b.logger.Error("parse pay:stars callback", "error", err)
		b.ack(cbID)
		return
	}

	ctx := context.Background()
	target, err := b.loadPayableOrder(ctx, userID, orderID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			b.alert(cbID, b.t(lang, "order_not_found"))
			return
		}
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			b.alert(cbID, b.t(lang, "order_already_paid"))
			return
		}
		b.logger.Error("load payable order for stars payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}
	// Subscription products need a recurring invoice (subscription_period).
	_, subDays, err := b.orderSubscriptionProduct(ctx, target)
	if err != nil {
		b.logger.Error("detect subscription product for stars payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	b.ack(cbID)
	if err := b.stars.SendInvoice(chatID, orderID, target.TotalStars, target.Items, payment.SubscriptionPeriodSeconds(subDays)); err != nil {
		b.logger.Error("send stars invoice", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "payment_error")))
		return
	}
}

func (b *Bot) onOrderCancel(cbID string, chatID, userID int64, msgID int, data, lang string) {
	orderID, err := parseIDFromCallback(data, "order:cancel:")
	if err != nil {
		b.logger.Error("parse order:cancel callback", "error", err)
		b.ack(cbID)
		return
	}

	ctx := context.Background()
	if _, err := b.loadPayableOrder(ctx, userID, orderID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			b.alert(cbID, b.t(lang, "order_not_found"))
			return
		}
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			b.alert(cbID, b.t(lang, "order_already_paid"))
			return
		}
		b.logger.Error("load payable order for cancel", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	if err := b.order.CancelOrder(ctx, orderID, userID); err != nil {
		b.logger.Error("cancel order", "order_id", orderID, "user_id", userID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	b.ack(cbID)
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_catalog"), "back:catalog"),
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_orders"), "back:orders"),
		),
	)

	text := fmt.Sprintf(b.t(lang, "order_cancelled"), orderID)
	if msgID > 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ParseMode = "HTML"
		edit.ReplyMarkup = &keyboard
		b.send(edit)
		return
	}

	reply := tgbotapi.NewMessage(chatID, text)
	reply.ParseMode = "HTML"
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

func (b *Bot) onPayCrypto(cbID string, chatID, userID int64, msgID int, data, lang string) {
	if !b.cryptoPaymentsEnabled() {
		b.alert(cbID, b.t(lang, "crypto_unavailable"))
		return
	}

	orderID, err := parseIDFromCallback(data, "pay:crypto:")
	if err != nil {
		b.logger.Error("parse pay:crypto callback", "error", err)
		b.ack(cbID)
		return
	}

	ctx := context.Background()
	target, err := b.loadPayableOrder(ctx, userID, orderID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			b.alert(cbID, b.t(lang, "order_not_found"))
			return
		}
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			b.alert(cbID, b.t(lang, "order_already_paid"))
			return
		}
		b.logger.Error("load payable order for crypto payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	// Subscription products are payable with Telegram Stars only.
	if _, subDays, subErr := b.orderSubscriptionProduct(ctx, target); subErr != nil {
		b.logger.Error("detect subscription product for crypto payment", "order_id", orderID, "error", subErr)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	} else if subDays > 0 {
		b.alert(cbID, b.t(lang, "sub_stars_only"))
		return
	}

	// Show skeleton state while generating the invoice.
	skeletonKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_generating_invoice"), "noop"),
		),
	)
	editSkeleton := tgbotapi.NewEditMessageReplyMarkup(chatID, msgID, skeletonKeyboard)
	b.send(editSkeleton)
	b.ack(cbID)

	desc := fmt.Sprintf(b.t(lang, "crypto_invoice_desc"), orderID)
	invoice, err := b.crypto.CreateInvoice(ctx, orderID, target.TotalUSD, desc)
	if err != nil {
		b.logger.Error("create crypto invoice", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "payment_error")))
		return
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(b.t(lang, "btn_pay_usdt"), invoice.PayURL),
		),
	)

	text := fmt.Sprintf(b.t(lang, "crypto_pay_title"), orderID, target.TotalUSD)
	reply := tgbotapi.NewMessage(chatID, text)
	reply.ParseMode = "HTML"
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

// --- Payment handlers ---

// handlePreCheckout handles Telegram PreCheckoutQuery for Stars payments.
func (b *Bot) handlePreCheckout(query *tgbotapi.PreCheckoutQuery) {
	ctx, cancel := handlerCtx()
	defer cancel()
	if err := b.stars.HandlePreCheckout(ctx, query); err != nil {
		b.logger.Error("handle pre-checkout", "error", err)
	}
}

// handleSuccessfulPayment is the router-compatible wrapper. Provider ingress
// calls processSuccessfulPayment directly so it can withhold its ACK when no
// durable settlement or review fact could be written.
func (b *Bot) handleSuccessfulPayment(msg *tgbotapi.Message) {
	if err := b.processSuccessfulPayment(msg); err != nil {
		b.logger.Error("Stars payment was not durably handled", "error", err)
	}
}

// processSuccessfulPayment handles a successful Stars payment. A nil result
// means the charge was either settled, recognized as an exact replay, or
// durably quarantined for operator review. Any non-nil result is retryable by
// the Telegram webhook/polling ingress and must not be acknowledged there.
func (b *Bot) processSuccessfulPayment(msg *tgbotapi.Message) error {
	if msg == nil {
		return nil
	}
	sp := msg.SuccessfulPayment
	if sp == nil {
		return nil
	}

	orderID, err := strconv.ParseInt(sp.InvoicePayload, 10, 64)
	if err != nil || orderID <= 0 {
		ctx, cancel := handlerCtx()
		defer cancel()
		if quarantineErr := b.recordStarsPaymentAnomaly(ctx, msg, 0, "stars_invalid_order_payload"); quarantineErr != nil {
			return fmt.Errorf("parse Stars order ID and quarantine provider fact: %w", quarantineErr)
		}
		b.logger.Warn("Stars payment quarantined: invalid order payload")
		return nil
	}

	ctx, cancel := handlerCtx()
	defer cancel()
	payerID := int64(0)
	if msg.From != nil {
		payerID = msg.From.ID
	}
	receipt := shop.PaymentReceipt{
		OrderID: orderID, Provider: storage.PaymentMethodStars,
		ExternalID: sp.TelegramPaymentChargeID, PayerID: payerID,
		Currency: sp.Currency, AmountMinor: int64(sp.TotalAmount), Scale: 0,
	}
	if msg.Date > 0 {
		receipt.OccurredAt = time.Unix(int64(msg.Date), 0).UTC()
	}
	if expiresAt, ok := b.takePendingSubExpiry(sp.TelegramPaymentChargeID); ok {
		receipt.SubscriptionExpiresAt = expiresAt
	}
	if b.isPendingSubscriptionRenewal(sp.TelegramPaymentChargeID) {
		_, renewalErr := b.order.RecordSubscriptionRenewal(ctx, receipt)
		if renewalErr != nil {
			if errors.Is(renewalErr, storage.ErrPaymentNeedsReview) ||
				errors.Is(renewalErr, storage.ErrPaymentReceiptMismatch) ||
				errors.Is(renewalErr, storage.ErrPaymentIdentityConflict) {
				b.logger.Warn("Stars subscription renewal quarantined", "order_id", orderID, "reason", renewalErr)
				return nil
			}
			if quarantineErr := b.recordStarsPaymentAnomaly(ctx, msg, orderID, "stars_subscription_renewal_failure"); quarantineErr != nil {
				return errors.Join(renewalErr, quarantineErr)
			}
			b.logger.Warn("Stars subscription renewal quarantined", "order_id", orderID, "reason", renewalErr)
			return nil
		}
		if b.metrics != nil {
			b.metrics.SuccessfulPayments.WithLabelValues("stars").Inc()
		}
		return nil
	}
	outcome, err := b.order.ConfirmPaymentReceipt(ctx, receipt)
	if err != nil {
		if errors.Is(err, storage.ErrProductOutOfStock) {
			recordErr := b.order.RecordUnexpectedPayment(ctx, receipt, "out_of_stock_after_charge")
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				b.logger.Warn("Stars payment quarantined after stock conflict", "order_id", orderID)
				return nil
			}
		}
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			// Duplicate Stars payment event — already confirmed, safe to ignore.
			b.logger.Info("stars payment already confirmed (idempotent)", "order_id", orderID)
			return nil
		}
		if errors.Is(err, storage.ErrPaymentNeedsReview) ||
			errors.Is(err, storage.ErrPaymentReceiptMismatch) ||
			errors.Is(err, storage.ErrPaymentIdentityConflict) ||
			errors.Is(err, storage.ErrNotFound) {
			b.logger.Warn("Stars payment durably quarantined", "order_id", orderID, "reason", err)
			return nil
		}
		// ConfirmPaymentReceipt durably quarantines known validation failures.
		// Re-recording the normalized fact is intentional: it proves the ACK
		// boundary even if a future domain path returns a new error before doing
		// so. Exact anomaly retries are idempotent.
		if quarantineErr := b.recordStarsPaymentAnomaly(ctx, msg, orderID, "stars_payment_processing_failure"); quarantineErr != nil {
			return errors.Join(err, quarantineErr)
		}
		b.logger.Warn("Stars payment quarantined", "order_id", orderID, "reason", err)
		return nil
	}

	if b.metrics != nil {
		b.metrics.SuccessfulPayments.WithLabelValues("stars").Inc()
	}

	b.outWebhook.Send(service.OutboundWebhookEvent{
		Event:      "order.paid",
		OrderID:    orderID,
		UserID:     payerID,
		TotalUSD:   outcome.Order.TotalUSD,
		TotalStars: outcome.Order.TotalStars,
		Method:     "stars",
		PaymentID:  sp.TelegramPaymentChargeID,
	})

	lang := ""
	if msg.From != nil {
		lang = msg.From.LanguageCode
	}

	b.notifyAdmins(ctx, AdminEventOrderPaid, fmt.Sprintf(b.t("en", "admin_order_paid_stars"), orderID, payerID))

	receiptText := fmt.Sprintf(b.t(lang, "stars_receipt"),
		orderID,
		sp.TotalAmount,
		time.Now().Format("02.01.2006"),
	)
	if msg.Chat != nil {
		reply := tgbotapi.NewMessage(msg.Chat.ID, receiptText)
		reply.ParseMode = "HTML"
		b.send(reply)
	}

	b.NotifyPaymentOutcome(ctx, outcome)
	return nil
}

func (b *Bot) recordStarsPaymentAnomaly(ctx context.Context, msg *tgbotapi.Message, orderID int64, reason string) error {
	if msg == nil || msg.SuccessfulPayment == nil {
		return fmt.Errorf("Stars payment anomaly is missing provider fields")
	}
	sp := msg.SuccessfulPayment
	payerID := int64(0)
	if msg.From != nil {
		payerID = msg.From.ID
	}
	occurredAt := time.Time{}
	if msg.Date > 0 {
		occurredAt = time.Unix(int64(msg.Date), 0).UTC()
	}
	amountMinor := int64(sp.TotalAmount)
	if amountMinor < 0 {
		amountMinor = 0
	}
	payloadDigest := sha256.Sum256([]byte(sp.InvoicePayload))
	err := b.order.RecordPaymentAnomaly(ctx, storage.PaymentAnomaly{
		ProposedOrderID: orderID,
		Provider:        storage.PaymentMethodStars,
		EventKind:       storage.PaymentEventCaptured,
		ExternalID:      sp.TelegramPaymentChargeID,
		PayerID:         payerID,
		AmountMinor:     amountMinor,
		Currency:        sp.Currency,
		Scale:           0,
		RawAmount:       strconv.Itoa(sp.TotalAmount),
		RawPayload:      fmt.Sprintf("invoice_payload_sha256:%x", payloadDigest),
		Reason:          reason,
		OccurredAt:      occurredAt,
	})
	if err == nil || errors.Is(err, storage.ErrPaymentNeedsReview) {
		return nil
	}
	return fmt.Errorf("persist Stars payment review fact: %w", err)
}

// NotifyPaymentOutcome sends the user-facing messages for the side effects of
// a confirmed payment: cashback points, level upgrades, the referral bonus for
// the referrer, and the welcome promo for the referred buyer. All sends are
// best-effort; the payment itself is already final.
func (b *Bot) NotifyPaymentOutcome(ctx context.Context, outcome *shop.PaymentOutcome) {
	if outcome == nil || outcome.Order == nil {
		return
	}

	buyerLang := b.userLang(ctx, outcome.Order.UserID)

	if outcome.PointsAwarded > 0 {
		msg := tgbotapi.NewMessage(outcome.Order.UserID,
			fmt.Sprintf(b.t(buyerLang, "loyalty_points_awarded"), outcome.PointsAwarded))
		msg.ParseMode = "HTML"
		b.send(msg)
	}
	if outcome.NewLevel != "" {
		msg := tgbotapi.NewMessage(outcome.Order.UserID,
			fmt.Sprintf(b.t(buyerLang, "loyalty_level_up"), outcome.NewLevel))
		msg.ParseMode = "HTML"
		b.send(msg)
	}
	if outcome.NewUserPromo != "" {
		msg := tgbotapi.NewMessage(outcome.Order.UserID,
			fmt.Sprintf(b.t(buyerLang, "referral_welcome_promo"), outcome.NewUserPromo))
		msg.ParseMode = "HTML"
		b.send(msg)
	}
	if outcome.ReferralReferrer != 0 {
		refLang := b.userLang(ctx, outcome.ReferralReferrer)
		msg := tgbotapi.NewMessage(outcome.ReferralReferrer,
			fmt.Sprintf(b.t(refLang, "referral_bonus_referrer"), outcome.ReferrerPoints))
		msg.ParseMode = "HTML"
		b.send(msg)
	}
}

// userLang resolves a user's stored language by Telegram ID, falling back to "" (→ en).
func (b *Bot) userLang(ctx context.Context, telegramID int64) string {
	user, err := b.users.GetByTelegramID(ctx, telegramID)
	if err != nil || user == nil {
		return ""
	}
	return user.LanguageCode
}
