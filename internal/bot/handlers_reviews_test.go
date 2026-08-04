package bot

import (
	"errors"
	"fmt"
	"testing"

	"shop_bot/internal/storage"
)

func TestReviewInviteKeyboard_FiveRatingButtons(t *testing.T) {
	t.Parallel()

	kb := reviewInviteKeyboard(42)

	if len(kb) != 1 {
		t.Fatalf("expected a single row, got %d", len(kb))
	}
	if len(kb[0]) != 5 {
		t.Fatalf("expected 5 rating buttons, got %d", len(kb[0]))
	}
	for i, btn := range kb[0] {
		wantData := fmt.Sprintf("review:42:%d", i+1)
		if btn.CallbackData != wantData {
			t.Errorf("button %d callback = %q, want %q", i, btn.CallbackData, wantData)
		}
		wantText := fmt.Sprintf("%d⭐", i+1)
		if btn.Text != wantText {
			t.Errorf("button %d text = %q, want %q", i, btn.Text, wantText)
		}
	}
}

func TestReviewableProducts_AllowsOwnDeliveredOrder(t *testing.T) {
	t.Parallel()

	order := &storage.Order{
		ID:     7,
		UserID: 42,
		Status: storage.OrderStatusDelivered,
		Items: []storage.OrderItem{
			{ProductID: 10},
			{ProductID: 11},
			{ProductID: 10}, // duplicate line item collapses to one review target
			{ProductID: 0},  // deleted product is skipped
		},
	}

	ids, err := reviewableProducts(order, 42)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(ids) != 2 || ids[0] != 10 || ids[1] != 11 {
		t.Fatalf("expected [10 11], got %v", ids)
	}
}

func TestReviewableProducts_RejectsForeignOrder(t *testing.T) {
	t.Parallel()

	order := &storage.Order{
		ID:     7,
		UserID: 99,
		Status: storage.OrderStatusDelivered,
		Items:  []storage.OrderItem{{ProductID: 10}},
	}

	if _, err := reviewableProducts(order, 42); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for foreign order, got %v", err)
	}
}

func TestReviewableProducts_RejectsUndeliveredOrder(t *testing.T) {
	t.Parallel()

	for _, status := range []string{storage.OrderStatusPending, storage.OrderStatusPaid, storage.OrderStatusCancelled} {
		order := &storage.Order{
			ID:     7,
			UserID: 42,
			Status: status,
			Items:  []storage.OrderItem{{ProductID: 10}},
		}
		if _, err := reviewableProducts(order, 42); !errors.Is(err, storage.ErrOrderStatusConflict) {
			t.Fatalf("status %q: expected ErrOrderStatusConflict, got %v", status, err)
		}
	}
}

func TestReviewableProducts_RejectsNilOrder(t *testing.T) {
	t.Parallel()

	if _, err := reviewableProducts(nil, 42); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for nil order, got %v", err)
	}
}

func TestFormatRatingLine_RoundsToOneDecimal(t *testing.T) {
	t.Parallel()

	b := newTextBot(t)

	if got := b.formatRatingLine("en", 4.666, 12); got != "⭐ 4.7 (12)" {
		t.Fatalf("rating line = %q, want %q", got, "⭐ 4.7 (12)")
	}
	// Empty language falls back to en instead of leaking the raw key.
	if got := b.formatRatingLine("", 5, 1); got != "⭐ 5.0 (1)" {
		t.Fatalf("rating line with empty lang = %q, want %q", got, "⭐ 5.0 (1)")
	}
}

func TestInsertReviewsRow_KeepsNavRowLast(t *testing.T) {
	t.Parallel()

	kb := StyledKeyboard{
		{Btn("add", "cart:add:1")},
		{Btn("back", "back:category:1"), Btn("menu", "back:menu")},
	}

	got := insertReviewsRow(kb, Btn("reviews", "review:list:1"))

	if len(got) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(got))
	}
	if got[1][0].CallbackData != "review:list:1" {
		t.Fatalf("expected reviews row before nav row, got %q", got[1][0].CallbackData)
	}
	if got[2][1].CallbackData != "back:menu" {
		t.Fatalf("expected nav row to stay last, got %q", got[2][1].CallbackData)
	}
}
