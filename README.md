# uni-chat-sdk

Shared Go packages for uni-chat engines and the private uni-chat core.

Packages:

- `pkg/protocol` contains the engine wire protocol.
- `pkg/capability` contains capability metadata.
- `state` contains per-engine state and atomic configuration helpers.
- `keychain` contains the macOS Keychain adapter.

The module is intentionally independent from `uni-chat` and engine repositories.
