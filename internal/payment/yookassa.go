package payment

import (
	"bytes"
	"context"
	"encoding/base64"
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
	ErrYooKassaNotConfigured  = errors.New("yookassa: credentials not configured")
	ErrInvalidYooKassaReceipt = errors.New("yookassa: invalid payment receipt")
)

const yookassaResponseLimit = 1 << 20

// yookassaMaxPageLimit is the largest page size the YooKassa list endpoint
// accepts; larger limits are rejected by the API.
const yookassaMaxPageLimit = 100

// YooKassaPayment handles RUB payments via the YooKassa API using redirect
// confirmation. Webhooks are unsigned, so every notification is verified by
// re-reading the payment from the API before it can settle an order.
type YooKassaPayment struct {
	shopID    string
	secretKey string
	returnURL string
	baseURL   string
	client    *http.Client
}

// NewYooKassaPayment creates a new YooKassaPayment with the given shop
// credentials and the URL the buyer returns to after paying.
func NewYooKassaPayment(shopID, secretKey, returnURL string) *YooKassaPayment {
	return &YooKassaPayment{
		shopID:    strings.TrimSpace(shopID),
		secretKey: strings.TrimSpace(secretKey),
		returnURL: strings.TrimSpace(returnURL),
		baseURL:   "https://api.yookassa.ru/v3",
		client:    &http.Client{},
	}
}

// Configured reports whether the YooKassa integration has usable credentials.
func (y *YooKassaPayment) Configured() bool {
	return y.shopID != "" && y.secretKey != "" && y.returnURL != ""
}

// SetBaseURL overrides the YooKassa API base URL. Test seam only: bot/webapi/e2e
// tests live in other packages and cannot touch the unexported baseURL field.
func (y *YooKassaPayment) SetBaseURL(url string) { y.baseURL = url }

type yookassaAmount struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

type yookassaConfirmation struct {
	Type            string `json:"type"`
	ReturnURL       string `json:"return_url,omitempty"`
	ConfirmationURL string `json:"confirmation_url,omitempty"`
}

type yookassaPaymentObject struct {
	ID           string               `json:"id"`
	Status       string               `json:"status"`
	Paid         bool                 `json:"paid"`
	Amount       yookassaAmount       `json:"amount"`
	Confirmation yookassaConfirmation `json:"confirmation"`
	Metadata     map[string]string    `json:"metadata"`
	CreatedAt    string               `json:"created_at"`
	CapturedAt   string               `json:"captured_at"`
}

type yookassaCreateRequest struct {
	Amount       yookassaAmount       `json:"amount"`
	Capture      bool                 `json:"capture"`
	Confirmation yookassaConfirmation `json:"confirmation"`
	Description  string               `json:"description"`
	Metadata     map[string]string    `json:"metadata"`
}

type yookassaErrorResponse struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Code        string `json:"code"`
	Description string `json:"description"`
}

// yookassaPaymentList is the envelope of GET /payments. Items are full
// payment objects — the same shape GetPayment returns — and next_cursor is
// absent or empty on the last page.
type yookassaPaymentList struct {
	Type       string                  `json:"type"`
	Items      []yookassaPaymentObject `json:"items"`
	NextCursor string                  `json:"next_cursor"`
}

// yookassaRefundRequest is the JSON body of POST /refunds. The amount value
// is an exact two-decimal string built with formatMinorUnits (integer string
// math, never float rounding); the refund API takes the currency lowercase.
type yookassaRefundRequest struct {
	Amount      yookassaAmount `json:"amount"`
	Description string         `json:"description"`
	PaymentID   string         `json:"payment_id"`
}

// yookassaRefundObject is the API's refund response: id (rf_...) plus status
// ("succeeded"|"pending"|"canceled").
type yookassaRefundObject struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// CreatePayment registers a redirect payment for the given order and returns
// its confirmation URL. A fresh idempotence key is used for every attempt:
// the ledger quarantines a second successful charge for the same order, so a
// retried request must not silently reuse an expired payment.
func (y *YooKassaPayment) CreatePayment(ctx context.Context, orderID int64, amountRUBMinor int64, description string) (*Invoice, error) {
	if !y.Configured() {
		return nil, ErrYooKassaNotConfigured
	}
	if orderID <= 0 || amountRUBMinor <= 0 {
		return nil, ErrInvalidYooKassaReceipt
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	reqBody := yookassaCreateRequest{
		Amount:       yookassaAmount{Value: formatMinorUnits(amountRUBMinor, 2), Currency: "RUB"},
		Capture:      true,
		Confirmation: yookassaConfirmation{Type: "redirect", ReturnURL: y.returnURL},
		Description:  description,
		Metadata:     map[string]string{"order_id": strconv.FormatInt(orderID, 10)},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("yookassa: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, y.baseURL+"/payments", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("yookassa: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotence-Key", uuid.NewString())
	req.Header.Set("Authorization", y.basicAuth())

	var payment yookassaPaymentObject
	rawBody, status, err := y.doJSON(req)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, yookassaAPIError(rawBody, status)
	}
	if err := json.Unmarshal(rawBody, &payment); err != nil {
		return nil, fmt.Errorf("yookassa: parse create payment response: %w", err)
	}
	if payment.ID == "" || strings.TrimSpace(payment.Confirmation.ConfirmationURL) == "" {
		return nil, errors.New("yookassa: API returned an empty confirmation URL")
	}
	return &Invoice{PayURL: payment.Confirmation.ConfirmationURL, InvoiceID: payment.ID}, nil
}

// CreateRefund refunds a captured payment, in part or in full. amountMinor is
// the exact refund amount in minor units (kopecks) and MUST be positive:
// partial-or-full is the caller's math (and the ledger's bookkeeping) — the
// adapter never guesses an amount, so unlike Stripe there is no omit-for-full
// leg here.
//
// The returned status ("succeeded"|"pending"|"canceled") is passed through
// verbatim; the CALLER decides what a non-succeeded refund means for the
// order.
//
// Every call carries a fresh Idempotence-Key (YooKassa's spelling), mirroring
// CreatePayment: two adapter calls are two distinct money-out operations and
// the provider must never collapse the second into a replay of the first.
// Cross-call dedup is the caller's job via the ledger.
func (y *YooKassaPayment) CreateRefund(ctx context.Context, paymentID string, amountMinor int64, description string) (*RefundResult, error) {
	if !y.Configured() {
		return nil, ErrYooKassaNotConfigured
	}
	if !validYooKassaID(paymentID) || amountMinor <= 0 {
		return nil, ErrInvalidYooKassaReceipt
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	reqBody := yookassaRefundRequest{
		Amount:      yookassaAmount{Value: formatMinorUnits(amountMinor, 2), Currency: "rub"},
		Description: description,
		PaymentID:   paymentID,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("yookassa: marshal refund request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, y.baseURL+"/refunds", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("yookassa: create refund request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotence-Key", uuid.NewString())
	req.Header.Set("Authorization", y.basicAuth())

	rawBody, status, err := y.doJSON(req)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, yookassaAPIError(rawBody, status)
	}

	var refund yookassaRefundObject
	if err := json.Unmarshal(rawBody, &refund); err != nil {
		return nil, fmt.Errorf("yookassa: parse refund response: %w", err)
	}
	if refund.ID == "" {
		// Fail closed: recording an anonymous money-out movement is worse
		// than an error.
		return nil, errors.New("yookassa: API returned a refund without an id")
	}
	return &RefundResult{ID: refund.ID, Status: refund.Status}, nil
}

// GetPayment reads the authoritative payment state from the YooKassa API.
func (y *YooKassaPayment) GetPayment(ctx context.Context, paymentID string) (*Payment, error) {
	if !y.Configured() {
		return nil, ErrYooKassaNotConfigured
	}
	if !validYooKassaID(paymentID) {
		return nil, ErrInvalidYooKassaReceipt
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, y.baseURL+"/payments/"+url.PathEscape(paymentID), nil)
	if err != nil {
		return nil, fmt.Errorf("yookassa: get payment request: %w", err)
	}
	req.Header.Set("Authorization", y.basicAuth())

	rawBody, status, err := y.doJSON(req)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, yookassaAPIError(rawBody, status)
	}

	var object yookassaPaymentObject
	if err := json.Unmarshal(rawBody, &object); err != nil {
		return nil, fmt.Errorf("yookassa: parse payment response: %w", err)
	}
	return object.toPayment()
}

// ListPayments pages through payments matching the given filters. It backs
// the lost-webhook poller: an authenticated API call over TLS, the same
// authority class as GetPayment, so items are parsed by the same parser and
// feed the same receipt validation.
//
// A zero createdAtGte omits the created_at filter — scanning the full history
// is the caller's choice. An empty cursor omits the cursor param (first
// page); the returned next cursor is empty on the last page. limit is clamped
// to [1, 100], the page-size range the YooKassa API accepts.
func (y *YooKassaPayment) ListPayments(ctx context.Context, status string, createdAtGte time.Time, cursor string, limit int) ([]Payment, string, error) {
	if !y.Configured() {
		return nil, "", ErrYooKassaNotConfigured
	}

	if limit < 1 {
		limit = 1
	}
	if limit > yookassaMaxPageLimit {
		limit = yookassaMaxPageLimit
	}

	query := url.Values{}
	if status != "" {
		query.Set("status", status)
	}
	if !createdAtGte.IsZero() {
		query.Set("created_at.gte", createdAtGte.UTC().Format(time.RFC3339))
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	query.Set("limit", strconv.Itoa(limit))

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, y.baseURL+"/payments?"+query.Encode(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("yookassa: list payments request: %w", err)
	}
	req.Header.Set("Authorization", y.basicAuth())

	rawBody, httpStatus, err := y.doJSON(req)
	if err != nil {
		return nil, "", err
	}
	if httpStatus < http.StatusOK || httpStatus >= http.StatusMultipleChoices {
		return nil, "", yookassaAPIError(rawBody, httpStatus)
	}

	var list yookassaPaymentList
	if err := json.Unmarshal(rawBody, &list); err != nil {
		return nil, "", fmt.Errorf("yookassa: parse payment list response: %w", err)
	}

	items := make([]Payment, 0, len(list.Items))
	for _, object := range list.Items {
		payment, err := object.toPayment()
		if err != nil {
			// Fail closed: a malformed item means the API contract broke,
			// and silently skipping payments could lose a settlement.
			return nil, "", fmt.Errorf("yookassa: parse payment list item: %w", err)
		}
		items = append(items, *payment)
	}
	return items, list.NextCursor, nil
}

func (p yookassaPaymentObject) toPayment() (*Payment, error) {
	if p.ID == "" || !validYooKassaID(p.ID) {
		return nil, ErrInvalidYooKassaReceipt
	}
	orderID := int64(0)
	if raw, ok := p.Metadata["order_id"]; ok {
		parsed, err := parsePositiveProviderID(raw)
		if err == nil {
			orderID = parsed
		}
	}
	occurredAt, _ := parseYooKassaTime(p.CapturedAt)
	if occurredAt.IsZero() {
		occurredAt, _ = parseYooKassaTime(p.CreatedAt)
	}
	return &Payment{
		ID:         p.ID,
		Status:     p.Status,
		Paid:       p.Paid,
		Amount:     p.Amount.Value,
		Currency:   strings.ToUpper(p.Amount.Currency),
		OrderID:    orderID,
		OccurredAt: occurredAt,
	}, nil
}

// Payment is the authoritative snapshot of a YooKassa payment.
type Payment struct {
	ID         string
	Status     string
	Paid       bool
	Amount     string
	Currency   string
	OrderID    int64
	OccurredAt time.Time
}

// PaymentReceipt turns a succeeded payment into a ledger receipt. The amount
// must be an exact positive RUB value with at most two fractional digits.
func (p *Payment) PaymentReceipt() (shop.PaymentReceipt, error) {
	if p == nil || !p.Paid || p.Status != "succeeded" || p.Currency != "RUB" {
		return shop.PaymentReceipt{}, ErrInvalidYooKassaReceipt
	}
	amountMinor, scale, err := parsePositiveFixedDecimal(p.Amount, 2)
	if err != nil || scale != 2 || p.OrderID <= 0 || !validYooKassaID(p.ID) || p.OccurredAt.IsZero() {
		return shop.PaymentReceipt{}, ErrInvalidYooKassaReceipt
	}
	return shop.PaymentReceipt{
		OrderID: p.OrderID, Provider: storage.PaymentMethodYooKassa,
		ExternalID: p.ID, Currency: "RUB",
		AmountMinor: amountMinor, Scale: scale, OccurredAt: p.OccurredAt.UTC(),
	}, nil
}

// PaymentAnomaly preserves the factual part of a payment that cannot be
// turned into a valid order receipt.
func (p *Payment) PaymentAnomaly(reason string) (storage.PaymentAnomaly, error) {
	if p == nil || strings.TrimSpace(reason) == "" {
		return storage.PaymentAnomaly{}, ErrInvalidYooKassaReceipt
	}
	amount, scale, err := normalizeAnomalyAmount(p.Amount)
	if err != nil {
		amount, scale = 0, 0
	}
	return storage.PaymentAnomaly{
		ProposedOrderID: p.OrderID,
		Provider:        storage.PaymentMethodYooKassa,
		ExternalID:      p.ID,
		AmountMinor:     amount,
		Currency:        p.Currency,
		Scale:           scale,
		RawAmount:       p.Amount,
		RawPayload:      "payment_id:" + p.ID,
		Reason:          reason,
		OccurredAt:      p.OccurredAt,
	}, nil
}

// YooKassaNotification is the minimal envelope of a YooKassa webhook body.
type YooKassaNotification struct {
	Event     string
	PaymentID string
}

// ParseWebhook extracts the event and payment ID from a YooKassa webhook
// body. The object itself is intentionally ignored: only the API response is
// authoritative for an unsigned notification.
func (y *YooKassaPayment) ParseWebhook(body []byte) (*YooKassaNotification, error) {
	var envelope struct {
		Event  string                `json:"event"`
		Object yookassaPaymentObject `json:"object"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("yookassa: parse webhook body: %w", err)
	}
	if strings.TrimSpace(envelope.Event) == "" {
		return nil, errors.New("yookassa: webhook event is missing")
	}
	if envelope.Object.ID != "" && !validYooKassaID(envelope.Object.ID) {
		return nil, ErrInvalidYooKassaReceipt
	}
	return &YooKassaNotification{Event: envelope.Event, PaymentID: envelope.Object.ID}, nil
}

func (y *YooKassaPayment) basicAuth() string {
	credentials := base64.StdEncoding.EncodeToString([]byte(y.shopID + ":" + y.secretKey))
	return "Basic " + credentials
}

func (y *YooKassaPayment) doJSON(req *http.Request) ([]byte, int, error) {
	resp, err := y.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("yookassa: send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, yookassaResponseLimit+1))
	if err != nil {
		return nil, 0, fmt.Errorf("yookassa: read response: %w", err)
	}
	if len(body) > yookassaResponseLimit {
		return nil, 0, errors.New("yookassa: response is too large")
	}
	return body, resp.StatusCode, nil
}

func yookassaAPIError(body []byte, status int) error {
	var apiErr yookassaErrorResponse
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Code != "" {
		return fmt.Errorf("yookassa: API error %s: %s", apiErr.Code, apiErr.Description)
	}
	return fmt.Errorf("yookassa: HTTP status %d", status)
}

func validYooKassaID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_':
		default:
			return false
		}
	}
	return true
}

func parseYooKassaTime(raw string) (time.Time, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return time.Time{}, ErrInvalidYooKassaReceipt
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil || parsed.IsZero() {
		return time.Time{}, ErrInvalidYooKassaReceipt
	}
	return parsed.UTC(), nil
}

func formatMinorUnits(units int64, scale int) string {
	if scale <= 0 {
		return strconv.FormatInt(units, 10)
	}
	digits := strconv.FormatInt(units, 10)
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	return digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
}
