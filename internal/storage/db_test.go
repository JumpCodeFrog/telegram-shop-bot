package storage

import (
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
