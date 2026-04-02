package bot

import (
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestMessageSupportsTextEdit_AllowsPlainTextMessages(t *testing.T) {
	msg := &tgbotapi.Message{
		MessageID: 42,
		Text:      "hello",
	}

	if !messageSupportsTextEdit(msg) {
		t.Fatal("expected plain text message to support EditMessageText")
	}
}

func TestMessageSupportsTextEdit_BlocksMediaMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  *tgbotapi.Message
	}{
		{
			name: "photo",
			msg: &tgbotapi.Message{
				MessageID: 1,
				Photo:     []tgbotapi.PhotoSize{{FileID: "photo-file"}},
			},
		},
		{
			name: "document",
			msg: &tgbotapi.Message{
				MessageID: 2,
				Document:  &tgbotapi.Document{FileID: "doc-file"},
			},
		},
		{
			name: "video",
			msg: &tgbotapi.Message{
				MessageID: 3,
				Video:     &tgbotapi.Video{FileID: "video-file"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if messageSupportsTextEdit(tt.msg) {
				t.Fatal("expected media message to require a fresh text message")
			}
		})
	}
}

func TestTextRenderMessageID(t *testing.T) {
	tests := []struct {
		name string
		msg  *tgbotapi.Message
		want int
	}{
		{
			name: "nil",
			msg:  nil,
			want: 0,
		},
		{
			name: "text",
			msg: &tgbotapi.Message{
				MessageID: 77,
				Text:      "screen",
			},
			want: 77,
		},
		{
			name: "photo",
			msg: &tgbotapi.Message{
				MessageID: 99,
				Photo:     []tgbotapi.PhotoSize{{FileID: "photo-file"}},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := textRenderMessageID(tt.msg); got != tt.want {
				t.Fatalf("textRenderMessageID() = %d, want %d", got, tt.want)
			}
		})
	}
}
