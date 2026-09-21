# HANDOFF — состояние проекта и процесс (21.09.2026)

> Живой документ передачи смены. Консолидирует то, что иначе осталось бы только в
> git-ignored SDD-ledger'ах (`.superpowers/sdd/`). Обновлять при каждом крупном этапе.

## 1. Состояние

- **main = `4dd7842`**, на 102 коммита впереди `origin/main`. **НЕ запушено** — push
  только с явного согласия владельца. Все фичевые ветки сохранены (не удалены).
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
4.14 per-update ctx + trace propagation (цепочка middleware `func(tgbotapi.Update)`);
4.15 durable actor column в ledger (сейчас webhook/worker settles атрибутированы
только в логах — §12 payment-operations).

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

## 8. Deliverables-реестр (10 планов)

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

Плюс: roadmap rewrite (`f6fb155`, `46ab401`, обновления в задачах) и controller-janitorial
коммиты (gofmt `36a0c84`, coupling-комментарии `9697320`).
