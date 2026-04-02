package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// RevenueSummary contains aggregate revenue figures across all orders.
type RevenueSummary struct {
	TotalOrders int
	PaidOrders  int
	TotalUSD    float64
	TotalStars  int
}

// DailyRevenue contains revenue figures for a single calendar day.
type DailyRevenue struct {
	Date       string
	TotalUSD   float64
	TotalStars int
	OrderCount int
}

// ProductStats contains sales figures for a single product.
type ProductStats struct {
	ProductID    int64
	Name         string
	TotalSold    int
	TotalRevenue float64
}

// PaymentMethodStat contains aggregate figures for a single payment method.
type PaymentMethodStat struct {
	Method     string
	OrderCount int
	TotalUSD   float64
}

// SQLAnalyticsStore implements AnalyticsStore using a *sql.DB connection.
type SQLAnalyticsStore struct {
	db *sql.DB
}

// NewSQLAnalyticsStore creates a new SQLAnalyticsStore from the given DB.
func NewSQLAnalyticsStore(d *DB) *SQLAnalyticsStore {
	return &SQLAnalyticsStore{db: d.Conn()}
}

// GetRevenueSummary returns aggregate order and revenue figures.
func (s *SQLAnalyticsStore) GetRevenueSummary(ctx context.Context) (*RevenueSummary, error) {
	var r RevenueSummary
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        SUM(CASE WHEN status = 'paid' THEN 1 ELSE 0 END),
		        COALESCE(SUM(CASE WHEN status = 'paid' THEN total_usd ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN status = 'paid' THEN total_stars ELSE 0 END), 0)
		 FROM orders`,
	).Scan(&r.TotalOrders, &r.PaidOrders, &r.TotalUSD, &r.TotalStars)
	if err != nil {
		return nil, fmt.Errorf("analytics: get revenue summary: %w", err)
	}
	return &r, nil
}

// GetRevenueByDays returns per-day revenue for the last N days.
func (s *SQLAnalyticsStore) GetRevenueByDays(ctx context.Context, days int) ([]DailyRevenue, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DATE(created_at),
		        COALESCE(SUM(total_usd), 0),
		        COALESCE(SUM(total_stars), 0),
		        COUNT(*)
		 FROM orders
		 WHERE status = 'paid'
		   AND created_at >= DATE('now', '-'||?||' days')
		 GROUP BY DATE(created_at)
		 ORDER BY DATE(created_at) DESC`, days)
	if err != nil {
		return nil, fmt.Errorf("analytics: get revenue by days: %w", err)
	}
	defer rows.Close()

	var result []DailyRevenue
	for rows.Next() {
		var d DailyRevenue
		if err := rows.Scan(&d.Date, &d.TotalUSD, &d.TotalStars, &d.OrderCount); err != nil {
			return nil, fmt.Errorf("analytics: scan daily revenue: %w", err)
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// GetTopProducts returns the best-selling products by quantity, limited to
// the given count.
func (s *SQLAnalyticsStore) GetTopProducts(ctx context.Context, limit int) ([]ProductStats, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT oi.product_id,
		        COALESCE(p.name, 'Удалён'),
		        SUM(oi.quantity),
		        COALESCE(SUM(oi.price_usd * oi.quantity), 0)
		 FROM order_items oi
		 LEFT JOIN products p ON p.id = oi.product_id
		 JOIN orders o ON o.id = oi.order_id AND o.status = 'paid'
		 GROUP BY oi.product_id
		 ORDER BY SUM(oi.quantity) DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("analytics: get top products: %w", err)
	}
	defer rows.Close()

	var result []ProductStats
	for rows.Next() {
		var ps ProductStats
		if err := rows.Scan(&ps.ProductID, &ps.Name, &ps.TotalSold, &ps.TotalRevenue); err != nil {
			return nil, fmt.Errorf("analytics: scan product stats: %w", err)
		}
		result = append(result, ps)
	}
	return result, rows.Err()
}

// GetPaymentMethodStats returns aggregate figures grouped by payment method.
func (s *SQLAnalyticsStore) GetPaymentMethodStats(ctx context.Context) ([]PaymentMethodStat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT COALESCE(payment_method, 'unknown'), COUNT(*), COALESCE(SUM(total_usd), 0)
		 FROM orders
		 WHERE status = 'paid'
		 GROUP BY payment_method`)
	if err != nil {
		return nil, fmt.Errorf("analytics: get payment method stats: %w", err)
	}
	defer rows.Close()

	var result []PaymentMethodStat
	for rows.Next() {
		var ps PaymentMethodStat
		if err := rows.Scan(&ps.Method, &ps.OrderCount, &ps.TotalUSD); err != nil {
			return nil, fmt.Errorf("analytics: scan payment method stat: %w", err)
		}
		result = append(result, ps)
	}
	return result, rows.Err()
}
