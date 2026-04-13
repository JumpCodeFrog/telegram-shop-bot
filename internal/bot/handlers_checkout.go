package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

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
		b.logger.Error("create order", "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "order_create_error"), "", nil)
		return
	}

	b.notifyAdmins(fmt.Sprintf(
		"🛍 Новый заказ #%d\nПользователь: %d\n💰 $%.2f / %d ⭐",
		orderID, userID, view.TotalUSD, view.TotalStars,
	))

	text := b.formatPaymentMethodsText(lang, orderID, view, b.cryptoPaymentsEnabled())
	kb := paymentMethodKeyboard(orderID, b.cryptoPaymentsEnabled(), view.TotalStars, view.TotalUSD, lang, b)

	b.sendOrEditStyled(chatID, msgID, text, "HTML", kb)
}

func paymentMethodKeyboard(orderID int64, cryptoEnabled bool, totalStars int, totalUSD float64, lang string, b *Bot) StyledKeyboard {
	starsLabel := fmt.Sprintf("⭐ Pay %d Stars", totalStars)
	cryptoLabel := fmt.Sprintf("💎 Pay $%.2f USDT", totalUSD)
	termsLabel := "📄 Terms"
	paySupportLabel := "🆘 Payment support"
	cancelLabel := "❌ Cancel order"
	ordersLabel := "📦 My Orders"
	menuLabel := "🏠 Menu"
	if b != nil {
		cryptoLabel = fmt.Sprintf("💎 %s ($%.2f)", b.t(lang, "btn_pay_crypto"), totalUSD)
		starsLabel = fmt.Sprintf("⭐ %s (%d ⭐)", b.t(lang, "btn_pay_stars"), totalStars)
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
