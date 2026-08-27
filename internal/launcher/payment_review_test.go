package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"shop_bot/internal/storage"
)

func TestPaymentReviewCLIListsPreviewsAndExplicitlyResolves(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shop.db")
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,status,order_state,payment_state,fulfillment_state)
		VALUES (42,5,100,'stars','pending','placed','pending','unfulfilled')`)
	if err != nil {
		t.Fatal(err)
	}
	orderID, _ := res.LastInsertId()
	ctx := context.Background()
	store := storage.NewSQLOrderStore(db)
	if err := store.UpdateOrderStatus(ctx, orderID, storage.OrderStatusPending, storage.OrderStatusPaid, "stars", "capture-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "capture-b", "second_charge"); !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	ledger := storage.NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, storage.Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "refund-b",
		PaymentExternalID: "capture-b", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	cases, err := ledger.ListPaymentReviews(ctx, "stars")
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	_ = db.Close()

	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(fmt.Sprintf("BOT_TOKEN=%s\nDB_PATH=%s\n", testToken, dbPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	baseOpts := func(out *bytes.Buffer) PaymentReviewOptions {
		return PaymentReviewOptions{
			EnvPath: envPath, BaseDir: dir, Out: out,
			LookupEnv: func(string) (string, bool) { return "", false },
		}
	}
	var listOut bytes.Buffer
	if code := RunPaymentReview(ctx, []string{"list", "--provider", "stars"}, baseOpts(&listOut)); code != 1 {
		t.Fatalf("list code=%d output=%q", code, listOut.String())
	}
	if !strings.Contains(listOut.String(), "event_ids=") || strings.Contains(listOut.String(), "capture-b") {
		t.Fatalf("list output is not redacted: %q", listOut.String())
	}

	args := []string{
		"resolve", "--provider", "stars", "--order", strconv.FormatInt(orderID, 10),
		"--state", "settled", "--actor", "operator:test", "--reason", "duplicate fully refunded",
	}
	for _, target := range cases[0].Targets {
		switch target.Kind {
		case storage.PaymentReviewTargetEvent:
			args = append(args, "--event", strconv.FormatInt(target.ID, 10))
		case storage.PaymentReviewTargetAnomaly:
			args = append(args, "--anomaly", strconv.FormatInt(target.ID, 10))
		}
	}
	var previewOut bytes.Buffer
	if code := RunPaymentReview(ctx, args, baseOpts(&previewOut)); code != 0 ||
		!strings.Contains(previewOut.String(), "No changes applied") {
		t.Fatalf("preview code=%d output=%q", code, previewOut.String())
	}
	checkDB, _ := storage.OpenReadOnly(dbPath)
	var resolutions int
	_ = checkDB.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions`).Scan(&resolutions)
	_ = checkDB.Close()
	if resolutions != 0 {
		t.Fatalf("preview wrote resolutions=%d", resolutions)
	}

	var wrongOut bytes.Buffer
	wrongArgs := append(append([]string{}, args...), "--apply", "--confirm-order", "999")
	if code := RunPaymentReview(ctx, wrongArgs, baseOpts(&wrongOut)); code != 2 {
		t.Fatalf("wrong confirmation code=%d output=%q", code, wrongOut.String())
	}
	var applyOut bytes.Buffer
	applyArgs := append(append([]string{}, args...), "--apply", "--confirm-order", strconv.FormatInt(orderID, 10))
	if code := RunPaymentReview(ctx, applyArgs, baseOpts(&applyOut)); code != 0 ||
		!strings.Contains(applyOut.String(), "resolved") {
		t.Fatalf("apply code=%d output=%q", code, applyOut.String())
	}

	finalDB, _ := storage.OpenReadOnly(dbPath)
	var state string
	_ = finalDB.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
	_ = finalDB.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions`).Scan(&resolutions)
	_ = finalDB.Close()
	if state != storage.PaymentStateSettled || resolutions != 2 {
		t.Fatalf("state=%s resolutions=%d", state, resolutions)
	}
	var finalList bytes.Buffer
	if code := RunPaymentReview(ctx, []string{"list", "--provider", "stars"}, baseOpts(&finalList)); code != 0 ||
		!strings.Contains(finalList.String(), "targets=0") {
		t.Fatalf("final list code=%d output=%q", code, finalList.String())
	}
}

func TestPaymentReviewCLIRequiresExplicitDecisionForLegacyNoAttempt(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shop.db")
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,status,order_state,payment_state,fulfillment_state)
		VALUES (42,5,100,'stars','paid','placed','needs_review','unfulfilled')`)
	if err != nil {
		t.Fatal(err)
	}
	orderID, _ := res.LastInsertId()
	res, err = db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint,proposed_order_id,provider,event_kind,external_id,amount_minor,currency,scale,reason)
		VALUES ('cli-legacy-no-attempt',?,'stars','captured','',100,'XTR',0,'legacy_capture_unverifiable')`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	anomalyID, _ := res.LastInsertId()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(fmt.Sprintf("BOT_TOKEN=%s\nDB_PATH=%s\n", testToken, dbPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	baseOpts := func(out *bytes.Buffer) PaymentReviewOptions {
		return PaymentReviewOptions{
			EnvPath: envPath, BaseDir: dir, Out: out,
			LookupEnv: func(string) (string, bool) { return "", false },
		}
	}
	baseArgs := []string{
		"resolve", "--provider", "stars", "--order", strconv.FormatInt(orderID, 10),
		"--anomaly", strconv.FormatInt(anomalyID, 10),
		"--actor", "operator:test", "--reason", "provider verified compensation",
	}

	var missingDecision bytes.Buffer
	args := append(append([]string{}, baseArgs...), "--state", "refunded")
	if code := RunPaymentReview(context.Background(), args, baseOpts(&missingDecision)); code != 1 {
		t.Fatalf("missing decision code=%d output=%q", code, missingDecision.String())
	}
	var unsafeSettled bytes.Buffer
	args = append(append([]string{}, baseArgs...), "--decision", "compensated", "--state", "settled")
	if code := RunPaymentReview(context.Background(), args, baseOpts(&unsafeSettled)); code != 1 {
		t.Fatalf("unsafe settled code=%d output=%q", code, unsafeSettled.String())
	}

	validArgs := append(append([]string{}, baseArgs...), "--decision", "compensated", "--state", "refunded")
	var preview bytes.Buffer
	if code := RunPaymentReview(context.Background(), validArgs, baseOpts(&preview)); code != 0 ||
		!strings.Contains(preview.String(), "decision=compensated") ||
		!strings.Contains(preview.String(), "No changes applied") {
		t.Fatalf("preview code=%d output=%q", code, preview.String())
	}
	var applyOut bytes.Buffer
	applyArgs := append(append([]string{}, validArgs...), "--apply", "--confirm-order", strconv.FormatInt(orderID, 10))
	if code := RunPaymentReview(context.Background(), applyArgs, baseOpts(&applyOut)); code != 0 ||
		!strings.Contains(applyOut.String(), "result=refunded") {
		t.Fatalf("apply code=%d output=%q", code, applyOut.String())
	}

	finalDB, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer finalDB.Close()
	var state, decision string
	if err := finalDB.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := finalDB.Conn().QueryRow(`SELECT decision FROM payment_resolutions
		WHERE target_kind='payment_anomaly' AND target_id=?`, anomalyID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if state != storage.PaymentStateRefunded || decision != "compensated" {
		t.Fatalf("state=%s decision=%s", state, decision)
	}
}

func TestPaymentReviewCLIListsProviderNeutralLegacyOrderTarget(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "unknown.db")
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,status,order_state,payment_state,fulfillment_state)
		VALUES (42,5,100,NULL,'paid','placed','needs_review','unfulfilled')`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	orderID, _ := res.LastInsertId()
	if _, err := db.Conn().Exec(`INSERT INTO order_events
		(order_id,event_type,from_state,to_state) VALUES (?,'payment.legacy_provider_unknown','settled','needs_review')`, orderID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	envPath := writeIngressCLIEnv(t, dir, dbPath)

	unknownOut, unknownCode := runIngressCLI(t, envPath, dir, nil,
		[]string{"list", "--provider", storage.PaymentReviewProviderUnknown})
	if unknownCode != 1 || !strings.Contains(unknownOut, "provider=unknown") ||
		!strings.Contains(unknownOut, "order_target="+strconv.FormatInt(orderID, 10)) ||
		!strings.Contains(unknownOut, "payment.legacy_provider_unknown") {
		t.Fatalf("unknown list code=%d output=%q", unknownCode, unknownOut)
	}
	assertIngressCLISecretsRedacted(t, unknownOut, testToken)

	starsOut, starsCode := runIngressCLI(t, envPath, dir, nil, []string{"list", "--provider", "stars"})
	if starsCode != 0 || !strings.Contains(starsOut, "targets=0") {
		t.Fatalf("stars list code=%d output=%q", starsCode, starsOut)
	}

	resolveArgs := []string{
		"resolve", "--provider", storage.PaymentReviewProviderUnknown,
		"--order", strconv.FormatInt(orderID, 10),
		"--order-target", strconv.FormatInt(orderID, 10),
		"--decision", "dismissed", "--state", storage.PaymentStateCancelled,
		"--actor", "operator:test", "--reason", "legacy row has no attributable provider",
	}
	previewOut, previewCode := runIngressCLI(t, envPath, dir, nil, resolveArgs)
	if previewCode != 0 || !strings.Contains(previewOut, "No changes applied") ||
		!strings.Contains(previewOut, "decision=dismissed") {
		t.Fatalf("neutral preview code=%d output=%q", previewCode, previewOut)
	}
	applyArgs := append(append([]string{}, resolveArgs...),
		"--apply", "--confirm-order", strconv.FormatInt(orderID, 10))
	applyOut, applyCode := runIngressCLI(t, envPath, dir, nil, applyArgs)
	if applyCode != 0 || !strings.Contains(applyOut, "targets=1") || !strings.Contains(applyOut, "result=cancelled") {
		t.Fatalf("neutral apply code=%d output=%q", applyCode, applyOut)
	}
	unknownOut, unknownCode = runIngressCLI(t, envPath, dir, nil,
		[]string{"list", "--provider", storage.PaymentReviewProviderUnknown})
	if unknownCode != 0 || !strings.Contains(unknownOut, "targets=0") {
		t.Fatalf("neutral final list code=%d output=%q", unknownCode, unknownOut)
	}
	finalDB, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer finalDB.Close()
	var status, state, decision string
	var attempts int
	if err := finalDB.Conn().QueryRow(`SELECT status,payment_state FROM orders WHERE id=?`, orderID).Scan(&status, &state); err != nil {
		t.Fatal(err)
	}
	if err := finalDB.Conn().QueryRow(`SELECT decision FROM payment_resolutions
		WHERE target_kind='order' AND target_id=?`, orderID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	_ = finalDB.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	if status != storage.OrderStatusCancelled || state != storage.PaymentStateCancelled ||
		decision != "dismissed" || attempts != 0 {
		t.Fatalf("status=%s state=%s decision=%s attempts=%d", status, state, decision, attempts)
	}
}
