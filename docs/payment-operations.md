# Payment operations

The payment ledger is fail-closed: ambiguous provider facts stay in
`needs_review` until an operator names the exact local targets. Commands print
local IDs, reason codes, amounts and aggregate counts; they do not print bot
tokens or provider transaction IDs.

## 1. Reconcile Telegram Stars

```bash
telegram-shop-bot reconcile-stars
```

The command reads a bounded Telegram window and the SQLite ledger without
changing either. Exit `0` means the complete inspected window is green. Exit
`1` means the window is incomplete or contains a provider-only, local-only,
mismatched, duplicate, or unresolved row.

For a larger account, raise the explicit bound:

```bash
telegram-shop-bot reconcile-stars --max-rows 5000 --page-size 100
```

An exact full final page is probed one row beyond the bound. The probe is not
processed; it only distinguishes an exact end from truncation.

## 2. List quarantined facts

```bash
telegram-shop-bot payment-review list --provider stars
telegram-shop-bot payment-review list --provider crypto
telegram-shop-bot payment-review list --provider yookassa
telegram-shop-bot payment-review list --provider stripe
telegram-shop-bot payment-review list --provider ton
telegram-shop-bot payment-review list --provider nowpayments
telegram-shop-bot payment-review list --provider unknown
```

Or through make:

```bash
make payment-review PROVIDER=yookassa
```

The list returns exit `1` while targets exist. Record the printed `order`,
`event_ids`, `anomaly_ids`, `order_target`, and `reasons`. A provider capture
identity is deliberately absent from this output.

`--provider unknown` is a provider-neutral inbox for legacy paid/delivered
orders whose original payment rail cannot be proven. It does not assign Stars
or crypto. Either attach an authenticated provider fact before resolving the
provider-specific case, or explicitly cancel the unprovable import as shown
below. Neither path can manufacture settled revenue.

## 3. Recover a provider-only Stars row

Use the exact transaction ID from the trusted Telegram operator interface. The
command reads Telegram again and requires exactly one authenticated match in a
complete bounded window.

Preview a capture without local writes:

```bash
telegram-shop-bot payment-review ingest-stars \
  --kind capture --transaction '<telegram-transaction-id>' --order 42 \
  --actor 'operator@example' --reason 'provider-only capture'
```

Apply it only after checking the preview:

```bash
telegram-shop-bot payment-review ingest-stars \
  --kind capture --transaction '<telegram-transaction-id>' --order 42 \
  --actor 'operator@example' --reason 'provider-only capture' \
  --apply --confirm-order 42
```

A recovered capture is quarantined. It never decrements stock, fulfills an
order, grants loyalty points, activates an entitlement, or sends a refund.

For a Stars refund, Telegram reuses the original capture transaction ID. The
command therefore derives the parent identity from the authenticated refund row
instead of accepting an operator-supplied parent:

```bash
telegram-shop-bot payment-review ingest-stars \
  --kind refund --transaction '<telegram-refund-id>' \
  --order 42 \
  --actor 'operator@example' --reason 'provider-only refund' \
  --apply --confirm-order 42
```

The refund is accepted only when its order, Telegram payer, provider timestamp,
money tuple, derived parent identity, and cumulative amount match the immutable
capture. Invalid facts become durable review evidence instead of being retried
blindly.

## 4. Preview and resolve an exact target set

Pass every target printed for one provider. Repeat `--event` and `--anomaly` as
needed. The first command is always read-only:

```bash
telegram-shop-bot payment-review resolve \
  --provider stars --order 42 --event 17 --event 18 \
  --state settled --actor 'operator@example' \
  --reason 'duplicate capture fully refunded'
```

Apply the same reviewed command with the order confirmation gate:

```bash
telegram-shop-bot payment-review resolve \
  --provider stars --order 42 --event 17 --event 18 \
  --state settled --actor 'operator@example' \
  --reason 'duplicate capture fully refunded' \
  --apply --confirm-order 42
```

The requested state is checked against a ledger-derived projection. A
quarantined capture must be fully compensated by a durable succeeded refund;
the command cannot arbitrarily turn it into revenue. If another provider still
has targets for the same order, the selected decisions are appended but the
order remains `needs_review` and the command returns exit `1`.

For a legacy pending subscription quarantine with no provider fact, use the
printed order target and the derived `cancelled` state:

```bash
telegram-shop-bot payment-review resolve \
  --provider stars --order 42 --order-target 42 --state cancelled \
  --actor 'operator@example' --reason 'stale unpaid reservation' \
  --apply --confirm-order 42
```

For a paid/delivered legacy row in the provider-neutral inbox, the only direct
terminal decision is an explicit non-revenue cancellation:

```bash
telegram-shop-bot payment-review resolve \
  --provider unknown --order 42 --order-target 42 \
  --decision dismissed --state cancelled \
  --actor 'operator@example' \
  --reason 'legacy row has no attributable provider' \
  --apply --confirm-order 42
```

This appends an immutable operator resolution and changes the order to
`cancelled`; it creates no payment attempt, refund, entitlement, or revenue.

## 5. YooKassa (RUB card) operations

### Security model: unsigned webhook, authoritative refetch

YooKassa notifications are unsigned. The `/yookassa-webhook` body is used only
to learn the event type and the payment id; settlement happens only after the
bot refetches the payment with an authenticated `GET /v3/payments/{id}` call.
A `payment.succeeded` notification therefore settles nothing by itself: the
refetched payment must be terminal, paid, an exact positive RUB amount with two
fractional digits, and bound to the local order's money tuple and id. Any
disagreement quarantines the fact instead of settling.

Known limitation: the `/yookassa-webhook` endpoint is unauthenticated (YooKassa
provides no notification signature). Each request provokes at most one upstream
`GetPayment`; unparseable or factless bodies are quarantined and acknowledged
without any upstream call. If the endpoint is abused, rate-limit it at the
reverse proxy — settlement is still impossible without valid credentials and a
matching order.

### Lost-webhook backup poller

The webhook is the primary settlement path; a background poller backs it up
when notifications are lost (bot downtime, network partition). Every 60
seconds it lists the shop's `succeeded` payments created within a rolling
24-hour window (`GET /v3/payments`, cursor pagination: 50 per page, capped at
20 pages per tick — a truncated tick logs one `page cap reached` warning and
defers the tail to the next tick, which re-scans from the start) and replays
each item through the same fail-closed receipt validation and ledger path the
webhook uses. The poller is idempotent: an already-settled order is answered
with a durable conflict no-op, and money that arrives after the stock sold out
never settles — it is durably quarantined with the same
`out_of_stock_after_charge` reason the webhook path uses (as a needs-review
capture the review surface lists) and is never retried. What the poller does
NOT cover: orders whose payment never succeeded at the
provider (a non-succeeded payment never appears in the list) and quarantined
cases, which stay in `payment-review` until an operator resolves them —
operator escalation is unchanged (`make payment-review PROVIDER=yookassa` /
`/payreview`). The worker starts only when YooKassa is fully configured and
`USD_TO_RUB_RATE` is positive — the exact checkout-button predicate — and
introduces no new environment variables.

### What quarantined YooKassa facts look like

Quarantined facts are `payment_anomalies` rows with provider `yookassa` and the
order in `needs_review`:

| Reason | Meaning |
|---|---|
| `webhook_parse_failure` | Unparseable webhook body, stored as a sha256 digest only |
| `webhook_missing_payment_id` | Envelope that parsed cleanly but carried no payment id, stored as a sha256 digest only |
| `webhook_invalid_receipt` | Refetched payment cannot produce a valid receipt |
| `receipt_mismatch` | Valid receipt that disagrees with the order's money tuple |
| `out_of_stock_after_charge` | Paid payment whose product went out of stock before fulfillment |

YooKassa facts carry no payer id (the provider has no Telegram payer
identity), so payer checks compare money and order linkage only.

### Resolve flow

```bash
make payment-review PROVIDER=yookassa
```

The semantics are identical to the Stars and crypto flows above: the list exits
`1` while targets exist and prints local ids and reason codes only; resolve
previews read-only first and then applies with `--apply --confirm-order N`.
A quarantined capture still requires a durable succeeded refund before it can
be resolved to `settled`.

### Refunds

Refunds are operator-driven: initiate them in the YooKassa dashboard or via
the YooKassa API. Nothing in this bot refunds automatically — the webhook
acknowledges `refund.*` notifications without touching the ledger. The ledger
records a refund through its refund ingestion path (`RecordRefund` /
`IngestProviderRefund`), which validates it against the immutable captured
attempt (exact parent identity, money tuple, cumulative amount) and appends
durable review evidence for anything that disagrees. There is no dedicated
refund-ingress CLI for YooKassa yet: `payment-review ingest-stars` reads the
Telegram Bot API and cannot serve this rail.

### RUB rate snapshots

`orders.total_rub` is frozen at order creation from `USD_TO_RUB_RATE`. Changing
the environment variable later changes only future orders; it never reprices
existing ones, and quarantined facts are validated against the frozen order
total.

### Subscriptions

Subscriptions remain Stars-only. The RUB button is never offered for
subscription carts.

## 6. Stripe (USD card) operations

### Security model: signed webhook, no refetch

Stripe notifications are signed. Every `/stripe-webhook` request must carry a
`Stripe-Signature` header whose HMAC-SHA256 over `<timestamp>.<body>` verifies
against the endpoint secret (`STRIPE_WEBHOOK_SECRET`, `whsec_` prefix);
timestamps more than 300 seconds from now are rejected as replays. Once the
signature verifies, the body itself is authoritative: settlement happens from
the verified body WITHOUT any API refetch (the CryptoBot pattern, unlike the
unsigned YooKassa flow, which must re-read the payment). An invalid signature
is unauthenticated junk: the endpoint answers `403` and records nothing — no
anomaly, no event, no order change.

### What quarantined Stripe facts look like

Quarantined facts are `payment_anomalies` rows with provider `stripe` and the
order in `needs_review`:

| Reason | Meaning |
|---|---|
| `webhook_parse_failure` | Signature-valid body that still fails to parse, stored as a sha256 digest only |
| `webhook_invalid_receipt` | Signed checkout session that cannot produce a valid receipt |
| `receipt_mismatch` | Valid receipt that disagrees with the order's money tuple |
| `out_of_stock_after_charge` | Paid session whose product went out of stock before fulfillment |

Stripe facts carry no payer id (the provider has no Telegram payer
identity), so payer checks compare money and order linkage only.

### Resolve flow

```bash
make payment-review PROVIDER=stripe
```

The semantics are identical to the Stars, crypto and YooKassa flows above: the
list exits `1` while targets exist and prints local ids and reason codes only;
resolve previews read-only first and then applies with
`--apply --confirm-order N`. A quarantined capture still requires a durable
succeeded refund before it can be resolved to `settled` — see the Refunds
paragraph below for how that refund is recorded.

### Refunds

Refunds are operator-driven: initiate them in the Stripe dashboard or via the
Stripe API. Nothing in this bot refunds automatically. The ledger records a
refund through its refund ingestion path (`RecordRefund` /
`IngestProviderRefund`), which validates it against the immutable captured
attempt (exact parent identity, money tuple, cumulative amount) and appends
durable review evidence for anything that disagrees. There is no dedicated
refund-ingress CLI for Stripe yet: `payment-review ingest-stars` reads the
Telegram Bot API and cannot serve this rail.

### USD-native amounts

Stripe charges the order's own USD total: there is no rate snapshot and no
converted currency, so `orders.total_usd` IS the charged fact that receipts
are validated against. Stripe refuses USD card charges below $0.50, so
checkout refuses to start a Stripe session below that minimum instead of
creating a payment that can never succeed.

### Subscriptions

Subscriptions remain Stars-only. The Stripe button is never offered for
subscription carts.

## 7. TON (on-chain TON) operations

### Security model: no webhook — polling worker

TON has no webhook or signature. A polling worker refetches the watched
wallet's latest transactions from toncenter every 30 seconds and replays each
matching transfer into the ledger, where repeats are idempotent no-ops keyed
by the `<lt>:<hash>` external id. toncenter's `getTransactions` returns only
finalized (ledger-confirmed) on-chain transactions, so a polled transfer is
final — there is no pending-state settlement risk. A transient toncenter
failure just defers settlement to the next tick.

Known window limitation: each poll reads only the wallet's latest 50
transactions with no cursor or pagination. A backlog deeper than 50 inbound
transfers between two ticks leaves the older tail unsettled until the window
covers it again (see the comment in `worker/ton_polling.go`); at
orders-per-30s realities the window is ample.

### Memo identification is mandatory

A transfer settles only when its comment is exactly `order-<id>` — the
`ton://transfer` deeplink the bot shows prefills it, and the instructions
display it in a code block for manual payers. A payer who forgets or mangles
the memo sends money the bot can never match automatically: the transfer
never becomes a receipt on its own, never enters the ledger, and is visible
only on-chain. The operator resolution starts on-chain: open a tonviewer
link to the watched wallet (`https://tonviewer.com/<TON_WALLET_ADDRESS>`),
find the transfer, and identify the sender and the intended order (usually
from the buyer's support thread, corroborated by the amount and timing).
Then either refund from the wallet, or attach the transfer to the order
through the capture-ingress CLI. Preview first (read-only):

```bash
telegram-shop-bot payment-review ingest-provider \
  --provider ton --order 42 --amount-minor 1500000000 --currency TON \
  --external-id '<lt>:<hash>' --occurred-at 1720000000 \
  --actor 'operator@example' --reason 'memo-less transfer, buyer identified'
```

Apply only after the preview reports `outcome=apply`:

```bash
telegram-shop-bot payment-review ingest-provider \
  --provider ton --order 42 --amount-minor 1500000000 --currency TON \
  --external-id '<lt>:<hash>' --occurred-at 1720000000 \
  --actor 'operator@example' --reason 'memo-less transfer, buyer identified' \
  --apply --confirm-order 42
```

`--amount-minor` is the ACTUAL received nanoton (scale 9) and
`--occurred-at` accepts unix seconds or RFC3339. The same overpay-tolerant
`>=` rule as the polling worker applies, now enforced by the storage fact
gate itself: an underpay is rejected at the preview with an actionable
amount-mismatch error (exit `1`, nothing written) instead of being recorded
— durable review evidence for underpaid transfers remains the polling
worker's `receipt_mismatch` quarantine; an exact re-run is a replay no-op.
A settle through this CLI is identical to a polling tick for the ledger and
stock, but the bot-runtime side effects (buyer notification, loyalty,
referral) do not fire — confirm with the buyer in their thread. The same
subcommand serves the other payerless rails with their rail currency and
exact frozen order amount (`yookassa` RUB, `stripe` USD, `nowpayments` USD).
`stars` keeps its authenticated `ingest-stars` flow; `balance` is rejected
— its captures are synthetic admin-panel facts, never provider statements.

### Overpay-tolerant settlement, rate snapshot

`orders.total_ton_nano` is frozen at order creation from `USD_PER_TON`.
Changing the environment variable later changes only future orders; it never
reprices existing ones, and a zero snapshot (TON disabled when the order was
created) never settles. Settlement is overpay-tolerant: a transfer of at
least the frozen snapshot settles and the ledger records the ACTUAL received
nanoton amount. An underpay quarantines as `receipt_mismatch` with the actual
facts preserved.

### What quarantined TON facts look like

Quarantined facts are `payment_anomalies` rows with provider `ton` and the
order in `needs_review`:

| Reason | Meaning |
|---|---|
| `receipt_mismatch` | Underpay below the frozen snapshot (or a zero snapshot) |
| `unknown_order` | Memo names an order that does not exist |
| `second_charge` | A distinct second transfer for an already settled order |
| `out_of_stock_after_charge` | Paid transfer whose product went out of stock before fulfillment; durable — never retried |

TON facts carry no payer id (on-chain transfers have no Telegram payer
identity), so payer checks compare money and order linkage only.

### Resolve flow

```bash
make payment-review PROVIDER=ton
```

The semantics are identical to the flows above: the list exits `1` while
targets exist and prints local ids and reason codes only; resolve previews
read-only first and then applies with `--apply --confirm-order N`. A
quarantined capture still requires a durable succeeded refund before it can
be resolved to `settled`.

### Refunds

Refunds are operator-driven: send them from the watched wallet. Nothing in
this bot refunds automatically. The ledger records a refund through its
refund ingestion path (`RecordRefund` / `IngestProviderRefund`), which
validates it against the immutable captured attempt (exact parent identity,
money tuple, cumulative amount) and appends durable review evidence for
anything that disagrees. There is no dedicated refund-ingress CLI for TON.

### Subscriptions

Subscriptions remain Stars-only. The TON button is never offered for
subscription carts.

## 8. NOWPayments (USD crypto invoice) operations

### Security model: signed IPN, no refetch

NOWPayments IPN callbacks are signed. Every `/nowpayments-webhook` request
must carry an `x-nowpayments-sig` header whose HMAC-SHA512 over the
canonicalized body verifies against `NOWPAYMENTS_IPN_SECRET`. The canonical
form is: JSON object keys sorted recursively, compact separators, no HTML
escaping, no trailing newline. Once the signature verifies, the body itself
is authoritative: settlement happens from the verified body WITHOUT any API
refetch (the Stripe pattern, unlike the unsigned YooKassa flow). An invalid
signature is unauthenticated junk: the endpoint answers `403` and records
nothing — no anomaly, no event, no order change.

Canonicalization interop note: byte-level agreement between this Go
canonicalization and NOWPayments' PHP-side signer is verified fail-closed — a
mismatch rejects genuine IPNs, it can never accept a forged one — but it
MUST be confirmed with ONE live test payment before enabling the rail in
production.

### Finished-only settlement

Only an IPN with `payment_status` `finished` can settle. The other statuses
(`waiting`, `confirming`, `confirmed`, `sending`, `partially_paid`, `failed`,
`refunded`, `expired`) are normal lifecycle noise: the endpoint acknowledges
them and settles and records nothing.

### What quarantined NOWPayments facts look like

Quarantined facts are `payment_anomalies` rows with provider `nowpayments`
and the order in `needs_review`:

| Reason | Meaning |
|---|---|
| `webhook_parse_failure` | Signature-valid body that still fails to parse, stored as a sha256 digest only |
| `webhook_invalid_receipt` | Signed finished IPN that cannot produce a valid receipt |
| `receipt_mismatch` | Valid receipt that disagrees with the order's money tuple |
| `out_of_stock_after_charge` | Paid invoice whose product went out of stock before fulfillment |

NOWPayments facts carry no payer id (the provider has no Telegram payer
identity), so payer checks compare money and order linkage only.

### Resolve flow

```bash
make payment-review PROVIDER=nowpayments
```

The semantics are identical to the flows above: the list exits `1` while
targets exist and prints local ids and reason codes only; resolve previews
read-only first and then applies with `--apply --confirm-order N`. A
quarantined capture still requires a durable succeeded refund before it can
be resolved to `settled`.

### Refunds

Refunds are operator-driven: initiate them in the NOWPayments dashboard.
Nothing in this bot refunds automatically. The ledger records a refund
through its refund ingestion path (`RecordRefund` / `IngestProviderRefund`),
which validates it against the immutable captured attempt (exact parent
identity, money tuple, cumulative amount) and appends durable review evidence
for anything that disagrees. There is no dedicated refund-ingress CLI for
NOWPayments: `payment-review ingest-stars` reads the Telegram Bot API and
cannot serve this rail.

### USD-priced amounts

NOWPayments invoices charge the order's own USD total: there is no rate
snapshot and no converted currency, so `orders.total_usd` IS the charged fact
that receipts are validated against. The signed IPN echoes our own invoice,
so the USD cents match exactly — unlike TON there is no overpay tolerance.

### Subscriptions

Subscriptions remain Stars-only. The NOWPayments button is never offered for
subscription carts.

## 9. Review queue in the bot (`/payreview`)

Admins (their Telegram IDs in `ADMIN_IDS`) can triage the same review queue
without leaving Telegram. `/payreview` lists every case across all provider
buckets — one line (`#<order> | <provider> | <state> | targets=<n> |
<reasons>`) and one card button per case. The bot aggregates the queue itself:
the ledger's `ListPaymentReviews` has no cross-provider wildcard, so the bot
loops the seven provider buckets (`stars`, `crypto`, `yookassa`, `stripe`,
`ton`, `nowpayments`, `unknown`).

A case card shows the order summary, the payment state, and every target with
its kind and reason code. Each action (Settle / Refund / Dismiss) is a
**two-tap flow**: the first tap reloads the case, rebuilds the resolution from
the CURRENT target set and previews it read-only; the confirm tap reloads,
rebuilds and re-validates against the ledger before applying.
A case that changed between preview and confirm fails closed with a conflict
message, and resolutions are recorded with actor `admin:<telegram-id>`. Orphan
facts that share a provider-proposed order id are addressed independently —
their callbacks carry the anomaly target id as a disambiguator.

Use the bot for quick triage of the cases the ledger can derive to a terminal
state on its own:

- **settle** — a paid/delivered order whose quarantined captures are all fully
  compensated by durable succeeded refunds returns to `settled`;
- **refund** — a paid/delivered order whose entire quarantined capture was
  refunded closes as `refunded`;
- **dismiss** — evidence-derived cancellation, and the provider-neutral
  `unknown` inbox (always `dismissed` + `cancelled`, never revenue);
- **orphan facts** (no local order) — settle acknowledges them as
  `compensated`, refund as `accepted_refund`, dismiss follows the ledger
  evidence; a detached provider order id can only be cancelled.

Keep using the CLI (sections 2–4) for what the bot deliberately cannot
express — every bot action names a terminal state, so these stay CLI-only:

- **cross-provider orders**: while another provider still holds unresolved
  targets for the same order, the only valid outcome is the partial
  `needs_review` — the bot's preview rejects every terminal action with the
  conflict message; resolve one provider per CLI command with
  `--state needs_review`;
- **refunded-kind anomalies with no durable refund row**: their only valid
  shape is `accepted_refund` + `needs_review` (the order stays quarantined
  until ingress binds the refund to its parent capture);
- any case whose preview the bot rejected: the CLI prints the exact target
  ids to investigate.

## 10. Balance (internal rail)

Buyers with a positive USD balance see a balance button at checkout
(non-subscription carts only; subscriptions remain Stars-only). The tap
settles the order **synchronously** — there is no provider round-trip: the
service debits the buyer's balance and commits a synthetic payment fact
(provider `balance`, external id `balance:<orderID>`, the buyer as the
required positive payer, USD cents at scale 2) through the same guarded
settlement path as the external rails.

The debit is idempotent across crash windows. The service composes the
balance store's own transaction with the settlement store's own transaction
(debit-first + compensating credit — the house helpers own no cross-store
transaction), and before debiting it reads the order's net balance effect —
the sum over its `order_payment:<id>` and `settlement_failed:<id>` rows in
`balance_txs`: net < 0 means a prior orphan debit already covers the order
and the debit is skipped; net ≥ 0 means no live debit (or a compensated one —
the money was returned, so the re-tap debits again). Each orphan debit deepens
the net-negative, so N crash loops still settle exactly once. Any settle
failure is answered with an exact compensating credit typed
`settlement_failed:<id>` — but only a debit made by that same call is
compensated; crediting a skipped orphan would mint money.

**Operator adjustments**: `/setbalance <user_id> <±amount> [reason...]`
(admins only, by Telegram user id) credits or debits a balance atomically —
the funds check and the mutation are one `UPDATE`, so a debit can never
overdraw — and appends a `balance_txs` audit row (type
`admin_adjust[: reason]`, `ref_id` = the acting admin's Telegram id). There is
no confirmation step: the audit trail is the mitigation.

**Crash-window residual**: a crash between the debit and the settle leaves the
order pending with an orphan `order_payment:<id>` debit. The buyer's re-tap
settles on that orphan without a second debit, so the residual an operator can
observe is only a still-pending order whose buyer never re-tapped. Reconcile
by inspecting `balance_txs` for `order_payment:<id>` rows without a matching
settled payment attempt; if the purchase should not complete, return the money
with `/setbalance <user_id> +<amount> reconcile order <id>`.

The balance rail never enters the review queue: a balance fact is validated
against the order's exact USD snapshot and required payer before settlement,
so there is no provider ambiguity to quarantine — ingress accepts or rejects
it outright.

## Exit codes

| Code | Meaning |
|---:|---|
| `0` | Green comparison, successful preview, or fully resolved apply |
| `1` | Review still required, bounded window incomplete, or provider/DB failure |
| `2` | Invalid CLI arguments or a missing confirmation gate |
