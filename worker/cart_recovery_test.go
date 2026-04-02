package worker

import (
	"context"
	"testing"
	"time"

	"shop_bot/internal/storage"
)

type mockCartRecoveryStore struct {
	olderThan time.Duration
}

func (m *mockCartRecoveryStore) AddItem(context.Context, int64, int64) error { return nil }
func (m *mockCartRecoveryStore) UpdateQuantity(context.Context, int64, int64, int) error {
	return nil
}
func (m *mockCartRecoveryStore) RemoveItem(context.Context, int64, int64) error { return nil }
func (m *mockCartRecoveryStore) ClearCart(context.Context, int64) error         { return nil }
func (m *mockCartRecoveryStore) GetItems(context.Context, int64) ([]storage.CartItem, error) {
	return nil, nil
}
func (m *mockCartRecoveryStore) GetAbandonedCarts(_ context.Context, olderThan time.Duration) ([]int64, error) {
	m.olderThan = olderThan
	return nil, nil
}
func (m *mockCartRecoveryStore) MarkRecoverySent(context.Context, int64) error { return nil }

func TestCartRecoveryWorker_DefaultAbandonedAfter(t *testing.T) {
	store := &mockCartRecoveryStore{}
	worker := NewCartRecoveryWorker(nil, store, nil, time.Hour)

	worker.runRecovery(context.Background())

	if store.olderThan != 24*time.Hour {
		t.Fatalf("expected default abandonedAfter 24h, got %v", store.olderThan)
	}
}

func TestCartRecoveryWorker_UsesConfiguredAbandonedAfter(t *testing.T) {
	store := &mockCartRecoveryStore{}
	worker := NewCartRecoveryWorker(nil, store, nil, time.Hour, 36*time.Hour)

	worker.runRecovery(context.Background())

	if store.olderThan != 36*time.Hour {
		t.Fatalf("expected configured abandonedAfter 36h, got %v", store.olderThan)
	}
}
