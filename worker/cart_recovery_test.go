package worker

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

type mockCartRecoveryStore struct {
	olderThan   time.Duration
	activeCarts int64
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
func (m *mockCartRecoveryStore) CountActiveCarts(context.Context) (int64, error) {
	return m.activeCarts, nil
}

// gaugeValue reads the current value of a Prometheus gauge without pulling
// the testutil package (which would add a new module dependency).
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}

func TestCartRecoveryWorker_DefaultAbandonedAfter(t *testing.T) {
	store := &mockCartRecoveryStore{}
	worker := NewCartRecoveryWorker(nil, store, nil, nil, nil, nil, time.Hour)

	worker.runRecovery(context.Background())

	if store.olderThan != 24*time.Hour {
		t.Fatalf("expected default abandonedAfter 24h, got %v", store.olderThan)
	}
}

func TestCartRecoveryWorker_UsesConfiguredAbandonedAfter(t *testing.T) {
	store := &mockCartRecoveryStore{}
	worker := NewCartRecoveryWorker(nil, store, nil, nil, nil, nil, time.Hour, 36*time.Hour)

	worker.runRecovery(context.Background())

	if store.olderThan != 36*time.Hour {
		t.Fatalf("expected configured abandonedAfter 36h, got %v", store.olderThan)
	}
}

func TestCartRecoveryWorker_RecountsActiveCartsGauge(t *testing.T) {
	store := &mockCartRecoveryStore{activeCarts: 5}
	metrics := service.NewMetricsServiceWith(prometheus.NewRegistry())
	worker := NewCartRecoveryWorker(nil, store, nil, nil, nil, metrics, time.Hour)

	worker.runRecovery(context.Background())

	if got := gaugeValue(t, metrics.ActiveCarts); got != 5 {
		t.Fatalf("expected ActiveCarts gauge 5, got %v", got)
	}

	// The gauge is recomputed, not accumulated: a second tick with fewer
	// carts must lower it.
	store.activeCarts = 2
	worker.runRecovery(context.Background())

	if got := gaugeValue(t, metrics.ActiveCarts); got != 2 {
		t.Fatalf("expected ActiveCarts gauge 2 after recount, got %v", got)
	}
}
