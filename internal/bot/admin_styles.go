package bot

import (
	"context"
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleBtnStyleAdmin handles the /btnstyle command.
func (b *Bot) handleBtnStyleAdmin(ctx context.Context, msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	b.sendBtnStyleList(ctx, msg.Chat.ID, 0, msg.From.LanguageCode)
}

// sendBtnStyleList renders (or edits) the button style overview for the admin.
// Each row shows the button label and its current style emoji, tapping opens the picker.
func (b *Bot) sendBtnStyleList(ctx context.Context, chatID int64, msgID int, lang string) {
	stored, _ := b.uiSettings.ListButtonStyles(ctx)

	var sb strings.Builder
	sb.WriteString(b.t(lang, "admin_btnstyle_title"))
	for _, key := range AllButtonKeys {
		style := ButtonStyle(stored[key])
		sb.WriteString(fmt.Sprintf("%s %s → %s\n", StyleEmoji(style), ButtonKeyLabel(key), styleLabel(style)))
	}

	// Build inline keyboard: 2 buttons per row (label + current style indicator).
	var rows [][]StyledButton
	for i := 0; i < len(AllButtonKeys); i += 2 {
		var row []StyledButton
		for j := i; j < i+2 && j < len(AllButtonKeys); j++ {
			key := AllButtonKeys[j]
			style := ButtonStyle(stored[key])
			label := fmt.Sprintf("%s %s", StyleEmoji(style), ButtonKeyLabel(key))
			row = append(row, Btn(label, "admin:btnpick:"+key))
		}
		rows = append(rows, row)
	}

	kb := StyledKeyboard(rows)
	b.sendOrEditStyled(chatID, msgID, sb.String(), "HTML", kb)
}

// sendBtnStylePicker renders (or edits) the style picker for a single button key.
func (b *Bot) sendBtnStylePicker(ctx context.Context, chatID int64, msgID int, key string, lang string) {
	current, _ := b.uiSettings.GetButtonStyle(ctx, key)

	text := fmt.Sprintf(
		b.t(lang, "admin_btnstyle_picker"),
		ButtonKeyLabel(key),
		StyleEmoji(ButtonStyle(current)),
		styleLabel(ButtonStyle(current)),
	)

	styleOptions := []struct {
		label string
		style ButtonStyle
	}{
		{"🔵 Primary", StylePrimary},
		{"🟢 Success", StyleSuccess},
		{"🔴 Danger", StyleDanger},
		{b.t(lang, "admin_btnstyle_default"), StyleDefault},
	}

	var styleRow []StyledButton
	for _, opt := range styleOptions {
		styleRow = append(styleRow, Btn(opt.label, fmt.Sprintf("admin:setstyle:%s:%s", key, string(opt.style))))
	}

	kb := StyledKeyboard{
		styleRow,
		{Btn(b.t(lang, "admin_btnstyle_back"), "admin:btnlist")},
	}
	b.sendOrEditStyled(chatID, msgID, text, "HTML", kb)
}

// onAdminSetStyle persists the style choice and returns to the overview.
func (b *Bot) onAdminSetStyle(ctx context.Context, chatID int64, msgID int, data, lang string) {
	// data format: "admin:setstyle:<key>:<style>"
	rest := strings.TrimPrefix(data, "admin:setstyle:")
	sep := strings.LastIndex(rest, ":")
	if sep < 0 {
		return
	}
	key := rest[:sep]
	style := rest[sep+1:]

	if err := b.uiSettings.SetButtonStyle(ctx, key, style); err != nil {
		b.logger.Error("set button style", "key", key, "style", style, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_btnstyle_save_failed")))
		return
	}
	// Invalidate cache entry and reload.
	b.uiStyles.Store(key, style)

	b.sendBtnStyleList(ctx, chatID, msgID, lang)
}

func styleLabel(s ButtonStyle) string {
	switch s {
	case StylePrimary:
		return "primary"
	case StyleSuccess:
		return "success"
	case StyleDanger:
		return "danger"
	default:
		return "default"
	}
}
