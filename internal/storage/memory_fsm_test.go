package storage

import (
	"context"
	"testing"
	"time"
)

func TestMemoryFSMStore_AddProductStateRoundTrip(t *testing.T) {
	store := NewMemoryFSMStore()
	state := &AddProductState{Name: "Widget", Step: StepDescription}

	if err := store.SetAddProductState(context.Background(), 42, state, time.Hour); err != nil {
		t.Fatalf("SetAddProductState: %v", err)
	}

	got, err := store.GetAddProductState(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetAddProductState: %v", err)
	}
	if got == nil || got.Name != "Widget" || got.Step != StepDescription {
		t.Fatalf("unexpected state: %+v", got)
	}

	if err := store.DelAddProductState(context.Background(), 42); err != nil {
		t.Fatalf("DelAddProductState: %v", err)
	}
	got, err = store.GetAddProductState(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetAddProductState after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil after delete, got %+v", got)
	}
}

func TestMemoryFSMStore_PromoStateRoundTrip(t *testing.T) {
	store := NewMemoryFSMStore()
	now := time.Now().Truncate(time.Second)

	if err := store.SetPromoState(context.Background(), 42, now, time.Hour); err != nil {
		t.Fatalf("SetPromoState: %v", err)
	}

	got, err := store.GetPromoState(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetPromoState: %v", err)
	}
	if got.IsZero() {
		t.Fatal("expected non-zero promo state time")
	}

	if err := store.DelPromoState(context.Background(), 42); err != nil {
		t.Fatalf("DelPromoState: %v", err)
	}
	got, err = store.GetPromoState(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetPromoState after delete: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("expected zero time after delete, got %v", got)
	}
}
