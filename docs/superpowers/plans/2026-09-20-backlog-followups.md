# Backlog Follow-Ups Plan (roadmap 4.6, 4.8-4.12)

> **Status:** In progress
> **Created:** 2026-09-20 (night program continuation — user directive «продолжай согласно роадмапу»)
> **Scope:** the six LOW-complexity roadmap items in one batch; 4.7/4.13 (Medium)
> and features (4.1/4.2/4.4/4.5) get their own plans next.

## Tasks

### Task 1: Webhook hygiene bundle (4.6 + 4.8 + 4.11)

- **4.6**: the factless-envelope branch (yookassa webhook.go:173-179 area — valid
  signature-less envelope that parses but carries no payment id) currently records a
  `webhook_parse_failure` anomaly; re-tag it `webhook_missing_payment_id` (more
  accurate reason for operators). Update every test pin of that branch (quality-sweep
  locked the reason strings — flip them deliberately; this is the sanctioned retag).
- **4.8**: bot-layer `out_of_stock_after_charge` coverage for the FOUR webhook
  handlers (crypto/yookassa/stripe/nowpayments) + Stars `successful_payment`: stage
  an order whose product stock is depleted between order creation and the payment
  event → webhook settles → assert the quarantine surface (needs_review attempt+event,
  anomaly per the actual mechanism — mirror the TON worker's Task 9 fix-round test:
  `out_of_stock_after_charge`, order NOT paid). The TON worker + storage levels are
  already covered; this is the bot-surface gap.
- **4.11**: `TestYooKassaWebhookReplayIsIdempotent` gains the `payment_state=settled`
  pin (storage level already pins it; bot level didn't).

### Task 2: Ingress CLI hardening (4.9 + 4.10)

- **4.9 single-source the TON `>=` rule into storage**: `validatePaymentFact`'s ton
  case currently SKIPS amount exactness by design (overpay tolerance was pushed to the
  receipt layer). Move the rule INTO the storage gate: for ton, `fact.AmountMinor >=
  expectedAmount` (expectedAmount = orderMoney's TotalTonNano) — underpay facts rejected
  at storage for EVERY caller (webhook path keeps its shop-layer check as
  defense-in-depth; the launcher's `providerCaptureSettleable` becomes redundant but
  stays — harmless double guard, comment it). Verify the crypto-expansion E2E + receipt
  tests stay green (overpay settles, underpay quarantines via BOTH layers now).
- **4.10 actionable mismatch errors**: `ingest-provider` currently prints generic
  "local preview failed" when `validatePaymentFact` rejects an amount (card rails);
  map the sentinel errors (`ErrPaymentReceiptMismatch` / `ErrInvalidMoney`) to distinct
  operator messages naming the fact-vs-order amounts (read the preview error path;
  the CLI has both numbers available). Exit codes unchanged (1 operational).
- Tests: storage legs (ton underpay fact rejected at validatePaymentFact; overpay
  accepted; stars/crypto/stripe/nowpayments exactness unchanged); CLI legs (mismatch
  message content per rail).

### Task 3: Exchange guard (4.12)

- `ConvertUSDToNanoTON`: the division result can still hit +Inf for absurd inputs
  (usd=1e200, rate=1e-200) → `int64(+Inf)` garbage. Guard the QUOTIENT too
  (`math.IsInf(q, 0) || math.IsNaN(q)` → 0) + test leg. Comment.

## Rules

- Each task: TDD where behavior changes; full `go build ./... && go test ./...` +
  gofmt empty before commit; one commit per task.
- 4.6/4.11 flip/extend EXISTING test expectations deliberately (call them out in
  commit bodies).
- After all three: roadmap.md §4 rows 4.6/4.8-4.12 marked done; CHANGELOG quality
  subsection extended.
