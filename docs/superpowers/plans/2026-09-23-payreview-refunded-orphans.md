# §6.22 refunded-orphan action mapping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** `/payreview` orphan cards for refunded-kind anomalies offer exactly the actions storage can accept: `[Refund]` (→ `accepted_refund`) when the anomaly carries the full money tuple, CLI-only otherwise. The dead `[Settle]` disappears from refunded-kind cards.

**Architecture:** Two new shape fields on the anomaly review target (`EventKind`, `RelatedExternalID`), one kind-aware branch in `payReviewActions`. Storage remains the final validator (P4); the filter mirrors the storage precondition (R17 CORRECTED pattern).

**Tech Stack:** Go, SQLite, tgbotapi — no new dependencies.

**Spec:** none (bounded follow-up). Authority: HANDOFF §6.22 + the verified storage gate `explicitNoAttemptAnomalyDecision` (`internal/storage/payment_resolutions.go:887-920`).

## Global Constraints

- Branch: `fix/payreview-refunded-orphans` off main `f0e6964`. One commit. No push.
- Verified storage precondition for a refunded-kind orphan anomaly (no `refunds` row): decision must be `accepted_refund` AND `TrimSpace(externalID) != ""` AND `TrimSpace(relatedID) != ""` AND shared head (`amount>0`, `currency!=""`, `scale∈[0,9]`, decision≠""). Dismiss (`""` decision) is rejected there — it only derives post-evidence (the trap-card polarity at admin_payreview.go trap branch).
- No locale changes (button labels exist). No new dependencies. No migrations.
- Refunded-kind orphan reasons (closed set, written by `recordRefundAnomaly`): `refund_parent_not_found`, `refund_identity_conflict`, `refund_exceeds_payment`, `refund_invalid_provider_fact`. The `refund_ledger_failure:order=N` trap branch is EARLIER in the switch and stays untouched.
- Gates: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/` (EMPTY) `&& go test ./...` (timeout ≥600000ms).

## Review Focus

1. **Branch polarity:** refunded-kind check must key on the target's `EventKind` (data), NOT the reason string (reasons rot; the kind is schema-checked). Pin: unit test with a refunded-kind target whose reason is NOT in the known set.
2. **Precondition fidelity:** the offered `[Refund]` must pass storage on production-reachable rows — predicate = amount>0 ∧ external≠"" ∧ related≠"" (currency/scale write-enforced at insert). Pin: end-to-end resolve-apply test (shaped refunded orphan → Refund action → `ResolvePaymentReview` succeeds, row leaves the queue).
3. **No regression on the trap + captured paths:** `refund_ledger_failure` branch and the captured-kind shape filter byte-identical. Pin: existing payreview tests stay green unchanged.

---

### Task 1: refunded-orphan action mapping

**Files:**
- Modify: `internal/storage/models.go` (`PaymentReviewTarget` — two fields)
- Modify: `internal/storage/payment_resolutions.go` (both SELECTs + target construction)
- Modify: `internal/bot/admin_payreview.go` (`payReviewActions` default branch)
- Test: `internal/storage/payment_resolutions_test.go` (read-back), `internal/bot/admin_payreview_test.go` (action sets + card + resolve-apply)
- Modify: `CHANGELOG.md` (`[Unreleased]`), `docs/superpowers/HANDOFF.md` (§6.22 ✅)

**Interfaces:**
- Consumes: `PaymentReviewTarget` (AmountMinor/ExternalID from §6.17).
- Produces: `PaymentReviewTarget.EventKind string` (populated for event AND anomaly targets) + `PaymentReviewTarget.RelatedExternalID string` (anomaly targets only; zero for event/order targets).

- [ ] **Step 1: Failing tests**
  - Bot unit (extend the existing orphan action-set test file region): refunded-kind target with `AmountMinor: 100, ExternalID: "rf-1", RelatedExternalID: "cap-1"` → actions == `[refund]`; refunded-kind with `RelatedExternalID: ""` → nil; refunded-kind whose reason is NOT one of the known four (kind-keyed, not reason-keyed) → still `[refund]` when shaped; captured-kind behavior unchanged.
  - Storage: anomaly target carries EventKind/RelatedExternalID; event target carries EventKind (== kind inside its `event_<kind>` ReasonCode) and zero RelatedExternalID.
  - End-to-end pin: seed a refunded-kind orphan anomaly via `RecordPaymentAnomaly` (full shape), drive the bot resolve-apply flow for the Refund action (mirror the existing `TestPayReviewOrphanAnomalyResolvesByExactTarget` pattern in admin_payreview_test.go) → resolution succeeds AND the anomaly leaves `ListPaymentReviews`.
- [ ] **Step 2: Run RED** — `go test ./internal/bot/ -run 'PayReview' -count=1` + storage focused → FAIL (no fields / no branch).
- [ ] **Step 3: Implementation**
  - `models.go` `PaymentReviewTarget` — append:
    ```go
    	// EventKind is the row's event kind ("captured"/"refunded") — populated for
    	// event and anomaly targets. RelatedExternalID is the parent capture's id —
    	// anomaly targets only (zero for event/order targets).
    	EventKind         string
    	RelatedExternalID string
    ```
  - `payment_resolutions.go` events SELECT: target literal gains `EventKind: eventKind` (the column is already scanned). Anomaly SELECT: add `a.event_kind, a.related_external_id` after `a.external_id`; extend the Scan (`&eventKind, &relatedID` after `&externalID`); target gains both fields.
  - `admin_payreview.go` `payReviewActions` default branch — insert BEFORE the captured-shape check:
    ```go
    		default:
    			t := item.Targets[0]
    			// Refunded-kind orphan (refund ingress failures): storage accepts
    			// ONLY accepted_refund and only with the full money tuple —
    			// amount + refund id + parent capture id
    			// (explicitNoAttemptAnomalyDecision refunded branch). Dismiss is
    			// deliberately not offered: with no refunds row recorded it is
    			// fail-closed until evidence arrives (trap-card polarity).
    			// Kind-keyed, not reason-keyed: reasons rot, kinds are
    			// schema-checked.
    			if t.EventKind == storage.PaymentEventRefunded {
    				if t.AmountMinor > 0 && t.ExternalID != "" && t.RelatedExternalID != "" {
    					return []string{payReviewActionRefund}
    				}
    				return nil
    			}
    ```
    then the existing shape-keyed settle check follows unchanged.
- [ ] **Step 4: Docs** — CHANGELOG `[Unreleased]` Quality, one bullet:
  ```markdown
  - `/payreview` refunded-kind orphan cards now offer `[Refund]` only when the
    anomaly carries the full money tuple (amount + refund id + parent capture
    id) — the only decision storage accepts there; degenerate rows are CLI-only
    (HANDOFF §6.22 closed).
  ```
  HANDOFF §6.22 gains the ✅ annotation line in the house style (`✅ закрыто 23.09.2026, план docs/superpowers/plans/2026-09-23-payreview-refunded-orphans.md`).
- [ ] **Step 5: Gates** — standard + `go test ./internal/bot/ -run 'PayReview' -count=1`.
- [ ] **Step 6: Commit**

```bash
git add internal/storage/ internal/bot/ CHANGELOG.md docs/superpowers/HANDOFF.md
git commit -m "feat(payreview): refunded-orphan cards offer shape-gated Refund (HANDOFF §6.22)"
```

---

## Self-Review Notes

- **Coverage:** §6.22 fully — kind-keyed branch, storage-mirrored shape predicate, Dismiss deliberately withheld (fail-closed, documented in-code), e2e resolve-apply pin proves the offered button passes storage.
- **Type consistency:** field names identical across models/storage/bot; `storage.PaymentEventRefunded` constant reused (no string literal).
- **No overlap risk:** single task, single commit; the trap branch and §6.17 filter are untouched context.
- **Post-merge janitorial (controller):** HANDOFF §1 state + §8 registry row (15 планов) + §9 digest line.
