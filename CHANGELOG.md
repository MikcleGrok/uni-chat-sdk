# Changelog

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

- Initial local extraction from `uni-chat`.
