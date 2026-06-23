package bot

import (
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

func (b *Bot) handleAddCategory(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 2 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Использование: /addcategory <emoji> <название>"))
		return
	}
	cat := &storage.Category{
		Emoji:    args[0],
		Name:     strings.Join(args[1:], " "),
		IsActive: true,
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	id, err := b.catalog.CreateCategory(ctx, cat)
	if err != nil {
		b.logger.Error("create category", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось создать категорию."))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("✅ Категория создана: #%d %s %s", id, cat.Emoji, cat.Name)))
}

func (b *Bot) handleEditCategory(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	args := strings.Fields(msg.CommandArguments())
	if len(args) < 3 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Использование: /editcategory <id> name|emoji <значение>"))
		return
	}

	categoryID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Некорректный ID категории."))
		return
	}

	ctx, cancel := handlerCtx()
	defer cancel()
	category, err := b.catalog.GetCategory(ctx, categoryID)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Категория не найдена."))
		return
	}

	value := strings.Join(args[2:], " ")
	switch strings.ToLower(args[1]) {
	case "name":
		category.Name = value
	case "emoji":
		category.Emoji = value
	default:
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Поддерживаются только поля name и emoji."))
		return
	}

	if err := b.catalog.UpdateCategory(ctx, category); err != nil {
		b.logger.Error("update category", "category_id", categoryID, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось обновить категорию."))
		return
	}

	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Категория обновлена."))
}

func (b *Bot) handleDeleteCategory(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(msg.CommandArguments()), 10, 64)
	if err != nil {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Использование: /deletecategory <id>"))
		return
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	if err := b.catalog.DeleteCategory(ctx, id); err != nil {
		b.logger.Error("delete category", "category_id", id, "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось удалить категорию. Возможно, в ней ещё есть товары."))
		return
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, "✅ Категория удалена."))
}

func (b *Bot) handleListCategories(msg *tgbotapi.Message) {
	if !b.isAdmin(msg.From.ID) {
		return
	}
	ctx, cancel := handlerCtx()
	defer cancel()
	categories, err := b.catalog.ListCategories(ctx)
	if err != nil {
		b.logger.Error("list categories", "error", err)
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Не удалось загрузить категории."))
		return
	}
	if len(categories) == 0 {
		b.send(tgbotapi.NewMessage(msg.Chat.ID, "Категории не найдены."))
		return
	}

	var sb strings.Builder
	sb.WriteString("📂 Категории:\n\n")
	for _, category := range categories {
		sb.WriteString(fmt.Sprintf("%d: %s %s\n", category.ID, category.Emoji, category.Name))
	}
	b.send(tgbotapi.NewMessage(msg.Chat.ID, sb.String()))
}
