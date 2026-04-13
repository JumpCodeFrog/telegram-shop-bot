package bot

// Bot API 9.4: colored inline keyboard buttons via style field.
// tgbotapi v5.5.1 does not expose this field, so we use raw JSON + MakeRequest.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ButtonStyle maps to Bot API 9.4 button style values.
type ButtonStyle string

const (
	StyleDefault ButtonStyle = ""
	StylePrimary ButtonStyle = "primary"
	StyleSuccess ButtonStyle = "success"
	StyleDanger  ButtonStyle = "danger"
)

// Button key constants used for admin-configurable style overrides.
// Each constant identifies a logical button in the UI.
const (
	BtnKeyMenuCatalog  = "menu_catalog"
	BtnKeyMenuCart     = "menu_cart"
	BtnKeyMenuOrders   = "menu_orders"
	BtnKeyMenuProfile  = "menu_profile"
	BtnKeyMenuSupport  = "menu_support"
	BtnKeyProductAdd   = "product_add"
	BtnKeyProductWish  = "product_wish"
	BtnKeyCartCheckout = "cart_checkout"
	BtnKeyCartRemove   = "cart_remove"
	BtnKeyPayStars     = "pay_stars"
	BtnKeyPayCrypto    = "pay_crypto"
	BtnKeyPayCancel    = "pay_cancel"
)

// AllButtonKeys lists every configurable button key in display order.
var AllButtonKeys = []string{
	BtnKeyMenuCatalog,
	BtnKeyMenuCart,
	BtnKeyMenuOrders,
	BtnKeyMenuProfile,
	BtnKeyMenuSupport,
	BtnKeyProductAdd,
	BtnKeyProductWish,
	BtnKeyCartCheckout,
	BtnKeyCartRemove,
	BtnKeyPayStars,
	BtnKeyPayCrypto,
	BtnKeyPayCancel,
}

// ButtonKeyLabel returns a human-readable Russian label for a button key.
func ButtonKeyLabel(key string) string {
	switch key {
	case BtnKeyMenuCatalog:
		return "🛍 Каталог"
	case BtnKeyMenuCart:
		return "🛒 Корзина"
	case BtnKeyMenuOrders:
		return "📦 Заказы"
	case BtnKeyMenuProfile:
		return "👤 Профиль"
	case BtnKeyMenuSupport:
		return "🆘 Поддержка"
	case BtnKeyProductAdd:
		return "🛒 Добавить в корзину"
	case BtnKeyProductWish:
		return "❤️ Вишлист"
	case BtnKeyCartCheckout:
		return "✅ Оформить заказ"
	case BtnKeyCartRemove:
		return "🗑 Удалить"
	case BtnKeyPayStars:
		return "⭐ Telegram Stars"
	case BtnKeyPayCrypto:
		return "💎 Crypto"
	case BtnKeyPayCancel:
		return "❌ Отмена"
	default:
		return key
	}
}

// StyleEmoji returns a short emoji indicator for a style value.
func StyleEmoji(style ButtonStyle) string {
	switch style {
	case StylePrimary:
		return "🔵"
	case StyleSuccess:
		return "🟢"
	case StyleDanger:
		return "🔴"
	default:
		return "⬜"
	}
}

// StyledButton represents an inline keyboard button with optional style.
type StyledButton struct {
	Text         string      `json:"text"`
	CallbackData string      `json:"callback_data,omitempty"`
	URL          string      `json:"url,omitempty"`
	Style        ButtonStyle `json:"style,omitempty"`
}

// StyledKeyboard is a 2D slice of StyledButton rows.
type StyledKeyboard [][]StyledButton

// Btn creates a default-style button.
func Btn(text, data string) StyledButton {
	return StyledButton{Text: text, CallbackData: data}
}

// BtnPrimary creates a blue primary-action button.
func BtnPrimary(text, data string) StyledButton {
	return StyledButton{Text: text, CallbackData: data, Style: StylePrimary}
}

// BtnSuccess creates a green success button.
func BtnSuccess(text, data string) StyledButton {
	return StyledButton{Text: text, CallbackData: data, Style: StyleSuccess}
}

// BtnDanger creates a red danger button.
func BtnDanger(text, data string) StyledButton {
	return StyledButton{Text: text, CallbackData: data, Style: StyleDanger}
}

// BtnURL creates a URL button (opens external link).
func BtnURL(text, url string) StyledButton {
	return StyledButton{Text: text, URL: url}
}

// styledBtn creates a button whose style comes from the admin-configured override.
// If no override is set for key, defaultStyle is used.
// Safe to call on a nil *Bot (returns defaultStyle, used in tests).
func (b *Bot) styledBtn(key, text, data string, defaultStyle ButtonStyle) StyledButton {
	if b != nil {
		if v, ok := b.uiStyles.Load(key); ok {
			return StyledButton{Text: text, CallbackData: data, Style: ButtonStyle(v.(string))}
		}
	}
	return StyledButton{Text: text, CallbackData: data, Style: defaultStyle}
}

// reloadButtonStyles fetches all button style overrides from DB into the in-memory cache.
// Called once at startup and after every admin style change.
func (b *Bot) reloadButtonStyles(ctx context.Context) {
	styles, err := b.uiSettings.ListButtonStyles(ctx)
	if err != nil {
		b.logger.Warn("reloadButtonStyles failed", "error", err)
		return
	}
	for k, v := range styles {
		b.uiStyles.Store(k, v)
	}
}

// sendStyled sends a new message with a styled inline keyboard via raw Bot API.
func (b *Bot) sendStyled(chatID int64, text string, parseMode string, kb StyledKeyboard) error {
	params, err := buildStyledParams(chatID, 0, text, parseMode, kb, false)
	if err != nil {
		return err
	}
	_, err = b.api.MakeRequest("sendMessage", params)
	return err
}

// editStyled edits an existing message with a styled inline keyboard via raw Bot API.
func (b *Bot) editStyled(chatID int64, msgID int, text string, parseMode string, kb StyledKeyboard) error {
	params, err := buildStyledParams(chatID, msgID, text, parseMode, kb, true)
	if err != nil {
		return err
	}
	_, err = b.api.MakeRequest("editMessageText", params)
	return err
}

// sendOrEditStyled sends a new message or edits existing one based on msgID.
// If editing fails (e.g. message not found), it falls back to sending a new message.
func (b *Bot) sendOrEditStyled(chatID int64, msgID int, text string, parseMode string, kb StyledKeyboard) {
	if msgID > 0 {
		if err := b.editStyled(chatID, msgID, text, parseMode, kb); err != nil {
			// "message is not modified" is harmless — content is the same, skip fallback.
			if isNotModified(err) {
				return
			}
			b.logger.Warn("editStyled failed, falling back to sendMessage", "chat_id", chatID, "msg_id", msgID, "error", err)
			// Fall through to send a fresh message.
		} else {
			return
		}
	}
	if err := b.sendStyled(chatID, text, parseMode, kb); err != nil {
		b.logger.Error("sendStyled failed", "chat_id", chatID, "error", err)
	}
}

// isNotModified reports whether a Telegram API error is "message is not modified".
func isNotModified(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "message is not modified")
}

func buildStyledParams(chatID int64, msgID int, text, parseMode string, kb StyledKeyboard, isEdit bool) (map[string]string, error) {
	kbJSON, err := json.Marshal(map[string]interface{}{
		"inline_keyboard": kb,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal styled keyboard: %w", err)
	}

	params := map[string]string{
		"chat_id":      strconv.FormatInt(chatID, 10),
		"text":         text,
		"reply_markup": string(kbJSON),
	}
	if parseMode != "" {
		params["parse_mode"] = parseMode
	}
	if isEdit && msgID > 0 {
		params["message_id"] = strconv.Itoa(msgID)
	}
	return params, nil
}

