package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPaymentReviewResolutionMovesReconcileRedToGreen(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "resolve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "capture-b", "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "capture-b",
		PaymentExternalID: "capture-b", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	providerRows := []ProviderTransaction{
		{Kind: PaymentEventCaptured, ExternalID: "capture-a", OrderID: orderID, PayloadValid: true, AmountMinor: 100, Currency: "XTR", Scale: 0, PayerID: 42, OccurredAt: paymentEventOccurredAt(t, db, PaymentEventCaptured, "capture-a")},
		{Kind: PaymentEventCaptured, ExternalID: "capture-b", OrderID: orderID, PayloadValid: true, AmountMinor: 100, Currency: "XTR", Scale: 0, PayerID: 42, OccurredAt: paymentEventOccurredAt(t, db, PaymentEventCaptured, "capture-b")},
		{Kind: PaymentEventRefunded, ExternalID: "capture-b", OrderID: orderID, PayloadValid: true, AmountMinor: 100, Currency: "XTR", Scale: 0, PayerID: 42, OccurredAt: paymentEventOccurredAt(t, db, PaymentEventRefunded, "capture-b")},
	}
	red, err := ledger.Reconcile(ctx, "stars", providerRows, true)
	if err != nil {
		t.Fatal(err)
	}
	if red.ProviderOnly != 0 || red.LocalOnly != 0 || red.NeedsReview == 0 {
		t.Fatalf("red report=%+v", red)
	}

	cases, err := ledger.ListPaymentReviews(ctx, "stars")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || cases[0].OrderID != orderID || len(cases[0].Targets) != 2 {
		t.Fatalf("review cases=%+v", cases)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: "stars", Actor: "operator:test",
		Reason: "duplicate charge fully refunded", ResultingPaymentState: PaymentStateSettled,
	}
	for _, target := range cases[0].Targets {
		switch target.Kind {
		case PaymentReviewTargetEvent:
			resolution.EventIDs = append(resolution.EventIDs, target.ID)
		case PaymentReviewTargetAnomaly:
			resolution.AnomalyIDs = append(resolution.AnomalyIDs, target.ID)
		}
	}
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); err != nil {
		t.Fatalf("preview error=%v", err)
	}
	partial := resolution
	partial.EventIDs = partial.EventIDs[:1]
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, partial); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("partial preview error=%v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatal(err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("exact resolution replay error=%v", err)
	}

	green, err := ledger.Reconcile(ctx, "stars", providerRows, true)
	if err != nil {
		t.Fatal(err)
	}
	if green.Matched != 3 || green.ProviderOnly != 0 || green.LocalOnly != 0 ||
		green.AmountMismatch != 0 || green.NeedsReview != 0 {
		t.Fatalf("green report=%+v", green)
	}
	var state string
	var events, refunds, resolutions int
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_events WHERE order_id=?`, orderID).Scan(&events)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM refunds WHERE order_id=?`, orderID).Scan(&refunds)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions WHERE order_id=?`, orderID).Scan(&resolutions)
	if state != PaymentStateSettled || events != 3 || refunds != 1 || resolutions != 2 {
		t.Fatalf("state=%s events=%d refunds=%d resolutions=%d", state, events, refunds, resolutions)
	}
	if _, err := db.Conn().Exec(`DELETE FROM payment_resolutions WHERE order_id=?`, orderID); err == nil {
		t.Fatal("payment resolution deletion unexpectedly succeeded")
	}
}

func TestPaymentReviewResolutionRejectsTargetsChangedAfterPreview(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "resolution-race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "capture-a", "late_capture"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "refund-a",
		PaymentExternalID: "capture-a", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	cases, err := ledger.ListPaymentReviews(ctx, "stars")
	if err != nil || len(cases) != 1 || len(cases[0].Targets) != 2 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: "stars",
		Actor: "operator:test", Reason: "reviewed", ResultingPaymentState: PaymentStateCancelled,
	}
	for _, target := range cases[0].Targets {
		if target.Kind == PaymentReviewTargetEvent {
			resolution.EventIDs = append(resolution.EventIDs, target.ID)
		}
	}
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "capture-b", "late_capture"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("changed target set error=%v", err)
	}
	var count int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions`).Scan(&count)
	if count != 0 {
		t.Fatalf("partial resolutions=%d", count)
	}
}

func TestPaymentReviewResolutionRejectsUncompensatedCapture(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "resolution-uncompensated.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "capture-unrefunded", "late_capture"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	cases, err := ledger.ListPaymentReviews(ctx, "stars")
	if err != nil || len(cases) != 1 || len(cases[0].Targets) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: "stars", EventIDs: []int64{cases[0].Targets[0].ID},
		Actor: "operator:test", Reason: "reviewed without refund", ResultingPaymentState: PaymentStateCancelled,
	}
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("uncompensated preview error=%v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("uncompensated apply error=%v", err)
	}
	var state string
	var resolutions int
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions WHERE order_id=?`, orderID).Scan(&resolutions)
	if state != PaymentStateNeedsReview || resolutions != 0 {
		t.Fatalf("state=%s resolutions=%d", state, resolutions)
	}
}

func TestPaymentReviewResolutionClosesFullyCompensatedCaptureWithoutSettledBaseline(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "resolution-compensated-only.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	if _, err := db.Conn().Exec(`UPDATE orders SET status='paid' WHERE id=?`, orderID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "capture-compensated", "legacy_capture_recovered"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "refund-compensated",
		PaymentExternalID: "capture-compensated", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	cases, err := ledger.ListPaymentReviews(ctx, "stars")
	if err != nil || len(cases) != 1 || len(cases[0].Targets) != 2 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: "stars", Actor: "operator:test",
		Reason: "recovered capture fully refunded", ResultingPaymentState: PaymentStateRefunded,
	}
	for _, target := range cases[0].Targets {
		if target.Kind == PaymentReviewTargetEvent {
			resolution.EventIDs = append(resolution.EventIDs, target.ID)
		}
	}
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); err != nil {
		t.Fatalf("preview=%v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("resolve=%v", err)
	}
	var state, status string
	if err := db.Conn().QueryRow(`SELECT payment_state,status FROM orders WHERE id=?`, orderID).Scan(&state, &status); err != nil {
		t.Fatal(err)
	}
	if state != PaymentStateRefunded || status != OrderStatusPaid {
		t.Fatalf("state=%s status=%s", state, status)
	}
}

func TestResolvedPaymentAnomalyExactReplayIsNoOp(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "resolved-anomaly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture"); err != nil {
		t.Fatal(err)
	}
	anomaly := PaymentAnomaly{
		ProposedOrderID: orderID, Provider: "stars", ExternalID: "mismatch",
		AmountMinor: 100, Currency: "XTR", Scale: 0, Reason: "receipt_mismatch",
	}
	if err := store.RecordPaymentAnomaly(ctx, anomaly); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	initial, err := ledger.ListPaymentReviews(ctx, "stars")
	if err != nil || len(initial) != 1 || len(initial[0].Targets) != 1 {
		t.Fatalf("initial cases=%+v err=%v", initial, err)
	}
	unsafe := PaymentReviewResolution{
		OrderID: orderID, Provider: "stars", AnomalyIDs: []int64{initial[0].Targets[0].ID},
		Actor: "operator:test", Reason: "no provider evidence", ResultingPaymentState: PaymentStateSettled,
	}
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, unsafe); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("unproven money anomaly preview=%v", err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "mismatch", "authenticated_capture_recovered"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "mismatch-refund",
		PaymentExternalID: "mismatch", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	cases, err := ledger.ListPaymentReviews(ctx, "stars")
	if err != nil || len(cases) != 1 || len(cases[0].Targets) != 3 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		Actor: "operator:test", Reason: "mismatch reviewed", ResultingPaymentState: PaymentStateSettled,
		OrderID: orderID, Provider: "stars",
	}
	for _, target := range cases[0].Targets {
		switch target.Kind {
		case PaymentReviewTargetEvent:
			resolution.EventIDs = append(resolution.EventIDs, target.ID)
		case PaymentReviewTargetAnomaly:
			resolution.AnomalyIDs = append(resolution.AnomalyIDs, target.ID)
		}
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordPaymentAnomaly(ctx, anomaly); err != nil {
		t.Fatalf("resolved anomaly replay error=%v", err)
	}
	var state string
	var anomalies int
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, orderID).Scan(&anomalies)
	cases, err = ledger.ListPaymentReviews(ctx, "stars")
	if err != nil || state != PaymentStateSettled || anomalies != 1 || len(cases) != 0 {
		t.Fatalf("state=%s anomalies=%d cases=%+v err=%v", state, anomalies, cases, err)
	}
}

func TestCrossProviderReviewsKeepProjectionQuarantinedUntilBothResolve(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "cross-provider.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture-a"); err != nil {
		t.Fatal(err)
	}
	for _, fact := range []struct {
		provider, capture, refund, currency string
		amount, scale                       int64
	}{
		{provider: "stars", capture: "stars-b", refund: "stars-r", currency: "XTR", amount: 100},
		{provider: "crypto", capture: "crypto-b", refund: "crypto-r", currency: "USD", amount: 1250, scale: 2},
	} {
		if err := store.RecordUnexpectedPayment(ctx, orderID, fact.provider, fact.capture, "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
			t.Fatalf("capture %s error=%v", fact.provider, err)
		}
		if err := NewSQLPaymentLedgerStore(db).RecordRefund(ctx, Refund{
			OrderID: orderID, Provider: fact.provider, ExternalID: fact.refund,
			PaymentExternalID: fact.capture, AmountMinor: fact.amount,
			Currency: fact.currency, Scale: int(fact.scale),
		}); err != nil {
			t.Fatalf("refund %s: %v", fact.provider, err)
		}
	}
	ledger := NewSQLPaymentLedgerStore(db)
	resolutionFor := func(provider string) PaymentReviewResolution {
		cases, err := ledger.ListPaymentReviews(ctx, provider)
		if err != nil || len(cases) != 1 {
			t.Fatalf("provider=%s cases=%+v err=%v", provider, cases, err)
		}
		r := PaymentReviewResolution{
			OrderID: orderID, Provider: provider, Actor: "operator:test",
			Reason: "duplicate fully refunded", ResultingPaymentState: PaymentStateSettled,
		}
		for _, target := range cases[0].Targets {
			if target.Kind == PaymentReviewTargetEvent {
				r.EventIDs = append(r.EventIDs, target.ID)
			} else if target.Kind == PaymentReviewTargetAnomaly {
				r.AnomalyIDs = append(r.AnomalyIDs, target.ID)
			}
		}
		return r
	}
	stars := resolutionFor("stars")
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, stars); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("partial preview accepted false final state: %v", err)
	}
	stars.ResultingPaymentState = PaymentStateNeedsReview
	preview, err := ledger.PreviewPaymentReviewResolution(ctx, stars)
	if err != nil || preview.RemainingTargets != 2 {
		t.Fatalf("stars preview=%+v err=%v", preview, err)
	}
	if err := ledger.ResolvePaymentReview(ctx, stars); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("partial resolution error=%v", err)
	}
	var state string
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
	if state != PaymentStateNeedsReview {
		t.Fatalf("state after one provider=%s", state)
	}
	var recordedState string
	if err := db.Conn().QueryRow(`SELECT DISTINCT resulting_payment_state FROM payment_resolutions WHERE provider='stars' AND order_id=?`, orderID).Scan(&recordedState); err != nil {
		t.Fatal(err)
	}
	if recordedState != PaymentStateNeedsReview {
		t.Fatalf("partial resolution audit state=%s", recordedState)
	}
	crypto := resolutionFor("crypto")
	if err := ledger.ResolvePaymentReview(ctx, crypto); err != nil {
		t.Fatalf("final provider resolution=%v", err)
	}
	_ = db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state)
	if state != PaymentStateSettled {
		t.Fatalf("final state=%s", state)
	}
}

func TestPaymentReviewOrphanAnomaliesResolveByExactTarget(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "orphan-exact-resolution.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := NewSQLOrderStore(db)
	for _, externalID := range []string{"orphan-a", "orphan-b"} {
		err := store.RecordPaymentAnomaly(ctx, PaymentAnomaly{
			Provider: PaymentMethodStars, EventKind: PaymentEventCaptured,
			ExternalID: externalID, AmountMinor: 100, Currency: "XTR", Scale: 0,
			Reason: "provider_verified_unknown_order",
		})
		if !errors.Is(err, ErrPaymentNeedsReview) {
			t.Fatalf("record %s: %v", externalID, err)
		}
	}
	ledger := NewSQLPaymentLedgerStore(db)
	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodStars)
	if err != nil || len(cases) != 2 || len(cases[0].Targets) != 1 || len(cases[1].Targets) != 1 {
		t.Fatalf("orphan cases=%+v err=%v", cases, err)
	}
	targetID := cases[0].Targets[0].ID
	resolution := PaymentReviewResolution{
		OrderID: 0, Provider: PaymentMethodStars, AnomalyIDs: []int64{targetID},
		Actor: "operator:test", Reason: "provider verified external compensation",
	}
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("no-attempt orphan accepted without explicit decision: %v", err)
	}
	resolution.Decision = "compensated"
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); err != nil {
		t.Fatalf("exact orphan preview: %v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("exact orphan resolve: %v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("exact orphan replay: %v", err)
	}
	cases, err = ledger.ListPaymentReviews(ctx, PaymentMethodStars)
	if err != nil || len(cases) != 1 || len(cases[0].Targets) != 1 || cases[0].Targets[0].ID == targetID {
		t.Fatalf("independent orphan remained cases=%+v err=%v", cases, err)
	}
	var decision string
	if err := db.Conn().QueryRow(`SELECT decision FROM payment_resolutions
		WHERE target_kind='payment_anomaly' AND target_id=?`, targetID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if decision != "compensated" {
		t.Fatalf("decision=%s", decision)
	}
}

func TestPaymentReviewLegacyNoAttemptCannotBecomeSettled(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "legacy-no-attempt-resolution.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, orderID, _ := seedLedgerOrder(t, db, 100)
	if _, err := db.Conn().Exec(`UPDATE orders
		SET status='paid', payment_method='stars', payment_state='needs_review' WHERE id=?`, orderID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint, proposed_order_id, provider, event_kind, external_id,
		 amount_minor, currency, scale, reason)
		VALUES ('legacy-no-attempt', ?, 'stars', 'captured', '', 100, 'XTR', 0,
		        'legacy_capture_unverifiable')`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	anomalyID, _ := res.LastInsertId()
	ledger := NewSQLPaymentLedgerStore(db)
	unsafe := PaymentReviewResolution{
		OrderID: orderID, Provider: PaymentMethodStars, AnomalyIDs: []int64{anomalyID},
		Decision: "compensated", Actor: "operator:test", Reason: "verified legacy compensation",
		ResultingPaymentState: PaymentStateSettled,
	}
	if _, err := ledger.PreviewPaymentReviewResolution(context.Background(), unsafe); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("legacy no-attempt became settled: %v", err)
	}
	unsafe.ResultingPaymentState = PaymentStateRefunded
	unsafe.Decision = ""
	if _, err := ledger.PreviewPaymentReviewResolution(context.Background(), unsafe); !errors.Is(err, ErrPaymentReviewConflict) {
		t.Fatalf("legacy no-attempt accepted without explicit decision: %v", err)
	}
	unsafe.Decision = "compensated"
	if _, err := ledger.PreviewPaymentReviewResolution(context.Background(), unsafe); err != nil {
		t.Fatalf("compensated preview: %v", err)
	}
	if err := ledger.ResolvePaymentReview(context.Background(), unsafe); err != nil {
		t.Fatalf("compensated resolve: %v", err)
	}
	withoutDecision := unsafe
	withoutDecision.Decision = ""
	if err := ledger.ResolvePaymentReview(context.Background(), withoutDecision); err == nil {
		t.Fatal("resolved no-attempt anomaly replayed without its explicit decision")
	}
	if err := ledger.ResolvePaymentReview(context.Background(), unsafe); err != nil {
		t.Fatalf("exact compensated replay: %v", err)
	}
	var state, status, decision string
	var attempts int
	if err := db.Conn().QueryRow(`SELECT payment_state,status FROM orders WHERE id=?`, orderID).Scan(&state, &status); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT decision FROM payment_resolutions
		WHERE target_kind='payment_anomaly' AND target_id=?`, anomalyID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	if state != PaymentStateRefunded || status != OrderStatusPaid || decision != "compensated" || attempts != 0 {
		t.Fatalf("state=%s status=%s decision=%s attempts=%d", state, status, decision, attempts)
	}
}

func TestPaymentAnomalyCanonicalProviderFactIgnoresIngressReasonAndFallbackTime(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "anomaly-canonical-fact.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := NewSQLOrderStore(db)
	fact := PaymentAnomaly{
		ProposedOrderID: 909090, Provider: PaymentMethodStars, EventKind: PaymentEventCaptured,
		ExternalID: "provider-fact-retry", PayerID: 42, AmountMinor: 100,
		Currency: "XTR", Scale: 0, RawPayload: `{"signed":true}`,
		Reason: "webhook_unknown_order",
	}
	if err := store.RecordPaymentAnomaly(ctx, fact); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("first anomaly: %v", err)
	}
	retry := fact
	retry.ProposedOrderID = 808080
	retry.Reason = "operator_provider_import"
	retry.Fingerprint = "caller-local-reason-fingerprint"
	if err := store.RecordPaymentAnomaly(ctx, retry); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("same provider fact through another ingress: %v", err)
	}

	ledger := NewSQLPaymentLedgerStore(db)
	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodStars)
	if err != nil || len(cases) != 1 || cases[0].OrderID != fact.ProposedOrderID || len(cases[0].Targets) != 1 {
		t.Fatalf("canonical cases=%+v err=%v", cases, err)
	}
	var anomalies int
	var firstReason string
	if err := db.Conn().QueryRow(`SELECT COUNT(*), MIN(reason) FROM payment_anomalies`).Scan(&anomalies, &firstReason); err != nil {
		t.Fatal(err)
	}
	if anomalies != 1 || firstReason != "webhook_unknown_order" {
		t.Fatalf("anomalies=%d first_reason=%q", anomalies, firstReason)
	}
	if _, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint,proposed_order_id,provider,event_kind,external_id,payer_id,
		 amount_minor,currency,scale,raw_amount,raw_payload,reason,occurred_at)
		VALUES ('legacy-local-time-fingerprint',0,'stars','captured','legacy-fallback',42,
		        100,'XTR',0,'100','{"signed":true}','old_local_reason','2024-01-02 03:04:05')`); err != nil {
		t.Fatal(err)
	}
	legacyRetry := fact
	legacyRetry.ProposedOrderID = 0
	legacyRetry.ExternalID = "legacy-fallback"
	legacyRetry.RawAmount = "100.0"
	legacyRetry.Reason = "new_ingress_reason"
	if err := store.RecordPaymentAnomaly(ctx, legacyRetry); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("legacy fallback retry: %v", err)
	}
	var legacyRows int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies
		WHERE provider='stars' AND external_id='legacy-fallback'`).Scan(&legacyRows); err != nil {
		t.Fatal(err)
	}
	if legacyRows != 1 {
		t.Fatalf("legacy fallback rows=%d", legacyRows)
	}

	resolution := PaymentReviewResolution{
		OrderID: fact.ProposedOrderID, Provider: PaymentMethodStars,
		AnomalyIDs: []int64{cases[0].Targets[0].ID}, Decision: "compensated",
		Actor: "operator:test", Reason: "verified external compensation",
		ResultingPaymentState: PaymentStateCancelled,
	}
	// A detached positive provider order ID is terminally reviewable without
	// creating a local order or manufacturing settled revenue.
	if _, err := ledger.PreviewPaymentReviewResolution(ctx, resolution); err != nil {
		t.Fatalf("detached positive-id preview: %v", err)
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatalf("detached positive-id resolve: %v", err)
	}
	retry.Reason = "third_ingress_reason"
	if err := store.RecordPaymentAnomaly(ctx, retry); err != nil {
		t.Fatalf("resolved canonical replay: %v", err)
	}
	var orders, resolutions int
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM orders WHERE id=?`, fact.ProposedOrderID).Scan(&orders)
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions WHERE target_kind='payment_anomaly'`).Scan(&resolutions)
	if orders != 0 || resolutions != 1 {
		t.Fatalf("orders=%d resolutions=%d", orders, resolutions)
	}
}

func TestProviderNeutralLegacyOrderHasTerminalNonRevenueResolution(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "provider-neutral-terminal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,status,order_state,payment_state,fulfillment_state)
		VALUES (42,5,100,NULL,'paid','placed','needs_review','unfulfilled')`)
	if err != nil {
		t.Fatal(err)
	}
	orderID, _ := res.LastInsertId()
	if _, err := db.Conn().Exec(`INSERT INTO order_events
		(order_id,event_type,from_state,to_state) VALUES (?,'payment.legacy_provider_unknown','settled','needs_review')`, orderID); err != nil {
		t.Fatal(err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: PaymentReviewProviderUnknown, OrderTargetID: orderID,
		Decision: "dismissed", Actor: "operator:test", Reason: "legacy row has no attributable provider",
		ResultingPaymentState: PaymentStateCancelled,
	}
	if _, err := ledger.PreviewPaymentReviewResolution(context.Background(), resolution); err != nil {
		t.Fatalf("neutral preview: %v", err)
	}
	if err := ledger.ResolvePaymentReview(context.Background(), resolution); err != nil {
		t.Fatalf("neutral resolve: %v", err)
	}
	if err := ledger.ResolvePaymentReview(context.Background(), resolution); err != nil {
		t.Fatalf("neutral exact replay: %v", err)
	}
	var status, state, decision string
	var attempts, resolutions int
	if err := db.Conn().QueryRow(`SELECT status,payment_state FROM orders WHERE id=?`, orderID).Scan(&status, &state); err != nil {
		t.Fatal(err)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_attempts WHERE order_id=?`, orderID).Scan(&attempts)
	if err := db.Conn().QueryRow(`SELECT decision FROM payment_resolutions
		WHERE target_kind='order' AND target_id=?`, orderID).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	_ = db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_resolutions WHERE target_kind='order' AND target_id=?`, orderID).Scan(&resolutions)
	if status != OrderStatusCancelled || state != PaymentStateCancelled || decision != "dismissed" || attempts != 0 || resolutions != 1 {
		t.Fatalf("status=%s state=%s decision=%s attempts=%d resolutions=%d", status, state, decision, attempts, resolutions)
	}
	cases, err := ledger.ListPaymentReviews(context.Background(), PaymentReviewProviderUnknown)
	if err != nil || len(cases) != 0 {
		t.Fatalf("neutral cases after terminal resolution=%+v err=%v", cases, err)
	}
}
