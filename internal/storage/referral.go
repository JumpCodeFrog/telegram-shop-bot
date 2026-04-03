package storage

import (
	"context"
	"database/sql"
	"time"
)

type ReferralStore struct {
	db *sql.DB
}

func NewReferralStore(db *sql.DB) *ReferralStore {
	return &ReferralStore{db: db}
}

func (s *ReferralStore) GetStats(ctx context.Context, userID int64) (*ReferralStats, error) {
	var st ReferralStats
	query := `SELECT user_id, total_referrals, total_earned FROM referral_stats WHERE user_id = ?`
	err := s.db.QueryRowContext(ctx, query, userID).Scan(&st.UserID, &st.TotalReferrals, &st.TotalEarned)
	if err == sql.ErrNoRows {
		return &ReferralStats{UserID: userID}, nil
	}
	return &st, err
}

func (s *ReferralStore) GetLeaderboard(ctx context.Context, limit int) ([]User, error) {
	query := `
		SELECT u.id, u.telegram_id, u.username, u.first_name, rs.total_referrals 
		FROM referral_stats rs
		JOIN users u ON rs.user_id = u.id
		ORDER BY rs.total_referrals DESC
		LIMIT ?
	`
	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var total int
		if err := rows.Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &total); err != nil {
			return nil, err
		}
		// We can reuse User struct or create a specific one
		users = append(users, u)
	}
	return users, nil
}

func (s *ReferralStore) SetReferrer(ctx context.Context, userID, referrerID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET referred_by = ? WHERE id = ? AND referred_by IS NULL`, referrerID, userID)
	if err != nil {
		return err
	}
	
	// Initialize or update stats for referrer
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO referral_stats (user_id, total_referrals) VALUES (?, 1)
		ON CONFLICT(user_id) DO UPDATE SET total_referrals = total_referrals + 1
	`, referrerID)
	
	return err
}

func (s *ReferralStore) UpdateReferralCode(ctx context.Context, userID int64, code string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET referral_code = ?, referral_code_expires_at = ? WHERE id = ?`,
		code, expiresAt, userID,
	)
	return err
}

func (s *ReferralStore) GetUserByReferralCode(ctx context.Context, code string) (*User, error) {
	var u User
	query := `SELECT id, telegram_id, username, first_name FROM users WHERE referral_code = ?`
	err := s.db.QueryRowContext(ctx, query, code).Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &u, err
}

type ReferralStats struct {
	UserID         int64
	TotalReferrals int
	TotalEarned    float64
}
