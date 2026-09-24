# HANDOFF — состояние проекта и процесс (21.09.2026)

> Живой документ передачи смены. Консолидирует то, что иначе осталось бы только в
> git-ignored SDD-ledger'ах (`.superpowers/sdd/`). Обновлять при каждом крупном этапе.

## 1. Состояние

- **main = `14acb60`**, на 158 коммитов впереди `origin/main` (мерж §6.22 payreview-refunded-orphans).
  **НЕ запушено** — push только с явного согласия владельца. Все фичевые ветки сохранены.
- **Roadmap §4 и HANDOFF §6 полностью закрыты** (§6.22 ✅ 23.09.2026), кроме
  отложенного решением §6.12 (TON re-scans до роста объёмов). Открытой работы нет —
  только push-чеклист (§5) по команде владельца.
- Ворота на HEAD: `go build` + `go vet` + `gofmt -l internal/ cmd/ worker/` (пусто) +
  `go test ./...` — зелёные; `-race` на money-пакетах зелёный.
- Роадмап (§4) закрыт полностью, кроме отложенного решением 4.3 и остатков 4.14/4.15.

## 2. Платёжная матрица (7 рельсов)

| Рельс | Валюта | Settlement | Ключ провайдера |
|---|---|---|---|
| Telegram Stars | XTR (scale 0) | вебхук/long-polling Telegram | `stars` |
| CryptoBot | USD | подписанный вебхук + polling-воркер | `crypto` |
| YooKassa | RUB (scale 2, снапшот `orders.total_rub`) | **неподписанный** вебхук → обязательный авторитетный re-fetch `GetPayment` + backup-poller (60s/24h) | `yookassa` |
| Stripe | USD | подписанный вебхук (HMAC-SHA256 `t.v1`, 300s) → settlement из тела, **без re-fetch** | `stripe` |
| TON | нанотоны int64 (scale 9, снапшот `orders.total_ton_nano`) | **только polling-воркер** (toncenter v2, memo `^order-(\d+)$`, overpay-tolerant `>=`) | `ton` |
| NOWPayments | USD | подписанный IPN (HMAC-SHA512 от сортированного компактного JSON), только `finished` | `nowpayments` |
| Баланс | USD | синхронно в боте (список `users.balance_usd` + `balance_txs` аудит) | `balance` |

Рефанды: `/refund <order_id> [amount]` — two-tap, исполняется для Stars (только полные),
Stripe, YooKassa, баланса; crypto/ton/nowpayments — вручную в дашборде провайдера
(запись в ledger для них — бэклог). Детали: `docs/payment-operations.md` §11.

## 3. Ключевые точки кода

- Адаптеры: `internal/payment/{stars,cryptobot,yookassa,stripe,ton,nowpayments}.go`
  (у всех есть `SetBaseURL` test-seam).
- Деньги: `internal/service/exchange.go` (единственные формулы конверсии),
  `internal/shop/cart.go` (once-at-end totals), `internal/shop/order.go`
  (снапшоты + `ConfirmPaymentReceipt`), `internal/storage/order_state.go`
  (`orderMoney` + `validatePaymentFact` — единая точка amount-правил).
- Ledger: `internal/storage/{ledger,payment_ingress,payment_resolutions,payment_anomalies,payment_recording}.go`;
  миграции 017–022 (`internal/storage/migrations/`).
- Поверхности: `internal/bot/` (handlers_*, admin_*, webhook.go), `internal/webapi/`,
  `web/app/app.js`, `worker/` (polling, ton_polling, yookassa_polling, subscription, …).
- Ops: `internal/launcher/{doctor,payment_review,payment_ingress,reconcile}.go`;
  `make payment-review PROVIDER=…`, `payment-review ingest-provider`, `ingest-stars`.

## 4. Инварианты (полный список — roadmap.md §5)

1. Деньги в minor units на каждой границе; одна формула конверсии на валюту; снапшот
   курса в заказе при создании.
2. Settlement только через проверенный факт: подпись / авторитетный re-fetch / on-chain.
3. Ledger immutable: факты не правятся — только quarantine + resolution.
4. Один provider-ключ = одна миграция CHECK; app-слой принимает только реализованное.
5. Подписки — только Stars на всех поверхностях.
6. Отключённый провайдер ⇒ все checkout-поверхности байт-идентичны.
7. Refunds: provider-first → ledger-second; детерминированные idempotency-ключи
   `refund:<orderID>:<amountMinor>:<paymentID>`; one-refund-per-order gate (settled-only)
   — **load-bearing для баланса** (см. комментарии в `executeRefund`/`BalanceTxTotal`:
   снятие gate требует сначала amount-scoped identity).

## 5. Эксплуатационный чеклист перед запуском

1. `make doctor` — матрица конфигурации всех рельсов.
2. **NOWPayments: один live-test платёж ОБЯЗАТЕЛЕН до включения** — канонизация
   сортированного JSON для IPN-подписи не верифицируема локально против их PHP-сайнера;
   расхождение fail-closed (подписи никогда не пройдут). `docs/payment-operations.md` §8.
3. Зарегистрировать вебхуки: `<WEBHOOK_URL>/yookassa-webhook`, `/stripe-webhook`
   (+ signing secret `whsec_…`), `/nowpayments-webhook` (IPN), `/cryptobot-webhook`,
   `/telegram-webhook`. TON — без вебхука (poller).
4. Курсы: `USD_TO_RUB_RATE`, `USD_PER_TON` (0/пусто ⇒ рельс скрыт; снапшоты заказов
   не пересчитываются).
5. `.env` — все секреты; `.env.example` намеренно с пустыми значениями.

## 6. Открытый бэклог (консолидация из всех ledger'ов)

**Roadmap §4:** 4.3 Coinbase/BTCPay (отложено решением — NOWPayments покрывает спрос);
4.14 ✅ (`c67e66e`), 4.15 ✅ (`dcbbdf6`, durable actor, миграция 023) — **§4 закрыт
полностью, кроме отложенного решением 4.3**.

**Мелкие FOLLOW-UP (без дома, все — polish/покрытие):**
1. NOWPayments-каноникаizer: пин свойства no-HTML-escape (тест с `<>&` в теле) —
   регрессия `SetEscapeHTML(false)` сейчас осталась бы зелёной (fail-closed в проде).
   ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-money-followups.md
2. YooKassa `GetPayment`: покрытие invalid-ID / `toPayment` веток.
   ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-polish-followups.md
3. doctor: 3 из 6 crypto env-ключей без behavioral overlay legs (только list-membership).
   ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-polish-followups.md
4. bot-уровень: `payment_state` pin в yookassa webhook replay-тесте (storage-уровень есть).
   ✅ pin существует с 20.09.2026 (коммит `ee7f926`, roadmap 4.11 — строка бэклога
   устарела) — остаток закрыт 21.09.2026, план
   docs/superpowers/plans/2026-09-21-polish-followups.md (симметричные пины
   stripe/nowpayments).
5. `docs/payment-operations.md` §5: intro таблицы карантина webhook-scoped —
   poller-факты это captures, не anomaly rows (уточнить формулировку).
   ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-polish-followups.md
6. Refunds path-5: provider-success + ledger-failure оставляет только log+chat след —
   best-effort anomaly row / durable outbox как follow-up.
   ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-money-followups.md
7. Баланс-рефанд: fail-closed гард при amount-divergent re-run (существующий
   `order_refund` tx с другой суммой ⇒ сейчас кредит скипается, ledger пишет полную).
   ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-money-followups.md
8. Stars subscription-renewal leg без actor-лога (вне ruled enumeration).
   ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-polish-followups.md
9. `/payreview`: orphan-карточки capture-рода показывают заведомо непроходимые
   Refund/Dismiss (fail-closed, UX); refund-рода path-5-карточки
   (`refund_ledger_failure`, вкл. balance-бакет) до recovery проходят только
   Refund (молча съедает карточку — warning в docs §11), после — только
   Dismiss; `admin_payrev_conflict` вторично как case-gone; ru-коллижинг
   «Подтвердить»/«Подтвердить».
   ✅ закрыто 21.09.2026 (case-gone сообщение, фильтр кнопок orphan-карточек +
   trap/CLI-only подсказки, 64-байт гард, ru-ренэйм), план
   docs/superpowers/plans/2026-09-21-polish-followups.md; CLI
   safeReviewCode-расхождение осталось в §6.16.
10. doctor: TrimSpace-несогласованность (crypto vs остальные); один тест хардкодит
    английский ON-префикс.
    ✅ закрыто 21.09.2026 (фактически admin_paystatus.go, не doctor — ruling P2),
    план docs/superpowers/plans/2026-09-21-polish-followups.md
11. `TestAppendPaymentIngressAuditAcceptsYooKassa` — имя устарело (покрывает stripe+sepa).
    ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-polish-followups.md
12. TON-poller: ~1440 re-scans/сутки settled-платежей (дёшево; сузить окно или
    settled-skip при росте объёмов).
13. `ConvertUSDToNanoTON`: конечные, но >MaxInt64 квотенты (теоретически; rate —
    operator-configured).
    ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-money-followups.md
14. YooKassa poller: warn-префикс `yookassa poller:` vs файловый `YooKassa polling:`.
    ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-polish-followups.md
15. `paymentMethodText` fallback для неизвестного провайдера HTML-эскейпится в
    plain-text карточке `/order` (косметика).
    ✅ закрыто 21.09.2026, план docs/superpowers/plans/2026-09-21-polish-followups.md
16. CLI `payment-review list` рендерит reason-коды через `safeReviewCode` (`=`→`_`),
    бот `/payreview` печатает raw reason — косметическое расхождение для карточек
    `refund_ledger_failure:order=<id>` (park из финального ревью money-followups;
    рассматривать вместе с §6.9).
    ✅ закрыто 23.09.2026 (documented as designed — CLI-санитизация сознательна
    ради парсимого key=value вывода, кода не меняли; note в
    docs/payment-operations.md), план
    docs/superpowers/plans/2026-09-23-micro-followups.md
17. `/payreview` action-фильтр key'уется по reason, не по форме факта: degenerate
    non-digest формы (amount≤0 при не-digest reason — `webhook_invalid_receipt` с
    непредставимой суммой и т.п.) оставляют мёртвый, но fail-closed `[Settle]`
    (polish-followups финал M-2; безопасно — storage отвергает). Радикальное решение:
    exposes amount/external-id presence в целях `ListPaymentReviews` → shape-keyed
    фильтр. Только если станет операторской annoyance.
    ✅ закрыто 23.09.2026 (shape-keyed фильтр в orphan-карточках; R17 CORRECTED:
    предикат зеркалит in-row shape conjuncts, attempt-collision остаётся
    fail-closed в storage), план
    docs/superpowers/plans/2026-09-23-micro-followups.md
18. Test-hardening batch (parked minors polish-followups): renewal E2E-leg не пинит
    settled `payment_events`-строку (T6(1)); `called`-флаг в GetPayment-тестах
    cross-goroutine — atomic/channel для `-race`-строгости (T1(1)); trap-ключ без
    trailing `\n` при cli_only с `\n` (T8(2), косметика, практически недостижимая ветка).
    ✅ закрыто 23.09.2026, план docs/superpowers/plans/2026-09-23-micro-followups.md
19. docs §5/§7: intro-формулировка «order in `needs_review`» loose для digest-only
    строк (нет order identity) — pre-existing nit (polish-followups T5(4)); чинить
    вместе с любым будущим docs-проходом по карантин-таблицам.
    ✅ закрыто 23.09.2026, план docs/superpowers/plans/2026-09-23-micro-followups.md
20. Fleet-wide `loggerFor(ctx)` sweep (4.14 follow-up, spec §8): перевести остальные
    ~200 handler-логов на trace_id-bound логгер (сейчас — только Logging/Recover +
    13 payment-critical); в том же заходе добавить Warn-строку на `middleware/auth.go`
    Upsert-swallow (финал-ревью 4.14, triage #1). Поверхностно, логи только.
    ✅ закрыто 23.09.2026, план docs/superpowers/plans/2026-09-23-micro-followups.md
21. Upgrade-пин для миграций: 022→023 (и будущих) — есть только fresh-DB schema-тест;
    harness-прецедент в `migration_020_test.go` (4.15 финал, M-3). Косметика надёжности.
    ✅ закрыто 23.09.2026, план docs/superpowers/plans/2026-09-23-micro-followups.md
22. `/payreview` orphan-карточки refunded-kind (`refund_parent_not_found` /
    `refund_identity_conflict` / `refund_exceeds_payment` / `refund_invalid_provider_fact`,
    ledger.go:158-167): default-ветка предлагает `[Settle]`, который storage никогда не
    примет (refunded kind требует `accepted_refund`), и НЕ предлагает `[Refund]`, который
    `explicitNoAttemptAnomalyDecision` принял бы при наличии external+related ids
    (micro-followups финал, observation #4 — pre-existing, shape-фильтр §6.17 только
    сокращает мёртвые кнопки). Reason-aware mapping действий для refunded-orphans.
    ✅ закрыто 23.09.2026, план
    docs/superpowers/plans/2026-09-23-payreview-refunded-orphans.md

## 7. Процесс (как велась работа — воспроизводим)

Superpowers SDD-конвейер на каждый план:
1. План-файл `docs/superpowers/plans/<date>-<name>.md` (Tasks с Files/Interfaces/Steps,
   Self-Review Notes: spec coverage, known ambiguities, out of scope).
2. Ветка от main; ledger `.superpowers/sdd/<plan>/progress.md` (ruling'и, статусы,
   deferred minors — git-ignored, живёт на диске).
3. На задачу: `task-brief <plan> <N>` → свежий implementer-субагент (TDD: RED→GREEN,
   no-subagents contract, полный отчёт в `task-N-report.md`) → `review-package <plan>
   <BASE> <HEAD>` → reviewer-субагент (spec+quality, named risks, read-only) →
   Minor в ledger (не в fix loop), Important/Critical → fix round (resume или свежий
   агент с verbatim finding) → scoped re-review.
4. Финал: broad whole-branch review (max-tier) с triage всех deferred → merge --no-ff
   в main локально (push — только явное согласие).
5. Спорные вопросы — controller ruling с записью в ledger; остановка только для:
   необратимых операций, security-sensitive действий, внешних side-эффектов,
   полностью сломанного плана.

Скрипты: `/home/thom/.cache/opencode/npm/git-superpowers-0958a5557860/1789807392434/node_modules/superpowers/skills/subagent-driven-development/scripts/{task-brief,review-package}`.

Модели субагентов (сложившаяся диспетчеризация): implementer/reviewer — `alibaba-cn/glm-5.3`;
scoped re-review и низко-рисковые ревью — `alibaba-cn/qwen3.8-flash`;
финальные whole-branch — `alibaba-cn/qwen3.8-max-0902`.

**Важные уроки процесса:**
- Ревьюер-субагенты иногда ошибаются в фактах («no production caller» в YooKassa T7 —
  оказалось ложным): контроллер проверяет нагрузочные утверждения сам (grep) перед ruling.
- `gofmt -l` невидим в диффах — включать в ворота каждой задачи.
- Промежуточные «as-found» пины в E2E легитимны, но должны обновляться фиксом
  (TON notifications, Task 12b).
- verb-parity тест локалей (`TestLocaleFilesHaveMatchingPrintfVerbs`) ловит класс
  багов перестановки `%d/%s` в переводах — держать зелёным.
- Plan-дефект, найденный по ходу (BLOCKED имплементера / Important ревьюера): контроллер
  верифицирует факт сам против кода → CORRECTED-ruling в ledger → если задача ещё НЕ
  исполнена: amend плана + regenerate brief + re-dispatch; если исполнена: fix round с
  controller-drafted точным текстом. Прецеденты: P7 (renewal semantics), P8 (taxonomy).
- Pre-authorized stop-условия в диспатче («если DB-asserts упадут до log-assert — STOP и
  BLOCKED») работают: T6 поймал план-дефект ДО коммита. Включать их в рискованные задачи.
- Minor elevating: parked Minor становится fix-now, если от него зависит truthfulness
  ДОКУМЕНТОВ следующей задачи (прецедент T8(1): CHANGELOG-claim «offer only the actions
  that can actually pass» был бы ложен без полноты digest-набора).
- Docs-truthfulness — самый рискованный класс: controller-supplied проза §5 ошибалась
  ДВАЖДЫ (P8, затем I-1). Правила: (а) перед поставкой прозы верифицируй полный механизм
  grep'ом ВСех writer-сайтов (не только названных в finding); (б) reviewer таких задач
  делает named-risk truth-checks против кода, модель glm-5.3 даже для «docs-only»;
  (в) фраза-классификатор должна быть per-reason/per-mechanism, а не per-path — пути
  часто разделяют один gate.
- Ревьюер-модель: «docs-only» НЕ автоматически flash — оператор-facing money-доки
  (CHANGELOG/§5/§11/§12) ревьюит glm-5.3.

## 8. Deliverables-реестр (15 планов)

| План | Ветка | Merge | Коммитов | Fix-раундов |
|---|---|---|---|---|
| YooKassa provider | feat/yookassa-provider | `7b064b3` | 21 | 1 (T7 preview parity) |
| Stripe provider | feat/stripe-provider | `032e7b5` | 9 | 0 |
| Crypto expansion (TON+NOWPayments) | feat/crypto-expansion | `2a63e17` | 15 | 1 (T9 out-of-stock) + Task 12b (notifications) |
| Admin full-management | feat/admin-management | `3fabd4a` | 8 | 1 (T3 idempotent debit) |
| Quality sweep | chore/quality-sweep | `45b17b3` | 9 | 1 (T1 middleware ctx) |
| Backlog follow-ups (4.6, 4.8–4.12) | chore/backlog-followups | `1926a1e` | 4 | 0 |
| YooKassa poller (4.5) | feat/yookassa-poller | `5e22ae4` | 4 | 0 (+warn fold) |
| Admin refunds (4.4) | feat/admin-refunds | `37cb865` | 7 | 2 (truthful guidance; amount-bearing recovery) |
| Ctx + attribution (4.7, 4.13) | chore/ctx-attribution | `0f625b7` | 2 | 0 |
| Money follow-ups (§6.13/6.1/6.7/6.6) | chore/money-followups | `4dd7842` | 8 | 2 (T2 comment direction; финал: CLI balance-бакет + docs-truthfulness) |
| Polish follow-ups (§6.2/3/4/5/8/9/10/11/14/15) | chore/polish-followups | `5420f3b` | 15 | 3 (T5 P8-таксономия; T8(1) elevated digest-set; финал I-1 §5 recast) + T6 P7 re-dispatch (pre-commit BLOCKED) |
| Update-ctx + trace (4.14) | feat/update-ctx-trace | `c67e66e` | 12 | 2 (T9 grep-recipe elevated; финал I-1 main.go shutdown-комментарий) |
| Durable actor column (4.15) | feat/durable-actor-column | `dcbbdf6` | 8 | 3 (T3 webhook digest-продюсеры; T4 recordRefundAnomaly + trim; T6 stale-комментарии + changelog-tension) + финал I-1 (worker:crypto quarantine stamp) |
| Micro-followups (§6.16–21) | chore/micro-followups | `f1dd838` | 8 | 3 (T1 R17 CORRECTED после pre-authorized BLOCKED; T2 +2 atomic-сайта; T3 NOWPayments sibling) + T4 fix (acceptance leg); финал: **Yes** без fixes |
| Payreview refunded-orphans (§6.22) | fix/payreview-refunded-orphans | `14acb60` | 2 | 0; финал: **Yes** без fixes |

Плюс: roadmap rewrite (`f6fb155`, `46ab401`, обновления в задачах) и controller-janitorial
коммиты (gofmt `36a0c84`, coupling-комментарии `9697320`).

## 9. Rulings digest закрытых планов (ledger'ы удалены — раздел служит durable-рекордом)

> Pre-plan ruling'и Полным текстом живут в самих планах (committed): «Design Rulings» R1–R7
> в `docs/superpowers/plans/2026-09-21-money-followups.md`, P1–P12 (вкл. P7/P8 CORRECTED)
> в `docs/superpowers/plans/2026-09-21-polish-followups.md`. Здесь — сжатая сводка +
> внутри-сессионные решения, которые в планах не отражены.

**Money-followups (merge `4dd7842`, финал «With fixes» → fix → re-review APPROVED):**
- R1: path-5 след = ORPHAN-аномалия (ProposedOrderID=0, БЕЗ needs_review-флипа — флип
  заблокировал бы /refund re-run remedy за settled-only гейтом); order-link в reason+payload.
- R2: `balance` в bot payReviewProviders. R3: `BalanceTxExists`→`BalanceTxTotal`
  (existence+net); LOAD-BEARING комментарии на 3 сайтах. R4: divergence-гард в balance-ветке
  `executeRefund`, существующее сообщение, без новых ключей. R5: операторские сообщения
  байт-неизменны. R6: детерминированный RawPayload → одна строка на повторные сбои.
  R7: outbox/миграция отклонены (несоразмерно).
- A: printf-фикс snippet'а брифа принят (дефект плана). B: rename в доке `refundableOrder`
  → в T4. C: исторические упоминания `BalanceTxExists` остаются. D: docs sweep stale-ссылок.
  T3-minor: mixed-units текст ошибки KEEP as specced.
- Финал-fix: **E** — CLI `payment-review` принимает `balance` (allow-list+usage; ingest-*
  НЕ тронуты — balance-факты синтетические); **F** — I-2 docs-only (§11 «Pre-recovery trap»
  warning + полярность §6.9; БЕЗ code-гейта — orphan `accepted_refund` есть механизм
  операторского признания); **G** — docs-pass (eight buckets, §10 scoping, §2 += balance).

**Polish-followups (merge `5420f3b`, финал «With fixes» → fix → re-review ADDRESSED):**
- P1: §6.4 устарел (пин с `ee7f926`); остаток — stripe/nowpayments симметрия. P2: §6.10
  misattributed → `admin_paystatus.go`, trim-унификация (whitespace ⇒ OFF; production
  env-path уже тримится config.Load — фикс есть защитная консистентность). P3: ErrNotFound →
  `admin_payrev_case_gone` на 3 сайтах. P4: orphan-фильтр кнопок по reason-семействам
  (UX-фильтр, НЕ гейт; storage — финальный валидатор). P5: ru settle → «Урегулировать».
  P6: 64-байтовый callback-гард (fail-closed → CLI-only). P9–P12: scope/rename-решения (в плане).
- **P7 CORRECTED** (T6 BLOCKED pre-commit): renewal СОХРАНЯЕТ первоначальный
  `subscriptions.telegram_charge_id` (ch-sub-1 — на нём держится cancel-leg);
  `renewSubscriptionTx` продлевает ТОЛЬКО expires_at и требует STRICT-продления
  (subscription_orders.go:111/:128) → helper +5s; новый charge = succeeded `payment_attempts`-
  строка (payment_recording.go:370-388). План amended `155055f`.
- **P8 CORRECTED ×2** (T5 fix `045f200`, затем финал I-1 fix `97af70f`): финальная таксономия
  карантинов — per-REASON-class, НЕ per-path: `receipt_mismatch`/`unknown_order`/
  `identity_conflict`/webhook-digest'ы/`webhook_invalid_receipt`/webhook-`out_of_stock` =
  anomaly-строки; `second_charge`+`capture_after_terminal_state` (order.go:419-423) и
  `capture_on_unresolved_order` (:373-378) = captures ШАРЕННОГО `ConfirmPaymentReceipt`-гейта
  (достигают ОБА пути — webhook.go:236 и поллеры); `out_of_stock_after_charge` — ЕДИНСТВЕННЫЙ
  path-split reason (webhook=anomaly :247, poller=capture :136). Оба механизма флипают order
  в needs_review и видны в review-очереди.
- **T8(1) ELEVATED** (fix `1ba1c6d`): digest-only набор ПОЛОН = {`webhook_parse_failure`,
  `webhook_missing_payment_id`, `stars_update_decode_failure`}; elevation-основание:
  load-bearing для truthfulness docs Task 10 (см. §7 урок).
- **FW-1** (финал I-1, fix `97af70f`): §5 intro+параграф переложены mechanism-first
  per-reason-class; CHANGELOG-парентеза «per path»→«per reason class»; T5(3)/(4)/(5)/(6)
  свёрнуты; §7 не тронут (true as far as it goes; M-1 subset-enumeration PARK → §6.17-19 дома).
- Model overrides: T10-ревью и final-fix re-review — glm-5.3 (docs-truthfulness).
- Парковки финала: M-1 (§7 subset), M-2 (shape-keyed фильтр невозможен bot-side — дом §6.17),
  T7(1) (preview default-arm → failed — намеренно, правдивее), T8(2)/T8(3) и прочие report/
  cosmetic-ниты — дома в §6.18-19 либо списаны как report-only.

**Update-ctx-trace / 4.14 (merge `c67e66e`, финал «With fixes» → fix → re-review ADDRESSED):**
- Design rulings Q1–Q8 — в spec (IMPLEMENTED). Plan-level: **R1** — spec-Q7 TODO-маркеры
  отменены, completion-proof = `grep handlerCtx` → EMPTY (равная сила, меньше шума);
  **R2** — `HandleUpdate(root, update)`: webhook передаёт `r.Context()`, usability-smoke —
  `context.Background()`; **R3** — `sendMainMenu` остаётся ctx-LAST, `handleWizardPhotoStep`
  уже ctx-first — без нормализации (минимальный diff).
- **R4**: T9-minor (grep-рецепт `trace_id=<id>` валиден только для text-handler; production
  JSON — `"trace_id":"<id>"`) ELEVATED по docs-truthfulness правилу §7 → fix round
  (двуформатный рецепт) → scoped re-review ADDRESSED. Второй minor («(§12 above)») parked.
- Финал I-1 (единственный Important за весь план): stale shutdown-комментарий в
  `cmd/bot/main.go` («per-handler») — fix `b501509`, re-review подтвердил truthfulness
  всех трёх утверждений нового комментария.
- Триаж deferred minors финала (все PARKED с причинами): auth Upsert-swallow (pre-existing;
  warn-строка — в §6.20 fleet-sweep), SetRootContext×3 в тесте (test-local), blank-line
  removals (gofmt-идиоматика), report-арифметика T6/T8 (process-артефакты), «(§12 above)».
- Отступления принятые по ходу: T1 extra-file `admin_refunds_test.go` (прямой вызов
  `e.handle` — forced minimal fix, plan-gap); T7 четвёртый stale-комментарий (bot.go:231,
  plan-gap, минимальная правка). Оба — plan-дополнения controller-approved.
- Ревьюеры: T1–T4/T6/T8/T9 glm-5.3, T5/T7/re-reviews qwen3.8-flash, финал qwen3.8-max-0902.
  -race гейты: T1/T4/T6/T7 зелёные (включая double-tap refundMu pin).

**Durable-actor-column / 4.15 (merge `dcbbdf6`, финал «With fixes» → fix wave → re-review ALL ADDRESSED):**
- Design rulings D1–D5 — в spec (IMPLEMENTED): обе таблицы (events+anomalies), явный
  fact-envelope flow (НЕ ctx-extraction), литералы = 4.13-таксономия, empty→NULL
  (NULLIF, атрибуция никогда не гейтит деньги), no trace_id-колонки, NULL-render
  byte-identical.
- **R1**: spec-инвентарь «5 INSERT sites» исправлен на 6 (main capture = `observePayment`,
  не `payment_recording.go:383` — то renewal); UPDATE-disposition путь actor не трогает.
- **R2**: actor НЕ входит в anomaly fingerprint (ingress-metadata, не fact identity;
  same-fact-different-ingress дедупится по факту, first writer's actor wins).
- **R3**: `PaymentEvent` model БЕЗ Actor (нет consumer; только `PaymentReviewTarget.Actor`).
- **R4** (T1, reviewer-verified): identity_conflict synthetic row (`payment_recording.go:596`)
  достижим только с fact==nil → NULL actor — единственно честное значение (не gap).
- **R5** (T3 fix round, plan-gap): provider-webhook digest/invalid-receipt аномалии
  (4 литерала + 7 adapter-legs) стемпятся в webhook.go по месту (адаптеры
  transport-neutral, НЕ тронуты). Discovery-канал — concern эскалация имплементера.
- **R6** (T4 fix round, plan-gap): `recordRefundAnomaly` actor = refund.Actor → fallback
  audit.Actor; CLI `--actor` TrimSpace для паритета fact/audit.
- **R7** (T6 elevations ×4): 6 stale-комментариев «no durable actor row» переписаны;
  CHANGELOG 4.13-буллет помечен superseded; balance-scope уточнён (captures NULL,
  refunds admin:<tgID>); §12 «is log-level»→«was».
- **Финал I-1**: crypto-worker `polling_invalid_paid_invoice` quarantine без actor →
  stamp `worker:crypto` + пин (dcfc08d). Deferred minors: все PARKED с причинами
  (см. реестр ниже; M-3 upgrade-пин → §6.21).
- Урок процесса (повторился 2×: T3, T4): producer-инвентарь в спеке неполон →
  concern-эскалации имплементеров ловят gap'ы до мержа. Правило «STOP при расхождении
  с инвентарём» в диспатчах работает.

**Micro-followups / §6.16–21 (merge `f1dd838`, финал «Yes» без fixes):**
- **R16**: §6.16 resolved as document-as-designed (CLI `safeReviewCode` — сознательная
  санитизация парсимого key=value вывода; бот-карточка — raw). Код не тронут.
- **R17 CORRECTED** (T1 pre-authorized BLOCKED поймал план-дефект): storage settle-gate
  богаче двух конъюнктов (amount/currency/scale/external∨legacy/kind=captured/no-attempt).
  Предикат фильтра оставлен `(amount>0 && external_id!="")`: точное разделение на
  production-reachable строках (остальные конъюнкты write-enforced/unreachable/уже мертвы
  сегодня — фильтр строго СОКРАЩАЕТ мёртвые кнопки); attempt-collision незеркалируем
  любым in-row фильтром → storage fail-closed. Комментарий у фильтра фиксирует это.
- **R18a**: renewal-пин расширяет `TestE2E_SubscriptionLifecycle` (amount vs live
  `orders.total_stars`, charge `ch-sub-2`).
- **R20**: sweep-правило (ctx-param only; bot.go/webhook.go/update_ctx.go исключены);
  148 конверсий, 5 законных остатков (ctx-less helpers), 0 double-wrap; reviewer
  механически сверил все пары 1:1.
- **R20b**: `middleware.Auth(users, logger)`; upsert-swallow → Warn (без trace_id —
  middleware-пакет не видит bot ctx-key, нет circular import; pinned в auth_test).
- Fix-расширения по ходу: T2 +2 unlisted yookassa atomic-сайта (repo-grep: cross-goroutine
  bool-флагов больше нет нигде); T3 NOWPayments §8 sibling qualification; T4 acceptance-leg
  (doc-comment overclaim — Important от ревьюера, brief-дефект).
- Триаж финала (все PARKED): whitespace-vs-TrimSpace asymmetry (unreachable), e2e line-ref
  comment rot, TON §7 loose wording (unknown_order — другой класс; кандидат в будущий
  docs-проход), T4 probe-strictness, report-арифметика, CHANGELOG exclusions
  parenthetical. Новый хвост → §6.22 (refunded-orphan action mapping, финал observation #4).
- Урок: concern-эскалации имплементеров + pre-authorized BLOCKED поймали 3 план-дефекта
  до коммита в этом батче (T1 predicate, T2 2 сайта, T4 overclaim) — дисциплина окупается.

**Payreview refunded-orphans / §6.22 (merge `14acb60`, финал «Yes» без fixes):**
- **R22a**: ветка key'уется по `EventKind` (schema-checked данные), НЕ по reason-строке
  (замкнутое множество причин гнилось бы).
- **R22b**: Dismiss на refunded-orphan НЕ предлагается — storage отвергает empty-decision
  до появления evidence-строк (полярность trap-карточки); только `[Refund]` при полном
  money-tuple, иначе CLI-only.
- **R22c**: event-targets тоже несут EventKind (колонка уже сканировалась), RelatedExternalID
  — только anomaly-targets.
- Финал-ревью (qwen3.8-max) подтвердил по широкой линзе: НИКАКАЯ bot-поверхность (кнопка
  или crafted callback) не ведёт к settle/compensated на refunded-orphan; TOCTOU-discipline
  preview→confirm сохранена; CHANGELOG-claim истинен против гейта.
- Residual (принят, зафиксирован в коде комментарием): replay уже-succeeded refund id
  (evidence row существует) незеркалируем shape-фильтром — fails visible at preview,
  post-evidence Dismiss остаётся CLI-only. Если станет operator-annoyance: добавить
  `Currency` в target shape (механизм read-back уже есть).
- Janitorial incl.: §6.22 ✅ indent-фикс (5→4), in-code comment residual acknowledgment.

**Cross-branch session review (23.09.2026, qwen3.8-max-0902, дельта 716b4ba..ce97792 — 4 ветки сессии):**
вердикт **bug-free**, все 6 пар взаимодействий clean. Minor (принят, зафиксирован в коде
admin_payreview.go): зеркало фильтров §6.17/§6.22 неполно в обе стороны — currency-конъюнкт
не зеркалится (только патологические строки, fail-visible) и legacy_capture_unverifiable
исключение (orphan-unreachable). Если станет operator-annoyance: `Currency` в
PaymentReviewTarget — механизм read-back есть. Informational для эксплуатации: Stars polling
settles атрибутированы `webhook:stars` (раздельный литерал, по дизайну); provider-webhook
settle-логи без trace_id (4.14 scope); 30s budget теперь per-update (не per-handler).
