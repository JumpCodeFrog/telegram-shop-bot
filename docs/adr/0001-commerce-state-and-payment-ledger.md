# ADR 0001: независимые commerce-состояния и payment ledger

- Статус: accepted for local Wave 1 patch
- Дата: 2026-08-27

## Контекст

Один legacy `orders.status` смешивал размещение заказа, оплату и исполнение.
Идемпотентность была привязана к заказу, а не к provider charge; refund и
сверка Stars отсутствовали.

## Решение

1. Legacy `status`, `payment_method`, `payment_id` остаются совместимой
   проекцией для текущего UI и интеграций.
2. Три состояния хранятся отдельно:
   - `order_state`: `placed | cancelled | completed`;
   - `payment_state`: `pending | settled | partially_refunded | refunded |
     cancelled | needs_review`;
   - `fulfillment_state`: `unfulfilled | fulfilled`.
3. `order_events`, `payment_events` — append-only timeline. Порядок задаёт `id`,
   потому что SQLite timestamps имеют секундную точность.
4. Денежные суммы ledger — целые `amount_minor + currency + scale`:
   Stars = `XTR/0`, Crypto checkout = `USD/2`.
5. Захват оплаты, legacy projection, новые состояния, stock, promo и timeline
   коммитятся одной SQLite-транзакцией.
6. Provider event identity — `(provider, event_kind, external_id)`: Telegram
   refund может использовать ID исходной Stars-транзакции.
7. Refund меняет только payment state. Restock и reversal бонусов не выводятся
   автоматически из денежного события.
8. `reconcile-stars` читает bounded окно Telegram, сравнивает агрегаты и ничего
   не списывает, не возвращает и не повторяет автоматически.
9. `payment-review` показывает только local ID/reason codes. Resolution
   получает точный target set, сначала строит preview, а затем
   требует `--apply --confirm-order`. Решения append-only.
10. Needs-review capture можно закрыть только после полного durable
    refund. Итоговый `payment_state` выводится из ledger и legacy
    status, а не из свободного решения оператора.
11. Provider-only Stars row можно внести только после точного
    authenticated lookup в полном bounded окне. Capture всегда идет в
    quarantine без stock/fulfillment/loyalty side effects.

## Переходы

| Legacy action | order | payment | fulfillment | Event |
|---|---|---|---|---|
| create | placed | pending | unfulfilled | `order.created` |
| pay | placed | settled | unfulfilled | `payment.settled` |
| deliver | completed | settled | fulfilled | `fulfillment.fulfilled` |
| cancel pending | cancelled | cancelled | unfulfilled | `order.cancelled` |
| partial refund | unchanged | partially_refunded | unchanged | `payment.refunded` |
| full refund | unchanged | refunded | unchanged | `payment.refunded` |
| identity conflict | unchanged | needs_review | unchanged | `identity_conflict` |

## Failure semantics

- Exact replay: no second stock/promo mutation.
- Charge identity reused by another order: second order stays unpaid and gets
  `needs_review` evidence.
- Receipt mismatch: no domain mutation.
- Provider ambiguity: manual reconciliation; no blind retry.
- Post-commit loyalty, notifications and outbound webhook remain a documented
  crash gap until the explicit Wave 2 outbox.
- Компенсация не запускается resolution-командой: refund должен
  уже быть подтверждён провайдером и записан в ledger.

## Вне Wave 1

Outbox/job queue, scheduled retries, automatic compensation, reservations,
variants, new admin frontend и multitenancy.
