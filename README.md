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
