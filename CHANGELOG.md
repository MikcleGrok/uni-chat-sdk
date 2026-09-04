# Changelog

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
