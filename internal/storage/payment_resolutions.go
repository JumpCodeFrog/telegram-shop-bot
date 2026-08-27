package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ListPaymentReviews returns only local IDs and reason codes. Provider
// transaction identities, payer details and raw payloads stay out of output.
func (s *SQLPaymentLedgerStore) ListPaymentReviews(ctx context.Context, provider string) ([]PaymentReviewCase, error) {
	provider = normalizePaymentProvider(provider)
	if provider != PaymentMethodStars && provider != PaymentMethodCrypto && provider != PaymentReviewProviderUnknown {
		return nil, ErrPaymentReviewConflict
	}
	cases := make(map[int64]*PaymentReviewCase)
	var orphanCases []PaymentReviewCase
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.order_id, o.payment_state, e.event_kind
		FROM payment_events e
		JOIN orders o ON o.id = e.order_id
		LEFT JOIN payment_resolutions r
		  ON r.target_kind = 'payment_event' AND r.target_id = e.id
		WHERE e.provider = ? AND e.disposition = 'needs_review' AND r.id IS NULL
		ORDER BY e.order_id, e.id`, provider)
	if err != nil {
		return nil, fmt.Errorf("ledger: list review events: %w", err)
	}
	for rows.Next() {
		var id, orderID int64
		var state, eventKind string
		if err := rows.Scan(&id, &orderID, &state, &eventKind); err != nil {
			rows.Close()
			return nil, fmt.Errorf("ledger: scan review event: %w", err)
		}
		item := getReviewCase(cases, orderID, provider, state)
		item.Targets = append(item.Targets, PaymentReviewTarget{
			Kind: PaymentReviewTargetEvent, ID: id, ReasonCode: "event_" + eventKind,
		})
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("ledger: close review events: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT a.id, a.proposed_order_id, COALESCE(o.payment_state, ''), a.reason
		FROM payment_anomalies a
		LEFT JOIN orders o ON o.id = a.proposed_order_id
		LEFT JOIN payment_resolutions r
		  ON r.target_kind = 'payment_anomaly' AND r.target_id = a.id
		WHERE a.provider = ? AND r.id IS NULL
		ORDER BY a.proposed_order_id, a.id`, provider)
	if err != nil {
		return nil, fmt.Errorf("ledger: list review anomalies: %w", err)
	}
	for rows.Next() {
		var id, orderID int64
		var state, reason string
		if err := rows.Scan(&id, &orderID, &state, &reason); err != nil {
			rows.Close()
			return nil, fmt.Errorf("ledger: scan review anomaly: %w", err)
		}
		target := PaymentReviewTarget{
			Kind: PaymentReviewTargetAnomaly, ID: id, ReasonCode: reason,
		}
		if state == "" {
			// Detached facts have no real order identity by which they can safely be
			// batched. This includes both order_id=0 and a positive provider-supplied
			// ID that does not exist locally. Expose every immutable fact separately.
			orphanCases = append(orphanCases, PaymentReviewCase{
				OrderID: 0, Provider: provider, PaymentState: state,
				Targets: []PaymentReviewTarget{target},
			})
			orphanCases[len(orphanCases)-1].OrderID = orderID
			continue
		}
		item := getReviewCase(cases, orderID, provider, state)
		item.Targets = append(item.Targets, target)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("ledger: close review anomalies: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT o.id, o.payment_state,
		       COALESCE((SELECT oe.event_type FROM order_events oe
		                 WHERE oe.order_id = o.id ORDER BY oe.id DESC LIMIT 1), 'order_needs_review')
		FROM orders o
			WHERE o.payment_state = 'needs_review'
			  AND (CASE WHEN lower(o.payment_method) IN ('crypto', 'cryptobot') THEN 'crypto'
			            WHEN lower(o.payment_method) = 'stars' THEN 'stars'
			            WHEN o.status = 'pending' AND o.subscription_product_id IS NOT NULL THEN 'stars'
			            ELSE 'unknown' END) = ?
		  AND NOT EXISTS (
		      SELECT 1 FROM payment_events e
		      LEFT JOIN payment_resolutions r
		        ON r.target_kind = 'payment_event' AND r.target_id = e.id
		      WHERE e.order_id = o.id AND e.disposition = 'needs_review' AND r.id IS NULL)
		  AND NOT EXISTS (
		      SELECT 1 FROM payment_anomalies a
		      LEFT JOIN payment_resolutions r
		        ON r.target_kind = 'payment_anomaly' AND r.target_id = a.id
		      WHERE a.proposed_order_id = o.id AND r.id IS NULL)
		  AND NOT EXISTS (
		      SELECT 1 FROM payment_resolutions r
		      WHERE r.target_kind = 'order' AND r.target_id = o.id)
		ORDER BY o.id`, provider)
	if err != nil {
		return nil, fmt.Errorf("ledger: list review orders: %w", err)
	}
	for rows.Next() {
		var orderID int64
		var state, reason string
		if err := rows.Scan(&orderID, &state, &reason); err != nil {
			rows.Close()
			return nil, fmt.Errorf("ledger: scan review order: %w", err)
		}
		item := getReviewCase(cases, orderID, provider, state)
		item.Targets = append(item.Targets, PaymentReviewTarget{
			Kind: PaymentReviewTargetOrder, ID: orderID, ReasonCode: reason,
		})
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("ledger: close review orders: %w", err)
	}

	result := make([]PaymentReviewCase, 0, len(cases)+len(orphanCases))
	for _, item := range cases {
		sortReviewTargets(item.Targets)
		result = append(result, *item)
	}
	result = append(result, orphanCases...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].OrderID != result[j].OrderID {
			return result[i].OrderID < result[j].OrderID
		}
		if len(result[i].Targets) == 0 || len(result[j].Targets) == 0 {
			return len(result[i].Targets) < len(result[j].Targets)
		}
		if result[i].Targets[0].Kind != result[j].Targets[0].Kind {
			return result[i].Targets[0].Kind < result[j].Targets[0].Kind
		}
		return result[i].Targets[0].ID < result[j].Targets[0].ID
	})
	return result, nil
}

func getReviewCase(cases map[int64]*PaymentReviewCase, orderID int64, provider, state string) *PaymentReviewCase {
	item := cases[orderID]
	if item == nil {
		item = &PaymentReviewCase{OrderID: orderID, Provider: provider, PaymentState: state}
		cases[orderID] = item
	}
	return item
}

func sortReviewTargets(targets []PaymentReviewTarget) {
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Kind == targets[j].Kind {
			return targets[i].ID < targets[j].ID
		}
		return targets[i].Kind < targets[j].Kind
	})
}

// PreviewPaymentReviewResolution validates an exact target set without writes.
func (s *SQLPaymentLedgerStore) PreviewPaymentReviewResolution(ctx context.Context, resolution PaymentReviewResolution) (*PaymentReviewCase, error) {
	if err := validateReviewResolutionInput(&resolution); err != nil {
		return nil, err
	}
	current, err := loadPaymentReviewCase(ctx, s.db, resolution.OrderID, resolution.Provider, resolution.AnomalyIDs)
	if err != nil {
		return nil, err
	}
	if current.PaymentState != "" && current.PaymentState != PaymentStateNeedsReview {
		return nil, ErrOrderStatusConflict
	}
	if err := matchReviewTargets(current, resolution); err != nil {
		return nil, err
	}
	decisions := make([]string, len(current.Targets))
	for i, target := range current.Targets {
		decision, err := reviewTargetDecision(ctx, s.db, target, resolution.Decision, resolution.ResultingPaymentState)
		if err != nil {
			return nil, err
		}
		decisions[i] = decision
	}
	if current.PaymentState != "" {
		remaining, err := countUnresolvedOtherProvider(ctx, s.db, resolution.OrderID, resolution.Provider)
		if err != nil {
			return nil, err
		}
		current.RemainingTargets = remaining
		if remaining > 0 {
			if resolution.ResultingPaymentState != PaymentStateNeedsReview {
				return nil, ErrPaymentReviewConflict
			}
		} else {
			derived, _, err := deriveResolutionProjection(ctx, s.db, resolution, current.Targets, decisions)
			if err != nil {
				return nil, err
			}
			if derived != resolution.ResultingPaymentState {
				return nil, ErrPaymentReviewConflict
			}
		}
	}
	return &current, nil
}

// ResolvePaymentReview appends acknowledgements for every currently unresolved
// target and moves a known order projection in the same transaction. A changed
// or partial target list fails closed so a new provider fact cannot be skipped.
func (s *SQLPaymentLedgerStore) ResolvePaymentReview(ctx context.Context, resolution PaymentReviewResolution) error {
	if err := validateReviewResolutionInput(&resolution); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ledger: begin review resolution: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if resolvedBatchMatches(ctx, tx, resolution) {
		stillReview := false
		if resolution.OrderID > 0 && resolution.Provider != PaymentReviewProviderUnknown {
			var state string
			_ = tx.QueryRowContext(ctx, `SELECT payment_state FROM orders WHERE id = ?`, resolution.OrderID).Scan(&state)
			stillReview = state == PaymentStateNeedsReview
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("ledger: commit exact review replay: %w", err)
		}
		if stillReview {
			return ErrPaymentNeedsReview
		}
		return nil
	}

	current, err := loadPaymentReviewCase(ctx, tx, resolution.OrderID, resolution.Provider, resolution.AnomalyIDs)
	if err != nil {
		if errors.Is(err, ErrNotFound) && resolvedBatchMatches(ctx, tx, resolution) {
			stillReview := false
			if resolution.OrderID > 0 && resolution.Provider != PaymentReviewProviderUnknown {
				var state string
				_ = tx.QueryRowContext(ctx, `SELECT payment_state FROM orders WHERE id = ?`, resolution.OrderID).Scan(&state)
				stillReview = state == PaymentStateNeedsReview
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			if stillReview {
				return ErrPaymentNeedsReview
			}
			return nil
		}
		return err
	}
	if current.PaymentState != "" && current.PaymentState != PaymentStateNeedsReview {
		if current.PaymentState == resolution.ResultingPaymentState && resolvedBatchMatches(ctx, tx, resolution) {
			return tx.Commit()
		}
		return ErrOrderStatusConflict
	}
	if err := matchReviewTargets(current, resolution); err != nil {
		return err
	}
	decisions := make([]string, len(current.Targets))
	for i, target := range current.Targets {
		decision, err := reviewTargetDecision(ctx, tx, target, resolution.Decision, resolution.ResultingPaymentState)
		if err != nil {
			return err
		}
		decisions[i] = decision
	}
	remainingOther := 0
	derivedState, legacyStatus := "", ""
	if current.PaymentState != "" {
		remainingOther, err = countUnresolvedOtherProvider(ctx, tx, resolution.OrderID, resolution.Provider)
		if err != nil {
			return err
		}
		if remainingOther > 0 {
			if resolution.ResultingPaymentState != PaymentStateNeedsReview {
				return ErrPaymentReviewConflict
			}
		} else {
			derivedState, legacyStatus, err = deriveResolutionProjection(ctx, tx, resolution, current.Targets, decisions)
			if err != nil {
				return err
			}
			if resolution.ResultingPaymentState != derivedState {
				return ErrPaymentReviewConflict
			}
		}
	}
	for i, target := range current.Targets {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO payment_resolutions
				(order_id, provider, target_kind, target_id, decision, actor, reason, resulting_payment_state)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			resolution.OrderID, resolution.Provider, target.Kind, target.ID,
			decisions[i], resolution.Actor, resolution.Reason, resolution.ResultingPaymentState); err != nil {
			return fmt.Errorf("ledger: append review resolution: %w", err)
		}
	}
	if current.PaymentState != "" {
		if remainingOther > 0 {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("ledger: commit partial review resolution: %w", err)
			}
			return ErrPaymentNeedsReview
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE orders
			SET status = ?,
			    order_state = CASE WHEN ? = 'cancelled' THEN 'cancelled' ELSE order_state END,
			    payment_state = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND payment_state = 'needs_review'`,
			legacyStatus, legacyStatus, derivedState, resolution.OrderID)
		if err != nil {
			return fmt.Errorf("ledger: update resolved projection: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return ErrOrderStatusConflict
		}
		if err := appendOrderEvent(ctx, tx, resolution.OrderID, "payment.review_resolved",
			PaymentStateNeedsReview, derivedState); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger: commit review resolution: %w", err)
	}
	if current.PaymentState != "" && resolution.ResultingPaymentState == PaymentStateNeedsReview {
		return ErrPaymentNeedsReview
	}
	return nil
}

type reviewQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadPaymentReviewCase(ctx context.Context, q reviewQueryer, orderID int64, provider string, orphanAnomalyIDs []int64) (PaymentReviewCase, error) {
	provider = normalizePaymentProvider(provider)
	item := PaymentReviewCase{OrderID: orderID, Provider: provider}
	orderFound := false
	if orderID > 0 {
		if err := q.QueryRowContext(ctx, `SELECT payment_state FROM orders WHERE id = ?`, orderID).Scan(&item.PaymentState); err == nil {
			orderFound = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return item, fmt.Errorf("ledger: load review order: %w", err)
		}
	}
	if !orderFound {
		// A detached provider fact cannot be grouped by a local order. Load only
		// the opaque local IDs named by the operator so unrelated facts remain
		// independent even when the provider proposed the same nonexistent ID.
		for _, anomalyID := range orphanAnomalyIDs {
			var target PaymentReviewTarget
			target.Kind = PaymentReviewTargetAnomaly
			if err := q.QueryRowContext(ctx, `
				SELECT a.id, a.reason FROM payment_anomalies a
				LEFT JOIN payment_resolutions r
				  ON r.target_kind = 'payment_anomaly' AND r.target_id = a.id
				WHERE a.id = ? AND a.proposed_order_id = ? AND a.provider = ? AND r.id IS NULL`,
				anomalyID, orderID, provider).Scan(&target.ID, &target.ReasonCode); errors.Is(err, sql.ErrNoRows) {
				return item, ErrNotFound
			} else if err != nil {
				return item, fmt.Errorf("ledger: load exact detached anomaly: %w", err)
			}
			item.Targets = append(item.Targets, target)
		}
		if len(item.Targets) == 0 {
			return item, ErrNotFound
		}
		sortReviewTargets(item.Targets)
		return item, nil
	}
	rows, err := q.QueryContext(ctx, `
		SELECT e.id, e.event_kind FROM payment_events e
		LEFT JOIN payment_resolutions r
		  ON r.target_kind = 'payment_event' AND r.target_id = e.id
		WHERE e.order_id = ? AND e.provider = ? AND e.disposition = 'needs_review' AND r.id IS NULL
		ORDER BY e.id`, orderID, provider)
	if err != nil {
		return item, fmt.Errorf("ledger: load review events: %w", err)
	}
	for rows.Next() {
		var target PaymentReviewTarget
		target.Kind = PaymentReviewTargetEvent
		var eventKind string
		if err := rows.Scan(&target.ID, &eventKind); err != nil {
			rows.Close()
			return item, fmt.Errorf("ledger: scan review target: %w", err)
		}
		target.ReasonCode = "event_" + eventKind
		item.Targets = append(item.Targets, target)
	}
	if err := rows.Close(); err != nil {
		return item, fmt.Errorf("ledger: close review targets: %w", err)
	}
	rows, err = q.QueryContext(ctx, `
		SELECT a.id, a.reason FROM payment_anomalies a
		LEFT JOIN payment_resolutions r
		  ON r.target_kind = 'payment_anomaly' AND r.target_id = a.id
		WHERE a.proposed_order_id = ? AND a.provider = ? AND r.id IS NULL
		ORDER BY a.id`, orderID, provider)
	if err != nil {
		return item, fmt.Errorf("ledger: load review anomalies: %w", err)
	}
	for rows.Next() {
		var target PaymentReviewTarget
		target.Kind = PaymentReviewTargetAnomaly
		if err := rows.Scan(&target.ID, &target.ReasonCode); err != nil {
			rows.Close()
			return item, fmt.Errorf("ledger: scan review anomaly target: %w", err)
		}
		item.Targets = append(item.Targets, target)
	}
	if err := rows.Close(); err != nil {
		return item, fmt.Errorf("ledger: close review anomaly targets: %w", err)
	}
	if orderID > 0 && len(item.Targets) == 0 && item.PaymentState == PaymentStateNeedsReview {
		var reason string
		err := q.QueryRowContext(ctx, `
			SELECT COALESCE((SELECT event_type FROM order_events
			                 WHERE order_id = ? ORDER BY id DESC LIMIT 1), 'order_needs_review')
			WHERE NOT EXISTS (SELECT 1 FROM payment_resolutions
			                  WHERE target_kind = 'order' AND target_id = ?)
			  AND NOT EXISTS (
			      SELECT 1 FROM payment_events e
			      WHERE e.order_id = ? AND e.disposition = 'needs_review'
			        AND NOT EXISTS (SELECT 1 FROM payment_resolutions r
			                        WHERE r.target_kind = 'payment_event' AND r.target_id = e.id))
			  AND NOT EXISTS (
			      SELECT 1 FROM payment_anomalies a
			      WHERE a.proposed_order_id = ?
			        AND NOT EXISTS (SELECT 1 FROM payment_resolutions r
			                        WHERE r.target_kind = 'payment_anomaly' AND r.target_id = a.id))`,
			orderID, orderID, orderID, orderID).Scan(&reason)
		if err == nil {
			item.Targets = append(item.Targets, PaymentReviewTarget{
				Kind: PaymentReviewTargetOrder, ID: orderID, ReasonCode: reason,
			})
		} else if !errors.Is(err, sql.ErrNoRows) {
			return item, fmt.Errorf("ledger: load review order target: %w", err)
		}
	}
	if len(item.Targets) == 0 {
		return item, ErrNotFound
	}
	sortReviewTargets(item.Targets)
	return item, nil
}

func validateReviewResolutionInput(r *PaymentReviewResolution) error {
	r.Provider = normalizePaymentProvider(r.Provider)
	r.Decision = strings.TrimSpace(r.Decision)
	r.Actor = strings.TrimSpace(r.Actor)
	r.Reason = strings.TrimSpace(r.Reason)
	if r.OrderID < 0 || (r.Provider != PaymentMethodStars && r.Provider != PaymentMethodCrypto && r.Provider != PaymentReviewProviderUnknown) ||
		r.Actor == "" || len(r.Actor) > 128 || r.Reason == "" || len(r.Reason) > 512 {
		return ErrPaymentReviewConflict
	}
	if r.Provider == PaymentReviewProviderUnknown {
		if r.OrderID <= 0 || len(r.EventIDs) != 0 || len(r.AnomalyIDs) != 0 ||
			r.OrderTargetID != r.OrderID || r.Decision != "dismissed" ||
			r.ResultingPaymentState != PaymentStateCancelled {
			return ErrPaymentReviewConflict
		}
		return nil
	}
	if r.OrderID == 0 {
		if len(r.EventIDs) != 0 || r.OrderTargetID != 0 || r.ResultingPaymentState != "" {
			return ErrPaymentReviewConflict
		}
	} else {
		switch r.ResultingPaymentState {
		case PaymentStateSettled, PaymentStatePartiallyRefunded, PaymentStateRefunded, PaymentStateCancelled, PaymentStateNeedsReview:
		default:
			return ErrPaymentReviewConflict
		}
	}
	if r.Decision != "" {
		if (r.Decision != "compensated" && r.Decision != "accepted_refund") ||
			len(r.AnomalyIDs) != 1 || r.OrderTargetID != 0 {
			return ErrPaymentReviewConflict
		}
		// The explicit decision applies only to the one no-attempt anomaly. Any
		// attached event targets still derive their own decisions from immutable
		// capture/refund rows. An explicit decision is never a path back to settled
		// revenue; it can record compensation or keep the order quarantined.
		if r.OrderID > 0 && r.ResultingPaymentState != PaymentStateRefunded &&
			r.ResultingPaymentState != PaymentStateNeedsReview &&
			r.ResultingPaymentState != PaymentStateCancelled {
			return ErrPaymentReviewConflict
		}
	}
	if !uniquePositiveIDs(r.EventIDs) || !uniquePositiveIDs(r.AnomalyIDs) || r.OrderTargetID < 0 ||
		(r.OrderTargetID > 0 && r.OrderTargetID != r.OrderID) ||
		len(r.EventIDs)+len(r.AnomalyIDs)+boolInt(r.OrderTargetID > 0) == 0 {
		return ErrPaymentReviewConflict
	}
	return nil
}

func uniquePositiveIDs(ids []int64) bool {
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func matchReviewTargets(current PaymentReviewCase, r PaymentReviewResolution) error {
	want := make(map[string]struct{}, len(r.EventIDs)+len(r.AnomalyIDs)+1)
	for _, id := range r.EventIDs {
		want[fmt.Sprintf("%s:%d", PaymentReviewTargetEvent, id)] = struct{}{}
	}
	for _, id := range r.AnomalyIDs {
		want[fmt.Sprintf("%s:%d", PaymentReviewTargetAnomaly, id)] = struct{}{}
	}
	if r.OrderTargetID > 0 {
		want[fmt.Sprintf("%s:%d", PaymentReviewTargetOrder, r.OrderTargetID)] = struct{}{}
	}
	if len(want) != len(current.Targets) {
		return ErrPaymentReviewConflict
	}
	for _, target := range current.Targets {
		if _, ok := want[fmt.Sprintf("%s:%d", target.Kind, target.ID)]; !ok {
			return ErrPaymentReviewConflict
		}
	}
	return nil
}

func resolvedBatchMatches(ctx context.Context, q reviewQueryer, r PaymentReviewResolution) bool {
	for kind, ids := range map[string][]int64{
		PaymentReviewTargetEvent: r.EventIDs, PaymentReviewTargetAnomaly: r.AnomalyIDs,
		PaymentReviewTargetOrder: []int64{r.OrderTargetID},
	} {
		for _, id := range ids {
			if id == 0 {
				continue
			}
			// A no-attempt anomaly is terminal only because the operator supplied an
			// explicit decision. Do not let a later request with an empty decision
			// masquerade as an exact replay merely because the durable row exists.
			if kind == PaymentReviewTargetAnomaly && r.Decision == "" {
				if _, err := reviewAnomalyDecision(ctx, q, id, "", r.ResultingPaymentState); err != nil {
					return false
				}
			}
			explicitDecision := ""
			if kind == PaymentReviewTargetAnomaly {
				explicitDecision = r.Decision
			}
			var count int
			err := q.QueryRowContext(ctx, `
					SELECT COUNT(*) FROM payment_resolutions
					WHERE order_id = ? AND provider = ? AND target_kind = ? AND target_id = ?
					  AND actor = ? AND reason = ? AND resulting_payment_state = ?
					  AND (? = '' OR decision = ?)`,
				r.OrderID, r.Provider, kind, id, r.Actor, r.Reason, r.ResultingPaymentState,
				explicitDecision, explicitDecision).Scan(&count)
			if err != nil || count != 1 {
				return false
			}
		}
	}
	return true
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func countUnresolvedOrderReviews(ctx context.Context, q reviewQueryer, orderID int64) (int, error) {
	var count int
	err := q.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM payment_events e
		   WHERE e.order_id = ? AND e.disposition = 'needs_review'
		     AND NOT EXISTS (SELECT 1 FROM payment_resolutions r
		                     WHERE r.target_kind = 'payment_event' AND r.target_id = e.id))
		+ (SELECT COUNT(*) FROM payment_anomalies a
		   WHERE a.proposed_order_id = ?
		     AND NOT EXISTS (SELECT 1 FROM payment_resolutions r
		                     WHERE r.target_kind = 'payment_anomaly' AND r.target_id = a.id))`,
		orderID, orderID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("ledger: count unresolved order reviews: %w", err)
	}
	return count, nil
}

func countUnresolvedOtherProvider(ctx context.Context, q reviewQueryer, orderID int64, provider string) (int, error) {
	var count int
	err := q.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM payment_events e
		   WHERE e.order_id = ? AND e.provider <> ? AND e.disposition = 'needs_review'
		     AND NOT EXISTS (SELECT 1 FROM payment_resolutions r
		                     WHERE r.target_kind = 'payment_event' AND r.target_id = e.id))
		+ (SELECT COUNT(*) FROM payment_anomalies a
		   WHERE a.proposed_order_id = ? AND a.provider <> ?
		     AND NOT EXISTS (SELECT 1 FROM payment_resolutions r
		                     WHERE r.target_kind = 'payment_anomaly' AND r.target_id = a.id))`,
		orderID, provider, orderID, provider).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("ledger: count cross-provider reviews: %w", err)
	}
	return count, nil
}

func deriveResolutionProjection(ctx context.Context, q reviewQueryer, resolution PaymentReviewResolution,
	targets []PaymentReviewTarget, decisions []string) (paymentState, legacyStatus string, err error) {
	if resolution.Provider == PaymentReviewProviderUnknown {
		if resolution.Decision != "dismissed" || resolution.ResultingPaymentState != PaymentStateCancelled ||
			len(targets) != 1 || len(decisions) != 1 || targets[0].Kind != PaymentReviewTargetOrder ||
			decisions[0] != "dismissed" {
			return "", "", ErrPaymentReviewConflict
		}
		var currentStatus string
		if err := q.QueryRowContext(ctx, `SELECT status FROM orders WHERE id = ?`, resolution.OrderID).Scan(&currentStatus); err != nil {
			return "", "", fmt.Errorf("ledger: load neutral import projection: %w", err)
		}
		if currentStatus != OrderStatusPaid && currentStatus != OrderStatusDelivered {
			return "", "", ErrPaymentReviewConflict
		}
		// A provider-neutral legacy row has no authenticated rail identity. The
		// only terminal acknowledgement cancels it and never creates settled
		// revenue. An actual provider fact must use the guarded ingress workflow.
		return PaymentStateCancelled, OrderStatusCancelled, nil
	}
	if resolution.Decision == "" {
		return deriveReviewProjection(ctx, q, resolution.OrderID)
	}
	if err := q.QueryRowContext(ctx, `SELECT status FROM orders WHERE id = ?`, resolution.OrderID).Scan(&legacyStatus); err != nil {
		return "", "", fmt.Errorf("ledger: load explicit review projection: %w", err)
	}
	if resolution.ResultingPaymentState == PaymentStateNeedsReview {
		return PaymentStateNeedsReview, legacyStatus, nil
	}
	for _, target := range targets {
		if target.Kind == PaymentReviewTargetEvent {
			// Once authenticated capture/refund rows are attached, those rows derive
			// the projection. The explicit anomaly decision acknowledges only the
			// otherwise unverifiable legacy fact and cannot override event evidence.
			return deriveReviewProjection(ctx, q, resolution.OrderID)
		}
	}
	if resolution.ResultingPaymentState != PaymentStateRefunded || resolution.Decision != "compensated" ||
		len(targets) != 1 || len(decisions) != 1 || decisions[0] != "compensated" ||
		targets[0].Kind != PaymentReviewTargetAnomaly {
		return "", "", ErrPaymentReviewConflict
	}
	if legacyStatus != OrderStatusPaid && legacyStatus != OrderStatusDelivered {
		return "", "", ErrPaymentReviewConflict
	}
	var anomalyOrder int64
	var kind string
	if err := q.QueryRowContext(ctx, `SELECT proposed_order_id, event_kind FROM payment_anomalies WHERE id = ?`,
		targets[0].ID).Scan(&anomalyOrder, &kind); err != nil {
		return "", "", fmt.Errorf("ledger: load explicit compensated anomaly: %w", err)
	}
	if anomalyOrder != resolution.OrderID || kind != PaymentEventCaptured {
		return "", "", ErrPaymentReviewConflict
	}
	var captures int
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM payment_attempts
		WHERE order_id = ? AND status IN ('succeeded', 'needs_review')`, resolution.OrderID).Scan(&captures); err != nil {
		return "", "", fmt.Errorf("ledger: count explicit compensated captures: %w", err)
	}
	// A manual no-attempt compensation can close a migrated payment only when
	// there is no attached capture whose own refund lifecycle should be used.
	if captures != 0 {
		return "", "", ErrPaymentReviewConflict
	}
	return PaymentStateRefunded, legacyStatus, nil
}

func deriveReviewProjection(ctx context.Context, q reviewQueryer, orderID int64) (paymentState, legacyStatus string, err error) {
	if err := q.QueryRowContext(ctx, `SELECT status FROM orders WHERE id = ?`, orderID).Scan(&legacyStatus); err != nil {
		return "", "", fmt.Errorf("ledger: load review projection: %w", err)
	}
	var reviewCaptures, uncompensated int
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN COALESCE((SELECT SUM(r.amount_minor) FROM refunds r
		                          WHERE r.provider = a.provider AND r.payment_external_id = a.external_id
		                            AND r.status = 'succeeded'), 0) <> a.amount_minor
		                         THEN 1 ELSE 0 END), 0)
		FROM payment_attempts a
		WHERE a.order_id = ? AND a.status = 'needs_review'`, orderID).Scan(&reviewCaptures, &uncompensated); err != nil {
		return "", "", fmt.Errorf("ledger: verify review compensation: %w", err)
	}
	if uncompensated > 0 {
		return "", "", ErrPaymentReviewConflict
	}
	if legacyStatus == OrderStatusPending || legacyStatus == OrderStatusCancelled {
		return PaymentStateCancelled, OrderStatusCancelled, nil
	}
	if legacyStatus != OrderStatusPaid && legacyStatus != OrderStatusDelivered {
		return "", "", ErrPaymentReviewConflict
	}
	var captures, refundedCaptures, remainingCaptures int
	if err := q.QueryRowContext(ctx, `
		WITH captures AS (
		  SELECT a.id, a.provider, a.external_id, a.amount_minor,
		         COALESCE((SELECT SUM(r.amount_minor) FROM refunds r
		                   WHERE r.provider = a.provider AND r.payment_external_id = a.external_id
		                     AND r.status = 'succeeded'), 0) AS refunded
		  FROM payment_attempts a WHERE a.order_id = ? AND a.status = 'succeeded'
		)
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN refunded > 0 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN refunded < amount_minor THEN 1 ELSE 0 END), 0)
		FROM captures`, orderID).Scan(&captures, &refundedCaptures, &remainingCaptures); err != nil {
		return "", "", fmt.Errorf("ledger: derive review projection: %w", err)
	}
	if captures == 0 {
		if reviewCaptures > 0 {
			return PaymentStateRefunded, legacyStatus, nil
		}
		return "", "", ErrPaymentReviewConflict
	}
	switch {
	case refundedCaptures == 0:
		paymentState = PaymentStateSettled
	case remainingCaptures == 0:
		paymentState = PaymentStateRefunded
	default:
		paymentState = PaymentStatePartiallyRefunded
	}
	return paymentState, legacyStatus, nil
}

func reviewTargetDecision(ctx context.Context, q reviewQueryer, target PaymentReviewTarget, explicitDecision, resultingState string) (string, error) {
	switch target.Kind {
	case PaymentReviewTargetAnomaly:
		return reviewAnomalyDecision(ctx, q, target.ID, explicitDecision, resultingState)
	case PaymentReviewTargetOrder:
		if explicitDecision == "dismissed" && resultingState == PaymentStateCancelled {
			return "dismissed", nil
		}
		if explicitDecision != "" {
			return "", ErrPaymentReviewConflict
		}
		return "cancelled", nil
	case PaymentReviewTargetEvent:
		// Explicit decisions belong only to no-attempt anomalies in a mixed exact
		// target set. Event decisions always come from their immutable ledger rows.
		var kind, provider, externalID string
		if err := q.QueryRowContext(ctx, `
			SELECT event_kind, provider, external_id FROM payment_events WHERE id = ?`, target.ID).
			Scan(&kind, &provider, &externalID); err != nil {
			return "", fmt.Errorf("ledger: load review decision target: %w", err)
		}
		switch kind {
		case PaymentEventCaptured:
			var amount, refunded int64
			var status string
			if err := q.QueryRowContext(ctx, `
				SELECT a.amount_minor, a.status,
				       COALESCE((SELECT SUM(r.amount_minor) FROM refunds r
				                 WHERE r.provider = a.provider AND r.payment_external_id = a.external_id
				                   AND r.status = 'succeeded'), 0)
				FROM payment_attempts a WHERE a.provider = ? AND a.external_id = ?`,
				provider, externalID).Scan(&amount, &status, &refunded); err != nil {
				return "", fmt.Errorf("ledger: load reviewed capture: %w", err)
			}
			if status != PaymentStateNeedsReview || amount != refunded {
				return "", ErrPaymentReviewConflict
			}
			return "compensated", nil
		case PaymentEventRefunded:
			return "accepted_refund", nil
		case PaymentEventIdentityConflict:
			return "dismissed", nil
		default:
			return "", ErrPaymentReviewConflict
		}
	default:
		return "", ErrPaymentReviewConflict
	}
}

func reviewAnomalyDecision(ctx context.Context, q reviewQueryer, anomalyID int64, explicitDecision, resultingState string) (string, error) {
	var orderID, amount int64
	var provider, kind, externalID, relatedID, currency, reason string
	var scale int
	if err := q.QueryRowContext(ctx, `
		SELECT proposed_order_id, provider, event_kind, external_id,
		       related_external_id, amount_minor, currency, scale, reason
		FROM payment_anomalies WHERE id = ?`, anomalyID).Scan(
		&orderID, &provider, &kind, &externalID, &relatedID, &amount, &currency, &scale, &reason); err != nil {
		return "", fmt.Errorf("ledger: load reviewed anomaly: %w", err)
	}
	switch kind {
	case PaymentEventCaptured:
		var attemptOrder, attemptAmount, refunded int64
		var attemptCurrency, status string
		var attemptScale int
		err := q.QueryRowContext(ctx, `
			SELECT a.order_id, a.amount_minor, a.currency, a.scale, a.status,
			       COALESCE((SELECT SUM(r.amount_minor) FROM refunds r
			                 WHERE r.provider = a.provider AND r.payment_external_id = a.external_id
			                   AND r.status = 'succeeded'), 0)
			FROM payment_attempts a WHERE a.provider = ? AND a.external_id = ?`,
			provider, externalID).Scan(&attemptOrder, &attemptAmount, &attemptCurrency, &attemptScale, &status, &refunded)
		if errors.Is(err, sql.ErrNoRows) {
			decisionOrderID := orderID
			if orderID > 0 {
				var exists int
				if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE id = ?`, orderID).Scan(&exists); err != nil {
					return "", fmt.Errorf("ledger: check anomaly proposed order: %w", err)
				}
				if exists == 0 {
					decisionOrderID = 0
				}
			}
			return explicitNoAttemptAnomalyDecision(decisionOrderID, kind, externalID, relatedID,
				amount, currency, scale, reason, explicitDecision, resultingState)
		}
		if err != nil {
			return "", fmt.Errorf("ledger: load anomaly capture evidence: %w", err)
		}
		if attemptAmount != amount || attemptCurrency != currency || attemptScale != scale {
			return "", ErrPaymentReviewConflict
		}
		if attemptOrder != orderID {
			return exactExplicitDecision("dismissed", explicitDecision)
		}
		if status == PaymentStateNeedsReview && refunded == attemptAmount {
			return exactExplicitDecision("compensated", explicitDecision)
		}
		if status == "succeeded" {
			return exactExplicitDecision("dismissed", explicitDecision)
		}
		return "", ErrPaymentReviewConflict
	case PaymentEventRefunded:
		var refundOrder, refundAmount int64
		var parentID, refundCurrency, status string
		var refundScale int
		err := q.QueryRowContext(ctx, `
			SELECT order_id, payment_external_id, amount_minor, currency, scale, status
			FROM refunds WHERE provider = ? AND external_id = ?`, provider, externalID).Scan(
			&refundOrder, &parentID, &refundAmount, &refundCurrency, &refundScale, &status)
		if errors.Is(err, sql.ErrNoRows) {
			decisionOrderID := orderID
			if orderID > 0 {
				var exists int
				if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE id = ?`, orderID).Scan(&exists); err != nil {
					return "", fmt.Errorf("ledger: check refund anomaly proposed order: %w", err)
				}
				if exists == 0 {
					decisionOrderID = 0
				}
			}
			return explicitNoAttemptAnomalyDecision(decisionOrderID, kind, externalID, relatedID,
				amount, currency, scale, reason, explicitDecision, resultingState)
		}
		if err != nil {
			return "", fmt.Errorf("ledger: load anomaly refund evidence: %w", err)
		}
		if parentID != relatedID || refundAmount != amount || refundCurrency != currency || refundScale != scale || status != "succeeded" {
			return "", ErrPaymentReviewConflict
		}
		if refundOrder != orderID {
			return exactExplicitDecision("dismissed", explicitDecision)
		}
		return exactExplicitDecision("accepted_refund", explicitDecision)
	default:
		return "", ErrPaymentReviewConflict
	}
}

func exactExplicitDecision(derived, explicit string) (string, error) {
	if explicit != "" && explicit != derived {
		return "", ErrPaymentReviewConflict
	}
	return derived, nil
}

// explicitNoAttemptAnomalyDecision is the narrow operator gate for an
// authenticated provider fact which cannot be attached to payment_attempts or
// refunds. The decision is never allowed to manufacture settled revenue.
func explicitNoAttemptAnomalyDecision(orderID int64, kind, externalID, relatedID string,
	amount int64, currency string, scale int, reason, explicitDecision, resultingState string) (string, error) {
	if amount <= 0 || strings.TrimSpace(currency) == "" || scale < 0 || scale > 9 || explicitDecision == "" {
		return "", ErrPaymentReviewConflict
	}
	// Legacy rows can have lost their provider transaction ID; every live
	// provider fact must retain one before it can receive a terminal decision.
	if strings.TrimSpace(externalID) == "" && reason != "legacy_capture_unverifiable" {
		return "", ErrPaymentReviewConflict
	}
	switch kind {
	case PaymentEventCaptured:
		if explicitDecision != "compensated" {
			return "", ErrPaymentReviewConflict
		}
		if orderID > 0 && resultingState != PaymentStateRefunded && resultingState != PaymentStateNeedsReview {
			return "", ErrPaymentReviewConflict
		}
		return explicitDecision, nil
	case PaymentEventRefunded:
		if explicitDecision != "accepted_refund" || strings.TrimSpace(externalID) == "" || strings.TrimSpace(relatedID) == "" {
			return "", ErrPaymentReviewConflict
		}
		// Without a durable refunds row the accepted provider fact may be
		// acknowledged, but a known order remains quarantined until ingress binds
		// it to its parent capture.
		if orderID > 0 && resultingState != PaymentStateNeedsReview {
			return "", ErrPaymentReviewConflict
		}
		return explicitDecision, nil
	default:
		return "", ErrPaymentReviewConflict
	}
}
