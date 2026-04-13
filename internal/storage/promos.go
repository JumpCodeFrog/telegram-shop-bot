package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// SQLPromoStore implements PromoStore using a *sql.DB connection.
type SQLPromoStore struct {
	db *sql.DB
}

// NewSQLPromoStore creates a new SQLPromoStore from the given DB.
func NewSQLPromoStore(d *DB) *SQLPromoStore {
	return &SQLPromoStore{db: d.Conn()}
}

// GetPromoByCode returns an active, valid promo code. Returns ErrNotFound if
// no matching code exists or it has expired / been exhausted.
func (s *SQLPromoStore) GetPromoByCode(ctx context.Context, code string) (*PromoCode, error) {
	var p PromoCode
	var expiresAt sql.NullTime
	var categoryID sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, code, discount, max_uses, used_count, expires_at, is_active, created_at, category_id
		 FROM promo_codes
		 WHERE code = ? AND is_active = 1
		   AND (expires_at IS NULL OR expires_at > datetime('now'))
		   AND (max_uses = 0 OR used_count < max_uses)`,
		code,
	).Scan(&p.ID, &p.Code, &p.Discount, &p.MaxUses, &p.UsedCount,
		&expiresAt, &p.IsActive, &p.CreatedAt, &categoryID)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("promo store: get promo: %w", err)
	}
	if expiresAt.Valid {
		t := expiresAt.Time
		p.ExpiresAt = &t
	}
	if categoryID.Valid {
		p.CategoryID = &categoryID.Int64
	}
	return &p, nil
}

// UsePromo records that a user has used the promo code for an order and
// increments the usage counter.
func (s *SQLPromoStore) UsePromo(ctx context.Context, promoID, userID, orderID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("promo store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO promo_usages (promo_id, user_id, order_id) VALUES (?, ?, ?)`,
		promoID, userID, orderID,
	); err != nil {
		return fmt.Errorf("promo store: insert promo usage: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE promo_codes SET used_count = used_count + 1 WHERE id = ?`, promoID,
	); err != nil {
		return fmt.Errorf("promo store: increment used_count: %w", err)
	}

	return tx.Commit()
}

// HasUserUsedPromo returns true if the user has already used the given promo.
func (s *SQLPromoStore) HasUserUsedPromo(ctx context.Context, promoID, userID int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM promo_usages WHERE promo_id = ? AND user_id = ?`,
		promoID, userID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("promo store: check user usage: %w", err)
	}
	return count > 0, nil
}

// CreatePromo inserts a new promo code and returns its ID.
func (s *SQLPromoStore) CreatePromo(ctx context.Context, p *PromoCode) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO promo_codes (code, discount, max_uses, expires_at, category_id) VALUES (?, ?, ?, ?, ?)`,
		p.Code, p.Discount, p.MaxUses, p.ExpiresAt, p.CategoryID,
	)
	if err != nil {
		return 0, fmt.Errorf("promo store: create promo: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("promo store: last insert id: %w", err)
	}
	return id, nil
}

// ListPromos returns all active promo codes ordered by creation date.
func (s *SQLPromoStore) ListPromos(ctx context.Context) ([]PromoCode, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, code, discount, max_uses, used_count, expires_at, is_active, created_at, category_id
		 FROM promo_codes WHERE is_active = 1 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("promo store: list promos: %w", err)
	}
	defer rows.Close()

	var promos []PromoCode
	for rows.Next() {
		var p PromoCode
		var expiresAt sql.NullTime
		var categoryID sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Code, &p.Discount, &p.MaxUses, &p.UsedCount,
			&expiresAt, &p.IsActive, &p.CreatedAt, &categoryID); err != nil {
			return nil, fmt.Errorf("promo store: scan promo: %w", err)
		}
		if expiresAt.Valid {
			t := expiresAt.Time
			p.ExpiresAt = &t
		}
		if categoryID.Valid {
			p.CategoryID = &categoryID.Int64
		}
		promos = append(promos, p)
	}
	return promos, rows.Err()
}

// DeactivatePromo marks a promo code as inactive.
func (s *SQLPromoStore) DeactivatePromo(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE promo_codes SET is_active = 0 WHERE id = ?`, id,
	); err != nil {
		return fmt.Errorf("promo store: deactivate promo: %w", err)
	}
	return nil
}
