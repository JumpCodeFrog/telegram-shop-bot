package bot

import (
	"context"
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

// handleCancel cancels any active dialog for the user.
func (b *Bot) handleCancel(msg *tgbotapi.Message) {
	ctx := context.Background()
	lang := msg.From.LanguageCode

	inAdd := false
	if _, err := b.fsm.GetAddProductState(ctx, msg.From.ID); err == nil {
		inAdd = true
		_ = b.fsm.DelAddProductState(ctx, msg.From.ID)
	}

	inPromo := false
	if _, err := b.fsm.GetPromoState(ctx, msg.From.ID); err == nil {
		inPromo = true
		_ = b.fsm.DelPromoState(ctx, msg.From.ID)
	}

	switch {
	case inAdd:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "cancel_add_product")))
	case inPromo:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "cancel_promo")))
	default:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "cancel_nothing")))
	}
}

// handleStart sends a welcome message with the main menu inline keyboard.
func (b *Bot) handleStart(msg *tgbotapi.Message) {
	lang := msg.From.LanguageCode
	// Handle referral deep links
	refCode := strings.TrimSpace(msg.CommandArguments())
	if refCode != "" {
		ctx := context.Background()
		referrer, err := b.referrals.GetUserByReferralCode(ctx, refCode)
		if err == nil && referrer != nil && referrer.TelegramID != msg.From.ID {
			// Check registration limit (Anti-Fraud)
			allowed, _ := b.referralService.CheckRegistrationLimit(ctx, referrer.TelegramID)
			if allowed {
				_ = b.referrals.SetReferrer(ctx, msg.From.ID, referrer.ID)
				// Bonus will be awarded on first purchase (Anti-Fraud)
				b.logger.Info("referral link used", "user_id", msg.From.ID, "referrer_id", referrer.ID)
			} else {
				b.logger.Warn("referral limit reached", "referrer_id", referrer.ID)
			}
		}
	}

	ctx := context.Background()
	b.sendMainMenu(msg.Chat.ID, msg.From.ID, 0, lang, ctx)
}

// sendMainMenu renders the main menu. If msgID > 0 it edits the existing message;
// otherwise it sends a new one. Uses Bot API 9.4 styled (colored) buttons.
func (b *Bot) sendMainMenu(chatID, userID int64, msgID int, lang string, ctx context.Context) {
	// Build cart button label with item count if cart is non-empty.
	cartLabel := b.t(lang, "btn_cart")
	if cartView, err := b.cart.Get(ctx, userID); err == nil && len(cartView.Items) > 0 {
		total := 0
		for _, item := range cartView.Items {
			total += item.Quantity
		}
		cartLabel = fmt.Sprintf("🛒 %s (%d)", b.t(lang, "cart"), total)
	}

	// Build welcome text with active order count.
	welcomeText := b.t(lang, "start_welcome")
	if orders, err := b.order.GetUserOrders(ctx, userID); err == nil {
		active := 0
		for _, o := range orders {
			if o.Status == storage.OrderStatusPending {
				active++
			}
		}
		if active > 0 {
			welcomeText += fmt.Sprintf("\n\n📦 %s: %d", b.t(lang, "active_orders"), active)
		}
	}

	// Bot API 9.4: admin-configurable styles, fallback to semantic defaults.
	kb := StyledKeyboard{
		{b.styledBtn(BtnKeyMenuCatalog, b.t(lang, "btn_catalog"), "back:catalog", StylePrimary)},
		{b.styledBtn(BtnKeyMenuCart, cartLabel, "back:cart", StylePrimary), b.styledBtn(BtnKeyMenuOrders, b.t(lang, "btn_orders"), "back:orders", StyleDefault)},
		{b.styledBtn(BtnKeyMenuProfile, b.t(lang, "btn_profile"), "back:profile", StyleDefault), b.styledBtn(BtnKeyMenuSupport, b.t(lang, "btn_support"), "support", StyleDefault)},
	}
	b.sendOrEditStyled(chatID, msgID, welcomeText, "HTML", kb)
}

// handleHelp sends a list of available commands.
func (b *Bot) handleHelp(msg *tgbotapi.Message) {
	reply := tgbotapi.NewMessage(msg.Chat.ID, b.t(msg.From.LanguageCode, "help_text"))
	b.send(reply)
}
