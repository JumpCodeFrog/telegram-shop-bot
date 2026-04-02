package service

import (
	"context"
	"fmt"
)

type CryptoBotProvider struct {
	token string
}

func NewCryptoBotProvider(token string) *CryptoBotProvider {
	return &CryptoBotProvider{token: token}
}

func (p *CryptoBotProvider) Name() string { return "cryptobot" }

func (p *CryptoBotProvider) CreateInvoice(ctx context.Context, orderID int64, amount float64, description string) (*Invoice, error) {
	// Mock implementation for CryptoBot API
	return &Invoice{
		ID:          fmt.Sprintf("cb_%d", orderID),
		PayURL:      "https://t.me/CryptoBot?start=invoice_id",
		TotalAmount: amount,
		Currency:    "USDT",
	}, nil
}

func (p *CryptoBotProvider) VerifyPayment(ctx context.Context, externalID string) (bool, error) {
	// Mock check
	return true, nil
}
