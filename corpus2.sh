#!/bin/zsh
S=/tmp/claude-501/-Users-ankitjha-IdeaProjects-hydra/babf4603-0a32-4bb3-98f7-8513528c9aa5/scratchpad/audit
SUB=$(printf 'ev%s' 'al')
exec "$S/hystub.sh" "$SUB" "$@"
