package bot

import (
	"context"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/config"
)

// AdminEvent identifies the kind of admin notification. It selects the forum
// topic (message_thread_id) when notifications go to an admin supergroup.
type AdminEvent int

const (
	AdminEventOrderNew AdminEvent = iota
	AdminEventOrderPaid
	AdminEventOrderDelivered
)

// topicFor returns the configured forum topic ID for the event, or 0 when the
// event has no topic configured (message goes to the group's General topic).
func topicFor(cfg *config.Config, kind AdminEvent) int {
	switch kind {
	case AdminEventOrderNew:
		return cfg.TopicOrdersNew
	case AdminEventOrderPaid:
		return cfg.TopicOrdersPaid
	case AdminEventOrderDelivered:
		return cfg.TopicOrdersDelivered
	default:
		return 0
	}
}

// resolveAdminTargets decides where an admin notification is delivered:
// with ADMIN_GROUP_ID set it returns the group chat (plus the event's topic,
// 0 = no topic) and no personal IDs; otherwise groupID is 0 and every
// configured admin gets a direct message.
func resolveAdminTargets(cfg *config.Config, kind AdminEvent) (groupID int64, threadID int, adminIDs []int64) {
	if cfg.AdminGroupID != 0 {
		return cfg.AdminGroupID, topicFor(cfg, kind), nil
	}
	return 0, 0, cfg.AdminIDs
}

// notifyAdmins delivers an admin notification. When an admin group is
// configured it sends a single message there (into the matching forum topic
// when one is set); otherwise it falls back to DMing every configured admin.
// Delivery is best-effort: failures are logged, never propagated.
func (b *Bot) notifyAdmins(ctx context.Context, kind AdminEvent, text string) {
	if ctx != nil && ctx.Err() != nil {
		return
	}

	groupID, threadID, adminIDs := resolveAdminTargets(b.cfg, kind)
	if groupID != 0 {
		// tgbotapi v5 MessageConfig predates forum topics, so send via raw
		// Bot API params to pass message_thread_id.
		params := tgbotapi.Params{"text": text}
		params.AddNonZero64("chat_id", groupID)
		params.AddNonZero("message_thread_id", threadID)
		if _, err := b.api.MakeRequest("sendMessage", params); err != nil {
			b.logger.Error("notify admins: group send", "chat_id", groupID, "thread_id", threadID, "error", err)
		}
		return
	}
	for _, adminID := range adminIDs {
		b.send(tgbotapi.NewMessage(adminID, text))
	}
}
