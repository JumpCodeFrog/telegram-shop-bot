package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/joho/godotenv"

	"shop_bot/internal/storage"
)

func envValue(env map[string]string, key, fallback string) string {
	if value, ok := env[key]; ok && value != "" {
		return value
	}
	return fallback
}

func main() {
	_ = godotenv.Load()
	env := map[string]string{
		"BOT_TOKEN": os.Getenv("BOT_TOKEN"), "CRYPTOBOT_TOKEN": os.Getenv("CRYPTOBOT_TOKEN"),
		"DB_PATH": os.Getenv("DB_PATH"), "REDIS_ADDR": os.Getenv("REDIS_ADDR"),
		"WEBHOOK_URL": os.Getenv("WEBHOOK_URL"), "TELEGRAM_WEBHOOK_SECRET": os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		"APP_ENV": os.Getenv("APP_ENV"), "LOG_LEVEL": os.Getenv("LOG_LEVEL"),
	}
	dbPath := envValue(env, "DB_PATH", "data/shop.db")
	redisAddr := envValue(env, "REDIS_ADDR", "localhost:6379")
	fail, warnings := false, false
	check := func(ok bool, label, detail string) {
		status := "OK"
		if !ok {
			status, fail = "FAIL", true
		}
		fmt.Printf("[%s] %s", status, label)
		if detail != "" {
			fmt.Printf(": %s", detail)
		}
		fmt.Println()
	}
	warn := func(label, detail string) { warnings = true; fmt.Printf("[WARN] %s: %s\n", label, detail) }
	fmt.Println("Telegram Shop Bot Preflight")
	fmt.Println()
	check(env["BOT_TOKEN"] != "", "BOT_TOKEN present", "")
	if env["CRYPTOBOT_TOKEN"] == "" {
		warn("CRYPTOBOT_TOKEN", "not set; crypto checkout disabled")
	} else {
		check(true, "CRYPTOBOT_TOKEN present", "crypto checkout enabled")
	}
	check(true, "DB_PATH", dbPath)
	check(true, "REDIS_ADDR", redisAddr)
	db, err := storage.New(dbPath)
	if err != nil {
		check(false, "SQLite open + migrations", err.Error())
	} else {
		check(true, "SQLite open + migrations", dbPath)
		_ = db.Close()
	}
	if host, port, err := net.SplitHostPort(redisAddr); err != nil {
		warn("Redis TCP", "invalid address; in-memory fallback will be used")
	} else if conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), time.Second); err != nil {
		warn("Redis TCP", "unavailable; in-memory fallback will be used")
	} else {
		_ = conn.Close()
		check(true, "Redis TCP", redisAddr)
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		warn("sqlite3 CLI", "backup worker will skip backups")
	} else {
		check(true, "sqlite3 CLI available", "")
	}
	if env["WEBHOOK_URL"] == "" {
		check(true, "Webhook mode", "polling expected")
	} else {
		check(true, "Webhook mode", fmt.Sprintf("configured, secret_set=%t", env["TELEGRAM_WEBHOOK_SECRET"] != ""))
	}
	fmt.Println()
	if fail {
		fmt.Println("Preflight completed with failures.")
		os.Exit(1)
	}
	if warnings {
		fmt.Println("Preflight completed with warnings.")
		return
	}
	fmt.Println("Preflight completed successfully.")
}
