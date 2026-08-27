package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// RevenueSummary contains capture gross, succeeded refunds, and net revenue.
// The legacy Total* fields are net values.
type RevenueSummary struct {
	TotalOrders int
	PaidOrders  int
	GrossUSD    float64
	RefundUSD   float64
	TotalUSD    float64
	GrossStars  int
	RefundStars int
	TotalStars  int
}

// DailyRevenue contains cashflow for a single calendar day. Captures use their
// ledger creation day; refunds use completed_at and fall back to created_at.
// The legacy Total* fields are net values and may be negative on refund days.
type DailyRevenue struct {
	Date        string
	GrossUSD    float64
	RefundUSD   float64
	TotalUSD    float64
	GrossStars  int
	RefundStars int
	TotalStars  int
	OrderCount  int
}

// ProductStats contains gross merchandising figures for a single product.
// It is an inventory/sales ranking rather than a cash-ledger view: refunds do
// not infer returned quantities and therefore do not reduce these figures.
type ProductStats struct {
	ProductID    int64
	Name         string
	TotalSold    int
	TotalRevenue float64
}

// PaymentMethodStat contains per-provider gross, refunds, and net revenue. USD
// fields apply to crypto and Stars fields apply to Telegram Stars; Total* is net.
type PaymentMethodStat struct {
	Method      string
	OrderCount  int
	GrossUSD    float64
	RefundUSD   float64
	TotalUSD    float64
	GrossStars  int
	RefundStars int
	TotalStars  int
}

// TopBuyer contains aggregate capture gross, refunds, and net USD-equivalent
// value for a single user. Stars refunds use their capture's proportional
// catalog USD value. TotalUSD is net.
type TopBuyer struct {
	UserID    int64
	Orders    int
	GrossUSD  float64
	RefundUSD float64
	TotalUSD  float64
}

// PromoUsageStat contains checkout usage figures for a single promo code, not
// cash-ledger revenue. Refunds do not reverse promo redemption or the discount
// applied at checkout. The total
// discount is reconstructed from paid orders (order totals are stored after
// the discount was applied), so it is only computable when every paid order
// carries a discount percentage strictly between 0 and 100; otherwise
// DiscountKnown is false and DiscountUSD is a partial lower bound.
type PromoUsageStat struct {
	Code          string
	Uses          int
	DiscountUSD   float64
	DiscountKnown bool
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
		`WITH captures AS (
		     SELECT a.order_id, a.provider, a.external_id, a.amount_minor,
		            CASE WHEN a.provider = 'crypto' THEN a.amount_minor / 100.0
		                 ELSE o.total_usd END AS gross_usd
		       FROM payment_attempts a
		       JOIN orders o ON o.id = a.order_id
		      WHERE a.status = 'succeeded'
		         OR (a.status = 'needs_review' AND EXISTS (
		                SELECT 1
		                  FROM payment_events e
		                  JOIN payment_resolutions pr
		                    ON pr.target_kind = 'payment_event' AND pr.target_id = e.id
		                 WHERE e.payment_attempt_id = a.id
		                   AND e.order_id = a.order_id
		                   AND e.provider = a.provider
		                   AND e.event_kind = 'captured'
		                   AND e.external_id = a.external_id
		                   AND e.amount_minor = a.amount_minor
		                   AND e.currency = a.currency
		                   AND e.scale = a.scale
		                   AND pr.order_id = a.order_id
		                   AND pr.provider = a.provider
		                   AND pr.decision = 'compensated'))
		 ), refund_values AS (
		     SELECT r.provider, r.amount_minor,
		            CASE WHEN r.provider = 'crypto' THEN r.amount_minor / 100.0
		                 WHEN c.amount_minor > 0 THEN c.gross_usd * r.amount_minor / c.amount_minor
		                 ELSE 0 END AS refund_usd
		       FROM refunds r
		       JOIN captures c ON c.provider = r.provider AND c.external_id = r.payment_external_id
		      WHERE r.status = 'succeeded'
		 )
		 SELECT (SELECT COUNT(*) FROM orders),
		        COUNT(DISTINCT order_id),
		        COALESCE(SUM(gross_usd), 0),
		        COALESCE((SELECT SUM(refund_usd) FROM refund_values), 0),
		        COALESCE(SUM(gross_usd), 0) - COALESCE((SELECT SUM(refund_usd) FROM refund_values), 0),
		        COALESCE(SUM(CASE WHEN provider = 'stars' THEN amount_minor ELSE 0 END), 0),
		        COALESCE((SELECT SUM(CASE WHEN provider = 'stars' THEN amount_minor ELSE 0 END) FROM refund_values), 0),
		        COALESCE(SUM(CASE WHEN provider = 'stars' THEN amount_minor ELSE 0 END), 0)
		          - COALESCE((SELECT SUM(CASE WHEN provider = 'stars' THEN amount_minor ELSE 0 END) FROM refund_values), 0)
		   FROM captures`,
	).Scan(&r.TotalOrders, &r.PaidOrders,
		&r.GrossUSD, &r.RefundUSD, &r.TotalUSD,
		&r.GrossStars, &r.RefundStars, &r.TotalStars)
	if err != nil {
		return nil, fmt.Errorf("analytics: get revenue summary: %w", err)
	}
	return &r, nil
}

// GetRevenueByDays returns per-day revenue for the last N days.
func (s *SQLAnalyticsStore) GetRevenueByDays(ctx context.Context, days int) ([]DailyRevenue, error) {
	rows, err := s.db.QueryContext(ctx,
		`WITH captures AS (
		     SELECT DATE(a.created_at) AS day, a.provider, a.order_id, a.external_id,
		            a.amount_minor, CASE WHEN a.provider = 'crypto' THEN a.amount_minor / 100.0
		                                 ELSE o.total_usd END AS gross_usd
		       FROM payment_attempts a
		       JOIN orders o ON o.id = a.order_id
		      WHERE a.status = 'succeeded'
		         OR (a.status = 'needs_review' AND EXISTS (
		                SELECT 1
		                  FROM payment_events e
		                  JOIN payment_resolutions pr
		                    ON pr.target_kind = 'payment_event' AND pr.target_id = e.id
		                 WHERE e.payment_attempt_id = a.id
		                   AND e.order_id = a.order_id
		                   AND e.provider = a.provider
		                   AND e.event_kind = 'captured'
		                   AND e.external_id = a.external_id
		                   AND e.amount_minor = a.amount_minor
		                   AND e.currency = a.currency
		                   AND e.scale = a.scale
		                   AND pr.order_id = a.order_id
		                   AND pr.provider = a.provider
		                   AND pr.decision = 'compensated'))
		 ), cashflow AS (
		     SELECT day, provider, order_id, amount_minor AS gross_minor,
		            0 AS refund_minor, gross_usd, 0.0 AS refund_usd, 1 AS is_capture
		       FROM captures
		     UNION ALL
		     SELECT DATE(COALESCE(r.completed_at, r.created_at)), r.provider, r.order_id,
		            0, r.amount_minor, 0.0,
		            CASE WHEN r.provider = 'crypto' THEN r.amount_minor / 100.0
		                 WHEN c.amount_minor > 0 THEN c.gross_usd * r.amount_minor / c.amount_minor
		                 ELSE 0 END,
		            0
		       FROM refunds r
		       JOIN captures c ON c.provider = r.provider AND c.external_id = r.payment_external_id
		      WHERE r.status = 'succeeded'
		 )
		 SELECT day,
		        COALESCE(SUM(gross_usd), 0),
		        COALESCE(SUM(refund_usd), 0),
		        COALESCE(SUM(gross_usd - refund_usd), 0),
		        COALESCE(SUM(CASE WHEN provider = 'stars' THEN gross_minor ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN provider = 'stars' THEN refund_minor ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN provider = 'stars' THEN gross_minor - refund_minor ELSE 0 END), 0),
		        COUNT(DISTINCT CASE WHEN is_capture = 1 THEN order_id END)
		   FROM cashflow
		  WHERE day >= DATE('now', '-'||?||' days')
		  GROUP BY day
		  ORDER BY day DESC`, days)
	if err != nil {
		return nil, fmt.Errorf("analytics: get revenue by days: %w", err)
	}
	defer rows.Close()

	var result []DailyRevenue
	for rows.Next() {
		var d DailyRevenue
		if err := rows.Scan(&d.Date,
			&d.GrossUSD, &d.RefundUSD, &d.TotalUSD,
			&d.GrossStars, &d.RefundStars, &d.TotalStars,
			&d.OrderCount); err != nil {
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
		`WITH captures AS (
		     SELECT a.provider, a.order_id, a.external_id, a.amount_minor,
		            CASE WHEN a.provider = 'crypto' THEN a.amount_minor / 100.0
		                 ELSE o.total_usd END AS gross_usd
		       FROM payment_attempts a
		       JOIN orders o ON o.id = a.order_id
		      WHERE a.status = 'succeeded'
		         OR (a.status = 'needs_review' AND EXISTS (
		                SELECT 1
		                  FROM payment_events e
		                  JOIN payment_resolutions pr
		                    ON pr.target_kind = 'payment_event' AND pr.target_id = e.id
		                 WHERE e.payment_attempt_id = a.id
		                   AND e.order_id = a.order_id
		                   AND e.provider = a.provider
		                   AND e.event_kind = 'captured'
		                   AND e.external_id = a.external_id
		                   AND e.amount_minor = a.amount_minor
		                   AND e.currency = a.currency
		                   AND e.scale = a.scale
		                   AND pr.order_id = a.order_id
		                   AND pr.provider = a.provider
		                   AND pr.decision = 'compensated'))
		 ), cashflow AS (
		     SELECT provider, order_id, amount_minor AS gross_minor,
		            0 AS refund_minor, gross_usd, 0.0 AS refund_usd, 1 AS is_capture
		       FROM captures
		     UNION ALL
		     SELECT r.provider, r.order_id, 0, r.amount_minor, 0.0,
		            CASE WHEN r.provider = 'crypto' THEN r.amount_minor / 100.0
		                 WHEN c.amount_minor > 0 THEN c.gross_usd * r.amount_minor / c.amount_minor
		                 ELSE 0 END,
		            0
		       FROM refunds r
		       JOIN captures c ON c.provider = r.provider AND c.external_id = r.payment_external_id
		      WHERE r.status = 'succeeded'
		 )
		 SELECT provider,
		        COUNT(DISTINCT CASE WHEN is_capture = 1 THEN order_id END),
		        COALESCE(SUM(gross_usd), 0),
		        COALESCE(SUM(refund_usd), 0),
		        COALESCE(SUM(gross_usd - refund_usd), 0),
		        COALESCE(SUM(CASE WHEN provider = 'stars' THEN gross_minor ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN provider = 'stars' THEN refund_minor ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN provider = 'stars' THEN gross_minor - refund_minor ELSE 0 END), 0)
		   FROM cashflow
		  GROUP BY provider
		  ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("analytics: get payment method stats: %w", err)
	}
	defer rows.Close()

	var result []PaymentMethodStat
	for rows.Next() {
		var ps PaymentMethodStat
		if err := rows.Scan(&ps.Method, &ps.OrderCount,
			&ps.GrossUSD, &ps.RefundUSD, &ps.TotalUSD,
			&ps.GrossStars, &ps.RefundStars, &ps.TotalStars); err != nil {
			return nil, fmt.Errorf("analytics: scan payment method stat: %w", err)
		}
		result = append(result, ps)
	}
	return result, rows.Err()
}

// TopBuyers returns users ranked by total paid revenue, limited to the given
// count. Paid and delivered orders both count as revenue.
func (s *SQLAnalyticsStore) TopBuyers(ctx context.Context, limit int) ([]TopBuyer, error) {
	rows, err := s.db.QueryContext(ctx,
		`WITH refunds_by_capture AS (
		     SELECT provider, payment_external_id, SUM(amount_minor) AS refund_minor
		       FROM refunds
		      WHERE status = 'succeeded'
		      GROUP BY provider, payment_external_id
		 ), capture_cashflow AS (
		     SELECT a.order_id, o.user_id,
		            CASE WHEN a.provider = 'crypto'
		                 THEN a.amount_minor / 100.0
		                 ELSE o.total_usd END AS gross_usd,
		            CASE WHEN a.provider = 'crypto'
		                 THEN COALESCE(r.refund_minor, 0) / 100.0
		                 WHEN a.amount_minor > 0
		                 THEN o.total_usd * COALESCE(r.refund_minor, 0) / a.amount_minor
		                 ELSE 0 END AS refund_usd
		       FROM payment_attempts a
		       JOIN orders o ON o.id = a.order_id
		       LEFT JOIN refunds_by_capture r
		         ON r.provider = a.provider AND r.payment_external_id = a.external_id
		      WHERE a.status = 'succeeded'
		         OR (a.status = 'needs_review' AND EXISTS (
		                SELECT 1
		                  FROM payment_events e
		                  JOIN payment_resolutions pr
		                    ON pr.target_kind = 'payment_event' AND pr.target_id = e.id
		                 WHERE e.payment_attempt_id = a.id
		                   AND e.order_id = a.order_id
		                   AND e.provider = a.provider
		                   AND e.event_kind = 'captured'
		                   AND e.external_id = a.external_id
		                   AND e.amount_minor = a.amount_minor
		                   AND e.currency = a.currency
		                   AND e.scale = a.scale
		                   AND pr.order_id = a.order_id
		                   AND pr.provider = a.provider
		                   AND pr.decision = 'compensated'))
		 )
		 SELECT user_id, COUNT(DISTINCT order_id),
		        COALESCE(SUM(gross_usd), 0),
		        COALESCE(SUM(refund_usd), 0),
		        COALESCE(SUM(gross_usd - refund_usd), 0)
		   FROM capture_cashflow
		  GROUP BY user_id
		  ORDER BY 5 DESC, user_id
		  LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("analytics: top buyers: %w", err)
	}
	defer rows.Close()

	var result []TopBuyer
	for rows.Next() {
		var b TopBuyer
		if err := rows.Scan(&b.UserID, &b.Orders, &b.GrossUSD, &b.RefundUSD, &b.TotalUSD); err != nil {
			return nil, fmt.Errorf("analytics: scan top buyer: %w", err)
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

// PromoUsage returns per-promo usage figures. Uses comes from the promo's own
// usage counter; the total discount is reconstructed from paid orders that
// reference the code: an order stores total_usd AFTER the discount, so the
// discount amount is total_usd * pct / (100 - pct). When any paid order
// carries a percentage outside (0, 100), the sum cannot be reconstructed and
// DiscountKnown is false.
func (s *SQLAnalyticsStore) PromoUsage(ctx context.Context) ([]PromoUsageStat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.code,
		        p.used_count,
		        COALESCE(SUM(CASE WHEN o.discount_pct > 0 AND o.discount_pct < 100
		                          THEN o.total_usd * o.discount_pct / (100.0 - o.discount_pct)
		                          ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN o.id IS NOT NULL AND (o.discount_pct <= 0 OR o.discount_pct >= 100)
		                          THEN 1 ELSE 0 END), 0)
		 FROM promo_codes p
		 LEFT JOIN orders o ON o.promo_code = p.code AND o.status = 'paid'
		 GROUP BY p.id
		 ORDER BY p.used_count DESC, p.code`)
	if err != nil {
		return nil, fmt.Errorf("analytics: promo usage: %w", err)
	}
	defer rows.Close()

	var result []PromoUsageStat
	for rows.Next() {
		var st PromoUsageStat
		var unknowable int
		if err := rows.Scan(&st.Code, &st.Uses, &st.DiscountUSD, &unknowable); err != nil {
			return nil, fmt.Errorf("analytics: scan promo usage: %w", err)
		}
		st.DiscountKnown = unknowable == 0
		result = append(result, st)
	}
	return result, rows.Err()
}
