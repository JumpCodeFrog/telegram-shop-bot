# Durable actor column (roadmap 4.15) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every new `payment_events` / `payment_anomalies` row carries the identity of its writing ingress (`webhook:<provider>` / `worker:<provider>` / `admin:<tgID>` / CLI `--actor`) in a nullable `actor` column, visible in `/payreview` cards and `payment-review list`.

**Architecture:** Migration 023 adds `actor TEXT NULL CHECK(...)` to both tables. The actor travels EXPLICITLY inside the existing fact envelopes (`PaymentReceipt.Actor` → `paymentFactFromReceipt` → `PaymentFact.Actor`; `PaymentAnomaly.Actor`; `Refund.Actor`), and every INSERT binds it via `NULLIF(?, '')` (empty → NULL; attribution never gates money). Read-back flows through `ListPaymentReviews` into the bot card (new locale key) and CLI list.

**Tech Stack:** Go, SQLite (embedded migrations), slog, tgbotapi — no new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-23-durable-actor-column-design.md` (rulings D1–D5; this plan implements it).

## Global Constraints

- Branch: `feat/durable-actor-column` off main `cc32cec`. One commit per task. No push.
- D1: migration `internal/storage/migrations/023_payment_actor.sql` adds to BOTH tables: `actor TEXT NULL CHECK (actor IS NULL OR length(actor) BETWEEN 1 AND 128)`. NO backfill.
- D2: literals EXACTLY the 4.13 log literals (`webhook:stars|crypto|yookassa|stripe|nowpayments`, `worker:crypto|ton|yookassa`, `admin:<tgID>`, CLI `--actor`). Stars barrier keeps `webhook:stars` for both transports. Balance rail untouched (NULL).
- D3: every INSERT binds actor via `NULLIF(?, '')`; missing attribution stores NULL and NEVER fails a write.
- D5: actor renders only when non-empty; NULL rows render byte-identical to today.
- Money semantics untouched: idempotency keys, UNIQUE constraints, replay paths, review-queue logic — byte-identical behavior.
- No new dependencies. Locale: only NEW keys, all 5 locale files (de/en/es/ru/zh), printf-verb parity.
- Gates per task: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/` (EMPTY) `&& go test ./...` (timeout ≥600000ms).
- VERIFIED INSERT inventory (6 sites — the spec's numbering said 5; reality has 5 event inserts + 1 anomaly insert; controller-corrected):
  1. `internal/storage/order_ledger_helpers.go:101` — `observePayment` — THE main capture path (`UpdateOrderStatusWithPaymentFact` → tx → observe + later `UPDATE ... SET disposition` at `:122`, which does NOT touch actor).
  2. `internal/storage/payment_recording.go:383` — `recordSubscriptionRenewalOnce` (renewal capture, 'settled').
  3. `internal/storage/payment_recording.go:568` — `recordUnexpectedPayment` (needs_review capture, INSERT OR IGNORE).
  4. `internal/storage/payment_recording.go:596` — `recordUnexpectedPayment` (identity_conflict synthetic row, INSERT OR IGNORE, 8 columns, no occurred_at).
  5. `internal/storage/ledger.go:329` — `recordRefundOnce` (refund event).
  6. `internal/storage/payment_anomalies.go:~90` — `recordPaymentAnomaly` (INSERT OR IGNORE).
- `PaymentEvent` model does NOT gain an Actor field (ruling: no consumer — `ListPaymentEvents` unchanged; only `PaymentReviewTarget.Actor` is added).

## Review Focus

1. **INSERT binding drift (money path):** 6 INSERTs gain a column + binding; a misaligned `?` corrupts rows. Pin: T1 per-site round-trip tests (insert with actor → read back actor) + reviewer grep reconciles `INSERT INTO payment_events|INSERT OR IGNORE INTO payment_events|INSERT INTO payment_anomalies` against the 6-site inventory.
2. **A missed write path** leaves new rows NULL (silent half-attribution). Pin: same grep reconciliation + T2/T3/T4 producer pins per literal.
3. **Replay determinism:** `INSERT OR IGNORE` replays must carry the same actor (recomputed at the same site). Pin: T2 idempotent re-settle test keeps first row's actor.
4. **NULL rendering byte-identity:** rows with NULL actor render exactly as before in card + CLI. Pin: T5 tests with legacy (NULL) fixtures.
5. **Migration safety:** additive ALTERs only; existing 022-state DBs upgrade cleanly; old rows NULL. Pin: T1 migration test (fresh + upgraded DB, CHECK rejects >128, accepts NULL).

---

### Task 1: Schema 023 + storage models + 6 INSERT sites + round-trip tests

**Files:**
- Create: `internal/storage/migrations/023_payment_actor.sql`
- Modify: `internal/storage/models.go` (`PaymentFact` `:198`, `Refund` `:244`, `PaymentAnomaly` `:226`, `PaymentReviewTarget` `:269`)
- Modify: `internal/storage/order_ledger_helpers.go` (INSERT `:101`)
- Modify: `internal/storage/payment_recording.go` (INSERTs `:383`, `:568`, `:596`)
- Modify: `internal/storage/ledger.go` (INSERT `:329`)
- Modify: `internal/storage/payment_anomalies.go` (INSERT `:90`)
- Test: `internal/storage/payment_actor_test.go` (new)

**Interfaces:**
- Consumes: nothing (first task).
- Produces:
  - `PaymentFact.Actor string`, `Refund.Actor string`, `PaymentAnomaly.Actor string`, `PaymentReviewTarget.Actor string`
  - Both tables have `actor` (NULL when unset) — consumed by Tasks 2–5.

- [ ] **Step 1: Failing migration/schema test** — create `internal/storage/payment_actor_test.go`:

```go
package storage

import (
	"context"
	"strings"
	"testing"
)

// TestPaymentActorSchema pins migration 023: both ledger tables carry a
// nullable actor column with the 1..128 CHECK, old rows stay NULL, new rows
// round-trip the value, and over-length actors are rejected.
func TestPaymentActorSchema(t *testing.T) {
	db := New(testDBPath(t))
	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{"payment_events", "payment_anomalies"} {
		var found, nullable int
		rows, err := db.Conn().Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			if name == "actor" {
				found++
				if notnull == 0 {
					nullable = 1
				}
			}
		}
		_ = rows.Close()
		if found != 1 || nullable != 1 {
			t.Fatalf("%s: actor column found=%d nullable=%d, want 1/1", table, found, nullable)
		}
	}
}

// TestPaymentActorCheckConstraint pins the 1..128 length CHECK.
func TestPaymentActorCheckConstraint(t *testing.T) {
	db := New(testDBPath(t))
	t.Cleanup(func() { _ = db.Close() })

	long := strings.Repeat("x", 129)
	if _, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint, proposed_order_id, provider, event_kind, external_id, amount_minor, currency, scale, reason, actor)
		VALUES ('fp-check', 0, 'stars', 'captured', 'ext-check', 1, 'XTR', 0, 'check', ?)`, long); err == nil {
		t.Fatal("129-char actor must be rejected by CHECK")
	}
	if _, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint, proposed_order_id, provider, event_kind, external_id, amount_minor, currency, scale, reason, actor)
		VALUES ('fp-ok', 0, 'stars', 'captured', 'ext-ok', 1, 'XTR', 0, 'check', 'webhook:stars')`); err != nil {
		t.Fatalf("valid actor rejected: %v", err)
	}
}
```

NOTE: if a `testDBPath(t)` helper does not exist in the storage test package, mirror whatever existing tests use (e.g. `filepath.Join(t.TempDir(), "x.db")`) — check `internal/storage/ledger_test.go` for the established helper first and use THAT.

- [ ] **Step 2: Run RED** — `go test ./internal/storage/ -run TestPaymentActor -count=1 -v` → FAIL (no such column).
- [ ] **Step 3: Migration** — create `internal/storage/migrations/023_payment_actor.sql`:

```sql
-- Roadmap 4.15: durable actor attribution on the immutable payment ledger.
-- NULL means "not recorded" (all pre-4.15 rows); new rows carry the ingress
-- identity (webhook:<provider> / worker:<provider> / admin:<tgID> / CLI --actor).
-- No backfill: the ledger is append-only and history is not rewritten.
ALTER TABLE payment_events
    ADD COLUMN actor TEXT NULL CHECK (actor IS NULL OR length(actor) BETWEEN 1 AND 128);
ALTER TABLE payment_anomalies
    ADD COLUMN actor TEXT NULL CHECK (actor IS NULL OR length(actor) BETWEEN 1 AND 128);
```

- [ ] **Step 4: Models** — `internal/storage/models.go`:
  - `PaymentFact` (`:198-207`): append field `Actor string` (after `OccurredAt`), doc comment: `// Actor is the durable ingress identity (4.15); "" stores NULL.`
  - `Refund` (`:244`): append `Actor string` with the same comment.
  - `PaymentAnomaly` (`:226`): append `Actor string` with the same comment.
  - `PaymentReviewTarget` (`:269-273`): append `Actor string` with comment `// Actor is the row's ingress identity ("" when not recorded).`
- [ ] **Step 5: Six INSERT sites** — pattern per site (column lists are NAMED; add `actor` as the LAST column and `NULLIF(?, '')` as the LAST binding, matching the struct field as source):
  1. `order_ledger_helpers.go:101` (`observePayment`): source = the `supplied *PaymentFact` param — nil-safe: compute before the insert:
     ```go
     var actor string
     if supplied != nil {
         actor = supplied.Actor
     }
     ```
     then `..., disposition, occurred_at)` → `..., disposition, occurred_at, actor)` and append `, NULLIF(?, '')` + `actor` binding.
  2. `payment_recording.go:383` (`recordSubscriptionRenewalOnce`): source = `fact.Actor` (the `fact PaymentFact` param).
  3. `payment_recording.go:568` (`recordUnexpectedPayment`): source = `fact.Actor` (the `fact *PaymentFact` param — it is non-nil at this site; verify, and if nil is possible guard like site 1 and say so in the report).
  4. `payment_recording.go:596` (identity_conflict row, 8 columns, NO occurred_at): `..., scale, disposition)` → `..., scale, disposition, actor)`, `VALUES (?, ?, ?, ?, ?, ?, ?, 'needs_review')` → append `, NULLIF(?, '')`, source `fact.Actor`.
  5. `ledger.go:329` (`recordRefundOnce`): source = `refund.Actor` (the `refund Refund` param).
  6. `payment_anomalies.go:~90` (`recordPaymentAnomaly` INSERT OR IGNORE): add `actor` after `reason` in the column list, `NULLIF(?, '')` before the occurred_at binding, source `anomaly.Actor`. NOTE: the fingerprint/canonical-retry logic is UNCHANGED — actor is NOT part of the fingerprint (it is ingress metadata, not fact identity; ruling: two same-fact writes from different ingresses dedupe on the fact, first writer's actor wins).
- [ ] **Step 6: Round-trip tests** — append to `payment_actor_test.go`:

```go
// TestPaymentActorRoundTrip pins the write path end to end: a settle with a
// fact actor stores it; a settle with empty actor stores NULL.
func TestPaymentActorRoundTrip(t *testing.T) {
	db := New(testDBPath(t))
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	store := NewSQLOrderStore(db)
	// reuse an existing helper from the package's tests to create a pending order
	// (see ledger_test.go / order_state_test.go for the established helper),
	// then:
	//   UpdateOrderStatusWithPaymentFact(ctx, id, "pending", "paid", PaymentFact{..., Actor: "webhook:stars"})
	//   SELECT actor FROM payment_events WHERE order_id = ? AND event_kind = 'captured' → "webhook:stars"
	// second order with Actor: "" → NULL (sql.NullString.Valid == false)
}
```

Write the FULL test using the package's existing order-creation helper (find it in the package's tests; do not invent a new fixture style).
- [ ] **Step 7: Gates**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`
Expected: clean/EMPTY/PASS (timeout ≥600000ms). Existing tests that insert events/anomalies without actor MUST pass unchanged (NULL actor).
- [ ] **Step 8: Commit**

```bash
git add internal/storage/
git commit -m "feat(storage): 4.15 durable actor column (023) + models + 6 INSERT sites (NULLIF binding)"
```

---

### Task 2: Capture producers — receipt.Actor at all 8 settle sites + renewal

**Files:**
- Modify: `internal/shop/order.go` (`PaymentReceipt` `:53`, `paymentFactFromReceipt` `:300`)
- Modify: `internal/bot/webhook.go` (receipt builds `:88`, `:236`, `:349`, `:464`)
- Modify: `internal/bot/handlers_payment.go` (stars barrier receipt `:627` region)
- Modify: `worker/polling.go` (`:224` region), `worker/ton_polling.go` (`:96` region), `worker/yookassa_polling.go` (`:127` region)
- Test: `internal/bot/payment_ack_test.go` or `internal/bot/e2e_test.go` (stars settle actor pin); `worker/` test for one worker literal if a harness exists, else storage-level pin suffices (say which in report)

**Interfaces:**
- Consumes: Task 1 (`PaymentFact.Actor`).
- Produces: `shop.PaymentReceipt.Actor string`; every settle row from these paths carries its literal.

- [ ] **Step 1: Failing pin** — append to `internal/bot/payment_ack_test.go` (it already drives webhook settles via `telegramSuccessfulPaymentBody`):

```go
func TestTelegramWebhookStarsSettleWritesDurableActor(t *testing.T) {
	e := newE2EEnv(t)
	e.bot.cfg.TelegramWebhookSecret = testTelegramWebhookSecret
	// place + pay one order via the existing helpers (mirror
	// TestTelegramWebhookValidStarsPaymentSettlesBeforeAcknowledgement), then:
	var actor string
	err := e.db.Conn().QueryRow(`SELECT COALESCE(actor, '') FROM payment_events
		WHERE provider = 'stars' AND event_kind = 'captured' ORDER BY id DESC LIMIT 1`).Scan(&actor)
	if err != nil {
		t.Fatal(err)
	}
	if actor != "webhook:stars" {
		t.Fatalf("actor = %q, want webhook:stars", actor)
	}
}
```

- [ ] **Step 2: Run RED** — `go test ./internal/bot/ -run TestTelegramWebhookStarsSettleWritesDurableActor -count=1 -v` → FAIL (`actor = ""`).
- [ ] **Step 3: Struct + translator** — `internal/shop/order.go`:
  - `PaymentReceipt` (`:53-63`): append `Actor string` with comment `// Actor is the durable ingress identity (4.15): webhook:<provider> / worker:<provider>; "" stores NULL.`
  - `paymentFactFromReceipt` (`:300`): add `Actor: receipt.Actor,` to the returned struct.
- [ ] **Step 4: Producer literals** — at each receipt-construction site add one field:
  - `webhook.go:88` (crypto) → `Actor: "webhook:crypto",`
  - `webhook.go:236` (yookassa) → `Actor: "webhook:yookassa",`
  - `webhook.go:349` (stripe) → `Actor: "webhook:stripe",`
  - `webhook.go:464` (nowpayments) → `Actor: "webhook:nowpayments",`
  - `handlers_payment.go` stars barrier receipt → `Actor: "webhook:stars",` (both transports keep this literal — pre-existing convention)
  - `worker/polling.go` → `Actor: "worker:crypto",`
  - `worker/ton_polling.go` → `Actor: "worker:ton",`
  - `worker/yookassa_polling.go` → `Actor: "worker:yookassa",`
  Renewal inherits via the same receipt (no extra site). Balance path (`shop/balance.go:103`) — DO NOT TOUCH.
- [ ] **Step 5: Replay pin** — extend the new test: post the same webhook body twice; assert still exactly ONE captured event row and its actor is still `webhook:stars` (first writer wins).
- [ ] **Step 6: Gates** — `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...` (timeout ≥600000ms).
- [ ] **Step 7: Commit**

```bash
git add internal/shop/order.go internal/bot/ worker/
git commit -m "feat(bot,worker): actor literals on all settle receipts (4.15 T2)"
```

---

### Task 3: Anomaly producers — PaymentAnomaly.Actor through quarantine paths

**Files:**
- Modify: `internal/shop/order.go` (`RecordPaymentAnomaly` `:482` region / `quarantineReceipt` — copy receipt.Actor)
- Modify: `internal/bot/handlers_payment.go` (`recordStarsPaymentAnomaly` — set `webhook:stars`)
- Modify: `internal/bot/webhook.go` (`quarantineUndecodableStarsUpdate` — set `webhook:stars`)
- Test: `internal/bot/payment_ack_test.go` (extend quarantine tests) + `internal/bot/out_of_stock_quarantine_test.go` if it asserts anomaly rows

**Interfaces:**
- Consumes: Task 1 (`PaymentAnomaly.Actor`), Task 2 (`PaymentReceipt.Actor` set at producers).
- Produces: anomaly rows carry actor for bot-side quarantine paths.

- [ ] **Step 1: Failing pin** — append to `payment_ack_test.go`:

```go
func TestTelegramWebhookStarsQuarantineWritesDurableActor(t *testing.T) {
	e := newE2EEnv(t)
	e.bot.cfg.TelegramWebhookSecret = testTelegramWebhookSecret
	// drive the invalid-payload quarantine (mirror
	// TestTelegramWebhookStarsParseFailureIsDurablyAcknowledged), then:
	var actor string
	err := e.db.Conn().QueryRow(`SELECT COALESCE(actor, '') FROM payment_anomalies
		WHERE provider = 'stars' AND external_id = 'stars-anomaly-actor-1'`).Scan(&actor)
	if err != nil {
		t.Fatal(err)
	}
	if actor != "webhook:stars" {
		t.Fatalf("actor = %q, want webhook:stars", actor)
	}
}
```

(Use a distinct external id in the driven body so the SELECT is unambiguous.)
- [ ] **Step 2: Run RED** — `go test ./internal/bot/ -run TestTelegramWebhookStarsQuarantineWritesDurableActor -count=1 -v` → FAIL.
- [ ] **Step 3: Producers** —
  - `shop.OrderService.RecordPaymentAnomaly`/`quarantineReceipt`: when the anomaly is built from a receipt, set `Actor: receipt.Actor`.
  - `internal/bot/handlers_payment.go recordStarsPaymentAnomaly`: set `Actor: "webhook:stars"` on the built anomaly.
  - `internal/bot/webhook.go quarantineUndecodableStarsUpdate`: set `Actor: "webhook:stars"` on the built anomaly.
  - DO NOT change the fingerprint inputs (actor is NOT part of the canonical fact — Global Constraints).
- [ ] **Step 4: Gates** — standard (`go test ./...`, timeout ≥600000ms). The existing quarantine E2E suite must pass byte-identical (actor is additive).
- [ ] **Step 5: Commit**

```bash
git add internal/shop/order.go internal/bot/
git commit -m "feat(bot): actor on stars quarantine/anomaly paths (4.15 T3)"
```

---

### Task 4: Refund + CLI producers — Refund.Actor and CLI fact/anomaly actors

**Files:**
- Modify: `internal/bot/admin_refunds.go` (refund build → `Actor: fmt.Sprintf("admin:%d", adminID)` — use the SAME value the path already puts in its ingress audit at `:460`)
- Modify: `internal/launcher/payment_ingress.go` (fact build → `Actor: *actor`; anomaly build `:180` → `Actor: *actor`; refund builds → `Actor: *actor`)
- Modify: `internal/launcher/payment_review.go` (resolve-apply refund path, if it builds a `storage.Refund` — set Actor from `--actor`)
- Test: `internal/storage/ledger_test.go` or launcher tests (refund row actor round-trip); `internal/launcher/payment_ingress_test.go` (CLI fact actor)

**Interfaces:**
- Consumes: Task 1 (`Refund.Actor`, all INSERTs).
- Produces: refund event rows + CLI-written rows carry actor.

- [ ] **Step 1: Failing pin** — storage-level round-trip: `RecordRefund` with `Refund{..., Actor: "admin:42"}` → `SELECT actor FROM payment_events WHERE event_kind='refunded' ...` → `admin:42`. Use the established ledger_test fixture style (a captured payment first — mirror an existing RecordRefund test).
- [ ] **Step 2: Run RED** — focused storage test → FAIL (`""`).
- [ ] **Step 3: Producers** —
  - `admin_refunds.go`: where the `storage.Refund` for recording is built, set `Actor` to the SAME string the path's ingress audit uses (`audit.Actor`, already `admin:<tgID>` — reuse the variable, do not reformat).
  - `payment_ingress.go`: every `storage.PaymentFact`/`storage.PaymentAnomaly`/`storage.Refund` built from flags gains `Actor: *actor`.
  - `payment_review.go` resolve-apply: if it builds a refund/event for the accepted_refund decision, set Actor from `--actor`. If the write goes through a path that already carries the audit actor, say so in the report and add the pin instead.
- [ ] **Step 4: Pins** — the Step-1 test GREEN + a CLI-shaped test (launcher package) asserting an ingested row's actor.
- [ ] **Step 5: Gates** — standard (`go test ./...`, timeout ≥600000ms).
- [ ] **Step 6: Commit**

```bash
git add internal/bot/admin_refunds.go internal/launcher/ internal/storage/
git commit -m "feat(refunds,cli): actor on refund events + CLI ingest writes (4.15 T4)"
```

---

### Task 5: Surfaces — ListPaymentReviews + /payreview card + CLI list

**Files:**
- Modify: `internal/storage/payment_resolutions.go` (`ListPaymentReviews` — both SELECTs + target mapping)
- Modify: `internal/bot/admin_payreview.go` (`sendPayReviewCard` target-line rendering `:330` region)
- Modify: `locales/{en,ru,de,es,zh}.json` (NEW key `admin_payreview_card_target_line_actor`)
- Modify: `internal/launcher/payment_review.go` (`reviewTargetFields` `:260` + list print `:107` region)
- Test: `internal/storage/` (target actor read-back), `internal/bot/` (card contains actor line only when set), `internal/launcher/payment_review_test.go` (list output)

**Interfaces:**
- Consumes: Tasks 1–4 (actor on rows + `PaymentReviewTarget.Actor`).
- Produces: operator-visible actor in bot card + CLI list.

- [ ] **Step 1: Failing tests** —
  - Storage: build a needs_review event row with actor → `ListPaymentReviews` target carries `Actor == "webhook:stars"`; a NULL-actor target yields `""`.
  - CLI: `reviewTargetFields`-fed print shows `actors=webhook:stars` (extend the existing list-test fixture style).
- [ ] **Step 2: Run RED** — focused runs → FAIL.
- [ ] **Step 3: Read-back** — `payment_resolutions.go`:
  - Events SELECT (`:21` region): `SELECT e.id, e.order_id, o.payment_state, e.event_kind` → add `, COALESCE(e.actor, '')`; scan into a string; set on the built `PaymentReviewTarget{..., Actor: actor}`.
  - Anomalies SELECT (`:48` region): same treatment (`a.actor`), set on anomaly targets.
  - Order-level targets (third select): leave Actor "" (no row identity).
- [ ] **Step 4: Bot card** — `admin_payreview.go` target loop:

```go
	for _, target := range item.Targets {
		if target.Actor != "" {
			sb.WriteString(b.i18n.Tf(lang, "admin_payreview_card_target_line_actor", target.Kind, target.ID, target.ReasonCode, target.Actor))
		} else {
			sb.WriteString(b.i18n.Tf(lang, "admin_payreview_card_target_line", target.Kind, target.ID, target.ReasonCode))
		}
	}
```

  Add to ALL FIVE locale files (identical value — technical literal):
  `"admin_payreview_card_target_line_actor": "• %s #%d — %s (actor: %s)\n",`
- [ ] **Step 5: CLI list** — `reviewTargetFields` gains an `actors []string` return (append only non-empty, `safeReviewCode`-pass — actor literals contain no `=`; still pass through safeReviewCode for consistency); the print line gains ` actors=%s` using `joinReviewStrings`-style fallback `-` when empty (reuse the existing `-` idiom). Update the two existing call sites of reviewTargetFields (list print + any other) — grep for them.
- [ ] **Step 6: Gates** — standard + `go test ./internal/bot/ -run 'PayReview' -count=1` + locale verb-parity test green.
- [ ] **Step 7: Commit**

```bash
git add internal/storage/payment_resolutions.go internal/bot/admin_payreview.go internal/launcher/ locales/
git commit -m "feat(payreview,cli): surface durable actor in card + list (4.15 T5)"
```

---

### Task 6: Docs — §12 rewrite + CHANGELOG

**Files:**
- Modify: `docs/payment-operations.md` (§12 rewrite of the log-only caveat)
- Modify: `CHANGELOG.md` (`[Unreleased]` Quality entry)

**Interfaces:**
- Consumes: Tasks 1–5 (shipped behavior).

- [ ] **Step 1: §12 rewrite** — in `docs/payment-operations.md` §12, replace the caveat that webhook/worker settles are log-only with a durable-attribution description. Exact replacement block (append as a new subsection `### Durable actor (4.15)` right after the §12 intro/actor table, and EDIT any now-false «log-only» sentence to point at it):

```markdown
### Durable actor (4.15)

Since migration 023, every new `payment_events` and `payment_anomalies` row
carries the ingress identity in a nullable `actor` column
(`CHECK length 1..128`): `webhook:<provider>` (incl. `webhook:stars` for the
Telegram Stars barrier in both transports), `worker:<provider>` (poller
settles), `admin:<tgID>` (bot refunds — same value as the refund's
ingress-audit actor), or the CLI `--actor` text. `NULL` means "not recorded"
(all pre-4.15 rows; the balance rail, whose attribution is self-evident from
the order's buyer). Actor is never part of an idempotency fingerprint and an
empty value never blocks a write. Surfaces: `/payreview` cards show
`actor:` on target lines when present; `payment-review list` prints an
`actors=` field. Relationship to `payment_ingress_audits`: audits record
OPERATOR actions (CLI/refund), while `events.actor` records which INGRESS
wrote the provider fact; for operator-driven writes the two carry the same
identity.
```

- [ ] **Step 2: CHANGELOG** — append to `[Unreleased]` Quality:

```markdown
- **Durable actor column (roadmap 4.15).** `payment_events` and
  `payment_anomalies` gained a nullable `actor` column (migration 023, no
  backfill — NULL means "not recorded"). Every new settle/capture/refund/
  quarantine row records its ingress: `webhook:<provider>`, `worker:<provider>`
  (pollers), `admin:<tgID>` (bot refunds), or the CLI `--actor`. Surfaced in
  `/payreview` target lines and `payment-review list`; docs §12 rewritten.
  Balance-rail rows stay NULL by design (buyer is on the order).
```

- [ ] **Step 3: Verify truthfulness** — grep each literal in the docs block against the code (all 8 literals + admin format must exist as written).
- [ ] **Step 4: Gates** — `gofmt -l internal/ cmd/ worker/` EMPTY; docs-only.
- [ ] **Step 5: Commit**

```bash
git add docs/payment-operations.md CHANGELOG.md
git commit -m "docs: 4.15 durable actor — §12 rewrite, CHANGELOG"
```

---

## Self-Review Notes

- **Spec coverage:** D1 → T1 (migration+CHECK+tests); D2 → T2/T3/T4 (literals, explicit flow, balance untouched); D3 → T1 (NULLIF at all 6 sites); D4 → Global Constraints (no trace column); D5 → T5 (+T1 NULL handling); §12 rewrite → T6. The spec's "5 INSERT sites" was controller-corrected to 6 (5 events + 1 anomaly) after verification — recorded in Global Constraints and ledger.
- **Placeholder scan:** T1 Step 6 + T2/T4 pins reference "the package's established fixture helper" — intentionally: the helper name must be discovered in-package (listed candidate files); the assertion code is exact. Everything else carries exact code/SQL/keys.
- **Type consistency:** `Actor string` everywhere on write-side structs; `PaymentReviewTarget.Actor string`; reads use `COALESCE(actor, '')`.
- **Fingerprint purity:** actor excluded from anomaly canonical fingerprint (ruling in T1 Step 5 note 6) — prevents dedupe-splitting of the same fact across ingresses.
- **Shared files:** `internal/shop/order.go` touched by T2 (receipt+translator) and T3 (quarantine copy) — strictly sequential.
- **Review Focus tests:** each of the 5 lines has its pin named in the owning task.
- **Post-merge janitorial (controller):** roadmap 4.15 ✅ + merge SHA; HANDOFF §1/§6 header line/§8 row/§9 digest; spec status flip.
