# Changelog

## [0.1.21]

- **[P0]** Keychain gate mock theater: `keychain_test.go`'s seam suite (`TestGetToken`, `TestGetTokenMissing`, `TestSetToken`) never ran anywhere because it's excluded from every existing test tag combination — a real exclusion, not an oversight, but it left the package with no gate at all. Added `make test-keychain-seam` (the seam suite in the shipped, no-shim build; safe, no macOS prompt) and a genuinely new `make test-keychain-native` (HOST-ONLY, owner: maintainer) that exercises the real `Security.framework` cgo path end-to-end via a new `TestNativeKeychainRoundTrip`, backed by a new `SecItemDelete`-based `platformDeleteTokenImpl` (`keychain_darwin.go`/`keychain_unsupported.go` — the package had no delete helper before). Confirmed live that, despite a per-run-unique account and a dedicated non-production service (`uni-chat-selftest`), an interactive macOS session still pops a real Keychain-access confirmation dialog on `SecItemAdd` — `go test` links an ad-hoc-signed binary with no stable trusted identity, so the "same process, no prompt" assumption doesn't hold. `test-keychain-native` is therefore bounded with `-timeout 20s`, kept out of `make check`, and documented as something a maintainer runs by hand, attentively, before cutting a release.
- **[P0]** `secrets-check` was structurally blind beyond three hardcoded literal patterns. Wired in `gitleaks` (`tools/go.mod`-pinned 8.30.1) via `make secrets-check`, scanning both the working tree and the full commit history, plus a project `.gitleaks.toml` adding an entropy-based rule for prefix-less tokens (e.g. a bare Mattermost personal access token) that no literal pattern could ever catch.
- Added `make tag-protection-check`: reads the repository's GitHub ruleset via `gh api` and writes machine-readable evidence (`.task/release-evidence/tag-protection.properties`). Created the missing ruleset itself — `protect-release-tags` (id `22587241`) on `refs/tags/v*`, forbidding tag deletion and non-fast-forward updates, no bypass actors — since nothing protected release tags before.
- `dependency-check` rewritten to schema `release-evidence-v2`: real exit-code parsing for both scanners (`govulncheck` 0/3, `osv-scanner` 0/1, anything else is a hard scanner/runtime-error blocker), plus `candidate_commit_sha`, `planned_tag`, `policy_decision`, `findings`, and `exceptions` fields the evidence was missing. Split the cheap staleness check into its own `dependency-freshness` target so `make check` doesn't re-run a full scan on every invocation.
- `make release-check`'s own recipe is now externalized to `scripts/release-check-summary.sh`, which always writes `.task/release-evidence/release-check.json` — including the blocker and exact resume point — even when a gate fails partway through, instead of silently stopping with no evidence at all.
- `osv-scanner` is now resolved from `tools/go.mod` (`go -C tools tool osv-scanner`), matching how `govulncheck`/`staticcheck` are already pinned, instead of a bare PATH lookup.
- Corrected the self-contradictory `verify-release` `N/A` rationale: only the published-source half of the target is `N/A` (this is a tag-only release profile with no published source/assets); the post-tag smoke half (`install-local-smoke`) stays applicable, so the target exists and writes `.task/release-evidence/verify-release.json`.
- Added `make cross-build`, compiling the declared platform matrix — `darwin/arm64` and `darwin/amd64` (`cgo=1`), `linux/amd64` and `linux/arm64` (`cgo=0`) — covering both branches of the `keychain` build tags; included in `check` and `release-check`.
- Acceptance and protocol test suites now run with `t.Parallel()` and a unique per-run marker (argv, not `os.Getenv`/`t.Setenv`, which don't compose with parallel tests) instead of serially. Parallelizing surfaced real bugs, not just flakiness: `pkg/protocol/process_test_unix.go` had never compiled as a Go test file at all (wrong filename suffix order — `_test.go` must be the suffix), so five tests never ran and the shipped library's own dependency graph was silently pulling in `testing`; renamed to `process_unix_test.go`, which then exposed a deadlock-detector crash (`signal.Ignore`+`select{}` raced the real SIGTERM/SIGKILL instead of blocking on a registered channel) and a stdout-corruption bug (a helper process missing `os.Exit(0)` let `go test`'s own summary line leak into a JSON response). Three more timeouts were tightened after repeated `-race` runs showed them too tight under race-instrumented, parallel subprocess spawn load.
- Added `pkg/protocol.HandlerContext`, an additive `context.Context`-aware handler type alongside the existing `Handler` (with a `Handler.WithContext()` adapter, so nothing existing breaks). `ServeContext`/`serveConn` now thread a real per-connection `context.WithCancel(ctx)` instead of accepting a bare `Handler` with no cancellation path.
- `CallStdio` no longer buffers a child process's stdout/stderr without bound: output is now capped via a new `boundedBuffer` (1 MiB stdout, 8 KiB stderr), and error messages sanitize the raw stderr tail before including it instead of embedding it verbatim.
- `state.Lock` no longer blocks on `flock(LOCK_EX)` forever: a new `LockContext` polls with a 30s default timeout (10ms interval) and returns a real error at the deadline instead of hanging. `state.writeJSON`/`Lock` also now actively tighten directory/file permissions (`chmod 0700`/`0600`) rather than only setting them at creation, and the atomic-write temp file is now unpredictable (`os.CreateTemp`) instead of a fixed `<path>.tmp`, closing a poisoned-temp-file permission-inheritance hole.
- `docs/security.md`: added a `## Crosswalk controls` table mapping abuse cases to concrete external references (OWASP ASVS 5.0.0 V2, Apple Keychain Services docs, NIST SSDF 1.1 PS, MITRE CWE-798, GitHub Rulesets API, FIPS 180-4). README's blanket chapter-14 `N/A` replaced with a per-row breakdown: native macOS is now applicable (closed by `test-keychain-native`), Docker-related rows stay `N/A` (no Docker in this repository), `-race` and the acceptance stage were already applicable and unchanged.

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
