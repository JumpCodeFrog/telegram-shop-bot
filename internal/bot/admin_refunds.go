package bot

// Admin refunds (/refund <order_id> [amount]): the money-out surface, driven
// by the same two-tap preview-then-confirm discipline as the payment-review
// queue. Every execution path obeys the ordering ruling — the provider refund
// runs FIRST and the immutable ledger record (IngestProviderRefund) SECOND —
// because money-out is the irreversible step: a ledger failure after a
// successful refund is re-runnable, while the inverse order could record
// money that never left.
//
// Double-refund firewall: every provider call carries the DETERMINISTIC
// idempotency key refund:<orderID>:<amountMinor>:<paymentID> (stripe
// Idempotency-Key / yookassa Idempotence-Key headers), so a re-run after a
// ledger-recording failure is collapsed provider-side into the original
// refund. The balance rail has no provider API — its deterministic
// order_refund:<orderID> balance_txs audit row is the equivalent identity
// and the credit is skipped when it already exists. Stars rely on Telegram
// itself rejecting a second refund of the same charge.
//
// Rails without a refund API (crypto, ton, nowpayments) get an informational
// card: the refund is issued manually at the provider dashboard and stays
// dashboard-visible — in-ledger recording for these rails is a known follow-up
// (the launcher's refund-recording CLI covers stars only today).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

// refundCallbackPrefix addresses the confirm tap. It never collides with the
// payreview prefixes (admin:payrev: / admin:payrevdo:), and the payload —
// prefix (13) plus two int64s plus a colon — stays inside Telegram's 64-byte
// callback-data limit. The rail is NOT encoded: the confirm callback reloads
// the order and re-derives everything from it (TOCTOU discipline).
const refundCallbackPrefix = "admin:refund:"

// refundCLIRecordLine is the launcher's payment-review ingest-stars usage
// line (printPaymentReviewUsage), narrowed to --kind refund. NOTE: the CLI
// authenticates facts against Telegram's star transactions, so it records
// STARS refunds ONLY. It is quoted in exactly one message — the stars
// ledger-failure recovery (admin_refund_ledger_failed_stars) — where it is
// the truthful remedy: a /refund re-run can never record a stars refund
// (Telegram rejects the repeat refundStarPayment), while the CLI records it
// with Telegram's authoritative OccurredAt. The manual rails (crypto, ton,
// nowpayments) have NO refund-recording CLI today: their refunds stay
// dashboard-visible and in-ledger recording is a known follow-up, stated as
// such by the admin_refund_manual card (which must never quote this line).
const refundCLIRecordLine = "telegram-shop-bot payment-review ingest-stars --kind refund --transaction ID --order N --actor NAME --reason TEXT [--apply --confirm-order N]"

// Sentinel errors classifying a refund build failure for message mapping.
var (
	errRefundAmountRange = errors.New("refund: amount out of range for the captured total")
	errRefundNoPaymentID = errors.New("refund: order has no recorded provider payment id")
)

// refundableOrder mirrors the ledger's refund preconditions on the order
// projection: only a paid or delivered order whose payment is settled can be
// refunded through this flow. A partially_refunded order has already used its
// bot-side refund (the confirm gate re-checks this on every tap).
//
// WARNING — this settled-only gate is LOAD-BEARING for the balance rail:
// BalanceTxExists' per-order "order_refund:<orderID>" identity assumes at
// most one balance refund per order. Before relaxing this gate (e.g. to
// accept partially_refunded for further partials), that identity MUST become
// amount-scoped — see the coupling comments in executeRefund's balance branch
// and storage/balance.go, and docs/payment-operations.md §11.
func refundableOrder(order *storage.Order) bool {
	return (order.Status == storage.OrderStatusPaid || order.Status == storage.OrderStatusDelivered) &&
		order.PaymentState == storage.PaymentStateSettled
}

// refundOrderStateLabel renders the order's commerce/payment projection for
// the not-refundable explanation.
func refundOrderStateLabel(order *storage.Order) string {
	state := order.PaymentState
	if state == "" {
		state = "-"
	}
	return order.Status + "/" + state
}

// refundPaymentStateLabel renders the payment state alone for the done
// message ("refunded", "partially_refunded", "needs_review").
func refundPaymentStateLabel(order *storage.Order) string {
	if order.PaymentState == "" {
		return "-"
	}
	return order.PaymentState
}

// refundIsManualRail reports whether the rail has no refund API in this bot:
// the refund happens manually at the provider dashboard; in-ledger recording
// is a known follow-up (no refund-recording CLI exists for these rails).
func refundIsManualRail(rail string) bool {
	switch rail {
	case storage.PaymentMethodCrypto, storage.PaymentMethodTON, storage.PaymentMethodNowpayments:
		return true
	}
	return false
}

// refundRailMoney derives the order's full refundable amount on its rail in
// minor units plus the rail's currency and scale. It mirrors storage's
// unexported orderMoney derivation (the launcher's
// expectedProviderCaptureAmount re-derives it the same way — minimal churn
// over promoting a shared helper): stars read the integer XTR snapshot,
// yookassa rounds the frozen RUB snapshot to kopecks, and stripe/balance
// round the frozen USD snapshot to cents.
func refundRailMoney(order *storage.Order, rail string) (amountMinor int64, currency string, scale int, ok bool) {
	switch rail {
	case storage.PaymentMethodStars:
		if order.TotalStars <= 0 {
			return 0, "", 0, false
		}
		return int64(order.TotalStars), "XTR", 0, true
	case storage.PaymentMethodYooKassa:
		if order.TotalRUB <= 0 || math.IsNaN(order.TotalRUB) || math.IsInf(order.TotalRUB, 0) {
			return 0, "", 0, false
		}
		return int64(math.Round(order.TotalRUB * 100)), "RUB", 2, true
	case storage.PaymentMethodStripe, storage.PaymentMethodBalance:
		if order.TotalUSD <= 0 || math.IsNaN(order.TotalUSD) || math.IsInf(order.TotalUSD, 0) {
			return 0, "", 0, false
		}
		return int64(math.Round(order.TotalUSD * 100)), "USD", 2, true
	}
	return 0, "", 0, false
}

// refundAmountDecimal renders minor units as a major-unit decimal string with
// the rail's scale, using integer string math (no float rounding). It is the
// exact inverse of the /refund command's amount parsing (ParseFloat →
// Round(x × Pow10(scale))): re-running the command with the rendered value
// reproduces the SAME amountMinor — which is why the ledger-failure recovery
// message quotes it inside the re-run command.
func refundAmountDecimal(amountMinor int64, scale int) string {
	if scale <= 0 {
		return strconv.FormatInt(amountMinor, 10)
	}
	digits := strconv.FormatInt(amountMinor, 10)
	for len(digits) <= scale {
		digits = "0" + digits
	}
	cut := len(digits) - scale
	return digits[:cut] + "." + digits[cut:]
}

// refundAmountLabel renders minor units as a major-unit decimal with the
// currency code, using integer string math (no float rounding).
func refundAmountLabel(amountMinor int64, currency string, scale int) string {
	return refundAmountDecimal(amountMinor, scale) + " " + currency
}

// refundIdempotencyKey builds the deterministic provider dedup key. See the
// file header: this is what makes the ledger-failure re-run a safe no-op at
// the provider. The pathological "two legitimate identical partial refunds"
// of one order collapses into a single provider refund — deliberately: on a
// money-out surface, blocking an exotic legitimate repeat beats ever risking
// a double payout.
func refundIdempotencyKey(orderID, amountMinor int64, paymentID string) string {
	return fmt.Sprintf("refund:%d:%d:%s", orderID, amountMinor, paymentID)
}

// refundCallbackData builds the confirm-tap payload for one order/amount.
func refundCallbackData(orderID, amountMinor int64) string {
	return fmt.Sprintf("%s%d:%d", refundCallbackPrefix, orderID, amountMinor)
}

// parseRefundCallback decodes admin:refund:<orderID>:<amountMinor>. Anything
// but exactly two positive integers is inert.
func parseRefundCallback(data string) (orderID, amountMinor int64, ok bool) {
	rest, found := strings.CutPrefix(data, refundCallbackPrefix)
	if !found {
		return 0, 0, false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	orderID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || orderID <= 0 {
		return 0, 0, false
	}
	amountMinor, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || amountMinor <= 0 {
		return 0, 0, false
	}
	return orderID, amountMinor, true
}

// refundPlan is one fully rebuilt refund execution: the ledger fact (with the
// pre-execution identity — the provider refund id replaces ExternalID after
// the money moved) plus the rail-specific execution inputs.
type refundPlan struct {
	rail          string
	fact          storage.Refund
	key           string // deterministic idempotency key (card rails)
	paymentIntent string // stripe only: resolved from the checkout session
}

// handleRefundCommand renders the first tap: gate, rail dispatch, amount
// resolution, ledger dry-run and the confirm card. Non-admins get nothing.
func (b *Bot) handleRefundCommand(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	send := func(text string) {
		b.sendOrEditStyled(msg.Chat.ID, 0, text, "", StyledKeyboard{})
	}

	args := strings.Fields(msg.CommandArguments())
	if len(args) == 0 || len(args) > 2 {
		send(b.t(lang, "admin_refund_usage"))
		return
	}
	orderID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || orderID <= 0 {
		send(b.t(lang, "admin_refund_usage"))
		return
	}
	rawAmount := ""
	parsedAmount := 0.0
	if len(args) == 2 {
		rawAmount = args[1]
		parsedAmount, err = strconv.ParseFloat(rawAmount, 64)
		if err != nil || math.IsNaN(parsedAmount) || math.IsInf(parsedAmount, 0) || parsedAmount <= 0 {
			send(b.t(lang, "admin_refund_usage"))
			return
		}
	}

	ctx, cancel := b.handlerCtx()
	defer cancel()
	order, err := b.order.GetOrder(ctx, orderID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			send(b.i18n.Tf(lang, "admin_order_not_found", orderID))
		} else {
			b.logger.Error("refund: load order", "order_id", orderID, "error", err)
			send(b.t(lang, "error_short"))
		}
		return
	}
	if !refundableOrder(order) {
		send(b.i18n.Tf(lang, "admin_refund_not_paid", orderID, refundOrderStateLabel(order)))
		return
	}
	rail := storage.CanonicalPaymentProvider(order.PaymentMethod)
	if refundIsManualRail(rail) {
		send(b.i18n.Tf(lang, "admin_refund_manual", rail))
		return
	}
	fullMinor, currency, scale, ok := refundRailMoney(order, rail)
	if !ok {
		send(b.i18n.Tf(lang, "admin_refund_not_paid", orderID, refundOrderStateLabel(order)))
		return
	}
	amountMinor := fullMinor
	if rawAmount != "" {
		if rail == storage.PaymentMethodStars {
			// Telegram has no partial star refund: an explicit amount is only
			// accepted when it names the full frozen total.
			if parsedAmount != float64(order.TotalStars) {
				send(b.i18n.Tf(lang, "admin_refund_partial_stars", orderID, int64(order.TotalStars)))
				return
			}
		} else {
			amountMinor = int64(math.Round(parsedAmount * math.Pow10(scale)))
			if amountMinor <= 0 {
				send(b.t(lang, "admin_refund_usage"))
				return
			}
			if amountMinor > fullMinor {
				send(b.t(lang, "admin_refund_conflict"))
				return
			}
		}
	}

	plan, outcome, err := b.prepareRefund(ctx, order, rail, amountMinor, currency, scale)
	if err != nil {
		send(b.refundFailureText(lang, orderID, err))
		return
	}
	if outcome == storage.PaymentIngressReplay {
		send(b.i18n.Tf(lang, "admin_refund_done", plan.fact.ExternalID, orderID, refundPaymentStateLabel(order)))
		return
	}
	if outcome != storage.PaymentIngressApply {
		send(b.t(lang, "admin_refund_conflict"))
		return
	}
	kb := StyledKeyboard{{Btn(b.t(lang, "admin_refund_confirm_btn"), refundCallbackData(orderID, amountMinor))}}
	b.sendOrEditStyled(msg.Chat.ID, 0,
		b.i18n.Tf(lang, "admin_refund_card", orderID, rail, refundAmountLabel(amountMinor, currency, scale)), "", kb)
}

// onAdminRefundCallback dispatches an already admin-gated admin:refund: tap.
func (b *Bot) onAdminRefundCallback(chatID int64, msgID int, userID int64, data, lang string) {
	orderID, amountMinor, ok := parseRefundCallback(data)
	if !ok {
		return
	}
	b.onAdminRefundConfirm(chatID, msgID, userID, orderID, amountMinor, lang)
}

// onAdminRefundConfirm is the second tap: RELOAD the order, REBUILD the
// refund fact from scratch and RE-PREVIEW it against the ledger (the same
// TOCTOU discipline as the payreview confirm), then execute provider-first
// and record second.
func (b *Bot) onAdminRefundConfirm(chatID int64, msgID int, adminID, orderID, amountMinor int64, lang string) {
	// Serialize confirm executions process-wide. Telegram can deliver a
	// double-tap as two concurrent updates; the replay check below is only
	// authoritative when no sibling confirm is mid-flight — critical on the
	// balance rail, whose credit has no provider-side dedup.
	b.refundMu.Lock()
	defer b.refundMu.Unlock()

	ctx, cancel := b.handlerCtx()
	defer cancel()
	render := func(text string) {
		b.sendOrEditStyled(chatID, msgID, text, "", StyledKeyboard{})
	}

	order, err := b.order.GetOrder(ctx, orderID)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			b.logger.Error("refund confirm: load order", "order_id", orderID, "error", err)
		}
		render(b.t(lang, "admin_refund_conflict"))
		return
	}
	rail := storage.CanonicalPaymentProvider(order.PaymentMethod)
	if refundIsManualRail(rail) {
		// The card never offers a confirm button for manual rails; a crafted
		// callback re-renders the instructions and executes nothing.
		render(b.i18n.Tf(lang, "admin_refund_manual", rail))
		return
	}
	// Replay BEFORE the refundable gate: a successful first confirm flips the
	// payment state, so the double-confirm must answer with the recorded
	// refund (the provider is NEVER called again) instead of a bare
	// not-refundable rejection.
	if recorded, hit := b.findRecordedRefund(ctx, order, rail, amountMinor); hit {
		render(b.i18n.Tf(lang, "admin_refund_done", recorded.ExternalID, orderID, refundPaymentStateLabel(order)))
		return
	}
	if !refundableOrder(order) {
		render(b.i18n.Tf(lang, "admin_refund_not_paid", orderID, refundOrderStateLabel(order)))
		return
	}
	fullMinor, currency, scale, ok := refundRailMoney(order, rail)
	if !ok {
		render(b.i18n.Tf(lang, "admin_refund_not_paid", orderID, refundOrderStateLabel(order)))
		return
	}
	if rail == storage.PaymentMethodStars && amountMinor != fullMinor {
		render(b.i18n.Tf(lang, "admin_refund_partial_stars", orderID, int64(order.TotalStars)))
		return
	}
	if amountMinor <= 0 || amountMinor > fullMinor {
		render(b.t(lang, "admin_refund_conflict"))
		return
	}

	plan, outcome, err := b.prepareRefund(ctx, order, rail, amountMinor, currency, scale)
	if err != nil {
		render(b.refundFailureText(lang, orderID, err))
		return
	}
	if outcome == storage.PaymentIngressReplay {
		render(b.i18n.Tf(lang, "admin_refund_done", plan.fact.ExternalID, orderID, refundPaymentStateLabel(order)))
		return
	}
	if outcome != storage.PaymentIngressApply {
		render(b.t(lang, "admin_refund_conflict"))
		return
	}

	// Ordering ruling: the provider refund executes FIRST. A failure here
	// leaves the ledger untouched and the order unchanged.
	refundID, err := b.executeRefund(ctx, plan, order, adminID)
	if err != nil {
		b.logger.Error("refund: provider execution failed",
			"order_id", orderID, "rail", rail, "amount_minor", amountMinor, "error", err)
		render(b.i18n.Tf(lang, "admin_refund_provider_failed", orderID, err.Error()))
		return
	}
	plan.fact.ExternalID = refundID

	audit := storage.PaymentIngressAudit{
		Actor:  fmt.Sprintf("admin:%d", adminID),
		Reason: "admin /refund",
	}
	if err := b.payLedger.IngestProviderRefund(ctx, plan.fact, audit); err != nil &&
		!errors.Is(err, storage.ErrPaymentNeedsReview) {
		// The money has LEFT. Never silent — and the remedy is RAIL-AWARE:
		// stripe/yookassa collapse a re-run into the original refund via the
		// deterministic idempotency key and balance skips the credit when the
		// order_refund audit row exists, so for them the /refund re-run only
		// completes the ledger record. The re-run message carries the EXACT
		// command WITH the amount: after a PARTIAL refund an amount-less
		// re-run defaults to FULL, and the balance rail's per-order
		// order_refund identity would skip the credit while the ledger
		// recorded a full refund — books claim more out than moved. (Stars
		// are full-only, so the amount is implicit.) Stars have NO safe
		// re-run: Telegram rejects the repeat refundStarPayment, and the
		// flow would falsely report "nothing recorded, order unchanged"
		// while the money is out. The stars recovery is the ingest-stars
		// CLI, which records the refund with Telegram's authoritative
		// OccurredAt.
		b.logger.Error("refund: LEDGER RECORDING FAILED AFTER PROVIDER SUCCESS",
			"order_id", orderID, "rail", rail, "refund_id", refundID, "error", err)
		if rail == storage.PaymentMethodStars {
			render(b.i18n.Tf(lang, "admin_refund_ledger_failed_stars", refundID, orderID, err.Error(), refundCLIRecordLine))
		} else {
			render(b.i18n.Tf(lang, "admin_refund_ledger_failed_rerun", refundID, orderID, err.Error(),
				orderID, refundAmountDecimal(amountMinor, scale)))
		}
		return
	}
	// Settlement attribution (docs/payment-operations.md §12): the log actor
	// matches the durable payment_ingress_audits row written above.
	b.logger.Info("refund recorded",
		"order_id", orderID, "rail", rail, "refund_id", refundID, "actor", audit.Actor)
	// ErrPaymentNeedsReview from the ingest means the refund row committed but
	// a fully refunded subscription entitlement lacks provenance: recorded,
	// order quarantined for review. Report the fresh state truthfully.
	state := ""
	if fresh, ferr := b.order.GetOrder(ctx, orderID); ferr == nil {
		state = fresh.PaymentState
	} else {
		b.logger.Error("refund: reload state after ingest", "order_id", orderID, "error", ferr)
	}
	if state == "" {
		state = "-"
	}
	render(b.i18n.Tf(lang, "admin_refund_done", refundID, orderID, state))
}

// prepareRefund rebuilds the refund fact for one execution attempt and
// dry-runs the ledger preview without writing anything. Shared by the first
// tap and the confirm — the confirm re-runs the whole sequence.
func (b *Bot) prepareRefund(ctx context.Context, order *storage.Order, rail string, amountMinor int64, currency string, scale int) (*refundPlan, string, error) {
	if amountMinor <= 0 {
		return nil, "", errRefundAmountRange
	}
	if strings.TrimSpace(order.PaymentID) == "" {
		return nil, "", errRefundNoPaymentID
	}
	fullMinor, _, _, ok := refundRailMoney(order, rail)
	if !ok || amountMinor > fullMinor {
		return nil, "", errRefundAmountRange
	}
	occurredAt := time.Now().UTC()
	// The deterministic key doubles as the pre-execution preview identity on
	// the card rails: their real ExternalID (the provider's refund object id)
	// does not exist until the money moved, and the preview only uses the id
	// for the refunds-table identity lookup — the parent/cap/payer checks are
	// what the dry-run is for. Stars and balance have deterministic final
	// identities from the start.
	key := refundIdempotencyKey(order.ID, amountMinor, order.PaymentID)
	fact := storage.Refund{
		OrderID: order.ID, Provider: rail,
		ExternalID: key, PaymentExternalID: order.PaymentID,
		PayerID: order.UserID, AmountMinor: amountMinor,
		Currency: currency, Scale: scale, OccurredAt: occurredAt,
	}
	plan := &refundPlan{rail: rail, fact: fact, key: key}
	switch rail {
	case storage.PaymentMethodStars:
		// Telegram returns no refund identity, and the ledger pins the stars
		// refund identity to the capture: ExternalID == PaymentExternalID ==
		// the telegram_payment_charge_id (the same shape the launcher's
		// ingest-stars CLI records — Telegram's refund transaction shares the
		// charge id). A synthetic "stars-refund:<orderID>" id would violate
		// that identity rule and could never be recorded.
		plan.fact.ExternalID = order.PaymentID
	case storage.PaymentMethodStripe:
		session, err := b.stripe.GetCheckoutSession(ctx, order.PaymentID)
		if err != nil {
			return nil, "", fmt.Errorf("stripe session lookup: %w", err)
		}
		if strings.TrimSpace(session.PaymentIntent) == "" {
			return nil, "", errors.New("stripe session has no payment intent — not refundable via API")
		}
		plan.paymentIntent = session.PaymentIntent
	case storage.PaymentMethodBalance:
		plan.fact.ExternalID = fmt.Sprintf("balance-refund:%d", order.ID)
	case storage.PaymentMethodYooKassa:
		// The card-rail defaults (key placeholder) already fit.
	default:
		return nil, "", fmt.Errorf("refund: unsupported rail %q", rail)
	}

	outcome, err := b.payLedger.PreviewProviderRefundIngress(ctx, plan.fact)
	if err != nil {
		return plan, "", fmt.Errorf("refund preview: %w", err)
	}
	return plan, outcome, nil
}

// findRecordedRefund reports whether the ledger already holds a succeeded
// refund of exactly this money movement (same rail, same capture, same
// amount). It is the replay gate that keeps a double confirm — or a re-run
// after a ledger failure that DID record — from ever reaching the provider
// again. Matching on (rail, capture, amount) also blocks the pathological
// "two legitimate identical partial refunds" before the provider call, the
// same safer trade-off the deterministic key makes provider-side.
func (b *Bot) findRecordedRefund(ctx context.Context, order *storage.Order, rail string, amountMinor int64) (*storage.Refund, bool) {
	refunds, err := b.payLedger.ListRefunds(ctx, order.ID)
	if err != nil {
		// A read failure degrades to "no replay found" — which fails closed
		// anyway: the ledger preview immediately after reads the same
		// database, and its identity/cap checks stop the deterministic-id
		// rails (stars/balance) before execution; the card rails are covered
		// by the deterministic provider key.
		b.logger.Error("refund: list recorded refunds", "order_id", order.ID, "error", err)
		return nil, false
	}
	for i := range refunds {
		r := refunds[i]
		if r.Provider == rail && r.PaymentExternalID == order.PaymentID &&
			r.AmountMinor == amountMinor && r.Status == "succeeded" {
			return &r, true
		}
	}
	return nil, false
}

// executeRefund performs the provider-side money movement ONLY — no ledger
// write happens here (ordering ruling). The returned string is the identity
// the ledger records as the refund's ExternalID.
func (b *Bot) executeRefund(ctx context.Context, plan *refundPlan, order *storage.Order, adminID int64) (string, error) {
	switch plan.rail {
	case storage.PaymentMethodStars:
		// tgbotapi v5 has no typed refundStarPayment — the raw request path is
		// the house pattern (setChatMenuButton, forum-topic sends). MakeRequest
		// surfaces the response's ok flag as an error.
		if _, err := b.api.MakeRequest("refundStarPayment", tgbotapi.Params{
			"user_id":                    strconv.FormatInt(order.UserID, 10),
			"telegram_payment_charge_id": order.PaymentID,
		}); err != nil {
			return "", fmt.Errorf("stars refund: %w", err)
		}
		return order.PaymentID, nil

	case storage.PaymentMethodStripe:
		// The deterministic key is the double-refund firewall: a re-run after
		// a failed ledger write is collapsed by Stripe into the original
		// refund. The trade-off — two legit identical partial refunds blocked
		// provider-side — is the safer one for money-out (see
		// refundIdempotencyKey).
		result, err := b.stripe.CreateRefund(ctx, plan.paymentIntent, plan.fact.AmountMinor, plan.key)
		if err != nil {
			return "", err
		}
		if result.Status == "failed" {
			// Stripe created the object but it did not move money: no ledger
			// record, the operator sees the provider fact.
			return "", fmt.Errorf("stripe refund %s status failed", result.ID)
		}
		return result.ID, nil

	case storage.PaymentMethodYooKassa:
		// Same deterministic-key firewall as stripe (Idempotence-Key spelling).
		result, err := b.yookassa.CreateRefund(ctx, order.PaymentID, plan.fact.AmountMinor,
			fmt.Sprintf("Order #%d refund", order.ID), plan.key)
		if err != nil {
			return "", err
		}
		if result.Status == "canceled" {
			return "", fmt.Errorf("yookassa refund %s canceled", result.ID)
		}
		// A "pending" yookassa refund completes asynchronously; the ledger
		// fact records the initiation.
		return result.ID, nil

	case storage.PaymentMethodBalance:
		// The credit IS this rail's provider step. Its deterministic
		// order_refund:<orderID> audit type doubles as the idempotency
		// identity: a re-run after a failed ledger write finds the prior
		// credit and skips it, so the recovery path mints money exactly once
		// (mirrors the crash-window net check ConfirmBalancePayment uses on
		// the debit side).
		//
		// LOAD-BEARING COUPLING: this identity is per-ORDER, not per-amount.
		// It is only safe while refundableOrder's settled-only gate limits
		// the flow to ONE balance refund per order. If that gate is ever
		// relaxed (e.g. to allow partial-then-remainder), the txType MUST
		// become amount-scoped (order_refund:<orderID>:<amountMinor>) FIRST —
		// otherwise a second partial would skip the credit (identity already
		// exists) yet record a ledger refund: books claiming money that never
		// moved. See docs/payment-operations.md §11.
		txType := fmt.Sprintf("order_refund:%d", order.ID)
		exists, err := b.balances.BalanceTxExists(ctx, order.UserID, txType)
		if err != nil {
			return "", fmt.Errorf("balance refund probe: %w", err)
		}
		if !exists {
			if _, err := b.balances.AdjustBalance(ctx, order.UserID,
				float64(plan.fact.AmountMinor)/100, txType, adminID); err != nil {
				return "", fmt.Errorf("balance credit: %w", err)
			}
		}
		return fmt.Sprintf("balance-refund:%d", order.ID), nil
	}
	return "", fmt.Errorf("refund: unsupported rail %q", plan.rail)
}

// refundFailureText maps a prepareRefund failure to the operator message.
func (b *Bot) refundFailureText(lang string, orderID int64, err error) string {
	if errors.Is(err, errRefundAmountRange) {
		return b.t(lang, "admin_refund_conflict")
	}
	// Provider-side rejections (session lookup, missing payment intent) and
	// preview storage errors name the error; nothing was executed and nothing
	// was recorded.
	return b.i18n.Tf(lang, "admin_refund_provider_failed", orderID, err.Error())
}
