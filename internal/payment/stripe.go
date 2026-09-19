package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

var (
	ErrStripeNotConfigured  = errors.New("stripe: credentials not configured")
	ErrInvalidStripeReceipt = errors.New("stripe: invalid payment receipt")

	ErrStripeSignatureMalformed = errors.New("stripe: malformed webhook signature header")
	ErrStripeSignatureStale     = errors.New("stripe: webhook signature timestamp outside tolerance")
	ErrStripeSignatureMismatch  = errors.New("stripe: webhook signature mismatch")
)

const (
	stripeResponseLimit = 1 << 20
	// stripeSignatureTolerance is the maximum accepted age of a webhook
	// signature timestamp, per Stripe's replay-protection guidance.
	stripeSignatureTolerance = 300 * time.Second
)

// StripePayment handles USD card payments via Stripe Checkout Sessions.
// Webhooks are HMAC-signed: once the signature verifies, the signed body is
// authoritative and settles the order WITHOUT any API refetch (see
// ParseWebhook).
type StripePayment struct {
	secretKey     string
	webhookSecret string
	returnURL     string
	baseURL       string
	client        *http.Client
	// nowFunc is the clock for webhook timestamp tolerance checks. It is an
	// unexported seam: production always uses time.Now, same-package tests
	// pin it by assigning the field directly.
	nowFunc func() time.Time
}

// NewStripePayment creates a new StripePayment with the given API secret,
// webhook signing secret and the URL the buyer returns to after paying.
func NewStripePayment(secretKey, webhookSecret, returnURL string) *StripePayment {
	return &StripePayment{
		secretKey:     strings.TrimSpace(secretKey),
		webhookSecret: strings.TrimSpace(webhookSecret),
		returnURL:     strings.TrimSpace(returnURL),
		baseURL:       "https://api.stripe.com/v1",
		client:        &http.Client{},
		nowFunc:       time.Now,
	}
}

// Configured reports whether the Stripe integration has usable credentials.
func (s *StripePayment) Configured() bool {
	return s.secretKey != "" && s.webhookSecret != "" && s.returnURL != ""
}

// SetBaseURL overrides the Stripe API base URL. Test seam only: bot/webapi/e2e
// tests live in other packages and cannot touch the unexported baseURL field.
func (s *StripePayment) SetBaseURL(url string) { s.baseURL = url }

// StripeSession is the authoritative snapshot of a Stripe Checkout Session.
type StripeSession struct {
	ID            string
	Status        string // open|complete|expired
	PaymentStatus string // paid|unpaid|no_payment_required
	AmountTotal   int64  // minor units (cents)
	Currency      string // normalized to uppercase on read
	OrderID       int64
}

type stripeSessionObject struct {
	ID                string            `json:"id"`
	Status            string            `json:"status"`
	PaymentStatus     string            `json:"payment_status"`
	AmountTotal       int64             `json:"amount_total"`
	Currency          string            `json:"currency"`
	Metadata          map[string]string `json:"metadata"`
	ClientReferenceID string            `json:"client_reference_id"`
}

type stripeErrorResponse struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// CreateCheckoutSession registers a Stripe Checkout Session for the given
// order and returns its hosted payment URL. A fresh idempotency key is used
// for every attempt: the ledger quarantines a second successful charge for
// the same order, so a retried request must not silently reuse an expired
// session.
func (s *StripePayment) CreateCheckoutSession(ctx context.Context, orderID int64, amountCents int64, description string) (*Invoice, error) {
	if !s.Configured() {
		return nil, ErrStripeNotConfigured
	}
	if orderID <= 0 || amountCents <= 0 {
		return nil, ErrInvalidStripeReceipt
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	orderRef := strconv.FormatInt(orderID, 10)
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", s.returnURL)
	form.Set("cancel_url", s.returnURL)
	form.Set("client_reference_id", orderRef)
	form.Set("metadata[order_id]", orderRef)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", "usd")
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(amountCents, 10))
	form.Set("line_items[0][price_data][product_data][name]", description)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("stripe: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+s.secretKey)
	req.Header.Set("Idempotency-Key", uuid.NewString())

	rawBody, status, err := s.do(req)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, stripeAPIError(rawBody, status)
	}

	var session struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rawBody, &session); err != nil {
		return nil, fmt.Errorf("stripe: parse create session response: %w", err)
	}
	if session.ID == "" || strings.TrimSpace(session.URL) == "" {
		return nil, errors.New("stripe: API returned an empty checkout URL")
	}
	return &Invoice{PayURL: session.URL, InvoiceID: session.ID}, nil
}

// GetCheckoutSession reads the authoritative session state from the Stripe
// API.
func (s *StripePayment) GetCheckoutSession(ctx context.Context, sessionID string) (*StripeSession, error) {
	if !s.Configured() {
		return nil, ErrStripeNotConfigured
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, ErrInvalidStripeReceipt
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/checkout/sessions/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return nil, fmt.Errorf("stripe: get session request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.secretKey)

	rawBody, status, err := s.do(req)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, stripeAPIError(rawBody, status)
	}

	var object stripeSessionObject
	if err := json.Unmarshal(rawBody, &object); err != nil {
		return nil, fmt.Errorf("stripe: parse session response: %w", err)
	}
	return object.toSession()
}

func (o stripeSessionObject) toSession() (*StripeSession, error) {
	if o.ID == "" {
		return nil, ErrInvalidStripeReceipt
	}
	orderID := int64(0)
	if raw, ok := o.Metadata["order_id"]; ok {
		if parsed, err := parsePositiveProviderID(raw); err == nil {
			orderID = parsed
		}
	}
	if orderID == 0 {
		if parsed, err := parsePositiveProviderID(o.ClientReferenceID); err == nil {
			orderID = parsed
		}
	}
	return &StripeSession{
		ID:            o.ID,
		Status:        o.Status,
		PaymentStatus: o.PaymentStatus,
		AmountTotal:   o.AmountTotal,
		Currency:      strings.ToUpper(o.Currency),
		OrderID:       orderID,
	}, nil
}

// VerifyWebhookSignature validates a Stripe-Signature header against the raw
// webhook body. The signed payload is "<t>.<body>", HMAC-SHA256 with the
// webhook secret, hex-encoded and compared in constant time against every v1
// value. Timestamps more than stripeSignatureTolerance away from now are
// rejected as replay protection.
func (s *StripePayment) VerifyWebhookSignature(header string, body []byte) error {
	if !s.Configured() {
		return ErrStripeNotConfigured
	}

	var timestamp string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return ErrStripeSignatureMalformed
		}
		switch key {
		case "t":
			timestamp = value
		case "v1":
			signatures = append(signatures, value)
		default:
			// Unknown keys (other schemes) are ignored.
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return ErrStripeSignatureMalformed
	}
	sentAt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrStripeSignatureMalformed
	}

	delta := s.nowFunc().Unix() - sentAt
	if delta < 0 {
		delta = -delta
	}
	if delta > int64(stripeSignatureTolerance/time.Second) {
		return ErrStripeSignatureStale
	}

	mac := hmac.New(sha256.New, []byte(s.webhookSecret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := mac.Sum(nil)
	for _, signature := range signatures {
		decoded, err := hex.DecodeString(signature)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare(decoded, expected) == 1 {
			return nil
		}
	}
	return ErrStripeSignatureMismatch
}

// stripeWebhookEnvelope is the outer shape of a Stripe event notification.
// Only checkout.session.completed carries a session this shop settles.
type stripeWebhookEnvelope struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object stripeSessionObject `json:"object"`
	} `json:"data"`
}

// ParseWebhook parses a signature-verified Stripe event body and returns the
// event type plus, for checkout.session.completed events that name a session,
// the session snapshot. Any other event — or a completed event without a
// session id — yields a nil session: there is nothing to settle. The caller
// MUST have verified the webhook signature first; the parsed body is
// authoritative and is never refetched from the API.
func (s *StripePayment) ParseWebhook(body []byte) (string, *StripeSession, error) {
	var event stripeWebhookEnvelope
	if err := json.Unmarshal(body, &event); err != nil {
		return "", nil, fmt.Errorf("stripe: parse webhook event: %w", err)
	}
	if event.Type != "checkout.session.completed" || event.Data.Object.ID == "" {
		return event.Type, nil, nil
	}
	session, err := event.Data.Object.toSession()
	if err != nil {
		return event.Type, nil, err
	}
	return event.Type, session, nil
}

// PaymentReceipt turns a settled session into a ledger receipt. A session
// settles only when it is complete AND paid, in USD, with a positive amount
// and a resolvable order reference.
func (s *StripeSession) PaymentReceipt() (shop.PaymentReceipt, error) {
	if s == nil || s.Status != "complete" || s.PaymentStatus != "paid" || s.Currency != "USD" {
		return shop.PaymentReceipt{}, ErrInvalidStripeReceipt
	}
	if s.AmountTotal <= 0 || s.OrderID <= 0 || strings.TrimSpace(s.ID) == "" {
		return shop.PaymentReceipt{}, ErrInvalidStripeReceipt
	}
	return shop.PaymentReceipt{
		OrderID: s.OrderID, Provider: storage.PaymentMethodStripe,
		ExternalID: s.ID,
		// Stripe has no Telegram payer identity; PayerID stays zero.
		PayerID: 0, Currency: "USD",
		AmountMinor: s.AmountTotal, Scale: 2,
	}, nil
}

// PaymentAnomaly preserves the factual part of a session that cannot be
// turned into a valid order receipt.
func (s *StripeSession) PaymentAnomaly(reason string) (storage.PaymentAnomaly, error) {
	if s == nil || strings.TrimSpace(reason) == "" {
		return storage.PaymentAnomaly{}, ErrInvalidStripeReceipt
	}
	amount, scale := s.AmountTotal, 2
	if amount <= 0 {
		amount, scale = 0, 0
	}
	return storage.PaymentAnomaly{
		ProposedOrderID: s.OrderID,
		Provider:        storage.PaymentMethodStripe,
		ExternalID:      s.ID,
		AmountMinor:     amount,
		Currency:        s.Currency,
		Scale:           scale,
		// Stripe reports the session amount as integer cents; keep that
		// exact spelling as the raw amount (mirrors YooKassa preserving
		// the provider's amount string).
		RawAmount:  strconv.FormatInt(s.AmountTotal, 10),
		RawPayload: "session_id:" + s.ID,
		Reason:     reason,
	}, nil
}

func (s *StripePayment) do(req *http.Request) ([]byte, int, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("stripe: send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, stripeResponseLimit+1))
	if err != nil {
		return nil, 0, fmt.Errorf("stripe: read response: %w", err)
	}
	if len(body) > stripeResponseLimit {
		return nil, 0, errors.New("stripe: response is too large")
	}
	return body, resp.StatusCode, nil
}

// stripeAPIError maps a non-2xx response to an error carrying the HTTP
// status and Stripe's error message. The secret key is never included.
func stripeAPIError(body []byte, status int) error {
	var apiErr stripeErrorResponse
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("stripe: HTTP status %d: %s", status, apiErr.Error.Message)
	}
	return fmt.Errorf("stripe: HTTP status %d", status)
}
