package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
)

func newAnalyticsTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedOrder(t *testing.T, db *DB, userID int64, totalUSD float64, status, promoCode string, discountPct int) {
	t.Helper()
	res, err := db.Conn().ExecContext(context.Background(),
		`INSERT INTO orders (user_id, total_usd, total_stars, status, discount_pct, promo_code)
		 VALUES (?, ?, 0, ?, ?, ?)`,
		userID, totalUSD, status, discountPct, promoCode)
	if err != nil {
		t.Fatalf("seed order: %v", err)
	}
	if status == OrderStatusPaid {
		orderID, _ := res.LastInsertId()
		externalID := fmt.Sprintf("analytics-%d", orderID)
		if _, err := db.Conn().ExecContext(context.Background(),
			`INSERT INTO payment_attempts
			 (order_id,provider,external_id,amount_minor,currency,scale,status)
			 VALUES (?, 'crypto', ?, ?, 'USD', 2, 'succeeded')`,
			orderID, externalID, int64(math.Round(totalUSD*100))); err != nil {
			t.Fatalf("seed payment attempt: %v", err)
		}
	}
}

func TestTopBuyers(t *testing.T) {
	db := newAnalyticsTestDB(t)
	store := NewSQLAnalyticsStore(db)
	ctx := context.Background()

	// User 1: two paid orders, $30 total. User 2: one paid order, $100.
	// User 3: one paid order, $5. User 4: pending only — must not appear.
	seedOrder(t, db, 1, 10, OrderStatusPaid, "", 0)
	seedOrder(t, db, 1, 20, OrderStatusPaid, "", 0)
	seedOrder(t, db, 2, 100, OrderStatusPaid, "", 0)
	seedOrder(t, db, 3, 5, OrderStatusPaid, "", 0)
	seedOrder(t, db, 4, 500, OrderStatusPending, "", 0)

	buyers, err := store.TopBuyers(ctx, 10)
	if err != nil {
		t.Fatalf("TopBuyers: %v", err)
	}
	if len(buyers) != 3 {
		t.Fatalf("expected 3 buyers, got %d: %+v", len(buyers), buyers)
	}

	want := []TopBuyer{
		{UserID: 2, Orders: 1, GrossUSD: 100, TotalUSD: 100},
		{UserID: 1, Orders: 2, GrossUSD: 30, TotalUSD: 30},
		{UserID: 3, Orders: 1, GrossUSD: 5, TotalUSD: 5},
	}
	for i, w := range want {
		if buyers[i] != w {
			t.Errorf("buyer[%d]: expected %+v, got %+v", i, w, buyers[i])
		}
	}

	// Limit truncates after ranking.
	top1, err := store.TopBuyers(ctx, 1)
	if err != nil {
		t.Fatalf("TopBuyers(1): %v", err)
	}
	if len(top1) != 1 || top1[0].UserID != 2 {
		t.Fatalf("expected only user 2, got %+v", top1)
	}
}

func seedAnalyticsCapture(t *testing.T, db *DB, userID int64, totalUSD float64, totalStars int,
	provider, externalID string, amountMinor int64, currency string, scale int, createdModifier string,
) int64 {
	t.Helper()
	res, err := db.Conn().ExecContext(context.Background(),
		`INSERT INTO orders
		 (user_id, total_usd, total_stars, status, payment_state, payment_method, payment_id)
		 VALUES (?, ?, ?, 'paid', 'settled', ?, ?)`,
		userID, totalUSD, totalStars, provider, externalID)
	if err != nil {
		t.Fatalf("seed ledger order: %v", err)
	}
	orderID, _ := res.LastInsertId()
	if _, err := db.Conn().ExecContext(context.Background(),
		`INSERT INTO payment_attempts
		 (order_id, provider, external_id, amount_minor, currency, scale, status, occurred_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'succeeded', datetime('now', ?), datetime('now', ?))`,
		orderID, provider, externalID, amountMinor, currency, scale,
		createdModifier, createdModifier); err != nil {
		t.Fatalf("seed ledger capture: %v", err)
	}
	return orderID
}

func seedAnalyticsRenewal(t *testing.T, db *DB, orderID int64, provider, externalID string,
	amountMinor int64, currency string, scale int, createdModifier string,
) {
	t.Helper()
	if _, err := db.Conn().ExecContext(context.Background(),
		`INSERT INTO payment_attempts
		 (order_id, provider, external_id, amount_minor, currency, scale, status, occurred_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'succeeded', datetime('now', ?), datetime('now', ?))`,
		orderID, provider, externalID, amountMinor, currency, scale,
		createdModifier, createdModifier); err != nil {
		t.Fatalf("seed renewal capture: %v", err)
	}
}

func seedAnalyticsRefund(t *testing.T, db *DB, orderID int64, provider, externalID, paymentExternalID string,
	amountMinor int64, currency string, scale int, status, createdModifier string, completedModifier *string,
) {
	t.Helper()
	var completedAt any
	if completedModifier != nil {
		var timestamp string
		if err := db.Conn().QueryRow(`SELECT datetime('now', ?)`, *completedModifier).Scan(&timestamp); err != nil {
			t.Fatalf("refund completed timestamp: %v", err)
		}
		completedAt = timestamp
	}
	if _, err := db.Conn().ExecContext(context.Background(),
		`INSERT INTO refunds
		 (order_id, provider, external_id, payment_external_id, amount_minor,
		  currency, scale, status, requested_at, completed_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now', ?), ?, datetime('now', ?))`,
		orderID, provider, externalID, paymentExternalID, amountMinor,
		currency, scale, status, createdModifier, completedAt, createdModifier); err != nil {
		t.Fatalf("seed refund: %v", err)
	}
}

func sqliteRelativeDate(t *testing.T, db *DB, modifier string) string {
	t.Helper()
	var date string
	if err := db.Conn().QueryRow(`SELECT DATE('now', ?)`, modifier).Scan(&date); err != nil {
		t.Fatalf("relative date: %v", err)
	}
	return date
}

func TestRefundAwareCashAnalytics(t *testing.T) {
	db := newAnalyticsTestDB(t)
	store := NewSQLAnalyticsStore(db)
	ctx := context.Background()

	// Two crypto orders exercise partial and full refunds. One Stars order has
	// an initial capture plus a renewal and a proportional renewal refund.
	partialOrder := seedAnalyticsCapture(t, db, 10, 10, 0,
		PaymentMethodCrypto, "crypto-partial", 1000, "USDT", 2, "-2 days")
	fullOrder := seedAnalyticsCapture(t, db, 10, 20, 0,
		PaymentMethodCrypto, "crypto-full", 2000, "USDT", 2, "-2 days")
	starsOrder := seedAnalyticsCapture(t, db, 20, 5, 100,
		PaymentMethodStars, "stars-initial", 100, "XTR", 0, "-2 days")
	seedAnalyticsRenewal(t, db, starsOrder, PaymentMethodStars,
		"stars-renewal", 100, "XTR", 0, "-1 day")

	minusOneDay := "-1 day"
	today := "+0 days"
	seedAnalyticsRefund(t, db, partialOrder, PaymentMethodCrypto,
		"refund-partial", "crypto-partial", 400, "USDT", 2, "succeeded", "-4 days", &minusOneDay)
	// completed_at is deliberately NULL: daily cashflow must fall back to created_at.
	seedAnalyticsRefund(t, db, fullOrder, PaymentMethodCrypto,
		"refund-full", "crypto-full", 2000, "USDT", 2, "succeeded", today, nil)
	// The renewal refund was created earlier but completed today; completed_at wins.
	seedAnalyticsRefund(t, db, starsOrder, PaymentMethodStars,
		"refund-renewal", "stars-renewal", 50, "XTR", 0, "succeeded", "-5 days", &today)
	// Non-succeeded refunds never reduce revenue.
	seedAnalyticsRefund(t, db, partialOrder, PaymentMethodCrypto,
		"refund-requested", "crypto-partial", 300, "USDT", 2, "requested", today, nil)

	summary, err := store.GetRevenueSummary(ctx)
	if err != nil {
		t.Fatalf("GetRevenueSummary: %v", err)
	}
	if summary.TotalOrders != 3 || summary.PaidOrders != 3 ||
		math.Abs(summary.GrossUSD-40) > 1e-9 || math.Abs(summary.RefundUSD-26.5) > 1e-9 || math.Abs(summary.TotalUSD-13.5) > 1e-9 ||
		summary.GrossStars != 200 || summary.RefundStars != 50 || summary.TotalStars != 150 {
		t.Fatalf("summary=%+v", summary)
	}

	daily, err := store.GetRevenueByDays(ctx, 7)
	if err != nil {
		t.Fatalf("GetRevenueByDays: %v", err)
	}
	byDate := make(map[string]DailyRevenue, len(daily))
	for _, day := range daily {
		byDate[day.Date] = day
	}
	captureDay := byDate[sqliteRelativeDate(t, db, "-2 days")]
	if math.Abs(captureDay.GrossUSD-35) > 1e-9 || captureDay.RefundUSD != 0 || math.Abs(captureDay.TotalUSD-35) > 1e-9 ||
		captureDay.GrossStars != 100 || captureDay.RefundStars != 0 || captureDay.TotalStars != 100 || captureDay.OrderCount != 3 {
		t.Fatalf("capture day=%+v", captureDay)
	}
	partialDay := byDate[sqliteRelativeDate(t, db, "-1 day")]
	if math.Abs(partialDay.GrossUSD-5) > 1e-9 || math.Abs(partialDay.RefundUSD-4) > 1e-9 || math.Abs(partialDay.TotalUSD-1) > 1e-9 ||
		partialDay.GrossStars != 100 || partialDay.RefundStars != 0 || partialDay.TotalStars != 100 || partialDay.OrderCount != 1 {
		t.Fatalf("partial/renewal day=%+v", partialDay)
	}
	refundDay := byDate[sqliteRelativeDate(t, db, today)]
	if refundDay.GrossUSD != 0 || math.Abs(refundDay.RefundUSD-22.5) > 1e-9 || math.Abs(refundDay.TotalUSD+22.5) > 1e-9 ||
		refundDay.GrossStars != 0 || refundDay.RefundStars != 50 || refundDay.TotalStars != -50 || refundDay.OrderCount != 0 {
		t.Fatalf("refund day=%+v", refundDay)
	}

	methods, err := store.GetPaymentMethodStats(ctx)
	if err != nil {
		t.Fatalf("GetPaymentMethodStats: %v", err)
	}
	if len(methods) != 2 {
		t.Fatalf("methods=%+v", methods)
	}
	byMethod := make(map[string]PaymentMethodStat, len(methods))
	for _, method := range methods {
		byMethod[method.Method] = method
	}
	crypto := byMethod[PaymentMethodCrypto]
	if crypto.OrderCount != 2 || math.Abs(crypto.GrossUSD-30) > 1e-9 ||
		math.Abs(crypto.RefundUSD-24) > 1e-9 || math.Abs(crypto.TotalUSD-6) > 1e-9 {
		t.Fatalf("crypto method=%+v", crypto)
	}
	stars := byMethod[PaymentMethodStars]
	if stars.OrderCount != 1 || stars.GrossStars != 200 || stars.RefundStars != 50 || stars.TotalStars != 150 ||
		math.Abs(stars.GrossUSD-10) > 1e-9 || math.Abs(stars.RefundUSD-2.5) > 1e-9 || math.Abs(stars.TotalUSD-7.5) > 1e-9 {
		t.Fatalf("stars method=%+v", stars)
	}

	buyers, err := store.TopBuyers(ctx, 10)
	if err != nil {
		t.Fatalf("TopBuyers: %v", err)
	}
	if len(buyers) != 2 || buyers[0].UserID != 20 || buyers[0].Orders != 1 ||
		math.Abs(buyers[0].GrossUSD-10) > 1e-9 || math.Abs(buyers[0].RefundUSD-2.5) > 1e-9 || math.Abs(buyers[0].TotalUSD-7.5) > 1e-9 ||
		buyers[1].UserID != 10 || buyers[1].Orders != 2 || math.Abs(buyers[1].GrossUSD-30) > 1e-9 ||
		math.Abs(buyers[1].RefundUSD-24) > 1e-9 || math.Abs(buyers[1].TotalUSD-6) > 1e-9 {
		t.Fatalf("buyers=%+v", buyers)
	}
}

func TestAnalyticsExcludesUnresolvedNeedsReviewCaptureAndItsRefund(t *testing.T) {
	db := newAnalyticsTestDB(t)
	ctx := context.Background()
	store, orderID, _ := seedLedgerOrder(t, db, 100)
	if err := store.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUnexpectedPayment(ctx, orderID, "stars", "capture-b", "second_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatal(err)
	}
	if err := NewSQLPaymentLedgerStore(db).RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: "stars", ExternalID: "refund-b",
		PaymentExternalID: "capture-b", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := NewSQLAnalyticsStore(db).GetRevenueSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(summary.GrossUSD-12.5) > 1e-9 || summary.RefundUSD != 0 || math.Abs(summary.TotalUSD-12.5) > 1e-9 ||
		summary.GrossStars != 100 || summary.RefundStars != 0 || summary.TotalStars != 100 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestAnalyticsIncludesResolvedCompensatedCaptureAndItsRefund(t *testing.T) {
	db := newAnalyticsTestDB(t)
	ctx := context.Background()
	orders, orderID, _ := seedLedgerOrder(t, db, 100)
	if err := orders.UpdateOrderStatus(ctx, orderID, OrderStatusPending, OrderStatusPaid, "stars", "capture-a"); err != nil {
		t.Fatal(err)
	}
	if err := orders.RecordUnexpectedPayment(ctx, orderID, "stars", "capture-b", "duplicate_charge"); !errors.Is(err, ErrPaymentNeedsReview) {
		t.Fatalf("unexpected capture error=%v", err)
	}
	ledger := NewSQLPaymentLedgerStore(db)
	if err := ledger.RecordRefund(ctx, Refund{
		OrderID: orderID, Provider: PaymentMethodStars, ExternalID: "capture-b",
		PaymentExternalID: "capture-b", AmountMinor: 100, Currency: "XTR", Scale: 0,
	}); err != nil {
		t.Fatal(err)
	}
	cases, err := ledger.ListPaymentReviews(ctx, PaymentMethodStars)
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	resolution := PaymentReviewResolution{
		OrderID: orderID, Provider: PaymentMethodStars, Actor: "operator:test",
		Reason: "duplicate charge compensated", ResultingPaymentState: PaymentStateSettled,
	}
	for _, target := range cases[0].Targets {
		switch target.Kind {
		case PaymentReviewTargetEvent:
			resolution.EventIDs = append(resolution.EventIDs, target.ID)
		case PaymentReviewTargetAnomaly:
			resolution.AnomalyIDs = append(resolution.AnomalyIDs, target.ID)
		}
	}
	if err := ledger.ResolvePaymentReview(ctx, resolution); err != nil {
		t.Fatal(err)
	}

	analytics := NewSQLAnalyticsStore(db)
	summary, err := analytics.GetRevenueSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PaidOrders != 1 || math.Abs(summary.GrossUSD-25) > 1e-9 ||
		math.Abs(summary.RefundUSD-12.5) > 1e-9 || math.Abs(summary.TotalUSD-12.5) > 1e-9 ||
		summary.GrossStars != 200 || summary.RefundStars != 100 || summary.TotalStars != 100 {
		t.Fatalf("summary=%+v", summary)
	}

	daily, err := analytics.GetRevenueByDays(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	byDate := make(map[string]DailyRevenue, len(daily))
	for _, day := range daily {
		byDate[day.Date] = day
	}
	today := byDate[sqliteRelativeDate(t, db, "+0 days")]
	if math.Abs(today.GrossUSD-25) > 1e-9 || math.Abs(today.RefundUSD-12.5) > 1e-9 ||
		math.Abs(today.TotalUSD-12.5) > 1e-9 || today.GrossStars != 200 ||
		today.RefundStars != 100 || today.TotalStars != 100 || today.OrderCount != 1 {
		t.Fatalf("today=%+v", today)
	}

	methods, err := analytics.GetPaymentMethodStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || methods[0].Method != PaymentMethodStars || methods[0].OrderCount != 1 ||
		math.Abs(methods[0].GrossUSD-25) > 1e-9 || math.Abs(methods[0].RefundUSD-12.5) > 1e-9 ||
		math.Abs(methods[0].TotalUSD-12.5) > 1e-9 || methods[0].GrossStars != 200 ||
		methods[0].RefundStars != 100 || methods[0].TotalStars != 100 {
		t.Fatalf("methods=%+v", methods)
	}

	buyers, err := analytics.TopBuyers(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(buyers) != 1 || buyers[0].Orders != 1 || math.Abs(buyers[0].GrossUSD-25) > 1e-9 ||
		math.Abs(buyers[0].RefundUSD-12.5) > 1e-9 || math.Abs(buyers[0].TotalUSD-12.5) > 1e-9 {
		t.Fatalf("buyers=%+v", buyers)
	}
}

func TestTopBuyers_Empty(t *testing.T) {
	db := newAnalyticsTestDB(t)
	store := NewSQLAnalyticsStore(db)

	buyers, err := store.TopBuyers(context.Background(), 10)
	if err != nil {
		t.Fatalf("TopBuyers: %v", err)
	}
	if len(buyers) != 0 {
		t.Fatalf("expected no buyers, got %+v", buyers)
	}
}

func seedPromo(t *testing.T, db *DB, code string, discount, usedCount int) {
	t.Helper()
	_, err := db.Conn().ExecContext(context.Background(),
		`INSERT INTO promo_codes (code, discount, used_count, is_active) VALUES (?, ?, ?, 1)`,
		code, discount, usedCount)
	if err != nil {
		t.Fatalf("seed promo: %v", err)
	}
}

func TestPromoUsage(t *testing.T) {
	db := newAnalyticsTestDB(t)
	store := NewSQLAnalyticsStore(db)
	ctx := context.Background()

	seedPromo(t, db, "SALE10", 10, 2)
	seedPromo(t, db, "FREE100", 100, 1)
	seedPromo(t, db, "UNUSED", 25, 0)

	// SALE10: two paid orders of $90 each (post-discount) → the discount that
	// was applied is 90 * 10 / (100-10) = $10 per order, $20 total.
	seedOrder(t, db, 1, 90, OrderStatusPaid, "SALE10", 10)
	seedOrder(t, db, 2, 90, OrderStatusPaid, "SALE10", 10)
	// A pending order must not contribute to the discount sum.
	seedOrder(t, db, 3, 90, OrderStatusPending, "SALE10", 10)
	// FREE100: 100% discount cannot be reconstructed from a $0 total.
	seedOrder(t, db, 4, 0, OrderStatusPaid, "FREE100", 100)

	stats, err := store.PromoUsage(ctx)
	if err != nil {
		t.Fatalf("PromoUsage: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("expected 3 promo rows, got %d: %+v", len(stats), stats)
	}

	byCode := make(map[string]PromoUsageStat, len(stats))
	for _, st := range stats {
		byCode[st.Code] = st
	}

	sale := byCode["SALE10"]
	if sale.Uses != 2 || !sale.DiscountKnown || math.Abs(sale.DiscountUSD-20) > 1e-9 {
		t.Errorf("SALE10: expected uses=2 discount=$20 known, got %+v", sale)
	}

	free := byCode["FREE100"]
	if free.Uses != 1 || free.DiscountKnown {
		t.Errorf("FREE100: expected uses=1 with unknowable discount, got %+v", free)
	}

	unused := byCode["UNUSED"]
	if unused.Uses != 0 || !unused.DiscountKnown || unused.DiscountUSD != 0 {
		t.Errorf("UNUSED: expected zero uses and $0 known discount, got %+v", unused)
	}

	// Ordering: most-used promo first.
	if stats[0].Code != "SALE10" {
		t.Errorf("expected SALE10 first by used_count, got %+v", stats)
	}
}

func TestCountActiveCarts(t *testing.T) {
	db := newAnalyticsTestDB(t)
	cartStore := NewSQLCartStore(db)
	ctx := context.Background()

	n, err := cartStore.CountActiveCarts(ctx)
	if err != nil {
		t.Fatalf("CountActiveCarts: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 active carts, got %d", n)
	}

	// Seed a category and two products for FK constraints.
	res, err := db.Conn().ExecContext(ctx,
		"INSERT INTO categories (name, emoji) VALUES (?, ?)", "Cat", "🧪")
	if err != nil {
		t.Fatalf("seed category: %v", err)
	}
	catID, _ := res.LastInsertId()
	var productIDs []int64
	for range 2 {
		res, err = db.Conn().ExecContext(ctx,
			`INSERT INTO products (category_id, name, description, photo_url, price_usd, price_stars, stock, is_active)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			catID, "P", "d", "u", 1.0, 10, 10, true)
		if err != nil {
			t.Fatalf("seed product: %v", err)
		}
		id, _ := res.LastInsertId()
		productIDs = append(productIDs, id)
	}

	// User 7 holds two items, user 8 one: two distinct carts.
	if err := cartStore.AddItem(ctx, 7, productIDs[0]); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if err := cartStore.AddItem(ctx, 7, productIDs[1]); err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if err := cartStore.AddItem(ctx, 8, productIDs[0]); err != nil {
		t.Fatalf("AddItem: %v", err)
	}

	n, err = cartStore.CountActiveCarts(ctx)
	if err != nil {
		t.Fatalf("CountActiveCarts: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 active carts, got %d", n)
	}
}
