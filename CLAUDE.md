# Hydra — Orchestrator Instructions
# Claude Code is the BRAIN. Everything else is a HEAD.

## What Hydra Is
A multi-model AI orchestration system. Claude Code orchestrates; Antigravity (agy) and Ollama do the work.
Never do work yourself that belongs to a lower tier. Never escalate work to yourself that a cheaper head can handle.

---

## Directory Layout
```
registry/routing.yaml   ← THE ENUM. Change tier assignments here only.
registry/models.yaml    ← All model definitions, token pools, fallback chains.
registry/domains.yaml   ← Domain → enum key routing (references routing.yaml).
registry/pricing.yaml   ← Static tier pricing fallback (used when offline).
dispatch/route.sh       ← Entry point for all delegated execution.
dispatch/agy.sh         ← agy subprocess wrapper.
dispatch/ollama.sh      ← Ollama API wrapper.
heads/                  ← Per-model identity/capability descriptions.
skills/                 ← Claude Code skills (delegate, rubber-duck, escalate).
.agents/skills/         ← agy TUI slash command skills.
context/                ← Convention templates to inject into delegated prompts.
logs/                   ← Dispatch log + state.json (pool exhaustion, claude_pct).

internal/pricing/       ← Live pricing DB (OpenRouter fetch + 24h cache + tier fallback).
internal/util/          ← Shared utilities (Accumulator, etc).
internal/swarm/         ← Swarm dispatch — fan-out to multiple heads (race/best/all).
```

---

## Orchestration Protocol

### Step 1 — Classify
Read `registry/domains.yaml` to identify the domain and task type.
Look up the enum key (e.g. `SIMPLE`, `COMPLEX`).
Check `registry/routing.yaml` to resolve the enum key to a tier number.

### Step 2 — Check State
```bash
dispatch/route.sh --status      # check claude_pct and exhausted pools
```
If claude_pct ≥ 75: freeze escalations, do not route new work to tier 1 (yourself).
If claude_pct ≥ 95: emergency mode — warn user, only route, don't execute.

### Step 3 — Dispatch
```bash
dispatch/route.sh --enum SIMPLE --prompt "<task>" [--context <file>] [--a2a logs/last_handoff.json]
```
route.sh handles all fallbacks automatically. You do not need to retry.

### Step 4 — Review
Read the output. Ask: does this compile? match conventions? solve the task?
If no → escalate one tier: `--tier $((current+1_lower))`  (route.sh handles it)
If yes → apply to disk, continue.

### Step 5 — Rubber Duck
For any output from tiers 2-3 (Claude family in agy), run rubber duck review:
```bash
dispatch/route.sh --tier 4 --prompt "Review this for tradeoffs and blind spots:\n<output>"
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
dispatch/route.sh --tier 6 --prompt "<next task>" --a2a /tmp/hydra_handoff.json
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

**Same 70%/75%/80% rule applies to ALL delegated models** — route.sh enforces this via fallback chains.
**Qwen (tier 10) is always the terminal fallback** — it runs locally so is always available regardless of API limits.

---

## Karpathy Guidelines (always apply)
- Think before coding. State assumptions. Push back when a simpler approach exists.
- Minimum code. No speculative features. No abstractions for single-use code.
- Surgical changes. Touch only what you must. Match existing style.
- If output is 200 lines and could be 50, ask the delegated head to rewrite.
- Never add error handling for impossible scenarios.

---

## gstack Skills Available
Use these when needed — invoke as /skill-name:
/browse        — web browsing (prefer this over MCP browser tools)
/review        — code review
/qa            — QA run
/ship          — ship checklist
/plan-eng-review — engineering plan review
/plan-ceo-review — executive plan review
/investigate   — deep investigation
/document-generate — documentation generation
/retro         — retrospective
/benchmark     — benchmarking
/canary        — canary deployment
/careful       — careful mode (extra checks)
Full list: autoplan, benchmark-models, browse, canary, careful, codex, cso,
design-consultation, design-html, design-review, design-shotgun, devex-review,
document-generate, document-release, freeze, guard, health, investigate,
land-and-deploy, learn, office-hours, pair-agent, plan-ceo-review,
plan-design-review, plan-devex-review, plan-eng-review, qa, qa-only,
retro, review, scrape, ship, skillify, unfreeze

## Karpathy Skills Available
Located at ~/.claude/skills/karpathy/skills/karpathy-guidelines
Apply these guidelines to any code review or generation task.
Key principles: think first, simplicity, surgical edits, test your code,
no hallucinated APIs, explicit about tradeoffs.

---

## Quick Reference
```bash
# Dispatch by enum key (preferred)
dispatch/route.sh --enum SIMPLE --prompt "write a User DTO in TypeScript"

# Dispatch by tier
dispatch/route.sh --tier 8 --prompt "write a User DTO in TypeScript"

# With file context
dispatch/route.sh --enum STANDARD --prompt "add pagination" --context src/users/controller.ts

# With A2A handoff
dispatch/route.sh --enum MODERATE --prompt "add auth" --a2a logs/last_handoff.json

# Check system status
dispatch/route.sh --status

# List all tiers
dispatch/route.sh --list
```

---

# Development Workflow — Issue-First, Always

> **Golden rule**: No code without a GitHub issue. No branch without an issue number.
> Claude Code must follow this workflow for every task, no exceptions.

---

## Step 1 — Create a GitHub Issue FIRST

Before touching any code, create an issue:

```bash
# Feature
gh issue create \
  --title "feat: <short description>" \
  --body "## Problem\n\n## Solution\n\n## Acceptance Criteria\n- [ ] " \
  --label "enhancement" \
  --assignee "@me"

# Bug
gh issue create \
  --title "fix: <short description>" \
  --body "## Steps to Reproduce\n\n## Expected\n\n## Actual\n\n## Fix" \
  --label "bug" \
  --assignee "@me"
```

Capture the issue number (e.g. `#43`). Everything that follows references it.

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
# Always branch from develop (except hotfix → branch from main)
git checkout develop && git pull origin develop
git checkout -b feature/43-hydra-stats
```

---

## Step 3 — Conventional Commits (required)

Every commit must follow the conventional commit spec.
**This drives automatic changelog generation and version bumps.**

| Prefix | Effect | Example |
|---|---|---|
| `feat:` | minor version bump | `feat: add hydra stats subcommand` |
| `fix:` | patch bump | `fix: nil panic in dispatch on empty prompt` |
| `feat!:` or `BREAKING CHANGE:` in body | major bump | `feat!: rename --tier to --level` |
| `perf:` | patch bump | `perf: cache probe results for 60s` |
| `refactor:` | no bump | `refactor: extract dispatch logic` |
| `chore(deps):` | no bump | `chore(deps): bump bubbletea v0.28` |
| `docs:`, `test:`, `ci:`, `style:` | no bump, hidden in changelog | — |

```bash
# Good commit messages
git commit -m "feat(dispatch): add --dry-run flag to preview routing decisions"
git commit -m "fix(update): skip check when HYDRA_NO_UPDATE_CHECK is set"
git commit -m "chore(deps): bump golang.org/x/sys to v0.25.0"
```

---

## Step 4 — Open a Pull Request → Link to Issue

```bash
gh pr create \
  --title "feat(stats): hydra stats — cost breakdown by model/tier/day" \
  --body "$(cat <<'EOF'
## Summary
- Adds `hydra stats` subcommand
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
```

**PR rules:**
- Title must be a valid conventional commit (e.g. `feat(scope): description`)
- Body must contain `Closes #<issue>` — auto-links and closes the issue on merge
- Target branch is `develop` (not `main`) for all feature/fix work
- Hotfixes only target `main`

---

## Step 5 — Review & Merge

- PRs to `develop` require 0 approvals (self-merge allowed) but must pass CI
- PRs to `main` require 1 approval
- Merge strategy: **Squash and merge** for features/fixes (clean linear history)
- Never force-push to `develop` or `main`

---

## Step 6 — Release Cycle

### Automatic (normal flow)
`release-please` watches `main` and does this automatically:

```
Commits land on develop
        ↓
PR merged to main (conventional commits)
        ↓
release-please opens "Release PR" automatically
(bumps version in .release-please-manifest.json,
 updates CHANGELOG.md with grouped commits)
        ↓
Review and merge the Release PR
        ↓
Tag created (e.g. v1.2.0) → release.yml fires
        ↓
GoReleaser builds all platforms, publishes GitHub Release,
updates Homebrew tap (ankit373/homebrew-hydra)
```

### Manual release (emergency / hotfix)
```bash
# Cut a release manually — only when release-please can't
git checkout main && git pull
git tag v1.2.1 -m "hotfix: fix nil panic in dispatch"
git push origin v1.2.1
# → release.yml fires automatically
```

### Release channels
| Channel | Branch | Tag | Install |
|---|---|---|---|
| **stable** | `main` (tagged) | `v1.2.0` | `brew install hydra` |
| **beta/rc** | `release/v1.2.0` | `v1.2.0-rc.1` | pre-release on GitHub |
| **edge** | `develop` | `edge` (overwritten) | GitHub pre-release |

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
ISSUE_URL=$(gh issue create --title "feat: hydra stats" --label enhancement --assignee "@me" \
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
git commit -m "feat(stats): add hydra stats subcommand (#${ISSUE})"

# 4. Push and open PR + move to In Review
git push -u origin HEAD
gh pr create --title "feat(stats): add hydra stats subcommand" \
  --body "Closes #${ISSUE}" --base develop
gh project item-edit --project-id PVT_kwHOAL1qLc4BZbZZ --id "$ITEM_ID" \
  --field-id PVTSSF_lAHOAL1qLc4BZbZZzhUaGlE --single-select-option-id 1490e846

# 5. After merge — move to Done
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
cliff.toml                    ← changelog generation from conventional commits
release-please-config.json    ← release-please behaviour
.release-please-manifest.json ← current version (managed by release-please)
CHANGELOG.md                  ← auto-generated, do not edit manually
internal/build/build.go       ← version vars set by ldflags at build time
internal/update/update.go     ← startup update checker (24h cache)
.github/workflows/release.yml ← fires on tag push → goreleaser
.github/workflows/edge.yml    ← fires on develop push → edge build
.github/workflows/release-please.yml ← fires on main push → release PR
```

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
| `internal/config` | Hydra config load/save (`~/.config/hydra/`) |
| `internal/company` | Playbook run state machine (multi-stage AI workflows) |
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
  → loadFallbackTiers()   # registry/pricing.yaml (always loaded)
```
`HYDRA_PRICING_TTL_HOURS` overrides the 24h TTL.
`hydra pricing refresh` forces a synchronous fetch.
`hydra pricing list [filter] [--json]` shows all known models.

### Swarm dispatch
```
hydra dispatch --swarm --swarm-mode race|best|all "<prompt>"
  --swarm-heads head1,head2    # explicit head IDs (bypasses tier)
  --swarm-max-heads 5          # cap fan-out
  --swarm-max-cost 0.05        # pre-flight cost guard in USD
  --swarm-judge-tier 1         # which tier judges in 'best' mode
```

---

## Open Backlog (issues to build next)

Track via `gh issue list`. Key upcoming features:

| # | Feature | Status |
|---|---|---|
| #11 | Swarm dispatch (race/best/all) | PR #51 open |
| #52 | Dynamic pricing (OpenRouter) | PR #61 open |
| #53 | Lifecycle hook system (PreDispatch/PostDispatch events) | open |
| #54 | `hydra stats` — session cost rollup | open |
| #55 | Programmatic token budget / claude_pct auto-tracking | open |
| #56 | TUI component library (Box/ScrollBox/ProgressBar/Spinner) | open |
| #57 | Context window tracker (auto-measure claude_pct from API responses) | open |
| #58 | `EndTruncatingAccumulator` | ✅ merged |
| #59 | Swarm head retry + reconnection (backoff on 429/503) | open |

### claude-code patterns being ported to Hydra
These were extracted from the Claude Code source (`/Users/ankitjha/claude-code/src`) and filed as issues above:
- **Cost tracking**: session rollup pattern from `cost-tracker.ts` → #54
- **Hook system**: 26-event lifecycle taxonomy from `agentSdkTypes.ts` → #53
- **Token budget enforcement**: `tokenBudget.ts` threshold logic → #55
- **TUI components**: Ink `Box/ScrollBox/ProgressBar/Spinner` patterns → #56
- **Context window tracking**: `tokens.ts` + `context.ts` measurement → #57
- **Output accumulator**: `EndTruncatingAccumulator` from `stringUtils.ts` → #58 ✅
- **Swarm reconnection**: `swarm/reconnection.ts` backoff patterns → #59
- **Dynamic pricing**: OpenRouter API (not `modelCost.ts` which is Anthropic-only) → #52
