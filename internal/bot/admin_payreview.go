package bot

// Admin payment-review queue (/payreview): lists quarantined payment cases
// from the ledger across every provider and resolves them through a two-tap
// preview-then-confirm flow. Every resolution names exactly the case's current
// target set; the ledger rejects anything else, so a stale or partial
// acknowledgement can never slip through.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

// payReviewProviders enumerates every provider bucket the review inbox can
// hold, including the provider-neutral "unknown" pseudo-provider. The
// balance bucket carries the refund path-5 orphan anomaly cards written by
// admin_refunds.go (ruling R2): without it, those durable traces would never
// surface in /payreview.
// ListPaymentReviews has no "all" wildcard: an empty provider is rejected, so
// the bot aggregates explicitly.
var payReviewProviders = []string{
	storage.PaymentMethodStars,
	storage.PaymentMethodCrypto,
	storage.PaymentMethodYooKassa,
	storage.PaymentMethodStripe,
	storage.PaymentMethodTON,
	storage.PaymentMethodNowpayments,
	storage.PaymentMethodBalance, // path-5 refund-ledger-failure cards (admin_refunds.go)
	storage.PaymentReviewProviderUnknown,
}

const (
	payReviewActionSettle  = "settle"
	payReviewActionRefund  = "refund"
	payReviewActionDismiss = "dismiss"
)

// handlePayReview renders the review queue for admins; non-admins get nothing.
func (b *Bot) handlePayReview(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	b.sendPayReviewList(msg.Chat.ID, 0, msg.From.LanguageCode)
}

// listAllPaymentReviews concatenates the queue of every provider.
func (b *Bot) listAllPaymentReviews(ctx context.Context) ([]storage.PaymentReviewCase, error) {
	var out []storage.PaymentReviewCase
	for _, provider := range payReviewProviders {
		cases, err := b.payLedger.ListPaymentReviews(ctx, provider)
		if err != nil {
			return nil, err
		}
		out = append(out, cases...)
	}
	return out, nil
}

// payReviewCaseCallback builds the callback data addressing one case. Orphan
// anomalies share their proposed order ID with siblings, so they carry their
// anomaly target ID as a disambiguator. Byte budget: Telegram caps callback
// data at 64 bytes — sendPayReviewCard degrades a card whose action callbacks
// would exceed the cap to the CLI-only hint (fail-closed).
func payReviewCaseCallback(prefix, action string, item storage.PaymentReviewCase) string {
	var sb strings.Builder
	sb.WriteString(prefix)
	if action != "" {
		sb.WriteString(action)
		sb.WriteString(":")
	}
	sb.WriteString(item.Provider)
	sb.WriteString(":")
	sb.WriteString(strconv.FormatInt(item.OrderID, 10))
	if item.PaymentState == "" && len(item.Targets) == 1 {
		sb.WriteString(":")
		sb.WriteString(strconv.FormatInt(item.Targets[0].ID, 10))
	}
	return sb.String()
}

// payReviewStateLabel renders an empty (orphan) payment state as a dash.
func payReviewStateLabel(state string) string {
	if state == "" {
		return "-"
	}
	return state
}

// payReviewReasons joins the case's target reason codes.
func payReviewReasons(item storage.PaymentReviewCase) string {
	reasons := make([]string, 0, len(item.Targets))
	for _, target := range item.Targets {
		reasons = append(reasons, target.ReasonCode)
	}
	return strings.Join(reasons, ",")
}

// sendPayReviewList renders the queue: one line plus one card button per case.
func (b *Bot) sendPayReviewList(chatID int64, msgID int, lang string) {
	ctx, cancel := b.handlerCtx()
	defer cancel()
	cases, err := b.listAllPaymentReviews(ctx)
	if err != nil {
		b.logger.Error("list payment reviews", "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payreview_failed"), "", StyledKeyboard{})
		return
	}
	if len(cases) == 0 {
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payreview_empty"), "", StyledKeyboard{})
		return
	}

	var sb strings.Builder
	sb.WriteString(b.t(lang, "admin_payreview_title"))
	kb := make(StyledKeyboard, 0, len(cases))
	for _, item := range cases {
		sb.WriteString(b.i18n.Tf(lang, "admin_payreview_case_line",
			item.OrderID, item.Provider, payReviewStateLabel(item.PaymentState),
			len(item.Targets), payReviewReasons(item)))
		kb = append(kb, []StyledButton{Btn(
			b.i18n.Tf(lang, "admin_payreview_open_btn", item.OrderID, item.Provider),
			payReviewCaseCallback("admin:payrev:", "", item))})
	}
	b.sendOrEditStyled(chatID, msgID, sb.String(), "", kb)
}

// payReviewRef is the parsed form of an admin:payrev[:do]: callback.
type payReviewRef struct {
	action    string // "", "list", settle, refund, dismiss
	confirm   bool
	provider  string
	orderID   int64
	anomalyID int64
	ok        bool
}

func isPayReviewAction(s string) bool {
	return s == payReviewActionSettle || s == payReviewActionRefund || s == payReviewActionDismiss
}

func isPayReviewProvider(s string) bool {
	for _, p := range payReviewProviders {
		if p == s {
			return true
		}
	}
	return false
}

// parsePayReviewData decodes both callback families:
//
//	admin:payrev:list                                  — back to the queue
//	admin:payrev:<provider>:<order>[:<anomaly>]        — case card
//	admin:payrev:<action>:<provider>:<order>[:<anomaly>]   — preview
//	admin:payrevdo:<action>:<provider>:<order>[:<anomaly>] — confirm
func parsePayReviewData(data string) payReviewRef {
	var ref payReviewRef
	rest, found := strings.CutPrefix(data, "admin:payrevdo:")
	if found {
		ref.confirm = true
	} else if rest, found = strings.CutPrefix(data, "admin:payrev:"); !found {
		return ref
	}
	parts := strings.Split(rest, ":")
	if !ref.confirm && len(parts) == 1 && parts[0] == "list" {
		ref.action = "list"
		ref.ok = true
		return ref
	}
	if isPayReviewAction(parts[0]) {
		ref.action = parts[0]
		parts = parts[1:]
	} else if ref.confirm {
		return ref
	}
	if len(parts) != 2 && len(parts) != 3 {
		return payReviewRef{}
	}
	if !isPayReviewProvider(parts[0]) {
		return payReviewRef{}
	}
	ref.provider = parts[0]
	var err error
	if ref.orderID, err = strconv.ParseInt(parts[1], 10, 64); err != nil || ref.orderID < 0 {
		return payReviewRef{}
	}
	if len(parts) == 3 {
		if ref.anomalyID, err = strconv.ParseInt(parts[2], 10, 64); err != nil || ref.anomalyID <= 0 {
			return payReviewRef{}
		}
	}
	if ref.confirm && ref.action == "" {
		return payReviewRef{}
	}
	ref.ok = true
	return ref
}

// onAdminPayReviewCallback dispatches an already admin-gated payrev callback.
func (b *Bot) onAdminPayReviewCallback(chatID int64, msgID int, userID int64, data, lang string) {
	ref := parsePayReviewData(data)
	if !ref.ok {
		return
	}
	switch {
	case ref.action == "list":
		b.sendPayReviewList(chatID, msgID, lang)
	case ref.action == "":
		b.sendPayReviewCard(chatID, msgID, ref, lang)
	case ref.confirm:
		b.onAdminPayReviewConfirm(chatID, msgID, userID, ref, lang)
	default:
		b.onAdminPayReviewPreview(chatID, msgID, userID, ref, lang)
	}
}

// findReviewCase reloads the current case addressed by ref. Orphans match by
// their anomaly target; attached cases match by provider plus order ID.
func (b *Bot) findReviewCase(ctx context.Context, ref payReviewRef) (storage.PaymentReviewCase, error) {
	cases, err := b.payLedger.ListPaymentReviews(ctx, ref.provider)
	if err != nil {
		return storage.PaymentReviewCase{}, err
	}
	for _, item := range cases {
		if ref.anomalyID > 0 {
			if item.OrderID == ref.orderID && len(item.Targets) == 1 &&
				item.Targets[0].Kind == storage.PaymentReviewTargetAnomaly && item.Targets[0].ID == ref.anomalyID {
				return item, nil
			}
			continue
		}
		if item.OrderID == ref.orderID && item.PaymentState != "" {
			return item, nil
		}
	}
	return storage.PaymentReviewCase{}, storage.ErrNotFound
}

// payReviewActions lists the actions offered on a case card. A provider-neutral
// row admits only terminal dismissal. Orphan cards (no local order) offer only
// the actions that can actually pass against the storage decision gates
// (payment_resolutions.go): digest-only facts fail every decision, path-5
// refund orphans pass Refund pre-recovery and Dismiss post-recovery, other
// capture orphans pass Settle (compensated). Attached cases keep the three
// candidate projections — the preview validates them against ledger evidence.
// This is a UX filter, never a gate: storage remains the final validator, and
// a filtered-out action that storage would accept is a bug, not a policy.
func payReviewActions(item storage.PaymentReviewCase) []string {
	if item.Provider == storage.PaymentReviewProviderUnknown {
		return []string{payReviewActionDismiss}
	}
	if isPayReviewOrphanAnomaly(item) {
		reason := item.Targets[0].ReasonCode
		switch {
		case payReviewIsRefundLedgerFailureOrphan(item):
			return []string{payReviewActionRefund, payReviewActionDismiss}
		case reason == "webhook_parse_failure" || reason == "webhook_missing_payment_id":
			return nil // digest-only: every decision provably conflicts — CLI-only card
		default:
			return []string{payReviewActionSettle}
		}
	}
	return []string{payReviewActionSettle, payReviewActionRefund, payReviewActionDismiss}
}

// isPayReviewOrphanAnomaly reports the single-anomaly orphan card shape (no
// local order — findReviewCase addresses it by the anomaly disambiguator).
func isPayReviewOrphanAnomaly(item storage.PaymentReviewCase) bool {
	return item.PaymentState == "" && len(item.Targets) == 1 &&
		item.Targets[0].Kind == storage.PaymentReviewTargetAnomaly
}

// payReviewIsRefundLedgerFailureOrphan reports a path-5 refund-ledger-failure
// orphan card (admin_refunds.go writes it with the reason grammar
// refund_ledger_failure:order=<id>).
func payReviewIsRefundLedgerFailureOrphan(item storage.PaymentReviewCase) bool {
	return isPayReviewOrphanAnomaly(item) &&
		strings.HasPrefix(item.Targets[0].ReasonCode, "refund_ledger_failure:")
}

func (b *Bot) payReviewActionLabel(lang, action string) string {
	switch action {
	case payReviewActionSettle:
		return b.t(lang, "admin_payreview_action_settle")
	case payReviewActionRefund:
		return b.t(lang, "admin_payreview_action_refund")
	default:
		return b.t(lang, "admin_payreview_action_dismiss")
	}
}

// sendPayReviewCard renders one case: order summary, payment state, every
// target with its reason code, and the action buttons.
func (b *Bot) sendPayReviewCard(chatID int64, msgID int, ref payReviewRef, lang string) {
	ctx, cancel := b.handlerCtx()
	defer cancel()
	item, err := b.findReviewCase(ctx, ref)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// The case left the queue between the list render and this tap.
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payrev_case_gone"), "", StyledKeyboard{})
			return
		}
		b.logger.Error("load payment review case", "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payreview_failed"), "", StyledKeyboard{})
		return
	}

	var sb strings.Builder
	sb.WriteString(b.i18n.Tf(lang, "admin_payreview_card_title", item.OrderID, item.Provider))
	if item.OrderID > 0 {
		if order, err := b.order.GetOrder(ctx, item.OrderID); err == nil {
			sb.WriteString(b.i18n.Tf(lang, "admin_payreview_card_order",
				order.UserID, order.TotalUSD, order.TotalStars))
		}
	}
	sb.WriteString(b.i18n.Tf(lang, "admin_payreview_card_state", payReviewStateLabel(item.PaymentState)))
	sb.WriteString(b.t(lang, "admin_payreview_card_targets"))
	for _, target := range item.Targets {
		sb.WriteString(b.i18n.Tf(lang, "admin_payreview_card_target_line", target.Kind, target.ID, target.ReasonCode))
	}

	actions := payReviewActions(item)
	if len(actions) == 0 {
		sb.WriteString(b.t(lang, "admin_payreview_card_cli_only"))
	} else if payReviewIsRefundLedgerFailureOrphan(item) {
		sb.WriteString(b.t(lang, "admin_payreview_card_trap"))
	}

	kb := StyledKeyboard{}
	row := []StyledButton{}
	for _, action := range actions {
		data := payReviewCaseCallback("admin:payrev:", action, item)
		// Telegram rejects callback data longer than 64 bytes. Realistic
		// payloads stay well inside (attached worst case
		// admin:payrev:dismiss:nowpayments:<int64> = 52; orphan order
		// components are local ids) — only a detached 19-digit provider order
		// id plus a large anomaly id could exceed it. An unaddressable action
		// is worse than no action: drop the row and point at the CLI.
		if len(data) > 64 {
			row = nil
			sb.WriteString(b.t(lang, "admin_payreview_card_cli_only"))
			break
		}
		row = append(row, Btn(b.payReviewActionLabel(lang, action), data))
	}
	if len(row) > 0 {
		kb = append(kb, row)
	}
	kb = append(kb, []StyledButton{Btn(b.t(lang, "admin_payreview_back_btn"), "admin:payrev:list")})
	b.sendOrEditStyled(chatID, msgID, sb.String(), "", kb)
}

// onAdminPayReviewPreview is the first tap of an action: build the resolution
// from the freshly reloaded case and validate it against the ledger without
// writing anything.
func (b *Bot) onAdminPayReviewPreview(chatID int64, msgID int, userID int64, ref payReviewRef, lang string) {
	ctx, cancel := b.handlerCtx()
	defer cancel()
	item, err := b.findReviewCase(ctx, ref)
	if err == nil {
		var resolution storage.PaymentReviewResolution
		resolution, err = buildReviewResolution(item, ref.action, userID)
		if err == nil {
			_, err = b.payLedger.PreviewPaymentReviewResolution(ctx, resolution)
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrNotFound):
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payrev_case_gone"), "", StyledKeyboard{})
		case errors.Is(err, storage.ErrPaymentReviewConflict), errors.Is(err, storage.ErrOrderStatusConflict):
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payrev_conflict"), "", StyledKeyboard{})
		default:
			b.logger.Error("preview payment review", "error", err)
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payreview_failed"), "", StyledKeyboard{})
		}
		return
	}

	targets := len(item.Targets)
	text := b.i18n.Tf(lang, "admin_payreview_preview",
		item.OrderID, item.Provider, payReviewStateLabel(payReviewResultState(item, ref.action)), targets)
	kb := StyledKeyboard{
		{Btn(b.t(lang, "admin_payreview_confirm_btn"), payReviewCaseCallback("admin:payrevdo:", ref.action, item))},
		{Btn(b.t(lang, "admin_payreview_back_btn"), payReviewCaseCallback("admin:payrev:", "", item))},
	}
	b.sendOrEditStyled(chatID, msgID, text, "", kb)
}

// payReviewResultState mirrors the state buildReviewResolution would request,
// for preview display only.
func payReviewResultState(item storage.PaymentReviewCase, action string) string {
	resolution, err := buildReviewResolution(item, action, 0)
	if err != nil {
		return ""
	}
	return resolution.ResultingPaymentState
}

// onAdminPayReviewConfirm is the second tap: reload, rebuild, re-preview (the
// target set may have changed since the first tap), then apply.
func (b *Bot) onAdminPayReviewConfirm(chatID int64, msgID int, userID int64, ref payReviewRef, lang string) {
	ctx, cancel := b.handlerCtx()
	defer cancel()
	item, err := b.findReviewCase(ctx, ref)
	if err == nil {
		var resolution storage.PaymentReviewResolution
		resolution, err = buildReviewResolution(item, ref.action, userID)
		if err == nil {
			if _, err = b.payLedger.PreviewPaymentReviewResolution(ctx, resolution); err == nil {
				err = b.payLedger.ResolvePaymentReview(ctx, resolution)
			}
		}
	}
	switch {
	case err == nil:
		state := payReviewStateLabel(payReviewResultState(item, ref.action))
		b.sendOrEditStyled(chatID, msgID,
			b.i18n.Tf(lang, "admin_payrev_resolved", item.OrderID, state), "", StyledKeyboard{})
	case errors.Is(err, storage.ErrPaymentNeedsReview):
		// This provider's targets are acknowledged; other providers keep the
		// order quarantined. Treat it as recorded but not terminal.
		b.sendOrEditStyled(chatID, msgID,
			b.i18n.Tf(lang, "admin_payrev_resolved", item.OrderID, storage.PaymentStateNeedsReview), "", StyledKeyboard{})
	case errors.Is(err, storage.ErrNotFound):
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payrev_case_gone"), "", StyledKeyboard{})
	case errors.Is(err, storage.ErrPaymentReviewConflict) || errors.Is(err, storage.ErrOrderStatusConflict):
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payrev_conflict"), "", StyledKeyboard{})
	default:
		b.logger.Error("resolve payment review", "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payreview_failed"), "", StyledKeyboard{})
	}
}

// buildReviewResolution maps one case plus a button action to the exact
// resolution the ledger will accept. It never invents targets: the event,
// anomaly and order-target sets are copied verbatim from the case. The
// storage layer remains the final validator — this helper only picks the
// candidate state/decision pair for the case shape:
//
//   - provider "unknown": only cancelled+dismissed validates (never revenue);
//   - orphan anomaly (no local order): no projection to move, so the state is
//     empty (or cancelled for a detached positive provider order ID) and the
//     decision acknowledges the fact — settle ⇒ compensated, refund ⇒
//     accepted_refund, dismiss ⇒ derived from ledger evidence;
//   - attached case: settle/refund/dismiss pick their terminal state with an
//     implicit decision, except a lone anomaly whose refund requires the
//     explicit compensated decision.
func buildReviewResolution(item storage.PaymentReviewCase, action string, adminID int64) (storage.PaymentReviewResolution, error) {
	resolution := storage.PaymentReviewResolution{
		OrderID: item.OrderID, Provider: item.Provider,
		Actor: fmt.Sprintf("admin:%d", adminID), Reason: "admin panel",
	}
	for _, target := range item.Targets {
		switch target.Kind {
		case storage.PaymentReviewTargetEvent:
			resolution.EventIDs = append(resolution.EventIDs, target.ID)
		case storage.PaymentReviewTargetAnomaly:
			resolution.AnomalyIDs = append(resolution.AnomalyIDs, target.ID)
		case storage.PaymentReviewTargetOrder:
			resolution.OrderTargetID = target.ID
		default:
			return storage.PaymentReviewResolution{}, fmt.Errorf("payreview: unknown target kind %q", target.Kind)
		}
	}
	if len(item.Targets) == 0 {
		return storage.PaymentReviewResolution{}, fmt.Errorf("payreview: empty target set")
	}
	if !isPayReviewAction(action) {
		return storage.PaymentReviewResolution{}, fmt.Errorf("payreview: unknown action %q", action)
	}

	if item.Provider == storage.PaymentReviewProviderUnknown {
		// A provider-neutral legacy row has exactly one terminal shape.
		if action != payReviewActionDismiss || resolution.OrderTargetID != resolution.OrderID ||
			len(resolution.EventIDs) != 0 || len(resolution.AnomalyIDs) != 0 {
			return storage.PaymentReviewResolution{}, fmt.Errorf("payreview: unknown-provider case admits only order dismissal")
		}
		resolution.Decision = "dismissed"
		resolution.ResultingPaymentState = storage.PaymentStateCancelled
		return resolution, nil
	}

	if item.PaymentState == "" {
		// A detached provider fact cannot move a local order projection. A
		// zero order ID requires an empty state; a positive provider-proposed
		// ID must carry a valid terminal state instead.
		if resolution.OrderID > 0 {
			resolution.ResultingPaymentState = storage.PaymentStateCancelled
		}
		switch action {
		case payReviewActionSettle:
			resolution.Decision = "compensated"
		case payReviewActionRefund:
			resolution.Decision = "accepted_refund"
		}
		return resolution, nil
	}

	switch action {
	case payReviewActionSettle:
		resolution.ResultingPaymentState = storage.PaymentStateSettled
	case payReviewActionRefund:
		resolution.ResultingPaymentState = storage.PaymentStateRefunded
		if len(resolution.AnomalyIDs) == 1 && len(resolution.EventIDs) == 0 && resolution.OrderTargetID == 0 {
			// A lone no-attempt capture anomaly closes as refunded only
			// through an explicit compensation decision.
			resolution.Decision = "compensated"
		}
	case payReviewActionDismiss:
		resolution.ResultingPaymentState = storage.PaymentStateCancelled
	}
	return resolution, nil
}
