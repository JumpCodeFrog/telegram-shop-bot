package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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

var ErrTelegramInitialization = errors.New("Telegram rejected the token or could not be reached")
var errTelegramTransport = errors.New("Telegram request failed")

type sanitizedTelegramClient struct {
	client tgbotapi.HTTPClient
}

type pendingSubscriptionSignal struct {
	expiresAt time.Time
	renewal   bool
	refs      int
}

func (c sanitizedTelegramClient) Do(request *http.Request) (*http.Response, error) {
	response, err := c.client.Do(request)
	if err == nil {
		return response, nil
	}
	// net/http can return a response together with an error. The dependency
	// will not receive it on this branch, so close its body here.
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	// url.Error embeds request.URL, and the Telegram Bot API URL embeds the
	// bot token. All tgbotapi calls use this boundary, including long polling.
	return nil, errTelegramTransport
}

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
	photos          storage.ProductPhotoStore
	reviews         storage.ReviewStore
	referrals       *storage.ReferralStore
	referralService *service.ReferralService
	stars           *payment.StarsPayment
	crypto          *payment.CryptoBotPayment
	logger          *slog.Logger
	metrics         *service.MetricsService
	fsm             storage.FSMStore
	i18n            *service.I18nService
	outWebhook      *service.OutboundWebhookService

	wishlist   *storage.WishlistStore
	uiSettings storage.UISettingsStore
	subs       storage.SubscriptionStore
	// tgbotapi v5 omits recurring fields. Reference-counted signals preserve
	// them across concurrent duplicate deliveries of the same charge.
	pendingSubSignalsMu sync.Mutex
	pendingSubSignals   map[string]pendingSubscriptionSignal
	// uiStyles is an in-memory cache of button style overrides loaded from DB.
	// Invalidated and reloaded whenever an admin changes a button style.
	uiStyles sync.Map

	// handler is the fully-chained update handler (used for both polling and webhook).
	handler func(tgbotapi.Update)

	handlerOnce sync.Once
}

// handlerCtx returns a context with a 30-second deadline for use in handler
// DB/service calls. This prevents a single slow query from holding a goroutine indefinitely.
func handlerCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// New creates a new Bot with all dependencies injected.
func New(cfg *config.Config, db *storage.DB, metrics *service.MetricsService, fsm storage.FSMStore, redisClient *redis.Client, logger *slog.Logger) (*Bot, error) {
	api, err := tgbotapi.NewBotAPIWithClient(
		cfg.BotToken,
		tgbotapi.APIEndpoint,
		sanitizedTelegramClient{client: &http.Client{}},
	)
	if err != nil {
		// tgbotapi transport errors contain the request URL, and the token is
		// embedded in that URL. Keep the secret out of caller logs.
		return nil, ErrTelegramInitialization
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
	loyaltyStore := storage.NewLoyaltyStore(db.Conn())
	loyaltySvc := service.NewLoyaltyService(loyaltyStore, 1)

	i18nSvc, err := service.NewI18nService(cfg.LocalesDir)
	if err != nil {
		return nil, fmt.Errorf("i18n: %w", err)
	}
	translate := func(lang, key string) string {
		if lang == "" {
			lang = "en"
		}
		return i18nSvc.T(lang, key)
	}

	paymentDeps := shop.PaymentDeps{
		Users:     us,
		Loyalty:   loyaltySvc,
		Points:    loyaltyStore,
		Referrals: referralStore,
		Promos:    promoStore,
		Cache:     cachedPS,
		Metrics:   metrics,
	}

	b := &Bot{
		api:             api,
		cfg:             cfg,
		catalog:         shop.NewCatalogService(cachedPS, exchangeSvc),
		cart:            shop.NewCartService(cs, cachedPS, exchangeSvc),
		order:           shop.NewOrderService(os, cs, cachedPS, paymentDeps, logger),
		users:           us,
		products:        cachedPS,
		promos:          promoStore,
		analytics:       analyticsStore,
		referrals:       referralStore,
		referralService: referralSvc,
		stars:           payment.NewStarsPayment(api, os, translate),
		crypto:          payment.NewCryptoBotPayment(cfg.CryptoBotToken),
		logger:          logger,
		metrics:         metrics,
		fsm:             fsm,
		i18n:            i18nSvc,
		wishlist:        storage.NewWishlistStore(db.Conn()),
		outWebhook:      service.NewOutboundWebhookService(cfg.OutboundWebhookURL, cfg.OutboundWebhookSecret, logger),
		uiSettings:      storage.NewSQLUISettingsStore(db.Conn()),
		photos:          storage.NewSQLProductPhotoStore(db),
		reviews:         storage.NewSQLReviewStore(db),
		subs:            storage.NewSQLSubscriptionStore(db),
	}
	b.reloadButtonStyles(context.Background())
	// handler is built lazily in Run so we have a context.
	return b, nil
}

// OrderService exposes the bot's order service so background workers (e.g.
// the CryptoBot polling worker) confirm payments through the same pipeline.
func (b *Bot) OrderService() *shop.OrderService {
	return b.order
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

// registerCommands registers the bot command list with Telegram so the "/" menu shows up.
func (b *Bot) registerCommands() {
	cmds := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "start", Description: "Main menu"},
		tgbotapi.BotCommand{Command: "catalog", Description: "Browse products"},
		tgbotapi.BotCommand{Command: "cart", Description: "Your cart"},
		tgbotapi.BotCommand{Command: "orders", Description: "My orders"},
		tgbotapi.BotCommand{Command: "mysubs", Description: "My subscriptions"},
		tgbotapi.BotCommand{Command: "wishlist", Description: "Wishlist"},
		tgbotapi.BotCommand{Command: "search", Description: "Search products"},
		tgbotapi.BotCommand{Command: "profile", Description: "Profile & loyalty"},
		tgbotapi.BotCommand{Command: "referral", Description: "Invite friends & earn points"},
		tgbotapi.BotCommand{Command: "support", Description: "Customer support"},
		tgbotapi.BotCommand{Command: "paysupport", Description: "Payment help"},
		tgbotapi.BotCommand{Command: "terms", Description: "Terms and conditions"},
		tgbotapi.BotCommand{Command: "help", Description: "All commands"},
		tgbotapi.BotCommand{Command: "cancel", Description: "Cancel current action"},
	)
	if _, err := b.api.Request(cmds); err != nil {
		b.logger.Warn("setMyCommands failed", "error", err)
	}

	// Set the chat menu button: the Mini App when WEBAPP_URL is configured,
	// otherwise the commands list (visible as "/" button in input field).
	menuButton := `{"type":"commands"}`
	if b.cfg.WebAppURL != "" {
		if raw, err := json.Marshal(map[string]any{
			"type":    "web_app",
			"text":    b.t("en", "webapp_menu_button"),
			"web_app": map[string]string{"url": b.cfg.WebAppURL},
		}); err == nil {
			menuButton = string(raw)
		}
	}
	if _, err := b.api.MakeRequest("setChatMenuButton", tgbotapi.Params{
		"menu_button": menuButton,
	}); err != nil {
		b.logger.Warn("setChatMenuButton failed", "error", err)
	}
}

// Run starts the main update loop (polling). It blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	b.ensureHandler(ctx)
	b.registerCommands()

	offset := 0
	var wg sync.WaitGroup
	for {
		if err := ctx.Err(); err != nil {
			wg.Wait()
			return err
		}
		response, err := b.api.MakeRequest("getUpdates", tgbotapi.Params{
			"offset":          strconv.Itoa(offset),
			"limit":           "100",
			"timeout":         "1",
			"allowed_updates": `["message","callback_query","pre_checkout_query","inline_query"]`,
		})
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return ctx.Err()
			}
			b.logger.Warn("getUpdates failed; retrying", "error", err)
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				wg.Wait()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		var rawUpdates []json.RawMessage
		if err := json.Unmarshal(response.Result, &rawUpdates); err != nil {
			b.logger.Warn("getUpdates returned malformed result", "error", err)
			continue
		}
		retryBatch := false
		for index, raw := range rawUpdates {
			update, cleanup, err := b.decodeTelegramUpdate(raw)
			if err != nil {
				handled, updateID, quarantineErr := b.quarantineUndecodableStarsUpdate(ctx, raw)
				if handled && quarantineErr == nil {
					if updateID >= offset {
						offset = updateID + 1
					}
					continue
				}
				if handled {
					b.logger.Error("polling Stars payment decode failure was not durably handled", "error", quarantineErr)
					retryBatch = true
					break
				}
				b.logger.Warn("skip malformed Telegram update", "error", err)
				continue
			}
			if update.Message != nil && update.Message.SuccessfulPayment != nil {
				// A payment is an ordering barrier. Finish older updates first, then
				// advance getUpdates offset only after settlement/review is durable.
				wg.Wait()
				err := b.processSuccessfulPayment(update.Message)
				cleanup()
				if err != nil {
					b.logger.Error("polling Stars payment not durably handled", "update_id", update.UpdateID, "error", err)
					retryBatch = true
					break
				}
				if update.UpdateID >= offset {
					offset = update.UpdateID + 1
				}
				// Do not let a later asynchronous update advance the in-memory
				// offset before this durable payment barrier is visible to the next
				// provider request. Remaining rows are fetched again and stay safe
				// under idempotent routing.
				if index+1 < len(rawUpdates) {
					break
				}
				continue
			}
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			wg.Add(1)
			go func(upd tgbotapi.Update, done func()) {
				defer wg.Done()
				defer done()
				b.handler(upd)
			}(update, cleanup)
		}
		if retryBatch {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				wg.Wait()
				return ctx.Err()
			case <-timer.C:
			}
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
	b.registerCommands()
	callbackURL := config.TelegramWebhookURL(webhookURL)
	if callbackURL == "" {
		return fmt.Errorf("telegram webhook URL is required")
	}
	if b.cfg == nil {
		return fmt.Errorf("telegram webhook configuration is required")
	}
	if err := config.ValidateTelegramWebhookSecret(b.cfg.TelegramWebhookSecret); err != nil {
		return fmt.Errorf("telegram webhook secret: %w", err)
	}
	params := tgbotapi.Params{
		"url":          callbackURL,
		"secret_token": b.cfg.TelegramWebhookSecret,
	}
	_, err := b.api.MakeRequest("setWebhook", params)
	return err
}

// t translates a locale key for the given language code.
// Falls back to "en" when lang is empty, then to the key itself.
func (b *Bot) t(lang, key string) string {
	if lang == "" {
		lang = "en"
	}
	return b.i18n.T(lang, key)
}
