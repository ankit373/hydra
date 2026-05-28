# Hydra Phase 2 — Notes & Open Work

Phase 1 (workspace + edit.sh + review.sh + parallel.sh `file` field) is shipped.
**Decision layer** (policy.yaml + decide.sh + --policy plumbing) is also shipped.

For the comprehensive strategy taxonomy informed by prior art (Aider, OpenHands,
Cline, Cursor, Morph, plan-then-execute literature), see
[STRATEGY_RESEARCH.md](./STRATEGY_RESEARCH.md). The recommended build order
below supersedes the original 4-item roadmap.

This file captures what we learned during Phase 1 and what Phase 2 should tackle.

---

## Probe: agy's agentic mode

`agy --help` reveals agy is fully agentic:

| Flag                             | Purpose                                       |
|----------------------------------|-----------------------------------------------|
| `--add-dir`                      | Add a directory to the workspace (repeatable) |
| `--dangerously-skip-permissions` | Auto-approve tool permission requests         |
| `--sandbox`                      | Run in a sandbox with terminal restrictions   |
| `--print` / `-p`                 | Single-shot non-interactive (current Hydra)   |
| `--prompt-interactive` / `-i`    | Agentic with continuation                     |
| `--continue` / `-c`              | Resume most recent conversation               |

Settings file (`~/.gemini/antigravity-cli/settings.json`) already has a
`trustedWorkspaces` array — agy has a native workspace concept.

### What we tested

Probe command:
```bash
agy --print --add-dir /tmp/hydra-probe --dangerously-skip-permissions \
  "Read probe.txt. Append a third line 'edited by agy'. Use file tools. Then print DONE."
```

### What happened

1. **agy IS agentic in `--print` mode.** It spawned internal Tasks, ran shell
   commands (`grep`, `ls`), launched timers — all without us prompting it to
   use tools. The framework is real.

2. **It went completely off-rails.** Instead of editing `probe.txt`, agy
   hallucinated a project called "vyuha" (likely free-associated from "agy"
   + Sanskrit-rooted word) and started exploring
   `/Users/ankitjha/Documents/Agents/vyuha` — a totally unrelated repo.

3. **Workspace boundaries are real.** Even with `--dangerously-skip-permissions`,
   agy emitted "I will request permission to read and write files in the
   `/Users/ankitjha/Documents/Agents/vyuha` directory". The permission flag
   skips *prompts*, not *boundaries* — agy still respected the workspace ACL.

4. **It timed out** without modifying the target file.

### Why it went off-rails (hypothesis)

`~/.gemini/antigravity-cli/` contains `brain/`, `knowledge/`, `implicit/`,
`scratch/`, and `conversations/` directories. agy auto-loads cross-session
memory and pre-existing project context. Our scoped probe inherited
unrelated context from prior sessions and the model latched onto it.

The current `dispatch/agy.sh` doesn't isolate the agent — each `agy --print`
call hits the same brain/knowledge dirs. For deterministic, stateless edits,
we'd need to either:
- Bypass `~/.gemini/antigravity-cli` per invocation (env var / config override)
- Use a fresh `--conversation <new-id>` per call so no continuation kicks in
- Wipe scratch + implicit before each call (destructive — affects user's IDE sessions)

### Verdict for Phase 1

Don't rely on agy's agentic mode yet. Stick with `--print` text-rewrite via
the strict-marker protocol in `edit.sh`. It works deterministically across
all tiers (agy, ollama, future GPT) without the brain-pollution problem.

---

## Phase 2 Roadmap — superseded

The original 4-item roadmap below is superseded by the 16-item taxonomy in
[STRATEGY_RESEARCH.md](./STRATEGY_RESEARCH.md). Sections 2.1–2.7 here remain
as the original design notes but the build order in STRATEGY_RESEARCH.md
should be the source of truth.

### Original Phase 2 Roadmap (kept for design history)

### 2.1 Stateless agentic mode for agy

Build `dispatch/agy-agent.sh` that:
1. Resolves the target workspace via `scope.sh`
2. Invokes agy with `--add-dir <git_root>` (not the whole workspace —
   tighter scope per task)
3. Uses a fresh `--conversation hydra-<uuid>` so no prior context leaks in
4. Sets `HOME` or `XDG_CONFIG_HOME` overrides to redirect agy to a
   per-invocation profile dir (no brain/knowledge inheritance)
5. After completion, just runs `git diff` (no marker parsing needed —
   agy has already written the files itself)
6. Returns the same JSON shape as `edit.sh` so it's a drop-in alternative

Tier capability matrix:

| Tier         | Text rewrite (Phase 1) | Agentic edit (Phase 2) |
|--------------|------------------------|------------------------|
| GRUNT (10)   | ✓ ollama               | — (no agentic mode)    |
| TRIVIAL–EXPERT (9–2) | ✓ agy --print  | ✓ agy --print + tools  |
| HARD (4)     | ✓ agy/GPT-OSS          | ?  (GPT-OSS tooling)   |
| CORE (1)     | n/a                    | n/a (Claude native)    |

Decision rule for Phase 2: agentic mode for multi-file changes; text rewrite
for single-file. Agentic loops can edit the whole feature in one round.

### 2.2 Aider-style search/replace blocks

For large files where full-rewrite is wasteful (e.g. a 1500-line file with
a 5-line change), support an SR-block protocol:

```
<<<HYDRA_SR_FILE>>> /abs/path.ts
<<<SEARCH>>>
old code lines
<<<REPLACE>>>
new code lines
<<<END>>>
```

edit.sh detects the SR variant and applies via `patch` or in-place string
replace. Fails over to full-rewrite if blocks don't apply cleanly.

### 2.3 Per-edit auto-commit

When the workspace has git, optionally auto-commit each approved edit with a
trailer like:
```
hydra: <enum> edited <file>
Co-Authored-By: Hydra Tier <KEY> <models@hydra.local>
```

Off by default. `--commit-on-approve` opt-in via flag or env. Gives finer
git history for `git bisect` when Hydra-generated code breaks.

### 2.4 Atomic multi-file `--atomic`

Today, if 2 of 3 edits in a parallel batch fail validation, the 1 successful
edit stays in the working tree. For migrations/refactors that need
all-or-nothing semantics, add `--atomic` flag to parallel.sh:
- Snapshot all targets via `git stash push` (or backup files)
- Run all edits
- If any fail → restore all from stash
- If all pass → drop the stash

### 2.5 Better validation for TS/TSX

Current path: try workspace-local `node_modules/.bin/tsc --noEmit`. Falls
back to nothing if not present. Phase 2:
- Detect monorepo tools (turbo / nx / pnpm workspace) and run their
  per-package typecheck
- Use SWC's parser for fast syntax check when full type check is too slow
- Per-package validator config in `workspace.yaml`

### 2.6 Diff size budget for review

For very large diffs (>500 lines), `review.sh summary` should:
- Truncate the per-file output, keeping just the stat
- Suggest QA-dispatch to a higher tier for sanity check before approval
- Maybe automatically split into hunks for piecewise review

### 2.7 Token tracking

Each `route.sh` / `edit.sh` call should log:
- Tier used, prompt tokens (estimated), response tokens (estimated)
- Wall time
- Pool name

Aggregate into `~/hydra/logs/cost.jsonl` so `hydra status` can show
"this session: 47k tokens across 12 dispatches, ~$0.X spent".

---

## Phase 1 caveats users should know

- **Stable backup file collision.** If two agents edit the same non-git file
  in parallel, they'd race on `.hydra-bak`. parallel.sh now pre-flight
  rejects duplicate targets, but a manual sequence of `edit.sh` calls won't
  be caught. Mitigated by: prefer git workspaces.

- **Small models drop markers.** Qwen (GRUNT) sometimes omits one of the
  HYDRA_FILE_START/END markers. edit.sh has a lenient parser that tolerates
  a missing START or END, but if both are missing it fails. Mitigated by:
  escalate to TRIVIAL/SIMPLE on `marker_parse_failed`.

- **Validator coverage is uneven.** TS/TSX only validates if a workspace-local
  tsc is present. Many file types have no validator. Mitigated by:
  manual review remains the safety net. Phase 2.5 expands coverage.

- **No multi-file synthesis in one dispatch.** Each edit.sh call edits one
  file. Cross-file refactors require either text-mode + manual orchestration
  or Phase 2.1 (agentic mode where agy edits multiple files in one loop).
