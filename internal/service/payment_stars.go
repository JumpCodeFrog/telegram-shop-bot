package service

import (
	"context"
	"fmt"
)

type StarsProvider struct{}

func NewStarsProvider() *StarsProvider {
	return &StarsProvider{}
}

func (p *StarsProvider) Name() string { return "stars" }

func (p *StarsProvider) CreateInvoice(ctx context.Context, orderID int64, amount float64, description string) (*Invoice, error) {
	// Telegram Stars are handled via SendInvoice in Bot API
	// We return a special internal ID to trigger the SendInvoice logic in the handler
	return &Invoice{
		ID:          fmt.Sprintf("stars_%d", orderID),
		PayURL:      "internal://stars",
		TotalAmount: amount,
		Currency:    "XTR",
	}, nil
}

func (p *StarsProvider) VerifyPayment(ctx context.Context, externalID string) (bool, error) {
	// Stars payment verification usually happens via PreCheckoutQuery and SuccessfulPayment updates
	return true, nil
}
