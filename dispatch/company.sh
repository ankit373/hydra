#!/usr/bin/env bash
# Hydra Company Runner — dispatch/company.sh
# ─────────────────────────────────────────────────────────────────────────────
# Deterministic playbook manifest emitter. Does NOT invoke skills or models.
# The brain (Claude Code) is the runtime; this script tells it what to do next.
#
# Run directory layout (workspace-scoped):
#   <workspace>/.hydra/runs/<run_id>/
#     manifest.json      — full immutable plan (conditionals already pruned)
#     state.json         — { current_stage, status, completed, ... } mutable
#     stages/<NN>_<name>/
#       output/          — files the stage produced
#       meta.json        — { status, started_at, finished_at, findings, ... }
#       parallel/<label>/output/   (for parallel stages)
#
# Subcommands:
#   start <playbook> --intent "<...>"
#                    [--workspace <path>]      default: $PWD
#                    [--has-ui]                set has_ui=true
#                    [--dev-facing]            set dev_facing=true
#     → creates run dir, writes manifest.json + state.json
#     → prints the new run_id to stdout
#
#   next <run_id>
#     → prints next-stage JSON: { run_id, stage, inputs_paths, output_dir }
#     → marks the stage as in_progress in state.json
#     → if no stages remain, prints { done: true }
#     → conditional stages whose `when:` evaluates false are auto-skipped
#
#   complete <run_id> <stage_name>
#                    [--status ok|fail|skipped]    default: ok
#                    [--findings critical,high,...]
#                    [--note "<short note>"]
#     → writes stages/<NN>_<name>/meta.json
#     → advances state.json.current_stage
#     → if stage has block_on and findings intersect → status: blocked
#       (caller must intervene: company.sh resolve <run_id> ...)
#
#   status <run_id>
#     → human-readable progress table
#
#   resume <run_id>
#     → same as next; intended after blocked/abort
#
#   list [--workspace <path>]
#     → lists active and recent runs in the workspace
#
#   show <run_id>
#     → prints manifest.json + state.json
#
#   worklog <run_id>
#     → prints a markdown section for one run (intent, status, ticked stages)
#
#   ledger [--workspace <path>]
#     → regenerates <workspace>/HYDRA.md from all runs in the workspace
#     → grouped by status (Open / Blocked / Completed / Failed), newest first
#     → called automatically by start and complete
#
#   ticket <run_id> --ref <url|key> [--platform github|jira]
#     → sets the ticket ref on an existing run (after brain creates it)
#
#   ticket-comment <run_id> --body "<...>" [--dry-run]
#     → posts a comment to the run's linked ticket
#     → github: shells out to `gh issue comment`
#     → jira:   exits 2 with a brain-readable directive (use MCP addCommentToJiraIssue)
#     → exits 0 on success, 1 if no ticket linked, 2 if jira (brain finishes)
#
#   prune [--older-than 30d] [--workspace <path>] [--include-completed] [--dry-run]
#     → deletes finished runs (blocked/failed by default; +completed with flag)
#     → never deletes pending/in_progress runs
#     → also removes the runs.index entries
#
#   rotate-logs [--max-size 5M] [--keep 5]
#     → rotates ~/hydra/logs/*.log files larger than --max-size
#     → keeps last --keep generations, gzipping older ones
#
# Conventions:
#   run_id = YYYYMMDD-HHMM-<playbook-slug>-<rand4>
#   Resolves $PWD's enclosing workspace via scope.sh resolve.
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$HYDRA_DIR/logs"
LOG_FILE="$LOG_DIR/company.log"
RUNS_INDEX="$LOG_DIR/runs.index"
PLAYBOOKS_FILE="$HYDRA_DIR/registry/playbooks.yaml"
WORKFORCE_FILE="$HYDRA_DIR/registry/workforce.yaml"
SCOPE="$HYDRA_DIR/dispatch/scope.sh"

mkdir -p "$LOG_DIR"
log() { printf '[%s] company: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG_FILE"; }
err() { echo "❌ company: $*" >&2; log "ERROR: $*"; }

command -v yq >/dev/null || { err "yq not installed"; exit 1; }
command -v jq >/dev/null || { err "jq not installed"; exit 1; }
[[ -f "$PLAYBOOKS_FILE" ]] || { err "playbooks.yaml not found at $PLAYBOOKS_FILE"; exit 1; }

# ── Helpers ─────────────────────────────────────────────────────────────────

resolve_workspace() {
  local target="${1:-$PWD}"
  if [[ -x "$SCOPE" ]]; then
    local resolved
    resolved="$("$SCOPE" resolve "$target/.hydra-probe" 2>/dev/null || true)"
    if [[ -n "$resolved" ]]; then
      echo "$resolved" | jq -r '.root'
      return 0
    fi
  fi
  # Fallback: just use the provided directory
  echo "$target"
}

playbook_exists() {
  yq ".playbooks | has(\"$1\")" "$PLAYBOOKS_FILE" | grep -q true
}

# Slugify a playbook name (for run_id)
slug() { echo "$1" | tr '_' '-' | tr '[:upper:]' '[:lower:]' | cut -c1-12; }

gen_run_id() {
  local pb="$1"
  local ts; ts="$(date +%Y%m%d-%H%M)"
  local rand; rand="$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c4)"
  echo "${ts}-$(slug "$pb")-${rand}"
}

run_dir() {
  local ws="$1"; local run_id="$2"
  echo "$ws/.hydra/runs/$run_id"
}

# Evaluate a `when:` clause against the inputs object (JSON).
# Supports: bare boolean keys (has_ui, dev_facing), "always", or "" → true.
# Unknown stage-ref conditions (foo.has_findings) → true (let brain decide).
eval_when() {
  local condition="$1"; local inputs_json="$2"
  [[ -z "$condition" || "$condition" == "always" || "$condition" == "null" ]] && return 0
  if echo "$inputs_json" | jq -e --arg k "$condition" '.[$k] == true' >/dev/null; then
    return 0
  fi
  # If it's a known bool that's false → skip
  if echo "$inputs_json" | jq -e --arg k "$condition" 'has($k)' >/dev/null; then
    return 1
  fi
  # Unknown ref → let brain handle (don't auto-skip)
  return 0
}

# ── Subcommand: start ───────────────────────────────────────────────────────

cmd_start() {
  local playbook=""
  local intent=""
  local workspace=""
  local has_ui=false
  local dev_facing=false
  local ticket_mode=""         # none | existing | create_after_plan (empty = not specified)
  local ticket_mode_explicit=false
  local ticket_ref=""
  local ticket_platform=""
  local needs_design_system=false
  local market=""
  local parent_run=""

  [[ $# -lt 1 ]] && { err "start <playbook> --intent \"...\" [--workspace ...] [--has-ui] [--dev-facing] [--ticket-ref <url|key>] [--ticket-mode existing|create_after_plan|none] [--ticket-platform github|jira]"; exit 1; }
  playbook="$1"; shift

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --intent)           intent="$2"; shift 2 ;;
      --workspace)        workspace="$2"; shift 2 ;;
      --has-ui)           has_ui=true; shift ;;
      --dev-facing)       dev_facing=true; shift ;;
      --ticket-ref)            ticket_ref="$2"; ticket_mode="existing"; ticket_mode_explicit=true; shift 2 ;;
      --ticket-mode)           ticket_mode="$2"; ticket_mode_explicit=true; shift 2 ;;
      --ticket-platform)       ticket_platform="$2"; shift 2 ;;
      --needs-design-system)   needs_design_system=true; shift ;;
      --market)                market="$2"; shift 2 ;;
      --parent-run)            parent_run="$2"; shift 2 ;;
      *) err "unknown flag: $1"; exit 1 ;;
    esac
  done

  # Auto-detect platform from ref if not specified
  if [[ -n "$ticket_ref" && -z "$ticket_platform" ]]; then
    case "$ticket_ref" in
      *github.com*) ticket_platform="github" ;;
      *atlassian.net*) ticket_platform="jira" ;;
      *-*) ticket_platform="jira" ;;   # Jira keys like PROJ-123
      *) ticket_platform="unknown" ;;
    esac
  fi

  [[ -z "$intent" ]] && { err "--intent is required"; exit 1; }
  playbook_exists "$playbook" || { err "playbook '$playbook' not in $PLAYBOOKS_FILE"; exit 1; }

  # Enforce requires_ticket_choice: brain must explicitly pass --ticket-mode or --ticket-ref
  local requires_ticket; requires_ticket="$(yq ".playbooks.\"$playbook\".requires_ticket_choice // false" "$PLAYBOOKS_FILE")"
  if [[ "$requires_ticket" == "true" && "$ticket_mode_explicit" == "false" ]]; then
    err "playbook '$playbook' requires an explicit ticket choice."
    err "Ask the user about a tracking ticket, then pass one of:"
    err "  --ticket-ref <url|key>           (link existing GitHub/Jira ticket)"
    err "  --ticket-mode create_after_plan  (draft one after finalize_plan)"
    err "  --ticket-mode none               (user explicitly chose to skip)"
    exit 1
  fi
  [[ -z "$ticket_mode" ]] && ticket_mode="none"

  [[ -z "$workspace" ]] && workspace="$(resolve_workspace "$PWD")"
  [[ -d "$workspace" ]] || { err "workspace dir does not exist: $workspace"; exit 1; }

  local run_id; run_id="$(gen_run_id "$playbook")"
  local rd; rd="$(run_dir "$workspace" "$run_id")"
  mkdir -p "$rd/stages"

  local inputs_json
  inputs_json="$(jq -nc \
    --arg intent "$intent" \
    --argjson has_ui "$has_ui" \
    --argjson dev_facing "$dev_facing" \
    --argjson needs_design_system "$needs_design_system" \
    --arg market "$market" \
    --arg parent_run "$parent_run" \
    --arg ticket_mode "$ticket_mode" \
    --arg ticket_ref "$ticket_ref" \
    --arg ticket_platform "$ticket_platform" \
    '{intent: $intent, has_ui: $has_ui, dev_facing: $dev_facing, needs_design_system: $needs_design_system, market: $market, parent_run_id: (if $parent_run == "" then null else $parent_run end), ticket: {mode: $ticket_mode, ref: (if $ticket_ref == "" then null else $ticket_ref end), platform: (if $ticket_platform == "" then null else $ticket_platform end)}}')"

  # Build manifest by walking the playbook stages and pruning conditionals
  local stages_json
  stages_json="$(yq -o=json ".playbooks.\"$playbook\".stages" "$PLAYBOOKS_FILE")"

  local pruned_stages='[]'
  local i=0
  local n; n="$(echo "$stages_json" | jq 'length')"
  while [[ $i -lt $n ]]; do
    local stage; stage="$(echo "$stages_json" | jq ".[$i]")"
    local when; when="$(echo "$stage" | jq -r '.when // "always"')"
    if eval_when "$when" "$inputs_json"; then
      # Filter parallel items by their own `when:`
      if echo "$stage" | jq -e '.invocation.type == "parallel"' >/dev/null; then
        local items; items="$(echo "$stage" | jq '.invocation.items')"
        local filtered='[]'
        local j=0
        local m; m="$(echo "$items" | jq 'length')"
        while [[ $j -lt $m ]]; do
          local item; item="$(echo "$items" | jq ".[$j]")"
          local iwhen; iwhen="$(echo "$item" | jq -r '.when // "always"')"
          if eval_when "$iwhen" "$inputs_json"; then
            filtered="$(echo "$filtered" | jq --argjson it "$item" '. + [$it]')"
          fi
          j=$((j+1))
        done
        stage="$(echo "$stage" | jq --argjson f "$filtered" '.invocation.items = $f')"
      fi
      pruned_stages="$(echo "$pruned_stages" | jq --argjson s "$stage" '. + [$s]')"
    fi
    i=$((i+1))
  done

  # Assign sequential IDs to surviving stages
  pruned_stages="$(echo "$pruned_stages" | jq 'to_entries | map(.value + {id: (.key + 1)})')"

  local manifest
  manifest="$(jq -nc \
    --arg run_id "$run_id" \
    --arg playbook "$playbook" \
    --arg workspace "$workspace" \
    --arg created_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson inputs "$inputs_json" \
    --argjson stages "$pruned_stages" \
    '{run_id: $run_id, playbook: $playbook, workspace: $workspace, created_at: $created_at, inputs: $inputs, stages: $stages}')"

  echo "$manifest" | jq '.' > "$rd/manifest.json"

  local state
  state="$(jq -nc \
    --arg run_id "$run_id" \
    --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{run_id: $run_id, current_stage: 1, status: "pending", completed_stages: [], blocking_findings: null, updated_at: $updated_at}')"
  echo "$state" | jq '.' > "$rd/state.json"

  # Append to global runs index so `next`/`complete` can find non-registered workspaces
  printf '%s\t%s\n' "$run_id" "$workspace" >> "$RUNS_INDEX"

  # Update HYDRA.md ledger
  cmd_ledger --workspace "$workspace" >/dev/null 2>&1 || true

  log "started run $run_id (playbook=$playbook, workspace=$workspace)"
  echo "$run_id"
}

# ── Resolve a run directory from run_id (search known workspaces) ───────────

find_run_dir() {
  local run_id="$1"
  # 1. Try the global runs index (authoritative)
  if [[ -f "$RUNS_INDEX" ]]; then
    local ws
    ws="$(awk -v id="$run_id" -F'\t' '$1 == id {print $2; exit}' "$RUNS_INDEX")"
    if [[ -n "$ws" ]]; then
      local candidate="$ws/.hydra/runs/$run_id"
      [[ -d "$candidate" ]] && { echo "$candidate"; return 0; }
    fi
  fi
  # 2. Try registered workspaces from workspace.yaml
  if [[ -f "$HYDRA_DIR/registry/workspace.yaml" ]]; then
    local roots
    roots="$(yq '.workspaces | to_entries | .[].value.root' "$HYDRA_DIR/registry/workspace.yaml" | tr -d '"')"
    while IFS= read -r ws; do
      [[ -z "$ws" ]] && continue
      local candidate="$ws/.hydra/runs/$run_id"
      [[ -d "$candidate" ]] && { echo "$candidate"; return 0; }
    done <<< "$roots"
  fi
  # 3. Fallback: $PWD
  local candidate="$PWD/.hydra/runs/$run_id"
  [[ -d "$candidate" ]] && { echo "$candidate"; return 0; }
  return 1
}

# ── Subcommand: next ────────────────────────────────────────────────────────

cmd_next() {
  local run_id="${1:-}"
  [[ -z "$run_id" ]] && { err "next <run_id>"; exit 1; }
  local rd; rd="$(find_run_dir "$run_id")" || { err "run $run_id not found"; exit 1; }

  local state; state="$(cat "$rd/state.json")"
  local status; status="$(echo "$state" | jq -r '.status')"
  if [[ "$status" == "blocked" ]]; then
    err "run $run_id is blocked: $(echo "$state" | jq -c '.blocking_findings')"
    exit 2
  fi

  local manifest; manifest="$(cat "$rd/manifest.json")"
  local current; current="$(echo "$state" | jq -r '.current_stage')"
  local total; total="$(echo "$manifest" | jq '.stages | length')"

  if [[ "$current" -gt "$total" ]]; then
    echo '{"done": true}'
    return 0
  fi

  local stage; stage="$(echo "$manifest" | jq ".stages[$((current-1))]")"
  local stage_name; stage_name="$(echo "$stage" | jq -r '.name')"
  local stage_id; stage_id="$(printf "%02d" "$current")"
  local stage_dir="$rd/stages/${stage_id}_${stage_name}"
  mkdir -p "$stage_dir/output"

  # Resolve inputs_from to absolute paths
  local inputs_paths='[]'
  local inputs_from; inputs_from="$(echo "$stage" | jq -c '.inputs_from // []')"
  if [[ "$inputs_from" != "[]" && "$inputs_from" != "null" ]]; then
    local k=0
    local kn; kn="$(echo "$inputs_from" | jq 'length')"
    while [[ $k -lt $kn ]]; do
      local ref; ref="$(echo "$inputs_from" | jq -r ".[$k]")"
      if [[ "$ref" == "ALL" ]]; then
        inputs_paths="$(echo "$inputs_paths" | jq --arg p "$rd/stages" '. + [$p]')"
      else
        # Find the stage dir matching ref name
        local match; match="$(ls -d "$rd/stages"/*_"$ref" 2>/dev/null | head -1 || true)"
        if [[ -n "$match" ]]; then
          inputs_paths="$(echo "$inputs_paths" | jq --arg p "$match" '. + [$p]')"
        fi
      fi
      k=$((k+1))
    done
  fi

  # Mark stage in_progress
  local now; now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "{\"status\": \"in_progress\", \"started_at\": \"$now\"}" | jq '.' > "$stage_dir/meta.json"
  echo "$state" | jq --arg now "$now" '.status = "in_progress" | .updated_at = $now' > "$rd/state.json.tmp"
  mv "$rd/state.json.tmp" "$rd/state.json"

  jq -nc \
    --arg run_id "$run_id" \
    --arg output_dir "$stage_dir/output" \
    --arg stage_dir "$stage_dir" \
    --argjson stage "$stage" \
    --argjson inputs_paths "$inputs_paths" \
    --argjson run_inputs "$(echo "$manifest" | jq '.inputs')" \
    '{run_id: $run_id, stage: $stage, stage_dir: $stage_dir, output_dir: $output_dir, inputs_paths: $inputs_paths, run_inputs: $run_inputs}' \
    | jq '.'
}

# ── Subcommand: complete ────────────────────────────────────────────────────

cmd_complete() {
  local run_id="${1:-}"; local stage_name="${2:-}"
  [[ -z "$run_id" || -z "$stage_name" ]] && { err "complete <run_id> <stage_name> [--status ...] [--findings ...] [--note ...]"; exit 1; }
  shift 2

  local status="ok"; local findings=""; local note=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --status)   status="$2"; shift 2 ;;
      --findings) findings="$2"; shift 2 ;;
      --note)     note="$2"; shift 2 ;;
      *) err "unknown flag: $1"; exit 1 ;;
    esac
  done

  local rd; rd="$(find_run_dir "$run_id")" || { err "run $run_id not found"; exit 1; }
  local manifest; manifest="$(cat "$rd/manifest.json")"
  local state; state="$(cat "$rd/state.json")"

  # Locate the stage by name
  local stage; stage="$(echo "$manifest" | jq --arg n "$stage_name" '.stages[] | select(.name == $n)')"
  [[ -z "$stage" ]] && { err "no stage '$stage_name' in run $run_id"; exit 1; }
  local stage_id; stage_id="$(echo "$stage" | jq -r '.id')"
  local stage_id_padded; stage_id_padded="$(printf "%02d" "$stage_id")"
  local stage_dir="$rd/stages/${stage_id_padded}_${stage_name}"
  mkdir -p "$stage_dir"

  local now; now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  # Write meta.json for the stage
  local findings_json='[]'
  if [[ -n "$findings" ]]; then
    findings_json="$(echo "$findings" | jq -Rc 'split(",")')"
  fi
  jq -nc \
    --arg status "$status" \
    --arg note "$note" \
    --arg finished_at "$now" \
    --argjson findings "$findings_json" \
    '{status: $status, note: $note, finished_at: $finished_at, findings: $findings}' \
    | jq '.' > "$stage_dir/meta.json"

  # Check block_on
  local block_on; block_on="$(echo "$stage" | jq -c '.block_on // []')"
  local should_block=false
  if [[ "$block_on" != "[]" && "$findings_json" != "[]" ]]; then
    local intersection
    intersection="$(jq -nc --argjson a "$block_on" --argjson b "$findings_json" '$a - ($a - $b)')"
    if [[ "$intersection" != "[]" ]]; then
      should_block=true
    fi
  fi

  if $should_block; then
    state="$(echo "$state" | jq \
      --arg now "$now" \
      --arg stage "$stage_name" \
      --argjson findings "$findings_json" \
      '.status = "blocked" | .blocking_findings = {stage: $stage, findings: $findings} | .updated_at = $now')"
    echo "$state" | jq '.' > "$rd/state.json"
    local ws; ws="$(jq -r '.workspace' "$rd/manifest.json")"
    cmd_ledger --workspace "$ws" >/dev/null 2>&1 || true
    log "run $run_id BLOCKED at $stage_name with findings: $findings_json"
    echo "$state" | jq '.'
    return 0
  fi

  # Advance
  state="$(echo "$state" | jq \
    --arg now "$now" \
    --arg stage "$stage_name" \
    --arg status "$status" \
    '.completed_stages += [{name: $stage, status: $status}] | .current_stage += 1 | .status = "pending" | .updated_at = $now')"
  echo "$state" | jq '.' > "$rd/state.json"

  # Update HYDRA.md ledger
  local ws; ws="$(jq -r '.workspace' "$rd/manifest.json")"
  cmd_ledger --workspace "$ws" >/dev/null 2>&1 || true

  log "run $run_id stage $stage_name complete (status=$status)"
  echo "$state" | jq '.'
}

# ── Subcommand: status ──────────────────────────────────────────────────────

cmd_status() {
  local run_id="${1:-}"
  [[ -z "$run_id" ]] && { err "status <run_id>"; exit 1; }
  local rd; rd="$(find_run_dir "$run_id")" || { err "run $run_id not found"; exit 1; }
  local manifest; manifest="$(cat "$rd/manifest.json")"
  local state; state="$(cat "$rd/state.json")"

  echo "Run:       $(echo "$manifest" | jq -r '.run_id')"
  echo "Playbook:  $(echo "$manifest" | jq -r '.playbook')"
  echo "Workspace: $(echo "$manifest" | jq -r '.workspace')"
  echo "Status:    $(echo "$state" | jq -r '.status')"
  echo "Progress:  $(echo "$state" | jq -r '.current_stage')/$(echo "$manifest" | jq '.stages | length')"
  echo ""
  echo "Stages:"
  local i=1
  local total; total="$(echo "$manifest" | jq '.stages | length')"
  while [[ $i -le $total ]]; do
    local s; s="$(echo "$manifest" | jq ".stages[$((i-1))]")"
    local name; name="$(echo "$s" | jq -r '.name')"
    local invok; invok="$(echo "$s" | jq -r '.invocation.type')"
    local gate; gate="$(echo "$s" | jq -r '.gate // "auto"')"
    local mark="·"
    local completed; completed="$(echo "$state" | jq --arg n "$name" '.completed_stages[] | select(.name == $n) | .status' 2>/dev/null | tr -d '"' || true)"
    if [[ -n "$completed" ]]; then
      case "$completed" in
        ok)      mark="✓" ;;
        skipped) mark="∅" ;;
        fail)    mark="✗" ;;
        *)       mark="$completed" ;;
      esac
    elif [[ "$i" -eq "$(echo "$state" | jq -r '.current_stage')" ]]; then
      mark="▶"
    fi
    printf "  %s %2d. %-22s (%s, %s)\n" "$mark" "$i" "$name" "$invok" "$gate"
    i=$((i+1))
  done

  local blocking; blocking="$(echo "$state" | jq -c '.blocking_findings')"
  if [[ "$blocking" != "null" ]]; then
    echo ""
    echo "🚫 BLOCKED: $blocking"
  fi
}

# ── Subcommand: list ────────────────────────────────────────────────────────

cmd_list() {
  local workspace=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --workspace) workspace="$2"; shift 2 ;;
      *) err "unknown flag: $1"; exit 1 ;;
    esac
  done
  [[ -z "$workspace" ]] && workspace="$(resolve_workspace "$PWD")"
  local runs_dir="$workspace/.hydra/runs"
  if [[ ! -d "$runs_dir" ]]; then
    echo "(no runs in $workspace)"
    return 0
  fi
  echo "Runs in $workspace:"
  for d in "$runs_dir"/*/; do
    [[ -d "$d" ]] || continue
    local rid; rid="$(basename "$d")"
    local st="?"
    [[ -f "$d/state.json" ]] && st="$(jq -r '.status' "$d/state.json")"
    local pb="?"
    [[ -f "$d/manifest.json" ]] && pb="$(jq -r '.playbook' "$d/manifest.json")"
    printf "  %s  %-22s  %s\n" "$st" "$pb" "$rid"
  done
}

# ── Subcommand: worklog ─────────────────────────────────────────────────────
# Emits a markdown section for one run. Used by `ledger`.

cmd_worklog() {
  local run_id="${1:-}"
  [[ -z "$run_id" ]] && { err "worklog <run_id>"; exit 1; }
  local rd; rd="$(find_run_dir "$run_id")" || { err "run $run_id not found"; exit 1; }
  local manifest; manifest="$(cat "$rd/manifest.json")"
  local state; state="$(cat "$rd/state.json")"

  local playbook intent created_at status current total
  playbook="$(echo "$manifest" | jq -r '.playbook')"
  intent="$(echo "$manifest" | jq -r '.inputs.intent')"
  created_at="$(echo "$manifest" | jq -r '.created_at')"
  status="$(echo "$state" | jq -r '.status')"
  current="$(echo "$state" | jq -r '.current_stage')"
  total="$(echo "$manifest" | jq '.stages | length')"

  local date_short; date_short="${created_at:0:10}"
  local status_emoji
  case "$status" in
    pending)     status_emoji="🟢" ;;
    in_progress) status_emoji="🟢" ;;
    blocked)     status_emoji="🚫" ;;
    completed)   status_emoji="✅" ;;
    failed)      status_emoji="✗" ;;
    *)           status_emoji="·" ;;
  esac

  # Derive whether all stages done → completed
  if [[ "$current" -gt "$total" ]]; then
    status="completed"
    status_emoji="✅"
  fi

  # Ticket info (if any)
  local ticket_mode ticket_ref ticket_platform
  ticket_mode="$(echo "$manifest" | jq -r '.inputs.ticket.mode // "none"')"
  ticket_ref="$(echo "$manifest" | jq -r '.inputs.ticket.ref // ""')"
  ticket_platform="$(echo "$manifest" | jq -r '.inputs.ticket.platform // ""')"

  local parent; parent="$(echo "$manifest" | jq -r '.inputs.parent_run_id // ""')"

  echo "### $date_short — $playbook"
  echo ""
  echo "**Intent:** $intent  "
  echo "**Run:** \`$run_id\`  "
  echo "**Status:** $status_emoji $status ($current/$total)  "
  if [[ -n "$parent" && "$parent" != "null" ]]; then
    echo "**Parent run:** \`$parent\`  "
  fi
  if [[ -n "$ticket_ref" && "$ticket_ref" != "null" ]]; then
    echo "**Ticket:** $ticket_ref _($ticket_platform)_  "
  elif [[ "$ticket_mode" == "create_after_plan" ]]; then
    echo "**Ticket:** _(will draft after finalize_plan)_  "
  fi
  echo "**Artifacts:** \`.hydra/runs/$run_id/\`"
  echo ""

  # Blocking note
  local blocking; blocking="$(echo "$state" | jq -c '.blocking_findings')"
  if [[ "$blocking" != "null" ]]; then
    local bstage bfindings
    bstage="$(echo "$blocking" | jq -r '.stage')"
    bfindings="$(echo "$blocking" | jq -r '.findings | join(", ")')"
    echo "> 🚫 **Blocked at \`$bstage\`** — findings: $bfindings"
    echo ""
  fi

  # Children (other runs whose parent_run_id == this run_id)
  local workspace_for_children; workspace_for_children="$(echo "$manifest" | jq -r '.workspace')"
  local children_runs_dir="$workspace_for_children/.hydra/runs"
  if [[ -d "$children_runs_dir" ]]; then
    local child_lines=""
    for d in "$children_runs_dir"/*/; do
      [[ -f "$d/manifest.json" ]] || continue
      local pid; pid="$(jq -r '.inputs.parent_run_id // ""' "$d/manifest.json")"
      [[ "$pid" == "$run_id" ]] || continue
      local crid; crid="$(basename "$d")"
      local cst ccur ctotal cintent
      cst="$(jq -r '.status' "$d/state.json")"
      ccur="$(jq -r '.current_stage' "$d/state.json")"
      ctotal="$(jq '.stages | length' "$d/manifest.json")"
      cintent="$(jq -r '.inputs.intent' "$d/manifest.json")"
      local cmark="·"
      case "$cst" in
        pending|in_progress) cmark="🟢" ;;
        blocked)             cmark="🚫" ;;
      esac
      [[ "$ccur" -gt "$ctotal" ]] && cmark="✅"
      child_lines+="- $cmark \`$crid\` ($ccur/$ctotal) — ${cintent:0:80}"$'\n'
    done
    if [[ -n "$child_lines" ]]; then
      echo "**Spawned ship runs:**"
      echo "$child_lines"
    fi
  fi

  echo "**Stages:**"
  local i=1
  while [[ $i -le $total ]]; do
    local s; s="$(echo "$manifest" | jq ".stages[$((i-1))]")"
    local name invok
    name="$(echo "$s" | jq -r '.name')"
    invok="$(echo "$s" | jq -r '.invocation.type')"

    # Look up completion status
    local completed_entry; completed_entry="$(echo "$state" | jq -c --arg n "$name" '.completed_stages[] | select(.name == $n)')"
    local mark="[ ]"
    local note=""
    if [[ -n "$completed_entry" && "$completed_entry" != "null" ]]; then
      local cs; cs="$(echo "$completed_entry" | jq -r '.status')"
      case "$cs" in
        ok)      mark="[x]" ;;
        skipped) mark="[~]"; note=" _(skipped)_" ;;
        fail)    mark="[!]"; note=" _(failed)_" ;;
        *)       mark="[?]" ;;
      esac
      # Pull findings/note from stage meta.json if present
      local stage_id_padded; stage_id_padded="$(printf "%02d" "$i")"
      local meta_file="$rd/stages/${stage_id_padded}_${name}/meta.json"
      if [[ -f "$meta_file" ]]; then
        local mnote mfindings
        mnote="$(jq -r '.note // ""' "$meta_file")"
        mfindings="$(jq -r '.findings // [] | join(", ")' "$meta_file")"
        [[ -n "$mnote" ]] && note="$note — $mnote"
        [[ -n "$mfindings" ]] && note="$note · findings: $mfindings"
      fi
    elif [[ "$i" -eq "$current" && "$status" == "in_progress" ]]; then
      mark="[▶]"; note=" _(in progress)_"
    fi

    # Parallel sub-items
    if [[ "$invok" == "parallel" ]]; then
      local items; items="$(echo "$s" | jq -r '.invocation.items | map(.label) | join(", ")')"
      echo "- $mark **$name** _($items)_$note"
    else
      echo "- $mark **$name**$note"
    fi
    i=$((i+1))
  done
  echo ""
}

# ── Subcommand: ledger ──────────────────────────────────────────────────────
# Regenerates <workspace>/HYDRA.md from all runs in the workspace.

cmd_ledger() {
  local workspace=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --workspace) workspace="$2"; shift 2 ;;
      *) err "unknown flag: $1"; exit 1 ;;
    esac
  done
  [[ -z "$workspace" ]] && workspace="$(resolve_workspace "$PWD")"
  local runs_dir="$workspace/.hydra/runs"
  local out="$workspace/HYDRA.md"

  # Group run_ids by status (newest first within each group)
  declare -a open=() blocked=() completed=() failed=()
  if [[ -d "$runs_dir" ]]; then
    # Sort by run_id descending (run_id starts with YYYYMMDD-HHMM so lexicographic = chronological)
    for d in $(ls -1 "$runs_dir" 2>/dev/null | sort -r); do
      [[ -d "$runs_dir/$d" ]] || continue
      [[ -f "$runs_dir/$d/state.json" && -f "$runs_dir/$d/manifest.json" ]] || continue
      local st cur total
      st="$(jq -r '.status' "$runs_dir/$d/state.json")"
      cur="$(jq -r '.current_stage' "$runs_dir/$d/state.json")"
      total="$(jq '.stages | length' "$runs_dir/$d/manifest.json")"
      if [[ "$st" == "blocked" ]]; then
        blocked+=("$d")
      elif [[ "$cur" -gt "$total" ]]; then
        completed+=("$d")
      elif [[ "$st" == "failed" ]]; then
        failed+=("$d")
      else
        open+=("$d")
      fi
    done
  fi

  {
    echo "# Hydra worklog — $(basename "$workspace")"
    echo ""
    echo "_Auto-maintained by \`~/hydra/dispatch/company.sh\`. Each run gets a section below._"
    echo "_Do not hand-edit between \`<!-- HYDRA-AUTO -->\` markers — they get overwritten._"
    echo ""
    echo "<!-- HYDRA-AUTO -->"
    echo ""

    if [[ ${#open[@]} -gt 0 ]]; then
      echo "## 🟢 Open"
      echo ""
      for r in "${open[@]}"; do cmd_worklog "$r"; done
    fi

    if [[ ${#blocked[@]} -gt 0 ]]; then
      echo "## 🚫 Blocked"
      echo ""
      for r in "${blocked[@]}"; do cmd_worklog "$r"; done
    fi

    if [[ ${#completed[@]} -gt 0 ]]; then
      echo "## ✅ Completed"
      echo ""
      for r in "${completed[@]}"; do cmd_worklog "$r"; done
    fi

    if [[ ${#failed[@]} -gt 0 ]]; then
      echo "## ✗ Failed"
      echo ""
      for r in "${failed[@]}"; do cmd_worklog "$r"; done
    fi

    if [[ ${#open[@]} -eq 0 && ${#blocked[@]} -eq 0 && ${#completed[@]} -eq 0 && ${#failed[@]} -eq 0 ]]; then
      echo "_(no runs yet)_"
      echo ""
    fi

    echo "<!-- /HYDRA-AUTO -->"
  } > "$out"

  log "ledger updated: $out"
  echo "$out"
}

# ── Subcommand: ticket ──────────────────────────────────────────────────────
# Set or update the ticket ref on an existing run (used after brain creates
# a ticket post-finalize_plan).

cmd_ticket() {
  local run_id="${1:-}"
  [[ -z "$run_id" ]] && { err "ticket <run_id> --ref <url|key> [--platform github|jira]"; exit 1; }
  shift
  local ref="" platform=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --ref)      ref="$2"; shift 2 ;;
      --platform) platform="$2"; shift 2 ;;
      *) err "unknown flag: $1"; exit 1 ;;
    esac
  done
  [[ -z "$ref" ]] && { err "--ref required"; exit 1; }

  local rd; rd="$(find_run_dir "$run_id")" || { err "run $run_id not found"; exit 1; }

  if [[ -z "$platform" ]]; then
    case "$ref" in
      *github.com*)    platform="github" ;;
      *atlassian.net*) platform="jira" ;;
      *-*)             platform="jira" ;;
      *)               platform="unknown" ;;
    esac
  fi

  jq --arg ref "$ref" --arg platform "$platform" \
    '.inputs.ticket.ref = $ref | .inputs.ticket.platform = $platform | .inputs.ticket.mode = "existing"' \
    "$rd/manifest.json" > "$rd/manifest.json.tmp"
  mv "$rd/manifest.json.tmp" "$rd/manifest.json"

  local ws; ws="$(jq -r '.workspace' "$rd/manifest.json")"
  cmd_ledger --workspace "$ws" >/dev/null 2>&1 || true
  log "run $run_id ticket set: $ref ($platform)"
  echo "ticket updated: $ref ($platform)"
}

# ── Subcommand: ticket-comment ──────────────────────────────────────────────
# Post a comment to the run's linked ticket. GitHub: shells out to `gh`.
# Jira: emits a brain directive (the brain must call MCP addCommentToJiraIssue).

cmd_ticket_comment() {
  local run_id="${1:-}"
  [[ -z "$run_id" ]] && { err "ticket-comment <run_id> --body \"<...>\" [--dry-run]"; exit 1; }
  shift
  local body="" dry_run=false
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --body)    body="$2"; shift 2 ;;
      --dry-run) dry_run=true; shift ;;
      *) err "unknown flag: $1"; exit 1 ;;
    esac
  done
  [[ -z "$body" ]] && { err "--body required"; exit 1; }

  local rd; rd="$(find_run_dir "$run_id")" || { err "run $run_id not found"; exit 1; }
  local ref platform
  ref="$(jq -r '.inputs.ticket.ref // ""' "$rd/manifest.json")"
  platform="$(jq -r '.inputs.ticket.platform // ""' "$rd/manifest.json")"

  if [[ -z "$ref" || "$ref" == "null" ]]; then
    err "run $run_id has no linked ticket"
    exit 1
  fi

  case "$platform" in
    github)
      if $dry_run; then
        echo "[dry-run] gh issue comment $ref --body \"...\""
        return 0
      fi
      if ! command -v gh >/dev/null; then
        err "gh CLI not installed — cannot comment on github ticket"
        exit 1
      fi
      gh issue comment -- "$ref" --body "$body" >/dev/null
      log "run $run_id commented on $ref"
      echo "commented on $ref"
      ;;
    jira)
      # Bash can't reach MCP directly — emit a directive for the brain
      jq -nc \
        --arg ref "$ref" \
        --arg body "$body" \
        --arg run "$run_id" \
        '{action: "jira_comment", ticket: $ref, body: $body, run_id: $run, mcp_tool: "addCommentToJiraIssue"}' \
        | jq '.'
      exit 2   # signals the brain to finish via MCP
      ;;
    *)
      err "unsupported platform: $platform"
      exit 1
      ;;
  esac
}

# ── Subcommand: prune ───────────────────────────────────────────────────────
# Delete finished runs older than a threshold. Never touches open runs.

parse_duration() {
  # Accepts: 30d, 12h, 90m. Outputs seconds.
  local s="$1"
  local n="${s%[dhm]}"
  local u="${s: -1}"
  case "$u" in
    d) echo $((n * 86400)) ;;
    h) echo $((n * 3600)) ;;
    m) echo $((n * 60)) ;;
    *) echo "$s" ;;  # assume already seconds
  esac
}

cmd_prune() {
  local workspace=""
  local older_than="30d"
  local include_completed=false
  local dry_run=false
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --older-than)        older_than="$2"; shift 2 ;;
      --workspace)         workspace="$2"; shift 2 ;;
      --include-completed) include_completed=true; shift ;;
      --dry-run)           dry_run=true; shift ;;
      *) err "unknown flag: $1"; exit 1 ;;
    esac
  done
  [[ -z "$workspace" ]] && workspace="$(resolve_workspace "$PWD")"
  local runs_dir="$workspace/.hydra/runs"
  [[ -d "$runs_dir" ]] || { echo "(no runs dir)"; return 0; }

  local cutoff_secs; cutoff_secs="$(parse_duration "$older_than")"
  local now_epoch; now_epoch="$(date +%s)"
  local cutoff_epoch=$((now_epoch - cutoff_secs))

  local kept=0 pruned=0
  declare -a to_prune=()

  for d in "$runs_dir"/*/; do
    [[ -d "$d" ]] || continue
    local rid; rid="$(basename "$d")"
    [[ -f "$d/state.json" && -f "$d/manifest.json" ]] || continue

    local st cur total updated_at
    st="$(jq -r '.status' "$d/state.json")"
    cur="$(jq -r '.current_stage' "$d/state.json")"
    total="$(jq '.stages | length' "$d/manifest.json")"
    updated_at="$(jq -r '.updated_at' "$d/state.json")"

    # Never prune open runs
    if [[ "$st" == "pending" || "$st" == "in_progress" ]] && [[ "$cur" -le "$total" ]]; then
      kept=$((kept+1)); continue
    fi

    # Completed runs only with explicit flag
    local is_completed=false
    [[ "$cur" -gt "$total" ]] && is_completed=true
    if $is_completed && ! $include_completed; then
      kept=$((kept+1)); continue
    fi

    # Age check (BSD date and GNU date both work for ISO-8601 UTC)
    local up_epoch
    if up_epoch="$(date -u -j -f "%Y-%m-%dT%H:%M:%SZ" "$updated_at" "+%s" 2>/dev/null)"; then
      :
    elif up_epoch="$(date -u -d "$updated_at" "+%s" 2>/dev/null)"; then
      :
    else
      up_epoch="$now_epoch"   # if parse fails, treat as fresh (safe)
    fi

    if [[ "$up_epoch" -lt "$cutoff_epoch" ]]; then
      to_prune+=("$rid")
    else
      kept=$((kept+1))
    fi
  done

  if [[ ${#to_prune[@]} -eq 0 ]]; then
    echo "Nothing to prune (kept $kept)."
    return 0
  fi

  for rid in "${to_prune[@]}"; do
    if $dry_run; then
      echo "[dry-run] would delete $runs_dir/$rid"
    else
      rm -rf "$runs_dir/$rid"
      # Remove from runs.index
      if [[ -f "$RUNS_INDEX" ]]; then
        grep -v "^$rid	" "$RUNS_INDEX" > "$RUNS_INDEX.tmp" || true
        mv "$RUNS_INDEX.tmp" "$RUNS_INDEX"
      fi
      log "pruned run $rid"
    fi
    pruned=$((pruned+1))
  done

  if ! $dry_run; then
    cmd_ledger --workspace "$workspace" >/dev/null 2>&1 || true
  fi
  echo "Pruned $pruned runs (kept $kept)$($dry_run && echo ' [dry-run]' || true)."
}

# ── Subcommand: rotate-logs ─────────────────────────────────────────────────
# Rotate ~/hydra/logs/*.log files that exceed --max-size. Keeps --keep
# generations, gzipping older ones.

cmd_rotate_logs() {
  local max_size_str="5M"
  local keep=5
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --max-size) max_size_str="$2"; shift 2 ;;
      --keep)     keep="$2"; shift 2 ;;
      *) err "unknown flag: $1"; exit 1 ;;
    esac
  done

  # Convert size to bytes
  local max_bytes
  case "$max_size_str" in
    *K|*k) max_bytes=$(( ${max_size_str%[Kk]} * 1024 )) ;;
    *M|*m) max_bytes=$(( ${max_size_str%[Mm]} * 1024 * 1024 )) ;;
    *G|*g) max_bytes=$(( ${max_size_str%[Gg]} * 1024 * 1024 * 1024 )) ;;
    *)     max_bytes="$max_size_str" ;;
  esac

  local rotated=0
  for log in "$LOG_DIR"/*.log; do
    [[ -f "$log" ]] || continue
    local size
    size="$(stat -f%z "$log" 2>/dev/null || stat -c%s "$log" 2>/dev/null || echo 0)"
    [[ "$size" -lt "$max_bytes" ]] && continue

    # Shift existing rotations: log.N → log.(N+1).gz (or delete if past --keep)
    for ((i=keep; i>=1; i--)); do
      local cur="$log.$i.gz"
      local nxt="$log.$((i+1)).gz"
      if [[ -f "$cur" ]]; then
        if [[ "$i" -ge "$keep" ]]; then
          rm -f "$cur"
        else
          mv "$cur" "$nxt"
        fi
      fi
    done

    # Current → .1, gzip it
    mv "$log" "$log.1"
    gzip "$log.1" 2>/dev/null && : > "$log" || mv "$log.1" "$log"
    rotated=$((rotated+1))
    log "rotated $log"
  done

  echo "Rotated $rotated log files (>$max_size_str, keep $keep)."
}

# ── Subcommand: children ────────────────────────────────────────────────────
# List all runs whose inputs.parent_run_id == <parent>. Useful for tracking
# a fanout — e.g. all ship_a_feature runs spawned from one business_to_design.

cmd_children() {
  local parent="${1:-}"
  [[ -z "$parent" ]] && { err "children <parent_run_id>"; exit 1; }
  local parent_rd; parent_rd="$(find_run_dir "$parent")" || { err "parent run $parent not found"; exit 1; }
  local workspace; workspace="$(jq -r '.workspace' "$parent_rd/manifest.json")"
  local runs_dir="$workspace/.hydra/runs"
  [[ -d "$runs_dir" ]] || return 0

  printf "Children of %s:\n" "$parent"
  local n=0
  for d in "$runs_dir"/*/; do
    [[ -f "$d/manifest.json" ]] || continue
    local pid; pid="$(jq -r '.inputs.parent_run_id // ""' "$d/manifest.json")"
    [[ "$pid" == "$parent" ]] || continue
    local rid; rid="$(basename "$d")"
    local st cur total intent pb
    st="$(jq -r '.status' "$d/state.json")"
    cur="$(jq -r '.current_stage' "$d/state.json")"
    total="$(jq '.stages | length' "$d/manifest.json")"
    intent="$(jq -r '.inputs.intent' "$d/manifest.json")"
    pb="$(jq -r '.playbook' "$d/manifest.json")"
    printf "  %-10s %2s/%-2s  %-22s  %s  %.80s\n" "$st" "$cur" "$total" "$pb" "$rid" "$intent"
    n=$((n+1))
  done
  [[ "$n" -eq 0 ]] && echo "  (no children)"
}

# ── Subcommand: fanout-ships ────────────────────────────────────────────────
# Read a feature backlog JSON and spawn parallel ship_a_feature runs, all
# linked to <parent_run_id>. The backlog format is documented in SKILL.md.
#
# Backlog format (array under .features):
#   [{ id, title, intent, priority, has_ui?, dev_facing?, ticket_ref?, ticket_mode?, rationale? }]

cmd_fanout_ships() {
  local parent="${1:-}"
  [[ -z "$parent" ]] && { err "fanout-ships <parent_run_id> --from <backlog.json> [--filter <jq>] [--max N] [--dry-run]"; exit 1; }
  shift
  local backlog_file=""
  local filter=""
  local max=999
  local dry_run=false
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --from)    backlog_file="$2"; shift 2 ;;
      --filter)  filter="$2"; shift 2 ;;
      --max)     max="$2"; shift 2 ;;
      --dry-run) dry_run=true; shift ;;
      *) err "unknown flag: $1"; exit 1 ;;
    esac
  done
  [[ -z "$backlog_file" ]] && { err "--from <backlog.json> required"; exit 1; }
  [[ -f "$backlog_file" ]] || { err "backlog file not found: $backlog_file"; exit 1; }

  local parent_rd; parent_rd="$(find_run_dir "$parent")" || { err "parent run $parent not found"; exit 1; }
  local workspace; workspace="$(jq -r '.workspace' "$parent_rd/manifest.json")"

  # Apply filter if provided, otherwise select all features.
  # --filter accepts a raw jq expression and must be trusted input (orchestrator-supplied).
  # Validate it contains only alphanumeric identifiers, dots, quotes, spaces, and comparison
  # operators to prevent env/path/shell injections from attacker-controlled backlog files.
  local features
  if [[ -n "$filter" ]]; then
    if ! [[ "$filter" =~ ^[A-Za-z0-9_.\ \"\'=!<>|]+$ ]]; then
      err "fanout-ships: --filter contains unsafe characters: $filter"
      exit 1
    fi
    features="$(jq -c --argjson dummy null ".features // [] | map(select($filter))" "$backlog_file")"
  else
    features="$(jq -c '.features // []' "$backlog_file")"
  fi
  local count; count="$(echo "$features" | jq 'length')"
  [[ "$count" -gt "$max" ]] && features="$(echo "$features" | jq --argjson m "$max" '.[:$m]')"
  count="$(echo "$features" | jq 'length')"

  if [[ "$count" -eq 0 ]]; then
    echo "(no features match the filter)"
    return 0
  fi

  echo "Fanning out $count ship_a_feature runs (parent=$parent)..."
  local i=0
  local results='[]'
  while [[ $i -lt $count ]]; do
    local f; f="$(echo "$features" | jq ".[$i]")"
    local intent; intent="$(echo "$f" | jq -r '.intent')"
    local title;  title="$(echo "$f" | jq -r '.title // .id // "(no title)"')"
    local has_ui;     has_ui="$(echo "$f" | jq -r '.has_ui // false')"
    local dev_facing; dev_facing="$(echo "$f" | jq -r '.dev_facing // false')"
    local tk_mode;    tk_mode="$(echo "$f" | jq -r '.ticket_mode // "none"')"
    local tk_ref;     tk_ref="$(echo "$f" | jq -r '.ticket_ref // ""')"

    local args=( ship_a_feature --intent "$intent" --workspace "$workspace" --parent-run "$parent" )
    if [[ "$has_ui" == "true" ]];     then args+=( --has-ui );     fi
    if [[ "$dev_facing" == "true" ]]; then args+=( --dev-facing ); fi
    if [[ -n "$tk_ref" && "$tk_ref" != "null" ]]; then
      args+=( --ticket-ref "$tk_ref" )
    else
      args+=( --ticket-mode "$tk_mode" )
    fi

    if $dry_run; then
      printf "  [dry-run] would start: %s\n    intent: %s\n" "$title" "$intent"
    else
      local new_id; new_id="$("$0" start "${args[@]}")"
      printf "  ✓ %-30s → %s\n" "$title" "$new_id"
      results="$(echo "$results" | jq --arg id "$new_id" --arg t "$title" '. + [{run_id: $id, title: $t}]')"
    fi
    i=$((i+1))
  done

  ! $dry_run && echo "$results" | jq '.'
}

# ── Subcommand: show ────────────────────────────────────────────────────────

cmd_show() {
  local run_id="${1:-}"
  [[ -z "$run_id" ]] && { err "show <run_id>"; exit 1; }
  local rd; rd="$(find_run_dir "$run_id")" || { err "run $run_id not found"; exit 1; }
  echo "=== manifest ==="
  jq '.' "$rd/manifest.json"
  echo "=== state ==="
  jq '.' "$rd/state.json"
}

# ── Main ────────────────────────────────────────────────────────────────────

usage() {
  cat <<EOF
Hydra Company Runner

Usage:
  company.sh start <playbook> --intent "<...>" [--workspace <path>] [--has-ui] [--dev-facing]
  company.sh next <run_id>
  company.sh complete <run_id> <stage_name> [--status ok|fail|skipped] [--findings critical,high,...] [--note "<...>"]
  company.sh status <run_id>
  company.sh resume <run_id>
  company.sh list [--workspace <path>]
  company.sh show <run_id>

Playbooks defined in $PLAYBOOKS_FILE
EOF
}

[[ $# -lt 1 ]] && { usage; exit 1; }

cmd="$1"; shift
case "$cmd" in
  start)    cmd_start "$@" ;;
  next)     cmd_next "$@" ;;
  resume)   cmd_next "$@" ;;
  complete) cmd_complete "$@" ;;
  status)   cmd_status "$@" ;;
  list)     cmd_list "$@" ;;
  show)     cmd_show "$@" ;;
  worklog)  cmd_worklog "$@" ;;
  ledger)   cmd_ledger "$@" ;;
  ticket)   cmd_ticket "$@" ;;
  ticket-comment) cmd_ticket_comment "$@" ;;
  children) cmd_children "$@" ;;
  fanout-ships) cmd_fanout_ships "$@" ;;
  prune)    cmd_prune "$@" ;;
  rotate-logs) cmd_rotate_logs "$@" ;;
  -h|--help|help) usage ;;
  *) err "unknown subcommand: $cmd"; usage; exit 1 ;;
esac
