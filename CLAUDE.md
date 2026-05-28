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
dispatch/route.sh       ← Entry point for all delegated execution.
dispatch/agy.sh         ← agy subprocess wrapper.
dispatch/ollama.sh      ← Ollama API wrapper.
heads/                  ← Per-model identity/capability descriptions.
skills/                 ← Claude Code skills (delegate, rubber-duck, escalate).
.agents/skills/         ← agy TUI slash command skills.
context/                ← Convention templates to inject into delegated prompts.
logs/                   ← Dispatch log + state.json (pool exhaustion, claude_pct).
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
