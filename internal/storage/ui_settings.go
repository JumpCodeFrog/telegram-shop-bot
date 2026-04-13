package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// UISettingsStore persists and retrieves UI configuration such as button styles.
type UISettingsStore interface {
	// GetButtonStyle returns the configured style for a button key,
	// or "" if the key is not found (meaning use the default style).
	GetButtonStyle(ctx context.Context, key string) (string, error)
	// SetButtonStyle sets (or replaces) the style for a button key.
	// Pass style="" to reset to the default.
	SetButtonStyle(ctx context.Context, key, style string) error
	// ListButtonStyles returns all currently configured key→style pairs.
	ListButtonStyles(ctx context.Context) (map[string]string, error)
}

// SQLUISettingsStore implements UISettingsStore on top of SQLite.
type SQLUISettingsStore struct {
	db *sql.DB
}

// NewSQLUISettingsStore creates a store backed by the given *sql.DB.
func NewSQLUISettingsStore(db *sql.DB) *SQLUISettingsStore {
	return &SQLUISettingsStore{db: db}
}

func (s *SQLUISettingsStore) GetButtonStyle(ctx context.Context, key string) (string, error) {
	var style string
	err := s.db.QueryRowContext(ctx,
		`SELECT style FROM button_styles WHERE key = ?`, key,
	).Scan(&style)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("ui_settings: get button style %q: %w", key, err)
	}
	return style, nil
}

func (s *SQLUISettingsStore) SetButtonStyle(ctx context.Context, key, style string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO button_styles (key, style) VALUES (?, ?)
         ON CONFLICT(key) DO UPDATE SET style = excluded.style`,
		key, style,
	)
	if err != nil {
		return fmt.Errorf("ui_settings: set button style %q=%q: %w", key, style, err)
	}
	return nil
}

func (s *SQLUISettingsStore) ListButtonStyles(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, style FROM button_styles`)
	if err != nil {
		return nil, fmt.Errorf("ui_settings: list button styles: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var key, style string
		if err := rows.Scan(&key, &style); err != nil {
			return nil, fmt.Errorf("ui_settings: scan row: %w", err)
		}
		out[key] = style
	}
	return out, rows.Err()
}
