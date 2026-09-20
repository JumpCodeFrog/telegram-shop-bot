package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// preOrdersTotalTonNanoDB applies every migration before 022, leaving the
// orders table exactly where production databases stood after migration 021 —
// without the total_ton_nano column.
func preOrdersTotalTonNanoDB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "v21.db")))
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
		if entry.Name() == "022_orders_total_ton_nano.sql" {
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

func applyOrdersTotalTonNano(t *testing.T, db *DB) {
	t.Helper()
	statements, err := migrationsFS.ReadFile("migrations/022_orders_total_ton_nano.sql")
	if err != nil {
		t.Fatalf("read 022_orders_total_ton_nano.sql: %v", err)
	}
	if err := db.applyMigration("022_orders_total_ton_nano.sql", string(statements)); err != nil {
		t.Fatalf("apply 022_orders_total_ton_nano.sql: %v", err)
	}
}

// TestMigration022OrdersTotalTonNano proves the column lands with DEFAULT 0 on
// rows that predate it and that an explicit nanoTON value round-trips.
func TestMigration022OrdersTotalTonNano(t *testing.T) {
	db := preOrdersTotalTonNanoDB(t)

	if _, err := db.Conn().Exec(`INSERT INTO orders (user_id, total_usd, total_stars, status)
		VALUES (42, 5, 100, 'pending')`); err != nil {
		t.Fatalf("insert pre-022 legacy order: %v", err)
	}

	applyOrdersTotalTonNano(t, db)

	var legacy int64
	if err := db.Conn().QueryRow(`SELECT total_ton_nano FROM orders WHERE id = 1`).Scan(&legacy); err != nil {
		t.Fatalf("read total_ton_nano on legacy row: %v", err)
	}
	if legacy != 0 {
		t.Fatalf("legacy row total_ton_nano = %d, want 0", legacy)
	}

	// 6.17 TON == 6_170_000_000 nanoTON.
	if _, err := db.Conn().Exec(`INSERT INTO orders (user_id, total_usd, total_ton_nano, status)
		VALUES (43, 12.34, 6170000000, 'pending')`); err != nil {
		t.Fatalf("insert explicit total_ton_nano: %v", err)
	}
	var explicit int64
	if err := db.Conn().QueryRow(`SELECT total_ton_nano FROM orders WHERE id = 2`).Scan(&explicit); err != nil {
		t.Fatalf("read explicit total_ton_nano: %v", err)
	}
	if explicit != 6170000000 {
		t.Fatalf("explicit row total_ton_nano = %d, want 6170000000", explicit)
	}
}

// TestOrderTotalTonNanoRoundTrip verifies that a TON order's nanoTON total
// survives every read path: GetOrder, the order list queries, the
// payment-recording loader, and the settlement transition that feeds the
// immutable ledger.
// Feature: shop_bot, Property 2: Round-trip хранилища данных
func TestOrderTotalTonNanoRoundTrip(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	defer db.Close()

	store := NewSQLOrderStore(db)
	ctx := context.Background()

	if _, err := db.Conn().ExecContext(ctx, "INSERT INTO categories (name, emoji) VALUES (?, ?)", "Cat", "🧪"); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if _, err := db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Gadget', 23.00, 0, 10, 1)`); err != nil {
		t.Fatalf("seed product: %v", err)
	}

	tonOrder := &Order{
		UserID:        42,
		TotalUSD:      23.00,
		TotalTonNano:  6170000000,
		PaymentMethod: PaymentMethodTON,
		Status:        OrderStatusPending,
	}
	items := []OrderItem{{ProductID: 1, Quantity: 1, PriceUSD: 23.00}}
	tonOrderID, err := store.CreateOrder(ctx, tonOrder, items)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	got, err := store.GetOrder(ctx, tonOrderID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.TotalTonNano != 6170000000 {
		t.Errorf("GetOrder TotalTonNano: got %d, want 6170000000", got.TotalTonNano)
	}

	userOrders, err := store.GetUserOrders(ctx, 42)
	if err != nil || len(userOrders) != 1 {
		t.Fatalf("GetUserOrders: orders=%d err=%v", len(userOrders), err)
	}
	if userOrders[0].TotalTonNano != 6170000000 {
		t.Errorf("GetUserOrders TotalTonNano: got %d, want 6170000000", userOrders[0].TotalTonNano)
	}

	for _, filter := range []string{"", OrderStatusPending} {
		allOrders, err := store.GetAllOrders(ctx, filter)
		if err != nil || len(allOrders) != 1 {
			t.Fatalf("GetAllOrders(%q): orders=%d err=%v", filter, len(allOrders), err)
		}
		if allOrders[0].TotalTonNano != 6170000000 {
			t.Errorf("GetAllOrders(%q) TotalTonNano: got %d, want 6170000000", filter, allOrders[0].TotalTonNano)
		}
	}

	paymentOrder, err := store.loadPaymentOrder(ctx, tonOrderID)
	if err != nil {
		t.Fatalf("loadPaymentOrder: %v", err)
	}
	if paymentOrder.TotalTonNano != 6170000000 {
		t.Errorf("loadPaymentOrder TotalTonNano: got %d, want 6170000000", paymentOrder.TotalTonNano)
	}

	// A conflicting transition exercises the status-transition order loader
	// (the SELECT whose result feeds observePayment/orderMoney) on a TON
	// order without writing to the provider-gated ledger tables.
	if err := store.UpdateOrderStatus(ctx, tonOrderID, OrderStatusPaid, OrderStatusDelivered, "", ""); !errors.Is(err, ErrOrderStatusConflict) {
		t.Fatalf("UpdateOrderStatus conflict path: err=%v, want ErrOrderStatusConflict", err)
	}

	// An order created without a TON total reads back as zero.
	starOrder := &Order{
		UserID:     43,
		TotalUSD:   5.0,
		TotalStars: 100,
		Status:     OrderStatusPending,
	}
	starOrderID, err := store.CreateOrder(ctx, starOrder, items)
	if err != nil {
		t.Fatalf("CreateOrder stars: %v", err)
	}
	starGot, err := store.GetOrder(ctx, starOrderID)
	if err != nil {
		t.Fatalf("GetOrder stars: %v", err)
	}
	if starGot.TotalTonNano != 0 {
		t.Errorf("stars order TotalTonNano: got %d, want 0", starGot.TotalTonNano)
	}
}
