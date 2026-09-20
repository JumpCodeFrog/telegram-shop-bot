package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// preCryptoExpansionRebuildDB applies every migration before 021, leaving the
// schema exactly where production databases stood after migration 020 — the
// state in which any ton/nowpayments/balance ledger INSERT fails the provider
// CHECK.
func preCryptoExpansionRebuildDB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "v20.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	db := &DB{conn: conn}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "021_ledger_provider_crypto_expansion.sql" {
			break
		}
		statements, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := db.applyMigration(entry.Name(), string(statements)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
	}
	return db
}

func applyCryptoExpansionRebuild(t *testing.T, db *DB) {
	t.Helper()
	statements, err := migrationsFS.ReadFile("migrations/021_ledger_provider_crypto_expansion.sql")
	if err != nil {
		t.Fatalf("read 021_ledger_provider_crypto_expansion.sql: %v", err)
	}
	if err := db.applyMigration("021_ledger_provider_crypto_expansion.sql", string(statements)); err != nil {
		t.Fatalf("apply 021_ledger_provider_crypto_expansion.sql: %v", err)
	}
}

// seedCryptoExpansionLegacyRows extends the shared stars/crypto fixture with
// yookassa/stripe rows (the provider identities migration 020 admitted), so
// the 021 rebuild proves lossless across all four pre-021 providers. The
// payment_events rows with non-NULL payment_attempt_id span the rebuild:
// dropping the referenced payment_attempts table mid-migration is exactly
// what the FK-less TEMP parking has to survive.
func seedCryptoExpansionLegacyRows(t *testing.T, db *DB) {
	t.Helper()
	seedLegacyLedgerRows(t, db)
	statements := []string{
		`INSERT INTO payment_attempts
		 (id, order_id, provider, external_id, payer_id, amount_minor, currency, scale, status,
		  entitlement_expires_at, occurred_at, created_at)
		 VALUES (3, 1, 'yookassa', 'seed-att-yookassa', 42, 184908, 'RUB', 2, 'succeeded', NULL,
		         '2026-03-04 05:06:07', '2026-03-04 05:06:08'),
		        (4, 2, 'stripe', 'seed-att-stripe', 43, 1999, 'USD', 2, 'succeeded', NULL,
		         '2026-03-05 06:07:08', '2026-03-05 06:07:09')`,
		`INSERT INTO payment_events
		 (id, order_id, payment_attempt_id, provider, event_kind, external_id, amount_minor,
		  currency, scale, disposition, occurred_at, created_at)
		 VALUES (3, 1, 3, 'yookassa', 'captured', 'seed-att-yookassa', 184908, 'RUB', 2, 'settled',
		         '2026-03-04 05:06:07', '2026-03-04 05:06:08'),
		        (4, 2, 4, 'stripe', 'captured', 'seed-att-stripe', 1999, 'USD', 2, 'settled',
		         '2026-03-05 06:07:08', '2026-03-05 06:07:09')`,
		`INSERT INTO refunds
		 (id, order_id, provider, external_id, payment_external_id, payer_id, amount_minor,
		  currency, scale, status, requested_at, completed_at, created_at)
		 VALUES (2, 2, 'stripe', 'seed-refund-stripe', 'seed-att-stripe', 43, 1999, 'USD', 2,
		         'succeeded', '2026-03-06 07:08:09', '2026-03-06 07:09:10', '2026-03-06 07:08:09')`,
		`INSERT INTO payment_resolutions
		 (id, order_id, provider, target_kind, target_id, decision, actor, reason,
		  resulting_payment_state, resolved_at)
		 VALUES (3, 1, 'yookassa', 'payment_event', 3, 'accepted_refund', 'operator:seed',
		         'seed yookassa refund accepted', 'refunded', '2026-03-06 07:09:10')`,
		`INSERT INTO payment_ingress_audits
		 (id, order_id, provider, event_kind, target_kind, target_id, actor, reason, applied_at)
		 VALUES (2, 2, 'stripe', 'refunded', 'refund', 2, 'operator:seed', 'seed stripe refund ingress',
		         '2026-03-06 07:09:11')`,
	}
	for _, statement := range statements {
		if _, err := db.Conn().Exec(statement); err != nil {
			t.Fatalf("seed crypto-expansion legacy rows: %v\n%s", err, statement)
		}
	}
}

// cryptoExpansionLedgerTables enumerates the six rebuilt tables with their
// declared column order (unchanged by the rebuild — only CHECK literals move).
func cryptoExpansionLedgerTables() []struct {
	name string
	cols []string
} {
	return []struct {
		name string
		cols []string
	}{
		{"payment_attempts", []string{"id", "order_id", "provider", "external_id", "payer_id",
			"amount_minor", "currency", "scale", "status", "entitlement_expires_at",
			"occurred_at", "created_at"}},
		{"payment_events", []string{"id", "order_id", "payment_attempt_id", "provider",
			"event_kind", "external_id", "amount_minor", "currency", "scale", "disposition",
			"occurred_at", "created_at"}},
		{"payment_anomalies", []string{"id", "fingerprint", "proposed_order_id", "provider",
			"event_kind", "external_id", "related_external_id", "payer_id", "amount_minor",
			"currency", "scale", "raw_amount", "raw_payload", "reason", "occurred_at"}},
		{"refunds", []string{"id", "order_id", "provider", "external_id", "payment_external_id",
			"payer_id", "amount_minor", "currency", "scale", "status", "requested_at",
			"completed_at", "created_at"}},
		{"payment_resolutions", []string{"id", "order_id", "provider", "target_kind", "target_id",
			"decision", "actor", "reason", "resulting_payment_state", "resolved_at"}},
		{"payment_ingress_audits", []string{"id", "order_id", "provider", "event_kind",
			"target_kind", "target_id", "actor", "reason", "applied_at"}},
	}
}

// TestMigration021PreservesLegacyLedgerRows rebuilds the six ledger tables on
// a database holding stars/crypto/yookassa/stripe rows and proves the rebuild
// is lossless: identical columns, identical values, all 7 provider indexes,
// all 13 six-table triggers, and no leftover *_new tables.
func TestMigration021PreservesLegacyLedgerRows(t *testing.T) {
	db := preCryptoExpansionRebuildDB(t)
	seedCryptoExpansionLegacyRows(t, db)

	tables := cryptoExpansionLedgerTables()
	before := make(map[string][][]string, len(tables))
	for _, table := range tables {
		cols, rows := ledgerTableDump(t, db, table.name)
		if !reflect.DeepEqual(cols, table.cols) {
			t.Fatalf("%s schema-020 columns %v, want %v", table.name, cols, table.cols)
		}
		before[table.name] = rows
	}

	applyCryptoExpansionRebuild(t, db)

	for _, table := range tables {
		cols, rows := ledgerTableDump(t, db, table.name)
		if !reflect.DeepEqual(cols, table.cols) {
			t.Fatalf("%s columns changed by rebuild: got %v, want %v", table.name, cols, table.cols)
		}
		if !reflect.DeepEqual(rows, before[table.name]) {
			t.Fatalf("%s rows changed by rebuild:\n got  %v\n want %v", table.name, rows, before[table.name])
		}
	}

	indexes := schemaObjectNames(t, db, "index")
	for _, name := range []string{
		"idx_payment_attempts_order", "idx_payment_events_order_time",
		"idx_payment_anomalies_provider_time", "idx_refunds_order",
		"idx_refunds_payment_identity", "idx_payment_resolutions_order",
		"idx_payment_ingress_audits_order",
		// order_events is not rebuilt; its index must survive regardless.
		"idx_order_events_order_time",
	} {
		if !indexes[name] {
			t.Errorf("index %s missing after rebuild: %v", name, indexes)
		}
	}

	triggers := schemaObjectNames(t, db, "trigger")
	for _, name := range []string{
		"payment_attempts_identity_no_update", "payment_attempts_entitlement_once",
		"payment_attempts_no_delete", "refunds_identity_no_update", "refunds_no_delete",
		"payment_events_no_update", "payment_events_no_delete",
		"payment_anomalies_no_update", "payment_anomalies_no_delete",
		"payment_resolutions_no_update", "payment_resolutions_no_delete",
		"payment_ingress_audits_no_update", "payment_ingress_audits_no_delete",
		// order_events is not rebuilt; its triggers must survive regardless.
		"order_events_no_update", "order_events_no_delete",
	} {
		if !triggers[name] {
			t.Errorf("trigger %s missing after rebuild: %v", name, triggers)
		}
	}

	for name := range schemaObjectNames(t, db, "table") {
		if strings.HasSuffix(name, "_new") {
			t.Errorf("temporary table %s left behind by rebuild", name)
		}
	}

	// FK integrity: every parked payment_events row still references an
	// existing payment_attempts row after the parent was dropped and rebuilt.
	var orphans int
	if err := db.Conn().QueryRow(
		`SELECT COUNT(*) FROM payment_events e
		 LEFT JOIN payment_attempts a ON a.id = e.payment_attempt_id
		 WHERE e.payment_attempt_id IS NOT NULL AND a.id IS NULL`).Scan(&orphans); err != nil {
		t.Fatalf("orphan check: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("rebuild orphaned %d payment_events rows", orphans)
	}
}

// TestMigration021AcceptsCryptoExpansionProviders proves the widened CHECKs
// accept ton, nowpayments and the forward-pinned balance provider identities —
// plus the four pre-021 providers — on all six tables (payment_resolutions
// also keeps 'unknown') while still rejecting an unapproved provider with a
// CHECK constraint failure.
func TestMigration021AcceptsCryptoExpansionProviders(t *testing.T) {
	db := preCryptoExpansionRebuildDB(t)
	applyCryptoExpansionRebuild(t, db)
	if _, err := db.Conn().Exec(`INSERT INTO orders (user_id, total_usd, total_stars, status)
		VALUES (42, 1, 100, 'pending')`); err != nil {
		t.Fatal(err)
	}

	providers := []string{"stars", "crypto", "yookassa", "stripe", "ton", "nowpayments", "balance"}
	for i, provider := range providers {
		seq := 1000 + i
		accepted := []string{
			fmt.Sprintf(`INSERT INTO payment_attempts
			 (order_id, provider, external_id, payer_id, amount_minor, currency, scale, status)
			 VALUES (1, '%s', '%s-att-1', 42, 100, 'USD', 2, 'observed')`, provider, provider),
			fmt.Sprintf(`INSERT INTO payment_events
			 (order_id, payment_attempt_id, provider, event_kind, external_id, amount_minor,
			  currency, scale, disposition)
			 VALUES (1, NULL, '%s', 'captured', '%s-evt-1', 100, 'USD', 2, 'observed')`, provider, provider),
			fmt.Sprintf(`INSERT INTO payment_anomalies
			 (fingerprint, proposed_order_id, provider, event_kind, external_id, payer_id,
			  amount_minor, currency, scale, reason)
			 VALUES ('%s-fp-1', 1, '%s', 'captured', '%s-anom-1', 42, 100, 'USD', 2,
			         'test %s anomaly')`, provider, provider, provider, provider),
			fmt.Sprintf(`INSERT INTO refunds
			 (order_id, provider, external_id, payment_external_id, payer_id, amount_minor,
			  currency, scale, status)
			 VALUES (1, '%s', '%s-ref-1', '%s-att-1', 42, 100, 'USD', 2, 'requested')`, provider, provider, provider),
			fmt.Sprintf(`INSERT INTO payment_resolutions
			 (order_id, provider, target_kind, target_id, decision, actor, reason,
			  resulting_payment_state)
			 VALUES (1, '%s', 'payment_anomaly', %d, 'dismissed', 'operator:test',
			         '%s anomaly dismissed', 'needs_review')`, provider, seq, provider),
			fmt.Sprintf(`INSERT INTO payment_ingress_audits
			 (order_id, provider, event_kind, target_kind, target_id, actor, reason)
			 VALUES (1, '%s', 'captured', 'refund', %d, 'operator:test',
			         '%s capture ingressed')`, provider, seq, provider),
		}
		for _, statement := range accepted {
			if _, err := db.Conn().Exec(statement); err != nil {
				t.Fatalf("provider %q insert rejected: %v\n%s", provider, err, statement)
			}
		}
	}

	// payment_resolutions is the one table whose CHECK also admits 'unknown'
	// (the provider-neutral operator inbox), exactly as under 020.
	if _, err := db.Conn().Exec(`INSERT INTO payment_resolutions
		(order_id, provider, target_kind, target_id, decision, actor, reason,
		 resulting_payment_state)
		VALUES (1, 'unknown', 'order', 2000, 'dismissed', 'operator:test',
		        'unknown provider dismissed', 'needs_review')`); err != nil {
		t.Fatalf("unknown resolution insert rejected: %v", err)
	}

	rejected := []string{
		`INSERT INTO payment_attempts
		 (order_id, provider, external_id, payer_id, amount_minor, currency, scale, status)
		 VALUES (1, 'sepa', 'sepa-att-1', 42, 100, 'EUR', 2, 'observed')`,
		`INSERT INTO payment_events
		 (order_id, payment_attempt_id, provider, event_kind, external_id, amount_minor,
		  currency, scale, disposition)
		 VALUES (1, NULL, 'sepa', 'captured', 'sepa-evt-1', 100, 'EUR', 2, 'observed')`,
		`INSERT INTO payment_anomalies
		 (fingerprint, proposed_order_id, provider, event_kind, external_id, payer_id,
		  amount_minor, currency, scale, reason)
		 VALUES ('sepa-fp-1', 1, 'sepa', 'captured', 'sepa-anom-1', 42, 100, 'EUR', 2,
		         'test sepa anomaly')`,
		`INSERT INTO refunds
		 (order_id, provider, external_id, payment_external_id, payer_id, amount_minor,
		  currency, scale, status)
		 VALUES (1, 'sepa', 'sepa-ref-1', 'sepa-att-1', 42, 100, 'EUR', 2, 'requested')`,
		`INSERT INTO payment_resolutions
		 (order_id, provider, target_kind, target_id, decision, actor, reason,
		  resulting_payment_state)
		 VALUES (1, 'sepa', 'payment_anomaly', 3000, 'dismissed', 'operator:test',
		         'sepa anomaly dismissed', 'needs_review')`,
		`INSERT INTO payment_ingress_audits
		 (order_id, provider, event_kind, target_kind, target_id, actor, reason)
		 VALUES (1, 'sepa', 'captured', 'refund', 3000, 'operator:test',
		         'sepa capture ingressed')`,
	}
	for _, statement := range rejected {
		_, err := db.Conn().Exec(statement)
		if err == nil {
			t.Fatalf("sepa insert unexpectedly accepted:\n%s", statement)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "check constraint") {
			t.Fatalf("sepa insert failed for the wrong reason: %v\n%s", err, statement)
		}
	}
}

// TestMigration021ImmutabilityTriggersSurviveRebuild proves the rebuild
// recreated every ledger guard: identity immutability, append-only updates,
// and no-delete protection must all still abort after the tables are dropped
// and renamed.
func TestMigration021ImmutabilityTriggersSurviveRebuild(t *testing.T) {
	db := preCryptoExpansionRebuildDB(t)
	seedLegacyLedgerRows(t, db)
	applyCryptoExpansionRebuild(t, db)

	aborts := []struct {
		note      string
		want      string
		statement string
	}{
		{"payment_attempts amount update", "identity is immutable",
			`UPDATE payment_attempts SET amount_minor = 999 WHERE id = 1`},
		{"payment_attempts identity update", "identity is immutable",
			`UPDATE payment_attempts SET provider = 'crypto', external_id = 'tampered' WHERE id = 1`},
		{"payment_attempts entitlement update", "entitlement expiry is immutable",
			`UPDATE payment_attempts SET entitlement_expires_at = NULL WHERE id = 1`},
		{"refunds identity update", "identity is immutable",
			`UPDATE refunds SET provider = 'crypto', external_id = 'tampered' WHERE id = 1`},
		{"payment_events update", "payment_events are append-only",
			`UPDATE payment_events SET amount_minor = 999 WHERE id = 1`},
		{"payment_anomalies update", "payment_anomalies are append-only",
			`UPDATE payment_anomalies SET amount_minor = 999 WHERE id = 1`},
		{"payment_resolutions update", "payment_resolutions are append-only",
			`UPDATE payment_resolutions SET reason = 'tampered' WHERE id = 1`},
		{"payment_ingress_audits update", "payment_ingress_audits are append-only",
			`UPDATE payment_ingress_audits SET reason = 'tampered' WHERE id = 1`},
		{"payment_attempts delete", "payment_attempts cannot be deleted",
			`DELETE FROM payment_attempts WHERE id = 1`},
		{"refunds delete", "refunds cannot be deleted",
			`DELETE FROM refunds WHERE id = 1`},
		{"payment_events delete", "payment_events are append-only",
			`DELETE FROM payment_events WHERE id = 1`},
		{"payment_anomalies delete", "payment_anomalies are append-only",
			`DELETE FROM payment_anomalies WHERE id = 1`},
		{"payment_resolutions delete", "payment_resolutions are append-only",
			`DELETE FROM payment_resolutions WHERE id = 1`},
		{"payment_ingress_audits delete", "payment_ingress_audits are append-only",
			`DELETE FROM payment_ingress_audits WHERE id = 1`},
	}
	for _, tc := range aborts {
		_, err := db.Conn().Exec(tc.statement)
		if err == nil {
			t.Fatalf("%s: unexpectedly succeeded", tc.note)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q does not contain %q", tc.note, err.Error(), tc.want)
		}
	}
}
