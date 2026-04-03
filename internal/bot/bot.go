package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"

	"shop_bot/internal/bot/middleware"
	"shop_bot/internal/config"
	"shop_bot/internal/payment"
	"shop_bot/internal/service"
	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

// Bot is the main Telegram bot that routes updates to handlers.
type Bot struct {
	api             *tgbotapi.BotAPI
	cfg             *config.Config
	catalog         *shop.CatalogService
	cart            *shop.CartService
	order           *shop.OrderService
	users           storage.UserStore
	products        storage.ProductStore
	promos          storage.PromoStore
	analytics       storage.AnalyticsStore
	referrals       *storage.ReferralStore
	referralService *service.ReferralService
	stars           *payment.StarsPayment
	crypto          *payment.CryptoBotPayment
	logger          *slog.Logger
	metrics         *service.MetricsService
	fsm             storage.FSMStore
	i18n            *service.I18nService

	wishlist *storage.WishlistStore

	// handler is the fully-chained update handler (used for both polling and webhook).
	handler func(tgbotapi.Update)

	handlerOnce sync.Once
}

// New creates a new Bot with all dependencies injected.
func New(cfg *config.Config, db *storage.DB, metrics *service.MetricsService, fsm storage.FSMStore, redisClient *redis.Client, logger *slog.Logger) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return nil, err
	}

	return NewWithAPI(cfg, api, db, metrics, fsm, redisClient, logger)
}

// NewWithAPI creates a new Bot using the provided Bot API client.
// This is primarily useful for local smoke tooling and tests that need to
// intercept outgoing Telegram requests without hitting the real Bot API.
func NewWithAPI(cfg *config.Config, api *tgbotapi.BotAPI, db *storage.DB, metrics *service.MetricsService, fsm storage.FSMStore, redisClient *redis.Client, logger *slog.Logger) (*Bot, error) {
	if api == nil {
		return nil, fmt.Errorf("bot api client is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	ps := storage.NewSQLProductStore(db)
	cachedPS := storage.NewCachedProductStore(ps, redisClient, 1*time.Hour)
	cs := storage.NewCartStore(db.Conn())
	os := storage.NewSQLOrderStore(db)
	us := storage.NewUserStore(db.Conn())
	promoStore := storage.NewSQLPromoStore(db)
	analyticsStore := storage.NewSQLAnalyticsStore(db)
	referralStore := storage.NewReferralStore(db.Conn())
	referralSvc := service.NewReferralService(2.0, 1.0, 100, redisClient)
	exchangeSvc := service.NewExchangeService(cfg.USDToStarsRate)

	i18nSvc, err := service.NewI18nService(cfg.LocalesDir)
	if err != nil {
		return nil, fmt.Errorf("i18n: %w", err)
	}

	b := &Bot{
		api:             api,
		cfg:             cfg,
		catalog:         shop.NewCatalogService(cachedPS, exchangeSvc),
		cart:            shop.NewCartService(cs, cachedPS, exchangeSvc),
		order:           shop.NewOrderService(os, cs, cachedPS, logger),
		users:           us,
		products:        cachedPS,
		promos:          promoStore,
		analytics:       analyticsStore,
		referrals:       referralStore,
		referralService: referralSvc,
		stars:           payment.NewStarsPayment(api),
		crypto:          payment.NewCryptoBotPayment(cfg.CryptoBotToken),
		logger:          logger,
		metrics:         metrics,
		fsm:             fsm,
		i18n:            i18nSvc,
		wishlist:        storage.NewWishlistStore(db.Conn()),
	}
	// handler is built lazily in Run so we have a context.
	return b, nil
}

// prepareHandler builds the fully-chained update handler and stores it in b.handler.
// ctx controls the lifetime of the rate-limit cleanup goroutine.
func (b *Bot) prepareHandler(ctx context.Context) {
	// 30 requests per 10 seconds with a burst of 10 per user.
	b.handler = Chain(b.route,
		LoggingMiddleware(b.logger, b.metrics),
		RecoverMiddleware(b.logger),
		middleware.Auth(b.users),
		RateLimitMiddleware(ctx, rate.Every(10*time.Second/30), 10),
	)
}

func (b *Bot) ensureHandler(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	b.handlerOnce.Do(func() {
		b.prepareHandler(ctx)
	})
}

// API returns the underlying Telegram Bot API instance.
func (b *Bot) API() *tgbotapi.BotAPI {
	return b.api
}

func (b *Bot) cryptoPaymentsEnabled() bool {
	return b.crypto != nil && b.crypto.Configured()
}

// Run starts the main update loop (polling). It blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	b.ensureHandler(ctx)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)

	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			wg.Wait()
			return ctx.Err()
		case update := <-updates:
			wg.Add(1)
			go func(upd tgbotapi.Update) {
				defer wg.Done()
				b.handler(upd)
			}(update)
		}
	}
}

// HandleUpdate processes a single Telegram update through the full middleware
// chain. It is useful for local smoke tooling and webhook-style entry points.
func (b *Bot) HandleUpdate(update tgbotapi.Update) {
	b.ensureHandler(context.Background())
	b.handler(update)
}

// RegisterTelegramWebhook registers the bot's webhook URL with the Telegram API.
func (b *Bot) RegisterTelegramWebhook(webhookURL string) error {
	if b.cfg.TelegramWebhookSecret != "" {
		params := tgbotapi.Params{
			"url":          webhookURL + "/telegram-webhook",
			"secret_token": b.cfg.TelegramWebhookSecret,
		}
		_, err := b.api.MakeRequest("setWebhook", params)
		return err
	}
	wh, err := tgbotapi.NewWebhook(webhookURL + "/telegram-webhook")
	if err != nil {
		return err
	}
	_, err = b.api.Request(wh)
	return err
}

// t translates a locale key for the given language code.
// Falls back to "ru" when lang is empty, then to the key itself.
func (b *Bot) t(lang, key string) string {
	if lang == "" {
		lang = "ru"
	}
	return b.i18n.T(lang, key)
}

// notifyAdmins sends a message to all configured admin IDs.
func (b *Bot) notifyAdmins(text string) {
	for _, adminID := range b.cfg.AdminIDs {
		b.send(tgbotapi.NewMessage(adminID, text))
	}
}

// sendError logs err and sends a localized error message to the user.
// If the user is an admin, the raw error is appended.
func (b *Bot) sendError(chatID int64, lang string, key string, err error) {
	b.logger.Error(key, "chat_id", chatID, "error", err)
	text := b.t(lang, key)
	if b.isAdmin(chatID) && err != nil {
		text += "\n\n⚠️ " + err.Error()
	}
	b.send(tgbotapi.NewMessage(chatID, text))
}

