package payment

import (
	"testing"

	"shop_bot/internal/storage"
)

func TestBuildDescription_Empty(t *testing.T) {
	desc := buildDescription(nil)
	if desc != "Оплата заказа" {
		t.Fatalf("expected fallback description, got %q", desc)
	}
}

func TestBuildDescription_SingleItem(t *testing.T) {
	items := []storage.OrderItem{
		{ProductID: 5, ProductName: "Футболка", Quantity: 2},
	}
	desc := buildDescription(items)
	want := "Футболка × 2"
	if desc != want {
		t.Fatalf("got %q, want %q", desc, want)
	}
}

func TestBuildDescription_MultipleItems(t *testing.T) {
	items := []storage.OrderItem{
		{ProductID: 1, ProductName: "Футболка", Quantity: 3},
		{ProductID: 7, ProductName: "Кепка", Quantity: 1},
	}
	desc := buildDescription(items)
	want := "Футболка × 3, ещё 1 товар"
	if desc != want {
		t.Fatalf("got %q, want %q", desc, want)
	}
}

func TestBuildDescription_FallsBackToProductID(t *testing.T) {
	items := []storage.OrderItem{
		{ProductID: 7, Quantity: 1},
	}
	desc := buildDescription(items)
	want := "Товар #7 × 1"
	if desc != want {
		t.Fatalf("got %q, want %q", desc, want)
	}
}

func TestInvoiceStartParameter(t *testing.T) {
	if got := invoiceStartParameter(42); got != "order-42" {
		t.Fatalf("invoiceStartParameter = %q", got)
	}
}
