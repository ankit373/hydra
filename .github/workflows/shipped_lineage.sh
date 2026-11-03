#!/usr/bin/env bash
# Write shipped_prs.txt: every (#n) in the lineage a release shipped.
# Driven by close-shipped-issues.yml; close_shipped_issues.py reads the file.
set -euo pipefail

tag="${1:?usage: shipped_lineage.sh <tag>}"
subjects() { sed -nE 's/.*\(#([0-9]+)\)[[:space:]]*$/\1/p'; }

git rev-parse -q --verify "refs/tags/${tag}^{commit}" >/dev/null \
  || { echo "::error::Tag ${tag} does not exist."; exit 1; }

# main carries one squash commit per release, so its own log names only the
# release PRs. Each one's refs/pull/N/head is the develop lineage that shipped.
git log --format='%s' "refs/tags/${tag}" | subjects | sort -un > release_prs.txt
echo "Release PRs on main: $(tr '\n' ' ' < release_prs.txt)"

cp release_prs.txt shipped_prs.txt
while read -r pr; do
  if git fetch --quiet --no-tags origin "refs/pull/${pr}/head:refs/shipped/${pr}" 2>/dev/null; then
    git log --format='%s' "refs/shipped/${pr}" | subjects >> shipped_prs.txt
  else
    echo "::warning::refs/pull/${pr}/head is unavailable, skipping its lineage."
  fi
done < release_prs.txt

# A carry PR's tree is the release branch's and its parent is main's tip, so it
# ships the code and discards the history the walk above needs. The release
# branch still holds it (#1140).
#
# Bounded by the previous release branch, and used only when one exists: the
# branch reaches back through every earlier release, so unbounded it would name
# their still-open issues as shipped by this one. Measured on v1.5.0, 455
# numbers unbounded against 159 since v1.4.2.
if git ls-remote --exit-code --heads origin "refs/heads/release/${tag}" >/dev/null 2>&1; then
  prev=$(git ls-remote --heads origin 'refs/heads/release/*' \
    | sed -nE 's#.*refs/heads/release/(v[0-9]+\.[0-9]+\.[0-9]+)$#\1#p' \
    | sort -V | awk -v t="${tag}" '$0 == t { exit } { last = $0 } END { print last }')
  if [ -n "${prev}" ]; then
    git fetch --quiet --no-tags origin \
      "refs/heads/release/${tag}:refs/release/this" \
      "refs/heads/release/${prev}:refs/release/prev"
    git log --format='%s' refs/release/prev..refs/release/this | subjects >> shipped_prs.txt
    echo "Carried release: $(git rev-list --count refs/release/prev..refs/release/this) commits from release/${tag} since release/${prev}."
  else
    echo "::warning::release/${tag} has no earlier release branch to bound it, so its lineage is not used: unbounded it would claim earlier releases' issues."
  fi
fi

sort -un -o shipped_prs.txt shipped_prs.txt
echo "Shipped PRs: $(wc -l < shipped_prs.txt)"
