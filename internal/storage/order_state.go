package storage

import (
	"fmt"
	"math"
	"strings"
)

type commerceState struct {
	order       string
	payment     string
	fulfillment string
}

func stateForLegacyStatus(status string) (commerceState, error) {
	switch status {
	case OrderStatusPending:
		return commerceState{OrderStatePlaced, PaymentStatePending, FulfillmentStateUnfulfilled}, nil
	case OrderStatusPaid:
		return commerceState{OrderStatePlaced, PaymentStateSettled, FulfillmentStateUnfulfilled}, nil
	case OrderStatusDelivered:
		return commerceState{OrderStateCompleted, PaymentStateSettled, FulfillmentStateFulfilled}, nil
	case OrderStatusCancelled:
		return commerceState{OrderStateCancelled, PaymentStateCancelled, FulfillmentStateUnfulfilled}, nil
	default:
		return commerceState{}, fmt.Errorf("order store: unknown legacy status %q", status)
	}
}

func normalizePaymentProvider(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case PaymentMethodStars:
		return PaymentMethodStars
	case PaymentMethodCrypto, "cryptobot":
		return PaymentMethodCrypto
	default:
		return strings.ToLower(strings.TrimSpace(method))
	}
}

// CanonicalPaymentProvider maps persisted legacy aliases to the provider
// identity used by the immutable ledger and service-layer replay checks.
func CanonicalPaymentProvider(method string) string {
	return normalizePaymentProvider(method)
}

func orderMoney(order Order, provider string) (amount int64, currency string, scale int, err error) {
	switch normalizePaymentProvider(provider) {
	case PaymentMethodStars:
		if order.TotalStars <= 0 {
			return 0, "", 0, ErrInvalidMoney
		}
		return int64(order.TotalStars), "XTR", 0, nil
	case PaymentMethodCrypto:
		if order.TotalUSD <= 0 || math.IsNaN(order.TotalUSD) || math.IsInf(order.TotalUSD, 0) {
			return 0, "", 0, ErrInvalidMoney
		}
		return int64(math.Round(order.TotalUSD * 100)), "USD", 2, nil
	default:
		return 0, "", 0, fmt.Errorf("order store: unsupported payment provider %q", provider)
	}
}

func validatePaymentFact(order Order, fact PaymentFact) (PaymentFact, error) {
	fact.Provider = normalizePaymentProvider(fact.Provider)
	expectedAmount, _, expectedScale, err := orderMoney(order, fact.Provider)
	if err != nil {
		return PaymentFact{}, err
	}
	if fact.ExternalID == "" || fact.AmountMinor != expectedAmount || fact.Scale != expectedScale {
		return PaymentFact{}, ErrPaymentReceiptMismatch
	}
	switch fact.Provider {
	case PaymentMethodStars:
		if fact.Currency != "XTR" || (fact.PayerID > 0 && fact.PayerID != order.UserID) {
			return PaymentFact{}, ErrPaymentReceiptMismatch
		}
	case PaymentMethodCrypto:
		if fact.Currency != "USD" && fact.Currency != "USDT" {
			return PaymentFact{}, ErrPaymentReceiptMismatch
		}
	default:
		return PaymentFact{}, ErrPaymentReceiptMismatch
	}
	return fact, nil
}
