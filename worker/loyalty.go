package worker

import (
	"context"
	"fmt"
	"log/slog"
	"shop_bot/internal/service"
	"shop_bot/internal/storage"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/redis/go-redis/v9"
)

type LoyaltyWorker struct {
	db      *storage.LoyaltyStoreImpl
	service *service.LoyaltyService
	redis   *redis.Client
	bot     *tgbotapi.BotAPI
	stream  string
}

func NewLoyaltyWorker(db *storage.LoyaltyStoreImpl, svc *service.LoyaltyService, rdb *redis.Client, bot *tgbotapi.BotAPI) *LoyaltyWorker {
	return &LoyaltyWorker{
		db:      db,
		service: svc,
		redis:   rdb,
		bot:     bot,
		stream:  "loyalty:tasks",
	}
}

func (w *LoyaltyWorker) Start(ctx context.Context) {
	slog.Info("Loyalty Worker started", "stream", w.stream)
	
	// Ensure group exists
	_ = w.redis.XGroupCreateMkStream(ctx, w.stream, "loyalty_group", "0").Err()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Loyalty Worker stopped")
			return
		default:
			// Read from stream
			streams, err := w.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    "loyalty_group",
				Consumer: "loyalty_worker_1",
				Streams:  []string{w.stream, ">"},
				Count:    1,
				Block:    5 * time.Second,
			}).Result()

			if err != nil {
				if err != redis.Nil {
					slog.Error("Redis stream read error", "error", err)
				}
				continue
			}

			for _, stream := range streams {
				for _, msg := range stream.Messages {
					w.handleMessage(ctx, msg)
				}
			}
		}
	}
}

func (w *LoyaltyWorker) handleMessage(ctx context.Context, msg redis.XMessage) {
	userID, _ := strconv.ParseInt(msg.Values["user_id"].(string), 10, 64)
	pts, _ := strconv.Atoi(msg.Values["pts"].(string))
	reason := msg.Values["reason"].(string)
	refID := msg.Values["ref_id"].(string)

	err := w.db.AddPoints(ctx, userID, pts, reason, refID)
	if err != nil {
		slog.Error("Error adding points from stream", "error", err, "user_id", userID)
	} else {
		ptsTotal, level, err := w.db.GetPoints(ctx, userID)
		if err == nil {
			if newLevel, upgraded := w.service.CheckAndUpgradeLevel(ctx, userID, level, ptsTotal); upgraded {
				msg := tgbotapi.NewMessage(userID, fmt.Sprintf("🎉 Поздравляем! Вы достигли уровня <b>%s</b>!", newLevel))
				msg.ParseMode = "HTML"
				w.bot.Send(msg)
				if newLevel == "vip" {
					w.bot.Send(tgbotapi.NewMessage(userID, "🎁 Вам отправлен VIP-подарок!"))
				}
			}
		}
	}

	w.redis.XAck(ctx, w.stream, "loyalty_group", msg.ID)
}

func (w *LoyaltyWorker) AddPointsAsync(ctx context.Context, userID int64, pts int, reason string, refID string) {
	err := w.redis.XAdd(ctx, &redis.XAddArgs{
		Stream: w.stream,
		Values: map[string]interface{}{
			"user_id": userID,
			"pts":     pts,
			"reason":  reason,
			"ref_id":  refID,
		},
	}).Err()
	if err != nil {
		slog.Error("Failed to add loyalty task to Redis", "error", err)
	}
}
