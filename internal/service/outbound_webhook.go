package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// OutboundWebhookEvent is the payload sent to the configured webhook URL.
type OutboundWebhookEvent struct {
	Event      string  `json:"event"`
	OrderID    int64   `json:"order_id"`
	UserID     int64   `json:"user_id"`
	TotalUSD   float64 `json:"total_usd"`
	TotalStars int     `json:"total_stars"`
	Method     string  `json:"method"`
	PaymentID  string  `json:"payment_id"`
}

// OutboundWebhookService fires HTTP POST notifications to an external URL on order events.
// If URL is empty, all calls are no-ops.
type OutboundWebhookService struct {
	url    string
	secret string
	client *http.Client
	logger *slog.Logger
}

// NewOutboundWebhookService creates a service. url and secret come from config.
func NewOutboundWebhookService(url, secret string, logger *slog.Logger) *OutboundWebhookService {
	return &OutboundWebhookService{
		url:    url,
		secret: secret,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: logger,
	}
}

// Enabled reports whether an outbound webhook URL is configured.
func (s *OutboundWebhookService) Enabled() bool {
	return s.url != ""
}

// Send fires the event asynchronously. Errors are logged but never returned to the caller.
func (s *OutboundWebhookService) Send(event OutboundWebhookEvent) {
	if !s.Enabled() {
		return
	}
	go s.send(event)
}

func (s *OutboundWebhookService) send(event OutboundWebhookEvent) {
	body, err := json.Marshal(event)
	if err != nil {
		s.logger.Error("outbound webhook: marshal", "error", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		s.logger.Error("outbound webhook: build request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if s.secret != "" {
		req.Header.Set("X-Webhook-Secret", s.secret)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Error("outbound webhook: send", "event", event.Event, "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.logger.Warn("outbound webhook: non-2xx response",
			"event", event.Event, "status", fmt.Sprintf("%d", resp.StatusCode))
	} else {
		s.logger.Debug("outbound webhook: delivered", "event", event.Event, "order_id", event.OrderID)
	}
}
