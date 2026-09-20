package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

// handleWizardPhotoStep processes StepPhoto: a photo message (largest
// PhotoSize wins) or a URL adds an image, up to storage.MaxProductPhotos;
// /done (or /skip) finishes the step. When state.EditProductID is set the
// photo is persisted directly on that product instead of the wizard state.
func (b *Bot) handleWizardPhotoStep(ctx context.Context, msg *tgbotapi.Message, state *storage.AddProductState) {
	chatID := msg.Chat.ID
	lang := msg.From.LanguageCode

	switch msg.Command() {
	case "done", "skip":
		if state.EditProductID != 0 {
			_ = b.fsm.DelAddProductState(ctx, msg.From.ID)
			b.sendAdminPhotoList(chatID, 0, state.EditProductID, lang)
			return
		}
		state.Step = storage.StepCategory
		_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_add_product_category")))
		return
	}

	fileID := ""
	if len(msg.Photo) > 0 {
		fileID = largestPhotoFileID(msg.Photo)
	} else if text := strings.TrimSpace(msg.Text); text != "" {
		fileID = text
	}
	if fileID == "" {
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_prompt")))
		return
	}

	if state.EditProductID != 0 {
		b.addProductPhoto(ctx, chatID, state.EditProductID, fileID, lang)
		return
	}

	if !appendWizardPhoto(state, fileID) {
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_limit")))
		return
	}
	_ = b.fsm.SetAddProductState(ctx, msg.From.ID, state, 30*time.Minute)
	b.send(tgbotapi.NewMessage(chatID, b.i18n.Tf(lang, "admin_photo_more", len(state.Photos), storage.MaxProductPhotos)))
}

// largestPhotoFileID returns the FileID of the PhotoSize with the largest
// pixel area. Telegram usually sends sizes sorted ascending, but the order is
// not guaranteed, so pick the maximum explicitly.
func largestPhotoFileID(sizes []tgbotapi.PhotoSize) string {
	fileID := ""
	bestArea := -1
	for _, s := range sizes {
		if area := s.Width * s.Height; area > bestArea {
			bestArea = area
			fileID = s.FileID
		}
	}
	return fileID
}

// appendWizardPhoto adds fileID to the in-progress wizard state and reports
// whether it fit under the storage.MaxProductPhotos limit.
func appendWizardPhoto(state *storage.AddProductState, fileID string) bool {
	if len(state.Photos) >= storage.MaxProductPhotos {
		return false
	}
	state.Photos = append(state.Photos, fileID)
	return true
}

// addProductPhoto persists one gallery photo on an existing product and sets
// it as the cover when the product has none.
func (b *Bot) addProductPhoto(ctx context.Context, chatID, productID int64, fileID, lang string) {
	if err := b.photos.Add(ctx, productID, fileID); err != nil {
		if errors.Is(err, storage.ErrTooManyPhotos) {
			b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_limit")))
			return
		}
		b.logger.Error("add product photo", "product_id", productID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_error")))
		return
	}
	if p, err := b.products.GetProduct(ctx, productID); err == nil && p.PhotoURL == "" {
		p.PhotoURL = fileID
		if err := b.products.UpdateProduct(ctx, p); err != nil {
			b.logger.Warn("update product cover", "product_id", productID, "error", err)
		}
	}
	b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_added")))
}

// sendAdminPhotoList renders the photo management screen for a product:
// one delete button per photo plus an add button.
func (b *Bot) sendAdminPhotoList(chatID int64, msgID int, productID int64, lang string) {
	ctx := context.Background()
	photos, err := b.photos.List(ctx, productID)
	if err != nil {
		b.logger.Error("list product photos", "product_id", productID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_error")))
		return
	}

	text := b.i18n.Tf(lang, "admin_photo_list_title", productID, len(photos), storage.MaxProductPhotos)
	if len(photos) == 0 {
		text += "\n" + b.t(lang, "admin_photo_none")
	}

	kb := make(StyledKeyboard, 0, len(photos)+1)
	for i, ph := range photos {
		kb = append(kb, []StyledButton{BtnDanger(fmt.Sprintf("🗑 %d", i+1), fmt.Sprintf("admin:photodel:%d:%d", ph.ID, productID))})
	}
	if len(photos) < storage.MaxProductPhotos {
		kb = append(kb, []StyledButton{Btn(b.t(lang, "admin_photo_add_btn"), fmt.Sprintf("admin:photoadd:%d", productID))})
	}
	b.sendOrEditStyled(chatID, msgID, text, "", kb)
}

// onAdminPhotoDelete handles admin:photodel:<photoID>:<productID>. After
// deleting it re-syncs the cover for wizard-managed (file_id) covers and
// re-renders the list.
func (b *Bot) onAdminPhotoDelete(chatID int64, msgID int, data, lang string) {
	parts := strings.Split(strings.TrimPrefix(data, "admin:photodel:"), ":")
	if len(parts) != 2 {
		return
	}
	photoID, err1 := strconv.ParseInt(parts[0], 10, 64)
	productID, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return
	}

	ctx := context.Background()
	if err := b.photos.Delete(ctx, photoID); err != nil {
		b.logger.Error("delete product photo", "photo_id", photoID, "error", err)
		b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_error")))
		return
	}
	b.syncProductCover(ctx, productID)
	b.sendAdminPhotoList(chatID, msgID, productID, lang)
}

// syncProductCover keeps products.photo_url pointing at an existing gallery
// photo. Explicit http(s) URL covers set by the admin are left untouched.
func (b *Bot) syncProductCover(ctx context.Context, productID int64) {
	p, err := b.products.GetProduct(ctx, productID)
	if err != nil {
		b.logger.Warn("get product for cover sync", "product_id", productID, "error", err)
		return
	}
	if strings.HasPrefix(p.PhotoURL, "http://") || strings.HasPrefix(p.PhotoURL, "https://") {
		return
	}
	photos, err := b.photos.List(ctx, productID)
	if err != nil {
		b.logger.Warn("list photos for cover sync", "product_id", productID, "error", err)
		return
	}
	for _, ph := range photos {
		if ph.FileID == p.PhotoURL {
			return
		}
	}
	cover := ""
	if len(photos) > 0 {
		cover = photos[0].FileID
	}
	if p.PhotoURL == cover {
		return
	}
	p.PhotoURL = cover
	if err := b.products.UpdateProduct(ctx, p); err != nil {
		b.logger.Warn("update product cover", "product_id", productID, "error", err)
	}
}

// onAdminPhotoAdd handles admin:photoadd:<productID>: puts the admin into a
// photo-only wizard state bound to the existing product.
func (b *Bot) onAdminPhotoAdd(chatID, userID int64, data, lang string) {
	productID, err := parseIDFromCallback(data, "admin:photoadd:")
	if err != nil {
		b.logger.Error("parse admin:photoadd callback", "error", err)
		return
	}
	ctx := context.Background()
	_ = b.fsm.SetAddProductState(ctx, userID, &storage.AddProductState{Step: storage.StepPhoto, EditProductID: productID, CreatedAt: time.Now()}, 30*time.Minute)
	b.send(tgbotapi.NewMessage(chatID, b.t(lang, "admin_photo_prompt")))
}
