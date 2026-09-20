package bot

import (
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
	ctx, cancel := b.handlerCtx()
	defer cancel()
	_, _ = b.promos.CreatePromo(ctx, p)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(msg.From.LanguageCode, "admin_promo_created")))
}

func (b *Bot) handleListPromos(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	ctx, cancel := b.handlerCtx()
	defer cancel()
	promos, _ := b.promos.ListPromos(ctx)
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
	ctx, cancel := b.handlerCtx()
	defer cancel()
	_ = b.promos.DeactivatePromo(ctx, id)
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(msg.From.LanguageCode, "admin_promo_deactivated")))
}
