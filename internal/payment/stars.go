package payment

import (
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

// StarsPayment handles Telegram Stars payments via the Bot Payments API.
type StarsPayment struct {
	bot *tgbotapi.BotAPI
}

// NewStarsPayment creates a new StarsPayment backed by the given Bot API.
func NewStarsPayment(bot *tgbotapi.BotAPI) *StarsPayment {
	return &StarsPayment{bot: bot}
}

// SendInvoice creates and sends a Telegram Stars invoice to the given chat.
// The invoice payload contains the order ID so it can be correlated later
// during the PreCheckoutQuery / SuccessfulPayment flow.
func (s *StarsPayment) SendInvoice(chatID int64, orderID int64, totalStars int, items []storage.OrderItem) error {
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

// HandlePreCheckout validates an incoming PreCheckoutQuery. It checks that the
// payload contains a valid (numeric) order ID and answers the query accordingly.
func (s *StarsPayment) HandlePreCheckout(query *tgbotapi.PreCheckoutQuery) error {
	_, err := strconv.ParseInt(query.InvoicePayload, 10, 64)
	if err != nil {
		resp := tgbotapi.PreCheckoutConfig{
			PreCheckoutQueryID: query.ID,
			OK:                 false,
			ErrorMessage:       "Некорректные данные заказа",
		}
		_, sendErr := s.bot.Request(resp)
		if sendErr != nil {
			return fmt.Errorf("stars: reject pre-checkout: %w", sendErr)
		}
		return nil
	}

	resp := tgbotapi.PreCheckoutConfig{
		PreCheckoutQueryID: query.ID,
		OK:                 true,
	}
	_, sendErr := s.bot.Request(resp)
	if sendErr != nil {
		return fmt.Errorf("stars: approve pre-checkout: %w", sendErr)
	}
	return nil
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
