package service

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type MetricsService struct {
	ActiveCarts        prometheus.Gauge
	SuccessfulPayments prometheus.CounterVec
	OrdersCreated      prometheus.Counter
	CartsAbandoned     prometheus.Counter
	RequestDuration    prometheus.HistogramVec
}

// NewMetricsService registers all metrics on the default Prometheus registerer.
func NewMetricsService() *MetricsService {
	return NewMetricsServiceWith(prometheus.DefaultRegisterer)
}

// NewMetricsServiceWith registers all metrics on the given registerer. Tests
// pass a fresh prometheus.NewRegistry() to avoid duplicate-registration panics.
func NewMetricsServiceWith(reg prometheus.Registerer) *MetricsService {
	factory := promauto.With(reg)
	return &MetricsService{
		ActiveCarts: factory.NewGauge(prometheus.GaugeOpts{
			Name: "shop_active_carts_total",
			Help: "The total number of active shopping carts",
		}),
		SuccessfulPayments: *factory.NewCounterVec(prometheus.CounterOpts{
			Name: "shop_payments_successful_total",
			Help: "The total number of successful payments",
		}, []string{"type"}),
		OrdersCreated: factory.NewCounter(prometheus.CounterOpts{
			Name: "shop_orders_created_total",
			Help: "The total number of created orders",
		}),
		CartsAbandoned: factory.NewCounter(prometheus.CounterOpts{
			Name: "shop_carts_abandoned_total",
			Help: "The total number of abandoned carts recovered by the worker",
		}),
		RequestDuration: *factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "shop_request_duration_seconds",
			Help:    "Time spent processing updates",
			Buckets: prometheus.DefBuckets,
		}, []string{"type"}),
	}
}
