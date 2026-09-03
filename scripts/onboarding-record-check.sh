#!/usr/bin/env bash
# Machine-checkable validation of the canonical onboarding record in
# README.md, required by guide-tools/README.md ("Onboarding record"):
# every mandatory field must be present and filled, an N/A value must
# carry a rationale rather than the bare marker, and the record goes
# stale 90 days after `last reviewed`.
#
# Run directly:     bash scripts/onboarding-record-check.sh
# Through the gate: make check-onboarding

set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

readme=README.md
max_age_days=90

fields=(
  'project type'
  'profiles'
  'OS/ARCH'
  'modes'
  'channels'
  'version source'
  'Makefile targets'
  'Docker toolchain image'
  'Docker runtime image'
  'Docker runtime base image'
  'host-only exceptions'
  'shared-location scoping'
  'owners'
  'SCA cadence'
  'SCA owner'
  'remediation deadline'
  'N/A controls/rationale'
  'last reviewed'
  'review trigger/profile state'
)

fail() {
  printf 'FAIL: onboarding record: %s\n' "$*" >&2
  exit 1
}

test -f "$readme" || fail "$readme is missing"

value_of() {
  awk -F'|' -v want="$1" '
    NF < 3 { next }
    {
      name = $2
      gsub(/`/, "", name)
      gsub(/^[ \t]+|[ \t]+$/, "", name)
      if (name != want) next
      value = $3
      gsub(/^[ \t]+|[ \t]+$/, "", value)
      print value
      exit
    }
  ' "$readme"
}

for field in "${fields[@]}"; do
  value="$(value_of "$field")"
  test -n "$value" || fail "the field \`$field\` is missing or empty"
  case "$value" in
    'N/A' | 'N/A.' | '`N/A`' | '`N/A`.')
      fail "the field \`$field\` is a bare N/A with no rationale"
      ;;
  esac
  if printf '%s' "$value" | grep -q 'N/A' && test "${#value}" -lt 40; then
    fail "the field \`$field\` records N/A without a usable rationale: $value"
  fi
done

reviewed="$(value_of 'last reviewed')"
printf '%s' "$reviewed" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' ||
  fail "\`last reviewed\` must be an ISO-8601 date (YYYY-MM-DD), found: $reviewed"

epoch_of() {
  date -j -f '%Y-%m-%d' "$1" '+%s' 2>/dev/null || date -d "$1" '+%s'
}

reviewed_epoch="$(epoch_of "$reviewed")"
now_epoch="$(date '+%s')"
age_days=$(((now_epoch - reviewed_epoch) / 86400))

test "$age_days" -ge 0 ||
  fail "\`last reviewed\` ($reviewed) is in the future"
test "$age_days" -le "$max_age_days" ||
  fail "the record is stale: last reviewed $reviewed, $age_days days ago, limit $max_age_days"

printf 'check-onboarding OK: %s fields present, last reviewed %s (%s days ago, limit %s)\n' \
  "${#fields[@]}" "$reviewed" "$age_days" "$max_age_days"
