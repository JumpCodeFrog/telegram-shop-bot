package bot

import (
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleReferral handles the /referral command.
func (b *Bot) handleReferral(msg *tgbotapi.Message) {
	b.sendReferralScreen(msg.Chat.ID, msg.From.ID, 0, msg.From.LanguageCode)
}

// sendReferralScreen renders the referral program screen: the personal deep
// link, the number of invited friends and the total referral points earned.
// A referral code is generated lazily on first open.
func (b *Bot) sendReferralScreen(chatID, userID int64, msgID int, lang string) {
	ctx, cancel := handlerCtx()
	defer cancel()

	user, err := b.users.GetByTelegramID(ctx, userID)
	if err != nil {
		b.logger.Error("referral screen: load user", "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "error_load_referral"), "", nil)
		return
	}

	code := user.ReferralCode.String
	if code == "" {
		rc := b.referralService.GenerateCode()
		if err := b.referrals.UpdateReferralCode(ctx, user.ID, rc.Code, rc.ExpiresAt); err != nil {
			b.logger.Error("referral screen: save code", "error", err)
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "error_load_referral"), "", nil)
			return
		}
		code = rc.Code
	}

	stats, err := b.referrals.GetStats(ctx, user.ID)
	if err != nil {
		b.logger.Error("referral screen: load stats", "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "error_load_referral"), "", nil)
		return
	}

	link := b.referralLink(code)
	text := b.formatReferralText(lang, link, stats.TotalReferrals, int64(stats.TotalEarned))

	kb := StyledKeyboard{
		{BtnSwitchInline(b.t(lang, "referral_screen_share"), link)},
		{
			Btn(b.t(lang, "btn_back"), "back:profile"),
			Btn(b.t(lang, "btn_menu"), "back:menu"),
		},
	}
	b.sendOrEditStyled(chatID, msgID, text, "HTML", kb)
}

// referralLink builds the t.me deep link for a referral code. handleStart
// strips the "ref_" prefix before looking the code up.
func (b *Bot) referralLink(code string) string {
	username := ""
	if b.api != nil {
		username = b.api.Self.UserName
	}
	return "https://t.me/" + username + "?start=ref_" + code
}

// formatReferralText renders the referral screen body. Referral codes are
// alphanumeric and the bot username is Telegram-validated, so the link is
// safe to embed in HTML as-is.
func (b *Bot) formatReferralText(lang, link string, invited int, earnedPts int64) string {
	var sb strings.Builder
	sb.WriteString(b.t(lang, "referral_screen_title"))
	sb.WriteString(fmt.Sprintf(b.t(lang, "referral_screen_link"), link))
	sb.WriteString(fmt.Sprintf(b.t(lang, "referral_screen_invited"), invited))
	sb.WriteString(fmt.Sprintf(b.t(lang, "referral_screen_earned"), earnedPts))
	sb.WriteString(b.t(lang, "referral_screen_hint"))
	return sb.String()
}
