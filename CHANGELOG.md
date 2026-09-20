# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [Unreleased]

### Added

- **YooKassa (RUB) card payments** — an optional third payment provider next to Stars and CryptoBot: the checkout keyboard and the Mini App grow a "Pay by card" button for non-subscription carts, the buyer is redirected to the YooKassa checkout page, and the order settles when YooKassa notifies the bot. USD prices convert at the new `USD_TO_RUB_RATE` (configured together with `YOOKASSA_SHOP_ID`, `YOOKASSA_SECRET_KEY` and `YOOKASSA_RETURN_URL`; rate `0` keeps the button hidden), and the RUB total is snapshotted on the order at checkout so a mid-flight rate change cannot desync the button from the ledger. YooKassa notifications are unsigned: the webhook body is used only to identify the payment, and settlement happens strictly after an authoritative `GetPayment` refetch validates the provider, amount, currency, and order binding — mismatched or malformed facts are quarantined for `payment-review` (`--provider yookassa`), and idempotent webhook replays re-verify and settle nothing new. Register `<WEBHOOK_URL>/yookassa-webhook` as the notification URL in the YooKassa merchant cabinet. Migrations: `019_orders_total_rub.sql` adds the order RUB snapshot; `020_ledger_provider_yookassa.sql` rebuilds the ledger provider CHECK constraints to accept `yookassa` (and `stripe`). Subscriptions remain Stars-only.

- **YooKassa lost-webhook backup poller** — a safety net for `payment.succeeded` notifications that never arrive (bot downtime, network partition): a new `ListPayments` adapter (authenticated `GET /v3/payments`, same fail-closed parser as `GetPayment`) feeds a background polling worker that every 60 seconds re-scans the shop's succeeded payments created within a rolling 24-hour window — cursor-paginated 50 per page, capped at 20 pages per tick, with a single `page cap reached` warning when a truncated tick defers its tail to the next one — and replays each item through the same receipt validation and idempotent ledger path as the webhook: already-settled orders are durable conflict no-ops, and money that arrives after the stock sold out never settles — it is durably quarantined with the same `out_of_stock_after_charge` reason the webhook path uses and is never retried. The worker is wired behind the exact checkout-button gate (YooKassa fully configured AND positive `USD_TO_RUB_RATE`); no new environment variables, and operator escalation is unchanged — quarantined cases still surface in `payment-review` (`--provider yookassa`) / `/payreview`.

- **Stripe (USD) card payments** — an optional fourth payment provider next to Stars, CryptoBot and YooKassa: the checkout keyboard and the Mini App grow a "Pay by card ($)" button for non-subscription carts, the buyer is redirected to Stripe's hosted Checkout page, and the order settles when Stripe notifies the bot. The integration speaks raw HTTP to the Stripe API — no SDK dependency. The USD total needs no conversion: it is snapshotted on the order at checkout and charged in integer cents, and orders below Stripe's $0.50 minimum are refused at payment. Enabled by `STRIPE_SECRET_KEY` (`sk_live_`/`sk_test_` prefix), `STRIPE_WEBHOOK_SECRET` (`whsec_` prefix) and `STRIPE_RETURN_URL` (public HTTPS) — all three must be set together. Register `<WEBHOOK_URL>/stripe-webhook` in the Stripe dashboard (Developers → Webhooks) for `checkout.session.completed` and copy its signing secret. Stripe notifications are HMAC-signed: once the signature verifies, the signed body itself is authoritative and settles the order WITHOUT an API refetch (unlike the unsigned YooKassa flow, which must re-fetch the payment). An invalid signature is rejected with 403 and recorded nowhere; signed replays are ACKed with 200 and settle nothing new; signed-but-inconsistent facts are quarantined for `payment-review` (`--provider stripe`). **No new migration** — `020_ledger_provider_yookassa.sql` pre-admitted `stripe` into the ledger provider CHECK constraints. Subscriptions remain Stars-only.

- **TON (on-chain) payments** — an optional fifth payment provider: the checkout keyboard and the Mini App grow a "Pay N TON" button for non-subscription carts, and the buyer gets the shop's wallet address, the exact nanoton amount and a **mandatory `order-<id>` memo** (a `ton://transfer` deeplink prefills all three), then transfers from any TON wallet. TON has no webhook: a new polling worker refetches the watched wallet's latest 50 transactions from toncenter every 30 seconds and replays each memo-matched transfer into the ledger, where exact repeats are durable no-ops keyed by the `<lt>:<hash>` external id. Money is integer nanotons (scale 9) end to end: the total is snapshotted on the order at checkout (`round(total_usd × 1e9 / USD_PER_TON)`), a later rate change never reprices an existing order, and a zero snapshot (TON disabled at checkout) never settles. Settlement is **overpay-tolerant** — a transfer of at least the snapshot settles and the ledger records the ACTUAL received amount (on-chain tips are real money received); an underpay quarantines as `receipt_mismatch` for `payment-review` (`--provider ton`), as do unknown orders, second charges and out-of-stock-after-charge transfers. Enabled by `TON_WALLET_ADDRESS` + `USD_PER_TON` together (`TON_API_KEY` optional, rate-limit lift only); a transfer without the exact memo can never be matched and is refunded manually from the wallet. Migrations: `021_ledger_provider_crypto_expansion.sql` rebuilds the ledger provider CHECK constraints to accept `ton` and `nowpayments` — and `balance`, a deliberate **DB-only forward-pin** for the approved admin balance feature (the app layer rejects `balance` facts until that feature lands, same discipline as `stripe` in 020); `022_orders_total_ton_nano.sql` adds the order nanoton snapshot. Subscriptions remain Stars-only.

- **NOWPayments (300+ coins) hosted invoices** — an optional sixth payment provider: the checkout keyboard and the Mini App grow a "Crypto (300+ coins)" button for non-subscription carts, the buyer is redirected to a NOWPayments hosted invoice page offering 300+ coins, and the order settles when NOWPayments sends a signed IPN callback. Enabled by `NOWPAYMENTS_API_KEY`, `NOWPAYMENTS_IPN_SECRET` and `NOWPAYMENTS_RETURN_URL` (public HTTPS) — all three must be set together. Register `<WEBHOOK_URL>/nowpayments-webhook` as the IPN callback URL in the NOWPayments dashboard. IPN callbacks are HMAC-SHA512-signed over the canonicalized body (object keys sorted recursively, compact separators, no HTML escaping, no trailing newline): once the signature verifies, the signed body itself is authoritative and settles the order WITHOUT an API refetch (the CryptoBot/Stripe pattern). An invalid signature is rejected with 403 and recorded nowhere; signed replays are ACKed with 200 and settle nothing new; signed-but-inconsistent facts are quarantined for `payment-review` (`--provider nowpayments`). Only `payment_status: finished` settles — every other lifecycle status is acknowledged without side effects — and the signed USD amount must match the order total exactly (the IPN echoes our own invoice, so unlike TON there is no overpay tolerance). ⚠️ Confirm the byte-level canonicalization agreement with NOWPayments' signer with ONE live test payment before enabling the rail in production (verification fails closed: a mismatch rejects genuine IPNs, it can never accept a forged one). **No new migration** — `021_ledger_provider_crypto_expansion.sql` admitted `nowpayments` into the ledger provider CHECK constraints. Subscriptions remain Stars-only.

- **Admin management pack** — the admin panel (`/admin`) grows payment-operations and order-inspection surfaces, and the admin handlers are split into thematic modules:
  - **Payment review queue in the bot** (`/payreview`) — quarantined payment cases from every provider bucket are listed in chat, one card per case (order summary, payment state, per-target reason codes), and Settle / Refund / Dismiss run as a two-tap preview-then-confirm flow that rebuilds and re-validates the exact target set against the ledger before applying — a case that changed between the taps fails closed with a conflict message. Resolutions are recorded with the acting admin's id. Cross-provider partial resolutions and refunded-kind anomalies without a durable refund row stay CLI-only (`payment-review resolve`).
  - **Balance payments** — buyers with a positive USD balance get a "Pay (balance)" button at checkout (non-subscription carts) that settles the order synchronously: the buyer's balance is debited and a synthetic payment fact (`balance:<orderID>`, the buyer as required payer) settles through the same guarded ledger path as the external rails. The debit is idempotent across crash windows — the order's net `balance_txs` effect decides whether a re-tap debits — and a failed settle is answered with an exact compensating credit. Admins adjust balances with `/setbalance <user_id> <±amount> [reason]`; every adjustment is audited in `balance_txs` (type `admin_adjust[: reason]`, the acting admin in `ref_id`). **No new migration** — `021_ledger_provider_crypto_expansion.sql` pre-admitted `balance` into the ledger provider CHECK constraints as a forward pin. Subscriptions remain Stars-only.
  - **Order card** (`/order <id>`) — full order details for admins: buyer, items, USD/Stars/RUB/TON totals, status, localized payment method and payment id.
  - **Provider status** (`/paystatus`) — one line per payment rail (Stars, CryptoBot, YooKassa, Stripe, TON, NOWPayments, Balance) with configuration state, exchange rates and the webhook URLs to register, plus a warn state for half-configured rails — without printing any secret.
  - Localized payment-method names in the buyer's order history now cover all seven rails (previously YooKassa, Stripe, TON and NOWPayments leaked their raw provider slugs).

### Fixed

- **Worker-path settlements deliver the full notification set** — orders settled by the polling workers (TON, where the poller is the ONLY settlement path, and the CryptoBot poller backup) now send the buyer's payment confirmation, the admin payment notification and the outbound `order.paid` webhook alongside the loyalty/referral outcome messages. Previously a TON buyer received no payment confirmation at all.

### Tests

- Added a full E2E regression for the YooKassa purchase journey: RUB button visibility at checkout, redirect payment creation, settlement after the authoritative refetch (order paid, stock, loyalty, buyer and admin notifications, outbound webhook), and idempotent webhook replay.
- Added a full E2E regression for the Stripe purchase journey: USD card button visibility at checkout, hosted Checkout Session creation via the mocked Stripe API, settlement straight from the signed webhook body with the API hit count pinned at exactly 1 — no refetch — (order paid, stock, loyalty, buyer and admin notifications, outbound webhook), and idempotent signed replay.
- Added a full E2E regression for the TON purchase journey: TON button visibility at checkout (hidden at a zero rate), instructions-only invoice with the prefilled `ton://transfer` deeplink and zero buyer-facing chain API calls, settlement by ONE polling-worker pass over a mock toncenter (order paid via `ton` with the `<lt>:<hash>` payment id, stock, loyalty, pinned worker-path notify surface), idempotent re-poll replay, and an underpaid transfer quarantined to `needs_review`.
- Added a full E2E regression for the NOWPayments purchase journey: crypto button visibility at checkout, hosted invoice creation via the mocked NOWPayments API, settlement straight from the signed IPN body with the API hit count pinned at exactly 1 — no refetch — (order paid, stock, loyalty, buyer and admin notifications, outbound webhook), and idempotent signed replay.
- Added E2E regressions for the admin-management pack: the balance purchase journey (admin `/setbalance` grant audited in `balance_txs` → checkout balance button → synchronous settlement → buyer/admin notifications and outbound webhook → replay-tap settles and debits nothing) and the bot payment-review flow (a duplicate YooKassa capture quarantined through the real webhook ingress → operator refund recorded → `/payreview` two-tap settle returns the order to `settled` and empties the queue).

### Quality

- **Context propagation** — all bot handlers now run under a per-handler 30-second timeout context (`b.handlerCtx()`, 54 former `context.Background()` call sites), the CryptoBot webhook joins the other provider webhooks in working under the request context, and the auth middleware's user upsert receives its context through a new factory parameter — the last unconverted site in the tree. The only remaining background roots are four documented lifetime-critical sites in `bot.go` (the helper definition, one-time setup, the rate-limit cleanup goroutine, update dispatch).
- **Security-boundary test hardening** — the invalid-receipt quarantine branch and the quarantine-write-failure path are now pinned for the YooKassa, Stripe and NOWPayments webhooks: a verifiable payment whose receipt cannot be built is ACKed (200) and lands in `payment_anomalies` as `webhook_invalid_receipt` without settling anything, while a failed anomaly write withholds the ACK (500) so the provider retries. The Stripe signature tests now prove an undecodable `v1` hex segment is skipped rather than failing verification, the YooKassa API mocks pin the exact `GET /v3/payments/{id}` method and path, and the ledger receipt tests once again assert the first order's zero-attempts property (an overwritten scan had silently discarded it).
- **Hygiene batch** — the three provider return-URL validators share one `isHTTPSURL` helper (RFC-correct: uppercase schemes accepted, hostless URLs rejected); the TON poller clamps its transaction-page limit to toncenter's 1–100 bounds; `ConvertUSDToNanoTON` guards the intermediate `usd×1e9` overflow; `LoyaltyWorker` depends on a two-method interface instead of the concrete store; the four ledger-rebuild migration tests share one harness (−501 lines of duplicated scaffolding); every payment replay leg re-asserts the persisted `payment_state`; and the TON checkout button no longer prints "TON" twice.
- **Generic capture-ingress CLI** — `payment-review ingest-provider` extends operator capture ingress beyond Stars to YooKassa, Stripe, TON and NOWPayments, with the same preview-then-`--apply --confirm-order` discipline as `ingest-stars`, per-rail currency/scale validation, the TON overpay-tolerance rule, quarantine for mismatched facts, and no-op replays. A memo-less TON transfer is now resolvable by attaching it to the intended order (previously manual-refund-only — see `docs/payment-operations.md` §7). Stars keeps `ingest-stars`; balance facts remain synthetic.
- **Mini App payment-button availability** — the cart payload now carries `total_rub`, `total_ton_nano` and per-rail `*_enabled` flags computed with the exact predicates of the bot checkout keyboard (rail configured, non-subscription cart, positive converted total), and the Mini App renders the YooKassa, Stripe, TON and NOWPayments buttons only when the corresponding flag is set — a disabled or zero-rate rail no longer offers a button that would only fail at checkout.
- **Docs and config text pass** — FAQ entries for enabling each card/crypto provider, the architecture doc's payment-notification section rewritten to match the real webhook/worker notify surfaces, `.env.example` ships empty Stripe placeholders (the format requirements moved into the comments), the TON combined-misconfiguration error names both offending variables, and the stale "USD→Stars" exchange comments now describe all three conversion rails.
- **Backlog follow-ups batch** — the bot layer gains `out_of_stock_after_charge` quarantine coverage for all five payment surfaces (CryptoBot/YooKassa/Stripe/NOWPayments webhooks + Stars `successful_payment`), each leg pinning the quarantine mechanism its handler actually writes, and the YooKassa factless-envelope anomaly is deliberately retagged `webhook_parse_failure` → `webhook_missing_payment_id` (a cleanly parsed envelope with no payment id is not a parse failure; true parse failures keep the old reason); the TON overpay-tolerant `>=` rule is single-sourced into the storage fact gate — `validatePaymentFact` now rejects an underpaying TON fact for EVERY caller (overpay still settles, every other rail keeps exact minor-unit equality) — and `payment-review ingest-provider` maps the storage sentinels to actionable operator messages naming the fact-vs-order amounts (or the rail with no valid frozen total) instead of the generic "local preview failed"; `ConvertUSDToNanoTON` additionally guards the nanoton quotient against NaN/±Inf, so even an absurd operator-configured rate can never turn `int64(+Inf)` platform garbage into an amount.

---

## [3.0.1] — 2026-09-13

### Added

- **Windows release downloads** — native `amd64` and `arm64` executables packaged as ZIP archives alongside the existing Linux archives. Downloads include translations, example settings, and setup documentation; the guided setup runs directly from PowerShell without Go, Make, or Docker.
- **Visible SHA-256 verification** — explicitly configured SHA-256 for the existing `checksums.txt` release manifest, included the sums and verification commands in release notes, and documented checking downloaded archives in PowerShell and Linux.

### Fixed

- **Checkout totals after a promo code** — the payment summary, Stars and USDT buttons, and new-order admin notification now display the saved order totals, including the discount and Stars rounding. Previously they showed the original cart price even though the invoice charged the discounted amount.
- **Payment operations on Windows** — `reconcile-stars` and `payment-review` now open existing SQLite databases using a correctly formed file URI. Windows drive letters previously produced an invalid URI and misleading database errors, including "out of memory". Read-only access and the refusal to create missing databases are preserved.
- **Payment settlement under concurrent writes** — writable SQLite transactions now reserve the writer before reading order state, allowing the configured busy timeout to wait for a competing writer. Previously a read-to-write upgrade could fail immediately and exhaust settlement retries during brief contention.

### Tests

- Added regression coverage for discounted checkout totals and existing database paths containing spaces, Unicode, `#`, and `%`.
- Added a controlled concurrent-writer regression that checks payment settlement, a single captured payment, and a single stock decrement after the competing writer finishes.
- The CI test job now runs on both Linux and Windows. Setup tests check Unix file permissions only on platforms that support them while retaining file and directory checks everywhere.

## [3.0.0] — 2026-08-27

### Added

- **Safe one-command bootstrap** — `telegram-shop-bot quickstart` now runs guided `init` → actionable `doctor` → normal `run`. The initializer asks only for a BotFather token and Telegram admin ID, validates bot identity with read-only `getMe`, derives `BOT_USERNAME`, atomically creates `.env` without overwriting an existing config, and applies private `0600` permissions on Unix. Previously the documented path required 6–7 commands plus manual editing, and `make setup` copied valid-looking placeholders into a group-readable file.
- **Unified diagnostics** — `telegram-shop-bot doctor` validates config, SQLite migrations, optional Redis, Telegram identity, webhook state and pending updates without printing the token or raw provider errors. It blocks polling when Telegram still has a webhook, verifies Redis with an authenticated `PING` instead of a bare TCP connect, and reports disabled inline mode. The old `cmd/preflight` entry point keeps its local/offline compatibility contract.
- **Small CLI contract** — `init`, `doctor`, `quickstart`, `run`, `version`, and `help` are available from the existing release binary without adding a CLI framework; running the binary with no arguments still starts the bot exactly as before.
- **Immutable commerce ledger** — order, payment, and fulfillment now have independent projections backed by append-only timelines, payment attempts, captures, refunds, anomalies, and operator resolutions. Provider ID, payer, amount, currency, and occurrence time are validated before settlement.
- **Guarded payment operations** — `reconcile-stars` performs a bounded read-only comparison with Telegram, while `payment-review` lists, previews, and resolves quarantined Stars and CryptoBot facts with explicit apply and order confirmation gates.

### Changed

- **Quick-start documentation** now leads with the real one-command flow (`make quickstart`) in English and Russian. `.env.example` no longer contains valid-looking secrets, and manual Docker setup stays available as a separate path.
- **Recurring Stars settlement** now commits payment and entitlement atomically, snapshots the subscription contract on the order, rejects stale or incompatible renewals, and prevents exact replays from extending access twice.
- **Revenue analytics** now reports gross captures, refunds, and net revenue while excluding compensated quarantined captures.
- **Webhook URL contract** now treats `WEBHOOK_URL` as the public base URL. Telegram uses `/telegram-webhook`; CryptoBot uses `/cryptobot-webhook`.

### Fixed

- **Webhook route contract** — `WEBHOOK_URL` is now consistently the public base URL: Telegram registers and the HTTP server accepts `<base>/telegram-webhook`; CryptoBot uses `<base>/cryptobot-webhook`. Previously the server mounted an unstripped `/webhook/` prefix while the inner handler expected root paths, so documented webhook requests returned 404.
- **Startup secret redaction** — Telegram transport failures during normal `run` now return a stable sanitized error instead of logging the request URL containing `BOT_TOKEN`. Token prevalidation only rejects structurally unsafe values; Telegram `getMe` remains authoritative so future BotFather token alphabets are not blocked locally.
- **Durable Stars acknowledgement** — webhook and polling acknowledgements are held until each charge is settled or durably quarantined. Failed updates no longer disappear through premature offset advancement.
- **Public webhook authentication** — every public Telegram webhook requires a strong secret regardless of application mode.
- **CryptoBot correctness** — exact decimal conversion, provider occurrence timestamps, malformed fact preservation, and mutable-head pagination prevent rounding drift, duplicate settlement, and skipped paid invoices.
- **Refund and anomaly finality** — subscription entitlements roll back on refund, resolved malformed facts remain terminal on replay, and non-existent orders can be closed without fabricated revenue.

## [2.0.0] — 2026-08-04

### ⚠️ Breaking Changes

- **`OrderService.ConfirmPayment` signature** — was `ConfirmPayment(ctx, orderID, method, paymentID) error`, now returns `(*shop.PaymentOutcome, error)`. The outcome reports everything that happened during confirmation (points awarded, level-up, referral bonus, personal promo issued); sending user-facing messages based on it is the bot layer's job. All three confirmation paths (Stars `successful_payment`, CryptoBot webhook, CryptoBot polling) were updated.
- **Worker constructors changed:**
  - `NewCryptoBotPollingWorker(crypto, orders PaymentConfirmer, notify func(ctx, *shop.PaymentOutcome), interval)` — the poller now confirms through `OrderService` (same loyalty/referral/cache side effects as webhooks) and reports outcomes via callback instead of writing to the order store directly.
  - `NewCartRecoveryWorker(bot, cart, promos, users, i18n, metrics, interval, …)` — gained `*service.I18nService` (reminders in the user's language) and `*service.MetricsService` (gauges).
  - `NewLoyaltyWorker(store, svc, rdb, bot, i18n, users)` — gained a `storage.UserStore`: the notification language is read from the DB instead of hardcoded `"ru"`.
- **Default language is now `en`** — an empty or unknown Telegram `language_code` falls back to English (previously Russian), as the docs always promised. Language tags are normalized to their primary subtag (`ru-RU` → `ru`, `zh-hans-CN` → `zh`).
- **WAL journal mode** — the SQLite DSN now sets `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=1`. The data directory gains `*.db-wal` / `*.db-shm` files (gitignored); naive `cp shop.db` backups are no longer safe — use the built-in `VACUUM INTO` backups (see Reliability).
- **Dead code removed** — `internal/service/payment*.go` stub adapters (`VerifyPayment` → always `true`), the unused duplicate `internal/bot/middleware/admin.go`, the root `migration/` directory, and 17 dead i18n keys (including the balance leftovers and the `$0.00` line in the profile).

### Critical Fixes

- **False payment confirmation in the CryptoBot polling worker** — the 30-second poller fetched `active` invoices and unconditionally flipped orders `pending → paid`, confirming *unpaid* orders. Now only invoices with `status == "paid"` are processed; the rest are logged at debug level.
- **Pre-checkout approved without validation** — Stars `pre_checkout_query` was always answered `ok=true`. Now the referenced order must exist, belong to the paying user, still be `pending`, and its `TotalStars` must equal the invoice amount — otherwise the checkout is declined with a localized reason.
- **Stock check ignored quantity** — `CreateFromCart` only required `stock > 0`; ordering 5 of an item with 1 in stock passed. Now `stock >= quantity` is enforced per item and violations fail with `*shop.ErrInsufficientStock{ProductName, Have, Want}` and a human message (`error_insufficient_stock`).
- **Graceful shutdown** — every background goroutine (workers + HTTP server) is registered in a named worker group; shutdown stops updates, shuts the HTTP server down, drains workers for up to 10 s (stuck workers are logged by name) and only then closes the DB.
- **Loyalty worker hardening** — no more type-assertion panics on malformed Redis Stream messages (garbage is logged and XAck'ed, not retried forever); `XReadGroup` failures back off exponentially 1 s → 30 s; `XAck` errors are logged.
- **Stale product cache after payment** — confirming a payment now invalidates the cached products of the order (`ProductCacheInvalidator`; no-op without Redis). Previously the cached stock could stay wrong for up to an hour.
- **CryptoBot webhook retry storm** — idempotent repeats (`ErrOrderStatusConflict` / `ErrNotFound`) now return HTTP 200 instead of 500, so CryptoBot stops re-delivering forever; the webhook secret is compared with `subtle.ConstantTimeCompare`.
- **Subscriptions were never persisted** (found by the E2E suite) — migration `014` declared `subscriptions.user_id REFERENCES users(id)`, but the code stores the buyer's *Telegram* ID there (same convention as `orders.user_id`), so with `foreign_keys=1` every real insert failed. Migration `016_subscriptions_user_fk.sql` rebuilds the table without the bogus FK, keeping data and indexes.
- **Referral link never attached the referrer** (found by the E2E suite) — `ReferralStore.SetReferrer` ran `UPDATE users … WHERE id = ?` with a Telegram ID, so `referred_by` was never set. It now addresses the user by `telegram_id`; the first referrer wins, and repeated `/start` deep links no longer inflate `referral_stats`.

### Loyalty & Referrals (now actually working)

- **Cashback on every paid order** — `ConfirmPayment` awards `CalculateCashback(level, totalUSD)` points (1–10 % by level), records the transaction and runs `CheckAndUpgradeLevel`; the buyer gets `loyalty_points_awarded` / `loyalty_level_up` messages.
- **Referral bonus on the first paid order** of an invited user: the referrer receives **100 points** (`referral_bonus_referrer`), the newcomer receives a personal one-off promo code `REF-XXXXXXXX` (−10 %, 30 days, `referral_welcome_promo`). Idempotent even under concurrent confirmations: `referral_awards` keyed by `referred_user_id` + `INSERT OR IGNORE`.
- **Personal promo codes** — `promo_codes.bound_user_id`: a bound code is visible and applicable only to its owner (NULL = public, behavior unchanged).
- **`/referral` screen** (also `ref:open` callback and menu/profile button) — personal link `t.me/<bot>?start=ref_<code>`, invited count, total points earned, a "Share" button (`switch_inline_query`).
- **Profile** shows real points/level and progress to the next level; referral codes are generated with `crypto/rand`.

### UX / Main Menu

- **2-column main menu**: [🛍 Catalog | 🔍 Search], [🛒 Cart | ❤️ Wishlist], [📦 Orders | 👤 Profile], [🎁 Referral | 🆘 Support], [📄 Terms].
- **`/search`** — every hit is a button opening the product card; Back | Menu row on all branches (results, empty, hint).
- **`/wishlist`** — each item is a product button plus a ✖ remove button; an empty wishlist offers a catalog button.
- **6 new configurable button keys** — `menu_search`, `menu_wishlist`, `menu_referral`, `menu_terms`, `catalog_category`, `catalog_product` (categories and product lists are now styled independently of the catalog menu button); `/btnstyle` now manages 18 keys, and `product_wish` is actually applied on the product card.
- **Navigation fixes** — `back:` targets `wishlist` and `search`; Back from orders/support/terms leads to the menu; `handleCallback` is nil-safe for callbacks without an attached message.
- **`SetMyCommands`** now registers `/mysubs`, `/wishlist` and `/referral` too.

### Reviews & Ratings

- **Rating request after delivery** — once an admin marks an order delivered, the buyer gets a 1–5 ⭐ row (`review:<orderID>:<rating>`), then an optional free-text step (FSM, with Skip).
- **Only verified buyers** can rate (a delivered order with the product is required); one review per product per user — re-rating replaces via upsert.
- **Product card** shows `⭐ 4.7 (12)` when reviews exist and a "Reviews" button (`review:list:<productID>`) with the last 3 texts.
- **`/reviews` (admin)** — the 10 most recent reviews with delete buttons (`review:del:<id>`).

### Product Photos

- **Admin wizard accepts photos as Telegram messages** (largest `PhotoSize` is stored as `file_id`); URL input remains an alternative; up to **10 photos** per product; edit mode manages the photo list with per-photo delete buttons.
- **Gallery on the product card** — one photo renders as before; several photos are sent as a media group with the card (and its buttons) as a separate message. Inline mode uses the first photo.

### Stars Subscriptions (recurring)

- **30-day subscription products** — a product with `sub_period_days=30` is sold as a recurring Telegram Stars subscription: the invoice is sent via raw `sendInvoice` with `subscription_period=2592000` (tgbotapi v5 does not know the field). Subscription products are payable with Stars only; crypto is hidden at checkout and rejected with `sub_stars_only`.
- **`/mysubs` command** — lists active subscriptions with expiry dates; each has a cancel button (`sub:cancel:<id>`) that calls raw `editUserStarSubscription{is_canceled:true}` and marks the row `canceled` locally (access remains until the paid period ends).
- **Subscription bookkeeping** — the initial recurring capture and entitlement commit atomically; `expires_at` uses Telegram's raw `subscription_expiration_date` when present, while only the first capture may use the deterministic order-period fallback. Renewal captures require an explicit provider expiry and exact replays never extend access twice.
- **`worker/subscription.go`** — hourly worker: marks overdue subscriptions `expired` and sends a one-shot `sub_expiring_soon` reminder 72h before expiry (`MarkReminded` only after a successful send, so failed reminders are retried).
- **Admin wizard** — the add-product dialog got a final step: regular product vs. 30-day subscription.
- ⚠️ **Recurring payments require live verification** («требует проверки в бою»): renewal `successful_payment` updates, `subscription_expiration_date` delivery, and `editUserStarSubscription` behavior cannot be exercised against the real Bot API from tests.

### Mini App + REST API

- **`web/app/`** — a vanilla-JS Mini App embedded into the binary (no build step): catalog → product card (photos, price, rating) → cart (+/−/remove) → checkout via `Telegram.WebApp.openInvoice` (Stars) or `openLink` (crypto). Theme from `themeParams`, strings served by the bot (`GET /api/i18n?lang=`).
- **REST API `/api/*`** (`internal/webapi/`) — `GET /api/me`, `GET /api/catalog`, `GET /api/products?category=&page=`, `GET /api/products/{id}` (with rating and photos), `GET/POST/DELETE /api/cart`, `POST /api/checkout {method: stars|crypto, promo?}` → `{invoice_link}`, `GET /api/photo/{file_id}` (proxies `getFile`). JSON errors are `{"error":"<i18n key>"}`; request bodies are capped at 64 KB.
- **Authentication** — `Authorization: tma <initData>` validated per the Telegram spec (secret = HMAC-SHA256(key="WebAppData", msg=botToken); sorted data-check-string; `auth_date` TTL 1 hour), covered by the official test vector.
- **Opt-in via `WEBAPP_URL`** — when set (must be public HTTPS), `/app` (static) and `/api/` are mounted on the existing :8080 server and the bot's menu button becomes a `web_app` button. Without it nothing is mounted and the bot works exactly as before (a warning is logged).

### Analytics & Metrics

- **`/analytics`** — 14-day revenue chart with text bars (`▇`), top-10 buyers (total spent, order count) and a promo-code report (uses, total discount); fully localized via `admin_*` keys.
- **`/export_orders [from] [to]`** — CSV export now accepts an optional date range (`/export_orders 2026-01-01 2026-02-01`); format errors are reported in a human way.
- **Live Prometheus metrics** — `OrdersCreated` incremented in `CreateFromCart`, `ActiveCarts` gauge recomputed by the cart-recovery worker each tick, `CartsAbandoned` incremented when a reminder is sent; new panels in `monitoring/grafana_dashboard.json`.

### Admin Notifications via Topics

- **`notifyAdmins(ctx, kind, text)`** (`internal/bot/notify.go`) — with `ADMIN_GROUP_ID` configured, order events (new / paid / delivered) go to the supergroup, optionally routed into forum topics via `TOPIC_ORDERS_NEW` / `TOPIC_ORDERS_PAID` / `TOPIC_ORDERS_DELIVERED`; without it, the old behavior remains (DM to every admin).
- Group notifications use English i18n keys (`admin_order_new`, `admin_order_paid_*`); the last hardcoded Russian admin texts are gone.

### Internationalisation

- **All 5 locales complete** — `ru`, `en`, `es`, `de`, `zh` now have full key parity, including the admin panel (`admin_*` keys, admin's own `language_code` is respected) and worker messages (cart recovery, loyalty level-up in the recipient's DB language). The parity test covers all 5 locales plus a reverse "key used in code exists in ru.json" check.
- Truncated es/de/zh translations (terms, onboarding, promo, error, paysupport texts) were completed properly, not machine-stubbed.

### Reliability & Ops

- **Backups without the sqlite3 CLI** — the daily backup runs `VACUUM INTO 'backups/shop_YYYYMMDD_HHMMSS.db'` on the live connection pool, so it works in scratch Docker images; rotation keeps the **7 newest** files.
- **HTTP server & workers** — one shared HTTP server on :8080 serves `/health`, `/metrics`, Telegram/CryptoBot webhooks, and (opt-in) `/app` + `/api/`; everything shuts down in order (see Critical Fixes).

### Testing

- Property test (rapid): double/concurrent `ConfirmPayment` of one order ⇒ exactly one stock decrement, one points award, one `referral_awards` row.
- Unit tests: polling worker (paid/active/mixed invoices), all four pre-checkout rejections + happy path, Mini App `initData` validation against the official Telegram vector, i18n tag normalization, backup rotation, review/subscription stores, WAL pragma.
- End-to-end scenarios (`internal/bot`, mock Bot API): full purchase flow with points and review, referral first-purchase bonus (and no second bonus), subscription payment bookkeeping.

### Migrations summary

| File | Description |
|------|-------------|
| `012_reviews.sql` | `reviews` table — rating 1..5, optional text, `UNIQUE(product_id, user_id)` |
| `013_product_photos.sql` | `product_photos` — Telegram `file_id` gallery, `ON DELETE CASCADE` |
| `014_subscriptions.sql` | `products.sub_period_days` + `subscriptions` table with indexes |
| `015_referral_awards.sql` | `promo_codes.bound_user_id` (personal promos) + `referral_awards` idempotency table |
| `016_subscriptions_user_fk.sql` | Rebuilds `subscriptions` without the incorrect `user_id → users(id)` FK (column holds Telegram IDs) |

---

## [1.2.0]

### Admin: Button Style Customization

- **`/btnstyle` command** — new admin command that opens an interactive inline menu showing all 12 configurable button keys with their current style indicators (🔵🟢🔴⬜). Tapping any button opens a style picker with four options (Primary, Success, Danger, Default); the change is applied immediately and persisted to SQLite.
- **`button_styles` table** (migration `011_button_styles.sql`) — stores per-button style overrides as `key TEXT PRIMARY KEY, style TEXT`. Automatically created on first startup via the existing migration system.
- **`UISettingsStore` interface** (`internal/storage/ui_settings.go`) — `GetButtonStyle`, `SetButtonStyle`, `ListButtonStyles` methods backed by `SQLUISettingsStore`.
- **In-memory style cache** (`Bot.uiStyles sync.Map`) — loaded from DB once at startup via `reloadButtonStyles(ctx)`. Single-key updated immediately when admin changes a style — no restart needed.
- **`styledBtn(key, text, data, defaultStyle)` method** — all 12 semantic buttons across the UI now resolve their color through this helper. If no override is stored for a key, the default style (same as before) is used transparently. Nil-safe for test fixtures.
- **Button key constants** (`BtnKeyMenuCatalog`, `BtnKeyMenuCart`, `BtnKeyMenuOrders`, `BtnKeyMenuProfile`, `BtnKeyMenuSupport`, `BtnKeyProductAdd`, `BtnKeyProductWish`, `BtnKeyCartCheckout`, `BtnKeyCartRemove`, `BtnKeyPayStars`, `BtnKeyPayCrypto`, `BtnKeyPayCancel`) — defined in `styled_keyboard.go`; each maps to a human-readable label via `ButtonKeyLabel()`.
- **`StyleEmoji()` helper** — returns 🔵/🟢/🔴/⬜ for a given `ButtonStyle`; used in both the admin list view and the inline style picker.
- **Callback routing** — three new admin callback prefixes handled in `handleCallback`: `admin:btnlist`, `admin:btnpick:<key>`, `admin:setstyle:<key>:<style>`.

### UX / Navigation

- **Bot API 9.4 colored buttons** (`styled_keyboard.go`) — full support for `style` field in inline keyboard buttons via raw API calls. `BtnPrimary` (blue), `BtnSuccess` (green), `BtnDanger` (red) used across all screens.
- **"🏠 Menu" button everywhere** — all screens (catalog, product, cart, checkout, orders, profile, support, terms, payment) now have a persistent "go to main menu" button via `back:menu` callback.
- **Main menu redesign** — catalog button (primary/blue), cart button (success/green) for visual hierarchy.
- **Category & product lists** — primary-style buttons for categories and products; back/menu nav row on every screen.
- **Checkout flow** — confirm order button is success/green; cancel order is danger/red; pay Stars is primary/blue; pay crypto is success/green.
- **`sendMainMenu` helper** — extracted from `handleStart`; reused for `/start`, callback `back:menu`, and any screen's "🏠" button.
- **`setChatMenuButton`** — sets the persistent "/" commands button in the Telegram input bar at bot startup.
- **`SetMyCommands`** — registers `/start`, `/catalog`, `/cart`, `/orders`, `/search`, `/profile`, `/support` in the Telegram commands menu.
- **Smooth photo transitions** — `onProductSelected` now uses raw `editMessageMedia` API with styled keyboard; falls back to `sendPhoto` for new messages.
- **`toast()` helper** — non-blocking `answerCallbackQuery` popups for cart-add and wishlist-toggle confirmations (no blocking alert).
- **`ForceReply` for promo input** — promo code entry uses `ForceReply` with placeholder `PROMO123`.
- **Payment button amounts** — Stars and crypto payment buttons show the actual amount: `⭐ Pay Stars (100 ⭐)`, `💎 Pay Crypto ($1.50)`.

### Performance

- **DB indexes** — added 7 indexes on frequently queried columns (`orders.user_id`, `orders.status`, `order_items.order_id`, `cart_items.user_id`, `products.category`, `wishlist.user_id`, `users.ref_code`) via migration `008_add_indexes.sql`. Significant speedup on medium/large datasets.

### Bug Fixes

- **Wishlist notification dedup** — each price-drop and back-in-stock notification is now sent exactly once per event cycle. Added `price_drop_notified_at` / `back_in_stock_notified_at` columns (migration `009_wishlist_notif_tracking.sql`) and 4 new store methods (`MarkPriceDropNotified`, `ClearPriceDropNotified`, `MarkBackInStockNotified`, `ClearBackInStockNotified`).
- **CryptoBot polling** — fixed `GetInvoices` call: was polling `"paid"` (entire history, grows unboundedly); now polls `"active"` (only outstanding invoices that may have been paid but missed via webhook).
- **Order `updated_at`** — `UpdateOrderStatus` and `CancelOrder` now set `updated_at = CURRENT_TIMESTAMP` on every status transition. Migration `010_orders_updated_at.sql` adds the column and backfills it from `created_at`.

### Internationalisation

- **i18n coverage** — removed all hardcoded Russian strings from bot handlers:
  - Stars payment receipt (`stars_receipt`)
  - Admin notification on Stars payment (`admin_order_paid_stars`)
  - Admin notification on crypto payment (`admin_order_paid_crypto`)
  - Loyalty level-up message (`loyalty_level_up`)
  - VIP gift notification (`loyalty_vip_gift`)
- Added `Tf(lang, key, args...)` helper to `I18nService` for format-string keys.
- Crypto payment webhook now resolves the buyer's language before sending the confirmation message.

### Code Quality

- **Handlers split** — monolithic `handlers.go` (1543 → 319 lines) split into 9 themed files:
  - `handlers_start.go` — `/start`, `/cancel`, `/help`
  - `handlers_catalog.go` — catalog browsing, category/product selection
  - `handlers_cart.go` — cart view, add/remove/qty changes, checkout
  - `handlers_checkout.go` — promo input, order confirm, payment method keyboard
  - `handlers_payment.go` — Stars and crypto payment flows, pre-checkout
  - `handlers_orders.go` — order history
  - `handlers_search.go` — `/search` command
  - `handlers_wishlist.go` — wishlist toggle and view
  - `handlers_support.go` — support and terms pages
  - `handlers_inline.go` — inline mode catalog (new)
  - `handlers.go` — routing core only (`route`, `routeMessage`, `handleCallback`, `send`, helpers)
- **Worker interfaces** — `OnboardingWorker` and `WishlistWatcherWorker` now depend on minimal local interfaces (`onboardingUserStore`, `wishlistStore`) instead of concrete `*storage.SQL*` types. Decouples workers from storage implementation.
- **Inline catalog** (`handlers_inline.go`) — added `update.InlineQuery` branch in `route()`; `handleInlineQuery` returns up to 20 matching active in-stock products as `InlineQueryResultCachedPhoto` (with photo) or `InlineQueryResultArticleHTML` (without). `AllowedUpdates` in polling config updated to include `inline_query`.
- **Context propagation** — all `context.Background()` calls in handlers replaced with `handlerCtx()` (30 s timeout). Added `handlerCtx()` helper in `bot.go`.
- **Dead code removed** — deleted `internal/storage/balance.go` (internal balance/USD top-up feature, unused). Removed `Transaction` type and `PaymentMethodBalance` constant from `models.go` and `ui_text.go`.
- **LoyaltyWorker** — constructor now receives `*service.I18nService`; level-up messages use i18n instead of hardcoded Russian.

### Security & Reliability

- **HTTP server timeouts** — added `ReadTimeout: 10s`, `WriteTimeout: 10s`, `IdleTimeout: 60s` to the webhook HTTP server.
- **Request body limit** — `http.MaxBytesReader` (1 MB) applied in both webhook handlers to prevent oversized payload attacks.

### CI / Tooling

- **golangci-lint** — added `.golangci.yml` (errcheck, govet, staticcheck, gosimple, unused, ineffassign, misspell) and a `lint` job to `.github/workflows/ci.yml`.
- **Coverage reporting** — `go test -coverprofile=coverage.out` runs in CI; `coverage.out` uploaded as build artifact.
- **goreleaser** — added `.goreleaser.yml`; Linux amd64/arm64 binaries + Docker image pushed to `ghcr.io` on `v*` tag push via `.github/workflows/release.yml`.

### Open-Source Developer Experience

- **`.env.example`** fully rewritten with all variables, section headers, and inline comments. Covers bot, payment, security, Redis, monitoring, outbound webhook, and localisation settings.
- **Hot-reload dev environment** — `docker-compose.dev.yml` + `Dockerfile.dev` + `.air.toml` for instant live-reload during development with `make dev`.
- **`make setup`** — bootstrap target: copies `.env.example` → `.env`, creates `data/` and `backups/` directories.
- **Documentation** (`docs/`):
  - `getting-started.md` — step-by-step setup guide (local, Docker, Telegram configuration)
  - `environment-variables.md` — reference for every environment variable with defaults and descriptions
  - `faq.md` — common questions for users and contributors
  - `architecture.md` — component diagram, request flow, data model overview (Mermaid-compatible ASCII)
- **Localisation additions** — `locales/es.json` (Spanish), `locales/de.json` (German), `locales/zh.json` (Chinese). All keys fully translated. Bot auto-selects locale from the user's Telegram `LanguageCode`.
- **`CONTRIBUTING.md`** rewritten — quick-start flow, hot-reload guide, how to add a new locale, code style rules, PR checklist.
- **GitHub Issue Templates** (`.github/ISSUE_TEMPLATE/`):
  - `bug_report.md` — structured bug report form
  - `feature_request.md` — feature / improvement proposal form
  - `question.md` — help & question form

### Integrations

- **Outbound webhooks** (`internal/service/outbound_webhook.go`) — fire `order.paid` and `order.delivered` events to an external URL. Configurable via `OUTBOUND_WEBHOOK_URL` and `OUTBOUND_WEBHOOK_SECRET` env vars. Requests include `X-Webhook-Secret` header and a JSON payload with order metadata. Async (non-blocking). Triggered after Stars payment, CryptoBot payment confirmation, and admin "set delivered" action.

### Migrations summary

| File | Description |
|------|-------------|
| `008_add_indexes.sql` | 7 performance indexes |
| `009_wishlist_notif_tracking.sql` | Notification dedup columns on wishlist |
| `010_orders_updated_at.sql` | `updated_at` column on orders |

---

## [1.1.0] — 2025-05-27

- Production-ready improvements (see git tag `v1.1.0`)

## [1.0.0] — Initial public release

- Initial public release of Telegram Shop Bot
