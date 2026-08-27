package shop

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"shop_bot/internal/storage"
)

func TestLegacyCryptoBotAliasExactReplayStaysSettled(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "alias.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	res, err := db.Conn().Exec(`INSERT INTO orders
		(user_id,total_usd,total_stars,payment_method,payment_id,status,order_state,payment_state,fulfillment_state)
		VALUES (42,12.34,0,'cryptobot','legacy-charge','paid','placed','settled','unfulfilled')`)
	if err != nil {
		t.Fatal(err)
	}
	orderID, _ := res.LastInsertId()
	attempt, err := db.Conn().Exec(`INSERT INTO payment_attempts
		(order_id,provider,external_id,amount_minor,currency,scale,status)
		VALUES (?,'crypto','legacy-charge',1234,'USDT',2,'succeeded')`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	attemptID, _ := attempt.LastInsertId()
	if _, err := db.Conn().Exec(`INSERT INTO payment_events
		(order_id,payment_attempt_id,provider,event_kind,external_id,amount_minor,currency,scale,disposition)
		VALUES (?,?,'crypto','captured','legacy-charge',1234,'USDT',2,'settled')`, orderID, attemptID); err != nil {
		t.Fatal(err)
	}

	svc := NewOrderService(storage.NewSQLOrderStore(db), nil, nil, PaymentDeps{}, slog.Default())
	_, got := svc.ConfirmPaymentReceipt(ctx, PaymentReceipt{
		OrderID: orderID, Provider: storage.PaymentMethodCrypto, ExternalID: "legacy-charge",
		Currency: "USDT", AmountMinor: 1234, Scale: 2,
	})
	if !errors.Is(got, storage.ErrOrderStatusConflict) {
		t.Fatalf("exact replay error=%v", got)
	}
	var state string
	var anomalies int
	if err := db.Conn().QueryRow(`SELECT payment_state FROM orders WHERE id=?`, orderID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM payment_anomalies WHERE proposed_order_id=?`, orderID).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	if state != storage.PaymentStateSettled || anomalies != 0 {
		t.Fatalf("state=%s anomalies=%d", state, anomalies)
	}
}
