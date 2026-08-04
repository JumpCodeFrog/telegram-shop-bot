package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestPhotoAdd_AssignsIncreasingSortOrder(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLProductPhotoStore(db)
	ctx := context.Background()
	productID := seedProduct(t, db, "Widget")

	for _, fileID := range []string{"file-a", "file-b", "file-c"} {
		if err := store.Add(ctx, productID, fileID); err != nil {
			t.Fatalf("Add(%s): %v", fileID, err)
		}
	}

	photos, err := store.List(ctx, productID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(photos) != 3 {
		t.Fatalf("got %d photos, want 3", len(photos))
	}
	for i, want := range []string{"file-a", "file-b", "file-c"} {
		if photos[i].FileID != want {
			t.Errorf("photos[%d].FileID = %q, want %q", i, photos[i].FileID, want)
		}
		if photos[i].SortOrder != i {
			t.Errorf("photos[%d].SortOrder = %d, want %d", i, photos[i].SortOrder, i)
		}
		if photos[i].ProductID != productID {
			t.Errorf("photos[%d].ProductID = %d, want %d", i, photos[i].ProductID, productID)
		}
	}
}

func TestPhotoAdd_LimitPerProduct(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLProductPhotoStore(db)
	ctx := context.Background()
	productID := seedProduct(t, db, "Widget")
	otherID := seedProduct(t, db, "Other")

	for i := range MaxProductPhotos {
		if err := store.Add(ctx, productID, fmt.Sprintf("file-%d", i)); err != nil {
			t.Fatalf("Add #%d: %v", i+1, err)
		}
	}

	err := store.Add(ctx, productID, "file-overflow")
	if !errors.Is(err, ErrTooManyPhotos) {
		t.Fatalf("11th Add error = %v, want ErrTooManyPhotos", err)
	}
	photos, err := store.List(ctx, productID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(photos) != MaxProductPhotos {
		t.Errorf("got %d photos after overflow attempt, want %d", len(photos), MaxProductPhotos)
	}

	// The limit is per product: another product is unaffected.
	if err := store.Add(ctx, otherID, "other-file"); err != nil {
		t.Errorf("Add to other product: %v, want nil", err)
	}
}

func TestPhotoDelete(t *testing.T) {
	db := newTestDB(t)
	store := NewSQLProductPhotoStore(db)
	ctx := context.Background()
	productID := seedProduct(t, db, "Widget")

	for _, fileID := range []string{"file-a", "file-b"} {
		if err := store.Add(ctx, productID, fileID); err != nil {
			t.Fatalf("Add(%s): %v", fileID, err)
		}
	}
	photos, err := store.List(ctx, productID)
	if err != nil || len(photos) != 2 {
		t.Fatalf("List: %v (len %d)", err, len(photos))
	}

	if err := store.Delete(ctx, photos[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	photos, err = store.List(ctx, productID)
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(photos) != 1 || photos[0].FileID != "file-b" {
		t.Errorf("after delete got %+v, want single file-b", photos)
	}

	// Deleting a non-existent photo is a no-op.
	if err := store.Delete(ctx, 424242); err != nil {
		t.Errorf("Delete(non-existent): %v, want nil", err)
	}
}
