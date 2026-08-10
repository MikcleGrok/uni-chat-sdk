SHELL := /bin/sh

VERSION ?= $(shell tag=$$(git describe --exact-match --tags HEAD 2>/dev/null) && printf '%s' "$$tag" | sed 's/^v//' || printf '%s' dev)
TAG ?= v$(VERSION)
PREFIX ?= $(HOME)/.local
DIST ?= dist
ARCHIVE := $(DIST)/uni-chat-sdk-$(VERSION).tar.gz
LOCAL_RELEASE_DIR := $(DIST)/local-release/$(VERSION)
LOCAL_RELEASE_ARCHIVE := $(LOCAL_RELEASE_DIR)/uni-chat-sdk-$(VERSION).tar.gz

.PHONY: format lint vet test race coverage secrets-check dependency-check check check-local-tag package-local install-local verify-local-install install-local-smoke release-local

format:
	@test -z "$$(gofmt -l $$(go list -f '{{.Dir}}' ./...))"

lint:
	@if command -v staticcheck >/dev/null; then staticcheck ./...; else echo 'staticcheck is required for lint' >&2; exit 1; fi

vet:
	@go vet ./...

test:
	@go test -tags uni_chat_test_keychain -count=1 ./...

race:
	@go test -tags uni_chat_test_keychain -race -count=1 ./...

coverage:
	@go test -tags uni_chat_test_keychain -coverprofile=coverage.out ./...

secrets-check:
	@! git grep -nE '(COSIGN_PRIVATE_KEY[[:space:]]*=|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|ghp_[A-Za-z0-9]+)' -- . ':!go.sum'

dependency-check:
	@if command -v govulncheck >/dev/null; then govulncheck ./...; else echo 'govulncheck unavailable' >&2; exit 1; fi

check-local-tag:
	@test -z "$$(git status --porcelain --untracked-files=all)" || { printf '%s\n' 'check-local-tag requires a clean tree' >&2; exit 1; }
	@test "$(VERSION)" != dev -a "$(VERSION)" != "" || { printf '%s\n' 'VERSION must be canonical SemVer from an exact tag' >&2; exit 1; }
	@test "$(TAG)" = "v$(VERSION)" || { printf '%s\n' 'TAG must be the exact canonical tag v$(VERSION)' >&2; exit 1; }
	@test "$$(git describe --exact-match --tags HEAD 2>/dev/null || true)" = "$(TAG)" || { printf '%s\n' 'HEAD must be the exact canonical local tag' >&2; exit 1; }
	@test "$$(git rev-parse --verify "$(TAG)^{commit}")" = "$$(git rev-parse HEAD)"

package-local:
	@mkdir -p "$(DIST)"
	@tar --exclude='./.git' --exclude='./$(DIST)' -czf "$(ARCHIVE)" .
	@test -s "$(ARCHIVE)"

install-local: package-local
	@rm -rf "$(PREFIX)/share/uni-chat-sdk/$(VERSION)"
	@mkdir -p "$(PREFIX)/share/uni-chat-sdk/$(VERSION)"
	@tar -xzf "$(ARCHIVE)" -C "$(PREFIX)/share/uni-chat-sdk/$(VERSION)"

verify-local-install:
	@test -f "$(PREFIX)/share/uni-chat-sdk/$(VERSION)/go.mod"
	@test -d "$(PREFIX)/share/uni-chat-sdk/$(VERSION)/pkg"
	@test -d "$(PREFIX)/share/uni-chat-sdk/$(VERSION)/state"

install-local-smoke: check-local-tag
	@prefix=$$(mktemp -d); dist=$$(mktemp -d); trap 'rm -rf "$$prefix" "$$dist"' EXIT; $(MAKE) --no-print-directory package-local DIST="$$dist" VERSION="$(VERSION)" TAG="$(TAG)"; $(MAKE) --no-print-directory install-local PREFIX="$$prefix" DIST="$$dist" VERSION="$(VERSION)" TAG="$(TAG)"; $(MAKE) --no-print-directory verify-local-install PREFIX="$$prefix" VERSION="$(VERSION)"

release-local: check-local-tag
	@set -eu; release_dir="$(LOCAL_RELEASE_DIR)"; archive="$(LOCAL_RELEASE_ARCHIVE)"; mkdir -p "$$release_dir"; git archive --format=tar.gz --prefix="uni-chat-sdk-$(VERSION)/" "$(TAG)" -o "$$archive"; (cd "$$release_dir" && shasum -a 256 "$$(basename "$$archive")" > SHA256SUMS); sha256=$$(cut -d ' ' -f 1 "$$release_dir/SHA256SUMS"); commit=$$(git rev-parse "$(TAG)^{commit}"); printf '{\n  "version": "$(VERSION)",\n  "tag": "$(TAG)",\n  "commit": "%s",\n  "archive": "%s",\n  "sha256": "%s"\n}\n' "$$commit" "$$(basename "$$archive")" "$$sha256" > "$$release_dir/metadata.json"; git show "$(TAG):CHANGELOG.md" > "$$release_dir/RELEASE_NOTES.md"; printf 'Local release written to %s\n' "$$release_dir"

check: format lint vet test race coverage secrets-check dependency-check
