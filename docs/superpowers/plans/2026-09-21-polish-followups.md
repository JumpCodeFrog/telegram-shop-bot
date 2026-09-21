# Polish Follow-Ups Implementation Plan (HANDOFF §6: items 2, 3, 4, 5, 8, 9, 10, 11, 14, 15)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the remaining polish/coverage FOLLOW-UP items from `docs/superpowers/HANDOFF.md` §6: YooKassa `GetPayment` branch coverage (2), doctor env-overlay legs (3), `/paystatus` TrimSpace unification + hardcoded ON prefix (10), replay `payment_state` pin symmetry (4 residual), quarantine-table docs scoping (5), Stars renewal actor log (8), `/payreview` orphan-card ergonomics + case-gone message + ru collision (9), stale test name (11), poller warn prefix (14), plain-text `paymentMethodText` fallback (15).

**Architecture:** Ten surgical, mostly independent changes: five test-only or test-first coverage tasks (1, 2, 4 + parts of 3/6/8), three small behavior fixes (3, 6, 7/8), two docs tasks (5, 10), one micro-rename task (9), one housekeeping task (11→9, 15→3). No migrations, no new dependencies. Three new i18n keys ×5 locales (Task 7/8) — the ONLY locale churn in the plan; verb-parity and key-set gates stay green (no `%` verbs in any new value).

**Tech Stack:** Go (module `shop_bot`), SQLite ledger, tgbotapi v5, house e2e harness (`internal/bot/e2e_test.go`), locale JSONs in `locales/{de,en,es,ru,zh}.json`.

**Spec:** `docs/superpowers/HANDOFF.md` §6 items 2/3/4/5/8/9/10/11/14/15 (post-money-followups wording, including the corrected §6.9 polarity note) + Design Rulings below (part of the spec). Recon evidence: controller-verified file:line facts embedded per task.

**Branch:** `chore/polish-followups`, base `main@1fe02e2`. Ledger: `.superpowers/sdd/2026-09-21-polish-followups/progress.md`.

## Global Constraints

- Money in minor units at every boundary; ledger immutable (facts never edited — quarantine + resolution only); settlement only via verified fact.
- NO new dependencies; NO migrations. New i18n keys ONLY where a task lists them, added to ALL FIVE locale files with identical key sets; `TestLocaleFilesHaveMatchingPrintfVerbs`, `TestLocaleFilesHaveIdenticalKeySets`, `TestBotLocaleFilesCoverAllTranslationKeys` stay green.
- Disabled provider ⇒ checkout surfaces byte-identical (invariant 6): the Task 3 trim unification changes rendering ONLY for whitespace-only (misconfigured) credentials — empty/unset values render exactly as today.
- The storage layer remains the final validator for every `/payreview` action: Task 8's bot-side action filtering is a UX superset-reduction, never a gate replacement — a filtered-out action that storage WOULD accept is a bug, a rendered action that storage rejects stays fail-closed via the preview conflict.
- Gates per task (storage suite ~100s — allow ≥600000ms timeouts):
  `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/` (EMPTY output) `&& go test ./...`
- One commit per task. No push, no external side effects.

## Design Rulings (controller, pre-verified against code at 1fe02e2)

- **P1 (§6.4 is stale):** the bot-level yookassa replay `payment_state` pin ALREADY EXISTS (`internal/bot/yookassa_webhook_test.go:426-430`, added by `ee7f926`, roadmap 4.11 ✅; HANDOFF line annotated at merge `1fe02e2`). The real residual: `TestStripeWebhookReplayIsIdempotent` (stripe_webhook_test.go:357-388) and `TestNowpaymentsWebhookReplayIsIdempotent` (nowpayments_webhook_test.go:355-386) do NOT re-assert `payment_state` — contradicting the CHANGELOG hygiene claim "every payment replay leg re-asserts the persisted payment_state". Task 4 adds the two symmetric pins.
- **P2 (§6.10 is misattributed):** `doctor.go` trims uniformly (:236-238, :247, :266-268, :288-291, :321-323). The actual inconsistency lives in `/paystatus`: `internal/bot/admin_paystatus.go:41` trims crypto, but :42-44 (yookassa/stripe/nowpayments) and :38-39 (TON address/api key, compared at :77/:79) compare RAW values — a whitespace-only credential renders ON/WARN there while doctor says unconfigured. Ruling: unify on TRIM EVERYWHERE (whitespace-only ⇒ OFF; fail-closed, matches doctor). The hardcoded English assertion is `admin_paystatus_test.go:135` (`"YooKassa (RUB card) — ✅ ON"`) — replace with the locale-derived prefix idiom of :76-79.
- **P3 (§6.9 case-gone):** `storage.ErrNotFound` gets its own message via NEW key `admin_payrev_case_gone` (×5) at all three sites that currently render `admin_payrev_conflict` for it: card load (admin_payreview.go:272-277 — non-NotFound load errors move to the truthful `admin_payreview_failed`), preview (:319-326), confirm (:373-375). `ErrPaymentReviewConflict`/`ErrOrderStatusConflict` keep the conflict text; internal errors keep log+failed.
- **P4 (§6.9 orphan action sets):** `payReviewActions` filters single-anomaly ORPHAN cards (`PaymentState == ""`) by reason family: `refund_ledger_failure:*` → `[Refund, Dismiss]` + trap-warning line (new key `admin_payreview_card_trap` ×5; pre-recovery only Refund passes, post-recovery only Dismiss — both stay offered with the warning, because recovery state is invisible bot-side); digest-only (`webhook_parse_failure`, `webhook_missing_payment_id` — shape pinned by webhook.go:186-189: amount 0, no external id ⇒ all three actions provably conflict) → NO actions + CLI-only line (new key `admin_payreview_card_cli_only` ×5); any other orphan anomaly → `[Settle]` (compensated is the only possibly-passable decision, payment_resolutions.go:898-905). Attached cases and `unknown` provider: UNCHANGED.
- **P5 (§6.9 ru collision):** `locales/ru.json:259` `admin_payreview_action_settle`: «✅ Подтвердить» → «✅ Урегулировать» (value-only; en/de/es/zh verified non-colliding: Settle/Confirm, Begleichen/Bestätigen, Liquidar/Confirmar, 结清/确认). No verbs ⇒ parity gates untouched.
- **P6 (§6.9 callback budget, controller addition from recon):** house-style 64-byte comment on `payReviewCaseCallback` + fail-closed guard in `sendPayReviewCard`: any action callback >64 bytes ⇒ drop the action row, append the CLI-only hint. Realistic attached payloads ≤52 bytes (`admin:payrev:dismiss:nowpayments:<int64>` = 13+8+12+19); only a detached positive provider order id (up to 19 digits) PLUS a large anomaly id can exceed (theoretical 72) — the guard makes that card CLI-only instead of rendering dead buttons.
- **P7 (§6.8):** the renewal leg (handlers_payment.go:624-643) logs NOTHING on success today (metrics only). Add `b.logger.Info("stars subscription renewal settled", "order_id", orderID, "payment_id", sp.TelegramPaymentChargeID, "actor", "webhook:stars")` mirroring the one-time settle (:682-683); docs §12 table gains the renewal row. E2E: new `successfulPaymentRenewal` helper (raw-update boundary with `is_recurring:true, is_first_recurring:false`), renewal leg inserted into `TestE2E_SubscriptionLifecycle` AFTER first-payment asserts and BEFORE cancel (cancel flips the subscription status; renewal must run against the active subscription).
- **P8 (§6.5):** §5 (yookassa, docs:202-212) and §7 (ton, docs:405-416) intros claim ALL quarantined facts are `payment_anomalies` rows — false for the poller path's `out_of_stock_after_charge`, which `RecordUnexpectedPayment` writes as a needs_review attempt + captured/needs_review event (worker/yookassa_polling.go:135-141, worker/ton_polling.go:104-110; mechanism pinned by out_of_stock_quarantine_test.go:20-30). It surfaces in the review queue via the needs_review EVENT leg of `ListPaymentReviews` (payment_resolutions.go:21-46). §6 (stripe :273) and §8 (nowpayments :480) are webhook-only rails — verified truthful, NO change.
- **P9 (§6.2 scope):** cover the invalid-ID input leg (yookassa.go:256-258), non-2xx (`yookassaAPIError` :478-484), decode failure (:277-280), and the `toPayment` branches (:352-376) through `GetPayment`: invalid body id, missing metadata `order_id`, unparsable metadata `order_id`, malformed `captured_at` fallback, both timestamps absent, lowercase currency. DELIBERATELY EXCLUDED: `http.NewRequestWithContext` failure (unreachable with a valid base URL) and doJSON transport/read failures (sibling-method coverage exists; closing the httptest server mid-call races) — noted as considered.
- **P10 (§6.15):** NEW `paymentMethodTextPlain` with a RAW fallback, used by the plain-text admin `/order` card (admin_orders.go:115; card documented verbatim at :86-88). `paymentMethodText` (HTML-escaping fallback) stays for the HTML `/orders` list (ui_text.go:198, handlers_orders.go:34) and its pin (ui_text_test.go:64-67). `orderStatusText`'s identical fallback (:47-49) verified HTML-context-only — unchanged. Both localized value sets are shared; only the fallback differs.
- **P11 (§6.11):** rename `TestAppendPaymentIngressAuditAcceptsYooKassa` (payment_ingress_audit_test.go:20) → `TestAppendPaymentIngressAuditAcceptsYooKassaStarsStripe` (body exercises yookassa+stars+stripe+sepa-rejection; sibling naming pattern `…AcceptsTONAndNowpayments` at :154). Rename only — no split (churn without coverage gain).
- **P12 (§6.14):** worker/yookassa_polling.go:113 `"yookassa poller: page cap reached…"` → `"YooKassa polling: page cap reached…"` (sibling conventions: `CryptoBot polling:` worker/polling.go, `TON polling:` worker/ton_polling.go; all other messages in the file already use `YooKassa polling:` — :96, :123, :138, :144, :147, :154). Test pins are prefix-agnostic substrings (`"page cap reached"`, yookassa_polling_test.go:413, :426) — verified safe.

## Review Focus

1. Path-5 `refund_ledger_failure` card: operator must see the trap warning and only [Refund, Dismiss] — a pre-recovery Refund silently consumes the durable trace (Task 8 pins warning + button set in all-card renders; locale parity gates own the ×5 text).
2. Whitespace-only credentials: `/paystatus` must render OFF exactly like `doctor` diagnoses — a misleading ON sends an operator hunting a working rail (Task 3 pins all five rails OFF + no webhook section).
3. Stars renewal settle without the actor line would reopen the §12 attribution hole (Task 6 pins exactly one `actor=webhook:stars` renewal line and the absence of the one-time line).
4. Replay legs that stop re-asserting `payment_state` let projection drift pass silently (Task 4 pins stripe+nowpayments replays, completing the yookassa pattern).
5. Malformed/hostile YooKassa `GetPayment` responses: invalid ids must fail closed with NO HTTP call, and normalization (metadata, timestamps, currency case) must be exact — a wrong receipt poisons settlement (Task 1 pins every branch).

---

### Task 1: YooKassa GetPayment branch coverage (HANDOFF §6.2, ruling P9)

Test-only. `GetPayment` (internal/payment/yookassa.go:252-282) has uncovered branches: invalid-ID input (:256-258), non-2xx (:273-275), decode failure (:277-280), and the `toPayment` (:352-376) legs never exercised through it.

**Files:**
- Test: `internal/payment/yookassa_test.go` (insert after `TestYooKassaGetPaymentFallsBackToCreatedAt`, :248)

**Interfaces:**
- Consumes: existing helpers `newYookassaTestClient(srv)` (:30-34), sentinels `ErrInvalidYooKassaReceipt`, `ErrYooKassaNotConfigured`; `Payment` struct is comparable (all scalars — sibling :225 compares `*payment != *want`).
- Produces: nothing (test-only).

- [ ] **Step 1: Write the invalid-ID pin (fails closed with zero HTTP)**

```go
func TestYooKassaGetPaymentRejectsInvalidID(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	for _, tc := range []struct {
		name      string
		paymentID string
	}{
		{name: "empty", paymentID: ""},
		{name: "path separator", paymentID: "pay/1"},
		{name: "space", paymentID: "pay 1"},
		{name: "over 64 chars", paymentID: strings.Repeat("a", 65)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.GetPayment(context.Background(), tc.paymentID); !errors.Is(err, ErrInvalidYooKassaReceipt) {
				t.Fatalf("expected ErrInvalidYooKassaReceipt, got %v", err)
			}
		})
	}
	if called {
		t.Fatal("GetPayment made an HTTP call for an invalid id")
	}
}
```

(`validYooKassaID` — yookassa.go:486-498 — rejects empty, >64 chars, and any character outside `[A-Za-z0-9-_]`; the table mirrors the `CreateRefund` rejection template at :391-423.)

- [ ] **Step 2: Write the API-error and decode-failure pins**

```go
func TestYooKassaGetPaymentAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"resource_not_found","description":"Payment not found"}`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	_, err := client.GetPayment(context.Background(), "pay_404")
	if err == nil || !strings.Contains(err.Error(), "resource_not_found") {
		t.Fatalf("expected API error mentioning resource_not_found, got %v", err)
	}
}

func TestYooKassaGetPaymentDecodeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":`))
	}))
	defer srv.Close()

	client := newYookassaTestClient(srv)

	_, err := client.GetPayment(context.Background(), "pay_1")
	if err == nil || !strings.Contains(err.Error(), "parse payment response") {
		t.Fatalf("expected parse error, got %v", err)
	}
}
```

(The 404 body exercises `yookassaAPIError`'s structured branch :480-481; the status-fallback branch `"yookassa: HTTP status %d"` :483 is already pinned by `TestYooKassaCreateRefundAPIError` :425.)

- [ ] **Step 3: Write the toPayment-through-GetPayment table**

```go
// TestYooKassaGetPaymentToPaymentBranches covers the toPayment normalization
// legs through GetPayment: an invalid id in a 200 body fails closed, while
// metadata/timestamp/currency quirks normalize exactly (never an error, never
// a wrong receipt).
func TestYooKassaGetPaymentToPaymentBranches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    *Payment // nil ⇒ wantErr must be set
		wantErr error
	}{
		{
			name:    "invalid id in body fails closed",
			body:    `{"id":"pay/1","status":"succeeded","paid":true,"amount":{"value":"1999.00","currency":"RUB"},"metadata":{"order_id":"42"},"created_at":"2026-09-19T10:00:00Z"}`,
			wantErr: ErrInvalidYooKassaReceipt,
		},
		{
			name: "missing metadata order_id stays zero",
			body: `{"id":"pay_1","status":"succeeded","paid":true,"amount":{"value":"1999.00","currency":"RUB"},"created_at":"2026-09-19T10:00:00Z"}`,
			want: &Payment{ID: "pay_1", Status: "succeeded", Paid: true, Amount: "1999.00", Currency: "RUB",
				OrderID: 0, OccurredAt: time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)},
		},
		{
			name: "unparsable metadata order_id stays zero",
			body: `{"id":"pay_1","status":"succeeded","paid":true,"amount":{"value":"1999.00","currency":"RUB"},"metadata":{"order_id":"abc"},"created_at":"2026-09-19T10:00:00Z"}`,
			want: &Payment{ID: "pay_1", Status: "succeeded", Paid: true, Amount: "1999.00", Currency: "RUB",
				OrderID: 0, OccurredAt: time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)},
		},
		{
			name: "malformed captured_at falls back to created_at",
			body: `{"id":"pay_1","status":"succeeded","paid":true,"amount":{"value":"1999.00","currency":"RUB"},"metadata":{"order_id":"42"},"created_at":"2026-09-19T10:00:00Z","captured_at":"not-a-time"}`,
			want: &Payment{ID: "pay_1", Status: "succeeded", Paid: true, Amount: "1999.00", Currency: "RUB",
				OrderID: 42, OccurredAt: time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)},
		},
		{
			name: "both timestamps absent leave the zero time",
			body: `{"id":"pay_1","status":"pending","paid":false,"amount":{"value":"1999.00","currency":"RUB"},"metadata":{"order_id":"42"}}`,
			want: &Payment{ID: "pay_1", Status: "pending", Paid: false, Amount: "1999.00", Currency: "RUB",
				OrderID: 42, OccurredAt: time.Time{}},
		},
		{
			name: "lowercase currency normalizes to upper",
			body: `{"id":"pay_1","status":"succeeded","paid":true,"amount":{"value":"1999.00","currency":"rub"},"metadata":{"order_id":"42"},"captured_at":"2026-09-19T10:01:00Z"}`,
			want: &Payment{ID: "pay_1", Status: "succeeded", Paid: true, Amount: "1999.00", Currency: "RUB",
				OrderID: 42, OccurredAt: time.Date(2026, 9, 19, 10, 1, 0, 0, time.UTC)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client := newYookassaTestClient(srv)

			got, err := client.GetPayment(context.Background(), "pay_1")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetPayment: %v", err)
			}
			if *got != *tc.want {
				t.Fatalf("payment = %+v, want %+v", *got, *tc.want)
			}
		})
	}
}
```

- [ ] **Step 4: Run the package, verify GREEN (pins of existing correct behavior)**

Run: `go test ./internal/payment/ -run 'TestYooKassaGetPayment' -v -count=1`
Expected: PASS for all new + pre-existing GetPayment tests. These are coverage pins of correct behavior — RED evidence per leg is a scratch negative control (temporarily flip one expectation, e.g. `OrderID: 42` in the missing-metadata leg, see it fail, restore); record the controls in the task report.

- [ ] **Step 5: Full gates + commit**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`
Expected: clean / clean / EMPTY / all ok.

```bash
git add internal/payment/yookassa_test.go
git commit -m "test(payment): cover YooKassa GetPayment invalid-ID, API-error, decode and toPayment branches (HANDOFF 6.2)"
```

---

### Task 2: doctor env-overlay legs for the three unpinned crypto keys (HANDOFF §6.3, mutation-verified)

Test-only. `loadEnvironment` (doctor.go:194-216) merges process env over `.env` only for keys in `knownEnvironmentKeys` (:219-228). `TON_API_KEY`, `NOWPAYMENTS_IPN_SECRET`, `NOWPAYMENTS_RETURN_URL` appear in NO `LookupEnv` closure today — deleting any of them from the list keeps the suite green while process-env-only configuration becomes silently undiagnosable. Positive overlay legs (full rail config through `LookupEnv` alone ⇒ `[OK] … configured`) make the deletion loud, mirroring the existing FAIL-side overlay legs (doctor_test.go:603-657 TON, :741-766 NOWPayments).

**Files:**
- Test: `internal/launcher/doctor_test.go` (insert after `TestRunDoctorPassesConfiguredNowpayments`, :826)

**Interfaces:**
- Consumes: `RunDoctor(ctx, DoctorOptions{EnvPath, Out, Inspector, LookupEnv, CheckRedis})`, `fakeInspector{state: TelegramState{Identity: BotIdentity{ID: 7, Username: "shop_bot", SupportsInlineQueries: true}}}`, `refusedRedis`, consts `testToken`, `tonDoctorTestAddress` — exact harness of `TestRunDoctorPassesConfiguredNowpayments` (:799-826).
- Produces: nothing (test-only).

- [ ] **Step 1: Write the TON overlay leg**

```go
// TestRunDoctorPassesConfiguredTONViaEnvOverlay pins the environment-overlay
// leg for ALL THREE TON keys: with no .env file at all, the process environment
// alone must light the rail up fully. Dropping TON_API_KEY (or either other
// key) from knownEnvironmentKeys degrades the report to the missing-API-key
// WARN — and fails this test.
func TestRunDoctorPassesConfiguredTONViaEnvOverlay(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shop.db")
	var output bytes.Buffer
	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath:   filepath.Join(dir, "missing.env"),
		Out:       &output,
		Inspector: &fakeInspector{state: TelegramState{Identity: BotIdentity{ID: 7, Username: "shop_bot", SupportsInlineQueries: true}}},
		LookupEnv: func(key string) (string, bool) {
			switch key {
			case "BOT_TOKEN":
				return testToken, true
			case "ADMIN_IDS":
				return "42", true
			case "DB_PATH":
				return dbPath, true
			case "TON_WALLET_ADDRESS":
				return tonDoctorTestAddress, true
			case "USD_PER_TON":
				return "5.25", true
			case "TON_API_KEY":
				return "ton_key_do_not_print", true
			}
			return "", false
		},
		CheckRedis: func(context.Context, string, string) error { return nil },
	})
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0:\n%s", report.ExitCode(), output.String())
	}
	if !strings.Contains(output.String(), "[OK] TON payments: configured") {
		t.Fatalf("missing configured line:\n%s", output.String())
	}
	if strings.Contains(output.String(), "ton_key_do_not_print") {
		t.Fatal("doctor output leaked the TON API key")
	}
}
```

- [ ] **Step 2: Write the NOWPayments overlay leg (pins IPN_SECRET + RETURN_URL + API_KEY together)**

```go
// TestRunDoctorPassesConfiguredNowpaymentsViaEnvOverlay pins the overlay leg
// for the NOWPayments triple: dropping NOWPAYMENTS_IPN_SECRET or
// NOWPAYMENTS_RETURN_URL from knownEnvironmentKeys degrades the rail to the
// partial-credential FAIL — and fails this test.
func TestRunDoctorPassesConfiguredNowpaymentsViaEnvOverlay(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shop.db")
	var output bytes.Buffer
	report := RunDoctor(context.Background(), DoctorOptions{
		EnvPath:   filepath.Join(dir, "missing.env"),
		Out:       &output,
		Inspector: &fakeInspector{state: TelegramState{Identity: BotIdentity{ID: 7, Username: "shop_bot", SupportsInlineQueries: true}}},
		LookupEnv: func(key string) (string, bool) {
			switch key {
			case "BOT_TOKEN":
				return testToken, true
			case "ADMIN_IDS":
				return "42", true
			case "DB_PATH":
				return dbPath, true
			case "NOWPAYMENTS_API_KEY":
				return "np_key_do_not_print", true
			case "NOWPAYMENTS_IPN_SECRET":
				return "np_secret_do_not_print", true
			case "NOWPAYMENTS_RETURN_URL":
				return "https://shop.example.com/return", true
			}
			return "", false
		},
		CheckRedis: func(context.Context, string, string) error { return nil },
	})
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0:\n%s", report.ExitCode(), output.String())
	}
	if !strings.Contains(output.String(), "[OK] NOWPayments payments: configured") {
		t.Fatalf("missing configured line:\n%s", output.String())
	}
	if strings.Contains(output.String(), "np_key_do_not_print") ||
		strings.Contains(output.String(), "np_secret_do_not_print") {
		t.Fatal("doctor output leaked NOWPayments credentials")
	}
}
```

- [ ] **Step 3: Run — GREEN against correct code**

Run: `go test ./internal/launcher/ -run 'TestRunDoctorPassesConfigured.*Overlay' -v -count=1`
Expected: PASS (both).

- [ ] **Step 4: Mutation-verify (RED evidence)**

Three separate mutations of `knownEnvironmentKeys` (doctor.go:226-227), one at a time; after each, run the command from Step 3, expect FAIL of the owning test, restore:
1. delete `"TON_API_KEY"` → TON overlay test fails (WARN line instead of `[OK] … configured`);
2. delete `"NOWPAYMENTS_IPN_SECRET"` → NOWPayments overlay test fails;
3. delete `"NOWPAYMENTS_RETURN_URL"` → NOWPayments overlay test fails.
Record all three mutation outcomes in the task report.

- [ ] **Step 5: Full gates + commit**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`

```bash
git add internal/launcher/doctor_test.go
git commit -m "test(launcher): doctor env-overlay legs for TON_API_KEY, NOWPAYMENTS_IPN_SECRET/RETURN_URL (HANDOFF 6.3)"
```

---

### Task 3: /paystatus trims every credential like doctor + drop the hardcoded ON prefix (HANDOFF §6.10, ruling P2)

**Files:**
- Modify: `internal/bot/admin_paystatus.go:38-44`
- Test: `internal/bot/admin_paystatus_test.go` (new test + fix :135)

**Interfaces:**
- Consumes: `newE2EEnvWithConfig(t, func(c *config.Config){…})`, `payStatusText(t, e)` (admin_paystatus_test.go:19-26), locale keys `admin_paystatus_*_off`, `admin_paystatus_webhooks_title`.
- Produces: unchanged `formatPayStatus` signature; behavior change ONLY for whitespace-only credentials (empty/unset render byte-identically — invariant 6 safe).

- [ ] **Step 1: Write the failing test**

```go
// TestPayStatusWhitespaceOnlyCredentialsRenderOff pins the TrimSpace
// unification (HANDOFF §6.10): a whitespace-only credential is a
// misconfiguration, not a configured rail — /paystatus must render OFF for it
// exactly like doctor diagnoses it, never a misleading ON/WARN.
func TestPayStatusWhitespaceOnlyCredentialsRenderOff(t *testing.T) {
	e := newE2EEnvWithConfig(t, func(c *config.Config) {
		c.CryptoBotToken = " "
		c.YooKassaShopID, c.YooKassaSecretKey, c.YooKassaReturnURL = " ", " ", " "
		c.StripeSecretKey, c.StripeWebhookSecret, c.StripeReturnURL = " ", " ", " "
		c.TONWalletAddress, c.TONAPIKey = " ", " "
		c.NowpaymentsAPIKey, c.NowpaymentsIPNSecret, c.NowpaymentsReturnURL = " ", " ", " "
	})

	text := payStatusText(t, e)
	for _, want := range []string{
		e.bot.t("en", "admin_paystatus_crypto_off"),
		e.bot.t("en", "admin_paystatus_yookassa_off"),
		e.bot.t("en", "admin_paystatus_stripe_off"),
		e.bot.t("en", "admin_paystatus_ton_off"),
		e.bot.t("en", "admin_paystatus_nowpayments_off"),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("paystatus missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, e.bot.t("en", "admin_paystatus_webhooks_title")) {
		t.Errorf("whitespace-only credentials must not open the webhook section:\n%s", text)
	}
}
```

Run: `go test ./internal/bot/ -run TestPayStatusWhitespaceOnlyCredentialsRenderOff -count=1`
Expected: FAIL — today yookassa renders the WARN line (creds-without-rate), stripe/nowpayments render ON, TON renders WARN (raw `tonAddress != ""`).

- [ ] **Step 2: Unify on TrimSpace**

In `formatPayStatus` (admin_paystatus.go:35-45) replace the credential reads:

```go
	if b.cfg != nil {
		yooRate = b.cfg.USDToRUBRate
		tonRate = b.cfg.USDPerTON
		tonAddress = strings.TrimSpace(b.cfg.TONWalletAddress)
		tonAPIKey = strings.TrimSpace(b.cfg.TONAPIKey)
		baseURL = b.cfg.WebhookURL
		cryptoOn = strings.TrimSpace(b.cfg.CryptoBotToken) != ""
		yooCreds = strings.TrimSpace(b.cfg.YooKassaShopID) != "" &&
			strings.TrimSpace(b.cfg.YooKassaSecretKey) != "" &&
			strings.TrimSpace(b.cfg.YooKassaReturnURL) != ""
		stripeOn = strings.TrimSpace(b.cfg.StripeSecretKey) != "" &&
			strings.TrimSpace(b.cfg.StripeWebhookSecret) != "" &&
			strings.TrimSpace(b.cfg.StripeReturnURL) != ""
		nowOn = strings.TrimSpace(b.cfg.NowpaymentsAPIKey) != "" &&
			strings.TrimSpace(b.cfg.NowpaymentsIPNSecret) != "" &&
			strings.TrimSpace(b.cfg.NowpaymentsReturnURL) != ""
	}
```

(The trimmed `tonAddress`/`tonAPIKey` flow into the existing comparisons at :77/:79/:83 unchanged; `baseURL` is already trimmed at its use site :105. Add a one-line comment above the block: `// Whitespace-only values are misconfiguration, not configuration — trim like doctor.go does.`)

- [ ] **Step 3: Fix the hardcoded English assertion (:135)**

In `TestPayStatusYooKassaCredsWithoutRate`, replace

```go
	if strings.Contains(text, "YooKassa (RUB card) — ✅ ON") ||
```

with the locale-derived prefix idiom of :76-79:

```go
	yookassaOnPrefix := strings.Split(e.bot.t("en", "admin_paystatus_yookassa_on"), "%s")[0]
	if strings.Contains(text, yookassaOnPrefix) ||
```

- [ ] **Step 4: Run bot paystatus suite — GREEN + no regression**

Run: `go test ./internal/bot/ -run TestPayStatus -count=1`
Expected: PASS (all paystatus tests — the trim only changes whitespace-only renders; `TestPayStatusFullyConfigured` and friends use real values).

- [ ] **Step 5: Full gates + commit**

```bash
git add internal/bot/admin_paystatus.go internal/bot/admin_paystatus_test.go
git commit -m "fix(bot): /paystatus trims every credential like doctor does; locale-derived ON assertion (HANDOFF 6.10)"
```

---

### Task 4: stripe/nowpayments replay tests re-assert payment_state (HANDOFF §6.4 residual, ruling P1)

Test-only. The yookassa replay test pins the persisted projection (yookassa_webhook_test.go:426-430); the stripe and nowpayments replay tests stop at attempts-count and silence.

**Files:**
- Test: `internal/bot/stripe_webhook_test.go` (`TestStripeWebhookReplayIsIdempotent`, insert after the payment_attempts assert :381-384)
- Test: `internal/bot/nowpayments_webhook_test.go` (`TestNowpaymentsWebhookReplayIsIdempotent`, insert after :379-382)

**Interfaces:**
- Consumes: `e.qStr` (e2e_test.go:434), `storage.PaymentStateSettled` — both already used in these files' packages.
- Produces: nothing.

- [ ] **Step 1: Insert the identical pin into BOTH tests**

```go
	// The replay leaves the ledger projection exactly as the first settlement
	// wrote it (the storage level pins settled; pin it at the bot level too).
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id = ?`, orderID); got != storage.PaymentStateSettled {
		t.Fatalf("payment_state after replay = %q, want settled", got)
	}
```

- [ ] **Step 2: Run + negative control**

Run: `go test ./internal/bot/ -run 'TestStripeWebhookReplayIsIdempotent|TestNowpaymentsWebhookReplayIsIdempotent' -v -count=1`
Expected: PASS. Negative control (RED evidence; house precedent for regression pins — a pin of existing correct behavior cannot fail first): in a scratch run change the expectation to `storage.PaymentStateNeedsReview` in one test, see it FAIL with `payment_state after replay = "settled"`, restore. Record in the report.

- [ ] **Step 3: Full gates + commit**

```bash
git add internal/bot/stripe_webhook_test.go internal/bot/nowpayments_webhook_test.go
git commit -m "test(bot): stripe/nowpayments webhook replays re-assert the persisted payment_state (HANDOFF 6.4 residual)"
```

---

### Task 5: quarantine-table intros name the writing mechanism (HANDOFF §6.5, ruling P8)

Docs-only. §5 (yookassa) and §7 (ton) claim ALL quarantined facts are `payment_anomalies` rows; the poller path's `out_of_stock_after_charge` is a needs_review capture (`RecordUnexpectedPayment`), surfacing via the review queue's event leg. §6 (stripe) / §8 (nowpayments) are webhook-only rails — verify and leave unchanged.

**Files:**
- Modify: `docs/payment-operations.md` §5 (:201-212 area) and §7 (:405-416 area)

**Interfaces:**
- Consumes: nothing. Produces: nothing (docs).

- [ ] **Step 1: Verify the mechanisms (evidence for the wording)**

Read: `worker/yookassa_polling.go:135-141` and `worker/ton_polling.go:104-110` (out-of-stock → `RecordUnexpectedPayment`), `internal/bot/out_of_stock_quarantine_test.go:20-30` (mechanism matrix per surface), `internal/storage/payment_resolutions.go:21-46` (needs_review EVENTS surface as review targets), `internal/bot/webhook.go:246-252` (yookassa webhook out-of-stock → anomaly row). Read §6 (:273) and §8 (:480) intros and confirm they are webhook-only-rail truthful; record the confirmation in the report.

- [ ] **Step 2: §5 edit**

Replace the intro (:203-204):

```
Quarantined facts are `payment_anomalies` rows with provider `yookassa` and the
order in `needs_review`:
```

with:

```
Webhook-quarantined facts are `payment_anomalies` rows with provider `yookassa`
and the order in `needs_review`:
```

and insert AFTER the reason table (before the next prose paragraph):

```
The backup poller quarantines one class differently: an out-of-stock settlement
it finds is recorded via `RecordUnexpectedPayment` as a needs_review payment
attempt plus a captured/needs_review payment event — no `payment_anomalies`
row — and surfaces in the review queue through that event target. Every other
poller quarantine (receipt mismatch, identity conflicts) runs through the same
storage gate as the webhook and writes `payment_anomalies` rows exactly as
tabulated above.
```

- [ ] **Step 3: §7 edit**

Replace the intro (:406-408):

```
Quarantined facts are `payment_anomalies` rows with provider `ton` and the
order in `needs_review`:
```

with:

```
Quarantined TON facts are `payment_anomalies` rows with provider `ton` and the
order in `needs_review` — except `out_of_stock_after_charge`, which the poller
(TON's only settlement path) records via `RecordUnexpectedPayment` as a
needs_review attempt/event capture instead; it surfaces in the review queue
through that event target:
```

and extend the table row's Meaning cell:

```
| `out_of_stock_after_charge` | Paid transfer whose product went out of stock before fulfillment; durable — never retried; recorded as a needs_review capture, not an anomaly row (poller path) |
```

- [ ] **Step 4: Consistency check + commit**

`grep -n "payment_anomalies rows with provider" docs/payment-operations.md` — every remaining hit must be webhook-only-rail truthful (§6 stripe, §8 nowpayments). No code touched; run the docs-only sanity gates (`go build ./...` trivially green).

```bash
git add docs/payment-operations.md
git commit -m "docs: quarantine-table intros name the writing mechanism per path (HANDOFF 6.5)"
```

---

### Task 6: Stars subscription renewal logs its settle actor (HANDOFF §6.8, ruling P7)

**Files:**
- Modify: `internal/bot/handlers_payment.go:638-643` (renewal success leg)
- Modify: `internal/bot/e2e_test.go` (`successfulPayment` helper :341-383 → shared raw builder + renewal variant; `TestE2E_SubscriptionLifecycle` :753-833 gains the renewal leg)
- Modify: `docs/payment-operations.md` §12 table (:797-803)

**Interfaces:**
- Consumes: `b.logger` (swappable in tests — idiom yookassa_webhook_test.go:229), `e.bot.subs.ListActiveByUser`, `e.userDBID`, `storage.SubStatusActive`.
- Produces: `e.successfulPaymentRenewal(userID int64, payload string, totalStars int, chargeID string) []tgCall` (test harness); log line `"stars subscription renewal settled"` with fields `order_id`, `payment_id`, `actor=webhook:stars` (docs §12 references it).

- [ ] **Step 1: Refactor the e2e helper (no behavior change) + add the renewal variant**

Replace `successfulPayment` (e2e_test.go:341-383) with:

```go
func (e *e2eEnv) successfulPayment(userID int64, payload string, totalStars int, chargeID string) []tgCall {
	return e.rawStarsPayment(userID, payload, totalStars, chargeID, false)
}

// successfulPaymentRenewal drives a recurring Stars charge that is NOT the
// first one (is_recurring && !is_first_recurring) — the renewal leg of
// handleSuccessfulPayment (RecordSubscriptionRenewal).
func (e *e2eEnv) successfulPaymentRenewal(userID int64, payload string, totalStars int, chargeID string) []tgCall {
	return e.rawStarsPayment(userID, payload, totalStars, chargeID, true)
}

func (e *e2eEnv) rawStarsPayment(userID int64, payload string, totalStars int, chargeID string, renewal bool) []tgCall {
	e.updSeq++
	update := tgbotapi.Update{
		UpdateID: e.updSeq,
		Message: &tgbotapi.Message{
			MessageID: e.updSeq,
			Date:      int(time.Now().Unix()),
			Chat:      &tgbotapi.Chat{ID: userID, Type: "private"},
			From:      &tgbotapi.User{ID: userID, LanguageCode: "ru"},
			SuccessfulPayment: &tgbotapi.SuccessfulPayment{
				Currency:                "XTR",
				TotalAmount:             totalStars,
				InvoicePayload:          payload,
				TelegramPaymentChargeID: chargeID,
			},
		},
	}
	// Drive the same raw-update boundary as production so subscription-only
	// fields omitted by tgbotapi v5 are present during settlement.
	expiresAt := time.Now().Add(30 * 24 * time.Hour).Unix()
	sp := map[string]any{
		"currency": "XTR", "total_amount": totalStars, "invoice_payload": payload,
		"telegram_payment_charge_id": chargeID, "subscription_expiration_date": expiresAt,
	}
	if renewal {
		sp["is_recurring"] = true
		sp["is_first_recurring"] = false
	}
	raw, err := json.Marshal(map[string]any{
		"update_id": update.UpdateID,
		"message": map[string]any{
			"message_id":       update.Message.MessageID,
			"date":             update.Message.Date,
			"chat":               update.Message.Chat,
			"from":               update.Message.From,
			"successful_payment": sp,
		},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	decoded, cleanup, err := e.bot.decodeTelegramUpdate(raw)
	if err != nil {
		e.t.Fatal(err)
	}
	defer cleanup()
	return e.do(decoded)
}
```

(The first-payment raw JSON gains NO fields — `renewal=false` keeps the exact current shape, so every existing caller is byte-identical.)

- [ ] **Step 2: Write the renewal E2E leg (RED — no actor log yet)**

In `TestE2E_SubscriptionLifecycle`, insert AFTER the first-payment expiry assert (:808) and BEFORE the `/mysubs` block (:810):

```go
	// Renewal: a second recurring charge (not the first) for the same order
	// payload extends the subscription under a NEW charge id with zero order,
	// stock, loyalty or message side effects — and logs the settle actor (§12).
	stockBefore := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodSub)
	var renewLogs bytes.Buffer
	e.bot.logger = slog.New(slog.NewTextHandler(&renewLogs, nil))
	beforeRenew := e.tg.count()
	e.successfulPaymentRenewal(buyer, payload, 100, "ch-sub-2")
	if got := e.tg.count() - beforeRenew; got != 0 {
		t.Fatalf("renewal sent %d messages, want 0:\n%s", got, dumpCalls(e.tg.since(beforeRenew)))
	}
	if got := e.qStr(`SELECT telegram_charge_id FROM subscriptions WHERE user_id = ?`, buyer); got != "ch-sub-2" {
		t.Fatalf("renewal charge = %q, want ch-sub-2", got)
	}
	subsAfter, err := e.bot.subs.ListActiveByUser(t.Context(), buyer)
	if err != nil || len(subsAfter) != 1 || !subsAfter[0].ExpiresAt.After(subs[0].ExpiresAt) {
		t.Fatalf("renewal did not extend the expiry: %v (err %v)", subsAfter, err)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM orders WHERE user_id = ?`, buyer); got != 1 {
		t.Fatalf("renewal created extra orders: %d, want 1", got)
	}
	if got := e.qInt(`SELECT stock FROM products WHERE id = ?`, e.prodSub); got != stockBefore {
		t.Fatalf("renewal touched stock: %d, want %d", got, stockBefore)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM loyalty_txs WHERE user_id = ?`, e.userDBID(buyer)); got != 1 {
		t.Fatalf("renewal replayed loyalty: %d rows, want still 1", got)
	}
	if got := strings.Count(renewLogs.String(), "stars subscription renewal settled"); got != 1 {
		t.Fatalf("renewal settle log count = %d, want exactly 1; logs:\n%s", got, renewLogs.String())
	}
	if got := strings.Count(renewLogs.String(), "actor=webhook:stars"); got != 1 {
		t.Fatalf("renewal actor count = %d, want exactly 1; logs:\n%s", got, renewLogs.String())
	}
	if strings.Contains(renewLogs.String(), "stars payment settled") {
		t.Fatalf("renewal logged the one-time settle line:\n%s", renewLogs.String())
	}
```

Add `"bytes"` to the e2e_test.go import block if absent (`log/slog`, `strings`, `time` are already imported).

Run: `go test ./internal/bot/ -run TestE2E_SubscriptionLifecycle -count=1`
Expected: FAIL — `renewal settle log count = 0, want exactly 1` (today the leg logs nothing; all DB asserts must pass already — if any DB assert fails first, STOP and report: the renewal semantics assumption is wrong).

- [ ] **Step 3: Add the actor log**

In `handlers_payment.go`, inside the renewal branch — after the metrics increment (:639-641), before `return nil` (:642):

```go
		// Settlement attribution (docs/payment-operations.md §12): the renewal
		// arrives through the same Telegram successful_payment ingress as the
		// one-time settle — log-level actor, no durable actor row.
		b.logger.Info("stars subscription renewal settled",
			"order_id", orderID, "payment_id", sp.TelegramPaymentChargeID, "actor", "webhook:stars")
```

- [ ] **Step 4: docs §12 table row**

Insert after the "Stars `successful_payment` settlement" row (:800):

```
| Stars subscription renewal (recurring `successful_payment`, `is_recurring && !is_first_recurring`) | `webhook:stars` | settlement-success log line only |
```

- [ ] **Step 5: Run — GREEN, then full gates + commit**

Run: `go test ./internal/bot/ -run 'TestE2E_SubscriptionLifecycle|TestStash|TestDecodeTelegramUpdate' -count=1` → PASS.

```bash
git add internal/bot/handlers_payment.go internal/bot/e2e_test.go docs/payment-operations.md
git commit -m "feat(bot): Stars subscription renewal logs its settle actor (HANDOFF 6.8)"
```

---

### Task 7: /payreview case-gone message + ru Settle label (HANDOFF §6.9, rulings P3/P5)

**Files:**
- Modify: `internal/bot/admin_payreview.go:272-277` (card load), `:319-326` (preview), `:373-375` (confirm)
- Modify: `locales/{de,en,es,ru,zh}.json` (new key `admin_payrev_case_gone` after `admin_payrev_conflict` :266; ru :259 value change)
- Test: `internal/bot/admin_payreview_test.go` (new test after `TestPayReviewConfirmDetectsTargetsChanged`, :460)

**Interfaces:**
- Consumes: `seedUnknownProviderCase(t, e)` (existing helper), `b.t`, `sendOrEditStyled`.
- Produces: locale key `admin_payrev_case_gone` (Task 8 does not touch it); the ErrNotFound→case-gone mapping at all three sites (Task 8 edits OTHER parts of the same functions — sequence T7 → T8).

- [ ] **Step 1: Add the locale key ×5 + the ru rename**

Add to ALL five locale files, immediately after the `admin_payrev_conflict` line (each file's :266), keeping JSON valid:

```json
  "admin_payrev_case_gone": "ℹ️ The case is no longer in the review queue — it was resolved elsewhere or its targets changed. Reopen /payreview.",
```

- ru: `"ℹ️ Карточка больше не в очереди ревью — её уже разрешили или её цели изменились. Откройте /payreview заново."`
- de: `"ℹ️ Der Fall ist nicht mehr in der Prüfungswarteschlange — er wurde anderswo gelöst oder seine Ziele haben sich geändert. /payreview erneut öffnen."`
- es: `"ℹ️ El caso ya no está en la cola de revisión: se resolvió en otro lugar o sus objetivos cambiaron. Vuelve a abrir /payreview."`
- zh: `"ℹ️ 该案件已不在复核队列中——已在别处解决或其目标已变更。请重新打开 /payreview。"`
- en: the string above.

And in `locales/ru.json:259` change `"admin_payreview_action_settle": "✅ Подтвердить"` → `"✅ Урегулировать"` (en/de/es/zh verified non-colliding — P5). No `%` verbs anywhere ⇒ `TestLocaleFilesHaveMatchingPrintfVerbs` untouched.

- [ ] **Step 2: Write the failing test**

```go
// TestPayReviewCaseGoneMessageIsDistinct pins ruling P3 (HANDOFF §6.9): a case
// that left the queue between taps answers with its own case-gone message at
// every entry point (card, preview, confirm) — the conflict text stays reserved
// for changed/invalid target sets.
func TestPayReviewCaseGoneMessageIsDistinct(t *testing.T) {
	e := newE2EEnv(t)
	orderID := seedUnknownProviderCase(t, e)

	// Resolve the case through the regular two-tap flow.
	e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:dismiss:unknown:%d", orderID), "en")
	e.cb(e2eAdminID, fmt.Sprintf("admin:payrevdo:dismiss:unknown:%d", orderID), "en")
	if got := e.qInt(`SELECT COUNT(*) FROM payment_resolutions`); got != 1 {
		t.Fatalf("resolutions = %d, want 1", got)
	}

	want := e.bot.t("en", "admin_payrev_case_gone")
	conflict := e.bot.t("en", "admin_payrev_conflict")
	for _, stale := range []string{
		fmt.Sprintf("admin:payrev:unknown:%d", orderID),          // card tap
		fmt.Sprintf("admin:payrev:dismiss:unknown:%d", orderID),   // preview tap
		fmt.Sprintf("admin:payrevdo:dismiss:unknown:%d", orderID), // confirm tap
	} {
		calls := e.cb(e2eAdminID, stale, "en")
		if got := tgText(calls); !strings.Contains(got, want) || strings.Contains(got, conflict) {
			t.Fatalf("stale %s text = %q, want case-gone %q", stale, got, want)
		}
	}
}
```

Run: `go test ./internal/bot/ -run TestPayReviewCaseGoneMessageIsDistinct -count=1`
Expected: FAIL — today all three stale taps render `admin_payrev_conflict` (case-gone text absent).

- [ ] **Step 3: Split ErrNotFound at the three sites**

Card load (admin_payreview.go:272-277) becomes:

```go
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// The case left the queue between the list render and this tap.
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payrev_case_gone"), "", StyledKeyboard{})
			return
		}
		b.logger.Error("load payment review case", "error", err)
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payreview_failed"), "", StyledKeyboard{})
		return
	}
```

Preview (:319-326) becomes:

```go
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrNotFound):
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payrev_case_gone"), "", StyledKeyboard{})
		case errors.Is(err, storage.ErrPaymentReviewConflict), errors.Is(err, storage.ErrOrderStatusConflict):
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payrev_conflict"), "", StyledKeyboard{})
		default:
			b.logger.Error("preview payment review", "error", err)
			b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payreview_failed"), "", StyledKeyboard{})
		}
		return
	}
```

Confirm (:373-375): split the combined case arm into two:

```go
	case errors.Is(err, storage.ErrNotFound):
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payrev_case_gone"), "", StyledKeyboard{})
	case errors.Is(err, storage.ErrPaymentReviewConflict) || errors.Is(err, storage.ErrOrderStatusConflict):
		b.sendOrEditStyled(chatID, msgID, b.t(lang, "admin_payrev_conflict"), "", StyledKeyboard{})
```

- [ ] **Step 4: Run bot payreview/refund/e2e suites — GREEN + no regression**

Run: `go test ./internal/bot/ -run 'TestPayReview|TestAdminPayReview|TestE2EPayreview|TestAdminRefundLedgerFailure' -count=1`
Expected: PASS — `TestPayReviewConfirmDetectsTargetsChanged` (:451 pins the CONFLICT text on a real conflict) and `TestE2EPayreviewFlow` (e2e_test.go:1894 conflict leg) stay green because their legs are conflicts, not not-found.

- [ ] **Step 5: Locale gates + full gates + commit**

Run: `go test ./internal/bot/ -run 'TestLocaleFiles|TestBotLocaleFilesCoverAllTranslationKeys' -count=1` → PASS, then the full gates.

```bash
git add internal/bot/admin_payreview.go internal/bot/admin_payreview_test.go locales/de.json locales/en.json locales/es.json locales/ru.json locales/zh.json
git commit -m "fix(bot): /payreview case-gone gets its own message; ru Settle label disambiguated (HANDOFF 6.9)"
```

---

### Task 8: /payreview orphan-card action sets, trap + CLI-only hints, callback byte guard (HANDOFF §6.9, rulings P4/P6)

**Files:**
- Modify: `internal/bot/admin_payreview.go` (`payReviewActions` :244-253, `sendPayReviewCard` body :280-303, comment on `payReviewCaseCallback` :66-68)
- Modify: `locales/{de,en,es,ru,zh}.json` (new keys `admin_payreview_card_trap`, `admin_payreview_card_cli_only`)
- Test: `internal/bot/admin_payreview_test.go` (unit + e2e legs)

**Interfaces:**
- Consumes: Task 7's file state (sequence T7 → T8; different hunks of `sendPayReviewCard`), `storage.PaymentReviewTargetAnomaly`, `store.RecordPaymentAnomaly` seeding idiom (admin_payreview_test.go:469-477), path-5 anomaly shape (admin_refunds.go:435-452), digest shape (webhook.go:186-189: Provider + RawPayload `sha256:…` + Reason only).
- Produces: `payReviewIsRefundLedgerFailureOrphan(item storage.PaymentReviewCase) bool`; filtered `payReviewActions`; two locale keys.

- [ ] **Step 1: Add the two locale keys ×5**

`admin_payreview_card_trap` (after `admin_payreview_card_target_line`, each file's :258):
- en: `"⚠️ Refund recording failure card: act only AFTER the recovery re-run completed the ledger record. Before recovery, ↩️ Refund acknowledges money the books have not recorded and closes this card permanently; after recovery, use 🚫 Dismiss. Details: docs/payment-operations.md §11."`
- ru: `"⚠️ Карточка сбоя записи возврата: действуйте только ПОСЛЕ того, как повторный /refund завершит запись в реестре. До восстановления ↩️ Refund подтверждает деньги, которых нет в книгах, и навсегда закрывает карточку; после восстановления используйте 🚫 Dismiss. Подробности: docs/payment-operations.md §11."`
- de: `"⚠️ Karte für fehlgeschlagene Rückerstattungsbuchung: Erst handeln, NACHDEM der Wiederholungslauf die Ledger-Buchung abgeschlossen hat. Vor der Wiederherstellung bestätigt ↩️ Refund Geld, das die Bücher nicht erfasst haben, und schließt diese Karte dauerhaft; danach 🚫 Dismiss verwenden. Details: docs/payment-operations.md §11."`
- es: `"⚠️ Tarjeta de fallo de registro del reembolso: actúa solo DESPUÉS de que la reejecución de recuperación complete el registro contable. Antes de la recuperación, ↩️ Refund reconoce un dinero que los libros no registraron y cierra esta tarjeta permanentemente; después, usa 🚫 Dismiss. Detalles: docs/payment-operations.md §11."`
- zh: `"⚠️ 退款记账失败卡片：仅在恢复重跑完成台账记录之后再操作。恢复前,↩️ Refund 会确认账上尚未记录的资金并永久关闭此卡片;恢复后请使用 🚫 Dismiss。详见 docs/payment-operations.md §11。"`

`admin_payreview_card_cli_only` (right after the trap key):
- en: `"🔧 No bot-side action passes on this case — resolve it via the payment-review CLI (docs §4).\n"`
- ru: `"🔧 Ни одно действие в боте не пройдёт для этой карточки — разрешите её через CLI payment-review (docs §4).\n"`
- de: `"🔧 Keine Bot-Aktion besteht für diesen Fall — Lösung über die payment-review-CLI (Doku §4).\n"`
- es: `"🔧 Ninguna acción del bot pasa para este caso: resuélvelo con la CLI payment-review (docs §4).\n"`
- zh: `"🔧 此案件在机器人侧无可用操作——请通过 payment-review CLI 解决(docs §4)。\n"`

(No `%` verbs ⇒ parity gates untouched. The `cli_only` value ends with `\n` because it is appended into the card text; the trap key too — match the card_targets/target_line trailing-newline convention.)

- [ ] **Step 2: Write the failing unit test for the action sets**

```go
// TestPayReviewOrphanCardActionSets pins ruling P4: orphan cards offer only
// the actions that can actually pass — digest-only cards none (CLI-only),
// path-5 refund-ledger-failure cards Refund+Dismiss (with the trap warning),
// other capture orphans Settle. Attached and unknown-provider cases keep
// their existing sets.
func TestPayReviewOrphanCardActionSets(t *testing.T) {
	orphan := func(provider, reason string) storage.PaymentReviewCase {
		return storage.PaymentReviewCase{
			OrderID: 0, Provider: provider, PaymentState: "",
			Targets: []storage.PaymentReviewTarget{{
				Kind: storage.PaymentReviewTargetAnomaly, ID: 7, ReasonCode: reason,
			}},
		}
	}
	for _, tc := range []struct {
		name string
		item storage.PaymentReviewCase
		want []string
	}{
		{"path-5 refund orphan", orphan(storage.PaymentMethodBalance, "refund_ledger_failure:order=7"),
			[]string{payReviewActionRefund, payReviewActionDismiss}},
		{"digest parse failure", orphan(storage.PaymentMethodYooKassa, "webhook_parse_failure"), nil},
		{"digest missing payment id", orphan(storage.PaymentMethodYooKassa, "webhook_missing_payment_id"), nil},
		{"capture orphan", orphan(storage.PaymentMethodStars, "provider_verified_unknown_order"),
			[]string{payReviewActionSettle}},
		{"attached case keeps the triple", storage.PaymentReviewCase{
			OrderID: 3, Provider: storage.PaymentMethodStars, PaymentState: storage.PaymentStateNeedsReview,
			Targets: []storage.PaymentReviewTarget{{
				Kind: storage.PaymentReviewTargetAnomaly, ID: 1, ReasonCode: "late_capture",
			}},
		}, []string{payReviewActionSettle, payReviewActionRefund, payReviewActionDismiss}},
		{"unknown provider keeps dismiss", storage.PaymentReviewCase{
			OrderID: 3, Provider: storage.PaymentReviewProviderUnknown, PaymentState: storage.PaymentStateNeedsReview,
			Targets: []storage.PaymentReviewTarget{{
				Kind: storage.PaymentReviewTargetOrder, ID: 3, ReasonCode: "order_needs_review",
			}},
		}, []string{payReviewActionDismiss}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := payReviewActions(tc.item)
			if len(got) != len(tc.want) {
				t.Fatalf("actions = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("actions = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
```

Run: `go test ./internal/bot/ -run TestPayReviewOrphanCardActionSets -count=1`
Expected: FAIL (compile error once Step 3's helper is referenced, or wrong action sets pre-change).

- [ ] **Step 3: Implement the filter + helpers**

Replace `payReviewActions` (admin_payreview.go:244-253):

```go
// payReviewActions lists the actions offered on a case card. A provider-neutral
// row admits only terminal dismissal. Orphan cards (no local order) offer only
// the actions that can actually pass against the storage decision gates
// (payment_resolutions.go): digest-only facts fail every decision, path-5
// refund orphans pass Refund pre-recovery and Dismiss post-recovery, other
// capture orphans pass Settle (compensated). Attached cases keep the three
// candidate projections — the preview validates them against ledger evidence.
// This is a UX filter, never a gate: storage remains the final validator, and
// a filtered-out action that storage would accept is a bug, not a policy.
func payReviewActions(item storage.PaymentReviewCase) []string {
	if item.Provider == storage.PaymentReviewProviderUnknown {
		return []string{payReviewActionDismiss}
	}
	if isPayReviewOrphanAnomaly(item) {
		reason := item.Targets[0].ReasonCode
		switch {
		case payReviewIsRefundLedgerFailureOrphan(item):
			return []string{payReviewActionRefund, payReviewActionDismiss}
		case reason == "webhook_parse_failure" || reason == "webhook_missing_payment_id":
			return nil // digest-only: every decision provably conflicts — CLI-only card
		default:
			return []string{payReviewActionSettle}
		}
	}
	return []string{payReviewActionSettle, payReviewActionRefund, payReviewActionDismiss}
}

// isPayReviewOrphanAnomaly reports the single-anomaly orphan card shape (no
// local order — findReviewCase addresses it by the anomaly disambiguator).
func isPayReviewOrphanAnomaly(item storage.PaymentReviewCase) bool {
	return item.PaymentState == "" && len(item.Targets) == 1 &&
		item.Targets[0].Kind == storage.PaymentReviewTargetAnomaly
}

// payReviewIsRefundLedgerFailureOrphan reports a path-5 refund-ledger-failure
// orphan card (admin_refunds.go writes it with the reason grammar
// refund_ledger_failure:order=<id>).
func payReviewIsRefundLedgerFailureOrphan(item storage.PaymentReviewCase) bool {
	return isPayReviewOrphanAnomaly(item) &&
		strings.HasPrefix(item.Targets[0].ReasonCode, "refund_ledger_failure:")
}
```

In `sendPayReviewCard`, replace the keyboard-build block (:294-302) and add the hint lines after the targets loop (:292):

```go
	actions := payReviewActions(item)
	if len(actions) == 0 {
		sb.WriteString(b.t(lang, "admin_payreview_card_cli_only"))
	} else if payReviewIsRefundLedgerFailureOrphan(item) {
		sb.WriteString(b.t(lang, "admin_payreview_card_trap"))
	}

	kb := StyledKeyboard{}
	row := []StyledButton{}
	for _, action := range actions {
		data := payReviewCaseCallback("admin:payrev:", action, item)
		// Telegram rejects callback data longer than 64 bytes. Realistic
		// payloads stay well inside (attached worst case
		// admin:payrev:dismiss:nowpayments:<int64> = 52; orphan order
		// components are local ids) — only a detached 19-digit provider order
		// id plus a large anomaly id could exceed it. An unaddressable action
		// is worse than no action: drop the row and point at the CLI.
		if len(data) > 64 {
			row = nil
			sb.WriteString(b.t(lang, "admin_payreview_card_cli_only"))
			break
		}
		row = append(row, Btn(b.payReviewActionLabel(lang, action), data))
	}
	if len(row) > 0 {
		kb = append(kb, row)
	}
	kb = append(kb, []StyledButton{Btn(b.t(lang, "admin_payreview_back_btn"), "admin:payrev:list")})
	b.sendOrEditStyled(chatID, msgID, sb.String(), "", kb)
```

Also extend the `payReviewCaseCallback` doc comment (:66-68) with the budget note:

```go
// payReviewCaseCallback builds the callback data addressing one case. Orphan
// anomalies share their proposed order ID with siblings, so they carry their
// anomaly target ID as a disambiguator. Byte budget: Telegram caps callback
// data at 64 bytes — sendPayReviewCard degrades a card whose action callbacks
// would exceed the cap to the CLI-only hint (fail-closed).
```

- [ ] **Step 4: Write the e2e card-render legs (RED before Step 3, GREEN after)**

```go
// TestPayReviewPathFiveCardWarnsAndFiltersActions pins ruling P4 on the real
// path-5 card shape: the trap warning renders, Refund+Dismiss are offered,
// Settle is not.
func TestPayReviewPathFiveCardWarnsAndFiltersActions(t *testing.T) {
	e := newE2EEnv(t)
	store := storage.NewSQLOrderStore(e.db)
	err := store.RecordPaymentAnomaly(context.Background(), storage.PaymentAnomaly{
		Provider:          storage.PaymentMethodBalance,
		EventKind:         storage.PaymentEventRefunded,
		ExternalID:        "balance-refund:7",
		RelatedExternalID: "balance:7",
		PayerID:           42,
		AmountMinor:       525,
		Currency:          "USD",
		Scale:             2,
		Reason:            "refund_ledger_failure:order=7",
		RawPayload:        `{"order_id":7,"rail":"balance","refund_id":"balance-refund:7"}`,
	})
	if !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("seed path-5 orphan: %v", err)
	}
	var anomalyID int64
	if err := e.db.Conn().QueryRow(`SELECT id FROM payment_anomalies`).Scan(&anomalyID); err != nil {
		t.Fatal(err)
	}

	calls := e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:balance:0:%d", anomalyID), "en")
	if got := tgText(calls); !strings.Contains(got, e.bot.t("en", "admin_payreview_card_trap")) {
		t.Fatalf("card lacks the trap warning:\n%s", got)
	}
	var markup string
	for _, c := range calls {
		if m := c.markup(); m != "" {
			markup = m
		}
	}
	if !strings.Contains(markup, fmt.Sprintf("admin:payrev:refund:balance:0:%d", anomalyID)) ||
		!strings.Contains(markup, fmt.Sprintf("admin:payrev:dismiss:balance:0:%d", anomalyID)) {
		t.Fatalf("card lacks Refund/Dismiss: %s", markup)
	}
	if strings.Contains(markup, "admin:payrev:settle:") {
		t.Fatalf("path-5 card must not offer Settle: %s", markup)
	}
}

// TestPayReviewDigestOrphanCardIsCLIOnly pins ruling P4's digest-only leg:
// the card carries the CLI-only hint and no action buttons at all.
func TestPayReviewDigestOrphanCardIsCLIOnly(t *testing.T) {
	e := newE2EEnv(t)
	store := storage.NewSQLOrderStore(e.db)
	// The exact digest shape webhook.go:186-189 writes for an unparseable
	// body: provider + sha256 payload + reason, zero money tuple, no ids.
	digest := sha256.Sum256([]byte("not json"))
	err := store.RecordPaymentAnomaly(context.Background(), storage.PaymentAnomaly{
		Provider:   storage.PaymentMethodYooKassa,
		RawPayload: fmt.Sprintf("sha256:%x", digest),
		Reason:     "webhook_parse_failure",
	})
	if !errors.Is(err, storage.ErrPaymentNeedsReview) {
		t.Fatalf("seed digest orphan: %v", err)
	}
	var anomalyID int64
	if err := e.db.Conn().QueryRow(`SELECT id FROM payment_anomalies`).Scan(&anomalyID); err != nil {
		t.Fatal(err)
	}

	calls := e.cb(e2eAdminID, fmt.Sprintf("admin:payrev:yookassa:0:%d", anomalyID), "en")
	if got := tgText(calls); !strings.Contains(got, e.bot.t("en", "admin_payreview_card_cli_only")) {
		t.Fatalf("card lacks the CLI-only hint:\n%s", got)
	}
	var markup string
	for _, c := range calls {
		if m := c.markup(); m != "" {
			markup = m
		}
	}
	if strings.Contains(markup, "admin:payrev:settle:") || strings.Contains(markup, "admin:payrev:refund:") ||
		strings.Contains(markup, "admin:payrev:dismiss:") {
		t.Fatalf("digest-only card must offer no actions: %s", markup)
	}
	if !strings.Contains(markup, "admin:payrev:list") {
		t.Fatalf("digest-only card lost its Back button: %s", markup)
	}
}
```

(`crypto/sha256` import: add to admin_payreview_test.go if absent. `storage.PaymentEventRefunded`, `storage.NewSQLOrderStore`, `context` are already used in this file.)

Run: `go test ./internal/bot/ -run 'TestPayReviewPathFiveCard|TestPayReviewDigestOrphanCard|TestPayReviewOrphanCardActionSets' -count=1`
Expected after Step 3: PASS.

- [ ] **Step 5: Callback byte-budget unit pin**

```go
// TestPayReviewCallbackByteBudget documents the 64-byte budget (ruling P6):
// the realistic attached worst case fits; the theoretical detached worst case
// (19-digit provider order id + large anomaly id) exceeds it and is degraded
// to the CLI-only card by sendPayReviewCard's guard.
func TestPayReviewCallbackByteBudget(t *testing.T) {
	attached := storage.PaymentReviewCase{
		OrderID: math.MaxInt64, Provider: storage.PaymentMethodNowpayments,
		PaymentState: storage.PaymentStateNeedsReview,
		Targets: []storage.PaymentReviewTarget{{
			Kind: storage.PaymentReviewTargetAnomaly, ID: 1, ReasonCode: "receipt_mismatch",
		}},
	}
	if got := payReviewCaseCallback("admin:payrev:", payReviewActionDismiss, attached); len(got) > 64 {
		t.Fatalf("attached worst case %d bytes > 64: %s", len(got), got)
	}
	detached := storage.PaymentReviewCase{
		OrderID: math.MaxInt64, Provider: storage.PaymentMethodNowpayments, PaymentState: "",
		Targets: []storage.PaymentReviewTarget{{
			Kind: storage.PaymentReviewTargetAnomaly, ID: math.MaxInt64, ReasonCode: "receipt_mismatch",
		}},
	}
	if got := payReviewCaseCallback("admin:payrev:", payReviewActionDismiss, detached); len(got) <= 64 {
		t.Fatalf("detached worst case %d bytes unexpectedly fits — re-check the guard rationale: %s", len(got), got)
	}
}
```

(`math` import: add if absent.)

- [ ] **Step 6: Regression sweep + full gates + commit**

Run: `go test ./internal/bot/ -run 'TestPayReview|TestAdminPayReview|TestE2EPayreview|TestAdminRefundLedgerFailure|TestLocaleFiles|TestBotLocaleFiles' -count=1`
Expected: PASS — notably `TestPayReviewOrphanAnomalyResolvesByExactTarget` (capture orphan: settle callback still rendered and its two-tap flow drives callbacks directly, not buttons) and `TestAdminRefundLedgerFailureRecordsDurableAnomaly` (list-level line unchanged). Then full gates.

```bash
git add internal/bot/admin_payreview.go internal/bot/admin_payreview_test.go locales/de.json locales/en.json locales/es.json locales/ru.json locales/zh.json
git commit -m "feat(bot): /payreview orphan cards get truthful action sets, trap and CLI-only hints, 64-byte guard (HANDOFF 6.9)"
```

---

### Task 9: micro-renames — stale test name + poller warn prefix (HANDOFF §6.11 + §6.14, rulings P11/P12)

Also folds ruling P10 (§6.15) — see Step 3; both are one-line-scale renames/fallbacks with unit pins, one review seat.

**Files:**
- Modify: `internal/storage/payment_ingress_audit_test.go:20` (rename)
- Modify: `worker/yookassa_polling.go:113` (prefix)
- Modify: `internal/bot/ui_text.go:52-71` (+ new plain variant), `internal/bot/admin_orders.go:115`
- Test: `internal/bot/ui_text_test.go` (extend `TestPaymentMethodText_AllProvidersLocalized`)

**Interfaces:**
- Consumes: nothing new. Produces: `func (b *Bot) paymentMethodTextPlain(lang, method string) string`.

- [ ] **Step 1: Rename the stale test (P11)**

`internal/storage/payment_ingress_audit_test.go:20`:

```go
func TestAppendPaymentIngressAuditAcceptsYooKassa(t *testing.T) {
```

→

```go
// The matrix this test actually exercises: yookassa, stars, stripe, and the
// sepa fail-closed rejection — the name was YooKassa-scoped historically.
func TestAppendPaymentIngressAuditAcceptsYooKassaStarsStripe(t *testing.T) {
```

Verify no other references: `grep -rn TestAppendPaymentIngressAuditAcceptsYooKassa . --exclude-dir=.git --exclude-dir=.superpowers` must return only this site afterwards (docs/plans historical mentions stay — historical record, money-followups ruling C precedent).

- [ ] **Step 2: Rename the poller warn prefix (P12)**

`worker/yookassa_polling.go:113`:

```go
		slog.Warn("yookassa poller: page cap reached, tail deferred to next tick",
```

→

```go
		slog.Warn("YooKassa polling: page cap reached, tail deferred to next tick",
```

(The pins at yookassa_polling_test.go:413/:426 assert the prefix-agnostic substring `"page cap reached"` — verified safe.)

- [ ] **Step 3: Plain-text payment-method fallback (P10, §6.15)**

Write the failing pin first — extend `TestPaymentMethodText_AllProvidersLocalized` (ui_text_test.go, after the escaped-fallback assert :64-67):

```go
	// The plain-text variant shares every localized value but keeps an unknown
	// method RAW: the admin /order card has no parse mode, so HTML entities
	// there would be visible garbage.
	if got := b.paymentMethodTextPlain("en", storage.PaymentMethodTON); got != b.paymentMethodText("en", storage.PaymentMethodTON) {
		t.Errorf("plain variant diverged for a known method: %q", got)
	}
	if got := b.paymentMethodTextPlain("en", "we<ird>"); got != "we<ird>" {
		t.Errorf("plain unknown fallback = %q, want raw", got)
	}
```

Run: `go test ./internal/bot/ -run TestPaymentMethodText -count=1` → FAIL (compile error, `paymentMethodTextPlain` undefined).

Implement in `ui_text.go` — refactor the shared switch (after `paymentMethodText`, :71):

```go
// paymentMethodTextPlain is paymentMethodText for plain-text surfaces (the
// admin /order card renders without a parse mode): localized values are
// identical, but an unknown method falls back to the RAW string instead of
// HTML entities that would be visible garbage. Reachability is legacy-only —
// orders.payment_method is CHECK-constrained to the implemented providers.
func (b *Bot) paymentMethodTextPlain(lang, method string) string {
	if text, ok := b.paymentMethodLocalized(lang, method); ok {
		return text
	}
	return method
}
```

and restructure `paymentMethodText` to share the lookup:

```go
func (b *Bot) paymentMethodText(lang, method string) string {
	if text, ok := b.paymentMethodLocalized(lang, method); ok {
		return text
	}
	return escapeHTML(method)
}

// paymentMethodLocalized maps an implemented provider key to its localized
// display name; ok=false means the method is unknown to the app layer.
func (b *Bot) paymentMethodLocalized(lang, method string) (string, bool) {
	switch method {
	case storage.PaymentMethodStars:
		return b.t(lang, "payment_method_stars"), true
	case storage.PaymentMethodCrypto:
		return b.t(lang, "payment_method_crypto"), true
	case storage.PaymentMethodYooKassa:
		return b.t(lang, "payment_method_yookassa"), true
	case storage.PaymentMethodStripe:
		return b.t(lang, "payment_method_stripe"), true
	case storage.PaymentMethodTON:
		return b.t(lang, "payment_method_ton"), true
	case storage.PaymentMethodNowpayments:
		return b.t(lang, "payment_method_nowpayments"), true
	case storage.PaymentMethodBalance:
		return b.t(lang, "payment_method_balance"), true
	default:
		return "", false
	}
}
```

Switch the plain-text card call site — `admin_orders.go:115`:

```go
		sb.WriteString(fmt.Sprintf(b.t(lang, "admin_order_card_method"), b.paymentMethodTextPlain(lang, order.PaymentMethod)))
```

`orderStatusText`'s identical fallback (ui_text.go:47-49) is verified HTML-context-only (sole caller formatOrdersText :198 → handlers_orders.go:34 parse mode "HTML") — deliberately unchanged.

- [ ] **Step 4: Run affected suites — GREEN**

Run: `go test ./internal/storage/ -run TestAppendPaymentIngressAudit -count=1 && go test ./worker/ -run TestYooKassaPolling -count=1 && go test ./internal/bot/ -run 'TestPaymentMethodText|TestFormatAdminOrderCard|AdminOrder' -count=1`
Expected: PASS. (If `worker` test names differ, run `go test ./worker/ -count=1` — the package is small.)

- [ ] **Step 5: Full gates + commit**

```bash
git add internal/storage/payment_ingress_audit_test.go worker/yookassa_polling.go internal/bot/ui_text.go internal/bot/admin_orders.go internal/bot/ui_text_test.go
git commit -m "chore: micro-renames (stale test name, poller warn prefix) + plain-text payment-method fallback (HANDOFF 6.11/6.14/6.15)"
```

---

### Task 10: Docs sweep — CHANGELOG entry + HANDOFF §6 annotations

**Files:**
- Modify: `CHANGELOG.md` ([Unreleased] → Quality, after the «Money-followups batch» entry)
- Modify: `docs/superpowers/HANDOFF.md` §6 (annotate items 2, 3, 4, 5, 8, 9, 10, 11, 14, 15 — ✅ + date + this plan's path; do NOT renumber; items 12 and 16 stay open)

**Interfaces:**
- Consumes: the merged state of Tasks 1-9. Produces: nothing.

- [ ] **Step 1: CHANGELOG Quality entry (house style: behavior + why + operator impact)**

Add ONE entry under `### Quality` in `[Unreleased]`, after the «Money-followups batch (HANDOFF §6)» entry:

```markdown
- **Polish follow-ups batch (HANDOFF §6)** — coverage, diagnostics and operator-UX polish across the payment surfaces. YooKassa `GetPayment` gains its missing fail-closed coverage: invalid payment IDs are rejected before any HTTP call, non-2xx API errors and undecodable bodies surface their real causes, and the `toPayment` normalization branches (invalid body IDs, missing/unparsable `order_id` metadata, timestamp fallbacks, lowercase currencies) are pinned. `doctor`'s environment overlay is mutation-verified for the three crypto keys that only had list-membership (`TON_API_KEY`, `NOWPAYMENTS_IPN_SECRET`, `NOWPAYMENTS_RETURN_URL`): process-env-only configuration must fully light up the TON and NOWPayments rails. `/paystatus` now trims every credential exactly like `doctor` does — whitespace-only values render OFF instead of a misleading ON/WARN — and its last hardcoded English assertion derives from the locale file. The Stripe and NOWPayments webhook-replay tests re-assert the persisted `payment_state`, completing the replay-pin symmetry with YooKassa. Stars subscription renewals log their settlement with the structured `actor=webhook:stars` field (docs §12 gains the row) — the last settle path outside the ruled actor enumeration. `/payreview` ergonomics: a case that left the queue between taps answers with its own case-gone message instead of the generic conflict text; orphan cards offer only the actions that can actually pass (digest-only cards point to the CLI, path-5 `refund_ledger_failure` cards keep Refund+Dismiss and carry the pre-recovery trap warning of docs §11); a 64-byte callback-data guard degrades an unaddressable card to the CLI hint; and the ru «Подтвердить»/«Подтвердить» button collision is resolved («Урегулировать» for Settle). The quarantine-table intros in docs §5/§7 now name the writing mechanism per path (webhook anomaly rows vs poller needs_review captures). Housekeeping: the multi-provider ingress-audit test loses its stale YooKassa-only name, the YooKassa poller's page-cap warning uses the file's `YooKassa polling:` prefix, and the plain-text admin `/order` card keeps an unknown payment method raw instead of HTML-escaping it.
```

- [ ] **Step 2: HANDOFF §6 annotations**

Append to each closed item (roadmap-§4 annotation style, no renumbering): `✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-polish-followups.md`
- Item 2 → after "веток." 
- Item 3 → after "(только list-membership)."
- Item 4 → replace the trailing "→ план polish-followups." of its existing annotation with "— остаток закрыт 21.09.2026, план docs/superpowers/plans/2026-09-21-polish-followups.md (симметричные пины stripe/nowpayments)."
- Item 5 → after "(уточнить формулировку)."
- Item 8 → after "(вне ruled enumeration)."
- Item 9 → append: "✅ закрыто 21.09.2026 (case-gone сообщение, фильтр кнопок orphan-карточек + trap/CLI-only подсказки, 64-байт гард, ru-ренэйм), план docs/superpowers/plans/2026-09-21-polish-followups.md; CLI safeReviewCode-расхождение осталось в §6.16."
- Item 10 → note the corrected attribution in the annotation: "✅ закрыто 21.09.2026 (фактически admin_paystatus.go, не doctor — ruling P2), план docs/superpowers/plans/2026-09-21-polish-followups.md"
- Item 11 → after "(покрывает stripe+sepa)."
- Item 14 → after "`YooKassa polling:`."
- Item 15 → after "(косметика)."

Items 12 (TON re-scans, deferred by its own note) and 16 (safeReviewCode, parked) stay OPEN and untouched.

- [ ] **Step 3: Verify + gates + commit**

Verify every annotation renders inside its item (no renumbering, list intact): `grep -n "✅ закрыто 21.09.2026" docs/superpowers/HANDOFF.md` shows 4 (money-followups) + 10 (this plan) annotations. Full gates (docs-only, must stay green).

```bash
git add CHANGELOG.md docs/superpowers/HANDOFF.md
git commit -m "docs: CHANGELOG + HANDOFF annotations for the polish-followups batch"
```

---

## Self-Review Notes

**Spec coverage:** §6.2 → Task 1; §6.3 → Task 2; §6.10 → Task 3; §6.4 residual → Task 4; §6.5 → Task 5; §6.8 → Task 6; §6.9 → Tasks 7+8 (+§6.16 parked by controller ruling, out of scope); §6.11 → Task 9.1; §6.14 → Task 9.2; §6.15 → Task 9.3; CHANGELOG/HANDOFF sweep → Task 10. §6.12 (TON re-scans) remains deferred by its own note; §6.1/6/7/13 closed by the money-followups plan.

**Known ambiguities (implementer decisions allowed, record in report):**
- Task 3: if `newE2EEnvWithConfig` rejects whitespace-only credentials at construction (it should not — validation lives in doctor/config-load, not the bot constructor), adapt the seeding and record how.
- Task 6: `bytes` import addition; if the renewal E2E leg's DB asserts fail before the log assert, STOP and report (renewal-semantics assumption wrong).
- Task 8: if the digest-orphan `RecordPaymentAnomaly` seed is rejected by a storage validator (it mirrors webhook.go's exact shape, so it must not), fall back to driving the real yookassa webhook with an unparseable body (yookassa_webhook_test.go:460-475 pattern) and record the deviation.
- Task 9: exact `worker` test-run filter (package is small — full `go test ./worker/` is acceptable).

**Out of scope (explicitly):** HANDOFF §6.12 (TON re-scan window, deferred by note); §6.16 (CLI `safeReviewCode` `=`→`_` vs bot raw reason — parked, consider with future §6.9 follow-ons); roadmap 4.14 (per-update ctx chain) and 4.15 (durable actor column); storage decision-gate changes for the pre-recovery Refund trap (deliberately docs+UX only — money-followups ruling F); buyer-facing renewal notification behavior (unchanged by design — the renewal leg has never sent messages); `orderStatusText` fallback (verified HTML-only); any migration or dependency.

**Type consistency:** `payReviewActions(item storage.PaymentReviewCase) []string` — same signature as today; `paymentMethodTextPlain(lang, method string) string` and `paymentMethodLocalized(lang, method string) (string, bool)` — consistent across Task 9's three sites; `successfulPaymentRenewal(userID int64, payload string, totalStars int, chargeID string) []tgCall` — mirrors `successfulPayment` exactly; locale keys `admin_payrev_case_gone`, `admin_payreview_card_trap`, `admin_payreview_card_cli_only` — identical names in all five files and at every `b.t` call site; reason-grammar strings `refund_ledger_failure:` / `webhook_parse_failure` / `webhook_missing_payment_id` identical in code, tests and docs.

**Conflict scan (sequential tasks, one implementer each):** T7 and T8 both edit `admin_payreview.go` + locales — DISJOINT hunks (T7: error branches :272-277/:319-326/:373-375 + key `admin_payrev_case_gone`; T8: `payReviewActions`/card body/callback comment + trap/cli_only keys) — sequence T7 → T8. T5 and T6 both edit `docs/payment-operations.md` — disjoint sections (§5/§7 vs §12). T6 edits `e2e_test.go` (helper + lifecycle test); T4 edits the stripe/nowpayments webhook test files; T8 edits `admin_payreview_test.go` — no overlap. All tasks touch disjoint production files otherwise. Locale gates (`TestLocaleFiles*`) run in every task's full gates, so a T7 key-set mistake cannot silently cascade into T8.

**Review Focus mapping:** RF1 → Task 8 Steps 2/4; RF2 → Task 3 Step 1; RF3 → Task 6 Step 2; RF4 → Task 4 Steps 1/2; RF5 → Task 1 Steps 1-3.
