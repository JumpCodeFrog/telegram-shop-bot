# Money Follow-Ups Implementation Plan (HANDOFF §6: items 13, 1, 7, 6)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the four money-bearing FOLLOW-UP items from `docs/superpowers/HANDOFF.md` §6: the TON quotient overflow residual (13), the NOWPayments canonicalizer escape-pin (1), the balance-refund amount-divergent fail-closed guard (7), and the refunds path-5 durable trace (6).

**Architecture:** Four surgical changes on existing flows — two local guards (service/payment), one storage-probe upgrade + bot guard (balance refund), one best-effort durable anomaly write on the refund path-5 failure window (bot). No migrations, no new dependencies, no new i18n keys.

**Tech Stack:** Go (module `shop_bot`), SQLite ledger (migrations 017–022), tgbotapi v5, house e2e harness (`internal/bot/e2e_test.go`).

**Spec:** `docs/superpowers/HANDOFF.md` §6 items 13/1/7/6 + `.superpowers/sdd/2026-09-21-admin-refunds/progress.md` triage dispositions («path-5 durable-visibility FOLLOW-UP», «amount-divergent balance re-run = optional hardening FOLLOW-UP»). Design rulings below are part of the spec.

**Branch:** `chore/money-followups`, base `main@5539a64`. Ledger: `.superpowers/sdd/2026-09-21-money-followups/progress.md`.

## Global Constraints

- Money in minor units at every boundary; ledger immutable (facts never edited — quarantine + resolution only).
- NO new dependencies; NO new migrations; NO new i18n keys (`TestLocaleFilesHaveMatchingPrintfVerbs` and locale-file parity stay green untouched).
- `refundableOrder`'s settled-only gate and the `order_refund:<orderID>` identity are LOAD-BEARING — this plan hardens around them, never relaxes them.
- Provider-first → ledger-second ordering ruling stays as is.
- Gates per task (storage is slow, ~100s — allow generous timeouts):
  `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/` (EMPTY output) `&& go test ./...`
- One commit per task. No push, no external side effects.

## Design Rulings (controller, pre-verified against code)

- **R1 (§6.6 shape):** the path-5 durable trace is an ORPHAN anomaly row (`ProposedOrderID=0`, no order-state flip). A `needs_review` flip (what `recordPaymentAnomaly` does for `ProposedOrderID>0`) would block the documented `/refund` re-run remedy behind the settled-only gate (`refundableOrder`, admin_refunds.go:76). The order link rides in `Reason` (`refund_ledger_failure:order=<id>`) and `RawPayload` JSON instead.
- **R2 (§6.6 visibility):** `balance` joins the bot's `payReviewProviders` (admin_payreview.go:25) — storage `ListPaymentReviews` already accepts it (payment_resolutions.go:16); without this the balance-rail card would be invisible in `/payreview`.
- **R3 (§6.7 API):** `BalanceStore.BalanceTxExists` is REPLACED by `BalanceTxTotal(ctx, userID, txType) (totalUSD float64, found bool, err error)` — one probe returns existence + net amount (SUM, mirroring `OrderBalanceNet`'s net-effect philosophy). Sole production caller is `executeRefund`; the LOAD-BEARING coupling comments travel to the new method (all three sites: interface doc, storage doc, executeRefund branch).
- **R4 (§6.7 placement):** the divergence guard lives in `executeRefund`'s balance branch (the rail's "provider step") — a divergent re-run surfaces through the EXISTING `admin_refund_provider_failed` message with the error text; nothing is credited, nothing is recorded, order stays settled.
- **R5 (§6.6 messages):** operator guidance messages are UNCHANGED (they already carry the exact remedy); the durable card is documented in `docs/payment-operations.md` §11 + CHANGELOG.
- **R6 (§6.6 idempotency):** `RawPayload` is deterministic (`order_id`/`rail`/`refund_id` only — NO error text), so repeated path-5 failures for the same refund reuse ONE anomaly row (fingerprint-stable, `INSERT OR IGNORE` + legacy-reuse path in `recordPaymentAnomaly`).
- **R7 (rejected alternative):** durable outbox / new table rejected — migration for a Low-impact follow-up is disproportionate (4.13/4.15 ruling precedent).

## Review Focus

1. Amount-divergent balance re-run after a ledger failure ⇒ fail-closed: no ledger row, prior credit untouched, order stays `settled`, error names both amounts (Task 3).
2. Same-amount balance re-run ⇒ still completes exactly once: credit skipped, ledger records, state flips (Task 3 regression over the existing `TestAdminRefundLedgerFailure*` pins).
3. IPN bodies containing `<`, `>`, `&` ⇒ canonical bytes keep them RAW; an HTML-escaping regression must fail the pins (Task 2, mutation-verified).
4. Finite TON quotient ≥ 2^63 ⇒ `ConvertUSDToNanoTON` returns 0, never `int64` conversion garbage (Task 1).
5. Path-5 must NOT flip the order to `needs_review` (would kill the re-run remedy), and a FAILED anomaly write must not change the guidance message or panic (Task 4).

---

### Task 1: ConvertUSDToNanoTON — finite quotient ≥ 2^63 guard (HANDOFF §6.13)

**Files:**
- Modify: `internal/service/exchange.go:84-115` (package-level `ConvertUSDToNanoTON`)
- Test: `internal/service/exchange_test.go` (`TestConvertUSDToNanoTON` table, lines 43-81)

**Interfaces:**
- Consumes: nothing new.
- Produces: unchanged signature `func ConvertUSDToNanoTON(usd, usdPerTon float64) int64` — only the guard set widens.

- [ ] **Step 1: Write the failing test leg**

Add to the `TestConvertUSDToNanoTON` table (after the `{name: "overflowing quotient", ...}` row, matching its comment style):

```go
		// Finite operands, finite product (1e12*1e9 = 1e21) and a FINITE
		// quotient (1e21/1e-12 = 1e33) — but the quotient is >= 2^63, so
		// int64(math.Round(q)) is platform garbage (MinInt64 on amd64),
		// not a shippable amount. Same absurd-operator-config class as the
		// two rows above.
		{name: "finite quotient above MaxInt64", usd: 1e12, usdPerTon: 1e-12, want: 0},
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/service/ -run TestConvertUSDToNanoTON -v`
Expected: FAIL — the new leg returns `-9223372036854775808` (amd64 `int64(1e33)` conversion garbage), want `0`.

- [ ] **Step 3: Implement the guard**

In `internal/service/exchange.go`, extend the quotient guard (currently `if math.IsNaN(q) || math.IsInf(q, 0) { return 0 }` at lines 111-113) and its comment:

```go
	q := usd * 1e9 / usdPerTon
	// Defense against absurd operator configs: even with both operands (and
	// the product above) finite, the quotient itself can overflow — e.g.
	// usd=1e200 at usdPerTon=1e-200 gives +Inf — and int64(+Inf) is platform
	// garbage, not a runaway amount we can ship. A FINITE quotient at or
	// above 2^63 overflows int64 the same way: float64(math.MaxInt64) rounds
	// to exactly 2^63, the largest float below it converts cleanly, so this
	// one comparison is exact and complete. NaN is unreachable for finite
	// positive operands; guarded anyway so the conversion can never emit a
	// non-number.
	if math.IsNaN(q) || math.IsInf(q, 0) || q >= float64(math.MaxInt64) {
		return 0
	}
	return int64(math.Round(q))
```

Also update the function doc comment (lines 84-88): change "…or the amount is non-positive…" wording so it reads that 0 is returned for non-positive, NaN or infinite inputs, for finite inputs whose product overflows float64, **and for finite inputs whose quotient reaches int64 overflow (≥ 2^63)**.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/service/ -v -run 'TestConvertUSDToNanoTON|TestExchangeService'`
Expected: PASS (all table legs, including the pre-existing overflow rows).

- [ ] **Step 5: Full gates + commit**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`
Expected: build/vet clean, gofmt EMPTY, all packages ok.

```bash
git add internal/service/exchange.go internal/service/exchange_test.go
git commit -m "fix(service): ConvertUSDToNanoTON guards finite quotients at/above 2^63 (HANDOFF 6.13)"
```

---

### Task 2: NOWPayments canonicalizer no-HTML-escape pins (HANDOFF §6.1)

Test-only task: the property `enc.SetEscapeHTML(false)` (nowpayments.go:165) is currently UNPINNED — deleting that line keeps the whole suite green while production fails closed (genuine IPNs containing `<`, `>` or `&` would never verify). Pins below make the regression loud.

**Files:**
- Test: `internal/payment/nowpayments_test.go` (extend `TestNowpaymentsVerifyIPNSignature` at line 170; new top-level canonicalizer test)

**Interfaces:**
- Consumes: `canonicalizeNowpaymentsIPN(body []byte) ([]byte, error)` and `(*NowpaymentsPayment).VerifyIPNSignature(header string, body []byte) error` — both existing, unchanged.
- Produces: nothing (test-only).

- [ ] **Step 1: Write the direct canonicalizer pin**

New top-level test (place next to `TestNowpaymentsVerifyIPNSignature`):

```go
func TestCanonicalizeNowpaymentsIPNKeepsHTMLCharactersRaw(t *testing.T) {
	// Pins the SetEscapeHTML(false) property DIRECTLY: <, > and & must stay
	// raw in the canonical form (keys sorted recursively, compact, no
	// trailing newline). Go's json.Encoder escapes them as \u003c \u003e
	// \u0026 by default; a regression to the default would compute a
	// different HMAC than NOWPayments' signer over any IPN body containing
	// these characters — fail-closed in production (genuine IPNs never
	// verify), and invisible to the signature tests below without this pin.
	got, err := canonicalizeNowpaymentsIPN([]byte(`{"b":"<b>&","a":[1,{"z":"<p>&x"}]}`))
	if err != nil {
		t.Fatalf("canonicalizeNowpaymentsIPN: %v", err)
	}
	const want = `{"a":[1,{"z":"<p>&x"}],"b":"<b>&"}`
	if string(got) != want {
		t.Fatalf("canonical = %s, want %s", got, want)
	}
}
```

- [ ] **Step 2: Write the signature-level pin**

Add a leg INSIDE `TestNowpaymentsVerifyIPNSignature` (after the existing `"valid signature against hand-pinned canonical bytes"` leg at line 182, reusing the same `client`):

```go
	t.Run("valid signature over hand-pinned canonical bytes with raw HTML characters", func(t *testing.T) {
		// Hand-pinned canonical form (NOT via canonicalizeNowpaymentsIPN),
		// same discipline as the leg above, extended with the HTML-escape
		// property: raw <, > and & stay raw. An escaping regression makes
		// the canonicalizer's HMAC differ from this pin.
		pinnedBody := []byte(`{"product_name":"<b>&","payment_status":"finished"}`)
		const wantCanonical = `{"payment_status":"finished","product_name":"<b>&"}`
		mac := hmac.New(sha512.New, []byte(nowpaymentsTestIPNSecret))
		mac.Write([]byte(wantCanonical))
		header := hex.EncodeToString(mac.Sum(nil))
		if err := client.VerifyIPNSignature(header, pinnedBody); err != nil {
			t.Fatalf("expected valid signature over the raw-HTML canonical bytes, got %v", err)
		}
	})
```

- [ ] **Step 3: Run the new tests to verify they pass against correct code**

Run: `go test ./internal/payment/ -run 'Nowpayments' -v`
Expected: PASS (this is a pin of existing correct behavior — RED comes from the mutation check next).

- [ ] **Step 4: Mutation-verify the pins (RED evidence)**

Temporarily comment out `enc.SetEscapeHTML(false)` (nowpayments.go:165).
Run: `go test ./internal/payment/ -run 'Nowpayments'`
Expected: FAIL — BOTH new legs fail (canonical form becomes `"\u003cb\u003e\u0026"`). Restore the line, re-run, PASS. Record the mutation evidence in the task report.

- [ ] **Step 5: Full gates + commit**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`
Expected: clean/empty/ok.

```bash
git add internal/payment/nowpayments_test.go
git commit -m "test(payment): pin NOWPayments IPN canonicalizer no-HTML-escape property (HANDOFF 6.1)"
```

---

### Task 3: Balance refund — amount-divergent re-run fails closed (HANDOFF §6.7)

Today: after a partial balance refund whose ledger record failed, a re-run with a DIFFERENT amount skips the credit (identity exists) yet records the new amount — books claim money that never moved. Mitigation so far was guidance-only (the recovery message quotes the exact amount). This task enforces it: probe returns existence + amount; divergence ⇒ error before any write.

**Files:**
- Modify: `internal/storage/interfaces.go:113-117` (BalanceStore — replace `BalanceTxExists` with `BalanceTxTotal`)
- Modify: `internal/storage/balance.go:52-75` (SQLBalanceStore implementation + doc)
- Test: `internal/storage/balance_test.go:224-251` (replace `TestSQLBalanceStoreBalanceTxExists`)
- Modify: `internal/shop/balance_test.go:417-429` (`mockBalanceStore` — replace `BalanceTxExists`, migrate `txTypes map[string]bool` usages)
- Modify: `internal/bot/admin_refunds.go:589-616` (`executeRefund` balance branch + LOAD-BEARING comment)
- Test: `internal/bot/admin_refunds_test.go` (new `TestAdminRefundBalanceDivergentRerunFailsClosed`)
- Modify: `docs/payment-operations.md` §11 (balance re-run paragraph)

**Interfaces:**
- Consumes: `storage.PaymentMethodBalance`, `plan.fact.AmountMinor` (int64 minor units), existing e2e helpers (`newE2EEnv`, `seedRefundOrder`, `failingRefundLedger`, `refundCBData`, `refundFullUSD`, `assertRefundRow`, `tgText`, `e.qInt`, `e.qStr`).
- Produces: `BalanceTxTotal(ctx context.Context, userID int64, txType string) (totalUSD float64, found bool, err error)` on `storage.BalanceStore` (REPLACES `BalanceTxExists` — sole production caller is `executeRefund`). `found=false, totalUSD=0, err=nil` for an unknown user (same contract as the old probe).

- [ ] **Step 1: Rewrite the storage test (RED)**

Replace `TestSQLBalanceStoreBalanceTxExists` (balance_test.go:224-251) — keep its exact setup scaffolding (DB, user 42 seeding) and adapt:

```go
// TestSQLBalanceStoreBalanceTxTotal pins the probe the admin refund flow uses
// on the balance rail: the deterministic order_refund:<orderID> credit's
// existence AND its net amount, so an amount-divergent re-run can fail closed
// instead of skipping the credit while recording a different sum.
func TestSQLBalanceStoreBalanceTxTotal(t *testing.T) {
	// ... same setup as the test being replaced ...
	if total, found, err := store.BalanceTxTotal(ctx, 424242, "order_refund:7"); err != nil || found || total != 0 {
		t.Fatalf("unknown user = (%v, %v, %v), want (0, false, nil)", total, found, err)
	}
	if total, found, err := store.BalanceTxTotal(ctx, 42, "order_refund:7"); err != nil || found || total != 0 {
		t.Fatalf("before credit = (%v, %v, %v), want (0, false, nil)", total, found, err)
	}
	if _, err := store.AdjustBalance(ctx, 42, 12.50, "order_refund:7", 9001); err != nil {
		t.Fatal(err)
	}
	if total, found, err := store.BalanceTxTotal(ctx, 42, "order_refund:7"); err != nil || !found || total != 12.50 {
		t.Fatalf("after credit = (%v, %v, %v), want (12.50, true, nil)", total, found, err)
	}
	if _, found, err := store.BalanceTxTotal(ctx, 42, "order_refund:8"); err != nil || found {
		t.Fatalf("other order = found %v, want false", found)
	}
}
```

Run: `go test ./internal/storage/ -run TestSQLBalanceStoreBalanceTxTotal`
Expected: FAIL (compile error — `BalanceTxTotal` undefined).

- [ ] **Step 2: Implement the storage probe**

Replace `BalanceTxExists` in `internal/storage/balance.go` (lines 52-75) — carry the LOAD-BEARING doc over, updated:

```go
// BalanceTxTotal reports whether the user already has balance_txs audit rows
// of exactly this type string, and their net amount in USD. The admin refund
// flow uses it on the balance rail's deterministic "order_refund:<orderID>"
// identity: after a ledger-recording failure the re-run finds the prior
// credit, skips minting a second one, AND can fail closed when the re-run's
// amount diverges from the prior credit (books must never claim money that
// did not move). Mirrors the crash-window idempotency OrderBalanceNet gives
// the checkout debit. An unknown user has no rows and reports
// (0, false, nil).
//
// LOAD-BEARING COUPLING: the refund flow's identity is per-ORDER, not
// per-amount, and is only sound while the bot's settled-only refund gate
// (admin_refunds.go refundableOrder) allows ONE balance refund per order —
// with executeRefund's amount-divergence guard failing closed on a divergent
// re-run. Relaxing that gate REQUIRES making the txType amount-scoped first.
// See docs/payment-operations.md §11.
func (s *SQLBalanceStore) BalanceTxTotal(ctx context.Context, userID int64, txType string) (float64, bool, error) {
	var total float64
	var found int
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM balance_txs
		  WHERE user_id = (SELECT id FROM users WHERE telegram_id = ?) AND type = ?),
		 COALESCE((SELECT SUM(amount_usd) FROM balance_txs
		  WHERE user_id = (SELECT id FROM users WHERE telegram_id = ?) AND type = ?), 0)`,
		userID, txType, userID, txType).Scan(&found, &total); err != nil {
		return 0, false, fmt.Errorf("balance store: tx total: %w", err)
	}
	return total, found == 1, nil
}
```

Update the `BalanceStore` interface in `internal/storage/interfaces.go` (lines 113-117): replace the `BalanceTxExists` declaration + doc with:

```go
	// BalanceTxTotal reports whether the user already has balance_txs audit
	// rows of exactly this type string (e.g. the admin refund flow's
	// deterministic "order_refund:<orderID>" credit) and their net USD
	// amount. It answers (0, false, nil) for an unknown user. LOAD-BEARING:
	// see the SQLBalanceStore.BalanceTxTotal doc (per-order identity assumes
	// the bot's settled-only refund gate + amount-divergence guard).
	BalanceTxTotal(ctx context.Context, userID int64, txType string) (totalUSD float64, found bool, err error)
```

- [ ] **Step 3: Fix the shop mock**

In `internal/shop/balance_test.go`: run `grep -n txTypes internal/shop/` and migrate every usage — field becomes `txTotals map[string]float64`, method becomes:

```go
func (m *mockBalanceStore) BalanceTxTotal(_ context.Context, _ int64, txType string) (float64, bool, error) {
	total, found := m.txTotals[txType]
	return total, found, nil
}
```

Setter sites in existing shop tests change from `txTypes[x] = true` to `txTotals[x] = <the amount the test's scenario credits>`.

Run: `go build ./... && go test ./internal/shop/ ./internal/storage/`
Expected: PASS (storage probe test green).

- [ ] **Step 4: Write the bot guard test (RED)**

New test in `internal/bot/admin_refunds_test.go` (next to `TestAdminRefundLedgerFailurePartialCarriesPartialAmount`, copying its setup):

```go
// TestAdminRefundBalanceDivergentRerunFailsClosed pins the HANDOFF §6.7
// guard: after a PARTIAL balance refund ($5.25 of $12.50) whose ledger
// record failed, a re-run with a DIFFERENT amount must fail closed — the
// per-order order_refund identity already spent its credit, so recording a
// divergent amount would make books claim money that never moved. Before
// the guard this re-run skipped the credit AND recorded a full refund.
func TestAdminRefundBalanceDivergentRerunFailsClosed(t *testing.T) {
	e := newE2EEnv(t)
	e.cmd(refundBuyer, "/start", "en") // the credit needs the users row
	orderID := seedRefundOrder(t, e, storage.PaymentMethodBalance, "")
	balances := storage.NewSQLBalanceStore(e.db.Conn())
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, 12.50, "grant", e2eAdminID); err != nil {
		t.Fatal(err)
	}
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, -12.50,
		fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}
	ledger := &failingRefundLedger{payLedgerStore: e.bot.payLedger, failIngest: true}
	e.bot.payLedger = ledger

	// Partial refund: the credit executes ($5.25), the ledger record fails.
	e.cb(e2eAdminID, refundCBData(orderID, 525), "en")
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "5.25" {
		t.Fatalf("balance after partial credit = %s, want 5.25", got)
	}

	// Divergent re-run (FULL amount) with the ledger healed: fail closed.
	ledger.setFailIngest(false)
	calls := e.cb(e2eAdminID, refundCBData(orderID, refundFullUSD), "en")
	if got := e.qInt(`SELECT COUNT(*) FROM refunds`); got != 0 {
		t.Fatalf("divergent re-run wrote %d refund rows, want 0", got)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM balance_txs WHERE type=?`, fmt.Sprintf("order_refund:%d", orderID)); got != 1 {
		t.Fatalf("divergent re-run left %d order_refund rows, want the prior 1", got)
	}
	if got := e.qStr(`SELECT printf('%.2f', balance_usd) FROM users WHERE telegram_id=?`, refundBuyer); got != "5.25" {
		t.Fatalf("divergent re-run moved the balance to %s, want 5.25 untouched", got)
	}
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateSettled {
		t.Fatalf("divergent re-run left state %s, want settled", got)
	}
	if text := tgText(calls); !strings.Contains(text, "prior credit") {
		t.Fatalf("divergent re-run text does not name the prior credit: %q", text)
	}

	// The EXACT-amount re-run still completes the record (credit skipped).
	calls = e.cb(e2eAdminID, refundCBData(orderID, 525), "en")
	if got := e.qInt(`SELECT COUNT(*) FROM balance_txs WHERE type=?`, fmt.Sprintf("order_refund:%d", orderID)); got != 1 {
		t.Fatalf("recovery minted %d credits, want exactly 1", got)
	}
	assertRefundRow(t, e, orderID, "balance", fmt.Sprintf("balance-refund:%d", orderID),
		fmt.Sprintf("balance:%d", orderID), refundBuyer, 525, "USD", 2)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStatePartiallyRefunded {
		t.Fatalf("state after recovery = %s, want partially_refunded", got)
	}
	want := e.bot.i18n.Tf("en", "admin_refund_done",
		fmt.Sprintf("balance-refund:%d", orderID), orderID, storage.PaymentStatePartiallyRefunded)
	if got := tgText(calls); got != want {
		t.Fatalf("recovery text = %q, want %q", got, want)
	}
}
```

Run: `go test ./internal/bot/ -run TestAdminRefundBalanceDivergentRerunFailsClosed`
Expected: FAIL (compile error while `executeRefund` still calls `BalanceTxExists`; after Step 3 the bot no longer compiles until Step 5 — implement Step 5 immediately after seeing the RED).

- [ ] **Step 5: Implement the bot guard**

Replace `executeRefund`'s balance branch (admin_refunds.go:589-616):

```go
	case storage.PaymentMethodBalance:
		// The credit IS this rail's provider step. Its deterministic
		// order_refund:<orderID> audit type doubles as the idempotency
		// identity: a re-run after a failed ledger write finds the prior
		// credit and skips it, so the recovery path mints money exactly once
		// (mirrors the crash-window net check ConfirmBalancePayment uses on
		// the debit side).
		//
		// LOAD-BEARING COUPLING: this identity is per-ORDER, not per-amount.
		// It is only safe while refundableOrder's settled-only gate limits
		// the flow to ONE balance refund per order AND the divergence guard
		// below fails closed on a re-run whose amount differs from the prior
		// credit. If the gate is ever relaxed (e.g. partial-then-remainder),
		// the txType MUST become amount-scoped
		// (order_refund:<orderID>:<amountMinor>) FIRST — otherwise a legit
		// second partial dies on the divergence guard with no bot path.
		// See docs/payment-operations.md §11.
		txType := fmt.Sprintf("order_refund:%d", order.ID)
		priorUSD, found, err := b.balances.BalanceTxTotal(ctx, order.UserID, txType)
		if err != nil {
			return "", fmt.Errorf("balance refund probe: %w", err)
		}
		if found {
			// Amount-divergent re-run: the credit identity is spent, so
			// recording a DIFFERENT amount would make books claim money that
			// never moved. Fail closed — nothing credited, nothing recorded;
			// the operator re-runs with the exact prior amount (the recovery
			// message quotes it) or resolves via payment-review. The balance
			// rail is USD scale 2, so the stored audit amount converts *100.
			if int64(math.Round(priorUSD*100)) != plan.fact.AmountMinor {
				return "", fmt.Errorf(
					"balance refund: prior credit for order %d is %.2f USD but this refund is %d minor units — re-run with the exact prior amount or resolve via payment-review",
					order.ID, priorUSD, plan.fact.AmountMinor)
			}
		} else if _, err := b.balances.AdjustBalance(ctx, order.UserID,
			float64(plan.fact.AmountMinor)/100, txType, adminID); err != nil {
			return "", fmt.Errorf("balance credit: %w", err)
		}
		return fmt.Sprintf("balance-refund:%d", order.ID), nil
```

(`math` and `fmt` are already imported in admin_refunds.go.)

- [ ] **Step 6: Run bot + shop + storage suites**

Run: `go test ./internal/bot/ ./internal/shop/ ./internal/storage/`
Expected: PASS — the new guard test green AND every existing pin untouched: `TestAdminRefundConfirmBalanceE2E`, `TestAdminRefundLedgerFailurePartialCarriesPartialAmount` (its recovery re-run uses the SAME amount → passes the guard), the full-amount ledger-failure recovery test, `TestAdminRefundConfirmBalanceConcurrentDoubleTap`.

- [ ] **Step 7: Update docs §11**

In `docs/payment-operations.md` §11, find the balance-rail re-run / one-refund-per-order coupling text (`grep -n "order_refund" docs/payment-operations.md`) and state the enforcement: a re-run whose amount diverges from the prior `order_refund:<orderID>` credit now FAILS CLOSED (error names both amounts; nothing credited, nothing recorded) — the recovery is the exact-amount re-run quoted by the failure message, or `payment-review` resolution. Keep the existing gate-relaxation warning (amount-scoped identity FIRST).

- [ ] **Step 8: Full gates + commit**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`
Expected: clean/empty/ok (storage ~100s).

```bash
git add internal/storage/interfaces.go internal/storage/balance.go internal/storage/balance_test.go \
        internal/shop/balance_test.go internal/bot/admin_refunds.go internal/bot/admin_refunds_test.go \
        docs/payment-operations.md
git commit -m "fix(bot,storage): balance refund fails closed on amount-divergent re-run (HANDOFF 6.7)"
```

---

### Task 4: Refunds path-5 durable anomaly trace (HANDOFF §6.6) + docs sweep

Provider-success + ledger-failure ("path-5") currently leaves only a log line and a chat message. This task adds the best-effort durable trace: an orphan `payment_anomalies` row (rulings R1/R2/R5/R6), visible in `/payreview` and `payment-review`, plus the plan's docs sweep.

**Files:**
- Modify: `internal/bot/admin_refunds.go` (path-5 block, lines 405-431; add `encoding/json` import)
- Modify: `internal/bot/admin_payreview.go:25-33` (`payReviewProviders` += balance, ruling R2)
- Test: `internal/bot/admin_refunds_test.go` (two new tests)
- Modify: `docs/payment-operations.md` §11 (path-5 paragraph)
- Modify: `CHANGELOG.md` ([Unreleased] — Quality entry for ALL FOUR plan items)
- Modify: `docs/superpowers/HANDOFF.md` §6 (mark items 1, 6, 7, 13 done — ✅ + date + this plan's path, roadmap-§4-style annotations; renumber nothing)

**Interfaces:**
- Consumes: `b.order.RecordPaymentAnomaly(ctx, storage.PaymentAnomaly) error` (shop wrapper, shop/order.go:482 — returns `storage.ErrPaymentNeedsReview` on a SUCCESSFUL write, the webhook convention); `storage.PaymentEventRefunded`; `plan.fact` (`storage.Refund`: `PaymentExternalID`, `PayerID`, `AmountMinor`, `Currency`, `Scale`); `refundID` (executeRefund's return); e2e helpers `e.failAnomalyRecording(err)` (e2e_test.go:230), `failingRefundLedger`, `admin_payreview_case_line` locale key.
- Produces: anomaly reason grammar `refund_ledger_failure:order=<id>` (pinned by tests + docs); `balance` as an accepted `/payreview` provider bucket.

- [ ] **Step 1: Write the durable-trace test (RED)**

New test in `internal/bot/admin_refunds_test.go` (setup copied from `TestAdminRefundLedgerFailurePartialCarriesPartialAmount`):

```go
// TestAdminRefundLedgerFailureRecordsDurableAnomaly pins the path-5 durable
// trace (HANDOFF §6.6): provider success + ledger failure writes a
// best-effort ORPHAN anomaly (proposed_order_id=0 — a needs_review flip
// would block the documented /refund re-run remedy behind the settled-only
// gate; the order link rides in the reason and raw_payload). The case joins
// /payreview, and the healed re-run completes WITHOUT duplicating the row.
func TestAdminRefundLedgerFailureRecordsDurableAnomaly(t *testing.T) {
	e := newE2EEnv(t)
	e.cmd(refundBuyer, "/start", "en") // the credit needs the users row
	orderID := seedRefundOrder(t, e, storage.PaymentMethodBalance, "")
	balances := storage.NewSQLBalanceStore(e.db.Conn())
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, 12.50, "grant", e2eAdminID); err != nil {
		t.Fatal(err)
	}
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, -12.50,
		fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}
	ledger := &failingRefundLedger{payLedgerStore: e.bot.payLedger, failIngest: true}
	e.bot.payLedger = ledger

	calls := e.cb(e2eAdminID, refundCBData(orderID, 525), "en")
	// The operator guidance message is UNCHANGED (ruling R5).
	want := e.bot.i18n.Tf("en", "admin_refund_ledger_failed_rerun",
		fmt.Sprintf("balance-refund:%d", orderID), orderID, "mock ledger ingest failure", orderID, "5.25")
	if got := tgText(calls); got != want {
		t.Fatalf("loud text = %q, want %q", got, want)
	}
	// Durable orphan anomaly with the full money tuple and the order link.
	var provider, kind, extID, relID, currency, reason, payload string
	var proposed, payer, amount int64
	var scale int
	if err := e.db.Conn().QueryRow(`
		SELECT provider, event_kind, external_id, related_external_id, proposed_order_id,
		       payer_id, amount_minor, currency, scale, reason, raw_payload
		FROM payment_anomalies`).Scan(&provider, &kind, &extID, &relID, &proposed,
		&payer, &amount, &currency, &scale, &reason, &payload); err != nil {
		t.Fatalf("path-5 anomaly row: %v", err)
	}
	if provider != "balance" || kind != storage.PaymentEventRefunded {
		t.Fatalf("anomaly provider/kind = %s/%s, want balance/refunded", provider, kind)
	}
	if extID != fmt.Sprintf("balance-refund:%d", orderID) || relID != fmt.Sprintf("balance:%d", orderID) {
		t.Fatalf("anomaly ids = %q/%q", extID, relID)
	}
	if proposed != 0 {
		t.Fatalf("proposed_order_id = %d, want 0 (orphan — no needs_review flip)", proposed)
	}
	if payer != refundBuyer || amount != 525 || currency != "USD" || scale != 2 {
		t.Fatalf("anomaly money tuple = %d/%d/%s/%d", payer, amount, currency, scale)
	}
	if wantReason := fmt.Sprintf("refund_ledger_failure:order=%d", orderID); reason != wantReason {
		t.Fatalf("reason = %q, want %q", reason, wantReason)
	}
	if !strings.Contains(payload, fmt.Sprintf(`"order_id":%d`, orderID)) {
		t.Fatalf("raw_payload lacks the order link: %q", payload)
	}
	// The order state is UNTOUCHED — the flip would kill the re-run remedy.
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStateSettled {
		t.Fatalf("path-5 flipped the order to %s, want settled", got)
	}
	// The case surfaces in /payreview (balance joined the buckets, ruling R2).
	calls = e.cmd(e2eAdminID, "/payreview", "en")
	listLine := e.bot.i18n.Tf("en", "admin_payreview_case_line",
		int64(0), "balance", "-", 1, reason)
	if !strings.Contains(tgText(calls), listLine) {
		t.Fatalf("/payreview list lacks the path-5 case:\n%s", tgText(calls))
	}

	// Healed re-run: completes the record, does NOT duplicate the anomaly.
	ledger.setFailIngest(false)
	e.cb(e2eAdminID, refundCBData(orderID, 525), "en")
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies`); got != 1 {
		t.Fatalf("recovery left %d anomaly rows, want the original 1", got)
	}
	assertRefundRow(t, e, orderID, "balance", fmt.Sprintf("balance-refund:%d", orderID),
		fmt.Sprintf("balance:%d", orderID), refundBuyer, 525, "USD", 2)
	if got := e.qStr(`SELECT payment_state FROM orders WHERE id=?`, orderID); got != storage.PaymentStatePartiallyRefunded {
		t.Fatalf("state after recovery = %s, want partially_refunded", got)
	}
}
```

NOTE for the implementer: `admin_payreview_case_line`'s exact argument types/rendering — read the locale string and `sendPayReviewList` (admin_payreview.go:117-120) and match the pin to the real formatting (the `int64(0)` / `1` types above are the expected shapes; adjust if `Tf` renders differently). If `tgText(calls)` for the list proves fragile, pin via `strings.Contains` on the provider+reason fragment instead and say so in the report.

Run: `go test ./internal/bot/ -run TestAdminRefundLedgerFailureRecordsDurableAnomaly`
Expected: FAIL (no anomaly row today — `path-5 anomaly row: sql: no rows in result set`).

- [ ] **Step 2: Write the best-effort test (RED)**

```go
// TestAdminRefundLedgerFailureAnomalyWriteIsBestEffort pins ruling R5/R6's
// best-effort contract: when the anomaly write ALSO fails (the DB is likely
// what just broke), the operator still gets the exact same guidance message
// — the durable trace degrades to the pre-existing log+chat surface, never
// swallowing the primary failure.
func TestAdminRefundLedgerFailureAnomalyWriteIsBestEffort(t *testing.T) {
	e := newE2EEnv(t)
	e.cmd(refundBuyer, "/start", "en")
	orderID := seedRefundOrder(t, e, storage.PaymentMethodBalance, "")
	balances := storage.NewSQLBalanceStore(e.db.Conn())
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, 12.50, "grant", e2eAdminID); err != nil {
		t.Fatal(err)
	}
	if _, err := balances.AdjustBalance(context.Background(), refundBuyer, -12.50,
		fmt.Sprintf("order_payment:%d", orderID), 0); err != nil {
		t.Fatal(err)
	}
	ledger := &failingRefundLedger{payLedgerStore: e.bot.payLedger, failIngest: true}
	e.bot.payLedger = ledger
	e.failAnomalyRecording(errors.New("injected anomaly write failure"))

	calls := e.cb(e2eAdminID, refundCBData(orderID, 525), "en")
	want := e.bot.i18n.Tf("en", "admin_refund_ledger_failed_rerun",
		fmt.Sprintf("balance-refund:%d", orderID), orderID, "mock ledger ingest failure", orderID, "5.25")
	if got := tgText(calls); got != want {
		t.Fatalf("loud text = %q, want %q", got, want)
	}
	if got := e.qInt(`SELECT COUNT(*) FROM payment_anomalies`); got != 0 {
		t.Fatalf("failed anomaly write left %d rows, want 0", got)
	}
	// The credit still executed exactly once (the provider step is untouched
	// by the trace failure).
	if got := e.qInt(`SELECT COUNT(*) FROM balance_txs WHERE type=?`, fmt.Sprintf("order_refund:%d", orderID)); got != 1 {
		t.Fatalf("credits = %d, want 1", got)
	}
}
```

Run: `go test ./internal/bot/ -run TestAdminRefundLedgerFailureAnomalyWriteIsBestEffort`
Expected: PASS even before implementation (today nothing writes an anomaly) — this leg is a REGRESSION pin for the new write's error handling; note that in the report (a pin that cannot go RED first is legitimate here; its RED evidence is Step 3's implementation mutating behavior only on the success path).

- [ ] **Step 3: Implement the path-5 anomaly write + payreview bucket**

In `internal/bot/admin_refunds.go`, inside `onAdminRefundConfirm`'s ledger-failure branch — AFTER the `b.logger.Error("refund: LEDGER RECORDING FAILED AFTER PROVIDER SUCCESS", ...)` call (line 422-423) and BEFORE the `if rail == storage.PaymentMethodStars {` rendering split (line 424) — insert:

```go
		// Durable path-5 trace (best-effort, ruling R1): the money moved but
		// the books did not record it. An ORPHAN anomaly row keeps the order
		// at settled — a needs_review flip would block the documented /refund
		// re-run remedy behind the settled-only gate; the order link rides in
		// the reason and raw_payload instead. The card surfaces in /payreview
		// and payment-review; after the recovery completes the record the
		// operator resolves it there (docs §11). RawPayload is deterministic
		// (no error text) so repeated failures reuse ONE row (ruling R6).
		// RecordPaymentAnomaly signals a successful write with
		// ErrPaymentNeedsReview — the webhook convention.
		anomalyPayload, _ := json.Marshal(map[string]any{
			"order_id": orderID, "rail": rail, "refund_id": refundID,
		})
		if recordErr := b.order.RecordPaymentAnomaly(ctx, storage.PaymentAnomaly{
			Provider:          rail,
			EventKind:         storage.PaymentEventRefunded,
			ExternalID:        refundID,
			RelatedExternalID: plan.fact.PaymentExternalID,
			PayerID:           plan.fact.PayerID,
			AmountMinor:       plan.fact.AmountMinor,
			Currency:          plan.fact.Currency,
			Scale:             plan.fact.Scale,
			Reason:            fmt.Sprintf("refund_ledger_failure:order=%d", orderID),
			RawPayload:        string(anomalyPayload),
		}); recordErr != nil && !errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
			b.logger.Error("refund: path-5 anomaly was not recorded",
				"order_id", orderID, "rail", rail, "refund_id", refundID, "error", recordErr)
		}
```

Add `"encoding/json"` to the imports.

In `internal/bot/admin_payreview.go` (lines 25-33), add the balance bucket (ruling R2) and extend the doc comment:

```go
var payReviewProviders = []string{
	storage.PaymentMethodStars,
	storage.PaymentMethodCrypto,
	storage.PaymentMethodYooKassa,
	storage.PaymentMethodStripe,
	storage.PaymentMethodTON,
	storage.PaymentMethodNowpayments,
	storage.PaymentMethodBalance, // path-5 refund-ledger-failure cards (admin_refunds.go)
	storage.PaymentReviewProviderUnknown,
}
```

Check `grep -rn payReviewProviders internal/bot/*_test.go` and any callback-grammar pins enumerating providers — update deliberately if pinned.

- [ ] **Step 4: Run bot suite**

Run: `go test ./internal/bot/`
Expected: PASS — both new tests green; ALL existing pins untouched, in particular the ledger-failure recovery tests (their path-5 now also writes an anomaly row — if any existing test counts `payment_anomalies` after a ledger failure and expected 0, that pin is now deliberately stale: update it and call it out in the commit body), `TestAdminPayReview*`, e2e payreview flow.

- [ ] **Step 5: Docs sweep**

1. `docs/payment-operations.md` §11 — path-5 paragraph: the failure window now leaves a durable ORPHAN anomaly card (`refund_ledger_failure:order=<id>`, provider bucket = the rail, `/payreview` + `payment-review` visible; balance included); the order deliberately stays `settled` so the quoted re-run remedy works; after recovery, resolve the card via `payment-review resolve` (CLI; bot-side orphan-card ergonomics are a known follow-up — HANDOFF §6.9).
2. `CHANGELOG.md` [Unreleased] → Quality: ONE entry «Money-followups batch (HANDOFF §6)» covering all four items: the ≥2^63 TON quotient guard; the mutation-verified NOWPayments no-HTML-escape pins; the balance amount-divergence fail-closed guard (BalanceTxExists → BalanceTxTotal); the path-5 durable orphan-anomaly trace + balance /payreview bucket. Follow the house entry style (behavior + why + operator impact).
3. `docs/superpowers/HANDOFF.md` §6: annotate items 1, 6, 7, 13 as done — `✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-money-followups.md` (roadmap-§4 annotation style; do NOT renumber the remaining items).

- [ ] **Step 6: Full gates + commit**

Run: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/ && go test ./...`
Expected: clean/empty/ok.

```bash
git add internal/bot/admin_refunds.go internal/bot/admin_payreview.go internal/bot/admin_refunds_test.go \
        docs/payment-operations.md CHANGELOG.md docs/superpowers/HANDOFF.md
git commit -m "feat(bot): durable path-5 anomaly trace for refund ledger failures (HANDOFF 6.6)"
```

---

## Self-Review Notes

**Spec coverage:** §6.13 → Task 1; §6.1 → Task 2; §6.7 → Task 3; §6.6 → Task 4. All four dispositions covered; docs/CHANGELOG/HANDOFF sweep folded into Task 4 (house precedent: backlog-followups plan).

**Known ambiguities (implementer decisions allowed, record in report):**
- Task 3 Step 3: the exact `txTypes` usage sites in shop tests (grep-driven migration).
- Task 4 Step 1: the `admin_payreview_case_line` pin's exact formatting (fallback: fragment pin, documented).
- Task 4 Step 4: any pre-existing anomaly-count pin colliding with the new path-5 write (update deliberately, commit body).

**Out of scope (explicitly):** HANDOFF §6.9 (`/payreview` orphan-card UX — including resolving these new cards from the bot), §6.8 (subscription-renewal actor leg), §6.2/3/4/5/10/11/14/15 (polish batch — next plan), §6.12 (TON re-scan window — deferred by its own note), roadmap 4.14/4.15, any i18n key changes, any migration.

**Type consistency:** `BalanceTxTotal(ctx, userID int64, txType string) (float64, bool, error)` — same signature in interfaces.go, balance.go, shop mock, and the admin_refunds.go call site. `PaymentAnomaly` field set matches models.go:226-242 exactly. `refund_ledger_failure:order=%d` grammar identical in code, test, and docs.

**Review Focus mapping:** RF1/RF2 → Task 3 Steps 4/6; RF3 → Task 2 Steps 1-4; RF4 → Task 1; RF5 → Task 4 Steps 1-2.
