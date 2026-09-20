package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"crypto/subtle"
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

var (
	ErrNowpaymentsNotConfigured  = errors.New("nowpayments: credentials not configured")
	ErrInvalidNowpaymentsReceipt = errors.New("nowpayments: invalid payment receipt")

	ErrNowpaymentsSignatureMalformed = errors.New("nowpayments: malformed IPN signature")
	ErrNowpaymentsSignatureMismatch  = errors.New("nowpayments: IPN signature mismatch")
)

const nowpaymentsResponseLimit = 1 << 20

// NowpaymentsPayment handles USD payments via NOWPayments hosted invoices.
// IPN callbacks are HMAC-SHA512-signed over a canonicalized body: once the
// signature verifies, the body is authoritative and settles the order WITHOUT
// any API refetch (see ParseIPN).
type NowpaymentsPayment struct {
	apiKey     string
	ipnSecret  string
	returnURL  string
	webhookURL string
	baseURL    string
	client     *http.Client
}

// NewNowpaymentsPayment creates a new NowpaymentsPayment with the given API
// key, IPN signing secret, the URL the buyer returns to after paying and the
// IPN callback URL registered on every invoice.
func NewNowpaymentsPayment(apiKey, ipnSecret, returnURL, webhookURL string) *NowpaymentsPayment {
	return &NowpaymentsPayment{
		apiKey:     strings.TrimSpace(apiKey),
		ipnSecret:  strings.TrimSpace(ipnSecret),
		returnURL:  strings.TrimSpace(returnURL),
		webhookURL: strings.TrimSpace(webhookURL),
		baseURL:    "https://api.nowpayments.io/v1",
		client:     &http.Client{},
	}
}

// Configured reports whether the NOWPayments integration has usable
// credentials.
func (n *NowpaymentsPayment) Configured() bool {
	return n.apiKey != "" && n.ipnSecret != "" && n.returnURL != "" && n.webhookURL != ""
}

// SetBaseURL overrides the NOWPayments API base URL. Test seam only:
// bot/webapi/e2e tests live in other packages and cannot touch the
// unexported baseURL field.
func (n *NowpaymentsPayment) SetBaseURL(url string) { n.baseURL = url }

// nowpaymentsInvoiceRequest is the JSON body sent to the POST /v1/invoice
// endpoint. Unlike Stripe's form encoding, this API takes JSON.
type nowpaymentsInvoiceRequest struct {
	PriceAmount      float64 `json:"price_amount"`
	PriceCurrency    string  `json:"price_currency"`
	OrderID          string  `json:"order_id"`
	OrderDescription string  `json:"order_description"`
	IPNCallbackURL   string  `json:"ipn_callback_url"`
	SuccessURL       string  `json:"success_url"`
	CancelURL        string  `json:"cancel_url"`
}

// nowpaymentsErrorResponse covers both documented error shapes:
// {"status":false,"statusCode":...,"message":"..."} and {"message":"..."}.
type nowpaymentsErrorResponse struct {
	Message string `json:"message"`
}

// CreateInvoice registers a NOWPayments hosted invoice for the given order
// and returns its payment URL. The API has no idempotency-key header; the
// bot's one-create-per-order-tap flow applies, and the ledger quarantines a
// second successful charge for the same order.
func (n *NowpaymentsPayment) CreateInvoice(ctx context.Context, orderID int64, amountUSDCents int64, description string) (*Invoice, error) {
	if !n.Configured() {
		return nil, ErrNowpaymentsNotConfigured
	}
	if orderID <= 0 || amountUSDCents <= 0 {
		return nil, ErrInvalidNowpaymentsReceipt
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// price_amount is a float of dollars. Any realistic order total fits in
	// float53 cents, and the nearest float64 to cents/100 JSON-marshals to
	// the expected decimal spelling (1999 cents -> 19.99).
	reqBody := nowpaymentsInvoiceRequest{
		PriceAmount:      float64(amountUSDCents) / 100,
		PriceCurrency:    "usd",
		OrderID:          strconv.FormatInt(orderID, 10),
		OrderDescription: description,
		IPNCallbackURL:   n.webhookURL,
		SuccessURL:       n.returnURL,
		CancelURL:        n.returnURL,
	}
	encoded, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("nowpayments: encode create invoice request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.baseURL+"/invoice", bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("nowpayments: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", n.apiKey)

	rawBody, status, err := n.do(req)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, nowpaymentsAPIError(rawBody, status)
	}

	var invoice struct {
		ID         string `json:"id"`
		InvoiceURL string `json:"invoice_url"`
	}
	if err := json.Unmarshal(rawBody, &invoice); err != nil {
		return nil, fmt.Errorf("nowpayments: parse create invoice response: %w", err)
	}
	if invoice.ID == "" || strings.TrimSpace(invoice.InvoiceURL) == "" {
		return nil, errors.New("nowpayments: API returned an empty invoice URL")
	}
	return &Invoice{PayURL: invoice.InvoiceURL, InvoiceID: invoice.ID}, nil
}

// canonicalizeNowpaymentsIPN re-encodes a JSON body in the canonical form
// NOWPayments signs: object keys sorted (recursively — Go maps marshal with
// sorted keys), compact separators, no HTML escaping, no trailing newline.
//
// Known interop risk: the canonical form relies on Go's shortest-round-trip
// float formatting plus sorted keys plus no HTML escaping matching
// NOWPayments' own canonicalization. A mismatch fails CLOSED: signatures
// never verify and nothing settles.
func canonicalizeNowpaymentsIPN(body []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	// Encoder.Encode appends a newline that is not part of the canonical form.
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// VerifyIPNSignature validates the x-nowpayments-sig header against the raw
// IPN body. The signed payload is the canonicalized body, HMAC-SHA512 with
// the IPN secret, hex-encoded and compared in constant time. The scheme has
// no timestamp, so there is no replay tolerance check.
func (n *NowpaymentsPayment) VerifyIPNSignature(header string, body []byte) error {
	// Fail closed: an empty IPN secret would make every forged signature
	// valid, so verification refuses before any signature is compared.
	if n.ipnSecret == "" {
		return ErrNowpaymentsNotConfigured
	}

	header = strings.TrimSpace(header)
	if header == "" {
		return ErrNowpaymentsSignatureMalformed
	}
	provided, err := hex.DecodeString(header)
	if err != nil {
		return ErrNowpaymentsSignatureMalformed
	}

	canonical, err := canonicalizeNowpaymentsIPN(body)
	if err != nil {
		return ErrNowpaymentsSignatureMalformed
	}

	mac := hmac.New(sha512.New, []byte(n.ipnSecret))
	mac.Write(canonical)
	expected := mac.Sum(nil)
	if subtle.ConstantTimeCompare(provided, expected) != 1 {
		return ErrNowpaymentsSignatureMismatch
	}
	return nil
}

// NowpaymentsIPN is the authoritative snapshot of a NOWPayments IPN
// callback. The caller MUST have verified the IPN signature first; the
// parsed body is authoritative and is never refetched from the API.
type NowpaymentsIPN struct {
	PaymentID     string  // provider payment id as a decimal string
	PaymentStatus string  // waiting|confirming|confirmed|sending|partially_paid|finished|failed|refunded|expired
	OrderID       int64   // parsed from order_id; 0 when missing/unparsable
	PriceAmount   float64 // invoice price in dollars
	PriceCurrency string  // normalized to uppercase on read
}

// nowpaymentsIPNObject is the wire shape of an IPN callback. PaymentID is a
// RawMessage so the exact lexical form survives: ids are large integers and
// float64 decoding could drift or render them with an exponent.
type nowpaymentsIPNObject struct {
	PaymentID     json.RawMessage `json:"payment_id"`
	PaymentStatus string          `json:"payment_status"`
	OrderID       string          `json:"order_id"`
	PriceAmount   float64         `json:"price_amount"`
	PriceCurrency string          `json:"price_currency"`
}

// ParseIPN parses a signature-verified IPN body into a NowpaymentsIPN.
func (n *NowpaymentsPayment) ParseIPN(body []byte) (*NowpaymentsIPN, error) {
	var object nowpaymentsIPNObject
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, fmt.Errorf("nowpayments: parse IPN body: %w", err)
	}
	paymentID, err := parseNowpaymentsPaymentID(object.PaymentID)
	if err != nil {
		return nil, err
	}
	// An order id that does not parse stays zero; PaymentReceipt rejects it.
	orderID, _ := parsePositiveProviderID(object.OrderID)
	return &NowpaymentsIPN{
		PaymentID:     paymentID,
		PaymentStatus: object.PaymentStatus,
		OrderID:       orderID,
		PriceAmount:   object.PriceAmount,
		PriceCurrency: strings.ToUpper(strings.TrimSpace(object.PriceCurrency)),
	}, nil
}

// parseNowpaymentsPaymentID renders the provider's payment id as a decimal
// string. The id arrives as a JSON number (occasionally a quoted string);
// the raw lexical form is kept for integer spellings so large ids neither
// drift through float64 nor gain an exponent (5077125051 stays
// "5077125051", never "5.077125051e+09").
func parseNowpaymentsPaymentID(raw json.RawMessage) (string, error) {
	spelling := strings.TrimSpace(string(raw))
	if spelling == "" || spelling == "null" {
		return "", ErrInvalidNowpaymentsReceipt
	}
	if spelling[0] == '"' {
		var quoted string
		if err := json.Unmarshal(raw, &quoted); err != nil {
			return "", ErrInvalidNowpaymentsReceipt
		}
		spelling = strings.TrimSpace(quoted)
	} else if strings.ContainsAny(spelling, ".eE") {
		// Int-valued id spelled with a fraction or exponent: normalize to a
		// plain decimal string.
		value, err := strconv.ParseFloat(spelling, 64)
		if err != nil {
			return "", ErrInvalidNowpaymentsReceipt
		}
		spelling = strconv.FormatFloat(value, 'f', -1, 64)
	}
	if spelling == "" {
		return "", ErrInvalidNowpaymentsReceipt
	}
	return spelling, nil
}

// PaymentReceipt turns a settled IPN into a ledger receipt. An IPN settles
// only when its status is finished, in USD, with a positive amount and a
// resolvable order reference.
func (n *NowpaymentsIPN) PaymentReceipt() (shop.PaymentReceipt, error) {
	if n == nil || n.PaymentStatus != "finished" || n.PriceCurrency != "USD" {
		return shop.PaymentReceipt{}, ErrInvalidNowpaymentsReceipt
	}
	amountMinor := int64(math.Round(n.PriceAmount * 100))
	if amountMinor <= 0 || n.OrderID <= 0 || strings.TrimSpace(n.PaymentID) == "" {
		return shop.PaymentReceipt{}, ErrInvalidNowpaymentsReceipt
	}
	return shop.PaymentReceipt{
		OrderID: n.OrderID, Provider: storage.PaymentMethodNowpayments,
		ExternalID: n.PaymentID,
		// NOWPayments has no Telegram payer identity; PayerID stays zero.
		PayerID: 0, Currency: "USD",
		AmountMinor: amountMinor, Scale: 2,
	}, nil
}

// PaymentAnomaly preserves the factual part of an IPN that cannot be turned
// into a valid order receipt.
func (n *NowpaymentsIPN) PaymentAnomaly(reason string) (storage.PaymentAnomaly, error) {
	if n == nil || strings.TrimSpace(reason) == "" {
		return storage.PaymentAnomaly{}, ErrInvalidNowpaymentsReceipt
	}
	amount, scale := int64(math.Round(n.PriceAmount*100)), 2
	if amount <= 0 {
		amount, scale = 0, 0
	}
	return storage.PaymentAnomaly{
		ProposedOrderID: n.OrderID,
		Provider:        storage.PaymentMethodNowpayments,
		ExternalID:      n.PaymentID,
		AmountMinor:     amount,
		Currency:        n.PriceCurrency,
		Scale:           scale,
		// Keep the provider's dollar amount spelling as the raw amount
		// (mirrors Stripe/YooKassa preserving the provider's amount).
		RawAmount:  strconv.FormatFloat(n.PriceAmount, 'f', -1, 64),
		RawPayload: "payment_id:" + n.PaymentID,
		Reason:     reason,
	}, nil
}

func (n *NowpaymentsPayment) do(req *http.Request) ([]byte, int, error) {
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("nowpayments: send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, nowpaymentsResponseLimit+1))
	if err != nil {
		return nil, 0, fmt.Errorf("nowpayments: read response: %w", err)
	}
	if len(body) > nowpaymentsResponseLimit {
		return nil, 0, errors.New("nowpayments: response is too large")
	}
	return body, resp.StatusCode, nil
}

// nowpaymentsAPIError maps a non-2xx response to an error carrying the HTTP
// status and the provider's error message. The API key is never included.
func nowpaymentsAPIError(body []byte, status int) error {
	var apiErr nowpaymentsErrorResponse
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Message != "" {
		return fmt.Errorf("nowpayments: HTTP status %d: %s", status, apiErr.Message)
	}
	return fmt.Errorf("nowpayments: HTTP status %d", status)
}
