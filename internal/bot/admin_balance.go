package bot

// Admin balance adjustment (/setbalance <user_id> <±amount> [reason...]):
// credits or debits a user's internal USD balance through the balance store,
// which enforces the non-negativity guard atomically and appends the
// balance_txs audit row. Every adjustment names the acting admin and carries
// an "admin_adjust[: reason]" type so the operator trail stays complete.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

func (b *Bot) handleSetBalance(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	send := func(text string) {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, text))
	}

	args := strings.Fields(msg.CommandArguments())
	if len(args) < 2 {
		send(b.t(lang, "admin_setbalance_usage"))
		return
	}
	targetID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || targetID <= 0 {
		send(b.t(lang, "admin_setbalance_usage"))
		return
	}
	amount, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		send(b.t(lang, "admin_setbalance_usage"))
		return
	}

	reason := "admin_adjust"
	if len(args) > 2 {
		reason = "admin_adjust: " + strings.Join(args[2:], " ")
	}

	ctx, cancel := handlerCtx()
	defer cancel()
	newBalance, err := b.balances.AdjustBalance(ctx, targetID, amount, reason, msg.From.ID)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrNotFound):
			send(fmt.Sprintf(b.t(lang, "admin_setbalance_unknown_user"), targetID))
		case errors.Is(err, storage.ErrInsufficientFunds):
			send(fmt.Sprintf(b.t(lang, "admin_setbalance_insufficient"), targetID))
		case errors.Is(err, storage.ErrInvalidMoney):
			// Zero/NaN/Inf amounts are malformed input, not a store failure.
			send(b.t(lang, "admin_setbalance_usage"))
		default:
			b.logger.Error("adjust balance", "target_user_id", targetID, "error", err)
			send(b.t(lang, "error_short"))
		}
		return
	}
	send(fmt.Sprintf(b.t(lang, "admin_setbalance_ok"), targetID, newBalance))
}
