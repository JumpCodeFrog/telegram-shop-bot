package service

import (
	"context"
)

type Invoice struct {
	ID          string
	PayURL      string
	TotalAmount float64
	Currency    string
}

type PaymentProvider interface {
	Name() string
	CreateInvoice(ctx context.Context, orderID int64, amount float64, description string) (*Invoice, error)
	VerifyPayment(ctx context.Context, externalID string) (bool, error)
}

type PaymentService struct {
	providers map[string]PaymentProvider
}

func NewPaymentService() *PaymentService {
	return &PaymentService{
		providers: make(map[string]PaymentProvider),
	}
}

func (s *PaymentService) RegisterProvider(p PaymentProvider) {
	s.providers[p.Name()] = p
}

func (s *PaymentService) GetProvider(name string) PaymentProvider {
	return s.providers[name]
}
