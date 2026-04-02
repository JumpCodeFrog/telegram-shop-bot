package service

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type MetricsService struct {
	ActiveCarts      prometheus.Gauge
	SuccessfulPayments prometheus.CounterVec
	RequestDuration  prometheus.HistogramVec
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
		}, []string{"currency"}),
		RequestDuration: *promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "shop_request_duration_seconds",
			Help:    "Time spent processing updates",
			Buckets: prometheus.DefBuckets,
		}, []string{"type"}),
	}
}
