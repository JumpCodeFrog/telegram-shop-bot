package launcher

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

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
