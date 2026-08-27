package launcher

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shop_bot/internal/storage"
)

type fakeStarLister struct {
	pages   map[int][]StarTransaction
	offsets []int
	err     error
}

func (f *fakeStarLister) ListStarTransactions(_ context.Context, _ string, offset, _ int) ([]StarTransaction, error) {
	f.offsets = append(f.offsets, offset)
	if f.err != nil {
		return nil, f.err
	}
	return f.pages[offset], nil
}

func TestRunStarsReconcilePaginatesAndPrintsAggregateOnly(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shop.db")
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_stars,status,order_state,payment_state,fulfillment_state)
		VALUES (42,5,'pending','placed','pending','unfulfilled')`); err != nil {
		t.Fatal(err)
	}
	store := storage.NewSQLOrderStore(db)
	if err := store.UpdateOrderStatusWithPaymentFact(context.Background(), 1, "pending", "paid", storage.PaymentFact{
		Provider: "stars", ExternalID: "local-charge", PayerID: 42,
		AmountMinor: 5, Currency: "XTR", Scale: 0, OccurredAt: time.Unix(ingressProviderUnix, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte(fmt.Sprintf("BOT_TOKEN=%s\nDB_PATH=%s\n", testToken, dbPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeStarLister{pages: map[int][]StarTransaction{
		0: {{ID: "local-charge", Date: ingressProviderUnix, Amount: 5, Source: invoiceParty("1", 42)}},
		1: {},
	}}
	var out bytes.Buffer
	code := RunStarsReconcile(context.Background(), StarsReconcileOptions{
		EnvPath: env, BaseDir: dir, Out: &out, LookupEnv: func(string) (string, bool) { return "", false },
		Client: client, MaxRows: 2, PageSize: 1,
	})
	if code != 0 || !strings.Contains(out.String(), "matched=1") || !strings.Contains(out.String(), "complete=true") {
		t.Fatalf("code=%d output=%q offsets=%v", code, out.String(), client.offsets)
	}
	if strings.Contains(out.String(), "local-charge") || strings.Contains(out.String(), testToken) {
		t.Fatalf("sensitive identity leaked: %q", out.String())
	}
}

func TestRunStarsReconcileIgnoresNonInvoiceOutgoingUserRows(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shop.db")
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte(fmt.Sprintf("BOT_TOKEN=%s\nDB_PATH=%s\n", testToken, dbPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	receiver := &StarTransactionPartner{Type: "user", TransactionType: "gift_purchase"}
	receiver.User.ID = 42
	client := &fakeStarLister{pages: map[int][]StarTransaction{0: {{ID: "gift", Date: ingressProviderUnix, Amount: -5, Receiver: receiver}}}}
	var out bytes.Buffer
	code := RunStarsReconcile(context.Background(), StarsReconcileOptions{EnvPath: env, BaseDir: dir, Out: &out, LookupEnv: func(string) (string, bool) { return "", false }, Client: client, MaxRows: 10, PageSize: 10})
	if code != 0 || !strings.Contains(out.String(), "rows=0") || !strings.Contains(out.String(), "provider_only=0") {
		t.Fatalf("code=%d output=%q", code, out.String())
	}
}

func TestRunStarsReconcileProbesExactCapAndAllowsLargerExplicitWindow(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shop.db")
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewSQLOrderStore(db)
	for i := int64(1); i <= 2; i++ {
		res, err := db.Conn().Exec(`INSERT INTO orders
			(user_id,total_stars,status,order_state,payment_state,fulfillment_state)
			VALUES (?,5,'pending','placed','pending','unfulfilled')`, 40+i)
		if err != nil {
			t.Fatal(err)
		}
		orderID, _ := res.LastInsertId()
		if err := store.UpdateOrderStatusWithPaymentFact(context.Background(), orderID, "pending", "paid", storage.PaymentFact{
			Provider: "stars", ExternalID: fmt.Sprintf("charge-%d", i), PayerID: 40 + i,
			AmountMinor: 5, Currency: "XTR", Scale: 0, OccurredAt: time.Unix(ingressProviderUnix+i-1, 0).UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte(fmt.Sprintf("BOT_TOKEN=%s\nDB_PATH=%s\n", testToken, dbPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := []StarTransaction{
		{ID: "charge-1", Date: ingressProviderUnix, Amount: 5, Source: invoiceParty("1", 41)},
		{ID: "charge-2", Date: ingressProviderUnix + 1, Amount: 5, Source: invoiceParty("2", 42)},
	}
	for _, tc := range []struct {
		name      string
		probe     []StarTransaction
		wantCode  int
		wantState string
	}{
		{name: "exact cap", wantState: "complete=true"},
		{name: "row beyond cap", probe: []StarTransaction{{ID: "third"}}, wantCode: 1, wantState: "complete=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeStarLister{pages: map[int][]StarTransaction{0: rows, 2: tc.probe}}
			var out bytes.Buffer
			code := RunStarsReconcileCLI(context.Background(), []string{"--max-rows", "2", "--page-size", "2"}, StarsReconcileOptions{
				EnvPath: env, BaseDir: dir, Out: &out, LookupEnv: func(string) (string, bool) { return "", false },
				Client: client, MaxRows: 500, PageSize: 100,
			})
			if code != tc.wantCode || !strings.Contains(out.String(), tc.wantState) ||
				!strings.Contains(out.String(), "matched=2") {
				t.Fatalf("code=%d output=%q offsets=%v", code, out.String(), client.offsets)
			}
			if len(client.offsets) != 2 || client.offsets[1] != 2 {
				t.Fatalf("probe offsets=%v", client.offsets)
			}
		})
	}
}
