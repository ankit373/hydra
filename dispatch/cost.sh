#!/usr/bin/env bash
# Hydra Cost Reporter — dispatch/cost.sh
# ─────────────────────────────────────────────────────────────────────────────
# Reads logs/cost.jsonl (appended to by route.sh per dispatch) and produces
# spend summaries by various groupings.
#
# Subcommands:
#   cost.sh                            → today's summary + all-time totals
#   cost.sh today                      → today's per-tier breakdown
#   cost.sh all                        → all-time per-tier breakdown
#   cost.sh by-pool                    → per-pool totals
#   cost.sh by-task <task_id>          → spending for a specific task
#   cost.sh by-run  <run_id>           → spending for a specific playbook run
#   cost.sh tail [N]                   → last N calls (default 10)
#   cost.sh json [--since <ISO>]       → raw rows, optionally filtered
#
# Output is human-readable by default; pass `--json` for machine-readable.
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$HYDRA_DIR/logs"
COST_LOG="$LOG_DIR/cost.jsonl"

JSON_OUT=0

# ── Helpers ───────────────────────────────────────────────────────────────────

err() { echo "❌ cost: $*" >&2; }

ensure_log() {
  if [[ ! -f "$COST_LOG" ]]; then
    err "no cost log at $COST_LOG — has anything dispatched yet?"
    exit 1
  fi
}

today_iso() { date -u +%Y-%m-%d; }

# Filter rows whose .ts starts with the given date (UTC).
rows_for_date() {
  local d="$1"
  jq -c "select(.ts | startswith(\"$d\"))" "$COST_LOG" 2>/dev/null || true
}

# Aggregate rows (one JSON object per line) by .tier
agg_by_field() {
  local field="$1"
  jq -s "
    group_by(.$field)
    | map({
        key: (.[0].$field // \"unknown\"),
        calls: length,
        prompt_tokens:   (map(.prompt_tokens)   | add // 0),
        response_tokens: (map(.response_tokens) | add // 0),
        est_cost_usd:    ((map(.est_cost_usd)   | add // 0) * 1000000 | round | . / 1000000),
        wall_ms:         (map(.wall_ms)         | add // 0)
      })
    | sort_by(.est_cost_usd) | reverse"
}

# Total-stats summary
agg_totals() {
  jq -s "
    {
      calls: length,
      prompt_tokens:   (map(.prompt_tokens)   | add // 0),
      response_tokens: (map(.response_tokens) | add // 0),
      est_cost_usd:    ((map(.est_cost_usd)   | add // 0) * 1000000 | round | . / 1000000),
      wall_seconds:    ((map(.wall_ms)        | add // 0) / 1000 | round)
    }"
}

# Render an aggregated array as a table (key | calls | tok_in | tok_out | $cost | wall_s)
render_table() {
  local title="$1"
  echo ""
  echo "  $title"
  echo "  ─────────────────────────────────────────────────────────────"
  printf "  %-18s %6s %10s %10s %10s %8s\n" "key" "calls" "tok_in" "tok_out" "\$" "wall_s"
  jq -r '.[] | [
    (.key | tostring), .calls, .prompt_tokens, .response_tokens,
    (.est_cost_usd | tostring),
    ((.wall_ms / 1000) | round)
  ] | @tsv' \
    | awk -F'\t' '{ printf "  %-18s %6d %10d %10d %10s %8d\n", $1, $2, $3, $4, $5, $6 }'
}

# ── Subcommands ───────────────────────────────────────────────────────────────

# Strip leading --json flag if present
for arg in "$@"; do
  [[ "$arg" == "--json" ]] && JSON_OUT=1
done
args=()
for arg in "$@"; do
  [[ "$arg" == "--json" ]] && continue
  args+=("$arg")
done
set -- "${args[@]+"${args[@]}"}"

cmd="${1:-summary}"; shift || true

case "$cmd" in

  summary|"")
    ensure_log
    today=$(today_iso)
    today_rows=$(rows_for_date "$today")
    today_totals=$(echo "$today_rows" | agg_totals)
    all_totals=$(cat "$COST_LOG" | agg_totals)

    if [[ $JSON_OUT -eq 1 ]]; then
      jq -n --argjson today "$today_totals" --argjson all "$all_totals" \
        '{ today: $today, all_time: $all }'
    else
      echo ""
      echo "  Hydra cost summary"
      echo "  ═════════════════════════════════════════════════════════════"
      echo ""
      echo "  Today ($today)"
      echo "$today_totals" | jq -r '"    calls          \(.calls)\n    tokens in/out  \(.prompt_tokens) / \(.response_tokens)\n    est cost       $\(.est_cost_usd)\n    wall time      \(.wall_seconds)s"'
      echo ""
      echo "  All time"
      echo "$all_totals"   | jq -r '"    calls          \(.calls)\n    tokens in/out  \(.prompt_tokens) / \(.response_tokens)\n    est cost       $\(.est_cost_usd)\n    wall time      \(.wall_seconds)s"'

      echo ""
      cat "$COST_LOG" | jq -s '. | reverse | .[0:5]' | jq -r '.[] | "  \(.ts) \(.enum)/\(.tier) \(.model) — \(.prompt_tokens)+\(.response_tokens) tok, $\(.est_cost_usd), \(.wall_ms)ms"' \
        | (echo "  Recent (last 5):"; cat)
      echo ""
    fi
    ;;

  today)
    ensure_log
    rows=$(rows_for_date "$(today_iso)")
    if [[ -z "$rows" ]]; then
      err "no calls today"; exit 0
    fi
    by_tier=$(echo "$rows" | agg_by_field tier)
    [[ $JSON_OUT -eq 1 ]] && { echo "$by_tier"; exit 0; }
    echo "$by_tier" | render_table "Today's spend by tier ($(today_iso))"
    echo ""
    ;;

  all)
    ensure_log
    by_tier=$(cat "$COST_LOG" | agg_by_field tier)
    [[ $JSON_OUT -eq 1 ]] && { echo "$by_tier"; exit 0; }
    echo "$by_tier" | render_table "All-time spend by tier"
    echo ""
    ;;

  by-pool)
    ensure_log
    by_pool=$(cat "$COST_LOG" | agg_by_field pool)
    [[ $JSON_OUT -eq 1 ]] && { echo "$by_pool"; exit 0; }
    echo "$by_pool" | render_table "All-time spend by pool"
    echo ""
    ;;

  by-task)
    ensure_log
    task_id="${1:-}"
    [[ -z "$task_id" ]] && err "usage: cost.sh by-task <task_id>" && exit 1
    rows=$(jq -c --arg t "$task_id" 'select(.task_id == $t)' "$COST_LOG")
    [[ -z "$rows" ]] && err "no calls for task_id=$task_id" && exit 0
    totals=$(echo "$rows" | agg_totals)
    [[ $JSON_OUT -eq 1 ]] && { echo "$totals"; exit 0; }
    echo ""
    echo "  Task $task_id"
    echo "$totals" | jq -r '"    calls   \(.calls)\n    tok     \(.prompt_tokens)+\(.response_tokens)\n    cost    $\(.est_cost_usd)\n    wall    \(.wall_seconds)s"'
    echo ""
    ;;

  by-run)
    ensure_log
    run_id="${1:-}"
    [[ -z "$run_id" ]] && err "usage: cost.sh by-run <run_id>" && exit 1
    rows=$(jq -c --arg r "$run_id" 'select(.run_id == $r)' "$COST_LOG")
    [[ -z "$rows" ]] && err "no calls for run_id=$run_id" && exit 0
    totals=$(echo "$rows" | agg_totals)
    by_tier=$(echo "$rows" | agg_by_field tier)
    if [[ $JSON_OUT -eq 1 ]]; then
      jq -n --argjson t "$totals" --argjson b "$by_tier" '{ totals: $t, by_tier: $b }'
      exit 0
    fi
    echo ""
    echo "  Run $run_id"
    echo "$totals" | jq -r '"    calls   \(.calls)\n    cost    $\(.est_cost_usd)\n    wall    \(.wall_seconds)s"'
    echo "$by_tier" | render_table "Per-tier"
    echo ""
    ;;

  tail)
    ensure_log
    n="${1:-10}"
    [[ $JSON_OUT -eq 1 ]] && { tail -n "$n" "$COST_LOG"; exit 0; }
    tail -n "$n" "$COST_LOG" | jq -r '"  \(.ts) \(.enum // "?")/\(.tier) \(.model) — \(.prompt_tokens)+\(.response_tokens) tok, $\(.est_cost_usd), \(.wall_ms)ms"'
    ;;

  json)
    ensure_log
    since=""
    if [[ "${1:-}" == "--since" ]]; then
      since="$2"
    fi
    if [[ -n "$since" ]]; then
      jq -c --arg s "$since" 'select(.ts >= $s)' "$COST_LOG"
    else
      cat "$COST_LOG"
    fi
    ;;

  *)
    err "unknown command: $cmd"
    err "usage: cost.sh {summary|today|all|by-pool|by-task|by-run|tail|json}"
    exit 1
    ;;
esac
