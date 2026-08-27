package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNew_CreatesAllTables(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:) error: %v", err)
	}
	defer db.Close()

	expected := []string{"categories", "products", "cart_items", "orders", "order_items"}
	for _, table := range expected {
		var name string
		err := db.Conn().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found after migration: %v", table, err)
		}
	}
}

func TestNew_ForeignKeysEnabled(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:) error: %v", err)
	}
	defer db.Close()

	var fk int
	if err := db.Conn().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys query error: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestWALEnabled(t *testing.T) {
	// WAL requires a real file: in-memory databases always report "memory".
	db, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New(temp file) error: %v", err)
	}
	defer db.Close()

	var mode string
	if err := db.Conn().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode query error: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestNew_CartItemsUniqueConstraint(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:) error: %v", err)
	}
	defer db.Close()

	// Insert a category and product first (foreign key targets).
	_, err = db.Conn().Exec("INSERT INTO categories (name) VALUES ('test')")
	if err != nil {
		t.Fatalf("insert category: %v", err)
	}
	_, err = db.Conn().Exec(
		"INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active) VALUES (1, 'p', 1.0, 10, 5, 1)",
	)
	if err != nil {
		t.Fatalf("insert product: %v", err)
	}

	// First insert should succeed.
	_, err = db.Conn().Exec("INSERT INTO cart_items (user_id, product_id) VALUES (100, 1)")
	if err != nil {
		t.Fatalf("first cart_items insert: %v", err)
	}

	// Duplicate (user_id, product_id) should fail.
	_, err = db.Conn().Exec("INSERT INTO cart_items (user_id, product_id) VALUES (100, 1)")
	if err == nil {
		t.Error("expected UNIQUE constraint error for duplicate (user_id, product_id), got nil")
	}
}

func TestClose(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:) error: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}

	// After close, queries should fail.
	if err := db.Conn().Ping(); err == nil {
		t.Error("expected error after Close(), got nil")
	}
}

func TestApplyMigrationIsAtomic(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "atomic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.applyMigration("999_broken.sql", `
		CREATE TABLE must_rollback (id INTEGER PRIMARY KEY);
		INSERT INTO table_that_does_not_exist VALUES (1);
	`)
	if err == nil {
		t.Fatal("broken migration unexpectedly succeeded")
	}
	var count int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='must_rollback'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("half-migrated table exists: count=%d", count)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version='999_broken.sql'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed migration was recorded: count=%d", count)
	}
}

func TestOpenReadOnlyRejectsWritesAndDoesNotMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly.db")
	db, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if _, err := readOnly.Conn().Exec(`INSERT INTO categories (name) VALUES ('mutated')`); err == nil {
		t.Fatal("read-only connection accepted a write")
	}
}

func TestOpenReadOnlyDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing database #1.db")
	if _, err := OpenReadOnly(path); err == nil {
		t.Fatal("OpenReadOnly unexpectedly opened a missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing database was created: %v", err)
	}
}
