# YooKassa Lost-Webhook Poller (roadmap 4.5)

> **Status:** In progress
> **Created:** 2026-09-20
> **Pattern:** CryptoBot polling worker (worker/polling.go) + TON polling worker
> (worker/ton_polling.go) — the house's third poller.

## Goal

YooKassa retries webhooks, but a prolonged outage (bot down, network partition)
leaves paid orders stuck `pending` until an operator runs payment-review. This plan
adds a backup poller: periodically list recent `succeeded` payments from the YooKassa
API and settle any whose order is still pending — through the SAME receipt-validation
path as the webhook (shop.ConfirmPaymentReceipt), so ledger idempotency makes overlaps
no-ops.

## Binding rulings

- **Trust model:** the list endpoint is an authenticated API call over TLS — the same
  authority class as the webhook's `GetPayment` refetch. Receipts built from list items
  are equivalent to refetched ones (no body-trust problem: there is no untrusted body).
- **No new persistence:** provider-driven scan (list succeeded payments in a rolling
  `created_at` window, default 24h), NOT order-driven (created payment ids are not
  persisted pre-settlement — and adding persistence for a backup path is overkill).
  Ledger idempotency (ErrOrderStatusConflict on replay) makes re-scans safe.
- **Settlement path:** `ConfirmPaymentReceipt` with the receipt built from the list
  item (metadata.order_id → OrderID; amount/currency → the existing PaymentReceipt
  rules — reuse the adapter's session/payment → receipt mapping, do NOT duplicate it).
  Error classes: conflict/needs-review/not-found → debug log + continue (expected
  noise); out-of-stock → `RecordUnexpectedPayment(receipt, "out_of_stock_after_charge")`
  (mirror the TON worker's fix-round branch); unexpected → error log + continue.
- **Notifications:** `AnnouncePaidOutcome(ctx, outcome, storage.PaymentMethodYooKassa)`
  — the worker path's full surface (Task 12b established this for both existing pollers).
- **Gating:** worker starts only when yookassa `Configured() && cfg.USDToRUBRate > 0`
  (same predicate as the checkout button); interval 60s (webhooks are primary; this is
  a backup — no new env var, constant in main.go wiring like crypto's 30s).
- **Window/cursor:** each tick scans `created_at.gte = now-24h` with cursor pagination
  (`GET /v3/payments?limit=50&cursor=...&status=succeeded&created_at.gte=...`); stop at
  empty next_cursor or a hard page cap (e.g. 20 pages/tick — comment: a deeper backlog
  is an operator-scale event, payment-review covers it).
- **Adapter surface:** `ListPayments(ctx, status string, createdAtGte time.Time, cursor string, limit int) (items []Payment, nextCursor string, err error)` on YooKassaPayment — reuses the existing Payment struct + parsing (the list response items are the same shape as GetPayment's).

## Tasks

### Task 1: Adapter ListPayments + tests

- `internal/payment/yookassa.go`: the method above; query params URL-encoded; Bearer-ish
  auth exactly as GetPayment (read it — basic auth shopID:secretKey); response
  `{"type":"list","items":[...],"next_cursor":"..."}`; items parsed via the EXISTING
  payment parser (no duplication); SetBaseURL-covered.
- Tests (mirror yookassa_test.go style): query-param pins (status/created_at.gte RFC3339/
  cursor/limit), auth header, item parsing (a succeeded item with metadata → receipt
  builds), next_cursor passthrough, empty list, error mapping (4xx/5xx), limit clamp.

### Task 2: Worker + wiring + tests

- `worker/yookassa_polling.go` mirroring ton_polling.go's structure: interfaces
  (`YooKassaLister` for ListPayments; reuse `PaymentConfirmer` + add the
  RecordUnexpectedPayment method it needs — check ton_polling's confirmer interface),
  ticker loop, `pollOnce`-style exported test seam (mirror TON's `PollOnce`), window +
  cursor pagination + page cap, per-item: receipt build (skip unparseable/foreign
  metadata) → ConfirmPaymentReceipt → error-class handling per the rulings → notify.
- `cmd/bot/main.go`: construct the adapter instance (main-level, mirroring the crypto/
  ton worker instances), start when Configured() && rate>0, notify = AnnouncePaidOutcome
  wrapper, interval 60s.
- Tests (mirror ton_polling_test.go): settles a pending order from a list item (real
  SQLite; attempt row provider yookassa); skips already-settled (conflict → no-op, no
  notify); skips needs_review; skips foreign/missing metadata; out-of-stock →
  RecordUnexpectedPayment quarantine (mirror the TON fix-round leg: needs_review
  attempt+event, not paid, NO retry on next poll); list error → clean return; page-cap
  respected; window param correctness (created_at.gte ≈ now-24h, tolerance-pinned).

### Task 3: Docs + roadmap + CHANGELOG

- docs/payment-operations.md §5: the poller paragraph (backup for lost webhooks, 60s/24h
  window, idempotent, out-of-stock quarantine, operator escalation still payment-review
  for anything the poller can't settle).
- README/README.ru: one line in the YooKassa section (lost-webhook backup poller).
- roadmap.md: 4.5 → ✅; CHANGELOG [Unreleased] entry.
- Final gate: build + vet + gofmt + full suite (+ targeted -race on worker).

## Self-Review Notes

### Spec coverage
Third poller in the house; every shape decision mirrors an existing one (TON worker for
structure/test seam, crypto worker for windowing discipline, yookassa webhook for receipt
rules). No new trust model, no new persistence, no new env vars.

### Out of scope
Order-driven polling (needs pre-settlement persistence); NOWPayments/Stripe pollers
(their webhooks are signed with provider-side retry + the CLI covers gaps — file as
backlog if operators ask); configurable interval/window env vars.
