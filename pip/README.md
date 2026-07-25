# hyctl

**Hydra** — a local-first, multi-vendor AI control plane. This package installs the `hyctl` CLI.

```bash
pip install hyctl
hyctl init
```

On first run it downloads the prebuilt `hyctl` binary matching this package's version from the [GitHub release](https://github.com/ankit373/hydra/releases), verifies its checksum, and caches it under `~/.cache/hyctl/`. No Go toolchain required.

## Other install methods

```bash
brew tap ankit373/hydra && brew install hyctl          # Homebrew
npm install -g hyctl                                    # npm
npx hyctl init                                          # npx (no install)
curl -fsSL https://raw.githubusercontent.com/ankit373/hydra/main/install.sh | sh   # standalone
```

- Docs: https://hydra.uvansa.com
- Source: https://github.com/ankit373/hydra
- License: MIT
