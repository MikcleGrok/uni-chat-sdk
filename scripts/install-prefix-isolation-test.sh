#!/usr/bin/env bash
# Regression test for the shared-location scoping rule in
# guide-tools/00-overview.md ("Границы владения и общие каталоги"): an
# operation that writes into a shared location MUST touch only the paths
# it owns, MUST NOT directory-swap or recursively delete the location
# itself, and MUST prove that property with a test rather than an
# argument.
#
# `install-local` writes into $PREFIX, which defaults to $HOME/.local --
# a genuinely shared prefix holding other tools' binaries and data. It is
# structurally safe today because it only ever creates and replaces the
# single subtree it owns, share/uni-chat-sdk/<version>. Nothing proved
# that, which is what this script does.
#
# It seeds a disposable prefix with sentinel files that stand in for an
# unrelated tool sharing the prefix (plus a previously installed version
# of this module, which an upgrade must also leave alone), runs the real
# `make install-local` twice -- first install, then upgrade in place --
# and asserts every sentinel survives byte-identical, with its mtime
# untouched, and that the prefix directory itself was never swapped for a
# freshly built one.
#
# Run directly:      bash scripts/install-prefix-isolation-test.sh
# Through the gate:  make install-scoping-test
#
# To prove this test actually catches the regression it guards against,
# change install-local's first line to `rm -rf "$(PREFIX)"` (the
# whole-prefix wipe that hit uni-chat) or make it stage a fresh directory
# and rename it over $(PREFIX), and rerun: it reports FAIL on the missing
# sentinel or the swapped prefix inode. Restore the recipe and it passes.

set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

MAKE_BIN="${MAKE:-make}"
VERSION="${VERSION:-$(git describe --exact-match --tags HEAD 2>/dev/null | sed 's/^v//' || printf dev)}"
TAG="${TAG:-v$VERSION}"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

file_mtime() { stat -c '%Y' "$1" 2>/dev/null || stat -f '%m' "$1"; }
file_hash() { shasum -a 256 "$1" | cut -d ' ' -f1; }
inode_of() { stat -c '%i' "$1" 2>/dev/null || stat -f '%i' "$1"; }

tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT HUP INT TERM

prefix="$tmp_root/prefix"
dist="$tmp_root/dist"
mkdir -p "$prefix" "$dist"

# Sentinels: two files belonging to an unrelated tool that happens to
# share the prefix, and one file from an earlier install of this module,
# which an upgrade to a different version must also leave in place.
sentinels=(
  bin/some-other-tool
  share/unrelated-tool/versions/9.9.9/thing.txt
  share/uni-chat-sdk/0.0.0-previous/go.mod
)

mkdir -p \
  "$prefix/bin" \
  "$prefix/share/unrelated-tool/versions/9.9.9" \
  "$prefix/share/uni-chat-sdk/0.0.0-previous"
printf '#!/bin/sh\necho not uni-chat-sdk\n' >"$prefix/bin/some-other-tool"
chmod 755 "$prefix/bin/some-other-tool"
printf 'unrelated payload, not uni-chat-sdk managed\n' >"$prefix/share/unrelated-tool/versions/9.9.9/thing.txt"
printf 'module example.com/previously-installed\n' >"$prefix/share/uni-chat-sdk/0.0.0-previous/go.mod"

hashes=()
mtimes=()
for rel in "${sentinels[@]}"; do
  hashes+=("$(file_hash "$prefix/$rel")")
  mtimes+=("$(file_mtime "$prefix/$rel")")
done
prefix_inode="$(inode_of "$prefix")"

# mtime has one-second granularity on some filesystems, so make sure any
# rewrite performed after this point would be visible as a changed mtime.
sleep 1

assert_intact() {
  local label="$1" i=0 rel path hash mtime
  for rel in "${sentinels[@]}"; do
    path="$prefix/$rel"
    test -e "$path" || fail "$label: sentinel $rel is gone"
    hash="$(file_hash "$path")"
    mtime="$(file_mtime "$path")"
    test "$hash" = "${hashes[$i]}" ||
      fail "$label: sentinel $rel content changed (${hashes[$i]} -> $hash)"
    test "$mtime" = "${mtimes[$i]}" ||
      fail "$label: sentinel $rel mtime changed (${mtimes[$i]} -> $mtime) -- it was rewritten even though its content matches"
    i=$((i + 1))
  done
  test "$(inode_of "$prefix")" = "$prefix_inode" ||
    fail "$label: PREFIX itself was replaced (inode $prefix_inode -> $(inode_of "$prefix")) -- a directory swap, not a scoped write"
}

install_once() {
  "$MAKE_BIN" --no-print-directory install-local \
    PREFIX="$prefix" DIST="$dist" VERSION="$VERSION" TAG="$TAG" >/dev/null
}

install_once
test -f "$prefix/share/uni-chat-sdk/$VERSION/go.mod" ||
  fail "first install: install-local did not populate share/uni-chat-sdk/$VERSION"
assert_intact 'after first install'
printf '%s\n' "PASS: first install into a shared prefix left every unrelated file untouched"

install_once
"$MAKE_BIN" --no-print-directory verify-local-install \
  PREFIX="$prefix" VERSION="$VERSION" >/dev/null
assert_intact 'after upgrade in place'
printf '%s\n' "PASS: re-install (upgrade in place) left every unrelated file untouched"

printf '%s\n' "PASS: install-prefix-isolation-test (install-local owns only share/uni-chat-sdk/<version> and touches nothing else in PREFIX)"
