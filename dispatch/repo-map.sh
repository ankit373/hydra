#!/usr/bin/env bash
# Hydra Repo Map — dispatch/repo-map.sh
# ─────────────────────────────────────────────────────────────────────────────
# Builds a compressed, scoped symbol map of a package for prompt injection.
# Aider/Cline-style — gives the agent a "where things are" view without
# pasting whole files.
#
# Subcommands:
#   repo-map.sh for <file>           → emit package-scoped map for the file
#                                       (auto-detects: same git_root if any,
#                                        else top-level sub-package in workspace)
#   repo-map.sh build <root>         → full map of a directory tree
#   repo-map.sh stats <file>         → byte size + file count of the for-map
#
# Output format (compact, ~30-80 chars per symbol line):
#   ▸ packages/foo/src/auth.ts
#       export function login(email, password)
#       export async function logout()
#       export class AuthError extends Error
#       export const SESSION_KEY
#
# Language coverage:
#   .ts/.tsx/.js/.mjs/.cjs — top-level exports + interfaces + types + classes
#   .py                    — top-level def/class/async def
#   .go                    — top-level func/type
#   .rs                    — top-level pub fn/pub struct/pub enum
#   .sh                    — top-level function declarations
#   anything else          — skipped (just listed by path)
#
# Excludes: node_modules, .git, dist, build, .next, .wrangler, vendor, target,
# __pycache__, *.test.*, *.spec.*, *.d.ts (declaration files are noise).
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

HYDRA_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$HYDRA_DIR/logs"
LOG_FILE="$LOG_DIR/repo-map.log"
SCOPE="$HYDRA_DIR/dispatch/scope.sh"

mkdir -p "$LOG_DIR"
err() { echo "❌ repo-map: $*" >&2; }
log() { printf '[%s] repo-map: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >> "$LOG_FILE"; }

# Directory names to skip entirely
EXCLUDE_DIRS=(
  node_modules .git dist build .next .wrangler __pycache__ target vendor
  .turbo .venv venv
)

# ── Symbol extractor per file ─────────────────────────────────────────────────
extract_symbols() {
  local f="$1"
  local ext="${f##*.}"
  case "$ext" in
    ts|tsx|js|mjs|cjs)
      # Skip declaration files
      [[ "$f" == *.d.ts ]] && return 0
      # Match common top-level export forms
      grep -nE '^[[:space:]]*export[[:space:]]+(default[[:space:]]+)?(async[[:space:]]+)?(function|class|interface|type|enum|const|let|var)[[:space:]]+[A-Za-z_$][A-Za-z0-9_$]*' "$f" 2>/dev/null \
        | sed -E 's/^[[:space:]]*//; s/[[:space:]]*\{.*$//; s/[[:space:]]+=[[:space:]].*$//' \
        | awk '!seen[$0]++' \
        | head -40
      ;;
    py)
      grep -nE '^(def |class |async def )' "$f" 2>/dev/null \
        | sed -E 's/^[[:space:]]*//; s/:[[:space:]]*$//' \
        | head -40
      ;;
    go)
      grep -nE '^(func|type) ' "$f" 2>/dev/null \
        | sed -E 's/[[:space:]]*\{.*$//' \
        | head -40
      ;;
    rs)
      grep -nE '^(pub fn |pub struct |pub enum |pub trait |pub mod )' "$f" 2>/dev/null \
        | sed -E 's/[[:space:]]*\{.*$//' \
        | head -40
      ;;
    sh|bash)
      grep -nE '^[a-zA-Z_][a-zA-Z0-9_]*\(\)[[:space:]]*\{' "$f" 2>/dev/null \
        | sed -E 's/\(\).*$/()/' \
        | head -40
      ;;
    *)
      return 0
      ;;
  esac
}

# ── Determine scope root for a given file ─────────────────────────────────────
# Prefer enclosing .git root. If none, walk up to the workspace root's first
# child (e.g. apphire/core), giving a per-package scope.
scope_root_for() {
  local f="$1"
  local git_root; git_root=$("$SCOPE" git-root "$f" 2>/dev/null || true)
  if [[ -n "$git_root" ]]; then
    echo "$git_root"
    return 0
  fi
  local resolved; resolved=$("$SCOPE" resolve "$f" 2>/dev/null || true)
  local ws_root; ws_root=$(echo "$resolved" | jq -r '.root // empty' 2>/dev/null)
  if [[ -n "$ws_root" ]]; then
    # Walk up until parent is ws_root
    local p; p=$(dirname "$f")
    while [[ "$p" != "/" && "$(dirname "$p")" != "$ws_root" ]]; do
      p=$(dirname "$p")
    done
    [[ "$p" != "/" ]] && echo "$p" || echo "$ws_root"
    return 0
  fi
  err "no scope root for $f"; return 1
}

# ── Build a map for a directory tree ──────────────────────────────────────────
build_map() {
  local root="$1"
  [[ ! -d "$root" ]] && err "not a directory: $root" && return 1

  printf '# Repo map: %s\n# Generated: %s\n\n' "$root" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  # Build the prune predicate from EXCLUDE_DIRS
  local prune_args=()
  local first=1
  for d in "${EXCLUDE_DIRS[@]}"; do
    if [[ $first -eq 1 ]]; then
      prune_args+=(-name "$d")
      first=0
    else
      prune_args+=(-o -name "$d")
    fi
  done

  # Files in stable sorted order
  find "$root" -type d \( "${prune_args[@]}" \) -prune -o \
    -type f \
    \( -name "*.ts" -o -name "*.tsx" -o -name "*.js" -o -name "*.mjs" -o -name "*.cjs" \
       -o -name "*.py" -o -name "*.go" -o -name "*.rs" -o -name "*.sh" -o -name "*.bash" \) \
    ! -name "*.test.*" ! -name "*.spec.*" ! -name "*.d.ts" \
    -print 2>/dev/null \
    | sort \
    | while IFS= read -r f; do
        local rel="${f#$root/}"
        local syms
        syms=$(extract_symbols "$f" || true)
        if [[ -n "$syms" ]]; then
          printf '▸ %s\n' "$rel"
          echo "$syms" | sed 's/^/    /'
          printf '\n'
        fi
      done
}

# ── Subcommands ───────────────────────────────────────────────────────────────

cmd="${1:-}"; shift || true

case "$cmd" in
  for)
    f="${1:-}"
    [[ -z "$f" ]] && err "usage: repo-map.sh for <file>" && exit 1
    [[ "$f" != /* ]] && err "path must be absolute" && exit 1
    root=$(scope_root_for "$f") || exit 1
    log "for $f → root=$root"
    build_map "$root"
    ;;

  build)
    root="${1:-}"
    [[ -z "$root" ]] && err "usage: repo-map.sh build <root>" && exit 1
    [[ "$root" != /* ]] && err "path must be absolute" && exit 1
    log "build $root"
    build_map "$root"
    ;;

  stats)
    f="${1:-}"
    [[ -z "$f" ]] && err "usage: repo-map.sh stats <file>" && exit 1
    root=$(scope_root_for "$f") || exit 1
    map=$(build_map "$root")
    bytes=${#map}
    file_count=$(echo "$map" | grep -c '^▸ ' || true)
    sym_count=$(echo "$map" | grep -cE '^    ' || true)
    jq -n --arg root "$root" --argjson bytes "$bytes" \
      --argjson files "$file_count" --argjson syms "$sym_count" \
      '{ root: $root, bytes: $bytes, files: $files, symbols: $syms }'
    ;;

  *)
    err "unknown command: $cmd"
    err "usage: repo-map.sh {for|build|stats} <args>"
    exit 1
    ;;
esac
