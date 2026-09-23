# Micro-followups batch (HANDOFF §6.16–21) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the remaining HANDOFF §6 micro-followups: 16 (CLI/bot reason-rendering asymmetry — document), 17 (shape-keyed settle filter for orphan anomalies), 18 (test hardening ×3), 19 (docs §5 wording), 20 (fleet-wide `loggerFor` sweep + auth-upsert warn), 21 (022→023 migration upgrade pin).

**Architecture:** Six small independent tasks, one commit each. Only T1 changes behavior (a UX filter; storage remains the final validator — polish-followups P4 ruling stands). T5 is a mechanical log-expression sweep with a strict in-scope rule.

**Tech Stack:** Go, SQLite, slog, tgbotapi — no new dependencies.

**Spec:** none (micro-batch; each item's authority is its HANDOFF §6 entry + the rulings below).

## Global Constraints

- Branch: `chore/micro-followups` off main `a8d0f56`. One commit per task. No push.
- No new dependencies. No migrations (T4 adds a TEST only). Locale changes: only value-level (T2c adds a trailing `\n` to an existing key in all 5 locale files).
- Rulings (controller, pre-registered):
  - **R16:** §6.16 is DOCUMENTED, not changed — CLI `safeReviewCode` (`=`→`_`) is deliberate for parseable key=value output; the bot card renders raw. Docs note, zero code change.
  - **R17:** the shape filter mirrors the storage-side settle precondition (`amount_minor > 0` AND `external_id != ""`) and is applied ONLY to the orphan-anomaly default branch; it must be a subset of what storage accepts (fail-closed).
  - **R20:** T5 converts `b.logger.` → `b.loggerFor(ctx).` ONLY inside functions with a `ctx context.Context` parameter in scope, ONLY in `internal/bot`, EXCLUDING `bot.go` (process-lifetime ctxs — passthrough would be a no-op), `webhook.go` (provider webhooks use `r.Context()` — no trace key; actor= is their attribution per 4.13/§12), `update_ctx.go` (defines loggerFor). Test files excluded.
  - **R20b:** `middleware.Auth` gains a `*slog.Logger` param; the upsert swallow becomes a Warn. No trace_id on that line (middleware package cannot see bot's ctx key — no circular import); plain logger + user_id.
  - **R18a:** the renewal payment_events full-shape pin extends `TestE2E_SubscriptionLifecycle` (the 4.15 actor pin already lives there).
- Gates per task: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/` (EMPTY) `&& go test ./...` (timeout ≥600000ms).

## Review Focus

1. **T1 filter must fail closed:** a shape-filtered orphan must lose ONLY `[Settle]`; dismiss/cli-only behavior unchanged; attached (event) cases' actions byte-identical. Pin: T1 tests (degenerate orphan → no Settle; well-formed orphan → Settle present; attached case unchanged).
2. **T1 predicate vs storage:** the filter predicate must match the storage settle precondition for anomalies. Pin: reviewer greps the storage resolve path for the actual precondition and compares.
3. **T5 sweep discipline:** zero conversions in bot.go/webhook.go/update_ctx.go/test files; zero conversions in functions without a ctx param. Pin: implementer's per-file grep accounting + reviewer spot-check of 3 files.
4. **T5 log-pin stability:** existing log-content assertions must still pass (trace_id is additive). Pin: full suite.
5. **T4 upgrade fidelity:** old rows' actor IS NULL after 022→023; new inserts work on the upgraded DB. Pin: the new migration test.

---

### Task 1: §6.17 — shape-keyed settle filter for orphan anomalies

**Files:**
- Modify: `internal/storage/models.go` (`PaymentReviewTarget` ~:269)
- Modify: `internal/storage/payment_resolutions.go` (`ListPaymentReviews` anomaly SELECT ~:48-56 region)
- Modify: `internal/bot/admin_payreview.go` (`payReviewActions` `:253-273`)
- Test: `internal/bot/admin_payreview_test.go`; `internal/storage/` (shape read-back)

**Interfaces:**
- Consumes: existing `PaymentReviewTarget` (has `Actor` since 4.15).
- Produces: `PaymentReviewTarget.AmountMinor int64` + `PaymentReviewTarget.ExternalID string` (populated for ANOMALY targets only; zero values for event/order targets — documented on the fields).

- [ ] **Step 1: Failing tests**
  - Storage: record an anomaly (`RecordPaymentAnomaly`) with amount>0/external set → `ListPaymentReviews` anomaly target carries `AmountMinor`/`ExternalID`; an event target keeps zero values.
  - Bot: card/actions test — orphan anomaly with `AmountMinor: 0, ExternalID: "x"` (or amount>0, external "") and a NON-digest reason (e.g. `webhook_invalid_receipt`) → rendered keyboard has NO Settle action (CLI-only line present); the same with `AmountMinor: 100, ExternalID: "x"` → Settle present. Use the existing admin_payreview_test fixtures (find how orphan anomaly cases are built there — mirror).
- [ ] **Step 2: Run RED** — `go test ./internal/bot/ -run 'PayReview' -count=1` and the storage test → FAIL (no fields / Settle present).
- [ ] **Step 3: Implementation**
  - `models.go` `PaymentReviewTarget`: append
    ```go
    	// AmountMinor and ExternalID carry the anomaly row's fact shape (4.15 batch
    	// §6.17) — populated for anomaly targets only; zero for event/order targets.
    	AmountMinor int64
    	ExternalID  string
    ```
  - `payment_resolutions.go` anomaly SELECT: add `a.amount_minor, a.external_id` to the column list; extend the Scan; set both on the built target.
  - `admin_payreview.go` `payReviewActions`, orphan-anomaly default branch: replace `return []string{payReviewActionSettle}` with:
    ```go
    		default:
    			// Shape-keyed gate (§6.17): storage rejects a settle without an
    			// amount and external id — don't offer a dead button. Fail-closed:
    			// CLI stays available for anything ambiguous.
    			if item.Targets[0].AmountMinor > 0 && item.Targets[0].ExternalID != "" {
    				return []string{payReviewActionSettle}
    			}
    			return nil
    ```
- [ ] **Step 4: Gates** — standard + `go test ./internal/bot/ -run 'PayReview' -count=1`.
- [ ] **Step 5: Commit**

```bash
git add internal/storage/ internal/bot/
git commit -m "feat(payreview): shape-keyed settle filter for orphan anomalies (HANDOFF §6.17)"
```

---

### Task 2: §6.18 — test hardening batch (renewal pin, atomic called-flag, trap newline)

**Files:**
- Modify: `internal/bot/e2e_test.go` or the subscription E2E file (wherever `TestE2E_SubscriptionLifecycle` lives — find it)
- Modify: `internal/payment/nowpayments_test.go:140`, `internal/payment/stripe_test.go:158,:496`, `internal/payment/yookassa_test.go:162`
- Modify: `locales/{en,ru,de,es,zh}.json` (`admin_payreview_card_trap` value gains trailing `\n`)
- Modify: `internal/bot/admin_payreview_test.go` IF a test asserts the trap string byte-exactly (grep first)

**Interfaces:**
- Consumes: existing tests.
- Produces: nothing new for other tasks.

- [ ] **Step 1: Renewal full-shape pin** — in `TestE2E_SubscriptionLifecycle`, where the renewal actor is already asserted (4.15 T2), extend the same query/assertion to also check `event_kind='captured'`, `disposition='settled'`, `currency='XTR'`, `amount_minor` equals the subscription price in stars, `external_id` equals the renewal charge id. Run it GREEN immediately (it should pass on current code — this is a coverage pin; if it FAILS, STOP → BLOCKED with output, that's a real bug).
- [ ] **Step 2: atomic called-flag** — at each of the 4 sites the pattern is `called := false` + `called = true` inside a callback/HTTP-handler goroutine + `called` read from the test goroutine. Convert to:
  ```go
  var called atomic.Bool
  ... called.Store(true) ...
  ... called.Load() ...
  ```
  with `"sync/atomic"` added to imports. Do not change what the callbacks otherwise do.
- [ ] **Step 3: trap newline** — in all 5 locale files, `admin_payreview_card_trap`'s value gains a trailing `\n` (consistency with `admin_payreview_card_cli_only`). Grep admin_payreview_test.go for a byte-exact trap assertion first; update it if present. Run `go test ./internal/bot/ -run 'Locale|PayReview' -count=1`.
- [ ] **Step 4: Gates** — standard + `go test ./internal/payment/ -race -count=1` (the atomic conversion targets cross-goroutine flags).
- [ ] **Step 5: Commit**

```bash
git add internal/bot/ internal/payment/ locales/
git commit -m "test: §6.18 hardening — renewal row shape pin, atomic called-flag ×4, trap newline parity"
```

---

### Task 3: §6.16 + §6.19 — docs cosmetics (CLI rendering asymmetry note, §5 wording)

**Files:**
- Modify: `docs/payment-operations.md` (CLI-list section + §5 ~:291 region)

**Interfaces:**
- Produces: operator-facing truth.

- [ ] **Step 1: §6.16 note** — in the section documenting `payment-review list` output (find it — §4 area), append one paragraph:

```markdown
Rendering note: `payment-review list` sanitizes reason codes for parseable
key=value output (`=` becomes `_` via `safeReviewCode`); the bot's `/payreview`
card shows the raw reason (e.g. `refund_ledger_failure:order=123`). Same fact,
two renderings — when correlating a bot card with CLI output, read `_` in the
CLI as `=`.
```

- [ ] **Step 2: §6.19 wording** — find the §5 sentence ~:291 of the form "order in `needs_review`" that introduces the review-queue entry semantics and qualifies loosely over digest-only rows. Edit MINIMALLY so the sentence says the order flip applies when an order identity exists; digest-only rows carry no order identity (mirror the existing parenthetical style at :207). Quote before→after in the report.
- [ ] **Step 3: Truthfulness check** — grep `safeReviewCode` for the `=`→`_` claim; read the :207 parenthetical for style consistency.
- [ ] **Step 4: Gates** — `gofmt -l internal/ cmd/ worker/` EMPTY (docs-only).
- [ ] **Step 5: Commit**

```bash
git add docs/payment-operations.md
git commit -m "docs: §6.16 CLI reason-rendering asymmetry note, §6.19 digest-only needs_review wording"
```

---

### Task 4: §6.21 — 022→023 migration upgrade pin

**Files:**
- Create: `internal/storage/migration_023_test.go`

**Interfaces:**
- Consumes: the migration test harness — `migrationDBBefore(t, "<file>")` and `applyMigrationFile(t, db, "<file>")` (see `migration_020_test.go`); use them EXACTLY.

- [ ] **Step 1: Failing-proof test** (it passes on current code — it pins the upgrade; if it FAILS, STOP → BLOCKED, that's a real migration bug):

```go
package storage

// TestMigration023UpgradeKeepsLegacyRows pins the 022→023 ALTER upgrade: rows
// written before the actor column existed keep NULL actor, the column exists
// post-upgrade, and new actor-bearing writes work on the upgraded database.

import (
	"database/sql"
	"strings"
	"testing"
)

func TestMigration023UpgradeKeepsLegacyRows(t *testing.T) {
	db := migrationDBBefore(t, "023_payment_actor.sql")
	// Seed one row per table on the 022 schema (no actor column yet).
	if _, err := db.Conn().Exec(`INSERT INTO orders (id, user_id, total_usd, total_stars, payment_method, payment_id, status)
		VALUES (1, 42, 1, 100, 'stars', 'seed-023', 'paid')`); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	if _, err := db.Conn().Exec(`INSERT INTO payment_events
		(order_id, provider, event_kind, external_id, amount_minor, currency, scale, disposition)
		VALUES (1, 'stars', 'captured', 'seed-evt-023', 100, 'XTR', 0, 'settled')`); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint, proposed_order_id, provider, event_kind, external_id, amount_minor, currency, scale, reason)
		VALUES ('fp-023', 0, 'stars', 'captured', 'seed-anom-023', 100, 'XTR', 0, 'legacy')`); err != nil {
		t.Fatalf("seed anomaly: %v", err)
	}

	applyMigrationFile(t, db, "023_payment_actor.sql")

	for _, q := range []string{
		`SELECT actor FROM payment_events WHERE external_id = 'seed-evt-023'`,
		`SELECT actor FROM payment_anomalies WHERE external_id = 'seed-anom-023'`,
	} {
		var actor sql.NullString
		if err := db.Conn().QueryRow(q).Scan(&actor); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if actor.Valid {
			t.Fatalf("%s: legacy row actor = %q, want NULL", q, actor.String)
		}
	}
	// New writes work on the upgraded DB, and the CHECK is live there too.
	if _, err := db.Conn().Exec(`UPDATE payment_events SET actor = 'webhook:stars' WHERE external_id = 'seed-evt-023'`); err == nil {
		t.Fatal("ledger immutability: UPDATE must be rejected by trigger (sanity that 020 triggers survived)")
	}
	if _, err := db.Conn().Exec(`INSERT INTO payment_anomalies
		(fingerprint, proposed_order_id, provider, event_kind, external_id, amount_minor, currency, scale, reason, actor)
		VALUES ('fp-023-b', 0, 'stars', 'captured', 'seed-anom-023-b', 100, 'XTR', 0, 'new', ?)`,
		strings.Repeat("x", 129)); err == nil {
		t.Fatal("129-char actor must be rejected post-upgrade")
	}
}
```

NOTE: if the seed inserts fail because the 022 schema requires additional NOT NULL columns, adjust the column lists to the real 022 schema (read migrations 017/020/021) and say so in the report; the assertions stand.
- [ ] **Step 2: Gates** — standard (`go test ./internal/storage/ -run TestMigration023 -count=1 -v` focused, then full).
- [ ] **Step 3: Commit**

```bash
git add internal/storage/migration_023_test.go
git commit -m "test(storage): 022→023 upgrade pin — legacy rows keep NULL actor (HANDOFF §6.21)"
```

---

### Task 5: §6.20 — fleet-wide loggerFor sweep + auth-upsert warn

**Files:**
- Modify: `internal/bot/*.go` production files — ALL EXCEPT `bot.go`, `webhook.go`, `update_ctx.go` (R20)
- Modify: `internal/bot/middleware/auth.go` (logger param + warn)
- Modify: `internal/bot/bot.go` (ONLY the `prepareHandler` Auth call site) + `internal/bot/e2e_test.go` (ONLY the harness Auth call site)
- Test: new small test for the auth warn (see below)

**Interfaces:**
- Consumes: `loggerFor(ctx)` (4.14); ctx params everywhere (4.14).
- Produces: `middleware.Auth(userStore UserStore, logger *slog.Logger)` — 2 call sites updated.

- [ ] **Step 1: The sweep rule (apply mechanically):** in every production (non-`_test.go`) file in `internal/bot/` EXCEPT `bot.go`, `webhook.go`, `update_ctx.go`: replace `b.logger.` with `b.loggerFor(ctx).` IF AND ONLY IF the enclosing function's signature has a `ctx context.Context` parameter. Otherwise leave the call untouched. (`b.logger` on the Bot struct is read at call time, so the existing test-time logger swaps keep working.)
- [ ] **Step 2: Accounting** — after the sweep, produce the per-file table for the report: for each touched file, `grep -c "b\.loggerFor(ctx)\."` (converted) and `grep -c "b\.logger\."` (remaining — each remainder must be in a function WITHOUT a ctx param; spot-verify and list any remainder WITH ctx in scope as a self-review finding).
- [ ] **Step 3: auth warn** — `middleware/auth.go`:
  ```go
  func Auth(userStore UserStore, logger *slog.Logger) func(next func(ctx context.Context, update tgbotapi.Update)) func(ctx context.Context, update tgbotapi.Update) {
  ```
  (add `"log/slog"` import; nil logger → `slog.Default()` guard). The upsert block:
  ```go
  			if err := userStore.Upsert(ctx, user); err != nil {
  				logger.Warn("auth: user upsert failed", "user_id", user.TelegramID, "error", err)
  			}
  ```
  Call sites: `bot.go` prepareHandler → `middleware.Auth(b.users, b.logger)`; `e2e_test.go` harness → `middleware.Auth(b.users, <the harness logger var>)`.
- [ ] **Step 4: auth warn pin** — new test (suggest `internal/bot/middleware/auth_test.go`, package middleware): a `UserStore` whose Upsert returns an error + a buffer logger → drive the middleware → assert the Warn line contains `auth: user upsert failed` and the user_id. (First test file in that package — keep it minimal and self-contained.)
- [ ] **Step 5: Gates** — standard; the full suite must pass with ZERO log-assertion failures (trace_id is additive; substring pins survive).
- [ ] **Step 6: Commit**

```bash
git add internal/bot/
git commit -m "refactor(bot): fleet-wide loggerFor sweep + auth upsert warn (HANDOFF §6.20)"
```

---

### Task 6: Batch docs — CHANGELOG + HANDOFF §6 annotations

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased]` Quality entry)
- Modify: `docs/superpowers/HANDOFF.md` (§6 items 16–21 annotations)

**Interfaces:**
- Consumes: Tasks 1–5 (describes landed behavior).

- [ ] **Step 1: CHANGELOG** — append ONE Quality entry to `[Unreleased]`:

```markdown
- **Micro-followups batch (§6.16–21).** `/payreview` orphan cards hide the
  dead `[Settle]` button when the anomaly row lacks amount/external-id
  (shape-keyed UX filter; storage remains the validator). Logging: nearly all
  handler logs now carry `trace_id` (fleet-wide `loggerFor` sweep; bot.go /
  provider webhooks keep process/request scope); Auth middleware logs upsert
  failures instead of swallowing them. Tests: renewal `payment_events`
  full-shape pin, race-strict atomic flags in provider adapter tests,
  022→023 migration upgrade pin (legacy rows keep NULL actor). Docs:
  CLI-vs-bot reason rendering asymmetry documented; §5 wording tightened for
  digest-only rows.
```

- [ ] **Step 2: HANDOFF §6 annotations** — append to each of items 16, 17, 18, 19, 20, 21 a line: `    ✅ закрыто 23.09.2026, plan docs/superpowers/plans/2026-09-23-micro-followups.md` (match the existing ✅-annotation style used for items 1–15 — read one first). Item 16's annotation notes the resolution was document-as-designed (R16).
- [ ] **Step 3: Truthfulness check** — every CHANGELOG claim greps true against the landed code (filter, sweep, warn, pins).
- [ ] **Step 4: Gates** — `gofmt -l internal/ cmd/ worker/` EMPTY (docs-only).
- [ ] **Step 5: Commit**

```bash
git add CHANGELOG.md docs/superpowers/HANDOFF.md
git commit -m "docs: micro-followups batch — CHANGELOG + HANDOFF §6.16-21 closed"
```

---

## Self-Review Notes

- **Coverage:** 16 → T3 (R16: document); 17 → T1; 18 → T2 (a/b/c); 19 → T3; 20 → T5 (R20/R20b); 21 → T4. All six items have tasks; all tasks have pins.
- **Placeholder scan:** T2 Step 1's "extend the same query" requires reading the 4.15 pin — intentional (the exact assertion shape exists in-tree); T4's schema-adjustment note is conditional with a STOP rule.
- **Type consistency:** `PaymentReviewTarget` fields `AmountMinor int64`, `ExternalID string` used identically in T1 storage+bot sides; `Auth(userStore, logger)` signature consistent across definition and 2 call sites.
- **Shared files:** admin_payreview.go touched by T1 (filter) and T2 (trap-locale assertion possibly) — sequential. No other overlaps.
- **Risk:** T5 is the largest diff (~150 mechanical sites) — the accounting step (Step 2) is the anti-drift control; reviewer gets the table + the rule and spot-checks.
- **Post-merge janitorial (controller):** HANDOFF §1 state + §8 registry row (14 планов) + §9 digest; roadmap untouched (no roadmap items in this batch).
