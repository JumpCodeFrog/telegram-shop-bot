package bot

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/payment"
	"shop_bot/internal/service"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

func (b *Bot) onPayStars(ctx context.Context, cbID string, chatID, userID int64, msgID int, data, lang string) {
	orderID, err := parseIDFromCallback(data, "pay:stars:")
	if err != nil {
		b.loggerFor(ctx).Error("parse pay:stars callback", "error", err)
		b.ack(cbID)
		return
	}

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
		b.loggerFor(ctx).Error("load payable order for stars payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}
	// Subscription products need a recurring invoice (subscription_period).
	_, subDays, err := b.orderSubscriptionProduct(ctx, target)
	if err != nil {
		b.loggerFor(ctx).Error("detect subscription product for stars payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	b.ack(cbID)
	if err := b.stars.SendInvoice(chatID, orderID, target.TotalStars, target.Items, payment.SubscriptionPeriodSeconds(subDays)); err != nil {
		b.loggerFor(ctx).Error("send stars invoice", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "payment_error")))
		return
	}
}

func (b *Bot) onOrderCancel(ctx context.Context, cbID string, chatID, userID int64, msgID int, data, lang string) {
	orderID, err := parseIDFromCallback(data, "order:cancel:")
	if err != nil {
		b.loggerFor(ctx).Error("parse order:cancel callback", "error", err)
		b.ack(cbID)
		return
	}

	if _, err := b.loadPayableOrder(ctx, userID, orderID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			b.alert(cbID, b.t(lang, "order_not_found"))
			return
		}
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			b.alert(cbID, b.t(lang, "order_already_paid"))
			return
		}
		b.loggerFor(ctx).Error("load payable order for cancel", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	if err := b.order.CancelOrder(ctx, orderID, userID); err != nil {
		b.loggerFor(ctx).Error("cancel order", "order_id", orderID, "user_id", userID, "error", err)
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

func (b *Bot) onPayCrypto(ctx context.Context, cbID string, chatID, userID int64, msgID int, data, lang string) {
	if !b.cryptoPaymentsEnabled() {
		b.alert(cbID, b.t(lang, "crypto_unavailable"))
		return
	}

	orderID, err := parseIDFromCallback(data, "pay:crypto:")
	if err != nil {
		b.loggerFor(ctx).Error("parse pay:crypto callback", "error", err)
		b.ack(cbID)
		return
	}

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
		b.loggerFor(ctx).Error("load payable order for crypto payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	// Subscription products are payable with Telegram Stars only.
	if _, subDays, subErr := b.orderSubscriptionProduct(ctx, target); subErr != nil {
		b.loggerFor(ctx).Error("detect subscription product for crypto payment", "order_id", orderID, "error", subErr)
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
		b.loggerFor(ctx).Error("create crypto invoice", "error", err)
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

func (b *Bot) onPayYooKassa(ctx context.Context, cbID string, chatID, userID int64, msgID int, data, lang string) {
	if !b.yooKassaPaymentsEnabled() {
		b.alert(cbID, b.t(lang, "yookassa_unavailable"))
		return
	}

	orderID, err := parseIDFromCallback(data, "pay:yookassa:")
	if err != nil {
		b.loggerFor(ctx).Error("parse pay:yookassa callback", "error", err)
		b.ack(cbID)
		return
	}

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
		b.loggerFor(ctx).Error("load payable order for card payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	// Subscription products are payable with Telegram Stars only.
	if _, subDays, subErr := b.orderSubscriptionProduct(ctx, target); subErr != nil {
		b.loggerFor(ctx).Error("detect subscription product for card payment", "order_id", orderID, "error", subErr)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	} else if subDays > 0 {
		b.alert(cbID, b.t(lang, "sub_stars_only"))
		return
	}

	// The RUB snapshot is taken at checkout; an order created while RUB was
	// disabled (or a stale conversion) cannot be charged.
	amountMinor := int64(math.Round(target.TotalRUB * 100))
	if amountMinor <= 0 {
		b.alert(cbID, b.t(lang, "yookassa_unavailable"))
		return
	}

	// Show skeleton state while generating the payment.
	skeletonKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_generating_invoice"), "noop"),
		),
	)
	editSkeleton := tgbotapi.NewEditMessageReplyMarkup(chatID, msgID, skeletonKeyboard)
	b.send(editSkeleton)
	b.ack(cbID)

	desc := fmt.Sprintf(b.t(lang, "yookassa_invoice_desc"), orderID)
	invoice, err := b.yookassa.CreatePayment(ctx, orderID, amountMinor, desc)
	if err != nil {
		b.loggerFor(ctx).Error("create yookassa payment", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "payment_error")))
		return
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(b.t(lang, "btn_pay_rub"), invoice.PayURL),
		),
	)

	text := fmt.Sprintf(b.t(lang, "yookassa_pay_title"), orderID, target.TotalRUB)
	reply := tgbotapi.NewMessage(chatID, text)
	reply.ParseMode = "HTML"
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

func (b *Bot) onPayStripe(ctx context.Context, cbID string, chatID, userID int64, msgID int, data, lang string) {
	if !b.stripePaymentsEnabled() {
		b.alert(cbID, b.t(lang, "stripe_unavailable"))
		return
	}

	orderID, err := parseIDFromCallback(data, "pay:stripe:")
	if err != nil {
		b.loggerFor(ctx).Error("parse pay:stripe callback", "error", err)
		b.ack(cbID)
		return
	}

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
		b.loggerFor(ctx).Error("load payable order for stripe payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	// Subscription products are payable with Telegram Stars only.
	if _, subDays, subErr := b.orderSubscriptionProduct(ctx, target); subErr != nil {
		b.loggerFor(ctx).Error("detect subscription product for stripe payment", "order_id", orderID, "error", subErr)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	} else if subDays > 0 {
		b.alert(cbID, b.t(lang, "sub_stars_only"))
		return
	}

	// Stripe refuses USD card charges below $0.50, so a tiny order total
	// surfaces the same "unavailable" alert as an unconfigured adapter.
	amountCents := int64(math.Round(target.TotalUSD * 100))
	if amountCents < 50 {
		b.alert(cbID, b.t(lang, "stripe_unavailable"))
		return
	}

	// Show skeleton state while generating the checkout session.
	skeletonKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_generating_invoice"), "noop"),
		),
	)
	editSkeleton := tgbotapi.NewEditMessageReplyMarkup(chatID, msgID, skeletonKeyboard)
	b.send(editSkeleton)
	b.ack(cbID)

	invoice, err := b.stripe.CreateCheckoutSession(ctx, orderID, amountCents, b.t(lang, "stripe_invoice_desc"))
	if err != nil {
		b.loggerFor(ctx).Error("create stripe checkout session", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "payment_error")))
		return
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(b.t(lang, "btn_pay_stripe"), invoice.PayURL),
		),
	)

	text := fmt.Sprintf(b.t(lang, "stripe_pay_title"), orderID, target.TotalUSD)
	reply := tgbotapi.NewMessage(chatID, text)
	reply.ParseMode = "HTML"
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

func (b *Bot) onPayTON(ctx context.Context, cbID string, chatID, userID int64, msgID int, data, lang string) {
	if !b.tonPaymentsEnabled() {
		b.alert(cbID, b.t(lang, "ton_unavailable"))
		return
	}

	orderID, err := parseIDFromCallback(data, "pay:ton:")
	if err != nil {
		b.loggerFor(ctx).Error("parse pay:ton callback", "error", err)
		b.ack(cbID)
		return
	}

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
		b.loggerFor(ctx).Error("load payable order for ton payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	// Subscription products are payable with Telegram Stars only.
	if _, subDays, subErr := b.orderSubscriptionProduct(ctx, target); subErr != nil {
		b.loggerFor(ctx).Error("detect subscription product for ton payment", "order_id", orderID, "error", subErr)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	} else if subDays > 0 {
		b.alert(cbID, b.t(lang, "sub_stars_only"))
		return
	}

	// The nanoton snapshot is taken at checkout; an order created while TON
	// was disabled cannot be paid in TON.
	if target.TotalTonNano <= 0 {
		b.alert(cbID, b.t(lang, "ton_unavailable"))
		return
	}

	b.ack(cbID)

	// TON settlement is 100% worker-side: the chain poller watches the wallet
	// and settles each transfer by its order comment. The buyer-facing flow
	// only renders the transfer instructions plus a prefilled ton:// deeplink
	// — there is NO server-side invoice and this handler performs NO provider
	// API calls.
	memo := fmt.Sprintf("order-%d", orderID)
	text := fmt.Sprintf(b.t(lang, "ton_pay_instructions"),
		orderID, formatTON(target.TotalTonNano), b.cfg.TONWalletAddress, memo)
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(b.t(lang, "btn_pay_ton"), b.ton.TransferLink(target.TotalTonNano, orderID)),
		),
	)
	reply := tgbotapi.NewMessage(chatID, text)
	reply.ParseMode = "HTML"
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

func (b *Bot) onPayNowpayments(ctx context.Context, cbID string, chatID, userID int64, msgID int, data, lang string) {
	if !b.nowpaymentsEnabled() {
		b.alert(cbID, b.t(lang, "nowpayments_unavailable"))
		return
	}

	orderID, err := parseIDFromCallback(data, "pay:nowpayments:")
	if err != nil {
		b.loggerFor(ctx).Error("parse pay:nowpayments callback", "error", err)
		b.ack(cbID)
		return
	}

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
		b.loggerFor(ctx).Error("load payable order for nowpayments payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	// Subscription products are payable with Telegram Stars only.
	if _, subDays, subErr := b.orderSubscriptionProduct(ctx, target); subErr != nil {
		b.loggerFor(ctx).Error("detect subscription product for nowpayments payment", "order_id", orderID, "error", subErr)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	} else if subDays > 0 {
		b.alert(cbID, b.t(lang, "sub_stars_only"))
		return
	}

	// NOWPayments' hosted invoice has no documented hard minimum, so unlike
	// Stripe there is no client-side amount guard — the order's USD snapshot
	// is priced directly.
	amountCents := int64(math.Round(target.TotalUSD * 100))

	// Show skeleton state while generating the invoice.
	skeletonKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(b.t(lang, "btn_generating_invoice"), "noop"),
		),
	)
	editSkeleton := tgbotapi.NewEditMessageReplyMarkup(chatID, msgID, skeletonKeyboard)
	b.send(editSkeleton)
	b.ack(cbID)

	invoice, err := b.nowpayments.CreateInvoice(ctx, orderID, amountCents, b.t(lang, "nowpayments_invoice_desc"))
	if err != nil {
		b.loggerFor(ctx).Error("create nowpayments invoice", "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "payment_error")))
		return
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(b.t(lang, "btn_pay_nowpayments"), invoice.PayURL),
		),
	)

	text := fmt.Sprintf(b.t(lang, "nowpayments_pay_title"), orderID, target.TotalUSD)
	reply := tgbotapi.NewMessage(chatID, text)
	reply.ParseMode = "HTML"
	reply.ReplyMarkup = keyboard
	b.send(reply)
}

// onPayBalance settles the order synchronously through the internal balance
// rail: no adapter call, no invoice — ConfirmBalancePayment debits the buyer
// and commits the settlement in one step, then the standard announce surface
// delivers the notifications.
func (b *Bot) onPayBalance(ctx context.Context, cbID string, chatID, userID int64, msgID int, data, lang string) {
	orderID, err := parseIDFromCallback(data, "pay:balance:")
	if err != nil {
		b.loggerFor(ctx).Error("parse pay:balance callback", "error", err)
		b.ack(cbID)
		return
	}

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
		b.loggerFor(ctx).Error("load payable order for balance payment", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}

	// Subscription products are payable with Telegram Stars only.
	if _, subDays, subErr := b.orderSubscriptionProduct(ctx, target); subErr != nil {
		b.loggerFor(ctx).Error("detect subscription product for balance payment", "order_id", orderID, "error", subErr)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	} else if subDays > 0 {
		b.alert(cbID, b.t(lang, "sub_stars_only"))
		return
	}

	outcome, err := b.order.ConfirmBalancePayment(ctx, orderID, userID)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrInsufficientFunds):
			// Report the CURRENT balance; a lookup failure degrades to 0.
			balance, balErr := b.balances.GetBalance(ctx, userID)
			if balErr != nil {
				b.loggerFor(ctx).Warn("load balance for insufficient-funds alert", "user_id", userID, "error", balErr)
			}
			b.alert(cbID, fmt.Sprintf(b.t(lang, "balance_insufficient"), balance))
		case errors.Is(err, storage.ErrOrderStatusConflict):
			b.alert(cbID, b.t(lang, "order_already_paid"))
		case errors.Is(err, shop.ErrBalanceSubscriptionUnsupported):
			b.alert(cbID, b.t(lang, "sub_stars_only"))
		default:
			b.loggerFor(ctx).Error("confirm balance payment", "order_id", orderID, "error", err)
			b.alert(cbID, b.t(lang, "error_short"))
		}
		return
	}

	b.ack(cbID)
	b.AnnouncePaidOutcome(ctx, outcome, storage.PaymentMethodBalance)
}

// formatTON renders an integer nanoton amount (TON minor units, scale 9) as
// a decimal TON string with trailing fractional zeros trimmed:
// 2000000000 → "2", 1500000000 → "1.5", 3896686160 → "3.89668616".
func formatTON(nano int64) string {
	const scale = int64(1_000_000_000)
	whole := nano / scale
	frac := nano % scale
	if frac == 0 {
		return strconv.FormatInt(whole, 10)
	}
	fracStr := strings.TrimRight(fmt.Sprintf("%09d", frac), "0")
	return fmt.Sprintf("%d.%s", whole, fracStr)
}

// --- Payment handlers ---

// handlePreCheckout handles Telegram PreCheckoutQuery for Stars payments.
func (b *Bot) handlePreCheckout(ctx context.Context, query *tgbotapi.PreCheckoutQuery) {
	if err := b.stars.HandlePreCheckout(ctx, query); err != nil {
		b.loggerFor(ctx).Error("handle pre-checkout", "error", err)
	}
}

// handleSuccessfulPayment is the router-compatible wrapper. Provider ingress
// calls processSuccessfulPayment directly so it can withhold its ACK when no
// durable settlement or review fact could be written.
func (b *Bot) handleSuccessfulPayment(ctx context.Context, msg *tgbotapi.Message) {
	if err := b.processSuccessfulPayment(ctx, msg); err != nil {
		b.loggerFor(ctx).Error("Stars payment was not durably handled", "error", err)
	}
}

// processSuccessfulPayment handles a successful Stars payment. A nil result
// means the charge was either settled, recognized as an exact replay, or
// durably quarantined for operator review. Any non-nil result is retryable by
// the Telegram webhook/polling ingress and must not be acknowledged there.
func (b *Bot) processSuccessfulPayment(ctx context.Context, msg *tgbotapi.Message) error {
	if msg == nil {
		return nil
	}
	sp := msg.SuccessfulPayment
	if sp == nil {
		return nil
	}

	orderID, err := strconv.ParseInt(sp.InvoicePayload, 10, 64)
	if err != nil || orderID <= 0 {
		if quarantineErr := b.recordStarsPaymentAnomaly(ctx, msg, 0, "stars_invalid_order_payload"); quarantineErr != nil {
			return fmt.Errorf("parse Stars order ID and quarantine provider fact: %w", quarantineErr)
		}
		b.loggerFor(ctx).Warn("Stars payment quarantined: invalid order payload")
		return nil
	}

	payerID := int64(0)
	if msg.From != nil {
		payerID = msg.From.ID
	}
	// The barrier receipt is transport-shared (polling route and Telegram
	// webhook both land here), so the 4.13/4.15 attribution convention keeps
	// one literal for both: webhook:stars. Renewal and out-of-stock legs flow
	// through this same receipt, inheriting the durable actor.
	receipt := shop.PaymentReceipt{
		OrderID: orderID, Provider: storage.PaymentMethodStars,
		ExternalID: sp.TelegramPaymentChargeID, PayerID: payerID,
		Currency: sp.Currency, AmountMinor: int64(sp.TotalAmount), Scale: 0,
		Actor: "webhook:stars",
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
				b.loggerFor(ctx).Warn("Stars subscription renewal quarantined", "order_id", orderID, "reason", renewalErr)
				return nil
			}
			if quarantineErr := b.recordStarsPaymentAnomaly(ctx, msg, orderID, "stars_subscription_renewal_failure"); quarantineErr != nil {
				return errors.Join(renewalErr, quarantineErr)
			}
			b.loggerFor(ctx).Warn("Stars subscription renewal quarantined", "order_id", orderID, "reason", renewalErr)
			return nil
		}
		if b.metrics != nil {
			b.metrics.SuccessfulPayments.WithLabelValues("stars").Inc()
		}
		// Settlement attribution (docs/payment-operations.md §12): the renewal
		// arrives through the same Telegram successful_payment ingress as the
		// one-time settle — mirrored durably in payment_events.actor (4.15).
		b.loggerFor(ctx).Info("stars subscription renewal settled",
			"order_id", orderID, "payment_id", sp.TelegramPaymentChargeID, "actor", "webhook:stars")
		return nil
	}
	outcome, err := b.order.ConfirmPaymentReceipt(ctx, receipt)
	if err != nil {
		if errors.Is(err, storage.ErrProductOutOfStock) {
			recordErr := b.order.RecordUnexpectedPayment(ctx, receipt, "out_of_stock_after_charge")
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				b.loggerFor(ctx).Warn("Stars payment quarantined after stock conflict", "order_id", orderID)
				return nil
			}
		}
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			// Duplicate Stars payment event — already confirmed, safe to ignore.
			b.loggerFor(ctx).Info("stars payment already confirmed (idempotent)", "order_id", orderID)
			return nil
		}
		if errors.Is(err, storage.ErrPaymentNeedsReview) ||
			errors.Is(err, storage.ErrPaymentReceiptMismatch) ||
			errors.Is(err, storage.ErrPaymentIdentityConflict) ||
			errors.Is(err, storage.ErrNotFound) {
			b.loggerFor(ctx).Warn("Stars payment durably quarantined", "order_id", orderID, "reason", err)
			return nil
		}
		// ConfirmPaymentReceipt durably quarantines known validation failures.
		// Re-recording the normalized fact is intentional: it proves the ACK
		// boundary even if a future domain path returns a new error before doing
		// so. Exact anomaly retries are idempotent.
		if quarantineErr := b.recordStarsPaymentAnomaly(ctx, msg, orderID, "stars_payment_processing_failure"); quarantineErr != nil {
			return errors.Join(err, quarantineErr)
		}
		b.loggerFor(ctx).Warn("Stars payment quarantined", "order_id", orderID, "reason", err)
		return nil
	}

	if b.metrics != nil {
		b.metrics.SuccessfulPayments.WithLabelValues("stars").Inc()
	}
	// Settlement attribution (docs/payment-operations.md §12): the Telegram
	// successful_payment update is the authority for this settle — the
	// log-level actor is mirrored durably in payment_events.actor (4.15).
	b.loggerFor(ctx).Info("stars payment settled",
		"order_id", orderID, "payment_id", sp.TelegramPaymentChargeID, "actor", "webhook:stars")

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
	// Durable actor (4.15): the successful_payment ingress that handed us
	// this message is the transport, mirroring the settle receipt's literal.
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
		Actor:           "webhook:stars",
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

// AnnouncePaidOutcome delivers the full settlement announcement set for a
// worker-path confirmed payment: the success metric, the buyer's
// payment_success message, the loyalty/referral outcome messages
// (NotifyPaymentOutcome), the admin notification and the outbound webhook.
// The polling workers (TON — whose poller is the ONLY settlement path — and
// the CryptoBot poller, a backup to its webhook) wire this as their notify
// callback; the webhook handlers keep their own inline blocks.
func (b *Bot) AnnouncePaidOutcome(ctx context.Context, outcome *shop.PaymentOutcome, provider string) {
	if outcome == nil || outcome.Order == nil {
		return
	}
	order := outcome.Order

	if b.metrics != nil {
		b.metrics.SuccessfulPayments.WithLabelValues(provider).Inc()
	}

	buyerLang := b.userLang(ctx, order.UserID)
	b.send(tgbotapi.NewMessage(order.UserID, fmt.Sprintf(b.t(buyerLang, "payment_success"), order.ID)))

	b.NotifyPaymentOutcome(ctx, outcome)

	var adminText string
	switch provider {
	case storage.PaymentMethodTON:
		adminText = fmt.Sprintf(b.t("en", "admin_order_paid_ton"), order.ID, order.UserID, formatTON(order.TotalTonNano))
	case storage.PaymentMethodYooKassa:
		adminText = fmt.Sprintf(b.t("en", "admin_order_paid_yookassa"), order.ID, order.UserID, order.TotalRUB)
	case storage.PaymentMethodStripe:
		adminText = fmt.Sprintf(b.t("en", "admin_order_paid_stripe"), order.ID, order.UserID, order.TotalUSD)
	case storage.PaymentMethodNowpayments:
		adminText = fmt.Sprintf(b.t("en", "admin_order_paid_nowpayments"), order.ID, order.UserID, order.TotalUSD)
	case storage.PaymentMethodBalance:
		adminText = fmt.Sprintf(b.t("en", "admin_order_paid_balance"), order.ID, order.UserID, order.TotalUSD)
	default: // crypto and anything else
		adminText = fmt.Sprintf(b.t("en", "admin_order_paid_crypto"), order.ID, order.UserID, order.TotalUSD)
	}
	b.notifyAdmins(ctx, AdminEventOrderPaid, adminText)

	b.outWebhook.Send(service.OutboundWebhookEvent{
		Event:      "order.paid",
		OrderID:    order.ID,
		UserID:     order.UserID,
		TotalUSD:   order.TotalUSD,
		TotalStars: order.TotalStars,
		Method:     provider,
		PaymentID:  order.PaymentID,
	})
}

// userLang resolves a user's stored language by Telegram ID, falling back to "" (→ en).
func (b *Bot) userLang(ctx context.Context, telegramID int64) string {
	user, err := b.users.GetByTelegramID(ctx, telegramID)
	if err != nil || user == nil {
		return ""
	}
	return user.LanguageCode
}
