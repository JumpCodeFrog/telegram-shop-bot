package storage

import (
	"context"
	"database/sql"
)

type LoyaltyStoreImpl struct {
	db *sql.DB
}

func NewLoyaltyStore(db *sql.DB) *LoyaltyStoreImpl {
	return &LoyaltyStoreImpl{db: db}
}

func (s *LoyaltyStoreImpl) AddPoints(ctx context.Context, userID int64, pts int, reason string, refID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `INSERT INTO loyalty_txs (user_id, pts, reason, ref_id) VALUES (?, ?, ?, ?)`, userID, pts, reason, refID)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `UPDATE users SET loyalty_pts = loyalty_pts + ? WHERE id = ?`, pts, userID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *LoyaltyStoreImpl) GetPoints(ctx context.Context, userID int64) (int, string, error) {
	var pts int
	var level string
	err := s.db.QueryRowContext(ctx, `SELECT loyalty_pts, loyalty_level FROM users WHERE id = ?`, userID).Scan(&pts, &level)
	return pts, level, err
}

func (s *LoyaltyStoreImpl) UpdateLevel(ctx context.Context, userID int64, level string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET loyalty_level = ? WHERE id = ?`, level, userID)
	return err
}

func (s *LoyaltyStoreImpl) AddAchievement(ctx context.Context, userID int64, achievementID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO user_achievements (user_id, achievement_id) VALUES (?, ?)`, userID, achievementID)
	return err
}

func (s *LoyaltyStoreImpl) HasAchievement(ctx context.Context, userID int64, code string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM user_achievements ua 
			JOIN achievements a ON ua.achievement_id = a.id 
			WHERE ua.user_id = ? AND a.code = ?
		)`, userID, code).Scan(&exists)
	return exists, err
}
