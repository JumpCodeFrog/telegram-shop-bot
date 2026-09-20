package shop

// Internal balance rail: ConfirmBalancePayment settles a pending order by
// debiting the buyer's USD balance — synchronously, with no provider
// round-trip.
//
// Atomicity: the storage layer owns no cross-store transaction
// (updateOrderStatusOnce opens and commits its own tx around the order
// transition + ledger rows, and SQLBalanceStore.AdjustBalance commits its
// own tx), so a single debit+settle tx is not available through the house
// helpers. The service therefore runs DEBIT-FIRST + COMPENSATING CREDIT:
// any settle failure — including the ErrOrderStatusConflict of a lost
// double-tap race — is answered with an exact credit, so a confirmed
// payment can never double-charge and a failed settle never keeps the money.
// Replay safety comes from the pre-debit precondition snapshot (the order
// must still be pending) combined with the settle CAS.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"shop_bot/internal/storage"
)

var (
	// ErrBalanceUnavailable reports that the service was built without a
	// balance store (unit tests, partial wiring).
	ErrBalanceUnavailable = errors.New("shop: balance store unavailable")
	// ErrBalanceSubscriptionUnsupported rejects balance settlement for
	// subscription orders: subscriptions settle through the Stars
	// entitlement path only.
	ErrBalanceSubscriptionUnsupported = errors.New("shop: subscription orders are not payable by balance")
)

// ConfirmBalancePayment debits the buyer's internal USD balance by the
// order's USD snapshot and settles the order through the same fact path as
// the external rails (provider "balance", deterministic ExternalID
// "balance:<orderID>", the buyer as required positive payer).
//
// Guards mirror ConfirmPaymentReceipt: unknown or foreign order →
// ErrNotFound; non-pending order → ErrOrderStatusConflict (checked BEFORE
// any balance mutation, so a replayed tap debits nothing); subscription
// order → ErrBalanceSubscriptionUnsupported. Insufficient funds surface as
// storage.ErrInsufficientFunds with no state change at all.
func (s *OrderService) ConfirmBalancePayment(ctx context.Context, orderID, userID int64) (*PaymentOutcome, error) {
	if s.payments.Balances == nil {
		return nil, ErrBalanceUnavailable
	}
	order, err := s.orders.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.UserID != userID {
		// A foreign order is indistinguishable from an unknown one.
		return nil, storage.ErrNotFound
	}
	if order.Status != storage.OrderStatusPending {
		return nil, storage.ErrOrderStatusConflict
	}
	if order.PaymentState == storage.PaymentStateNeedsReview {
		return nil, storage.ErrPaymentNeedsReview
	}
	if order.SubscriptionProductID > 0 {
		return nil, ErrBalanceSubscriptionUnsupported
	}
	// Cents rounding at the boundary, same rule as the storage store.
	amountUSD := math.Round(order.TotalUSD*100) / 100
	if amountUSD <= 0 {
		return nil, storage.ErrInvalidMoney
	}

	// Crash-window idempotency: a prior tap may have crashed between the
	// debit and the settle, leaving the order pending with an orphan debit
	// that every pre-debit guard above still passes. The order's net
	// ledger effect decides: net < 0 means money was taken for this order
	// and never returned, so the orphan debit covers it and the debit is
	// skipped; net >= 0 means no prior debit (or a compensated one — the
	// money was legitimately returned, so the re-tap debits again). Each
	// orphan debit deepens the net-negative, so N crash loops still settle
	// exactly once.
	net, err := s.payments.Balances.OrderBalanceNet(ctx, userID, orderID)
	if err != nil {
		return nil, err
	}
	debited := false
	if net < 0 {
		s.logger.Warn("balance debit skipped: prior orphan debit covers the order",
			"order_id", orderID, "user_id", userID, "order_net", net)
	} else {
		// Debit first: the store's UPDATE guard makes the funds check and
		// the mutation one atomic statement. No settle work happens before
		// the money is durably reserved.
		if _, err := s.payments.Balances.AdjustBalance(ctx, userID, -amountUSD,
			fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
			return nil, err
		}
		debited = true
	}

	fact := storage.PaymentFact{
		Provider:    storage.PaymentMethodBalance,
		ExternalID:  fmt.Sprintf("balance:%d", orderID),
		PayerID:     userID,
		AmountMinor: int64(math.Round(order.TotalUSD * 100)),
		Currency:    "USD",
		Scale:       2,
		OccurredAt:  time.Now().UTC(),
	}
	if settler, ok := s.orders.(ProviderFactSettler); ok {
		err = settler.UpdateOrderStatusWithPaymentFact(ctx, orderID,
			storage.OrderStatusPending, storage.OrderStatusPaid, fact)
	} else {
		err = s.orders.UpdateOrderStatus(ctx, orderID,
			storage.OrderStatusPending, storage.OrderStatusPaid,
			storage.PaymentMethodBalance, fact.ExternalID)
	}
	if err != nil {
		// Compensating credit: the debit must never survive a failed settle
		// — including the lost-CAS conflict of a concurrent double-tap.
		// Only a debit made by THIS call is compensated: crediting a
		// skipped orphan debit would mint money (a concurrent tap may have
		// settled on it, or it legitimately awaits its own settle).
		if !debited {
			return nil, err
		}
		if _, creditErr := s.payments.Balances.AdjustBalance(ctx, userID, amountUSD,
			fmt.Sprintf("settlement_failed:%d", orderID), 0); creditErr != nil {
			s.logger.Error("balance compensation credit failed",
				"order_id", orderID, "user_id", userID, "amount", amountUSD, "error", creditErr)
			return nil, errors.Join(err, creditErr)
		}
		return nil, err
	}
	return s.outcomeAfterPayment(ctx, orderID)
}
