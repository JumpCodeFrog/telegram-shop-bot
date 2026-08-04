package bot

import (
	"fmt"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/storage"
)

func TestLargestPhotoFileID_PicksMaxResolution(t *testing.T) {
	// Deliberately unsorted: Telegram usually sends ascending sizes,
	// but the order is not guaranteed.
	sizes := []tgbotapi.PhotoSize{
		{FileID: "mid", Width: 320, Height: 240},
		{FileID: "big", Width: 1280, Height: 960},
		{FileID: "small", Width: 90, Height: 60},
	}

	if got := largestPhotoFileID(sizes); got != "big" {
		t.Fatalf("largestPhotoFileID() = %q, want %q", got, "big")
	}
}

func TestLargestPhotoFileID_EmptySlice(t *testing.T) {
	if got := largestPhotoFileID(nil); got != "" {
		t.Fatalf("largestPhotoFileID(nil) = %q, want empty", got)
	}
}

func TestAppendWizardPhoto_EnforcesLimit(t *testing.T) {
	state := &storage.AddProductState{}

	for i := range storage.MaxProductPhotos {
		if !appendWizardPhoto(state, fmt.Sprintf("file-%d", i)) {
			t.Fatalf("photo %d rejected before the %d-photo limit", i+1, storage.MaxProductPhotos)
		}
	}

	if appendWizardPhoto(state, "one-too-many") {
		t.Fatalf("photo %d accepted beyond the limit", storage.MaxProductPhotos+1)
	}
	if len(state.Photos) != storage.MaxProductPhotos {
		t.Fatalf("state holds %d photos, want %d", len(state.Photos), storage.MaxProductPhotos)
	}
	if state.Photos[0] != "file-0" {
		t.Fatalf("first photo = %q, want %q (cover source)", state.Photos[0], "file-0")
	}
}

func TestPhotoFileData_DistinguishesURLsFromFileIDs(t *testing.T) {
	if _, ok := photoFileData("https://example.com/a.jpg").(tgbotapi.FileURL); !ok {
		t.Fatal("https reference should map to tgbotapi.FileURL")
	}
	if _, ok := photoFileData("AgACAgIAAxkBAAI").(tgbotapi.FileID); !ok {
		t.Fatal("plain reference should map to tgbotapi.FileID")
	}
}
