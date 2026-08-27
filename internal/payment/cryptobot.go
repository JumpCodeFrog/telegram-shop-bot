package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

// ErrCryptoBotNotConfigured reports that CryptoBot-dependent operations were
// requested without an API token.
var (
	ErrCryptoBotNotConfigured = errors.New("cryptobot: token not configured")
	ErrInvalidCryptoReceipt   = errors.New("cryptobot: invalid paid invoice receipt")
	ErrCryptoInvoiceWindow    = errors.New("cryptobot: paid invoice window is incomplete")
)

const cryptoResponseLimit = 1 << 20

// Invoice represents a payment invoice returned by a payment provider.
type Invoice struct {
	PayURL    string
	InvoiceID string
}

// WebhookPayload represents the parsed body of a CryptoBot webhook callback.
type WebhookPayload struct {
	InvoiceID       string
	Status          string
	OrderID         int64
	Payload         string
	Asset           string
	Amount          string
	AmountMinor     int64
	PaidAt          string
	OccurredAt      time.Time
	ReceiptComplete bool
}

// CryptoBotPayment handles USDT payments via the CryptoBot API.
type CryptoBotPayment struct {
	token   string
	baseURL string
	client  *http.Client
}

// NewCryptoBotPayment creates a new CryptoBotPayment with the given API token.
func NewCryptoBotPayment(token string) *CryptoBotPayment {
	return &CryptoBotPayment{
		token:   token,
		baseURL: "https://pay.crypt.bot/api",
		client:  &http.Client{},
	}
}

// Configured reports whether the CryptoBot integration has a usable API token.
func (c *CryptoBotPayment) Configured() bool {
	return strings.TrimSpace(c.token) != ""
}

// createInvoiceRequest is the JSON body sent to the CryptoBot createInvoice endpoint.
type createInvoiceRequest struct {
	Asset       string `json:"asset"`
	Amount      string `json:"amount"`
	Description string `json:"description"`
	Payload     string `json:"payload"`
}

// createInvoiceResponse represents the CryptoBot API response for createInvoice.
type createInvoiceResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		InvoiceID     int64  `json:"invoice_id"`
		PayURL        string `json:"pay_url"`
		BotInvoiceURL string `json:"bot_invoice_url"`
	} `json:"result"`
	Error struct {
		Code int    `json:"code"`
		Name string `json:"name"`
	} `json:"error"`
}

// CreateInvoice sends a POST request to CryptoBot to create a USDT invoice.
// It uses a 10-second timeout derived from the provided context.
func (c *CryptoBotPayment) CreateInvoice(ctx context.Context, orderID int64, amountUSD float64, description string) (*Invoice, error) {
	if !c.Configured() {
		return nil, ErrCryptoBotNotConfigured
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	reqBody := createInvoiceRequest{
		Asset: "USDT",
		// Use the same integer-minor-unit rounding as receipt validation. Direct
		// FormatFloat rounding can produce a different invoice for binary floats
		// such as 2.675 ("2.67" here while the ledger expects 268 cents).
		Amount:      strconv.FormatFloat(float64(math.Round(amountUSD*100))/100, 'f', 2, 64),
		Description: description,
		Payload:     strconv.FormatInt(orderID, 10),
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("cryptobot: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/createInvoice", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cryptobot: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Crypto-Pay-API-Token", c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cryptobot: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("cryptobot: HTTP status %d", resp.StatusCode)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, cryptoResponseLimit+1))
	if err != nil {
		return nil, fmt.Errorf("cryptobot: read response: %w", err)
	}
	if len(respBody) > cryptoResponseLimit {
		return nil, errors.New("cryptobot: response is too large")
	}

	var apiResp createInvoiceResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("cryptobot: parse response: %w", err)
	}

	if !apiResp.OK {
		return nil, fmt.Errorf("cryptobot: API error %d: %s", apiResp.Error.Code, apiResp.Error.Name)
	}

	payURL := apiResp.Result.BotInvoiceURL
	if payURL == "" {
		payURL = apiResp.Result.PayURL
	}
	if apiResp.Result.InvoiceID <= 0 || strings.TrimSpace(payURL) == "" {
		return nil, fmt.Errorf("cryptobot: API returned an empty invoice URL")
	}
	return &Invoice{
		PayURL:    payURL,
		InvoiceID: strconv.FormatInt(apiResp.Result.InvoiceID, 10),
	}, nil
}

// VerifyWebhook checks the HMAC-SHA256 signature of a CryptoBot webhook request.
// The key is SHA256(token) and the MAC is computed over the raw body.
func (c *CryptoBotPayment) VerifyWebhook(body []byte, signature string) bool {
	if !c.Configured() {
		return false
	}

	secret := sha256.Sum256([]byte(c.token))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// webhookBody represents the top-level structure of a CryptoBot webhook request.
type webhookBody struct {
	UpdateType string          `json:"update_type"`
	Payload    json.RawMessage `json:"payload"`
}

// webhookInvoice represents the invoice payload inside a CryptoBot webhook.
type webhookInvoice struct {
	InvoiceID int64  `json:"invoice_id"`
	Status    string `json:"status"`
	Payload   string `json:"payload"`
	Asset     string `json:"asset"`
	Amount    string `json:"amount"`
	PaidAt    string `json:"paid_at"`
}

// PendingInvoice represents an active (unpaid) invoice returned by GetInvoices.
type PendingInvoice struct {
	InvoiceID  string
	Status     string
	OrderID    int64
	Payload    string
	Asset      string
	Amount     string
	PaidAt     string
	OccurredAt time.Time
}

func (p PendingInvoice) PaymentReceipt() (shop.PaymentReceipt, error) {
	amountMinor, err := parseStrictProviderAmount(p.Amount)
	invoiceID, invoiceIDErr := parsePositiveProviderID(p.InvoiceID)
	if p.OrderID <= 0 || p.Asset != "USDT" || err != nil || invoiceIDErr != nil || invoiceID <= 0 || p.OccurredAt.IsZero() {
		return shop.PaymentReceipt{}, ErrInvalidCryptoReceipt
	}
	return shop.PaymentReceipt{
		OrderID: p.OrderID, Provider: storage.PaymentMethodCrypto,
		ExternalID: p.InvoiceID, Currency: p.Asset,
		AmountMinor: amountMinor, Scale: 2, OccurredAt: p.OccurredAt.UTC(),
	}, nil
}

// PaymentAnomaly preserves the factual part of a paid invoice that cannot be
// turned into a valid order receipt (for example, a malformed payload).
func (p PendingInvoice) PaymentAnomaly(reason string) (storage.PaymentAnomaly, error) {
	amount, scale, err := normalizeAnomalyAmount(p.Amount)
	if strings.TrimSpace(reason) == "" {
		return storage.PaymentAnomaly{}, ErrInvalidCryptoReceipt
	}
	if err != nil {
		amount, scale = 0, 0
	}
	return storage.PaymentAnomaly{
		ProposedOrderID: p.OrderID,
		Provider:        storage.PaymentMethodCrypto,
		ExternalID:      p.InvoiceID,
		AmountMinor:     amount,
		Currency:        p.Asset,
		Scale:           scale,
		RawAmount:       p.Amount,
		RawPayload:      cryptoAnomalyPayload(p.Payload, p.PaidAt),
		Reason:          reason,
		OccurredAt:      p.OccurredAt,
	}, nil
}

func cryptoAnomalyPayload(payload, paidAt string) string {
	parts := make([]string, 0, 2)
	if payload != "" {
		parts = append(parts, "invoice_payload:"+payload)
	}
	if paidAt != "" {
		parts = append(parts, "paid_at:"+paidAt)
	}
	return strings.Join(parts, "\n")
}

// parseStrictProviderAmount converts a provider amount to scale 2 without
// floating point or rounding. Only an unsigned, positive fixed decimal with at
// most two fractional digits is accepted; signs, whitespace, exponent notation
// and precision that would otherwise be rounded are rejected.
func parseStrictProviderAmount(raw string) (int64, error) {
	units, scale, err := parsePositiveFixedDecimal(raw, 2)
	if err != nil {
		return 0, err
	}
	for scale < 2 {
		if units > int64(^uint64(0)>>1)/10 {
			return 0, ErrInvalidCryptoReceipt
		}
		units *= 10
		scale++
	}
	return units, nil
}

// normalizeAnomalyAmount preserves an exact positive amount in the anomaly
// schema's (amount_minor, scale) representation. The receipt parser remains
// deliberately stricter. Exponent notation is normalized only for quarantine,
// while the original spelling is retained in PaymentAnomaly.Reason.
func normalizeAnomalyAmount(raw string) (int64, int, error) {
	if units, scale, err := parsePositiveFixedDecimal(raw, 9); err == nil {
		return units, scale, nil
	}
	if !strings.ContainsAny(raw, "eE") {
		dot := strings.IndexByte(raw, '.')
		if dot > 0 && dot < len(raw)-1 && strings.IndexByte(raw[dot+1:], '.') < 0 {
			fraction := strings.TrimRight(raw[dot+1:], "0")
			if len(fraction) <= 9 {
				normalized := raw[:dot]
				if fraction != "" {
					normalized += "." + fraction
				}
				if units, scale, err := parsePositiveFixedDecimal(normalized, 9); err == nil {
					return units, scale, nil
				}
			}
		}
		return 0, 0, ErrInvalidCryptoReceipt
	}

	exponentAt := strings.IndexAny(raw, "eE")
	if exponentAt <= 0 || strings.IndexAny(raw[exponentAt+1:], "eE") >= 0 {
		return 0, 0, ErrInvalidCryptoReceipt
	}
	mantissa := raw[:exponentAt]
	exponentText := raw[exponentAt+1:]
	if exponentText == "" {
		return 0, 0, ErrInvalidCryptoReceipt
	}
	if exponentText[0] == '+' || exponentText[0] == '-' {
		if len(exponentText) == 1 {
			return 0, 0, ErrInvalidCryptoReceipt
		}
	} else if exponentText[0] < '0' || exponentText[0] > '9' {
		return 0, 0, ErrInvalidCryptoReceipt
	}
	for _, ch := range exponentText[1:] {
		if ch < '0' || ch > '9' {
			return 0, 0, ErrInvalidCryptoReceipt
		}
	}
	exponent, err := strconv.Atoi(exponentText)
	if err != nil || exponent < -100 || exponent > 100 {
		return 0, 0, ErrInvalidCryptoReceipt
	}

	dot := strings.IndexByte(mantissa, '.')
	fractionDigits := 0
	digits := mantissa
	if dot >= 0 {
		if dot == 0 || dot == len(mantissa)-1 || strings.IndexByte(mantissa[dot+1:], '.') >= 0 {
			return 0, 0, ErrInvalidCryptoReceipt
		}
		fractionDigits = len(mantissa) - dot - 1
		digits = mantissa[:dot] + mantissa[dot+1:]
	}
	if digits == "" {
		return 0, 0, ErrInvalidCryptoReceipt
	}
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return 0, 0, ErrInvalidCryptoReceipt
		}
	}

	scale := fractionDigits - exponent
	if scale < 0 {
		if -scale > 18 || len(digits)-scale > 19 {
			return 0, 0, ErrInvalidCryptoReceipt
		}
		digits += strings.Repeat("0", -scale)
		scale = 0
	}
	for scale > 9 && strings.HasSuffix(digits, "0") {
		digits = strings.TrimSuffix(digits, "0")
		scale--
	}
	if scale > 9 {
		return 0, 0, ErrInvalidCryptoReceipt
	}
	units, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || units <= 0 {
		return 0, 0, ErrInvalidCryptoReceipt
	}
	return units, scale, nil
}

func parsePositiveFixedDecimal(raw string, maxFractionDigits int) (int64, int, error) {
	if raw == "" {
		return 0, 0, ErrInvalidCryptoReceipt
	}
	dot := strings.IndexByte(raw, '.')
	integerPart := raw
	fractionPart := ""
	if dot >= 0 {
		if dot == 0 || dot == len(raw)-1 || strings.IndexByte(raw[dot+1:], '.') >= 0 {
			return 0, 0, ErrInvalidCryptoReceipt
		}
		integerPart, fractionPart = raw[:dot], raw[dot+1:]
	}
	if len(fractionPart) > maxFractionDigits {
		return 0, 0, ErrInvalidCryptoReceipt
	}
	for _, ch := range integerPart + fractionPart {
		if ch < '0' || ch > '9' {
			return 0, 0, ErrInvalidCryptoReceipt
		}
	}
	units, err := strconv.ParseInt(integerPart+fractionPart, 10, 64)
	if err != nil || units <= 0 {
		return 0, 0, ErrInvalidCryptoReceipt
	}
	return units, len(fractionPart), nil
}

func parsePositiveProviderID(raw string) (int64, error) {
	if raw == "" {
		return 0, ErrInvalidCryptoReceipt
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, ErrInvalidCryptoReceipt
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, ErrInvalidCryptoReceipt
	}
	return value, nil
}

// getInvoicesResponse represents the CryptoBot API response for getInvoices.
type getInvoicesResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		Items []struct {
			InvoiceID int64  `json:"invoice_id"`
			Status    string `json:"status"`
			Payload   string `json:"payload"`
			Asset     string `json:"asset"`
			Amount    string `json:"amount"`
			PaidAt    string `json:"paid_at"`
		} `json:"items"`
	} `json:"result"`
	Error struct {
		Code int    `json:"code"`
		Name string `json:"name"`
	} `json:"error"`
}

// GetInvoices fetches invoices with the given status from the CryptoBot API.
// Used by the polling worker as a webhook fallback.
func (c *CryptoBotPayment) GetInvoices(ctx context.Context, status string) ([]PendingInvoice, error) {
	invoices, _, err := c.GetInvoicesWindow(ctx, status, 0)
	return invoices, err
}

// GetInvoicesWindow fetches at most 1000 invoices beginning at startOffset.
// When more rows exist it returns ErrCryptoInvoiceWindow and a strictly larger
// continuation offset. This keeps each request bounded while allowing the
// polling worker to reach every page instead of retrying offsets 0..999 forever.
func (c *CryptoBotPayment) GetInvoicesWindow(ctx context.Context, status string, startOffset int) ([]PendingInvoice, int, error) {
	if !c.Configured() {
		return nil, 0, ErrCryptoBotNotConfigured
	}
	if startOffset < 0 {
		return nil, 0, fmt.Errorf("cryptobot: invalid invoice start offset %d", startOffset)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	const maxInvoices = 1000
	const pageSize = 100
	var invoices []PendingInvoice
	for relativeOffset := 0; relativeOffset < maxInvoices; relativeOffset += pageSize {
		offset := startOffset + relativeOffset
		page, rawCount, err := c.getInvoicesPage(ctx, status, offset, pageSize)
		if err != nil {
			return nil, 0, err
		}
		invoices = append(invoices, page...)
		if rawCount < pageSize {
			return invoices, 0, nil
		}
	}
	// A one-item probe distinguishes an exact 1000-item result from a result
	// that exceeds our bounded scan. The extra invoice is deliberately not
	// returned without the preceding continuation contract: callers receive a
	// partial window plus a sentinel and can alert/reconcile explicitly.
	nextOffset := startOffset + maxInvoices
	_, rawCount, err := c.getInvoicesPage(ctx, status, nextOffset, 1)
	if err != nil {
		return invoices, 0, fmt.Errorf("%w: continuation probe: %v", ErrCryptoInvoiceWindow, err)
	}
	if rawCount == 0 {
		return invoices, 0, nil
	}
	return invoices, nextOffset, fmt.Errorf("%w: more than %d invoices from offset %d", ErrCryptoInvoiceWindow, maxInvoices, startOffset)
}

func (c *CryptoBotPayment) getInvoicesPage(ctx context.Context, status string, offset, count int) ([]PendingInvoice, int, error) {
	requestURL := fmt.Sprintf("%s/getInvoices?status=%s&offset=%d&count=%d", c.baseURL, status, offset, count)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("cryptobot: getInvoices create request: %w", err)
	}
	req.Header.Set("Crypto-Pay-API-Token", c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cryptobot: getInvoices request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, 0, fmt.Errorf("cryptobot: getInvoices HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cryptoResponseLimit+1))
	if err != nil {
		return nil, 0, fmt.Errorf("cryptobot: getInvoices read response: %w", err)
	}
	if len(body) > cryptoResponseLimit {
		return nil, 0, errors.New("cryptobot: getInvoices response is too large")
	}

	var apiResp getInvoicesResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, 0, fmt.Errorf("cryptobot: getInvoices parse response: %w", err)
	}
	if !apiResp.OK {
		return nil, 0, fmt.Errorf("cryptobot: getInvoices API error %d: %s", apiResp.Error.Code, apiResp.Error.Name)
	}

	rawCount := len(apiResp.Result.Items)
	invoices := make([]PendingInvoice, 0, rawCount)
	for _, item := range apiResp.Result.Items {
		orderID, err := parsePositiveProviderID(item.Payload)
		if err != nil || orderID <= 0 {
			orderID = 0
		}
		occurredAt, _ := parseCryptoPaidAt(item.PaidAt)
		invoices = append(invoices, PendingInvoice{
			InvoiceID:  strconv.FormatInt(item.InvoiceID, 10),
			Status:     item.Status,
			OrderID:    orderID,
			Payload:    item.Payload,
			Asset:      item.Asset,
			Amount:     item.Amount,
			PaidAt:     item.PaidAt,
			OccurredAt: occurredAt,
		})
	}
	return invoices, rawCount, nil
}

// ParseWebhook parses the raw webhook body into a WebhookPayload.
func (c *CryptoBotPayment) ParseWebhook(body []byte) (*WebhookPayload, error) {
	var wb webhookBody
	if err := json.Unmarshal(body, &wb); err != nil {
		return nil, fmt.Errorf("cryptobot: parse webhook body: %w", err)
	}
	if wb.UpdateType != "invoice_paid" {
		return nil, fmt.Errorf("cryptobot: unexpected update type %q", wb.UpdateType)
	}

	var inv webhookInvoice
	if err := json.Unmarshal(wb.Payload, &inv); err != nil {
		return nil, fmt.Errorf("cryptobot: parse webhook payload: %w", err)
	}

	orderID, payloadErr := parsePositiveProviderID(inv.Payload)
	if payloadErr != nil || orderID <= 0 {
		orderID = 0
	}

	amountMinor, amountErr := parseStrictProviderAmount(inv.Amount)
	occurredAt, paidAtErr := parseCryptoPaidAt(inv.PaidAt)
	return &WebhookPayload{
		InvoiceID:       strconv.FormatInt(inv.InvoiceID, 10),
		Status:          inv.Status,
		OrderID:         orderID,
		Payload:         inv.Payload,
		Asset:           inv.Asset,
		Amount:          inv.Amount,
		AmountMinor:     amountMinor,
		PaidAt:          inv.PaidAt,
		OccurredAt:      occurredAt,
		ReceiptComplete: inv.InvoiceID > 0 && orderID > 0 && inv.Asset == "USDT" && amountErr == nil && paidAtErr == nil,
	}, nil
}

func parseCryptoPaidAt(raw string) (time.Time, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return time.Time{}, ErrInvalidCryptoReceipt
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil || parsed.IsZero() {
		return time.Time{}, ErrInvalidCryptoReceipt
	}
	return parsed.UTC(), nil
}
