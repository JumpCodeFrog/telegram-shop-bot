package shop

import (
	"context"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

// counterValue reads the current value of a Prometheus counter without
// pulling the testutil package (which would add a new module dependency).
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// TestCreateFromCart_IncrementsOrdersCreated verifies the OrdersCreated
// counter grows once per successfully created order and stays untouched when
// order creation fails.
func TestCreateFromCart_IncrementsOrdersCreated(t *testing.T) {
	metrics := service.NewMetricsServiceWith(prometheus.NewRegistry())
	os := newMockOrderStore()
	svc := NewOrderService(os, &mockClearCartStore{}, isActiveProductStore{}, PaymentDeps{Metrics: metrics}, slog.Default())

	view := &CartView{
		Items:      []CartItemView{{Product: storage.Product{ID: 1, PriceUSD: 10.0, PriceStars: 100}, Quantity: 1}},
		TotalUSD:   10.0,
		TotalStars: 100,
	}

	for i := 1; i <= 3; i++ {
		if _, err := svc.CreateFromCart(context.Background(), 42, view, nil); err != nil {
			t.Fatalf("CreateFromCart #%d: %v", i, err)
		}
		if got := counterValue(t, metrics.OrdersCreated); got != float64(i) {
			t.Fatalf("after %d orders expected OrdersCreated=%d, got %v", i, i, got)
		}
	}

	// A rejected order (empty cart) must not bump the counter.
	if _, err := svc.CreateFromCart(context.Background(), 42, &CartView{}, nil); err != storage.ErrEmptyCart {
		t.Fatalf("expected ErrEmptyCart, got %v", err)
	}
	if got := counterValue(t, metrics.OrdersCreated); got != 3 {
		t.Fatalf("failed order must not increment OrdersCreated: got %v", got)
	}
}

// TestCreateFromCart_NilMetrics verifies the metrics dependency is optional.
func TestCreateFromCart_NilMetrics(t *testing.T) {
	os := newMockOrderStore()
	svc := NewOrderService(os, &mockClearCartStore{}, isActiveProductStore{}, PaymentDeps{}, slog.Default())

	view := &CartView{
		Items:      []CartItemView{{Product: storage.Product{ID: 1, PriceUSD: 10.0, PriceStars: 100}, Quantity: 1}},
		TotalUSD:   10.0,
		TotalStars: 100,
	}
	if _, err := svc.CreateFromCart(context.Background(), 42, view, nil); err != nil {
		t.Fatalf("CreateFromCart without metrics: %v", err)
	}
}
