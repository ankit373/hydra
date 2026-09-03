# Hydra — Orchestrator Instructions
# Provider-neutral, local-first control plane. The orchestrator delegates; cheaper/local heads do the work.

## What Hydra Is
A **local-first, multi-vendor Trust Control Plane** shipped as a Go CLI (`hyctl`). It discovers
every model on your machine (CLI agents, API keys, local servers), scores each, and routes tasks by
complexity/cost with automatic fallback — enforcing policy (PII/local-only) and logging spend. It
also routes to a target *confidence of correctness*: calibration (`internal/trust`) + an
optimal-stopping SPRT ensemble (`hyctl dispatch --confidence`), graph-aware routing
(`internal/graph`), causal A2A handoffs (`internal/a2a`), a context-entropy governor
(`internal/entropy`), a local MCP accountability ledger (`internal/ledger`), and a pluggable
verification-oracle interface (`internal/oracle`) — all shipped, not roadmap. See the package map
below, and the private planning docs (`ROADMAP_TRUST_CONTROL_PLANE.md`, `HYDRA_MANIFESTO.md`,
`SPEC_TRUST_V1.md`) for the theory/math behind it.

Whatever model you drive Hydra with acts as the **orchestrator**; Antigravity (agy), API providers,
and Ollama are interchangeable **heads**. No single vendor is privileged — the whole point is
routing *across* providers (and *away* from expensive ones), so keep it provider-neutral.
Never do work yourself that belongs to a lower tier. Never escalate work to yourself that a cheaper head can handle.

---

## Directory Layout

**The `hyctl` Go CLI is the entire interface** (`cmd/hydra` + `internal/`). The legacy
`dispatch/*.sh` shell layer and the `internal/company` playbook engine have been **removed**
(#86, #88) — there is no shell fallback and no `hyctl run`. Everything is native Go.
```
cmd/hydra/              ← CLI entry point (Cobra): dispatch, probe, status, cost, stats,
                          pricing, edit, review, parallel, trust, init.
internal/dispatch/      ← Tier routing + fallback + policy + cost logging (the router).
internal/executor/      ← Native executors: agy, Ollama, HTTP (API providers), CLI subprocess.
internal/provider/      ← Discovery: cli / env (API keys) / port / agy.
internal/swarm/         ← Fan-out (race/best/all + judge) + SPRT adapter (swarm→trust).
internal/trust/         ← Trust Control Plane: calibration (LLR/D) + defect-cost + SPRT ensemble.
internal/graph/         ← Code dependency graph (graph.json) → blast radius + coupling k + percolation κ (Molloy–Reed).
internal/a2a/           ← Causal agent handoffs: vector clocks + concurrent-edit conflict detection.
internal/optimal/       ← Optimal parallel-agent count n*=√((1-s)/k) (Amdahl+coordination, Law 4).
internal/entropy/       ← Context signal density (gzip proxy) → useful_tokens=L·ρ; compaction governor (Law 5).
internal/ledger/        ← MCP accountability ledger: record + policy-gate what agents touch. `hyctl mcp`.
internal/pending/       ← Tasks parked on a ledger `ask` verdict (logs/pending/<task-id>.json). `hyctl ask`.
internal/mcpregistry/   ← MCP server trust registry: sync official registry, scan installed servers,
                          score (CSA-shaped categories), version-bump trust automaton, backtest against
                          known incidents. `hyctl mcp registry`.
internal/oracle/        ← Verification oracles (tests/compile/lint) as calibrated evidence sources. `hyctl oracle`.
internal/pricing/       ← Live pricing DB (OpenRouter fetch + 24h cache + tier fallback).
internal/policy/        ← PII detection + local-only enforcement.
internal/{cost,budget}/ ← Spend reporting (est/actual labeling) + token-budget governor (static bands + rate-aware first-passage on claude_pct).
internal/capabilities/  ← Model capability scores: embedded data.json ⊕ runtime user overlay (~/.hydra/models.json). `hyctl models`.
internal/util/          ← Shared utilities (Accumulator, etc).

registry/               ← Routing data, compiled into the binary via `go:embed` (registry.go)
                          and overridable on disk at `$HYDRA_HOME/registry/<file>`. Nothing ships
                          these files as separate artifacts — brew/npm/pip/curl install the binary
                          alone, so before #238 every install ran with no registry at all.
  routing.yaml          ← Enum → tier reference table, and what `hyctl init` writes. NOTE: the
                          runtime mapping is `dispatch.EnumToTier`, a hardcoded Go switch — editing
                          this file alone does NOT change how a dispatch routes.
  models.yaml           ← Model definitions, token pools, context windows (flags are
                          install-specific defaults — verify against your providers). Read by
                          the agy provider and the budget governor.
  domains.yaml          ← Domain → enum key routing (references routing.yaml).
  pricing.yaml          ← Tier pricing. Prices the CLI-agent heads that never appear in
                          OpenRouter's catalog, so it is load-bearing, not just an offline fallback.
  policy.yaml           ← File-policy rules.  workspace.yaml ← workspace roots + validators.
logs/                   ← Dispatch log + state.json (claude_pct, claude_pct_history).
```

---

## Orchestration Protocol

### Step 1 — Classify
Read `registry/domains.yaml` to identify the domain and task type.
Look up the enum key (e.g. `SIMPLE`, `COMPLEX`).
`registry/routing.yaml` documents which tier that enum resolves to; the mapping the router
actually applies is `dispatch.EnumToTier`.

### Step 2 — Check State
```bash
hyctl status      # check claude_pct, budget, and available heads
```
If claude_pct ≥ 75: freeze escalations, do not route new work to tier 1 (yourself).
If claude_pct ≥ 95: emergency mode — warn user, only route, don't execute.

### Step 3 — Dispatch
```bash
hyctl dispatch --enum SIMPLE "<task>" [--system <text>] [--a2a logs/last_handoff.json]
```
Dispatch handles fallbacks automatically. You do not need to retry. Use `--dry-run` to preview
the routing chain, `--local` to force local-only, `--tier N` to pin a tier.

### Step 4 — Review
Read the output. Ask: does this compile? match conventions? solve the task?
If no → escalate one tier: `hyctl dispatch --tier <lower-number> …` (lower tier number = stronger).
If yes → apply to disk, continue.

### Step 5 — Rubber Duck
For any output from tiers 2-3 (agy Claude family), run rubber duck review:
```bash
hyctl dispatch --tier 4 "Review this for tradeoffs and blind spots:\n<output>"
```
Skip rubber duck if claude_pct ≥ 75 (preserve tokens).

---

## A2A Context Handoff
When passing work between agents, always write a handoff file:
```bash
# Generate handoff
cat > /tmp/hydra_handoff.json <<EOF
{
  "from": "claude-orchestrator",
  "task": "<what was asked>",
  "files": ["src/foo.ts", "src/bar.ts"],
  "conventions": "<paste relevant conventions>",
  "context": "<key context the next agent needs>",
  "prior_output": "<what was already done>"
}
EOF
hyctl dispatch --tier 6 "<next task>" --a2a /tmp/hydra_handoff.json
```
The last handoff is always saved to `logs/last_handoff.json` automatically.

---

## Token Preservation Rules (CRITICAL)
**Global rule: No model should exceed 70-75% of its context window. Hard ceiling is 80%.**
Claude Code IS the orchestrator. If it runs out, everything stops.

| claude_pct | Mode      | Action |
|-----------|-----------|--------|
| 0–49%     | normal    | Full orchestration |
| 50–64%    | compact   | Run `/compact` — recommended now |
| 65–69%    | caution   | Run `/compact` URGENTLY. Stop self-reviewing agy output. |
| 70–74%    | warning   | Downgrade all tasks 1 tier. Freeze escalations. `/compact` now. |
| 75–79%    | critical  | Hard switch. Routing only. Only CORE if truly necessary. |
| 80%+      | emergency | 🚨 STOP all generation. Route to Qwen. Warn user to start new session. |

**When to run `/compact`**: Proactively at 50%. Urgently at 65%. It's too late after 75%.

Update `logs/state.json` when you know your token usage percent:
```bash
jq '.claude_pct = 52' logs/state.json > logs/state.json.tmp && mv logs/state.json.tmp logs/state.json
```

**Same 70%/75%/80% rule applies to ALL delegated models** — `hyctl dispatch` enforces this via the budget governor + fallback chains.
**Local heads are tier 10, the terminal fallback** — they cost nothing, so `rank.UITier` puts any
`LocalOnly` head at the cheapest tier regardless of its score, and API limits never apply to them.

This holds only while a local head is actually **routable**. Ollama is discovered twice: as a binary
on `$PATH` (not routable on its own — nothing can drive it) and, once its server answers on `:11434`,
as one routable head per model via the port provider. With the server down there is no tier-10 head,
and dispatch degrades to the cheapest routable head and says so. `hyctl probe` marks unroutable
heads with `✗` and the reason (#248).

---

## Code Quality Standard — Non-Negotiable

**Everything written here must be industry-best. No exceptions. No "good enough".**

When reviewing or producing code:
- **Be brutally honest.** If it's wrong, say it's wrong. If it's mediocre, say it's mediocre. Do not soften findings to protect feelings.
- **Run the race detector.** `go test -race ./...`. If it fails, it ships nothing.
- **Never ship duplicate logic.** Three copies of the same threshold table is a bug, not an inconvenience.
- **Dead code is a lie.** A branch that can never execute is a statement about the code that is false. Delete it.
- **User-visible output must be correct.** `used/1000` truncating to zero is broken, not "close enough."
- **Exported symbols must be used.** An exported function with no callers is noise that misleads the next engineer.
- **If the race detector, linter, or vet flag it — fix it before asking for review.** Not after.

The bar is: would a senior engineer at a top systems shop approve this without comment? If not, keep working.

---

## Karpathy Guidelines (always apply)
- Think before coding. State assumptions. Push back when a simpler approach exists.
- Minimum code. No speculative features. No abstractions for single-use code.
- Surgical changes. Touch only what you must. Match existing style.
- If output is 200 lines and could be 50, ask the delegated head to rewrite.
- Never add error handling for impossible scenarios.

## Comments — 2-3 lines, hard cap
**No comment is longer than 2-3 lines. No exceptions, including file headers.**
Say why, not what; the code already says what. If the rationale genuinely needs a
page, it belongs in a planning doc or the PR body, not the source. A 20-line essay
at the top of a file is the tell that the design was argued in comments instead of
being made obvious in code — delete it and make the names carry it.

---

## gstack Skills Available
Use the `/browse` skill from gstack for all web browsing. Never use `mcp__claude-in-chrome__*` tools.

Available skills: /office-hours, /plan-ceo-review, /plan-eng-review, /plan-design-review,
/design-consultation, /design-shotgun, /design-html, /review, /ship, /land-and-deploy, /canary,
/benchmark, /browse, /connect-chrome, /qa, /qa-only, /design-review, /setup-browser-cookies,
/setup-deploy, /setup-gbrain, /retro, /investigate, /document-release, /document-generate, /codex,
/cso, /autoplan, /plan-devex-review, /devex-review, /careful, /freeze, /guard, /unfreeze,
/gstack-upgrade, /learn

## Karpathy Skills Available
Located at ~/.claude/skills/karpathy/skills/karpathy-guidelines
Apply these guidelines to any code review or generation task.
Key principles: think first, simplicity, surgical edits, test your code,
no hallucinated APIs, explicit about tradeoffs.

---

## Quick Reference
```bash
# Dispatch by enum key (preferred)
hyctl dispatch --enum SIMPLE "write a User DTO in TypeScript"

# Dispatch by tier
hyctl dispatch --tier 8 "write a User DTO in TypeScript"

# Preview the routing/fallback chain without executing
hyctl dispatch --dry-run --enum STANDARD "add pagination"

# Force local-only (no API calls)
hyctl dispatch --local "write unit tests"

# With A2A handoff
hyctl dispatch --enum MODERATE "add auth" --a2a logs/last_handoff.json

# Fan-out to multiple heads (swarm) and judge the best
hyctl dispatch --swarm --swarm-mode best "implement rate limiter"

# Route to a target confidence of correctness (SPRT optimal-stopping ensemble)
hyctl dispatch --confidence 0.95 "is this migration safe for prod?"

# Blast-radius aware: --file raises the confidence bar by the code graph
hyctl graph blast internal/auth/token.go
hyctl dispatch --confidence 0.90 --file internal/auth/token.go "rotate signing key"

# Optimal parallelism (Law 4): how many agents to fan out for these files
hyctl graph parallel internal/a.go internal/b.go
# A2A handoffs (last_handoff.json) now carry a vector clock for causal ordering.

# Trust Control Plane: calibration, defect-cost, run stats, and the LLR ledger
hyctl trust calibration ; hyctl trust record --source model:x --domain go --said-correct --outcome correct
hyctl trust defect --pii --production ; hyctl trust stats ; hyctl trust explain <task_hash>

# Add a model at runtime (no rebuild) — merges into ~/.hydra/models.json overlay
hyctl models add kimi-k3 --name "Kimi K3" --provider moonshot --cap-score 85
hyctl models list ; hyctl models remove kimi-k3 ; hyctl models sync   # import OpenRouter catalog

# Tasks parked waiting on a human (ledger `ask` verdict)
hyctl ask list ; hyctl ask answer <task-id> "go ahead" ; hyctl ask decline <task-id> "not prod"

# System state / discovered heads / spend
# `hyctl status` shows the rate-aware claude_pct governor (first-passage risk toward 80%).
hyctl status ; hyctl probe ; hyctl cost ; hyctl stats
```

---

# Development Workflow — Issue-First, Always

> **Golden rule**: No code without a GitHub issue. No branch without an issue number.
> Claude Code must follow this workflow for every task, no exceptions.

---

## Branching Strategy

Modelled after GitHub CLI + Helm — simple enough for a small team, rigorous enough that nothing untested hits main.

```
main                ← production only. NEVER pushed directly. Tags live here.
  ↑ squash PR
release/v1.x        ← UAT gate. Cut from develop 1-2 days before release.
  ↑ squash PR         Bug fixes land here only. Merged → main AND back → develop.
develop             ← integration. All features land here. Edge builds fire here.
  ↑ squash PR
feature/#{n}-slug   ← short-lived. Always branch from develop.
fix/#{n}-slug
chore/#{n}-slug

hotfix/#{n}-slug    ← branches from main tag. Merged → main, cherry-picked → develop.
```

### Branch rules (hard rules, no exceptions)

| Branch | Who pushes | Version bump? | CI publishes |
|---|---|---|---|
| `main` | release-please PR only | **YES** — semver tag | stable release + Homebrew |
| `release/v*` | cut from develop | no | RC pre-release (`v1.2.0-rc.1`) |
| `develop` | feature PR merges | no | edge pre-release (overwritten) |
| `feature/*` `fix/*` `chore/*` | you | no | nothing |
| `hotfix/*` | you | no | nothing (merges to main trigger release) |

---

## GitHub Project & Issue Hygiene (MANDATORY)

Every issue must be:
1. **On the GitHub project board** (Project #2 "Hydra Roadmap")
2. **Linked to its branch** — GitHub auto-links when branch name contains the issue number (`feature/54-hydra-stats` links to #54)
3. **Linked to its PR** — PR body must contain `Closes #<issue>` so the PR shows on the issue
4. **Moving through board states** at every transition (Todo → In Progress → In Review → Done)
5. **Closed by hand once the release carrying it reaches `main`** — see below; nothing closes it for you

### Link a branch to an issue (GitHub auto-detection)
GitHub automatically links a branch to an issue when the branch name contains the issue number.
**Always name branches `feature/#{n}-slug`, `fix/#{n}-slug` etc.** — this is what creates the link.

To verify the link is showing on the issue:
```bash
gh issue view 54 --json linkedBranches
```

### Link a PR to an issue
Always include `Closes #<n>` in the PR body. This shows the PR on the issue page and creates the
link — that is the whole reason to keep writing it.

**It does not close the issue.** GitHub only honours the closing keyword when the PR merges into the
**default branch**, which here is `main`. Every feature/fix PR targets `develop` by design, so the
keyword links but never fires — and it fails silently: the PR merges green and the issue stays open.
17 issues accumulated this way before anyone noticed (#217).

Close issues explicitly when the release carrying them lands on `main`, which is the board's
existing `Deploy` → `Done` transition:

```bash
gh issue close <n> --comment "Shipped in v1.1.0."
```

(This is unrelated to the project-board columns, which need a `read:project` OAuth scope. Closing
an issue needs no extra scope.)

---

## Step 1 — Create a GitHub Issue FIRST

Before touching any code, create an issue and add it to the board:

```bash
# Feature
ISSUE_URL=$(gh issue create \
  --title "feat: <short description>" \
  --body "## Problem\n\n## Solution\n\n## Acceptance Criteria\n- [ ] " \
  --label "enhancement" \
  --assignee "@me")
ISSUE=$(echo "$ISSUE_URL" | grep -oE '[0-9]+$')

# Bug
ISSUE_URL=$(gh issue create \
  --title "fix: <short description>" \
  --body "## Steps to Reproduce\n\n## Expected\n\n## Actual\n\n## Fix" \
  --label "bug" \
  --assignee "@me")
ISSUE=$(echo "$ISSUE_URL" | grep -oE '[0-9]+$')

# Add to project board and move to Todo
gh project item-add 2 --owner ankit373 --url "$ISSUE_URL"
ITEM_ID=$(gh project item-list 2 --owner ankit373 --format json --limit 100 \
  | python3 -c "import json,sys; [print(i['id']) for i in json.load(sys.stdin).get('items',[]) if '/${ISSUE}' in str(i.get('content',{}).get('url',''))]")
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ --id "$ITEM_ID" \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE --single-select-option-id f75ad846
```

---

## Step 2 — Create a Branch (from develop)

Branch naming is strict — always include the issue number:

| Type | Pattern | Example |
|---|---|---|
| Feature | `feature/#{issue}-short-desc` | `feature/43-hydra-stats` |
| Bug fix | `fix/#{issue}-short-desc` | `fix/44-version-crash` |
| Hotfix (prod) | `hotfix/#{issue}-short-desc` | `hotfix/45-nil-panic` |
| Chore / deps | `chore/#{issue}-short-desc` | `chore/46-bump-bubbletea` |

```bash
# Features/fixes — always branch from develop
git checkout develop && git pull origin develop
git checkout -b feature/43-hydra-stats

# Hotfixes — branch from the last production tag
git checkout main && git pull origin main
git checkout -b hotfix/45-nil-panic

# Move issue to In Progress
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ --id "$ITEM_ID" \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE --single-select-option-id 47fc9ee4
```

---

## Step 3 — Conventional Commits (required)

Every commit must follow the conventional commit spec.
**This drives automatic changelog generation and version bumps.**

| Prefix | Effect | Example |
|---|---|---|
| `feat:` | minor version bump | `feat: add hyctl stats subcommand` |
| `fix:` | patch bump | `fix: nil panic in dispatch on empty prompt` |
| `feat!:` or `BREAKING CHANGE:` in body | major bump | `feat!: rename --tier to --level` |
| `perf:` | patch bump | `perf: cache probe results for 60s` |
| `refactor:` | no bump | `refactor: extract dispatch logic` |
| `chore(deps):` | no bump | `chore(deps): bump bubbletea v0.28` |
| `docs:`, `test:`, `ci:`, `style:` | no bump, hidden in changelog | — |

```bash
git commit -m "feat(dispatch): add --dry-run flag to preview routing decisions"
git commit -m "fix(update): skip check when HYDRA_NO_UPDATE_CHECK is set"
git commit -m "chore(deps): bump golang.org/x/sys to v0.25.0"
```

---

## Step 4 — Open a Pull Request → Link to Issue

```bash
gh pr create \
  --title "feat(stats): hyctl stats — cost breakdown by model/tier/day" \
  --body "$(cat <<'EOF'
## Summary
- Adds `hyctl stats` subcommand
- Reads cost.jsonl, groups by model / tier / day
- Outputs table with totals

## Changes
- `internal/stats/stats.go` — new package
- `cmd/hydra/main.go` — wire cmdStats()

Closes #43
EOF
)" \
  --base develop \
  --draft=false

# Move issue to In Review
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ --id "$ITEM_ID" \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE --single-select-option-id 1490e846
```

**PR rules:**
- Title must be a valid conventional commit (e.g. `feat(scope): description`)
- Body must contain `Closes #<issue>` — this links the PR to the issue. It does **not** close it:
  the keyword only fires on merge into the default branch (`main`). Close it by hand at release.
- All features/fixes target `develop`. Hotfixes target `main` — and there the keyword *does* fire.
- Never open a PR directly to `main` from a feature branch.

---

## Step 5 — Review & Merge

- PRs to `develop` require 0 approvals (self-merge allowed) but must pass CI
- PRs to `main` (release branch merges, hotfixes) require 1 approval
- Merge strategy: **Squash and merge** everywhere — clean linear history
- Never force-push to `develop`, `release/*`, or `main`

```bash
# After merge — move issue to Done
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ --id "$ITEM_ID" \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE --single-select-option-id 98236657
```

---

## Step 6 — Release Cycle

### Normal release flow (feature release)

```
Features merge to develop (conventional commits)
        ↓  (accumulate until ready)
Cut release branch: git checkout -b release/v1.2.0 develop
        ↓  (push → rc.yml fires → publishes v1.2.0-rc.1 pre-release)
UAT testing on release/v1.2.0
Bug fixes land via PR into release/v1.2.0 (direct pushes are rejected — the
release-branch-protection ruleset requires a PR and linear history, so a merge
commit cannot land there either; squash or rebase only)
        ↓  (each merge → rc.yml publishes v1.2.0-rc.2, rc.3 …)
Sign-off ✓
        ↓
PR: release/v1.2.0 → main  (squash merge — MUST carry a Release-As footer, see below)
        ↓
release-please opens Release PR on main (bumps version, updates CHANGELOG)
        ↓
Merge Release PR → tag v1.2.0 created → release.yml fires
        ↓
GoReleaser builds all platforms, publishes stable release, updates Homebrew tap
        ↓
Cherry-pick any release-branch fixes back to develop
```

### ⚠️ The squash-merge trap — `Release-As:` is mandatory

**A squash merge of `release/v*` → `main` destroys the commit history release-please
needs, and the release silently does not happen.**

This is not hypothetical: it is exactly what happened to v1.1.0. PR #219 merged green,
`Release Please` ran green, and **no release was produced**. Its log:

```
✔ Considering: 3 commits
✔ No user facing commits found since e5e766e — skipping
```

The reason: squashing collapses every `feat:`/`fix:` commit on the release branch into a
**single** commit whose type is taken from the PR title — here `chore(release): v1.1.0`.
`chore` does not bump. release-please saw no user-facing commit and correctly did nothing.
Nothing failed, nothing was red, and no `v1.1.0` tag was ever created.

**Therefore: the release→main PR body MUST contain a `Release-As:` footer**, which forces
the version regardless of commit types:

```
Release-As: 1.2.0
```

Put it on its own line at the end of the PR **body** — a squash composes the commit message
from title + body, so a footer in a local commit message is discarded. Give the PR a
`fix(release):` or `feat(release):` title as well, so a user-facing commit exists and
release-please cannot take the "nothing to do" path at all. Belt and braces — this step is
invisible when it works and completely silent when it is missed.

**Verify after merging**, every time — a green `Release Please` run does not mean a release:

```bash
gh run list --branch main --workflow "Release Please" --limit 1   # must be success
gh pr list --state open --search "release"                        # a Release PR MUST appear
git ls-remote --tags origin | grep "v1.2.0$"                      # after merging that PR
```

If no Release PR appears, the footer was missing. Fix it by pushing another PR to `main`
carrying the footer; do **not** create the tag by hand — a manual tag leaves
`.release-please-manifest.json` behind at the old version, and the next release is then
computed off the wrong base (#215).

### Cutting a release branch

```bash
git checkout develop && git pull origin develop
git checkout -b release/v1.2.0
git push -u origin release/v1.2.0
# → rc.yml fires automatically, publishes v1.2.0-rc.1
```

### Hotfix flow (production bug)

```bash
# Branch from the last production tag
git checkout main && git pull origin main
git checkout -b hotfix/45-nil-panic

# Fix, commit, push
git commit -m "fix(dispatch): nil panic when prompt is empty"
git push -u origin hotfix/45-nil-panic

# PR → main (requires 1 approval)
gh pr create --base main --title "fix(dispatch): nil panic when prompt is empty"

# After merge → release-please picks it up → patch release (v1.2.1)
# Cherry-pick back to develop
git checkout develop && git cherry-pick <commit-sha>
git push origin develop
```

### Release channels

| Channel | Branch | Tag pattern | Install |
|---|---|---|---|
| **stable** | `main` (tagged by release-please) | `v1.2.0` | `brew install hyctl` |
| **RC / UAT** | `release/v*` | `v1.2.0-rc.1` | GitHub pre-release |
| **edge** | `develop` | `edge` (overwritten) | GitHub pre-release |

### Version bump rules (CRITICAL)
- **Only `main` ever gets a semver tag** — release-please enforces this
- `release/*` and `develop` get pre-release tags only (RC / edge) — no semver bump
- Never manually bump version numbers — release-please reads conventional commits
- `BREAKING CHANGE:` in commit body → major bump; `feat:` → minor; `fix:`/`perf:` → patch

---

## Step 7 — GitHub Project Board (MANDATORY — every state change)

Board: **Project #2 "Hydra Roadmap"** — `PVT_kwHOAL1qLc4BZbZZ`
Field: **Status** — `PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE`

| Column | Option ID | When to move |
|---|---|---|
| Todo | `f75ad846` | Issue created (new work planned) |
| In Progress | `47fc9ee4` | Branch created / coding started |
| In Review | `1490e846` | PR opened |
| Deploy | `bcafa7ca` | PR merged, waiting for release tag |
| Done | `98236657` | Released / closed |

**This is not optional.** Every issue must be moved at every transition. Do not skip steps.

### How to move an issue

First, get the project item ID for the issue:
```bash
# Find item ID for issue #43
ITEM_ID=$(gh project item-list 2 --owner ankit373 --format json --limit 100 \
  | python3 -c "
import json,sys
d=json.load(sys.stdin)
for i in d.get('items',[]):
    if '#43' in str(i.get('content',{}).get('url','')):
        print(i['id'])
" 2>/dev/null)
# OR look it up directly:
gh project item-list 2 --owner ankit373 --format json --limit 100 | python3 -c \
  "import json,sys; [print(i['id'], i.get('status',''), i.get('title','')[:60]) for i in json.load(sys.stdin).get('items',[])]"
```

Then move it:
```bash
# Move to Todo (issue created)
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ \
  --id <ITEM_ID> \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE \
  --single-select-option-id f75ad846

# Move to In Progress (branch created, coding started)
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ \
  --id <ITEM_ID> \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE \
  --single-select-option-id 47fc9ee4

# Move to In Review (PR opened)
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ \
  --id <ITEM_ID> \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE \
  --single-select-option-id 1490e846

# Move to Done (merged and closed)
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ \
  --id <ITEM_ID> \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE \
  --single-select-option-id 98236657
```

### Add new issues to the board automatically
```bash
# After gh issue create, add it to the project and set Todo
ISSUE_URL=$(gh issue create ... | tail -1)
gh project item-add 2 --owner ankit373 --url "$ISSUE_URL"
# then move to Todo using item-edit as above
```

---

## Quick Start — Full Flow in One Go

```bash
# 1. Create issue + add to board + move to Todo
ISSUE_URL=$(gh issue create --title "feat: hyctl stats" --label enhancement --assignee "@me" \
  --body "Add cost stats subcommand")
ISSUE=$(echo "$ISSUE_URL" | grep -oE '[0-9]+$')
gh project item-add 2 --owner ankit373 --url "$ISSUE_URL"
ITEM_ID=$(gh project item-list 2 --owner ankit373 --format json --limit 100 \
  | python3 -c "import json,sys; [print(i['id']) for i in json.load(sys.stdin).get('items',[]) if '/${ISSUE}' in str(i.get('content',{}).get('url',''))]")
# Move → Todo
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ --id "$ITEM_ID" \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE --single-select-option-id f75ad846

# 2. Create branch + move to In Progress
git checkout develop && git pull origin develop
git checkout -b feature/${ISSUE}-hydra-stats
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ --id "$ITEM_ID" \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE --single-select-option-id 47fc9ee4

# 3. Write code, commit with conventional message
git add internal/stats/ cmd/hydra/main.go
git commit -m "feat(stats): add hyctl stats subcommand (#${ISSUE})"

# 4. Push and open PR + move to In Review
git push -u origin HEAD
gh pr create --title "feat(stats): add hyctl stats subcommand" \
  --body "Closes #${ISSUE}" --base develop
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ --id "$ITEM_ID" \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE --single-select-option-id 1490e846

# 5. After merge to develop — move to Deploy (merged, not yet released)
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ --id "$ITEM_ID" \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE --single-select-option-id bcafa7ca

# 6. After the release carrying it lands on main — close the issue, then move to Done.
#    Nothing does this for you: "Closes #n" does not fire on a develop-targeted PR (#217).
gh issue close "${ISSUE}" --comment "Shipped in v1.1.0."
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ --id "$ITEM_ID" \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE --single-select-option-id 98236657
```

---

## Versioning Rules (SemVer)

```
v{MAJOR}.{MINOR}.{PATCH}

MAJOR — breaking CLI interface change (rare, needs migration guide in PR)
MINOR — new subcommand, new feature, new model support
PATCH — bug fix, security patch, dependency update
```

Current version is tracked in `.release-please-manifest.json`.
**Never edit version numbers manually** — release-please handles all bumps.

---

## Files Involved in the Release Pipeline

```
.goreleaser.yaml              ← build matrix, archives, homebrew tap config
release-please-config.json    ← release-please behaviour
.release-please-manifest.json ← last released version. release-please reads THIS, not tags,
                                to compute the next bump — if it drifts from the newest tag,
                                the next release is computed off the wrong base (#215).
CHANGELOG.md                  ← release-please prepends each release; the pre-1.0 tail is a
                                hand-written historical record. Do not edit the generated part.
internal/build/build.go       ← version vars set by ldflags at build time
internal/update/update.go     ← startup update checker (24h cache)
.github/workflows/release.yml ← fires on tag push → goreleaser
.github/workflows/edge.yml    ← fires on develop push → edge build
.github/workflows/rc.yml      ← fires on release/v* push → RC pre-release
.github/workflows/publish.yml ← fans a release out to brew/npm/pip
.github/workflows/release-please.yml ← fires on main push → release PR
.github/workflows/sync-develop.yml   ← fires on main push → back-merge PR (main → develop).
                                       FAILS loudly on conflict; a red run here means develop
                                       is behind main and a release cut will not merge cleanly.
```

---

## Docs Site — Keep These Files in Sync (MANDATORY)

The GitHub Pages site at `hydra.uvansa.com` serves static files from `docs/`. Several of these are **manually maintained** — they do not update themselves. Whenever a relevant change lands, update all affected files in the same commit or PR.

```
docs/index.html    ← landing page (features, stats, CLI tab, cost table)
docs/llms.txt      ← AI context file (ChatGPT/Claude/Perplexity read this)
docs/pricing.md    ← machine-readable pricing for AI agents
docs/sitemap.xml   ← lastmod date + any new public URLs
docs/robots.txt    ← AI crawler rules (only change if bot policy changes)
```

### What triggers an update

| Change | Files to update |
|---|---|
| New CLI subcommand (`hyctl foo`) | `index.html` (CLI tab), `llms.txt` (What It Does) |
| New feature shipped | `index.html` (What's New section), `llms.txt` |
| Version bump (e.g. 1.0 → 1.1) | `index.html` (badge, structured data), `llms.txt`, `sitemap.xml` lastmod, **`app.html` download links** |
| New desktop build target | `app.html` download table, `README.md` platform table, `llms.txt` platform support |
| Pricing change (model rates) | `pricing.md`, cost comparison table in `index.html`, `llms.txt` |
| New public page or anchor | `sitemap.xml` |
| AI crawler policy change | `robots.txt` |

### Rules
- `llms.txt` must always reflect what `hyctl --help` and `hyctl stats` actually do — no aspirational features
- `pricing.md` costs must match `hyctl pricing list` live output — never hardcode stale rates without noting the date
- `sitemap.xml` `lastmod` must be updated whenever `index.html` changes
- `app.html`'s direct download links are **version-pinned by necessity** — desktop asset names embed
  their version, so GitHub's `/releases/latest/download/` shortcut cannot address them. They go stale
  every release and must be bumped by hand; the `install-app.sh` command beside them resolves the
  newest tag at runtime and never goes stale, which is why it is the primary path on the page
- Do not add features to `llms.txt` that haven't shipped to `main` yet

---

## Go Control Plane — Package Map

All Go source lives under `cmd/` and `internal/`. Key packages:

| Package | Purpose |
|---|---|
| `internal/dispatch` | Core router: policy → head selection → executor → fallback |
| `internal/executor` | Per-provider execution: agy, ollama, HTTP (OpenAI-compat), CLI |
| `internal/provider` | Head discovery plugins (agy registry, env, port, CLI) |
| `internal/probe` | Machine scan — finds all live heads at startup |
| `internal/swarm` | Fan-out dispatch: race / best (LLM judge) / all (CapScore rank) |
| `internal/pricing` | Live cost DB: OpenRouter fetch + 24h cache + tier YAML fallback |
| `internal/util` | Shared utilities: `Accumulator` (bounded io.Writer, 33 MB cap) |
| `internal/cost` | Reads `cost.jsonl`, produces spend summaries |
| `internal/policy` | Allow/deny rules (PII local-only, etc.) |
| `internal/rank` | CapScore ranking helpers |
| `registry` | The routing YAML **and** the `go:embed` that compiles it into the binary. `registry.Read(home, name)` prefers `$HYDRA_HOME/registry/<name>` so operators can retune without a rebuild, and falls back to the embedded copy — which is what every brew/npm/pip/curl install uses, since none of them ship the files (#238). |
| `internal/config` | Hydra config load/save (`~/.config/hydra/`); `Breadcrumb()` — SHA256 deployment-identity fingerprint over `registry/{routing,models,domains}.yaml`, auto-stamped into ledger/trust/cost log entries so they can be tied back to the exact routing rules in effect. |
| `internal/capabilities` | Model capability scores: embedded `data.json` ⊕ runtime user overlay (`~/.hydra/models.json`) merged at discovery, so new models are added without a rebuild. Drives `hyctl models list\|add\|remove\|sync`. |
| `internal/budget` | Token-budget governor: static pressure bands (`ModeFor`) + a rate-aware first-passage-time model on the orchestrator's `claude_pct` session history (`RiskFromHistory`/`EffectiveMode`) that escalates before a threshold is crossed. Feeds `claudeMode` downgrades and `hyctl status`. |
| `internal/trust` | Trust Control Plane confidence layer: per-source calibration (Beta-Bernoulli → LLR/D), defect-cost model + `RequiredConfidence`, and the SPRT optimal-stopping ensemble (`trust.Run`). Drives `hyctl dispatch --confidence` and `hyctl trust calibration\|record\|defect\|stats\|explain`. |
| `internal/graph` | Code dependency graph (`graph.json`, Graphify or any tree-sitter indexer) → transitive-dependent blast radius + coupling `k` + Molloy–Reed percolation κ=⟨k²⟩/⟨k⟩ (κ≥2 ⟹ cascade-capable core; `PercolationFactor` lifts hub-core files). Drives `hyctl graph blast\|parallel` and `hyctl dispatch --file`. |
| `internal/a2a` | Agent-to-agent handoffs with vector clocks: causal ordering (before/after/concurrent) + `ConflictsWith` (concurrent + overlapping files). Backs `last_handoff.json` and `--a2a`. |
| `internal/optimal` | Optimal parallel-agent count `n*=√((1−s)/k)` and speedup (Amdahl + coordination, Manifesto Law 4). Drives `hyctl graph parallel`. |
| `internal/entropy` | Context signal density ρ (gzip-ratio proxy) → `useful_tokens = L·ρ` + a compaction governor (Manifesto Law 5). Drives `hyctl context entropy`. |
| `internal/ledger` | Local MCP accountability ledger: append-only access events + glob allow/deny `Policy.Decide` gate (records every decision), classification-aware (`Rule.Classification`, auto-derived from content via `policy.ContainsPII` or set explicitly) + `HashParams`/`VerifyParams` SHA256 parameter-hash binding for tamper-evidence between a decision and its execution. `Check` fails **closed** (unhashable params → `Deny`, recorded); `LoadPolicy` rejects unparseable decisions/actions/globs rather than silently voiding a rule, actions/classifications are case-normalized, and only **Allow** events count as approvals for `verify`. Drives `hyctl mcp check\|record\|verify\|log\|report`. |
| `internal/mcpregistry` | Local-first MCP server trust registry — identity-only sync/scan/audit of what's installed (never reads secret/env values from client configs, by construction), a CSA MCP Selection Scorecard-shaped score (known-CVE cross-reference via OSV.dev, edit-distance typosquat detection, GitHub maintenance recency, declared-not-verified auth posture — each category renders "insufficient evidence" rather than a fabricated number), and a trust lifecycle automaton (new/provisional/trusted/flagged/quarantined/delisted) where every version bump — a content-hash diff of the manifest — drops a server back to provisional. Only a *confirmed* finding quarantines (`quarantineThreshold`, -80): the near-duplicate heuristic scores -40 and deliberately sits above it, because it false-positived on 0.7% of the live registry and quarantine has no automatic exit — `Clear` is the manual recovery path. An unevaluated category contributes `neutralBaseline` rather than dropping out of the weighted average, so missing evidence can never raise a score, and a server with no substantive category reads "insufficient evidence" instead of a number. `ClassificationForTool` feeds `mcp-unverified`/`mcp-flagged`/`mcp-quarantined` into `internal/ledger`'s classification, the same mechanism `policy.ContainsPII` uses for content. `BehaviorClassification` adds `mcp-behavior-change` from local ledger history alone — a server whose recorded `Action`s have only ever been one kind performing another for the first time — no cross-user aggregation or registry-declared capability data needed (neither exists yet). `Backtest` validates the pipeline against real documented incidents (`postmark-mcp`'s rug-pull, CVE-2025-6514) before any public directory export is trusted. Drives `hyctl mcp registry sync\|scan\|audit\|export\|backtest\|list\|clear`. |
| `internal/pending` | Tasks parked on a ledger `ask` verdict, under `logs/pending/<task-id>.json`. An `ask` stops dispatch **before any executor runs** and does not fall through to the next fallback candidate — skipping the head that needs permission and running a cheaper one would mean the question is never asked, and for a resource-scoped rule would reach the gated resource anyway. `Save` is temp-then-rename; `Load` fails loudly on a corrupt or incomplete file rather than resuming on a zero value; the bound refuses new work instead of pruning, since discarding a question drops work someone is waiting on. `dispatch.Resume` consumes the file before dispatching, which is what makes answering idempotent, and re-approves **only the stored head** (`Options.AnsweredHead`). `dispatch.Decline` is a package function, not a Dispatcher method, so a machine with no working config can still refuse a task it parked. Drives `hyctl ask list\|answer\|decline`. |
| `internal/oracle` | Verification oracles: `Oracle`/`CommandOracle` run tests/compile/lint (exit 0 = pass) and map the verdict to a calibrated LLR (`oracle.LLR`) — a high-`D` evidence source. Drives `hyctl oracle verify`. |
| `internal/tui` | Bubble Tea TUI: init wizard, install flow |
| `internal/review` | Code review subcommand |
| `internal/editor` | Editor integration |

### Key invariants
- `internal/util.Accumulator` **must** be used for all subprocess stdout/stderr capture — never `bytes.Buffer` for unbounded output.
- `internal/pricing.DB` is the single source of truth for all cost estimation — never hardcode $/token values.
- `internal/swarm` uses `sync.WaitGroup` (not errgroup) for race mode to guarantee goroutine drain and prevent zombie agy subprocesses.
- All executors must set `Response.Truncated = true` when output was capped.

### Pricing flow
```
pricing.Load()
  → readCache()           # ~/.config/hydra/pricing_cache.json (24h TTL)
  → fetchFromOpenRouter() # background refresh if stale
  → loadFallbackTiers()   # registry/pricing.yaml — embedded in the binary, on-disk copy wins
```
The tier table is not just an offline fallback: it is what prices the CLI-agent heads (claude, agy,
codex, cursor…) that never appear in OpenRouter's catalog.
`HYDRA_PRICING_TTL_HOURS` overrides the 24h TTL.
`hyctl pricing refresh` forces a synchronous fetch.
`hyctl pricing list [filter] [--json]` shows all known models.

### Swarm dispatch
```
hyctl dispatch --swarm --swarm-mode race|best|all "<prompt>"
  --swarm-heads head1,head2    # explicit head IDs (bypasses tier)
  --swarm-max-heads 5          # cap fan-out
  --swarm-max-cost 0.05        # pre-flight cost guard in USD
  --swarm-judge-tier 1         # which tier judges in 'best' mode
```
