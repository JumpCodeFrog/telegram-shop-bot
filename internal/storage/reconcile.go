package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ProviderTransaction is the normalized, non-secret portion of a provider
// ledger row. Refunds may reuse the original Stars transaction ID, so Kind is
// part of the identity.
type ProviderTransaction struct {
	Provider       string
	Kind           string
	ExternalID     string
	OrderID        int64
	PayloadValid   bool
	AmountMinor    int64
	Currency       string
	Scale          int
	NanostarAmount int
	PayerID        int64
	OccurredAt     time.Time
}

type ReconciliationReport struct {
	ProviderRows   int
	Matched        int
	ProviderOnly   int
	AmountMismatch int
	NeedsReview    int
	WindowComplete bool
	DuplicateRows  int
	LocalOnly      int
}

// Reconcile compares a bounded provider window with local immutable events.
// It never settles, refunds, or retries money operations.
func (s *SQLPaymentLedgerStore) Reconcile(ctx context.Context, provider string, rows []ProviderTransaction, windowComplete bool) (ReconciliationReport, error) {
	report := ReconciliationReport{ProviderRows: len(rows), WindowComplete: windowComplete}
	provider = normalizePaymentProvider(provider)
	seen := make(map[string]ProviderTransaction, len(rows))
	for _, row := range rows {
		kind := row.Kind
		if kind == "" {
			kind = PaymentEventCaptured
		}
		identity := kind + "\x00" + row.ExternalID
		if previous, ok := seen[identity]; ok {
			report.DuplicateRows++
			if previous.OrderID != row.OrderID || previous.PayloadValid != row.PayloadValid ||
				previous.AmountMinor != row.AmountMinor || previous.Currency != row.Currency ||
				previous.Scale != row.Scale || previous.NanostarAmount != row.NanostarAmount ||
				previous.PayerID != row.PayerID || !previous.OccurredAt.Equal(row.OccurredAt) {
				report.AmountMismatch++
			}
			report.NeedsReview++
			continue
		}
		seen[identity] = row
		validMoney := false
		switch provider {
		case PaymentMethodStars:
			validMoney = row.Currency == "XTR" && row.Scale == 0 && row.AmountMinor > 0 && row.NanostarAmount == 0 &&
				row.PayerID > 0 && !row.OccurredAt.IsZero()
		case PaymentMethodCrypto:
			validMoney = (row.Currency == "USD" || row.Currency == "USDT") && row.Scale == 2 && row.AmountMinor > 0 &&
				!row.OccurredAt.IsZero()
		}
		if row.ExternalID == "" || !row.PayloadValid || row.OrderID <= 0 || !validMoney {
			report.AmountMismatch++
			report.NeedsReview++
			continue
		}
		var localOrderID, orderPayerID, localPayerID, amount int64
		var currency, disposition string
		var resolved int
		var scale int
		var localOccurredAt time.Time
		err := s.db.QueryRowContext(ctx,
			`SELECT e.order_id, o.user_id,
			        CASE WHEN e.event_kind = 'captured' THEN COALESCE(a.payer_id, 0)
			             WHEN e.event_kind = 'refunded' THEN COALESCE(f.payer_id, 0)
			             ELSE 0 END,
			        e.amount_minor, e.currency, e.scale, e.disposition, e.occurred_at,
			        CASE WHEN r.id IS NULL THEN 0 ELSE 1 END
			 FROM payment_events e
			 JOIN orders o ON o.id = e.order_id
			 LEFT JOIN payment_attempts a ON a.id = e.payment_attempt_id
			 LEFT JOIN refunds f
			   ON f.provider = e.provider AND f.external_id = e.external_id
			 LEFT JOIN payment_resolutions r
			   ON r.target_kind = 'payment_event' AND r.target_id = e.id
			 WHERE e.provider = ? AND e.event_kind = ? AND e.external_id = ?`,
			provider, kind, row.ExternalID).Scan(&localOrderID, &orderPayerID, &localPayerID,
			&amount, &currency, &scale, &disposition, &localOccurredAt, &resolved)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				report.ProviderOnly++
				continue
			}
			return report, fmt.Errorf("ledger: reconcile lookup: %w", err)
		}
		if localOrderID != row.OrderID || (provider == PaymentMethodStars &&
			(orderPayerID != row.PayerID || (localPayerID > 0 && localPayerID != row.PayerID))) ||
			!localOccurredAt.Equal(row.OccurredAt.UTC()) || amount != row.AmountMinor || currency != row.Currency ||
			scale != row.Scale || (disposition != PaymentDispositionSettled && resolved == 0) {
			report.AmountMismatch++
			report.NeedsReview++
			continue
		}
		report.Matched++
	}
	if windowComplete {
		query := `SELECT event_kind, external_id FROM payment_events
		          WHERE provider = ? AND event_kind IN (?, ?)`
		local, err := s.db.QueryContext(ctx, query, provider, PaymentEventCaptured, PaymentEventRefunded)
		if err != nil {
			return report, fmt.Errorf("ledger: list local reconcile identities: %w", err)
		}
		defer local.Close()
		for local.Next() {
			var kind, externalID string
			if err := local.Scan(&kind, &externalID); err != nil {
				return report, fmt.Errorf("ledger: scan local reconcile identity: %w", err)
			}
			if _, ok := seen[kind+"\x00"+externalID]; !ok {
				report.LocalOnly++
			}
		}
		if err := local.Err(); err != nil {
			return report, fmt.Errorf("ledger: iterate local reconcile identities: %w", err)
		}
	}
	// A provider row may correctly match one order while another order is in
	// needs_review because it claimed the same identity (or legacy import data
	// was incomplete). Such a window must never be reported green.
	var unresolvedEvents, unresolvedOrders, unresolvedAnomalies int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM payment_events e
		 WHERE e.provider = ? AND e.disposition = ?
		   AND NOT EXISTS (SELECT 1 FROM payment_resolutions r
		                   WHERE r.target_kind = 'payment_event' AND r.target_id = e.id)`,
		provider, PaymentDispositionNeedsReview).Scan(&unresolvedEvents); err != nil {
		return report, fmt.Errorf("ledger: count unresolved events: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM orders
		 WHERE payment_state = ?
		   AND CASE WHEN lower(payment_method) IN ('crypto', 'cryptobot') THEN 'crypto'
		            WHEN lower(payment_method) = 'stars' THEN 'stars'
		            WHEN status = 'pending' AND subscription_product_id IS NOT NULL THEN 'stars'
		            ELSE '' END = ?`,
		PaymentStateNeedsReview, provider).Scan(&unresolvedOrders); err != nil {
		return report, fmt.Errorf("ledger: count unresolved orders: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM payment_anomalies a WHERE a.provider = ?
		   AND NOT EXISTS (SELECT 1 FROM payment_resolutions r
		                   WHERE r.target_kind = 'payment_anomaly' AND r.target_id = a.id)`, provider).Scan(&unresolvedAnomalies); err != nil {
		return report, fmt.Errorf("ledger: count payment anomalies: %w", err)
	}
	if unresolvedEvents > report.NeedsReview {
		report.NeedsReview = unresolvedEvents
	}
	if unresolvedOrders > report.NeedsReview {
		report.NeedsReview = unresolvedOrders
	}
	if unresolvedAnomalies > report.NeedsReview {
		report.NeedsReview = unresolvedAnomalies
	}
	return report, nil
}
