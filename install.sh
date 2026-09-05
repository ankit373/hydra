#!/usr/bin/env sh
# Hydra (hyctl), standalone binary installer
#
#   curl -fsSL https://raw.githubusercontent.com/ankit373/hydra/main/install.sh | sh
#
# Downloads the prebuilt `hyctl` binary for your OS/arch from the latest
# GitHub release, verifies it against the published checksums, and installs it.
# No Go toolchain required.
#
# Environment overrides:
#   HYDRA_VERSION=v1.2.0       pin a specific release (default: latest)
#   HYDRA_BIN=/usr/local/bin   install directory (default: auto)
set -eu

REPO="ankit373/hydra"
PROJECT="hydra"      # goreleaser project_name → archive filename prefix
BINARY="hyctl"       # binary name inside the archive
BASE="https://github.com/${REPO}/releases"

info()  { printf '  %s\n' "$*"; }
warn()  { printf '  ! %s\n' "$*" >&2; }
die()   { printf '  x %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "required tool not found: $1"; }

# ── Detect a downloader ────────────────────────────────────────────────────────
if command -v curl >/dev/null 2>&1; then
  DL="curl -fsSL"
  DLO="curl -fsSL -o"
elif command -v wget >/dev/null 2>&1; then
  DL="wget -qO-"
  DLO="wget -qO"
else
  die "need curl or wget to download"
fi

# ── Detect OS ──────────────────────────────────────────────────────────────────
os="$(uname -s)"
case "$os" in
  Darwin) OS="darwin" ;;
  Linux)  OS="linux" ;;
  *)      die "unsupported OS: $os (Windows: download the .zip from ${BASE}/latest)" ;;
esac

# ── Detect arch ────────────────────────────────────────────────────────────────
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)   ARCH="amd64" ;;
  arm64|aarch64)  ARCH="arm64" ;;
  *)              die "unsupported architecture: $arch" ;;
esac

echo "Hydra installer (hyctl)"

# ── Resolve version ────────────────────────────────────────────────────────────
if [ "${HYDRA_VERSION:-}" != "" ]; then
  TAG="$HYDRA_VERSION"
else
  # Parse tag_name from the GitHub API without requiring jq.
  TAG="$($DL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -m1 '"tag_name"' \
    | sed -E 's/.*"tag_name" *: *"([^"]+)".*/\1/')"
  [ "${TAG:-}" != "" ] || die "could not determine latest release, set HYDRA_VERSION=vX.Y.Z"
fi

# Archive version = tag without a leading 'v' (matches goreleaser name_template).
VER="${TAG#v}"
ARCHIVE="${PROJECT}_${VER}_${OS}_${ARCH}.tar.gz"
URL="${BASE}/download/${TAG}/${ARCHIVE}"
SUMS_URL="${BASE}/download/${TAG}/checksums.txt"

info "Release : ${TAG}"
info "Target  : ${OS}/${ARCH}"
info "Archive : ${ARCHIVE}"

# ── Download into a temp dir ───────────────────────────────────────────────────
TMP="$(mktemp -d 2>/dev/null || mktemp -d -t hyctl)"
trap 'rm -rf "$TMP"' EXIT INT TERM

echo "-> Downloading..."
$DLO "$TMP/$ARCHIVE" "$URL" || die "download failed: $URL"

# ── Verify checksum (skip only if no checksums.txt or no sha tool) ──────────────
if $DLO "$TMP/checksums.txt" "$SUMS_URL" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    SHA="$(sha256sum "$TMP/$ARCHIVE" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    SHA="$(shasum -a 256 "$TMP/$ARCHIVE" | awk '{print $1}')"
  else
    SHA=""
    warn "no sha256 tool found, skipping checksum verification"
  fi
  if [ "${SHA:-}" != "" ]; then
    EXPECTED="$(grep " ${ARCHIVE}\$" "$TMP/checksums.txt" | awk '{print $1}')"
    [ "${EXPECTED:-}" != "" ] || die "checksum for ${ARCHIVE} not found in checksums.txt"
    [ "$SHA" = "$EXPECTED" ] || die "checksum mismatch, refusing to install (expected ${EXPECTED}, got ${SHA})"
    info "Checksum: verified"
  fi
else
  warn "checksums.txt not published for ${TAG}, skipping verification"
fi

# ── Extract ────────────────────────────────────────────────────────────────────
need tar
tar -xzf "$TMP/$ARCHIVE" -C "$TMP" "$BINARY" || die "archive did not contain '${BINARY}'"
chmod +x "$TMP/$BINARY"

# ── Choose install dir ─────────────────────────────────────────────────────────
if [ "${HYDRA_BIN:-}" != "" ]; then
  DEST="$HYDRA_BIN"
elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
  DEST="/usr/local/bin"
else
  DEST="$HOME/.local/bin"
fi
mkdir -p "$DEST"

mv "$TMP/$BINARY" "$DEST/$BINARY"
info "Installed: ${DEST}/${BINARY}"

# ── PATH hint ──────────────────────────────────────────────────────────────────
case ":$PATH:" in
  *":$DEST:"*) : ;;
  *) warn "add ${DEST} to your PATH:  export PATH=\"\$PATH:${DEST}\"" ;;
esac

echo ""
echo "Done, ${BINARY} ${TAG} installed. Run:  ${BINARY} init"
