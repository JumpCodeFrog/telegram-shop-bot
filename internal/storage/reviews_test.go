package storage

import (
	"context"
	"testing"
)

// newTestDB opens an in-memory DB with all migrations applied and closes it
// when the test finishes.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedProduct inserts a minimal active product (with its own category) and
// returns its ID.
func seedProduct(t *testing.T, db *DB, name string) int64 {
	t.Helper()
	catID := seedCategory(t, db, "Cat-"+name, "")
	res, err := db.Conn().Exec(
		`INSERT INTO products (category_id, name, price_usd, stock, is_active)
		 VALUES (?, ?, 1.0, 10, 1)`, catID, name)
	if err != nil {
		t.Fatalf("seed product: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// seedUser inserts a user row and returns its internal ID (needed for tables
// with a users(id) foreign key).
func seedUser(t *testing.T, db *DB, telegramID int64) int64 {
	t.Helper()
	res, err := db.Conn().Exec("INSERT INTO users (telegram_id) VALUES (?)", telegramID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestReviewUpsert_SecondUpsertReplaces(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLReviewStore(db)
	ctx := context.Background()
	productID := seedProduct(t, db, "Widget")

	if err := store.Upsert(ctx, Review{ProductID: productID, UserID: 7, Rating: 5, Text: "great"}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := store.Upsert(ctx, Review{ProductID: productID, UserID: 7, Rating: 2, Text: "changed my mind"}); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	reviews, err := store.ListByProduct(ctx, productID, 10)
	if err != nil {
		t.Fatalf("ListByProduct: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("got %d reviews, want 1 (upsert must replace, not duplicate)", len(reviews))
	}
	if reviews[0].Rating != 2 || reviews[0].Text != "changed my mind" {
		t.Errorf("got rating=%d text=%q, want rating=2 text=%q", reviews[0].Rating, reviews[0].Text, "changed my mind")
	}
	if reviews[0].UserID != 7 || reviews[0].ProductID != productID {
		t.Errorf("unexpected identity: %+v", reviews[0])
	}
}

func TestReviewProductRating_ThreeReviews(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLReviewStore(db)
	ctx := context.Background()
	productID := seedProduct(t, db, "Widget")

	for i, rating := range []int{5, 4, 3} {
		if err := store.Upsert(ctx, Review{ProductID: productID, UserID: int64(i + 1), Rating: rating}); err != nil {
			t.Fatalf("Upsert user %d: %v", i+1, err)
		}
	}

	avg, count, err := store.ProductRating(ctx, productID)
	if err != nil {
		t.Fatalf("ProductRating: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	if avg != 4.0 {
		t.Errorf("avg = %v, want 4.0", avg)
	}
}

func TestReviewProductRating_EmptyProduct(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLReviewStore(db)
	productID := seedProduct(t, db, "Lonely")

	avg, count, err := store.ProductRating(context.Background(), productID)
	if err != nil {
		t.Fatalf("ProductRating: %v", err)
	}
	if avg != 0 || count != 0 {
		t.Errorf("got (%v, %d), want (0, 0)", avg, count)
	}
}

func TestReviewListByProduct_LimitAndOrder(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLReviewStore(db)
	ctx := context.Background()
	productID := seedProduct(t, db, "Widget")
	otherID := seedProduct(t, db, "Other")

	for user := int64(1); user <= 3; user++ {
		if err := store.Upsert(ctx, Review{ProductID: productID, UserID: user, Rating: int(user) + 2}); err != nil {
			t.Fatalf("Upsert user %d: %v", user, err)
		}
	}
	// A review of another product must never leak into the listing.
	if err := store.Upsert(ctx, Review{ProductID: otherID, UserID: 99, Rating: 1}); err != nil {
		t.Fatalf("Upsert other product: %v", err)
	}

	reviews, err := store.ListByProduct(ctx, productID, 2)
	if err != nil {
		t.Fatalf("ListByProduct: %v", err)
	}
	if len(reviews) != 2 {
		t.Fatalf("got %d reviews, want 2 (limit)", len(reviews))
	}
	// Newest first: inserted 1, 2, 3 — so 3 then 2.
	if reviews[0].UserID != 3 || reviews[1].UserID != 2 {
		t.Errorf("order = [user %d, user %d], want newest first [3, 2]", reviews[0].UserID, reviews[1].UserID)
	}
	for _, r := range reviews {
		if r.ProductID != productID {
			t.Errorf("review %d belongs to product %d, want %d", r.ID, r.ProductID, productID)
		}
	}
}

func TestReviewDelete(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLReviewStore(db)
	ctx := context.Background()
	productID := seedProduct(t, db, "Widget")

	if err := store.Upsert(ctx, Review{ProductID: productID, UserID: 1, Rating: 5}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	reviews, err := store.ListByProduct(ctx, productID, 10)
	if err != nil || len(reviews) != 1 {
		t.Fatalf("ListByProduct: %v (len %d)", err, len(reviews))
	}

	if err := store.Delete(ctx, reviews[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, count, err := store.ProductRating(ctx, productID)
	if err != nil {
		t.Fatalf("ProductRating: %v", err)
	}
	if count != 0 {
		t.Errorf("count after delete = %d, want 0", count)
	}

	// Deleting a non-existent review is a no-op.
	if err := store.Delete(ctx, 424242); err != nil {
		t.Errorf("Delete(non-existent): %v, want nil", err)
	}
}
