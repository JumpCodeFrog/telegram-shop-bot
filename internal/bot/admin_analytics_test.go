package bot

import (
	"strings"
	"testing"
	"time"

	"shop_bot/internal/storage"
)

func TestRenderRevenueChart_NormalizesToMax(t *testing.T) {
	today := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	daily := []storage.DailyRevenue{
		{Date: "2026-08-04", TotalUSD: 100},
		{Date: "2026-08-02", TotalUSD: 50},
	}

	got := renderRevenueChart(daily, today, 3)
	want := strings.Join([]string{
		"08-02 " + strings.Repeat("▇", 5) + " $50.00",
		"08-03 ·",
		"08-04 " + strings.Repeat("▇", 10) + " $100.00",
	}, "\n") + "\n"

	if got != want {
		t.Fatalf("chart mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderRevenueChart_EmptyDaysAreDots(t *testing.T) {
	today := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)

	got := renderRevenueChart(nil, today, 14)

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 14 {
		t.Fatalf("expected 14 lines, got %d:\n%s", len(lines), got)
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, " ·") {
			t.Errorf("expected empty-day dot line, got %q", line)
		}
		if strings.Contains(line, "▇") {
			t.Errorf("empty chart must have no bars, got %q", line)
		}
	}
	if !strings.HasPrefix(lines[0], "07-22 ") || !strings.HasPrefix(lines[13], "08-04 ") {
		t.Errorf("expected window 07-22..08-04, got first=%q last=%q", lines[0], lines[13])
	}
}

func TestRenderRevenueChart_TinyRevenueStillGetsOneBar(t *testing.T) {
	today := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	daily := []storage.DailyRevenue{
		{Date: "2026-08-04", TotalUSD: 100},
		{Date: "2026-08-03", TotalUSD: 1}, // 1% of max rounds to 0 bars → clamped to 1
	}

	got := renderRevenueChart(daily, today, 2)

	if !strings.Contains(got, "08-03 ▇ $1.00") {
		t.Fatalf("expected a single bar for the tiny day, got:\n%s", got)
	}
}

func TestParseExportRange(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}

	t.Run("no args", func(t *testing.T) {
		from, to, bad := parseExportRange(nil)
		if from != nil || to != nil || bad != "" {
			t.Fatalf("expected unbounded range, got from=%v to=%v bad=%q", from, to, bad)
		}
	})

	t.Run("from only", func(t *testing.T) {
		from, to, bad := parseExportRange([]string{"2026-01-01"})
		if bad != "" || from == nil || to != nil {
			t.Fatalf("unexpected result: from=%v to=%v bad=%q", from, to, bad)
		}
		if !from.Equal(day(2026, 1, 1)) {
			t.Fatalf("expected from 2026-01-01, got %v", from)
		}
	})

	t.Run("from and to", func(t *testing.T) {
		from, to, bad := parseExportRange([]string{"2026-01-01", "2026-02-01"})
		if bad != "" || from == nil || to == nil {
			t.Fatalf("unexpected result: from=%v to=%v bad=%q", from, to, bad)
		}
		// The to-bound is exclusive midnight after the inclusive end date.
		if !to.Equal(day(2026, 2, 2)) {
			t.Fatalf("expected exclusive to-bound 2026-02-02, got %v", to)
		}
	})

	t.Run("bad from", func(t *testing.T) {
		_, _, bad := parseExportRange([]string{"01.02.2026"})
		if bad != "01.02.2026" {
			t.Fatalf("expected bad arg %q, got %q", "01.02.2026", bad)
		}
	})

	t.Run("bad to", func(t *testing.T) {
		_, _, bad := parseExportRange([]string{"2026-01-01", "2026-13-40"})
		if bad != "2026-13-40" {
			t.Fatalf("expected bad arg %q, got %q", "2026-13-40", bad)
		}
	})
}

func TestFilterOrdersByDate(t *testing.T) {
	at := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 15, 30, 0, 0, time.UTC)
	}
	orders := []storage.Order{
		{ID: 1, CreatedAt: at(2025, 12, 31)},
		{ID: 2, CreatedAt: at(2026, 1, 1)},
		{ID: 3, CreatedAt: at(2026, 1, 15)},
		{ID: 4, CreatedAt: at(2026, 2, 1)},
		{ID: 5, CreatedAt: at(2026, 2, 2)},
	}

	ids := func(list []storage.Order) []int64 {
		out := make([]int64, len(list))
		for i := range list {
			out[i] = list[i].ID
		}
		return out
	}

	t.Run("unbounded returns everything", func(t *testing.T) {
		if got := filterOrdersByDate(orders, nil, nil); len(got) != len(orders) {
			t.Fatalf("expected all %d orders, got %v", len(orders), ids(got))
		}
	})

	t.Run("from and inclusive to", func(t *testing.T) {
		from, to, bad := parseExportRange([]string{"2026-01-01", "2026-02-01"})
		if bad != "" {
			t.Fatalf("unexpected bad arg %q", bad)
		}
		got := ids(filterOrdersByDate(orders, from, to))
		want := []int64{2, 3, 4} // order 4 is ON the end date and must be kept
		if len(got) != len(want) {
			t.Fatalf("expected %v, got %v", want, got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("expected %v, got %v", want, got)
			}
		}
	})

	t.Run("from only", func(t *testing.T) {
		from, _, _ := parseExportRange([]string{"2026-02-01"})
		got := ids(filterOrdersByDate(orders, from, nil))
		if len(got) != 2 || got[0] != 4 || got[1] != 5 {
			t.Fatalf("expected orders 4,5 got %v", got)
		}
	})
}
