package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

// reviewStateTTL bounds how long a rated order waits for its optional text.
const reviewStateTTL = 10 * time.Hour

// reviewableProducts validates that userID may review the given order and
// returns the distinct product IDs of its items. Only the order owner may
// review, and only after delivery.
//
// MVP decision: for a multi-item order the chosen rating (and later the text)
// is applied to every product of the order. A per-item rating flow would need
// an extra product-picker step; revisit if it ever matters.
func reviewableProducts(order *storage.Order, userID int64) ([]int64, error) {
	if order == nil || order.UserID != userID {
		return nil, storage.ErrNotFound
	}
	if order.Status != storage.OrderStatusDelivered {
		return nil, storage.ErrOrderStatusConflict
	}
	ids := make([]int64, 0, len(order.Items))
	seen := make(map[int64]struct{}, len(order.Items))
	for _, item := range order.Items {
		// ProductID 0 means the product row was deleted; nothing to rate.
		if item.ProductID == 0 {
			continue
		}
		if _, dup := seen[item.ProductID]; dup {
			continue
		}
		seen[item.ProductID] = struct{}{}
		ids = append(ids, item.ProductID)
	}
	if len(ids) == 0 {
		return nil, storage.ErrNotFound
	}
	return ids, nil
}

// reviewInviteKeyboard builds the 1..5 star rating row for an order.
func reviewInviteKeyboard(orderID int64) StyledKeyboard {
	row := make([]StyledButton, 0, 5)
	for rating := 1; rating <= 5; rating++ {
		row = append(row, Btn(strconv.Itoa(rating)+"⭐", fmt.Sprintf("review:%d:%d", orderID, rating)))
	}
	return StyledKeyboard{row}
}

// sendReviewInvite asks the buyer to rate a freshly delivered order.
func (b *Bot) sendReviewInvite(ctx context.Context, order *storage.Order) {
	if order == nil {
		return
	}
	lang := "en"
	if u, err := b.users.GetByTelegramID(ctx, order.UserID); err == nil && u != nil && u.LanguageCode != "" {
		lang = u.LanguageCode
	}
	text := b.i18n.Tf(lang, "review_invite", order.ID)
	if err := b.sendStyled(order.UserID, text, "", reviewInviteKeyboard(order.ID)); err != nil {
		b.logger.Warn("send review invite", "order_id", order.ID, "error", err)
	}
}

// handleReviewCallback routes every "review:" callback to its sub-handler.
func (b *Bot) handleReviewCallback(cb *tgbotapi.CallbackQuery) {
	data := cb.Data
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	userID := cb.From.ID
	lang := cb.From.LanguageCode

	switch {
	case data == "review:skip":
		b.onReviewSkip(cb.ID, chatID, userID, msgID, lang)

	case strings.HasPrefix(data, "review:list:"):
		b.ack(cb.ID)
		b.onReviewList(chatID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)

	case strings.HasPrefix(data, "review:del:"):
		b.ack(cb.ID)
		if b.isAdmin(userID) {
			b.onReviewDelete(chatID, data, lang)
		}

	default:
		b.onReviewRate(cb.ID, chatID, userID, msgID, data, lang)
	}
}

// onReviewRate handles "review:<orderID>:<rating>": records the rating for
// every product of the order and asks for an optional text.
func (b *Bot) onReviewRate(cbID string, chatID, userID int64, msgID int, data, lang string) {
	parts := strings.Split(strings.TrimPrefix(data, "review:"), ":")
	if len(parts) != 2 {
		b.ack(cbID)
		return
	}
	orderID, errOrder := strconv.ParseInt(parts[0], 10, 64)
	rating, errRating := strconv.Atoi(parts[1])
	if errOrder != nil || errRating != nil || rating < 1 || rating > 5 {
		b.ack(cbID)
		return
	}

	ctx, cancel := handlerCtx()
	defer cancel()

	order, err := b.order.GetOrder(ctx, orderID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		b.logger.Error("review: load order", "order_id", orderID, "error", err)
		b.alert(cbID, b.t(lang, "review_error"))
		return
	}
	productIDs, err := reviewableProducts(order, userID)
	if err != nil {
		b.alert(cbID, b.t(lang, "review_not_allowed"))
		return
	}

	for _, pid := range productIDs {
		// Re-rating resets any previous text; the FSM step below re-collects it.
		if err := b.reviews.Upsert(ctx, storage.Review{ProductID: pid, UserID: userID, OrderID: orderID, Rating: rating}); err != nil {
			b.logger.Error("review: upsert rating", "order_id", orderID, "product_id", pid, "error", err)
			b.alert(cbID, b.t(lang, "review_error"))
			return
		}
	}

	if err := b.fsm.SetReviewState(ctx, userID, &storage.ReviewState{OrderID: orderID, Rating: rating}, reviewStateTTL); err != nil {
		b.logger.Warn("review: set fsm state", "user_id", userID, "error", err)
	}

	b.ack(cbID)
	kb := StyledKeyboard{{Btn(b.t(lang, "review_btn_skip"), "review:skip")}}
	b.sendOrEditStyled(chatID, msgID, b.i18n.Tf(lang, "review_text_prompt", strings.Repeat("⭐", rating)), "", kb)
}

// onReviewSkip finishes the review flow without a text.
func (b *Bot) onReviewSkip(cbID string, chatID, userID int64, msgID int, lang string) {
	ctx, cancel := handlerCtx()
	defer cancel()
	_ = b.fsm.DelReviewState(ctx, userID) // best effort: state also expires by TTL

	b.ack(cbID)
	b.sendOrEditStyled(chatID, msgID, b.t(lang, "review_thanks"), "", nil)
}

// handleReviewTextInput consumes the free-form text step of the review FSM.
func (b *Bot) handleReviewTextInput(msg *tgbotapi.Message, state *storage.ReviewState) {
	userID := msg.From.ID
	chatID := msg.Chat.ID
	lang := msg.From.LanguageCode

	ctx, cancel := handlerCtx()
	defer cancel()
	// Clear the state up front so a failing save never traps the user in the FSM.
	_ = b.fsm.DelReviewState(ctx, userID)

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "review_thanks")))
		return
	}

	order, err := b.order.GetOrder(ctx, state.OrderID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		b.logger.Error("review: load order for text", "order_id", state.OrderID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "review_error")))
		return
	}
	productIDs, err := reviewableProducts(order, userID)
	if err != nil {
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "review_not_allowed")))
		return
	}

	for _, pid := range productIDs {
		if err := b.reviews.Upsert(ctx, storage.Review{ProductID: pid, UserID: userID, OrderID: state.OrderID, Rating: state.Rating, Text: text}); err != nil {
			b.logger.Error("review: upsert text", "order_id", state.OrderID, "product_id", pid, "error", err)
			b.send(tgbotapi.NewMessage(chatID, b.t(lang, "review_error")))
			return
		}
	}
	b.send(tgbotapi.NewMessage(chatID, b.t(lang, "review_saved")))
}

// onReviewList shows the last 3 reviews of a product ("review:list:<productID>").
func (b *Bot) onReviewList(chatID int64, msgID int, data, lang string) {
	prodID, err := parseIDFromCallback(data, "review:list:")
	if err != nil {
		b.logger.Error("parse review list callback", "data", data, "error", err)
		return
	}

	ctx, cancel := handlerCtx()
	defer cancel()
	reviews, err := b.reviews.ListByProduct(ctx, prodID, 3)
	if err != nil {
		b.logger.Error("review: list by product", "product_id", prodID, "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "review_error"), "", nil)
		return
	}

	var sb strings.Builder
	sb.WriteString(b.t(lang, "review_list_title"))
	sb.WriteString("\n\n")
	if len(reviews) == 0 {
		sb.WriteString(b.t(lang, "review_list_empty"))
	}
	for _, r := range reviews {
		sb.WriteString(strings.Repeat("⭐", r.Rating))
		if r.Text != "" {
			sb.WriteString(" — ")
			sb.WriteString(r.Text)
		}
		sb.WriteString("\n\n")
	}

	kb := StyledKeyboard{{
		Btn(b.t(lang, "btn_back"), fmt.Sprintf("product:%d", prodID)),
		Btn(b.t(lang, "btn_menu"), "back:menu"),
	}}
	b.sendOrEditStyled(chatID, msgID, strings.TrimRight(sb.String(), "\n"), "", kb)
}

// handleReviewsAdmin implements the admin-only /reviews command: the 10 most
// recent reviews with a delete button per review.
func (b *Bot) handleReviewsAdmin(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode

	ctx, cancel := handlerCtx()
	defer cancel()
	reviews, err := b.reviews.ListRecent(ctx, 10)
	if err != nil {
		b.logger.Error("review: list recent", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "review_error")))
		return
	}
	if len(reviews) == 0 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "review_admin_empty")))
		return
	}

	var sb strings.Builder
	sb.WriteString(b.t(lang, "review_admin_title"))
	sb.WriteString("\n\n")
	kb := make(StyledKeyboard, 0, len(reviews))
	for _, r := range reviews {
		sb.WriteString(fmt.Sprintf("#%d | user %d | product %d | %s\n", r.ID, r.UserID, r.ProductID, strings.Repeat("⭐", r.Rating)))
		if r.Text != "" {
			sb.WriteString(r.Text)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		kb = append(kb, []StyledButton{BtnDanger(fmt.Sprintf("🗑 #%d", r.ID), fmt.Sprintf("review:del:%d", r.ID))})
	}
	if err := b.sendStyled(msg.Chat.ID, strings.TrimRight(sb.String(), "\n"), "", kb); err != nil {
		b.logger.Error("review: send admin list", "error", err)
	}
}

// onReviewDelete removes a review by ID ("review:del:<id>", admin only —
// enforced by the caller).
func (b *Bot) onReviewDelete(chatID int64, data, lang string) {
	id, err := parseIDFromCallback(data, "review:del:")
	if err != nil {
		b.logger.Error("parse review delete callback", "data", data, "error", err)
		return
	}

	ctx, cancel := handlerCtx()
	defer cancel()
	if err := b.reviews.Delete(ctx, id); err != nil {
		b.logger.Error("review: delete", "review_id", id, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "review_error")))
		return
	}
	b.send(tgbotapi.NewMessage(chatID, b.i18n.Tf(lang, "review_admin_deleted", id)))
}

// productRating fetches the aggregate rating of a product, degrading to
// "no rating" on storage errors so the product card still renders.
func (b *Bot) productRating(ctx context.Context, productID int64) (float64, int64) {
	avg, count, err := b.reviews.ProductRating(ctx, productID)
	if err != nil {
		b.logger.Warn("product rating", "product_id", productID, "error", err)
		return 0, 0
	}
	return avg, count
}

// formatRatingLine renders the "⭐ 4.7 (12)" line of the product card.
func (b *Bot) formatRatingLine(lang string, avg float64, count int64) string {
	if lang == "" {
		lang = "en"
	}
	return b.i18n.Tf(lang, "review_rating_line", avg, count)
}

// insertReviewsRow places the reviews button on its own row right before the
// trailing navigation row of a product keyboard.
func insertReviewsRow(kb StyledKeyboard, btn StyledButton) StyledKeyboard {
	row := []StyledButton{btn}
	if len(kb) == 0 {
		return StyledKeyboard{row}
	}
	last := kb[len(kb)-1]
	return append(append(kb[:len(kb)-1], row), last)
}
