# hyctl

**Hydra** sends each AI task to the model best suited to it, and checks the answer. This npm package installs the prebuilt `hyctl` binary for your platform.

```bash
npm install -g hyctl
hyctl init
```

On install, it downloads the `hyctl` binary matching this package's version from the [GitHub release](https://github.com/ankit373/hydra/releases) and verifies its checksum. No Go toolchain required.

> **Note:** unrelated to the `hydra`/`hydra-cli` packages (pnxtech microservice libraries). This project's CLI is named `hyctl`.

## Other install methods

```bash
# Homebrew
brew trust ankit373/hydra && brew install ankit373/hydra/hyctl

# Standalone (curl)
curl -fsSL https://raw.githubusercontent.com/ankit373/hydra/main/install.sh | sh
```

- Docs: https://hydra.uvansa.com
- Source: https://github.com/ankit373/hydra
- License: MIT
