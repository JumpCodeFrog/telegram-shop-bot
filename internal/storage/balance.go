package storage

import (
	"context"
	"database/sql"
	"fmt"
)

type BalanceStore struct {
	db *sql.DB
}

func NewBalanceStore(db *sql.DB) *BalanceStore {
	return &BalanceStore{db: db}
}

func (s *BalanceStore) AddTransaction(ctx context.Context, tx *Transaction) error {
	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer dbTx.Rollback()

	// 1. Insert transaction
	query := `INSERT INTO balance_txs (user_id, amount_usd, type, ref_id) VALUES (?, ?, ?, ?)`
	_, err = dbTx.ExecContext(ctx, query, tx.UserID, tx.AmountUSD, tx.Type, tx.RefID)
	if err != nil {
		return fmt.Errorf("insert balance_tx: %w", err)
	}

	// 2. Update user balance
	_, err = dbTx.ExecContext(ctx, `UPDATE users SET balance_usd = balance_usd + ? WHERE id = ?`, tx.AmountUSD, tx.UserID)
	if err != nil {
		return fmt.Errorf("update user balance: %w", err)
	}

	return dbTx.Commit()
}

func (s *BalanceStore) GetHistory(ctx context.Context, userID int64, limit int) ([]Transaction, error) {
	query := `SELECT id, user_id, amount_usd, type, ref_id, created_at FROM balance_txs WHERE user_id = ? ORDER BY created_at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txs []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.ID, &t.UserID, &t.AmountUSD, &t.Type, &t.RefID, &t.CreatedAt); err != nil {
			return nil, err
		}
		txs = append(txs, t)
	}
	return txs, nil
}
