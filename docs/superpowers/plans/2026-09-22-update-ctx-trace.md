# Update-ctx propagation + trace (roadmap 4.14) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Process every Telegram update under ONE per-update `context.Context` derived at ingress (30s budget + trace id), thread it through the whole middleware chain into all handlers, and correlate logs via `trace_id`.

**Architecture:** Chain signature flips to `func(ctx context.Context, update tgbotapi.Update)`. Ingress points (polling dispatch goroutine, polling payment barrier, Telegram webhook, `HandleUpdate`) derive the per-update ctx via the new `newUpdateCtx(root, update)`. Migration is STAGED with a bridge: core switches first while unmigrated handlers keep self-creating their 30s ctx via the still-present `handlerCtx()`; per-group tasks then migrate handlers file-by-file (each commit compiles + green gates); cleanup deletes `handlerCtx()`.

**Tech Stack:** Go 1.x, `context`, `crypto/rand` (trace ids — NO new dependencies, no OpenTelemetry), `log/slog`, tgbotapi v5.

**Spec:** `docs/superpowers/specs/2026-09-22-update-ctx-trace-design.md` — approved design with rulings Q1–Q8; this plan implements it. Read the spec first.

## Global Constraints

- Branch: `feat/update-ctx-trace` off main `716b4ba`. One commit per task. No push, no PR.
- No new dependencies. No DB migrations. No locale/copy changes. Buyer-visible surfaces stay byte-identical (roadmap §5 invariant 6).
- Money semantics untouched. `refundMu` serialization in `onAdminRefundConfirm` untouched. Polling payment-barrier ordering (`wg.Wait()` → settle → offset advance) and webhook 500-on-non-durable ACK-withholding semantics preserved exactly.
- Rate-limit cleanup goroutine keeps its PROCESS-LIFETIME ctx (constructor arg of `RateLimitMiddleware` / `ensureHandler`); per-update ctx must never be stored on `Bot` fields (concurrent dispatch — data race).
- Single 30s budget per update (spec Q4): multi-window flows (`handleStart`, `handlePromoInput`, `processSuccessfulPayment`) collapse to the one propagated ctx.
- Gates per task: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/` (EMPTY) `&& go test ./...` (storage ~100s; timeout ≥600000ms). Additionally `go test ./internal/bot/ -race -count=1` in Tasks 1, 4, 6, 7.
- Mechanical transformation pattern (Tasks 2–6): handler signature gains `ctx context.Context` as FIRST parameter; body deletes `ctx, cancel := b.handlerCtx()` + `defer cancel()`; all internal `ctx` uses then refer to the parameter; call sites pass their own ctx. Ensure `context` is imported in every touched file (compile catches).
- Refinement of spec Q7 (ruling, recorded): NO `TODO(4.14)` marker comments — the completion proof is `grep -rn "handlerCtx" internal/ cmd/ worker/` → EMPTY at Task 7.

## Review Focus

1. **Barrier durability:** polling must not advance `offset` when `processSuccessfulPayment` errs; webhook must 500. Pinned by named runs in Task 1 + 4: `go test ./internal/bot/ -run 'TestTelegramPollingDoesNotAdvancePastUndurableStarsPayment|TestTelegramWebhookStarsStorageFailureWithholdsAcknowledgement|TestTelegramWebhookValidStarsPaymentSettlesBeforeAcknowledgement' -count=1 -v` → all PASS.
2. **Rate-limit goroutine lifetime:** `ensureHandler`/`prepareHandler` ctx must remain process-lifetime. Pinned by full E2E suite per task (a per-update ctx here would silently drop later updates) + reviewer read of Task 1 diff.
3. **4.7 shutdown property:** cancelled root ⇒ cancelled update ctx. Pinned by `TestNewUpdateCtxDerivesFromRoot` (Task 1).
4. **Shared-budget flows (spec Q4):** checkout (`handlePromoInput` two windows→one), `handleStart` (ref + menu windows→one), `processSuccessfulPayment` (payload + main windows→one). Pinned by named runs: Task 3 `go test ./internal/bot/ -run 'Promo|Checkout' -count=1`, Task 1+4 payment E2E suite, Task 2 `go test ./internal/bot/ -run 'Start|Referral' -count=1`.
5. **Trace id:** present, 16-hex, unique per processing attempt, `""` when absent; Auth upsert uses chain ctx. Pinned by `update_ctx_test.go` + `TestLoggingMiddleware_LogsTraceAndUpdateID` + `TestRecoverMiddleware_LogsTraceID` (Task 1); end-to-end by `TestTelegramWebhookStarsQuarantineLogCarriesTraceID` (Task 8).

---

### Task 1: Core — chain types, middlewares, Auth, ingress, newUpdateCtx, harness

**Files:**
- Create: `internal/bot/update_ctx.go`
- Create: `internal/bot/update_ctx_test.go`
- Modify: `internal/bot/middleware.go` (type `:17`, Logging `:54-79`, Recover `:82-97`, AdminOnly `:101-115`, RateLimit `:127-172`, Chain `:176-181`)
- Modify: `internal/bot/middleware/auth.go` (whole file)
- Modify: `internal/bot/bot.go` (`handler` field `:120`, `prepareHandler` `:259-267`, `Run` barrier `:417-438` + dispatch `:443-448`, `HandleUpdate` `:465-470`)
- Modify: `internal/bot/webhook.go` (ingress `:559-568`)
- Modify: `internal/bot/handlers.go` (`route` `:11-25`, `routeMessage` `:28-167`, `handleCallback` `:188-381`)
- Modify: `internal/bot/handlers_payment.go` (`handleSuccessfulPayment` `:577-581`, `processSuccessfulPayment` `:587+`, two internal `handlerCtx` sites `:598`, `:607`)
- Modify: `internal/bot/middleware_test.go` (all chain-touching tests)
- Modify: `internal/bot/e2e_test.go` (`handle` field `:147`, chain build `:211`, `do` `:277-282`)
- Modify: `cmd/usability-smoke/main.go` (12 `HandleUpdate` call sites `:417-464` + `context` import)

**Interfaces:**
- Consumes: nothing (first task).
- Produces (all later tasks rely on these):
  - `type Middleware func(handler func(ctx context.Context, update tgbotapi.Update)) func(ctx context.Context, update tgbotapi.Update)`
  - `func Chain(handler func(ctx context.Context, update tgbotapi.Update), middlewares ...Middleware) func(ctx context.Context, update tgbotapi.Update)`
  - `func middleware.Auth(userStore UserStore) func(next func(ctx context.Context, update tgbotapi.Update)) func(ctx context.Context, update tgbotapi.Update)` (NO ctxFn param)
  - `func (b *Bot) newUpdateCtx(root context.Context, update tgbotapi.Update) (context.Context, context.CancelFunc)` — root nil → `b.rootCtx` → `context.Background()`
  - `func TraceID(ctx context.Context) string` ("" when absent)
  - `func (b *Bot) loggerFor(ctx context.Context) *slog.Logger`
  - `func (b *Bot) route(ctx context.Context, update tgbotapi.Update)`, `routeMessage(ctx, msg)`, `handleCallback(ctx, cb)`
  - `func (b *Bot) processSuccessfulPayment(ctx context.Context, msg *tgbotapi.Message) error`, `handleSuccessfulPayment(ctx, msg)`
  - `func (b *Bot) HandleUpdate(root context.Context, update tgbotapi.Update)`
  - `const updateTimeout = 30 * time.Second`
  - `handlerCtx()` REMAINS (bridge for unmigrated handlers; deleted in Task 7).

- [ ] **Step 1: Write the failing tests** — create `internal/bot/update_ctx_test.go`:

```go
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
```

Also append to `internal/bot/middleware_test.go` (new trace pins):

```go
func TestLoggingMiddleware_LogsTraceAndUpdateID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	b := &Bot{}
	upd := newMessageUpdate(42)
	upd.UpdateID = 777
	ctx, cancel := b.newUpdateCtx(nil, upd)
	defer cancel()
	mw := LoggingMiddleware(logger)
	mw(func(ctx context.Context, update tgbotapi.Update) {})(ctx, upd)
	out := buf.String()
	if !strings.Contains(out, "trace_id="+TraceID(ctx)) {
		t.Fatalf("log missing trace_id: %s", out)
	}
	if !strings.Contains(out, "update_id=777") {
		t.Fatalf("log missing update_id: %s", out)
	}
}

func TestRecoverMiddleware_LogsTraceID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	b := &Bot{}
	upd := newMessageUpdate(42)
	ctx, cancel := b.newUpdateCtx(nil, upd)
	defer cancel()
	mw := RecoverMiddleware(logger)
	mw(func(ctx context.Context, update tgbotapi.Update) { panic("boom") })(ctx, upd)
	if !strings.Contains(buf.String(), "trace_id="+TraceID(ctx)) {
		t.Fatalf("panic log missing trace_id: %s", buf.String())
	}
}
```

- [ ] **Step 2: Run to verify RED**

Run: `go test ./internal/bot/ -run 'TestNewUpdateCtx|TestTraceIDAbsent|TestLoggingMiddleware_LogsTraceAndUpdateID|TestRecoverMiddleware_LogsTraceID' -count=1`
Expected: FAIL — `undefined: b.newUpdateCtx` / `undefined: TraceID` (compile error).

- [ ] **Step 3: Create `internal/bot/update_ctx.go`**

```go
package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// updateTimeout bounds all work for one Telegram update (4.14): a single
// per-update budget, replacing 4.7's per-handler windows.
const updateTimeout = 30 * time.Second

// updateCtxKey is the unexported context key type for per-update values.
type updateCtxKey struct{}

// traceIDKey carries the per-update trace correlation id.
var traceIDKey updateCtxKey

// TraceID returns the per-update trace id stored in ctx, or "" if absent.
func TraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// newTraceID returns 8 crypto/rand bytes hex-encoded (16 chars). On rand
// failure it falls back to the zero-padded update id: ingress must never
// crash to produce a correlation id.
func newTraceID(update tgbotapi.Update) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%016x", uint64(update.UpdateID))
	}
	return hex.EncodeToString(b[:])
}

// newUpdateCtx derives the per-update context: root (nil → b.rootCtx →
// context.Background()) + trace id + the 30s per-update budget. Deriving
// from the process-lifetime root keeps 4.7's property: shutdown cancellation
// reaches in-flight handler DB work.
func (b *Bot) newUpdateCtx(root context.Context, update tgbotapi.Update) (context.Context, context.CancelFunc) {
	if root == nil {
		root = b.rootCtx
	}
	if root == nil {
		root = context.Background()
	}
	ctx := context.WithValue(root, traceIDKey, newTraceID(update))
	return context.WithTimeout(ctx, updateTimeout)
}

// loggerFor returns the bot logger with the update's trace_id bound, so all
// lines emitted while handling one update correlate. Passthrough when the
// ctx carries no trace id.
func (b *Bot) loggerFor(ctx context.Context) *slog.Logger {
	if id := TraceID(ctx); id != "" {
		return b.logger.With("trace_id", id)
	}
	return b.logger
}
```

- [ ] **Step 4: Flip the chain** — `internal/bot/middleware.go`:

  - `:17` → `type Middleware func(handler func(ctx context.Context, update tgbotapi.Update)) func(ctx context.Context, update tgbotapi.Update)`
  - LoggingMiddleware inner closures gain `ctx context.Context` first param; `handler(ctx, update)`; the `"incoming update"` log gains two fields after `"user_id", userID,`: `"update_id", update.UpdateID,` and `"trace_id", TraceID(ctx),`.
  - RecoverMiddleware: closures gain ctx; `handler(ctx, update)`; panic log gains `"update_id", update.UpdateID,` and `"trace_id", TraceID(ctx),`.
  - AdminOnly: closures gain ctx; final line `handler(ctx, update)`.
  - RateLimitMiddleware: constructor signature UNCHANGED (its ctx stays process-lifetime for the cleanup goroutine — update the doc comment: "ctx controls the cleanup goroutine and is process-lifetime; it is unrelated to the per-update ctx the chain carries"). Inner closures gain ctx; `handler(ctx, update)`. NOTE: the inner `ctx` param shadows the constructor ctx inside the returned closure — intended; the goroutine captured the outer one before the closure exists.
  - Chain: `func Chain(handler func(ctx context.Context, update tgbotapi.Update), middlewares ...Middleware) func(ctx context.Context, update tgbotapi.Update)` (body unchanged).

- [ ] **Step 5: Auth drops the ctxFn factory** — `internal/bot/middleware/auth.go`:

```go
package middleware

import (
	"context"
	"shop_bot/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type UserStore interface {
	Upsert(ctx context.Context, user *storage.User) error
}

// Auth upserts the Telegram user into storage before the update proceeds,
// using the per-update context carried by the chain (roadmap 4.14).
func Auth(userStore UserStore) func(next func(ctx context.Context, update tgbotapi.Update)) func(ctx context.Context, update tgbotapi.Update) {
	return func(next func(ctx context.Context, update tgbotapi.Update)) func(ctx context.Context, update tgbotapi.Update) {
		return func(ctx context.Context, update tgbotapi.Update) {
			var tgUser *tgbotapi.User

			if update.Message != nil {
				tgUser = update.Message.From
			} else if update.CallbackQuery != nil {
				tgUser = update.CallbackQuery.From
			}

			if tgUser != nil {
				user := &storage.User{
					TelegramID:   tgUser.ID,
					Username:     tgUser.UserName,
					FirstName:    tgUser.FirstName,
					LanguageCode: tgUser.LanguageCode,
				}

				// Foreground upsert so the user row exists for later handlers.
				_ = userStore.Upsert(ctx, user)
			}

			next(ctx, update)
		}
	}
}
```

- [ ] **Step 6: bot.go** —
  - `:120` field → `handler func(ctx context.Context, update tgbotapi.Update)`; update its comment: "handler is the fully-chained update handler (used for both polling and webhook); every call passes the per-update ctx".
  - `prepareHandler` (`:259-267`): `middleware.Auth(b.users, b.handlerCtx)` → `middleware.Auth(b.users)`. Keep the doc comment ("ctx controls the lifetime of the rate-limit cleanup goroutine").
  - `Run` barrier (`:417-438`) — replace the settle call:

```go
			if update.Message != nil && update.Message.SuccessfulPayment != nil {
				// A payment is an ordering barrier. Finish older updates first, then
				// advance getUpdates offset only after settlement/review is durable.
				wg.Wait()
				uCtx, uCancel := b.newUpdateCtx(ctx, update)
				err := b.processSuccessfulPayment(uCtx, update.Message)
				uCancel()
				cleanup()
```

  - `Run` dispatch (`:443-448`):

```go
			wg.Add(1)
			go func(upd tgbotapi.Update, done func()) {
				defer wg.Done()
				defer done()
				uCtx, uCancel := b.newUpdateCtx(ctx, upd)
				defer uCancel()
				b.handler(uCtx, upd)
			}(update, cleanup)
```

  - `HandleUpdate` (`:465-470`):

```go
// HandleUpdate processes a single Telegram update through the full middleware
// chain. root scopes the update's processing (the webhook passes its request
// ctx); nil falls back to the process root, then Background. Used by the
// Telegram webhook and local smoke tooling.
func (b *Bot) HandleUpdate(root context.Context, update tgbotapi.Update) {
	// ensureHandler keeps this ctx for the handler-chain lifetime (rate-limit
	// cleanup goroutine), so it must be background, not a per-update ctx.
	b.ensureHandler(context.Background())
	uCtx, cancel := b.newUpdateCtx(root, update)
	defer cancel()
	b.handler(uCtx, update)
}
```

  - DO NOT delete `handlerCtx()` — the bridge stays until Task 7.

- [ ] **Step 7: webhook.go ingress (`:559-568`)**:

```go
		if update.Message != nil && update.Message.SuccessfulPayment != nil {
			uCtx, uCancel := b.newUpdateCtx(r.Context(), update)
			err := b.processSuccessfulPayment(uCtx, update.Message)
			uCancel()
			if err != nil {
				b.logger.Error("telegram webhook: Stars payment not durably handled", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		} else {
			b.HandleUpdate(r.Context(), update)
		}
```

- [ ] **Step 8: handlers.go routing** —
  - `route` → `func (b *Bot) route(ctx context.Context, update tgbotapi.Update)`; body: `b.handlePreCheckout(update.PreCheckoutQuery)` (UNCHANGED — migrated in Task 4), `b.handleInlineQuery(update.InlineQuery)` (UNCHANGED — Task 2), `b.routeMessage(ctx, update.Message)`, `b.handleCallback(ctx, update.CallbackQuery)`.
  - `routeMessage` → `func (b *Bot) routeMessage(ctx context.Context, msg *tgbotapi.Message)`; DELETE `routeCtx, routeCancel := b.handlerCtx()` + `defer routeCancel()` (`:34-35`); all three `routeCtx` FSM reads use `ctx`; `b.handleSuccessfulPayment(ctx, msg)` (`:30`); every other dispatch call UNCHANGED (unmigrated handlers — Tasks 2–6 add ctx per group).
  - `handleCallback` → `func (b *Bot) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery)`; ALL dispatch calls UNCHANGED this task (ctx param unused until Task 2 — legal Go).
- [ ] **Step 9: handlers_payment.go barrier functions** —
  - `handleSuccessfulPayment` → `func (b *Bot) handleSuccessfulPayment(ctx context.Context, msg *tgbotapi.Message)`; body: `if err := b.processSuccessfulPayment(ctx, msg); err != nil {`.
  - `processSuccessfulPayment` → `func (b *Bot) processSuccessfulPayment(ctx context.Context, msg *tgbotapi.Message) error`; DELETE both `ctx, cancel := b.handlerCtx()` + `defer cancel()` sites (`:598` invalid-payload branch, `:607` main); all ctx uses below refer to the parameter.
- [ ] **Step 10: e2e harness** — `internal/bot/e2e_test.go`:
  - `:147` field → `handle func(ctx context.Context, upd tgbotapi.Update)`
  - `:211` → `handle: middleware.Auth(b.users)(b.route),` (keep the "production chain minus rate limiting and logging" comment)
  - `do` (`:277-282`):

```go
func (e *e2eEnv) do(upd tgbotapi.Update) []tgCall {
	e.t.Helper()
	before := e.tg.count()
	ctx, cancel := e.bot.newUpdateCtx(context.Background(), upd)
	defer cancel()
	e.handle(ctx, upd)
	return e.tg.since(before)
}
```

  - Ensure `context` is imported in e2e_test.go.
- [ ] **Step 11: middleware_test.go migration** — for EACH of these tests the handler literal becomes `func(ctx context.Context, update tgbotapi.Update)` and invocations pass a ctx first arg (`context.Background()` unless the test needs a traced ctx): `TestLoggingMiddleware_LogsFields`, `TestLoggingMiddleware_CallbackQuery`, `TestRecoverMiddleware_NoPanic`, `TestRecoverMiddleware_CatchesPanic`, `TestRecoverMiddleware_ContinuesAfterPanic`, `TestAdminOnly_AllowsAdmin`, `TestAdminOnly_BlocksNonAdmin`, `TestAdminOnly_BlocksZeroUserID`, `TestAdminOnly_EmptyAdminList`, `TestAdminOnly_CallbackQuery`, `TestChain_AppliesInOrder`, `TestProperty_AdminOnlyAccess`, `TestProperty_LoggingContainsRequiredFields`, `TestProperty_RecoverMiddlewareCatchesPanics`. Pure-function tests (`TestExtractUserID_*`, `TestUpdateType`) unchanged. Example before→after:

```go
// before
mw := LoggingMiddleware(logger)
handler := func(update tgbotapi.Update) { handled = true }
mw(handler)(newMessageUpdate(42))
// after
mw := LoggingMiddleware(logger)
handler := func(ctx context.Context, update tgbotapi.Update) { handled = true }
mw(handler)(context.Background(), newMessageUpdate(42))
```

Add `"context"` to the import block.
- [ ] **Step 12: usability-smoke** — `cmd/usability-smoke/main.go`: all 12 `b.HandleUpdate(<upd>)` calls → `b.HandleUpdate(context.Background(), <upd>)`; add `"context"` to imports.
- [ ] **Step 13: Gates**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`
Expected: build/vet clean, gofmt EMPTY, all tests PASS (timeout ≥600000ms).
Run: `go test ./internal/bot/ -race -count=1`
Expected: PASS.
Run: `go test ./internal/bot/ -run 'TestTelegramPollingDoesNotAdvancePastUndurableStarsPayment|TestTelegramWebhookStarsStorageFailureWithholdsAcknowledgement|TestTelegramWebhookValidStarsPaymentSettlesBeforeAcknowledgement' -count=1 -v`
Expected: 3× PASS (barrier semantics intact).

- [ ] **Step 14: Commit**

```bash
git add internal/bot/ cmd/usability-smoke/
git commit -m "feat(bot): ctx-aware middleware chain + per-update ctx with trace id at ingress (4.14 core; handlerCtx bridge remains)"
```

---

### Task 2: Handler group G1 — routing-adjacent + small files

**Files:**
- Modify: `internal/bot/handlers.go` (`onBack` `:383-413` + call sites `:364`, `:399`, `:402`, `:390`)
- Modify: `internal/bot/handlers_start.go` (`handleCancel` `:15`, `handleStart` — sites `:49`, `:65`)
- Modify: `internal/bot/handlers_search.go` (`handleSearch` `:53`)
- Modify: `internal/bot/handlers_inline.go` (`handleInlineQuery` `:16`)
- Modify: `internal/bot/handlers_orders.go` (`handleOrders` `:8`, `sendOrders` `:14`)
- Modify: `internal/bot/profile.go` (`handleProfile` `:7`, `sendProfile` `:12`)
- Modify: `internal/bot/handlers_referral.go` (`handleReferral` `:11`, `sendReferralScreen` `:19`)

**Interfaces:**
- Consumes: `route`/`routeMessage`/`handleCallback` ctx params (Task 1).
- Produces: `handleCancel(ctx, msg)`, `handleStart(ctx, msg)`, `handleSearch(ctx, msg)`, `handleInlineQuery(ctx, iq)`, `handleOrders(ctx, msg)`, `sendOrders(ctx, chatID, userID, msgID, lang)`, `handleProfile(ctx, msg)`, `sendProfile(ctx, chatID, userID, msgID, lang)`, `handleReferral(ctx, msg)`, `sendReferralScreen(ctx, chatID, userID, msgID, lang)`, `onBack(ctx, chatID, userID, msgID, data, lang)`.

- [ ] **Step 1: Apply the mechanical pattern to every function above.** Per function: add `ctx context.Context` as first param; delete its `ctx, cancel := b.handlerCtx()` + `defer cancel()`; body uses the param. `handleStart` has TWO sites (`:49` ref deep-link block, `:65` main menu) — delete BOTH, single param ctx serves the whole function (spec Q4 shared budget). `onBack`: gains ctx; the `menu` branch deletes its local `handlerCtx()` and calls `b.sendMainMenu(chatID, userID, msgID, lang, ctx)` (sendMainMenu keeps its pre-existing ctx-LAST signature — do not normalize).

- [ ] **Step 2: Update call sites** (all in `internal/bot/handlers.go`):
  - routeMessage: `:70` `b.handleStart(ctx, msg)`, `:82` `b.handleSearch(ctx, msg)`, `:87` `b.handleOrders(ctx, msg)`, `:92` `b.handleProfile(ctx, msg)`, `:95` `b.handleReferral(ctx, msg)`, `:101` `b.handleCancel(ctx, msg)`.
  - route: `:17` `b.handleInlineQuery(ctx, update.InlineQuery)`.
  - handleCallback: `:344` `b.sendProfile(ctx, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)`, `:355` `b.sendReferralScreen(ctx, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), lang)`, `:364` `b.onBack(ctx, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)`.
  - onBack internal: `:399` `b.sendOrders(ctx, chatID, userID, msgID, lang)`, `:402` `b.sendProfile(ctx, chatID, userID, msgID, lang)`. LEAVE `:393` sendCatalog, `:396` sendCart, `:405` sendWishlist, `:411` onCategorySelected WITHOUT ctx (Task 3).
- [ ] **Step 3: Gates**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./internal/bot/ -count=1`
Expected: clean + EMPTY + PASS.
Run: `go test ./internal/bot/ -run 'Start|Referral|Profile|Orders|Search|Inline' -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/bot/
git commit -m "refactor(bot): thread per-update ctx through G1 handlers (start/search/inline/orders/profile/referral/onBack)"
```

---

### Task 3: Handler group G2 — buyer flows (cart/catalog/checkout/wishlist/reviews)

**Files:**
- Modify: `internal/bot/handlers_cart.go` (`handleCart` `:11`, `sendCart` `:17`, `onCartAdd` `:63`, `onProductQuantityChange` `:83`, `onCartPlus` `:102`, `onCartMinus` `:119`, `onCartDel` `:156`, `onCartCheckout` `:167`)
- Modify: `internal/bot/handlers_catalog.go` (`handleCatalog` `:18`, `sendCatalog` `:24`, `onCategorySelected` `:66`, `onProductSelected` `:139`, `refreshProductKeyboard` `:289`)
- Modify: `internal/bot/handlers_checkout.go` (`onPromoEnter` `:18`, `handlePromoInput` — sites `:37`, `:44`, `onOrderConfirm` `:131`)
- Modify: `internal/bot/handlers_wishlist.go` (`onWishlistToggle` `:20`, `onWishlistRemove` `:65`, `handleWishlist` `:92`, `sendWishlist` `:98`)
- Modify: `internal/bot/handlers_reviews.go` (`handleReviewCallback` `:77`, `onReviewRate` `:118`, `onReviewSkip` `:153`, `handleReviewTextInput` `:167`, `onReviewList` `:208`, `handleReviewsAdmin` `:247`, `onReviewDelete` `:287`)
- Modify: `internal/bot/handlers.go` (call sites only)

**Interfaces:**
- Consumes: Task 1 chain + Task 2 pattern.
- Produces: all listed functions with `ctx context.Context` first param.

- [ ] **Step 1: Apply the mechanical pattern to every function above.** `handlePromoInput` has TWO `handlerCtx()` sites (`:37` fsmCtx, `:44` ctx) — delete BOTH, use the param (spec Q4: checkout's two windows collapse to the single per-update budget).
- [ ] **Step 2: Update call sites**:
  - `handlers.go` routeMessage: `:41` `b.handlePromoInput(ctx, msg)`, `:50` `b.handleReviewTextInput(ctx, msg, reviewState)`, `:80` `b.handleCatalog(ctx, msg)`, `:84` `b.handleCart(ctx, msg)`, `:98` `b.handleWishlist(ctx, msg)`, `:119` `b.handleReviewsAdmin(ctx, msg)`.
  - `handlers.go` handleCallback: `:204` `b.onCategorySelected(ctx, chatID, userID, b.prepareTextRenderMessageID(chatID, cb.Message), data, lang)`, `:208` onProductSelected, `:211`/`:214` onProductQuantityChange, `:217` onCartAdd, `:221` onCartPlus, `:225` onCartMinus, `:229` onCartDel, `:233` onCartCheckout, `:237` onPromoEnter, `:241` onOrderConfirm, `:337` onWishlistRemove, `:340` onWishlistToggle, `:347` `b.handleReviewCallback(ctx, cb)` — ALL gain `ctx, ` after `b.`.
  - `handlers.go` onBack: `:393` `b.sendCatalog(ctx, chatID, msgID, lang)`, `:396` `b.sendCart(ctx, chatID, userID, msgID, lang)`, `:405` `b.sendWishlist(ctx, chatID, userID, msgID, lang)`, `:411` `b.onCategorySelected(ctx, chatID, userID, msgID, target, lang)`.
  - Cross-file internal: `handlers_cart.go:12` handleCart→`b.sendCart(ctx, ...)`, `:72` onCartAdd→`b.refreshProductKeyboard(ctx, ...)`, `:92` onProductQuantityChange→same, `:109` onCartPlus→`b.sendCart(ctx, ...)`, `:146` onCartMinus→same, `:163` onCartDel→same; `handlers_catalog.go:19` handleCatalog→`b.sendCatalog(ctx, ...)`; `handlers_wishlist.go:52` onWishlistToggle→`b.refreshProductKeyboard(ctx, ...)`, `:73` →`b.sendWishlist(ctx, ...)`, `:93` handleWishlist→`b.sendWishlist(ctx, ...)`; `handlers_reviews.go:86` handleReviewCallback→`b.onReviewSkip(ctx, ...)`, `:90` →`b.onReviewList(ctx, ...)`, `:95` →`b.onReviewDelete(ctx, ...)`, `:99` →`b.onReviewRate(ctx, ...)`.
  - NOTE line numbers are pre-migration; locate by content after earlier tasks' edits.
- [ ] **Step 3: Gates**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./internal/bot/ -count=1`
Expected: clean + EMPTY + PASS.
Run: `go test ./internal/bot/ -run 'Promo|Checkout|Cart|Catalog|Wishlist|Review' -count=1`
Expected: PASS (pins Review Focus #4 for the checkout shared-budget change).

- [ ] **Step 4: Commit**

```bash
git add internal/bot/
git commit -m "refactor(bot): thread per-update ctx through G2 buyer-flow handlers (cart/catalog/checkout/wishlist/reviews)"
```

---

### Task 4: Handler group G3 — payments + subscriptions

**Files:**
- Modify: `internal/bot/handlers_payment.go` (`onPayStars` `:29`, `onOrderCancel` `:69`, `onPayCrypto` `:127`, `onPayYooKassa` `:198`, `onPayStripe` `:277`, `onPayTON` `:355`, `onPayNowpayments` `:423`, `onPayBalance` `:497`, `handlePreCheckout` `:567`)
- Modify: `internal/bot/handlers_subs.go` (`handleMySubs` `:140`, `sendMySubs` `:147`, `onSubCancel` `:190`)
- Modify: `internal/bot/handlers.go` (call sites only)

**Interfaces:**
- Consumes: Task 1 (incl. already-migrated `processSuccessfulPayment`/`handleSuccessfulPayment`).
- Produces: all listed functions with `ctx context.Context` first param.

- [ ] **Step 1: Apply the mechanical pattern** to every function above.
- [ ] **Step 2: Update call sites**:
  - `handlers.go` route: `:14` `b.handlePreCheckout(ctx, update.PreCheckoutQuery)`.
  - `handlers.go` handleCallback: `:244` onOrderCancel, `:247` onPayStars, `:250` onPayCrypto, `:253` onPayYooKassa, `:256` onPayStripe, `:259` onPayTON, `:262` onPayNowpayments, `:265` onPayBalance, `:350` onSubCancel — all gain `ctx, ` first arg.
  - `handlers.go` routeMessage: `:89` `b.handleMySubs(ctx, msg)`.
  - `handlers_subs.go`: `:141` handleMySubs→`b.sendMySubs(ctx, ...)`, `:225` onSubCancel→`b.sendMySubs(ctx, ...)`.
- [ ] **Step 3: Gates** — standard gates PLUS race + barrier pins:

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`
Run: `go test ./internal/bot/ -race -count=1`
Run: `go test ./internal/bot/ -run 'TestTelegramPollingDoesNotAdvancePastUndurableStarsPayment|TestTelegramWebhookStarsStorageFailureWithholdsAcknowledgement|TestTelegramWebhookValidStarsPaymentSettlesBeforeAcknowledgement' -count=1 -v`
Expected: all clean/PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/bot/
git commit -m "refactor(bot): thread per-update ctx through G3 payment/subscription handlers"
```

---

### Task 5: Handler group G4 — admin A (products/photos/categories/styles)

**Files:**
- Modify: `internal/bot/admin_products.go` (`handleAddProduct` `:18`, `handleAddProductStep` `:25`, `finishAddProduct` `:87`, `handleEditProduct` `:150`, `handleEditProductField` `:171`, `handleDeleteProduct` `:236`, `onAdminToggleStock` `:254`, `routeEditProduct` `:279`)
- Modify: `internal/bot/admin_photos.go` (`sendAdminPhotoList` `:110`, `onAdminPhotoDelete` `:148`, `onAdminPhotoAdd` `:201`)
- Modify: `internal/bot/admin_categories.go` (`handleAddCategory` `:28`, `handleEditCategory` `:56`, `handleDeleteCategory` `:94`, `handleListCategories` `:109`)
- Modify: `internal/bot/admin_styles.go` (`handleBtnStyleAdmin` `:11`, `sendBtnStyleList` `:21`, `sendBtnStylePicker` `:51`, `onAdminSetStyle` `:95`)
- Modify: `internal/bot/handlers.go` (call sites only)

**Interfaces:**
- Produces: all listed functions with `ctx context.Context` first param. `routeEditProduct(ctx, msg)`.

- [ ] **Step 1: Apply the mechanical pattern** to every function above (incl. sub-router `routeEditProduct`).
- [ ] **Step 2: Update call sites**:
  - `handlers.go` routeMessage: `:63` `b.handleAddProductStep(ctx, msg)` (keeps bool return), `:107` handleAddProduct, `:109` `b.routeEditProduct(ctx, msg)`, `:111` handleDeleteProduct, `:123` handleAddCategory, `:125` handleEditCategory, `:127` handleDeleteCategory, `:129` handleListCategories, `:165` handleBtnStyleAdmin.
  - `handlers.go` handleCallback: `:270` onAdminToggleStock, `:277` `b.sendAdminPhotoList(ctx, chatID, msgID, prodID, lang)`, `:284` onAdminPhotoDelete, `:290` onAdminPhotoAdd, `:320` sendBtnStyleList, `:327` sendBtnStylePicker, `:333` onAdminSetStyle.
  - Internal: `admin_products.go:81` handleAddProductStep→`b.finishAddProduct(ctx, ...)`, `:286` routeEditProduct→`b.handleEditProduct(ctx, msg)`, `:288` →`b.handleEditProductField(ctx, msg, prodID, field, value)`; `admin_products.go:68` `b.handleWizardPhotoStep(ctx, msg, state)` — handleWizardPhotoStep ALREADY takes ctx first (pre-existing); the ctx passed is now the param (was the deleted local). `admin_photos.go:28` handleWizardPhotoStep→`b.sendAdminPhotoList(ctx, chatID, 0, state.EditProductID, lang)` (its ctx param threads through), `:156` onAdminPhotoDelete→`b.sendAdminPhotoList(ctx, ...)`; `admin_styles.go:15` handleBtnStyleAdmin→`b.sendBtnStyleList(ctx, ...)`, `:105` onAdminSetStyle→`b.sendBtnStyleList(ctx, ...)`.
- [ ] **Step 3: Gates**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./internal/bot/ -count=1`
Expected: clean + EMPTY + PASS (add-product wizard E2E incl. photo steps).

- [ ] **Step 4: Commit**

```bash
git add internal/bot/
git commit -m "refactor(bot): thread per-update ctx through G4 admin handlers (products/photos/categories/styles)"
```

---

### Task 6: Handler group G5 — admin B (orders/analytics/balance/promos/payreview/refunds)

**Files:**
- Modify: `internal/bot/admin_orders.go` (`handleOrdersAll` `:24`, `handleOrderCard` `:63`, `handleSetDelivered` `:135`, `handleExportOrders` `:217`)
- Modify: `internal/bot/admin_analytics.go` (`handleAnalytics` `:15`, `handleAnalyticsCallback` `:19`, `sendAnalytics` `:73`)
- Modify: `internal/bot/admin_balance.go` (`handleSetBalance` `:50`)
- Modify: `internal/bot/admin_promos.go` (`handleAddPromo` `:23`, `handleListPromos` `:33`, `handleDeletePromo` `:48`)
- Modify: `internal/bot/admin_payreview.go` (`handlePayReview` `:46`, `sendPayReviewList` `:107`, `onAdminPayReviewCallback` `:207`, `sendPayReviewCard` `:308`, `onAdminPayReviewPreview` `:371`, `onAdminPayReviewConfirm` `:417`)
- Modify: `internal/bot/admin_refunds.go` (`handleRefundCommand` `:243`, `onAdminRefundCallback` `:310`, `onAdminRefundConfirm` `:330`)
- Modify: `internal/bot/handlers.go` (call sites only)

**Interfaces:**
- Produces: all listed functions with `ctx context.Context` first param.

- [ ] **Step 1: Apply the mechanical pattern.** CRITICAL: `onAdminRefundConfirm` — ONLY the signature + deletion of `ctx, cancel := b.handlerCtx()` + `defer cancel()` (`:330-331`); the `b.refundMu.Lock()` / `defer b.refundMu.Unlock()` block and everything else stays byte-identical.
- [ ] **Step 2: Update call sites**:
  - `handlers.go` routeMessage: `:113` handleOrdersAll, `:115` handleOrderCard, `:117` handleSetDelivered, `:161` handleExportOrders, `:141` handleAnalytics, `:157` handleSetBalance, `:133` handleAddPromo, `:135` handleListPromos, `:137` handleDeletePromo, `:145` handlePayReview, `:149` handleRefundCommand.
  - `handlers.go` handleCallback: `:296` + `:302` `b.onAdminPayReviewCallback(ctx, chatID, msgID, userID, data, lang)`, `:308` `b.onAdminRefundCallback(ctx, chatID, msgID, userID, data, lang)`, `:314` `b.handleAnalyticsCallback(ctx, chatID, msgID, data, cb.From.LanguageCode)`.
  - Internal: `admin_analytics.go:16` handleAnalytics→`b.sendAnalytics(ctx, ...)`, `:26` handleAnalyticsCallback→same; `admin_payreview.go:50` handlePayReview→`b.sendPayReviewList(ctx, ...)`, `:214` `b.sendPayReviewList(ctx, chatID, msgID, lang)`, `:216` `b.sendPayReviewCard(ctx, chatID, msgID, ref, lang)`, `:218` `b.onAdminPayReviewConfirm(ctx, ...)`, `:220` `b.onAdminPayReviewPreview(ctx, ...)`; `admin_refunds.go:315` `b.onAdminRefundConfirm(ctx, chatID, msgID, userID, orderID, amountMinor, lang)`.
- [ ] **Step 3: Gates** — standard gates PLUS race + the refund double-tap pin:

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`
Run: `go test ./internal/bot/ -race -count=1`
Run: `go test ./internal/bot/ -run 'TestAdminRefundConfirmBalanceConcurrentDoubleTap' -count=1 -v`
Expected: all clean/PASS (refundMu serialization intact).

- [ ] **Step 4: Commit**

```bash
git add internal/bot/
git commit -m "refactor(bot): thread per-update ctx through G5 admin handlers (orders/analytics/balance/promos/payreview/refunds)"
```

---

### Task 7: Cleanup — delete the handlerCtx bridge

**Files:**
- Modify: `internal/bot/bot.go` (delete `handlerCtx` `:136-148`; comments `:113-117`, `:125-131`, `:270-274`)
- Delete: `internal/bot/root_ctx_test.go`

**Interfaces:**
- Consumes: zero remaining `handlerCtx` references (Tasks 1–6).
- Produces: no `handlerCtx` symbol anywhere.

- [ ] **Step 1: Verify no remaining users, then delete**

Run: `grep -rn "handlerCtx" internal/ cmd/ worker/`
Expected: matches ONLY in `internal/bot/bot.go` (definition + comments) and `internal/bot/root_ctx_test.go`. If ANY other match exists: STOP — a task left a bridge site; fix it first.

- [ ] **Step 2: Delete `handlerCtx()` from bot.go** and rewrite the three stale comments:
  - `rootCtx` field comment (`:113-117`) →

```go
	// rootCtx is the process-lifetime cancellation root (SetRootContext) that
	// newUpdateCtx derives per-update contexts from. Nil until set:
	// newUpdateCtx falls back to context.Background(), so direct/test
	// constructors keep working unchanged.
```

  - `SetRootContext` doc (`:125-131`) →

```go
// SetRootContext installs the process-lifetime cancellation root that
// per-update contexts derive from (newUpdateCtx). Call ONCE before starting
// the bot — main passes its signal.NotifyContext ctx — so shutdown
// cancellation reaches in-flight update work instead of letting it outlive
// the process signal by up to the 30s per-update bound. Nil-safe: an unset
// (or nil) root leaves newUpdateCtx on its context.Background() fallback.
```

  - `ensureHandler` inner comment (`:271-273`): "a per-handler handlerCtx (30s) would kill it" → "a per-update ctx (30s) would kill it".
- [ ] **Step 3: Delete `internal/bot/root_ctx_test.go`** (superseded by `update_ctx_test.go`, Task 1).
- [ ] **Step 4: Verify zero residue**

Run: `grep -rn "handlerCtx" internal/ cmd/ worker/`
Expected: EMPTY (no output).

- [ ] **Step 5: Gates** — standard gates PLUS race:

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`
Run: `go test ./internal/bot/ -race -count=1`
Expected: clean/PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/bot/
git commit -m "refactor(bot): delete handlerCtx bridge — per-update ctx is the only handler context (4.14)"
```

---

### Task 8: Observability — trace_id on payment-critical logs

**Files:**
- Modify: `internal/bot/handlers_payment.go` (log sites `:570`, `:579`, `:603`, `:630`, `:636`, `:645`, `:654`, `:660`, `:667`, `:677`, `:687` — all inside `handlePreCheckout`/`handleSuccessfulPayment`/`processSuccessfulPayment`, which have ctx since Tasks 1/4)
- Modify: `internal/bot/bot.go` (polling barrier error log in `Run`)
- Modify: `internal/bot/webhook.go` (telegram barrier error log)
- Test: `internal/bot/payment_ack_test.go` (new pin)

**Interfaces:**
- Consumes: `b.loggerFor(ctx)` (Task 1), ctx in scope at all listed sites.
- Produces: settle/renewal/quarantine log lines carry `trace_id`.

- [ ] **Step 1: Write the failing test** — append to `internal/bot/payment_ack_test.go`:

```go
func TestTelegramWebhookStarsQuarantineLogCarriesTraceID(t *testing.T) {
	e := newE2EEnv(t)
	e.bot.cfg.TelegramWebhookSecret = testTelegramWebhookSecret
	var logs bytes.Buffer
	e.bot.logger = slog.New(slog.NewTextHandler(&logs, nil))
	body := telegramSuccessfulPaymentBody(11, 6201, "not-an-order", "stars-trace-1", 500)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/telegram-webhook", strings.NewReader(body))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", testTelegramWebhookSecret)
	e.bot.TelegramWebhookHandler()(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}

	line := ""
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "Stars payment quarantined: invalid order payload") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("quarantine log missing, got: %s", logs.String())
	}
	if !regexp.MustCompile(`trace_id=[0-9a-f]{16}`).MatchString(line) {
		t.Fatalf("quarantine log lacks 16-hex trace_id: %q", line)
	}
}
```

Add `"bytes"`, `"log/slog"`, `"regexp"` to the file's imports as needed.
- [ ] **Step 2: Run to verify RED**

Run: `go test ./internal/bot/ -run TestTelegramWebhookStarsQuarantineLogCarriesTraceID -count=1 -v`
Expected: FAIL — "quarantine log lacks 16-hex trace_id".

- [ ] **Step 3: Convert the log sites** — pattern: `b.logger.` → `b.loggerFor(ctx).` at exactly these sites (fields unchanged):
  - `handlers_payment.go`: `handlePreCheckout` error (`:570`); `handleSuccessfulPayment` error (`:579`); inside `processSuccessfulPayment`: `:603` invalid-payload Warn, `:630` + `:636` renewal-quarantine Warns, `:645` renewal-settled Info, `:654` stock-conflict Warn, `:660` idempotent Info, `:667` durably-quarantined Warn, `:677` quarantined Warn, `:687` settled Info.
  - `bot.go` Run barrier: `b.logger.Error("polling Stars payment not durably handled", "update_id", update.UpdateID, "error", err)` → `b.loggerFor(uCtx).Error(...)` (same fields; `uCtx` in scope since Task 1).
  - `webhook.go` telegram barrier: `b.logger.Error("telegram webhook: Stars payment not durably handled", "error", err)` → `b.loggerFor(uCtx).Error("telegram webhook: Stars payment not durably handled", "error", err)`.
  - OUT OF SCOPE (stay `b.logger`): all `onPay*` invoice-creation logs (`:24-539`), the decode-failure logs (no update ctx exists there — quarantineUndecodableStarsUpdate path uses the Run/request ctx), provider-webhook files. Fleet-wide sweep is a follow-up (HANDOFF §6).
- [ ] **Step 4: Gates**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./internal/bot/ -count=1`
Expected: clean + EMPTY + PASS (new pin GREEN).

- [ ] **Step 5: Commit**

```bash
git add internal/bot/
git commit -m "feat(bot): trace_id on Stars settle/renewal/quarantine and barrier logs (4.14 observability)"
```

---

### Task 9: Docs — CHANGELOG + payment-operations §12

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased]`, Quality-style entry)
- Modify: `docs/payment-operations.md` (§12 addition)

**Interfaces:**
- Consumes: everything above (docs describe the merged behavior).
- Produces: operator-facing truth about per-update ctx + trace_id.

- [ ] **Step 1: CHANGELOG** — append to `[Unreleased]` (match house entry style):

```markdown
- **Update ctx + trace (roadmap 4.14).** Every Telegram update now runs under
  ONE per-update context derived at ingress (polling dispatch, polling payment
  barrier, Telegram webhook) from the process root: a single 30s budget per
  update (replacing 4.7's per-handler windows) plus a per-update `trace_id`
  (crypto/rand hex, no new dependencies). The middleware chain (incl. `Auth`,
  which loses its ctx factory) and all handler call sites take `ctx` as the
  first parameter; the `handlerCtx()` bridge is gone. LoggingMiddleware and
  RecoverMiddleware log `trace_id`+`update_id`, and the Stars
  settle/renewal/quarantine + payment-barrier logs carry `trace_id`, so all
  lines of one update correlate.
```

- [ ] **Step 2: payment-operations.md §12** — append a subsection (after the actor-attribution content):

```markdown
### Update tracing (roadmap 4.14)

Every Telegram update is processed under a single per-update context derived
at ingress (30s budget, cancelled with the process root). It carries a
per-update `trace_id` (16 hex chars, crypto/rand; new id per processing
attempt — redeliveries of one `update_id` get fresh traces). Operator-facing
correlation: `LoggingMiddleware` ("incoming update") and `RecoverMiddleware`
panic logs always include `trace_id` and `update_id`; the Stars
settle/renewal/quarantine logs and the payment-barrier error logs include
`trace_id`. To reconstruct one update's path: `grep trace_id=<id>` across the
bot log. Provider-webhook and worker settles are not part of the update chain
— their attribution remains the `actor=` field (§12 above).
```

- [ ] **Step 3: Gates** — docs only: `gofmt -l internal/ cmd/ worker/` EMPTY (paranoia) + render check by reading the diff.
- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md docs/payment-operations.md
git commit -m "docs: 4.14 update-ctx+trace — CHANGELOG entry, payment-operations §12 tracing note"
```

---

## Self-Review Notes

- **Spec coverage:** Q1 (chain sig) → T1 S4/S8; Q2 (trace mechanism) → T1 S3; Q3 (derivation at all ingresses) → T1 S3/S6/S7 + HandleUpdate; Q4 (single budget) → T1 S3 + group tasks' two-window deletions (T1 S9, T2 S1, T3 S1), pinned by Review Focus #4; Q5 (handlerCtx removal) → T7; Q6 (handler signatures) → T2–T6; Q7 (staged bridge) → task structure itself + T7 grep-to-zero; Q8 (loggerFor scope) → T1 S3 + T8. Non-goals respected: no deps/migrations/locale changes; provider webhooks & workers untouched (except the telegram-webhook ingress itself).
- **Placeholder scan:** every task carries exact code or exact per-site instructions + verification commands; line numbers are pre-migration anchors with locate-by-content fallback note (T3 S2).
- **Type consistency:** `func(ctx context.Context, update tgbotapi.Update)` everywhere; `newUpdateCtx(root, update)` / `TraceID(ctx)` / `loggerFor(ctx)` names identical across tasks; sendMainMenu stays ctx-LAST (pre-existing, noted in T2).
- **Shared-file conflicts:** `internal/bot/handlers.go` is touched by T1–T6 (dispatch call sites per group) and `handlers_payment.go` by T1/T4/T8 — tasks are strictly sequential (SDD), never parallel.
- **Review Focus tests:** all five lines have pinned tests named in the owning tasks (T1: #1/#2/#3/#5, T3: #4 checkout, T2: #4 start, T4: #1 re-run, T6: refundMu pin, T8: #5 e2e).
- **Risk — unused ctx param mid-migration** (e.g. handleCallback in T1): legal Go, vet-clean; disappears as groups land.
- **Risk — `context` import missing** in a touched file: compile catches; implementer adds.
- **Post-merge janitorial (controller, not a task):** roadmap 4.14 ✅ + merge SHA; HANDOFF §1 state + §8 registry row; spec header status flip.
