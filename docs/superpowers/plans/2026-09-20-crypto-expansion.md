# Crypto Expansion Plan — TON + NOWPayments

> **Status:** In progress
> **Created:** 2026-09-20 (night program «Roadmap Zero», Stage B)
> **Modeled on:** YooKassa + Stripe plans (both executed and merged)

## Goal

Two more crypto rails, complementary by design:

1. **TON** — native Telegram crypto: direct on-chain transfers to the shop wallet,
   identified by memo `order-<id>`, settled by a **polling worker** (no webhook exists
   on-chain) reading the toncenter v2 API (authoritative finalized data). Integer
   nanoton amounts (scale 9) with an order-level snapshot column (`total_ton_nano`).
2. **NOWPayments** — one integration → 300+ coins (BTC/ETH/USDT/…): hosted invoice
   redirect (the YooKassa/Stripe UX shape), **signed IPN webhook** (HMAC-SHA512 of the
   canonically sorted JSON body — trusted, no refetch, the CryptoBot/Stripe pattern),
   USD-priced (scale 2, no snapshot needed — provider converts).

## Verified API shapes (controller-checked 2026-09-20)

- **TON Center v2** `GET {base}/api/v2/getTransactions?address=<addr>&limit=<N>`
  (optional `api_key` param): `result[]` each has `transaction_id{lt,hash}`, `utime`,
  `in_msg{source, destination, value (string nanotons), msg_data}`; plain-text comments
  arrive as `msg_data."@type" == "msg.dataText"` with `.text` — binary/encrypted ones
  are `msg.dataRaw` (skip). Only finalized transactions are returned.
  Deeplink: `ton://transfer/<addr>?amount=<nano>&text=order-<id>`.
- **NOWPayments**: `POST /v1/invoice` JSON `{price_amount (float USD), price_currency:"usd",
  order_id:"<id>", order_description, ipn_callback_url, success_url, cancel_url}`,
  header `x-api-key` → response `{id, invoice_url, ...}`. IPN POSTs to our webhook with
  header `x-nowpayments-sig` = hex HMAC-SHA512 of the **key-sorted compact JSON body**
  (recursive sort; Go: unmarshal to `any`, re-marshal with sorted keys + HTML escaping
  DISABLED via json.Encoder SetEscapeHTML(false), no trailing newline) keyed by the IPN
  secret. Settle ONLY on `payment_status == "finished"` (statuses: waiting/confirming/
  confirmed/sending/partially_paid/finished/failed/refunded/expired). Body carries
  `payment_id` (number), `order_id` (string), `price_amount` (float USD echo),
  `price_currency` ("usd").

## Binding rulings

- **Provider keys:** `ton`, `nowpayments` (constants `PaymentMethodTON`,
  `PaymentMethodNowpayments`). Migration **021** rebuilds the six ledger tables widening
  CHECKs to `('stars','crypto','yookassa','stripe','ton','nowpayments','balance')`
  (+`'unknown'` where 020 has it) — `'balance'` is a DB-level forward-pin for Stage C
  (admin balance feature), app layer must NOT accept it in this plan (same discipline as
  stripe-in-020). Migration **022**: `ALTER TABLE orders ADD COLUMN total_ton_nano INTEGER
  NOT NULL DEFAULT 0` + plumbing through every full-Order INSERT/SELECT/scan site
  (the 019 precedent: orders.go ×5 + payment_recording.go ×2 — recount them).
- **TON money:** nanotons int64 everywhere (NO float at boundaries). Snapshot
  `total_ton_nano = round(TotalUSD_afterPromo × 1e9 / USD_PER_TON)` computed ONCE in
  `CreateFromCart` from the discounted total; `CartView.TotalTONNano` computed once-at-end
  in cart Get() from TotalUSD. `orderMoney` ton case reads the column directly (currency
  `"TON"`, scale 9). **Settlement is overpay-tolerant**: `receipt.AmountMinor >=
  order.TotalTonNano` settles (ledger records the ACTUAL received amount); underpay →
  quarantine. Rate source: env `USD_PER_TON` (NaN/Inf-guarded like USD_TO_RUB_RATE);
  rate=0/unset → TON hidden. Comment format pinned: exactly `order-<id>` (regex
  `^order-(\d+)$`); anything else is ignored (never settles).
- **NOWPayments money:** USD cents (`round(price_amount×100)`), `"USD"`, scale 2,
  EXACT match vs `order.TotalUSD` (signed IPN echoes our own invoice). ExternalID =
  decimal string of `payment_id`. PayerID 0.
- **Payer predicate** generalizes once more: `==0` accepted for normalized
  {yookassa, stripe, ton, nowpayments}; `<0` rejects for ALL; `>0` equality for ALL.
  Refactor the provider list into ONE named unexported helper/set (no third ad-hoc list).
- **TON "invoice" = instructions, no server-side creation**: `onPayTON` only renders
  address + nanoton amount + `order-<id>` memo (locale template with code-escaped
  copyable blocks) + a `ton://` URL button. No API call at tap. Settlement is 100%
  worker-side. Idempotent by construction.
- **TON polling worker** mirrors `CryptoBotPollingWorker` (interface-bound, testable):
  poll latest N=50 txs every 30s; for each `msg.dataText` tx whose comment parses and
  whose order is pending → build receipt (`AmountMinor=received nano`, `ExternalID=
  <lt>:<hash>`, `OccurredAt=utime`) → `ConfirmPaymentReceipt` (ledger idempotency makes
  repeats no-ops — the cursor question is answered by the ledger, same as crypto).
- **NOWPayments webhook** `/nowpayments-webhook` mirrors the Stripe handler: 405 → 503
  unconfigured → MaxBytesReader → `x-nowpayments-sig` verify (invalid → **403 no
  artifacts**) → parse (post-signature garbage → digest anomaly) → non-`finished` → 200
  no-op → receipt → ConfirmPaymentReceipt → side effects (metrics/outWebhook/admin msg
  `admin_order_paid_nowpayments`, user message). Canonicalizer correctness is fail-closed
  (wrong canonical form ⇒ signatures never verify ⇒ nothing settles).
- **webapi:** `method=ton` returns `invoice_link = ton://...` deeplink (guard
  `TotalTonNano>0` → else 400 disabled-key); `method=nowpayments` creates the invoice →
  hosted URL. app.js buttons 5 & 6 (unconditional, sanctioned fallback).
- **Button order:** stars → crypto → yookassa → stripe → ton → nowpayments (append only).
- **ui_text suppression:** extend to cover all five non-Stars rails (positional bool
  growth is getting silly — implementer chooses: extend positionally OR collapse the
  three/five into one `altAvailable` param IF the keyboard's per-provider flags can feed
  it without churn; report the choice).
- **Subscriptions Stars-only** everywhere (both new rails inherit the existing guard).

## Tasks

### Task 1: Migrations 021+022 + models constants + column plumbing

- `021_ledger_provider_crypto_expansion.sql`: rebuild the six ledger tables EXACTLY per
  020's mechanics (defer_foreign_keys, park payment_events FK-less TEMP, drop child
  before parent, recreate 13 triggers + 7 indexes) with the widened CHECK set incl.
  `'balance'` (+`'unknown'` where present). Header documents the forward-pin.
- `022_orders_total_ton_nano.sql`: `ALTER TABLE orders ADD COLUMN total_ton_nano INTEGER
  NOT NULL DEFAULT 0`.
- models.go: `PaymentMethodTON = "ton"`, `PaymentMethodNowpayments = "nowpayments"`,
  `PaymentMethodBalance = "balance"` (constant only — NOT accepted anywhere yet);
  `Order.TotalTonNano int64` field + plumbing through ALL full-Order INSERT/SELECT/scan
  sites (grep the 019 pattern: orders.go, payment_recording.go — recount, don't trust "7").
- Tests: mirror migration_020_test.go (inventory 13 triggers + 7 indexes, FK window,
  legacy rows survive, new keys accepted in DB) + 022 column round-trip (insert/read
  default 0, explicit value round-trip through every SELECT site — the 019 test shape).

### Task 2: Config (TON + NOWPayments)

- TON: `TON_WALLET_ADDRESS` (non-empty when enabled; basic shape check: len ≥ 48 — TON
  friendly addresses are 48 chars base64url — plus charset, comment that full validation
  is toncenter's job), `USD_PER_TON` (float, NaN/Inf-guarded, >0 when address set),
  `TON_API_KEY` (OPTIONAL — may be empty with all else set; relax the all-or-nothing to
  address+rate mandatory, api-key optional).
- NOWPayments: `NOWPAYMENTS_API_KEY`, `NOWPAYMENTS_IPN_SECRET`, `NOWPAYMENTS_RETURN_URL`
  (https) — all-or-nothing, mirror Stripe's validator shape (no prefix rules exist —
  none invented).
- Helpers: `NowpaymentsWebhookURL()` (`<base>/nowpayments-webhook`); TON needs none.
- `.env.example` blocks; config_test legs per family (mirror stripe/yookassa test shapes;
  TON optional-key legs explicit).

### Task 3: NOWPayments adapter + tests

`internal/payment/nowpayments.go`: `NewNowpaymentsPayment(apiKey, ipnSecret, returnURL,
webhookURL string)` (webhookURL passed in — ipn_callback_url per-invoice), `Configured()`,
`SetBaseURL`, `CreateInvoice(ctx, orderID, amountUSDCents, description)` (float
`price_amount = cents/100`, JSON POST, `x-api-key`, response `{id, invoice_url}` →
Invoice; error mapping w/ status+message), `VerifyIPNSignature(header, body)` with the
sorted-compact-noescape canonicalizer + HMAC-SHA512 hex constant-time (sentinels:
ErrNowpaymentsSignatureMalformed/Mismatch — no timestamp in scheme), `ParseIPN(body)` →
struct → `PaymentReceipt()` (finished-only; `"USD"` normalized+validated; ExternalID =
payment_id decimal string; OrderID from order_id; PayerID 0) + `PaymentAnomaly(reason)`.
Tests: exact JSON body pin, signature matrix (valid/wrong-secret/tampered/key-order-
insensitive canonicalization proof — same logical body with DIFFERENT key order verifies),
receipt matrix (finished settles; every non-finished status rejected; wrong currency
rejected; payment_id float formatting pinned e.g. 5077125051 → "5077125051").

### Task 4: TON chain client + amount/link helpers + tests

`internal/payment/ton.go`: `NewTONPayment(walletAddress, apiKey string)` (NO rate here —
rate is service-layer), `Configured()`, `SetBaseURL`, `GetTransactions(ctx, limit)` →
parsed `[]TONTransaction{LT, Hash, Source, ValueNano int64, Comment string, Utime}`
(dataText only; dataRaw skipped with debug log; value string→int64 parse), comment parser
`ParseOrderComment(s) (orderID int64, ok bool)` (`^order-(\d+$`), deeplink builder
`TransferLink(nano int64, orderID int64) string` (`ton://transfer/<addr>?amount=<nano>&
text=order-<id>`), receipt builder `PaymentReceipt(orderID)` → AmountMinor=ValueNano,
Currency "TON", Scale 9, Provider ton, ExternalID `<lt>:<hash>`, OccurredAt=utime,
PayerID 0. In `internal/service/exchange.go`: `ConvertUSDToNanoTON(usd, usdPerTon float64)
int64` = `int64(math.Round(usd × 1e9 / usdPerTon))` with ≤0/NaN/Inf guard returning 0 —
load-bearing comment (integer-minor-unit contract). Tests: getTransactions parse against
a recorded-shape fixture (dataText + dataRaw + string values), comment parser matrix,
deeplink format pin, conversion pins (10 USD @ 5 USD/TON → 2_000_000_000; fractional
rounding pin e.g. $19.99 @ 5.13 → compute exactly; guard legs).

### Task 5: Storage acceptance (ton + nowpayments; NOT balance)

- orderMoney: ton → `order.TotalTonNano` (direct int64, `"TON"`, scale 9, ≤0 →
  ErrInvalidMoney); nowpayments → `round(order.TotalUSD*100)`, `"USD"`, 2.
- validatePaymentFact: ton (currency "TON", scale 9, no payer check, overpay-tolerant NOT
  here — fact equality is ledger-level; exactness vs orderMoney? NO: settlement uses
  ConfirmPaymentReceipt's >= rule; validatePaymentFact currency/scale checks only —
  mirror how amount comparison is split between fact validation and receipt validation
  for yookassa and follow the same division for ton), nowpayments ("USD", scale 2).
- Allowlists +ton +nowpayments: payment_anomalies, payment_ingress_audit,
  payment_resolutions ×2, ledger.go ×2, payment_ingress.go preview.
- Payer predicate: single named payerless-provider set {yookassa, stripe, ton,
  nowpayments}; negative rejects ALL (keep); tests extended (ton/nowp zero-accept,
  negative-reject, positive-mismatch-reject) + balance STILL rejected everywhere at app
  level (explicit fail-closed legs — balance is DB-only).
- normalizePaymentProvider passthrough legs for both.

### Task 6: Shop — TON cart column + receipt cases

- `CartView.TotalTONNano int64` computed once-at-end in Get() when rate configured
  (mirror TotalRUB placement/drift-comment), `CreateFromCart` snapshot from the
  discounted total (mirror the RUB promo-rounding placement; integer output),
  `ConfirmPaymentReceipt`: ton case `receipt.AmountMinor >= order.TotalTonNano` +
  "TON" + scale 9 (overpay settles; comment the tolerance rationale); nowpayments case
  exact== mirror stripe.
- Tests: cart conversion leg incl. a DRIFT pin mirroring the RUB drift fixture (choose
  fixture numbers where per-item nano sum ≠ once-at-end nano); CreateFromCart promo
  snapshot leg; receipt legs for both providers (happy/replay/underpay-quarantine for
  ton incl. exact-boundary == leg and overpay leg; mismatch legs for nowpayments).

### Task 7: Bot — buttons + handlers + locales

- Keyboard rows 5 (TON) & 6 (NOWPayments): TON visible when `tonConfigured && rate>0 &&
  !sub && view.TotalTONNano > 0`; NOWPayments when `Configured() && !sub`.
- `onPayTON`: parse, loadPayableOrder, sub guard, guards (configured/rate/TotalTonNano>0
  → `ton_unavailable`), then send instructions message (`ton_pay_instructions` locale:
  order id, nano→TON display amount (X.XXXXXXXXX trim), wallet address in <code>, memo
  `order-<id>` in <code>) + URL button `TransferLink`. NO adapter write call.
- `onPayNowpayments`: mirror onPayStripe (CreateInvoice, URL button, `payment_error`).
- Router: `pay:ton:`, `pay:nowpayments:`.
- ui_text suppression covers all five rails (implementer's structural choice, reported).
- Locales ×5: `btn_pay_ton`, `ton_pay_instructions`, `ton_unavailable`,
  `btn_pay_nowpayments`, `nowpayments_pay_title`, `nowpayments_unavailable`,
  `nowpayments_invoice_desc` (+ verify parity/coverage tests).
- Tests: disabled-path invariance extended (both new rails), onPayTON legs (instructions
  content pins: address, amount formatting, memo, deeplink; zero API calls — TON has no
  create call; sub guard; unavailable legs), onPayNowpayments happy/mock legs.

### Task 8: NOWPayments webhook

- `/nowpayments-webhook` route + interface + fake; handler mirroring the Stripe handler
  with: `x-nowpayments-sig` verify → 403 no artifacts on failure; post-signature garbage
  → digest anomaly; non-`finished` statuses → 200 no-op (one leg per interesting status:
  waiting/partially_paid/failed); settlement + side effects (metrics `nowpayments`,
  admin msg `admin_order_paid_nowpayments` with $%.2f TotalUSD, outWebhook Method);
  replay leg; mismatch → quarantine leg.
- Locale: `admin_order_paid_nowpayments` ×5.
- Tests mirror stripe_webhook_test.go structure (in-test canonicalizer+HMAC helper —
  must be non-vacuous: helper canonicalizes independently).

### Task 9: TON polling worker

- `worker/ton_polling.go` mirroring `CryptoBotPollingWorker`: interfaces
  (`TxFetcher{GetTransactions}`, reuse `PaymentConfirmer`), 30s default interval,
  `pollOnce(ctx)` exported-for-tests IF crypto's worker exposes an equivalent (mirror
  whatever test seam polling_test.go uses), per-tx processing: comment parse → order
  load → pending check → receipt → ConfirmPaymentReceipt → notify (mirror crypto's
  notify wiring); failures logged and skipped (never panic the ticker).
- main.go wiring: start when TON configured (mirror crypto worker wiring at ~:235).
- Tests mirror polling_test.go: settles matching tx; ignores dataRaw/foreign-comment/
  already-settled (idempotent replay via ledger); underpaid → quarantine; notify called
  once.

### Task 10: Ops

- payment-review: PROVIDER=ton / nowpayments; doctor: TON checks (address+rate matrix,
  rate-missing-with-address WARN mirroring yookassa), NOWPayments creds all-or-nothing
  FAIL + https check; knownEnvironmentKeys +6 (TON_WALLET_ADDRESS, USD_PER_TON,
  TON_API_KEY, NOWPAYMENTS_API_KEY, NOWPAYMENTS_IPN_SECRET, NOWPAYMENTS_RETURN_URL).
- payment-operations.md §7 TON (no webhook — polling model; memo identification;
  on-chain finality; overpay tolerance; manual verification via tonviewer link;
  resolve flow) + §8 NOWPayments (signed IPN model, sorted-JSON canonicalization note,
  finished-only, refunds operator-driven via dashboard). reconcile.go untouched
  (commit-body note).

### Task 11: Webapi + frontend

- `TONPayer`? NO — interfaces `TONInvoicer`-analog is unneeded (no server create): webapi
  needs the wallet address + rate to build the deeplink → simplest: Deps gains
  `TON *payment.TONPayment` + the ORDER's TotalTonNano (already on the order) → build
  `TransferLink` in the handler (no interface needed? mirror YooKassaInvoicer anyway for
  symmetry IF it keeps tests clean — implementer chooses, reports); guard
  `TotalTonNano <= 0` → 400 `webapp_err_ton_disabled`. NOWPayments: `NowpaymentsInvoicer`
  interface (Configured + CreateInvoice) mirroring StripeInvoicer.
- app.js: buttons 5 & 6 unconditional; locales ×5: `webapp_pay_ton`,
  `webapp_err_ton_disabled`, `webapp_pay_nowpayments`, `webapp_err_nowpayments_disabled`.
- Tests mirror stripe webapi legs for both methods (ton: deeplink format + zero-guard;
  nowp: create legs + disabled legs).

### Task 12: E2E ×2 + docs + CHANGELOG

- `TestE2ETONPurchase`: mock toncenter (SetBaseURL) returning a dataText tx
  (`order-<realID>`, exact nano) → run ONE worker poll (the Task 9 test seam) →
  observables exact-pinned (paid + method ton + payment_id `<lt>:<hash>`, stock once,
  loyalty once, user+admin messages, outWebhook) + second poll replay no-op + underpaid
  tx → quarantine leg.
- `TestE2ENowpaymentsPurchase`: mirror the Stripe E2E (signed IPN, mock create only,
  hit-count==1 pin, replay).
- README/README.ru: TON + NOWPayments subsections; environment-variables.md rows;
  getting-started.md if applicable; CHANGELOG [Unreleased] (migrations 021+022 named;
  balance forward-pin noted as DB-only).

## Self-Review Notes

### Spec coverage
Every YooKassa/Stripe surface mirrored; the genuinely new surfaces (polling-settlement,
instructions-only invoice, on-chain overpay tolerance, sorted-JSON HMAC) each have a
binding ruling above + dedicated test matrices.

### Known ambiguities (controller rulings inline)
- Overpay settles for TON (>=) — on-chain tips are real money received; underpay
  quarantines. NOWPayments exact== (signed echo of our own invoice).
- `payment_id` float→string: decimal without exponent (pinned test).
- TON comment `order-<id>` fixed (not configurable) — keeps parsing fail-closed.

### Out of scope (follow-ups)
- 'balance' app-level acceptance (Stage C), TON testnet toggle, NOWPayments
  per-coin selection UI (hosted page already offers coin choice), toncenter v3
  migration, automatic refunds, subscription support on crypto rails (Stars-only holds).
