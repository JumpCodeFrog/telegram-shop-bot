package storage

import (
	"context"
	"testing"

	"pgregory.net/rapid"
)

// Feature: shop_bot, Property 5: Добавление товара в корзину увеличивает количество
// Validates: Requirements 4.1, 4.2, 12.3

func TestCartAddItemIncrementsQuantity_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db, err := New(":memory:")
		if err != nil {
			t.Fatalf("New(:memory:): %v", err)
		}
		defer db.Close()

		ctx := context.Background()
		cartStore := NewSQLCartStore(db)

		// Seed a category and product for FK constraints.
		res, err := db.Conn().ExecContext(ctx,
			"INSERT INTO categories (name, emoji) VALUES (?, ?)", "TestCat", "🧪")
		if err != nil {
			t.Fatalf("seed category: %v", err)
		}
		catID, _ := res.LastInsertId()

		res, err = db.Conn().ExecContext(ctx,
			`INSERT INTO products (category_id, name, description, photo_url, price_usd, price_stars, stock, is_active)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			catID, "TestProduct", "desc", "photo1", 9.99, 100, 10, true)
		if err != nil {
			t.Fatalf("seed product: %v", err)
		}
		productID, _ := res.LastInsertId()

		// Generate a random userID and number of AddItem calls.
		userID := rapid.Int64Range(1, 1_000_000).Draw(t, "userID")
		addCount := rapid.IntRange(1, 10).Draw(t, "addCount")

		// After each AddItem call, verify quantity equals previous + 1.
		for i := 1; i <= addCount; i++ {
			if err := cartStore.AddItem(ctx, userID, productID); err != nil {
				t.Fatalf("AddItem call %d: %v", i, err)
			}

			items, err := cartStore.GetItems(ctx, userID)
			if err != nil {
				t.Fatalf("GetItems after call %d: %v", i, err)
			}

			if len(items) != 1 {
				t.Fatalf("after %d AddItem calls: got %d items, want 1 (upsert)", i, len(items))
			}

			if items[0].Quantity != i {
				t.Fatalf("after %d AddItem calls: quantity = %d, want %d", i, items[0].Quantity, i)
			}

			if items[0].ProductID != productID {
				t.Errorf("ProductID: got %d, want %d", items[0].ProductID, productID)
			}

			if items[0].UserID != userID {
				t.Errorf("UserID: got %d, want %d", items[0].UserID, userID)
			}
		}
	})
}

// Feature: shop_bot, Property 7: Удаление товара из корзины
// Validates: Requirements 4.6

func TestCartRemoveItem_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db, err := New(":memory:")
		if err != nil {
			t.Fatalf("New(:memory:): %v", err)
		}
		defer db.Close()

		ctx := context.Background()
		cartStore := NewSQLCartStore(db)

		// Seed a category and product for FK constraints.
		res, err := db.Conn().ExecContext(ctx,
			"INSERT INTO categories (name, emoji) VALUES (?, ?)", "TestCat", "🧪")
		if err != nil {
			t.Fatalf("seed category: %v", err)
		}
		catID, _ := res.LastInsertId()

		res, err = db.Conn().ExecContext(ctx,
			`INSERT INTO products (category_id, name, description, photo_url, price_usd, price_stars, stock, is_active)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			catID, "TestProduct", "desc", "photo1", 9.99, 100, 10, true)
		if err != nil {
			t.Fatalf("seed product: %v", err)
		}
		productID, _ := res.LastInsertId()

		// Generate a random userID and number of AddItem calls (1-5).
		userID := rapid.Int64Range(1, 1_000_000).Draw(t, "userID")
		addCount := rapid.IntRange(1, 5).Draw(t, "addCount")

		// Add the product to cart addCount times (resulting in quantity = addCount).
		for i := 0; i < addCount; i++ {
			if err := cartStore.AddItem(ctx, userID, productID); err != nil {
				t.Fatalf("AddItem call %d: %v", i+1, err)
			}
		}

		// Verify the product is in the cart before removal.
		itemsBefore, err := cartStore.GetItems(ctx, userID)
		if err != nil {
			t.Fatalf("GetItems before remove: %v", err)
		}
		if len(itemsBefore) != 1 {
			t.Fatalf("expected 1 item in cart before remove, got %d", len(itemsBefore))
		}

		// Remove the product from the cart.
		if err := cartStore.RemoveItem(ctx, userID, productID); err != nil {
			t.Fatalf("RemoveItem: %v", err)
		}

		// Verify GetItems returns empty list — product no longer present.
		itemsAfter, err := cartStore.GetItems(ctx, userID)
		if err != nil {
			t.Fatalf("GetItems after remove: %v", err)
		}
		if len(itemsAfter) != 0 {
			t.Fatalf("expected 0 items after RemoveItem, got %d (product %d still present with quantity %d)",
				len(itemsAfter), itemsAfter[0].ProductID, itemsAfter[0].Quantity)
		}
	})
}
