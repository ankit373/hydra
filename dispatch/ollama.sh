#!/usr/bin/env bash
# Hydra — dispatch/ollama.sh
# ─────────────────────────────────────────────────────────────────────────────
# Wrapper around the Ollama REST API. Checks server health before dispatching.
# Called by route.sh. Do not call directly unless testing.
#
# Usage: ollama.sh <model_flag> "<prompt>"
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

model_flag="${1:-}"
prompt="${2:-}"

[[ -z "$model_flag" ]] && echo "❌ ollama.sh: model_flag required" >&2 && exit 1
[[ -z "$prompt" ]]     && echo "❌ ollama.sh: prompt required" >&2 && exit 1

OLLAMA_HOST="${OLLAMA_HOST:-http://localhost:11434}"

# ── Health check ──────────────────────────────────────────────────────────────
if ! curl -sf "$OLLAMA_HOST/" > /dev/null 2>&1; then
  echo "⚠️  Ollama not running. Starting..." >&2
  ollama serve &>/dev/null &
  sleep 3
  if ! curl -sf "$OLLAMA_HOST/" > /dev/null 2>&1; then
    echo "❌ Ollama failed to start. Is it installed?" >&2
    exit 1
  fi
fi

# ── Dispatch ──────────────────────────────────────────────────────────────────
response=$(curl -sf "$OLLAMA_HOST/api/generate" \
  -H "Content-Type: application/json" \
  -d "$(jq -n \
    --arg model "$model_flag" \
    --arg prompt "$prompt" \
    '{model: $model, prompt: $prompt, stream: false}'
  )")

if [[ -z "$response" ]]; then
  echo "❌ Ollama returned empty response" >&2
  exit 1
fi

# ── Token sidecar (real counts from Ollama API) ──────────────────────────────
# route.sh sets HYDRA_TOKEN_SIDECAR to a temp path; if absent, skip silently.
if [[ -n "${HYDRA_TOKEN_SIDECAR:-}" ]]; then
  echo "$response" | jq \
    --arg model "$model_flag" \
    --arg executor "ollama" \
    --arg source "real" \
    '{ model: $model, executor: $executor, source: $source,
       prompt_tokens:   (.prompt_eval_count // 0),
       response_tokens: (.eval_count        // 0),
       prompt_eval_ns:  (.prompt_eval_duration // 0),
       eval_ns:         (.eval_duration       // 0) }' \
    > "$HYDRA_TOKEN_SIDECAR" 2>/dev/null || true
fi

echo "$response" | jq -r '.response'
