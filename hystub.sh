#!/bin/zsh
# hyctl with the stub head as the ONLY Ollama server. Isolated HYDRA_HOME.
S=/tmp/claude-501/-Users-ankitjha-IdeaProjects-hydra/babf4603-0a32-4bb3-98f7-8513528c9aa5/scratchpad/audit
export HYDRA_HOME=$S/home2
export OLLAMA_HOST=http://127.0.0.1:19434
mkdir -p "$HYDRA_HOME"
exec /tmp/hyctl-edit-audit "$@"
