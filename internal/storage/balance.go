package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SQLBalanceStore implements BalanceStore against users.balance_usd with the
// balance_txs audit trail. Users are addressed by telegram_id (see the
// BalanceStore interface doc).
type SQLBalanceStore struct {
	db *sql.DB
}

func NewSQLBalanceStore(db *sql.DB) *SQLBalanceStore {
	return &SQLBalanceStore{db: db}
}

func (s *SQLBalanceStore) GetBalance(ctx context.Context, userID int64) (float64, error) {
	var balance float64
	if err := s.db.QueryRowContext(ctx,
		`SELECT balance_usd FROM users WHERE telegram_id = ?`, userID).Scan(&balance); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, fmt.Errorf("balance store: get balance: %w", err)
	}
	return balance, nil
}

// OrderBalanceNet sums the order's order_payment and settlement_failed
// audit rows (the deterministic type strings written by the shop balance
// rail). The user is addressed by telegram id like the rest of the store;
// the rows themselves key on the internal users.id.
func (s *SQLBalanceStore) OrderBalanceNet(ctx context.Context, userID, orderID int64) (float64, error) {
	var net float64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_usd), 0) FROM balance_txs
		 WHERE user_id = (SELECT id FROM users WHERE telegram_id = ?)
		   AND type IN (?, ?)`,
		userID, fmt.Sprintf("order_payment:%d", orderID),
		fmt.Sprintf("settlement_failed:%d", orderID)).Scan(&net); err != nil {
		return 0, fmt.Errorf("balance store: order balance net: %w", err)
	}
	return net, nil
}

// AdjustBalance moves the balance by deltaUSD and appends the audit row in
// ONE transaction. Money math happens in integer cents at the boundary:
// round(delta*100) — a sub-cent or zero delta is rejected as meaningless,
// NaN/Inf can never enter the ledger, and every stored balance stays snapped
// to exact cents (ROUND(...*100) integer arithmetic) so binary float drift
// can never accumulate or defeat the non-negativity guard.
func (s *SQLBalanceStore) AdjustBalance(ctx context.Context, userID int64, deltaUSD float64, reason string, adminID int64) (float64, error) {
	if math.IsNaN(deltaUSD) || math.IsInf(deltaUSD, 0) {
		return 0, ErrInvalidMoney
	}
	deltaCents := int64(math.Round(deltaUSD * 100))
	if deltaCents == 0 {
		return 0, ErrInvalidMoney
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, ErrInvalidMoney
	}
	var refID any
	if adminID > 0 {
		refID = strconv.FormatInt(adminID, 10)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("balance store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The non-negativity guard rides the UPDATE itself, so the check and the
	// mutation are one atomic statement: zero rows means either the user is
	// unknown or the debit would overdraw — disambiguated inside the tx.
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET balance_usd = (ROUND(balance_usd * 100) + ?) / 100.0
		 WHERE telegram_id = ? AND ROUND(balance_usd * 100) + ? >= 0`,
		deltaCents, userID, deltaCents)
	if err != nil {
		return 0, fmt.Errorf("balance store: adjust balance: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("balance store: adjust rows affected: %w", err)
	}
	if affected == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM users WHERE telegram_id = ?)`, userID).Scan(&exists); err != nil {
			return 0, fmt.Errorf("balance store: check user existence: %w", err)
		}
		if exists == 0 {
			return 0, ErrNotFound
		}
		return 0, ErrInsufficientFunds
	}

	// balance_txs.user_id references the internal users.id — resolve it from
	// the telegram id in the same tx so the FK always holds.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO balance_txs (user_id, amount_usd, type, ref_id)
		 SELECT id, ?, ?, ? FROM users WHERE telegram_id = ?`,
		float64(deltaCents)/100.0, reason, refID, userID); err != nil {
		return 0, fmt.Errorf("balance store: append balance tx: %w", err)
	}

	var newBalance float64
	if err := tx.QueryRowContext(ctx,
		`SELECT balance_usd FROM users WHERE telegram_id = ?`, userID).Scan(&newBalance); err != nil {
		return 0, fmt.Errorf("balance store: read post-tx balance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("balance store: commit adjustment: %w", err)
	}
	return newBalance, nil
}
