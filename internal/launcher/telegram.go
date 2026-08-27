package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const telegramAPIEndpoint = "https://api.telegram.org/bot%s/%s"

var ErrTelegramCheck = errors.New("Telegram rejected the token or could not be reached")

type BotIdentity struct {
	ID                    int64
	FirstName             string
	Username              string
	SupportsInlineQueries bool
}

type TelegramState struct {
	Identity           BotIdentity
	WebhookURL         string
	PendingUpdateCount int
	LastErrorMessage   string
}

type TelegramVerifier interface {
	Verify(context.Context, string) (BotIdentity, error)
}

type TelegramInspector interface {
	Inspect(context.Context, string) (TelegramState, error)
}

type StarTransactionPartner struct {
	Type            string `json:"type"`
	TransactionType string `json:"transaction_type"`
	InvoicePayload  string `json:"invoice_payload"`
	User            struct {
		ID int64 `json:"id"`
	} `json:"user"`
}

type StarTransaction struct {
	ID             string                  `json:"id"`
	Date           int64                   `json:"date"`
	Amount         int64                   `json:"amount"`
	NanostarAmount int                     `json:"nanostar_amount"`
	Source         *StarTransactionPartner `json:"source"`
	Receiver       *StarTransactionPartner `json:"receiver"`
}

// TelegramClient performs only read-only Bot API checks. Errors are
// deliberately sanitized so a token embedded in the request URL is never
// copied into terminal output.
type TelegramClient struct {
	endpoint string
	client   *http.Client
}

func NewTelegramClient(timeout time.Duration) *TelegramClient {
	return NewTelegramClientWithEndpoint(telegramAPIEndpoint, timeout)
}

func NewTelegramClientWithEndpoint(endpoint string, timeout time.Duration) *TelegramClient {
	return &TelegramClient{
		endpoint: endpoint,
		client:   &http.Client{Timeout: timeout},
	}
}

func (c *TelegramClient) Verify(ctx context.Context, token string) (BotIdentity, error) {
	var result struct {
		ID                    int64  `json:"id"`
		IsBot                 bool   `json:"is_bot"`
		FirstName             string `json:"first_name"`
		Username              string `json:"username"`
		SupportsInlineQueries bool   `json:"supports_inline_queries"`
	}
	if err := c.call(ctx, token, "getMe", &result); err != nil {
		return BotIdentity{}, err
	}
	if !result.IsBot || result.ID <= 0 || result.Username == "" {
		return BotIdentity{}, ErrTelegramCheck
	}
	return BotIdentity{
		ID:                    result.ID,
		FirstName:             result.FirstName,
		Username:              result.Username,
		SupportsInlineQueries: result.SupportsInlineQueries,
	}, nil
}

func (c *TelegramClient) Inspect(ctx context.Context, token string) (TelegramState, error) {
	identity, err := c.Verify(ctx, token)
	if err != nil {
		return TelegramState{}, err
	}

	var webhook struct {
		URL                string `json:"url"`
		PendingUpdateCount int    `json:"pending_update_count"`
		LastErrorMessage   string `json:"last_error_message"`
	}
	if err := c.call(ctx, token, "getWebhookInfo", &webhook); err != nil {
		return TelegramState{}, err
	}
	return TelegramState{
		Identity:           identity,
		WebhookURL:         webhook.URL,
		PendingUpdateCount: webhook.PendingUpdateCount,
		LastErrorMessage:   webhook.LastErrorMessage,
	}, nil
}

// ListStarTransactions reads one bounded page from Telegram's authoritative
// Stars ledger. It is intentionally a narrow raw adapter for the older bot SDK.
func (c *TelegramClient) ListStarTransactions(ctx context.Context, token string, offset, limit int) ([]StarTransaction, error) {
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, ErrTelegramCheck
	}
	form := url.Values{}
	form.Set("offset", strconv.Itoa(offset))
	form.Set("limit", strconv.Itoa(limit))
	var result struct {
		Transactions []StarTransaction `json:"transactions"`
	}
	if err := c.callForm(ctx, token, "getStarTransactions", form, &result); err != nil {
		return nil, err
	}
	return result.Transactions, nil
}

func (c *TelegramClient) call(ctx context.Context, token, method string, result any) error {
	return c.callForm(ctx, token, method, nil, result)
}

func (c *TelegramClient) callForm(ctx context.Context, token, method string, form url.Values, result any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf(c.endpoint, token, method), body)
	if err != nil {
		return ErrTelegramCheck
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return ErrTelegramCheck
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ErrTelegramCheck
	}

	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(&envelope); err != nil || !envelope.OK {
		return ErrTelegramCheck
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return ErrTelegramCheck
	}
	return nil
}
