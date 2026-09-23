package storage

import (
	"database/sql"
	"strings"
	"testing"
)

// TestMigration023UpgradeKeepsLegacyRows pins the 022→023 ALTER upgrade: rows
// written before the actor column existed keep NULL actor, the column exists
// post-upgrade, and new actor-bearing writes work on the upgraded database.
func TestMigration023UpgradeKeepsLegacyRows(t *testing.T) {
	db := migrationDBBefore(t, "023_payment_actor.sql")
	// Seed one row per table on the 022 schema (no actor column yet).
	if _, err := db.Conn().Exec(`INSERT INTO orders (id, user_id, total_usd, total_stars, payment_method, payment_id, status)
		VALUES (1, 42, 1, 100, 'stars', 'seed-023', 'paid')`); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	if _, err := db.Conn().Exec(`INSERT INTO payment_events
		(order_id, provider, event_kind, external_id, amount_minor, currency, scale, disposition)
		VALUES (1, 'stars', 'captured', 'seed-evt-023', 100, 'XTR', 0, 'settled')`); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint, proposed_order_id, provider, event_kind, external_id, amount_minor, currency, scale, reason)
		VALUES ('fp-023', 0, 'stars', 'captured', 'seed-anom-023', 100, 'XTR', 0, 'legacy')`); err != nil {
		t.Fatalf("seed anomaly: %v", err)
	}

	applyMigrationFile(t, db, "023_payment_actor.sql")

	for _, q := range []string{
		`SELECT actor FROM payment_events WHERE external_id = 'seed-evt-023'`,
		`SELECT actor FROM payment_anomalies WHERE external_id = 'seed-anom-023'`,
	} {
		var actor sql.NullString
		if err := db.Conn().QueryRow(q).Scan(&actor); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if actor.Valid {
			t.Fatalf("%s: legacy row actor = %q, want NULL", q, actor.String)
		}
	}
	// New writes work on the upgraded DB, and the CHECK is live there too.
	if _, err := db.Conn().Exec(`UPDATE payment_events SET actor = 'webhook:stars' WHERE external_id = 'seed-evt-023'`); err == nil {
		t.Fatal("ledger immutability: UPDATE must be rejected by trigger (sanity that 020 triggers survived)")
	}
	if _, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint, proposed_order_id, provider, event_kind, external_id, amount_minor, currency, scale, reason, actor)
		VALUES ('fp-023-b', 0, 'stars', 'captured', 'seed-anom-023-b', 100, 'XTR', 0, 'new', ?)`,
		strings.Repeat("x", 129)); err == nil {
		t.Fatal("129-char actor must be rejected post-upgrade")
	}
	// A valid actor-bearing write lands on the upgraded table and reads back.
	if _, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint, proposed_order_id, provider, event_kind, external_id, amount_minor, currency, scale, reason, actor)
		VALUES ('fp-023-c', 0, 'stars', 'captured', 'seed-anom-023-c', 100, 'XTR', 0, 'new', 'webhook:stars')`); err != nil {
		t.Fatalf("valid actor insert rejected post-upgrade: %v", err)
	}
	var actor string
	if err := db.Conn().QueryRow(
		`SELECT actor FROM payment_anomalies WHERE external_id = 'seed-anom-023-c'`).Scan(&actor); err != nil {
		t.Fatalf("read back new actor: %v", err)
	}
	if actor != "webhook:stars" {
		t.Fatalf("read back actor = %q, want %q", actor, "webhook:stars")
	}
}
