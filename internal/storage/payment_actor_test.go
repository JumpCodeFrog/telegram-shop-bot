package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPaymentActorSchema pins migration 023: both ledger tables carry a
// nullable actor column with the 1..128 CHECK, old rows stay NULL, new rows
// round-trip the value, and over-length actors are rejected.
func TestPaymentActorSchema(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "payment-actor-schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{"payment_events", "payment_anomalies"} {
		var found, nullable int
		rows, err := db.Conn().Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			if name == "actor" {
				found++
				if notnull == 0 {
					nullable = 1
				}
			}
		}
		_ = rows.Close()
		if found != 1 || nullable != 1 {
			t.Fatalf("%s: actor column found=%d nullable=%d, want 1/1", table, found, nullable)
		}
	}
}

// TestPaymentActorCheckConstraint pins the 1..128 length CHECK.
func TestPaymentActorCheckConstraint(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "payment-actor-check.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	long := strings.Repeat("x", 129)
	if _, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint, proposed_order_id, provider, event_kind, external_id, amount_minor, currency, scale, reason, actor)
		VALUES ('fp-check', 0, 'stars', 'captured', 'ext-check', 1, 'XTR', 0, 'check', ?)`, long); err == nil {
		t.Fatal("129-char actor must be rejected by CHECK")
	}
	if _, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint, proposed_order_id, provider, event_kind, external_id, amount_minor, currency, scale, reason, actor)
		VALUES ('fp-ok', 0, 'stars', 'captured', 'ext-ok', 1, 'XTR', 0, 'check', 'webhook:stars')`); err != nil {
		t.Fatalf("valid actor rejected: %v", err)
	}
}

// TestPaymentActorRoundTrip pins the write path end to end: a settle with a
// fact actor stores it; a settle with empty actor stores NULL.
func TestPaymentActorRoundTrip(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "payment-actor-roundtrip.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	// Two independent pending orders from the package's established ledger
	// fixture (seedLedgerOrder, ledger_test.go).
	store, first, _ := seedLedgerOrder(t, db, 250)
	_, second, _ := seedLedgerOrder(t, db, 125)

	at := time.Now().UTC().Truncate(time.Second)
	fact := PaymentFact{
		Provider: PaymentMethodStars, ExternalID: "actor-capture-1",
		AmountMinor: 250, Currency: "XTR", Scale: 0, OccurredAt: at,
		Actor: "webhook:stars",
	}
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, first, OrderStatusPending, OrderStatusPaid, fact); err != nil {
		t.Fatalf("settle with actor: %v", err)
	}
	var got sql.NullString
	if err := db.Conn().QueryRowContext(ctx, `SELECT actor FROM payment_events
		WHERE order_id = ? AND event_kind = ?`, first, PaymentEventCaptured).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.String != "webhook:stars" {
		t.Fatalf("actor on captured event = %+v, want webhook:stars", got)
	}

	empty := fact
	empty.ExternalID = "actor-capture-2"
	empty.AmountMinor = 125
	empty.Actor = ""
	if err := store.UpdateOrderStatusWithPaymentFact(ctx, second, OrderStatusPending, OrderStatusPaid, empty); err != nil {
		t.Fatalf("settle without actor: %v", err)
	}
	var unset sql.NullString
	if err := db.Conn().QueryRowContext(ctx, `SELECT actor FROM payment_events
		WHERE order_id = ? AND event_kind = ?`, second, PaymentEventCaptured).Scan(&unset); err != nil {
		t.Fatal(err)
	}
	if unset.Valid {
		t.Fatalf("actor on empty-actor event = %q, want NULL", unset.String)
	}
}
