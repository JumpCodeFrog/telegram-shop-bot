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
	"sync"
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

// workerGroup owns every background goroutine of the process. Start registers
// the goroutine in the WaitGroup BEFORE it launches (so Wait cannot slip past
// it) and remembers its name for the shutdown-timeout diagnostic.
type workerGroup struct {
	wg      sync.WaitGroup
	mu      sync.Mutex
	running map[string]struct{}
}

func newWorkerGroup() *workerGroup {
	return &workerGroup{running: make(map[string]struct{})}
}

func (g *workerGroup) Start(ctx context.Context, name string, fn func(context.Context)) {
	g.wg.Add(1)
	g.mu.Lock()
	g.running[name] = struct{}{}
	g.mu.Unlock()
	go func() {
		defer g.wg.Done()
		defer func() {
			g.mu.Lock()
			delete(g.running, name)
			g.mu.Unlock()
		}()
		fn(ctx)
	}()
}

// Drain waits for every worker up to timeout. A stuck worker must not keep the
// container from restarting, so on timeout we log WHO is stuck and move on —
// db.Close() then runs over whatever is left, which is the lesser evil.
func (g *workerGroup) Drain(timeout time.Duration) {
	done := make(chan struct{})
	go func() { g.wg.Wait(); close(done) }()
	select {
	case <-done:
		slog.Info("All background workers stopped")
	case <-time.After(timeout):
		g.mu.Lock()
		stuck := make([]string, 0, len(g.running))
		for name := range g.running {
			stuck = append(stuck, name)
		}
		g.mu.Unlock()
		slog.Warn("Shutdown timeout: workers did not stop", "timeout", timeout, "stuck", stuck)
	}
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

	// 7. Start Workers — every background goroutine goes through the group so
	// shutdown can wait for them BEFORE the deferred db.Close() runs.
	workers := newWorkerGroup()

	backupW := worker.NewBackupWorker(db.Conn(), 24*time.Hour)
	workers.Start(ctx, "backup", backupW.Start)

	// We need the stores for the worker
	cartStore := storage.NewCartStore(db.Conn())
	promoStore := storage.NewSQLPromoStore(db)
	userStore := storage.NewUserStore(db.Conn())
	cartW := worker.NewCartRecoveryWorker(b.API(), cartStore, promoStore, time.Hour)
	workers.Start(ctx, "cart_recovery", cartW.Start)

	if redisClient != nil {
		loyaltyW := worker.NewLoyaltyWorker(loyaltyStore, loyaltySvc, redisClient, b.API(), i18n, userStore)
		workers.Start(ctx, "loyalty", loyaltyW.Start)
	}

	wishlistStore := storage.NewWishlistStore(db.Conn())
	wishlistW := worker.NewWishlistWatcherWorker(b.API(), wishlistStore, i18n, 30*time.Minute)
	workers.Start(ctx, "wishlist_watcher", wishlistW.Start)

	onboardingW := worker.NewOnboardingWorker(b.API(), userStore, i18n, cfg.BotUsername, 24*time.Hour)
	workers.Start(ctx, "onboarding", onboardingW.Start)

	cryptoPayments := payment.NewCryptoBotPayment(cfg.CryptoBotToken)
	if cryptoPayments.Configured() {
		// Confirm through the bot's OrderService so polled payments get the
		// same loyalty/referral/cache side effects as webhook payments, and
		// let the bot send the outcome messages.
		pollingW := worker.NewCryptoBotPollingWorker(cryptoPayments, b.OrderService(), b.NotifyPaymentOutcome, 30*time.Second)
		workers.Start(ctx, "cryptobot_polling", pollingW.Start)
	} else {
		slog.Warn("CryptoBot disabled, skipping polling worker")
	}

	// 8. Health Check & Metrics API
	workers.Start(ctx, "http_api", func(ctx context.Context) {
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
	})

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

	// Drain background workers before the deferred db.Close() fires: a closed
	// DB under a live worker turns clean shutdown into "sql: database is closed".
	slog.Info("Shutdown: draining background workers")
	workers.Drain(10 * time.Second)

	slog.Info("Bot exited gracefully")
}
