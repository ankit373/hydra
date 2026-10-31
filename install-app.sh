#!/usr/bin/env sh
# Hydra Desktop, standalone app installer
#
#   curl -fsSL https://raw.githubusercontent.com/ankit373/hydra/main/install-app.sh | sh
#
# Downloads the prebuilt desktop app for your OS/arch from the latest GitHub
# release, verifies it against the published .sha256, and installs it.
#
# The release assets embed their version in the filename
# (hydra-desktop_v1.2.0_darwin_universal.zip), so GitHub's /latest/download/
# shortcut cannot address them, the tag has to be resolved first. That is most
# of what this script is for.
#
# Environment overrides:
#   HYDRA_VERSION=v1.2.0     pin a specific release (default: latest)
#   HYDRA_APP_DIR=/opt/hydra install directory (default: /Applications on macOS,
#                            ~/.local/share/hydra elsewhere)
set -eu

REPO="ankit373/hydra"
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

# ── Detect OS / arch ───────────────────────────────────────────────────────────
#
# The desktop app is not built for every target the CLI is. Saying so here beats
# downloading an archive that will not start (#263).
os="$(uname -s)"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)  ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)             die "unsupported architecture: $arch" ;;
esac

case "$os" in
  Darwin)
    # macOS ships as a universal binary, one archive for Apple Silicon and Intel.
    PLATFORM="darwin_universal"
    EXT="zip"
    ;;
  Linux)
    PLATFORM="linux_${ARCH}"
    EXT="tar.gz"
    ;;
  MINGW*|MSYS*|CYGWIN*)
    die "on Windows, download the .zip from ${BASE}/latest and unzip it, \
this script needs a POSIX shell"
    ;;
  *)
    die "unsupported OS: $os"
    ;;
esac

echo "Hydra Desktop installer"

# ── Resolve version ────────────────────────────────────────────────────────────
if [ "${HYDRA_VERSION:-}" != "" ]; then
  TAG="$HYDRA_VERSION"
else
  # Read the whole body before parsing. Piping straight into `grep -m1` closes
  # the pipe early and curl then prints a scary "Failure writing output to
  # destination" to stderr even though the fetch succeeded.
  LATEST="$($DL "https://api.github.com/repos/${REPO}/releases/latest" || true)"
  TAG="$(printf '%s' "$LATEST" \
    | sed -n -E 's/.*"tag_name" *: *"([^"]+)".*/\1/p' \
    | head -n 1)"
  [ "${TAG:-}" != "" ] || die "could not determine latest release, set HYDRA_VERSION=vX.Y.Z"
fi

ARCHIVE="hydra-desktop_${TAG}_${PLATFORM}.${EXT}"
URL="${BASE}/download/${TAG}/${ARCHIVE}"
SUM_URL="${URL}.sha256"

info "Release : ${TAG}"
info "Target  : ${PLATFORM}"
info "Archive : ${ARCHIVE}"

# ── Download ───────────────────────────────────────────────────────────────────
TMP="$(mktemp -d 2>/dev/null || mktemp -d -t hydra-desktop)"
trap 'rm -rf "$TMP"' EXIT INT TERM

echo "-> Downloading..."
#
# A missing archive is the normal way this fails, and the reason is almost
# always one of two things: a tag from before the app existed, or a target
# whose builds started later than the tag being installed. Naming both beats a
# bare 404, and asking for a newer release is something the user can act on.
if ! $DLO "$TMP/$ARCHIVE" "$URL"; then
  warn "no ${ARCHIVE} on release ${TAG}"
  case "$PLATFORM" in
    *_arm64) die "linux/arm64 desktop builds start after v1.2.0 (#263). Pick a \
newer release, or use the CLI, which has shipped for arm64 all along." ;;
    *)       die "the desktop app first shipped in v1.1.0, older tags have no \
build. Try without HYDRA_VERSION to take the newest release." ;;
  esac
fi

# ── Verify checksum ────────────────────────────────────────────────────────────
#
# Each desktop asset carries its own .sha256 rather than appearing in the shared
# checksums.txt, so this reads that file instead of grepping the combined one.
if $DLO "$TMP/expected.sha256" "$SUM_URL" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    SHA="$(sha256sum "$TMP/$ARCHIVE" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    SHA="$(shasum -a 256 "$TMP/$ARCHIVE" | awk '{print $1}')"
  else
    SHA=""
    warn "no sha256 tool found, skipping checksum verification"
  fi
  if [ "${SHA:-}" != "" ]; then
    EXPECTED="$(awk '{print $1}' "$TMP/expected.sha256")"
    [ "${EXPECTED:-}" != "" ] || die "published .sha256 for ${ARCHIVE} is empty"
    [ "$SHA" = "$EXPECTED" ] || die "checksum mismatch, refusing to install (expected ${EXPECTED}, got ${SHA})"
    info "Checksum: verified"
  fi
else
  warn "no .sha256 published for ${ARCHIVE}, skipping verification"
fi

# ── Extract ────────────────────────────────────────────────────────────────────
echo "-> Extracting..."
if [ "$EXT" = "zip" ]; then
  need unzip
  unzip -q "$TMP/$ARCHIVE" -d "$TMP/out" || die "could not unzip ${ARCHIVE}"
else
  need tar
  mkdir -p "$TMP/out"
  tar -xzf "$TMP/$ARCHIVE" -C "$TMP/out" || die "could not untar ${ARCHIVE}"
fi

# ── Install ────────────────────────────────────────────────────────────────────
if [ "$os" = "Darwin" ]; then
  APP="$(find "$TMP/out" -maxdepth 2 -name '*.app' -print -quit)"
  [ -n "${APP:-}" ] || die "no .app bundle inside ${ARCHIVE}"
  DEST="${HYDRA_APP_DIR:-/Applications}"
  mkdir -p "$DEST" 2>/dev/null || true
  if [ ! -w "$DEST" ]; then
    DEST="$HOME/Applications"
    mkdir -p "$DEST"
    warn "/Applications is not writable, installing to ${DEST}"
  fi
  NAME="$(basename "$APP")"
  rm -rf "${DEST:?}/${NAME}"
  cp -R "$APP" "$DEST/$NAME"

  # The build is not notarised, so Gatekeeper quarantines it and the first
  # launch fails with "damaged". Clearing the quarantine flag on a file the
  # user just chose to install is the same decision right-click → Open makes,
  # made once here instead of confusingly at launch.
  if command -v xattr >/dev/null 2>&1; then
    xattr -dr com.apple.quarantine "$DEST/$NAME" 2>/dev/null || true
  fi
  info "Installed: ${DEST}/${NAME}"
  echo ""
  echo "Done, open it from Launchpad, or:  open \"${DEST}/${NAME}\""
else
  BIN="$(find "$TMP/out" -maxdepth 2 -type f -perm -u+x -print -quit)"
  [ -n "${BIN:-}" ] || die "no executable inside ${ARCHIVE}"
  DEST="${HYDRA_APP_DIR:-$HOME/.local/share/hydra}"
  mkdir -p "$DEST"
  cp "$BIN" "$DEST/hydra-desktop"
  chmod +x "$DEST/hydra-desktop"
  info "Installed: ${DEST}/hydra-desktop"

  # A launcher on PATH, so it can be started by name like any other tool.
  LINKDIR="$HOME/.local/bin"
  mkdir -p "$LINKDIR"
  ln -sf "$DEST/hydra-desktop" "$LINKDIR/hydra-desktop"
  case ":$PATH:" in
    *":$LINKDIR:"*) : ;;
    *) warn "add ${LINKDIR} to your PATH:  export PATH=\"\$PATH:${LINKDIR}\"" ;;
  esac
  echo ""
  echo "Done, run:  hydra-desktop"
fi

# ── Point at the CLI if it is missing ──────────────────────────────────────────
#
# The app reads the logs hyctl writes. Without it there is nothing to display,
# and an empty app looks broken rather than unconfigured.
if ! command -v hyctl >/dev/null 2>&1; then
  echo ""
  warn "hyctl is not on your PATH. The app reads the logs the CLI writes, so"
  warn "install it too:  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sh"
fi
