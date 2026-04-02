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
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrCryptoBotNotConfigured reports that CryptoBot-dependent operations were
// requested without an API token.
var ErrCryptoBotNotConfigured = errors.New("cryptobot: token not configured")

// Invoice represents a payment invoice returned by a payment provider.
type Invoice struct {
	PayURL    string
	InvoiceID string
}

// WebhookPayload represents the parsed body of a CryptoBot webhook callback.
type WebhookPayload struct {
	InvoiceID string
	Status    string
	OrderID   int64
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
		InvoiceID int64  `json:"invoice_id"`
		PayURL    string `json:"pay_url"`
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
		Asset:       "USDT",
		Amount:      strconv.FormatFloat(amountUSD, 'f', 2, 64),
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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cryptobot: read response: %w", err)
	}

	var apiResp createInvoiceResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("cryptobot: parse response: %w", err)
	}

	if !apiResp.OK {
		return nil, fmt.Errorf("cryptobot: API error %d: %s", apiResp.Error.Code, apiResp.Error.Name)
	}

	return &Invoice{
		PayURL:    apiResp.Result.PayURL,
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
}

// PendingInvoice represents an active (unpaid) invoice returned by GetInvoices.
type PendingInvoice struct {
	InvoiceID string
	Status    string
	OrderID   int64
}

// getInvoicesResponse represents the CryptoBot API response for getInvoices.
type getInvoicesResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		Items []struct {
			InvoiceID int64  `json:"invoice_id"`
			Status    string `json:"status"`
			Payload   string `json:"payload"`
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
	if !c.Configured() {
		return nil, ErrCryptoBotNotConfigured
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/getInvoices?status=%s", c.baseURL, status)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cryptobot: getInvoices create request: %w", err)
	}
	req.Header.Set("Crypto-Pay-API-Token", c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cryptobot: getInvoices request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cryptobot: getInvoices read response: %w", err)
	}

	var apiResp getInvoicesResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("cryptobot: getInvoices parse response: %w", err)
	}
	if !apiResp.OK {
		return nil, fmt.Errorf("cryptobot: getInvoices API error %d: %s", apiResp.Error.Code, apiResp.Error.Name)
	}

	invoices := make([]PendingInvoice, 0, len(apiResp.Result.Items))
	for _, item := range apiResp.Result.Items {
		orderID, err := strconv.ParseInt(item.Payload, 10, 64)
		if err != nil {
			continue // skip malformed payloads
		}
		invoices = append(invoices, PendingInvoice{
			InvoiceID: strconv.FormatInt(item.InvoiceID, 10),
			Status:    item.Status,
			OrderID:   orderID,
		})
	}
	return invoices, nil
}

// ParseWebhook parses the raw webhook body into a WebhookPayload.
func (c *CryptoBotPayment) ParseWebhook(body []byte) (*WebhookPayload, error) {
	var wb webhookBody
	if err := json.Unmarshal(body, &wb); err != nil {
		return nil, fmt.Errorf("cryptobot: parse webhook body: %w", err)
	}

	var inv webhookInvoice
	if err := json.Unmarshal(wb.Payload, &inv); err != nil {
		return nil, fmt.Errorf("cryptobot: parse webhook payload: %w", err)
	}

	orderID, err := strconv.ParseInt(inv.Payload, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("cryptobot: parse order ID from payload: %w", err)
	}

	return &WebhookPayload{
		InvoiceID: strconv.FormatInt(inv.InvoiceID, 10),
		Status:    inv.Status,
		OrderID:   orderID,
	}, nil
}
