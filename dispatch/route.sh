#!/usr/bin/env bash
# Hydra Router — dispatch/route.sh
# ─────────────────────────────────────────────────────────────────────────────
# Reads registry/models.yaml + registry/routing.yaml and routes a prompt to
# the right model tier. Implements cascading fallbacks, pool quota detection,
# Claude token preservation thresholds, and A2A context passing.
#
# Usage:
#   route.sh --tier <n> --prompt "<prompt>"
#   route.sh --enum SIMPLE --prompt "<prompt>"          # use routing enum key
#   route.sh --tier <n> --prompt "<prompt>" --context <file>
#   route.sh --tier <n> --prompt "<prompt>" --a2a <handoff.json>
#   route.sh --list                                      # list all tiers
#   route.sh --status                                    # show pool/token status
#
# Fallback behaviour:
#   - Model error/timeout  → try next tier in fallback_chains
#   - Pool quota exhausted → skip pool, use pool_fallback_chains
#   - Claude at 75%+       → escalation_freeze, downgrade tiers
#   - Claude at 95%+       → emergency mode, warn user
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

# ── Deprecation notice ────────────────────────────────────────────────────────
# dispatch/route.sh is the legacy entry point for external callers.
# The Go control plane (hydra dispatch) is now the single external entry point.
# Internal script callers (edit.sh, parallel.sh, etc.) set HYDRA_INTERNAL=1
# to suppress this warning.
if [[ "${HYDRA_INTERNAL:-}" != "1" ]]; then
  echo "⚠️  route.sh is deprecated as an external entry point." >&2
  echo "   Use: hydra dispatch --enum <KEY> --prompt \"<prompt>\"" >&2
  echo "   Or:  hydra dispatch --tier <N> --prompt \"<prompt>\"" >&2
  echo "   Set HYDRA_INTERNAL=1 to suppress this warning in script callers." >&2
fi

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REGISTRY_MODELS="$HYDRA_DIR/registry/models.yaml"
REGISTRY_ROUTING="$HYDRA_DIR/registry/routing.yaml"
# HYDRA_DATA overrides the mutable data dir (logs, state.json) so the TUI
# and the router always read/write the same state.json.
LOG_DIR="${HYDRA_DATA:+$HYDRA_DATA/logs}"; LOG_DIR="${LOG_DIR:-$HYDRA_DIR/logs}"
LOG_FILE="$LOG_DIR/dispatch.log"
STATE_FILE="$LOG_DIR/state.json"   # tracks pool exhaustion, claude usage %

# ── Helpers ───────────────────────────────────────────────────────────────────

log() { printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG_FILE"; }
err() { echo "❌ $*" >&2; log "ERROR: $*"; }
warn() { echo "⚠️  $*" >&2; log "WARN: $*"; }
info() { echo "🐍 $*" >&2; log "INFO: $*"; }

# Read from models.yaml for a given tier
model_field() { yq ".models[] | select(.tier == $1) | .$2" "$REGISTRY_MODELS" | tr -d '"'; }

# Read from routing.yaml
routing_field() { yq ".$1" "$REGISTRY_ROUTING" | tr -d '"'; }

# Resolve enum key → tier number
resolve_enum() {
  local key="$1"
  yq ".routing_map.$key" "$REGISTRY_ROUTING" | tr -d '"'
}

# Get fallback chain for a tier as space-separated numbers
fallback_chain() {
  yq ".fallback_chains.$1 | join(\" \")" "$REGISTRY_ROUTING" | tr -d '"'
}

# Get pool for a tier
tier_pool() { model_field "$1" token_pool; }

# Check if a pool is marked exhausted in state.json
pool_exhausted() {
  [[ -f "$STATE_FILE" ]] && jq -e --arg p "$1" '(.exhausted_pools // []) | index($p) != null' "$STATE_FILE" > /dev/null 2>&1
}

# Mark a pool as exhausted
mark_pool_exhausted() {
  mkdir -p "$LOG_DIR"
  if [[ -f "$STATE_FILE" ]]; then
    jq --arg p "$1" '.exhausted_pools = ((.exhausted_pools // []) + [$p])' "$STATE_FILE" > "$STATE_FILE.tmp" && mv "$STATE_FILE.tmp" "$STATE_FILE"
  else
    jq -n --arg p "$1" '{"exhausted_pools":[$p],"claude_pct":0}' > "$STATE_FILE"
  fi
  warn "Pool '$1' marked exhausted. Will skip all tiers in this pool."
}

# Get Claude token usage percent (0-100). Update STATE_FILE externally to track this.
claude_pct() {
  [[ -f "$STATE_FILE" ]] && jq -r '.claude_pct // 0' "$STATE_FILE" || echo "0"
}

# Get active claude preservation mode based on current %
claude_mode() {
  local pct="$1"
  if   (( pct >= 80 )); then echo "emergency"  # absolute ceiling — stop generation
  elif (( pct >= 75 )); then echo "critical"   # hard switch — routing only
  elif (( pct >= 70 )); then echo "warning"    # downgrade + compact urgent
  elif (( pct >= 65 )); then echo "caution"    # compact immediately
  elif (( pct >= 50 )); then echo "compact"    # compact recommended
  else echo "normal"
  fi
}

# Auto-downgrade tier by N based on claude mode
apply_tier_downgrade() {
  local tier="$1" mode="$2"
  local downgrade=0
  case "$mode" in
    warning)   downgrade=1 ;;
    critical)  downgrade=2 ;;
    emergency) downgrade=3 ;;
  esac
  echo $(( tier + downgrade ))
}

# ── Args ──────────────────────────────────────────────────────────────────────

tier=""
enum_key=""
prompt=""
context_file=""
context_inline=""
a2a_file=""
list_mode=false
status_mode=false

while [[ $# -gt 0 ]]; do
  case $1 in
    --tier)           tier="$2"; shift 2 ;;
    --enum)           enum_key="$2"; shift 2 ;;
    --prompt)         prompt="$2"; shift 2 ;;
    --context)        context_file="$2"; shift 2 ;;
    --context-inline) context_inline="$2"; shift 2 ;;
    --a2a)            a2a_file="$2"; shift 2 ;;
    --list)           list_mode=true; shift ;;
    --status)         status_mode=true; shift ;;
    -h|--help)
      echo "Usage: route.sh --tier <n>|--enum <KEY> --prompt \"<prompt>\""
      echo "       route.sh --tier <n> --context <file>"
      echo "       route.sh --tier <n> --a2a <handoff.json>   (A2A context pass)"
      echo "       route.sh --list | --status"
      exit 0 ;;
    *) err "Unknown argument: $1"; exit 1 ;;
  esac
done

# ── List / Status ─────────────────────────────────────────────────────────────

if $list_mode; then
  echo "🐍 Hydra — Available Tiers"
  echo "─────────────────────────────────────────────────────────────────"
  yq '.models[] | select(.enabled == true) | "  Tier " + (.tier | tostring) + "  [" + .frequency + "]  " + .name + "  (" + .token_pool + ")"' \
    "$REGISTRY_MODELS" | tr -d '"'
  echo ""
  exit 0
fi

if $status_mode; then
  pct=$(claude_pct)
  mode=$(claude_mode "$pct")
  echo "🐍 Hydra — System Status"
  echo "─────────────────────────────────────────────────────────────────"
  echo "  Claude token usage : ${pct}%  [mode: $mode]"
  if [[ -f "$STATE_FILE" ]]; then
    exhausted=$(jq -r '.exhausted_pools | join(", ")' "$STATE_FILE")
    [[ -n "$exhausted" ]] && echo "  Exhausted pools    : $exhausted" || echo "  Exhausted pools    : none"
  fi
  echo ""
  exit 0
fi

# ── Resolve tier ──────────────────────────────────────────────────────────────

if [[ -n "$enum_key" ]]; then
  tier=$(resolve_enum "$enum_key")
  [[ -z "$tier" || "$tier" == "null" ]] && err "Unknown enum key: $enum_key" && exit 1
fi

[[ -z "$tier" ]]   && err "--tier or --enum required" && exit 1
[[ -z "$prompt" ]] && err "--prompt required" && exit 1

# ── Claude preservation check ─────────────────────────────────────────────────
# Thresholds: 50% compact-recommended, 65% compact-urgent, 70% warn+downgrade,
#             75% hard-switch, 80% absolute ceiling → emergency

pct=$(claude_pct)
mode=$(claude_mode "$pct")

case "$mode" in
  emergency)
    warn "🚨 EMERGENCY: Claude at ${pct}% — 80% ABSOLUTE LIMIT HIT."
    warn "All Claude generation STOPPED. Routing to Qwen (tier 10) for everything."
    warn "User action required: start a new Claude Code session."
    tier=10
    ;;
  critical)
    warn "🔴 CRITICAL: Claude at ${pct}% — 75% hard limit. Routing only, no Claude generation."
    tier=$(apply_tier_downgrade "$tier" "$mode")
    ;;
  warning)
    warn "🟠 WARNING: Claude at ${pct}% — 70% threshold. Downgrading tier by 1. Run /compact now."
    tier=$(apply_tier_downgrade "$tier" "$mode")
    ;;
  caution)
    warn "🟡 CAUTION: Claude at ${pct}% — Run /compact immediately to free context window."
    ;;
  compact)
    info "ℹ️  Claude at ${pct}% — Consider running /compact to compress context."
    ;;
esac

# Cap tier at 10 (max available), floor at 1
(( tier > 10 )) && tier=10
(( tier < 1  )) && tier=1

# ── A2A Context Protocol ──────────────────────────────────────────────────────
# If --a2a is provided, inject the handoff JSON as structured context.
# Handoff format: { "from": "agent_id", "task": "...", "files": [...],
#                   "context": "...", "conventions": "...", "prior_output": "..." }

if [[ -n "$a2a_file" && -f "$a2a_file" ]]; then
  a2a_from=$(jq -r '.from // "unknown"' "$a2a_file")
  a2a_task=$(jq -r '.task // ""' "$a2a_file")
  a2a_context=$(jq -r '.context // ""' "$a2a_file")
  a2a_conventions=$(jq -r '.conventions // ""' "$a2a_file")
  a2a_prior=$(jq -r '.prior_output // ""' "$a2a_file")
  a2a_files=$(jq -r '.files // [] | join(", ")' "$a2a_file")

  a2a_block="$(printf \
    'A2A HANDOFF from: %s\nFiles in scope: %s\nConventions:\n%s\nPrior output:\n%s\nContext:\n%s\n\nTASK:\n%s' \
    "$a2a_from" "$a2a_files" "$a2a_conventions" "$a2a_prior" "$a2a_context" "$a2a_task")"

  prompt="$a2a_block

ADDITIONAL INSTRUCTION:
$prompt"
fi

# ── Context injection ─────────────────────────────────────────────────────────

if [[ -n "$context_file" && -f "$context_file" ]]; then
  context=$(cat "$context_file")
  prompt="$(printf 'CONTEXT:\n%s\n\nTASK:\n%s' "$context" "$prompt")"
fi

if [[ -n "$context_inline" ]]; then
  prompt="$(printf 'CONTEXT:\n%s\n\nTASK:\n%s' "$context_inline" "$prompt")"
fi

# ── Dispatch with cascading fallbacks ─────────────────────────────────────────

mkdir -p "$LOG_DIR"
chain=$(fallback_chain "$tier")
read -ra tiers_to_try <<< "$chain"

for try_tier in "${tiers_to_try[@]}"; do
  executor=$(model_field "$try_tier" executor)
  model_flag=$(model_field "$try_tier" model_flag)
  model_name=$(model_field "$try_tier" name)
  enabled=$(model_field "$try_tier" enabled)
  pool=$(tier_pool "$try_tier")

  # Skip disabled tiers
  if [[ "$enabled" != "true" ]]; then
    warn "Tier $try_tier ($model_name) disabled — trying next fallback"
    continue
  fi

  # Skip pool-exhausted tiers
  if pool_exhausted "$pool"; then
    warn "Pool '$pool' exhausted — skipping tier $try_tier ($model_name)"
    continue
  fi

  # Tier 1 special case: orchestrator handles directly
  if [[ "$executor" == "claude" ]]; then
    if [[ "$mode" == "emergency" ]]; then
      warn "Tier 1 (Claude Core) blocked in emergency mode"
      continue
    fi
    info "Tier 1: Claude Core — returning prompt for orchestrator"
    log "dispatch tier=1 model=Claude_Core mode=$mode"
    echo "$prompt"
    exit 0
  fi

  # Attempt dispatch
  info "Tier $try_tier: $model_name (pool: $pool)"
  log "dispatch tier=$try_tier model=\"$model_name\" executor=$executor pool=$pool mode=$mode prompt_len=${#prompt}"

  # Token-tracking sidecar — executor writes counts here, we read after
  HYDRA_TOKEN_SIDECAR=$(mktemp -t hydra-tokens.XXXXXX.json)
  export HYDRA_TOKEN_SIDECAR
  call_start_ns=$(date +%s%N 2>/dev/null || python3 -c 'import time;print(int(time.time()*1e9))' 2>/dev/null || echo 0)

  set +e
  case "$executor" in
    agy)
      output=$("$HYDRA_DIR/dispatch/agy.sh" "$model_flag" "$prompt" 2>&1)
      exit_code=$?
      ;;
    ollama)
      output=$("$HYDRA_DIR/dispatch/ollama.sh" "$model_flag" "$prompt" 2>&1)
      exit_code=$?
      ;;
    *)
      err "Unknown executor '$executor' for tier $try_tier"
      exit_code=1
      ;;
  esac
  set -e

  call_end_ns=$(date +%s%N 2>/dev/null || python3 -c 'import time;print(int(time.time()*1e9))' 2>/dev/null || echo 0)
  [[ "$call_start_ns" =~ ^[0-9]+$ ]] || call_start_ns=0
  [[ "$call_end_ns"   =~ ^[0-9]+$ ]] || call_end_ns=0
  wall_ms=$(( (call_end_ns - call_start_ns) / 1000000 ))

  # ── Log token cost (best-effort; a logging failure must never abort a dispatch) ──
  {
    if [[ -s "$HYDRA_TOKEN_SIDECAR" ]] && jq empty "$HYDRA_TOKEN_SIDECAR" 2>/dev/null; then
      sidecar=$(cat "$HYDRA_TOKEN_SIDECAR")
      pricing_file="$HYDRA_DIR/registry/pricing.yaml"
      in_price=0; out_price=0
      if [[ -f "$pricing_file" ]]; then
        in_price=$(yq -r ".tiers.\"$try_tier\".input_per_million // 0"  "$pricing_file" 2>/dev/null || echo 0)
        out_price=$(yq -r ".tiers.\"$try_tier\".output_per_million // 0" "$pricing_file" 2>/dev/null || echo 0)
      fi
      p_tok=$(echo "$sidecar" | jq -r '.prompt_tokens   // 0')
      r_tok=$(echo "$sidecar" | jq -r '.response_tokens // 0')
      cost=$(awk -v p="$p_tok" -v r="$r_tok" -v pi="$in_price" -v po="$out_price" \
        'BEGIN{ printf "%.6f", (p/1000000)*pi + (r/1000000)*po }')

      # Ensure numeric values before --argjson to prevent jq parse errors
      [[ "$p_tok"   =~ ^[0-9]+$           ]] || p_tok=0
      [[ "$r_tok"   =~ ^[0-9]+$           ]] || r_tok=0
      [[ "$cost"    =~ ^[0-9]+\.?[0-9]*$  ]] || cost=0
      [[ "$wall_ms" =~ ^-?[0-9]+$         ]] || wall_ms=0

      cost_log="$LOG_DIR/cost.jsonl"
      jq -n -c \
        --arg ts        "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        --argjson tier  "$try_tier" \
        --arg enum      "${enum_key:-}" \
        --arg model     "$model_name" \
        --arg executor  "$executor" \
        --arg pool      "$pool" \
        --argjson prompt_tokens   "$p_tok" \
        --argjson response_tokens "$r_tok" \
        --argjson est_cost_usd    "$cost" \
        --argjson wall_ms         "$wall_ms" \
        --argjson sidecar         "$sidecar" \
        --arg task_id   "${HYDRA_TASK_ID:-}" \
        --arg run_id    "${HYDRA_RUN_ID:-}" \
        '{ ts: $ts, tier: $tier, enum: $enum, model: $model,
           executor: $executor, pool: $pool,
           prompt_tokens: $prompt_tokens, response_tokens: $response_tokens,
           est_cost_usd: $est_cost_usd, wall_ms: $wall_ms,
           source: $sidecar.source, task_id: $task_id, run_id: $run_id }' \
        >> "$cost_log"
    fi
    rm -f "${HYDRA_TOKEN_SIDECAR:-}"
  } || { rm -f "${HYDRA_TOKEN_SIDECAR:-}"; true; }

  if [[ $exit_code -eq 0 && -n "$output" ]]; then
    echo "$output"

    # Write A2A handoff artifact for next agent
    handoff_file="$LOG_DIR/last_handoff.json"
    jq -n \
      --arg from "hydra-tier-$try_tier" \
      --arg model "$model_name" \
      --arg task "$prompt" \
      --arg output "$output" \
      --arg pool "$pool" \
      '{ from: $from, model: $model, task: $task, prior_output: $output, pool: $pool }' \
      > "$handoff_file"

    log "success tier=$try_tier model=\"$model_name\""
    exit 0
  fi

  # Auth required (exit 3) — surface URL, skip tier, do NOT mark pool exhausted
  if [[ $exit_code -eq 3 ]]; then
    auth_file="$LOG_DIR/auth_required.json"
    if [[ -f "$auth_file" ]]; then
      auth_url=$(jq -r '.auth_url // "unknown"' "$auth_file")
      warn "🔐 AUTH REQUIRED for pool '$pool' — authenticate then retry"
      warn "   URL: $auth_url"
    else
      warn "🔐 AUTH REQUIRED for tier $try_tier ($model_name) — authenticate then retry"
    fi
    log "auth_required tier=$try_tier pool=$pool"
    continue
  fi

  # Detect quota exhaustion from error output
  if echo "$output" | grep -qiE "quota|rate.?limit|429|exhausted|limit.?reached"; then
    mark_pool_exhausted "$pool"
    warn "Quota hit on pool '$pool'. Skipping all tiers in this pool."
  else
    warn "Tier $try_tier failed (exit $exit_code). Trying next fallback."
    log "fail tier=$try_tier exit=$exit_code error=$(echo "$output" | head -1)"
  fi

done

err "All fallbacks exhausted for original tier $tier. No model could handle this task."
err "Check logs: $LOG_FILE"
err "Check state: $STATE_FILE"
exit 1
