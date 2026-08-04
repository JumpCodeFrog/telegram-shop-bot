package worker

import (
	"context"
	"fmt"
	"log/slog"
	"shop_bot/internal/service"
	"shop_bot/internal/storage"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/redis/go-redis/v9"
)

const (
	loyaltyBackoffMin = time.Second
	loyaltyBackoffMax = 30 * time.Second
)

type LoyaltyWorker struct {
	db      *storage.LoyaltyStoreImpl
	service *service.LoyaltyService
	i18n    *service.I18nService
	users   storage.UserStore
	redis   *redis.Client
	bot     *tgbotapi.BotAPI
	stream  string
}

func NewLoyaltyWorker(db *storage.LoyaltyStoreImpl, svc *service.LoyaltyService, rdb *redis.Client, bot *tgbotapi.BotAPI, i18n *service.I18nService, users storage.UserStore) *LoyaltyWorker {
	return &LoyaltyWorker{
		db:      db,
		service: svc,
		i18n:    i18n,
		users:   users,
		redis:   rdb,
		bot:     bot,
		stream:  "loyalty:tasks",
	}
}

func (w *LoyaltyWorker) Start(ctx context.Context) {
	slog.Info("Loyalty Worker started", "stream", w.stream)

	// BUSYGROUP means the group already exists — that is the steady state.
	if err := w.redis.XGroupCreateMkStream(ctx, w.stream, "loyalty_group", "0").Err(); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		slog.Error("Loyalty stream group create failed", "error", err, "stream", w.stream)
	}

	backoff := loyaltyBackoffMin
	for {
		if ctx.Err() != nil {
			slog.Info("Loyalty Worker stopped")
			return
		}

		streams, err := w.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    "loyalty_group",
			Consumer: "loyalty_worker_1",
			Streams:  []string{w.stream, ">"},
			Count:    1,
			Block:    5 * time.Second,
		}).Result()

		if err != nil {
			if err == redis.Nil {
				// No new messages within the Block window — a healthy round trip.
				backoff = loyaltyBackoffMin
				continue
			}
			if ctx.Err() != nil {
				slog.Info("Loyalty Worker stopped")
				return
			}
			slog.Error("Redis stream read error", "error", err, "retry_in", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				slog.Info("Loyalty Worker stopped")
				return
			}
			backoff = min(backoff*2, loyaltyBackoffMax)
			continue
		}
		backoff = loyaltyBackoffMin

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				w.handleMessage(ctx, msg)
			}
		}
	}
}

// loyaltyTask is a validated loyalty stream message payload.
type loyaltyTask struct {
	userID int64
	pts    int
	reason string
	refID  string
}

// parseLoyaltyTask validates msg.Values without panicking on missing keys or
// unexpected types: Redis stream payloads are external input.
func parseLoyaltyTask(values map[string]interface{}) (loyaltyTask, error) {
	str := func(key string) (string, error) {
		raw, ok := values[key]
		if !ok {
			return "", fmt.Errorf("missing field %q", key)
		}
		s, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("field %q: expected string, got %T", key, raw)
		}
		return s, nil
	}

	userRaw, err := str("user_id")
	if err != nil {
		return loyaltyTask{}, err
	}
	userID, err := strconv.ParseInt(userRaw, 10, 64)
	if err != nil {
		return loyaltyTask{}, fmt.Errorf("field \"user_id\" = %q: %w", userRaw, err)
	}

	ptsRaw, err := str("pts")
	if err != nil {
		return loyaltyTask{}, err
	}
	pts, err := strconv.Atoi(ptsRaw)
	if err != nil {
		return loyaltyTask{}, fmt.Errorf("field \"pts\" = %q: %w", ptsRaw, err)
	}

	reason, err := str("reason")
	if err != nil {
		return loyaltyTask{}, err
	}
	refID, err := str("ref_id")
	if err != nil {
		return loyaltyTask{}, err
	}

	return loyaltyTask{userID: userID, pts: pts, reason: reason, refID: refID}, nil
}

func (w *LoyaltyWorker) handleMessage(ctx context.Context, msg redis.XMessage) {
	task, err := parseLoyaltyTask(msg.Values)
	if err != nil {
		// Malformed payloads are acked, not retried: they will never parse better.
		slog.Warn("Dropping malformed loyalty message", "id", msg.ID, "error", err)
		w.ack(ctx, msg.ID)
		return
	}

	if err := w.db.AddPoints(ctx, task.userID, task.pts, task.reason, task.refID); err != nil {
		slog.Error("Error adding points from stream", "error", err, "user_id", task.userID)
	} else if ptsTotal, level, err := w.db.GetPoints(ctx, task.userID); err != nil {
		slog.Error("Loyalty points lookup failed", "error", err, "user_id", task.userID)
	} else if newLevel, upgraded := w.service.CheckAndUpgradeLevel(ctx, task.userID, level, ptsTotal); upgraded {
		lang := w.userLang(ctx, task.userID)
		levelUpMsg := tgbotapi.NewMessage(task.userID, w.i18n.Tf(lang, "loyalty_level_up", newLevel))
		levelUpMsg.ParseMode = "HTML"
		if _, err := w.bot.Send(levelUpMsg); err != nil {
			slog.Error("Loyalty level-up notification failed", "error", err, "user_id", task.userID)
		}
		if newLevel == "vip" {
			if _, err := w.bot.Send(tgbotapi.NewMessage(task.userID, w.i18n.T(lang, "loyalty_vip_gift"))); err != nil {
				slog.Error("Loyalty VIP gift notification failed", "error", err, "user_id", task.userID)
			}
		}
	}

	w.ack(ctx, msg.ID)
}

// ack acknowledges a stream message; a failed ack means the message will be
// redelivered, so it is worth a loud log line.
func (w *LoyaltyWorker) ack(ctx context.Context, id string) {
	if err := w.redis.XAck(ctx, w.stream, "loyalty_group", id).Err(); err != nil {
		slog.Error("Loyalty XAck failed", "error", err, "message_id", id)
	}
}

// userLang resolves the notification language from the user's stored
// language_code, falling back to "en".
func (w *LoyaltyWorker) userLang(ctx context.Context, userID int64) string {
	if w.users == nil {
		return "en"
	}
	u, err := w.users.GetByTelegramID(ctx, userID)
	if err != nil {
		slog.Error("Loyalty user language lookup failed", "error", err, "user_id", userID)
		return "en"
	}
	if u == nil || u.LanguageCode == "" {
		return "en"
	}
	return u.LanguageCode
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
