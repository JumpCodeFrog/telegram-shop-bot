package worker

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// backupKeep is how many newest backups rotation preserves.
const backupKeep = 7

type BackupWorker struct {
	db        *sql.DB
	interval  time.Duration
	backupDir string
	keep      int
}

func NewBackupWorker(db *sql.DB, interval time.Duration) *BackupWorker {
	return &BackupWorker{
		db:        db,
		interval:  interval,
		backupDir: "backups",
		keep:      backupKeep,
	}
}

func (w *BackupWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("Backup Worker started", "interval", w.interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Backup Worker stopped")
			return
		case <-ticker.C:
			w.runBackup(ctx)
		}
	}
}

// runBackup snapshots the live database via VACUUM INTO on the existing
// connection pool (no external sqlite3 CLI required) and rotates old files.
func (w *BackupWorker) runBackup(ctx context.Context) {
	if err := os.MkdirAll(w.backupDir, 0o755); err != nil {
		slog.Error("Error ensuring backup directory", "error", err, "dir", w.backupDir)
		return
	}

	backupPath := filepath.Join(w.backupDir, "shop_"+time.Now().Format("20060102_150405")+".db")
	if _, err := w.db.ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
		slog.Error("Error creating backup", "error", err, "path", backupPath)
		return
	}
	slog.Info("Backup created", "path", backupPath)

	w.rotate()
}

// rotate keeps the w.keep newest shop_*.db backups and removes the rest.
// Timestamped names sort chronologically, so a plain string sort suffices.
func (w *BackupWorker) rotate() {
	entries, err := os.ReadDir(w.backupDir)
	if err != nil {
		slog.Error("Backup rotation: read dir failed", "error", err, "dir", w.backupDir)
		return
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name := e.Name(); strings.HasPrefix(name, "shop_") && strings.HasSuffix(name, ".db") {
			names = append(names, name)
		}
	}
	if len(names) <= w.keep {
		return
	}

	sort.Strings(names)
	for _, name := range names[:len(names)-w.keep] {
		path := filepath.Join(w.backupDir, name)
		if err := os.Remove(path); err != nil {
			slog.Error("Backup rotation: remove failed", "error", err, "path", path)
			continue
		}
		slog.Info("Old backup removed", "path", path)
	}
}
