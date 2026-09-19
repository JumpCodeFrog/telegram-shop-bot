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

### What quarantined YooKassa facts look like

Quarantined facts are `payment_anomalies` rows with provider `yookassa` and the
order in `needs_review`:

| Reason | Meaning |
|---|---|
| `webhook_parse_failure` | Unparseable webhook body, stored as a sha256 digest only |
| `webhook_invalid_receipt` | Refetched payment cannot produce a valid receipt |
| `receipt_mismatch` | Valid receipt that disagrees with the order's money tuple |

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

## Exit codes

| Code | Meaning |
|---:|---|
| `0` | Green comparison, successful preview, or fully resolved apply |
| `1` | Review still required, bounded window incomplete, or provider/DB failure |
| `2` | Invalid CLI arguments or a missing confirmation gate |
