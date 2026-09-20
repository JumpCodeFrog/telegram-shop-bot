package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"shop_bot/internal/shop"
	"shop_bot/internal/storage"
)

var (
	ErrTONNotConfigured  = errors.New("ton: wallet address not configured")
	ErrInvalidTONReceipt = errors.New("ton: invalid payment receipt")
)

const tonResponseLimit = 1 << 20

// tonOrderCommentRE pins the exact transfer-comment format. Wallets prefill
// the comment via the TransferLink deeplink; manual payers must copy it
// exactly (the instructions message shows it in a code block). No spaces, no
// case folding, nothing before or after.
var tonOrderCommentRE = regexp.MustCompile(`^order-(\d+)$`)

// TONPayment watches a TON wallet for inbound transfers via the toncenter
// v2 API. There is no webhook or signature: the poller (service layer)
// refetches transactions and the ledger settles each one by its
// lt:hash external id, so duplicates are quarantined downstream.
type TONPayment struct {
	walletAddress string
	apiKey        string
	baseURL       string
	client        *http.Client
}

// NewTONPayment creates a new TONPayment watching the given wallet address.
// The toncenter API key is optional — without one the API is just
// rate-limited harder — so Configured only requires the address.
func NewTONPayment(walletAddress, apiKey string) *TONPayment {
	return &TONPayment{
		walletAddress: strings.TrimSpace(walletAddress),
		apiKey:        strings.TrimSpace(apiKey),
		baseURL:       "https://toncenter.com",
		client:        &http.Client{},
	}
}

// Configured reports whether the TON integration has a wallet to watch.
func (t *TONPayment) Configured() bool {
	return t.walletAddress != ""
}

// SetBaseURL overrides the toncenter API base URL. Test seam only:
// bot/webapi/e2e tests live in other packages and cannot touch the
// unexported baseURL field.
func (t *TONPayment) SetBaseURL(url string) { t.baseURL = url }

// TONTransaction is the parsed snapshot of an inbound wallet transfer.
// LT stays a string: values exceed int32, and while int64 would fit, the
// pair is only ever used as the ExternalID "<lt>:<hash>" — never in
// arithmetic — so the lossless lexical form is kept.
type TONTransaction struct {
	LT        string
	Hash      string
	Source    string
	ValueNano int64 // nanotons (TON minor units, scale 9), parsed from the API's string value
	Comment   string
	Utime     int64
}

// tonGetTransactionsResponse is the wire shape of the toncenter v2
// getTransactions response (controller-verified against the live API,
// 2026-09-20). Nanoton values and lt arrive as strings; comments arrive as
// msg.dataText (plain text) or msg.dataRaw (binary, skipped).
type tonGetTransactionsResponse struct {
	OK     bool `json:"ok"`
	Result []struct {
		TransactionID struct {
			LT   string `json:"lt"`
			Hash string `json:"hash"`
		} `json:"transaction_id"`
		Utime int64 `json:"utime"`
		InMsg struct {
			Source  string `json:"source"`
			Value   string `json:"value"`
			MsgData struct {
				Type string `json:"@type"`
				Text string `json:"text"`
			} `json:"msg_data"`
		} `json:"in_msg"`
	} `json:"result"`
	Error string `json:"error"`
	Code  int    `json:"code"`
}

// GetTransactions fetches the wallet's latest transactions and returns the
// parsed inbound transfers that carry a plain-text comment. Binary (dataRaw)
// messages and transactions with unparsable or negative values are skipped:
// they can never become an order receipt, and one malformed entry must not
// kill the whole poll batch.
func (t *TONPayment) GetTransactions(ctx context.Context, limit int) ([]TONTransaction, error) {
	if !t.Configured() {
		return nil, ErrTONNotConfigured
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	query := url.Values{}
	query.Set("address", t.walletAddress)
	query.Set("limit", strconv.Itoa(limit))
	// The API key is only sent when present — toncenter answers keyless
	// requests (with tighter rate limits) and an empty api_key param is a
	// 400-class error on some deployments.
	if t.apiKey != "" {
		query.Set("api_key", t.apiKey)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/api/v2/getTransactions?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("ton: get transactions request: %w", err)
	}

	rawBody, status, err := t.do(req)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, tonAPIError(rawBody, status)
	}

	var response tonGetTransactionsResponse
	if err := json.Unmarshal(rawBody, &response); err != nil {
		return nil, fmt.Errorf("ton: parse transactions response: %w", err)
	}
	if !response.OK {
		// toncenter sometimes spells failures as HTTP 200 + ok:false.
		return nil, tonAPIError(rawBody, status)
	}

	transactions := make([]TONTransaction, 0, len(response.Result))
	for _, raw := range response.Result {
		if raw.InMsg.MsgData.Type != "msg.dataText" {
			slog.Debug("ton: skipping transaction without text comment",
				"lt", raw.TransactionID.LT, "msgDataType", raw.InMsg.MsgData.Type)
			continue
		}
		value, err := strconv.ParseInt(raw.InMsg.Value, 10, 64)
		if err != nil || value < 0 {
			slog.Debug("ton: skipping transaction with unparsable value",
				"lt", raw.TransactionID.LT, "value", raw.InMsg.Value)
			continue
		}
		transactions = append(transactions, TONTransaction{
			LT:        raw.TransactionID.LT,
			Hash:      raw.TransactionID.Hash,
			Source:    raw.InMsg.Source,
			ValueNano: value,
			Comment:   raw.InMsg.MsgData.Text,
			Utime:     raw.Utime,
		})
	}
	return transactions, nil
}

// ParseOrderComment extracts the order id from an exact "order-<id>"
// transfer comment. ok is false for anything else, including zero,
// overflowing and non-numeric ids.
func ParseOrderComment(comment string) (orderID int64, ok bool) {
	match := tonOrderCommentRE.FindStringSubmatch(comment)
	if match == nil {
		return 0, false
	}
	orderID, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || orderID <= 0 {
		return 0, false
	}
	return orderID, true
}

// TransferLink builds the ton:// deeplink that opens the buyer's wallet with
// address, amount and order comment prefilled. Amount is decimal nanotons;
// the comment part is always "order-<digits>", which is URL-safe as-is.
func (t *TONPayment) TransferLink(nano int64, orderID int64) string {
	return "ton://transfer/" + t.walletAddress +
		"?amount=" + strconv.FormatInt(nano, 10) +
		"&text=order-" + strconv.FormatInt(orderID, 10)
}

// PaymentReceipt turns an inbound transfer into a ledger receipt. A
// transfer settles only when its comment is an exact order reference, the
// value is positive and the lt:hash pair (the ledger external id) is
// complete. TON has no Telegram payer identity; PayerID stays zero.
func (tx TONTransaction) PaymentReceipt() (shop.PaymentReceipt, error) {
	orderID, ok := ParseOrderComment(tx.Comment)
	if !ok {
		return shop.PaymentReceipt{}, ErrInvalidTONReceipt
	}
	if tx.ValueNano <= 0 || strings.TrimSpace(tx.LT) == "" || strings.TrimSpace(tx.Hash) == "" {
		return shop.PaymentReceipt{}, ErrInvalidTONReceipt
	}
	return shop.PaymentReceipt{
		OrderID: orderID, Provider: storage.PaymentMethodTON,
		ExternalID: tx.LT + ":" + tx.Hash,
		PayerID:    0, Currency: "TON",
		AmountMinor: tx.ValueNano, Scale: 9,
		OccurredAt: time.Unix(tx.Utime, 0).UTC(),
	}, nil
}

func (t *TONPayment) do(req *http.Request) ([]byte, int, error) {
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("ton: send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, tonResponseLimit+1))
	if err != nil {
		return nil, 0, fmt.Errorf("ton: read response: %w", err)
	}
	if len(body) > tonResponseLimit {
		return nil, 0, errors.New("ton: response is too large")
	}
	return body, resp.StatusCode, nil
}

// tonAPIError maps an error response to an error carrying the HTTP status
// and toncenter's error message. The API key is never included.
func tonAPIError(body []byte, status int) error {
	var apiErr tonGetTransactionsResponse
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error != "" {
		return fmt.Errorf("ton: HTTP status %d: %s", status, apiErr.Error)
	}
	return fmt.Errorf("ton: HTTP status %d", status)
}
