package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeWebhookEndpoints struct{}

func (fakeWebhookEndpoints) TelegramWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
}

func (fakeWebhookEndpoints) CryptoBotWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }
}

func TestMountWebhookRoutesMatchesPublicContract(t *testing.T) {
	mux := http.NewServeMux()
	mountWebhookRoutes(mux, fakeWebhookEndpoints{})

	tests := []struct {
		path string
		want int
	}{
		{path: "/telegram-webhook", want: http.StatusNoContent},
		{path: "/cryptobot-webhook", want: http.StatusAccepted},
		{path: "/webhook/telegram-webhook", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, tt.path, nil))
			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.want)
			}
		})
	}
}
