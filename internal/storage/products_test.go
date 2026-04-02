package storage

import (
	"context"
	"testing"

	"pgregory.net/rapid"
)

// newTestProductStore creates an in-memory DB and returns a ready SQLProductStore.
func newTestProductStore(t *testing.T) (*SQLProductStore, *DB) {
	t.Helper()
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	return NewSQLProductStore(db), db
}

// seedCategory inserts a category and returns its ID.
func seedCategory(t *testing.T, db *DB, name, emoji string) int64 {
	t.Helper()
	res, err := db.Conn().Exec("INSERT INTO categories (name, emoji) VALUES (?, ?)", name, emoji)
	if err != nil {
		t.Fatalf("seed category: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestGetCategories_ReturnsAll(t *testing.T) {
	store, db := newTestProductStore(t)
	defer db.Close()

	seedCategory(t, db, "Electronics", "📱")
	seedCategory(t, db, "Books", "📚")

	cats, err := store.GetCategories(context.Background())
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	if len(cats) != 2 {
		t.Fatalf("got %d categories, want 2", len(cats))
	}
}

func TestGetCategories_Empty(t *testing.T) {
	store, db := newTestProductStore(t)
	defer db.Close()

	cats, err := store.GetCategories(context.Background())
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	if len(cats) != 0 {
		t.Fatalf("got %d categories, want 0", len(cats))
	}
}

func TestCreateProduct_ReturnsID(t *testing.T) {
	store, db := newTestProductStore(t)
	defer db.Close()

	catID := seedCategory(t, db, "Test", "🧪")
	p := &Product{
		CategoryID:  catID,
		Name:        "Widget",
		Description: "A fine widget",
		PriceUSD:    9.99,
		PriceStars:  100,
		PhotoURL:    "photo123",
		IsActive:    true, Stock: 10,
	}

	id, err := store.CreateProduct(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	if id <= 0 {
		t.Fatalf("got id %d, want > 0", id)
	}
}

func TestGetProduct_Found(t *testing.T) {
	store, db := newTestProductStore(t)
	defer db.Close()
	ctx := context.Background()

	catID := seedCategory(t, db, "Cat", "🐱")
	id, err := store.CreateProduct(ctx, &Product{
		CategoryID: catID, Name: "Toy", Description: "desc",
		PriceUSD: 5.0, PriceStars: 50, PhotoURL: "ph", IsActive: true, Stock: 10,
	})
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}

	got, err := store.GetProduct(ctx, id)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if got.Name != "Toy" || got.PriceUSD != 5.0 {
		t.Errorf("unexpected product: %+v", got)
	}
}

func TestGetProduct_NotFound(t *testing.T) {
	store, db := newTestProductStore(t)
	defer db.Close()

	_, err := store.GetProduct(context.Background(), 999)
	if err != ErrNotFound {
		t.Fatalf("got err %v, want ErrNotFound", err)
	}
}

func TestGetProductsByCategory(t *testing.T) {
	store, db := newTestProductStore(t)
	defer db.Close()
	ctx := context.Background()

	cat1 := seedCategory(t, db, "A", "🅰️")
	cat2 := seedCategory(t, db, "B", "🅱️")

	store.CreateProduct(ctx, &Product{CategoryID: cat1, Name: "P1", PriceUSD: 1, PriceStars: 10, IsActive: true, Stock: 10})
	store.CreateProduct(ctx, &Product{CategoryID: cat1, Name: "P2", PriceUSD: 2, PriceStars: 20, IsActive: true, Stock: 10})
	store.CreateProduct(ctx, &Product{CategoryID: cat2, Name: "P3", PriceUSD: 3, PriceStars: 30, IsActive: true, Stock: 10})

	products, err := store.GetProductsByCategory(ctx, cat1)
	if err != nil {
		t.Fatalf("GetProductsByCategory: %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("got %d products for cat1, want 2", len(products))
	}
}

func TestUpdateProduct(t *testing.T) {
	store, db := newTestProductStore(t)
	defer db.Close()
	ctx := context.Background()

	catID := seedCategory(t, db, "Cat", "🐱")
	id, _ := store.CreateProduct(ctx, &Product{
		CategoryID: catID, Name: "Old", PriceUSD: 1, PriceStars: 10, IsActive: true, Stock: 10,
	})

	err := store.UpdateProduct(ctx, &Product{
		ID: id, CategoryID: catID, Name: "New", Description: "updated",
		PriceUSD: 2, PriceStars: 20, PhotoURL: "newph", IsActive: false, Stock: 0,
	})
	if err != nil {
		t.Fatalf("UpdateProduct: %v", err)
	}

	got, _ := store.GetProduct(ctx, id)
	if got.Name != "New" || got.PriceUSD != 2 || got.IsActive || got.Stock != 0 {
		t.Errorf("update not applied: %+v", got)
	}
}

func TestDeleteProduct(t *testing.T) {
	store, db := newTestProductStore(t)
	defer db.Close()
	ctx := context.Background()

	catID := seedCategory(t, db, "Cat", "🐱")
	id, _ := store.CreateProduct(ctx, &Product{
		CategoryID: catID, Name: "Gone", PriceUSD: 1, PriceStars: 10, IsActive: true, Stock: 10,
	})

	if err := store.DeleteProduct(ctx, id); err != nil {
		t.Fatalf("DeleteProduct: %v", err)
	}

	_, err := store.GetProduct(ctx, id)
	if err != ErrNotFound {
		t.Fatalf("after delete: got err %v, want ErrNotFound", err)
	}
}

// TestDeleteProduct_NonExistent verifies that deleting a product with a
// non-existent ID does not return an error (no-op).
// Validates: Requirements 9.7
func TestDeleteProduct_NonExistent(t *testing.T) {
	store, db := newTestProductStore(t)
	defer db.Close()

	err := store.DeleteProduct(context.Background(), 999)
	if err != nil {
		t.Fatalf("DeleteProduct(999): got err %v, want nil (no-op)", err)
	}
}

// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3

func TestProductRoundTrip_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db, err := New(":memory:")
		if err != nil {
			t.Fatalf("New(:memory:): %v", err)
		}
		defer db.Close()
		store := NewSQLProductStore(db)
		ctx := context.Background()

		// Seed a category for the foreign key.
		res, err := db.Conn().ExecContext(ctx,
			"INSERT INTO categories (name, emoji) VALUES (?, ?)", "TestCat", "🧪")
		if err != nil {
			t.Fatalf("seed category: %v", err)
		}
		catID, _ := res.LastInsertId()

		// Generate random valid product data.
		name := rapid.StringMatching(`[A-Za-z0-9 ]{1,50}`).Draw(t, "name")
		description := rapid.StringMatching(`[A-Za-z0-9 ]{0,100}`).Draw(t, "description")
		priceUSD := rapid.Float64Range(0.01, 99999.99).Draw(t, "priceUSD")
		priceStars := rapid.IntRange(1, 999999).Draw(t, "priceStars")
		photoID := rapid.StringMatching(`[A-Za-z0-9]{0,30}`).Draw(t, "photoID")
		isActive := rapid.Bool().Draw(t, "isActive")

		p := &Product{
			CategoryID:  catID,
			Name:        name,
			Description: description,
			PriceUSD:    priceUSD,
			PriceStars:  priceStars,
			PhotoURL:    photoID,
			IsActive:    isActive,
			Stock:       1,
		}

		id, err := store.CreateProduct(ctx, p)
		if err != nil {
			t.Fatalf("CreateProduct: %v", err)
		}

		got, err := store.GetProduct(ctx, id)
		if err != nil {
			t.Fatalf("GetProduct: %v", err)
		}

		// Verify all fields match.
		if got.ID != id {
			t.Errorf("ID: got %d, want %d", got.ID, id)
		}
		if got.CategoryID != catID {
			t.Errorf("CategoryID: got %d, want %d", got.CategoryID, catID)
		}
		if got.Name != name {
			t.Errorf("Name: got %q, want %q", got.Name, name)
		}
		if got.Description != description {
			t.Errorf("Description: got %q, want %q", got.Description, description)
		}
		// Use epsilon for float64 comparison (SQLite REAL precision).
		if diff := got.PriceUSD - priceUSD; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("PriceUSD: got %f, want %f", got.PriceUSD, priceUSD)
		}
		if got.PriceStars != priceStars {
			t.Errorf("PriceStars: got %d, want %d", got.PriceStars, priceStars)
		}
		if got.PhotoURL != photoID {
			t.Errorf("PhotoURL: got %q, want %q", got.PhotoURL, photoID)
		}
		if got.IsActive != isActive {
			t.Errorf("IsActive: got %v, want %v", got.IsActive, isActive)
		}
		if got.CreatedAt.IsZero() {
			t.Error("CreatedAt should not be zero")
		}
	})
}

// Feature: shop_bot, Property 2: Round-trip хранилища данных
// Validates: Requirements 12.5, 9.3
func TestCategoryRoundTrip_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db, err := New(":memory:")
		if err != nil {
			t.Fatalf("New(:memory:): %v", err)
		}
		defer db.Close()
		store := NewSQLProductStore(db)
		ctx := context.Background()

		// Generate random valid category data.
		name := rapid.StringMatching(`[A-Za-z0-9 ]{1,50}`).Draw(t, "name")
		emoji := rapid.StringMatching(`[A-Za-z0-9]{0,10}`).Draw(t, "emoji")

		// Insert category directly (no CreateCategory method on ProductStore).
		res, err := db.Conn().ExecContext(ctx,
			"INSERT INTO categories (name, emoji) VALUES (?, ?)", name, emoji)
		if err != nil {
			t.Fatalf("insert category: %v", err)
		}
		catID, _ := res.LastInsertId()

		// Read back via GetCategories.
		cats, err := store.GetCategories(ctx)
		if err != nil {
			t.Fatalf("GetCategories: %v", err)
		}

		// Find our category.
		var found *Category
		for i := range cats {
			if cats[i].ID == catID {
				found = &cats[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("category with ID %d not found in GetCategories result", catID)
		}

		if found.Name != name {
			t.Errorf("Name: got %q, want %q", found.Name, name)
		}
		if found.Emoji != emoji {
			t.Errorf("Emoji: got %q, want %q", found.Emoji, emoji)
		}
	})
}

// Feature: shop_bot, Property 15: CRUD товаров — обновление и удаление
// Validates: Requirements 9.4, 9.5

func TestProductUpdateAndDelete_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db, err := New(":memory:")
		if err != nil {
			t.Fatalf("New(:memory:): %v", err)
		}
		defer db.Close()
		store := NewSQLProductStore(db)
		ctx := context.Background()

		// Seed a category for the foreign key.
		res, err := db.Conn().ExecContext(ctx,
			"INSERT INTO categories (name, emoji) VALUES (?, ?)", "TestCat", "🧪")
		if err != nil {
			t.Fatalf("seed category: %v", err)
		}
		catID, _ := res.LastInsertId()

		// Generate random product data and create the product.
		origName := rapid.StringMatching(`[A-Za-z0-9 ]{1,50}`).Draw(t, "origName")
		origDesc := rapid.StringMatching(`[A-Za-z0-9 ]{0,100}`).Draw(t, "origDesc")
		origPriceUSD := rapid.Float64Range(0.01, 99999.99).Draw(t, "origPriceUSD")
		origPriceStars := rapid.IntRange(1, 999999).Draw(t, "origPriceStars")
		origPhotoURL := rapid.StringMatching(`[A-Za-z0-9]{0,30}`).Draw(t, "origPhotoURL")
		origInStock := rapid.Bool().Draw(t, "origInStock")

		id, err := store.CreateProduct(ctx, &Product{
			CategoryID:  catID,
			Name:        origName,
			Description: origDesc,
			PriceUSD:    origPriceUSD,
			PriceStars:  origPriceStars,
			PhotoURL:    origPhotoURL,
			IsActive:    origInStock,
			Stock:       1,
		})
		if err != nil {
			t.Fatalf("CreateProduct: %v", err)
		}

		// --- Update phase: generate new random values for all fields ---
		newName := rapid.StringMatching(`[A-Za-z0-9 ]{1,50}`).Draw(t, "newName")
		newDesc := rapid.StringMatching(`[A-Za-z0-9 ]{0,100}`).Draw(t, "newDesc")
		newPriceUSD := rapid.Float64Range(0.01, 99999.99).Draw(t, "newPriceUSD")
		newPriceStars := rapid.IntRange(1, 999999).Draw(t, "newPriceStars")
		newPhotoURL := rapid.StringMatching(`[A-Za-z0-9]{0,30}`).Draw(t, "newPhotoURL")
		newInStock := rapid.Bool().Draw(t, "newInStock")

		err = store.UpdateProduct(ctx, &Product{
			ID:          id,
			CategoryID:  catID,
			Name:        newName,
			Description: newDesc,
			PriceUSD:    newPriceUSD,
			PriceStars:  newPriceStars,
			PhotoURL:    newPhotoURL,
			IsActive:    newInStock,
			Stock:       1,
		})
		if err != nil {
			t.Fatalf("UpdateProduct: %v", err)
		}

		// Verify GetProduct returns the updated values.
		got, err := store.GetProduct(ctx, id)
		if err != nil {
			t.Fatalf("GetProduct after update: %v", err)
		}
		if got.Name != newName {
			t.Errorf("Name: got %q, want %q", got.Name, newName)
		}
		if got.Description != newDesc {
			t.Errorf("Description: got %q, want %q", got.Description, newDesc)
		}
		if diff := got.PriceUSD - newPriceUSD; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("PriceUSD: got %f, want %f", got.PriceUSD, newPriceUSD)
		}
		if got.PriceStars != newPriceStars {
			t.Errorf("PriceStars: got %d, want %d", got.PriceStars, newPriceStars)
		}
		if got.PhotoURL != newPhotoURL {
			t.Errorf("PhotoURL: got %q, want %q", got.PhotoURL, newPhotoURL)
		}
		if got.IsActive != newInStock {
			t.Errorf("IsActive: got %v, want %v", got.IsActive, newInStock)
		}

		// --- Delete phase ---
		err = store.DeleteProduct(ctx, id)
		if err != nil {
			t.Fatalf("DeleteProduct: %v", err)
		}

		// Verify GetProduct returns ErrNotFound after deletion.
		_, err = store.GetProduct(ctx, id)
		if err != ErrNotFound {
			t.Fatalf("GetProduct after delete: got err %v, want ErrNotFound", err)
		}
	})
}
