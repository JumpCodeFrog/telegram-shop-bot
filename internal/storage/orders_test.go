package storage

import (
	"context"
	"math"
	"testing"

	"pgregory.net/rapid"
)

// TestGetOrder_NotFound verifies that GetOrder returns ErrNotFound for a
// non-existent order ID.
// Validates: Requirements 10.5
func TestGetOrder_NotFound(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	defer db.Close()

	store := NewSQLOrderStore(db)
	_, err = store.GetOrder(context.Background(), 999)
	if err != ErrNotFound {
		t.Fatalf("got err %v, want ErrNotFound", err)
	}
}

// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
func TestOrderRoundTrip_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db, err := New(":memory:")
		if err != nil {
			t.Fatalf("New(:memory:): %v", err)
		}
		defer db.Close()
		store := NewSQLOrderStore(db)
		ctx := context.Background()

		// Seed a category and products for foreign keys in order_items.
		_, err = db.Conn().ExecContext(ctx,
			"INSERT INTO categories (name, emoji) VALUES (?, ?)", "TestCat", "🧪")
		if err != nil {
			t.Fatalf("seed category: %v", err)
		}

		numItems := rapid.IntRange(1, 5).Draw(t, "numItems")
		productIDs := make([]int64, numItems)
		for i := 0; i < numItems; i++ {
			res, err := db.Conn().ExecContext(ctx,
				`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
				 VALUES (1, ?, ?, ?, 10, 1)`,
				rapid.StringMatching(`[A-Za-z0-9]{1,20}`).Draw(t, "prodName"),
				rapid.Float64Range(0.01, 9999.99).Draw(t, "prodPrice"),
				rapid.IntRange(1, 99999).Draw(t, "prodStars"))
			if err != nil {
				t.Fatalf("seed product: %v", err)
			}
			pid, _ := res.LastInsertId()
			productIDs[i] = pid
		}

		// Generate random order data.
		userID := rapid.Int64Range(1, 999999).Draw(t, "userID")
		totalUSD := rapid.Float64Range(0.01, 99999.99).Draw(t, "totalUSD")
		totalStars := rapid.IntRange(1, 999999).Draw(t, "totalStars")
		status := rapid.SampledFrom([]string{OrderStatusPending, OrderStatusPaid, OrderStatusDelivered}).Draw(t, "status")

		order := &Order{
			UserID:     userID,
			TotalUSD:   totalUSD,
			TotalStars: totalStars,
			Status:     status,
		}

		// Generate order items.
		items := make([]OrderItem, numItems)
		for i := 0; i < numItems; i++ {
			items[i] = OrderItem{
				ProductID: productIDs[i],
				Quantity:  rapid.IntRange(1, 100).Draw(t, "quantity"),
				PriceUSD:  rapid.Float64Range(0.01, 9999.99).Draw(t, "itemPrice"),
			}
		}

		orderID, err := store.CreateOrder(ctx, order, items)
		if err != nil {
			t.Fatalf("CreateOrder: %v", err)
		}

		got, err := store.GetOrder(ctx, orderID)
		if err != nil {
			t.Fatalf("GetOrder: %v", err)
		}

		// Verify order fields.
		if got.ID != orderID {
			t.Errorf("ID: got %d, want %d", got.ID, orderID)
		}
		if got.UserID != userID {
			t.Errorf("UserID: got %d, want %d", got.UserID, userID)
		}
		if math.Abs(got.TotalUSD-totalUSD) > 1e-9 {
			t.Errorf("TotalUSD: got %f, want %f", got.TotalUSD, totalUSD)
		}
		if got.TotalStars != totalStars {
			t.Errorf("TotalStars: got %d, want %d", got.TotalStars, totalStars)
		}
		if got.Status != status {
			t.Errorf("Status: got %q, want %q", got.Status, status)
		}
		if got.CreatedAt.IsZero() {
			t.Error("CreatedAt should not be zero")
		}

		// Verify order items.
		if len(got.Items) != numItems {
			t.Fatalf("Items count: got %d, want %d", len(got.Items), numItems)
		}
		for i, gotItem := range got.Items {
			wantItem := items[i]
			if gotItem.OrderID != orderID {
				t.Errorf("Item[%d].OrderID: got %d, want %d", i, gotItem.OrderID, orderID)
			}
			if gotItem.ProductID != wantItem.ProductID {
				t.Errorf("Item[%d].ProductID: got %d, want %d", i, gotItem.ProductID, wantItem.ProductID)
			}
			if gotItem.Quantity != wantItem.Quantity {
				t.Errorf("Item[%d].Quantity: got %d, want %d", i, gotItem.Quantity, wantItem.Quantity)
			}
			if math.Abs(gotItem.PriceUSD-wantItem.PriceUSD) > 1e-9 {
				t.Errorf("Item[%d].PriceUSD: got %f, want %f", i, gotItem.PriceUSD, wantItem.PriceUSD)
			}
		}
	})
}

// Feature: shop_bot, Property 12: Заказы пользователя отсортированы и принадлежат ему
// Validates: Requirements 8.1, 8.2
func TestUserOrdersSortedAndOwned_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db, err := New(":memory:")
		if err != nil {
			t.Fatalf("New(:memory:): %v", err)
		}
		defer db.Close()
		store := NewSQLOrderStore(db)
		ctx := context.Background()

		// Seed category and product for FK constraints.
		_, err = db.Conn().ExecContext(ctx,
			"INSERT INTO categories (name, emoji) VALUES (?, ?)", "Cat", "🧪")
		if err != nil {
			t.Fatalf("seed category: %v", err)
		}
		_, err = db.Conn().ExecContext(ctx,
			`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
			 VALUES (1, 'Prod', 1.00, 10, 10, 1)`)
		if err != nil {
			t.Fatalf("seed product: %v", err)
		}

		// Two distinct users.
		userA := rapid.Int64Range(1, 499999).Draw(t, "userA")
		userB := rapid.Int64Range(500000, 999999).Draw(t, "userB")

		// Create random number of orders for each user.
		numA := rapid.IntRange(1, 5).Draw(t, "numOrdersA")
		numB := rapid.IntRange(1, 5).Draw(t, "numOrdersB")

		for i := 0; i < numA; i++ {
			o := &Order{
				UserID:     userA,
				TotalUSD:   1.0,
				TotalStars: 10,
				Status:     OrderStatusPending,
			}
			items := []OrderItem{{ProductID: 1, Quantity: 1, PriceUSD: 1.0}}
			if _, err := store.CreateOrder(ctx, o, items); err != nil {
				t.Fatalf("CreateOrder userA: %v", err)
			}
		}
		for i := 0; i < numB; i++ {
			o := &Order{
				UserID:     userB,
				TotalUSD:   2.0,
				TotalStars: 20,
				Status:     OrderStatusPaid,
			}
			items := []OrderItem{{ProductID: 1, Quantity: 1, PriceUSD: 2.0}}
			if _, err := store.CreateOrder(ctx, o, items); err != nil {
				t.Fatalf("CreateOrder userB: %v", err)
			}
		}

		// Verify GetUserOrders for userA.
		ordersA, err := store.GetUserOrders(ctx, userA)
		if err != nil {
			t.Fatalf("GetUserOrders(userA): %v", err)
		}
		if len(ordersA) != numA {
			t.Fatalf("userA orders count: got %d, want %d", len(ordersA), numA)
		}
		for i, o := range ordersA {
			if o.UserID != userA {
				t.Errorf("ordersA[%d].UserID: got %d, want %d", i, o.UserID, userA)
			}
		}
		// Verify sorted by created_at DESC (since IDs are auto-increment and
		// correlate with insertion order, descending IDs imply descending time).
		for i := 1; i < len(ordersA); i++ {
			if ordersA[i].CreatedAt.After(ordersA[i-1].CreatedAt) {
				t.Errorf("ordersA not sorted DESC: [%d].CreatedAt=%v > [%d].CreatedAt=%v",
					i, ordersA[i].CreatedAt, i-1, ordersA[i-1].CreatedAt)
			}
		}

		// Verify GetUserOrders for userB returns none of userA's orders.
		ordersB, err := store.GetUserOrders(ctx, userB)
		if err != nil {
			t.Fatalf("GetUserOrders(userB): %v", err)
		}
		if len(ordersB) != numB {
			t.Fatalf("userB orders count: got %d, want %d", len(ordersB), numB)
		}
		for i, o := range ordersB {
			if o.UserID != userB {
				t.Errorf("ordersB[%d].UserID: got %d, want %d", i, o.UserID, userB)
			}
		}
	})
}

// Feature: shop_bot, Property 13: Фильтрация заказов по статусу
// Validates: Requirements 10.1, 10.2
func TestOrderStatusFilter_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db, err := New(":memory:")
		if err != nil {
			t.Fatalf("New(:memory:): %v", err)
		}
		defer db.Close()
		store := NewSQLOrderStore(db)
		ctx := context.Background()

		// Seed category and product.
		_, err = db.Conn().ExecContext(ctx,
			"INSERT INTO categories (name, emoji) VALUES (?, ?)", "Cat", "🧪")
		if err != nil {
			t.Fatalf("seed category: %v", err)
		}
		_, err = db.Conn().ExecContext(ctx,
			`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
			 VALUES (1, 'Prod', 1.00, 10, 10, 1)`)
		if err != nil {
			t.Fatalf("seed product: %v", err)
		}

		statuses := []string{OrderStatusPending, OrderStatusPaid, OrderStatusDelivered}
		counts := make(map[string]int)
		totalOrders := 0

		// Create random number of orders per status.
		for _, st := range statuses {
			n := rapid.IntRange(0, 4).Draw(t, "num_"+st)
			counts[st] = n
			totalOrders += n
			for i := 0; i < n; i++ {
				o := &Order{
					UserID:     int64(rapid.IntRange(1, 999999).Draw(t, "uid")),
					TotalUSD:   1.0,
					TotalStars: 10,
					Status:     st,
				}
				items := []OrderItem{{ProductID: 1, Quantity: 1, PriceUSD: 1.0}}
				if _, err := store.CreateOrder(ctx, o, items); err != nil {
					t.Fatalf("CreateOrder(%s): %v", st, err)
				}
			}
		}

		// Pick a random status to filter by.
		filterStatus := rapid.SampledFrom(statuses).Draw(t, "filterStatus")
		filtered, err := store.GetAllOrders(ctx, filterStatus)
		if err != nil {
			t.Fatalf("GetAllOrders(%q): %v", filterStatus, err)
		}
		if len(filtered) != counts[filterStatus] {
			t.Errorf("filtered count for %q: got %d, want %d", filterStatus, len(filtered), counts[filterStatus])
		}
		for i, o := range filtered {
			if o.Status != filterStatus {
				t.Errorf("filtered[%d].Status: got %q, want %q", i, o.Status, filterStatus)
			}
		}

		// Empty filter returns all orders.
		all, err := store.GetAllOrders(ctx, "")
		if err != nil {
			t.Fatalf("GetAllOrders(\"\"): %v", err)
		}
		if len(all) != totalOrders {
			t.Errorf("all orders count: got %d, want %d", len(all), totalOrders)
		}
	})
}

// Feature: shop_bot, Property 14: Обновление статуса на delivered
// Validates: Requirements 10.3
func TestSetDelivered_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db, err := New(":memory:")
		if err != nil {
			t.Fatalf("New(:memory:): %v", err)
		}
		defer db.Close()
		store := NewSQLOrderStore(db)
		ctx := context.Background()

		// Seed category and product.
		_, err = db.Conn().ExecContext(ctx,
			"INSERT INTO categories (name, emoji) VALUES (?, ?)", "Cat", "🧪")
		if err != nil {
			t.Fatalf("seed category: %v", err)
		}
		_, err = db.Conn().ExecContext(ctx,
			`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
			 VALUES (1, 'Prod', 1.00, 10, 10, 1)`)
		if err != nil {
			t.Fatalf("seed product: %v", err)
		}

		// Create an order with status "paid".
		userID := rapid.Int64Range(1, 999999).Draw(t, "userID")
		o := &Order{
			UserID:     userID,
			TotalUSD:   rapid.Float64Range(0.01, 9999.99).Draw(t, "totalUSD"),
			TotalStars: rapid.IntRange(1, 99999).Draw(t, "totalStars"),
			Status:     OrderStatusPaid,
		}
		items := []OrderItem{{ProductID: 1, Quantity: rapid.IntRange(1, 10).Draw(t, "qty"), PriceUSD: 1.0}}
		orderID, err := store.CreateOrder(ctx, o, items)
		if err != nil {
			t.Fatalf("CreateOrder: %v", err)
		}

		// Verify initial status is "paid".
		got, err := store.GetOrder(ctx, orderID)
		if err != nil {
			t.Fatalf("GetOrder before update: %v", err)
		}
		if got.Status != OrderStatusPaid {
			t.Fatalf("initial status: got %q, want %q", got.Status, OrderStatusPaid)
		}

		// Update status to "delivered" via UpdateOrderStatus.
		err = store.UpdateOrderStatus(ctx, orderID, OrderStatusPaid, OrderStatusDelivered, got.PaymentMethod, got.PaymentID)
		if err != nil {
			t.Fatalf("UpdateOrderStatus: %v", err)
		}

		// Verify status is now "delivered".
		updated, err := store.GetOrder(ctx, orderID)
		if err != nil {
			t.Fatalf("GetOrder after update: %v", err)
		}
		if updated.Status != OrderStatusDelivered {
			t.Errorf("updated status: got %q, want %q", updated.Status, OrderStatusDelivered)
		}
		// Verify other fields are preserved.
		if updated.UserID != userID {
			t.Errorf("UserID changed: got %d, want %d", updated.UserID, userID)
		}
		if math.Abs(updated.TotalUSD-o.TotalUSD) > 1e-9 {
			t.Errorf("TotalUSD changed: got %f, want %f", updated.TotalUSD, o.TotalUSD)
		}
		if updated.TotalStars != o.TotalStars {
			t.Errorf("TotalStars changed: got %d, want %d", updated.TotalStars, o.TotalStars)
		}
	})
}

// TestUpdateOrderStatus_Idempotency verifies that attempting to confirm a
// payment on an already-paid order returns ErrOrderStatusConflict.
func TestUpdateOrderStatus_Idempotency(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	defer db.Close()

	store := NewSQLOrderStore(db)
	ctx := context.Background()

	_, err = db.Conn().ExecContext(ctx, "INSERT INTO categories (name, emoji) VALUES (?, ?)", "Cat", "🧪")
	if err != nil {
		t.Fatalf("seed category: %v", err)
	}

	order := &Order{UserID: 1, TotalUSD: 10, TotalStars: 100, Status: OrderStatusPaid}
	orderID, err := store.CreateOrder(ctx, order, nil)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	// Attempting pending→paid on an already-paid order must return ErrOrderStatusConflict.
	err = store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "charge_1")
	if err != ErrOrderStatusConflict {
		t.Fatalf("expected ErrOrderStatusConflict, got %v", err)
	}

	// The order must remain unchanged.
	got, err := store.GetOrder(ctx, orderID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != OrderStatusPaid {
		t.Errorf("status changed unexpectedly: got %q", got.Status)
	}
}

// TestUpdateOrderStatus_WrongFromStatus verifies that a transition with a
// mismatched fromStatus returns ErrOrderStatusConflict.
func TestUpdateOrderStatus_WrongFromStatus(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	defer db.Close()

	store := NewSQLOrderStore(db)
	ctx := context.Background()

	_, err = db.Conn().ExecContext(ctx, "INSERT INTO categories (name, emoji) VALUES (?, ?)", "Cat", "🧪")
	if err != nil {
		t.Fatalf("seed category: %v", err)
	}

	order := &Order{UserID: 1, TotalUSD: 10, TotalStars: 100, Status: OrderStatusPending}
	orderID, err := store.CreateOrder(ctx, order, nil)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	// Trying to mark a pending order as delivered (skipping paid) must fail.
	err = store.UpdateOrderStatus(ctx, orderID, OrderStatusPaid, OrderStatusDelivered, "", "")
	if err != ErrOrderStatusConflict {
		t.Fatalf("expected ErrOrderStatusConflict, got %v", err)
	}
}

func TestUpdateOrderStatus_RedeemsPromoOnPaidTransition(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	defer db.Close()

	store := NewSQLOrderStore(db)
	ctx := context.Background()

	_, err = db.Conn().ExecContext(ctx, "INSERT INTO categories (name, emoji) VALUES (?, ?)", "Cat", "🧪")
	if err != nil {
		t.Fatalf("seed category: %v", err)
	}
	_, err = db.Conn().ExecContext(ctx,
		`INSERT INTO products (category_id, name, price_usd, price_stars, stock, is_active)
		 VALUES (1, 'Prod', 10.00, 100, 10, 1)`)
	if err != nil {
		t.Fatalf("seed product: %v", err)
	}
	res, err := db.Conn().ExecContext(ctx,
		`INSERT INTO promo_codes (code, discount, max_uses, is_active) VALUES ('WELCOME10', 10, 1, 1)`)
	if err != nil {
		t.Fatalf("seed promo: %v", err)
	}
	promoID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("promo last insert id: %v", err)
	}

	order := &Order{
		UserID:      42,
		TotalUSD:    9.0,
		TotalStars:  90,
		Status:      OrderStatusPending,
		DiscountPct: 10,
		PromoCode:   "WELCOME10",
	}
	items := []OrderItem{{ProductID: 1, Quantity: 1, PriceUSD: 10.0}}
	orderID, err := store.CreateOrder(ctx, order, items)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	err = store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "charge_1")
	if err != nil {
		t.Fatalf("UpdateOrderStatus: %v", err)
	}

	var usedCount int
	err = db.Conn().QueryRowContext(ctx,
		`SELECT used_count FROM promo_codes WHERE id = ?`, promoID,
	).Scan(&usedCount)
	if err != nil {
		t.Fatalf("query promo used_count: %v", err)
	}
	if usedCount != 1 {
		t.Fatalf("expected used_count 1, got %d", usedCount)
	}

	var usageRows int
	err = db.Conn().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM promo_usages WHERE promo_id = ? AND user_id = ? AND order_id = ?`,
		promoID, 42, orderID,
	).Scan(&usageRows)
	if err != nil {
		t.Fatalf("query promo usage: %v", err)
	}
	if usageRows != 1 {
		t.Fatalf("expected one promo usage row, got %d", usageRows)
	}
}
