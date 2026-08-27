package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const (
	PaymentIngressTargetEvent   = "payment_event"
	PaymentIngressTargetRefund  = "refund"
	PaymentIngressTargetAnomaly = "payment_anomaly"
)

// PaymentIngressAudit attributes an explicit operator write. Provider
// identities remain in their immutable ledger tables; this record references
// the exact local row instead of copying secrets into operator output.
type PaymentIngressAudit struct {
	Actor  string
	Reason string
}

func validatePaymentIngressAudit(audit PaymentIngressAudit) error {
	audit.Actor = strings.TrimSpace(audit.Actor)
	audit.Reason = strings.TrimSpace(audit.Reason)
	if audit.Actor == "" || len(audit.Actor) > 128 || audit.Reason == "" || len(audit.Reason) > 512 {
		return ErrPaymentReviewConflict
	}
	return nil
}

func appendPaymentIngressAudit(ctx context.Context, tx *sql.Tx, orderID int64, provider, eventKind, targetKind string, targetID int64, audit PaymentIngressAudit) error {
	if err := validatePaymentIngressAudit(audit); err != nil {
		return err
	}
	provider = normalizePaymentProvider(provider)
	if orderID < 0 || targetID <= 0 ||
		(provider != PaymentMethodStars && provider != PaymentMethodCrypto) ||
		(eventKind != PaymentEventCaptured && eventKind != PaymentEventRefunded) ||
		(targetKind != PaymentIngressTargetEvent && targetKind != PaymentIngressTargetRefund && targetKind != PaymentIngressTargetAnomaly) {
		return ErrPaymentReviewConflict
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO payment_ingress_audits
			(order_id, provider, event_kind, target_kind, target_id, actor, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		orderID, provider, eventKind, targetKind, targetID,
		strings.TrimSpace(audit.Actor), strings.TrimSpace(audit.Reason)); err != nil {
		return fmt.Errorf("payment ingress: append audit: %w", err)
	}
	return nil
}
