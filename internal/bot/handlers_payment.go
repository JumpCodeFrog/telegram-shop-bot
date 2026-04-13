package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/service"
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

	b.ack(cbID)
	if err := b.stars.SendInvoice(chatID, orderID, target.TotalStars, target.Items); err != nil {
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
	if err := b.stars.HandlePreCheckout(query); err != nil {
		b.logger.Error("handle pre-checkout", "error", err)
	}
}

// handleSuccessfulPayment handles a successful Stars payment, updating the order status.
func (b *Bot) handleSuccessfulPayment(msg *tgbotapi.Message) {
	sp := msg.SuccessfulPayment
	if sp == nil {
		return
	}

	orderID, err := strconv.ParseInt(sp.InvoicePayload, 10, 64)
	if err != nil {
		b.logger.Error("parse order ID from successful payment", "error", err)
		return
	}

	ctx := context.Background()
	if err := b.order.ConfirmPayment(ctx, orderID, "stars", sp.TelegramPaymentChargeID); err != nil {
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			// Duplicate Stars payment event — already confirmed, safe to ignore.
			b.logger.Info("stars payment already confirmed (idempotent)", "order_id", orderID)
			return
		}
		b.logger.Error("confirm stars payment", "order_id", orderID, "error", err)
		return
	}

	if b.metrics != nil {
		b.metrics.SuccessfulPayments.WithLabelValues("stars").Inc()
	}

	// Fetch order for outbound webhook payload
	if order, err := b.order.GetOrder(ctx, orderID); err == nil {
		b.outWebhook.Send(service.OutboundWebhookEvent{
			Event:      "order.paid",
			OrderID:    orderID,
			UserID:     msg.From.ID,
			TotalUSD:   order.TotalUSD,
			TotalStars: order.TotalStars,
			Method:     "stars",
			PaymentID:  sp.TelegramPaymentChargeID,
		})
	}

	lang := msg.From.LanguageCode

	b.notifyAdmins(fmt.Sprintf(b.t("ru", "admin_order_paid_stars"), orderID, msg.From.ID))

	receipt := fmt.Sprintf(b.t(lang, "stars_receipt"),
		orderID,
		sp.TotalAmount,
		time.Now().Format("02.01.2006"),
	)
	reply := tgbotapi.NewMessage(msg.Chat.ID, receipt)
	reply.ParseMode = "HTML"
	b.send(reply)
}
