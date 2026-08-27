package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestDispatchDefaultRunsBot(t *testing.T) {
	var calls []string
	code := dispatch(nil, &bytes.Buffer{}, commandSet{
		init:           func() int { calls = append(calls, "init"); return 0 },
		doctor:         func() int { calls = append(calls, "doctor"); return 0 },
		reconcileStars: func([]string) int { calls = append(calls, "reconcile"); return 0 },
		paymentReview:  func([]string) int { calls = append(calls, "payment-review"); return 0 },
		run:            func() int { calls = append(calls, "run"); return 0 },
	}, "dev", "none", "unknown")
	if code != 0 || !reflect.DeepEqual(calls, []string{"run"}) {
		t.Fatalf("code = %d, calls = %v", code, calls)
	}
}

func TestDispatchQuickstartOrderAndFailures(t *testing.T) {
	tests := []struct {
		name      string
		initCode  int
		docCode   int
		wantCode  int
		wantCalls []string
	}{
		{name: "success", wantCalls: []string{"init", "doctor", "run"}},
		{name: "init fails", initCode: 1, wantCode: 1, wantCalls: []string{"init"}},
		{name: "doctor fails", docCode: 1, wantCode: 1, wantCalls: []string{"init", "doctor"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			code := dispatch([]string{"quickstart"}, &bytes.Buffer{}, commandSet{
				init:           func() int { calls = append(calls, "init"); return tt.initCode },
				doctor:         func() int { calls = append(calls, "doctor"); return tt.docCode },
				reconcileStars: func([]string) int { calls = append(calls, "reconcile"); return 0 },
				paymentReview:  func([]string) int { calls = append(calls, "payment-review"); return 0 },
				run:            func() int { calls = append(calls, "run"); return 0 },
			}, "dev", "none", "unknown")
			if code != tt.wantCode || !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Fatalf("code = %d, calls = %v; want %d, %v", code, calls, tt.wantCode, tt.wantCalls)
			}
		})
	}
}

func TestDispatchHelpVersionAndUnknown(t *testing.T) {
	commands := commandSet{init: func() int { return 0 }, doctor: func() int { return 0 }, reconcileStars: func([]string) int { return 0 }, paymentReview: func([]string) int { return 0 }, run: func() int { return 0 }}
	tests := []struct {
		args       []string
		wantCode   int
		wantOutput string
	}{
		{args: []string{"help"}, wantOutput: "quickstart"},
		{args: []string{"version"}, wantOutput: "telegram-shop-bot v3-test"},
		{args: []string{"unknown"}, wantCode: 2, wantOutput: "unknown command"},
		{args: []string{"doctor", "extra"}, wantCode: 2, wantOutput: "does not accept arguments"},
		{args: []string{"reconcile-stars"}, wantOutput: ""},
		{args: []string{"payment-review", "list", "--provider", "stars"}, wantOutput: ""},
	}
	for _, tt := range tests {
		var output bytes.Buffer
		code := dispatch(tt.args, &output, commands, "v3-test", "abc", "today")
		if code != tt.wantCode || !strings.Contains(output.String(), tt.wantOutput) {
			t.Fatalf("args = %v, code = %d, output = %q", tt.args, code, output.String())
		}
	}
}

func TestRedisAvailableRejectsMissingService(t *testing.T) {
	if redisAvailable("127.0.0.1:0", "not-used") {
		t.Fatal("redisAvailable() = true for a missing service")
	}
}
