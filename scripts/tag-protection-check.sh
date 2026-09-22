#!/usr/bin/env bash
# Machine-readable evidence for the repository's GitHub tag-protection
# ruleset, required by guide-tools/06-release.md:227-235 ("ещё до tag `make
# release-check` MUST получить machine-readable evidence repository tag
# policy — canonical remote, tag pattern, запрет force-update/delete,
# required permissions, результат проверки и timestamp <...> Невозможность
# проверить любое поле policy блокирует release. Repository policy MUST
# запрещать force-update и delete release tags.").
#
# Reads the ruleset through `gh api` (never git-describes or guesses it from
# README text — a declaration without a checkable source is explicitly not
# evidence per the same section) and writes
# .task/release-evidence/tag-protection.properties. Exits non-zero, with the
# exact missing/failing field named, if: gh is unavailable/unauthenticated,
# the repository has no ruleset targeting tags, no such ruleset is actively
# enforced and covers this project's tag pattern, or the covering ruleset
# does not forbid both deletion and force-update (non-fast-forward update).
#
# Run directly:      bash scripts/tag-protection-check.sh
# Through the gate:   make tag-protection-check

set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

evidence_dir="${EVIDENCE_DIR:-.task/release-evidence}"
evidence_file="$evidence_dir/tag-protection.properties"
# The exact pattern this project's release tags match — vMAJOR.MINOR.PATCH
# with an optional SemVer prerelease (see check-version's own regex) all
# start with "v", so "refs/tags/v*" is the tag-protection policy's scope.
tag_pattern="${TAG_PATTERN:-refs/tags/v*}"

fail() {
  printf 'BLOCKED: tag-protection-check: %s\n' "$*" >&2
  exit 1
}

command -v gh >/dev/null 2>&1 || fail "gh CLI is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"
gh auth status >/dev/null 2>&1 || fail "gh is not authenticated (run: gh auth login)"

remote_url=$(git remote get-url origin 2>/dev/null) || fail "no 'origin' remote configured"
# Normalize an ssh remote (git@github.com:owner/repo.git) to the canonical
# https form, so evidence is comparable regardless of the local clone's
# chosen protocol.
case "$remote_url" in
  git@github.com:*) remote_url="https://github.com/${remote_url#git@github.com:}" ;;
esac
case "$remote_url" in
  https://github.com/*) ;;
  *) fail "origin remote '$remote_url' is not a github.com remote — this evidence mechanism (gh api rulesets) is GitHub-specific" ;;
esac

owner_repo=${remote_url#https://github.com/}
owner_repo=${owner_repo%.git}
test -n "$owner_repo" || fail "could not resolve owner/repo from remote '$remote_url'"

rulesets_json=$(gh api "repos/$owner_repo/rulesets" 2>&1) || fail "gh api repos/$owner_repo/rulesets failed: $rulesets_json"

candidate_ids=$(printf '%s' "$rulesets_json" | jq -r '[.[] | select(.target == "tag")] | .[].id')
test -n "$candidate_ids" || fail "repository $owner_repo has no ruleset targeting tags at all — tag policy does not exist"

selected_json=""
selected_id=""
for id in $candidate_ids; do
  detail=$(gh api "repos/$owner_repo/rulesets/$id" 2>&1) || fail "gh api repos/$owner_repo/rulesets/$id failed: $detail"
  covers=$(printf '%s' "$detail" | jq -r --arg pattern "$tag_pattern" '
    (.enforcement == "active") and
    (([.conditions.ref_name.include // []] | flatten | index($pattern)) != null) and
    (([.rules[]?.type] | index("deletion")) != null) and
    (([.rules[]?.type] | index("non_fast_forward")) != null)
  ')
  if [ "$covers" = true ]; then
    selected_json="$detail"
    selected_id="$id"
    break
  fi
done

if [ -z "$selected_json" ]; then
  fail "no actively-enforced ruleset on $owner_repo covers tag pattern '$tag_pattern' with both a 'deletion' and a 'non_fast_forward' rule (checked ruleset ids: $(printf '%s' "$candidate_ids" | tr '\n' ' '))"
fi

ruleset_name=$(printf '%s' "$selected_json" | jq -r '.name // empty')
test -n "$ruleset_name" || fail "ruleset $selected_id has no readable name"

bypass_count=$(printf '%s' "$selected_json" | jq -r '.bypass_actors | length')
current_user_can_bypass=$(printf '%s' "$selected_json" | jq -r '.current_user_can_bypass // "unknown"')
if [ "$bypass_count" -eq 0 ]; then
  required_permissions="none (bypass_actors is empty; current_user_can_bypass=$current_user_can_bypass)"
else
  required_permissions=$(printf '%s' "$selected_json" | jq -r '[.bypass_actors[] | "\(.actor_type):\(.actor_id // "n/a"):\(.bypass_mode)"] | join(",")')
fi
test -n "$required_permissions" || fail "could not resolve required_permissions (bypass_actors) from ruleset $selected_id"

mkdir -p "$evidence_dir"
checked_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
checked_at_epoch=$(date +%s)

{
  printf 'schema=tag-protection-evidence-v1\n'
  printf 'canonical_remote=%s\n' "$remote_url"
  printf 'tag_pattern=%s\n' "$tag_pattern"
  printf 'forbids_force_update=true\n'
  printf 'forbids_delete=true\n'
  printf 'required_permissions=%s\n' "$required_permissions"
  printf 'ruleset_id=%s\n' "$selected_id"
  printf 'ruleset_name=%s\n' "$ruleset_name"
  printf 'check_result=pass\n'
  printf 'checked_at=%s\n' "$checked_at"
  printf 'checked_at_epoch=%s\n' "$checked_at_epoch"
} > "$evidence_file"

printf 'tag-protection-check OK: ruleset "%s" (id=%s) on %s forbids force-update and delete for %s; required_permissions=%s\n' \
  "$ruleset_name" "$selected_id" "$owner_repo" "$tag_pattern" "$required_permissions"
