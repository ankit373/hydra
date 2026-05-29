#!/usr/bin/env bash
# Hydra — standalone installer
# Usage: ./install.sh
#        HYDRA_HOME=/custom/path ./install.sh
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Normalize to absolute path so the baked-in launcher works from any CWD.
_raw_dest="${HYDRA_HOME:-$HOME/.hydra}"
mkdir -p "$_raw_dest"
DEST="$(cd "$_raw_dest" && pwd)"
unset _raw_dest
SHELL_RC="${SHELL_RC:-}"

echo "🐍 Hydra installer"
echo "   Source : $REPO"
echo "   Dest   : $DEST"
echo ""

# ── Dependency checks ────────────────────────────────────────────────────────
missing=()
for dep in bun jq yq; do
  command -v "$dep" &>/dev/null || missing+=("$dep")
done
if [[ ${#missing[@]} -gt 0 ]]; then
  echo "❌ Missing: ${missing[*]}"
  echo "   brew install ${missing[*]}"
  exit 1
fi

# ── Copy files ───────────────────────────────────────────────────────────────
mkdir -p "$DEST/logs"
for dir in dispatch registry context skills ui; do
  rm -rf "$DEST/$dir"
  cp -r "$REPO/$dir" "$DEST/$dir"
done
chmod +x "$DEST/dispatch/"*.sh

# Install UI dependencies (frozen — uses existing bun.lock)
cd "$DEST/ui"
bun install --frozen-lockfile --silent
cd "$REPO"

# ── Seed state.json ──────────────────────────────────────────────────────────
if [[ ! -f "$DEST/logs/state.json" ]]; then
  echo '{"claude_pct":0,"exhausted_pools":[]}' > "$DEST/logs/state.json"
fi

# ── Create hydra launcher ────────────────────────────────────────────────────
LAUNCHER="$DEST/hydra"
cat > "$LAUNCHER" <<SH
#!/usr/bin/env bash
export HYDRA_HOME="$DEST"
export HYDRA_DATA="\${HYDRA_DATA:-\$HOME/.hydra}"
mkdir -p "\$HYDRA_DATA/logs"
[[ -f "\$HYDRA_DATA/logs/state.json" ]] || echo '{"claude_pct":0,"exhausted_pools":[]}' > "\$HYDRA_DATA/logs/state.json"
exec env NODE_ENV=production bun "$DEST/ui/src/index.tsx" "\$@"
SH
chmod +x "$LAUNCHER"

# ── Shell integration ────────────────────────────────────────────────────────
if [[ -z "$SHELL_RC" ]]; then
  if [[ -f "$HOME/.zshrc" ]]; then SHELL_RC="$HOME/.zshrc"
  elif [[ -f "$HOME/.bashrc" ]]; then SHELL_RC="$HOME/.bashrc"
  fi
fi

if [[ -n "$SHELL_RC" ]] && ! grep -q "HYDRA_HOME" "$SHELL_RC" 2>/dev/null; then
  {
    echo ""
    echo "# Hydra"
    echo "export HYDRA_HOME=\"$DEST\""
    echo "export PATH=\"\$PATH:$DEST\""
  } >> "$SHELL_RC"
  echo "✓ Added HYDRA_HOME and PATH to $SHELL_RC"
fi

echo ""
echo "✓ Installed to $DEST"
echo ""
echo "  Run now   : $DEST/hydra"
echo "  After rc  : source ${SHELL_RC:-~/.zshrc} && hydra"
