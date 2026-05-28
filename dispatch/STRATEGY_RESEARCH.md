# Hydra — Execution Strategy Research

Survey of prior art (Aider, OpenHands, Cline, Cursor, Morph, Claude Code,
Plan-then-Execute literature, AST/tree-sitter refactoring) to expand the
strategy taxonomy beyond the initial 4 (atomic, SR-blocks, agentic-agy,
auto-commit, token-tracking).

Each strategy below is tagged:
- **MUST** — build in Phase 2 (high leverage, fits Hydra arch)
- **NICE** — build later (smaller win or larger lift)
- **SKIP** — not a fit for Hydra (different paradigm / out of scope)

---

## A. Edit format strategies

How the agent represents changes to a file.

| Strategy | Source | Tag | Why |
|---|---|---|---|
| **rewrite** (full file) | Aider "whole" | shipped (Phase 1 default) | Robust but costly on large files |
| **SR-blocks** (search/replace) | Aider "diff", Morph | MUST | Aider benchmarks: outperforms whole for high-capacity models; surgical |
| **udiff** (unified diff) | Aider "udiff" | MUST | Anti-laziness for GPT-4 Turbo–family; cheap to parse |
| **agentic** (agent does its own editing) | Cursor Composer, agy | MUST | Multi-file in one loop; off-balance-sheet tool turns |
| **AST-typed operations** (RenameSymbol, MoveFunction, ExtractModule) | Morph, tree-sitter | NICE | Semantic correctness over textual; large lift (tree-sitter integration per language) |

**Hydra design note:** per-tier capability matrix matters. Qwen (GRUNT) does
SR-blocks badly; Sonnet/Opus do all formats well. Encode in `policy.yaml`:
```yaml
- name: weak_tier_prefers_rewrite
  when: { enum_tier_gte: 8 }
  apply: { edit_mode: rewrite }   # weak models drop markers in SR
```

---

## B. Isolation strategies

How parallel work avoids stepping on itself.

| Strategy | Source | Tag | Why |
|---|---|---|---|
| **same-file pre-flight reject** | Hydra Phase 1 | shipped | Already done |
| **atomic snapshot/restore** | DB transactions | MUST (was Option 2) | All-or-nothing for migrations |
| **git worktrees per agent** | Cursor 3, Claude Code subagents | MUST | True isolation — N agents on the same repo edit different worktrees; merge at end. Up to 8 parallel agents in Cursor 3. |
| **file-level locking** | basic | shipped | Phase 1 same-file reject covers this |

**Hydra design note:** worktrees + `parallel.sh` would unlock 5+ agents on
the same git_root without conflict. Each subtask gets its own worktree;
parallel.sh merges in dependency order at the end. Replaces the same-file
race rejection with a proper merge.

---

## C. Verification strategies

How we know an edit is correct.

| Strategy | Source | Tag | Why |
|---|---|---|---|
| **syntax validator** | basic | shipped | Phase 1 has `node --check`, `python -m py_compile`, etc. |
| **type-check validator** | basic | shipped (TS partial) | Phase 1 falls back to local `tsc` |
| **rubber duck** (cross-tier review) | basic | shipped (in policy.yaml) | Tier 4 reviews tier 5-8 output |
| **auto-test loop** | Cursor agent, Aider `/test` | MUST | Run tests → if red, feed errors back to model → iterate. Massive correctness win. |
| **linter-driven fix loop** | Aider `/lint`, Cline | MUST | Same as test loop but with eslint/ruff/etc. Cheaper than tests. |
| **plan-then-execute with critic** | P-t-E literature, GAN-style | NICE | Separate planner → executor → critic roles. Company Mode does this at workflow level; per-task version would be heavier. |
| **dual-track testing** (mocked + real) | OpenHands | SKIP | About testing Hydra itself, not a runtime strategy |
| **diff size cap** (reject if >N% changed) | safety net | shipped (in policy.yaml) | Already added |

**Hydra design note:** test/lint loops are the single biggest correctness
upgrade. Pattern:
```
edit.sh → validator → if fail → re-dispatch with error in context → repeat (max_retries)
```
Already partially supported via `max_retries` + `escalate_on_fail` in policy.

---

## D. Context strategies

What the agent gets to see.

| Strategy | Source | Tag | Why |
|---|---|---|---|
| **repo map** (compressed codebase view) | Aider repo-map, Cline indexing | MUST | Symbol-level summary of the whole repo so the agent can navigate without reading every file. Huge token savings on large repos. |
| **file context tracking** (read/edit timeline) | Cline FileContextTracker | NICE | Detects "user edited file outside agent" → reload before next edit. Avoids stale-context bugs. |
| **duplicate-read dedup** | Cline | MUST | Replace second+ read of same file with `[ALREADY READ AT TURN N]` marker. Easy token saver. |
| **CodeAct** (agent generates bash/python directly) | OpenHands | SKIP | Different paradigm — Hydra agents return text/edits, not executable. Company Mode handles this at the playbook level. |
| **out-of-sync detection** | Cline | NICE | Hash-check file before edit; if changed since last read, re-fetch. |

**Hydra design note:** repo map is the biggest token-efficiency unlock. A
~50KB compressed summary of apphire's structure lets edit.sh prompts skip
the "let me read the whole file first" round-trip. Build via tree-sitter
extraction or naive `ctags` to start.

---

## E. Routing & escalation strategies

When to bump up to a stronger tier, when to give up.

| Strategy | Source | Tag | Why |
|---|---|---|---|
| **bounded retries** (N attempts then escalate) | basic | shipped (in policy `max_retries`) | Already in policy |
| **escalate on fail** | basic | shipped | In policy |
| **confidence thresholding** | model-self-rated | NICE | Model returns confidence score; below threshold → escalate without consuming retries. Most models don't reliably self-rate. |
| **cost ceiling per task** | guard rail | MUST | Abort if accumulated cost > $X for a single task. Prevents runaway loops. Needs token tracking first. |
| **time ceiling per task** | guard rail | MUST | Same but wall-clock. Easy add. |
| **tier-by-task-complexity** (Hydra core) | Hydra | shipped | The whole enum system is this |

**Hydra design note:** cost+time ceilings are mandatory once auto-test loops
exist (those loops can iterate indefinitely). Add `max_cost_usd` and
`max_wall_seconds` to policy.

---

## F. Caching strategies

| Strategy | Source | Tag | Why |
|---|---|---|---|
| **Anthropic prompt cache** | SDK feature | MUST | System prompt + repo map → cache_control. ~90% cost reduction on long system prompts. Free win once we use the SDK directly. |
| **response cache** (hash(prompt+file) → reuse) | basic | NICE | Same edit instruction on same file → reuse last response. Useful for retries. |
| **repo-map cache** | Aider | MUST | Recompute repo map only when files change (file mtime check). |

**Hydra design note:** the agy/ollama wrappers don't expose Anthropic
caching directly — they shell out to CLIs that may or may not use it.
Phase 2: a direct-SDK tier for Claude calls (replaces the agy→Claude path)
to unlock caching.

---

## G. Workflow strategies (multi-step orchestration)

| Strategy | Source | Tag | Why |
|---|---|---|---|
| **plan-then-execute** | P-t-E literature | shipped via Company Mode | `ship_a_feature` playbook is exactly this |
| **subagent tree decomposition** | Cursor 3 | NICE | A complex stage spawns N subagents in parallel worktrees. Phase 3-level — needs worktree primitive first. |
| **long-horizon agents** | Composer 2.5 | NICE | Persistent context across many turns. Better fit for Claude Code itself than for Hydra primitives. |
| **defensive programming mode** | OpenHands | NICE | "Read more context before editing" — vague but useful as a policy flag for risky tasks. |

---

## Recommended Phase 2 build order

Sequenced for dependency + leverage:

### Wave 1 — efficiency wins (cheap, high impact)
1. **Token tracking** (already on roadmap; enables ceilings + audit)
2. **Anthropic prompt cache** via direct-SDK Claude tier
3. **Repo map** + **repo-map cache** (per workspace, built via tree-sitter or ctags)
4. **Duplicate-read dedup** (in `edit.sh` prompt builder)

### Wave 2 — correctness wins (medium lift)
5. **udiff edit mode** (alongside SR-blocks)
6. **SR-blocks edit mode** (was Option 3)
7. **Auto-test loop** + **linter-driven fix loop** (validator extensions)
8. **Cost ceiling** + **time ceiling** in policy

### Wave 3 — parallelism wins (heavier lift)
9. **Atomic batches** (was Option 2)
10. **Git worktrees per parallel agent**
11. **Auto-commit per approved edit** (was Option 4)
12. **Agentic agy** with stateless invocation (was Option 1)

### Wave 4 — semantic depth (research-grade)
13. **AST-typed operations** (Morph-style RenameSymbol, MoveFunction)
14. **File context tracking** with out-of-sync detection
15. **Confidence thresholding** (if/when models reliably self-rate)
16. **Subagent tree decomposition** (Cursor 3 pattern)

---

## Sources

- Aider edit formats: https://aider.chat/docs/more/edit-formats.html
- Aider unified diffs: https://aider.chat/docs/unified-diffs.html
- OpenHands SDK paper: https://arxiv.org/pdf/2511.03690
- Cline context management: https://medium.com/@balajibal/dissecting-cline-cline-context-management-260aec3d84cb
- Cline SDK announce: https://cline.ghost.io/introducing-cline-sdk-the-upgraded-agent-runtime/
- Cursor 2.0 architecture: https://www.digitalapplied.com/blog/cursor-2-0-agent-first-architecture-guide
- Cursor 3 / Composer 2.5: https://lushbinary.com/blog/composer-2-5-long-horizon-agents-cursor-sdk-guide/
- Plan-then-Execute pattern (SAP): https://community.sap.com/t5/security-and-compliance-blog-posts/plan-then-execute-an-architectural-pattern-for-responsible-agentic-ai/ba-p/14239753
- Git worktree patterns for AI agents: https://zylos.ai/research/2026-02-22-git-worktree-parallel-ai-development
- Morph AST-level refactoring: https://dev.to/nilofer_tweets/morph-ast-level-refactoring-where-the-llm-describes-intent-not-code-1hh6
- AST vs textual edits: https://themiloway.github.io/milo-blog/agentic-coding/typescript/2026/02/03/structural-vs-textual-code-manipulation.html
