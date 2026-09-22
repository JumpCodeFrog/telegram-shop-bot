package bot

// Per-update context derivation (roadmap 4.14): every update is processed
// under one ctx derived at ingress from the process-lifetime root (4.7),
// carrying a 30s budget and a per-update trace id. The nil fallbacks keep
// direct/test constructors working unchanged.

import (
	"context"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestNewUpdateCtxDerivesFromRoot(t *testing.T) {
	b := &Bot{}

	// Unset root: working ctx, deadline within (29s, 30s], trace id present.
	ctx, cancel := b.newUpdateCtx(nil, tgbotapi.Update{UpdateID: 1})
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("unset root: ctx already cancelled: %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("unset root: no deadline")
	}
	if d := time.Until(deadline); d <= 29*time.Second || d > 30*time.Second {
		t.Fatalf("unset root: deadline in %v, want within (29s, 30s]", d)
	}
	if id := TraceID(ctx); len(id) != 16 {
		t.Fatalf("trace id %q: want 16 hex chars", id)
	}

	// Cancelled root: shutdown reaches in-flight handler work immediately.
	root, cancelRoot := context.WithCancel(context.Background())
	cancelRoot()
	b.SetRootContext(root)
	ctx2, cancel2 := b.newUpdateCtx(nil, tgbotapi.Update{UpdateID: 2})
	defer cancel2()
	if ctx2.Err() == nil {
		t.Fatal("cancelled root: ctx not cancelled (shutdown must reach update work)")
	}

	// Explicitly nil root after SetRootContext(nil): background fallback.
	b.SetRootContext(nil)
	ctx3, cancel3 := b.newUpdateCtx(nil, tgbotapi.Update{UpdateID: 3})
	defer cancel3()
	if ctx3.Err() != nil {
		t.Fatalf("nil root: ctx cancelled: %v", ctx3.Err())
	}
}

func TestNewUpdateCtxPrefersExplicitRoot(t *testing.T) {
	b := &Bot{}
	root, cancelRoot := context.WithCancel(context.Background())
	ctx, cancel := b.newUpdateCtx(root, tgbotapi.Update{UpdateID: 4})
	defer cancel()
	cancelRoot()
	if ctx.Err() == nil {
		t.Fatal("explicit root cancel must cancel the update ctx")
	}
}

func TestNewUpdateCtxTraceIDUniquePerAttempt(t *testing.T) {
	b := &Bot{}
	ctx1, cancel1 := b.newUpdateCtx(nil, tgbotapi.Update{UpdateID: 10})
	defer cancel1()
	ctx2, cancel2 := b.newUpdateCtx(nil, tgbotapi.Update{UpdateID: 10})
	defer cancel2()
	if TraceID(ctx1) == TraceID(ctx2) {
		t.Fatalf("trace ids must differ across processing attempts, got %q twice", TraceID(ctx1))
	}
}

func TestTraceIDAbsent(t *testing.T) {
	if id := TraceID(context.Background()); id != "" {
		t.Fatalf("want empty trace id, got %q", id)
	}
}
