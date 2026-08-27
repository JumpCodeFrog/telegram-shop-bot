package launcher

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"shop_bot/internal/storage"
)

const ingressProviderUnix int64 = 1_700_000_000

type ingressStarsClient struct {
	rows   []StarTransaction
	tokens []string
}

func (f *ingressStarsClient) ListStarTransactions(_ context.Context, token string, offset, limit int) ([]StarTransaction, error) {
	f.tokens = append(f.tokens, token)
	if offset != 0 || limit <= 0 {
		return nil, nil
	}
	if len(f.rows) <= limit {
		return append([]StarTransaction(nil), f.rows...), nil
	}
	return append([]StarTransaction(nil), f.rows[:limit]...), nil
}

func TestPaymentReviewIngestStarsCapturePreviewApplyGateAndExactReplay(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "capture.db")
	db, _, orderID, productID := seedIngressCLIOrder(t, dbPath, 100)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	transactionID := "provider-capture-do-not-print"
	client := &ingressStarsClient{rows: []StarTransaction{{
		ID: transactionID, Date: ingressProviderUnix, Amount: 100,
		Source: invoiceParty(strconv.FormatInt(orderID, 10), 42),
	}}}
	envPath := writeIngressCLIEnv(t, dir, dbPath)
	baseArgs := []string{
		"ingest-stars", "--kind", "capture", "--transaction", transactionID,
		"--order", strconv.FormatInt(orderID, 10), "--actor", "operator:test", "--reason", "provider-only capture",
	}
	wrongPayerClient := &ingressStarsClient{rows: []StarTransaction{{
		ID: transactionID, Date: ingressProviderUnix, Amount: 100,
		Source: invoiceParty(strconv.FormatInt(orderID, 10), 43),
	}}}
	wrongPayerOut, wrongPayerCode := runIngressCLI(t, envPath, dir, wrongPayerClient, baseArgs)
	if wrongPayerCode != 1 || !strings.Contains(wrongPayerOut, "local preview failed") {
		t.Fatalf("wrong payer code=%d output=%q", wrongPayerCode, wrongPayerOut)
	}
	assertIngressCLISecretsRedacted(t, wrongPayerOut, transactionID, testToken)
	assertCaptureCLIState(t, dbPath, orderID, productID, storage.PaymentStatePending, 0, 0)

	previewOut, previewCode := runIngressCLI(t, envPath, dir, client, baseArgs)
	if previewCode != 0 || !strings.Contains(previewOut, "outcome=quarantine") || !strings.Contains(previewOut, "No changes applied") {
		t.Fatalf("preview code=%d output=%q", previewCode, previewOut)
	}
	assertIngressCLISecretsRedacted(t, previewOut, transactionID, testToken)
	assertCaptureCLIState(t, dbPath, orderID, productID, storage.PaymentStatePending, 0, 0)

	wrongArgs := append(append([]string{}, baseArgs...), "--apply", "--confirm-order", strconv.FormatInt(orderID+1, 10))
	wrongOut, wrongCode := runIngressCLI(t, envPath, dir, client, wrongArgs)
	if wrongCode != 2 || !strings.Contains(wrongOut, "must exactly match") {
		t.Fatalf("wrong confirmation code=%d output=%q", wrongCode, wrongOut)
	}
	assertIngressCLISecretsRedacted(t, wrongOut, transactionID, testToken)
	assertCaptureCLIState(t, dbPath, orderID, productID, storage.PaymentStatePending, 0, 0)

	applyArgs := append(append([]string{}, baseArgs...), "--apply", "--confirm-order", strconv.FormatInt(orderID, 10))
	applyOut, applyCode := runIngressCLI(t, envPath, dir, client, applyArgs)
	if applyCode != 1 || !strings.Contains(applyOut, "Provider ingress quarantined") {
		t.Fatalf("apply code=%d output=%q", applyCode, applyOut)
	}
	assertIngressCLISecretsRedacted(t, applyOut, transactionID, testToken)
	assertCaptureCLIState(t, dbPath, orderID, productID, storage.PaymentStateNeedsReview, 1, 1)
	verifyIngressCaptureEvidence(t, dbPath, orderID, 42, time.Unix(ingressProviderUnix, 0).UTC())

	replayOut, replayCode := runIngressCLI(t, envPath, dir, client, applyArgs)
	if replayCode != 0 || !strings.Contains(replayOut, "outcome=replay") {
		t.Fatalf("replay code=%d output=%q", replayCode, replayOut)
	}
	assertIngressCLISecretsRedacted(t, replayOut, transactionID, testToken)
	assertCaptureCLIState(t, dbPath, orderID, productID, storage.PaymentStateNeedsReview, 1, 1)
	if len(client.tokens) == 0 {
		t.Fatal("provider client was not called")
	}
	for _, token := range client.tokens {
		if token != testToken {
			t.Fatalf("provider received unexpected token")
		}
	}
}

func TestPaymentReviewIngestStarsRefundPreviewConfirmationApplyAndGreenReconcile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "refund.db")
	db, store, orderID, _ := seedIngressCLIOrder(t, dbPath, 100)
	captureID := "local-capture-do-not-print"
	if err := store.UpdateOrderStatusWithPaymentFact(context.Background(), orderID, storage.OrderStatusPending, storage.OrderStatusPaid, storage.PaymentFact{
		Provider: storage.PaymentMethodStars, ExternalID: captureID, PayerID: 42,
		AmountMinor: 100, Currency: "XTR", Scale: 0, OccurredAt: time.Unix(ingressProviderUnix, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	payload := strconv.FormatInt(orderID, 10)
	client := &ingressStarsClient{rows: []StarTransaction{
		{ID: captureID, Date: ingressProviderUnix, Amount: 100, Source: invoiceParty(payload, 42)},
		{ID: captureID, Date: ingressProviderUnix + 60, Amount: -100, Receiver: invoiceParty(payload, 42)},
	}}
	envPath := writeIngressCLIEnv(t, dir, dbPath)
	baseArgs := []string{
		"ingest-stars", "--kind", "refund", "--transaction", captureID,
		"--order", strconv.FormatInt(orderID, 10), "--actor", "operator:test", "--reason", "provider-only refund",
	}
	legacyParentArgs := append(append([]string{}, baseArgs...), "--capture", captureID)
	legacyParentOut, legacyParentCode := runIngressCLI(t, envPath, dir, client, legacyParentArgs)
	if legacyParentCode != 2 || !strings.Contains(legacyParentOut, "invalid arguments") {
		t.Fatalf("legacy parent code=%d output=%q", legacyParentCode, legacyParentOut)
	}
	assertIngressCLISecretsRedacted(t, legacyParentOut, captureID, testToken)
	assertRefundCLIState(t, dbPath, orderID, storage.PaymentStateSettled, 0, 0)

	previewOut, previewCode := runIngressCLI(t, envPath, dir, client, baseArgs)
	if previewCode != 0 || !strings.Contains(previewOut, "outcome=apply") || !strings.Contains(previewOut, "No changes applied") {
		t.Fatalf("preview code=%d output=%q", previewCode, previewOut)
	}
	assertIngressCLISecretsRedacted(t, previewOut, captureID, testToken)
	assertRefundCLIState(t, dbPath, orderID, storage.PaymentStateSettled, 0, 0)

	wrongArgs := append(append([]string{}, baseArgs...), "--apply", "--confirm-order", strconv.FormatInt(orderID+1, 10))
	wrongOut, wrongCode := runIngressCLI(t, envPath, dir, client, wrongArgs)
	if wrongCode != 2 || !strings.Contains(wrongOut, "must exactly match") {
		t.Fatalf("wrong confirmation code=%d output=%q", wrongCode, wrongOut)
	}
	assertIngressCLISecretsRedacted(t, wrongOut, captureID, testToken)
	assertRefundCLIState(t, dbPath, orderID, storage.PaymentStateSettled, 0, 0)

	applyArgs := append(append([]string{}, baseArgs...), "--apply", "--confirm-order", strconv.FormatInt(orderID, 10))
	applyOut, applyCode := runIngressCLI(t, envPath, dir, client, applyArgs)
	if applyCode != 0 || !strings.Contains(applyOut, "Provider ingress applied") {
		t.Fatalf("apply code=%d output=%q", applyCode, applyOut)
	}
	assertIngressCLISecretsRedacted(t, applyOut, captureID, testToken)
	assertRefundCLIState(t, dbPath, orderID, storage.PaymentStateRefunded, 1, 1)
	checkDB, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var refundPayer int64
	var refundOccurred time.Time
	if err := checkDB.Conn().QueryRow(`SELECT payer_id, completed_at FROM refunds WHERE order_id=?`, orderID).
		Scan(&refundPayer, &refundOccurred); err != nil {
		checkDB.Close()
		t.Fatal(err)
	}
	_ = checkDB.Close()
	wantRefundTime := time.Unix(ingressProviderUnix+60, 0).UTC()
	if refundPayer != 42 || !refundOccurred.Equal(wantRefundTime) {
		t.Fatalf("refund payer_id=%d occurred_at=%s", refundPayer, refundOccurred)
	}

	var reconcileOut bytes.Buffer
	reconcileCode := RunStarsReconcile(context.Background(), StarsReconcileOptions{
		EnvPath: envPath, BaseDir: dir, Out: &reconcileOut,
		LookupEnv: func(string) (string, bool) { return "", false },
		Client:    client, MaxRows: 10, PageSize: 10,
	})
	if reconcileCode != 0 || !strings.Contains(reconcileOut.String(), "matched=2") ||
		!strings.Contains(reconcileOut.String(), "needs_review=0") || !strings.Contains(reconcileOut.String(), "complete=true") {
		t.Fatalf("reconcile code=%d output=%q", reconcileCode, reconcileOut.String())
	}
	assertIngressCLISecretsRedacted(t, reconcileOut.String(), captureID, testToken)
}

func verifyIngressCaptureEvidence(t *testing.T, dbPath string, orderID, payerID int64, occurredAt time.Time) {
	t.Helper()
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var storedPayer int64
	var storedOccurred time.Time
	if err := db.Conn().QueryRow(`SELECT payer_id, occurred_at FROM payment_attempts WHERE order_id=?`, orderID).
		Scan(&storedPayer, &storedOccurred); err != nil {
		t.Fatal(err)
	}
	if storedPayer != payerID || !storedOccurred.Equal(occurredAt) {
		t.Fatalf("payer_id=%d occurred_at=%s", storedPayer, storedOccurred)
	}
}

func seedIngressCLIOrder(t *testing.T, dbPath string, totalStars int) (*storage.DB, *storage.SQLOrderStore, int64, int64) {
	t.Helper()
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	category, err := db.Conn().ExecContext(ctx, `INSERT INTO categories (name) VALUES ('ingress')`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	categoryID, _ := category.LastInsertId()
	product, err := db.Conn().ExecContext(ctx, `INSERT INTO products
		(category_id, name, price_usd, price_stars, stock, is_active)
		VALUES (?, 'Ingress product', 5, ?, 9, 1)`, categoryID, totalStars)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	productID, _ := product.LastInsertId()
	store := storage.NewSQLOrderStore(db)
	orderID, err := store.CreateOrder(ctx, &storage.Order{
		UserID: 42, TotalUSD: 5, TotalStars: totalStars, Status: storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: productID, ProductName: "Ingress product", Quantity: 2, PriceUSD: 5}})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, store, orderID, productID
}

func writeIngressCLIEnv(t *testing.T, dir, dbPath string) string {
	t.Helper()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(fmt.Sprintf("BOT_TOKEN=%s\nDB_PATH=%s\n", testToken, dbPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	return envPath
}

func runIngressCLI(t *testing.T, envPath, dir string, client StarsTransactionLister, args []string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := RunPaymentReview(context.Background(), args, PaymentReviewOptions{
		EnvPath: envPath, BaseDir: dir, Out: &out,
		LookupEnv:   func(string) (string, bool) { return "", false },
		StarsClient: client, MaxRows: 10, PageSize: 10,
	})
	return out.String(), code
}

func assertCaptureCLIState(t *testing.T, dbPath string, orderID, productID int64, wantState string, wantAttempts, wantEvents int) {
	t.Helper()
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status, paymentState, fulfillmentState, paymentID string
	var stock, attempts, events, audits int
	if err := db.Conn().QueryRow(`SELECT status, payment_state, fulfillment_state, COALESCE(payment_id, '')
		FROM orders WHERE id=?`, orderID).Scan(&status, &paymentState, &fulfillmentState, &paymentID); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT stock FROM products WHERE id=?`, productID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=? AND event_kind='captured'`, orderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits
		WHERE order_id=? AND event_kind='captured' AND actor='operator:test' AND reason='provider-only capture'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if status != storage.OrderStatusPending || paymentState != wantState ||
		fulfillmentState != storage.FulfillmentStateUnfulfilled || paymentID != "" || stock != 9 ||
		attempts != wantAttempts || events != wantEvents || audits != wantEvents {
		t.Fatalf("status=%s payment=%s fulfillment=%s payment_id=%q stock=%d attempts=%d events=%d audits=%d",
			status, paymentState, fulfillmentState, paymentID, stock, attempts, events, audits)
	}
}

func assertRefundCLIState(t *testing.T, dbPath string, orderID int64, wantState string, wantRefunds, wantEvents int) {
	t.Helper()
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	var refunds, events, audits int
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds WHERE order_id=?`, orderID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=? AND event_kind='refunded'`, orderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_ingress_audits
		WHERE order_id=? AND event_kind='refunded' AND actor='operator:test' AND reason='provider-only refund'`, orderID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if state != wantState || refunds != wantRefunds || events != wantEvents || audits != wantRefunds {
		t.Fatalf("state=%s refunds=%d events=%d audits=%d", state, refunds, events, audits)
	}
}

func assertIngressCLISecretsRedacted(t *testing.T, output string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(output, secret) {
			t.Fatalf("secret leaked in operator output: %q", output)
		}
	}
}

func invoiceParty(orderID string, payerID int64) *StarTransactionPartner {
	partner := &StarTransactionPartner{
		Type: "user", TransactionType: "invoice_payment", InvoicePayload: orderID,
	}
	partner.User.ID = payerID
	return partner
}
