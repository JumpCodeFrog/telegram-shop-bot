package storage

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var (
	ErrNotFound                  = errors.New("storage: resource not found")
	ErrOrderStatusConflict       = errors.New("storage: order status conflict")
	ErrPaymentIdentityConflict   = errors.New("storage: payment identity belongs to another order")
	ErrPaymentNeedsReview        = errors.New("storage: payment requires manual review")
	ErrRefundExceedsPayment      = errors.New("storage: refund exceeds captured payment")
	ErrInvalidMoney              = errors.New("storage: invalid money value")
	ErrPaymentReceiptMismatch    = errors.New("storage: payment receipt mismatch")
	ErrPaymentReviewConflict     = errors.New("storage: payment review targets changed")
	ErrInvalidSubscriptionCart   = errors.New("storage: subscription must be the only cart item with quantity one")
	ErrSubscriptionOrderConflict = errors.New("storage: subscription already active or awaiting payment")
	ErrSubscriptionEntitlement   = errors.New("storage: subscription entitlement write failed")
	ErrProductOutOfStock         = errors.New("storage: product out of stock")
	ErrEmptyCart                 = errors.New("storage: cart is empty")
)

// DB wraps *sql.DB and provides storage operations.
type DB struct {
	conn *sql.DB
}

// New opens a SQLite database at dbPath, enables WAL/busy_timeout/foreign_keys
// on every pooled connection via DSN pragmas, and runs migrations. Returns an
// initialised *DB or an error.
func New(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, fmt.Errorf("storage: open db: %w", err)
	}

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("storage: ping db: %w", err)
	}

	conn.SetMaxOpenConns(25)
	conn.SetMaxIdleConns(10)
	conn.SetConnMaxLifetime(5 * time.Minute)

	db := &DB{conn: conn}

	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("storage: migrate: %w", err)
	}

	return db, nil
}

// OpenReadOnly opens an existing, fully migrated database without running
// migrations or creating WAL sidecars. Operator inspection commands use this
// boundary so a typo or an old schema cannot be mutated by a read operation.
func OpenReadOnly(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", readOnlyDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("storage: open read-only db: %w", err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("storage: ping read-only db: %w", err)
	}
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = '017_commerce_ledger.sql'`).Scan(&count); err != nil || count != 1 {
		conn.Close()
		return nil, fmt.Errorf("storage: commerce ledger migration is required")
	}
	return &DB{conn: conn}, nil
}

// OpenReadWriteExisting opens an already-migrated database without creating a
// new file or applying migrations. Explicit operator resolution commands use
// this boundary after a read-only preview.
func OpenReadWriteExisting(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", existingReadWriteDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("storage: open existing db: %w", err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("storage: ping existing db: %w", err)
	}
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = '017_commerce_ledger.sql'`).Scan(&count); err != nil || count != 1 {
		conn.Close()
		return nil, fmt.Errorf("storage: commerce ledger migration is required")
	}
	return &DB{conn: conn}, nil
}

// dsn appends connection pragmas to dbPath. Pragmas set through the DSN are
// applied by the driver to every connection in the pool, unlike a one-off
// Exec which only configures the single connection it happens to run on.
func dsn(dbPath string) string {
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return dbPath + sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
}

func readOnlyDSN(dbPath string) string {
	// modernc/sqlite only passes URI mode flags to sqlite3_open_v2 when the
	// DSN begins with file:. Encode the filesystem path so spaces, #, ?, and
	// non-ASCII bytes cannot become URI syntax.
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		abs = dbPath
	}
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = query.Encode()
	return u.String()
}

func existingReadWriteDSN(dbPath string) string {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		abs = dbPath
	}
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	query := u.Query()
	query.Set("mode", "rw")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = query.Encode()
	return u.String()
}

// Conn returns the underlying *sql.DB for use by store implementations.
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// migrate applies pending SQL migrations in order using the embedded FS,
// tracking applied versions in the schema_migrations table.
func (db *DB) migrate() error {
	if _, err := db.conn.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY)`,
	); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version := entry.Name()

		sqlBytes, err := migrationsFS.ReadFile("migrations/" + version)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", version, err)
		}

		if err := db.applyMigration(version, string(sqlBytes)); err != nil {
			return err
		}
	}

	return nil
}

// applyMigration commits the schema change and its version marker together.
// A failed statement therefore cannot leave an untracked half-migration.
func (db *DB) applyMigration(version, statements string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version,
	).Scan(&count); err != nil {
		return fmt.Errorf("check migration %s: %w", version, err)
	}
	if count > 0 {
		return nil
	}
	if _, err := tx.Exec(statements); err != nil {
		return fmt.Errorf("exec migration %s: %w", version, err)
	}
	if _, err := tx.Exec(
		"INSERT INTO schema_migrations (version) VALUES (?)", version,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", version, err)
	}
	return nil
}
