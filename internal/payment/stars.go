package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

// SubscriptionPeriodSeconds converts a subscription period in days to the
// seconds value the Bot API expects. Telegram currently only accepts
// 2592000 (30 days).
func SubscriptionPeriodSeconds(days int) int {
	return days * 24 * 60 * 60
}

// OrderGetter is the narrow order-lookup dependency HandlePreCheckout needs.
type OrderGetter interface {
	GetOrder(ctx context.Context, id int64) (*storage.Order, error)
}

// Translator resolves an i18n key for the given language. The bot layer
// injects its locale service; payment code stays free of i18n plumbing.
type Translator func(lang, key string) string

// Pre-checkout rejection i18n keys, one per validation rule.
const (
	PreCheckoutKeyOrderNotFound   = "precheckout_order_not_found"
	PreCheckoutKeyWrongUser       = "precheckout_wrong_user"
	PreCheckoutKeyNotPending      = "precheckout_order_not_pending"
	PreCheckoutKeyAmountMismatch  = "precheckout_amount_mismatch"
	PreCheckoutKeyValidationError = "precheckout_validation_error"
)

// StarsPayment handles Telegram Stars payments via the Bot Payments API.
type StarsPayment struct {
	bot       *tgbotapi.BotAPI
	orders    OrderGetter
	translate Translator
}

// NewStarsPayment creates a new StarsPayment backed by the given Bot API,
// order lookup and translator.
func NewStarsPayment(bot *tgbotapi.BotAPI, orders OrderGetter, translate Translator) *StarsPayment {
	if translate == nil {
		translate = func(_, key string) string { return key }
	}
	return &StarsPayment{bot: bot, orders: orders, translate: translate}
}

// SendInvoice creates and sends a Telegram Stars invoice to the given chat.
// The invoice payload contains the order ID so it can be correlated later
// during the PreCheckoutQuery / SuccessfulPayment flow.
//
// subscriptionPeriodSeconds > 0 turns the invoice into a recurring Stars
// subscription (Bot API `subscription_period`). tgbotapi v5 does not know
// that field, so the subscription variant goes through a raw MakeRequest.
func (s *StarsPayment) SendInvoice(chatID int64, orderID int64, totalStars int, items []storage.OrderItem, subscriptionPeriodSeconds int) error {
	if subscriptionPeriodSeconds > 0 {
		return s.sendSubscriptionInvoice(chatID, orderID, totalStars, items, subscriptionPeriodSeconds)
	}

	invoice := tgbotapi.InvoiceConfig{
		BaseChat: tgbotapi.BaseChat{
			ChatID: chatID,
		},
		Title:               fmt.Sprintf("Заказ #%d", orderID),
		Description:         buildDescription(items),
		Payload:             strconv.FormatInt(orderID, 10),
		StartParameter:      invoiceStartParameter(orderID),
		Currency:            "XTR",
		SuggestedTipAmounts: []int{},
		Prices: []tgbotapi.LabeledPrice{
			{Label: "Итого", Amount: totalStars},
		},
	}

	_, err := s.bot.Send(invoice)
	if err != nil {
		return fmt.Errorf("stars: send invoice: %w", err)
	}
	return nil
}

// sendSubscriptionInvoice sends a recurring Stars invoice via the raw Bot API,
// because tgbotapi v5 has no subscription_period field on InvoiceConfig.
func (s *StarsPayment) sendSubscriptionInvoice(chatID, orderID int64, totalStars int, items []storage.OrderItem, periodSeconds int) error {
	prices, err := json.Marshal([]tgbotapi.LabeledPrice{{Label: "Итого", Amount: totalStars}})
	if err != nil {
		return fmt.Errorf("stars: marshal subscription prices: %w", err)
	}

	params := tgbotapi.Params{
		"chat_id":             strconv.FormatInt(chatID, 10),
		"title":               fmt.Sprintf("Заказ #%d", orderID),
		"description":         buildDescription(items),
		"payload":             strconv.FormatInt(orderID, 10),
		"currency":            "XTR",
		"prices":              string(prices),
		"subscription_period": strconv.Itoa(periodSeconds),
	}

	if _, err := s.bot.MakeRequest("sendInvoice", params); err != nil {
		return fmt.Errorf("stars: send subscription invoice: %w", err)
	}
	return nil
}

// HandlePreCheckout validates an incoming PreCheckoutQuery against the stored
// order and answers the query. The payment is approved only when the order
// exists, belongs to the paying user, is still pending, and its Stars total
// matches the invoice amount; otherwise the query is rejected with a
// user-facing localized reason.
func (s *StarsPayment) HandlePreCheckout(ctx context.Context, query *tgbotapi.PreCheckoutQuery) error {
	rejectKey := s.validatePreCheckout(ctx, query)

	resp := tgbotapi.PreCheckoutConfig{
		PreCheckoutQueryID: query.ID,
		OK:                 rejectKey == "",
	}
	if rejectKey != "" {
		lang := ""
		if query.From != nil {
			lang = query.From.LanguageCode
		}
		resp.ErrorMessage = s.translate(lang, rejectKey)
	}
	if _, err := s.bot.Request(resp); err != nil {
		return fmt.Errorf("stars: answer pre-checkout: %w", err)
	}
	return nil
}

// validatePreCheckout returns "" when the query is payable, or the i18n key
// of the rejection reason.
func (s *StarsPayment) validatePreCheckout(ctx context.Context, query *tgbotapi.PreCheckoutQuery) string {
	orderID, err := strconv.ParseInt(query.InvoicePayload, 10, 64)
	if err != nil {
		return PreCheckoutKeyOrderNotFound
	}
	order, err := s.orders.GetOrder(ctx, orderID)
	switch {
	case errors.Is(err, storage.ErrNotFound) || (err == nil && order == nil):
		return PreCheckoutKeyOrderNotFound
	case err != nil:
		return PreCheckoutKeyValidationError
	case query.From == nil || order.UserID != query.From.ID:
		return PreCheckoutKeyWrongUser
	case order.Status != storage.OrderStatusPending:
		return PreCheckoutKeyNotPending
	case order.TotalStars != query.TotalAmount:
		return PreCheckoutKeyAmountMismatch
	}
	return ""
}

// buildDescription builds a short invoice description from order line items.
// Uses "Товар #<id> × <qty>" format so it works even when ProductName is empty.
func buildDescription(items []storage.OrderItem) string {
	if len(items) == 0 {
		return "Оплата заказа"
	}

	first := formatInvoiceItemLabel(items[0])
	if len(items) == 1 {
		return first
	}

	extra := len(items) - 1
	if extra == 1 {
		return fmt.Sprintf("%s, ещё %d товар", first, extra)
	}

	return fmt.Sprintf("%s, ещё %d товара", first, extra)
}

func formatInvoiceItemLabel(item storage.OrderItem) string {
	label := strings.TrimSpace(item.ProductName)
	if label == "" {
		label = fmt.Sprintf("Товар #%d", item.ProductID)
	}
	return fmt.Sprintf("%s × %d", label, item.Quantity)
}

func invoiceStartParameter(orderID int64) string {
	return fmt.Sprintf("order-%d", orderID)
}
