# YooKassa RUB Payment Provider — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a complete YooKassa (RUB, card) payment provider — the last unfinished item of roadmap.md §3.5 — on top of the existing WIP adapter in `internal/payment/yookassa.go`, which currently does not compile.

**Architecture:** Mirror the CryptoBot provider exactly: a raw-HTTP adapter in `internal/payment/`, a `pay:yookassa:<orderID>` checkout button, an unsigned-webhook route that settles ONLY after an authoritative `GetPayment` refetch, and full integration with the immutable payment ledger (`ConfirmPaymentReceipt`, `orderMoney`, anomaly quarantine). RUB totals are derived from the USD base price via a new `USD_TO_RUB_RATE` env var, exactly like Stars derive via `USD_TO_STARS_RATE`, and snapshotted on the order at creation (`orders.total_rub`).

**Tech Stack:** Go 1.24, `net/http` (no SDK), `modernc.org/sqlite`, `github.com/google/uuid` (already in go.mod), tgbotapi v5.

**Spec:** Approved in-chat design (2026-09-19): RUB pricing via env exchange rate; YooKassa first, Stripe as a follow-up plan reusing this pattern.

## Global Constraints

- Module path is `shop_bot`; run all commands from the repo root `/home/thom/telegram-shop-bot`.
- No new Go module dependencies. Raw `net/http` + `encoding/json` only (match `cryptobot.go` style).
- All money comparisons use integer minor units. RUB scale is **2**. The receipt must satisfy `receipt.AmountMinor == int64(math.Round(order.TotalRUB*100))` exactly.
- YooKassa webhooks are **unsigned**. A webhook body is never trusted for money facts: parse only `event` + `object.id`, then refetch via `GetPayment` (Basic auth) before any settlement. This is already the WIP adapter's design — keep it.
- Every `CreatePayment` call uses a **fresh** `uuid.NewString()` `Idempotence-Key` (never reuse: the ledger quarantines a second successful charge for the same order).
- Provider constant is `storage.PaymentMethodYooKassa = "yookassa"` (already added in the WIP `models.go`).
- Every user-facing string goes through i18n (`b.t(lang, key)`); new keys must be added to **all 5 locales** (`locales/ru.json`, `en.json`, `es.json`, `de.json`, `zh.json`) — the parity test `internal/bot/i18n_keys_test.go` enforces this.
- Next migration number is **019** (`internal/storage/migrations/`).
- Feature is opt-in: with no `YOOKASSA_*` env vars the bot behaves exactly as before (no RUB button, webhook route returns 404-equivalent "not configured" and settles nothing).
- Subscription products remain **Stars-only**: RUB payment must be rejected for them with `sub_stars_only` (mirror the crypto guard).
- Tests: `go build ./...` and `go test ./...` must be green after every task; `golangci-lint run` clean (config in `.golangci.yml`).
- Commit after every task (conventional-commit style, e.g. `feat(payment): ...`).

## Review Focus

1. **Unsigned webhook spoofing** — an attacker POSTing `{"event":"payment.succeeded","object":{"id":"..."}}` to `/yookassa-webhook` must never settle an order without the API refetch confirming `paid=true, status=succeeded, currency=RUB, amount==order total`. (Task 6 test.)
2. **Amount drift** — a YooKassa payment whose refetched amount differs from `order.TotalRUB` (rate changed, tampered metadata) must be quarantined as an anomaly, never settled. (Task 4 + Task 6 tests.)
3. **Rounding** — `USD_TO_RUB_RATE` conversion and promo-discount multiplication must round to exactly 2 decimal places so the minor-unit equality check cannot fail on float noise (e.g. `19.99 * 0.9`). (Task 2 + Task 4 tests.)
4. **Replay** — the same YooKassa `payment.succeeded` webhook delivered twice must settle once and ACK both (200), matching the crypto webhook's idempotent-ACK error classes. (Task 6 test.)
5. **Feature-off safety** — with YooKassa unconfigured: no RUB button in checkout, `/api/checkout {method:"yookassa"}` rejected, webhook route inert. Nothing else changes. (Tasks 5, 6, 8 tests.)

---

### Task 1: Fix the WIP adapter and cover it with tests

**Files:**
- Modify: `internal/payment/yookassa.go` (fix 2 compile errors)
- Test: `internal/payment/yookassa_test.go` (create)

**Interfaces:**
- Consumes: `parsePositiveFixedDecimal(raw string, maxFractionDigits int) (int64, int, error)` and `parsePositiveProviderID(raw string) (int64, error)` and `normalizeAnomalyAmount(raw string) (int64, int, error)` — all defined in `internal/payment/cryptobot.go:281-410`. `Invoice` struct (`cryptobot.go:34`): `{PayURL, InvoiceID string}`. `shop.PaymentReceipt` (`internal/shop/order.go:53`). `storage.PaymentAnomaly` (`internal/storage/models.go:217`).
- Produces (used by Tasks 5-8):
  - `NewYooKassaPayment(shopID, secretKey, returnURL string) *YooKassaPayment`
  - `(*YooKassaPayment) Configured() bool`
  - `(*YooKassaPayment) CreatePayment(ctx context.Context, orderID int64, amountRUBMinor int64, description string) (*Invoice, error)`
  - `(*YooKassaPayment) GetPayment(ctx context.Context, paymentID string) (*Payment, error)`
  - `(*YooKassaPayment) ParseWebhook(body []byte) (*YooKassaNotification, error)` where `YooKassaNotification{Event, PaymentID string}`
  - `(*Payment) PaymentReceipt() (shop.PaymentReceipt, error)`
  - `(*Payment) PaymentAnomaly(reason string) (storage.PaymentAnomaly, error)`
  - Exported for tests/wiring: `ErrYooKassaNotConfigured`, `ErrInvalidYooKassaReceipt`
  - **NEW in this task:** `(*YooKassaPayment) SetBaseURL(url string)` — test seam, mirrors how `cryptobot_test.go` overrides `baseURL` (check that file first; if it sets the unexported field directly from the same package, do the same and skip SetBaseURL).

- [ ] **Step 1: Write the failing adapter tests**

Create `internal/payment/yookassa_test.go`. Model the harness on `internal/payment/cryptobot_test.go` (httptest server + same-package field override). Required test cases:

```go
func TestYooKassaCreatePaymentSendsRedirectConfirmation(t *testing.T) {
    // httptest server asserts: POST /v3/payments, Basic auth header present and
    // decodes to "shopID:secretKey", Idempotence-Key header non-empty,
    // body JSON: amount.value=="1999.00", amount.currency=="RUB",
    // capture==true, confirmation.type=="redirect",
    // confirmation.return_url==configured URL, metadata.order_id=="42".
    // Responds 200 {"id":"pay_1","status":"pending","confirmation":{"confirmation_url":"https://yoomoney/..."}}.
    // Assert: invoice.PayURL == confirmation_url, invoice.InvoiceID == "pay_1".
    // Second call must send a DIFFERENT Idempotence-Key.
}

func TestYooKassaCreatePaymentAPIError(t *testing.T) {
    // Server returns 400 {"code":"invalid_parameter","description":"bad"}.
    // Assert: error mentions "invalid_parameter".
}

func TestYooKassaCreatePaymentRejectsInvalidInput(t *testing.T) {
    // orderID<=0 or amountRUBMinor<=0 -> ErrInvalidYooKassaReceipt, no HTTP call made.
}

func TestYooKassaGetPaymentMapsSucceededPayment(t *testing.T) {
    // Server GET /v3/payments/pay_1 returns:
    // {"id":"pay_1","status":"succeeded","paid":true,
    //  "amount":{"value":"1999.00","currency":"RUB"},
    //  "metadata":{"order_id":"42"},
    //  "created_at":"2026-09-19T10:00:00Z","captured_at":"2026-09-19T10:01:00Z"}
    // Assert: Payment{ID:"pay_1", Status:"succeeded", Paid:true, Amount:"1999.00",
    //   Currency:"RUB", OrderID:42, OccurredAt: captured_at (UTC)}.
}

func TestYooKassaGetPaymentFallsBackToCreatedAt(t *testing.T) {
    // No captured_at -> OccurredAt == created_at.
}

func TestYooKassaPaymentReceiptValidatesEverything(t *testing.T) {
    // Table test. Valid succeeded RUB payment -> receipt{OrderID:42,
    //   Provider:"yookassa", ExternalID:"pay_1", Currency:"RUB",
    //   AmountMinor:199900, Scale:2, OccurredAt:...UTC}.
    // Each mutation -> ErrInvalidYooKassaReceipt:
    //   paid=false; status!="succeeded"; currency!="RUB";
    //   amount "1999.000" (3 frac digits); amount "-5.00"; amount "abc";
    //   metadata order_id missing/0/negative; ID with illegal chars; zero timestamps.
}

func TestYooKassaParseWebhookExtractsEventAndIDOnly(t *testing.T) {
    // Body {"event":"payment.succeeded","object":{"id":"pay_1","status":"succeeded","paid":true}}
    // -> {Event:"payment.succeeded", PaymentID:"pay_1"}.
    // Missing event -> error. Illegal object.id -> ErrInvalidYooKassaReceipt.
    // Garbage JSON -> error. Object without id -> {Event:..., PaymentID:""} (no error).
}

func TestYooKassaPaymentAnomalyPreservesFacts(t *testing.T) {
    // Unpaid/mismatched payment + reason -> anomaly{Provider:"yookassa",
    //   ExternalID, ProposedOrderID, RawAmount, Reason, OccurredAt}.
    // Empty reason -> ErrInvalidYooKassaReceipt.
}

func TestYooKassaNotConfiguredFailsClosed(t *testing.T) {
    // NewYooKassaPayment("","","").CreatePayment/GetPayment -> ErrYooKassaNotConfigured.
    // Configured()==false; Configured()==true only when all three fields non-empty.
}

func TestYooKassaResponseSizeLimit(t *testing.T) {
    // Server returns body > 1MB -> error "response is too large".
}
```

- [ ] **Step 2: Run tests, verify they fail to build**

Run: `go test ./internal/payment/ -run TestYooKassa -v`
Expected: BUILD FAILURE — the two known compile errors in `yookassa.go`.

- [ ] **Step 3: Fix the two compile errors**

Fix 1 — `yookassaConfirmation` (line ~63) lacks `ReturnURL`. The request needs `return_url`; the response carries `confirmation_url`:

```go
type yookassaConfirmation struct {
	Type            string `json:"type"`
	ReturnURL       string `json:"return_url,omitempty"`
	ConfirmationURL string `json:"confirmation_url,omitempty"`
}
```

Fix 2 — `parsePositiveFixedDecimal` returns `(int64, int, error)` (cryptobot.go:369), but `PaymentReceipt()` (line ~223) assigns 2 values. Change to use all three and carry the parsed scale:

```go
func (p *Payment) PaymentReceipt() (shop.PaymentReceipt, error) {
	if p == nil || !p.Paid || p.Status != "succeeded" || p.Currency != "RUB" {
		return shop.PaymentReceipt{}, ErrInvalidYooKassaReceipt
	}
	amountMinor, scale, err := parsePositiveFixedDecimal(p.Amount, 2)
	if err != nil || scale != 2 || p.OrderID <= 0 || !validYooKassaID(p.ID) || p.OccurredAt.IsZero() {
		return shop.PaymentReceipt{}, ErrInvalidYooKassaReceipt
	}
	return shop.PaymentReceipt{
		OrderID: p.OrderID, Provider: storage.PaymentMethodYooKassa,
		ExternalID: p.ID, Currency: "RUB",
		AmountMinor: amountMinor, Scale: scale, OccurredAt: p.OccurredAt.UTC(),
	}, nil
}
```

Keep everything else in the WIP file as-is (it already implements the security model correctly).

- [ ] **Step 4: Run tests, verify all pass**

Run: `go test ./internal/payment/ -run TestYooKassa -v` then `go build ./... && go test ./...`
Expected: all YooKassa tests PASS; full suite green.

- [ ] **Step 5: Commit**

```bash
git add internal/payment/yookassa.go internal/payment/yookassa_test.go internal/storage/models.go
git commit -m "feat(payment): fix YooKassa adapter compile errors and cover it with tests"
```

(`internal/storage/models.go` carries the WIP `PaymentMethodYooKassa` + `TotalRUB` field — commit it here so the tree is clean.)

---

### Task 2: Config (YOOKASSA_*, USD_TO_RUB_RATE) + RUB exchange rate

**Files:**
- Modify: `internal/config/config.go` (Config struct ~line 12, load() ~line 54)
- Modify: `internal/config/validation.go` (add webhook URL helper next to `TelegramWebhookURL`, line ~70)
- Modify: `internal/service/exchange.go`
- Modify: `.env.example`
- Test: `internal/config/config_test.go`, `internal/service/exchange_test.go` (create if absent)

**Interfaces:**
- Produces: `Config.YooKassaShopID, Config.YooKassaSecretKey, Config.YooKassaReturnURL string`; `Config.USDToRUBRate float64`; `config.YooKassaWebhookURL(raw string) string`; `(*service.ExchangeService) ConvertUSDToRUB(amountUSD float64) float64`; `NewExchangeService(usdToStarsRate int, usdToRUBRate float64)` — **signature change**, update the single caller `internal/bot/bot.go:136`.
- Env contract: `YOOKASSA_SHOP_ID`, `YOOKASSA_SECRET_KEY`, `YOOKASSA_RETURN_URL` (all optional; all-or-none validated), `USD_TO_RUB_RATE` (optional float, default **0** = RUB disabled; must be > 0 when set).

- [ ] **Step 1: Write failing config tests**

Append to `internal/config/config_test.go` (follow the existing `load(map)`-style helpers in that file):

```go
func TestYooKassaConfigLoadsWhenComplete(t *testing.T) {
    // lookup with YOOKASSA_SHOP_ID=123, YOOKASSA_SECRET_KEY=live_abc,
    // YOOKASSA_RETURN_URL=https://shop.example.com/return
    // -> cfg fields set verbatim.
}

func TestYooKassaConfigPartialCredentialsRejected(t *testing.T) {
    // Any one of the three set but not all -> load() returns error
    // mentioning "YOOKASSA". (all-or-none: half-configured must fail at startup,
    // never silently disable.)
}

func TestYooKassaReturnURLMustBeHTTPS(t *testing.T) {
    // YOOKASSA_RETURN_URL=http://... -> error. (YooKassa requires HTTPS return URLs.)
}

func TestUSDToRUBRateDefaultsToZeroAndParsesFloat(t *testing.T) {
    // unset -> USDToRUBRate == 0; "92.5" -> 92.5; "abc" -> error; "-5" -> error; "0" -> error when set explicitly.
}

func TestYooKassaWebhookURLDerivesFromBase(t *testing.T) {
    // config.YooKassaWebhookURL("https://shop.example.com/") == "https://shop.example.com/yookassa-webhook"
    // "" -> "".
}
```

And exchange tests (`internal/service/exchange_test.go`):

```go
func TestConvertUSDToRUB(t *testing.T) {
    s := NewExchangeService(50, 92.5)
    // 19.99 USD -> 1849.08 (math.Round(19.99*92.5*100)/100 == 1849.08)
    // 0 -> 0; rate 0 (RUB disabled) -> 0 for any input
    // 2.675 with rate 100 -> 267.50 exactly (float-noise guard: use Round(x*100)/100, never FormatFloat chains)
}
```

- [ ] **Step 2: Run, verify failures**

Run: `go test ./internal/config/ ./internal/service/ -run "YooKassa|RUB" -v`
Expected: compile errors (unknown fields/functions).

- [ ] **Step 3: Implement config + exchange changes**

`config.go` — add fields to `Config` (after `WebAppURL`):

```go
	// YooKassa RUB card payments. All three must be set together or none.
	YooKassaShopID    string
	YooKassaSecretKey string
	YooKassaReturnURL string
	// USDToRUBRate converts USD order totals to RUB (0 = RUB payments disabled).
	USDToRUBRate float64
```

In `load()`, before the `return &Config{...}`:

```go
	yooShopID := strings.TrimSpace(value(lookup, "YOOKASSA_SHOP_ID"))
	yooSecret := strings.TrimSpace(value(lookup, "YOOKASSA_SECRET_KEY"))
	yooReturn := strings.TrimSpace(value(lookup, "YOOKASSA_RETURN_URL"))
	if err := ValidateYooKassaConfig(yooShopID, yooSecret, yooReturn); err != nil {
		return nil, err
	}

	usdToRUB := 0.0
	if raw := strings.TrimSpace(value(lookup, "USD_TO_RUB_RATE")); raw != "" {
		rate, err := strconv.ParseFloat(raw, 64)
		if err != nil || rate <= 0 {
			return nil, fmt.Errorf("USD_TO_RUB_RATE: must be a positive number, got %q", raw)
		}
		usdToRUB = rate
	}
```

Add to the returned struct literal: `YooKassaShopID: yooShopID, YooKassaSecretKey: yooSecret, YooKassaReturnURL: yooReturn, USDToRUBRate: usdToRUB,`.

`validation.go` — add next to `TelegramWebhookURL`:

```go
// ValidateYooKassaConfig enforces all-or-none credentials and an HTTPS return URL.
func ValidateYooKassaConfig(shopID, secretKey, returnURL string) error {
	set := 0
	for _, v := range []string{shopID, secretKey, returnURL} {
		if v != "" {
			set++
		}
	}
	if set == 0 {
		return nil
	}
	if set != 3 {
		return errors.New("YOOKASSA_SHOP_ID, YOOKASSA_SECRET_KEY and YOOKASSA_RETURN_URL must be set together")
	}
	if !strings.HasPrefix(returnURL, "https://") {
		return errors.New("YOOKASSA_RETURN_URL must be a public https:// URL")
	}
	return nil
}

// YooKassaWebhookURL turns the configured public base URL into the YooKassa
// notification endpoint, mirroring TelegramWebhookURL.
func YooKassaWebhookURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return ""
	}
	return base + "/yookassa-webhook"
}
```

`exchange.go` — extend the service (keep `SetRate` for Stars; add rate field + method):

```go
type ExchangeService struct {
	mu         sync.RWMutex
	usdToStars int
	usdToRUB   float64
}

func NewExchangeService(usdToStarsRate int, usdToRUBRate float64) *ExchangeService {
	return &ExchangeService{usdToStars: usdToStarsRate, usdToRUB: usdToRUBRate}
}

// ConvertUSDToRUB converts a USD amount to RUB rounded to 2 decimal places.
// Returns 0 when the RUB rate is not configured (RUB payments disabled).
func (s *ExchangeService) ConvertUSDToRUB(amountUSD float64) float64 {
	s.mu.RLock()
	rate := s.usdToRUB
	s.mu.RUnlock()
	if rate <= 0 || amountUSD <= 0 {
		return 0
	}
	return math.Round(amountUSD*rate*100) / 100
}
```

(Add `"math"` import.) Update the one caller: `internal/bot/bot.go:136` → `service.NewExchangeService(cfg.USDToStarsRate, cfg.USDToRUBRate)`. Fix any other `NewExchangeService` callers the compiler reports (search tests too).

`.env.example` — add a YooKassa section mirroring the CryptoBot section's comment style:

```env
# --- YooKassa (RUB card payments, optional) ---
# All three must be set together; leave all empty to disable RUB payments.
YOOKASSA_SHOP_ID=
YOOKASSA_SECRET_KEY=
# Public HTTPS page the buyer returns to after paying.
YOOKASSA_RETURN_URL=
# RUB per 1 USD, e.g. 92.5. Required (and > 0) to enable the RUB pay button.
USD_TO_RUB_RATE=
```

- [ ] **Step 4: Run tests, verify pass + full suite green**

Run: `go test ./internal/config/ ./internal/service/ -v -run "YooKassa|RUB"` then `go build ./... && go test ./...`

- [ ] **Step 5: Commit**

```bash
git add internal/config/ internal/service/exchange.go .env.example internal/bot/bot.go
git commit -m "feat(config): YooKassa credentials, USD_TO_RUB_RATE and RUB exchange conversion"
```

---

### Task 3: Storage — migration 019, `total_rub` round-trip, ledger money case

**Files:**
- Create: `internal/storage/migrations/019_orders_total_rub.sql`
- Modify: `internal/storage/orders.go` (INSERT line ~67, SELECT column lists at lines ~115, ~169, ~220, ~230, ~371)
- Modify: `internal/storage/order_state.go` (`orderMoney` switch, line ~47)
- Test: `internal/storage/orders_test.go` (extend), `internal/storage/order_state_test.go` (create or extend nearest existing)

**Interfaces:**
- Consumes: `Order.TotalRUB float64` (already in the WIP `models.go`), `PaymentMethodYooKassa` constant.
- Produces: persisted `orders.total_rub` column; `orderMoney(order, "yookassa")` returns `(round(TotalRUB*100), "RUB", 2, nil)`.

- [ ] **Step 1: Write failing tests**

In `internal/storage/orders_test.go` (follow the existing temp-DB helper used by `TestCreateOrder*` cases in that file):

```go
func TestOrderTotalRUBRoundTrip(t *testing.T) {
    // Create order with TotalRUB: 1849.08 -> GetOrder returns 1849.08 (exact float compare via
    // math.Abs(got-1849.08) < 1e-9). Order created without TotalRUB -> 0.
    // GetUserOrders / GetOrdersByStatus (the other SELECTs you touched) also return TotalRUB.
}
```

For `orderMoney` (it is unexported — test from package `storage`):

```go
func TestOrderMoneyYooKassa(t *testing.T) {
    // {TotalRUB: 1849.08} -> (184908, "RUB", 2, nil)
    // {TotalRUB: 0}       -> ErrInvalidMoney
    // {TotalRUB: NaN/Inf} -> ErrInvalidMoney
    // unknown provider still errors as before.
}
```

- [ ] **Step 2: Run, verify failures**

Run: `go test ./internal/storage/ -run "TotalRUB|OrderMoneyYooKassa" -v`
Expected: FAIL (no such column / unsupported provider).

- [ ] **Step 3: Implement migration + column plumbing**

`019_orders_total_rub.sql`:

```sql
ALTER TABLE orders ADD COLUMN total_rub REAL DEFAULT 0;
```

`orders.go` — add `total_rub` to the INSERT (column list, `VALUES` placeholder, args `order.TotalRUB` after `order.TotalStars`) and to **every** SELECT that scans an Order (lines ~115, ~169, ~220, ~230, ~371): add `COALESCE(total_rub, 0)` after the `total_stars` expression and `&o.TotalRUB` in the matching Scan position. Search the file for `COALESCE(total_stars, 0)` to find them all; do not miss any (a missed Scan shifts every following column).

`order_state.go` — add a case to `orderMoney` (mirror the crypto case):

```go
	case PaymentMethodYooKassa:
		if order.TotalRUB <= 0 || math.IsNaN(order.TotalRUB) || math.IsInf(order.TotalRUB, 0) {
			return 0, "", 0, ErrInvalidMoney
		}
		return int64(math.Round(order.TotalRUB * 100)), "RUB", 2, nil
```

Check `normalizePaymentProvider` in the same file: `"yookassa"` must pass through unchanged (the `default` branch lowercases — verify, and add an explicit alias case only if needed).

- [ ] **Step 4: Run tests, verify pass + full storage suite**

Run: `go test ./internal/storage/ -v -run "TotalRUB|OrderMoney"` then `go test ./internal/storage/` (note: this package takes ~40s).

- [ ] **Step 5: Commit**

```bash
git add internal/storage/
git commit -m "feat(storage): orders.total_rub column (migration 019) and yookassa ledger money case"
```

---

### Task 3b: Migration 020 — ledger provider CHECK rebuild (added by controller ruling)

**Why this task exists:** `017_commerce_ledger.sql` pins `CHECK (provider IN ('stars','crypto'))` on six ledger tables (`payment_attempts`, `payment_events`, `refunds`, `payment_anomalies`, `payment_resolutions` — which also allows `'unknown'` — and `payment_ingress_audits`). Every YooKassa settlement/anomaly/refund INSERT fails on the CHECK (verified empirically during Task 3). SQLite cannot ALTER a CHECK constraint: the six tables must be rebuilt.

**Ruling on CHECK content:** the rebuilt CHECKs list `'stars','crypto','yookassa','stripe'` (plus `'unknown'` for `payment_resolutions`). `'stripe'` is included now because Stripe is the approved immediate follow-up plan and a second 6-table rebuild of the immutable ledger is the costlier risk; the app layer (`orderMoney` default case) rejects `stripe` facts until the Stripe provider exists, so the DB CHECK remains a safety net, not a feature flag. The Stripe plan MUST use provider key `stripe`.

**Files:**
- Create: `internal/storage/migrations/020_ledger_provider_yookassa.sql`
- Test: `internal/storage/migration_020_test.go` (create; harness pattern: `migration_017_test.go` — build an older-schema DB, insert legacy rows, apply migrations, assert)

**Rebuild mechanics (per table, all inside the single migration file — the migrator wraps each file in one transaction, and SQLite DDL is transactional):**
1. `CREATE TABLE <name>_new (…)`: copy the full column definition from 017 (lines ~145-250) with the widened `provider` CHECK. Keep every other constraint (NOT NULL, DEFAULT, PRIMARY KEY, UNIQUE) byte-identical.
2. `INSERT INTO <name>_new (<explicit column list>) SELECT <same column list> FROM <name>;` — never `SELECT *`.
3. `DROP TABLE <name>;` — this silently drops the table's triggers and indexes.
4. `ALTER TABLE <name>_new RENAME TO <name>;`
5. Recreate indexes (017 lines 254-260): `idx_payment_attempts_order`, `idx_payment_events_order_time`, `idx_payment_anomalies_provider_time`, `idx_refunds_order`, `idx_refunds_payment_identity`, `idx_payment_resolutions_order`, `idx_payment_ingress_audits_order`.
6. Recreate triggers — **current versions**: `payment_attempts_identity_no_update` and `refunds_identity_no_update` from **018** (they supersede 017's); `payment_attempts_entitlement_once`, `payment_attempts_no_delete`, `refunds_no_delete` from 017; `payment_events_no_update/no_delete`, `payment_anomalies_no_update/no_delete`, `payment_resolutions_no_update/no_delete`, `payment_ingress_audits_no_update/no_delete` from 017 (lines ~389-429). Use `CREATE TRIGGER` (no `IF NOT EXISTS` needed after DROP; `DROP TRIGGER IF EXISTS` first is acceptable defensive style matching 018).
   `order_events` and `idx_order_events_order_time` are NOT touched (no provider column).

**Tests (`migration_020_test.go`):**
```go
func TestMigration020PreservesLegacyLedgerRows(t *testing.T)
    // Build a DB at schema 019 (apply 001..019 via the production migrator or the
    // 017-test harness), insert representative rows into all six tables
    // (stars + crypto providers), apply 020, assert every row survives byte-identical
    // and all 7 indexes exist (sqlite_master query).

func TestMigration020AcceptsYooKassaAndStripeProviders(t *testing.T)
    // After 020: INSERT provider='yookassa' succeeds into all six tables
    // (payment_resolutions also 'unknown'); INSERT provider='stripe' succeeds;
    // INSERT provider='paypal' still fails with a CHECK constraint error.

func TestMigration020ImmutabilityTriggersSurviveRebuild(t *testing.T)
    // After 020: UPDATE payment_attempts SET amount_minor=… -> RAISE abort
    //   ("identity is immutable" — the 018 trigger text);
    // UPDATE of provider/external_id on refunds -> abort;
    // DELETE FROM payment_events -> abort; DELETE FROM payment_anomalies -> abort;
    // DELETE FROM payment_resolutions / payment_ingress_audits -> abort;
    // UPDATE on payment_events/anomalies/resolutions/ingress_audits -> abort (no_update triggers).

func TestYooKassaSettlementWritesLedgerRows(t *testing.T)
    // The end-to-end storage settlement the Task 3 round-trip test had to stop short of:
    // create an order with TotalRUB, UpdateOrderStatusWithPaymentFact (or the
    // UpdateOrderStatus path) with a yookassa fact (RUB, 184908, scale 2, external id)
    // -> succeeds; payment_attempts row exists with provider 'yookassa';
    // a replayed identical fact is idempotent; a conflicting fact quarantines
    // (ErrPaymentNeedsReview) instead of failing on a CHECK.
```

**Verification:** `go test ./internal/storage/ -run "Migration020|YooKassaSettlement" -v` then the full suite `go build ./... && go test ./...` (~40s storage).

**Commit:** `feat(storage): migration 020 widens ledger provider checks for yookassa/stripe`

**Explicitly out of scope (Task 7 carries these):** app-level provider allowlists that still reject yookassa — `payment_ingress.go:54` (refund preview), `payment_resolutions.go:16` and `:468` (resolution validation).

---

### Task 4: Shop layer — CartView.TotalRUB, order snapshot, receipt validation

**Files:**
- Modify: `internal/shop/cart.go` (CartView line ~12, Get() line ~55)
- Modify: `internal/shop/order.go` (CreateFromCart line ~185, ConfirmPaymentReceipt switch line ~296)
- Test: `internal/shop/cart_test.go`, `internal/shop/order_test.go`, `internal/shop/payment_receipt_test.go`

**Interfaces:**
- Consumes: `ExchangeService.ConvertUSDToRUB` (Task 2), `Order.TotalRUB` persistence (Task 3), `PaymentMethodYooKassa`.
- Produces: `CartView.TotalRUB float64`; `CreateFromCart` snapshots discounted `TotalRUB` onto the order; `ConfirmPaymentReceipt` accepts provider `"yookassa"` with `Currency=="RUB"`, `Scale==2`, `AmountMinor==round(order.TotalRUB*100)`, non-empty `ExternalID` — any mismatch routes to the existing `receiptMismatch` quarantine path.

- [ ] **Step 1: Write failing tests**

`cart_test.go`:

```go
func TestCartViewTotalRUB(t *testing.T) {
    // exchange rate 92.5; cart: 1x $10.00 + 2x $4.995 => TotalUSD 19.99
    // -> TotalRUB == 1849.08 (computed from TotalUSD ONCE at the end of Get(),
    //    NOT summed per item — per-item rounding drifts).
    // No exchange service (nil) -> TotalRUB == 0. Rate 0 -> TotalRUB == 0.
}
```

`order_test.go` (follow the existing `CreateFromCart` test harness in that file):

```go
func TestCreateFromCartSnapshotsTotalRUB(t *testing.T) {
    // cartView{TotalUSD: 19.99, TotalRUB: 1849.08} -> created order has TotalRUB 1849.08.
    // With promo 10%: TotalUSD 17.991, TotalRUB == math.Round(1849.08*90)/100 == 1664.17.
}
```

`payment_receipt_test.go` (extend the existing receipt table tests — that file already covers stars/crypto validation):

```go
func TestConfirmPaymentReceiptYooKassa(t *testing.T) {
    // Order: TotalRUB 1849.08, pending.
    // Valid: {Provider:"yookassa", Currency:"RUB", AmountMinor:184908, Scale:2, ExternalID:"pay_1"}
    //   -> settles paid, outcome returned.
    // Mismatch cases -> ErrPaymentReceiptMismatch AND an anomaly/quarantine row
    //   (assert via the same harness the crypto mismatch cases use):
    //   AmountMinor 184907; AmountMinor 184909; Currency "USD"; Scale 0;
    //   ExternalID ""; order TotalRUB 0 (RUB disabled at creation).
    // Replay: same receipt twice -> second returns ErrOrderStatusConflict-class
    //   idempotent result, settles once (mirror the existing crypto replay test).
}
```

- [ ] **Step 2: Run, verify failures**

Run: `go test ./internal/shop/ -run "TotalRUB|YooKassa" -v`

- [ ] **Step 3: Implement**

`cart.go` — add `TotalRUB float64` to `CartView`; at the end of `Get()` (after the items loop, before `return view`):

```go
	if s.exchange != nil {
		view.TotalRUB = s.exchange.ConvertUSDToRUB(view.TotalUSD)
	}
```

`order.go` `CreateFromCart` — after the promo discount block (line ~214):

```go
	totalRUB := cartView.TotalRUB
	if promo != nil {
		totalRUB = math.Round(totalRUB*float64(100-discountPct)) / 100
	}
```

and set `TotalRUB: totalRUB` in the `storage.Order` literal (line ~216).

`order.go` `ConfirmPaymentReceipt` — add a case before `default:` (line ~311):

```go
	case storage.PaymentMethodYooKassa:
		if receipt.Currency != "RUB" || receipt.AmountMinor <= 0 ||
			receipt.AmountMinor != int64(math.Round(order.TotalRUB*100)) ||
			receipt.Scale != 2 || receipt.ExternalID == "" {
			return nil, s.receiptMismatch(ctx, receipt)
		}
```

- [ ] **Step 4: Run tests, verify pass + full suite**

Run: `go test ./internal/shop/ -v -run "TotalRUB|YooKassa"` then `go build ./... && go test ./...`

- [ ] **Step 5: Commit**

```bash
git add internal/shop/
git commit -m "feat(shop): RUB cart totals, order snapshot and yookassa receipt validation"
```

---

### Task 5: Bot checkout — RUB pay button, `onPayYooKassa`, callback routing, Bot wiring

**Files:**
- Modify: `internal/bot/bot.go` (Bot struct line ~55, NewBot line ~174)
- Modify: `internal/bot/styled_keyboard.go` (BtnKey constants line ~42, key list line ~64, label switch line ~102)
- Modify: `internal/bot/handlers_checkout.go` (paymentMethodKeyboard line ~266, formatPaymentMethodsText call line ~260, `onOrderConfirm`)
- Modify: `internal/bot/ui_text.go` (`formatPaymentMethodsText` line ~146)
- Modify: `internal/bot/handlers_payment.go` (add `onPayYooKassa` after `onPayCrypto`)
- Modify: `internal/bot/handlers.go` (callback router — add `pay:yookassa:` prefix next to `pay:crypto:`)
- Modify: `locales/ru.json`, `en.json`, `es.json`, `de.json`, `zh.json`
- Test: `internal/bot/checkout_totals_test.go` (extend), `internal/bot/i18n_keys_test.go` (must pass unchanged — it enforces locale parity)

**Interfaces:**
- Consumes: Task 1 adapter (`NewYooKassaPayment`, `Configured`, `CreatePayment(ctx, orderID, amountRUBMinor, desc)`), Task 2 `Config.YooKassa*`, Task 4 `Order.TotalRUB`.
- Produces: `Bot.yookassa *payment.YooKassaPayment`; `(b *Bot) yooKassaPaymentsEnabled() bool`; `(b *Bot) onPayYooKassa(cbID string, chatID, userID int64, msgID int, data, lang string)`; callback prefix `pay:yookassa:<orderID>`; `BtnKeyPayYooKassa = "pay_yookassa"`; i18n keys `btn_pay_rub`, `yookassa_pay_title`, `yookassa_unavailable`, `yookassa_invoice_desc`.

- [ ] **Step 1: Write failing tests**

Extend `internal/bot/checkout_totals_test.go` (it already builds a Bot fixture with mock stores — reuse its harness):

```go
func TestPaymentKeyboardShowsRUBOnlyWhenEnabled(t *testing.T) {
    // YooKassa unconfigured OR USDToRUBRate==0 -> keyboard identical to before
    //   (no pay:yookassa row) — snapshot-compare callback data strings.
    // Configured + rate>0 + order.TotalRUB>0 + no subscription in cart ->
    //   exactly one extra row: callback "pay:yookassa:<orderID>",
    //   label contains "1849.08" formatted via b.t(lang,"btn_pay_rub").
    // Subscription cart -> RUB row hidden even when configured (Stars-only rule).
}

func TestOnPayYooKassaCreatesRedirectPayment(t *testing.T) {
    // Mock YooKassa API (httptest, adapter baseURL override like cryptobot tests):
    //   valid pending order owned by user -> bot sends message whose keyboard has
    //   a URL button equal to the confirmation_url returned by the API;
    //   the API received amount "1849.08" RUB and metadata order_id.
    // Not owner / not pending -> alert order_not_found / order_already_paid, no API call.
    // Adapter unconfigured -> alert yookassa_unavailable.
    // Subscription order -> alert sub_stars_only.
}
```

- [ ] **Step 2: Run, verify failures**

Run: `go test ./internal/bot/ -run "RUB|YooKassa" -v`

- [ ] **Step 3: Implement**

`bot.go`: add field `yookassa *payment.YooKassaPayment` to `Bot` (next to `crypto`, line ~70). In `NewBot` (line ~174, next to the crypto construction):

```go
	yookassa: payment.NewYooKassaPayment(cfg.YooKassaShopID, cfg.YooKassaSecretKey, cfg.YooKassaReturnURL),
```

Add next to `cryptoPaymentsEnabled` (line ~224):

```go
// yooKassaPaymentsEnabled reports whether RUB card payments can be offered:
// credentials configured AND a positive RUB exchange rate AND the order has a
// positive RUB snapshot (checked per-order at button build time).
func (b *Bot) yooKassaPaymentsEnabled() bool {
	return b.yookassa != nil && b.yookassa.Configured() && b.cfg != nil && b.cfg.USDToRUBRate > 0
}
```

`styled_keyboard.go`: add `BtnKeyPayYooKassa = "pay_yookassa"` to the constants (line ~42), to the key list (line ~64) and to the `ButtonKeyLabel`/style switch (line ~102, default style `StylePrimary` like Stars).

`handlers_checkout.go` `paymentMethodKeyboard` (line ~266) — add `yookassaOK bool, totalRUB float64` params; after the crypto row:

```go
	if yookassaOK {
		rubLabel := fmt.Sprintf("💳 %s (%.2f ₽)", b.t(lang, "btn_pay_rub"), totalRUB)
		kb = append(kb, []StyledButton{b.styledBtn(BtnKeyPayYooKassa, rubLabel, fmt.Sprintf("pay:yookassa:%d", orderID), StylePrimary)})
	}
```

Update the caller (`onOrderConfirm`, line ~259): `yookassaOK := b.yooKassaPaymentsEnabled() && !cartHasSubscription(view) && view.TotalRUB > 0`; pass `view.TotalRUB`. Extend `formatPaymentMethodsText` (`ui_text.go:146`) with the RUB line when enabled (mirror how the crypto line is added).

`handlers_payment.go` — add `onPayYooKassa`, a line-by-line mirror of `onPayCrypto` (lines 110-178) with these substitutions: guard `b.yooKassaPaymentsEnabled()` → alert `yookassa_unavailable`; callback prefix `"pay:yookassa:"`; amount `amountMinor := int64(math.Round(target.TotalRUB * 100))` with a `amountMinor <= 0` guard (alert `yookassa_unavailable`); `b.yookassa.CreatePayment(ctx, orderID, amountMinor, desc)`; desc key `yookassa_invoice_desc`; title key `yookassa_pay_title` (format `%.2f` RUB); URL button label `btn_pay_rub`. Keep the skeleton "generating invoice" edit and the subscription guard (`sub_stars_only`) identical.

`handlers.go` — in the callback router, next to the `pay:crypto:` branch, add `pay:yookassa:` dispatching to `b.onPayYooKassa(cb.Data ...)` with the same argument pattern.

Locales — add to all 5 files (parity test enforces):

```json
"btn_pay_rub": "💳 Pay by card",
"yookassa_pay_title": "💳 <b>Pay order <code>#%d</code></b>\n\nAmount: <b>%.2f ₽</b>\n\nClick the button below to pay:",
"yookassa_unavailable": "💳 Card payment is not available right now. Try Telegram Stars or contact the administrator.",
"yookassa_invoice_desc": "Order #%d"
```

(Translate properly for ru/es/de/zh — ru: «💳 Оплата картой», title «💳 <b>Оплата заказа <code>#%d</code></b>\n\nСумма: <b>%.2f ₽</b>...», unavailable «💳 Оплата картой сейчас недоступна...», desc «Заказ #%d». Follow the tone of the existing `crypto_*` keys in each locale.)

- [ ] **Step 4: Run tests, verify pass + full suite**

Run: `go test ./internal/bot/ -run "RUB|YooKassa" -v` then `go build ./... && go test ./...` (i18n parity test must pass).

- [ ] **Step 5: Commit**

```bash
git add internal/bot/ locales/
git commit -m "feat(bot): RUB card checkout button and onPayYooKassa redirect flow"
```

---

### Task 6: Webhook route + settlement through the ledger

**Files:**
- Modify: `internal/bot/webhook.go` (add `YooKassaWebhookHandler` after `CryptoBotWebhookHandler`)
- Modify: `cmd/bot/http_routes.go` (interface + mount)
- Modify: `cmd/bot/http_routes_test.go` (fake endpoints gains the method)
- Test: `internal/bot/yookassa_webhook_test.go` (create; harness pattern from `internal/bot/payment_ack_test.go`)

**Interfaces:**
- Consumes: Task 1 `ParseWebhook`/`GetPayment`/`PaymentReceipt`/`PaymentAnomaly`; Task 4 `ConfirmPaymentReceipt` yookassa case; existing `b.order.RecordPaymentAnomaly`, `b.NotifyPaymentOutcome`, `b.notifyAdmins`, `b.outWebhook`, `b.userLang`, `b.metrics.SuccessfulPayments`.
- Produces: `(b *Bot) YooKassaWebhookHandler() http.HandlerFunc`; route `POST /yookassa-webhook`; `webhookEndpoints` interface gains `YooKassaWebhookHandler() http.HandlerFunc`.

- [ ] **Step 1: Write failing tests**

`internal/bot/yookassa_webhook_test.go` — Bot fixture with a mock YooKassa API (httptest; override adapter baseURL) and the real SQLite-backed OrderService (the pattern `payment_ack_test.go` uses). Cases:

```go
func TestYooKassaWebhookSettlesAfterRefetch(t *testing.T) {
    // Pending order TotalRUB 1849.08. POST body
    // {"event":"payment.succeeded","object":{"id":"pay_1"}}.
    // Mock API GET /v3/payments/pay_1 -> succeeded/paid/RUB/1849.00... 
    //   NOTE: use amount "1849.08", metadata order_id = the real order.
    // Assert: HTTP 200; order status paid, payment_method "yookassa",
    //   payment_id "pay_1"; user got payment_success message;
    //   metrics SuccessfulPayments{provider="yookassa"} incremented;
    //   outbound webhook fired with Method "yookassa".
}

func TestYooKassaWebhookBodyIsNeverTrusted(t *testing.T) {
    // Body claims succeeded, but mock API GET returns status "pending"/paid=false
    //   -> HTTP 200 (ACK, YooKassa retries are pointless for non-terminal state),
    //      order stays pending, NO settlement. (review focus #1)
    // Body claims succeeded, API returns amount "1.00" (mismatch)
    //   -> anomaly row recorded (reason carries the mismatch), order stays
    //      pending or needs_review per ledger rules, HTTP 200. (review focus #2)
}

func TestYooKassaWebhookIgnoresNonPaymentEvents(t *testing.T) {
    // event "refund.succeeded" / "payment.canceled" -> 200, no API call, no state change.
    // (Refunds are operator-driven via payment-review, not auto-applied — out of scope.)
}

func TestYooKassaWebhookReplayIsIdempotent(t *testing.T) {
    // Same succeeded webhook twice -> first settles, second returns 200 without
    //   double settlement (stock decremented once, points once). (review focus #4)
}

func TestYooKassaWebhookUnconfiguredIsInert(t *testing.T) {
    // Bot without YooKassa creds -> any POST returns 503 and touches nothing. (review focus #5)
}

func TestYooKassaWebhookGarbageBodyQuarantines(t *testing.T) {
    // Invalid JSON -> anomaly recorded with sha256 digest payload (mirror the
    //   crypto parse-failure path), HTTP 200 so YooKassa stops retrying garbage.
    // Oversized body (>1MB) -> 400 via MaxBytesReader.
}

func TestYooKassaWebhookRefetchFailureWithholdsACK(t *testing.T) {
    // Mock API down (500 / timeout) on GET -> HTTP 500 so YooKassa retries later.
    //   Nothing settled, nothing quarantined as mismatch (transient, not a fact).
}
```

- [ ] **Step 2: Run, verify failures**

Run: `go test ./internal/bot/ -run TestYooKassaWebhook -v`

- [ ] **Step 3: Implement the handler**

`webhook.go` — `YooKassaWebhookHandler`, structurally mirroring `CryptoBotWebhookHandler` (lines 25-144) with the refetch step inserted:

```go
// YooKassaWebhookHandler processes YooKassa payment notifications. YooKassa
// webhooks are unsigned: the body is used ONLY to learn which payment changed;
// the authoritative state is refetched from the API before any settlement.
func (b *Bot) YooKassaWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if b.yookassa == nil || !b.yookassa.Configured() {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
		body, err := io.ReadAll(r.Body)
		if err != nil {
			b.logger.Error("yookassa webhook: read body", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		notification, err := b.yookassa.ParseWebhook(body)
		if err != nil || notification.PaymentID == "" {
			// Unparseable or factless body: quarantine a digest, ACK so YooKassa
			// does not retry garbage forever (mirrors the crypto parse path).
			digest := sha256.Sum256(body)
			recordErr := b.order.RecordPaymentAnomaly(r.Context(), storage.PaymentAnomaly{
				Provider: storage.PaymentMethodYooKassa, RawPayload: fmt.Sprintf("sha256:%x", digest),
				Reason: "webhook_parse_failure",
			})
			if err == nil && notification != nil && notification.PaymentID == "" {
				w.WriteHeader(http.StatusOK) // valid envelope, no payment id: nothing to do
				return
			}
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("yookassa webhook: malformed body was not quarantined", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if notification.Event != "payment.succeeded" {
			// payment.canceled / refund.* / etc: acknowledge, no auto-settlement.
			w.WriteHeader(http.StatusOK)
			return
		}

		ctx := r.Context()
		// Authoritative refetch — the unsigned body is never trusted for money.
		p, err := b.yookassa.GetPayment(ctx, notification.PaymentID)
		if err != nil {
			b.logger.Error("yookassa webhook: refetch payment", "payment_id", notification.PaymentID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError) // retryable by YooKassa
			return
		}
		if !p.Paid || p.Status != "succeeded" {
			b.logger.Info("yookassa webhook: refetched payment is not terminal",
				"payment_id", p.ID, "status", p.Status)
			w.WriteHeader(http.StatusOK)
			return
		}
		receipt, receiptErr := p.PaymentReceipt()
		if receiptErr != nil {
			anomaly, _ := p.PaymentAnomaly("webhook_invalid_receipt")
			recordErr := b.order.RecordPaymentAnomaly(ctx, anomaly)
			if recordErr == nil || errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
				w.WriteHeader(http.StatusOK)
				return
			}
			b.logger.Error("yookassa webhook: invalid receipt was not quarantined", "payment_id", p.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		outcome, err := b.order.ConfirmPaymentReceipt(ctx, receipt)
		if err != nil {
			// Same idempotent-ACK error classes as the crypto webhook:
			if errors.Is(err, storage.ErrOrderStatusConflict) || errors.Is(err, storage.ErrNotFound) ||
				errors.Is(err, storage.ErrPaymentNeedsReview) || errors.Is(err, storage.ErrPaymentIdentityConflict) ||
				errors.Is(err, storage.ErrPaymentReceiptMismatch) {
				b.logger.Info("yookassa webhook ignored (idempotent)", "payment_id", p.ID, "reason", err)
				w.WriteHeader(http.StatusOK)
				return
			}
			if errors.Is(err, storage.ErrProductOutOfStock) {
				anomaly, _ := p.PaymentAnomaly("out_of_stock_after_charge")
				if recordErr := b.order.RecordPaymentAnomaly(ctx, anomaly); recordErr == nil ||
					errors.Is(recordErr, storage.ErrPaymentNeedsReview) {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			b.logger.Error("yookassa webhook: confirm payment", "payment_id", p.ID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if b.metrics != nil {
			b.metrics.SuccessfulPayments.WithLabelValues("yookassa").Inc()
		}
		order := outcome.Order
		lang := b.userLang(ctx, order.UserID)
		b.send(tgbotapi.NewMessage(order.UserID, fmt.Sprintf(b.t(lang, "payment_success"), order.ID)))
		b.NotifyPaymentOutcome(ctx, outcome)
		b.notifyAdmins(ctx, AdminEventOrderPaid, fmt.Sprintf(b.t("en", "admin_order_paid_yookassa"),
			order.ID, order.UserID, order.TotalRUB))
		b.outWebhook.Send(service.OutboundWebhookEvent{
			Event: "order.paid", OrderID: order.ID, UserID: order.UserID,
			TotalUSD: order.TotalUSD, TotalStars: order.TotalStars,
			Method: "yookassa", PaymentID: p.ID,
		})
		w.WriteHeader(http.StatusOK)
	}
}
```

Add locale key `admin_order_paid_yookassa` to all 5 locales (mirror `admin_order_paid_crypto`, e.g. en: `"💳 Order #%d paid by card (YooKassa)\nUser: %d\nAmount: %.2f ₽"`).

`cmd/bot/http_routes.go`:

```go
type webhookEndpoints interface {
	TelegramWebhookHandler() http.HandlerFunc
	CryptoBotWebhookHandler() http.HandlerFunc
	YooKassaWebhookHandler() http.HandlerFunc
}

func mountWebhookRoutes(mux *http.ServeMux, endpoints webhookEndpoints) {
	mux.Handle("/telegram-webhook", endpoints.TelegramWebhookHandler())
	mux.Handle("/cryptobot-webhook", endpoints.CryptoBotWebhookHandler())
	mux.Handle("/yookassa-webhook", endpoints.YooKassaWebhookHandler())
}
```

Update `fakeWebhookEndpoints` in `cmd/bot/http_routes_test.go` with the third method.

- [ ] **Step 4: Run tests, verify pass + full suite**

Run: `go test ./internal/bot/ ./cmd/bot/ -run "YooKassa" -v` then `go build ./... && go test ./...`

- [ ] **Step 5: Commit**

```bash
git add internal/bot/ cmd/bot/ locales/
git commit -m "feat(bot): YooKassa webhook settles only after authoritative API refetch"
```

---

### Task 7: Ops tooling — payment-review / reconcile / doctor for yookassa

**Files:**
- Modify: `internal/launcher/payment_review.go` (accept `PROVIDER=yookassa`)
- Modify: `internal/launcher/reconcile.go` (only if it enumerates providers — read first; if it is Stars-only by design, leave it and note why in the commit message)
- Modify: `internal/launcher/doctor.go` (report YooKassa config state: configured/partial/rate-missing)
- Modify: `docs/payment-operations.md`
- Test: `internal/launcher/payment_review_test.go`, `internal/launcher/doctor_test.go`

**Interfaces:**
- Consumes: storage review/ledger APIs already provider-generic (check `PaymentReviewProviderUnknown` usage); Task 2 config fields.
- Produces: `make payment-review PROVIDER=yookassa` works; `doctor` surfaces YooKassa misconfiguration (partial creds, HTTPS return URL, rate missing while creds present).

- [ ] **Step 1: Read `internal/launcher/payment_review.go` and its test to find where providers are enumerated/validated**

Run: `grep -n "crypto\|stars\|provider" internal/launcher/payment_review.go | head -30`
Expected: a provider allow-list or switch to extend with `"yookassa"`.

- [ ] **Step 2: Write failing tests**

Mirror the existing crypto cases in `payment_review_test.go`: a quarantined yookassa anomaly (from Task 6's mismatch path) appears in `PROVIDER=yookassa` listing, preview shows amount/currency/order, resolve applies. `doctor_test.go`: partial creds -> actionable error line; creds without `USD_TO_RUB_RATE` -> warning that the RUB button stays hidden; `http://` return URL -> error.

- [ ] **Step 3: Implement** — extend the provider allow-list/switch, doctor checks, and `docs/payment-operations.md` (add a YooKassa section: unsigned-webhook model, what quarantined yookassa facts look like, resolve flow).

- [ ] **Step 4: Run tests, verify pass + full suite**

Run: `go test ./internal/launcher/ -v` then `go test ./...`

- [ ] **Step 5: Commit**

```bash
git add internal/launcher/ docs/payment-operations.md
git commit -m "feat(ops): yookassa provider in payment-review and doctor checks"
```

---

### Task 8: Mini App — `/api/checkout` method `yookassa` + frontend button

**Files:**
- Modify: `internal/webapi/handlers.go` (`handleCheckout` line ~470; server deps struct — find `Crypto` field)
- Modify: `internal/webapi/server.go` (or wherever `deps` is defined — add `YooKassa *payment.YooKassaPayment`)
- Modify: `cmd/bot/main.go` (~line 258 where `Crypto: cryptoPayments` is wired — add YooKassa)
- Modify: `web/app/` checkout UI (find the crypto/stars method choice in `app.js`/`index.html`)
- Test: `internal/webapi/handlers_test.go`

**Interfaces:**
- Consumes: Task 1 adapter, Task 4 `Order.TotalRUB`.
- Produces: `POST /api/checkout {"method":"yookassa"}` → `{"order_id":N,"invoice_link":"https://..."}`; errors: `webapp_err_method` (unknown), `webapp_err_yookassa_disabled` (not configured), `webapp_err_sub_stars_only` (subscription cart).

- [ ] **Step 1: Write failing tests** (mirror the existing crypto checkout tests in `handlers_test.go`):

```go
func TestCheckoutYooKassa(t *testing.T) {
    // Configured adapter (httptest mock) + cart with products -> 200
    //   {order_id>0, invoice_link == mock confirmation_url}; order persisted
    //   with TotalRUB snapshot.
    // Adapter nil/unconfigured -> 400 webapp_err_yookassa_disabled.
    // Subscription cart + method yookassa -> 400 webapp_err_sub_stars_only.
    // Unknown method -> 400 webapp_err_method (unchanged).
}
```

- [ ] **Step 2: Run, verify failures** — `go test ./internal/webapi/ -run YooKassa -v`

- [ ] **Step 3: Implement**

`handlers.go` `handleCheckout`: accept `storage.PaymentMethodYooKassa` in the method check (line ~478); guard `s.deps.YooKassa == nil || !s.deps.YooKassa.Configured()` → `webapp_err_yookassa_disabled`; extend the subscription-only rule to reject yookassa (it currently forces stars, line ~511 — `req.Method != storage.PaymentMethodStars` already covers it, verify); after order creation, when method is yookassa: `amountMinor := int64(math.Round(order.TotalRUB*100))`, guard `<= 0` → `webapp_err_yookassa_disabled`, then `invoice, err := s.deps.YooKassa.CreatePayment(ctx, orderID, amountMinor, desc)` → respond `{"order_id":..., "invoice_link": invoice.PayURL}` (follow how the crypto branch returns `invoice_link`). Add locale key `webapp_err_yookassa_disabled` to all 5 locales if webapi errors are localized through the same files (check `writeError` first — if keys are bot-locale keys, add there).

`web/app/` frontend: where the checkout screen builds payment method buttons (search `method` / `crypto` in `web/app/*.js`), add a RUB option shown only when the API reports it available. Simplest consistent approach: extend `GET /api/me` or the i18n/config endpoint the app already calls with a `yookassa_enabled` + rate flag if one exists; otherwise have checkout attempt `method:"yookassa"` and surface `webapp_err_yookassa_disabled` gracefully. Read `web/app/app.js` checkout section first and follow its existing pattern for the crypto button.

`main.go`: wire `YooKassa: yookassaPayments` into the webapi deps struct (construct once near `cryptoPayments := payment.NewCryptoBotPayment(...)`, line ~230, and reuse the same instance the Bot uses if the Bot exposes it — otherwise construct a second instance from the same cfg values; prefer a single instance via a small accessor on Bot).

- [ ] **Step 4: Run tests, verify pass + full suite** — `go test ./internal/webapi/ -v` then `go test ./...`

- [ ] **Step 5: Commit**

```bash
git add internal/webapi/ cmd/bot/main.go web/app/ locales/
git commit -m "feat(webapi): yookassa checkout method for the Mini App"
```

---

### Task 9: E2E scenario + docs + CHANGELOG

**Files:**
- Modify: `internal/bot/e2e_test.go` (append scenario)
- Modify: `README.md` (Payments section ~line 228), `docs/readme/README.ru.md` (same section), `docs/environment-variables.md`, `docs/getting-started.md` (if it lists payment setup)
- Modify: `CHANGELOG.md` (new `[Unreleased]` section)

**Interfaces:**
- Consumes: everything from Tasks 1-8.
- Produces: regression-protected full buyer journey; user-facing docs.

- [ ] **Step 1: Write the E2E test** (follow the existing full-purchase scenario in `e2e_test.go` — mock Bot API + real SQLite + httptest YooKassa mock):

```go
func TestE2EYooKassaPurchase(t *testing.T) {
    // /start -> catalog -> add to cart -> checkout -> RUB button visible (rate configured)
    // -> pay:yookassa:<id> -> URL button with confirmation_url
    // -> POST /yookassa-webhook {"event":"payment.succeeded","object":{"id":"pay_e2e"}}
    //    with mock API refetch returning succeeded/1849.08 RUB/order metadata
    // -> order paid (method yookassa), stock decremented once, loyalty points
    //    awarded once, user notified, admin notified, outbound webhook fired.
    // -> webhook replay: second POST settles nothing new, both 200.
}
```

- [ ] **Step 2: Run, verify pass** — `go test ./internal/bot/ -run TestE2EYooKassa -v`

- [ ] **Step 3: Docs**

- `README.md` Payments: add a **YooKassa (RUB)** subsection after CryptoBot: what it is (redirect card payment), the three env vars + `USD_TO_RUB_RATE`, the webhook URL to register in the YooKassa merchant cabinet (`<WEBHOOK_URL>/yookassa-webhook`), the unsigned-webhook/refetch security note, and that subscriptions stay Stars-only. Mirror the same text (translated) into `docs/readme/README.ru.md`.
- `docs/environment-variables.md`: table rows for `YOOKASSA_SHOP_ID`, `YOOKASSA_SECRET_KEY`, `YOOKASSA_RETURN_URL`, `USD_TO_RUB_RATE` (required-together note, HTTPS note, default 0 = disabled).
- `CHANGELOG.md`: `## [Unreleased]` → `### Added` YooKassa RUB provider summary (adapter with refetch-verified unsigned webhooks, checkout button, Mini App method, ledger/op-tooling integration, migration 019).

- [ ] **Step 4: Full verification**

Run: `go build ./... && go test ./... && golangci-lint run` (if the binary is unavailable, `go vet ./...`).
Expected: everything green.

- [ ] **Step 5: Commit**

```bash
git add internal/bot/e2e_test.go README.md docs/ CHANGELOG.md
git commit -m "feat(yookassa): E2E purchase scenario, docs and changelog"
```

---

## Self-Review Notes

- **Spec coverage:** adapter+tests (T1), config/rate (T2), storage (T3), shop money math (T4), bot checkout (T5), webhook settlement (T6), ops tooling (T7), Mini App (T8), E2E+docs (T9). Review-focus items map: #1→T6, #2→T4+T6, #3→T2+T4, #4→T6+T9, #5→T5+T6+T8.
- **Type consistency:** `CreatePayment(ctx, orderID int64, amountRUBMinor int64, description string) (*Invoice, error)` used identically in T1/T5/T8; `ConvertUSDToRUB(float64) float64` in T2/T4; `TotalRUB float64` in T3/T4/T5/T6/T8; `PaymentMethodYooKassa = "yookassa"` everywhere.
- **Known ambiguity ruled:** RUB snapshot lives on the order at creation (like TotalUSD/TotalStars), NOT recomputed at payment time — a mid-flight rate change therefore cannot desync an unpaid order's button from its ledger expectation; orders created before the rate was configured have `TotalRUB=0` and simply never show the RUB button (guard `view.TotalRUB > 0` in T5).
- **Out of scope (follow-up plans):** Stripe provider (separate plan, same pattern); YooKassa refunds via API (operator resolves quarantined facts via payment-review; auto-refund handling is a future feature); a polling fallback worker for lost webhooks (YooKassa retries webhooks for days; revisit if operators report stuck pendings).
