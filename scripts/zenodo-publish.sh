#!/usr/bin/env bash
# Archive one stable release on Zenodo as a new version of hydra's record.
#
#   ZENODO_TOKEN=... [ZENODO_CONCEPT_ID=...] scripts/zenodo-publish.sh v1.7.0
#
# Run from a checkout of the tag. Uploads `git archive HEAD` with the metadata
# in .zenodo.json, then publishes. Safe to re-run: a version Zenodo already has
# is skipped, and a draft left by a failed run is reused.
#
# ZENODO_CONCEPT_ID is the concept record id (the number in the concept DOI,
# 10.5281/zenodo.<id>), kept as a repository variable. Unset, the script
# creates the first record and prints the id to store.
#
# ZENODO_URL defaults to https://zenodo.org; https://sandbox.zenodo.org takes
# a sandbox token for a trial run.
set -euo pipefail

TAG="${1:?usage: zenodo-publish.sh vX.Y.Z}"
VER="${TAG#v}"
: "${ZENODO_TOKEN:?ZENODO_TOKEN is not set}"
CONCEPT="${ZENODO_CONCEPT_ID:-}"
URL="${ZENODO_URL:-https://zenodo.org}"
REPO="ankit373/hydra"

if ! printf '%s' "$TAG" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "::error::$TAG is not a stable vX.Y.Z tag; only stable releases go to Zenodo" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# call METHOD URL [curl args...]: prints the body, fails on any non-2xx.
call() {
  local method="$1" url="$2" code
  shift 2
  code=$(curl -sS -L -o "$work/body" -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer $ZENODO_TOKEN" "$@" "$url")
  if [ "${code:0:1}" != 2 ]; then
    echo "::error::Zenodo $method $url returned $code" >&2
    cat "$work/body" >&2
    echo >&2
    return 1
  fi
  cat "$work/body"
}

json() { call "$@" -H "Content-Type: application/json"; }

# Metadata for this version. An ORCID that is not a real iD (the placeholder in
# .zenodo.json) is dropped, since Zenodo rejects the whole request over it.
jq --arg v "$VER" --arg date "$(git log -1 --format=%cs HEAD)" \
  --arg tree "https://github.com/$REPO/tree/$TAG" '
  .creators |= map(if (.orcid // "" | test("^[0-9]{4}-[0-9]{4}-[0-9]{4}-[0-9]{3}[0-9X]$"))
                   then . else del(.orcid) end)
  | .version = $v
  | .publication_date = $date
  | .related_identifiers = [{identifier: $tree, relation: "isSupplementTo", resource_type: "software"}]
  | {metadata: .}' .zenodo.json >"$work/metadata.json"
title="$(jq -r .metadata.title "$work/metadata.json")"

if [ -n "$CONCEPT" ]; then
  latest="$(call GET "$URL/api/records/$CONCEPT/versions/latest")"
  if [ "$(jq -r .conceptrecid <<<"$latest")" != "$CONCEPT" ]; then
    echo "::error::record $CONCEPT is not a concept record; check ZENODO_CONCEPT_ID" >&2
    exit 1
  fi
  if [ "$(jq -r .metadata.version <<<"$latest")" = "$VER" ]; then
    echo "Zenodo already has $VER: $(jq -r .links.html <<<"$latest")"
    exit 0
  fi

  draft_id="$(call GET "$URL/api/deposit/depositions?status=draft&size=100" |
    jq -r --arg c "$CONCEPT" '[.[] | select((.conceptrecid | tostring) == $c)][0].id // empty')"
  if [ -n "$draft_id" ]; then
    echo "Reusing draft $draft_id left by an earlier run."
  else
    draft_url="$(json POST "$URL/api/deposit/depositions/$(jq -r .id <<<"$latest")/actions/newversion" |
      jq -r .links.latest_draft)"
    draft_id="${draft_url##*/}"
  fi
  draft="$(call GET "$URL/api/deposit/depositions/$draft_id")"

  # A new version starts with a copy of the previous version's files.
  for fid in $(jq -r '.files[]?.id' <<<"$draft"); do
    call DELETE "$URL/api/deposit/depositions/$draft_id/files/$fid" >/dev/null
  done
else
  # With no concept id, a second "first" record would split the citation
  # count. Refuse if this account already has a record with this title.
  existing="$(call GET "$URL/api/deposit/depositions?status=published&size=100" |
    jq -r --arg t "$title" '[.[] | select(.title == $t)][0].conceptrecid // empty')"
  if [ -n "$existing" ]; then
    echo "::error::a record titled \"$title\" already exists; set the ZENODO_CONCEPT_ID variable to $existing" >&2
    exit 1
  fi
  draft="$(json POST "$URL/api/deposit/depositions" -d '{}')"
  draft_id="$(jq -r .id <<<"$draft")"
fi

archive="hydra-$VER.tar.gz"
git archive --format=tar.gz --prefix="hydra-$VER/" -o "$work/$archive" HEAD
call PUT "$(jq -r .links.bucket <<<"$draft")/$archive" --upload-file "$work/$archive" >/dev/null
json PUT "$URL/api/deposit/depositions/$draft_id" --data-binary "@$work/metadata.json" >/dev/null
done="$(json POST "$URL/api/deposit/depositions/$draft_id/actions/publish")"

echo "Published $TAG: https://doi.org/$(jq -r .doi <<<"$done")"
echo "Concept DOI (all versions): https://doi.org/$(jq -r .conceptdoi <<<"$done")"
if [ -z "$CONCEPT" ]; then
  echo "::notice::First Zenodo record. Set the repository variable ZENODO_CONCEPT_ID to $(jq -r .conceptrecid <<<"$done") so the next release adds a version to it."
fi
