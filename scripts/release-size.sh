#!/usr/bin/env bash
# Describe what is unreleased between the last release and a ref, as
# key=value lines for $GITHUB_OUTPUT.
#
#   scripts/release-size.sh [ref] [manifest-ref]     (both default HEAD)
#
#   last_tag   the last release, from .release-please-manifest.json at
#              manifest-ref. On the release PR itself the manifest already
#              holds the next version, so pass the base branch there.
#   days       whole days since that tag was made
#   changes    user-facing commits: feat, fix, perf, revert, minus CI-only scopes
#   feats      how many of those are feat
#   breaking   true if any commit has `!` or a BREAKING CHANGE footer
#   contract   true if a public contract changed (see contract_changed)
#   size       none; small (fixes only); medium (has a feat); large (over 20
#              changes, or a contract changed); major (breaking)
#
# Needs the full history and tags (fetch-depth: 0).
set -euo pipefail

REF="${1:-HEAD}"
MANIFEST_REF="${2:-$REF}"
# A change that only users of the repo itself can see does not make a release.
USER_FACING='^(feat|fix|perf|revert)(\((.+)\))?!?: '
NOT_USER_SCOPE='^(feat|fix|perf|revert)\((ci|site|blog|release|mergify)\)!?: '
# Installers, config and policy schema, and the serve HTTP API.
CONTRACT_PATHS='^(install\.sh|install\.ps1|install-app\.sh|npm/|pip/|internal/config/|internal/policy/|internal/serve/)'
LARGE_AT=20

last="$(git show "$MANIFEST_REF:.release-please-manifest.json" | jq -r '."."')"
last_tag="v$last"
git rev-parse -q --verify "refs/tags/$last_tag" >/dev/null || {
  echo "release-size: tag $last_tag (from the manifest) does not exist" >&2
  exit 1
}

# One line per commit: hash, then subject.
user="$(git log --no-merges --format='%H %s' "$last_tag..$REF" |
  awk '{ s = substr($0, 42) } s ~ /'"$USER_FACING"'/ && s !~ /'"$NOT_USER_SCOPE"'/' || true)"
changes="$(printf '%s' "$user" | grep -c . || true)"
feats="$(printf '%s' "$user" | cut -c42- | grep -cE '^feat' || true)"

breaking=false
while read -r sha subject; do
  [ -n "$sha" ] || continue
  # Captured first: grep -q closing a pipe early fails it under pipefail.
  body="$(git log -1 --format=%b "$sha")"
  if grep -qE '^[a-z]+(\(.+\))?!: ' <<<"$subject" ||
    grep -qE '^BREAKING[ -]CHANGE:' <<<"$body"; then
    breaking=true
    break
  fi
done <<<"$user"

# The minor of a version string: go1.26.11 -> 1.26, v2.17.1 -> 2.17.
minor() { grep -oE '[0-9]+\.[0-9]+' <<<"$1" | head -1; }
dep() { git show "$1:$2" 2>/dev/null | grep -E "$3" | head -1 || true; }

contract_changed() {
  grep -qE "$CONTRACT_PATHS" <<<"$(git diff --name-only "$last_tag" "$REF")" && return 0
  # A new Go toolchain minor or Wails minor changes what users run.
  local pair
  for pair in "go.mod|^toolchain " "desktop/go.mod|wailsapp/wails/v2 "; do
    local file="${pair%%|*}" pat="${pair#*|}"
    [ "$(minor "$(dep "$last_tag" "$file" "$pat")")" != "$(minor "$(dep "$REF" "$file" "$pat")")" ] && return 0
  done
  return 1
}
contract=false
if [ "$changes" -gt 0 ] && contract_changed; then contract=true; fi

if [ "$changes" -eq 0 ]; then
  size=none
elif [ "$breaking" = true ]; then
  size=major
elif [ "$changes" -gt "$LARGE_AT" ] || [ "$contract" = true ]; then
  size=large
elif [ "$feats" -gt 0 ]; then
  size=medium
else
  size=small
fi

tagged="$(git for-each-ref --format='%(creatordate:unix)' "refs/tags/$last_tag")"
days=$(( ($(date +%s) - tagged) / 86400 ))

printf '%s\n' "last_tag=$last_tag" "days=$days" "changes=$changes" "feats=$feats" \
  "breaking=$breaking" "contract=$contract" "size=$size"
