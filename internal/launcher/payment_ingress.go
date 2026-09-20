package launcher

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"shop_bot/internal/storage"
)

func runPaymentReviewIngestStars(ctx context.Context, args []string, opts PaymentReviewOptions) int {
	defaults := DefaultPaymentReviewOptions()
	fs := flag.NewFlagSet("payment-review ingest-stars", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	kind := fs.String("kind", "", "capture or refund")
	transactionID := fs.String("transaction", "", "exact Telegram transaction id")
	orderID := fs.Int64("order", -1, "exact order id")
	actor := fs.String("actor", "", "operator identity")
	reason := fs.String("reason", "", "operator reason")
	apply := fs.Bool("apply", false, "persist the authenticated provider fact")
	confirmOrder := fs.Int64("confirm-order", -1, "must exactly equal --order when applying")
	maxRows := fs.Int("max-rows", choosePositive(opts.MaxRows, defaults.MaxRows), "maximum provider rows")
	pageSize := fs.Int("page-size", choosePositive(opts.PageSize, defaults.PageSize), "provider page size")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || (*kind != "capture" && *kind != "refund") ||
		strings.TrimSpace(*transactionID) == "" || *orderID <= 0 || strings.TrimSpace(*actor) == "" || len(strings.TrimSpace(*actor)) > 128 ||
		strings.TrimSpace(*reason) == "" || len(strings.TrimSpace(*reason)) > 512 || *maxRows < 1 || *maxRows > 100000 || *pageSize < 1 || *pageSize > 100 {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: invalid arguments")
		return 2
	}
	cfg, dbPath, ok := loadPaymentReviewConfig(opts)
	if !ok {
		return 1
	}
	client := opts.StarsClient
	if client == nil {
		client = defaults.StarsClient
	}
	providerRow, err := findExactStarTransaction(ctx, client, cfg.BotToken, *transactionID, *kind, *maxRows, *pageSize)
	if err != nil || !providerRow.PayloadValid || providerRow.OrderID != *orderID ||
		providerRow.AmountMinor <= 0 || providerRow.NanostarAmount != 0 || providerRow.PayerID <= 0 || providerRow.OccurredAt.IsZero() {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: authoritative row missing, ambiguous, or invalid")
		return 1
	}
	previewDB, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: database error")
		return 1
	}
	outcome := ""
	if *kind == "capture" {
		outcome, err = storage.NewSQLOrderStore(previewDB).PreviewProviderCaptureIngress(ctx, *orderID, storage.PaymentFact{
			Provider: storage.PaymentMethodStars, ExternalID: providerRow.ExternalID,
			PayerID: providerRow.PayerID, AmountMinor: providerRow.AmountMinor, Currency: "XTR", Scale: 0,
			OccurredAt: providerRow.OccurredAt,
		})
	} else {
		outcome, err = storage.NewSQLPaymentLedgerStore(previewDB).PreviewProviderRefundIngress(ctx, storage.Refund{
			OrderID: *orderID, Provider: storage.PaymentMethodStars, ExternalID: providerRow.ExternalID,
			PaymentExternalID: providerRow.ExternalID, PayerID: providerRow.PayerID,
			AmountMinor: providerRow.AmountMinor, Currency: "XTR", Scale: 0, OccurredAt: providerRow.OccurredAt,
		})
	}
	_ = previewDB.Close()
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: local preview failed")
		return 1
	}
	fmt.Fprintf(paymentReviewOut(opts), "Provider ingress preview: kind=%s order=%d amount=%d outcome=%s\n",
		*kind, *orderID, providerRow.AmountMinor, safeReviewCode(outcome))
	if !*apply {
		fmt.Fprintf(paymentReviewOut(opts), "No changes applied; rerun with --apply --confirm-order=%d\n", *orderID)
		return 0
	}
	if *confirmOrder != *orderID {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: --confirm-order must exactly match --order")
		return 2
	}
	writeDB, err := storage.OpenReadWriteExisting(dbPath)
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: database error")
		return 1
	}
	defer writeDB.Close()
	if *kind == "capture" {
		err = storage.NewSQLOrderStore(writeDB).IngestProviderCapture(ctx, *orderID, storage.PaymentFact{
			Provider: storage.PaymentMethodStars, ExternalID: providerRow.ExternalID,
			PayerID: providerRow.PayerID, AmountMinor: providerRow.AmountMinor, Currency: "XTR", Scale: 0,
			OccurredAt: providerRow.OccurredAt,
		}, storage.PaymentIngressAudit{Actor: *actor, Reason: *reason})
	} else {
		err = storage.NewSQLPaymentLedgerStore(writeDB).IngestProviderRefund(ctx, storage.Refund{
			OrderID: *orderID, Provider: storage.PaymentMethodStars, ExternalID: providerRow.ExternalID,
			PaymentExternalID: providerRow.ExternalID, PayerID: providerRow.PayerID,
			AmountMinor: providerRow.AmountMinor, Currency: "XTR", Scale: 0, OccurredAt: providerRow.OccurredAt,
		}, storage.PaymentIngressAudit{Actor: *actor, Reason: *reason})
	}
	if err == nil {
		fmt.Fprintf(paymentReviewOut(opts), "Provider ingress applied: kind=%s order=%d outcome=%s\n",
			*kind, *orderID, safeReviewCode(outcome))
		return 0
	}
	if errors.Is(err, storage.ErrPaymentNeedsReview) || errors.Is(err, storage.ErrNotFound) ||
		errors.Is(err, storage.ErrPaymentIdentityConflict) || errors.Is(err, storage.ErrRefundExceedsPayment) {
		fmt.Fprintf(paymentReviewOut(opts), "Provider ingress quarantined: kind=%s order=%d\n", *kind, *orderID)
		return 1
	}
	fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: database error")
	return 1
}

// providerIngressRail pins the money tuple a payerless rail settles in: the
// operator supplies the actual amount, the rail itself supplies the currency
// and scale, so a flag can never mix tuples across rails.
type providerIngressRail struct {
	currency string
	scale    int
}

var providerIngressRails = map[string]providerIngressRail{
	storage.PaymentMethodYooKassa:    {currency: "RUB", scale: 2},
	storage.PaymentMethodStripe:      {currency: "USD", scale: 2},
	storage.PaymentMethodTON:         {currency: "TON", scale: 9},
	storage.PaymentMethodNowpayments: {currency: "USD", scale: 2},
}

// runPaymentReviewIngestProvider ingests an operator-authenticated capture on
// the payerless rails (card providers and on-chain/IPN crypto). Unlike
// ingest-stars there is no provider API to refetch: the operator vouches for
// the fact (dashboard receipt, tonviewer transfer), so the fact is previewed
// against the local ledger and applied only behind the confirmation gate. A
// fact that matches a pending order's rail money settles through the same
// storage gate the webhook/polling paths use; anything else is quarantined
// as durable review evidence instead of settling.
func runPaymentReviewIngestProvider(ctx context.Context, args []string, opts PaymentReviewOptions) int {
	fs := flag.NewFlagSet("payment-review ingest-provider", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	provider := fs.String("provider", "", "yookassa, stripe, ton, or nowpayments")
	orderID := fs.Int64("order", -1, "exact order id")
	amountMinor := fs.Int64("amount-minor", 0, "actual received amount in the rail's minor units")
	currency := fs.String("currency", "", "rail currency: RUB for yookassa, USD for stripe/nowpayments, TON for ton")
	externalID := fs.String("external-id", "", "provider capture identity (never printed)")
	occurredAtRaw := fs.String("occurred-at", "", "provider timestamp: RFC3339 or unix seconds")
	actor := fs.String("actor", "", "operator identity")
	reason := fs.String("reason", "", "operator reason")
	apply := fs.Bool("apply", false, "persist the authenticated provider fact")
	confirmOrder := fs.Int64("confirm-order", -1, "must exactly equal --order when applying")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: invalid arguments")
		return 2
	}
	normalizedProvider := strings.ToLower(strings.TrimSpace(*provider))
	rail, ok := providerIngressRails[normalizedProvider]
	if !ok {
		switch normalizedProvider {
		case storage.PaymentMethodStars:
			// Stars facts are authenticated against the Telegram Bot API;
			// the dedicated flow owns that rail.
			fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: stars captures belong to payment-review ingest-stars")
		case storage.PaymentMethodBalance:
			// Balance captures are synthetic facts minted by the admin panel
			// (/setbalance adjustments and the checkout debit); there is no
			// external provider statement to ingest.
			fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: balance captures are synthetic admin-panel facts, not provider ingress")
		default:
			fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: unsupported provider")
		}
		return 2
	}
	if *currency != rail.currency {
		fmt.Fprintf(paymentReviewOut(opts), "Provider ingress: currency does not match the %s rail (%s)\n",
			normalizedProvider, rail.currency)
		return 2
	}
	occurredAt, timeErr := parseProviderIngressTime(*occurredAtRaw)
	if timeErr != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: invalid occurred-at (want RFC3339 or unix seconds)")
		return 2
	}
	if *orderID <= 0 || *amountMinor <= 0 || strings.TrimSpace(*externalID) == "" || len(*externalID) > 256 ||
		strings.TrimSpace(*actor) == "" || len(strings.TrimSpace(*actor)) > 128 ||
		strings.TrimSpace(*reason) == "" || len(strings.TrimSpace(*reason)) > 512 {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: invalid arguments")
		return 2
	}
	fact := storage.PaymentFact{
		Provider: normalizedProvider, ExternalID: strings.TrimSpace(*externalID),
		// These rails carry no Telegram payer identity, so the fact keeps
		// PayerID 0 — exactly what the payer predicate accepts for them.
		PayerID: 0, AmountMinor: *amountMinor, Currency: rail.currency, Scale: rail.scale,
		OccurredAt: occurredAt,
	}
	_, dbPath, ok := loadPaymentReviewConfig(opts)
	if !ok {
		return 1
	}
	previewDB, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: database error")
		return 1
	}
	previewStore := storage.NewSQLOrderStore(previewDB)
	outcome, err := previewStore.PreviewProviderCaptureIngress(ctx, *orderID, fact)
	why := "exact_fact_exists"
	if err == nil && outcome == storage.PaymentIngressQuarantine {
		// Every fresh capture identity previews as quarantine, even one that
		// would settle cleanly. Split "would settle" from "must quarantine"
		// with the rail rules; the apply gate re-validates authoritatively.
		var order *storage.Order
		order, err = previewStore.GetOrder(ctx, *orderID)
		if err == nil {
			var settleable bool
			settleable, why = providerCaptureSettleable(order, fact)
			if settleable {
				outcome = storage.PaymentIngressApply
			}
		}
	}
	_ = previewDB.Close()
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: local preview failed")
		return 1
	}
	fmt.Fprintf(paymentReviewOut(opts), "Provider ingress preview: kind=capture provider=%s order=%d amount=%d outcome=%s why=%s\n",
		fact.Provider, *orderID, fact.AmountMinor, safeReviewCode(outcome), safeReviewCode(why))
	if !*apply {
		fmt.Fprintf(paymentReviewOut(opts), "No changes applied; rerun with --apply --confirm-order=%d\n", *orderID)
		return 0
	}
	if *confirmOrder != *orderID {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: --confirm-order must exactly match --order")
		return 2
	}
	writeDB, err := storage.OpenReadWriteExisting(dbPath)
	if err != nil {
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: database error")
		return 1
	}
	defer writeDB.Close()
	if outcome == storage.PaymentIngressApply {
		err = storage.NewSQLOrderStore(writeDB).UpdateOrderStatusWithPaymentFact(ctx, *orderID,
			storage.OrderStatusPending, storage.OrderStatusPaid, fact)
		if err == nil {
			fmt.Fprintf(paymentReviewOut(opts), "Provider ingress applied: kind=capture provider=%s order=%d outcome=%s\n",
				fact.Provider, *orderID, safeReviewCode(outcome))
			return 0
		}
		if errors.Is(err, storage.ErrOrderStatusConflict) {
			// The order moved between preview and apply; the settlement
			// transaction rolled back and nothing was written.
			fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: order state changed; re-run the preview")
			return 1
		}
		if errors.Is(err, storage.ErrPaymentIdentityConflict) || errors.Is(err, storage.ErrPaymentNeedsReview) {
			fmt.Fprintf(paymentReviewOut(opts), "Provider ingress quarantined: kind=capture provider=%s order=%d\n",
				fact.Provider, *orderID)
			return 1
		}
		fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: settlement failed; order unchanged")
		return 1
	}
	err = storage.NewSQLOrderStore(writeDB).IngestProviderCapture(ctx, *orderID, fact,
		storage.PaymentIngressAudit{Actor: *actor, Reason: *reason})
	if err == nil {
		// An exact replay of a settled (or already resolved) fact is a no-op.
		fmt.Fprintf(paymentReviewOut(opts), "Provider ingress applied: kind=capture provider=%s order=%d outcome=%s\n",
			fact.Provider, *orderID, safeReviewCode(outcome))
		return 0
	}
	if errors.Is(err, storage.ErrPaymentNeedsReview) || errors.Is(err, storage.ErrPaymentIdentityConflict) ||
		errors.Is(err, storage.ErrNotFound) {
		fmt.Fprintf(paymentReviewOut(opts), "Provider ingress quarantined: kind=capture provider=%s order=%d\n",
			fact.Provider, *orderID)
		return 1
	}
	fmt.Fprintln(paymentReviewOut(opts), "Provider ingress: database error")
	return 1
}

// providerCaptureSettleable reports whether a fresh payerless-rail fact would
// settle the pending order, mirroring the receipt-layer rail rules
// (OrderService.ConfirmPaymentReceipt): exact frozen money on the card/IPN
// rails, overpay-tolerant on the TON rail. The apply gate re-validates every
// rule authoritatively; this check only chooses the apply path.
func providerCaptureSettleable(order *storage.Order, fact storage.PaymentFact) (bool, string) {
	if order.Status != storage.OrderStatusPending ||
		(order.PaymentState != "" && order.PaymentState != storage.PaymentStatePending) {
		return false, "order_not_pending"
	}
	switch fact.Provider {
	case storage.PaymentMethodYooKassa:
		if order.TotalRUB <= 0 || math.IsNaN(order.TotalRUB) || math.IsInf(order.TotalRUB, 0) ||
			fact.AmountMinor != int64(math.Round(order.TotalRUB*100)) {
			return false, "amount_mismatch"
		}
	case storage.PaymentMethodStripe, storage.PaymentMethodNowpayments:
		if order.TotalUSD <= 0 || math.IsNaN(order.TotalUSD) || math.IsInf(order.TotalUSD, 0) ||
			fact.AmountMinor != int64(math.Round(order.TotalUSD*100)) {
			return false, "amount_mismatch"
		}
	case storage.PaymentMethodTON:
		if order.TotalTonNano <= 0 || fact.AmountMinor < order.TotalTonNano {
			return false, "amount_below_order_total"
		}
	default:
		return false, "unsupported_provider"
	}
	return true, "matches_order_money"
}

// parseProviderIngressTime accepts the shapes an operator can copy from a
// provider dashboard or a block explorer: RFC3339, or unix seconds (the shape
// the Stars flow reads from the Telegram API).
func parseProviderIngressTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("empty occurred-at")
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC(), nil
	}
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}, errors.New("invalid occurred-at")
	}
	return time.Unix(secs, 0).UTC(), nil
}

func findExactStarTransaction(ctx context.Context, client StarsTransactionLister, token, transactionID, kind string, maxRows, pageSize int) (storage.ProviderTransaction, error) {
	wantKind := storage.PaymentEventCaptured
	if kind == "refund" {
		wantKind = storage.PaymentEventRefunded
	}
	var match storage.ProviderTransaction
	matches := 0
	complete := false
	for offset := 0; offset < maxRows; offset += pageSize {
		limit := pageSize
		if remaining := maxRows - offset; remaining < limit {
			limit = remaining
		}
		page, err := client.ListStarTransactions(ctx, token, offset, limit)
		if err != nil {
			return storage.ProviderTransaction{}, err
		}
		for _, row := range page {
			normalized, ok := normalizeStarTransaction(row)
			if ok && normalized.Kind == wantKind && normalized.ExternalID == transactionID {
				match = normalized
				matches++
			}
		}
		if len(page) < limit {
			complete = true
			break
		}
	}
	if !complete {
		probe, err := client.ListStarTransactions(ctx, token, maxRows, 1)
		if err != nil {
			return storage.ProviderTransaction{}, err
		}
		complete = len(probe) == 0
	}
	if !complete || matches != 1 {
		return storage.ProviderTransaction{}, errors.New("provider ingress window incomplete or identity ambiguous")
	}
	return match, nil
}

func choosePositive(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
