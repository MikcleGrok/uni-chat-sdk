# Security

The library contains no release binaries or signing keys. Consumers verify Go
module checksums through the normal Go checksum database and their `go.sum`.

Адаптер macOS использует документированные API `SecItem` из
`Security.framework`. Токен передаётся внутри процесса как `CFDataRef`; адаптер
не запускает CLI `security` и никогда не помещает токен в argv или stdin.

The `UNI_CHAT_TEST_KEYCHAIN` seam is compiled only with the
`uni_chat_test_keychain` и не входит в release-сборки. В сборках не для macOS
или без cgo использование адаптера завершается явной ошибкой.
