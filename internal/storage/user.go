package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type SQLUserStore struct {
	db *sql.DB
}

func NewUserStore(db *sql.DB) *SQLUserStore {
	return &SQLUserStore{db: db}
}

func (s *SQLUserStore) Upsert(ctx context.Context, user *User) error {
	query := `
		INSERT INTO users (telegram_id, username, first_name, language_code, is_premium)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(telegram_id) DO UPDATE SET
			username = excluded.username,
			first_name = excluded.first_name,
			language_code = CASE WHEN users.language_code IS NULL THEN excluded.language_code ELSE users.language_code END,
			is_premium = excluded.is_premium
		RETURNING id, balance_usd, loyalty_pts, loyalty_level, referral_code, referred_by, created_at
	`
	err := s.db.QueryRowContext(ctx, query,
		user.TelegramID, user.Username, user.FirstName, user.LanguageCode, user.IsPremium,
	).Scan(
		&user.ID, &user.BalanceUSD, &user.LoyaltyPts, &user.LoyaltyLevel, &user.ReferralCode, &user.ReferredBy, &user.CreatedAt,
	)

	if err != nil {
		return fmt.Errorf("upsert user: %w", err)
	}
	return nil
}

func (s *SQLUserStore) GetByTelegramID(ctx context.Context, telegramID int64) (*User, error) {
	user := &User{TelegramID: telegramID}
	query := `SELECT id, username, first_name, language_code, is_premium, balance_usd, loyalty_pts, loyalty_level, referral_code, referred_by, created_at FROM users WHERE telegram_id = ?`
	err := s.db.QueryRowContext(ctx, query, telegramID).Scan(
		&user.ID, &user.Username, &user.FirstName, &user.LanguageCode, &user.IsPremium, &user.BalanceUSD, &user.LoyaltyPts, &user.LoyaltyLevel, &user.ReferralCode, &user.ReferredBy, &user.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by telegram id: %w", err)
	}
	return user, nil
}

// GetNewUsersWithoutOrders returns users created between minAge and maxAge ago who have no orders.
// Used by OnboardingWorker to send welcome messages.
func (s *SQLUserStore) GetNewUsersWithoutOrders(ctx context.Context, minAge, maxAge time.Duration) ([]User, error) {
	now := time.Now()
	from := now.Add(-maxAge)
	to := now.Add(-minAge)

	query := `
		SELECT id, telegram_id, username, first_name, language_code, is_premium,
		       balance_usd, loyalty_pts, loyalty_level, referral_code, referred_by, created_at
		FROM users
		WHERE created_at >= ? AND created_at < ?
		  AND id NOT IN (SELECT DISTINCT user_id FROM orders)
	`
	rows, err := s.db.QueryContext(ctx, query, from, to)
	if err != nil {
		return nil, fmt.Errorf("get new users without orders: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(
			&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &u.LanguageCode, &u.IsPremium,
			&u.BalanceUSD, &u.LoyaltyPts, &u.LoyaltyLevel, &u.ReferralCode, &u.ReferredBy, &u.CreatedAt,
		); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
