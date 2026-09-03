SHELL := /bin/sh
.DEFAULT_GOAL := help

VERSION ?= $(shell tag=$$(git describe --exact-match --tags HEAD 2>/dev/null) && printf '%s' "$$tag" | sed 's/^v//' || printf '%s' dev)
TAG ?= v$(VERSION)
PREFIX ?= $(HOME)/.local
GO ?= go
GO_MIN := $(shell awk '/^go /{print $$2; exit}' go.mod)
CLAUDE_SCRIPTS_DIR ?= $(HOME)/.claude/scripts
GO_TEST_WRAPPER := $(CLAUDE_SCRIPTS_DIR)/go-test.sh
GO_VET_WRAPPER := $(CLAUDE_SCRIPTS_DIR)/go-vet.sh
GOFMT_WRAPPER := $(CLAUDE_SCRIPTS_DIR)/gofmt-check.sh
GO_TEST_CMD := $(if $(wildcard $(GO_TEST_WRAPPER)),"$(GO_TEST_WRAPPER)",go) $(if $(wildcard $(GO_TEST_WRAPPER)),,test)
GO_VET_CMD := $(if $(wildcard $(GO_VET_WRAPPER)),"$(GO_VET_WRAPPER)",go) $(if $(wildcard $(GO_VET_WRAPPER)),,vet)
DIST ?= dist
# Acceptance stage: the tests that cross a real process boundary — Listen/Dial
# over a real unix socket and CallStdio over a real exec.Command child. They
# live next to the code they exercise, so the stage is selected by name rather
# than by directory; ACCEPTANCE_MIN guards the selector against rotting to a
# pattern that silently matches nothing.
ACCEPTANCE_PKG ?= ./pkg/protocol
ACCEPTANCE_RUN ?= ^Test(Serve|Call)
ACCEPTANCE_MIN ?= 15
ARCHIVE := $(DIST)/uni-chat-sdk-$(VERSION).tar.gz
LOCAL_RELEASE_DIR := $(DIST)/local-release/$(VERSION)
LOCAL_RELEASE_ARCHIVE := $(LOCAL_RELEASE_DIR)/uni-chat-sdk-$(VERSION).tar.gz

.PHONY: help setup check-env format fmt lint vet build test test-unit test-acceptance race coverage secrets-check dependency-check security version check-version release-check check check-local-tag package-local install-local verify-local-install install-local-smoke release-local local-release

help: ## Show this help: every target with its purpose
	@printf 'uni-chat-sdk — make targets\n\n'
	@awk 'BEGIN{FS = ":.*## "} /^[a-zA-Z0-9_.-]+:.*## /{printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf '\nVariables: GO=%s PREFIX=%s DIST=%s VERSION=%s TAG=%s\n' '$(GO)' '$(PREFIX)' '$(DIST)' '$(VERSION)' '$(TAG)'

setup: ## Prepare the local dev environment (module deps and the tools the gates call)
	@$(GO) mod download
	@$(GO) mod verify
	@$(GO) tool staticcheck -version
	@command -v govulncheck >/dev/null 2>&1 || { printf '%s\n' 'setup: installing govulncheck'; $(GO) install golang.org/x/vuln/cmd/govulncheck@latest; }
	@$(MAKE) --no-print-directory check-env

check-env: ## Verify the Go toolchain and the external tools the targets assume
	@command -v $(GO) >/dev/null 2>&1 || { printf '%s\n' 'check-env: $(GO) is required' >&2; exit 1; }
	@have=$$($(GO) env GOVERSION | sed 's/^go//'); printf '%s\n%s\n' '$(GO_MIN)' "$$have" | sort -V -C || { printf 'check-env: go.mod requires Go %s or newer, found %s\n' '$(GO_MIN)' "$$have" >&2; exit 1; }
	@for tool in git tar shasum awk sed; do command -v "$$tool" >/dev/null 2>&1 || { printf 'check-env: %s is required\n' "$$tool" >&2; exit 1; }; done
	@command -v govulncheck >/dev/null 2>&1 || { printf '%s\n' 'check-env: dependency-check needs govulncheck; run make setup' >&2; exit 1; }
	@$(GO) tool staticcheck -version >/dev/null 2>&1 || { printf '%s\n' 'check-env: the go.mod-pinned staticcheck is unavailable; run make setup' >&2; exit 1; }
	@printf 'check-env OK: Go %s (go.mod requires %s), govulncheck and the pinned staticcheck are available\n' "$$($(GO) env GOVERSION | sed 's/^go//')" '$(GO_MIN)'

format: ## Fail when any tracked Go file is not gofmt-clean
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; export HOME="$$tmp/home"; if test -x "$(GOFMT_WRAPPER)"; then "$(GOFMT_WRAPPER)" -C . -novcs; else test -z "$$(gofmt -l $$(go list -f '{{.Dir}}' ./...))"; fi

fmt: format

lint: ## Run the go.mod-pinned staticcheck over every package
	@go tool staticcheck ./...

vet: ## Run go vet over every package
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; HOME="$$tmp/home" $(GO_VET_CMD) -C . ./...

build: ## Compile every package (library module: no binary is produced)
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; HOME="$$tmp/home" $(GO) build -C . ./...

test: ## Run the full package test suite against an isolated HOME and test keychain
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; printf '%s\n' '{}' > "$$tmp/keychain.json"; HOME="$$tmp/home" UNI_CHAT_TEST_KEYCHAIN="$$tmp/keychain.json" $(GO_TEST_CMD) -C . -tags uni_chat_test_keychain ./...

test-unit: test

test-acceptance: ## Run only the process-boundary suite (real unix sockets, real subprocesses)
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; printf '%s\n' '{}' > "$$tmp/keychain.json"; \
		HOME="$$tmp/home" UNI_CHAT_TEST_KEYCHAIN="$$tmp/keychain.json" $(GO_TEST_CMD) -C . -tags uni_chat_test_keychain -count=1 -v -run '$(ACCEPTANCE_RUN)' $(ACCEPTANCE_PKG) > "$$tmp/out" 2>&1 || { cat "$$tmp/out"; exit 1; }; \
		ran=$$(grep -c '^=== RUN   Test' "$$tmp/out" || true); \
		test "$$ran" -ge $(ACCEPTANCE_MIN) || { cat "$$tmp/out"; printf 'test-acceptance: selector %s in %s matched %s tests, expected at least %s — the selector has rotted\n' '$(ACCEPTANCE_RUN)' '$(ACCEPTANCE_PKG)' "$$ran" '$(ACCEPTANCE_MIN)' >&2; exit 1; }; \
		printf 'test-acceptance OK: %s process-boundary tests over real unix sockets and real subprocesses\n' "$$ran"

race: ## Run the test suite under the race detector
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; printf '%s\n' '{}' > "$$tmp/keychain.json"; HOME="$$tmp/home" UNI_CHAT_TEST_KEYCHAIN="$$tmp/keychain.json" $(GO_TEST_CMD) -C . -tags uni_chat_test_keychain -race ./...

coverage: ## Measure coverage as a side metric (no threshold gate)
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; printf '%s\n' '{}' > "$$tmp/keychain.json"; HOME="$$tmp/home" UNI_CHAT_TEST_KEYCHAIN="$$tmp/keychain.json" $(GO_TEST_CMD) -C . -tags uni_chat_test_keychain -coverprofile="$$tmp/coverage.out" ./...

secrets-check: ## Fail when a private key or token pattern is committed
	@! git grep -nE '(COSIGN_PRIVATE_KEY[[:space:]]*=|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|ghp_[A-Za-z0-9]+)' -- . ':!go.sum'

dependency-check: ## Run the SCA scan (govulncheck) fresh against the current dependency set
	@if command -v govulncheck >/dev/null; then govulncheck ./...; else echo 'govulncheck unavailable' >&2; exit 1; fi

security: secrets-check dependency-check ## Run every security gate (secrets and SCA)

version: ## Print the normalized module version resolved from the exact tag on HEAD
	@printf '%s\n' '$(VERSION)'

check-version: ## Validate the single version source (SemVer shape and TAG/VERSION agreement)
	@test -n '$(VERSION)' || { printf '%s\n' 'check-version: VERSION must not be empty' >&2; exit 1; }
	@test '$(TAG)' = 'v$(VERSION)' || { printf '%s\n' 'check-version: TAG must be v$(VERSION)' >&2; exit 1; }
	@if test '$(VERSION)' = dev; then \
		test -z "$$(git describe --exact-match --tags HEAD 2>/dev/null || true)" || { printf '%s\n' 'check-version: VERSION resolved to dev while HEAD carries an exact tag' >&2; exit 1; }; \
		printf '%s\n' 'check-version OK: untagged checkout, normalized version "dev" (not releasable)'; \
	else \
		printf '%s' '$(VERSION)' | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$' || { printf '%s\n' 'check-version: VERSION must be MAJOR.MINOR.PATCH with an optional SemVer prerelease and no build metadata' >&2; exit 1; }; \
		printf 'check-version OK: normalized version %s, tag %s\n' '$(VERSION)' '$(TAG)'; \
	fi

release-check: ## Pre-tag release completeness gate on the candidate commit (make release-check VERSION=X.Y.Z)
	@test '$(VERSION)' != dev || { printf '%s\n' 'release-check: pass the candidate release version, e.g. make release-check VERSION=0.1.19' >&2; exit 1; }
	@test -z "$$(git status --porcelain --untracked-files=all)" || { printf '%s\n' 'release-check: requires a clean tree' >&2; exit 1; }
	@$(MAKE) --no-print-directory check-version
	@if git rev-parse --verify --quiet 'refs/tags/$(TAG)' >/dev/null; then test "$$(git rev-parse '$(TAG)^{commit}')" = "$$(git rev-parse HEAD)" || { printf '%s\n' 'release-check: planned tag $(TAG) already exists on a different commit' >&2; exit 1; }; fi
	@grep -q '^## \[$(VERSION)\]' CHANGELOG.md || { printf '%s\n' 'release-check: CHANGELOG.md has no "## [$(VERSION)]" section' >&2; exit 1; }
	@$(MAKE) --no-print-directory check-env format lint vet build test test-acceptance race secrets-check dependency-check
	@printf 'release-check OK: candidate %s, planned tag %s, normalized version %s\n' "$$(git rev-parse HEAD)" '$(TAG)' '$(VERSION)'

check-local-tag: ## Assert HEAD is the exact canonical local tag on a clean tree
	@test -z "$$(git status --porcelain --untracked-files=all)" || { printf '%s\n' 'check-local-tag requires a clean tree' >&2; exit 1; }
	@test "$(VERSION)" != dev -a "$(VERSION)" != "" || { printf '%s\n' 'VERSION must be canonical SemVer from an exact tag' >&2; exit 1; }
	@test "$(TAG)" = "v$(VERSION)" || { printf '%s\n' 'TAG must be the exact canonical tag v$(VERSION)' >&2; exit 1; }
	@test "$$(git describe --exact-match --tags HEAD 2>/dev/null || true)" = "$(TAG)" || { printf '%s\n' 'HEAD must be the exact canonical local tag' >&2; exit 1; }
	@test "$$(git cat-file -t "$(TAG)" 2>/dev/null || true)" = tag || { printf '%s\n' '$(TAG) must be an annotated tag, not a lightweight one' >&2; exit 1; }
	@test "$$(git rev-parse --verify "$(TAG)^{commit}")" = "$$(git rev-parse HEAD)"

package-local: ## Build the local source archive into the DIST directory
	@mkdir -p "$(DIST)"
	@tar --exclude='./.git' --exclude='./$(DIST)' -czf "$(ARCHIVE)" .
	@test -s "$(ARCHIVE)"

install-local: package-local ## Unpack the source archive into the owned PREFIX/share/uni-chat-sdk/VERSION subtree
	@rm -rf "$(PREFIX)/share/uni-chat-sdk/$(VERSION)"
	@mkdir -p "$(PREFIX)/share/uni-chat-sdk/$(VERSION)"
	@tar -xzf "$(ARCHIVE)" -C "$(PREFIX)/share/uni-chat-sdk/$(VERSION)"

verify-local-install: ## Assert the installed version subtree carries the expected module layout
	@test -f "$(PREFIX)/share/uni-chat-sdk/$(VERSION)/go.mod"
	@test -d "$(PREFIX)/share/uni-chat-sdk/$(VERSION)/pkg"
	@test -d "$(PREFIX)/share/uni-chat-sdk/$(VERSION)/state"

install-local-smoke: check-local-tag ## Package and install the exact tag into a disposable PREFIX and verify it
	@prefix=$$(mktemp -d); dist=$$(mktemp -d); trap 'rm -rf "$$prefix" "$$dist"' EXIT; $(MAKE) --no-print-directory package-local DIST="$$dist" VERSION="$(VERSION)" TAG="$(TAG)"; $(MAKE) --no-print-directory install-local PREFIX="$$prefix" DIST="$$dist" VERSION="$(VERSION)" TAG="$(TAG)"; $(MAKE) --no-print-directory verify-local-install PREFIX="$$prefix" VERSION="$(VERSION)"

release-local: check-local-tag ## Write the offline release bundle for the exact tag into dist/local-release/<version>
	@set -eu; release_dir="$(LOCAL_RELEASE_DIR)"; archive="$(LOCAL_RELEASE_ARCHIVE)"; mkdir -p "$$release_dir"; git archive --format=tar.gz --prefix="uni-chat-sdk-$(VERSION)/" "$(TAG)" -o "$$archive"; (cd "$$release_dir" && shasum -a 256 "$$(basename "$$archive")" > SHA256SUMS); sha256=$$(cut -d ' ' -f 1 "$$release_dir/SHA256SUMS"); commit=$$(git rev-parse "$(TAG)^{commit}"); printf '{\n  "version": "$(VERSION)",\n  "tag": "$(TAG)",\n  "commit": "%s",\n  "archive": "%s",\n  "sha256": "%s"\n}\n' "$$commit" "$$(basename "$$archive")" "$$sha256" > "$$release_dir/metadata.json"; git show "$(TAG):CHANGELOG.md" > "$$release_dir/RELEASE_NOTES.md"; printf 'Local release written to %s\n' "$$release_dir"

local-release: release-local ## Alias for release-local

check: check-env check-version format lint vet build test test-acceptance race coverage secrets-check dependency-check ## Run every local gate
