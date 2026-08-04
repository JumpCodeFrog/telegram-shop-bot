package storage

import (
	"context"
	"math"
	"testing"
)

func newAnalyticsTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedOrder(t *testing.T, db *DB, userID int64, totalUSD float64, status, promoCode string, discountPct int) {
	t.Helper()
	_, err := db.Conn().ExecContext(context.Background(),
		`INSERT INTO orders (user_id, total_usd, total_stars, status, discount_pct, promo_code)
		 VALUES (?, ?, 0, ?, ?, ?)`,
		userID, totalUSD, status, discountPct, promoCode)
	if err != nil {
		t.Fatalf("seed order: %v", err)
	}
}

func TestTopBuyers(t *testing.T) {
	db := newAnalyticsTestDB(t)
	store := NewSQLAnalyticsStore(db)
	ctx := context.Background()

	// User 1: two paid orders, $30 total. User 2: one paid order, $100.
	// User 3: one paid order, $5. User 4: pending only — must not appear.
	seedOrder(t, db, 1, 10, OrderStatusPaid, "", 0)
	seedOrder(t, db, 1, 20, OrderStatusPaid, "", 0)
	seedOrder(t, db, 2, 100, OrderStatusPaid, "", 0)
	seedOrder(t, db, 3, 5, OrderStatusPaid, "", 0)
	seedOrder(t, db, 4, 500, OrderStatusPending, "", 0)

	buyers, err := store.TopBuyers(ctx, 10)
	if err != nil {
		t.Fatalf("TopBuyers: %v", err)
	}
	if len(buyers) != 3 {
		t.Fatalf("expected 3 buyers, got %d: %+v", len(buyers), buyers)
	}

	want := []TopBuyer{
		{UserID: 2, Orders: 1, TotalUSD: 100},
		{UserID: 1, Orders: 2, TotalUSD: 30},
		{UserID: 3, Orders: 1, TotalUSD: 5},
	}
	for i, w := range want {
		if buyers[i] != w {
			t.Errorf("buyer[%d]: expected %+v, got %+v", i, w, buyers[i])
		}
	}

	// Limit truncates after ranking.
	top1, err := store.TopBuyers(ctx, 1)
	if err != nil {
		t.Fatalf("TopBuyers(1): %v", err)
	}
	if len(top1) != 1 || top1[0].UserID != 2 {
		t.Fatalf("expected only user 2, got %+v", top1)
	}
}

func TestTopBuyers_Empty(t *testing.T) {
	db := newAnalyticsTestDB(t)
	store := NewSQLAnalyticsStore(db)

	buyers, err := store.TopBuyers(context.Background(), 10)
	if err != nil {
		t.Fatalf("TopBuyers: %v", err)
	}
	if len(buyers) != 0 {
		t.Fatalf("expected no buyers, got %+v", buyers)
	}
}

func seedPromo(t *testing.T, db *DB, code string, discount, usedCount int) {
	t.Helper()
	_, err := db.Conn().ExecContext(context.Background(),
		`INSERT INTO promo_codes (code, discount, used_count, is_active) VALUES (?, ?, ?, 1)`,
		code, discount, usedCount)
	if err != nil {
		t.Fatalf("seed promo: %v", err)
	}
}

func TestPromoUsage(t *testing.T) {
	db := newAnalyticsTestDB(t)
	store := NewSQLAnalyticsStore(db)
	ctx := context.Background()

	seedPromo(t, db, "SALE10", 10, 2)
	seedPromo(t, db, "FREE100", 100, 1)
	seedPromo(t, db, "UNUSED", 25, 0)

	// SALE10: two paid orders of $90 each (post-discount) → the discount that
	// was applied is 90 * 10 / (100-10) = $10 per order, $20 total.
	seedOrder(t, db, 1, 90, OrderStatusPaid, "SALE10", 10)
	seedOrder(t, db, 2, 90, OrderStatusPaid, "SALE10", 10)
	// A pending order must not contribute to the discount sum.
	seedOrder(t, db, 3, 90, OrderStatusPending, "SALE10", 10)
	// FREE100: 100% discount cannot be reconstructed from a $0 total.
	seedOrder(t, db, 4, 0, OrderStatusPaid, "FREE100", 100)

	stats, err := store.PromoUsage(ctx)
	if err != nil {
		t.Fatalf("PromoUsage: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("expected 3 promo rows, got %d: %+v", len(stats), stats)
	}

	byCode := make(map[string]PromoUsageStat, len(stats))
	for _, st := range stats {
		byCode[st.Code] = st
	}

	sale := byCode["SALE10"]
	if sale.Uses != 2 || !sale.DiscountKnown || math.Abs(sale.DiscountUSD-20) > 1e-9 {
		t.Errorf("SALE10: expected uses=2 discount=$20 known, got %+v", sale)
	}

	free := byCode["FREE100"]
	if free.Uses != 1 || free.DiscountKnown {
		t.Errorf("FREE100: expected uses=1 with unknowable discount, got %+v", free)
	}

	unused := byCode["UNUSED"]
	if unused.Uses != 0 || !unused.DiscountKnown || unused.DiscountUSD != 0 {
		t.Errorf("UNUSED: expected zero uses and $0 known discount, got %+v", unused)
	}

	// Ordering: most-used promo first.
	if stats[0].Code != "SALE10" {
		t.Errorf("expected SALE10 first by used_count, got %+v", stats)
	}
}

func TestCountActiveCarts(t *testing.T) {
	db := newAnalyticsTestDB(t)
	cartStore := NewSQLCartStore(db)
	ctx := context.Background()

	n, err := cartStore.CountActiveCarts(ctx)
	if err != nil {
		t.Fatalf("CountActiveCarts: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 active carts, got %d", n)
	}

	// Seed a category and two products for FK constraints.
	res, err := db.Conn().ExecContext(ctx,
		"INSERT INTO categories (name, emoji) VALUES (?, ?)", "Cat", "🧪")
	if err != nil {
		t.Fatalf("seed category: %v", err)
	}
	catID, _ := res.LastInsertId()
	var productIDs []int64
	for range 2 {
		res, err = db.Conn().ExecContext(ctx,
			`INSERT INTO products (category_id, name, description, photo_url, price_usd, price_stars, stock, is_active)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			catID, "P", "d", "u", 1.0, 10, 10, true)
		if err != nil {
			t.Fatalf("seed product: %v", err)
		}
		id, _ := res.LastInsertId()
		productIDs = append(productIDs, id)
	}

	// User 7 holds two items, user 8 one: two distinct carts.
	if err := cartStore.AddItem(ctx, 7, productIDs[0]); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if err := cartStore.AddItem(ctx, 7, productIDs[1]); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if err := cartStore.AddItem(ctx, 8, productIDs[0]); err != nil {
		t.Fatalf("AddItem: %v", err)
	}

	n, err = cartStore.CountActiveCarts(ctx)
	if err != nil {
		t.Fatalf("CountActiveCarts: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 active carts, got %d", n)
	}
}
