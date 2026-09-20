# Admin-Initiated Refunds Plan (roadmap 4.4)

> **Status:** In progress
> **Created:** 2026-09-21
> **Ruling:** NO auto-triggers — refunds are operator-initiated from the bot admin
> panel with two-tap confirm. Provider execution where APIs exist (Stars, Stripe,
> YooKassa, balance); informational routing where they don't (crypto, TON,
> NOWPayments — manual at provider dashboards, recorded via the existing
> `payment-review` refund CLI).

## Goal

`/refund <order_id> [amount]` closes the money lifecycle: every executable rail can
be refunded from Telegram, with the refund durably recorded in the immutable ledger
(`IngestProviderRefund` — preview-then-apply, cumulative cap enforced).

## Context (controller-audited)

- Ledger refund machinery EXISTS and is tested: `PreviewProviderRefundIngress` /
  `IngestProviderRefund` (storage/ledger.go:125+, payment_ingress.go:63+); the
  payer-corroboration fix (both-positive rule) makes payerless-capture rails
  (yookassa/stripe/ton/nowpayments) refundable with `PayerID = order.UserID`.
  Ingest flips the order to `refunded`. `Bot.payLedger` is already injected
  (admin Task 2 payreview).
- Refund struct fields: OrderID, Provider, ExternalID (provider refund id),
  PaymentExternalID (original capture id = order.PaymentID), PayerID, AmountMinor,
  Currency, Scale, OccurredAt.
- Stars: `orders.payment_id` = telegram_payment_charge_id. tgbotapi v5 has no typed
  refundStarPayment — use `b.api.MakeRequest("refundStarPayment", params)` (the
  raw-request path webapi's TelegramAPI interface already uses). FULL refunds only
  (Telegram has no partial star refund) — partial amount for stars → reject with
  an explanatory message.
- Stripe: refund needs `payment_intent` — `GetCheckoutSession` must additionally
  parse it; `POST /v1/refunds` form-encoded (`payment_intent`, `amount` cents for
  partial, omit for full; `reason=requested_by_customer`). Response `{id: "re_...",
  status}` — record on succeeded/pending.
- YooKassa: `POST /v3/refunds` JSON `{"amount":{"value":"<2dp string>",
  "currency":"rub"},"description":...,"payment_id":"<order.PaymentID>"}` + fresh
  uuid `Idempotence-Key` (house pattern). Response `{id, status}` — record on
  succeeded/pending (comment: pending refunds complete asynchronously; the ledger
  fact is the initiation).
- Balance: credit back via `AdjustBalance(+amount, "order_refund:<orderID>",
  adminID)` — the balance_txs audit is the record; ledger refund row via
  IngestProviderRefund with ExternalID `balance-refund:<orderID>`.
- Ordering ruling: **provider refund FIRST, ledger record SECOND** (money-out is the
  irreversible step; a ledger failure after a successful refund is re-runnable —
  ExternalID idempotency makes the retry a replay; the inverse order could record
  money that never left).
- Double-confirm safety: the confirm callback re-runs the preview immediately before
  apply (targets can drift — same TOCTOU discipline as payreview).

## Tasks

### Task 1: Adapter refund methods

- `internal/payment/stripe.go`: add `payment_intent` to the session struct +
  GetCheckoutSession parse; `CreateRefund(ctx, paymentIntentID string, amountCents
  int64) (*RefundResult, error)` (form-encoded POST /v1/refunds; amountCents<=0 →
  omit amount = full; response id+status; error mapping reuse).
- `internal/payment/yookassa.go`: `CreateRefund(ctx, paymentID string, amountMinor
  int64, description string) (*RefundResult, error)` (JSON POST /v3/refunds;
  amount string `%.2f` from minor/100 + currency "rub"; fresh uuid Idempotence-Key;
  response id+status; error mapping reuse).
- Shared `RefundResult{ID, Status string}` (or per-provider structs — mirror the
  house style; report the choice).
- Tests: form/JSON body pins, idempotence-key freshness (yookassa), full-vs-partial
  legs, status passthrough, error mapping, payment_intent parse leg.

### Task 2: Bot `/refund` command + confirm flow

- New `internal/bot/admin_refunds.go`: `/refund <order_id> [amount]` (admin-gated):
  - load order; reject non-paid (status/payment_state gates — read what "refundable"
    means for the ledger preview and mirror); reject unknown.
  - amount default = full (order total in the rail's minor units via orderMoney's
    published shape — read how the CLI derives expected amounts; reuse, don't
    duplicate: the launcher has `expectedProviderCaptureAmount` — if unexported and
    launcher-local, re-derive in the bot with a comment, or promote a shared helper
    into storage — choose minimal-churn, report).
  - rail dispatch: stars (full-only rule) / stripe (needs payment_intent: call
    GetCheckoutSession first) / yookassa / balance → preview card (order, rail,
    amount, provider action) + confirm button `admin:refund:<orderID>:<amountMinor>`;
    crypto/ton/nowpayments → informational card (manual dashboard step + the CLI
    recording command line).
  - confirm callback: re-preview (`PreviewProviderRefundIngress`) → execute provider
    refund → `IngestProviderRefund` (audit: actor `admin:<tgID>`) → result message
    (refunded + refund id / or the failure with the provider error). Ledger failure
    AFTER a successful provider refund → loud error message instructing re-run
    (idempotent) — never silent.
  - Router: command + `admin:refund:` callback prefix (isAdmin re-check per house
    pattern).
- Locales ×5: `admin_refund_*` family (card, confirm button, result, errors:
  not-paid, partial-stars, manual-rail, provider-failed, ledger-failed-rerun) +
  `/refund` line in `admin_panel`.
- Tests: preview cards per rail; confirm flow end-to-end with mock provider APIs
  (stripe/yookassa httptest via SetBaseURL; stars via the mock Telegram API's
  MakeRequest path — check how e2e mocks raw requests) asserting: provider called
  with correct params, ledger refund row, order `refunded`, balance credited
  (balance rail); double-confirm → replay (no double provider call — the second
  preview sees the recorded refund → replay outcome → no execution; pin); non-admin
  inert; non-paid rejected; stars-partial rejected; manual rails informational;
  provider-failure → NO ledger write (ordering ruling pinned).

### Task 3: Docs + CHANGELOG + roadmap

- payment-operations.md: refunds sections per rail updated (bot `/refund` for the
  executable rails; manual+CLI for the rest; the ordering ruling + re-run semantics;
  cumulative cap; no restock — fulfillment is manual).
- README EN/RU: `/refund` in the admin feature list.
- CHANGELOG [Unreleased]; roadmap 4.4 → ✅.
- Final gate: build + vet + gofmt + full suite + targeted -race.

## Self-Review Notes

### Spec coverage
Provider-first/ledger-second ordering, two-tap TOCTOU discipline, cumulative cap,
full-only stars, informational manual rails — each has a dedicated test leg in
Task 2's matrix.

### Out of scope
Auto-refund triggers (any form); stock restock on refund; partial refunds for
stars/crypto; NOWPayments/crypto refund APIs (don't exist for our flows); refund
webhooks/IPN listeners (the ledger records at initiation; provider-side async
completion is dashboard-visible).
