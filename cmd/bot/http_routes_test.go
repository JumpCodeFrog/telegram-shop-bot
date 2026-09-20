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

func (fakeWebhookEndpoints) YooKassaWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }
}

func (fakeWebhookEndpoints) StripeWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusResetContent) }
}

func (fakeWebhookEndpoints) NowpaymentsWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusPartialContent) }
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
		{path: "/yookassa-webhook", want: http.StatusCreated},
		{path: "/stripe-webhook", want: http.StatusResetContent},
		{path: "/nowpayments-webhook", want: http.StatusPartialContent},
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
