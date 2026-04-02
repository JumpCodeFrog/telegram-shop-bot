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
		"BOT_TOKEN":               os.Getenv("BOT_TOKEN"),
		"CRYPTOBOT_TOKEN":         os.Getenv("CRYPTOBOT_TOKEN"),
		"DB_PATH":                 os.Getenv("DB_PATH"),
		"REDIS_ADDR":              os.Getenv("REDIS_ADDR"),
		"REDIS_PASSWORD":          os.Getenv("REDIS_PASSWORD"),
		"WEBHOOK_URL":             os.Getenv("WEBHOOK_URL"),
		"TELEGRAM_WEBHOOK_SECRET": os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		"APP_ENV":                 os.Getenv("APP_ENV"),
		"LOG_LEVEL":               os.Getenv("LOG_LEVEL"),
	}

	dbPath := envValue(env, "DB_PATH", "data/shop.db")
	redisAddr := envValue(env, "REDIS_ADDR", "localhost:6379")
	appEnv := envValue(env, "APP_ENV", "development")
	logLevel := envValue(env, "LOG_LEVEL", "info")

	fmt.Println("Telegram Shop Bot Preflight")
	fmt.Println()

	hasFailures := false
	hasWarnings := false

	check := func(ok bool, label, detail string) {
		status := "OK"
		if !ok {
			status = "FAIL"
			hasFailures = true
		}
		fmt.Printf("[%s] %s", status, label)
		if detail != "" {
			fmt.Printf(": %s", detail)
		}
		fmt.Println()
	}
	warn := func(label, detail string) {
		hasWarnings = true
		fmt.Printf("[WARN] %s", label)
		if detail != "" {
			fmt.Printf(": %s", detail)
		}
		fmt.Println()
	}

	check(env["BOT_TOKEN"] != "", "BOT_TOKEN present", "")
	if env["CRYPTOBOT_TOKEN"] == "" {
		warn("CRYPTOBOT_TOKEN", "not set; USDT checkout and CryptoBot polling worker will stay disabled")
	} else {
		check(true, "CRYPTOBOT_TOKEN present", "crypto checkout enabled")
	}
	check(true, "DB_PATH", dbPath)
	check(true, "REDIS_ADDR", redisAddr)
	check(true, "APP_ENV", appEnv)
	check(true, "LOG_LEVEL", logLevel)

	db, err := storage.New(dbPath)
	if err != nil {
		check(false, "SQLite open + migrations", err.Error())
	} else {
		check(true, "SQLite open + migrations", dbPath)
		_ = db.Close()
	}

	host, port, err := net.SplitHostPort(redisAddr)
	if err != nil {
		warn("Redis TCP", fmt.Sprintf("invalid REDIS_ADDR %q; app will fall back to in-memory FSM", redisAddr))
	} else {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), time.Second)
		if err != nil {
			warn("Redis TCP", err.Error()+"; app will fall back to in-memory FSM")
		} else {
			_ = conn.Close()
			check(true, "Redis TCP", redisAddr)
		}
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		warn("sqlite3 CLI available", "backup worker will skip backups until installed")
	} else {
		check(true, "sqlite3 CLI available", "")
	}

	if _, err := exec.LookPath("redis-cli"); err != nil {
		warn("redis-cli available", "optional helper for manual ops")
	} else {
		check(true, "redis-cli available", "")
	}

	if env["WEBHOOK_URL"] == "" {
		check(true, "Webhook mode", "not configured; polling mode expected")
	} else {
		secretSet := env["TELEGRAM_WEBHOOK_SECRET"] != ""
		detail := fmt.Sprintf("url configured, secret_set=%t", secretSet)
		check(true, "Webhook mode", detail)
	}

	fmt.Println()
	if hasFailures {
		fmt.Println("Preflight completed with failures.")
		os.Exit(1)
	}
	if hasWarnings {
		fmt.Println("Preflight completed with warnings.")
		return
	}
	fmt.Println("Preflight completed successfully.")
}
