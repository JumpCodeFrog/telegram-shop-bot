# Quality Sweep Plan

> **Status:** In progress
> **Created:** 2026-09-20 (night program «Roadmap Zero», Stage D)
> **Input:** consolidated deferred-FOLLOW-UP lists from the YooKassa / Stripe /
> crypto-expansion / admin-management ledgers (each ledger's "Final review: triage"
> items, already triaged to FOLLOW-UP by the whole-branch reviews).

## Goal

Close every FOLLOW-UP with a home in the quality phase: security-boundary test
hardening, context propagation, code hygiene, the generic payment-ingress CLI,
text/docs pass, and the webapp availability flags.

## Tasks

### Task 1: Context propagation (bot)

- New helper `Bot.handlerCtx()` (context.WithTimeout(context.Background(), 30*time.Second))
  replacing every `context.Background()` in internal/bot handlers (~50 sites across
  admin_*.go, handlers_*.go, bot.go — grep them all).
- Webhook handlers (webhook.go ×4): use `r.Context()` instead (free propagation —
  the request IS the context), keeping the 30s-timeout helper ONLY where no request
  context exists (keyboard/message handlers).
- No behavior change beyond cancellation semantics; tests stay green.

### Task 2: Security-boundary test hardening (bundle)

- **invalid-receipt quarantine legs ×3**: yookassa (webhook.go:210-220 — refetched
  payment whose receipt build fails, e.g. missing metadata order_id →
  webhook_invalid_receipt anomaly + 200), stripe (:318-329 — signed complete session
  with missing order ref), nowpayments (signed finished IPN with unparsable order_id).
- **500-on-quarantine-failure legs ×3**: anomaly write fails (closed DB or equivalent
  harness) → 500, nothing settled.
- stripe hex-skip pin: multi-v1 with an INVALID-hex v1 first, valid last → verifies.
- Mock API method+path pins: yookassa webhook test mock asserts GET + /v3/payments/
  prefix (T6.5); the E2E mock likewise (T9.1).
- attempts-scan restoration ×4: payment_receipt_test.go overwritten-scan warts
  (yookassa :587 area + stripe + ton + nowpayments twins) — restore the discarded
  first scan's assertion with separate vars.

### Task 3: Code hygiene batch

- Remove dead `drained()` helper (yookassa_webhook_test.go:131).
- exchange.go: stale "USD→Stars" comments (:8-10, :31) corrected; ConvertUSDToNanoTON
  gains the intermediate-overflow guard (`math.IsInf(usd*1e9, 0)` → 0, comment).
- ton.go: GetTransactions limit clamped to [1, 100] (comment: toncenter bound).
- validation.go: replace `strings.HasPrefix(url, "https://")` with a shared
  `isHTTPSURL(string) bool` using url.Parse (scheme=="https" && host!=""), applied at
  EVERY provider check (yookassa/stripe/nowpayments/ton-independent); behavior change:
  `HTTPS://` uppercase now ACCEPTED (url.Parse normalizes scheme? NO — Parse keeps
  case; lowercase the scheme before compare) — pin with tests both directions.
- Migration test shared harness: extract the common apply-from-NNN + inventory
  assertion helpers used by migration_017/020/021/022 tests into one unexported
  helper file in the storage test package (behavior-neutral test refactor).
- Replay legs re-assert `payment_state=settled` (T3c.1: the yookassa replay legs in
  payment_receipt_test.go / order_state_test.go — find every replay leg and add the
  payment_state pin where missing).
- LoyaltyWorker: `*storage.LoyaltyStoreImpl` → minimal interface (АРХ-3 residue).
- TON button label duplication (T7.3): `💎 TON (1.5 TON)` → drop the duplicated
  currency suffix (keep the amount) — check the label-building code + tests.

### Task 4: Generic payment-ingress CLI (non-stars providers)

- Extend `internal/launcher/payment_ingress.go` (currently Stars-only `ingest-stars`)
  with a generic subcommand (e.g. `payment-review ingest-provider --provider ton
  --order <id> --amount-minor <n> --external-id <lt>:<hash> --occurred-at <ts>`)
  flowing through `PreviewProviderCaptureIngress` + `IngestProviderCapture` (the
  storage gates already accept all providers; the payer predicate applies).
- This closes the documented gap: memo-less TON payments become operator-resolvable
  (docs §7 currently prescribes manual refund only — update the docs to mention the
  CLI path once it exists).
- Tests: CLI legs mirroring the stars ingest tests + a memo-less-TON scenario
  (preview quarantine → resolve via payment-review flow consistency).

### Task 5: Text + docs pass

- docs/faq.md: YooKassa entry (mirrors the CryptoBot entry's shape).
- payment-operations.md §8: live-test note "should"→"must".
- docs/architecture.md:142: payment notification description updated for
  AnnouncePaidOutcome (worker path) vs NotifyPaymentOutcome.
- validation.go error message: key+rate-without-address names BOTH (T2.2-crypto).
- .env.example: stripe placeholders emptied (final-review follow-up; keep comments).
- e2e_test.go comment typos: `$1849.08 = ... kopecks` ($ on RUB), stale file header
  ("never localized message texts" — the suite now asserts localized texts via t()).
- payment-operations §6 cross-reference polish (T7-stripe).

### Task 6: Webapp availability flags

- cartJSON gains: `total_rub`, `total_ton_nano`, and per-rail availability booleans
  (`yookassa_enabled`, `stripe_enabled`, `ton_enabled`, `nowpayments_enabled`,
  `balance_enabled` — balance needs the viewer's balance>0; crypto/stars stay
  unconditional as today... check what the current buttons do and keep the change
  minimal: the four NEWER card/crypto rails become conditional; stars/crypto remain
  as-is to avoid scope creep — OR make all six consistent if trivially safe;
  implementer reports the choice).
- app.js: buttons conditional on the flags (replacing the sanctioned unconditional
  fallback); docs qualifier: README/payment-operations "button stays hidden" claims
  gain the surface qualifier (bot vs webapp semantics now match).
- Tests: cartJSON legs (flags present + correct per config matrix), existing webapi
  tests green.

### Task 7: Final verification + CHANGELOG + roadmap

- CHANGELOG [Unreleased]: quality-sweep entry (context propagation, security test
  hardening, hygiene, generic ingress CLI, webapp availability flags).
- roadmap.md: mark Stage D items done.
- Full gate: build + vet + gofmt + full suite + targeted -race; golangci-lint if
  available (`command -v golangci-lint`).

## Self-Review Notes

### Spec coverage
Every ledger FOLLOW-UP has a task above. WAIVE'd items stay waived (recorded in the
ledgers). Nothing here touches settlement semantics — hardening only.

### Out of scope
New features; provider changes; the postponed backlog (Topics, deep analytics,
Coinbase/BTCPay, auto-refunds, YooKassa lost-webhook poller).
