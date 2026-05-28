#!/usr/bin/env bash
# Hydra weekly maintenance — scripts/maintenance.sh
# ─────────────────────────────────────────────────────────────────────────────
# Run by the LaunchAgent com.hydra.maintenance every Sunday at 03:00 local.
# Safe to run manually any time:
#   ~/hydra/scripts/maintenance.sh           # do it
#   ~/hydra/scripts/maintenance.sh --dry-run # show what would happen
#
# What it does:
#   1. For each workspace in workspace.yaml: prune non-open runs older than
#      30 days (does NOT include completed runs — preserve ship history).
#   2. Rotate ~/hydra/logs/*.log files over 5MB, keep last 5 generations.
#
# Logs to ~/hydra/logs/maintenance.log.
# To disable: launchctl unload ~/Library/LaunchAgents/com.hydra.maintenance.plist
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
COMPANY="$HYDRA_DIR/dispatch/company.sh"
WS_FILE="$HYDRA_DIR/registry/workspace.yaml"
LOG_FILE="$HYDRA_DIR/logs/maintenance.log"

mkdir -p "$(dirname "$LOG_FILE")"

DRY_RUN=false
for arg in "$@"; do
  [[ "$arg" == "--dry-run" ]] && DRY_RUN=true
done

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { printf '[%s] maintenance: %s\n' "$(ts)" "$*" | tee -a "$LOG_FILE"; }

log "===== run start (dry-run=$DRY_RUN) ====="

# Iterate every registered workspace
if [[ -f "$WS_FILE" ]]; then
  while IFS= read -r ws_root; do
    [[ -z "$ws_root" ]] && continue
    [[ -d "$ws_root" ]] || { log "skip (missing): $ws_root"; continue; }
    log "prune workspace: $ws_root"
    if $DRY_RUN; then
      "$COMPANY" prune --workspace "$ws_root" --older-than 30d --dry-run 2>&1 | tee -a "$LOG_FILE" || true
    else
      "$COMPANY" prune --workspace "$ws_root" --older-than 30d 2>&1 | tee -a "$LOG_FILE" || true
    fi
  done < <(yq '.workspaces | to_entries | .[].value.root' "$WS_FILE" | tr -d '"')
else
  log "no workspace.yaml — skipping prune step"
fi

# Rotate logs
log "rotate logs"
if $DRY_RUN; then
  log "(dry-run: would rotate any *.log > 5M)"
else
  "$COMPANY" rotate-logs --max-size 5M --keep 5 2>&1 | tee -a "$LOG_FILE" || true
fi

log "===== run end ====="
