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
# Dev/security tooling (staticcheck, govulncheck, osv-scanner, gitleaks) is
# pinned by exact version in tools/go.mod and resolved from that manifest via
# `go -C tools tool <name>` — never from PATH, and never as a root go.mod tool
# directive: this repo is a library imported by other repos, and a root tool
# directive would drag the scanners' own dependency trees into every
# consumer's module graph and into what `dependency-check` scans as this
# module's own dependency state. See the header comment in tools/go.mod for
# the measurement that forced the split.
#
# `go -C $(GO_TOOLS_DIR) tool <name>` runs <name> with its process cwd set to
# $(GO_TOOLS_DIR) (a separate module with no packages of its own), which is
# fine for a flag-only invocation (`-version`) but wrong for one that takes a
# package pattern: `./...` would then resolve inside tools/, not against this
# module's source. govulncheck has its own `-C dir` flag to redirect analysis
# back to the real module (used in `dependency-check`); staticcheck has no
# such flag, so `lint` instead builds the tools/go.mod-pinned binary once to a
# temp path and runs that binary from the repo root.
GO_TOOLS_DIR := tools
GO_TOOL := $(GO) -C $(GO_TOOLS_DIR) tool
EVIDENCE_DIR ?= .task/release-evidence
DEPENDENCY_EVIDENCE := $(EVIDENCE_DIR)/dependency.properties
# Plain-CLI SCA cadence: at least once every 30 days (08-security-and-reliability.md).
# Enforced by the dependency-freshness staleness gate inside `check`/`release-check`.
SCA_WINDOW_DAYS ?= 30
# osv-scanner's mandatory full-native flag set (--data-source native
# --all-vulns) was verified against `osv-scanner scan source --help` of the
# exact version pinned in tools/go.mod; the version itself is no longer
# duplicated here as a Makefile variable — tools/go.mod/go.sum is the single
# source, resolved via $(GO_TOOL) osv-scanner, never from PATH.
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

.PHONY: help setup check-env format fmt lint vet build test test-unit test-acceptance race coverage test-keychain-seam test-keychain-native secrets-check dependency-check dependency-freshness security version check-version check-onboarding whats-new cross-build tag-protection-check release-check check check-local-tag package-local install-local verify-local-install install-scoping-test install-local-smoke release-local local-release verify-release

help: ## Show this help: every target with its purpose
	@printf 'uni-chat-sdk — make targets\n\n'
	@awk 'BEGIN{FS = ":.*## "} /^[a-zA-Z0-9_.-]+:.*## /{printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf '\nVariables: GO=%s PREFIX=%s DIST=%s VERSION=%s TAG=%s GOOS=%s GOARCH=%s\n' '$(GO)' '$(PREFIX)' '$(DIST)' '$(VERSION)' '$(TAG)' "$$($(GO) env GOOS)" "$$($(GO) env GOARCH)"
	@printf '\nHOST-ONLY: make test-keychain-native exercises the real macOS Security.framework (cgo) directly and only\n'
	@printf 'runs on darwin+cgo (N/A elsewhere); every other package, target and keychain test stage\n'
	@printf '(test, test-acceptance, race, coverage, test-keychain-seam) is platform-independent. Owner: maintainer.\n'
	@printf '\nSCA: dependency-check scans fresh (govulncheck + osv-scanner); dependency-freshness enforces the %s-day cadence gate without re-scanning.\n' '$(SCA_WINDOW_DAYS)'

setup: ## Prepare the local dev environment (module deps and the tools the gates call)
	@$(GO) mod download
	@$(GO) mod verify
	@$(GO) -C $(GO_TOOLS_DIR) mod download
	@$(GO) -C $(GO_TOOLS_DIR) mod verify
	@$(GO_TOOL) staticcheck -version
	@$(GO_TOOL) govulncheck -version
	@$(GO_TOOL) osv-scanner --version
	@$(GO_TOOL) gitleaks version
	@$(MAKE) --no-print-directory check-env

check-env: ## Verify the Go toolchain and the external tools the targets assume
	@command -v $(GO) >/dev/null 2>&1 || { printf '%s\n' 'check-env: $(GO) is required' >&2; exit 1; }
	@have=$$($(GO) env GOVERSION | sed 's/^go//'); printf '%s\n%s\n' '$(GO_MIN)' "$$have" | sort -V -C || { printf 'check-env: go.mod requires Go %s or newer, found %s\n' '$(GO_MIN)' "$$have" >&2; exit 1; }
	@for tool in git tar shasum awk sed jq; do command -v "$$tool" >/dev/null 2>&1 || { printf 'check-env: %s is required\n' "$$tool" >&2; exit 1; }; done
	@$(GO_TOOL) staticcheck -version >/dev/null 2>&1 || { printf '%s\n' 'check-env: the tools/go.mod-pinned staticcheck is unavailable; run make setup' >&2; exit 1; }
	@$(GO_TOOL) govulncheck -version >/dev/null 2>&1 || { printf '%s\n' 'check-env: the tools/go.mod-pinned govulncheck is unavailable; run make setup' >&2; exit 1; }
	@$(GO_TOOL) osv-scanner --version >/dev/null 2>&1 || { printf '%s\n' 'check-env: the tools/go.mod-pinned osv-scanner is unavailable; run make setup' >&2; exit 1; }
	@$(GO_TOOL) gitleaks version >/dev/null 2>&1 || { printf '%s\n' 'check-env: the tools/go.mod-pinned gitleaks is unavailable; run make setup' >&2; exit 1; }
	@command -v gh >/dev/null 2>&1 || { printf '%s\n' 'check-env: tag-protection-check needs the gh CLI; install: https://cli.github.com' >&2; exit 1; }
	@gh auth status >/dev/null 2>&1 || { printf '%s\n' 'check-env: gh is not authenticated; run: gh auth login' >&2; exit 1; }
	@printf 'check-env OK: Go %s (go.mod requires %s); tools/go.mod-pinned staticcheck+govulncheck+osv-scanner+gitleaks and an authenticated gh are available\n' "$$($(GO) env GOVERSION | sed 's/^go//')" '$(GO_MIN)'

format: ## Fail when any tracked Go file is not gofmt-clean
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; export HOME="$$tmp/home"; if test -x "$(GOFMT_WRAPPER)"; then "$(GOFMT_WRAPPER)" -C . -novcs; else test -z "$$(gofmt -l $$(go list -f '{{.Dir}}' ./...))"; fi

fmt: format

lint: ## Run the tools/go.mod-pinned staticcheck over every package
	@bindir=$$(mktemp -d); trap 'rm -rf "$$bindir"' EXIT HUP INT TERM; $(GO) -C $(GO_TOOLS_DIR) build -o "$$bindir/staticcheck" honnef.co/go/tools/cmd/staticcheck; "$$bindir/staticcheck" ./...

vet: ## Run go vet over every package
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; HOME="$$tmp/home" $(GO_VET_CMD) -C . ./...

build: ## Compile every package (library module: no binary is produced)
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; HOME="$$tmp/home" $(GO) build -C . ./...

# Declared OS/ARCH matrix (README.md's OS/ARCH row, 14-cross-platform-ci.md:
# "Linux build и macOS build MUST иметь явно зафиксированные GOOS/GOARCH,
# CGO-политику..."): darwin/arm64 and darwin/amd64 build cgo=1, exercising the
# real keychain_darwin.go branch; linux/amd64 and linux/arm64 build cgo=0,
# exercising keychain_unsupported.go — that branch previously compiled on no
# gate at all on a macOS dev host. Pure-Go cross-compilation for every pair
# here needs only the already-installed host Go toolchain's std library
# object files, no network access and no foreign C toolchain.
CROSS_BUILD_TARGETS := darwin/arm64/1 darwin/amd64/1 linux/amd64/0 linux/arm64/0

cross-build: ## Build every declared OS/ARCH pair with its documented CGO policy (see README.md OS/ARCH)
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; \
	for target in $(CROSS_BUILD_TARGETS); do \
	  goos=$${target%%/*}; rest=$${target#*/}; goarch=$${rest%%/*}; cgo=$${rest##*/}; \
	  HOME="$$tmp/home" GOOS="$$goos" GOARCH="$$goarch" CGO_ENABLED="$$cgo" $(GO) build -C . ./... || { printf 'cross-build: FAILED for GOOS=%s GOARCH=%s CGO_ENABLED=%s\n' "$$goos" "$$goarch" "$$cgo" >&2; exit 1; }; \
	done; \
	printf 'cross-build OK: %s\n' '$(CROSS_BUILD_TARGETS)'

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

# `make test`/`test-acceptance`/`race`/`coverage` above always pass
# `-tags uni_chat_test_keychain`, so keychain_test.go's seam tests
# (`//go:build !uni_chat_test_keychain`) never compile into any of them — the
# tag's own negation excludes the file. That is correct given the shim's
# design (`test_shim.go`'s testToken() makes GetToken return early, so a seam
# test asserting the real platformGetToken boundary would have to fail under
# that tag), but it also means the seam suite itself — TestGetToken,
# TestGetTokenMissing, TestSetToken — was compiled by no gate at all. This
# target closes that gap: it runs the package in the shipped (no-shim) build,
# selected explicitly by name so it never picks up
# TestNativeKeychainRoundTrip (keychain_native_darwin_test.go, see
# test-keychain-native below) even on a darwin+cgo host where that file's own
# build tag (`darwin && cgo && !uni_chat_test_keychain`) would otherwise admit
# it into the same `./keychain/...` build. It is safe to run anywhere,
# unattended: the seam tests replace platformGetToken/platformSetToken with
# function-literal stubs, so Security.framework is never reached.
test-keychain-seam: ## Run the keychain seam suite in the shipped (no-shim) build
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; \
		HOME="$$tmp/home" $(GO_TEST_CMD) -C . -count=1 -v -run '^(TestGetToken|TestGetTokenMissing|TestSetToken)$$' ./keychain/... > "$$tmp/out" 2>&1 || { cat "$$tmp/out"; exit 1; }; \
		for want in TestGetToken TestGetTokenMissing TestSetToken; do grep -Fq -- "--- PASS: $$want" "$$tmp/out" || { cat "$$tmp/out"; printf 'test-keychain-seam: %s did not report PASS — the seam suite selector has rotted\n' "$$want" >&2; exit 1; }; done; \
		printf 'test-keychain-seam OK: TestGetToken, TestGetTokenMissing, TestSetToken ran against the shipped (no test-keychain tag) build\n'

# HOST-ONLY (owner: maintainer). Exercises the real Security.framework cgo
# path directly (keychain_darwin.go's platformGetTokenImpl/
# platformSetTokenImpl/platformDeleteTokenImpl via
# keychain_native_darwin_test.go's TestNativeKeychainRoundTrip) instead of the
# swappable function-var seam every other keychain test uses — the mock
# theater this gate exists to close (12-test-contract.md:32-33). Applicable
# only on darwin with cgo enabled; every other OS/ARCH build compiles
# keychain_unsupported.go instead (the same build-tag-only exclusion already
# used everywhere else in this package), so this target explicitly reports
# `N/A` there rather than silently succeeding with "0 tests, ok" —
# 14-cross-platform-ci.md:179-183 forbids a platform branch silently
# vanishing from a gate's result.
#
# Deliberately NOT part of `make check`, unlike test-keychain-seam above —
# this is a documented deviation from this fix's own "как проверить" note
# (which says both new targets join `check` and `release-check`), made after
# verifying live on this exact host (macOS 15/Sequoia, this repository's own
# darwin+cgo dev machine) that the very first SecItemAdd from a freshly built,
# ad-hoc-signed `go test` binary blocks on a real interactive SecurityAgent
# confirmation dialog — even with a brand-new never-reused account name and a
# dedicated non-production service (`uni-chat-selftest`), i.e. even with every
# mitigation this test's own design already applies to avoid exactly that
# prompt. `check` is the frequent, often headless/agent-driven dev-loop gate;
# wiring a test that can require live human interaction at the keyboard into
# it would turn an ordinary `make check` into a potential indefinite wait on
# a GUI dialog no unattended session can answer. `-timeout 20s` below bounds
# that failure mode to a fast, loud, actionable non-zero exit instead of a
# hang (verified: the timeout fires and reports the exact blocked cgo call).
# `make release-check` below DOES include this target, because cutting a
# release is a deliberate, infrequent action where a maintainer is expected
# to be at the keyboard already — the same assumption this org's release
# tooling already makes for other one-time OS confirmations (e.g. Touch ID).
test-keychain-native: ## HOST-ONLY (owner: maintainer): exercise the real Security.framework cgo path (darwin+cgo only; not part of `check` — see comment above)
	@case "$$($(GO) env GOOS)/$$($(GO) env CGO_ENABLED)" in \
	  darwin/1) ;; \
	  *) printf 'test-keychain-native: N/A on GOOS=%s CGO_ENABLED=%s — applicable only on darwin with cgo enabled; keychain_unsupported.go covers every other OS/ARCH\n' "$$($(GO) env GOOS)" "$$($(GO) env CGO_ENABLED)"; exit 0;; \
	esac; \
	tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; \
	HOME="$$tmp/home" $(GO) test -C . -timeout 20s -count=1 -v -run '^TestNativeKeychainRoundTrip$$' ./keychain/... > "$$tmp/out" 2>&1 || { cat "$$tmp/out"; exit 1; }; \
	grep -Fq -- '--- PASS: TestNativeKeychainRoundTrip' "$$tmp/out" || { cat "$$tmp/out"; printf 'test-keychain-native: TestNativeKeychainRoundTrip did not report PASS\n' >&2; exit 1; }; \
	printf 'test-keychain-native OK: real Security.framework round trip (SecItemAdd/SecItemCopyMatching/SecItemDelete) via service uni-chat-selftest\n'

coverage: ## Measure coverage as a side metric (no threshold gate)
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; mkdir -p "$$tmp/home"; printf '%s\n' '{}' > "$$tmp/keychain.json"; HOME="$$tmp/home" UNI_CHAT_TEST_KEYCHAIN="$$tmp/keychain.json" $(GO_TEST_CMD) -C . -tags uni_chat_test_keychain -coverprofile="$$tmp/coverage.out" ./...

# gitleaks scans both the working tree (dir) and the full commit history
# (git): a secret introduced and then removed in a later commit is still a
# leak. --redact keeps a found secret out of the gate's own log; the target
# path is passed as $(CURDIR) rather than "." because `go -C tools tool`
# changes the invoked tool's process cwd to tools/ (Makefile:23-30's
# documented trap) — a bare "." would scan the tools/ submodule tree instead
# of the repository root, and gitleaks' own config auto-discovery
# ("(target path)/.gitleaks.toml") would then miss the root .gitleaks.toml too.
secrets-check: ## Fail when a secret pattern (private key, token, or high-entropy generic credential) is committed, in the working tree or history
	@$(GO_TOOL) gitleaks dir "$(CURDIR)" --redact --exit-code 1
	@$(GO_TOOL) gitleaks git "$(CURDIR)" --redact --exit-code 1

# osv-scanner defaults --data-source to deps.dev, which is not the native OSV
# data 08-security-and-reliability.md requires; both flags below were confirmed
# against `osv-scanner scan source --help` of the pinned version in
# tools/go.mod. `--lockfile "$(CURDIR)/go.mod"` uses an absolute path rather
# than the bare "go.mod" the recipe used before: `go -C $(GO_TOOLS_DIR) tool
# <name>` runs the tool with its process cwd set to tools/ (Makefile:23-30's
# documented trap), so a relative "go.mod" would resolve against tools/go.mod
# instead of the module this gate is supposed to scan — govulncheck already
# avoids this via its own `-C` flag.
# `rm -f` first: without it, a stale-but-still-matching-digest evidence file
# from BEFORE a new vulnerability was published would let dependency-freshness
# stay green after a subsequent red scan — this scan must actually re-earn its
# evidence every time it runs, not merely leave old evidence in place.
#
# scan_status/policy_decision/findings are computed from the actual scanner
# output, never hardcoded: govulncheck exits 3 when it finds a vulnerability
# reachable through the call graph (0 = clean, anything else is a scanner/
# runtime error and is treated as a hard blocker, never as clean — per
# 06-release.md's "scanner/runtime error... всегда блокируют release");
# osv-scanner exits 1 when its (broader, non-call-graph-scoped) native scan
# finds anything at all (0 = clean, anything else is likewise a hard
# blocker). Either non-clean result — or either tool exiting for a reason
# other than "clean" or "found" — makes the whole gate fail before evidence
# claiming "clean" can ever be written.
dependency-check: ## Run the SCA scan (govulncheck + osv-scanner) fresh and write evidence
	@mkdir -p "$(EVIDENCE_DIR)"
	@rm -f "$(DEPENDENCY_EVIDENCE)"
	@$(GO) mod verify
	@set -eu; \
	  govulncheck_status=0; \
	  $(GO_TOOL) govulncheck -C "$(CURDIR)" ./... > "$(EVIDENCE_DIR)/govulncheck.txt" 2>&1 || govulncheck_status=$$?; \
	  case "$$govulncheck_status" in 0|3) ;; *) printf 'BLOCKED: govulncheck exited %s (scanner/runtime error, not a clean-or-found result); dependency state cannot be trusted\n' "$$govulncheck_status" >&2; cat "$(EVIDENCE_DIR)/govulncheck.txt" >&2; exit 1;; esac; \
	  osv_status=0; \
	  $(GO_TOOL) osv-scanner scan source --lockfile "$(CURDIR)/go.mod" --data-source native --all-vulns --format json > "$(EVIDENCE_DIR)/osv-scanner.json" 2>"$(EVIDENCE_DIR)/osv-scanner.stderr.txt" || osv_status=$$?; \
	  case "$$osv_status" in 0|1) ;; *) printf 'BLOCKED: osv-scanner exited %s (scanner/runtime error, not a clean-or-found result); dependency state cannot be trusted\n' "$$osv_status" >&2; cat "$(EVIDENCE_DIR)/osv-scanner.stderr.txt" >&2; exit 1;; esac; \
	  test -s "$(EVIDENCE_DIR)/govulncheck.txt" -a -s "$(EVIDENCE_DIR)/osv-scanner.json"; \
	  govulncheck_ids=$$(grep -oE 'GO-[0-9]{4}-[0-9]+' "$(EVIDENCE_DIR)/govulncheck.txt" | sort -u | tr '\n' ',' | sed 's/,$$//'); \
	  osv_ids=$$(jq -r '[.results[]?.packages[]?.vulnerabilities[]?.id] | unique | join(",")' "$(EVIDENCE_DIR)/osv-scanner.json"); \
	  all_ids=$$(printf '%s\n%s\n' "$$govulncheck_ids" "$$osv_ids" | tr ',' '\n' | sed '/^$$/d' | sort -u | tr '\n' ',' | sed 's/,$$//'); \
	  finding_count=$$(test -z "$$all_ids" && printf 0 || printf '%s' "$$all_ids" | awk -F, '{print NF}'); \
	  if test "$$govulncheck_status" = 3 -o "$$osv_status" = 1; then status=blocked; else status=clean; fi; \
	  if test "$$finding_count" = 0; then findings=none; else findings="$$finding_count:$$all_ids"; fi; \
	  printf 'schema=release-evidence-v2\ntools=govulncheck,osv-scanner\ntool_versions=%s | %s\nformat=text/plain,application/json\ndatabase=Go vulnerability database | OSV.dev (native)\npolicy=docs/security.md\nscan_status=%s\ncandidate_commit_sha=%s\nplanned_tag=$(TAG)\npolicy_decision=%s\nfindings=%s\nexceptions=none\ninput_digest=%s\nscanned_at=%s\nscanned_at_epoch=%s\ncadence_window_days=$(SCA_WINDOW_DAYS)\nevidence=$(EVIDENCE_DIR)/govulncheck.txt,$(EVIDENCE_DIR)/osv-scanner.json\nevidence_sha256=%s,%s\n' \
	    "$$($(GO_TOOL) govulncheck -version 2>&1 | tr '\n' ' ')" "$$($(GO_TOOL) osv-scanner --version 2>&1 | tr '\n' ' ')" \
	    "$$status" "$$(git rev-parse HEAD)" "$$status" "$$findings" \
	    "$$(cat go.mod go.sum | shasum -a 256 | cut -d ' ' -f 1)" \
	    "$$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$$(date +%s)" \
	    "$$(shasum -a 256 "$(EVIDENCE_DIR)/govulncheck.txt" | cut -d ' ' -f 1)" \
	    "$$(shasum -a 256 "$(EVIDENCE_DIR)/osv-scanner.json" | cut -d ' ' -f 1)" \
	    > "$(DEPENDENCY_EVIDENCE)"; \
	  test "$$status" = clean || { printf 'BLOCKED: dependency-check found %s (see $(DEPENDENCY_EVIDENCE) and $(EVIDENCE_DIR)/govulncheck.txt / osv-scanner.json)\n' "$$findings" >&2; exit 1; }

# Cadence staleness gate: cheap, runs inside `check` and `release-check`. It
# never re-scans — it refuses to go green on evidence that is missing, stale,
# or describes a different dependency state than the committed one.
# REQUIRE_CANDIDATE_COMMIT=1 (set by release-check's sub-make invocation, not
# by plain `check`) additionally requires the evidence's candidate_commit_sha
# to be exactly the current HEAD: 06-release.md's release-check gate MUST
# reject evidence "относящееся к другому кандидату", but the ordinary dev-loop
# `check` gate deliberately tolerates evidence scanned on a recent-but-earlier
# commit — that decoupling from per-commit SHA is the whole point of the
# 30-day cadence window below.
dependency-freshness: ## Fail fast when SCA evidence is missing, stale, red, or out of date with go.mod/go.sum
	@test -s "$(DEPENDENCY_EVIDENCE)" || { printf '%s\n' 'BLOCKED: no SCA evidence at $(DEPENDENCY_EVIDENCE); missing evidence is a blocker, not a first run — run: make dependency-check' >&2; exit 1; }
	@grep -Fxq 'scan_status=clean' "$(DEPENDENCY_EVIDENCE)" || { printf '%s\n' 'BLOCKED: last SCA run did not end clean; re-run make dependency-check' >&2; exit 1; }
	@grep -Fxq "input_digest=$$(cat go.mod go.sum | shasum -a 256 | cut -d ' ' -f 1)" "$(DEPENDENCY_EVIDENCE)" || { printf '%s\n' 'BLOCKED: SCA evidence describes a different dependency state than the committed go.mod/go.sum; run: make dependency-check' >&2; exit 1; }
	@test -z "$(REQUIRE_CANDIDATE_COMMIT)" || { sha=$$(sed -n 's/^candidate_commit_sha=//p' "$(DEPENDENCY_EVIDENCE)"); head=$$(git rev-parse HEAD); test "$$sha" = "$$head" || { printf 'BLOCKED: SCA evidence candidate_commit_sha=%s does not match candidate HEAD %s; this evidence describes a different release candidate — run: make dependency-check\n' "$$sha" "$$head" >&2; exit 1; }; }
	@scanned=$$(sed -n 's/^scanned_at_epoch=//p' "$(DEPENDENCY_EVIDENCE)"); test -n "$$scanned" || { printf '%s\n' 'BLOCKED: SCA evidence has no scanned_at_epoch; run: make dependency-check' >&2; exit 1; }; age=$$(( $$(date +%s) - scanned )); window=$$(( $(SCA_WINDOW_DAYS) * 86400 )); test "$$age" -le "$$window" || { printf 'BLOCKED: SCA evidence is %s days old, cadence window is $(SCA_WINDOW_DAYS) days; run: make dependency-check\n' "$$(( age / 86400 ))" >&2; exit 1; }; printf 'dependency-freshness: age %s days, window $(SCA_WINDOW_DAYS) days, evidence $(DEPENDENCY_EVIDENCE), scan_status=clean\n' "$$(( age / 86400 ))"

security: secrets-check dependency-check ## Run every security gate (secrets and a fresh SCA scan)

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

check-onboarding: ## Validate the README onboarding record (required fields and 90-day freshness)
	@bash scripts/onboarding-record-check.sh

# Pre-tag gate only (06-release.md:117-124, :227-235): a network call to the
# GitHub API on every ordinary `make check` would be excessive for the normal
# dev loop, so this is wired into `release-check`, not `check`.
tag-protection-check: ## Verify the GitHub tag-protection ruleset via `gh api` and write machine-readable evidence (pre-tag gate only)
	@bash scripts/tag-protection-check.sh

whats-new: ## Materialize the release notes for VERSION from CHANGELOG.md
	@notes=$$(awk -v want='## [$(VERSION)]' '$$0 == want { inside = 1; next } /^## /{ inside = 0 } inside' CHANGELOG.md); \
		test -n "$$(printf '%s' "$$notes" | tr -d '[:space:]')" || { printf '%s\n' 'whats-new: CHANGELOG.md has no non-empty "## [$(VERSION)]" section' >&2; exit 1; }; \
		printf '%s\n' "$$notes"

# Externalized to scripts/release-check-summary.sh (not a plain sequence of
# recipe lines) because 06-release.md:408-412 requires a machine-readable
# phase summary — .task/release-evidence/release-check.json — that MUST be
# written even when a gate fails partway through; a Makefile recipe stops at
# its first non-zero line and would leave no evidence of a failed run.
release-check: ## Pre-tag release completeness gate on the candidate commit (make release-check VERSION=X.Y.Z)
	@test '$(VERSION)' != dev || { printf '%s\n' 'release-check: pass the candidate release version, e.g. make release-check VERSION=0.1.19' >&2; exit 1; }
	@VERSION='$(VERSION)' TAG='$(TAG)' MAKE='$(MAKE)' EVIDENCE_DIR='$(EVIDENCE_DIR)' bash scripts/release-check-summary.sh

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

install-scoping-test: ## Prove install-local never touches unrelated files in a shared PREFIX
	@MAKE='$(MAKE)' VERSION='$(VERSION)' TAG='$(TAG)' bash scripts/install-prefix-isolation-test.sh

install-local-smoke: check-local-tag ## Package and install the exact tag into a disposable PREFIX and verify it
	@prefix=$$(mktemp -d); dist=$$(mktemp -d); trap 'rm -rf "$$prefix" "$$dist"' EXIT; $(MAKE) --no-print-directory package-local DIST="$$dist" VERSION="$(VERSION)" TAG="$(TAG)"; $(MAKE) --no-print-directory install-local PREFIX="$$prefix" DIST="$$dist" VERSION="$(VERSION)" TAG="$(TAG)"; $(MAKE) --no-print-directory verify-local-install PREFIX="$$prefix" VERSION="$(VERSION)"

release-local: check-local-tag ## Write the offline release bundle for the exact tag into dist/local-release/<version>
	@set -eu; release_dir="$(LOCAL_RELEASE_DIR)"; archive="$(LOCAL_RELEASE_ARCHIVE)"; mkdir -p "$$release_dir"; git archive --format=tar.gz --prefix="uni-chat-sdk-$(VERSION)/" "$(TAG)" -o "$$archive"; (cd "$$release_dir" && shasum -a 256 "$$(basename "$$archive")" > SHA256SUMS); sha256=$$(cut -d ' ' -f 1 "$$release_dir/SHA256SUMS"); commit=$$(git rev-parse "$(TAG)^{commit}"); printf '{\n  "version": "$(VERSION)",\n  "tag": "$(TAG)",\n  "commit": "%s",\n  "archive": "%s",\n  "sha256": "%s"\n}\n' "$$commit" "$$(basename "$$archive")" "$$sha256" > "$$release_dir/metadata.json"; git show "$(TAG):CHANGELOG.md" > "$$release_dir/RELEASE_NOTES.md"; printf 'Local release written to %s\n' "$$release_dir"

local-release: release-local ## Alias for release-local

# 05-build-test-docs.md:71-79: a tag-only release profile fixes the
# published-source part of verify-release as N/A with rationale ONLY when no
# post-tag check remains applicable at all — and one does remain here
# (install-local-smoke), so the target itself MUST exist rather than being
# `N/A` in its entirety (the state this repo was in before this fix: README's
# `Makefile targets` row listed verify-release under "Отсутствуют как N/A",
# which contradicted its own smoke-check paragraph one row below).
verify-release: check-local-tag install-local-smoke ## Post-tag verification: install-local-smoke (the one applicable post-tag check); published-source part is explicitly not-applicable
	@mkdir -p "$(EVIDENCE_DIR)"
	@commit=$$(git rev-parse "$(TAG)^{commit}"); \
	jq -n --arg schema 'verify-release-summary-v1' --arg phase post-tag --arg tag '$(TAG)' --arg version '$(VERSION)' --arg commit "$$commit" \
	  --arg pub_status not-applicable \
	  --arg pub_rationale 'tag-only release profile: README.md channels lists only an exact immutable git tag and the offline make release-local bundle, no published source/assets/formula channel exists, so there is nothing beyond the tag itself to verify as "published" — check-local-tag (a prerequisite of this target) already proved the exact annotated tag is on HEAD' \
	  --arg checked_at "$$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	  '{schema:$$schema, phase:$$phase, tag:$$tag, version:$$version, commit:$$commit, published_source:{status:$$pub_status, rationale:$$pub_rationale}, install_local_smoke:{status:"pass"}, checked_at:$$checked_at}' \
	  > "$(EVIDENCE_DIR)/verify-release.json"; \
	printf 'verify-release OK: tag %s at commit %s; install-local-smoke passed; published-source status=not-applicable (tag-only release profile)\n' '$(TAG)' "$$commit"

check: check-env check-version check-onboarding format lint vet build test test-acceptance race coverage test-keychain-seam cross-build secrets-check dependency-freshness install-scoping-test ## Run every local gate
