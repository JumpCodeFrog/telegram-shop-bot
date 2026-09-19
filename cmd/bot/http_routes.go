package main

import "net/http"

type webhookEndpoints interface {
	TelegramWebhookHandler() http.HandlerFunc
	CryptoBotWebhookHandler() http.HandlerFunc
	YooKassaWebhookHandler() http.HandlerFunc
	StripeWebhookHandler() http.HandlerFunc
}

func mountWebhookRoutes(mux *http.ServeMux, endpoints webhookEndpoints) {
	mux.Handle("/telegram-webhook", endpoints.TelegramWebhookHandler())
	mux.Handle("/cryptobot-webhook", endpoints.CryptoBotWebhookHandler())
	mux.Handle("/yookassa-webhook", endpoints.YooKassaWebhookHandler())
	mux.Handle("/stripe-webhook", endpoints.StripeWebhookHandler())
}
