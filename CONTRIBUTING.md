# Contributing to Hydra

Thank you for taking the time to contribute! This document explains how to get started, what we expect, and how to submit changes.

---

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Ways to Contribute](#ways-to-contribute)
- [Development Setup](#development-setup)
- [Making Changes](#making-changes)
- [Commit Style](#commit-style)
- [Pull Request Process](#pull-request-process)
- [Shell Script Standards](#shell-script-standards)
- [Registry / YAML Changes](#registry--yaml-changes)

---

## Code of Conduct

By participating you agree to our [Code of Conduct](CODE_OF_CONDUCT.md). We maintain a welcoming, inclusive environment — please read it.

---

## Ways to Contribute

- **Bug reports** — open a GitHub issue using the [bug template](.github/ISSUE_TEMPLATE/bug_report.md)
- **Feature requests** — open an issue using the [feature template](.github/ISSUE_TEMPLATE/feature_request.md)
- **Documentation improvements** — edit `.md` files and send a PR
- **New model integrations** — add an entry to `registry/models.yaml` + a tier in `registry/routing.yaml`
- **Shell script fixes** — see the standards section below
- **UI (Ink/React TUI)** — see `ui/README.md` for the frontend dev workflow

---

## Development Setup

### Prerequisites

| Tool | Purpose | Install |
|------|---------|---------|
| `bash` ≥ 5 | Shell runtime | `brew install bash` |
| `jq` | JSON processing | `brew install jq` |
| `yq` | YAML processing | `brew install yq` |
| `bun` | TUI runtime | `brew install oven-sh/bun/bun` |
| `shellcheck` | Shell linting | `brew install shellcheck` |

Optional (for full dispatch testing):

| Tool | Purpose |
|------|---------|
| `agy` (Antigravity CLI) | Tier 2-9 model execution |
| `ollama` | Tier 10 local inference |

### Local setup

```bash
git clone https://github.com/ankit373/hydra.git
cd hydra

# Install TUI dependencies
cd ui && bun install && cd ..

# Verify scripts are executable
chmod +x dispatch/*.sh hydra-ui install.sh

# Run status check
dispatch/route.sh --status
```

No secrets or API keys are needed just to browse, modify, or test the routing logic.

---

## Making Changes

1. Fork the repository and create a branch from `main`:
   ```bash
   git checkout -b feat/my-feature
   ```
2. Make your changes (see standards below).
3. Run `shellcheck` on any modified shell scripts:
   ```bash
   shellcheck dispatch/*.sh
   ```
4. Test manually:
   ```bash
   dispatch/route.sh --list
   dispatch/route.sh --status
   ```
5. Commit and push, then open a pull request.

---

## Commit Style

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short summary>
```

Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`

Examples:
```
feat(router): add gemini-2.0-flash-lite to tier 9 fallback chain
fix(agy): handle auth_required when settings.json is missing
docs(readme): clarify HYDRA_DATA vs HYDRA_HOME distinction
```

Keep the first line under 72 characters. Add a body paragraph if the *why* is non-obvious.

---

## Pull Request Process

1. Fill out the PR template completely.
2. Link the relevant issue (`Closes #123`).
3. Ensure CI passes (shellcheck, bun typecheck).
4. A maintainer will review within a few days.
5. Address review feedback in new commits — do not force-push while a review is open.
6. Once approved, a maintainer will squash-merge.

---

## Shell Script Standards

- `set -euo pipefail` at the top of every script.
- Quote all variable expansions: `"$VAR"`, not `$VAR`.
- Use `printf '%s\n'` instead of `echo` for data that may contain escape sequences.
- Never hardcode absolute paths like `/Users/someone/` — use `$HOME`.
- Never store secrets in scripts. Read from env vars or tool-managed config files.
- Prefer `[[ ]]` over `[ ]` for conditionals.
- Run `shellcheck` — zero warnings/errors required for new files, improvements welcome for existing.

---

## Registry / YAML Changes

- `registry/routing.yaml` — the single source of truth for tier/enum mappings. Keep enum names SCREAMING_SNAKE_CASE.
- `registry/models.yaml` — add new models here. Include `pool`, `token_limit`, and `pricing_key`.
- `registry/domains.yaml` — maps task domains to enum keys. Keep entries alphabetically sorted.
- Always validate YAML syntax: `yq '.' registry/models.yaml > /dev/null`.

---

## Questions?

Open a [Discussion](https://github.com/ankit373/hydra/discussions) on GitHub — we're happy to help.
