#!/bin/zsh
# wrapper: isolated HYDRA_HOME, never touches ~/.hydra
export HYDRA_HOME=/tmp/claude-501/-Users-ankitjha-IdeaProjects-hydra/babf4603-0a32-4bb3-98f7-8513528c9aa5/scratchpad/audit/home
mkdir -p "$HYDRA_HOME"
exec /tmp/hyctl-edit-audit "$@"
