package worker

import (
	"context"
	"fmt"
	"log/slog"
	"shop_bot/internal/storage"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
)

type CartRecoveryWorker struct {
	cart           storage.CartStore
	promos         storage.PromoStore
	bot            *tgbotapi.BotAPI
	interval       time.Duration
	abandonedAfter time.Duration
}

func NewCartRecoveryWorker(bot *tgbotapi.BotAPI, cart storage.CartStore, promos storage.PromoStore, interval time.Duration, abandonedAfter ...time.Duration) *CartRecoveryWorker {
	age := 24 * time.Hour
	if len(abandonedAfter) > 0 {
		age = abandonedAfter[0]
	}
	return &CartRecoveryWorker{
		bot:            bot,
		cart:           cart,
		promos:         promos,
		interval:       interval,
		abandonedAfter: age,
	}
}

func (w *CartRecoveryWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("Cart Recovery Worker started", "interval", w.interval, "abandoned_after", w.abandonedAfter)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Cart Recovery Worker stopped")
			return
		case <-ticker.C:
			w.runRecovery(ctx)
		}
	}
}

func (w *CartRecoveryWorker) runRecovery(ctx context.Context) {
	// Find carts older than the configured abandonment threshold.
	userIDs, err := w.cart.GetAbandonedCarts(ctx, w.abandonedAfter)
	if err != nil {
		slog.Error("Failed to get abandoned carts", "error", err)
		return
	}

	for _, userID := range userIDs {
		w.processUser(ctx, userID)
	}
}

func (w *CartRecoveryWorker) processUser(ctx context.Context, userID int64) {
	// 1. Generate personal promo with unpredictable suffix.
	suffix := strings.ToUpper(uuid.New().String()[:8])
	code := fmt.Sprintf("RECOVER10-%s", suffix)
	promo := &storage.PromoCode{
		Code:     code,
		Discount: 10,
		MaxUses:  1,
		IsActive: true,
	}
	// Expires in 3 days
	expiresAt := time.Now().Add(72 * time.Hour)
	promo.ExpiresAt = &expiresAt

	_, err := w.promos.CreatePromo(ctx, promo)
	if err != nil {
		slog.Error("Failed to create recovery promo", "user_id", userID, "error", err)
		return
	}

	// 2. Send message
	text := fmt.Sprintf("👋 Мы заметили, что вы оставили товары в корзине!\n\n"+
		"Специально для вас мы подготовили промокод на скидку **10%%**: `%s`\n\n"+
		"Поторопитесь, он действует всего 3 дня!", code)

	msg := tgbotapi.NewMessage(userID, text)
	msg.ParseMode = "Markdown"
	if _, err := w.bot.Send(msg); err != nil {
		slog.Error("Failed to send recovery message", "user_id", userID, "error", err)
		// We still mark as sent to avoid spamming if user blocked the bot
	}

	// 3. Mark as sent
	if err := w.cart.MarkRecoverySent(ctx, userID); err != nil {
		slog.Error("Failed to mark recovery as sent", "user_id", userID, "error", err)
	}
}
