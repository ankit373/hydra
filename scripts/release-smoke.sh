#!/usr/bin/env bash
# Install one published release from one channel and check that it runs.
#
#   scripts/release-smoke.sh <brew|npm|pip|install.sh|archive> v1.7.0
#
# Passes when `hyctl --version` reports the tag and `hyctl --help` works with
# no network. Registries take a few minutes to serve a new version, so each
# install is retried before it counts as a failure.
set -euo pipefail

CHANNEL="${1:?usage: release-smoke.sh <channel> vX.Y.Z}"
TAG="${2:?usage: release-smoke.sh <channel> vX.Y.Z}"
VER="${TAG#v}"
REPO="ankit373/hydra"
TRIES="${SMOKE_TRIES:-10}"
WAIT="${SMOKE_WAIT_SECONDS:-30}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Keeps hyctl's own update banner out of the output.
export HYDRA_NO_UPDATE_CHECK=1

retry() {
  local n=1
  until "$@"; do
    if [ "$n" -ge "$TRIES" ]; then
      echo "::error::gave up after $TRIES tries: $*"
      return 1
    fi
    echo "try $n failed, waiting ${WAIT}s: $*"
    sleep "$WAIT"
    n=$((n + 1))
  done
}

is_windows() { case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) return 0 ;; esac; return 1; }

goos() {
  case "$(uname -s)" in
    Darwin) echo darwin ;;
    Linux) echo linux ;;
    *) echo windows ;;
  esac
}

goarch() {
  case "$(uname -m)" in
    arm64 | aarch64) echo arm64 ;;
    *) echo amd64 ;;
  esac
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# Runs a command with no way out. On Linux that is a fresh network namespace;
# macOS and Windows have no unprivileged equivalent, so there every proxy
# variable points at a closed port and any HTTP call fails at once.
offline() {
  local dead=http://127.0.0.1:9
  if [ "$(goos)" = linux ]; then
    sudo -E unshare --net --setuid "$(id -u)" --setgid "$(id -g)" -- \
      env PATH="$PATH" HOME="$HOME" HTTP_PROXY="$dead" HTTPS_PROXY="$dead" "$@"
  else
    env HTTP_PROXY="$dead" HTTPS_PROXY="$dead" ALL_PROXY="$dead" NO_PROXY= "$@"
  fi
}

check() {
  local bin="$1" out got
  out="$("$bin" --version)"
  echo "$out"
  got="$(printf '%s\n' "$out" | awk '$1 == "hydra" {print $2; exit}')"
  if [ "$got" != "$VER" ]; then
    echo "::error::$CHANNEL installed hyctl $got, want $VER"
    return 1
  fi
  if ! offline "$bin" --help >"$work/help.txt" 2>&1; then
    cat "$work/help.txt"
    echo "::error::$CHANNEL: hyctl --help failed with no network"
    return 1
  fi
  grep -q "Usage:" "$work/help.txt" || {
    cat "$work/help.txt"
    echo "::error::$CHANNEL: hyctl --help printed no usage"
    return 1
  }
  echo "ok: $CHANNEL hyctl $got"
}

brew_has_version() {
  brew update --quiet >/dev/null
  [ "$(brew info --json=v2 ankit373/hydra/hyctl | jq -r '.formulae[0].versions.stable')" = "$VER" ]
}

smoke_brew() {
  retry brew_has_version
  HOMEBREW_NO_AUTO_UPDATE=1 brew install ankit373/hydra/hyctl
  check "$(brew --prefix)/bin/hyctl"
}

smoke_npm() {
  retry npm install --global "hyctl@$VER"
  check "$(command -v hyctl)"
}

smoke_pip() {
  local venv="$work/venv" bin
  python -m venv "$venv"
  bin="$venv/bin"
  if is_windows; then bin="$venv/Scripts"; fi
  retry "$bin/python" -m pip install --quiet --no-cache-dir "hyctl==$VER"
  # The pip package fetches the binary on first run, so --version comes first.
  check "$bin/hyctl"
}

smoke_install_sh() {
  local dest="$work/bin"
  curl -fsSL "https://raw.githubusercontent.com/$REPO/main/install.sh" -o "$work/install.sh"
  HYDRA_VERSION="$TAG" HYDRA_BIN="$dest" sh "$work/install.sh"
  check "$dest/hyctl"

  # Unpinned, install.sh installs /releases/latest. Asking the API that
  # question directly is the same check without an unauthenticated call that
  # shared runner IPs often get rate limited on.
  if [ "${SMOKE_EXPECT_LATEST:-false}" = true ]; then
    local latest
    latest="$(gh release view --repo "$REPO" --json tagName -q .tagName)"
    if [ "$latest" != "$TAG" ]; then
      echo "::error::the latest release is $latest, not $TAG, so an unpinned install.sh gets $latest"
      return 1
    fi
    echo "ok: $TAG is the latest release"
  fi
}

smoke_archive() {
  local ext=tar.gz bin=hyctl name want got
  if is_windows; then ext=zip bin=hyctl.exe; fi
  name="hydra_${VER}_$(goos)_$(goarch).$ext"

  gh release download "$TAG" --repo "$REPO" --dir "$work" -p "$name" -p checksums.txt
  want="$(awk -v f="$name" '$2 == f {print $1}' "$work/checksums.txt")"
  got="$(sha256 "$work/$name")"
  if [ -z "$want" ]; then
    echo "::error::$name is not listed in checksums.txt"
    return 1
  fi
  if [ "$want" != "$got" ]; then
    echo "::error::checksum mismatch for $name: want $want, got $got"
    return 1
  fi
  echo "checksum ok: $name"

  mkdir -p "$work/x"
  if [ "$ext" = zip ]; then
    if command -v unzip >/dev/null 2>&1; then
      unzip -q "$work/$name" -d "$work/x"
    else
      7z x -y -o"$work/x" "$work/$name" >/dev/null
    fi
  else
    tar -xzf "$work/$name" -C "$work/x"
  fi
  check "$work/x/$bin"
}

case "$CHANNEL" in
  brew) smoke_brew ;;
  npm) smoke_npm ;;
  pip) smoke_pip ;;
  install.sh) smoke_install_sh ;;
  archive) smoke_archive ;;
  *)
    echo "unknown channel: $CHANNEL" >&2
    exit 2
    ;;
esac
