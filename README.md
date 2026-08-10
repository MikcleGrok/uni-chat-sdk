# uni-chat-sdk

Shared Go packages for uni-chat engines and the private uni-chat core.

Packages:

- `pkg/protocol` contains the engine wire protocol.
- `pkg/capability` contains capability metadata.
- `state` contains per-engine state and atomic configuration helpers.
- `keychain` contains the macOS Keychain adapter.

The module is intentionally independent from `uni-chat` and engine repositories.

## Local release verification

The local release path is offline and does not use a formula, remote download,
GitHub API, or publishing service. `release-local` requires a clean checkout
whose `HEAD` is the exact canonical tag, then archives tracked files directly
from that tag. It leaves the release artifacts in `dist/local-release/<version>`:
the source archive, `SHA256SUMS`, `metadata.json`, and `RELEASE_NOTES.md`.

```sh
make release-local VERSION=0.1.1 TAG=v0.1.1
```

The version is the tag version without the leading `v`; both values must match
the exact tag at `HEAD`. The existing `install-local-smoke` target remains a
disposable installation check and is separate from the persistent release
artifact target.

## Library onboarding, release и security record

Это библиотека, а не самостоятельное приложение. Подключающий её репозиторий
должен зависеть от опубликованного module version, запускать совместимые
Makefile-gates и проверять изменения публичного API до обновления зависимости.

| Поле | Политика и границы |
| --- | --- |
| `module/API compatibility` | Канонический module path: `github.com/MikcleGrok/uni-chat-sdk`. До `v1` изменения экспортируемых типов, функций и wire-facing JSON могут требовать адаптации потребителей; после `v1` breaking changes допускаются только в новом major version. Patch releases сохраняют API и поведение, minor releases добавляют совместимые возможности. |
| `supported Go versions` | Go 1.26 и новее; `go.mod` задаёт `go 1.26`, CI использует toolchain из `go.mod` (`go1.26.5`). Более старые версии Go не являются поддерживаемыми. |
| `OS/ARCH scope` | `pkg/protocol`, `pkg/capability` и `state` не требуют OS-specific runtime и поддерживаются на платформах, где работает Go 1.26. `keychain` поддерживается только на macOS с системной утилитой `security`; для остальных OS потребитель должен не включать этот пакет. Архитектуры отдельно не ограничиваются для pure-Go пакетов; macOS scope наследует поддерживаемые Go `darwin` architectures. |
| `onboarding` | Выполнить `go get github.com/MikcleGrok/uni-chat-sdk@<tag>`, затем `make check` или эквивалентные gates в consuming repository. Для локальной проверки SDK доступны `make format`, `make test-unit`, `make vet`, `make race` и `make security`. |
| `release record` | Канонический offline flow: `make release-local VERSION=X.Y.Z TAG=vX.Y.Z`; безопасный алиас: `make local-release`. Требуются clean tree и exact canonical tag на `HEAD`; артефакты и SHA-256 остаются в `dist/local-release/<version>/`. Это исходный module archive, не устанавливаемый бинарный дистрибутив. |
| `SCA cadence` | `govulncheck ./...` запускается в каждом CI push/PR через `make dependency-check` и перед каждым локальным release; владелец SCA и remediation решений: maintainer/repository owner. Ручная проверка cadence: не реже еженедельно при активной разработке. |
| `CLI profile` | `N/A`: SDK не поставляет CLI и не обещает CLI-команды или TUI. Примеры onboarding используют Go module и Makefile только для разработки и проверки. |
| `install profile` | `N/A`: у библиотеки нет самостоятельного install/runtime bundle; потребитель устанавливает module через стандартный Go toolchain. `install-local*` в Makefile проверяет только disposable source archive, а не системную установку продукта. |
| `runtime profile` | `N/A`: SDK не запускается как daemon или service. `pkg/protocol` предоставляет библиотечный wire API, а lifecycle процесса принадлежит consuming engine; его реализация уже защищена subprocess/socket tests. |
| `security scope` | Секреты не входят в release archive. Go module checksums проверяются стандартным checksum database и `go.sum`; test-only Keychain seam доступен только с build tag `uni_chat_test_keychain`. |
