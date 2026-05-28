#!/usr/bin/env bash
# Hydra Review Surface — dispatch/review.sh
# ─────────────────────────────────────────────────────────────────────────────
# Reviews uncommitted edits made by edit.sh / parallel.sh and lets the
# orchestrator approve, reject (rollback), or hand off to a QA tier.
#
# Subcommands:
#   summary [<file> ...]   → JSON { files: [{ file, git_root, added, removed, status }] }
#                            If no files given, summarises all edits in last_parallel.json
#                            and last_edit.json.
#   diff <file>            → prints unified diff for <file> (git or .hydra-bak)
#   approve <file>         → no-op for git (leaves diff in working tree); for
#                            non-git workspaces, removes any .hydra-bak file
#   reject <file>          → rolls back to pre-edit state (git checkout or backup)
#   qa <file> [--tier N]   → dispatches the file diff to tier N (default 4 = HARD/GPT-OSS)
#                            for review; returns the reviewer's verdict
#
# All operations are scope-checked (no review/rollback outside workspaces).
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$HYDRA_DIR/logs"
LOG_FILE="$LOG_DIR/review.log"
SCOPE="$HYDRA_DIR/dispatch/scope.sh"
ROUTE="$HYDRA_DIR/dispatch/route.sh"

mkdir -p "$LOG_DIR"
log() { printf '[%s] review: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG_FILE"; }
err() { echo "❌ review: $*" >&2; log "ERROR: $*"; }
info() { echo "🔍 review: $*" >&2; log "INFO: $*"; }

# Resolve git_root for a file via scope.sh
git_root_of() { "$SCOPE" git-root "$1" 2>/dev/null || echo ""; }

# Stable backup path written by edit.sh
backup_for() {
  local f="$1"
  local b="${f}.hydra-bak"
  [[ -f "$b" ]] && echo "$b" || true
}

# Compute numstat for a single file (returns "added\tremoved\tstatus")
numstat_one() {
  local f="$1"
  local gr; gr=$(git_root_of "$f")

  if [[ -n "$gr" ]]; then
    if (cd "$gr" && git ls-files --error-unmatch "$f" >/dev/null 2>&1); then
      local s; s=$(cd "$gr" && git diff --numstat -- "$f" 2>/dev/null | head -1)
      if [[ -z "$s" ]]; then
        printf '0\t0\tunchanged\n'
      else
        printf '%s\tmodified\n' "$s"
      fi
    else
      # Untracked / new file
      if [[ -f "$f" ]]; then
        local n; n=$(wc -l < "$f" | tr -d ' ')
        printf '%d\t0\tnew\n' "$n"
      else
        printf '0\t0\tmissing\n'
      fi
    fi
  else
    local b; b=$(backup_for "$f")
    if [[ -n "$b" && -f "$b" ]]; then
      local s; s=$(diff -u "$b" "$f" 2>/dev/null | awk '
        /^\+[^+]/ { a++ } /^-[^-]/ { r++ }
        END { printf "%d\t%d", (a+0), (r+0) }')
      printf '%s\tmodified\n' "$s"
    elif [[ -f "$f" ]]; then
      printf '0\t0\tno_baseline\n'
    else
      printf '0\t0\tmissing\n'
    fi
  fi
}

# ── Subcommands ───────────────────────────────────────────────────────────────

cmd="${1:-}"; shift || true

case "$cmd" in

  summary)
    files=("$@")
    if [[ ${#files[@]} -eq 0 ]]; then
      # Pull from last edit/parallel logs. parallel.sh edit results have .file
      # at the top level; .file may also be missing for text-mode rows.
      if [[ -f "$LOG_DIR/last_parallel.json" ]]; then
        while IFS= read -r f; do
          [[ -n "$f" ]] && files+=("$f")
        done < <(jq -r '.[] | select(.mode=="edit") | .file // empty' "$LOG_DIR/last_parallel.json" 2>/dev/null || true)
      fi
      if [[ ${#files[@]} -eq 0 && -f "$LOG_DIR/last_edit.json" ]]; then
        f=$(jq -r '.file // empty' "$LOG_DIR/last_edit.json" 2>/dev/null || true)
        [[ -n "$f" ]] && files+=("$f")
      fi
    fi

    if [[ ${#files[@]} -eq 0 ]]; then
      echo '{ "files": [] }'
      exit 0
    fi

    out='[]'
    for f in "${files[@]}"; do
      [[ "$f" != /* ]] && { err "skipping non-absolute: $f"; continue; }
      # Scope check (warn but don't bail — orchestrator may want to see denied edits)
      ws=$("$SCOPE" check "$f" 2>/dev/null || echo "")
      gr=$(git_root_of "$f")
      ns=$(numstat_one "$f")
      added=$(echo "$ns"   | awk '{print $1+0}')
      removed=$(echo "$ns" | awk '{print $2+0}')
      status=$(echo "$ns"  | awk '{print $3}')

      out=$(echo "$out" | jq \
        --arg file "$f" --arg workspace "$ws" --arg git_root "$gr" \
        --argjson added "$added" --argjson removed "$removed" \
        --arg status "$status" \
        '. += [{ file: $file, workspace: $workspace, git_root: $git_root,
                 lines_added: $added, lines_removed: $removed, status: $status }]')
    done

    jq -n --argjson files "$out" '{ files: $files,
      totals: ($files | { count: length,
                          added: (map(.lines_added) | add // 0),
                          removed: (map(.lines_removed) | add // 0) }) }'
    ;;

  diff)
    f="${1:-}"
    [[ -z "$f" ]] && err "usage: review.sh diff <file>" && exit 1
    [[ "$f" != /* ]] && err "path must be absolute: $f" && exit 1

    gr=$(git_root_of "$f")
    if [[ -n "$gr" ]]; then
      (cd "$gr" && git diff -- "$f")
    else
      b=$(backup_for "$f")
      if [[ -n "$b" && -f "$b" ]]; then
        diff -u "$b" "$f" || true
      else
        err "no diff available for $f (no git, no backup)"
        exit 1
      fi
    fi
    ;;

  approve)
    f="${1:-}"
    [[ -z "$f" ]] && err "usage: review.sh approve <file>" && exit 1
    [[ "$f" != /* ]] && err "path must be absolute: $f" && exit 1

    "$SCOPE" check "$f" >/dev/null 2>&1 || { err "scope rejected: $f"; exit 2; }

    gr=$(git_root_of "$f")
    if [[ -n "$gr" ]]; then
      info "approved (left in working tree): $f"
    else
      b=$(backup_for "$f")
      [[ -n "$b" && -f "$b" ]] && rm -f "$b" && info "approved and removed backup: $f"
    fi
    jq -n --arg file "$f" '{ status: "approved", file: $file }'
    ;;

  reject)
    f="${1:-}"
    [[ -z "$f" ]] && err "usage: review.sh reject <file>" && exit 1
    [[ "$f" != /* ]] && err "path must be absolute: $f" && exit 1

    "$SCOPE" check "$f" >/dev/null 2>&1 || { err "scope rejected: $f"; exit 2; }

    gr=$(git_root_of "$f")
    rolled="false"; how=""
    if [[ -n "$gr" ]] && (cd "$gr" && git ls-files --error-unmatch "$f" >/dev/null 2>&1); then
      (cd "$gr" && git checkout -- "$f")
      rolled="true"; how="git_checkout"
    elif [[ -n "$gr" && -f "$f" ]] && ! (cd "$gr" && git ls-files --error-unmatch "$f" >/dev/null 2>&1); then
      # Untracked new file — just delete it
      rm -f "$f"
      rolled="true"; how="rm_untracked"
    else
      b=$(backup_for "$f")
      if [[ -n "$b" && -f "$b" ]]; then
        mv "$b" "$f"
        rolled="true"; how="backup_restore"
      fi
    fi

    if [[ "$rolled" == "true" ]]; then
      info "rejected ($how): $f"
      jq -n --arg file "$f" --arg how "$how" '{ status: "rejected", file: $file, method: $how }'
    else
      err "nothing to roll back for $f"
      exit 1
    fi
    ;;

  qa)
    f="${1:-}"; shift || true
    [[ -z "$f" ]] && err "usage: review.sh qa <file> [--tier N]" && exit 1
    [[ "$f" != /* ]] && err "path must be absolute: $f" && exit 1

    tier=4
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --tier) tier="$2"; shift 2 ;;
        *) err "unknown arg: $1"; exit 1 ;;
      esac
    done

    "$SCOPE" check "$f" >/dev/null 2>&1 || { err "scope rejected: $f"; exit 2; }

    # Build the diff to review
    gr=$(git_root_of "$f")
    diff_text=""
    if [[ -n "$gr" ]]; then
      diff_text=$(cd "$gr" && git diff -- "$f")
    else
      b=$(backup_for "$f")
      [[ -n "$b" && -f "$b" ]] && diff_text=$(diff -u "$b" "$f" || true)
    fi

    if [[ -z "$diff_text" ]]; then
      err "no diff to review for $f"
      exit 1
    fi

    qa_prompt=$(cat <<EOF
You are a code reviewer. Review the following diff for: bugs, security issues,
broken invariants, style violations, and missing edge cases. Be concise.

File: $f

Diff:
$diff_text

Output exactly one of:
APPROVED <one-line reason>
CONCERNS <bullet list of issues>
EOF
)

    info "sending diff to tier $tier for QA"
    verdict=$("$ROUTE" --tier "$tier" --prompt "$qa_prompt")
    jq -n --arg file "$f" --argjson tier "$tier" --arg verdict "$verdict" \
      '{ status: "reviewed", file: $file, reviewer_tier: $tier, verdict: $verdict }'
    ;;

  *)
    err "unknown command: $cmd"
    err "usage: review.sh {summary|diff|approve|reject|qa} <args>"
    exit 1
    ;;

esac
