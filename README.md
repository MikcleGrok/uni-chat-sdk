# uni-chat-sdk

Shared Go packages for uni-chat engines and the private uni-chat core.

This repository is held to the Go tooling standard kept in the sibling
`guide-tools` checkout (`~/projects/tools/guide-tools`): its `README.md` defines
the onboarding record reproduced below, `00-overview.md` the shared-location
ownership rule that `make install-scoping-test` proves,
`05-build-test-docs.md` the mandatory Makefile baseline, `06-release.md` the
release gates, `08-security-and-reliability.md` the security baseline and
`12-test-contract.md` the test contract.

Packages:

- `pkg/protocol` contains the engine wire protocol.
- `pkg/capability` contains capability metadata.
- `state` contains per-engine state and atomic configuration helpers.
- `keychain` contains the macOS Keychain adapter.

The module is intentionally independent from `uni-chat` and engine repositories.

## Local gates

The Makefile is the only public entry point for development, verification and
release actions; `make help` lists every target with its purpose and prints the
current value of the documented variables `GO`, `PREFIX`, `DIST`, `VERSION` and
`TAG`.

```sh
make setup                         # module deps and the tools the gates call
make check                         # every local gate
make test-acceptance               # only the process-boundary suite
make release-check VERSION=0.1.19  # pre-tag completeness gate on this commit
```

`make check` runs `check-env`, `check-version`, `check-onboarding`, `format`,
`lint`, `vet`, `build`, `test`, `test-acceptance`, `race`, `coverage`,
`secrets-check`, `dependency-check` and `install-scoping-test`.

`make release-check` is the canonical pre-tag gate. It runs against the
candidate commit *before* any tag exists, so it takes the planned release
version as a parameter and never requires an exact tag; it refuses a `dev`
version, a dirty tree, a planned tag that already points at a different commit
and a missing changelog section, then runs the gates that do not depend on a
tag. `check-local-tag`, `install-local-smoke` and `release-local` have the
inverse contract: they require the exact annotated tag to already be on `HEAD`.

`make test-acceptance` runs the process-boundary stage on its own: the tests
that go through a real unix socket (`Listen`/`Dial`) and a real subprocess
(`CallStdio` over `exec.Command`). `make test` runs both stages together.

## Local release verification

The local release path is offline and does not use a formula, remote download,
GitHub API, or publishing service. `release-local` requires a clean checkout
whose `HEAD` is the exact canonical annotated tag, then archives tracked files
directly from that tag. It leaves the release artifacts in
`dist/local-release/<version>`: the source archive, `SHA256SUMS`,
`metadata.json`, and `RELEASE_NOTES.md`.

```sh
make release-local VERSION=0.1.1 TAG=v0.1.1
```

The version is the tag version without the leading `v`; both values must match
the exact tag at `HEAD`. The existing `install-local-smoke` target remains a
disposable installation check and is separate from the persistent release
artifact target.

`install-local` is a real install, not an archive-only check: with no `PREFIX`
override it writes into `$HOME/.local`, a prefix shared with other tools. It is
safe there because it owns exactly one subtree, `$PREFIX/share/uni-chat-sdk/<version>`,
creates and replaces only that subtree, and never swaps or deletes `$PREFIX`
itself. `make install-scoping-test` proves it: it seeds a disposable prefix with
files belonging to an unrelated tool and to an earlier installed version, runs
`install-local` twice (install, then upgrade in place) and asserts every seeded
file survives with the same content hash and the same mtime, and that `$PREFIX`
keeps its inode.

## Onboarding record

| Поле | Значение |
| --- | --- |
| `project type` | Go library module (`pkg/protocol`, `pkg/capability`, `state`, `keychain`). Не CLI, не TUI, не daemon: потребляется приватным `uni-chat` core и репозиториями движков как обычная зависимость. |
| `profiles` | `active`: library-профиль по `05-build-test-docs.md` (baseline Makefile targets), контракт тестов по `12-test-contract.md` (обе ступени), tag-only release по `06-release.md`, security baseline по `08-security-and-reliability.md`. `N/A`: base/operator CLI (`01`), TUI (`02`), single-binary и install channel продукта (`03`), agent/daemon (`04`), distribution contract и verifier (`10`, `11`), Docker (`13`), CI и cross-platform поставка (`14`), Homebrew/GitHub install (`15`). `planned`: нет. |
| `OS/ARCH` | `pkg/protocol`, `pkg/capability` и `state` — любая платформа, поддерживаемая Go 1.26+; отдельных ограничений по архитектуре нет. `keychain` — только `darwin` с cgo и системным `Security.framework` (`keychain_darwin.go`); на остальных OS собирается `keychain_unsupported.go` и пакет возвращает явную ошибку, поэтому module остаётся собираемым везде. |
| `modes` | Только library import; исполняемых режимов, TTY-контракта и non-interactive fallback у модуля нет. Режимы разработки и проверки — Makefile targets, все non-interactive и не зависящие от cwd, `HOME` и shell. |
| `channels` | Go module по exact immutable annotated tag (`go get github.com/MikcleGrok/uni-chat-sdk@vX.Y.Z`) и offline source bundle `make release-local` в `dist/local-release/<version>/`. `N/A`: Homebrew formula, prebuilt binary assets, container registry — бинарные артефакты не публикуются. |
| `version source` | Единственный источник — normalized SemVer из exact annotated tag `vMAJOR.MINOR.PATCH` на `HEAD`; `VERSION` в Makefile снимает его через `git describe --exact-match` и убирает ведущий `v`. Untagged checkout даёт `dev` и не проходит `release-check`. Проверяется `make check-version`; build metadata (`+...`) в release tags запрещена. |
| `Makefile targets` | baseline: `setup`, `check-env`, `help`, `format`, `lint`, `vet`, `test`, `build`, `security`, `dependency-check`, `secrets-check`, `version`, `check-version`, `release-check`, `check`. conditional (применимые): `fmt`, `test-unit`, `test-acceptance`, `race`, `coverage`, `check-onboarding`, `whats-new`, `package-local`, `install-local`, `verify-local-install`, `install-scoping-test`, `install-local-smoke`, `check-local-tag`, `release-local`, `local-release`. Отсутствуют как `N/A`: `sbom`, `release-manifest`, `sign`, `attest`, `verify-provenance`, `verify-release`, `docs`, `man`, `completions`, `completion-check`, `man-check`, `install`/`uninstall`/`upgrade` продукта, Homebrew и Docker targets — см. `N/A controls/rationale`. |
| `Docker toolchain image` | `N/A`: в репозитории нет ни Dockerfile, ни compose-файла, весь контур host-first. Toolchain задан `go.mod` (`go 1.26.0`, `toolchain go1.26.5`) и проверяется `make check-env`; dev-инструмент `staticcheck` закреплён tool-директивой `go.mod` и вызывается как `go tool staticcheck`. |
| `Docker runtime image` | `N/A`: библиотека ничего не поставляет как контейнер и не запускается как процесс — shipped runtime image не существует. |
| `Docker runtime base image` | `N/A`: shipped runtime image отсутствует, выбирать базовый образ не для чего. |
| `host-only exceptions` | `HOST-ONLY`: пакет `keychain` компилируется и тестируется только на macOS (cgo + `Security.framework`); owner — maintainer. Помечено в `make help` отдельной строкой HOST-ONLY, отдельный видимый результат даёт `make test`, который на macOS прогоняет `keychain` через test-only seam с build tag `uni_chat_test_keychain`, а на остальных OS собирает `keychain_unsupported.go`. Других host-only исключений нет. |
| `shared-location scoping` | Стратегия — фиксированное перечисление owned paths. Единственная операция, пишущая вне exclusively owned location, — `install-local`: она владеет ровно одним подкаталогом `$(PREFIX)/share/uni-chat-sdk/$(VERSION)`, создаёт и заменяет только его и никогда не делает directory-swap, rename-swap или recursive delete самого `$(PREFIX)` (default `$HOME/.local` — shared prefix). Фактический owned set перечислен в самом рецепте `install-local` в `Makefile` (три строки, обращающиеся к `$(PREFIX)`) и проверяется `verify-local-install`. Доказательство сохранности постороннего файла — `scripts/install-prefix-isolation-test.sh` через `make install-scoping-test`, включённый в `check` и `release-check`: sentinel переживает install и повторную install с тем же content hash и mtime, а `$(PREFIX)` сохраняет inode. Uninstall/reset-state операций у модуля нет. |
| `owners` | maintainer и владелец репозитория: `mickle <mickle.grok@proton.me>` (`MikcleGrok`); release и security — тот же maintainer. Git-операции выполняются напрямую `git`, отдельный wrapper не назначен. |
| `SCA cadence` | Не реже одного раза в 30 дней: library-профиль, не internet-facing, не privileged, не daemon/service, бинарные артефакты не публикуются. Механизм исполнения — staleness gate по свежести SCA-evidence в `make check` и `make release-check`: оба каждый раз заново запускают `make dependency-check` (`govulncheck ./...`) против текущего dependency set, внешнего триггера не требуют и самодостаточны. CI-конфигурации в репозитории нет, и объявлять CI механизмом каденции было бы неверно. Evidence location — вывод самого gate-прогона; для релиза он сохраняется вместе с остальным release evidence в `.task/<KEY>/release/` по `06-release.md`. Применяемое окно — нулевое: evidence создаётся тем же прогоном, который его использует, поэтому устаревшего SCA-evidence не бывает, а календарный срок обеспечивается обязательным прогоном gate перед каждым релизом и при каждой правке `go.mod`/`go.sum`. |
| `SCA owner` | `mickle <mickle.grok@proton.me>` (maintainer и владелец репозитория); он же принимает решения по remediation и exceptions. |
| `remediation deadline` | critical/high: 7 календарных дней; прочие findings: 30 календарных дней. Действующий finding блокирует `release-check` и релиз до устранения или оформленного exception. |
| `N/A controls/rationale` | `01-cli.md` (command surface, `version`/`bash_completion`/`help` aliases, exit codes, completion-check, man-check) — `N/A`: модуль не поставляет бинарник и не имеет command line. `02-tui.md` — `N/A`: TUI нет. `03`/`15` install продукта, targets/artifacts/manifest/smoke установленного бинарника — `N/A`: устанавливается не продукт, а Go module средствами go toolchain; `install-local*` покрывает только локальную установку исходников. `04-agent-daemon.md` (lifecycle, signals, health/readiness, transport trust, self-update) — `N/A`: модуль не запускается как процесс, lifecycle принадлежит consuming engine. `10`/`11` distribution contract и verifier, `sbom`, `release-manifest`, `sign`, `attest`, `verify-provenance` — `N/A`: publishable binary artifact отсутствует, artifact chain создавать не из чего. `verify-release` — `N/A`: tag-only release profile без опубликованных source/assets, ни один post-tag check (installed version, CLI aliases, package/man/completion, smoke) неприменим; локальный эквивалент — `install-local-smoke`. `docs`/`man`/`completions` generation — `N/A`: документация ручная, генерируемых файлов в репозитории нет; validation README выполняется `make check-onboarding`. `13-docker-workflow.md` — `N/A`: ни Dockerfile, ни compose. `14-cross-platform-ci.md` — `N/A`: CI-конфигурации нет, cross-platform поставки нет; gates выполняются локально через Makefile на host toolchain. `-race` — не `N/A`: concurrency-heavy код (`Serve`/`ServeContext`) объявлен applicable, отдельный target `race` входит в `check` и `release-check`. Acceptance-ступень — не `N/A`: process boundary есть (unix socket и subprocess), ступень объявлена как `test-acceptance`. |
| `last reviewed` | 2026-09-03 |
| `review trigger/profile state` | Состояние профиля: active. Пересмотр при изменении profile, channel, version source, публичного API (`pkg/*`, wire-формат) или trust boundary, а иначе не позже 2026-12-02 (90 дней от `last reviewed`). Обязательные поля и это правило freshness проверяются машинно через `make check-onboarding`, входящий в `check` и `release-check`. |

### Дополнительные границы модуля

| Поле | Политика и границы |
| --- | --- |
| `module/API compatibility` | Канонический module path: `github.com/MikcleGrok/uni-chat-sdk`. До `v1` изменения экспортируемых типов, функций и wire-facing JSON могут требовать адаптации потребителей; после `v1` breaking changes допускаются только в новом major version. Patch releases сохраняют API и поведение, minor releases добавляют совместимые возможности. |
| `supported Go versions` | Go 1.26 и новее; `go.mod` задаёт `go 1.26.0` и `toolchain go1.26.5`. Более старые версии Go не поддерживаются; `make check-env` сравнивает установленный toolchain с директивой `go` из `go.mod` и падает, если он старше. |
| `onboarding` | Выполнить `go get github.com/MikcleGrok/uni-chat-sdk@<tag>`, затем `make check` или эквивалентные gates в consuming repository. Для локальной проверки SDK: `make setup`, затем `make check`. |
| `release record` | Канонический offline flow: `make release-check VERSION=X.Y.Z` до тега, затем exact annotated tag, затем `make release-local VERSION=X.Y.Z TAG=vX.Y.Z` (безопасный алиас `make local-release`). Требуются clean tree и exact annotated canonical tag на `HEAD`; артефакты и SHA-256 остаются в `dist/local-release/<version>/`. Это исходный module archive, не устанавливаемый бинарный дистрибутив. |
| `security scope` | Секреты не входят в release archive и проверяются `make secrets-check`. Go module checksums проверяются стандартным checksum database и `go.sum` (`make setup` выполняет `go mod verify`); test-only Keychain seam доступен только с build tag `uni_chat_test_keychain`. |
