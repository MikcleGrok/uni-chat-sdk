# Security

The library contains no release binaries or signing keys. Consumers verify Go
module checksums through the normal Go checksum database and their `go.sum`.

The `UNI_CHAT_TEST_KEYCHAIN` seam is compiled only with the
`uni_chat_test_keychain` build tag and is not part of release builds.
