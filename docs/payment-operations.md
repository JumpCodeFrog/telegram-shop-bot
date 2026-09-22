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
telegram-shop-bot payment-review list --provider balance
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

For an admin-initiated refund, prefer the bot's `/refund` command (§11) —
Stars refunds are full-amount only. The CLI above remains the path for
provider-only refunds and for the one `/refund` failure it can recover: a
Stars refund that EXECUTED at Telegram but whose ledger write failed can never
be recorded by a `/refund` re-run (Telegram rejects the repeat
`refundStarPayment`) — `ingest-stars --kind refund` records it from Telegram's
authoritative refund transaction instead.

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

Quarantined YooKassa facts are recorded through two mechanisms, keyed by reason
rather than by ingress path. The reasons tabulated below are `payment_anomalies`
rows with provider `yookassa`; a fact tied to a known order flips it to
`needs_review` (the two digest-only reasons carry no order identity — see their
sha256 notes). The capture-class exceptions, including `out_of_stock_after_charge`
on the poller path, are described after the table:

| Reason | Meaning |
|---|---|
| `webhook_parse_failure` | Unparseable webhook body, stored as a sha256 digest only |
| `webhook_missing_payment_id` | Envelope that parsed cleanly but carried no payment id, stored as a sha256 digest only |
| `webhook_invalid_receipt` | Refetched payment cannot produce a valid receipt |
| `receipt_mismatch` | Valid receipt that disagrees with the order's money tuple |
| `out_of_stock_after_charge` | Paid payment whose product went out of stock before fulfillment |

Not every quarantined fact is an anomaly row. `out_of_stock_after_charge` is
path-dependent: the WEBHOOK writes the anomaly row tabulated above, but the
backup POLLER records it via `RecordUnexpectedPayment` as a needs_review attempt
+ captured/needs_review event (no anomaly row). Separately, the shared
`ConfirmPaymentReceipt` gate — reached by BOTH the webhook and the poller —
yields capture-class quarantines via `RecordUnexpectedPayment` (never anomaly
rows): `second_charge` and `capture_after_terminal_state` (a distinct re-charge
of an order already settled or in a terminal state) and `capture_on_unresolved_order`.
Every mechanism surfaces as a review-queue target — an anomaly target, or a
needs_review event target for the capture class.

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

Refunds are operator-driven — nothing in this bot refunds automatically, and
the webhook acknowledges `refund.*` notifications without touching the ledger.
The primary path is the bot's `/refund <order_id> [amount]` admin command
(§11): it previews, then on confirm calls `POST /v3/refunds` with the captured
payment id and a deterministic `Idempotence-Key`, and records the refund
through the ledger's ingestion path (`IngestProviderRefund`), which validates
it against the immutable captured attempt (exact parent identity, money tuple,
cumulative amount) and appends durable review evidence for anything that
disagrees. A `pending` YooKassa refund is recorded as the initiation — its
asynchronous completion is dashboard-visible. Refunds issued directly in the
YooKassa dashboard remain possible but have NO ledger recording path: there is
no dedicated refund-ingress CLI for YooKassa yet (`payment-review ingest-stars`
reads the Telegram Bot API and cannot serve this rail, and `ingest-provider`
is capture-only) — prefer `/refund` so the ledger stays authoritative.

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

Refunds are operator-driven — nothing in this bot refunds automatically. The
primary path is the bot's `/refund <order_id> [amount]` admin command (§11):
it previews, then on confirm resolves the checkout session's `payment_intent`,
calls `POST /v1/refunds` with a deterministic `Idempotency-Key`, and records
the refund through the ledger's ingestion path (`IngestProviderRefund`), which
validates it against the immutable captured attempt (exact parent identity,
money tuple, cumulative amount) and appends durable review evidence for
anything that disagrees. A refund Stripe reports as `failed` aborts before any
recording — the operator sees the provider fact. Refunds issued directly in
the Stripe dashboard remain possible but have NO ledger recording path: there
is no dedicated refund-ingress CLI for Stripe yet (`payment-review
ingest-stars` reads the Telegram Bot API and cannot serve this rail, and
`ingest-provider` is capture-only) — prefer `/refund` so the ledger stays
authoritative.

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

Quarantined TON facts reach the review queue through two mechanisms — the
poller is TON's only settlement path, and the order flips to `needs_review`
either way. `receipt_mismatch` and `unknown_order` are written as
`payment_anomalies` rows; `second_charge` and `out_of_stock_after_charge` are
recorded via `RecordUnexpectedPayment` as a needs_review attempt/event capture
(no anomaly row). Both surface as review-queue targets:

| Reason | Meaning |
|---|---|
| `receipt_mismatch` | Underpay below the frozen snapshot (or a zero snapshot) |
| `unknown_order` | Memo names an order that does not exist |
| `second_charge` | A distinct second transfer for an already settled order; recorded as a needs_review capture, not an anomaly row (poller path) |
| `out_of_stock_after_charge` | Paid transfer whose product went out of stock before fulfillment; durable — never retried; recorded as a needs_review capture, not an anomaly row (poller path) |

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

Refunds are operator-driven: send them from the watched wallet — the bot
cannot move money on this rail, and nothing in this bot refunds automatically.
`/refund` for a TON order renders an informational card only (wallet
instructions, no execution, no confirm button). The ledger records a refund
through its refund ingestion path (`RecordRefund` / `IngestProviderRefund`),
which validates it against the immutable captured attempt (exact parent
identity, money tuple, cumulative amount) and appends durable review evidence
for anything that disagrees — but no refund RECORDING path exists for TON
yet: there is no dedicated refund-ingress CLI (`payment-review ingest-stars`
reads the Telegram Bot API and cannot serve this rail, and `ingest-provider`
is capture-only). A wallet refund therefore stays an on-chain fact visible
only in the wallet / chain explorer, the order's ledger payment state remains
`settled`, and closing that gap is a documented follow-up.

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

Refunds are operator-driven: initiate them in the NOWPayments dashboard — the
bot cannot move money on this rail, and nothing in this bot refunds
automatically. `/refund` for a NOWPayments order renders an informational card
only (dashboard instructions, no execution, no confirm button). The ledger
records a refund through its refund ingestion path (`RecordRefund` /
`IngestProviderRefund`), which validates it against the immutable captured
attempt (exact parent identity, money tuple, cumulative amount) and appends
durable review evidence for anything that disagrees — but no refund RECORDING
path exists for NOWPayments yet: there is no dedicated refund-ingress CLI
(`payment-review ingest-stars` reads the Telegram Bot API and cannot serve
this rail, and `ingest-provider` is capture-only). A dashboard refund
therefore stays visible at the provider only, the order's ledger payment state
remains `settled`, and closing that gap is a documented follow-up.

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
loops the eight provider buckets (`stars`, `crypto`, `yookassa`, `stripe`,
`ton`, `nowpayments`, `balance`, `unknown`).

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

The balance rail never enters the review queue through settlement ingress: a
balance fact is validated against the order's exact USD snapshot and required
payer before settlement, so there is no provider ambiguity to quarantine —
settlement ingress accepts or rejects it outright. The balance review bucket
(§2, §9) exists for one exception only: the path-5 refund orphans of §11, a
refund credit that succeeded while its ledger record failed.

**Refunds**: `/refund <order_id>` (§11) for a balance-paid order credits the
buyer's balance back — the credit IS this rail's provider step (there is no
external provider). The deterministic `order_refund:<orderID>` `balance_txs`
audit type doubles as the idempotency identity: after a ledger-recording
failure the `/refund` re-run finds the prior credit and skips it, so the money
is minted exactly once (mirroring the crash-window net check the debit side
uses above). Confirm executions are serialized process-wide, so a double tap
delivered as two concurrent updates cannot race two credits. Note the
identity's coupling to the one-refund-per-order gate — see §11 before ever
relaxing that gate.

## 11. Admin refunds from the bot (`/refund`)

`/refund <order_id> [amount]` (admins only) is the bot's money-out surface.
Refunds are admin-initiated ONLY: no webhook, worker, or order event refunds
anything automatically. The amount is optional and denominated in the rail's
currency (USD, RUB, XTR); omitting it refunds the order in full. An amount
above the captured total is rejected before any provider call. To resolve a
QUARANTINED case whose capture was already refunded elsewhere, keep using
`/payreview` (§9) — `/refund` executes new money movement, it does not triage.

### Two-tap flow

The command renders a preview card (order, rail, amount) with a confirm
button. The confirm tap RELOADs the order, REBUILDS the refund fact from
scratch and RE-PREVIEWs it against the ledger (`PreviewProviderRefundIngress`
dry-run: parent identity, payer, money tuple, cumulative cap) before executing
— the same TOCTOU discipline as `/payreview`. Anything that drifted between
the taps fails closed with a conflict message and zero provider calls. A
double confirm is a replay: the recorded refund is found first (before the
refundable gate) and answered with the done message — the provider is never
called twice. Confirm executions are serialized process-wide, so concurrent
taps cannot race. Every recorded refund is audited in the same ledger
transaction (`payment_ingress_audits`, actor `admin:<telegram-id>`, reason
`admin /refund`).

### Executable rails

| Rail | Execution | Amounts | Notes |
|---|---|---|---|
| `stars` | `refundStarPayment` (Telegram) | full only | Telegram has no partial star refund; an explicit amount must equal the frozen XTR total. The ledger identity is the capture's charge id |
| `stripe` | `POST /v1/refunds` against the checkout session's `payment_intent` | full or partial | a refund whose Stripe status is `failed` aborts before recording |
| `yookassa` | `POST /v3/refunds` against the captured payment id | full or partial | status `canceled` aborts before recording; a `pending` refund is recorded as the initiation — async completion is dashboard-visible |
| `balance` | credit back via `AdjustBalance` | full or partial | the credit IS the provider step; see §10 |

### Manual rails (crypto, ton, nowpayments)

These rails have no refund API in this bot: `/refund` renders an
informational card (issue the refund at the provider's dashboard or from the
watched wallet) and executes nothing — no provider call, no write, no confirm
button, on either tap. In-ledger recording for these rails is a KNOWN
FOLLOW-UP: the refund-recording CLI (`payment-review ingest-stars --kind
refund`, §3) authenticates against Telegram's star transactions and therefore
covers Stars only, and `payment-review ingest-provider` is capture-only. Until
a recording CLI exists, a manual refund stays visible at the provider and the
order's payment state remains `settled`.

### Ordering ruling: provider first, ledger second

Money-out is the irreversible step, so the provider refund executes FIRST and
the immutable ledger record (`IngestProviderRefund`) SECOND. A provider
failure leaves the ledger untouched and the order unchanged. A ledger failure
AFTER a provider success is recoverable — the inverse order could record
money that never left.

### Deterministic idempotency keys

Every card-rail provider call carries the deterministic key
`refund:<orderID>:<amountMinor>:<paymentID>` (stripe `Idempotency-Key` /
yookassa `Idempotence-Key`), and the balance rail's `order_refund:<orderID>`
`balance_txs` audit type is the equivalent identity. A re-run after a
ledger-recording failure is collapsed into the original money movement, so the
recovery is a money-safe no-op that only completes the record. The deliberate
trade-off: two legitimate identical partial refunds of one order are blocked
provider-side — on a money-out surface, blocking an exotic legitimate repeat
beats ever risking a double payout.

### Ledger-failure recovery is rail-aware

The failure message (loud in chat, ERROR in logs with the refund id) names the
remedy per rail:

- `stripe` / `yookassa` / `balance` — re-run `/refund <order_id> <amount>`:
  the dedup above prevents a second money movement, and the re-run only
  completes the ledger record. Re-run with the SAME amount — an amount-less
  re-run defaults to the full total, which is a different refund (the card
  rails' providers reject it once a partial moved money, but the re-run then
  completes nothing; the balance rail's divergence guard fails it closed
  before any write — see the coupling note below);
- `stars` — do NOT re-run `/refund`: Telegram rejects the repeat
  `refundStarPayment`, so a re-run can never record it. Record the refund with
  the Stars CLI instead (§3): `payment-review ingest-stars --kind refund
  --transaction <telegram-refund-id> --order N --actor … --reason … --apply
  --confirm-order N` reads Telegram's authoritative refund transaction and
  records it with the provider's own timestamp.

The failure window also leaves a durable trace: the moment a provider refund
succeeds but the ledger record fails, a best-effort ORPHAN
`payment_anomalies` row is written with the reason
`refund_ledger_failure:order=<id>` in the rail's provider bucket (balance
included, so the card is visible in `/payreview` (§9) and `payment-review`).
It is deliberately an orphan (no proposed order): the order stays `settled`
so the re-run remedy quoted above still works — a `needs_review` quarantine
would fail the refundable gate and close that path. The write is best-effort:
when it also fails (the database is likely what just broke), the log + chat
message above remain the full trace and nothing else changes. The row's
`raw_payload` is deterministic (order id, rail, refund id — no error text),
so repeated failures reuse one row.

**Pre-recovery trap**: acting on the card's Refund action before the recovery
above has completed the ledger record resolves the card without recording a
`refunds` row — Refund is the only decision that passes on a refund orphan
pre-recovery, and it acknowledges a refund the books never recorded. A
resolved card never re-surfaces: a repeated path-5 failure reuses the
resolved row, so the durable trace is silently consumed. Resolve the card
only after the recovery completes the record — post-recovery the refund row
exists, Refund fails closed, and Dismiss is the passing action.

After the re-run or the Stars CLI recovery completes the record, resolve the
card via `payment-review resolve` (§4; bot-side orphan-card ergonomics are a
known follow-up — HANDOFF §6.9).

### One bot-side refund per order

The gate requires a `paid` or `delivered` order whose payment state is
`settled`. The first recorded refund flips that state — `refunded` when the
cumulative refunds equal the captured total, `partially_refunded` otherwise
(ledger-derived) — which closes the bot path for that order. A
partial-then-remainder refund therefore has NO bot path today: after a
partial `/refund`, the remainder needs CLI/ledger tooling. This is a
documented limitation of the settled-only gate, not an oversight.

### Cumulative cap is ledger-enforced

Both the preview dry-run and the ingest sum every succeeded refund for the
capture and refuse to exceed it: an over-cap preview fails closed as a
conflict message before any provider call, and an over-cap ingest quarantines
as a `refund_exceeds_payment` anomaly instead of recording.

### No restock

A refund updates the payment projection only: stock, fulfillment, loyalty,
referral and entitlement side effects are NOT reversed by it — fulfillment
after a refund stays a manual, explicit policy decision. The one quarantine
case is a fully refunded subscription entitlement without provenance: the
refund records, and the order goes to `needs_review` for §9/CLI triage.

### Balance gate coupling (load-bearing)

The balance rail's idempotency identity `order_refund:<orderID>` is
ORDER-scoped, which is sound only while the settled-only gate admits exactly
one bot refund per order. The probe therefore returns the prior credit's
net amount as well as its existence (`BalanceTxTotal`), and a re-run whose
amount diverges from that credit FAILS CLOSED before any write: nothing is
credited, nothing is recorded, and the error names both amounts. Recovery is
the exact-amount re-run (the failure message quotes the prior credit) or
`/payreview` (§9) resolution. If the settled-only gate is ever relaxed to
accept `partially_refunded` orders (a remainder refund), the identity MUST
become amount-scoped FIRST (e.g. `order_refund:<orderID>:<amountMinor>`) —
otherwise a legit second partial dies on the divergence guard with no bot
path. The coupling is commented at both ends: the balance branch of
`executeRefund` (`internal/bot/admin_refunds.go`) and `BalanceTxTotal`
(`internal/storage/balance.go`).

## 12. Operation attribution

Every money-moving operation has a named actor. How the actor is recorded
depends on the path:

| Path | Actor | Durable record |
|---|---|---|
| Provider webhooks (crypto, yookassa, stripe, nowpayments) | `webhook:<provider>` | settlement-success log line only (structured `actor` field next to `order_id` and the provider payment id) |
| Stars `successful_payment` settlement (Telegram is the provider; the update arrives via webhook or long polling) | `webhook:stars` | settlement-success log line only |
| Stars subscription renewal (recurring `successful_payment`, `is_recurring && !is_first_recurring`) | `webhook:stars` | settlement-success log line only |
| Polling workers (crypto, ton, yookassa) | `worker:<provider>` | settle-success log line only |
| CLI ingress (`payment-review ingest-stars` / `ingest-provider` / `resolve`) | the `--actor` flag value | durable `payment_ingress_audits` row |
| Bot `/refund` executions and `/payreview` resolutions | `admin:<telegram_id>` | durable `payment_ingress_audits` row (refunds additionally log the same actor) |
| Balance adjustments (`/setbalance`) | the acting admin's Telegram id | durable `balance_txs` row (`admin_adjust[: reason]` type, admin id in `ref_id`) |

For webhook and worker settlements the provider fact itself is the authority —
a verified/refetched provider statement (or an on-chain transfer) caused the
settle, not a person — so attribution is log-level: the settlement-success
line carries a structured `actor` field. These paths have **no durable actor
row**: the immutable ledger tables carry no actor column, and adding one was
assessed as disproportionate for the low operator impact — it remains a
documented backlog item (roadmap 4.15). Operator-driven paths (CLI ingress,
bot refunds, review resolutions, balance adjustments) all write durable audit
rows naming the actor.

### Update tracing (roadmap 4.14)

Every Telegram update is processed under a single per-update context derived
at ingress (30s budget, cancelled with the process root). It carries a
per-update `trace_id` (16 hex chars, crypto/rand; new id per processing
attempt — redeliveries of one `update_id` get fresh traces). Operator-facing
correlation: `LoggingMiddleware` ("incoming update") and `RecoverMiddleware`
panic logs always include `trace_id` and `update_id`; the Stars
settle/renewal/quarantine logs and the payment-barrier error logs include
`trace_id`. To reconstruct one update's path: grep the bot log for the trace
id (`trace_id=<id>` with the text log handler, `"trace_id":"<id>"` with
JSON). Provider-webhook and worker settles are not part of the update chain
— their attribution remains the `actor=` field (§12 above).

## Exit codes

| Code | Meaning |
|---:|---|
| `0` | Green comparison, successful preview, or fully resolved apply |
| `1` | Review still required, bounded window incomplete, or provider/DB failure |
| `2` | Invalid CLI arguments or a missing confirmation gate |
