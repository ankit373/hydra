#!/usr/bin/env bash
# Hydra Atomic File Editor — dispatch/edit.sh
# ─────────────────────────────────────────────────────────────────────────────
# Direct-file edit primitive. Replaces the "agent returns text → orchestrator
# pastes" round-trip with a scoped, validated, rollback-safe write.
#
# Flow:
#   1. scope.sh check + resolve       → reject if outside workspace
#   2. snapshot current file content  → for rollback
#   3. build edit prompt with HYDRA_FILE markers
#   4. route.sh --enum <KEY>          → agent returns text
#   5. parse response between markers → reject if malformed
#   6. atomic write (tmp + mv)
#   7. validate by extension          → rollback + escalate on fail
#   8. emit JSON result               → for parallel.sh / orchestrator
#
# Usage:
#   edit.sh --file <abs-path> --enum <KEY> --prompt "<instruction>"
#           [--no-validate] [--mode rewrite]
#
# Output (stdout): single JSON object
#   { status, file, workspace, git_root, enum, lines_added, lines_removed,
#     validator_passed, rolled_back, error }
#
# Exit codes: 0 ok, 1 generic fail, 2 scope/validation reject, 3 auth required
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$HYDRA_DIR/logs"
LOG_FILE="$LOG_DIR/edit.log"
SCOPE="$HYDRA_DIR/dispatch/scope.sh"
ROUTE="$HYDRA_DIR/dispatch/route.sh"

mkdir -p "$LOG_DIR"
log() { printf '[%s] edit: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG_FILE"; }
err() { echo "❌ edit: $*" >&2; log "ERROR: $*"; }
info() { echo "🧷 edit: $*" >&2; log "INFO: $*"; }

emit_json() {
  # emit_json <status> <file> <workspace> <git_root> <enum> <added> <removed> <validator> <rolled_back> <error>
  jq -n \
    --arg status "$1" --arg file "$2" --arg workspace "$3" --arg git_root "$4" \
    --arg enum "$5" --argjson added "$6" --argjson removed "$7" \
    --arg validator "$8" --argjson rolled_back "$9" --arg error "${10}" \
    '{ status: $status, file: $file, workspace: $workspace, git_root: $git_root,
       enum: $enum, lines_added: $added, lines_removed: $removed,
       validator_passed: ($validator == "true"), rolled_back: $rolled_back, error: $error }'
}

# ── Arg parsing ───────────────────────────────────────────────────────────────

FILE=""; ENUM=""; PROMPT=""; VALIDATE=1; MODE="rewrite"; POLICY_JSON=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --file)        FILE="$2"; shift 2 ;;
    --enum)        ENUM="$2"; shift 2 ;;
    --prompt)      PROMPT="$2"; shift 2 ;;
    --no-validate) VALIDATE=0; shift ;;
    --mode)        MODE="$2"; shift 2 ;;
    --policy)      POLICY_JSON="$2"; shift 2 ;;  # path to policy.json from decide.sh
    *) err "unknown arg: $1"; exit 1 ;;
  esac
done

[[ -z "$FILE"   ]] && err "--file required"   && exit 1
[[ -z "$ENUM"   ]] && err "--enum required"   && exit 1
[[ -z "$PROMPT" ]] && err "--prompt required" && exit 1
[[ "$FILE" != /* ]] && err "--file must be absolute: $FILE" && exit 1

# ── Load policy (if provided) ────────────────────────────────────────────────
# Policy flags become POL_* env-style locals. Phase 1 reads but mostly no-ops;
# Phase 2 features (sr_blocks, atomic, agentic, auto_commit, etc.) hook here.

POL_EDIT_MODE="rewrite"
POL_ATOMIC="false"
POL_AUTO_COMMIT="false"
POL_TRACK_TOKENS="true"
POL_VALIDATORS="[]"
POL_MAX_RETRIES=1
POL_ESCALATE_ON_FAIL="true"
POL_RUBBER_DUCK="false"
POL_DIFF_CAP_PCT=90
POL_VALIDATE_STRICT="false"
POL_USE_REPO_MAP="false"
POL_USE_WORKTREE="false"
POL_TEST_LOOP="false"
POL_LINT_LOOP="false"
POL_DEDUP_FILE_READS="true"
POL_PROMPT_CACHE="true"
POL_DEFENSIVE="false"
POL_MAX_COST_USD=0
POL_MAX_WALL_SECONDS=600
POL_MATCHED_RULES="[]"

if [[ -n "$POLICY_JSON" ]]; then
  if [[ ! -f "$POLICY_JSON" ]]; then
    err "--policy file not found: $POLICY_JSON"; exit 1
  fi
  if ! jq empty "$POLICY_JSON" >/dev/null 2>&1; then
    err "--policy file is not valid JSON: $POLICY_JSON"; exit 1
  fi
  POL_EDIT_MODE=$(jq -r       '.edit_mode             // "rewrite"'   "$POLICY_JSON")
  POL_ATOMIC=$(jq -r          '.atomic                // false | tostring' "$POLICY_JSON")
  POL_AUTO_COMMIT=$(jq -r     '.auto_commit           // false | tostring' "$POLICY_JSON")
  POL_TRACK_TOKENS=$(jq -r    '.track_tokens          // true  | tostring' "$POLICY_JSON")
  POL_VALIDATORS=$(jq -c      '.validators            // []'              "$POLICY_JSON")
  POL_MAX_RETRIES=$(jq -r     '.max_retries           // 1'               "$POLICY_JSON")
  POL_ESCALATE_ON_FAIL=$(jq -r '.escalate_on_fail     // true  | tostring' "$POLICY_JSON")
  POL_RUBBER_DUCK=$(jq -r     '.rubber_duck           // false | tostring' "$POLICY_JSON")
  POL_DIFF_CAP_PCT=$(jq -r    '.diff_size_cap_pct     // 90'              "$POLICY_JSON")
  POL_VALIDATE_STRICT=$(jq -r '.validate_strict       // false | tostring' "$POLICY_JSON")
  POL_USE_REPO_MAP=$(jq -r    '.use_repo_map          // false | tostring' "$POLICY_JSON")
  POL_USE_WORKTREE=$(jq -r    '.use_worktree          // false | tostring' "$POLICY_JSON")
  POL_TEST_LOOP=$(jq -r       '.test_loop             // false | tostring' "$POLICY_JSON")
  POL_LINT_LOOP=$(jq -r       '.lint_loop             // false | tostring' "$POLICY_JSON")
  POL_DEDUP_FILE_READS=$(jq -r '.dedup_file_reads     // true  | tostring' "$POLICY_JSON")
  POL_PROMPT_CACHE=$(jq -r    '.prompt_cache          // true  | tostring' "$POLICY_JSON")
  POL_DEFENSIVE=$(jq -r       '.defensive             // false | tostring' "$POLICY_JSON")
  POL_MAX_COST_USD=$(jq -r    '.max_cost_usd          // 0'                "$POLICY_JSON")
  POL_MAX_WALL_SECONDS=$(jq -r '.max_wall_seconds     // 600'              "$POLICY_JSON")
  POL_MATCHED_RULES=$(jq -c   '.matched_rules         // []'              "$POLICY_JSON")
  info "policy loaded: mode=$POL_EDIT_MODE atomic=$POL_ATOMIC commit=$POL_AUTO_COMMIT repo_map=$POL_USE_REPO_MAP strict=$POL_VALIDATE_STRICT rules=$(echo "$POL_MATCHED_RULES" | jq -r 'join(",")')"
fi

# Honor policy edit_mode if it was set and --mode wasn't explicitly given.
# (Phase 1 only supports rewrite; sr_blocks/agentic gracefully fall back with a warning.)
if [[ -n "$POLICY_JSON" && "$MODE" == "rewrite" && "$POL_EDIT_MODE" != "rewrite" ]]; then
  info "policy requested edit_mode=$POL_EDIT_MODE — Phase 1 only implements rewrite, falling back"
fi

# CORE tier is the orchestrator itself — it should use native Edit/Write
if [[ "$ENUM" == "CORE" ]]; then
  err "CORE tier: use Claude's native Edit/Write directly, not edit.sh"
  exit 1
fi

# ── Scope check ───────────────────────────────────────────────────────────────

if ! WORKSPACE=$("$SCOPE" check "$FILE" 2>&1); then
  reason="$WORKSPACE"
  emit_json "fail" "$FILE" "" "" "$ENUM" 0 0 "false" "false" "scope_rejected: $reason"
  exit 2
fi

resolved=$("$SCOPE" resolve "$FILE")
GIT_ROOT=$(echo "$resolved" | jq -r '.git_root')

# ── Snapshot ──────────────────────────────────────────────────────────────────

ORIG_EXISTED=0; ORIG_CONTENT=""
if [[ -f "$FILE" ]]; then
  ORIG_EXISTED=1
  ORIG_CONTENT=$(cat "$FILE")
fi
ORIG_LINES=$(printf '%s' "$ORIG_CONTENT" | wc -l | tr -d ' ')
ORIG_LINES=$((ORIG_LINES + (ORIG_EXISTED == 1 ? 1 : 0)))  # cat -n style

info "target=$FILE workspace=$WORKSPACE enum=$ENUM existed=$ORIG_EXISTED orig_lines=$ORIG_LINES"

# Backup path (used when git_root is empty). Stable name — created on FIRST
# edit only, persists until review.sh approves/rejects. Subsequent edits before
# review preserve the original baseline so reject can roll back to pre-Hydra.
BACKUP="${FILE}.hydra-bak"
WE_CREATED_BACKUP=0
if [[ -z "$GIT_ROOT" && $ORIG_EXISTED -eq 1 && ! -f "$BACKUP" ]]; then
  cp "$FILE" "$BACKUP"
  WE_CREATED_BACKUP=1
fi

# Helper: only delete backup on early-exit if WE created it this run
cleanup_our_backup() {
  [[ $WE_CREATED_BACKUP -eq 1 && -f "$BACKUP" ]] && rm -f "$BACKUP"
}

# ── Build edit prompt ─────────────────────────────────────────────────────────
# Strict markers so we can extract the new file content unambiguously.

if [[ $ORIG_EXISTED -eq 1 ]]; then
  current_block="$ORIG_CONTENT"
  context_note="The file currently exists. Modify it per the instruction below."
else
  current_block="<empty — file does not exist yet>"
  context_note="The file does NOT yet exist. Create it per the instruction below."
fi

# ── Optional repo-map injection (policy.use_repo_map=true) ──────────────────
REPO_MAP_BLOCK=""
if [[ "${POL_USE_REPO_MAP:-}" == "true" ]]; then
  RM_SH="$HYDRA_DIR/dispatch/repo-map.sh"
  if [[ -x "$RM_SH" ]]; then
    rm_out=$("$RM_SH" for "$FILE" 2>/dev/null || true)
    rm_bytes=${#rm_out}
    # Cap at 24KB to keep prompt size sane; trim with a marker if too big
    if [[ $rm_bytes -gt 24576 ]]; then
      info "repo map ${rm_bytes}B truncated to 24KB"
      rm_out=$(printf '%s\n…[truncated, %d bytes total]…' "${rm_out:0:24576}" "$rm_bytes")
    fi
    if [[ -n "$rm_out" ]]; then
      REPO_MAP_BLOCK=$(printf '\nProject context (symbol map of nearby files):\n%s\n' "$rm_out")
      info "repo map injected (${rm_bytes}B)"
    fi
  fi
fi

EDIT_PROMPT=$(cat <<EOF
You are editing a single file. Output ONLY the new file content between the
markers. No prose. No explanations. No code fences (no \`\`\`).
$REPO_MAP_BLOCK
File path: $FILE
$context_note

Instruction:
$PROMPT

Current file content:
<<<HYDRA_FILE_START>>>
$current_block
<<<HYDRA_FILE_END>>>

Now output the COMPLETE new file content (every line, not a diff, not a
snippet) between these exact markers and nothing else:
<<<HYDRA_FILE_START>>>
(new content here)
<<<HYDRA_FILE_END>>>
EOF
)

# ── Dispatch ──────────────────────────────────────────────────────────────────

info "dispatching to enum=$ENUM"
set +e
RAW=$("$ROUTE" --enum "$ENUM" --prompt "$EDIT_PROMPT" 2>>"$LOG_FILE")
RC=$?
set -e

if [[ $RC -ne 0 ]]; then
  err "route.sh failed (exit $RC) — no edit attempted"
  cleanup_our_backup
  emit_json "fail" "$FILE" "$WORKSPACE" "$GIT_ROOT" "$ENUM" 0 0 "false" "false" "route_failed: exit $RC"
  exit $RC
fi

# ── Parse response ────────────────────────────────────────────────────────────
# Extract content strictly between the markers. Tolerate stray prose outside.
# Tolerate code fences immediately inside the markers (strip them).

extract_between() {
  # Strict: content between START and END markers (exclusive).
  echo "$1" | awk '
    BEGIN { inside=0; printed=0 }
    /<<<HYDRA_FILE_END>>>/ && inside==1 { inside=0; printed=1; exit }
    inside==1 { print }
    /<<<HYDRA_FILE_START>>>/ && printed==0 { inside=1 }
  '
}

extract_lenient() {
  # Lenient: tolerate a missing START or END marker (common with small models).
  # - Both missing  → empty (caller will reject)
  # - START missing → everything from line 1 up to END (exclusive)
  # - END missing   → everything from line after START to EOF
  local text="$1"
  local has_start has_end
  has_start=$(echo "$text" | grep -c '<<<HYDRA_FILE_START>>>' || true)
  has_end=$(echo "$text"   | grep -c '<<<HYDRA_FILE_END>>>'   || true)

  if [[ "$has_start" -gt 0 && "$has_end" -gt 0 ]]; then
    extract_between "$text"
  elif [[ "$has_end" -gt 0 ]]; then
    echo "$text" | awk '/<<<HYDRA_FILE_END>>>/ { exit } { print }'
  elif [[ "$has_start" -gt 0 ]]; then
    echo "$text" | awk 'p { print } /<<<HYDRA_FILE_START>>>/ { p=1 }'
  else
    echo ""
  fi
}

NEW_CONTENT=$(extract_lenient "$RAW")

if [[ -z "$NEW_CONTENT" ]]; then
  err "marker parse failed — agent did not return content between HYDRA_FILE_START/END"
  log "raw response (first 500 chars): $(printf '%s' "$RAW" | head -c 500)"
  cleanup_our_backup
  emit_json "fail" "$FILE" "$WORKSPACE" "$GIT_ROOT" "$ENUM" 0 0 "false" "false" "marker_parse_failed"
  exit 2
fi

# Strip a leading/trailing fence-only line if the agent disobeyed and added them
NEW_CONTENT=$(echo "$NEW_CONTENT" | awk '
  { lines[NR]=$0 }
  END {
    start=1; end=NR
    if (NR > 0 && lines[1]   ~ /^```/)  start=2
    if (NR > 0 && lines[end] ~ /^```$/) end--
    for (i=start; i<=end; i++) print lines[i]
  }
')

# Marker leakage check — abort if any HYDRA marker survives in the body
if echo "$NEW_CONTENT" | grep -q "<<<HYDRA_FILE_"; then
  err "marker leakage detected in new content — rejecting"
  cleanup_our_backup
  emit_json "fail" "$FILE" "$WORKSPACE" "$GIT_ROOT" "$ENUM" 0 0 "false" "false" "marker_leakage"
  exit 2
fi

# Sanity: if the original had content, the new shouldn't be empty
if [[ $ORIG_EXISTED -eq 1 && -z "$NEW_CONTENT" ]]; then
  err "agent returned empty content for a non-empty file — rejecting"
  cleanup_our_backup
  emit_json "fail" "$FILE" "$WORKSPACE" "$GIT_ROOT" "$ENUM" 0 0 "false" "false" "empty_replacement"
  exit 2
fi

# ── Atomic write ──────────────────────────────────────────────────────────────

TMP="${FILE}.hydra-tmp.$$"
mkdir -p "$(dirname "$FILE")"
printf '%s\n' "$NEW_CONTENT" > "$TMP"
mv "$TMP" "$FILE"
info "wrote new content ($(wc -l < "$FILE" | tr -d ' ') lines)"

# ── Validate ──────────────────────────────────────────────────────────────────

VALIDATOR_OK="true"
if [[ $VALIDATE -eq 1 && "${HYDRA_VALIDATE:-on}" != "off" ]]; then
  ext="${FILE##*.}"
  vtmpl=$("$SCOPE" validator "$ext" 2>/dev/null || true)

  # Special path for ts/tsx: try workspace-local tsc if present.
  # Prefer running tsc with the package's tsconfig.json (-p flag) so the package's
  # compiler options (target, lib, jsx, etc.) apply. Without -p, tsc ignores the
  # tsconfig and defaults to ES3 target, which fails on any modern code in sibling
  # files. See ankit373/hydra#4.
  if [[ -z "$vtmpl" && ( "$ext" == "ts" || "$ext" == "tsx" ) && -n "$GIT_ROOT" ]]; then
    if [[ -f "$GIT_ROOT/node_modules/.bin/tsc" && -f "$GIT_ROOT/tsconfig.json" ]]; then
      vtmpl="$GIT_ROOT/node_modules/.bin/tsc --noEmit -p $GIT_ROOT/tsconfig.json"
    elif [[ -f "$GIT_ROOT/node_modules/.bin/tsc" ]]; then
      vtmpl="$GIT_ROOT/node_modules/.bin/tsc --noEmit --allowJs --skipLibCheck --target es2022 --lib es2022,dom {file}"
    fi
  fi

  if [[ -n "$vtmpl" ]]; then
    cmd="${vtmpl//\{file\}/$FILE}"
    info "validating: $cmd"
    set +e
    vout=$(eval "$cmd" 2>&1)
    vrc=$?
    set -e

    if [[ $vrc -ne 0 ]]; then
      err "validation failed (exit $vrc): $(echo "$vout" | head -3)"
      log "validator output: $vout"

      # Rollback
      if [[ -n "$GIT_ROOT" ]] && (cd "$GIT_ROOT" && git ls-files --error-unmatch "$FILE" >/dev/null 2>&1); then
        (cd "$GIT_ROOT" && git checkout -- "$FILE")
        info "rolled back via git checkout"
      elif [[ -f "$BACKUP" ]]; then
        mv "$BACKUP" "$FILE"
        info "rolled back via .hydra-bak"
      elif [[ $ORIG_EXISTED -eq 0 ]]; then
        rm -f "$FILE"
        info "rolled back by removing new file"
      else
        printf '%s' "$ORIG_CONTENT" > "$FILE"
        info "rolled back via in-memory snapshot"
      fi

      emit_json "fail" "$FILE" "$WORKSPACE" "$GIT_ROOT" "$ENUM" 0 0 "false" "true" "validation_failed: $(echo "$vout" | head -1)"
      exit 2
    fi
    VALIDATOR_OK="true"
  else
    info "no validator for .$ext — skipping"
    VALIDATOR_OK="true"
  fi
fi

# ── Diff stats ────────────────────────────────────────────────────────────────

NEW_LINES=$(wc -l < "$FILE" | tr -d ' ')
LINES_ADDED=0; LINES_REMOVED=0

set +e
if [[ -n "$GIT_ROOT" ]] && (cd "$GIT_ROOT" && git ls-files --error-unmatch "$FILE" >/dev/null 2>&1); then
  stats=$(cd "$GIT_ROOT" && git diff --numstat -- "$FILE" 2>/dev/null | head -1)
elif [[ -f "$BACKUP" ]]; then
  stats=$(diff -u "$BACKUP" "$FILE" 2>/dev/null | awk '
    /^\+[^+]/ { a++ } /^-[^-]/ { r++ }
    END { printf "%d\t%d\n", (a+0), (r+0) }')
else
  stats=$(printf '%d\t%d\n' "$NEW_LINES" "$ORIG_LINES")
fi
set -e
LINES_ADDED=$(printf '%s\n' "$stats"   | awk '{print $1+0}')
LINES_REMOVED=$(printf '%s\n' "$stats" | awk '{print $2+0}')
[[ -z "$LINES_ADDED"   ]] && LINES_ADDED=0
[[ -z "$LINES_REMOVED" ]] && LINES_REMOVED=0

# Success: keep .hydra-bak so review.sh has a baseline. review.sh approve/reject
# is what removes the backup. (Git workspaces never had a backup to begin with.)

info "✓ edited ${FILE##*/} (+$LINES_ADDED -$LINES_REMOVED)"

# A2A handoff for the next agent in a chain
HANDOFF="$LOG_DIR/last_edit.json"
jq -n \
  --arg from "hydra-edit-$ENUM" \
  --arg file "$FILE" \
  --arg enum "$ENUM" \
  --arg workspace "$WORKSPACE" \
  --argjson added "$LINES_ADDED" \
  --argjson removed "$LINES_REMOVED" \
  '{ from: $from, file: $file, enum: $enum, workspace: $workspace,
     lines_added: $added, lines_removed: $removed }' > "$HANDOFF"

emit_json "ok" "$FILE" "$WORKSPACE" "$GIT_ROOT" "$ENUM" "$LINES_ADDED" "$LINES_REMOVED" "$VALIDATOR_OK" "false" ""
exit 0
