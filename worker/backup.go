package worker

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

type BackupWorker struct {
	dbPath        string
	interval      time.Duration
	backupDir     string
	cliMissingLog bool
}

func NewBackupWorker(dbPath string, interval time.Duration) *BackupWorker {
	return &BackupWorker{
		dbPath:    dbPath,
		interval:  interval,
		backupDir: "backups",
	}
}

func (w *BackupWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("Backup Worker started")

	for {
		select {
		case <-ctx.Done():
			slog.Info("Backup Worker stopped")
			return
		case <-ticker.C:
			w.runBackup()
		}
	}
}

func (w *BackupWorker) runBackup() {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		if !w.cliMissingLog {
			slog.Warn("Backup skipped: sqlite3 CLI not found in PATH")
			w.cliMissingLog = true
		}
		return
	}

	timestamp := time.Now().Format("2006-01-02_15-04-05")
	backupPath := w.backupDir + "/shop_" + timestamp + ".db"

	// Ensure backup dir exists.
	if err := os.MkdirAll(w.backupDir, 0o755); err != nil {
		slog.Error("Error ensuring backup directory", "error", err, "dir", w.backupDir)
		return
	}

	// Create backup using sqlite3 CLI
	cmd := exec.Command("sqlite3", w.dbPath, ".backup "+backupPath)
	err := cmd.Run()
	if err != nil {
		slog.Error("Error creating backup", "error", err, "path", backupPath)
		return
	}

	slog.Info("Backup created", "path", backupPath)

	// Optional: Rotate old backups (keep last 7)
}
