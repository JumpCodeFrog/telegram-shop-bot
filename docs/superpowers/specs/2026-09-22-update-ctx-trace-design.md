# Design Spec — Roadmap 4.14: per-update ctx + trace propagation

> **Status:** IMPLEMENTED 22.09.2026 — merge `c67e66e` (branch `feat/update-ctx-trace`, 9 tasks + final-review fix, ledger `.superpowers/sdd/2026-09-22-update-ctx-trace/` deleted post-merge; rulings digest — HANDOFF §9).
> **Author:** controller session 21–22.09.2026 (after `chore/money-followups` merge
> `4dd7842` and `chore/polish-followups` merge `5420f3b`).
> **Base for planning:** main tip at session start (run `git log --oneline -3`; this spec
> was written against `6fe11d7`). Branch name suggestion: `feat/update-ctx-trace`.

---

## 1. Goal / Non-goals

**Goal (roadmap 4.14, verbatim):** «Полное update-ctx propagation: per-update ctx + trace
через весь middleware/handler chain (chain type сейчас `func(tgbotapi.Update)`; остаток 4.7
после root-context derivation — shutdown-отмена уже доходит до хендлеров)».

Concretely:
1. Every Telegram update is processed under ONE per-update `context.Context` derived at the
   ingress from the process-lifetime root (4.7's `SetRootContext`), carrying a 30s bound and
   a per-update trace id.
2. The middleware chain type becomes ctx-aware: `func(ctx context.Context, update tgbotapi.Update)`.
3. All ~79 production `b.handlerCtx()` call sites (24 files) are replaced by the propagated ctx.
4. Structured logs of one update correlate via `trace_id` (LoggingMiddleware, RecoverMiddleware,
   payment-critical handler logs).

**Non-goals:**
- NO OpenTelemetry or any tracing library — the «no new dependencies» invariant stands;
  trace = lightweight correlation id only.
- Provider webhooks (`/yookassa-webhook`, `/stripe-webhook`, …) are NOT part of the update
  chain and already run under `r.Context()` with actor attribution (4.13) — untouched.
- Workers keep their own process-lifetime contexts — untouched.
- No change to the 30s bound value, no change to rate-limit/auth semantics, no checkout-surface
  changes (invariant 6: byte-identical when providers disabled).
- Roadmap 4.15 (durable actor column) is a SEPARATE follow-on (sketch in §9 — it consumes
  this plan's ctx plumbing but must not be folded into it).

## 2. Current architecture (verified at `6fe11d7`, file:line map)

**Chain type & composition**
- `internal/bot/middleware.go:17` — `type Middleware func(handler func(update tgbotapi.Update)) func(update tgbotapi.Update)` (no ctx).
- `middleware.go:54` LoggingMiddleware (logs type/user_id/duration AFTER handler returns), `:82` RecoverMiddleware, `:101` AdminOnly, `:127` RateLimitMiddleware (constructor takes a process-lifetime ctx for its hourly cleanup goroutine — this ctx is NOT per-update and must stay long-lived), `:176` `Chain(handler, middlewares...)`.
- `internal/bot/middleware/auth.go:17` — `Auth(userStore, ctxFn func() (context.Context, context.CancelFunc))`; comment :15-16 states the chain carries no context — the factory exists solely for this. After 4.14 Auth takes ctx from the chain and the factory parameter disappears.
- `internal/bot/bot.go:120` — `handler func(tgbotapi.Update)` field; `:259-267` `prepareHandler` builds `Chain(b.route, Logging, Recover, Auth(b.users, b.handlerCtx), RateLimit(ctx,…))`; `:269-279` `ensureHandler` (handlerOnce; long-lived ctx for rate-limit cleanup).
- `internal/bot/handlers.go:11` — `func (b *Bot) route(update tgbotapi.Update)` (chain terminus); `:28` `routeMessage` (takes its own `routeCtx` via handlerCtx at `:34`); `:388` one more handlerCtx site; `admin_products.go:279` `routeEditProduct` sub-router.

**Ctx plumbing today (4.7 residual)**
- `bot.go:113-117` `rootCtx` field (nil until `SetRootContext` `:132-134`; main wires `signal.NotifyContext`).
- `bot.go:136-148` `handlerCtx()` = `WithTimeout(rootCtx|Background, 30s)` — called PER HANDLER INVOCATION (~79 production sites; each handler does `ctx, cancel := b.handlerCtx(); defer cancel()`).
- `internal/bot/root_ctx_test.go` pins handlerCtx derivation (unset root → Background+deadline; cancelled root → cancelled ctx; nil root → fallback).

**Ingress paths (three)**
1. Polling `Run` (`bot.go:360-461`): per-update goroutine `go func(upd){ b.handler(upd) }(update)` (`:443-448`) — CONCURRENT processing ⇒ per-update ctx can only travel as a call-stack parameter (never a Bot field; data race).
2. Polling durable-payment barrier (`bot.go:417-438`): `successful_payment` updates bypass the chain — `wg.Wait()` then synchronous `b.processSuccessfulPayment(update.Message)` (`handlers_payment.go:587`; internal handlerCtx sites `:598`, `:607`). Ordering barrier semantics (offset advance only after durable settle) MUST be preserved.
3. Telegram webhook (`webhook.go` ~`:555-570`): same barrier split — `processSuccessfulPayment` directly, else `b.HandleUpdate(update)`; `HandleUpdate` (`bot.go:465-470`) calls `ensureHandler(context.Background())` + `b.handler(update)`. The HTTP handler HAS `r.Context()` available (request-scoped, cancelled on shutdown) — currently unused for the update ctx.

**Test harness coupling**
- `internal/bot/e2e_test.go:147` — `handle func(tgbotapi.Update)` field; `:208-211` builds the production chain minus rate limiting: `middleware.Auth(b.users, b.handlerCtx)(b.route)`; `e.cmd/e.cb/e.do` dispatch through it.
- `internal/bot/middleware_test.go:257-277` — chain/middleware unit tests on the old signature.
- `root_ctx_test.go` — pins handlerCtx (to be replaced by newUpdateCtx pins).

**Scale (verified by grep at planning time — re-verify before writing the plan):**
79 production `b.handlerCtx()` sites in 24 files:
admin_analytics 1, admin_balance 1, admin_categories 4, admin_orders 4, admin_payreview 4,
admin_photos 3, admin_products 7, admin_promos 3, admin_refunds 2, admin_styles 3,
handlers.go 2, handlers_cart 7, handlers_catalog 4, handlers_checkout 4, handlers_inline 1,
handlers_orders 1, handlers_payment 11, handlers_referral 1, handlers_reviews 6,
handlers_search 1, handlers_start 3, handlers_subs 2, handlers_wishlist 3, profile 1.

## 3. Design Rulings (approved; the plan's Global Constraints inherit them)

- **Q1 — chain signature:** `func(ctx context.Context, update tgbotapi.Update)` everywhere
  (Middleware type, Chain, `b.handler` field, route/routeMessage/routeEditProduct, all five
  middlewares, Auth). Idiomatic ctx-first. Concurrency (§2 ingress 1) forbids any
  request-scoped state on Bot fields.
- **Q2 — trace mechanism (no new deps):** per-update trace id = 8 random bytes from
  `crypto/rand`, hex-encoded (16 chars). Stored in ctx under an UNEXPORTED key type
  (`type updateCtxKey struct{}` + `traceIDKey`), read via exported-to-package helper
  `TraceID(ctx) string` ("" when absent). On crypto/rand failure fall back to
  `fmt.Sprintf("%016x", uint64(update.UpdateID))` — ingress must never crash. Redelivered
  updates get a NEW trace id per processing attempt (correlation is per-attempt; `update_id`
  field distinguishes redeliveries). NOT OpenTelemetry; no spans, no propagation headers.
- **Q3 — per-update ctx derivation:** new `func (b *Bot) newUpdateCtx(root context.Context, update tgbotapi.Update) (context.Context, context.CancelFunc)`:
  nil root → `b.rootCtx` → `context.Background()` fallback chain (preserves 4.7 nil-safety for
  direct/test constructors); attaches trace id (Q2); `context.WithTimeout(…, 30*time.Second)`.
  Called at EVERY ingress: polling goroutine (root = `Run`'s ctx), polling barrier
  (root = `Run`'s ctx), webhook (root = `r.Context()` — request lifetime composes with
  shutdown), `HandleUpdate` (root = `b.rootCtx`|Background; signature gains a ctx variant —
  plan decides: `HandleUpdate(ctx, update)` with all callers updated, grep `HandleUpdate(` —
  webhook.go + cmd/telegram-smoke + local tooling).
- **Q4 — timeout model:** ONE 30s budget per update, derived at ingress (replaces per-handler
  fresh 30s). Semantics delta accepted: a flow that today takes two handlerCtx windows
  (e.g. handlers_checkout.go:18 fsmCtx + :44 ctx) gets a single shared 30s window — this is
  the INTENT of «per-update ctx» and bounds total work per update. Provider adapters keep
  their own inner HTTP timeouts (10s). Shutdown cancellation still reaches in-flight DB work
  via the rootCtx derivation (4.7 property preserved — pin it).
- **Q5 — handlerCtx removal:** `handlerCtx()` is DELETED at the end (cleanup task); during
  migration it survives as the bridge (§4). `rootCtx`/`SetRootContext` STAY (newUpdateCtx
  consumes them). `root_ctx_test.go` is rewritten onto `newUpdateCtx` (same three properties:
  unset root → deadline+Background, cancelled root → cancelled, nil → fallback) + new legs:
  trace id present/unique, 30s deadline.
- **Q6 — handler signatures:** every handler method dispatched by route/routeMessage/
  routeEditProduct gains `ctx context.Context` as the FIRST parameter; the method body drops
  `ctx, cancel := b.handlerCtx(); defer cancel()` and uses the parameter. Internal helpers
  called only from one handler may take ctx too where natural (plan's per-file tasks decide;
  keep diffs mechanical). Direct-call test sites update in the same task as their file.
- **Q7 — migration strategy: STAGED WITH A TEMPORARY BRIDGE (not one atomic commit).**
  The signature change is compile-atomic in the strict sense (route's call sites must match
  handler signatures), BUT it decomposes cleanly: T-core switches chain+route+ingress to
  `(ctx, update)` while route TEMPORARILY lets each still-unmigrated handler create its own
  `handlerCtx()` (bridge — behaviorally identical: 30s from the same root); then per-group
  tasks migrate handler files (signature + call sites in route + tests), each commit
  compilable and gate-green; T-cleanup deletes `handlerCtx` and the bridge. Rejected
  alternative: one ~1500-line atomic commit — worse reviewability, no bisect granularity,
  all-or-nothing risk. The bridge is marked with `// TODO(4.14 T<n>):` comments so no
  half-state survives silently; T-cleanup greps them to zero.
- **Q8 — logging integration scope:** new tiny helper `func (b *Bot) loggerFor(ctx) *slog.Logger`
  = `b.logger.With("trace_id", TraceID(ctx))` (no-op passthrough when trace absent). In 4.14
  it is adopted by: LoggingMiddleware («incoming update» gains `trace_id` + `update_id`
  fields), RecoverMiddleware (panic log gains `trace_id`), and the payment-critical logs
  (stars settle/renewal/quarantine in handlers_payment.go, the four provider-webhook settle
  logs are OUT — not chain code). Sweeping all ~200 handler log calls to loggerFor is a
  FOLLOW-UP (§8), not this plan — keeps the diff bounded and review-focused.

## 4. Migration bridge (compile-safety detail for the plan)

After T-core, `route(ctx, update)` dispatches e.g. `b.handleStart(ctx, msg)` for MIGRATED
handlers and, for not-yet-migrated ones, keeps `b.handleX(msg)` where the handler still does
`ctx, cancel := b.handlerCtx(); defer cancel()`. Both forms coexist per commit; every commit
compiles and passes gates. The bridge window is ≤ the plan's duration; T-cleanup proves
completion by: `grep -rn "b.handlerCtx()" internal/bot/*.go | grep -v _test` → EMPTY, and
`grep -rn "TODO(4.14" internal/` → EMPTY.

Barrier path: `processSuccessfulPayment(msg)` gains ctx in T-core (it is an ingress-adjacent
synchronous path with only 2 internal handlerCtx sites, `handlers_payment.go:598/:607`) —
both polling barrier (`bot.go:421`) and webhook barrier (`webhook.go`) pass their
`newUpdateCtx`-derived ctx. `handleSuccessfulPayment` (:577, router-compatible wrapper)
migrates with the handlers_payment group.

## 5. Task decomposition sketch (input to writing-plans; ~9 tasks)

- **T1 core:** Middleware type + all 5 middlewares + Chain → `(ctx, update)`; Auth loses
  `ctxFn` (takes chain ctx); `newUpdateCtx` + trace-id (Q2/Q3); `b.handler` field;
  `prepareHandler`; `HandleUpdate` variant; `Run` goroutine + barrier; webhook ingress
  (root = `r.Context()`); route/routeMessage/routeEditProduct signatures + bridge;
  Logging/Recover trace fields + `loggerFor`; harness migration (e2e_test.go:147/:208-211,
  middleware_test.go); `newUpdateCtx` tests (root_ctx_test.go rewrite). Gates + `-race` on
  `./internal/bot/`. THE riskiest task — reviewer on glm-5.3, named risks: barrier ordering
  preserved, rate-limit cleanup goroutine still process-lifetime, 4.7 shutdown property pinned.
- **T2 handlers group G1** (routing-adjacent + small, 10 sites): handlers.go(2 — incl. the
  routeMessage bridge removal for its own file), handlers_start(3), handlers_search(1),
  handlers_inline(1), handlers_orders(1), profile(1), handlers_referral(1).
- **T3 G2 buyer flows** (24 sites): handlers_cart(7), handlers_catalog(4),
  handlers_checkout(4 — note the two-window→one-window semantics of Q4, pin checkout E2E),
  handlers_wishlist(3), handlers_reviews(6).
- **T4 G3 payments** (13 sites): handlers_payment(11 — incl. handleSuccessfulPayment wrapper,
  renewal leg; barrier already migrated in T1), handlers_subs(2). Payment E2E suite + `-race`.
- **T5 G4 admin A** (17 sites): admin_products(7), admin_photos(3), admin_categories(4),
  admin_styles(3).
- **T6 G5 admin B** (15 sites): admin_orders(4), admin_analytics(1), admin_balance(1),
  admin_promos(3), admin_payreview(4), admin_refunds(2 — refundMu serialization untouched;
  money-path reviewer attention).
- **T7 cleanup:** delete `handlerCtx` + bridge TODOs (grep to zero); root_ctx_test final form;
  full `go test ./... -race` on bot+worker.
- **T8 observability:** payment-critical logs → `loggerFor(ctx)` (stars settle/renewal/
  quarantine lines); pin `trace_id=` presence in the E2E log assertions for one settle path.
- **T9 docs sweep:** docs/payment-operations.md §12 short «Update tracing» note (trace_id
  correlation, per-update 30s budget); CHANGELOG Quality entry; HANDOFF (4.14 ✅ + spec
  status flip); roadmap 4.14 row ✅ with merge SHA.

Each task = one commit, own review; group tasks are mechanical but MUST update direct-call
test sites in the same commit (compile-atomic per file group). Plan writer: re-grep the site
counts (code may drift before execution) and read each group's files before writing exact code.

## 6. Verification strategy

- Gates per task: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/` (EMPTY)
  `&& go test ./...`; PLUS `go test ./internal/bot/ -race -count=1` in T1, T4, T7 (concurrent
  dispatch + money paths).
- Behavioral pins that must survive (regression armor already in the suite): full E2E set
  (buyer journeys, payreview, refunds incl. `TestAdminRefundConfirmBalanceConcurrentDoubleTap`
  — refundMu path), durable-payment barrier tests (polling offset semantics), webhook idempotency
  replays, 4.7 shutdown-cancellation property (rewritten onto newUpdateCtx), actor-log pins
  (`actor=webhook:*` counts — yookassa_webhook_test.go:295 idiom) MUST stay byte-stable except
  where T8 adds trace_id fields (update those pins deliberately in T8, called out in commit body).
- New pins: trace id uniqueness per update; same trace_id across LoggingMiddleware line and a
  handler log line of one update; cancelled root ⇒ cancelled update ctx; 30s deadline present.

## 7. Invariants & risks

- Invariants (roadmap §5): no money-semantics changes (this is plumbing); no migrations; no
  new deps (Q2); checkout surfaces byte-identical (no locale/UI changes); subscriptions
  Stars-only untouched; ledger untouched.
- **Risk 1 — barrier semantics drift** (polling `wg.Wait()`/offset ordering, webhook 500-on-
  non-durable): T1 named risk; barrier tests exist — pin before/after.
- **Risk 2 — Q4 budget sharing** changes multi-window flows (checkout): watch for new
  timeout errors in E2E; if a legitimate flow needs >30s total, the ruling is to raise the
  per-update bound ONCE (constant, documented) — not to reintroduce per-handler windows.
- **Risk 3 — harness churn:** e2e harness is used by ~all bot tests; T1 must land it atomically
  with the chain change (same commit) or nothing compiles.
- **Risk 4 — mechanical drift** in group tasks (missed direct-call test): compile catches it;
  gates per task.

## 8. Follow-ups deliberately OUT of this plan (homes recorded)

- Fleet-wide `loggerFor(ctx)` adoption across all handler log calls (observability polish;
  candidate §6 item after 4.14 lands).
- 4.15 durable actor column (§9 sketch) — separate plan AFTER this merges.
- HANDOFF §6 open items stay where they are (17-19 added 22.09.2026: shape-keyed payreview
  filter data in ListPaymentReviews; test-hardening batch; digest-row «order in needs_review»
  wording).

## 9. Roadmap 4.15 sketch (for the session AFTER 4.14; needs its own brainstorm/spec)

Durable actor attribution in the immutable ledger (today: webhook/worker settles log
`actor=…` only — docs §12; CLI and bot refunds already write durable `payment_ingress_audits`).
- Migration 023: nullable `actor TEXT` column — WHERE is the open question: `payment_events`
  (every settle/capture/refund event) is the natural home; append-only discipline means the
  column is filled at INSERT time only, existing rows stay NULL («not recorded» — no backfill,
  ledger immutable).
- Actor value flow: 4.14's ctx already reaches every settle call site — options: (a) explicit
  actor param through shop/storage APIs (visible, churny), (b) actor extracted from ctx inside
  storage (invisible coupling, needs a ctx key convention shared with bot) — brainstorm ruling
  required; (a) is the house-style default (explicit money-path APIs).
- Surfaces: `/payreview` cards + `payment-review` CLI list gain actor display; §12 rewritten
  («durable actor row» becomes true for webhook/worker settles).
- Effort: Medium; depends on 4.14 being merged (ctx availability at insert sites).

## 10. Next-session checklist

1. Read `docs/superpowers/HANDOFF.md` (state, §6 backlog incl. items 17-19, §7 process +
   lessons, §9 rulings digest) → `roadmap.md` §4/§5 → this spec → `CHANGELOG.md` [Unreleased].
2. Re-verify §2/§5 file:line and site counts by grep (code may drift); update this spec inline
   if reality moved (spec is a living doc until the plan supersedes it).
3. `superpowers:writing-plans` → `docs/superpowers/plans/<date>-update-ctx-trace.md`
   (Global Constraints inherit §3 rulings + §7 invariants; Review Focus from §6-7; per-task
   exact code — no placeholders; Self-Review incl. conflict scan of group tasks vs shared
   files: route call sites are touched by EVERY group task — sequence strictly, never parallel).
4. Branch `feat/update-ctx-trace` from main; ledger `.superpowers/sdd/<plan>/progress.md`;
   SDD pipeline per HANDOFF §7 (models: implementer/task-reviewer glm-5.3, low-risk reviews
   qwen3.8-flash, final whole-branch qwen3.8-max-0902; T1/T4/T6 reviewers glm-5.3).
5. Pre-push checklist (when the owner allows push) unchanged: `make doctor` + ONE NOWPayments
   live test payment + webhook registrations (HANDOFF §5).
