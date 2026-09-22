package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

func (b *Bot) handleAddCategory(ctx context.Context, msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 2 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_addcategory")))
		return
	}
	cat := &storage.Category{
		Emoji:    args[0],
		Name:     strings.Join(args[1:], " "),
		IsActive: true,
	}
	id, err := b.catalog.CreateCategory(ctx, cat)
	if err != nil {
		b.logger.Error("create category", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_create_failed")))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf(b.t(lang, "admin_category_created"), id, cat.Emoji, cat.Name)))
}

func (b *Bot) handleEditCategory(ctx context.Context, msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 3 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_editcategory")))
		return
	}

	categoryID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_invalid_category_id")))
		return
	}

	category, err := b.catalog.GetCategory(ctx, categoryID)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_not_found")))
		return
	}

	value := strings.Join(args[2:], " ")
	switch strings.ToLower(args[1]) {
	case "name":
		category.Name = value
	case "emoji":
		category.Emoji = value
	default:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_fields")))
		return
	}

	if err := b.catalog.UpdateCategory(ctx, category); err != nil {
		b.logger.Error("update category", "category_id", categoryID, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_update_failed")))
		return
	}

	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_updated")))
}

func (b *Bot) handleDeleteCategory(ctx context.Context, msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	id, err := strconv.ParseInt(strings.TrimSpace(msg.CommandArguments()), 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_usage_deletecategory")))
		return
	}
	if err := b.catalog.DeleteCategory(ctx, id); err != nil {
		b.logger.Error("delete category", "category_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_delete_failed")))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_category_deleted")))
}

func (b *Bot) handleListCategories(ctx context.Context, msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	lang := msg.From.LanguageCode
	categories, err := b.catalog.ListCategories(ctx)
	if err != nil {
		b.logger.Error("list categories", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_categories_load_failed")))
		return
	}
	if len(categories) == 0 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, b.t(lang, "admin_categories_empty")))
		return
	}

	var sb strings.Builder
	sb.WriteString(b.t(lang, "admin_categories_title"))
	for _, category := range categories {
		sb.WriteString(fmt.Sprintf("%d: %s %s\n", category.ID, category.Emoji, category.Name))
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, sb.String()))
}
