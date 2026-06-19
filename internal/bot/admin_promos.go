package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

func (b *Bot) handleAddPromo(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 2 {
		return
	}
	discount, _ := strconv.Atoi(args[1])
	p := &storage.PromoCode{Code: strings.ToUpper(args[0]), Discount: discount, IsActive: true}
	_, _ = b.promos.CreatePromo(context.Background(), p)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Промокод создан"))
}

func (b *Bot) handleListPromos(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	promos, _ := b.promos.ListPromos(context.Background())
	var sb strings.Builder
	for _, p := range promos {
		sb.WriteString(fmt.Sprintf("%d: %s (-%d%%)\n", p.ID, p.Code, p.Discount))
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, sb.String()))
}

func (b *Bot) handleDeletePromo(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	id, _ := strconv.ParseInt(msg.CommandArguments(), 10, 64)
	_ = b.promos.DeactivatePromo(context.Background(), id)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Промокод деактивирован"))
}
