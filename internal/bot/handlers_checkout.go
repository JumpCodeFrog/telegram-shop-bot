package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

// onPromoEnter sets the user into promo-entry mode and asks for the code.
func (b *Bot) onPromoEnter(chatID, userID int64, lang string) {
	fsmCtx, cancel := handlerCtx()
	defer cancel()
	_ = b.fsm.SetPromoState(fsmCtx, userID, time.Now(), 10*time.Hour)

	msg := tgbotapi.NewMessage(chatID, b.t(lang, "promo_enter_prompt"))
	msg.ReplyMarkup = tgbotapi.ForceReply{
		ForceReply:            true,
		InputFieldPlaceholder: "PROMO123",
		Selective:             true,
	}
	b.send(msg)
}

// handlePromoInput processes a text message from a user who is in promo-entry mode.
func (b *Bot) handlePromoInput(msg *tgbotapi.Message) {
	userID := msg.From.ID
	chatID := msg.Chat.ID
	lang := msg.From.LanguageCode

	fsmCtx, fsmCancel := handlerCtx()
	// Clear promo state immediately regardless of outcome.
	_ = b.fsm.DelPromoState(fsmCtx, userID)
	fsmCancel()

	code := strings.TrimSpace(strings.ToUpper(msg.Text))

	ctx, cancel := handlerCtx()
	defer cancel()
	promo, err := b.promos.GetPromoByCode(ctx, code)
	if err != nil {
		if err == storage.ErrNotFound {
			b.sendOrEditStyled(chatID, 0, b.t(lang, "promo_not_found"), "", nil)
			return
		}
		b.logger.Error("get promo", "error", err)
		b.sendOrEditStyled(chatID, 0, b.t(lang, "error_promo_check"), "", nil)
		return
	}

	// Personal promos are invisible to anyone but their owner.
	if promo.BoundUserID != nil && *promo.BoundUserID != userID {
		b.sendOrEditStyled(chatID, 0, b.t(lang, "promo_not_found"), "", nil)
		return
	}

	// Check if user has already used this promo.
	used, err := b.promos.HasUserUsedPromo(ctx, promo.ID, userID)
	if err != nil {
		b.logger.Error("check promo usage", "error", err)
		b.sendOrEditStyled(chatID, 0, b.t(lang, "error_promo_check"), "", nil)
		return
	}
	if used {
		b.sendOrEditStyled(chatID, 0, b.t(lang, "promo_already_used"), "", nil)
		return
	}

	// Fetch cart to show updated totals.
	view, err := b.cart.Get(ctx, userID)
	if err != nil {
		b.logger.Error("get cart for promo", "error", err)
		b.sendOrEditStyled(chatID, 0, b.t(lang, "error_load_cart"), "", nil)
		return
	}

	if len(view.Items) == 0 {
		b.sendOrEditStyled(chatID, 0, b.t(lang, "cart_empty"), "", nil)
		return
	}
	if err := shop.ValidateSubscriptionCart(view); err != nil {
		b.sendOrEditStyled(chatID, 0, b.t(lang, "sub_alone"), "", nil)
		return
	}

	// Check category restriction if promo has one.
	if promo.CategoryID != nil {
		hasMatch := false
		for _, item := range view.Items {
			if item.Product.CategoryID == *promo.CategoryID {
				hasMatch = true
				break
			}
		}
		if !hasMatch {
			b.sendOrEditStyled(chatID, 0, b.t(lang, "promo_category_mismatch"), "", nil)
			return
		}
	}

	discountedUSD := view.TotalUSD * float64(100-promo.Discount) / 100
	discountedStars := view.TotalStars * (100 - promo.Discount) / 100

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(b.t(lang, "promo_applied_header"), promo.Code, promo.Discount))
	sb.WriteString(fmt.Sprintf(b.t(lang, "promo_original_total"), view.TotalUSD, view.TotalStars))
	sb.WriteString(fmt.Sprintf(b.t(lang, "promo_discounted_total"), discountedUSD, discountedStars))

	keyboard := StyledKeyboard{
		{b.styledBtn(BtnKeyCartCheckout, b.t(lang, "btn_confirm_with_promo"), fmt.Sprintf("order:confirm:promo:%s", promo.Code), StyleSuccess)},
		{Btn(b.t(lang, "btn_confirm_no_promo"), "order:confirm")},
		{Btn(b.t(lang, "btn_back_to_cart"), "back:cart"), Btn(b.t(lang, "btn_menu"), "back:menu")},
	}

	b.sendOrEditStyled(chatID, 0, sb.String(), "", keyboard)
}

func (b *Bot) onOrderConfirm(chatID, userID int64, msgID int, data, lang string) {
	// Extract optional promo code from callback data.
	var promoCode string
	if strings.HasPrefix(data, "order:confirm:promo:") {
		promoCode = strings.TrimPrefix(data, "order:confirm:promo:")
	}

	ctx, cancel := handlerCtx()
	defer cancel()
	view, err := b.cart.Get(ctx, userID)
	if err != nil {
		b.logger.Error("get cart for order confirm", "error", err)
		return
	}

	if len(view.Items) == 0 {
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "order_empty_cart"), "", nil)
		return
	}
	if err := shop.ValidateSubscriptionCart(view); err != nil {
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "sub_alone"), "", nil)
		return
	}
	if cartHasSubscription(view) && b.subs != nil {
		active, subErr := b.subs.ListActiveByUser(ctx, userID)
		if subErr != nil {
			b.logger.Error("check active subscription before checkout", "user_id", userID, "error", subErr)
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "error_short"), "", nil)
			return
		}
		for _, item := range view.Items {
			if item.Product.SubPeriodDays <= 0 {
				continue
			}
			for _, sub := range active {
				if sub.ProductID == item.Product.ID {
					b.sendOrEditStyled(chatID, msgID, b.t(lang, "sub_already_active"), "", nil)
					return
				}
			}
		}
	}

	// Resolve promo if provided.
	var promo *storage.PromoCode
	if promoCode != "" {
		promo, err = b.promos.GetPromoByCode(ctx, promoCode)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				b.sendOrEditStyled(chatID, 0, b.t(lang, "promo_expired"), "", nil)
				return
			}
			b.logger.Error("get promo for order confirm", "error", err)
			b.sendOrEditStyled(chatID, 0, b.t(lang, "error_promo_check"), "", nil)
			return
		}

		// Personal promos are invisible to anyone but their owner.
		if promo.BoundUserID != nil && *promo.BoundUserID != userID {
			b.sendOrEditStyled(chatID, 0, b.t(lang, "promo_not_found"), "", nil)
			return
		}

		used, err := b.promos.HasUserUsedPromo(ctx, promo.ID, userID)
		if err != nil {
			b.logger.Error("check promo usage for order confirm", "error", err)
			b.sendOrEditStyled(chatID, 0, b.t(lang, "error_promo_check"), "", nil)
			return
		}
		if used {
			b.sendOrEditStyled(chatID, 0, b.t(lang, "promo_already_used"), "", nil)
			return
		}

		userOrders, err := b.order.GetUserOrders(ctx, userID)
		if err != nil {
			b.logger.Error("get user orders for promo validation", "error", err)
			b.sendOrEditStyled(chatID, 0, b.t(lang, "error_promo_check"), "", nil)
			return
		}
		if hasPendingOrderWithPromo(userOrders, promo.Code) {
			b.sendOrEditStyled(chatID, 0, b.t(lang, "promo_pending_order"), "", nil)
			return
		}

		if promo.CategoryID != nil {
			hasMatch := false
			for _, item := range view.Items {
				if item.Product.CategoryID == *promo.CategoryID {
					hasMatch = true
					break
				}
			}
			if !hasMatch {
				b.sendOrEditStyled(chatID, 0, b.t(lang, "promo_category_mismatch"), "", nil)
				return
			}
		}
	}

	orderID, err := b.order.CreateFromCart(ctx, userID, view, promo)
	if err != nil {
		var stockErr *shop.ErrInsufficientStock
		if errors.Is(err, storage.ErrSubscriptionOrderConflict) {
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "sub_already_active"), "", nil)
			return
		}
		if errors.As(err, &stockErr) {
			b.sendOrEditStyled(chatID, msgID,
				fmt.Sprintf(b.t(lang, "error_insufficient_stock"), stockErr.ProductName, stockErr.Have), "", nil)
			return
		}
		b.logger.Error("create order", "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "order_create_error"), "", nil)
		return
	}

	createdOrder, err := b.order.GetOrder(ctx, orderID)
	if err != nil {
		b.logger.Error("load created order for payment summary", "order_id", orderID, "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "error_short"), "", StyledKeyboard{
			{Btn(b.t(lang, "btn_orders"), "back:orders"), Btn(b.t(lang, "btn_menu"), "back:menu")},
		})
		return
	}
	// The committed order includes promo discounts and Stars rounding.
	view.TotalUSD = createdOrder.TotalUSD
	view.TotalStars = createdOrder.TotalStars
	view.TotalRUB = createdOrder.TotalRUB
	view.TotalTONNano = createdOrder.TotalTonNano

	b.notifyAdmins(ctx, AdminEventOrderNew, fmt.Sprintf(
		b.t("en", "admin_order_new"),
		orderID, userID, view.TotalUSD, view.TotalStars,
	))

	// Subscription products are payable with Stars only — hide crypto.
	cryptoOK := b.cryptoPaymentsEnabled() && !cartHasSubscription(view)
	yookassaOK := b.yooKassaPaymentsEnabled() && !cartHasSubscription(view) && view.TotalRUB > 0
	stripeOK := b.stripePaymentsEnabled() && !cartHasSubscription(view)
	tonOK := b.tonPaymentsEnabled() && !cartHasSubscription(view) && view.TotalTONNano > 0
	nowpaymentsOK := b.nowpaymentsEnabled() && !cartHasSubscription(view)
	// The internal balance rail is offered only for non-subscription orders
	// when the buyer holds a positive balance; a lookup failure hides the row
	// rather than blocking checkout.
	balanceUSD := 0.0
	if b.balances != nil && !cartHasSubscription(view) {
		if bal, balErr := b.balances.GetBalance(ctx, userID); balErr == nil {
			balanceUSD = bal
		} else if !errors.Is(balErr, storage.ErrNotFound) {
			b.logger.Warn("load buyer balance for payment keyboard", "user_id", userID, "error", balErr)
		}
	}
	text := b.formatPaymentMethodsText(lang, orderID, view, cryptoOK, yookassaOK, stripeOK, tonOK, nowpaymentsOK)
	kb := paymentMethodKeyboard(orderID, cryptoOK, yookassaOK, stripeOK, tonOK, nowpaymentsOK, balanceUSD, view.TotalRUB, view.TotalStars, view.TotalUSD, view.TotalTONNano, lang, b)

	b.sendOrEditStyled(chatID, msgID, text, "HTML", kb)
}

func paymentMethodKeyboard(orderID int64, cryptoEnabled, yookassaOK, stripeOK, tonOK, nowpaymentsOK bool, balanceUSD, totalRUB float64, totalStars int, totalUSD float64, totalTONNano int64, lang string, b *Bot) StyledKeyboard {
	starsLabel := fmt.Sprintf("⭐ Pay %d Stars", totalStars)
	cryptoLabel := fmt.Sprintf("💎 Pay $%.2f USDT", totalUSD)
	rubLabel := fmt.Sprintf("💳 Pay %.2f ₽", totalRUB)
	stripeLabel := fmt.Sprintf("💳 Pay $%.2f", totalUSD)
	tonLabel := fmt.Sprintf("💎 Pay %s TON", formatTON(totalTONNano))
	nowpaymentsLabel := "🪙 Pay crypto"
	balanceLabel := fmt.Sprintf("💰 Pay $%.2f (balance)", balanceUSD)
	termsLabel := "📄 Terms"
	paySupportLabel := "🆘 Payment support"
	cancelLabel := "❌ Cancel order"
	ordersLabel := "📦 My Orders"
	menuLabel := "🏠 Menu"
	if b != nil {
		cryptoLabel = fmt.Sprintf("💎 %s ($%.2f)", b.t(lang, "btn_pay_crypto"), totalUSD)
		starsLabel = fmt.Sprintf("⭐ %s (%d ⭐)", b.t(lang, "btn_pay_stars"), totalStars)
		rubLabel = fmt.Sprintf("💳 %s (%.2f ₽)", b.t(lang, "btn_pay_rub"), totalRUB)
		stripeLabel = b.t(lang, "btn_pay_stripe")
		tonLabel = fmt.Sprintf("%s (%s TON)", b.t(lang, "btn_pay_ton"), formatTON(totalTONNano))
		nowpaymentsLabel = b.t(lang, "btn_pay_nowpayments")
		balanceLabel = fmt.Sprintf("💰 %s ($%.2f)", b.t(lang, "btn_pay_balance"), balanceUSD)
		termsLabel = b.t(lang, "btn_terms")
		paySupportLabel = b.t(lang, "btn_paysupport")
		cancelLabel = b.t(lang, "btn_cancel_order")
		ordersLabel = b.t(lang, "btn_orders")
		menuLabel = b.t(lang, "btn_menu")
	}
	kb := StyledKeyboard{
		{b.styledBtn(BtnKeyPayStars, starsLabel, fmt.Sprintf("pay:stars:%d", orderID), StylePrimary)},
	}
	if cryptoEnabled {
		kb = append(kb, []StyledButton{b.styledBtn(BtnKeyPayCrypto, cryptoLabel, fmt.Sprintf("pay:crypto:%d", orderID), StyleSuccess)})
	}
	// RUB card payments are offered after crypto, only when the adapter is
	// configured and the order has a positive RUB snapshot.
	if yookassaOK {
		kb = append(kb, []StyledButton{b.styledBtn(BtnKeyPayYooKassa, rubLabel, fmt.Sprintf("pay:yookassa:%d", orderID), StylePrimary)})
	}
	// USD card payments via Stripe come last among the card rails: the USD
	// snapshot is charged directly, so no per-order total guard is needed.
	if stripeOK {
		kb = append(kb, []StyledButton{b.styledBtn(BtnKeyPayStripe, stripeLabel, fmt.Sprintf("pay:stripe:%d", orderID), StylePrimary)})
	}
	// TON on-chain transfers follow the card rails, only when the wallet is
	// configured, the rate is positive and the order has a positive nanoton
	// snapshot.
	if tonOK {
		kb = append(kb, []StyledButton{b.styledBtn(BtnKeyPayTON, tonLabel, fmt.Sprintf("pay:ton:%d", orderID), StyleSuccess)})
	}
	// Hosted crypto invoices via NOWPayments close the external rails: the
	// USD snapshot is priced directly, so no per-order total guard is needed.
	if nowpaymentsOK {
		kb = append(kb, []StyledButton{b.styledBtn(BtnKeyPayNowpayments, nowpaymentsLabel, fmt.Sprintf("pay:nowpayments:%d", orderID), StyleSuccess)})
	}
	// The internal balance rail settles synchronously and closes the payment
	// section; it is offered only while the buyer holds a positive balance
	// (the caller passes 0 for subscription orders and lookup failures).
	if balanceUSD > 0 {
		kb = append(kb, []StyledButton{b.styledBtn(BtnKeyPayBalance, balanceLabel, fmt.Sprintf("pay:balance:%d", orderID), StyleSuccess)})
	}
	kb = append(kb,
		[]StyledButton{Btn(termsLabel, "terms"), Btn(paySupportLabel, "paysupport")},
		[]StyledButton{b.styledBtn(BtnKeyPayCancel, cancelLabel, fmt.Sprintf("order:cancel:%d", orderID), StyleDanger), Btn(ordersLabel, "back:orders")},
		[]StyledButton{Btn(menuLabel, "back:menu")},
	)
	return kb
}

func ensureOrderPayableForUser(order *storage.Order, userID int64) error {
	if order == nil || order.UserID != userID {
		return storage.ErrNotFound
	}
	if order.Status != storage.OrderStatusPending {
		return storage.ErrOrderStatusConflict
	}
	if order.PaymentState == storage.PaymentStateNeedsReview {
		return storage.ErrPaymentNeedsReview
	}
	return nil
}

func hasPendingOrderWithPromo(orders []storage.Order, promoCode string) bool {
	for _, order := range orders {
		if order.Status == storage.OrderStatusPending && order.PromoCode == promoCode {
			return true
		}
	}
	return false
}

func (b *Bot) loadPayableOrder(ctx context.Context, userID, orderID int64) (*storage.Order, error) {
	order, err := b.order.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err := ensureOrderPayableForUser(order, userID); err != nil {
		return nil, err
	}
	return order, nil
}
