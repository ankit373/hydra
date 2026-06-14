#!/usr/bin/env bash
# Hydra — dispatch/agy.sh
# ─────────────────────────────────────────────────────────────────────────────
# Wrapper around `agy --print` with model selection and timeout handling.
# Called by route.sh. Do not call directly unless testing.
#
# Usage: agy.sh <model_flag> "<prompt>"
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

model_flag="${1:-}"
prompt="${2:-}"

[[ -z "$model_flag" ]] && echo "❌ agy.sh: model_flag required" >&2 && exit 1
[[ -z "$prompt" ]]     && echo "❌ agy.sh: prompt required" >&2 && exit 1

# null model_flag means use default (shouldn't happen for agy tier, but guard it)
if [[ "$model_flag" == "null" ]]; then
  echo "❌ agy.sh: model_flag is null — check registry entry" >&2
  exit 1
fi

AGY_TIMEOUT="${AGY_TIMEOUT:-300}"  # default 5 min, override via env

SETTINGS="$HOME/.gemini/antigravity-cli/settings.json"
HYDRA_DIR="${HYDRA_DIR:-$(cd "$(dirname "$0")/.." && pwd)}"
# AUTH_FILE must align with the path route.sh reads — use $HYDRA_DIR/logs/
AUTH_FILE="${HYDRA_AUTH_FILE:-$HYDRA_DIR/logs/auth_required.json}"

# ── Settings swap with flock to prevent concurrent corruption ────────────────
# When parallel.sh invokes multiple agy.sh instances concurrently, each must
# hold an exclusive lock for the full read-modify-run-restore cycle.
LOCK_FILE="${SETTINGS}.hydra.lock"
exec 9>"$LOCK_FILE"
flock -x 9

original_model=$(jq -r '.model // empty' "$SETTINGS" 2>/dev/null || true)
restore_settings() {
  if [[ -n "$original_model" ]]; then
    jq --arg m "$original_model" '.model = $m' "$SETTINGS" > "${SETTINGS}.tmp" && mv "${SETTINGS}.tmp" "$SETTINGS"
  else
    jq 'del(.model)' "$SETTINGS" > "${SETTINGS}.tmp" && mv "${SETTINGS}.tmp" "$SETTINGS"
  fi
  flock -u 9
}
trap restore_settings EXIT

jq --arg m "$model_flag" '.model = $m' "$SETTINGS" > "${SETTINGS}.tmp" && mv "${SETTINGS}.tmp" "$SETTINGS"

# Capture stdout and stderr separately — auth errors come on stderr before any output
stderr_output=$(mktemp -t hydra-agy-stderr.XXXXXX)
trap 'rm -f "$stderr_output"; restore_settings' EXIT
output=$(agy --print "$prompt" --print-timeout "${AGY_TIMEOUT}s" 2>"$stderr_output")
exit_code=$?
stderr_content=$(cat "$stderr_output"); rm -f "$stderr_output"

# Auth detection: only check stderr and the first 3 lines of stdout
# Never scan full output — model responses may contain auth-related strings as code
first_lines=$(echo "$output" | head -3)
auth_signal=$(printf '%s\n%s' "$stderr_content" "$first_lines")

if echo "$auth_signal" | grep -qiE "not (logged|authenticated|authorized)|please (log in|sign in|authenticate)|login required|auth(entication)? required|visit https?://accounts\.google\.com|antigravity\.google/auth|sign in to continue"; then
  auth_url=$(echo "$auth_signal" | grep -oE 'https?://[^ ]+' | head -1)
  [[ -z "$auth_url" ]] && auth_url="run: agy interactively to authenticate"
  mkdir -p "$(dirname "$AUTH_FILE")"
  jq -n --arg pool "agy" --arg model "$model_flag" --arg url "$auth_url" --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{ pool: $pool, model: $model, auth_url: $url, detected_at: $ts }' > "$AUTH_FILE"
  echo "🔐 AUTH REQUIRED for agy ($model_flag)" >&2
  echo "   URL: $auth_url" >&2
  exit 3
fi

if [[ $exit_code -ne 0 ]]; then
  echo "$output"
  exit $exit_code
fi

# ── Token sidecar (estimated — agy --print doesn't expose real counts) ──────
if [[ -n "${HYDRA_TOKEN_SIDECAR:-}" ]]; then
  prompt_chars=${#prompt}
  response_chars=${#output}
  factor=4
  pricing_file="$HYDRA_DIR/registry/pricing.yaml"
  if [[ -f "$pricing_file" ]]; then
    f=$(yq -r '.estimate_factor // 4' "$pricing_file" 2>/dev/null || echo 4)
    # Require a strictly positive integer to avoid division by zero
    [[ "$f" =~ ^[1-9][0-9]*$ ]] && factor="$f"
  fi
  prompt_tokens=$(( prompt_chars / factor ))
  response_tokens=$(( response_chars / factor ))

  jq -n \
    --arg model "$model_flag" \
    --arg executor "agy" \
    --arg source "estimate" \
    --argjson prompt_tokens   "$prompt_tokens" \
    --argjson response_tokens "$response_tokens" \
    --argjson factor          "$factor" \
    '{ model: $model, executor: $executor, source: $source,
       prompt_tokens: $prompt_tokens, response_tokens: $response_tokens,
       estimate_factor: $factor }' \
    > "$HYDRA_TOKEN_SIDECAR" 2>/dev/null || true
fi

echo "$output"
