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

// orderSubscriptionProduct reads only the immutable checkout snapshot. A
// later catalog edit must never turn an already-created regular order into a
// recurring invoice (or strip recurring terms from a subscription order).
func (b *Bot) orderSubscriptionProduct(ctx context.Context, order *storage.Order) (productID int64, periodDays int, err error) {
	_ = ctx
	if order == nil {
		return 0, 0, nil
	}
	if order.SubscriptionProductID > 0 && order.SubscriptionPeriodDays > 0 {
		return order.SubscriptionProductID, order.SubscriptionPeriodDays, nil
	}
	return 0, 0, nil
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
				ChargeID         string `json:"telegram_payment_charge_id"`
				ExpiresAt        int64  `json:"subscription_expiration_date"`
				IsRecurring      bool   `json:"is_recurring"`
				IsFirstRecurring bool   `json:"is_first_recurring"`
			} `json:"successful_payment"`
		} `json:"message"`
	}
	if err := json.Unmarshal(rawUpdate, &probe); err != nil {
		return noop
	}
	sp := probe.Message.SuccessfulPayment
	if sp == nil || sp.ChargeID == "" {
		return noop
	}
	expiresAt := time.Time{}
	if sp.ExpiresAt > 0 {
		expiresAt = time.Unix(sp.ExpiresAt, 0)
	}
	renewal := sp.IsRecurring && !sp.IsFirstRecurring
	b.pendingSubSignalsMu.Lock()
	if b.pendingSubSignals == nil {
		b.pendingSubSignals = make(map[string]pendingSubscriptionSignal)
	}
	signal := b.pendingSubSignals[sp.ChargeID]
	if expiresAt.After(signal.expiresAt) {
		signal.expiresAt = expiresAt
	}
	signal.renewal = signal.renewal || renewal
	signal.refs++
	b.pendingSubSignals[sp.ChargeID] = signal
	b.pendingSubSignalsMu.Unlock()
	return func() {
		b.pendingSubSignalsMu.Lock()
		current := b.pendingSubSignals[sp.ChargeID]
		current.refs--
		if current.refs <= 0 {
			delete(b.pendingSubSignals, sp.ChargeID)
		} else {
			b.pendingSubSignals[sp.ChargeID] = current
		}
		b.pendingSubSignalsMu.Unlock()
	}
}

// decodeTelegramUpdate preserves subscription fields that tgbotapi v5 does
// not model. Webhook and polling both pass the exact provider JSON through
// this boundary before the SDK-compatible update is handled.
func (b *Bot) decodeTelegramUpdate(raw []byte) (tgbotapi.Update, func(), error) {
	var update tgbotapi.Update
	if err := json.Unmarshal(raw, &update); err != nil {
		return update, func() {}, err
	}
	return update, b.stashSubscriptionExpiry(raw), nil
}

func (b *Bot) isPendingSubscriptionRenewal(chargeID string) bool {
	b.pendingSubSignalsMu.Lock()
	defer b.pendingSubSignalsMu.Unlock()
	return b.pendingSubSignals[chargeID].renewal
}

// takePendingSubExpiry reads the stashed expiry. It remains available to every
// concurrent duplicate delivery until all reference-counted cleanups run.
func (b *Bot) takePendingSubExpiry(chargeID string) (time.Time, bool) {
	if chargeID == "" {
		return time.Time{}, false
	}
	b.pendingSubSignalsMu.Lock()
	defer b.pendingSubSignalsMu.Unlock()
	signal, ok := b.pendingSubSignals[chargeID]
	if !ok || signal.expiresAt.IsZero() {
		return time.Time{}, false
	}
	return signal.expiresAt, true
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
