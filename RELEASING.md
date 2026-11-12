# Releasing hydra

Everything merges to `main` through the Mergify queue. release-please keeps one open release PR
(`autorelease: pending`) and updates it on every merge. That PR is the release train. Merging it
tags `vX.Y.Z`, builds, and publishes to GitHub Releases, Homebrew, npm, PyPI and the desktop
builds. It also bumps the version pinned in the docs (README.md, docs/app.html, docs/index.html).

**Nothing releases without the maintainer's approval on the release PR.** No bot approves it, and
Mergify queues it only after that approval (GOVERNANCE.md: never auto-merge).

## 1. What bumps what

release-please reads the PR titles (the squash commit subjects) and the footers in the PR body.

| Title | Bump | Use it for |
|---|---|---|
| `fix(scope): ...`, `perf(scope): ...` | patch | a user-visible bug fix, a security fix, a dependency fix that changes behaviour (`fix(deps):`) |
| `feat(scope): ...` | minor | a new command, flag, header, endpoint, config key or model; a deprecation |
| `feat(scope)!: ...` plus a `BREAKING CHANGE: <what users do>` footer | major | anything in the table below under "major" |
| `ci:`, `docs:`, `chore:`, `refactor:`, `test:` | none | anything users cannot see. **Never** `fix(ci)`, `fix(site)`, `fix(release)` |

The PR body becomes the commit body, so a `BREAKING CHANGE:` or `Release-As:` line in it changes
the version even when the title does not say so. Leave them out unless you mean them.

### The public API

| Surface | Minor (compatible) | Major (breaking) |
|---|---|---|
| CLI commands and flags | add | remove, rename, or change a default that changes routing, spend or output files |
| Exit codes (`mcp check` 0/3/4, `hyverify` 0/1/2) | a new code for a new case | change a documented code |
| `--json`, `cost.jsonl`, `dispatch.jsonl` | add a field | remove, rename or retype a field |
| `hyctl serve` (`/v1/*`, `X-Hydra-*`, the `hydra` body block) | add an endpoint, header, field or opt-in behaviour | remove or rename one, add a required header, or change what an unpinned request routes to |
| `config.toml`, `policy.yaml`, registry overrides, `state.json` | add an optional key | remove or rename a key, change its meaning, or stop loading an old file |
| Shipped registry data | add a model (minor); refresh prices (patch) | remove a model users can pin |
| Install channels | add one | remove or rename one |

Not part of the API: human-readable output, TUI layout, help text and Go packages. The Go module
is not a library.

### Deprecation

1. Deprecate in a minor (`feat:`), with:
   - a stderr warning (CLI);
   - a `Deprecation:` header (HTTP, RFC 9745);
   - a load-time warning (config);
   - a "Deprecated" paragraph in the PR body.
2. `HYDRA_NO_DEPRECATION_WARNINGS=1` silences the warnings.
3. Open a `deprecation` issue with the milestone of the major that removes it.
4. Remove it only in a major, at least **2 minor releases and 4 weeks** after the deprecation
   shipped, whichever is longer.

## 2. When a release happens

| Rule | When | What happens |
|---|---|---|
| Train | Wednesday 10:00 IST, if `main` has at least 1 `feat`/`fix`/`perf`/`revert` since the last tag | approve the release PR that day, or put `release:hold` on it with a comment saying why |
| Skip | only ci/docs/chore since the last tag | nothing (release-please opens no PR) |
| Overdue | 14 days since the last release, or 20 unreleased user-facing changes | release now, or hold with a reason |
| Fast lane | a merged `security` PR, data loss, a broken install, a desktop app that will not launch | release within 24 h, off-train |
| Hold | `release:hold` on the PR, `release:blocked` on the PR, any open `release:blocked` issue, red CI on main | do not approve. Mergify will not queue a PR labelled `release:hold`, `release:blocked` or `hold` |
| Rate cap | 2 releases outside the fast lane already in the last 7 days | wait for the next train |

release-train.yml checks the overdue limits every Wednesday. On the release PR it sets a size label
(`release:small`, `release:medium`, `release:large`, `release:major`), adds `release:overdue`,
copies `release:blocked` from any open issue, and keeps one comment with the numbers. It never
approves or merges. The fast lane and the rate cap are still checked by hand.

## 3. Size and gates

The `Release gates` check on the release PR enforces the RC age and the major's fix-only rule, and
the queue requires it. `release:urgent` on the PR skips the RC soak for the fast lane: add the
label, then re-run the check.

| Size | What it is | Gates (all sizes: CI + release build + security in the queue) |
|---|---|---|
| Small | patch only | none extra |
| Medium | minor, at most 20 user-facing changes, no contract surface touched | read the generated CHANGELOG in the PR |
| Large | minor with more than 20 changes, or touching config schema, serve API, desktop runtime, Go toolchain minor or installers | an RC (section 4) at least 48 h old, used daily |
| Major | any `!` | every removal deprecated per section 1; migration notes in the PR; an RC at least 7 days old; only `fix` commits after the RC (any `feat` means a new RC) |

## 4. Steps

### Every release (small or medium)
1. Open the `autorelease: pending` PR and read the CHANGELOG diff. Is every line user-facing and
   correctly typed? A merged commit cannot be retyped. If that makes the version wrong, land an
   empty `chore:` commit with a `Release-As: X.Y.Z` footer in its PR body. Never set `release-as`
   in release-please-config.json, because it is sticky.
2. Check that the docs files in the PR moved to the new version and that CI on main is green.
3. Start CI on the release PR. release-please opens it with `GITHUB_TOKEN`, which starts no
   workflow, so close and reopen the PR to run CI.
4. **Approve the PR.** Mergify queues it once the release build and security checks are green,
   and it merges. A new merge to main makes release-please push to the PR, which dismisses your
   approval: read the new CHANGELOG and approve again.
5. Watch the run:
   `gh run watch $(gh run list --workflow release-please.yml -L1 --json databaseId -q '.[0].databaseId')`.
   If one channel fails, use "Re-run failed jobs". Do not re-dispatch publish.yml for a version
   that is partly published.
6. release-smoke.yml runs after publish. It installs from brew, npm, pip, install.sh and the
   release archive, and checks `hyctl --version` and an offline `hyctl --help`. If it fails it
   opens a `release:blocked` issue and labels the next release PR; go to section 5. Run it by
   hand with `gh workflow run release-smoke.yml -f tag=vX.Y.Z`.

### Large or major
1. Pick the commit (usually main's head) and run `gh workflow run rc.yml -f sha=<sha>`. It tags
   `vX.Y.Z-rc.N` at that commit, taking X.Y.Z from the release PR (or `-f release=X.Y.Z`), and
   builds the CLI and desktop.
2. Use the RC (or edge) daily for 48 h (large) or 7 days (major). Put `release:hold` on the
   release PR while you do.
3. Fixes land on main as usual. For a major, only `fix` commits may land until the release. If a
   `feat` lands, cut a new RC and restart the clock.
4. Remove `release:hold` and do the "Every release" steps.

### Hotfix when main is not shippable
Use this only if main has unreleased work you cannot ship yet. This path has not been run on
hydra yet: rehearse it before you need it.
1. `git switch -c release/vX.Y origin/vX.Y.Z` (the last tag) and push it.
2. Land the fix on main first, then cherry-pick it onto `release/vX.Y` through a PR (fixes always
   go to main first).
3. Run release-please against that branch (`--target-branch release/vX.Y`).
4. Delete the branch after the next normal release.

## 5. A bad release

npm and PyPI never allow a version to be reused. The fix is always a new patch release.

1. Open a `release:blocked` issue and put `release:blocked` on the release PR. The train holds.
2. Take the bad version off the defaults with rollback.yml: a dry run first
   (`gh workflow run rollback.yml -f tag=vBAD -f reason="..."`), then again with
   `-f dry_run=false`. It moves GitHub's latest, npm's `latest` dist-tag and the tap formula back
   to the previous stable release, and deprecates the npm version. Yank BAD on PyPI by hand (web
   UI). RUNBOOK-rollback.md has the details.
3. Land the fix as `fix(...)`. The release PR becomes a patch. Release it through the fast lane.
4. Close the `release:blocked` issue once the new release checks out on every channel.

## 6. Support

- Only the latest minor gets fixes.
- After a major, the previous major's last minor gets security fixes for 3 months.
- Report vulnerabilities through SECURITY.md, not issues.
