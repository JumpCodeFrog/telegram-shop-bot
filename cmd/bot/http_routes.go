package main

import "net/http"

type webhookEndpoints interface {
	TelegramWebhookHandler() http.HandlerFunc
	CryptoBotWebhookHandler() http.HandlerFunc
}

func mountWebhookRoutes(mux *http.ServeMux, endpoints webhookEndpoints) {
	mux.Handle("/telegram-webhook", endpoints.TelegramWebhookHandler())
	mux.Handle("/cryptobot-webhook", endpoints.CryptoBotWebhookHandler())
}
