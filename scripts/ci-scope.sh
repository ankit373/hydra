#!/usr/bin/env bash
# Read the files a PR changes, one per line on stdin, and print full=true or
# full=false for $GITHUB_OUTPUT. false means docs only: every file is under
# docs/, a *.md, or a registry/*.yaml. No files at all counts as full.
set -euo pipefail

DOCS_ONLY='^(docs/|registry/[^/]+\.yaml$|.*\.md$)'

n=0
while IFS= read -r f; do
  [ -n "$f" ] || continue
  n=$((n + 1))
  if ! [[ $f =~ $DOCS_ONLY ]]; then
    echo "full=true"
    exit 0
  fi
done

if [ "$n" -eq 0 ]; then echo "full=true"; else echo "full=false"; fi
