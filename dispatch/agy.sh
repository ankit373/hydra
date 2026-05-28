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
AUTH_FILE="${HYDRA_AUTH_FILE:-$HOME/hydra/logs/auth_required.json}"

# Swap model in settings.json, restore on exit
original_model=$(jq -r '.model // empty' "$SETTINGS" 2>/dev/null || true)
restore_settings() {
  if [[ -n "$original_model" ]]; then
    jq --arg m "$original_model" '.model = $m' "$SETTINGS" > "${SETTINGS}.tmp" && mv "${SETTINGS}.tmp" "$SETTINGS"
  else
    jq 'del(.model)' "$SETTINGS" > "${SETTINGS}.tmp" && mv "${SETTINGS}.tmp" "$SETTINGS"
  fi
}
trap restore_settings EXIT

jq --arg m "$model_flag" '.model = $m' "$SETTINGS" > "${SETTINGS}.tmp" && mv "${SETTINGS}.tmp" "$SETTINGS"

# Capture stdout and stderr separately — auth errors come on stderr before any output
stderr_output=$(mktemp)
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
  if [[ -f "$HOME/hydra/registry/pricing.yaml" ]]; then
    f=$(yq -r '.estimate_factor // 4' "$HOME/hydra/registry/pricing.yaml" 2>/dev/null || echo 4)
    [[ "$f" =~ ^[0-9]+$ ]] && factor="$f"
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
