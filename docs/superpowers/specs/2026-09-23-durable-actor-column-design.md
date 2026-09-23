# Design Spec — Roadmap 4.15: durable actor column in payment ledger tables

> **Status:** design COMPLETE 23.09.2026 (autonomous session; owner's standing
> directive «фулл автономно, rulings-not-stalls» covers the review gates —
> all decisions recorded as rulings D1–D5). Next step: `superpowers:writing-plans`.
> **Author:** controller session 23.09.2026 (after 4.14 merge `c67e66e`).
> **Depends on:** 4.14 ✅ (ctx/trace plumbing — NOT consumed directly; actor flows
> explicitly, see D2-rationale). Branch name suggestion: `feat/durable-actor-column`.

---

## 1. Goal / Non-goals

**Goal (roadmap 4.15, verbatim):** «Durable actor-колонка в immutable ledger-таблицах
для webhook/worker settles (остаток 4.13: сейчас — только log-атрибуция
`actor=webhook:<provider>` / `actor=worker:<provider>`, docs §12; CLI и `/refund` уже
пишут durable `payment_ingress_audits`)».

Concretely: every NEW row in `payment_events` and `payment_anomalies` carries the
identity of the ingress that wrote it (`webhook:<provider>`, `worker:<provider>`,
`admin:<tgID>`, CLI `--actor` text) in a nullable `actor` column; operators see it in
`/payreview` cards and `payment-review list`; docs §12's «log-only» caveat disappears.

**Non-goals:**
- NO backfill: pre-4.15 rows keep `actor = NULL` («not recorded») — the ledger is
  append-only/immutable; history is not rewritten.
- NO `trace_id` column (**D4**): trace ids are ephemeral per-attempt correlation and
  live in logs (4.14); the ledger stores durable facts only.
- `payment_attempts` untouched (request-side, not a provider-fact ledger);
  `payment_ingress_audits` untouched (already has `actor` since 017).
- Balance-rail captures get `NULL` actor (**D2**): the rail has no webhook/worker
  ingress — attribution is self-evident from `order.user_id`. In scope are exactly the
  rows written by webhook/worker/CLI/admin-refund/payreview-apply paths.
- No change to money semantics, idempotency keys, or review-queue logic.

## 2. Current architecture (verified at `51c20ce`, file:line map)

**Schema (migration 017 + provider rebuilds 020/021):**
- `payment_events` (`017_commerce_ledger.sql:162`): immutable fact rows
  (`order_id, payment_attempt_id, provider, event_kind(captured|refunded|chargeback|
  identity_conflict), external_id, amount_minor, currency, scale, disposition
  (observed|settled|needs_review), occurred_at, created_at`,
  `UNIQUE(provider, event_kind, external_id)`). NO actor column.
- `payment_anomalies` (`017:182`): quarantine rows (`fingerprint, proposed_order_id,
  provider, event_kind, external_id, related_external_id, payer_id, amount_minor,
  currency, scale, raw_amount, raw_payload, reason, occurred_at`,
  `UNIQUE(provider, fingerprint)`). NO actor column.
- `payment_ingress_audits` (`017:240`): HAS `actor TEXT CHECK (length(actor) BETWEEN
  1 AND 128)` — the precedent for the CHECK shape (**D1**).
- Migrations: embedded FS (`internal/storage/db.go:17`), applied in filename order,
  tracked in `schema_migrations`; next free filename `023_payment_actor.sql`.

**INSERT sites (exhaustive, verified by grep):**
1. `internal/storage/payment_recording.go:383` — capture settle INSERT
   (the `UpdateOrderStatusWithPaymentFact` path — ALL 8 settle callers + CLI ingest-apply).
2. `internal/storage/payment_recording.go:568` and `:596` — `INSERT OR IGNORE`
   replay/reconcile variants on the same PaymentFact flow.
3. `internal/storage/ledger.go:329` — refund event INSERT (refund fact flow;
   callers: bot `executeRefund` (`admin:<tgID>`), CLI ingest, payreview accepted_refund apply).
4. `internal/storage/order_ledger_helpers.go:101` — 'observed' capture INSERT
   (CLI ingest-observe paths).
5. `internal/storage/payment_anomalies.go:18` — `RecordPaymentAnomaly` INSERT
   (all quarantine writers).

**Producer sites (who knows the actor):**
- Captures — `shop.PaymentReceipt` (`internal/shop/order.go:53`) is built at exactly
  8 settle call sites: `webhook.go:88` (crypto), `:236` (yookassa), `:349` (stripe),
  `:464` (nowpayments); `handlers_payment.go:627` (stars barrier — BOTH polling and
  telegram-webhook transports log `webhook:stars` today, keep that literal);
  `worker/polling.go:224` (crypto→`worker:crypto`), `worker/ton_polling.go:96`
  (`worker:ton`), `worker/yookassa_polling.go:127` (`worker:yookassa`).
  Renewal: `shop.OrderService.RecordSubscriptionRenewal` (`order.go:492`) — same
  stars barrier, literal `webhook:stars`.
  Translation: `paymentFactFromReceipt` (`order.go:301`) is THE single
  receipt→`storage.PaymentFact` (`models.go:198`) translator (capture + renewal).
  Balance path builds its own fact at `shop/balance.go:103` — untouched (**D2**).
- Anomalies — `storage.PaymentAnomaly` (`models.go:226`) writers:
  `shop.OrderService.RecordPaymentAnomaly` (`order.go:482`, fed by `quarantineReceipt`
  → copy `receipt.Actor`); bot `recordStarsPaymentAnomaly` (stars helper — literal
  `webhook:stars`); telegram decode-digest quarantine (`webhook.go`
  `quarantineUndecodableStarsUpdate` — literal `webhook:stars`); CLI ingest
  (`--actor` flag).
- Refunds — refund fact flow into `ledger.go:329`: bot `executeRefund`
  (`admin_refunds.go` — literal `admin:<tgID>`, already used for its audit row and
  success log `:460`); CLI refund/ingest paths (`--actor`); payreview resolve-apply
  with `accepted_refund` (operator actor from the resolution's `--actor`).
- CLI direct writes: `internal/launcher/payment_ingress.go:268`
  (`UpdateOrderStatusWithPaymentFact` with flag-built fact → `fact.Actor = --actor`);
  CLI `RecordPaymentAnomaly` calls → `anomaly.Actor = --actor`.

**Surfaces (read-back):**
- `storage.PaymentEvent` model (`models.go:209`) — gains `Actor sql.NullString`.
- `ListPaymentReviews` (`payment_resolutions.go:14`): selects review targets from
  `payment_events` (needs_review, unresolved) + orphan `payment_anomalies` — add
  `actor` to both selects; `PaymentReviewTarget` (+ case view struct as needed)
  carries it.
- `/payreview` card: `admin_payreview.go` `sendPayReviewCard` (`:308`) — one extra
  line when actor present (2 new locale keys en/ru — NEW keys only, invariant-safe).
- CLI `payment-review list` (`internal/launcher/payment_review.go`): actor column/
  field in the row rendering when present (plain text, no locale).

## 3. Design Rulings (D1–D5 — the plan's Global Constraints inherit them)

- **D1 — schema:** `023_payment_actor.sql` adds to BOTH tables:
  `actor TEXT NULL CHECK (actor IS NULL OR length(actor) BETWEEN 1 AND 128)`.
  Two `ALTER TABLE … ADD COLUMN` statements. No backfill, no default.
  Rationale: the 4.13 gap covers settle events AND quarantine rows; one migration,
  same plumbing; events-only would re-create the gap for orphan rows.
- **D2 — flow & values:** actor travels EXPLICITLY in the existing fact envelopes:
  `PaymentReceipt.Actor` → `paymentFactFromReceipt` → `storage.PaymentFact.Actor`;
  `storage.PaymentAnomaly.Actor`; refund fact struct gains Actor set by its caller.
  NO ctx extraction (house style: explicit money-path APIs; rejected alternative from
  the 4.14 spec §9). Literals are EXACTLY the 4.13 log literals (`webhook:stars`,
  `webhook:crypto`, `webhook:yookassa`, `webhook:stripe`, `webhook:nowpayments`,
  `worker:crypto`, `worker:ton`, `worker:yookassa`, `admin:<tgID>`, CLI `--actor`).
  Stars barrier keeps `webhook:stars` for both transports (pre-existing attribution
  convention). Balance rail: untouched, NULL.
- **D3 — empty → NULL:** every INSERT binds actor via `NULLIF(?, '')` — missing
  attribution stores NULL and NEVER fails a settle/refund/quarantine write.
  Attribution must not gate money.
- **D4 — no trace_id column:** logs carry ephemeral correlation; the ledger carries
  durable attribution. Considered, rejected (YAGNI + immutability semantics).
- **D5 — surfaces:** actor shown only when non-empty (NULL rows render exactly as
  today); two NEW locale keys for the bot card; CLI list adds a plain field;
  docs §12 rewritten (durable actor becomes true for webhook/worker settles).

## 4. Data flow (end-to-end)

ingress (webhook handler / worker / stars barrier / CLI / refund / payreview-apply)
→ sets `<envelope>.Actor` to its 4.13 literal (or `--actor` / `admin:<tgID>`)
→ shop translation (`paymentFactFromReceipt` / direct struct assignment)
→ storage INSERT with `NULLIF(?, '')` bound to actor
→ read-back in `ListPaymentReviews` (+ `PaymentEvent` model)
→ `/payreview` card / CLI `payment-review list` render actor when present
→ docs §12 + CHANGELOG describe it.

Replay/idempotency: the actor is recomputed at the same site on every attempt
(deterministic per site), so `INSERT OR IGNORE` replays are consistent; UNIQUE
conflicts keep the FIRST row's actor (correct: the first writer won).

## 5. Task decomposition sketch (input to writing-plans; ~6 tasks)

- **T1 schema+storage core:** migration 023; `PaymentFact.Actor`,
  `PaymentAnomaly.Actor`, `PaymentEvent.Actor sql.NullString`; all 5 INSERT sites bind
  `NULLIF(?, '')`; migration test (fresh DB has column; upgraded DB: old rows NULL,
  new insert round-trips actor; CHECK rejects >128).
- **T2 capture producers:** `PaymentReceipt.Actor` + `paymentFactFromReceipt` copy;
  8 settle call sites set literals; renewal path; E2E pins (stars webhook settle →
  `payment_events.actor='webhook:stars'`; one worker settle → `worker:<provider>`).
- **T3 anomaly producers:** `PaymentAnomaly.Actor` + `RecordPaymentAnomaly` INSERT;
  quarantineReceipt copies receipt.Actor; stars helpers + decode-digest set literals;
  CLI ingest sets flag value; pins (webhook mismatch → anomaly actor).
- **T4 refund/CLI paths:** refund fact flow actor (bot `admin:<tgID>`, CLI `--actor`,
  payreview accepted_refund apply); CLI `payment_ingress.go:268` fact actor;
  observed-event path (order_ledger_helpers) actor param from CLI; pins.
- **T5 surfaces:** `ListPaymentReviews` + models read-back; `/payreview` card line
  (2 locale keys); CLI list field; rendering pins (present-when-set, absent-when-NULL).
- **T6 docs:** §12 rewrite (durable actor table: which rows carry which literal;
  NULL semantics), CHANGELOG Quality entry. Post-merge janitorial (controller):
  roadmap 4.15 ✅, HANDOFF §1/§8/§9.

Each task = one commit, own review. Money-path discipline on every INSERT diff
(reviewer named risk: column-list/order + binding order match).

## 6. Verification strategy

- Gates per task: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/`
  (EMPTY) `&& go test ./...` (storage ~100s; timeout ≥600000ms).
- Migration pins: fresh DB → column exists; 022-state DB → upgrade adds column, old
  rows NULL; CHECK constraint rejects length>128 and accepts NULL/1..128.
- Behavioral pins: per-literal E2E/storage assertions (webhook:stars settle row;
  worker settle row; quarantine anomaly row; refund row `admin:<tgID>`; CLI ingest row);
  read-back pins (card shows actor; CLI list shows actor; NULL renders as before).
- Regression armor: full existing suite (inserts without actor → NULL, assertions
  byte-stable); payreview/refund/payment-ack E2E unchanged in behavior.
- Invariants: no new deps; migration additive-only; no locale edits beyond 2 NEW keys;
  checkout surfaces byte-identical (admin-only surfaces touched).

## 7. Risks

- **R1 — INSERT binding drift** (money path): 5 INSERT statements gain a column +
  binding; a misaligned `?` corrupts rows. Mitigation: named-column INSERTs (already
  named), per-site round-trip tests, reviewer named risk with grep of all 5.
- **R2 — a missed write path** leaves new rows NULL (silent half-attribution).
  Mitigation: plan briefs carry the verified §2 inventory; T1's reviewer greps
  `INSERT INTO payment_events|INSERT OR IGNORE INTO payment_events|INSERT INTO
  payment_anomalies` repo-wide and reconciles against the 5-site list.
- **R3 — locale key collision / card layout break:** new keys only; card renders
  conditionally; E2E card pins.
- **R4 — CLI audit duplication confusion:** an event row's actor and its
  ingress-audit row's actor are the same fact in two places — docs §12 explains the
  relationship (audits = operator actions; events.actor = ingress identity).

## 8. Follow-ups deliberately OUT (homes)

- `trace_id` durable correlation — rejected (D4); logs remain the answer (§12 4.14 note).
- Backfill of historical rows from `payment_ingress_audits` — rejected (immutability;
  operators join manually if ever needed).
- Balance-rail actor (`user:<id>`) — self-evident from order; revisit only if an
  operator asks (HANDOFF §6 candidate if requested).
