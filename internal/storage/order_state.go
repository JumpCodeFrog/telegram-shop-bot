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
	case PaymentMethodYooKassa:
		if order.TotalRUB <= 0 || math.IsNaN(order.TotalRUB) || math.IsInf(order.TotalRUB, 0) {
			return 0, "", 0, ErrInvalidMoney
		}
		return int64(math.Round(order.TotalRUB * 100)), "RUB", 2, nil
	case PaymentMethodStripe:
		if order.TotalUSD <= 0 || math.IsNaN(order.TotalUSD) || math.IsInf(order.TotalUSD, 0) {
			return 0, "", 0, ErrInvalidMoney
		}
		return int64(math.Round(order.TotalUSD * 100)), "USD", 2, nil
	case PaymentMethodTON:
		// TotalTonNano is already an integer nanoton count, so the float
		// NaN/Inf guards of the float-priced rails do not apply to this
		// int64 column; the <= 0 guard suffices.
		if order.TotalTonNano <= 0 {
			return 0, "", 0, ErrInvalidMoney
		}
		return order.TotalTonNano, "TON", 9, nil
	case PaymentMethodNowpayments:
		if order.TotalUSD <= 0 || math.IsNaN(order.TotalUSD) || math.IsInf(order.TotalUSD, 0) {
			return 0, "", 0, ErrInvalidMoney
		}
		return int64(math.Round(order.TotalUSD * 100)), "USD", 2, nil
	case PaymentMethodBalance:
		// The internal balance rail charges the USD snapshot directly, same
		// as the stripe/nowpayments USD rails.
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
	// Amount rule, single-sourced at this gate for every caller: the card
	// and IPN rails enforce exact minor-unit equality against the frozen
	// order money, while ton enforces the overpay-tolerant >= rule — an
	// on-chain transfer of at least the snapshot is real money received,
	// so overpay passes and underpay is rejected HERE (the former split,
	// where the fact gate skipped ton amounts and only the receipt layer
	// applied >=, is closed). Shop's ConfirmPaymentReceipt keeps its own
	// >= check as defense-in-depth, and the launcher's
	// providerCaptureSettleable mirrors the rule for CLI path selection.
	if fact.ExternalID == "" || fact.Scale != expectedScale ||
		(fact.Provider == PaymentMethodTON && fact.AmountMinor < expectedAmount) ||
		(fact.Provider != PaymentMethodTON && fact.AmountMinor != expectedAmount) {
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
	case PaymentMethodYooKassa:
		if fact.Currency != "RUB" {
			return PaymentFact{}, ErrPaymentReceiptMismatch
		}
	case PaymentMethodStripe:
		// Stripe has no Telegram payer identity, so only the money is
		// validated; the payer rule lives in invalidProviderCapturePayer.
		if fact.Currency != "USD" {
			return PaymentFact{}, ErrPaymentReceiptMismatch
		}
	case PaymentMethodTON:
		// On-chain TON transfers carry no Telegram payer identity, so no
		// payer check applies here; the payer rule lives in
		// invalidProviderCapturePayer. The overpay-tolerant amount rule
		// (fact >= the frozen nanoton snapshot) lives in the shared gate
		// above: storage rejects underpaying ton facts for every caller,
		// and the receipt layer (ConfirmPaymentReceipt) keeps its own >=
		// check as defense-in-depth.
		if fact.Currency != "TON" {
			return PaymentFact{}, ErrPaymentReceiptMismatch
		}
	case PaymentMethodNowpayments:
		// NOWPayments has no Telegram payer identity, so only the money is
		// validated; the payer rule lives in invalidProviderCapturePayer.
		if fact.Currency != "USD" {
			return PaymentFact{}, ErrPaymentReceiptMismatch
		}
	case PaymentMethodBalance:
		// The internal balance rail always knows its Telegram payer: the
		// debit and the settlement name the same user, so a missing or
		// foreign payer is a mismatch here (not merely an absent identity,
		// unlike the payerless rails — balance is deliberately NOT in
		// providerHasNoTelegramPayer).
		if fact.Currency != "USD" || fact.PayerID <= 0 || fact.PayerID != order.UserID {
			return PaymentFact{}, ErrPaymentReceiptMismatch
		}
	default:
		return PaymentFact{}, ErrPaymentReceiptMismatch
	}
	return fact, nil
}
