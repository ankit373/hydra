# Contributing to Hydra

Thank you for taking the time to contribute! This document explains how to get started, what we expect, and how to submit changes.

---

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Ways to Contribute](#ways-to-contribute)
- [Development Setup](#development-setup)
- [Making Changes](#making-changes)
- [The Regression Contract](#the-regression-contract)
- [Commit Style](#commit-style)
- [Pull Request Process](#pull-request-process)
- [Shell Script Standards](#shell-script-standards)
- [Registry / YAML Changes](#registry--yaml-changes)

---

## Code of Conduct

By participating you agree to our [Code of Conduct](CODE_OF_CONDUCT.md). We maintain a welcoming, inclusive environment, please read it.

---

## Ways to Contribute

- **Bug reports**, open a GitHub issue using the [bug template](.github/ISSUE_TEMPLATE/bug_report.md)
- **Feature requests**, open an issue using the [feature template](.github/ISSUE_TEMPLATE/feature_request.md)
- **Documentation improvements**, edit `.md` files and send a PR
- **New model integrations**, add an entry to `registry/models.yaml` + a tier in `registry/routing.yaml`
- **Terminal cockpit**, `internal/tui` (Bubble Tea)
- **Desktop app**, `desktop/` (Wails v2 + React/TS); its Go backend is `desktop/api`

---

## Development Setup

### Prerequisites

| Tool | Purpose | Install |
|------|---------|---------|
| `bash` ≥ 5 | Shell runtime | `brew install bash` |
| `jq` | JSON processing | `brew install jq` |
| `yq` | YAML processing | `brew install yq` |
| `node` ≥ 20 | Desktop frontend | `brew install node` |
| `shellcheck` | Shell linting | `brew install shellcheck` |
| `wails` | Desktop app builds (optional) | `go install github.com/wailsapp/wails/v2/cmd/wails@latest` |

Optional (for full dispatch testing):

| Tool | Purpose |
|------|---------|
| `agy` (Antigravity CLI) | Tier 2-9 model execution |
| `ollama` | Tier 10 local inference |

### Local setup

```bash
git clone https://github.com/ankit373/hydra.git
cd hydra

# Build and check the CLI, this is the whole interface
go build ./... && go test ./... -race
go run ./cmd/hydra status

# Desktop app (optional, it is a separate Go module)
cd desktop && go test ./... -race
cd frontend && npm ci && npm run typecheck
```

No secrets or API keys are needed just to browse, modify, or test the routing logic.

---

## Making Changes

1. Fork the repository and create a branch from `develop`:
   ```bash
   git checkout -b feat/my-feature
   ```
2. Make your changes (see standards below).
3. Everything CI gates on, before you push:
   ```bash
   gofmt -l ./cmd ./internal        # must print nothing
   go vet ./... && go build ./...
   go test ./... -race -count=1     # -count=1: Go caches results, and a cached
                                    # PASS from before your change looks identical
   staticcheck ./...                # go install honnef.co/go/tools/cmd/staticcheck@2026.1
   shellcheck --severity=error install.sh
   ```
   CI runs that suite on **Linux, macOS and Windows**, and separately builds and
   vets all six release targets (`darwin`/`linux`/`windows` × `amd64`/`arm64`).
   You only have one of those locally, so if you touch anything that reads a
   path, spawns a process, or checks a file mode, cross-compile before you push:
   ```bash
   GOOS=windows GOARCH=arm64 go build ./... && GOOS=windows GOARCH=arm64 go vet ./...
   ```
4. Test manually:
   ```bash
   go run ./cmd/hydra probe         # what this machine can route to
   go run ./cmd/hydra dispatch --dry-run --enum SIMPLE "add a helper"
   ```
5. Commit and push, then open a pull request against `develop`.

---

## The Regression Contract

Most of Hydra's tests are **regression contracts**: they pin behaviour that is
already correct, so a change either preserves it or goes red with an
explanation. They are not there to describe new features, they are there so
that you, working on one corner of the router, find out immediately when you
have changed something in another corner that someone depends on.

If a contract test fails on your branch, the default assumption is that **the
behaviour changed and that is the finding**, not that the test is stale.

### Writing one

Four rules, in order of how often they matter:

**1. Assert the contract, not the implementation.** Test observable behaviour,
CLI output and exit codes, on-disk file shapes, exported API. If your test
would fail on a pure rename, it is pinning the wrong thing and it will be
deleted the first time someone refactors.

**2. Be hermetic.** Use `testutil.NewSandbox(t)`. It gives the test a private
home directory, a private `$HYDRA_HOME`, an empty `$PATH` and no provider
credentials, so a contributor with no Ollama, no API keys and no `agy` gets
byte-identical results to CI and to a maintainer whose laptop has all three.

```go
s := testutil.NewSandbox(t)
s.FakeBinary(t, "ollama")            // this machine "has" ollama, and only ollama
s.SetKey(t, "ANTHROPIC_API_KEY", "k") // …and exactly one API head
```

Discovery tests especially: that layer's whole job is reporting what is
installed, so it is the layer most likely to report *your* machine instead of
the fixture.

**3. Be deterministic.** No wall-clock comparisons, no map-iteration order, no
randomness in assertions. Sort explicitly before comparing. If you need a
timestamp or a duration in output, let `testutil.Golden` normalise it.

**4. Parametrise by platform; do not skip.** Hydra ships for macOS, Linux and
Windows on x86-64 and ARM64, and CI runs the suite on all three OSes. A
`t.Skip("not on windows")` hides exactly the class of bug that matters, the
snapshot-permission and heartbeat bugs (#273, #274) were both found the day the
Windows leg was switched on. Where a guarantee genuinely differs by platform,
assert the platform's own mechanism:

```go
if runtime.GOOS == "windows" {
    // A FileMode only toggles the read-only bit here; the ACL on the user's
    // profile is the real protection, so assert containment instead.
    ...
    continue
}
if info.Mode().Perm()&0o077 != 0 { ... }
```

A skip needs a one-line reason, and the total number of them is budgeted, see
the coverage gate.

### Golden files

`testutil.Golden(t, "name", got, tempPaths...)` compares against
`testdata/name.golden` after normalising machine-specific values (temp paths,
versions, SHAs, timestamps, durations, path separators, line endings).

Re-bless with:
```bash
go test ./internal/somepkg -run TestName -update
```

**Re-blessing is legitimate when you meant to change the output. It is the bug
when you did not.** The failure message prints both versions and the first
differing line so you can tell which case you are in, read it before running
`-update`. A PR that re-blesses a golden should say in its description why the
output changed.

### Verifying that a test can actually fail

A regression test that cannot fail is worse than no test, because it reads as
coverage. Before you rely on one, break the thing it guards and confirm it goes
red:

```bash
# make the change you expect to be caught, then:
go test ./internal/somepkg -count=1     # must FAIL
# revert, then:
go test ./internal/somepkg -count=1     # must PASS
```

`-count=1` is required, Go caches test results, and a cached PASS from before
your change looks identical to a real one.

### The coverage gate

`ci/coverage-floors.txt` is a checked-in contract, enforced on every PR by
`go run ./ci/covergate`. It fails on three things, all of which are otherwise
invisible, they happen one PR at a time and never turn CI red on their own:

**Per-package coverage floors.** Every package has a minimum. The gate names
the package, what it measured, and the floor it missed.

Floors are a **ratchet**: raise them freely, in the same PR that raises the
coverage. Lowering one is allowed, sometimes the right move is to delete a
test that pinned the wrong thing, but it must be an explicit edit to this file
that a reviewer sees in the diff. That is the entire mechanism. Nothing else
stops coverage sliding back down.

The gate also prints `consider ratcheting` for any package sitting ten points
or more above its floor: a floor that has drifted far below reality no longer
protects anything.

**Packages with no test files.** A new package with no tests fails the gate.
The allow-list started at eleven, the whole platform and discovery layer, and
is down to two, both permanent: `internal/build` is four ldflags-injected
strings, and `cmd/specstest` is a debug main whose logic is covered in
`internal/sysinfo`. Adding to the list requires a reason on the line, because a
package with no tests is not a package with nothing to test.

**A skip budget.** The total number of `t.Skip` calls across the suite is
capped, and each needs a reason. Skips are how a cross-platform suite hides the
problem it exists to catch: a three-OS matrix where every awkward test skips on
two of them is decorative. Prefer rule 4 above, parametrise by platform, and
if you genuinely need a skip, raise the budget in the same commit so the
increase is visible.

`t.Skip()` inside an `f.Fuzz` body is not counted: there it is the fuzzer's own
control flow for rejecting a generated input, it happens thousands of times per
run, and a reason string would be noise.

Run it locally exactly as CI does:

```bash
go test ./internal/... ./cmd/... ./registry/... -coverprofile=cover.out -count=1
go run ./ci/covergate -profile cover.out -config ci/coverage-floors.txt
```

The gate has its own tests (`ci/covergate`), driven to both verdicts, a gate
that cannot go red is not a gate.

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
5. Address review feedback in new commits, do not force-push while a review is open.
6. Once approved, a maintainer will squash-merge.

---

## Shell Script Standards

- `set -euo pipefail` at the top of every script.
- Quote all variable expansions: `"$VAR"`, not `$VAR`.
- Use `printf '%s\n'` instead of `echo` for data that may contain escape sequences.
- Never hardcode absolute paths like `/Users/someone/`, use `$HOME`.
- Never store secrets in scripts. Read from env vars or tool-managed config files.
- Prefer `[[ ]]` over `[ ]` for conditionals.
- Run `shellcheck`, zero warnings/errors required for new files, improvements welcome for existing.

---

## Registry / YAML Changes

- `registry/routing.yaml`, the single source of truth for tier/enum mappings. Keep enum names SCREAMING_SNAKE_CASE.
- `registry/models.yaml`, add new models here. Include `pool`, `token_limit`, and `pricing_key`.
- `registry/domains.yaml`, maps task domains to enum keys. Keep entries alphabetically sorted.
- Always validate YAML syntax: `yq '.' registry/models.yaml > /dev/null`.

---

## Questions?

Open a [Discussion](https://github.com/ankit373/hydra/discussions) on GitHub, we're happy to help.
