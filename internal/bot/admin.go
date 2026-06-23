package bot

import (
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (b *Bot) isAdmin(userID int64) bool {
	_, ok := b.adminMap[userID]
	return ok
}

func (b *Bot) handleAdmin(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}

	text := "🔧 Панель администратора\n\n" +
		"Товары:\n" +
		"/addproduct — Добавить товар\n" +
		"/editproduct <id> — Редактировать товар\n" +
		"/deleteproduct <id> — Удалить товар\n\n" +
		"Категории:\n" +
		"/addcategory <emoji> <название> — Создать категорию\n" +
		"/editcategory <id> name|emoji <значение> — Изменить поле\n" +
		"/deletecategory <id> — Удалить категорию\n" +
		"/listcategories — Список категорий с ID\n\n" +
		"Заказы:\n" +
		"/orders_all [статус] — Все заказы\n" +
		"/setdelivered <id> — Отметить заказ доставленным\n" +
		"/export_orders — Экспорт заказов в CSV\n\n" +
		"Промокоды:\n" +
		"/addpromo <код> <скидка%> [макс_использований] [дней] [category_id] — Создать промокод\n" +
		"/listpromos — Активные промокоды\n" +
		"/deletepromo <id> — Деактивировать промокод\n\n" +
		"Дизайн:\n" +
		"/btnstyle — Настроить цвета кнопок\n\n" +
		"Аналитика:\n" +
		"/analytics — Статистика продаж"

	b.send(tgbotapi.NewMessage(msg.Chat.ID, text))
}

// handleBtnStyleAdmin handles the /btnstyle command.
func (b *Bot) handleBtnStyleAdmin(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	b.sendBtnStyleList(msg.Chat.ID, 0)
}

// sendBtnStyleList renders (or edits) the button style overview for the admin.
// Each row shows the button label and its current style emoji, tapping opens the picker.
func (b *Bot) sendBtnStyleList(chatID int64, msgID int) {
	ctx, cancel := handlerCtx()
	defer cancel()
	stored, _ := b.uiSettings.ListButtonStyles(ctx)

	var sb strings.Builder
	sb.WriteString("🎨 <b>Настройка стилей кнопок</b>\n\n")
	sb.WriteString("Нажмите на кнопку, чтобы изменить её цвет.\n\n")
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
func (b *Bot) sendBtnStylePicker(chatID int64, msgID int, key string) {
	ctx, cancel := handlerCtx()
	defer cancel()
	current, _ := b.uiSettings.GetButtonStyle(ctx, key)

	text := fmt.Sprintf(
		"🎨 <b>Стиль кнопки: %s</b>\n\nТекущий стиль: %s %s\n\nВыберите новый стиль:",
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
		{"⬜ По умолчанию", StyleDefault},
	}

	var styleRow []StyledButton
	for _, opt := range styleOptions {
		styleRow = append(styleRow, Btn(opt.label, fmt.Sprintf("admin:setstyle:%s:%s", key, string(opt.style))))
	}

	kb := StyledKeyboard{
		styleRow,
		{Btn("◀️ Назад к списку", "admin:btnlist")},
	}
	b.sendOrEditStyled(chatID, msgID, text, "HTML", kb)
}

// onAdminSetStyle persists the style choice and returns to the overview.
func (b *Bot) onAdminSetStyle(chatID int64, msgID int, data string) {
	// data format: "admin:setstyle:<key>:<style>"
	rest := strings.TrimPrefix(data, "admin:setstyle:")
	sep := strings.LastIndex(rest, ":")
	if sep < 0 {
		return
	}
	key := rest[:sep]
	style := rest[sep+1:]

	ctx, cancel := handlerCtx()
	defer cancel()
	if err := b.uiSettings.SetButtonStyle(ctx, key, style); err != nil {
		b.logger.Error("set button style", "key", key, "style", style, "error", err)
		b.send(tgbotapi.NewMessage(chatID, "❌ Не удалось сохранить стиль."))
		return
	}
	// Invalidate cache entry and reload.
	b.uiStyles.Store(key, style)

	b.sendBtnStyleList(chatID, msgID)
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
