package launcher

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"shop_bot/internal/storage"
)

const providerIngressTONExternalID = "1700000000001:abcdef0123456789"

func providerIngressArgs(orderID int64, provider, amountMinor, currency, externalID, occurredAt string) []string {
	return []string{
		"ingest-provider", "--provider", provider,
		"--order", strconv.FormatInt(orderID, 10),
		"--amount-minor", amountMinor, "--currency", currency,
		"--external-id", externalID, "--occurred-at", occurredAt,
		"--actor", "operator:test", "--reason", "provider-only capture",
	}
}

// seedPayerlessCLIOrder mirrors seedIngressCLIOrder for the payerless rails:
// one product with stock 9 and a pending order of quantity 2 whose money
// snapshot matches the named provider rail.
func seedPayerlessCLIOrder(t *testing.T, dbPath, provider string, totalUSD, totalRUB float64, totalTonNano int64) (*storage.DB, int64, int64) {
	t.Helper()
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('provider-ingress')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	product, err := db.Conn().ExecContext(ctx, `INSERT INTO products
		(category_id, name, price_usd, price_stars, stock, is_active)
		VALUES (1, 'Provider ingress product', 23, 0, 9, 1)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	productID, _ := product.LastInsertId()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalUSD: totalUSD, TotalRUB: totalRUB, TotalTonNano: totalTonNano,
		PaymentMethod: provider, Status: storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: productID, ProductName: "Provider ingress product", Quantity: 2, PriceUSD: 5}})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, orderID, productID
}

func providerIngressAttemptCount(t *testing.T, dbPath string, orderID int64) int {
	t.Helper()
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attempts int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	return attempts
}

func TestPaymentReviewIngestProviderRejectsInvalidFlags(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "provider-flags.db")
	db, orderID, _ := seedPayerlessCLIOrder(t, dbPath, storage.PaymentMethodTON, 0, 0, 1_500_000_000)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	envPath := writeIngressCLIEnv(t, dir, dbPath)
	valid := providerIngressArgs(orderID, "ton", "1500000000", "TON", providerIngressTONExternalID, "1700000000")

	legs := []struct {
		name string
		args []string
		want string
	}{
		{"unknown provider", withProvider(valid, "foo"), "unsupported provider"},
		{"stars belongs to its own flow", withProvider(valid, "stars"), "ingest-stars"},
		{"balance is admin-panel domain", withProvider(valid, "balance"), "balance"},
		{"ton rail settles TON", withCurrency(valid, "USD"), "currency"},
		{"yookassa rail settles RUB", withCurrency(withProvider(valid, "yookassa"), "USD"), "currency"},
		{"stripe rail settles USD", withCurrency(withProvider(valid, "stripe"), "RUB"), "currency"},
		{"nowpayments rail settles USD", withCurrency(withProvider(valid, "nowpayments"), "TON"), "currency"},
		{"missing external id", withoutFlag(valid, "--external-id"), "invalid arguments"},
		{"missing occurred-at", withoutFlag(valid, "--occurred-at"), "occurred-at"},
		{"bad occurred-at", withOccurredAt(valid, "soon"), "occurred-at"},
		{"zero amount", withAmount(valid, "0"), "invalid arguments"},
		{"missing actor", withoutFlag(valid, "--actor"), "invalid arguments"},
		{"missing reason", withoutFlag(valid, "--reason"), "invalid arguments"},
	}
	for _, leg := range legs {
		out, code := runIngressCLI(t, envPath, dir, nil, leg.args)
		if code != 2 || !strings.Contains(out, leg.want) || strings.Contains(out, "Usage:") {
			t.Fatalf("%s: code=%d output=%q", leg.name, code, out)
		}
		assertIngressCLISecretsRedacted(t, out, providerIngressTONExternalID, testToken)
	}

	wrongConfirm := append(append([]string{}, valid...), "--apply", "--confirm-order", strconv.FormatInt(orderID+1, 10))
	confirmOut, confirmCode := runIngressCLI(t, envPath, dir, nil, wrongConfirm)
	if confirmCode != 2 || !strings.Contains(confirmOut, "must exactly match") {
		t.Fatalf("wrong confirmation code=%d output=%q", confirmCode, confirmOut)
	}
	assertIngressCLISecretsRedacted(t, confirmOut, providerIngressTONExternalID, testToken)

	if attempts := providerIngressAttemptCount(t, dbPath, orderID); attempts != 0 {
		t.Fatalf("invalid flags wrote attempts=%d", attempts)
	}
	assertProviderIngressOrderState(t, dbPath, orderID, storage.OrderStatusPending, storage.PaymentStatePending)
}

func TestPaymentReviewIngestProviderTONSettlesAndReplays(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "provider-ton.db")
	db, orderID, productID := seedPayerlessCLIOrder(t, dbPath, storage.PaymentMethodTON, 0, 0, 1_500_000_000)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	envPath := writeIngressCLIEnv(t, dir, dbPath)
	base := providerIngressArgs(orderID, "ton", "1500000000", "TON", providerIngressTONExternalID, "1700000000")

	previewOut, previewCode := runIngressCLI(t, envPath, dir, nil, base)
	if previewCode != 0 || !strings.Contains(previewOut, "provider=ton") ||
		!strings.Contains(previewOut, "outcome=apply") || !strings.Contains(previewOut, "No changes applied") {
		t.Fatalf("preview code=%d output=%q", previewCode, previewOut)
	}
	assertIngressCLISecretsRedacted(t, previewOut, providerIngressTONExternalID, testToken)
	assertProviderIngressOrderState(t, dbPath, orderID, storage.OrderStatusPending, storage.PaymentStatePending)
	assertProviderIngressSettledFact(t, dbPath, orderID, productID, 0, 9)

	applyArgs := append(append([]string{}, base...), "--apply", "--confirm-order", strconv.FormatInt(orderID, 10))
	applyOut, applyCode := runIngressCLI(t, envPath, dir, nil, applyArgs)
	if applyCode != 0 || !strings.Contains(applyOut, "Provider ingress applied") ||
		!strings.Contains(applyOut, "outcome=apply") {
		t.Fatalf("apply code=%d output=%q", applyCode, applyOut)
	}
	assertIngressCLISecretsRedacted(t, applyOut, providerIngressTONExternalID, testToken)
	assertProviderIngressOrderState(t, dbPath, orderID, storage.OrderStatusPaid, storage.PaymentStateSettled)
	assertProviderIngressSettledFact(t, dbPath, orderID, productID, 1, 7)

	replayOut, replayCode := runIngressCLI(t, envPath, dir, nil, applyArgs)
	if replayCode != 0 || !strings.Contains(replayOut, "outcome=replay") {
		t.Fatalf("replay code=%d output=%q", replayCode, replayOut)
	}
	assertIngressCLISecretsRedacted(t, replayOut, providerIngressTONExternalID, testToken)
	assertProviderIngressOrderState(t, dbPath, orderID, storage.OrderStatusPaid, storage.PaymentStateSettled)
	assertProviderIngressSettledFact(t, dbPath, orderID, productID, 1, 7)
}

// TestPaymentReviewIngestProviderTONUnderpayRejectedAtStorage pins the
// closed split (roadmap 4.9 + 4.10): an underpaying ton fact is rejected by
// the storage fact gate itself (validatePaymentFact enforces >= the frozen
// snapshot for every caller), so the CLI preview fails with an actionable
// amount-mismatch message naming both numbers, exit code 1, and NOTHING is
// written — no quarantine evidence, order untouched. (Formerly the fact
// gate waved underpay through and the CLI quarantined it as durable review
// evidence; the automatic ton polling worker still records underpaid
// on-chain transfers as shop-layer anomalies.)
func TestPaymentReviewIngestProviderTONUnderpayRejectedAtStorage(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "provider-ton-underpay.db")
	db, orderID, productID := seedPayerlessCLIOrder(t, dbPath, storage.PaymentMethodTON, 0, 0, 1_500_000_000)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	envPath := writeIngressCLIEnv(t, dir, dbPath)
	underpayID := "1700000000002:fedcba9876543210"
	base := providerIngressArgs(orderID, "ton", "1499999999", "TON", underpayID, "1700000000")
	wantMismatch := "amount mismatch: fact 1499999999 TON vs order expected 1500000000 TON"

	previewOut, previewCode := runIngressCLI(t, envPath, dir, nil, base)
	if previewCode != 1 || !strings.Contains(previewOut, wantMismatch) {
		t.Fatalf("preview code=%d output=%q", previewCode, previewOut)
	}
	assertIngressCLISecretsRedacted(t, previewOut, underpayID, testToken)
	assertProviderIngressOrderState(t, dbPath, orderID, storage.OrderStatusPending, storage.PaymentStatePending)
	if attempts := providerIngressAttemptCount(t, dbPath, orderID); attempts != 0 {
		t.Fatalf("preview wrote attempts=%d", attempts)
	}

	applyArgs := append(append([]string{}, base...), "--apply", "--confirm-order", strconv.FormatInt(orderID, 10))
	applyOut, applyCode := runIngressCLI(t, envPath, dir, nil, applyArgs)
	if applyCode != 1 || !strings.Contains(applyOut, wantMismatch) ||
		strings.Contains(applyOut, "quarantined") {
		t.Fatalf("apply code=%d output=%q", applyCode, applyOut)
	}
	assertIngressCLISecretsRedacted(t, applyOut, underpayID, testToken)
	// The rejected fact leaves the order exactly as it was: pending, with
	// no attempt, event, audit, or stock movement.
	assertProviderIngressOrderState(t, dbPath, orderID, storage.OrderStatusPending, storage.PaymentStatePending)
	assertProviderIngressSettledFact(t, dbPath, orderID, productID, 0, 9)
}

// TestPaymentReviewIngestProviderPreviewOperationalErrors pins the remaining
// sentinel mappings of the ingest-provider preview error path (roadmap
// 4.10): ErrInvalidMoney names the rail whose frozen order total is missing
// or non-positive, and a non-money operational error (unknown order) keeps
// the generic message. Both exit 1 without writing anything.
func TestPaymentReviewIngestProviderPreviewOperationalErrors(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "provider-preview-errors.db")
	// TotalTonNano 0: an order created while TON was disabled carries no
	// valid frozen total for the ton rail.
	db, orderID, _ := seedPayerlessCLIOrder(t, dbPath, storage.PaymentMethodTON, 0, 0, 0)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	envPath := writeIngressCLIEnv(t, dir, dbPath)

	invalidMoney := providerIngressArgs(orderID, "ton", "1500000000", "TON", providerIngressTONExternalID, "1700000000")
	out, code := runIngressCLI(t, envPath, dir, nil, invalidMoney)
	if code != 1 || !strings.Contains(out, "no valid frozen total for the ton rail") {
		t.Fatalf("invalid money code=%d output=%q", code, out)
	}
	assertIngressCLISecretsRedacted(t, out, providerIngressTONExternalID, testToken)
	if attempts := providerIngressAttemptCount(t, dbPath, orderID); attempts != 0 {
		t.Fatalf("invalid money wrote attempts=%d", attempts)
	}

	missingOrder := providerIngressArgs(999999, "ton", "1500000000", "TON", providerIngressTONExternalID, "1700000000")
	out, code = runIngressCLI(t, envPath, dir, nil, missingOrder)
	if code != 1 || !strings.Contains(out, "local preview failed") {
		t.Fatalf("missing order code=%d output=%q", code, out)
	}
	assertIngressCLISecretsRedacted(t, out, providerIngressTONExternalID, testToken)
}

func TestPaymentReviewIngestProviderYooKassaAmountMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "provider-yookassa.db")
	db, orderID, _ := seedPayerlessCLIOrder(t, dbPath, storage.PaymentMethodYooKassa, 0, 1849.08, 0)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	envPath := writeIngressCLIEnv(t, dir, dbPath)
	yooID := "2c5f8f42-000f-5000-9000-1d4d2b5f0001"
	mismatch := providerIngressArgs(orderID, "yookassa", "184907", "RUB", yooID, "1700000000")

	previewOut, previewCode := runIngressCLI(t, envPath, dir, nil, mismatch)
	if previewCode != 1 ||
		!strings.Contains(previewOut, "amount mismatch: fact 184907 RUB vs order expected 184908 RUB") {
		t.Fatalf("mismatch preview code=%d output=%q", previewCode, previewOut)
	}
	assertIngressCLISecretsRedacted(t, previewOut, yooID, testToken)
	if attempts := providerIngressAttemptCount(t, dbPath, orderID); attempts != 0 {
		t.Fatalf("mismatch wrote attempts=%d", attempts)
	}
	assertProviderIngressOrderState(t, dbPath, orderID, storage.OrderStatusPending, storage.PaymentStatePending)

	// The exact kopeck amount settles through the same CLI.
	exact := providerIngressArgs(orderID, "yookassa", "184908", "RUB", yooID, "1700000000")
	applyArgs := append(append([]string{}, exact...), "--apply", "--confirm-order", strconv.FormatInt(orderID, 10))
	applyOut, applyCode := runIngressCLI(t, envPath, dir, nil, applyArgs)
	if applyCode != 0 || !strings.Contains(applyOut, "outcome=apply") {
		t.Fatalf("apply code=%d output=%q", applyCode, applyOut)
	}
	assertIngressCLISecretsRedacted(t, applyOut, yooID, testToken)
	assertProviderIngressOrderState(t, dbPath, orderID, storage.OrderStatusPaid, storage.PaymentStateSettled)
}

func TestPaymentReviewIngestProviderStripeSettlesRFC3339(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "provider-stripe.db")
	db, orderID, _ := seedPayerlessCLIOrder(t, dbPath, storage.PaymentMethodStripe, 23, 0, 0)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	envPath := writeIngressCLIEnv(t, dir, dbPath)
	stripeID := "cs_test_provider_ingress_capture"
	base := providerIngressArgs(orderID, "stripe", "2300", "USD", stripeID, "2023-11-14T22:13:20Z")
	applyArgs := append(append([]string{}, base...), "--apply", "--confirm-order", strconv.FormatInt(orderID, 10))
	applyOut, applyCode := runIngressCLI(t, envPath, dir, nil, applyArgs)
	if applyCode != 0 || !strings.Contains(applyOut, "outcome=apply") {
		t.Fatalf("apply code=%d output=%q", applyCode, applyOut)
	}
	assertIngressCLISecretsRedacted(t, applyOut, stripeID, testToken)
	assertProviderIngressOrderState(t, dbPath, orderID, storage.OrderStatusPaid, storage.PaymentStateSettled)

	checkDB, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer checkDB.Close()
	var payerID, amount int64
	var currency string
	var scale int
	var occurredAt time.Time
	if err := checkDB.Conn().QueryRow(`SELECT payer_id, amount_minor, currency, scale, occurred_at
		FROM payment_attempts WHERE provider='stripe' AND external_id=?`, stripeID).
		Scan(&payerID, &amount, &currency, &scale, &occurredAt); err != nil {
		t.Fatal(err)
	}
	if payerID != 0 || amount != 2300 || currency != "USD" || scale != 2 ||
		!occurredAt.Equal(time.Unix(ingressProviderUnix, 0).UTC()) {
		t.Fatalf("payer=%d amount=%d currency=%s scale=%d occurred_at=%s", payerID, amount, currency, scale, occurredAt)
	}
}

func withProvider(args []string, provider string) []string {
	return replaceFlag(args, "--provider", provider)
}

func withCurrency(args []string, currency string) []string {
	return replaceFlag(args, "--currency", currency)
}

func withAmount(args []string, amount string) []string {
	return replaceFlag(args, "--amount-minor", amount)
}

func withOccurredAt(args []string, occurredAt string) []string {
	return replaceFlag(args, "--occurred-at", occurredAt)
}

func replaceFlag(args []string, name, value string) []string {
	out := append([]string{}, args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == name {
			out[i+1] = value
			return out
		}
	}
	return append(out, name, value)
}

func withoutFlag(args []string, name string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func assertProviderIngressOrderState(t *testing.T, dbPath string, orderID int64, wantStatus, wantPaymentState string) {
	t.Helper()
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status, paymentState string
	if err := db.Conn().QueryRow(`SELECT status, payment_state FROM orders WHERE id=?`, orderID).
		Scan(&status, &paymentState); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || paymentState != wantPaymentState {
		t.Fatalf("status=%s payment_state=%s", status, paymentState)
	}
}

func assertProviderIngressSettledFact(t *testing.T, dbPath string, orderID, productID int64, wantAttempts, wantStock int) {
	t.Helper()
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var stock, attempts, events, audits int
	if err := db.Conn().QueryRow(`SELECT stock FROM products WHERE id=?`, productID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=? AND event_kind='captured'`, orderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits WHERE order_id=?`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if stock != wantStock || attempts != wantAttempts || events != wantAttempts || audits != 0 {
		t.Fatalf("stock=%d attempts=%d events=%d audits=%d", stock, attempts, events, audits)
	}
	if wantAttempts == 0 {
		return
	}
	var payerID, amount int64
	var status, currency, paymentID string
	var scale int
	var occurredAt time.Time
	if err := db.Conn().QueryRow(`SELECT payer_id, amount_minor, currency, scale, status, occurred_at
		FROM payment_attempts WHERE order_id=?`, orderID).
		Scan(&payerID, &amount, &currency, &scale, &status, &occurredAt); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COALESCE(payment_id, '') FROM orders WHERE id=?`, orderID).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if payerID != 0 || amount != 1_500_000_000 || currency != "TON" || scale != 9 || status != "succeeded" ||
		!occurredAt.Equal(time.Unix(ingressProviderUnix, 0).UTC()) || paymentID != providerIngressTONExternalID {
		t.Fatalf("payer=%d amount=%d currency=%s scale=%d status=%s occurred_at=%s payment_id=%q",
			payerID, amount, currency, scale, status, occurredAt, paymentID)
	}
}
