#!/usr/bin/env bash
# hydra — single entry point for the Hydra orchestration system
# ─────────────────────────────────────────────────────────────────────────────
# Usage:
#   hydra status                          # show all pools + claude token %
#   hydra list                            # list all model tiers
#   hydra do SIMPLE "write a User DTO"   # dispatch by enum key
#   hydra do GRUNT  "scaffold files"     # dispatch grunt work to Qwen
#   hydra do CORE   "design the auth"   # escalate to Claude orchestrator
#   hydra duck "review this: <code>"    # rubber duck review (tier 4 GPT-OSS)
#   hydra compact                         # remind Claude to /compact
#   hydra set-pct 52                     # update Claude token usage %
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
ROUTE="$HYDRA_DIR/dispatch/route.sh"
STATE="$HYDRA_DIR/logs/state.json"

cmd="${1:-status}"
shift || true

case "$cmd" in

  status)
    "$ROUTE" --status
    echo ""
    # Show auth warning if any pool needs authentication
    auth_file="$HYDRA_DIR/logs/auth_required.json"
    if [[ -f "$auth_file" ]]; then
      auth_url=$(jq -r '.auth_url // ""' "$auth_file")
      auth_pool=$(jq -r '.pool // ""' "$auth_file")
      auth_ts=$(jq -r '.detected_at // ""' "$auth_file")
      echo "⚠️  AUTH REQUIRED  (detected: $auth_ts)"
      echo "   Pool : $auth_pool"
      echo "   URL  : $auth_url"
      echo "   Run  : hydra auth   to open / copy the link"
      echo ""
    fi
    echo "Tier list:"
    "$ROUTE" --list
    ;;

  list)
    "$ROUTE" --list
    ;;

  do)
    enum_key="${1:-}"; shift || true
    prompt="${1:-}"; shift || true
    [[ -z "$enum_key" ]] && echo "Usage: hydra do <ENUM_KEY> \"<prompt>\"" && exit 1
    [[ -z "$prompt"   ]] && echo "Usage: hydra do <ENUM_KEY> \"<prompt>\"" && exit 1
    "$ROUTE" --enum "$enum_key" --prompt "$prompt" "$@"
    ;;

  duck)
    prompt="${1:-}"
    [[ -z "$prompt" ]] && echo "Usage: hydra duck \"<code or output to review>\"" && exit 1
    "$ROUTE" --tier 4 --prompt "You are a rubber duck reviewer. Review this for issues, tradeoffs, and better approaches. Output: APPROVED <summary> or ISSUES <bullets>.\n\n$prompt"
    ;;

  compact)
    echo "⚠️  Run /compact in your Claude Code session to compress the context window."
    echo "   Do this before reaching 75% to avoid losing orchestration ability."
    pct=$( [[ -f "$STATE" ]] && jq -r '.claude_pct // 0' "$STATE" || echo "0" )
    echo "   Current Claude context: ${pct}%"
    ;;

  set-pct)
    pct="${1:-}"
    [[ -z "$pct" ]] && echo "Usage: hydra set-pct <0-100>" && exit 1
    mkdir -p "$(dirname "$STATE")"
    if [[ -f "$STATE" ]]; then
      jq ".claude_pct = $pct" "$STATE" > "$STATE.tmp" && mv "$STATE.tmp" "$STATE"
    else
      echo "{\"claude_pct\":$pct,\"exhausted_pools\":[]}" > "$STATE"
    fi
    echo "✓ Claude context set to ${pct}%"
    "$ROUTE" --status
    ;;

  auth)
    auth_file="$HYDRA_DIR/logs/auth_required.json"
    if [[ -f "$auth_file" ]]; then
      auth_url=$(jq -r '.auth_url // ""' "$auth_file")
      auth_pool=$(jq -r '.pool // ""' "$auth_file")
      echo "🔐 Authentication required for pool: $auth_pool"
      echo ""
      echo "   $auth_url"
      echo ""
      # Try to open in browser on macOS/Linux
      if command -v open &>/dev/null; then
        echo "Opening in browser..."
        open "$auth_url" 2>/dev/null || true
      elif command -v xdg-open &>/dev/null; then
        xdg-open "$auth_url" 2>/dev/null || true
      fi
      echo "After authenticating, run: hydra auth-clear"
    else
      echo "✓ No authentication required — all pools appear healthy"
      echo "  (If a dispatch just failed with auth errors, re-run to capture the URL)"
    fi
    ;;

  auth-clear)
    auth_file="$HYDRA_DIR/logs/auth_required.json"
    [[ -f "$auth_file" ]] && rm "$auth_file" && echo "✓ Auth flag cleared" || echo "No auth flag set"
    ;;

  reset-pools)
    [[ -f "$STATE" ]] && jq '.exhausted_pools = []' "$STATE" > "$STATE.tmp" && mv "$STATE.tmp" "$STATE"
    echo "✓ All pool exhaustion flags cleared"
    ;;

  log)
    [[ -f "$HYDRA_DIR/logs/dispatch.log" ]] && tail -30 "$HYDRA_DIR/logs/dispatch.log" || echo "No log yet"
    ;;

  help|-h|--help)
    cat <<'EOF'
🐍 Hydra — Multi-Model AI Orchestrator

Usage:
  hydra status                         Show system state (pools, token %)
  hydra list                           List all model tiers
  hydra do <KEY> "<prompt>"           Dispatch by enum key
  hydra do <KEY> "<prompt>" --context <file>
  hydra do <KEY> "<prompt>" --a2a ~/hydra/logs/last_handoff.json
  hydra duck "<output to review>"     Cross-model rubber duck (GPT-OSS)
  hydra compact                        Remind Claude to /compact
  hydra set-pct <0-100>               Update Claude context % in state
  hydra reset-pools                    Clear pool exhaustion flags
  hydra auth                           Show/open auth URL if a pool needs login
  hydra auth-clear                     Clear the auth-required flag after logging in
  hydra log                            Show last 30 dispatch log entries

Enum Keys (routing.yaml):
  GRUNT      → Qwen (local, free)
  TRIVIAL    → Flash Low
  SIMPLE     → Flash Med
  STANDARD   → Flash High
  MODERATE   → Pro Low
  COMPLEX    → Pro High
  HARD       → GPT-OSS 120B
  VERY_HARD  → Sonnet Thinking
  EXPERT     → Opus Thinking
  CORE       → Claude Code (you)

EOF
    ;;

  *)
    echo "Unknown command: $cmd. Run: hydra help"
    exit 1
    ;;
esac
