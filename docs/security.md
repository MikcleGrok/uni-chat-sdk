# Security

The library contains no release binaries or signing keys. Consumers verify Go
module checksums through the normal Go checksum database and their `go.sum`.
Repository tag integrity (no force-update, no delete of a published release
tag) is enforced by a GitHub ruleset and verified machine-readably before
every release — see `make tag-protection-check` and `06-release.md`'s tag
policy requirement.

Адаптер macOS использует документированные API `SecItem` из
`Security.framework`. Токен передаётся внутри процесса как `CFDataRef`; адаптер
не запускает CLI `security` и никогда не помещает токен в argv или stdin.

Пакет `keychain` тестируется тремя независимыми ступенями, а не одной:

1. `make test`/`test-acceptance`/`race`/`coverage` прогоняют пакет с build tag
   `uni_chat_test_keychain`, под которым тестовый seam
   `UNI_CHAT_TEST_KEYCHAIN` (`keychain/test_shim.go`) — заглушка, хранящая
   токены в открытом виде в JSON-файле — подменяет обращение к настоящему
   Keychain. Платформонезависимо: тег не собирает cgo-путь вовсе.
2. `make test-keychain-seam` прогоняет тот же пакет **без** этого тега, то
   есть в точности в поставляемой (shipped) сборке: seam-тесты
   (`TestGetToken`, `TestGetTokenMissing`, `TestSetToken`) продолжают
   подменять `platformGetToken`/`platformSetToken` функциональными
   литералами, поэтому прогон безопасен и платформонезависим, но он
   проверяет, что пакет вообще компилируется и ведёт себя корректно без
   shim'а — до этого исправления эта ступень не была покрыта ни одним
   гейтом.
3. `make test-keychain-native` — HOST-ONLY, только `darwin`+cgo, owner
   maintainer — единственная ступень, которая реально вызывает
   `Security.framework`: `keychain/keychain_native_darwin_test.go`
   вызывает `platformGetTokenImpl`/`platformSetTokenImpl`/
   `platformDeleteTokenImpl` напрямую (не через подменяемые function vars),
   через выделенный non-production service `uni-chat-selftest` и
   уникальный на прогон account, с удалением через `t.Cleanup`. Она **не**
   входит в `make check` — на этом хосте (macOS 15/Sequoia) подтверждено
   вживую, что первая же запись через `SecItemAdd` из свежесобранного `go
   test`-бинарника блокируется на интерактивном системном диалоге
   SecurityAgent, то есть требует присутствия человека за клавиатурой;
   `check` — частый, часто headless dev-loop гейт, для которого это
   неприемлемо. Она входит в `make release-check`, где присутствие
   мейнтейнера уже предполагается (аналогично Touch ID/паролю на других
   шагах релиза).

В сборках не для macOS или без cgo использование адаптера завершается явной
ошибкой (`keychain/keychain_unsupported.go`).

## Модель угроз

### Активы

| Актив | Где в коде |
| --- | --- |
| Токен доступа в Keychain (per-engine personal access token, service `uni-chat`) | `keychain/keychain.go:12` (`const Service`), `GetToken` (`keychain/keychain.go:18`), `SetToken` (`keychain/keychain.go:30`) |
| On-disk cursors/config/lock (`~/.uni-chat/state.json`, `state.lock`) при правах 0600 | `state/state.go:407` (`writeJSON`, атомарная запись через `os.CreateTemp`+rename, явный `Chmod` каталога и temp-файла), `state/state.go:336,350` (`Lock`/`LockContext`, bounded cross-process flock) |
| Unix-сокет `~/.uni-chat/uni-chatd.sock` и его директория | `pkg/protocol/protocol.go:39` (`SocketPath`), `pkg/protocol/protocol.go:30` (`Dir`) |
| Целостность модуля (проверяемые контрольные суммы зависимостей и самого SDK у потребителей) | `go.sum` (у потребителей — запись для этого модуля; у самого SDK, после изоляции dev-инструментов, `go.sum` пуст — зависимостей во время выполнения нет) |
| Целостность репозиторных release-тегов (запрет force-update/delete) | GitHub ruleset `protect-release-tags` (`target=tag`, `refs/tags/v*`, правила `deletion`+`non_fast_forward`, `enforcement=active`); evidence — `make tag-protection-check` → `.task/release-evidence/tag-protection.properties` |

### Акторы

- Процессы владеющего uid — `uni-chat`/`uni-chatd` и запущенные им engine-бинарники; штатный, доверенный актор.
- Любой другой локальный процесс того же uid — модель однопользовательская, сокет и файлы состояния защищены только правами `0600`/`0700`, а не отдельным ACL между процессами одного пользователя.
- Локальный непривилегированный процесс — сценарий, при котором права `0700`/`0600` по ошибке оказались шире (баг, неверный umask, восстановление из бэкапа); не штатный случай, но именно от него защищают явные `os.Chmod` в `Listen`, в `state.Lock`/`LockContext` (каталог и lock-файл) и в `state.writeJSON` (каталог и temp-файл, созданный через `os.CreateTemp`, а не по предсказуемому пути) — режим создания сам по себе (`os.OpenFile`/`os.WriteFile` с фиксированным `perm`) уже существующий файл не сужает, поэтому именно explicit `Chmod` несёт эту гарантию.
- Автор вредоносной или скомпрометированной зависимости — актор supply chain; `go.sum`/checksum database ловят подмену уже опубликованного контента, но не легитимно опубликованный вредоносный релиз.
- Вредоносный или скомпрометированный engine-бинарник, запускаемый из приватного реестра через `CallStdio`, — именно этот актор и есть предмет описанной ниже границы доверия у `CallStdio`, включая его способность писать неограниченный вывод в stdout/stderr.
- Тот, кто может изменить или удалить опубликованный release-тег без обнаружения — против него действует репозиторный tag-protection ruleset, а не сам SDK.

### Границы доверия

- **uid-граница сокета.** `Listen` создаёт директорию `0700` и сам сокет `0600` (`pkg/protocol/protocol.go:759`) — доступ к сокету имеет только владеющий uid; ничего похожего на per-client аутентификацию поверх этого нет, потому что модель однопользовательская.
- **Процессная граница CallStdio/ServeStdio.** `CallStdio` (`pkg/protocol/protocol.go:1133`) порождает engine-бинарник через `exec.Command` в отдельной группе процессов с таймаутом и эскалацией SIGTERM→SIGKILL (`pkg/protocol/process_unix.go:18`), а его stdout/stderr буферизуются в памяти вызывающего только до `maxEngineStdoutBytes`/`maxEngineStderrBytes` (`pkg/protocol/protocol.go:898-899`) — сверх лимита `boundedBuffer` отбрасывает байты, не блокируясь и не разрастаясь; стандартная ошибка также прогоняется через `sanitizeStderrTail`, вырезающую управляющие символы недоверенного вывода. `ServeStdio`/`ServeStdioContext` (`pkg/protocol/protocol.go:1071` area) — сторона адаптера, разбирающая ровно один запрос со stdin и пишущая ровно один ответ в stdout, после чего процесс завершается (spawn-per-request).
- **Граница контекста обработчика.** `ServeContext` (`pkg/protocol/protocol.go:816`) передаёт каждому соединению собственный, производный от родительского, `context.Context` через `HandlerContext`; при cancel/shutdown этот контекст отменяется явно (`cancel()` в цикле приёма и в defer завершения), поэтому обработчик, реально проверяющий `ctx.Done()`, гарантированно не продолжает работу в фоне после возврата `ServeContext` — старый `Handler` (без контекста) по-прежнему поддерживается через адаптер `WithContext()` и не даёт этой гарантии, что и остаётся его документированным ограничением, а не регрессией.
- **ACL-граница Keychain.** Доступ к записи регулируется штатным ACL macOS Keychain для процесса-владельца; SDK не расширяет и не ослабляет этот ACL, только читает/пишет через `SecItem`. Реальный cgo-путь (`SecItemCopyMatching`/`SecItemUpdate`/`SecItemAdd`/`SecItemDelete`) проверяется `make test-keychain-native`, а не только инспекцией кода.
- **Граница cross-process file lock.** `state.Lock`/`LockContext` (`state/state.go:336,350`) ограничены `defaultLockTimeout` (30s) вместо неограниченного `LOCK_EX`: зависший или упавший держатель блокирует нового вызывающего не дольше этого окна, после чего он получает явную, actionable ошибку с путём к lock-файлу и дедлайном — не бесконечный hang.
- **Граница контрольных сумм модуля.** `go.sum` у потребителя — единственное, что удостоверяет неизменность содержимого этого модуля между публикацией тега и `go get`; сам SDK, будучи stdlib-only после изоляции dev-инструментов, ничего не добавляет и не ослабляет в этой границе.
- **Граница репозиторной tag policy.** GitHub ruleset `protect-release-tags` запрещает force-update и delete на `refs/tags/v*`; `bypass_actors` пуст (`current_user_can_bypass=never`), то есть обход недоступен даже владельцу репозитория через обычный API-путь. `make tag-protection-check` подтверждает это машинно перед каждым `release-check`, а не декларативно в README.
- **Ключевой вывод.** `EnginesPath` (`pkg/protocol/protocol.go:1064`) отдаёт путь `~/.uni-chat/engines.json` — реестр движков, который читает потребитель (uni-chat core/router), чтобы получить `bin`/`args` для `CallStdio`. Тот, кто может писать `engines.json`, тем самым получает произвольное выполнение кода через `CallStdio` — SDK доверяет вызывающей стороне, что путь к бинарнику взят из уже провалидированного приватного реестра. Именно это признаёт комментарий `#nosec G204` у самого вызова (`pkg/protocol/protocol.go:1140`): подавление статического анализа документирует принятый риск, а не устраняет его — граница доверия проходит по границе процесса, который пишет `engines.json`, а не внутри `CallStdio`.

### Поверхности и точки входа

| Поверхность | Точка входа | Применимость |
| --- | --- | --- |
| Unix-сокет | `Listen`/`Serve`/`ServeContext` (`pkg/protocol/protocol.go:759,806,816`) | Применимо — единственный сетевой (loopback-эквивалентный) вход в daemon-режиме потребителя |
| Порождаемый процесс (`CallStdio`) | `pkg/protocol/protocol.go:1133`, `pkg/protocol/process_unix.go:18` | Применимо — единственная точка запуска стороннего кода этим SDK |
| Keychain API | `keychain/keychain.go:18,30` (`GetToken`/`SetToken`), cgo-мост `keychain/keychain_darwin.go` | Применимо только на `darwin`+cgo; на прочих платформах — явная ошибка (`keychain_unsupported.go`) |
| On-disk файлы состояния | `state/state.go:336,350` (`Lock`/`LockContext`), `state/state.go:407` (`writeJSON`) | Применимо |
| `engines.json` (реестр движков) | `pkg/protocol/protocol.go:1064` (`EnginesPath`) | Применимо как источник пути для `CallStdio`; сам файл пишет и читает потребитель, не этот SDK |
| `go.mod`/`go.sum` | корень репозитория, `tools/go.mod` | Применимо |
| Репозиторные release-теги | GitHub ruleset `protect-release-tags` | Применимо — единственный канал распространения версий модуля |
| CLI-аргументы | — | N/A — библиотека без command line; command surface у неё нет вовсе |
| Сетевые пиры | — | N/A — только unix-сокет с uid-границей; TCP/HTTP-listener в SDK не заводится |
| CI credentials | — | N/A — в репозитории нет CI-конфигурации, gates исполняются локально через Makefile |
| Публикуемые/генерируемые артефакты | — | N/A — библиотека не публикует бинарники; единственный канал распространения — exact immutable git tag и офлайн `make release-local` |

### Abuse cases → доказательство

| Abuse case | Контроль | Чем доказано |
| --- | --- | --- |
| Слишком большой или специально сформированный JSON-запрос вешает или валит daemon | Лимит размера запроса `maxRequestJSONBytes` = 1 MiB (`pkg/protocol/protocol.go:879`) | `TestServeStdioRejectsOversizedRequest`, `TestServeRejectsOversizedSocketRequest` (`pkg/protocol/protocol_test.go`) + `make test-acceptance` |
| После валидного JSON в тот же коннекшен дописываются лишние байты (попытка контрабандой протащить второй запрос) | `rejectSocketTrailingData` (`pkg/protocol/protocol.go`) | `TestServeRejectsValidJSONWithOversizedSocketTrailingData`, `TestServeStdioRejectsValidJSONWithOversizedTrailingData` |
| Паника внутри обработчика роняет весь daemon и все параллельные соединения | Guard от паники в `callHandlerContext` (`pkg/protocol/protocol.go:1053`) превращает panic в `{ok:false}` | `TestCallHandlerPanicRecovered` |
| Флуд соединениями исчерпывает файловые дескрипторы/горутины daemon | Лимит на 32 одновременных соединения + явный busy-ответ сверх лимита (`ServeContext`, `pkg/protocol/protocol.go:816`, `writeBusyResponse`) | `TestServeReturnsBusyWhenConnectionLimitIsReached`, `TestServeBusyUnreadClientDoesNotBlockAcceptLoop` |
| Зависший или вредоносный engine-бинарник никогда не завершается, накапливая процессы | Таймаут с эскалацией SIGTERM→SIGKILL по группе процессов (`runBoundedCommand`, `pkg/protocol/process_unix.go:18`) | `TestCallStdioTimeoutKillsSlowChild`, `TestCallStdioTimeoutReportsSignalSequenceAndIsBounded`, `TestCallStdioTimeoutKillsDescendantOnlyWithinOwnGroup` (group-wide kill reaches a spawned descendant but not an unrelated sibling) |
| Вредоносный или сломанный engine исчерпывает память роутера безлимитным выводом в stdout/stderr | `boundedBuffer` ограничивает буферизацию `maxEngineStdoutBytes`=1 MiB / `maxEngineStderrBytes`=8 KiB (`pkg/protocol/protocol.go:898-899`); превышение stdout-лимита возвращает явную ошибку `response exceeds N bytes` вместо попытки разобрать усечённый JSON; stderr дополнительно прогоняется через `sanitizeStderrTail`, вырезающую управляющие символы (ANSI escape, переводы строк), которыми insecure engine мог бы подделать дополнительную лог-строку | `TestCallStdioBoundsOversizedStdout`, `TestCallStdioSanitizesOversizedStderr` (`pkg/protocol/protocol_test.go`); проверено вручную — helper, пишущий 1 GiB в stdout, не поднимает RSS вызывающего процесса выше единиц мегабайт (наблюдалось +3.1 MiB) |
| Обработчик (`HandlerContext`), зависший на длительной операции, продолжает работать в фоне после того, как daemon решил, что остановился | Per-connection `context.Context`, производный от `ServeContext`'s собственного, отменяется явно при shutdown/cancel, до истечения `serveShutdownTimeout` | `TestServeContextHandlerObservesConnectionContextCancellation` |
| Долгий держатель cross-process file lock (`state.Lock`) вешает вызывающего навсегда | `LockContext` ограничен deadline'ом контекста (`Lock` — `defaultLockTimeout`=30s), опрашивает `LOCK_EX\|LOCK_NB` вместо блокирующего `LOCK_EX` | `TestLockContextReturnsErrorAtDeadlineInsteadOfHanging`, `TestLockSucceedsAfterPriorReleaseWithGenerousDeadline` |
| Файл/каталог состояния или lock-файл существовал заранее с более широкими правами (баг, umask, восстановление из бэкапа) и должен быть сужен, а не унаследован | Явный `os.Chmod` каталога и temp-файла в `writeJSON`, явный `os.Chmod` каталога и lock-файла в `LockContext`; предсказуемый `path+".tmp"` заменён на `os.CreateTemp`, чтобы отравленный существующий temp-файл вообще не мог быть переиспользован | `TestSaveStateNarrowsAnAlreadyWideDirectoryMode`, `TestSaveStateIgnoresAPoisonedPredictableTempFile`, `TestLockNarrowsAnAlreadyWideLockFileMode` (`state/state_test.go`) — все три красные против кода до этого исправления |
| Токен утекает через список процессов (`ps`, `/proc/*/cmdline`) | Токен передаётся адаптеру Keychain как `CFDataRef` внутри процесса, никогда не попадает в argv/stdin CLI (`keychain/keychain_darwin.go`) | Инспекция кода: `keychain_darwin.go` не вызывает `exec.Command` вообще; передача через cgo-указатель на память процесса, а не через внешний процесс; дополнительно подтверждено тем, что реальный cgo-путь вообще не использует `os/exec` — `make test-keychain-native` прогоняет именно эту реализацию |
| Plaintext test-Keychain seam случайно попадает в поставляемую сборку или тестируется вместо реального Keychain | Доступен только под build tag `uni_chat_test_keychain`, отсутствует в обычной сборке; реальный `Security.framework`-путь дополнительно покрыт отдельным native-гейтом, а не только инспекцией кода | `make build`/`make lint`/`make vet` собирают без этого tag; `go build ./...` без `-tags` не включает `keychain/test_shim.go` вовсе; `make test-keychain-seam` доказывает поведение пакета в этой сборке; `make test-keychain-native` доказывает реальный `Security.framework`-путь |
| Секрет (приватный ключ, токен, включая Mattermost PAT без литерального префикса) попадает в коммит | `make secrets-check` — `gitleaks` 8.30.1 (`tools/go.mod`-pinned), сканирует и рабочее дерево (`dir`), и всю историю (`git`); `.gitleaks.toml` добавляет generic-entropy правило на bare 26-символьный `[a-z0-9]` токен поверх встроенных правил gitleaks (`ghp_...`, приватные ключи и т.д.); найденное значение редактируется (`--redact`) | Негативная проверка вручную: случайный 26-символьный токен и `ghp_...`-паттерн оба ловятся и редактируются; `make check-env` падает при отсутствии `gitleaks` |
| Repository release tag удалён или force-обновлён без обнаружения | GitHub ruleset `protect-release-tags` (`deletion`+`non_fast_forward`, `enforcement=active`, `bypass_actors` пуст) | `make tag-protection-check`; негативная проверка вручную — временное отключение (`enforcement=disabled`) детектируется как non-zero |
| Итого по acceptance-ступени | Все перечисленные выше process/socket-сценарии живут в acceptance-ступени | `make test-acceptance` — не менее `ACCEPTANCE_MIN=15` тестов по селектору `^Test(Serve\|Call)` в `pkg/protocol`, фактически 28 на момент написания (было 20 задокументировано, но реально запускалось только 20 из подразумевавшихся — файл `process_unix_test.go` был назван `process_test_unix.go` и потому не распознавался `go test` как тестовый вообще; см. «Честный потолок» ниже); ступень явно проверяет реальный unix-сокет и реальный `exec.Command`, а не моки |

### Честный потолок / остаточный риск

- До этого исправления `pkg/protocol/process_test_unix.go` не заканчивался на `_test.go` и потому не распознавался `go test` как тестовый файл вообще: пять тестов в нём (`TestCallStdioReturnsNormallyBeforeDeadline`, `TestCallStdioTimeoutReportsSignalSequenceAndIsBounded`, `TestCallStdioTimeoutKillsDescendantOnlyWithinOwnGroup`, `TestCallStdioChildSIGKILLIsNotReportedAsParentTimeout`, `TestCallStdioHelperProcess`) ни разу не выполнялись, а пакет `testing` утекал в зависимости реальной поставляемой библиотеки (`go list -deps` подтверждал `testing` как зависимость `pkg/protocol`). Переименован в `process_unix_test.go`; после включения два из пяти тестов оказались реально сломаны (stdout, засорённый собственным выводом `go test`; `select {}` после `signal.Ignore`, который надёжно триггерит `fatal error: all goroutines are asleep - deadlock!` раньше, чем успевает прийти внешний SIGTERM/SIGKILL) — оба исправлены, подробности в истории коммитов и в комментариях `pkg/protocol/process_unix_test.go`.
- `Listen` безусловно вызывает `os.Remove(socket)` перед созданием слушателя (`pkg/protocol/protocol.go:767`) — это осознанный выбор для очистки протухшего сокета после нештатного завершения daemon, а не уязвимость: он выполняется уже внутри uid-границы (директория `0700` не даёт постороннему uid туда что-либо положить), но стоит явно зафиксировать как дизайн-решение, а не как случайный побочный эффект.
- Plaintext test-Keychain seam (`UNI_CHAT_TEST_KEYCHAIN`/`keychain/test_shim.go`) хранит токены в открытом виде — риск ограничен build tag'ом, но остаётся человеческим фактором: собрать релиз с этим tag'ом по ошибке технически возможно, контроль тому — code review, а не машинная проверка.
- Между запусками `make dependency-check` есть окно до 30 дней (`SCA_WINDOW_DAYS`), в течение которого свежая CVE на уже используемую зависимость может быть не замечена — это принятый разрыв cadence для plain-CLI библиотечного профиля, не daemon и не internet-facing сервис.
- `CallStdio` не проверяет подпись или контрольную сумму бинарника перед запуском — доверие полностью на стороне вызывающего кода (значение `bin` должно приходить из уже провалидированного приватного реестра); сам SDK не может обеспечить эту гарантию, только документирует её как границу доверия выше.
- `make test-keychain-native` — единственный HOST-ONLY гейт репозитория и единственный, который может потребовать интерактивного присутствия человека (macOS Keychain-подтверждение); он намеренно не входит в `make check`, только в `make release-check` — см. rationale в `Makefile` у самого таргета.
- `gitleaks`' generic-entropy правило на 26-символьный токен ловит только этот конкретный формат (Mattermost PAT); секрет другого формата и достаточно низкой энтропии, не покрытый ни одним встроенным правилом gitleaks, теоретически может остаться незамеченным — контроль тому такой же, как и для остальных SCA-находок: review и периодическое расширение `.gitleaks.toml`, а не гарантия полноты.

## Crosswalk controls

Формат ряда: `control | source + verified version и ID либо точное название раздела | automatic/manual check | evidence/gate | owner`.
Значение `VERIFY` означает, что конкретный numeric ID/requirement внутри
названного раздела не был проверен в зафиксированной версии источника — сам
раздел проверен (см. rationale у каждого ряда), номер внутри него — нет,
согласно `08-security-and-reliability.md`'s правилу «ASVS ID нельзя
угадывать».

| Control/практика | Источник и идентификатор/раздел | Проверка | Evidence/gate | Owner |
| --- | --- | --- | --- | --- |
| Валидация внешнего ввода и безопасный отказ (лимит размера запроса, trailing-data guard, panic guard, bounded engine stdout/stderr) | OWASP ASVS 5.0.0, глава **V2: Validation and Business Logic** (название главы проверено вживую по `github.com/OWASP/ASVS` @ `5.0/en`; конкретный requirement ID — `VERIFY`) | unit/acceptance tests | `make test-acceptance`, `make test`; abuse-case ряды выше | maintainer |
| Хранение секрета в платформенном keystore (`SecItem`) | Apple Developer Documentation, *Keychain Services* (`developer.apple.com/documentation/security/keychain-services`); конкретный раздел/версия документации — `VERIFY` | native test против реального `Security.framework` | `make test-keychain-native` | maintainer |
| Целостность и pinning зависимостей (`tools/go.mod` tool directives: staticcheck, govulncheck, osv-scanner, gitleaks; `go.sum`) | NIST SSDF 1.1, practice family **PS (Protect the Software)**, конкретная practice/subpractice — `VERIFY` | pin/scan в Makefile gates | `make dependency-check`, `make check-env`, `.task/release-evidence/dependency.properties` | maintainer |
| Секреты в коммитах (рабочее дерево и история) | MITRE CWE-798: *Use of Hard-coded Credentials* (стабильный, публичный ID; принадлежность к конкретному году CWE Top 25 не проверялась и не заявляется) | автоматический скан | `make secrets-check` (gitleaks 8.30.1, `.gitleaks.toml`) | maintainer |
| Целостность репозиторных release-тегов (запрет force-update/delete) | GitHub Repository Rulesets API (`docs.github.com/en/rest/repos/rules`); конкретная версия REST API — `VERIFY` | автоматическая проверка через `gh api` | `make tag-protection-check`, `.task/release-evidence/tag-protection.properties` | maintainer |
| Хеширование release-артефактов | FIPS 180-4 (Secure Hash Standard, SHA-256) | `shasum -a 256` над фактическими файлами | `make release-local` → `SHA256SUMS` | maintainer |
| Provenance source/context, artifact digest, подпись релиза | — | — | `N/A`: непубликуемая библиотека без publishable artifact — `release-local` производит source archive, не подписываемый бинарный артефакт; `sign`/`attest`/`verify-provenance` зафиксированы `N/A` в README (`Makefile targets`) | — |

Проект заменит `VERIFY` на проверенный ID или точное название раздела по
мере фактической проверки каждого источника в зафиксированной версии — см.
`08-security-and-reliability.md`'s собственное правило: ни один ряд не
считается закрытым по одной ссылке, gate и ручное evidence перечислены
явно для каждого.
