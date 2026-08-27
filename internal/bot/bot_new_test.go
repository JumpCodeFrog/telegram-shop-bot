package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"shop_bot/internal/config"
	"shop_bot/internal/service"
	"shop_bot/internal/storage"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestNewSanitizesTelegramTransportFailure(t *testing.T) {
	const token = "123456789:secret-that-must-not-appear"
	db, err := storage.New(t.TempDir() + "/shop.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = New(
		&config.Config{BotToken: token, LocalesDir: "../../locales", USDToStarsRate: 50},
		db,
		service.NewMetricsService(),
		storage.NewMemoryFSMStore(),
		nil,
		slog.Default(),
	)
	if !errors.Is(err, ErrTelegramInitialization) {
		t.Fatalf("New() error = %v, want ErrTelegramInitialization", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("New() leaked token: %v", err)
	}
}

func TestSanitizedTelegramClientProtectsEveryRequest(t *testing.T) {
	const token = "123456789:secret-that-must-not-appear"
	client := sanitizedTelegramClient{client: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("transport failed at " + request.URL.String())
	})}}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.telegram.org/bot"+token+"/getUpdates", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(request)
	if !errors.Is(err, errTelegramTransport) || strings.Contains(err.Error(), token) {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestPollingLoggerReceivesSanitizedTransportError(t *testing.T) {
	const token = "123456789:secret-that-must-not-appear"
	var calls int
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 { // NewBotAPIWithClient performs getMe first.
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Bot","username":"bot"}}`)),
				Request:    request,
			}, nil
		}
		return nil, errors.New("simulated outage at " + request.URL.String())
	})
	api, err := tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, sanitizedTelegramClient{client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	if err := tgbotapi.SetLogger(safeTestLogger{out: logs}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tgbotapi.SetLogger(safeTestLogger{out: io.Discard}) })

	updates := api.GetUpdatesChan(tgbotapi.NewUpdate(0))
	deadline := time.Now().Add(time.Second)
	for logs.String() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	api.StopReceivingUpdates()
	select {
	case <-updates:
	case <-time.After(4 * time.Second):
		t.Fatal("polling goroutine did not stop")
	}
	if strings.Contains(logs.String(), token) || !strings.Contains(logs.String(), errTelegramTransport.Error()) {
		t.Fatalf("polling logs = %q", logs.String())
	}
}

type safeTestLogger struct{ out io.Writer }

func (l safeTestLogger) Println(values ...interface{}) {
	_, _ = fmt.Fprintln(l.out, values...)
}

func (l safeTestLogger) Printf(format string, values ...interface{}) {
	_, _ = fmt.Fprintf(l.out, format, values...)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
