package service

import (
	"context"
	"fmt"
)

type GenericProvider struct {
	name string
}

func NewGenericProvider(name string) *GenericProvider {
	return &GenericProvider{name: name}
}

func (p *GenericProvider) Name() string { return p.name }

func (p *GenericProvider) CreateInvoice(ctx context.Context, orderID int64, amount float64, description string) (*Invoice, error) {
	return &Invoice{
		ID:          fmt.Sprintf("%s_%d", p.name, orderID),
		PayURL:      fmt.Sprintf("https://checkout.%s.com/pay/...", p.name),
		TotalAmount: amount,
		Currency:    "USD",
	}, nil
}

func (p *GenericProvider) VerifyPayment(ctx context.Context, externalID string) (bool, error) {
	return true, nil
}
