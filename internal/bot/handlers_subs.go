package bot

// Stars recurring subscriptions: /mysubs screen, sub:cancel:<id> callback,
// subscription bookkeeping after a successful Stars payment, and the
// "expiring soon" notification used by the subscription worker.
//
// tgbotapi v5 predates Bot API 7.x subscription support, so the two gaps are
// covered with raw JSON: subscription_expiration_date is extracted from the
// raw webhook update body, and cancellation goes through a raw
// editUserStarSubscription call.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

// cartHasSubscription reports whether any cart item is a subscription product.
// Such carts are payable with Telegram Stars only.
func cartHasSubscription(view *shop.CartView) bool {
	if view == nil {
		return false
	}
	for _, item := range view.Items {
		if item.Product.SubPeriodDays > 0 {
			return true
		}
	}
	return false
}

// orderSubscriptionProduct returns the first subscription product of an order
// (product ID and period in days), or (0, 0) for a regular order. Products
// deleted since the order was placed are skipped.
func (b *Bot) orderSubscriptionProduct(ctx context.Context, order *storage.Order) (productID int64, periodDays int, err error) {
	if order == nil {
		return 0, 0, nil
	}
	for _, item := range order.Items {
		p, err := b.products.GetProduct(ctx, item.ProductID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			return 0, 0, fmt.Errorf("load product %d: %w", item.ProductID, err)
		}
		if p != nil && p.SubPeriodDays > 0 {
			return p.ID, p.SubPeriodDays, nil
		}
	}
	return 0, 0, nil
}

// recordSubscription upserts the subscription row for a paid subscription
// order. Expiry comes from the raw update's subscription_expiration_date when
// the webhook stashed one; otherwise now + the product's period (30 days).
func (b *Bot) recordSubscription(ctx context.Context, order *storage.Order, sp *tgbotapi.SuccessfulPayment) {
	if b.subs == nil || order == nil || sp == nil {
		return
	}
	productID, days, err := b.orderSubscriptionProduct(ctx, order)
	if err != nil {
		b.logger.Error("subscription: detect subscription product", "order_id", order.ID, "error", err)
		return
	}
	if days <= 0 {
		return
	}

	expiresAt, ok := b.takePendingSubExpiry(sp.TelegramPaymentChargeID)
	if !ok {
		expiresAt = time.Now().Add(time.Duration(days) * 24 * time.Hour)
	}

	sub := storage.Subscription{
		UserID:    order.UserID,
		ProductID: productID,
		OrderID:   order.ID,
		ChargeID:  sp.TelegramPaymentChargeID,
		Status:    storage.SubStatusActive,
		ExpiresAt: expiresAt,
	}
	if err := b.subs.Upsert(ctx, sub); err != nil {
		b.logger.Error("subscription: upsert", "order_id", order.ID, "product_id", productID, "error", err)
		return
	}
	b.logger.Info("subscription recorded", "order_id", order.ID, "product_id", productID, "expires_at", expiresAt)
}

// stashSubscriptionExpiry extracts successful_payment.subscription_expiration_date
// from a raw update JSON (tgbotapi v5 does not parse Bot API subscription
// fields) and stashes it keyed by charge ID for handleSuccessfulPayment.
// The returned cleanup removes the entry after the update is handled.
func (b *Bot) stashSubscriptionExpiry(rawUpdate []byte) func() {
	noop := func() {}
	var probe struct {
		Message struct {
			SuccessfulPayment *struct {
				ChargeID  string `json:"telegram_payment_charge_id"`
				ExpiresAt int64  `json:"subscription_expiration_date"`
			} `json:"successful_payment"`
		} `json:"message"`
	}
	if err := json.Unmarshal(rawUpdate, &probe); err != nil {
		return noop
	}
	sp := probe.Message.SuccessfulPayment
	if sp == nil || sp.ChargeID == "" || sp.ExpiresAt <= 0 {
		return noop
	}
	b.pendingSubExpiry.Store(sp.ChargeID, time.Unix(sp.ExpiresAt, 0))
	chargeID := sp.ChargeID
	return func() { b.pendingSubExpiry.Delete(chargeID) }
}

// takePendingSubExpiry pops the stashed subscription expiry for a charge ID.
func (b *Bot) takePendingSubExpiry(chargeID string) (time.Time, bool) {
	if chargeID == "" {
		return time.Time{}, false
	}
	v, ok := b.pendingSubExpiry.LoadAndDelete(chargeID)
	if !ok {
		return time.Time{}, false
	}
	t, ok := v.(time.Time)
	return t, ok
}

// handleMySubs handles the /mysubs command.
func (b *Bot) handleMySubs(msg *tgbotapi.Message) {
	b.sendMySubs(msg.Chat.ID, msg.From.ID, 0, msg.From.LanguageCode)
}

// sendMySubs renders the active-subscriptions screen with a cancel button per
// subscription.
func (b *Bot) sendMySubs(chatID, userID int64, msgID int, lang string) {
	ctx, cancel := handlerCtx()
	defer cancel()

	subs, err := b.subs.ListActiveByUser(ctx, userID)
	if err != nil {
		b.logger.Error("list subscriptions", "user_id", userID, "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "error_short"), "", nil)
		return
	}

	if len(subs) == 0 {
		kb := StyledKeyboard{
			{Btn(b.t(lang, "btn_catalog"), "back:catalog"), Btn(b.t(lang, "btn_menu"), "back:menu")},
		}
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "sub_none"), "HTML", kb)
		return
	}

	var sb strings.Builder
	sb.WriteString(b.t(lang, "sub_list_title"))
	kb := StyledKeyboard{}
	for _, sub := range subs {
		name := b.subscriptionProductName(ctx, sub.ProductID)
		sb.WriteString(fmt.Sprintf(b.t(lang, "sub_list_item"), name, sub.ExpiresAt.Format("02.01.2006")))
		kb = append(kb, []StyledButton{
			BtnDanger(fmt.Sprintf(b.t(lang, "sub_btn_cancel"), name), fmt.Sprintf("sub:cancel:%d", sub.ID)),
		})
	}
	kb = append(kb, []StyledButton{Btn(b.t(lang, "btn_menu"), "back:menu")})

	b.sendOrEditStyled(chatID, msgID, sb.String(), "HTML", kb)
}

// onSubCancel handles the sub:cancel:<id> callback: it cancels the recurring
// Stars subscription on the Telegram side, then marks it canceled locally.
func (b *Bot) onSubCancel(cbID string, chatID, userID int64, msgID int, data, lang string) {
	subID, err := parseIDFromCallback(data, "sub:cancel:")
	if err != nil {
		b.logger.Error("parse sub:cancel callback", "error", err)
		b.ack(cbID)
		return
	}

	ctx, cancel := handlerCtx()
	defer cancel()

	// Resolve the subscription through the user's own active list: this both
	// finds the charge ID and guarantees the caller owns the subscription.
	subs, err := b.subs.ListActiveByUser(ctx, userID)
	if err != nil {
		b.logger.Error("list subscriptions for cancel", "user_id", userID, "error", err)
		b.alert(cbID, b.t(lang, "error_short"))
		return
	}
	var target *storage.Subscription
	for i := range subs {
		if subs[i].ID == subID {
			target = &subs[i]
			break
		}
	}
	if target == nil {
		b.alert(cbID, b.t(lang, "sub_not_found"))
		return
	}

	if err := b.cancelStarSubscription(userID, target.ChargeID); err != nil {
		b.logger.Error("editUserStarSubscription", "subscription_id", subID, "error", err)
		b.alert(cbID, b.t(lang, "sub_cancel_error"))
		return
	}
	if err := b.subs.SetStatusByCharge(ctx, target.ChargeID, storage.SubStatusCanceled); err != nil {
		// Telegram side is already canceled; log and keep going so the user
		// still sees the confirmation.
		b.logger.Error("set subscription status canceled", "subscription_id", subID, "error", err)
	}

	b.toast(cbID, b.t(lang, "sub_canceled"))
	b.sendMySubs(chatID, userID, msgID, lang)
}

// cancelStarSubscription cancels a recurring Stars subscription via the raw
// Bot API — tgbotapi v5 has no editUserStarSubscription method.
func (b *Bot) cancelStarSubscription(userID int64, chargeID string) error {
	_, err := b.api.MakeRequest("editUserStarSubscription", tgbotapi.Params{
		"user_id":                    strconv.FormatInt(userID, 10),
		"telegram_payment_charge_id": chargeID,
		"is_canceled":                "true",
	})
	return err
}

// NotifySubscriptionExpiring sends the one-shot "expiring soon" reminder for a
// subscription. The subscription worker calls it and marks the subscription
// reminded only when the send succeeded, so a failed send is retried next tick.
func (b *Bot) NotifySubscriptionExpiring(ctx context.Context, sub storage.Subscription) error {
	lang := b.userLang(ctx, sub.UserID)
	name := b.subscriptionProductName(ctx, sub.ProductID)

	msg := tgbotapi.NewMessage(sub.UserID,
		fmt.Sprintf(b.t(lang, "sub_expiring_soon"), name, sub.ExpiresAt.Format("02.01.2006")))
	msg.ParseMode = "HTML"
	if _, err := b.api.Send(msg); err != nil {
		return fmt.Errorf("send subscription reminder to %d: %w", sub.UserID, err)
	}
	return nil
}

// subscriptionProductName resolves a product name for subscription screens,
// falling back to "#<id>" when the product is gone.
func (b *Bot) subscriptionProductName(ctx context.Context, productID int64) string {
	if p, err := b.products.GetProduct(ctx, productID); err == nil && p != nil {
		return p.Name
	}
	return fmt.Sprintf("#%d", productID)
}
