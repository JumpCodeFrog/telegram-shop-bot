package storage

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// migrationDBBefore opens a scratch database and applies every migration
// before stopFile, leaving the schema exactly where production databases
// stood before stopFile shipped.
func migrationDBBefore(t *testing.T, stopFile string) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "pre-migration.db")))
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
		if entry.Name() == stopFile {
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

// applyMigrationsFrom applies startFile and every later migration, so legacy
// data seeded on a pre-startFile schema is migrated to the current schema,
// exactly like the production migrator would.
func applyMigrationsFrom(t *testing.T, db *DB, startFile string) {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	for _, entry := range entries {
		if !reached {
			if entry.Name() != startFile {
				continue
			}
			reached = true
		}
		statements, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := db.applyMigration(entry.Name(), string(statements)); err != nil {
			t.Fatal(err)
		}
	}
}

// applyMigrationFile applies exactly one named migration — the shape the
// rebuild migrations (020/021/022) are tested in isolation with.
func applyMigrationFile(t *testing.T, db *DB, name string) {
	t.Helper()
	statements, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := db.applyMigration(name, string(statements)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

// ledgerTableSpec is one rebuilt ledger table with its declared column order
// (unchanged by the rebuilds — only CHECK literals move).
type ledgerTableSpec struct {
	name string
	cols []string
}

// ledgerRebuildTables enumerates the six ledger tables the 020/021 rebuild
// migrations drop and recreate.
func ledgerRebuildTables() []ledgerTableSpec {
	return []ledgerTableSpec{
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

// ledgerTableDump returns the table's declared column order and every row
// rendered as text (NULL as "<NULL>"), so a rebuild can be diffed column-by-
// column and value-by-value.
func ledgerTableDump(t *testing.T, db *DB, table string) (cols []string, rows [][]string) {
	t.Helper()
	rows_, err := db.Conn().Query(`SELECT * FROM ` + table + ` ORDER BY id`)
	if err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	defer rows_.Close()
	cols, err = rows_.Columns()
	if err != nil {
		t.Fatalf("dump %s columns: %v", table, err)
	}
	for rows_.Next() {
		values := make([]sql.NullString, len(cols))
		scan := make([]any, len(cols))
		for i := range values {
			scan[i] = &values[i]
		}
		if err := rows_.Scan(scan...); err != nil {
			t.Fatalf("dump %s scan: %v", table, err)
		}
		row := make([]string, len(cols))
		for i, value := range values {
			if value.Valid {
				row[i] = value.String
			} else {
				row[i] = "<NULL>"
			}
		}
		rows = append(rows, row)
	}
	if err := rows_.Err(); err != nil {
		t.Fatalf("dump %s rows: %v", table, err)
	}
	return cols, rows
}

func schemaObjectNames(t *testing.T, db *DB, objectType string) map[string]bool {
	t.Helper()
	rows, err := db.Conn().Query(`SELECT name FROM sqlite_master WHERE type = ?`, objectType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

// assertLedgerRebuildInventory pins the post-rebuild schema inventory: all 7
// provider indexes, all 13 six-table triggers (plus the untouched
// order_events guards), and no leftover *_new tables.
func assertLedgerRebuildInventory(t *testing.T, db *DB) {
	t.Helper()
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
}

// ledgerAbortCase is one statement that a rebuilt ledger table's
// immutability trigger must reject, with the expected error substring.
type ledgerAbortCase struct {
	note      string
	want      string
	statement string
}

// ledgerImmutabilityAborts is the shared probe set every ledger rebuild
// (020/021) must keep rejecting: identity immutability, append-only updates,
// and no-delete protection on all six tables.
func ledgerImmutabilityAborts() []ledgerAbortCase {
	return []ledgerAbortCase{
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
}
