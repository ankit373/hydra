#!/usr/bin/env bash
# Hydra Workspace Scope Checker — dispatch/scope.sh
# ─────────────────────────────────────────────────────────────────────────────
# Validates that a file path is inside a defined workspace and matches the
# workspace's allowed_globs / denied_globs. Used by edit.sh before any write.
#
# Usage:
#   scope.sh check <absolute-file-path>           → prints workspace name, exit 0
#                                                    or prints reason to stderr, exit 1
#   scope.sh resolve <absolute-file-path>         → prints JSON { workspace, root, git, git_root }
#   scope.sh validator <ext>                      → prints validator command template (or empty)
#   scope.sh git-root <absolute-file-path>        → prints enclosing .git root or empty
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
WS_FILE="$HYDRA_DIR/registry/workspace.yaml"
LOG_DIR="$HYDRA_DIR/logs"
LOG_FILE="$LOG_DIR/scope.log"

mkdir -p "$LOG_DIR"
log() { printf '[%s] scope: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG_FILE"; }
err() { echo "❌ scope: $*" >&2; log "ERROR: $*"; }

[[ ! -f "$WS_FILE" ]] && err "workspace.yaml not found at $WS_FILE" && exit 1

# ── Helpers ───────────────────────────────────────────────────────────────────

ws_names() { yq '.workspaces | keys | .[]' "$WS_FILE" | tr -d '"'; }
ws_field() { yq ".workspaces.$1.$2" "$WS_FILE" | tr -d '"'; }
ws_globs() { yq ".workspaces.$1.$2 | .[]" "$WS_FILE" 2>/dev/null | tr -d '"'; }

# Walk up from a path to find an enclosing .git directory
git_root_of() {
  local p="$1"
  [[ -e "$p" ]] || p="$(dirname "$p")"          # if file doesn't exist yet, use parent
  while [[ "$p" != "/" && -n "$p" ]]; do
    if [[ -d "$p/.git" ]]; then
      echo "$p"
      return 0
    fi
    p="$(dirname "$p")"
  done
  return 1
}

# Match a path against a glob (relative to workspace root). Uses bash extglob.
shopt -s globstar extglob nullglob 2>/dev/null || true
match_glob() {
  local rel="$1"; local pat="$2"
  # Bash [[ pattern ]] handles ** when globstar is on; emulate by stripping leading ./
  rel="${rel#./}"
  # shellcheck disable=SC2053
  [[ "$rel" == $pat ]]
}

# Find the workspace whose root contains the given absolute path.
# Honors HYDRA_WORKSPACE override if set.
find_workspace() {
  local target="$1"

  if [[ -n "${HYDRA_WORKSPACE:-}" ]]; then
    local root; root=$(ws_field "$HYDRA_WORKSPACE" root)
    if [[ -z "$root" || "$root" == "null" ]]; then
      err "HYDRA_WORKSPACE=$HYDRA_WORKSPACE not defined in registry"
      return 1
    fi
    if [[ "$target" == "$root"/* || "$target" == "$root" ]]; then
      echo "$HYDRA_WORKSPACE"; return 0
    fi
    err "HYDRA_WORKSPACE=$HYDRA_WORKSPACE root ($root) does not contain $target"
    return 1
  fi

  while IFS= read -r name; do
    local root; root=$(ws_field "$name" root)
    [[ -z "$root" || "$root" == "null" ]] && continue
    if [[ "$target" == "$root"/* || "$target" == "$root" ]]; then
      echo "$name"; return 0
    fi
  done < <(ws_names)

  return 1
}

# ── Subcommands ───────────────────────────────────────────────────────────────

cmd="${1:-}"; shift || true

case "$cmd" in

  check)
    target="${1:-}"
    [[ -z "$target" ]] && err "usage: scope.sh check <file>" && exit 1
    [[ "$target" != /* ]] && err "path must be absolute: $target" && exit 1

    ws=$(find_workspace "$target") || { err "no workspace contains $target"; exit 1; }
    root=$(ws_field "$ws" root)
    rel="${target#"$root/"}"

    # Denied globs win
    while IFS= read -r pat; do
      [[ -z "$pat" ]] && continue
      if match_glob "$rel" "$pat"; then
        err "DENIED by glob '$pat' in workspace '$ws': $target"
        exit 1
      fi
    done < <(ws_globs "$ws" denied_globs)

    # Must match at least one allowed glob
    allowed=0
    while IFS= read -r pat; do
      [[ -z "$pat" ]] && continue
      if match_glob "$rel" "$pat"; then allowed=1; break; fi
    done < <(ws_globs "$ws" allowed_globs)

    if [[ $allowed -eq 0 ]]; then
      err "not in any allowed_glob for workspace '$ws': $rel"
      exit 1
    fi

    log "OK workspace=$ws path=$rel"
    echo "$ws"
    ;;

  resolve)
    target="${1:-}"
    [[ -z "$target" ]] && err "usage: scope.sh resolve <file>" && exit 1
    [[ "$target" != /* ]] && err "path must be absolute: $target" && exit 1

    ws=$(find_workspace "$target") || { err "no workspace contains $target"; exit 1; }
    root=$(ws_field "$ws" root)
    git_setting=$(ws_field "$ws" git)
    git_root=""

    case "$git_setting" in
      auto) git_root=$(git_root_of "$target" || true) ;;
      true) git_root="$root" ;;
      false|null|"") git_root="" ;;
    esac

    jq -n \
      --arg workspace "$ws" \
      --arg root "$root" \
      --arg git "$git_setting" \
      --arg git_root "$git_root" \
      '{ workspace: $workspace, root: $root, git: $git, git_root: $git_root }'
    ;;

  validator)
    ext="${1:-}"
    [[ -z "$ext" ]] && err "usage: scope.sh validator <ext>" && exit 1
    v=$(yq ".validators.$ext" "$WS_FILE" 2>/dev/null | tr -d '"')
    [[ "$v" == "null" || -z "$v" ]] && echo "" || echo "$v"
    ;;

  git-root)
    target="${1:-}"
    [[ -z "$target" ]] && err "usage: scope.sh git-root <file>" && exit 1
    git_root_of "$target" || echo ""
    ;;

  *)
    err "unknown command: $cmd"
    err "usage: scope.sh {check|resolve|validator|git-root} <args>"
    exit 1
    ;;

esac
