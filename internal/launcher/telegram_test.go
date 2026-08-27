package launcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTelegramClientVerifyAndInspect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"id":77,"is_bot":true,"first_name":"Shop","username":"shop_bot","supports_inline_queries":true}}`)
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			fmt.Fprint(w, `{"ok":true,"result":{"url":"","pending_update_count":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewTelegramClientWithEndpoint(server.URL+"/bot%s/%s", time.Second)
	state, err := client.Inspect(context.Background(), "123456:secret")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if state.Identity.Username != "shop_bot" || state.Identity.ID != 77 {
		t.Fatalf("identity = %+v", state.Identity)
	}
	if !state.Identity.SupportsInlineQueries {
		t.Fatal("SupportsInlineQueries = false")
	}
	if state.PendingUpdateCount != 2 {
		t.Fatalf("PendingUpdateCount = %d, want 2", state.PendingUpdateCount)
	}
}

func TestTelegramClientSanitizesFailures(t *testing.T) {
	const token = "123456:super_secret_token_value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewTelegramClientWithEndpoint(server.URL+"/bot%s/%s", time.Second)
	_, err := client.Verify(context.Background(), token)
	if err == nil {
		t.Fatal("Verify() error = nil")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked token: %v", err)
	}
}

func TestTelegramClientRejectsMalformedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":{"id":77,"is_bot":false,"username":"user"}}`)
	}))
	defer server.Close()

	client := NewTelegramClientWithEndpoint(server.URL+"/bot%s/%s", time.Second)
	if _, err := client.Verify(context.Background(), "123456:secret"); err == nil {
		t.Fatal("Verify() error = nil, want malformed identity failure")
	}
}
