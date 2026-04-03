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

func NewMetricsService() *MetricsService {
	return &MetricsService{
		ActiveCarts: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "shop_active_carts_total",
			Help: "The total number of active shopping carts",
		}),
		SuccessfulPayments: *promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "shop_payments_successful_total",
			Help: "The total number of successful payments",
		}, []string{"type"}),
		OrdersCreated: promauto.NewCounter(prometheus.CounterOpts{
			Name: "shop_orders_created_total",
			Help: "The total number of created orders",
		}),
		CartsAbandoned: promauto.NewCounter(prometheus.CounterOpts{
			Name: "shop_carts_abandoned_total",
			Help: "The total number of abandoned carts recovered by the worker",
		}),
		RequestDuration: *promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "shop_request_duration_seconds",
			Help:    "Time spent processing updates",
			Buckets: prometheus.DefBuckets,
		}, []string{"type"}),
	}
}
