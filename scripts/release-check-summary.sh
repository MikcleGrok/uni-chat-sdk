#!/usr/bin/env bash
# release-check's own recipe, externalized to a script. guide-tools/
# 06-release.md:408-412 requires a machine-readable phase summary containing
# "phase, target tag/version, target commit, exact command/Makefile target,
# exit code, stdout, stderr, source, artifact, installed version, notes
# source и blocker/resume point" — and it MUST be written on failure too, not
# only on success. A plain Makefile recipe (the previous shape of
# `release-check`) stops at the first non-zero line, so a failing gate left
# no evidence at all of what failed or where to resume; this script runs the
# same sequence itself, captures the first failure without aborting, and
# always writes .task/release-evidence/release-check.json before exiting
# with the real result code.
#
# Usage:      VERSION=X.Y.Z bash scripts/release-check-summary.sh
# Through the gate: make release-check VERSION=X.Y.Z

set -uo pipefail # deliberately not -e: failures are captured, not fatal here

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

VERSION="${VERSION:?VERSION is required, e.g. VERSION=0.1.20}"
TAG="${TAG:-v$VERSION}"
MAKE_BIN="${MAKE:-make}"
EVIDENCE_DIR="${EVIDENCE_DIR:-.task/release-evidence}"
SUMMARY_FILE="$EVIDENCE_DIR/release-check.json"
MAKEFILE_TARGET="release-check"
COMMAND="make release-check VERSION=$VERSION"
# Secrets/tokens MUST NOT be persisted in this summary (06-release.md:408-412,
# "Secrets/tokens redacted и не сохраняются"): rather than re-running gitleaks
# over ad hoc captured command output, this script takes the simpler
# documented alternative the fix plan allows — a fixed-size tail — which
# bounds the file size and avoids ever writing a secret's neighboring context
# verbatim across a scan boundary.
CAPTURE_TAIL_BYTES=4000
mkdir -p "$EVIDENCE_DIR"

candidate_sha=$(git rev-parse HEAD 2>/dev/null || printf 'unknown')
module_path=$(awk '/^module /{print $2; exit}' go.mod 2>/dev/null || printf 'unknown')
source_id="$module_path@$candidate_sha"

stdout_file=$(mktemp)
stderr_file=$(mktemp)
trap 'rm -f "$stdout_file" "$stderr_file"' EXIT

stopped=0
blocker=""
resume_point=""
exit_code=0

run() {
  local label="$1"
  shift
  if [ "$stopped" -eq 1 ]; then
    return 0
  fi
  printf '\n==> %s\n' "$label" >>"$stdout_file"
  if ! "$@" >>"$stdout_file" 2>>"$stderr_file"; then
    exit_code=$?
    blocker="$label failed (exit $exit_code)"
    resume_point="$label"
    stopped=1
  fi
}

step_version_not_dev() { test "$VERSION" != dev; }
step_clean_tree() { test -z "$(git status --porcelain --untracked-files=all)"; }
step_check_version() { "$MAKE_BIN" --no-print-directory check-version VERSION="$VERSION" TAG="$TAG"; }
step_tag_not_conflicting() {
  if git rev-parse --verify --quiet "refs/tags/$TAG" >/dev/null 2>&1; then
    test "$(git rev-parse "$TAG^{commit}")" = "$candidate_sha"
  fi
}

notes_text=""
step_whats_new() {
  notes_text=$("$MAKE_BIN" --no-print-directory whats-new VERSION="$VERSION" 2>&1)
  local rc=$?
  printf '%s\n' "$notes_text"
  return "$rc"
}

step_gates() {
  "$MAKE_BIN" --no-print-directory check-env check-onboarding format lint vet build test test-acceptance race \
    test-keychain-seam test-keychain-native cross-build secrets-check dependency-check dependency-freshness \
    tag-protection-check install-scoping-test REQUIRE_CANDIDATE_COMMIT=1 VERSION="$VERSION" TAG="$TAG"
}

run "candidate version must not be dev" step_version_not_dev
run "clean tree required" step_clean_tree
run "check-version" step_check_version
run "planned tag $TAG must not already point at a different commit" step_tag_not_conflicting
run "whats-new (CHANGELOG.md release notes for $VERSION)" step_whats_new
run "gates: check-env check-onboarding format lint vet build test test-acceptance race test-keychain-seam test-keychain-native cross-build secrets-check dependency-check dependency-freshness tag-protection-check install-scoping-test" step_gates

# notes_digest: sha256 over exactly the text `make whats-new VERSION=$VERSION`
# prints (the same materialized-notes call whats-new itself makes — reused
# here rather than re-deriving the CHANGELOG.md section with a second awk, so
# there is exactly one place that knows how to extract it). Command
# substitution strips trailing newlines from $notes_text before hashing; that
# normalization is part of this digest's documented algorithm, not an
# incidental accident — the digest is this script's own identity for the
# notes content, not a general-purpose file checksum.
if [ -n "$notes_text" ]; then
  notes_digest="sha256:$(printf '%s' "$notes_text" | shasum -a 256 | cut -d ' ' -f1)"
else
  notes_digest="sha256:$(printf '' | shasum -a 256 | cut -d ' ' -f1)"
fi

stdout_tail=$(tail -c "$CAPTURE_TAIL_BYTES" "$stdout_file")
stderr_tail=$(tail -c "$CAPTURE_TAIL_BYTES" "$stderr_file")

checked_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)

jq -n \
  --arg schema "release-check-summary-v1" \
  --arg phase "pre-tag" \
  --arg target_tag "$TAG" \
  --arg target_version "$VERSION" \
  --arg target_commit "$candidate_sha" \
  --arg makefile_target "$MAKEFILE_TARGET" \
  --arg command "$COMMAND" \
  --argjson exit_code "$exit_code" \
  --arg stdout "$stdout_tail" \
  --arg stderr "$stderr_tail" \
  --arg source "$source_id" \
  --arg artifact "N/A — непубликуемый профиль: библиотека не собирает и не публикует release-артефакт (tag-only release, см. README.md channels)" \
  --arg installed_version "N/A — библиотека не устанавливается как бинарник; make install-local устанавливает исходники в \$(PREFIX)/share, что не является установленной версией продукта" \
  --arg notes_source "CHANGELOG.md#[$VERSION]" \
  --arg notes_digest "$notes_digest" \
  --arg blocker "$blocker" \
  --arg resume_point "$resume_point" \
  --arg checked_at "$checked_at" \
  '{
    schema: $schema,
    phase: $phase,
    target_tag: $target_tag,
    target_version: $target_version,
    target_commit: $target_commit,
    makefile_target: $makefile_target,
    command: $command,
    exit_code: $exit_code,
    stdout: $stdout,
    stderr: $stderr,
    source: $source,
    artifact: $artifact,
    installed_version: $installed_version,
    notes_source: $notes_source,
    notes_digest: $notes_digest,
    blocker: (if $blocker == "" then null else $blocker end),
    resume_point: (if $resume_point == "" then null else $resume_point end),
    checked_at: $checked_at
  }' >"$SUMMARY_FILE"

if [ "$exit_code" -eq 0 ]; then
  printf 'release-check OK: candidate %s, planned tag %s, normalized version %s\n' "$candidate_sha" "$TAG" "$VERSION"
else
  printf 'release-check: BLOCKED at "%s" (exit %s) — see %s and resume from there\n' "$resume_point" "$exit_code" "$SUMMARY_FILE" >&2
fi

exit "$exit_code"
