#!/usr/bin/env bash
# Hydra Parallel Dispatcher — dispatch/parallel.sh
# ─────────────────────────────────────────────────────────────────────────────
# Fans N independent subtasks out to N tiers simultaneously using background
# jobs. Each task is either a text dispatch (via route.sh) or a direct file
# edit (via edit.sh) depending on whether the task object has a "file" field.
#
# Usage:
#   parallel.sh --tasks <tasks.json>
#
# Task object shape:
#
#   Text dispatch (output returned in JSON):
#     { "label": "design",  "enum": "EXPERT", "prompt": "Sketch the auth flow" }
#
#   Direct-file edit (modifies the file via edit.sh):
#     { "label": "schema", "enum": "SIMPLE", "file": "/abs/path.ts",
#       "prompt": "Add an email field", "validate": true }
#
# Output (stdout): JSON array of result objects. Shape varies by mode:
#
#   text result:   { label, enum, mode: "text",  status, output }
#   edit result:   { label, enum, mode: "edit",  status, file, workspace,
#                    git_root, lines_added, lines_removed, validator_passed,
#                    rolled_back, error }
#
# Exit code: 0 if all tasks succeeded, 1 if any failed.
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$HYDRA_DIR/logs"
LOG_FILE="$LOG_DIR/parallel.log"
ROUTE="$HYDRA_DIR/dispatch/route.sh"
EDIT="$HYDRA_DIR/dispatch/edit.sh"
DECIDE="$HYDRA_DIR/dispatch/decide.sh"
SCOPE="$HYDRA_DIR/dispatch/scope.sh"

mkdir -p "$LOG_DIR"

log() { printf '[%s] parallel: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG_FILE"; }
err() { echo "❌ parallel: $*" >&2; log "ERROR: $*"; }

# ── Arg parsing ───────────────────────────────────────────────────────────────

TASKS_FILE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tasks) TASKS_FILE="$2"; shift 2 ;;
    *) err "Unknown arg: $1"; exit 1 ;;
  esac
done

[[ -z "$TASKS_FILE" ]] && err "--tasks <file> required" && exit 1
[[ ! -f "$TASKS_FILE" ]] && err "Tasks file not found: $TASKS_FILE" && exit 1

if ! jq empty "$TASKS_FILE" 2>/dev/null; then
  err "Invalid JSON in $TASKS_FILE"; exit 1
fi

task_count=$(jq 'length' "$TASKS_FILE")
[[ "$task_count" -eq 0 ]] && err "No tasks in $TASKS_FILE" && exit 1

# Pre-flight: detect file-write conflicts (same file targeted by two tasks).
# Parallel writes to the same file would race; reject up front.
dupes=$(jq -r '[.[] | select(.file != null) | .file] | group_by(.) | map(select(length>1) | .[0]) | .[]' "$TASKS_FILE" 2>/dev/null)
if [[ -n "$dupes" ]]; then
  err "two or more tasks target the same file (would race):"
  echo "$dupes" | sed 's/^/  • /' >&2
  exit 1
fi

# ── Build a task-spec JSON for decide.sh given task fields ────────────────────
# file_count is the count of edit-tasks in THIS batch (so cross-file rules fire).
# context_pct comes from state.json (Claude's reported usage).
build_spec_for_task() {
  local f="$1" enum="$2" prompt="$3"

  local lines=0 ext=""
  if [[ -f "$f" ]]; then
    lines=$(wc -l < "$f" | tr -d ' ')
  fi
  if [[ "$f" == *.* ]]; then
    ext="${f##*.}"
  fi

  local workspace="" has_git="false"
  local resolved
  resolved=$("$SCOPE" resolve "$f" 2>/dev/null || echo '{}')
  workspace=$(echo "$resolved" | jq -r '.workspace // ""')
  local git_root; git_root=$(echo "$resolved" | jq -r '.git_root // ""')
  [[ -n "$git_root" ]] && has_git="true"

  local tier=10
  case "$enum" in
    CORE)      tier=1 ;;
    EXPERT)    tier=2 ;;
    VERY_HARD) tier=3 ;;
    HARD)      tier=4 ;;
    COMPLEX)   tier=5 ;;
    MODERATE)  tier=6 ;;
    STANDARD)  tier=7 ;;
    SIMPLE)    tier=8 ;;
    TRIVIAL)   tier=9 ;;
    GRUNT)     tier=10 ;;
  esac

  local ctx_pct=0
  if [[ -f "$LOG_DIR/state.json" ]]; then
    ctx_pct=$(jq -r '.claude_pct // 0' "$LOG_DIR/state.json" 2>/dev/null || echo 0)
  fi

  jq -n \
    --arg file "$f" --argjson file_lines "$lines" \
    --argjson file_count "$EDIT_TASK_COUNT" \
    --arg file_extension "$ext" \
    --arg task_type "${PARALLEL_TASK_TYPE:-other}" \
    --arg workspace "$workspace" \
    --argjson has_git "$has_git" \
    --argjson enum_tier "$tier" \
    --argjson in_playbook "${PARALLEL_IN_PLAYBOOK:-false}" \
    --arg stage_name "${PARALLEL_STAGE_NAME:-}" \
    --argjson context_pct "$ctx_pct" \
    --arg prompt "$prompt" \
    --argjson prompt_length "${#prompt}" \
    '{ file: $file, file_lines: $file_lines, file_count: $file_count,
       file_extension: $file_extension, task_type: $task_type,
       workspace: $workspace, has_git: $has_git, enum_tier: $enum_tier,
       in_playbook: $in_playbook, stage_name: $stage_name,
       context_pct: $context_pct, prompt: $prompt, prompt_length: $prompt_length }'
}

# Pre-compute EDIT_TASK_COUNT (used by the spec builder above)
EDIT_TASK_COUNT=$(jq '[.[] | select(.file != null)] | length' "$TASKS_FILE")

# Optional batch-level overrides exposed by company.sh or callers
PARALLEL_TASK_TYPE="${PARALLEL_TASK_TYPE:-other}"
PARALLEL_IN_PLAYBOOK="${PARALLEL_IN_PLAYBOOK:-false}"
PARALLEL_STAGE_NAME="${PARALLEL_STAGE_NAME:-}"

log "Fanning out $task_count tasks in parallel (edit_task_count=$EDIT_TASK_COUNT)"
echo "🐙 Hydra parallel: dispatching $task_count tasks simultaneously..." >&2

# ── Parallel dispatch ─────────────────────────────────────────────────────────

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

pids=()
modes=()
for i in $(seq 0 $((task_count - 1))); do
  label=$(jq -r ".[$i].label // \"task_$i\"" "$TASKS_FILE")
  enum=$(jq -r  ".[$i].enum"                  "$TASKS_FILE")
  prompt=$(jq -r ".[$i].prompt"               "$TASKS_FILE")
  file=$(jq -r  ".[$i].file // empty"         "$TASKS_FILE")
  context_file=$(jq -r ".[$i].context // empty" "$TASKS_FILE")
  validate=$(jq -r ".[$i].validate // true"   "$TASKS_FILE")

  out_file="$TMP_DIR/${i}.out"
  result_file="$TMP_DIR/${i}.result.json"
  status_file="$TMP_DIR/${i}.status"

  if [[ -n "$file" ]]; then
    mode="edit"
    modes+=("edit")

    # ── Build task-spec + auto-decide policy (unless task provides one) ─────
    # Caller can override by including a "policy" object in the task JSON.
    inline_policy=$(jq -c ".[$i].policy // empty" "$TASKS_FILE")
    policy_file="$TMP_DIR/${i}.policy.json"

    if [[ -n "$inline_policy" ]]; then
      # Caller-provided policy — auto-decide first to fill defaults, then merge.
      spec=$(build_spec_for_task "$file" "$enum" "$prompt")
      auto=$(echo "$spec" | "$DECIDE" -)
      jq -n --argjson auto "$auto" --argjson over "$inline_policy" \
        '$auto * $over' > "$policy_file"
    else
      spec=$(build_spec_for_task "$file" "$enum" "$prompt")
      echo "$spec" | "$DECIDE" - > "$policy_file"
    fi

    matched=$(jq -r '.matched_rules | join(",")' "$policy_file")
    echo "  → [$label] ($enum) edit ${file##*/}  policy: $matched" >&2
    log "Spawning [$label] mode=edit enum=$enum file=$file policy_rules=$matched"

    (
      args=(--file "$file" --enum "$enum" --prompt "$prompt" --policy "$policy_file")
      [[ "$validate" == "false" ]] && args+=(--no-validate)

      if "$EDIT" "${args[@]}" > "$result_file" 2>"$out_file"; then
        echo "ok" > "$status_file"
        log "[$label] edit ok"
      else
        echo "fail" > "$status_file"
        log "[$label] edit failed"
      fi
    ) &
  else
    mode="text"
    modes+=("text")
    echo "  → [$label] ($enum) text" >&2
    log "Spawning [$label] mode=text enum=$enum"

    (
      args=(--enum "$enum" --prompt "$prompt")
      [[ -n "$context_file" && -f "$context_file" ]] && args+=(--context "$context_file")

      if "$ROUTE" "${args[@]}" > "$out_file" 2>>"$LOG_FILE"; then
        echo "ok" > "$status_file"
        log "[$label] text ok"
      else
        echo "fail" > "$status_file"
        log "[$label] text failed"
      fi
    ) &
  fi

  pids+=($!)
done

# ── Wait for all ─────────────────────────────────────────────────────────────

any_failed=0
for i in "${!pids[@]}"; do
  pid="${pids[$i]}"
  label=$(jq -r ".[$i].label // \"task_$i\"" "$TASKS_FILE")
  if wait "$pid"; then
    echo "  ✓ [$label] done" >&2
  else
    echo "  ✗ [$label] failed" >&2
    any_failed=1
  fi
done

# ── Collect results into JSON ─────────────────────────────────────────────────

results="[]"
for i in $(seq 0 $((task_count - 1))); do
  label=$(jq -r ".[$i].label // \"task_$i\"" "$TASKS_FILE")
  enum=$(jq -r  ".[$i].enum"                  "$TASKS_FILE")
  mode="${modes[$i]}"
  out_file="$TMP_DIR/${i}.out"
  result_file="$TMP_DIR/${i}.result.json"
  status_file="$TMP_DIR/${i}.status"

  status="fail"
  [[ -f "$status_file" ]] && status=$(cat "$status_file")

  if [[ "$mode" == "edit" ]]; then
    # edit.sh emits a JSON object on stdout (captured in result_file)
    edit_json='{}'
    if [[ -f "$result_file" && -s "$result_file" ]]; then
      if jq empty "$result_file" >/dev/null 2>&1; then
        edit_json=$(cat "$result_file")
      fi
    fi
    err_msg=""
    [[ -f "$out_file" ]] && err_msg=$(cat "$out_file")

    results=$(echo "$results" | jq \
      --arg label "$label" --arg enum "$enum" --arg mode "$mode" \
      --arg status "$status" --arg stderr "$err_msg" \
      --argjson edit "$edit_json" \
      '. += [($edit + { label: $label, enum: $enum, mode: $mode, status: $status, stderr: $stderr })]')
  else
    output=""
    [[ -f "$out_file" ]] && output=$(cat "$out_file")
    results=$(echo "$results" | jq \
      --arg label "$label" --arg enum "$enum" --arg mode "$mode" \
      --arg status "$status" --arg output "$output" \
      '. += [{ label: $label, enum: $enum, mode: $mode, status: $status, output: $output }]')
  fi
done

# Persist result for review.sh / orchestrator chaining
result_log="$LOG_DIR/last_parallel.json"
echo "$results" > "$result_log"
log "Results written to $result_log"

echo "$results"
echo "🐙 Hydra parallel: all $task_count tasks complete" >&2

[[ $any_failed -eq 0 ]] && exit 0 || exit 1
