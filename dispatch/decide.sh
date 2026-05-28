#!/usr/bin/env bash
# Hydra Policy Decider — dispatch/decide.sh
# ─────────────────────────────────────────────────────────────────────────────
# Reads a task-spec JSON, evaluates rules in registry/policy.yaml against it,
# and emits a merged policy JSON. Downstream tools (edit.sh, parallel.sh,
# future agy-agent.sh) consume the policy as flags and execute deterministically.
#
# Usage:
#   decide.sh <spec.json>            — file
#   echo '{...}' | decide.sh -       — stdin
#
# Spec fields (all optional; missing fields don't satisfy any condition):
#   file              absolute path
#   file_lines        int
#   file_count        int (for parallel batches)
#   file_extension    string (without dot)
#   task_type         string (rename | migration | scaffold | fix | feature |
#                              refactor | refactor_global | test | doc | other)
#   in_playbook       bool
#   stage_name        string (when in playbook)
#   has_git           bool
#   enum_tier         int 1-10
#   workspace         string
#   prompt            string
#   prompt_length     int (chars)
#   context_pct       int 0-100
#
# Output (stdout): a JSON object with all policy flags + a `matched_rules` array.
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
POLICY_FILE="$HYDRA_DIR/registry/policy.yaml"
LOG_DIR="$HYDRA_DIR/logs"
LOG_FILE="$LOG_DIR/decide.log"

mkdir -p "$LOG_DIR"
err() { echo "❌ decide: $*" >&2; }
log() { printf '[%s] decide: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG_FILE"; }

[[ ! -f "$POLICY_FILE" ]] && err "policy.yaml not found at $POLICY_FILE" && exit 1

spec_arg="${1:-}"
[[ -z "$spec_arg" ]] && err "usage: decide.sh <spec.json|->" && exit 1

if [[ "$spec_arg" == "-" ]]; then
  spec=$(cat)
elif [[ -f "$spec_arg" ]]; then
  spec=$(cat "$spec_arg")
else
  err "spec file not found: $spec_arg"
  exit 1
fi

if ! echo "$spec" | jq empty >/dev/null 2>&1; then
  err "invalid JSON in spec"
  exit 1
fi

# ── Match a single (key, value) condition against the spec ───────────────────
# Returns 0 if match, 1 if not.
match_condition() {
  local key="$1" val="$2" field op

  case "$key" in
    *_eq)        op="eq";        field="${key%_eq}" ;;
    *_ne)        op="ne";        field="${key%_ne}" ;;
    *_gt)        op="gt";        field="${key%_gt}" ;;
    *_lt)        op="lt";        field="${key%_lt}" ;;
    *_gte)       op="gte";       field="${key%_gte}" ;;
    *_lte)       op="lte";       field="${key%_lte}" ;;
    *_in)        op="in";        field="${key%_in}" ;;
    *_contains)  op="contains";  field="${key%_contains}" ;;
    *_present)   op="present";   field="${key%_present}" ;;
    always)      return 0 ;;     # always matches
    *)           op="eq";        field="$key" ;;
  esac

  local spec_val
  spec_val=$(echo "$spec" | jq -r "(.${field} // null) | if . == null then \"\" else tostring end")

  case "$op" in
    eq)
      [[ "$spec_val" == "$val" ]]
      ;;
    ne)
      [[ -n "$spec_val" && "$spec_val" != "$val" ]]
      ;;
    gt|lt|gte|lte)
      [[ -z "$spec_val" ]] && return 1
      awk -v s="$spec_val" -v v="$val" -v op="$op" '
        BEGIN {
          if (op=="gt")  exit !(s+0 >  v+0)
          if (op=="lt")  exit !(s+0 <  v+0)
          if (op=="gte") exit !(s+0 >= v+0)
          if (op=="lte") exit !(s+0 <= v+0)
        }'
      ;;
    in)
      [[ -z "$spec_val" ]] && return 1
      echo "$val" | jq -e --arg v "$spec_val" 'any(. == $v)' >/dev/null 2>&1
      ;;
    contains)
      [[ "$spec_val" == *"$val"* ]]
      ;;
    present)
      if [[ "$val" == "true" ]]; then
        [[ -n "$spec_val" && "$spec_val" != "null" ]]
      else
        [[ -z "$spec_val" || "$spec_val" == "null" ]]
      fi
      ;;
    *)
      return 1
      ;;
  esac
}

# ── Check if all conditions in a `when` block match ──────────────────────────
match_when() {
  local when_json="$1"

  # Empty / null when = always match
  if [[ "$when_json" == "null" || "$when_json" == "{}" || -z "$when_json" ]]; then
    return 0
  fi

  local keys
  keys=$(echo "$when_json" | jq -r 'keys[]')
  while IFS= read -r key; do
    [[ -z "$key" ]] && continue
    # Pull the value as JSON (lists stay lists; scalars become bare strings)
    local val_type val
    val_type=$(echo "$when_json" | jq -r ".\"$key\" | type")
    if [[ "$val_type" == "array" ]]; then
      val=$(echo "$when_json" | jq -c ".\"$key\"")
    else
      val=$(echo "$when_json" | jq -r ".\"$key\"")
    fi
    match_condition "$key" "$val" || return 1
  done <<< "$keys"
  return 0
}

# ── Start from defaults ──────────────────────────────────────────────────────
policy=$(yq -o=json '.defaults // {}' "$POLICY_FILE")
matched='[]'

# ── Walk rules in order, merging on match ────────────────────────────────────
rule_count=$(yq '.rules | length' "$POLICY_FILE")
for i in $(seq 0 $((rule_count - 1))); do
  rule_name=$(yq -r ".rules[$i].name // \"rule_$i\"" "$POLICY_FILE")
  when_json=$(yq -o=json ".rules[$i].when // {}" "$POLICY_FILE")
  apply_json=$(yq -o=json ".rules[$i].apply // {}" "$POLICY_FILE")

  if match_when "$when_json"; then
    # Recursive merge: later rule wins on key collision; lists overwrite
    policy=$(jq -n --argjson base "$policy" --argjson over "$apply_json" '$base * $over')
    matched=$(echo "$matched" | jq --arg n "$rule_name" '. + [$n]')
  fi
done

# ── Emit final policy with matched_rules audit trail ─────────────────────────
result=$(echo "$policy" | jq --argjson m "$matched" '. + { matched_rules: $m }')

log "spec=$(echo "$spec" | jq -c .)"
log "matched=$(echo "$matched" | jq -c .)"

echo "$result"
