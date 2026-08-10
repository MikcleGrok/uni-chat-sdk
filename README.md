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
release API, or publishing service. Run it from a clean checkout whose `HEAD` is
the exact canonical tag:

```sh
make check-local-tag VERSION=0.1.0 TAG=v0.1.0
make install-local-smoke VERSION=0.1.0 TAG=v0.1.0
```

The smoke target creates a source archive, installs it under a disposable
prefix, and verifies the module layout. It removes its temporary prefix and
artifact directory when it exits.
