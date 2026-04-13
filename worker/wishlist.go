package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

// wishlistStore is the minimal storage interface required by WishlistWatcherWorker.
type wishlistStore interface {
	GetAllWithProducts(ctx context.Context) ([]storage.WishlistEntry, error)
	MarkPriceDropNotified(ctx context.Context, userID, productID int64) error
	ClearPriceDropNotified(ctx context.Context, userID, productID int64) error
	MarkBackInStockNotified(ctx context.Context, userID, productID int64) error
	ClearBackInStockNotified(ctx context.Context, userID, productID int64) error
}

type WishlistWatcherWorker struct {
	bot           *tgbotapi.BotAPI
	wishlistStore wishlistStore
	i18n          *service.I18nService
	interval      time.Duration
}

func NewWishlistWatcherWorker(bot *tgbotapi.BotAPI, ws wishlistStore, i18n *service.I18nService, interval time.Duration) *WishlistWatcherWorker {
	return &WishlistWatcherWorker{
		bot:           bot,
		wishlistStore: ws,
		i18n:          i18n,
		interval:      interval,
	}
}

func (w *WishlistWatcherWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("Wishlist Watcher Worker started")

	for {
		select {
		case <-ctx.Done():
			slog.Info("Wishlist Watcher Worker stopped")
			return
		case <-ticker.C:
			w.checkWishlists(ctx)
		}
	}
}

func (w *WishlistWatcherWorker) checkWishlists(ctx context.Context) {
	slog.Info("Running wishlist price/stock check...")

	entries, err := w.wishlistStore.GetAllWithProducts(ctx)
	if err != nil {
		slog.Error("Failed to get wishlist entries", "error", err)
		return
	}

	for _, e := range entries {
		lang := e.LanguageCode
		if lang == "" {
			lang = "en"
		}

		// Price drop notification: current price dropped ≥10% compared to price at time of adding.
		// Only send once per price-drop cycle; clear flag when price recovers.
		if e.PriceAtAdded > 0 {
			drop := (e.PriceAtAdded - e.Product.PriceUSD) / e.PriceAtAdded
			if drop >= 0.10 {
				if !e.PriceDropNotifiedAt.Valid {
					text := fmt.Sprintf(
						w.i18n.T(lang, "wishlist_price_drop"),
						e.Product.Name,
						e.PriceAtAdded,
						e.Product.PriceUSD,
						drop*100,
					)
					msg := tgbotapi.NewMessage(e.UserID, text)
					msg.ParseMode = "HTML"
					if _, err := w.bot.Send(msg); err != nil {
						slog.Error("Failed to send price drop notification", "user_id", e.UserID, "product_id", e.ProductID, "error", err)
					} else {
						_ = w.wishlistStore.MarkPriceDropNotified(ctx, e.UserID, e.ProductID)
					}
				}
			} else if e.PriceDropNotifiedAt.Valid {
				// Price recovered — reset so the next drop triggers a new notification.
				_ = w.wishlistStore.ClearPriceDropNotified(ctx, e.UserID, e.ProductID)
			}
		}

		// Back-in-stock notification: item was out of stock when added, now available.
		// Only send once; clear flag when item goes out of stock again.
		if e.StockAtAdded == 0 {
			if e.Product.Stock > 0 && !e.BackInStockNotifiedAt.Valid {
				text := fmt.Sprintf(
					w.i18n.T(lang, "wishlist_back_in_stock"),
					e.Product.Name,
					e.Product.Stock,
				)
				msg := tgbotapi.NewMessage(e.UserID, text)
				msg.ParseMode = "HTML"
				if _, err := w.bot.Send(msg); err != nil {
					slog.Error("Failed to send back-in-stock notification", "user_id", e.UserID, "product_id", e.ProductID, "error", err)
				} else {
					_ = w.wishlistStore.MarkBackInStockNotified(ctx, e.UserID, e.ProductID)
				}
			} else if e.Product.Stock == 0 && e.BackInStockNotifiedAt.Valid {
				// Went out of stock again — reset so next restock triggers a new notification.
				_ = w.wishlistStore.ClearBackInStockNotified(ctx, e.UserID, e.ProductID)
			}
		}
	}
}
