# Admin Full-Management Plan

> **Status:** In progress
> **Created:** 2026-09-20 (night program «Roadmap Zero», Stage C)
> **Preceded by:** YooKassa (merged 7b064b3), Stripe (032e7b5), crypto expansion (2a63e17)

## Goal

Full shop management from the Telegram admin surface: split the 1094-line `admin.go`,
expose the payment review queue (currently CLI-only), wire the dormant balance schema
into a real payment method, upgrade order inspection with payment facts, and add a
provider status view.

## Context (controller-audited)

- `internal/bot/admin.go` (1094 lines) sections: isAdmin, handleAdmin (menu text),
  products CRUD + wizard + photos, categories CRUD, orders-all + setdelivered,
  analytics, promos, CSV export, stock toggle, btn styles.
- Admin surface is **command-driven** (`/addproduct`, …) with inline callbacks for
  photos/stock/styles (`admin:photos:` etc.). `/admin` prints a help text (locale key
  `admin_panel` ×5).
- Payment review exists ONLY as CLI: `internal/launcher/payment_review.go` wrapping
  storage `ListPaymentReviews(ctx, provider)` / `PreviewPaymentReviewResolution` /
  `ResolvePaymentReview` (payment_resolutions.go:14/170/218). The bot admin UI must
  reuse THESE storage APIs (no new storage semantics).
- Balance: schema exists (`users.balance_usd` 001:8, `balance_txs` 001:73); NO Go
  store code (cleaned since April); `PaymentMethodBalance = "balance"` constant exists
  (DB CHECK accepts it since migration 021; app layer rejects it everywhere today).
- `paymentMethodText` (ui_text.go:52-61) lacks yookassa/stripe/ton/nowpayments cases
  (order history shows raw method strings) — carried-over follow-up, fixed here.
- Worker-path notifications exist (`AnnouncePaidOutcome`, crypto-expansion Task 12b)
  — the balance settlement reuses it.
- Six payment rails live: stars, crypto, yookassa, stripe, ton, nowpayments.

## Binding rulings

- **Split is a pure move**: zero behavior change, same package, tests untouched and
  green; file naming mirrors the handlers_* precedent.
- **Balance settlement** is synchronous and INTERNAL: onPayBalance → balance check →
  deduct (atomic, insufficient-funds-safe) → settle via the SAME ledger path
  (`UpdateOrderStatusWithPaymentFact`) using a synthetic internal fact:
  provider `balance`, ExternalID `balance:<orderID>` (uniqueness: one successful
  settlement per order is enforced by the status guard; a rejected attempt writes NO
  fact, so reuse is safe), PayerID = order.UserID (**positive — equality enforced**,
  mirror stars), AmountMinor = round(TotalUSD*100), currency USD, scale 2,
  OccurredAt = now. NO ConfirmPaymentReceipt (that's for external provider facts).
- **Balance button** visible when `!subscription && userBalanceUSD > 0` (no provider
  config needed); insufficient at tap → `balance_insufficient` error showing current
  balance; subscription carts → Stars-only guard holds.
- **Payment review queue** (`/payreview`): list needs_review cases across ALL
  providers (check `ListPaymentReviews(ctx, "")` semantics — if empty provider isn't
  "all", call per-provider over the six known keys); inline card per case
  (`admin:payrev:<orderID>`) showing order + payment facts + anomalies; actions mirror
  the CLI's resolution kinds (read `PaymentReviewResolution` — expected: settle /
  mark-refunded; DO NOT invent new kinds) behind a two-tap inline confirm
  (`admin:payrev:<action>:<orderID>` → preview rendered → `admin:payrevdo:<action>:<orderID>`
  executes). Every step admin-gated.
- **Provider status** (`/paystatus`): per-rail configured/not-configured + the webhook
  URL to register (from cfg helpers) + rate values. NEVER print key material — only
  presence booleans and URLs.
- **Order card** (`/order <id>`): full order + payment facts (method display name via
  the FIXED paymentMethodText, payment_id, amounts incl. RUB/TON when set, state).
- **paymentMethodText fix**: add yookassa/stripe/ton/nowpayments/balance cases via NEW
  locale keys `payment_method_<provider>` ×5 (raw-string fallback stays for unknown).
- **i18n**: every new user-facing string = locale keys ×5, parity+coverage tests.

## Tasks

### Task 1: Split admin.go (pure move)

- `admin.go` keeps: isAdmin, handleAdmin, shared admin helpers (if any).
- New files: `admin_products.go` (CRUD + wizard + routeEditProduct),
  `admin_photos.go` (photo wizard step, lists, add/delete, syncProductCover),
  `admin_categories.go`, `admin_orders.go` (orders_all, setdelivered),
  `admin_analytics.go`, `admin_promos.go` (incl. export if it lives there — check),
  `admin_styles.go` (btn styles).
- Zero behavior change; full suite + gofmt; `wc -l admin*.go` in the report.

### Task 2: Payment review queue (`/payreview`)

- Command + inline flow per the ruling; locale keys ×5 (`admin_payreview_*`,
  `admin_payrev_*` family — list title, empty state, card template, action buttons,
  confirm prompt, result messages, non-admin no-op).
- Tests: list rendering (seeded needs_review cases across providers), card view with
  attempts+anomalies, two-tap resolve (settle + mark-refunded paths) asserting the
  storage outcome mirrors CLI semantics, permission gating, empty state.

### Task 3: Balance payments (closes РИСК-5 by implementing)

- storage: `SQLBalanceStore` (interfaces.go + balance.go): `GetBalance(ctx, userID)
  (float64, error)` (users.balance_usd), `AdjustBalance(ctx, userID int64, deltaUSD
  float64, reason string, adminID int64) error` — atomic guard against negative
  balance on debit (single UPDATE … SET balance_usd = balance_usd + ? WHERE id = ?
  AND balance_usd + ? >= 0; ErrInsufficientFunds on 0 rows) + balance_txs insert,
  one tx; cents rounding via round(x*100) at the boundary (comment).
- Storage acceptance: orderMoney balance case (round(TotalUSD*100), "USD", 2);
  validatePaymentFact balance case (payer REQUIRED + equality — mirror stars);
  allowlists (anomalies, ingress-audit, resolutions ×2, ledger refunds ×2,
  ingress preview) + PaymentMethodBalance joins `providerHasNoTelegramPayer`'s
  INVERSE (it has a payer — NOT added to the payerless set; predicate unchanged
  semantics for balance: ==0 rejects, >0 must match).
- shop: `ConfirmBalancePayment(ctx, orderID, userID)` — load order (pending, method
  unpaid), balance check + atomic debit + synthetic fact settlement via
  UpdateOrderStatusWithPaymentFact (+ rollback discipline: debit and settle in ONE
  storage tx if the house pattern allows — else debit-then-settle with compensating
  credit on failure; choose by reading the storage tx helpers, document the choice).
- bot: `btn_pay_balance` row (AFTER nowpayments; visible per ruling),
  `onPayBalance` (guards → ConfirmBalancePayment → AnnouncePaidOutcome(provider
  "balance") — user payment_success + `admin_order_paid_balance` + outWebhook);
  `/balance` user command? NO — admin-only: `/setbalance <user_id> <±amount> [reason]`
  admin command (+ confirm echo with new balance).
- Router `pay:balance:`; locales ×5 (`btn_pay_balance`, `balance_insufficient`,
  `balance_pay_title`?, `admin_order_paid_balance`, `admin_setbalance_*`,
  `payment_method_balance` … per the actual needs).
- Tests: store legs (adjust +/-, insufficient-funds guard, tx integrity);
  ConfirmBalancePayment legs (happy + replay-safe + insufficient + non-pending
  rejection); bot legs (button visibility incl. zero-balance hidden, tap flow,
  insufficient leg, sub guard); storage acceptance legs mirroring the other providers.

### Task 4: Order card + paymentMethodText fix

- `/order <id>` admin command: full card (items, totals incl. TotalRUB/TotalTonNano
  when >0, status, payment method via paymentMethodText, payment_id, created/updated).
- paymentMethodText: add the 5 missing provider cases via `payment_method_yookassa` /
  `_stripe` / `_ton` / `_nowpayments` / `_balance` keys ×5 (ru/en/es/de/zh).
- Tests: card rendering incl. a balance-paid order; paymentMethodText all-provider
  legs incl. raw-fallback for unknown.

### Task 5: Provider status (`/paystatus`)

- Command → per-rail line: Stars (always on), Crypto (token set?), YooKassa (creds +
  rate>0), Stripe (creds), TON (address + rate), NOWPayments (creds), Balance (always
  on) + each rail's webhook URL to register where applicable (cfg helpers) + current
  rates (USD_TO_RUB_RATE, USD_PER_TON values). Presence booleans/URLs ONLY — never
  secrets.
- Locale keys ×5; tests: configured/unconfigured matrices per rail.

### Task 6: Docs + CHANGELOG + E2E

- `/admin` menu text (`admin_panel` ×5) updated with the new commands.
- README/README.ru admin section update; docs/payment-operations.md — the payreview
  bot flow + balance semantics (synchronous, internal fact, operator adjustments
  audited in balance_txs); CHANGELOG [Unreleased].
- E2E: balance purchase journey (admin /setbalance → buyer pays with balance →
  settled + notifications) + payreview flow (seeded quarantine → /payreview →
  two-tap settle → order paid).

## Self-Review Notes

### Spec coverage
Every admin surface today is command-based; the plan stays consistent (commands +
inline callbacks only where the house already uses them). No webapp admin scope
(out of scope).

### Known ambiguities (rulings inline)
- Balance debit↔settle atomicity: prefer single-tx if storage helpers allow; else
  compensating credit; documented either way.
- `/payreview` cross-provider listing: `ListPaymentReviews(ctx, "")` semantics to be
  read; per-provider fallback loop sanctioned.

### Out of scope (follow-ups)
- Webapp admin surface; refund INITIATION from the bot (refunds stay operator-driven
  at provider dashboards; payreview marks refunded only); balance top-up by users
  themselves (admin-grant only); admin audit log table (balance_txs covers balance).
