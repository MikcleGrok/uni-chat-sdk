# Changelog

## [0.1.20]

- SCA gate: `dependency-check` раньше запускал только `govulncheck` (call-graph-scoped) и пропустил реальную уязвимость (`golang.org/x/mod@0.35.0`, GO-2026-6179/GO-2026-6180), которую видел `osv-scanner`, но не видел `govulncheck` — зависимость не вызывалась, но присутствовала в дереве. Теперь `dependency-check` запускает оба сканера (`govulncheck` + `osv-scanner` 2.5.1, `--data-source native --all-vulns`) и пишет evidence в `.task/release-evidence/dependency.properties`; новый `dependency-freshness` — дешёвый staleness gate (без повторного скана) в `make check`, а `make release-check` запускает оба — свежий скан и freshness-проверку.
- Tool isolation: `staticcheck` и `govulncheck` перенесены из tool-директивы корневого `go.mod` в отдельный `tools/go.mod` (тот же паттерн, что в `uni-chat-pachca/tools/go.mod`) — иначе их транзитивное дерево зависимостей (включая уязвимый `golang.org/x/mod@0.35.0`) становится частью dependency-графа самой библиотеки и того, что сканирует `dependency-check`. После изоляции корневой `go.mod` не имеет ни одной non-stdlib зависимости, а `osv-scanner`/`govulncheck` резолвятся исключительно из `tools/go.mod` через `go -C tools tool <name>`, никогда не с `PATH`.
- Исправлен `setup`: `go install golang.org/x/vuln/cmd/govulncheck@latest` нарушал правило pin-exact-version; `govulncheck` теперь закреплён версией в `tools/go.mod`, как и `staticcheck`, — отдельной PATH-установки больше нет.
- `docs/security.md`: исправлено грамматически сломанное предложение про `UNI_CHAT_TEST_KEYCHAIN` (build tag терялся при смешении английского и русского текста) и добавлен раздел `## Модель угроз` (активы, акторы, границы доверия, поверхности/точки входа, таблица abuse case → доказательство, честный потолок/остаточный риск) — внутри этого файла, а не отдельным `docs/threat-model.md`, для единообразия со всеми остальными репозиториями этой compliance-волны.

## [0.1.19]

- Репозиторий приведён в соответствие с `guide-tools`: добавлены недостающие baseline Makefile targets (`setup`, `check-env`, `help`, `build`, `version`, `check-version`, `release-check`), объявлена отдельная acceptance-ступень `test-acceptance`, `check-local-tag` теперь отклоняет lightweight tag, добавлен `whats-new`.
- Добавлен regression-тест владения общим `PREFIX` (`make install-scoping-test`) и машинная проверка onboarding record (`make check-onboarding`); оба включены в `check` и `release-check`.
- README: канонический onboarding record вместо самодельной таблицы; исправлены неверные утверждения о каденции SCA (CI в репозитории нет) и о том, что `install-local*` не выполняет настоящую установку.
- Добавлен `protocol.ReactionDetail` и поле `ReactionDetails` в `CheckItem`/`SearchItem` — группировка реакций по эмодзи с авторами (кто именно поставил реакцию).

## [0.1.15]

- Добавлен cursor-aware протокол проверки уведомлений для корректного polling состояния между вызовами.

## [0.1.10]

- В `PostData` добавлены поля `channel_id` и `post_id`.

## [0.1.9]

- Заменён вызов macOS `security` CLI на нативный `Security.framework` для чтения и записи токенов Keychain.
- Добавлены явная ошибка для неподдерживаемых сборок и тест round-trip через test-only Keychain seam.
- Обновлена документация security scope и поддержки macOS cgo.

## [0.1.6]

- Подготовлен локальный offline-релиз SDK.

## [0.1.4]

- Синхронизированы метаданные локального release workflow.

## Unreleased

- В `PostArgs` добавлено опциональное поле `RootPostID` (`root_post_id`) — ответ в тред вместо нового сообщения верхнего уровня; при пустом значении поведение и wire-формат не меняются.
- Initial local extraction from `uni-chat`.
