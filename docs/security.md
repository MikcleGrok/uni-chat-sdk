# Security

The library contains no release binaries or signing keys. Consumers verify Go
module checksums through the normal Go checksum database and their `go.sum`.

Адаптер macOS использует документированные API `SecItem` из
`Security.framework`. Токен передаётся внутри процесса как `CFDataRef`; адаптер
не запускает CLI `security` и никогда не помещает токен в argv или stdin.

Тестовый seam `UNI_CHAT_TEST_KEYCHAIN` (`keychain/test_shim.go`) — заглушка,
которая хранит токены в открытом виде в JSON-файле вместо настоящего Keychain.
Он собирается только под build tag `uni_chat_test_keychain` и не входит ни в
обычную сборку (`make build`, `make lint`, `make vet`), ни в release-сборку;
`make test`/`make test-acceptance` явно передают этот tag, чтобы прогнать
`keychain` без реального Keychain на CI/dev-машине. В сборках не для macOS или
без cgo использование адаптера завершается явной ошибкой.

## Модель угроз

### Активы

| Актив | Где в коде |
| --- | --- |
| Токен доступа в Keychain (per-engine personal access token, service `uni-chat`) | `keychain/keychain.go:12` (`const Service`), `GetToken` (`keychain/keychain.go:18`), `SetToken` (`keychain/keychain.go:30`) |
| On-disk cursors/config/lock (`~/.uni-chat/state.json`, `state.lock`) при правах 0600 | `state/state.go:335` (`writeJSON`, атомарная запись через temp+rename), `state/state.go:316` (`Lock`, cross-process flock) |
| Unix-сокол `~/.uni-chat/uni-chatd.sock` и его директория | `pkg/protocol/protocol.go:39` (`SocketPath`), `pkg/protocol/protocol.go:30` (`Dir`) |
| Целостность модуля (проверяемые контрольные суммы зависимостей и самого SDK у потребителей) | `go.sum` (у потребителей — запись для этого модуля; у самого SDK, после изоляции dev-инструментов, `go.sum` пуст — зависимостей во время выполнения нет) |

### Акторы

- Процессы владеющего uid — `uni-chat`/`uni-chatd` и запущенные им engine-бинарники; штатный, доверенный актор.
- Любой другой локальный процесс того же uid — модель однопользовательская, сокет и файлы состояния защищены только правами `0600`/`0700`, а не отдельным ACL между процессами одного пользователя.
- Локальный непривилегированный процесс — сценарий, при котором права `0700`/`0600` по ошибке оказались шире (баг, неверный umask, восстановление из бэкапа); не штатный случай, но именно от него защищают явные `os.Chmod` в `Listen` и `os.OpenFile`/`os.WriteFile` со фиксированными правами в `state`.
- Автор вредоносной или скомпрометированной зависимости — актор supply chain; `go.sum`/checksum database ловят подмену уже опубликованного контента, но не легитимно опубликованный вредоносный релиз.
- Вредоносный или скомпрометированный engine-бинарник, запускаемый из приватного реестра через `CallStdio`, — именно этот актор и есть предмет описанной ниже границы доверия у `CallStdio`.

### Границы доверия

- **uid-граница сокета.** `Listen` создаёт директорию `0700` и сам сокет `0600` (`pkg/protocol/protocol.go:759`) — доступ к сокету имеет только владеющий uid; ничего похожего на per-client аутентификацию поверх этого нет, потому что модель однопользовательская.
- **Процессная граница CallStdio/ServeStdio.** `CallStdio` (`pkg/protocol/protocol.go:1022`) порождает engine-бинарник через `exec.Command` в отдельной группе процессов с таймаутом и эскалацией SIGTERM→SIGKILL (`pkg/protocol/process_unix.go:18`); `ServeStdio` (`pkg/protocol/protocol.go:1008`) — это сторона адаптера, разбирающая ровно один запрос со stdin и пишущая ровно один ответ в stdout, после чего процесс завершается (spawn-per-request).
- **ACL-граница Keychain.** Доступ к записи регулируется штатным ACL macOS Keychain для процесса-владельца; SDK не расширяет и не ослабляет этот ACL, только читает/пишет через `SecItem`.
- **Граница контрольных сумм модуля.** `go.sum` у потребителя — единственное, что удостоверяет неизменность содержимого этого модуля между публикацией тега и `go get`; сам SDK, будучи stdlib-only после изоляции dev-инструментов, ничего не добавляет и не ослабляет в этой границе.
- **Ключевой вывод.** `EnginesPath` (`pkg/protocol/protocol.go:962`) отдаёт путь `~/.uni-chat/engines.json` — реестр движков, который читает потребитель (uni-chat core/router), чтобы получить `bin`/`args` для `CallStdio`. Тот, кто может писать `engines.json`, тем самым получает произвольное выполнение кода через `CallStdio` — SDK доверяет вызывающей стороне, что путь к бинарнику взят из уже провалидированного приватного реестра. Именно это признаёт комментарий `#nosec G204` у самого вызова (`pkg/protocol/protocol.go:1029`): подавление статического анализа документирует принятый риск, а не устраняет его — граница доверия проходит по границе процесса, который пишет `engines.json`, а не внутри `CallStdio`.

### Поверхности и точки входа

| Поверхность | Точка входа | Применимость |
| --- | --- | --- |
| Unix-сокет | `Listen`/`Serve`/`ServeContext` (`pkg/protocol/protocol.go:759,788,795`) | Применимо — единственный сетевой (loopback-эквивалентный) вход в daemon-режиме потребителя |
| Порождаемый процесс (`CallStdio`) | `pkg/protocol/protocol.go:1022`, `pkg/protocol/process_unix.go:18` | Применимо — единственная точка запуска стороннего кода этим SDK |
| Keychain API | `keychain/keychain.go:18,30` (`GetToken`/`SetToken`), cgo-мост `keychain/keychain_darwin.go` | Применимо только на `darwin`+cgo; на прочих платформах — явная ошибка (`keychain_unsupported.go`) |
| On-disk файлы состояния | `state/state.go:316` (`Lock`), `state/state.go:335` (`writeJSON`) | Применимо |
| `engines.json` (реестр движков) | `pkg/protocol/protocol.go:962` (`EnginesPath`) | Применимо как источник пути для `CallStdio`; сам файл пишет и читает потребитель, не этот SDK |
| `go.mod`/`go.sum` | корень репозитория, `tools/go.mod` | Применимо |
| CLI-аргументы | — | N/A — библиотека без command line; command surface у неё нет вовсе |
| Сетевые пиры | — | N/A — только unix-сокет с uid-границей; TCP/HTTP-listener в SDK не заводится |
| CI credentials | — | N/A — в репозитории нет CI-конфигурации, gates исполняются локально через Makefile |
| Публикуемые/генерируемые артефакты | — | N/A — библиотека не публикует бинарники; единственный канал распространения — exact immutable git tag и офлайн `make release-local` |

### Abuse cases → доказательство

| Abuse case | Контроль | Чем доказано |
| --- | --- | --- |
| Слишком большой или специально сформированный JSON-запрос вешает или валит daemon | Лимит размера запроса `maxRequestJSONBytes` = 1 MiB (`pkg/protocol/protocol.go:855`) | `TestServeStdioRejectsOversizedRequest`, `TestServeRejectsOversizedSocketRequest` (`pkg/protocol/protocol_test.go`) + `make test-acceptance` |
| После валидного JSON в тот же коннекшен дописываются лишние байты (попытка контрабандой протащить второй запрос) | `rejectSocketTrailingData` (`pkg/protocol/protocol.go:930`) | `TestServeRejectsValidJSONWithOversizedSocketTrailingData`, `TestServeStdioRejectsValidJSONWithOversizedTrailingData` |
| Паника внутри обработчика роняет весь daemon и все параллельные соединения | Guard от паники в `callHandler` (`pkg/protocol/protocol.go:951`) превращает panic в `{ok:false}` | `TestCallHandlerPanicRecovered` |
| Флуд соединениями исчерпывает файловые дескрипторы/горутины daemon | Лимит на 32 одновременных соединения + явный busy-ответ сверх лимита (`ServeContext`, `pkg/protocol/protocol.go:795-796,854`, `writeBusyResponse`) | `TestServeReturnsBusyWhenConnectionLimitIsReached`, `TestServeBusyUnreadClientDoesNotBlockAcceptLoop` |
| Зависший или вредоносный engine-бинарник никогда не завершается, накапливая процессы | Таймаут с эскалацией SIGTERM→SIGKILL по группе процессов (`runBoundedCommand`, `pkg/protocol/process_unix.go:18`) | `TestCallStdioTimeoutKillsSlowChild` |
| Токен утекает через список процессов (`ps`, `/proc/*/cmdline`) | Токен передаётся адаптеру Keychain как `CFDataRef` внутри процесса, никогда не попадает в argv/stdin CLI (`keychain/keychain_darwin.go`) | Инспекция кода: `keychain_darwin.go` не вызывает `exec.Command` вообще; передача через cgo-указатель на память процесса, а не через внешний процесс |
| Plaintext test-Keychain seam случайно попадает в поставляемую сборку | Доступен только под build tag `uni_chat_test_keychain`, отсутствует в обычной сборке | `make build`/`make lint`/`make vet` собирают без этого tag; `go build ./...` без `-tags` не включает `keychain/test_shim.go` вовсе |
| Итого по acceptance-ступени | Все перечисленные выше сценарии живут в процессной/сокетной acceptance-ступени | `make test-acceptance` — не менее `ACCEPTANCE_MIN=15` тестов по селектору `^Test(Serve|Call)` в `pkg/protocol`, фактически 20 на момент написания; ступень явно проверяет реальный unix-сокет и реальный `exec.Command`, а не моки |

### Честный потолок / остаточный риск

- `Listen` безусловно вызывает `os.Remove(socket)` перед созданием слушателя (`pkg/protocol/protocol.go:767`) — это осознанный выбор для очистки протухшего сокета после нештатного завершения daemon, а не уязвимость: он выполняется уже внутри uid-границы (директория `0700` не даёт постороннему uid туда что-либо положить), но стоит явно зафиксировать как дизайн-решение, а не как случайный побочный эффект.
- Plaintext test-Keychain seam (`UNI_CHAT_TEST_KEYCHAIN`/`keychain/test_shim.go`) хранит токены в открытом виде — риск ограничен build tag'ом, но остаётся человеческим фактором: собрать релиз с этим tag'ом по ошибке технически возможно, контроль тому — code review, а не машинная проверка.
- Между запусками `make dependency-check` есть окно до 30 дней (`SCA_WINDOW_DAYS`), в течение которого свежая CVE на уже используемую зависимость может быть не замечена — это принятый разрыв cadence для plain-CLI библиотечного профиля, не daemon и не internet-facing сервис.
- `CallStdio` не проверяет подпись или контрольную сумму бинарника перед запуском — доверие полностью на стороне вызывающего кода (значение `bin` должно приходить из уже провалидированного приватного реестра); сам SDK не может обеспечить эту гарантию, только документирует её как границу доверия выше.
