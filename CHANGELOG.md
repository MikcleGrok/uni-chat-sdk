# Changelog

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
