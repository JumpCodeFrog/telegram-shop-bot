package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"shop_bot/internal/bot"
	"shop_bot/internal/config"
	"shop_bot/internal/payment"
	"shop_bot/internal/service"
	"shop_bot/internal/storage"
	"shop_bot/worker"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

func logLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func redisAvailable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func main() {
	// 1. Load config
	if err := godotenv.Load(); err != nil {
		slog.Warn("No .env file found, using environment variables")
	}
	cfg, err := config.Load()
	if err != nil {
		slog.Error("Configuration error", "error", err)
		os.Exit(1)
	}

	// 2. Initialize Logger
	opts := &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)}
	var handler slog.Handler
	if cfg.AppEnv == "production" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)

	// 3. Initialize DB
	db, err := storage.New(cfg.DBPath)
	if err != nil {
		slog.Error("Database initialization error", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// 4. Initialize Services
	i18n, err := service.NewI18nService(cfg.LocalesDir)
	if err != nil {
		slog.Error("I18n initialization error", "error", err)
		os.Exit(1)
	}

	metrics := service.NewMetricsService()
	var (
		fsm         storage.FSMStore
		redisClient *redis.Client
	)
	if redisAvailable(cfg.RedisAddr) {
		redisFSM := storage.NewRedisFSMStore(cfg.RedisAddr, cfg.RedisPassword)
		fsm = redisFSM
		redisClient = redisFSM.Client()
		slog.Info("Redis available, using Redis-backed FSM/cache")
	} else {
		fsm = storage.NewMemoryFSMStore()
		slog.Warn("Redis unavailable, using in-memory FSM and disabling Redis-dependent workers", "addr", cfg.RedisAddr)
	}
	loyaltyStore := storage.NewLoyaltyStore(db.Conn())
	loyaltySvc := service.NewLoyaltyService(loyaltyStore, 1)

	// 5. Initialize Bot
	b, err := bot.New(cfg, db, metrics, fsm, redisClient, slog.Default())
	if err != nil {
		slog.Error("Bot initialization error", "error", err)
		os.Exit(1)
	}

	// 6. Context & Signal Handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 7. Start Workers
	backupW := worker.NewBackupWorker(cfg.DBPath, 24*time.Hour)
	go backupW.Start(ctx)

	// We need the stores for the worker
	cartStore := storage.NewCartStore(db.Conn())
	promoStore := storage.NewSQLPromoStore(db)
	cartW := worker.NewCartRecoveryWorker(b.API(), cartStore, promoStore, time.Hour)
	go cartW.Start(ctx)

	if redisClient != nil {
		loyaltyW := worker.NewLoyaltyWorker(loyaltyStore, loyaltySvc, redisClient, b.API(), i18n)
		go loyaltyW.Start(ctx)
	}

	wishlistStore := storage.NewWishlistStore(db.Conn())
	wishlistW := worker.NewWishlistWatcherWorker(b.API(), wishlistStore, i18n, 30*time.Minute)
	go wishlistW.Start(ctx)

	userStore := storage.NewUserStore(db.Conn())
	onboardingW := worker.NewOnboardingWorker(b.API(), userStore, i18n, cfg.BotUsername, 24*time.Hour)
	go onboardingW.Start(ctx)

	orderStore := storage.NewSQLOrderStore(db)
	cryptoPayments := payment.NewCryptoBotPayment(cfg.CryptoBotToken)
	if cryptoPayments.Configured() {
		pollingW := worker.NewCryptoBotPollingWorker(cryptoPayments, orderStore, 30*time.Second)
		go pollingW.Start(ctx)
	} else {
		slog.Warn("CryptoBot disabled, skipping polling worker")
	}

	// 8. Health Check & Metrics API
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			if err := db.Conn().PingContext(r.Context()); err != nil {
				slog.Error("Health check failed: DB ping", "error", err)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		})
		mux.Handle("/metrics", promhttp.Handler())

		// Mount webhook endpoints when WEBHOOK_URL is configured.
		if cfg.WebhookURL != "" {
			mux.Handle("/webhook/", b.WebhookHandler())
		}

		slog.Info("Health & Metrics API starting", "port", 8080)
		server := &http.Server{
			Addr:         ":8080",
			Handler:      mux,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		go func() {
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("API server error", "error", err)
			}
		}()
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("metrics server shutdown", "error", err)
		}
	}()

	// 9. Run Bot (webhook or polling)
	if cfg.WebhookURL != "" {
		slog.Info("Registering Telegram webhook", "url", cfg.WebhookURL)
		if err := b.RegisterTelegramWebhook(cfg.WebhookURL); err != nil {
			slog.Error("Failed to register webhook", "error", err)
			os.Exit(1)
		}
		slog.Info("Bot running in webhook mode — waiting for shutdown signal")
		<-ctx.Done()
	} else {
		slog.Info("Bot starting in polling mode...")
		if err := b.Run(ctx); err != nil && err != context.Canceled {
			slog.Error("Bot runtime error", "error", err)
			os.Exit(1)
		}
	}

	slog.Info("Bot exited gracefully")
}
