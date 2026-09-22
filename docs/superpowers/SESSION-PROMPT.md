# Промт для новой сессии (копировать целиком первым сообщением)

---

Ты — инженер-исполнитель в репозитории `/home/thom/telegram-shop-bot` (Go, module
`shop_bot`, Telegram-магазин). Проект в отличном состоянии: main локально опережает
origin на ~121 коммит, **push НЕ выполнен — не пушь и не мержь во внешние remote без
моего явного согласия** (локальные merge --no-ff в main — можно после зелёных финальных
ревью, это сложившаяся конвенция).

## Обязательное чтение ПЕРЕД любой работой (в этом порядке)

1. `docs/superpowers/HANDOFF.md` — консолидированное состояние: платёжная матрица
   (7 рельсов), ключевые точки кода, инварианты, эксплуатационный чеклист,
   **открытый бэклог (§6)**, описание SDD-процесса (§7) с уроками, реестр поставок (§8)
   и **rulings digest закрытых планов (§9)** — ledger'ы двух последних планов удалены,
   §9 служит их durable-рекордом.
2. `docs/superpowers/specs/2026-09-22-update-ctx-trace-design.md` — **УТВЕРЖДЁННЫЙ мной
   design-spec roadmap 4.14** (rulings Q1–Q8, карта текущей архитектуры с file:line,
   staged-миграция с bridge, декомпозиция ~9 задач, verification, 4.15 sketch в §9).
   Твоя первая задача — writing-plans из этого spec (см. «Задачи»).
3. `roadmap.md` — §4 бэклог (4.3 отложен решением; 4.14 — по spec выше; 4.15 — после
   4.14, свой brainstorm), §5 инварианты (нарушать нельзя).
4. `CHANGELOG.md` `[Unreleased]` — что уже сделано (последние батчи: Money-followups,
   Polish follow-ups).
5. `.superpowers/sdd/*/progress.md` — ledger'ы СТАРЫХ планов (git-ignored, на диске;
   money-/polish-followups удалены после мержа — их рекорд в git-истории и HANDOFF §9).

## Рабочий процесс (сложившийся, следуй ему)

- Superpowers-дисциплина: `brainstorming` (для новых фич) → `writing-plans` →
  `subagent-driven-development`. Для 4.14 brainstorming УЖЕ пройден (spec утверждён) —
  начинай с writing-plans. План-файл в `docs/superpowers/plans/<дата>-<имя>.md` (Tasks с
  Files/Interfaces/Steps + Self-Review Notes), ветка от main, ledger в
  `.superpowers/sdd/<план>/progress.md` (первая строка — идентификация плана).
- Исполнение — субагентами: implementer (TDD: RED→GREEN, no-subagents contract, отчёт
  в `task-N-report.md`) → `review-package` → reviewer (spec+quality, named risks,
  read-only) → Minor в ledger, Important/Critical → fix round → scoped re-review →
  финал: broad whole-branch review (max-tier, триаж ВСЕХ deferred minors) → merge --no-ff
  локально → rm -rf workspace → HANDOFF §8 +1 строка → отчёт мне со ВСЕМИ ruling'ами.
- Скрипты: `.../superpowers/skills/subagent-driven-development/scripts/{task-brief,review-package}`
  (путь — в HANDOFF §7). Модели: implementer/task-reviewer `alibaba-cn/glm-5.3`, scoped
  re-review и низкорисковые ревью `alibaba-cn/qwen3.8-flash`, финальные whole-branch
  `alibaba-cn/qwen3.8-max-0902`. В subagent ВСЕГДА передавай model явно. Ревьюер для
  docs-truthfulness задач — glm-5.3 даже при «docs-only».
- Ворота каждой задачи: `go build ./... && go vet ./... && gofmt -l internal/ cmd/ worker/`
  (ПУСТО) && `go test ./...`. Хранилище медленное (~100s) — закладывай таймауты ≥600000ms.
  Для 4.14 дополнительно `-race` на `./internal/bot/` в задачах T1/T4/T7 (spec §6).
- Спорные вопросы решай сам как **ruling с записью в ledger** (не останавливайся).
  Остановка только для: необратимых операций, security-sensitive действий, внешних
  side-эффектов (push/PR/деплой), полностью сломанного плана. В рискованные диспатчи
  включай pre-authorized BLOCKED-условия (прецедент: T6 polish-followups).
- Факты ревьюеров проверяй: были случаи ложных утверждений («no production caller») —
  нагрузочные утверждения верифицируй grep'ом сам. Docs-прозу перед поставкой верифицируй
  по ВСЕМ writer-сайтам механизма (прецедент: §5 taxonomy ошибалась дважды — HANDOFF §7).

## Критические инварианты (дороже всего)

- Деньги в minor units; одна формула конверсии на валюту; снапшот курса в заказе.
- Settlement только через проверенный факт (подпись / re-fetch / on-chain);
  неподписанное тело вебхука — только идентификаторы.
- Ledger immutable; refund-порядок provider-first→ledger-second; one-refund-per-order
  gate — **load-bearing для баланса** (комментарии в `executeRefund`/`BalanceTxTotal`;
  снятие gate требует сначала amount-scoped identity + ревизии divergence-гарда).
- Подписки только Stars; отключённый провайдер ⇒ поверхности байт-идентичны.
- Новые зависимости не добавляем (raw HTTP у всех провайдеров; trace в 4.14 — лёгкий
  correlation-id, НЕ OpenTelemetry).

## Задачи

1. **Roadmap 4.14** — writing-plans из утверждённого spec
   (`docs/superpowers/specs/2026-09-22-update-ctx-trace-design.md`): перечитай spec,
   ре-верифицируй §2/§5 file:line и счётчики сайтов grep'ом (код мог дрейфовать), план
   `docs/superpowers/plans/<дата>-update-ctx-trace.md`, ветка `feat/update-ctx-trace`,
   далее SDD-конвейер. Spec-разделы Q1–Q8 — частьGlobal Constraints плана.
2. После 4.14: **roadmap 4.15** (durable actor column) — sketch в spec §9; начни с
   brainstorming (свои open questions: schema, explicit-param vs ctx-extraction, surfaces).
3. Мелочи по остатку HANDOFF §6: 12 (отложен до роста объёмов), 16, 17, 18, 19 — можно
   батчем «micro-followups» в паузах или по моему запросу. 4.3 — только если явно попрошу.

Перед push (когда разрешю): `make doctor` + один live-test платёж NOWPayments (обязателен —
канонизация IPN-подписи) + регистрация вебхуков в кабинетах провайдеров (HANDOFF §5).

Общение со мной — на русском. Отвечай компактно, отчёты и планы — структурно.
Первым ответом: краткое подтверждение прочитанного (HANDOFF/spec/roadmap/CHANGELOG),
текущее состояние (main SHA, ворота), и что делаешь первым. Дальше — работай
автономно до результата; ruling'и принимай сам и перечисли ВСЕ в финальном отчёте.
