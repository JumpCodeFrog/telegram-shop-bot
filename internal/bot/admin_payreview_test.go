package bot

// Admin payment-review queue (/payreview) tests. Cases are seeded through the
// real storage layer (SQLite fixture from e2eEnv) exactly like the storage
// payment_resolutions tests, then driven through the production router with a
// fake Telegram API. Assertions target recorded Bot API calls and DB state.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"shop_bot/internal/storage"
)

// --- helpers -------------------------------------------------------------

// seedPayReviewOrder inserts a pending order owned by userID and returns the
// order store plus the new order ID.
func seedPayReviewOrder(t *testing.T, e *e2eEnv, userID int64, totalStars int) (*storage.SQLOrderStore, int64) {
	t.Helper()
	store := storage.NewSQLOrderStore(e.db)
	orderID, err := store.CreateOrder(context.Background(), &storage.Order{
		UserID: userID, TotalUSD: 12.50, TotalStars: totalStars, Status: storage.OrderStatusPending,
	}, []storage.OrderItem{{ProductID: e.prodReg, ProductName: "Tee", Quantity: 1, PriceUSD: 12.50}})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	return store, orderID
}

// seedDuplicateRefundedCase builds the classic quarantine: a settled Stars
// capture plus a second capture that was fully refunded. The case has two
// event targets and derives cleanly back to settled.
func seedDuplicateRefundedCase(t *testing.T, e *e2eEnv) int64 {
	t.Helper()
	ctx := context.Background()
	store, orderID := seedPayReviewOrder(t, e, e2eAdminID, 100)
	if err := store.UpdateOrderStatus(ctx, orderID, storage.OrderStatusPending, storage.OrderStatusPaid, "stars", "capture-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "capture-b", "second_charge"); !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture: %v", err)
	}
	ledger := storage.NewSQLPaymentLedgerStore(e.db)
	if err := ledger.RecordRefund(ctx, storage.Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "capture-b",
		PaymentExternalID: "capture-b", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	return orderID
}

// seedCompensatedOnlyCase builds a paid order whose only capture is a
// quarantined one that was fully refunded: the review resolves to refunded.
func seedCompensatedOnlyCase(t *testing.T, e *e2eEnv) int64 {
	t.Helper()
	ctx := context.Background()
	store, orderID := seedPayReviewOrder(t, e, e2eAdminID, 100)
	if _, err := e.db.Conn().Exec(`UPDATE orders SET status='paid' WHERE id=?`, orderID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "capture-c", "legacy_capture_recovered"); !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture: %v", err)
	}
	ledger := storage.NewSQLPaymentLedgerStore(e.db)
	if err := ledger.RecordRefund(ctx, storage.Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "refund-c",
		PaymentExternalID: "capture-c", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	return orderID
}

// seedUnknownProviderCase builds the provider-neutral legacy row whose rail
// cannot be established: a paid order quarantined without any provider facts.
func seedUnknownProviderCase(t *testing.T, e *e2eEnv) int64 {
	t.Helper()
	res, err := e.db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,status,order_state,payment_state,fulfillment_state)
		VALUES (?,5,100,NULL,'paid','placed','needs_review','unfulfilled')`, e2eAdminID)
	if err != nil {
		t.Fatal(err)
	}
	orderID, _ := res.LastInsertId()
	if _, err := e.db.Conn().Exec(`INSERT INTO order_events
		(order_id,event_type,from_state,to_state) VALUES (?,'payment.legacy_provider_unknown','settled','needs_review')`, orderID); err != nil {
		t.Fatal(err)
	}
	return orderID
}

// tgText returns the text param of the last recorded sendMessage or
// editMessageText call (callback flows re-render in place).
func tgText(calls []tgCall) string {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Method == "sendMessage" || calls[i].Method == "editMessageText" {
			return calls[i].Params.Get("text")
		}
	}
	return ""
}

// tgHasCall reports whether any recorded call matches substr.
func tgHasCall(calls []tgCall, substr string) bool {
	for _, c := range calls {
		if callMatches(c, substr) {
			return true
		}
	}
	return false
}

// tgSends reports whether any sendMessage/editMessageText call was recorded.
func tgSends(calls []tgCall) bool {
	for _, c := range calls {
		if c.Method == "sendMessage" || c.Method == "editMessageText" {
			return true
		}
	}
	return false
}

// --- buildReviewResolution unit matrix ------------------------------------

func reviewCaseFixture(orderID int64, provider, state string, targets ...storage.PaymentReviewTarget) storage.PaymentReviewCase {
	return storage.PaymentReviewCase{OrderID: orderID, Provider: provider, PaymentState: state, Targets: targets}
}

func TestBuildReviewResolution_OrderWithEvents(t *testing.T) {
	item := reviewCaseFixture(7, "stars", storage.PaymentStateNeedsReview,
		storage.PaymentReviewTarget{Kind: storage.PaymentReviewTargetEvent, ID: 11, ReasonCode: "event_captured"},
		storage.PaymentReviewTarget{Kind: storage.PaymentReviewTargetEvent, ID: 12, ReasonCode: "event_refunded"},
	)

	res, err := buildReviewResolution(item, "settle", 9001)
	if err != nil {
		t.Fatal(err)
	}
	if res.OrderID != 7 || res.Provider != "stars" || res.ResultingPaymentState != storage.PaymentStateSettled ||
		res.Decision != "" || len(res.EventIDs) != 2 || res.EventIDs[0] != 11 || res.EventIDs[1] != 12 ||
		res.OrderTargetID != 0 || res.Actor != "admin:9001" || res.Reason == "" {
		t.Fatalf("settle resolution=%+v", res)
	}

	res, err = buildReviewResolution(item, "refund", 9001)
	if err != nil || res.ResultingPaymentState != storage.PaymentStateRefunded || res.Decision != "" {
		t.Fatalf("refund resolution=%+v err=%v", res, err)
	}

	res, err = buildReviewResolution(item, "dismiss", 9001)
	if err != nil || res.ResultingPaymentState != storage.PaymentStateCancelled || res.Decision != "" {
		t.Fatalf("dismiss resolution=%+v err=%v", res, err)
	}

	if _, err := buildReviewResolution(item, "explode", 9001); err == nil {
		t.Fatal("unknown action must error")
	}
}

func TestBuildReviewResolution_SingleAnomalyAttached(t *testing.T) {
	item := reviewCaseFixture(9, "crypto", storage.PaymentStateNeedsReview,
		storage.PaymentReviewTarget{Kind: storage.PaymentReviewTargetAnomaly, ID: 21, ReasonCode: "legacy_capture_unverifiable"},
	)

	// A no-attempt capture anomaly closes as refunded only via an explicit
	// compensated decision; storage validation pins this shape.
	res, err := buildReviewResolution(item, "refund", 42)
	if err != nil || res.Decision != "compensated" || res.ResultingPaymentState != storage.PaymentStateRefunded ||
		len(res.AnomalyIDs) != 1 || res.AnomalyIDs[0] != 21 || res.OrderTargetID != 0 {
		t.Fatalf("refund resolution=%+v err=%v", res, err)
	}

	res, err = buildReviewResolution(item, "settle", 42)
	if err != nil || res.Decision != "" || res.ResultingPaymentState != storage.PaymentStateSettled {
		t.Fatalf("settle resolution=%+v err=%v", res, err)
	}

	res, err = buildReviewResolution(item, "dismiss", 42)
	if err != nil || res.Decision != "" || res.ResultingPaymentState != storage.PaymentStateCancelled {
		t.Fatalf("dismiss resolution=%+v err=%v", res, err)
	}
}

func TestBuildReviewResolution_OrphanAnomaly(t *testing.T) {
	orphan := reviewCaseFixture(0, "stars", "",
		storage.PaymentReviewTarget{Kind: storage.PaymentReviewTargetAnomaly, ID: 31, ReasonCode: "provider_verified_unknown_order"},
	)

	// A captured orphan is acknowledged as externally compensated.
	res, err := buildReviewResolution(orphan, "settle", 42)
	if err != nil || res.Decision != "compensated" || res.ResultingPaymentState != "" ||
		res.OrderID != 0 || len(res.AnomalyIDs) != 1 || len(res.EventIDs) != 0 || res.OrderTargetID != 0 {
		t.Fatalf("orphan settle resolution=%+v err=%v", res, err)
	}

	// A refund orphan is acknowledged as an accepted provider refund.
	res, err = buildReviewResolution(orphan, "refund", 42)
	if err != nil || res.Decision != "accepted_refund" || res.ResultingPaymentState != "" {
		t.Fatalf("orphan refund resolution=%+v err=%v", res, err)
	}

	// Dismissal stays implicit: the decision is derived from ledger evidence.
	res, err = buildReviewResolution(orphan, "dismiss", 42)
	if err != nil || res.Decision != "" || res.ResultingPaymentState != "" {
		t.Fatalf("orphan dismiss resolution=%+v err=%v", res, err)
	}

	// A detached provider-proposed order ID carries a valid terminal state
	// because validation requires a non-empty state for positive order IDs.
	detached := reviewCaseFixture(909090, "stars", "",
		storage.PaymentReviewTarget{Kind: storage.PaymentReviewTargetAnomaly, ID: 32, ReasonCode: "webhook_unknown_order"},
	)
	res, err = buildReviewResolution(detached, "settle", 42)
	if err != nil || res.OrderID != 909090 || res.ResultingPaymentState != storage.PaymentStateCancelled ||
		res.Decision != "compensated" {
		t.Fatalf("detached settle resolution=%+v err=%v", res, err)
	}
}

func TestBuildReviewResolution_UnknownProvider(t *testing.T) {
	item := reviewCaseFixture(5, storage.PaymentReviewProviderUnknown, storage.PaymentStateNeedsReview,
		storage.PaymentReviewTarget{Kind: storage.PaymentReviewTargetOrder, ID: 5, ReasonCode: "payment.legacy_provider_unknown"},
	)

	res, err := buildReviewResolution(item, "dismiss", 42)
	if err != nil || res.Decision != "dismissed" || res.ResultingPaymentState != storage.PaymentStateCancelled ||
		res.OrderTargetID != 5 || len(res.EventIDs) != 0 || len(res.AnomalyIDs) != 0 {
		t.Fatalf("unknown dismiss resolution=%+v err=%v", res, err)
	}

	// A provider-neutral row can never become settled or refunded revenue.
	if _, err := buildReviewResolution(item, "settle", 42); err == nil {
		t.Fatal("unknown-provider settle must error")
	}
	if _, err := buildReviewResolution(item, "refund", 42); err == nil {
		t.Fatal("unknown-provider refund must error")
	}
}

func TestBuildReviewResolution_InvalidCombos(t *testing.T) {
	if _, err := buildReviewResolution(reviewCaseFixture(1, "stars", storage.PaymentStateNeedsReview), "settle", 42); err == nil {
		t.Fatal("empty target set must error")
	}
	bogus := reviewCaseFixture(1, "stars", storage.PaymentStateNeedsReview,
		storage.PaymentReviewTarget{Kind: "martian", ID: 1, ReasonCode: "x"})
	if _, err := buildReviewResolution(bogus, "settle", 42); err == nil {
		t.Fatal("unknown target kind must error")
	}
	// An unknown-provider case without its order target can never validate.
	orphanedUnknown := reviewCaseFixture(5, storage.PaymentReviewProviderUnknown, storage.PaymentStateNeedsReview,
		storage.PaymentReviewTarget{Kind: storage.PaymentReviewTargetEvent, ID: 1, ReasonCode: "event_captured"})
	if _, err := buildReviewResolution(orphanedUnknown, "dismiss", 42); err == nil {
		t.Fatal("unknown-provider case without order target must error")
	}
}

// --- orphan action sets (ruling P4) + callback byte budget (P6) ------------

// TestPayReviewOrphanCardActionSets pins ruling P4: orphan cards offer only
// the actions that can actually pass — digest-only cards none (CLI-only),
// path-5 refund-ledger-failure cards Refund+Dismiss (with the trap warning),
// other capture orphans Settle. Attached and unknown-provider cases keep
// their existing sets.
func TestPayReviewOrphanCardActionSets(t *testing.T) {
	orphan := func(provider, reason string) storage.PaymentReviewCase {
		return storage.PaymentReviewCase{
			OrderID: 0, Provider: provider, PaymentState: "",
			Targets: []storage.PaymentReviewTarget{{
				Kind: storage.PaymentReviewTargetAnomaly, ID: 7, ReasonCode: reason,
			}},
		}
	}
	for _, tc := range []struct {
		name string
		item storage.PaymentReviewCase
		want []string
	}{
		{"path-5 refund orphan", orphan(storage.PaymentMethodBalance, "refund_ledger_failure:order=7"),
			[]string{payReviewActionRefund, payReviewActionDismiss}},
		{"digest parse failure", orphan(storage.PaymentMethodYooKassa, "webhook_parse_failure"), nil},
		{"digest missing payment id", orphan(storage.PaymentMethodYooKassa, "webhook_missing_payment_id"), nil},
		{"stars decode-failure digest orphan", orphan(storage.PaymentMethodStars, "stars_update_decode_failure"), nil},
		{"capture orphan", orphan(storage.PaymentMethodStars, "provider_verified_unknown_order"),
			[]string{payReviewActionSettle}},
		{"attached case keeps the triple", storage.PaymentReviewCase{
			OrderID: 3, Provider: storage.PaymentMethodStars, PaymentState: storage.PaymentStateNeedsReview,
			Targets: []storage.PaymentReviewTarget{{
				Kind: storage.PaymentReviewTargetAnomaly, ID: 1, ReasonCode: "late_capture",
			}},
		}, []string{payReviewActionSettle, payReviewActionRefund, payReviewActionDismiss}},
		{"unknown provider keeps dismiss", storage.PaymentReviewCase{
			OrderID: 3, Provider: storage.PaymentReviewProviderUnknown, PaymentState: storage.PaymentStateNeedsReview,
			Targets: []storage.PaymentReviewTarget{{
				Kind: storage.PaymentReviewTargetOrder, ID: 3, ReasonCode: "order_needs_review",
			}},
		}, []string{payReviewActionDismiss}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := payReviewActions(tc.item)
			if len(got) != len(tc.want) {
				t.Fatalf("actions = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("actions = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestPayReviewCallbackByteBudget documents the 64-byte budget (ruling P6):
// the realistic attached worst case fits; the theoretical detached worst case
// (19-digit provider order id + large anomaly id) exceeds it and is degraded
// to the CLI-only card by sendPayReviewCard's guard.
func TestPayReviewCallbackByteBudget(t *testing.T) {
	attached := storage.PaymentReviewCase{
		OrderID: math.MaxInt64, Provider: storage.PaymentMethodNowpayments,
		PaymentState: storage.PaymentStateNeedsReview,
		Targets: []storage.PaymentReviewTarget{{
			Kind: storage.PaymentReviewTargetAnomaly, ID: 1, ReasonCode: "receipt_mismatch",
		}},
	}
	if got := payReviewCaseCallback("admin:payrev:", payReviewActionDismiss, attached); len(got) > 64 {
		t.Fatalf("attached worst case %d bytes > 64: %s", len(got), got)
	}
	detached := storage.PaymentReviewCase{
		OrderID: math.MaxInt64, Provider: storage.PaymentMethodNowpayments, PaymentState: "",
		Targets: []storage.PaymentReviewTarget{{
			Kind: storage.PaymentReviewTargetAnomaly, ID: math.MaxInt64, ReasonCode: "receipt_mismatch",
		}},
	}
	if got := payReviewCaseCallback("admin:payrev:", payReviewActionDismiss, detached); len(got) <= 64 {
		t.Fatalf("detached worst case %d bytes unexpectedly fits — re-check the guard rationale: %s", len(got), got)
	}
}

// --- router / e2e legs -----------------------------------------------------

func TestPayReviewEmptyAndNonAdmin(t *testing.T) {
	e := newE2EEnv(t)

	calls := e.cmd(e2eAdminID, "/payreview", "en")
	if len(calls) != 1 || calls[0].Method != "sendMessage" {
		t.Fatalf("empty /payreview calls=%+v", calls)
	}
	if got, want := calls[0].Params.Get("text"), e.bot.t("en", "admin_payreview_empty"); got != want {
		t.Fatalf("empty text = %q, want %q", got, want)
	}

	// Non-admin command: fully inert.
	if calls := e.cmd(1234, "/payreview", "en"); len(calls) != 0 {
		t.Fatalf("non-admin /payreview produced calls=%+v", calls)
	}
	// Non-admin callbacks: acked at the router level, never acted upon.
	calls = e.cb(1234, "admin:payrev:stars:1", "en")
	if tgSends(calls) {
		t.Fatalf("non-admin card callback acted: %+v", calls)
	}
	calls = e.cb(1234, "admin:payrevdo:settle:stars:1", "en")
	if tgSends(calls) {
		t.Fatalf("non-admin confirm callback acted: %+v", calls)
	}
}

func TestPayReviewListsAcrossProviders(t *testing.T) {
	e := newE2EEnv(t)
	starsOrder := seedDuplicateRefundedCase(t, e)

	ctx := context.Background()
	cryptoStore, cryptoOrder := seedPayReviewOrder(t, e, e2eAdminID, 200)
	if err := cryptoStore.UpdateOrderStatus(ctx, cryptoOrder, storage.OrderStatusPending, storage.OrderStatusPaid, "crypto", "crypto-a"); err != nil {
		t.Fatal(err)
	}
	if err := cryptoStore.RecordUnexpectedPayment(ctx, cryptoOrder, "crypto", "crypto-b", "second_charge"); !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatal(err)
	}

	calls := e.cmd(e2eAdminID, "/payreview", "en")
	if len(calls) != 1 || calls[0].Method != "sendMessage" {
		t.Fatalf("list calls=%+v", calls)
	}
	text := calls[0].Params.Get("text")
	if !strings.Contains(text, fmt.Sprintf("#%d", starsOrder)) || !strings.Contains(text, fmt.Sprintf("#%d", cryptoOrder)) ||
		!strings.Contains(text, "stars") || !strings.Contains(text, "crypto") {
		t.Fatalf("list text=%q", text)
	}
	markup := calls[0].markup()
	if !strings.Contains(markup, fmt.Sprintf("admin:payrev:stars:%d", starsOrder)) ||
		!strings.Contains(markup, fmt.Sprintf("admin:payrev:crypto:%d", cryptoOrder)) {
		t.Fatalf("list markup=%s", markup)
	}
}

func TestPayReviewCardShowsTargetsAndActions(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedDuplicateRefundedCase(t, e)

	calls := e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:stars:%d", orderID), "en")
	text := tgText(calls)
	if !strings.Contains(text, "needs_review") || !strings.Contains(text, "event_captured") ||
		!strings.Contains(text, "event_refunded") {
		t.Fatalf("card text=%q", text)
	}
	var markup string
	for _, c := range calls {
		if m := c.markup(); m != "" {
			markup = m
		}
	}
	for _, want := range []string{
		fmt.Sprintf("admin:payrev:settle:stars:%d", orderID),
		fmt.Sprintf("admin:payrev:refund:stars:%d", orderID),
		fmt.Sprintf("admin:payrev:dismiss:stars:%d", orderID),
	} {
		if !strings.Contains(markup, want) {
			t.Fatalf("card markup %s missing %s", markup, want)
		}
	}
}

func TestPayReviewTwoTapSettleResolvesOrder(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedDuplicateRefundedCase(t, e)

	// First tap: preview only — no resolution rows, order untouched.
	calls := e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:settle:stars:%d", orderID), "en")
	if !tgHasCall(calls, fmt.Sprintf("admin:payrevdo:settle:stars:%d", orderID)) {
		t.Fatalf("preview calls=%+v", calls)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_resolutions`); got != 0 {
		t.Fatalf("preview wrote %d resolutions", got)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateNeedsReview {
		t.Fatalf("state after preview=%s", got)
	}

	// Second tap: applies.
	calls = e.cb(e2eAdminID, fmt.Sprintf("admin:payrevdo:settle:stars:%d", orderID), "en")
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateSettled {
		t.Fatalf("state after confirm=%s", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_resolutions WHERE order_id=?`, orderID); got != 2 {
		t.Fatalf("resolutions=%d", got)
	}
	want := e.bot.i18n.Tf("en", "admin_payrev_resolved", orderID, storage.PaymentStateSettled)
	if got := tgText(calls); !strings.Contains(got, want) {
		t.Fatalf("resolved text=%q want %q", got, want)
	}
	// The case leaves the queue.
	if calls := e.cmd(e2eAdminID, "/payreview", "en"); !strings.Contains(tgText(calls), e.bot.t("en", "admin_payreview_empty")) {
		t.Fatalf("queue not empty after resolve: %+v", calls)
	}
}

func TestPayReviewRefundFlowOnCompensatedCapture(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedCompensatedOnlyCase(t, e)

	calls := e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:refund:stars:%d", orderID), "en")
	if !tgHasCall(calls, fmt.Sprintf("admin:payrevdo:refund:stars:%d", orderID)) {
		t.Fatalf("preview calls=%+v", calls)
	}
	e.cb(e2eAdminID, fmt.Sprintf("admin:payrevdo:refund:stars:%d", orderID), "en")
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateRefunded {
		t.Fatalf("state after refund=%s", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_resolutions WHERE order_id=? AND resulting_payment_state='refunded'`, orderID); got != 2 {
		t.Fatalf("refund resolutions=%d", got)
	}
}

func TestPayReviewDismissUnknownProviderCancels(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedUnknownProviderCase(t, e)

	calls := e.cmd(e2eAdminID, "/payreview", "en")
	if !tgHasCall(calls, fmt.Sprintf("admin:payrev:unknown:%d", orderID)) {
		t.Fatalf("unknown case not listed: %+v", calls)
	}

	// The card of a provider-neutral row offers only dismissal.
	calls = e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:unknown:%d", orderID), "en")
	var markup string
	for _, c := range calls {
		if m := c.markup(); m != "" {
			markup = m
		}
	}
	if !strings.Contains(markup, fmt.Sprintf("admin:payrev:dismiss:unknown:%d", orderID)) ||
		strings.Contains(markup, "admin:payrev:settle:") || strings.Contains(markup, "admin:payrev:refund:") {
		t.Fatalf("unknown card markup=%s", markup)
	}

	calls = e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:dismiss:unknown:%d", orderID), "en")
	if !tgHasCall(calls, fmt.Sprintf("admin:payrevdo:dismiss:unknown:%d", orderID)) {
		t.Fatalf("preview calls=%+v", calls)
	}
	e.cb(e2eAdminID, fmt.Sprintf("admin:payrevdo:dismiss:unknown:%d", orderID), "en")
	if got := e.qStr(`SELECT status FROM orders WHERE id=?`, orderID); got != storage.OrderStatusCancelled {
		t.Fatalf("status after dismiss=%s", got)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateCancelled {
		t.Fatalf("state after dismiss=%s", got)
	}
	if got := e.qStr(`SELECT decision FROM payment_resolutions WHERE target_kind='order' AND target_id=?`, orderID); got != "dismissed" {
		t.Fatalf("decision=%s", got)
	}
}

func TestPayReviewConfirmDetectsTargetsChanged(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedDuplicateRefundedCase(t, e)

	calls := e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:settle:stars:%d", orderID), "en")
	if !tgHasCall(calls, fmt.Sprintf("admin:payrevdo:settle:stars:%d", orderID)) {
		t.Fatalf("preview calls=%+v", calls)
	}

	// A new provider fact lands between preview and confirm.
	store := storage.NewSQLOrderStore(e.db)
	if err := store.RecordUnexpectedPayment(context.Background(), orderID, "stars", "capture-d", "late_capture"); !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatal(err)
	}

	calls = e.cb(e2eAdminID, fmt.Sprintf("admin:payrevdo:settle:stars:%d", orderID), "en")
	if got, want := tgText(calls), e.bot.t("en", "admin_payrev_conflict"); !strings.Contains(got, want) {
		t.Fatalf("conflict text=%q want %q", got, want)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateNeedsReview {
		t.Fatalf("state after conflict=%s", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_resolutions`); got != 0 {
		t.Fatalf("conflict wrote %d resolutions", got)
	}
}

// TestPayReviewCaseGoneMessageIsDistinct pins ruling P3 (HANDOFF §6.9): a case
// that left the queue between taps answers with its own case-gone message at
// every entry point (card, preview, confirm) — the conflict text stays reserved
// for changed/invalid target sets.
func TestPayReviewCaseGoneMessageIsDistinct(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedUnknownProviderCase(t, e)

	// Resolve the case through the regular two-tap flow.
	e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:dismiss:unknown:%d", orderID), "en")
	e.cb(e2eAdminID, fmt.Sprintf("admin:payrevdo:dismiss:unknown:%d", orderID), "en")
	if got := e.qInt(`SELECT COUNT(*) FROM payment_resolutions`); got != 1 {
		t.Fatalf("resolutions = %d, want 1", got)
	}

	want := e.bot.t("en", "admin_payrev_case_gone")
	conflict := e.bot.t("en", "admin_payrev_conflict")
	for _, stale := range []string{
		fmt.Sprintf("admin:payrev:unknown:%d", orderID),           // card tap
		fmt.Sprintf("admin:payrev:dismiss:unknown:%d", orderID),   // preview tap
		fmt.Sprintf("admin:payrevdo:dismiss:unknown:%d", orderID), // confirm tap
	} {
		calls := e.cb(e2eAdminID, stale, "en")
		if got := tgText(calls); !strings.Contains(got, want) || strings.Contains(got, conflict) {
			t.Fatalf("stale %s text = %q, want case-gone %q", stale, got, want)
		}
	}
}

func TestPayReviewOrphanAnomalyResolvesByExactTarget(t *testing.T) {
	e := newE2EEnv(t)
	ctx := context.Background()
	store := storage.NewSQLOrderStore(e.db)
	// Two orphans share the same proposed (nonexistent) provider order ID: the
	// queue must address them independently.
	for _, externalID := range []string{"orphan-a", "orphan-b"} {
		err := store.RecordPaymentAnomaly(ctx, storage.PaymentAnomaly{
			Provider: storage.PaymentMethodStars, EventKind: storage.PaymentEventCaptured,
			ExternalID: externalID, AmountMinor: 100, Currency: "XTR", Scale: 0,
			Reason: "provider_verified_unknown_order",
		})
		if !errors.Is(err, storage.ErrPaymentNeedsReview) {
			t.Fatalf("record %s: %v", externalID, err)
		}
	}

	calls := e.cmd(e2eAdminID, "/payreview", "en")
	markup := calls[0].markup()
	var anomalyIDs []int64
	rows, err := e.db.Conn().Query(`SELECT id FROM payment_anomalies ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		anomalyIDs = append(anomalyIDs, id)
	}
	rows.Close()
	if len(anomalyIDs) != 2 {
		t.Fatalf("anomalies=%v", anomalyIDs)
	}
	for _, id := range anomalyIDs {
		if !strings.Contains(markup, fmt.Sprintf("admin:payrev:stars:0:%d", id)) {
			t.Fatalf("list markup %s missing orphan %d", markup, id)
		}
	}

	// Compensate the first orphan through the two-tap flow.
	e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:settle:stars:0:%d", anomalyIDs[0]), "en")
	calls = e.cb(e2eAdminID, fmt.Sprintf("admin:payrevdo:settle:stars:0:%d", anomalyIDs[0]), "en")
	if got := e.qStr(`SELECT decision FROM payment_resolutions WHERE target_kind='payment_anomaly' AND target_id=?`, anomalyIDs[0]); got != "compensated" {
		t.Fatalf("orphan decision=%q calls=%+v", got, calls)
	}
	// The sibling orphan is untouched and still listed.
	calls = e.cmd(e2eAdminID, "/payreview", "en")
	if !strings.Contains(calls[0].markup(), fmt.Sprintf("admin:payrev:stars:0:%d", anomalyIDs[1])) ||
		strings.Contains(calls[0].markup(), fmt.Sprintf("admin:payrev:stars:0:%d", anomalyIDs[0])) {
		t.Fatalf("sibling orphan markup=%s", calls[0].markup())
	}
}

// TestPayReviewPathFiveCardWarnsAndFiltersActions pins ruling P4 on the real
// path-5 card shape: the trap warning renders, Refund+Dismiss are offered,
// Settle is not.
func TestPayReviewPathFiveCardWarnsAndFiltersActions(t *testing.T) {
	e := newE2EEnv(t)
	store := storage.NewSQLOrderStore(e.db)
	err := store.RecordPaymentAnomaly(context.Background(), storage.PaymentAnomaly{
		Provider:          storage.PaymentMethodBalance,
		EventKind:         storage.PaymentEventRefunded,
		ExternalID:        "balance-refund:7",
		RelatedExternalID: "balance:7",
		PayerID:           42,
		AmountMinor:       525,
		Currency:          "USD",
		Scale:             2,
		Reason:            "refund_ledger_failure:order=7",
		RawPayload:        `{"order_id":7,"rail":"balance","refund_id":"balance-refund:7"}`,
	})
	if !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("seed path-5 orphan: %v", err)
	}
	var anomalyID int64
	if err := e.db.Conn().QueryRow(`SELECT id FROM payment_anomalies`).Scan(&anomalyID); err != nil {
		t.Fatal(err)
	}

	calls := e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:balance:0:%d", anomalyID), "en")
	if got := tgText(calls); !strings.Contains(got, e.bot.t("en", "admin_payreview_card_trap")) {
		t.Fatalf("card lacks the trap warning:\n%s", got)
	}
	var markup string
	for _, c := range calls {
		if m := c.markup(); m != "" {
			markup = m
		}
	}
	if !strings.Contains(markup, fmt.Sprintf("admin:payrev:refund:balance:0:%d", anomalyID)) ||
		!strings.Contains(markup, fmt.Sprintf("admin:payrev:dismiss:balance:0:%d", anomalyID)) {
		t.Fatalf("card lacks Refund/Dismiss: %s", markup)
	}
	if strings.Contains(markup, "admin:payrev:settle:") {
		t.Fatalf("path-5 card must not offer Settle: %s", markup)
	}
}

// TestPayReviewDigestOrphanCardIsCLIOnly pins ruling P4's digest-only leg:
// the card carries the CLI-only hint and no action buttons at all.
func TestPayReviewDigestOrphanCardIsCLIOnly(t *testing.T) {
	e := newE2EEnv(t)
	store := storage.NewSQLOrderStore(e.db)
	// The exact digest shape webhook.go:186-189 writes for an unparseable
	// body: provider + sha256 payload + reason, zero money tuple, no ids.
	digest := sha256.Sum256([]byte("not json"))
	err := store.RecordPaymentAnomaly(context.Background(), storage.PaymentAnomaly{
		Provider:   storage.PaymentMethodYooKassa,
		RawPayload: fmt.Sprintf("sha256:%x", digest),
		Reason:     "webhook_parse_failure",
	})
	if !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("seed digest orphan: %v", err)
	}
	var anomalyID int64
	if err := e.db.Conn().QueryRow(`SELECT id FROM payment_anomalies`).Scan(&anomalyID); err != nil {
		t.Fatal(err)
	}

	calls := e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:yookassa:0:%d", anomalyID), "en")
	if got := tgText(calls); !strings.Contains(got, e.bot.t("en", "admin_payreview_card_cli_only")) {
		t.Fatalf("card lacks the CLI-only hint:\n%s", got)
	}
	var markup string
	for _, c := range calls {
		if m := c.markup(); m != "" {
			markup = m
		}
	}
	if strings.Contains(markup, "admin:payrev:settle:") || strings.Contains(markup, "admin:payrev:refund:") ||
		strings.Contains(markup, "admin:payrev:dismiss:") {
		t.Fatalf("digest-only card must offer no actions: %s", markup)
	}
	if !strings.Contains(markup, "admin:payrev:list") {
		t.Fatalf("digest-only card lost its Back button: %s", markup)
	}
}
