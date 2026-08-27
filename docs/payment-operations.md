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
telegram-shop-bot payment-review list --provider unknown
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

## Exit codes

| Code | Meaning |
|---:|---|
| `0` | Green comparison, successful preview, or fully resolved apply |
| `1` | Review still required, bounded window incomplete, or provider/DB failure |
| `2` | Invalid CLI arguments or a missing confirmation gate |
