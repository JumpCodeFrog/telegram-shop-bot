# Stripe Provider Implementation Plan

> **Status:** In progress
> **Created:** 2026-09-19 (night program «Roadmap Zero», Stage A)
> **Modeled on:** `2026-09-19-yookassa-provider.md` (executed, merged 7b064b3)

## Goal

Add **Stripe** as a USD card payment provider (hosted Checkout Sessions, redirect flow —
the YooKassa shape) for the international audience. Raw HTTP (no SDK — project rule:
no new dependencies). Settlement only from **signature-verified** webhooks
(`Stripe-Signature`, HMAC-SHA256) validated against the order snapshot — the trusted
pattern (CryptoBot), NOT the refetch pattern (YooKassa is unsigned; Stripe signs).

## Context

- Repo: telegram-shop-bot, Go, module `shop_bot`. Providers today: `stars` (Telegram),
  `crypto` (CryptoBot), `yookassa` (RUB card). Ledger provider CHECKs ALREADY accept
  `'stripe'` (migration 020 — deliberate forward-pin). **No DB migration in this plan.**
- Shop is USD-native: `order.TotalUSD` is the authoritative total; no rate conversion,
  no new order column. RUB snapshot (`total_rub`) is YooKassa-only.
- Stripe API (raw HTTP, verified against stripe-go docs 2026-09):
  - `POST /v1/checkout/sessions` — **form-encoded** (`application/x-www-form-urlencoded`),
    `Authorization: Bearer <secret>`, `Idempotency-Key: <fresh uuid4>`.
    Params: `mode=payment`, `success_url`, `cancel_url`, `client_reference_id`,
    `metadata[order_id]`, `line_items[0][quantity]=1`,
    `line_items[0][price_data][currency]=usd`,
    `line_items[0][price_data][unit_amount]=<cents>`,
    `line_items[0][price_data][product_data][name]=<description>`.
    Response: `{ "id": "cs_...", "url": "https://checkout.stripe.com/...", ... }`.
  - `GET /v1/checkout/sessions/{id}` → `status` (`open`/`complete`/`expired`),
    `payment_status` (`paid`/`unpaid`/`no_payment_required`), `amount_total` (cents),
    `currency` (`"usd"` lowercase), `metadata.order_id`, `client_reference_id`.
  - Webhook: header `Stripe-Signature: t=<unix>,v1=<hex hmac>`; signed payload =
    `"<t>" + "." + <raw body>`; HMAC-SHA256 with webhook secret (`whsec_...`);
    constant-time compare; default tolerance 300s.
  - Stripe minimum for USD card charges: **$0.50** (50 cents) — guard below it.
- Security model per provider class: **signed webhook ⇒ body trusted after verification;
  settle from the verified session object** (mirrors CryptoBot webhook). No refetch
  needed; `ConfirmPaymentReceipt` snapshot validation stays as defense in depth.
- Provider key: `stripe` everywhere (constant `storage.PaymentMethodStripe`).
- Stripe facts carry **no Telegram payer identity** (PayerID 0), same as YooKassa.
- Subscriptions stay Stars-only on every surface.

## Tasks

### Task 1: Adapter `internal/payment/stripe.go` + tests

**Files:**
- Create: `internal/payment/stripe.go`, `internal/payment/stripe_test.go`
- Modify: `internal/storage/models.go` (+`PaymentMethodStripe = "stripe"`, next to YooKassa)

**Interfaces:**
- `NewStripePayment(secretKey, webhookSecret, returnURL string) *StripePayment`
- `Configured() bool` — all three non-empty
- `SetBaseURL(url string)` — exported test seam (mirrors YooKassaPayment)
- `CreateCheckoutSession(ctx, orderID int64, amountCents int64, description string) (*payment.Invoice, error)` — form-encoded POST, Bearer auth, **fresh uuid4 Idempotency-Key per call** (uuid package already used by yookassa.go — reuse the same import), maps `{id,url}` → `Invoice{PayURL, InvoiceID}`; non-200 → parse `{"error":{"message","code"}}` into an error containing HTTP status + message
- `GetCheckoutSession(ctx, id string) (*StripeSession, error)`
- `VerifyWebhookSignature(header string, body []byte) error` — parse `t=`/`v1=` (ignore other schemes), `abs(now-t) <= 300s`, `hmac-sha256(secret, "t.body")` hex vs every `v1` (constant-time); distinct sentinel errors: `ErrStripeSignatureMalformed`, `ErrStripeSignatureMismatch`, `ErrStripeSignatureStale`
- `(*StripeSession).PaymentReceipt() (shop.PaymentReceipt, error)` — valid ONLY when `status=="complete" && payment_status=="paid"`; `AmountMinor=amount_total`, `Currency:"USD"` (API sends lowercase — normalize/validate), `Scale:2`, `Provider: storage.PaymentMethodStripe`, `ExternalID: session.ID`, `OrderID` from `metadata.order_id` (fall back `client_reference_id`), PayerID 0; otherwise error
- `(*StripeSession).PaymentAnomaly(reason string) (storage.PaymentAnomaly, error)` — mirror YooKassa

**Tests (TDD):** httptest mock asserting: form-encoding (parse `r.PostForm()`), exact
param set, Bearer header, Idempotency-Key is a parseable uuid and DIFFERS across calls;
error mapping (400 with Stripe error body); GetCheckoutSession parse incl. metadata;
signature: valid → nil; wrong secret → Mismatch; tampered body → Mismatch; stale t →
Stale; garbage header → Malformed; receipt mapping happy + each non-settled combination
rejected (open+unpaid, complete+unpaid, expired); currency normalization `"usd"→"USD"`.

### Task 2: Config + validation

**Files:** `internal/config/config.go`, `validation.go`, `config_test.go`, `.env.example`

- `STRIPE_SECRET_KEY` (prefix `sk_live_`/`sk_test_` — fail otherwise),
  `STRIPE_WEBHOOK_SECRET` (prefix `whsec_` — fail otherwise),
  `STRIPE_RETURN_URL` (must be `https://` — same house-style check as YooKassa)
- All-or-nothing semantics mirroring `ValidateYooKassaConfig`; `StripeWebhookURL()`
  helper mirroring `YooKassaWebhookURL()` (`<WEBHOOK_URL base>/stripe-webhook`)
- .env.example block after YooKassa; tests mirror yookassa config legs
  (unset ok / partial fail / http return-URL fail / bad prefixes fail / full ok)

### Task 3: Storage acceptance (NO migration)

**Files:** `internal/storage/order_state.go` (orderMoney stripe case +
validatePaymentFact), `payment_anomalies.go`, `payment_ingress_audit.go`,
`payment_resolutions.go` (:16/:468), `ledger.go` (:190/:238),
`payment_ingress.go` (:66 refund preview + payer predicate), tests.

- `orderMoney`: stripe → `int64(math.Round(order.TotalUSD*100))`, `"USD"`, scale 2
- `validatePaymentFact`: stripe case — currency `"USD"` required, NO payer check
  (comment mirroring yookassa: provider has no Telegram payer identity)
- Extend every allowlist with `PaymentMethodStripe` (same sites as YooKassa Task 7 list)
- **Payer predicate generalization (carry-over from YooKassa final review, Minor 3):**
  `invalidProviderCapturePayer` — payer equality enforced when PayerID > 0 for every
  provider; PayerID accepted at zero ONLY for normalized `yookassa`/`stripe`; **negative
  PayerID rejected for ALL providers** (deliberate tightening — today yookassa accepts
  negatives; update/extend the payer-matrix tests and note the behavior change in the
  commit body)
- `normalizePaymentProvider` passthrough pinned
- Tests mirror the yookassa acceptance suite + stripe legs in the payer matrix +
  negative-payer rejection leg

### Task 4: Shop receipt validation

**Files:** `internal/shop/order.go` (ConfirmPaymentReceipt stripe case), tests.

- stripe receipt: `AmountMinor == int64(math.Round(order.TotalUSD*100))`, `"USD"`,
  scale 2, ExternalID non-empty; mismatch → quarantine (existing machinery)
- Replay + mismatch + wrong-currency legs mirroring the yookassa cases in
  `payment_receipt_test.go`
- No CartView changes (USD is native)

### Task 5: Bot checkout

**Files:** `internal/bot/bot.go` (field + construct in NewWithAPI),
`styled_keyboard.go`, `handlers_checkout.go`, `handlers_payment.go` (onPayStripe),
`handlers.go` (router `pay:stripe:`), `ui_text.go` (+tests), locales ×5.

- Button after the YooKassa row, shown when `Configured() && !subscription`
  (TotalUSD>0 always holds for a created order — mirror crypto's condition)
- Locale keys ×5: `btn_pay_stripe` («💳 Карта ($)» / "💳 Card ($)" / per-locale tone),
  `stripe_pay_title`, `stripe_unavailable`, `stripe_invoice_desc`
- `onPayStripe` mirrors `onPayYooKassa`: `amountMinor = round(order.TotalUSD*100)`;
  **Stripe minimum guard:** `< 50` → `stripe_unavailable` error path; CreateCheckoutSession
  with `stripe_invoice_desc`; URL button; errors via `payment_error`
- `ui_text.go`: the no-crypto note suppression condition becomes
  `!cryptoEnabled && !yookassaOK && !stripeOK` (text must stay true); signature +
  callers + tests updated; new suppression legs (stripe-only deployment)
- Disabled-path invariance test (full callback list identical when stripe unconfigured)
  mirroring the yookassa one

### Task 6: Webhook `/stripe-webhook` (SIGNED — trusted pattern)

**Files:** `internal/bot/webhook.go` (+handler after YooKassa), `cmd/bot/http_routes.go`
(interface + mount), `cmd/bot/http_routes_test.go` (fake),
`internal/bot/stripe_webhook_test.go` (new), locales ×5 (`admin_order_paid_stripe`).

- Order of checks mirroring the crypto/yookassa handlers: 405 non-POST → 503
  unconfigured (inert) → MaxBytesReader 1MB (400) → read body
- `VerifyWebhookSignature(r.Header.Get("Stripe-Signature"), body)`:
  - Malformed/Stale → 400, log; NO anomaly (unauthenticated junk — mirror crypto's
    invalid-signature handling exactly; check its status code and copy it)
  - Mismatch → same
- Parse event envelope `{"type","data":{"object":{...}}}`:
  - JSON parse failure → sha256-digest anomaly (`webhook_parse_failure`, provider
    stripe) → 200 (post-quarantine) — signature was VALID, so this is a real anomaly
  - `type != "checkout.session.completed"` → 200, no state change, no anomaly
  - valid envelope without session id → 200 no-op
- Session → `PaymentReceipt()` → `ConfirmPaymentReceipt` → side effects mirroring
  yookassa handler: metrics `SuccessfulPayments{provider:"stripe"}`, user
  `payment_success`, `NotifyPaymentOutcome`, `notifyAdmins` with
  `admin_order_paid_stripe` (`%.2f $` + TotalUSD), outWebhook `Method:"stripe"`
- ACK/500 table identical to yookassa handler (idempotent sentinels list =
  crypto's/yookassa's byte-identical list; out-of-stock branch mirror)
- Tests (signature computed in-test with a test `whsec_`): settles from SIGNED body
  WITHOUT any outbound API call (assert mock/api hit count zero — the key difference
  from yookassa); invalid signature legs (malformed/mismatch/stale → 400, zero state
  change, zero anomalies); non-completed events → 200 no-op; garbage-JSON-with-valid-
  signature → digest anomaly + 200; oversized → 400; replay idempotent (stock/points
  once); unconfigured → 503 inert

### Task 7: Ops tooling

**Files:** `internal/launcher/payment_review.go` (+stripe in provider validation),
`doctor.go` (+Stripe checks), `reconcile.go` (read; leave + commit-body note),
`docs/payment-operations.md` (+§6 Stripe), tests.

- doctor: partial creds FAIL; return URL non-https FAIL; bad key prefixes FAIL
  (reuse `ValidateStripeConfig` if structure allows — mirror YooKassa checks' style)
- docs §6: signed-webhook model (whsec, 300s tolerance, no refetch), quarantine shapes,
  `make payment-review PROVIDER=stripe`, refunds operator-driven via Stripe dashboard,
  USD-native (no rate snapshot), subs Stars-only

### Task 8: Mini App

**Files:** `internal/webapi/handlers.go` (`StripeInvoicer` interface mirroring
`YooKassaInvoicer`; Deps field; handleCheckout edits; doc comment),
`cmd/bot/main.go` (separate instance wiring — mirror yookassa),
`web/app/app.js` (button), `internal/webapi/handlers_test.go`, locales ×5
(`webapp_pay_stripe`, `webapp_err_stripe_disabled`).

- method acceptance + disabled guard (400 `webapp_err_stripe_disabled`) + switch case:
  `amountCents = round(order.TotalUSD*100)`; `< 50` → 400 disabled-key + return
  (never the 502 path); `CreateCheckoutSession(ctx, order.ID, amountCents,
  orderDescription(order.Items))` → `link = inv.PayURL`
- app.js: fourth unconditional `btn secondary` button (house fallback — cartJSON has no
  availability flags; consistent with the yookassa ruling)
- tests mirror the yookassa webapi legs (fake captures args; disabled nil/unconfigured;
  subscription guard already covers stripe — verify untouched; unknown method unchanged;
  minimum-amount leg via fake)

### Task 9: E2E + docs + CHANGELOG

**Files:** `internal/bot/e2e_test.go` (+`TestE2EStripePurchase`), `README.md`,
`docs/readme/README.ru.md`, `docs/environment-variables.md`, `docs/getting-started.md`
(if it lists payments), `CHANGELOG.md` (extend `[Unreleased]`).

- E2E mirrors the yookassa scenario: mock Stripe API ONLY for CreateCheckoutSession
  (SetBaseURL seam); webhook leg uses an in-test computed valid signature; **assert the
  mock API hit count stays exactly 1 after the webhook** (no refetch — settlement came
  from the signed body); observables pinned exactly (stock once, points once, attempts=1
  provider stripe, user+admin messages, outWebhook, replay 200 no-op)
- README Payments: Stripe (USD) subsection after YooKassa (env vars, webhook URL
  `<WEBHOOK_URL>/stripe-webhook`, dashboard webhook registration + signing secret,
  signed-webhook security note, $0.50 minimum, subs Stars-only); RU mirror
- environment-variables.md: 3 rows (prefix rules, https rule, all-or-nothing)
- CHANGELOG: Stripe provider entry (note: NO new migration — 020 pre-admitted 'stripe')

## Self-Review Notes

### Spec coverage
Every YooKassa-plan surface is mirrored 1:1 except: no migration (020 pre-admitted the
key), no rate/snapshot work (USD native), webhook trusts signatures (no refetch), plus
the deliberate payer-predicate tightening (negative rejection).

### Known ambiguities (controller rulings)
- Settle-from-signed-body vs refetch: signed body (crypto pattern). Refetch would add
  latency and a failure mode without security gain — the signature IS the authority.
- Invalid-signature HTTP code: mirror crypto's existing code exactly (implementer reads
  crypto handler; do not invent).
- Button order: stars → crypto → yookassa → stripe (append at end, never reorder
  existing rows).

### Out of scope (follow-ups)
- Stripe subscriptions/recurring, Stripe auto-refunds via API, PaymentIntent-level
  flows, Stripe polling worker (signed webhooks + payment-review suffice),
  Apple Pay / Google Pay toggles on the hosted page.
