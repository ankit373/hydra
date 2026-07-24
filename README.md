<div align="center">

<pre>
    ██╗  ██╗██╗   ██╗██████╗ ██████╗  █████╗ 
    ██║  ██║╚██╗ ██╔╝██╔══██╗██╔══██╗██╔══██╗
    ███████║ ╚████╔╝ ██║  ██║██████╔╝███████║
    ██╔══██║  ╚██╔╝  ██║  ██║██╔══██╗██╔══██║
    ██║  ██║   ██║   ██████╔╝██║  ██║██║  ██║
    ╚═╝  ╚═╝   ╚═╝   ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═╝
</pre>

### The AI Control Plane for Software Development

*One command. Every model. Total control.*

[![License: MIT](https://img.shields.io/badge/License-MIT-8b5cf6.svg)](LICENSE)
[![Go 1.24](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey?logo=apple)](https://hydra.uvansa.com)
[![GitHub Stars](https://img.shields.io/github/stars/ankit373/hydra?style=flat&color=8b5cf6)](https://github.com/ankit373/hydra/stargazers)
[![GitHub Issues](https://img.shields.io/github/issues/ankit373/hydra?color=06b6d4)](https://github.com/ankit373/hydra/issues)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

<br/>

[**Website**](https://hydra.uvansa.com) · [**Docs**](https://hydra.uvansa.com/docs) · [**Roadmap**](https://github.com/users/ankit373/projects/2) · [**Issues**](https://github.com/ankit373/hydra/issues)

</div>

---

## What is Hydra?

You have Claude Code for complex problems, Codex for code generation, Ollama running local models, API keys for half a dozen providers. Every prompt goes to the most expensive model because routing is manual. Sensitive data flows through third-party APIs because there is no enforcement layer. You have no idea what you're spending across all of them.

**Hydra is the control plane that sits in front of all of it.**

It discovers every AI model on your machine, assigns each a capability score, routes tasks to the right model by complexity and cost, enforces PII policy so sensitive data never leaves your machine, and logs every dispatch with token counts and cost — without any manual configuration.

And it's growing into a **Trust Control Plane**: route not just to the cheapest model, but to a *target confidence of correctness* — sampling models adaptively and stopping the moment you're sure enough (see **Confidence Routing** under [Features](#features)).

```bash
brew install ankit373/hydra/hydra && hydra init
```

> **Pure Go, single binary.** Hydra is one `hydra` CLI — the legacy shell layer is gone. Everything below runs through `hydra <command>`.

---

## Works With

Hydra discovers and routes to all of these automatically — no plugins, no manual config:

**Coding Agents & CLIs**

| | | | | |
|:-:|:-:|:-:|:-:|:-:|
| ![Claude](https://img.shields.io/badge/Claude_Code-8b5cf6?logo=anthropic&logoColor=white&style=flat-square) | ![Codex](https://img.shields.io/badge/OpenAI_Codex-412991?logo=openai&logoColor=white&style=flat-square) | ![Cursor](https://img.shields.io/badge/Cursor-black?logo=cursor&logoColor=white&style=flat-square) | ![Kiro](https://img.shields.io/badge/Amazon_Kiro-FF9900?logo=amazon&logoColor=white&style=flat-square) | ![Windsurf](https://img.shields.io/badge/Windsurf-0ea5e9?style=flat-square) |
| ![Gemini](https://img.shields.io/badge/Gemini_CLI-4285F4?logo=google&logoColor=white&style=flat-square) | ![Copilot](https://img.shields.io/badge/GitHub_Copilot-24292e?logo=github&logoColor=white&style=flat-square) | ![Cody](https://img.shields.io/badge/Sourcegraph_Cody-FF5543?style=flat-square) | ![Amp](https://img.shields.io/badge/Amp-10b981?style=flat-square) | ![Continue](https://img.shields.io/badge/Continue-6366f1?style=flat-square) |

**API Providers**

| | | | | | |
|:-:|:-:|:-:|:-:|:-:|:-:|
| ![Anthropic](https://img.shields.io/badge/Anthropic-8b5cf6?style=flat-square) | ![OpenAI](https://img.shields.io/badge/OpenAI-412991?logo=openai&logoColor=white&style=flat-square) | ![Google](https://img.shields.io/badge/Google_AI-4285F4?logo=google&logoColor=white&style=flat-square) | ![Groq](https://img.shields.io/badge/Groq-f97316?style=flat-square) | ![Mistral](https://img.shields.io/badge/Mistral-f59e0b?style=flat-square) | ![DeepSeek](https://img.shields.io/badge/DeepSeek-06b6d4?style=flat-square) |
| ![Together](https://img.shields.io/badge/Together_AI-10b981?style=flat-square) | ![Fireworks](https://img.shields.io/badge/Fireworks-ef4444?style=flat-square) | ![xAI](https://img.shields.io/badge/xAI_Grok-black?style=flat-square) | ![Bedrock](https://img.shields.io/badge/AWS_Bedrock-FF9900?logo=amazon&logoColor=white&style=flat-square) | ![Azure](https://img.shields.io/badge/Azure_OpenAI-0078D4?logo=microsoft&logoColor=white&style=flat-square) | ![Cohere](https://img.shields.io/badge/Cohere-39d353?style=flat-square) |

**Local Runtimes**

| | |
|:-:|:-:|
| ![Ollama](https://img.shields.io/badge/Ollama-black?style=flat-square) | ![LM Studio](https://img.shields.io/badge/LM_Studio-8b5cf6?style=flat-square) |

---

## Architecture

```
  ┌───────────────────────────────────────────────────────────────────────┐
  │   hydra dispatch  [--local | --swarm | --confidence 0.95]               │
  └───────────────────────────────────┬─────────────────────────────────────┘
                                       │
                     ┌─────────────────▼─────────────────┐
                     │  Policy Engine                     │  PII detection
                     │  (blocks before any network call)  │  cost ceiling · local-only
                     └─────────────────┬─────────────────┘
                                       │
                     ┌─────────────────▼─────────────────┐
                     │  Router                            │  CapScore → tier
                     │  score → tier → fallback chain     │  ~1.1 µs/dispatch
                     └───┬───────────────┬───────────────┬─┘
             single      │        swarm  │    confidence │  (SPRT)
          ┌──────────────▼┐  ┌───────────▼──┐  ┌──────────▼───────────┐
          │ best available│  │ race/best/all│  │ Trust Control Plane   │
          │ head + fallbk │  │ + LLM judge  │  │ calibration → LLR/D    │
          └──────┬────────┘  └──────┬───────┘  │ SPRT optimal-stopping  │
                 │                  │          │ defect-cost model      │
                 │                  │          └──────────┬────────────┘
                 └──────────────────┴─────────────────────┘
                                       │
              ┌────────────────────────┼────────────────────────┐
       ┌──────▼──────┐          ┌──────▼──────┐          ┌───────▼─────┐
       │  CLI Heads  │          │  API Heads  │          │ Local Heads │
       │ Claude Code │          │   OpenAI    │          │   Ollama    │
       │   Codex     │          │  Anthropic  │          │  LM Studio  │
       │  Cursor …   │          │  Groq … +12 │          │             │
       └─────────────┘          └─────────────┘          └─────────────┘
         score 60–95              score 70–95              score 50–72
                                       │
                     ┌─────────────────▼─────────────────┐
                     │  Observability                     │  cost.jsonl (est/actual)
                     │  cost + calibration + trust ledgers│  calibration.jsonl · trust.jsonl
                     └────────────────────────────────────┘
```

Every executor is **native Go** — `agy`, Ollama, per-provider HTTP (OpenAI-compatible + Anthropic/Gemini/Cohere/Azure/Bedrock/Replicate), and generic CLI. No shell scripts in the hot path.

---

## By the Numbers

| Metric | Value | How it's measured |
|---|---|---|
| Routing overhead | **~1,130 ns / dispatch** | `go test -bench=BenchmarkRoutingPath` (Apple M1): policy eval + head selection + budget check |
| Cost vs all-frontier | **75–85% lower** | typical coding session where most tasks are SIMPLE/STANDARD; see `hydra stats` for your own numbers |
| Machine discovery | **< 2 s** for 13+ CLIs, 14 API providers, 2 local runtimes | concurrent PATH + env + port scan (`hydra probe`) |
| Calibration convergence | a 90%-reliable source → **D ≈ 1.76 nats** diagnostic power | `internal/trust` table tests; a coin-flip source → **D ≈ 0** (contributes nothing) |
| SPRT — easy tasks | **2.6 samples, −49% vs fixed-5**, 98.8% accuracy | 20k seeded synthetic trials, `TestSPRT_Law3` |
| SPRT — blended workload | **3.8 samples, −24% vs fixed-5**, 98.2% accuracy | same suite; 71% easy / 29% hard task mix |
| SPRT — hard tasks | **6.8 samples** (adaptively *more* than 5), 96.7% accuracy | fixed-N ensembles can't do this — SPRT spends where accuracy demands it |
| Optimal parallelism | **6 agents** for independent work → **2** for same-subgraph edits | `n* = √((1−s)/k)`, Law 4; `k` from graph coupling (`internal/optimal`) |
| Context signal density | useful tokens = `length × ρ`; a dense 100k window beats a noisy 1M | Law 5; ρ via gzip-ratio proxy (`internal/entropy`) — compact on falling ρ |
| Output capture | bounded at **33 MB** per subprocess | `internal/util.Accumulator` — no unbounded buffers |

> **Methodology & honesty.** Routing overhead, discovery, calibration, and SPRT figures are **measured** by the test/benchmark suite (`go test ./...`, race-clean in CI). The SPRT numbers come from *synthetic* trials with known ground truth, not production traffic — they validate the algorithm (Wald sequential test), and land above the continuous theoretical `E[N]` (easy 1.33 / blended 2.48) because real evidence arrives in discrete steps. Cost-savings ranges are workload-dependent estimates; `hydra stats` reports your actual spend. As production `trust.jsonl` accumulates, `hydra trust stats` graduates these from *modeled* to *observed*.

---

## Features

### 🔍 Zero-Config Discovery

Hydra scans your machine in under two seconds. No config files to edit, no plugins to install.

```
$ hydra probe

  Heads discovered (6)                           2.1s

  › claude              Claude Code         score:95  cli    ✓ ready
    codex               OpenAI Codex        score:88  cli    ✓ ready
    openai              OpenAI (API)        score:88  env    ✓ key found
    ollama/qwen3:8b     Qwen3 8B            score:66  port   ✓ running
    ollama/phi4-mini    Phi-4 Mini          score:64  port   ✓ running
    anthropic           Anthropic (API)     score:95  env    ✓ key found
```

Scanning happens across three channels simultaneously:

| Channel | What it finds |
|---------|---------------|
| **PATH scan** | 13+ CLI tools — Claude Code, Codex, Cursor, Kiro, Windsurf, Gemini, Copilot, Cody, Amp, Continue, Ollama binary |
| **Port scan** | Ollama (11434), LM Studio (1234) — queries each server and lists every installed model individually |
| **Env vars** | 14 API providers — Anthropic, OpenAI, Google, xAI, Groq, Together, Fireworks, Mistral, DeepSeek, Bedrock, Azure, Perplexity, Cohere, Replicate |

### 🧠 Hardware-Aware Local Model Selection

When you have Ollama, Hydra picks the best model for your actual available memory — not your total RAM. It reads 7 days of memory usage history and uses the 75th-percentile free memory reading so the recommendation reflects how your machine actually runs under typical load, not a lucky snapshot.

```
  Detected: Apple Silicon · 16GB total · memory fully occupied (1.2GB free)
  ⚠  Memory tight — close other apps to run local models

  Recommended alternatives (free tiers available):

  ✓ Claude Code   Free tier via claude.ai — npm install -g @anthropic-ai/claude-code
  ✓ OpenAI Codex  Free tier — npm install -g @openai/codex
  ✓ Cursor        Free tier IDE — cursor.com
```

```
  Detected: Apple Silicon · 32GB total · 18.4GB free for models
  Based on 14 samples over 6 days · typical free: 17.8GB avg, 16.2GB p75

  ✓ Qwen2.5-Coder 32B   32B   uses ~20GB · 0.4GB left (very tight)
  ✓ Qwen3 14B           14B   uses ~10GB · 6.2GB left free
  ✓ Qwen2.5-Coder 14B   14B   uses ~10GB · 6.2GB left free
  ✓ Qwen3 8B             8B   uses ~6GB  · 10.2GB left free  ← recommended
  ✓ Qwen2.5-Coder 7B     7B   uses ~5GB  · 11.2GB left free
```

### 🔒 PII-Aware Routing (Enforced, Not Conventional)

Enable local-only policy in `hydra init` and any prompt containing sensitive data is blocked from leaving your machine — at the dispatch layer, before any network call is made.

Detected patterns: Social Security Numbers, credit card numbers, email addresses, API keys and tokens, IP addresses, private key material.

```bash
$ hydra dispatch --prompt "process payment for card 4111-1111-1111-1111"

  Policy violation: prompt contains PII (credit card pattern)
  Action: routing to local-only head

  Dispatching → ollama/qwen3:8b  [local, no API call made]
```

### 💰 Full Cost Visibility

Every dispatch is logged to `~/.hydra/cost.jsonl` with model, tier, token counts, estimated cost, and fallback chain. Costs are **honestly labeled**: `tokens_source` marks whether a provider reported real usage or Hydra estimated it, and `cost_source` is always `estimated` (pricing × tokens, never a billed figure). Run `hydra cost` or `hydra stats` to see where your budget is going.

```
$ hydra cost

  Cost — last 30 days

  Model                 Dispatches   Tokens       Est. Cost
  claude                12           48,200        $0.72
  openai                4            12,100        $0.18
  ollama/qwen3:8b       89           341,000       $0.00  (local, free)
  ─────────────────────────────────────────────────────────
  Total                 105          401,300       $0.90

  Saved vs all-Claude: ~$6.03  (87%)
  Local model share:   85%
```

### ⚡ Tier-Based Dispatch with Automatic Fallbacks

Tasks route through named tiers by capability score. If a head is unavailable, Hydra falls back automatically.

| Tier | Score range | Example heads |
|------|-------------|---------------|
| `expert` | 90–100 | Claude Code, Claude Opus |
| `complex` | 80–89 | Codex, GPT-4.1, Gemini Pro |
| `standard` | 70–79 | Gemini Flash, Claude Haiku |
| `simple` | 60–69 | Qwen3 8B, Qwen2.5-Coder 7B (local) |
| `local` | 50–59 | Llama 3.2 3B, Phi-4 Mini |

```bash
# Let Hydra pick the best available model
hydra dispatch --prompt "refactor auth middleware to use JWT refresh tokens"

# Preview the full fallback chain before dispatching
hydra dispatch --dry-run --prompt "write a SQL migration"
#
#   Primary:   claude (score: 95)
#   Fallback:  codex  (score: 88)  ← if claude unavailable
#   Fallback:  openai (score: 88)  ← if codex unavailable
#   Local:     ollama/qwen3:8b     ← always available

# Force local — no API calls regardless of policy
hydra dispatch --local --prompt "write unit tests for this function"
```

### 🐝 Swarm Dispatch

Fan a single prompt out to multiple heads at once, then keep the best answer:

```bash
hydra dispatch --swarm --swarm-mode race "prompt"   # first success wins (latency)
hydra dispatch --swarm --swarm-mode best "prompt"   # LLM judge picks the best answer
hydra dispatch --swarm --swarm-mode all  "prompt"   # every answer, ranked by CapScore
```

`--swarm-max-heads`, `--swarm-max-cost` (pre-flight cost guard), and `--swarm-heads id1,id2` give you fine control over fan-out.

### 🧭 Confidence Routing (Trust Control Plane)

Most routers optimize *cost*. Hydra is growing a second axis: **verified correctness**. Instead of always firing a fixed number of models, `--confidence` runs a **sequential probability ratio test (SPRT)** — it samples models adaptively, in most-diagnostic-per-dollar order, and stops the moment the calibrated log-odds cross the target confidence.

```bash
hydra dispatch --confidence 0.95 --prompt "is this migration safe to run in prod?"
```

It leans on **per-source calibration** you build from real outcomes — each model/verifier earns a measured sensitivity, specificity, and *diagnostic power* `D`. A coin-flip source (`D≈0`) contributes nothing; a proven one lets a single vote go a long way.

```bash
hydra trust record --source model:claude-sonnet --domain go --said-correct --outcome correct
hydra trust calibration          # per-source se / sp / D table
hydra trust defect --pii --production   # modeled $ cost of shipping a wrong answer
hydra trust stats                # samples saved vs fixed-N, achieved vs target confidence
hydra trust explain <task_hash>  # the full LLR ledger for a past run — why it stopped
```

**Blast-radius aware.** Point Hydra at a dependency graph (`graph.json` from [Graphify](https://github.com/safishamsi/graphify) or any tree-sitter indexer) and the confidence bar scales with how much code an edit could break — a fix to a hub everything imports demands far more certainty than a leaf helper:

```bash
hydra graph blast internal/auth/token.go        # 3 transitive dependents → demands 96.7%
hydra dispatch --confidence 0.90 --file internal/auth/token.go "rotate the signing key"
#   graph: blast radius 3.00 → demands confidence ≥ 96.7%  (raises the 0.90 floor)
```

In synthetic benchmarks this cuts model calls **~49% on easy tasks** and **~24% on a blended workload** at ≥98% accuracy — while deliberately sampling *more* than a fixed swarm on genuinely hard tasks, which a fixed-N ensemble cannot do. Calibration is cold-start conservative: with no history, sources are treated as uninformative and Hydra falls back to sampling broadly.

> The SPRT ensemble, calibration engine, and defect-cost model have shipped. Graph-aware (blast-radius) routing, a local MCP accountability ledger, and a pluggable verification-oracle interface are on the [roadmap](#roadmap).

---

## Comparison

| | Hydra | Manual routing | LiteLLM | RouteLLM |
|---|:---:|:---:|:---:|:---:|
| Auto-discovers tools on your machine | ✅ | ❌ | ❌ | ❌ |
| Hardware-aware local model selection | ✅ | ❌ | ❌ | ❌ |
| PII detection + local enforcement | ✅ | ❌ | ❌ | ❌ |
| Fallback chains | ✅ | ❌ | ✅ | ✅ |
| Per-dispatch cost logging (est. vs actual labeled) | ✅ | ❌ | ✅ | ❌ |
| Works with CLI tools (not just APIs) | ✅ | ✅ | ❌ | ❌ |
| Zero config to start | ✅ | ✅ | ❌ | ❌ |
| Swarm dispatch (race / best / all) | ✅ | ❌ | ❌ | ❌ |
| Route to a **confidence of correctness** (SPRT) | ✅ | ❌ | ❌ | ❌ |
| Per-source calibration (sensitivity / specificity / D) | ✅ | ❌ | ❌ | ❌ |
| MCP accountability ledger *(roadmap)* | 🔨 | ❌ | ❌ | ❌ |
| Central security agent *(roadmap)* | 🔨 | ❌ | ❌ | ❌ |

---

## Getting Started

### Install

**Homebrew (recommended):**
```bash
brew install ankit373/hydra/hydra
```

**Standalone installer:**
```bash
curl -fsSL https://raw.githubusercontent.com/ankit373/hydra/main/install.sh | sh
```

**From source:**
```bash
git clone https://github.com/ankit373/hydra.git
cd hydra
go build -o hydra ./cmd/hydra && mv hydra /usr/local/bin/
```

### First Run

```bash
hydra init
```

The wizard scans your machine, ranks every model it finds, walks you through picking a Cortex (your main model), lets you choose a local Ollama model calibrated to your actual hardware, and asks whether you work with sensitive data that should never leave your machine. Takes about 60 seconds.

### Core Commands

```bash
# Discovery & state
hydra init                              # first-run wizard
hydra probe                             # scan and display all available models
hydra status                            # live system state (heads, budget bars)

# Dispatch
hydra dispatch --prompt "..."           # route a prompt to the best model
hydra dispatch --dry-run --prompt "..." # preview routing without executing
hydra dispatch --local --prompt "..."   # local models only, no API calls
hydra dispatch --swarm --swarm-mode best "..."   # fan out to many heads, judge best
hydra dispatch --confidence 0.95 "..."  # SPRT: sample until this P(correct) is reached

# Cost & pricing
hydra cost                              # spend summary (est. vs actual labeled)
hydra stats                             # rollup by model / tier / day
hydra pricing list                      # live $/1M-token rates (OpenRouter + fallback)

# Trust Control Plane
hydra trust calibration                 # per-source sensitivity / specificity / D
hydra trust record ...                  # feed an outcome to train calibration
hydra trust defect ...                  # modeled cost of shipping a wrong answer
hydra trust stats                       # samples saved, achieved vs target confidence
hydra trust explain <task_hash>         # the LLR ledger for a past SPRT run
hydra graph blast <file>                # a file's blast radius + the confidence it demands
hydra graph parallel <files...>         # optimal number of parallel agents (Law 4)
hydra context entropy <file|->          # signal density + useful tokens + compact hint

# Accountability
hydra mcp check <tool> --agent A --resource R --action write  # gate + record an access
hydra mcp log --denied                  # what got blocked
hydra mcp report                        # allowed/denied by agent and tool

# Editing & batch
hydra edit --file ... --prompt "..."    # scoped, validated, rollback-safe file edit
hydra review ...                        # code review / approve / reject / QA
hydra parallel ...                      # fan independent tasks across heads
```

---

## Configuration

`~/.hydra/config.toml` — written by `hydra init`, editable by hand:

```toml
cortex = "claude"

[[tiers]]
name    = "expert"
heads   = ["claude", "codex"]

[[tiers]]
name    = "simple"
heads   = ["ollama/qwen3:8b"]

[[tiers]]
name    = "local"
heads   = ["ollama/phi4-mini"]

[policies.pii]
action  = "local-only"
```

---

## Adding a Model

Models are defined in [`internal/capabilities/data.json`](internal/capabilities/data.json). Adding support for a new model is one JSON entry — no Go code required:

```json
{
  "id": "your-model-id",
  "name": "Your Model Name",
  "provider": "your-provider",
  "capScore": 82
}
```

For Ollama models, add a family pattern:

```json
{
  "id": "family-prefix",
  "name": "Model Family Name",
  "provider": "ollama",
  "capScore": 70,
  "ollamaFamily": true
}
```

---

## Roadmap

| Feature | Status |
|---------|--------|
| Auto-discovery: CLI, API keys, local ports | ✅ Shipped |
| Hardware-aware Ollama model selection | ✅ Shipped |
| PII detection + local-only enforcement | ✅ Shipped |
| Tier routing with automatic fallback chains | ✅ Shipped |
| First-run TUI wizard (`hydra init`) | ✅ Shipped |
| `hydra cost` / `hydra stats` — spend breakdown, est. vs actual | ✅ Shipped |
| Dynamic live pricing (OpenRouter + 24h cache) | ✅ Shipped |
| Swarm dispatch — race / best / all + LLM judge | ✅ Shipped |
| **Per-source calibration** (Beta-Bernoulli → LLR / D) | ✅ Shipped |
| **Defect-cost model** (`hydra trust defect`) | ✅ Shipped |
| **SPRT confidence routing** (`hydra dispatch --confidence`) | ✅ Shipped |
| **Graph-aware routing** — blast-radius → defect cost → confidence | ✅ Shipped |
| **Optimal parallelism** — `n* = √((1−s)/k)` from graph coupling (Law 4) | ✅ Shipped |
| **Causal A2A handoffs** — vector clocks, concurrent-edit conflict detection | ✅ Shipped |
| **Context-entropy governor** — compact on falling signal density, not length | ✅ Shipped |
| **MCP accountability ledger** — record + gate what every agent touches | ✅ Shipped |
| Outcome auto-wiring (tests / review / revert → calibration) | 🔨 Building |
| Pluggable verification-oracle interface | 📋 Planned |
| MCP server registry + central security agent | 📋 [#9](https://github.com/ankit373/hydra/issues/9)/[#10](https://github.com/ankit373/hydra/issues/10) |
| Web UI + real-time cost dashboard | 📋 [#11](https://github.com/ankit373/hydra/issues/11)/[#12](https://github.com/ankit373/hydra/issues/12) |

See the full [Hydra Roadmap project board](https://github.com/users/ankit373/projects/2).

### The arc: from cost router to Trust Control Plane

Hydra started by answering *which model is cheapest for this task?* It's evolving to answer a harder question: ***how sure are we the answer is right, and what's the least attention we can spend to be that sure?***

- **Today** — calibration measures each source's real diagnostic power; SPRT samples adaptively to a target confidence; the defect-cost model prices what a wrong answer costs; graph-aware routing reads a code dependency graph so an edit's **blast radius** raises the confidence bar where a mistake is expensive; and a local **accountability ledger** records — and can gate — what every agent was allowed to touch and did.
- **Next** — a verification-oracle interface lets test-runners and compilers act as first-class, high-`D` evidence sources; outcomes (tests/review/revert) auto-train calibration.

The design principle throughout: **no single vendor is privileged** — Hydra routes *across* providers and *away* from expensive ones, optimizing verified correctness per unit of human attention.

---

## Project Structure

```
hydra/
├── cmd/hydra/                   # CLI entry point (Cobra) — every subcommand
├── internal/
│   ├── capabilities/            # Capability scoring — data.json, no hardcoded logic
│   ├── config/                  # TOML config at ~/.hydra/config.toml
│   ├── dispatch/                # Tier routing + fallback chains + policy + cost logging
│   ├── executor/                # Native executors: agy · Ollama · per-provider HTTP · CLI
│   ├── provider/                # Discovery providers (self-register via init())
│   │   ├── cli/                 # PATH scanner (13+ tools)
│   │   ├── env/                 # API key scanner (14 providers)
│   │   ├── port/                # Local server scanner (Ollama, LM Studio)
│   │   └── agy/                 # Antigravity tier registry
│   ├── probe/                   # Concurrent multi-provider discovery
│   ├── policy/                  # PII detection + local-only enforcement
│   ├── swarm/                   # Fan-out dispatch: race / best (LLM judge) / all + SPRT adapter
│   ├── trust/                   # Trust Control Plane: calibration · defect-cost · SPRT ensemble
│   ├── pricing/                 # Live cost DB (OpenRouter fetch + 24h cache + YAML fallback)
│   ├── cost/                    # cost.jsonl reader + spend summaries + source labeling
│   ├── budget/                  # Per-model token-budget governor (6 pressure modes)
│   ├── rank/                    # Deduplication + CapScore ranking
│   ├── editor/                  # Scoped, validated, rollback-safe file edits
│   ├── parallel/                # Independent multi-task fan-out
│   ├── review/                  # Code review / approve / reject / QA
│   ├── util/                    # Shared utilities (bounded Accumulator, 33 MB cap)
│   ├── sysinfo/                 # Hardware detection + 7-day memory history
│   ├── build/ · update/         # Version stamping + startup update check
│   └── tui/                     # Bubbletea init wizard + install guide
├── registry/                    # routing.yaml (the enum) · models · domains · pricing · policy
├── docs/                        # GitHub Pages (hydra.uvansa.com) — index.html, llms.txt
└── Formula/hydra.rb             # Homebrew formula (auto-updated by GoReleaser)
```

---

## Contributing

Issues and pull requests are welcome. A few entry points:

- **Add a model** — edit [`internal/capabilities/data.json`](internal/capabilities/data.json). One JSON entry. No Go required.
- **Add a discovery provider** — implement the [`Provider` interface](internal/provider/provider.go) and call `provider.Register()` in an `init()` function.
- **Add an executor** — add a row to `cliTemplates` in [`internal/executor/cli.go`](internal/executor/cli.go) or extend the HTTP executor for a new API schema.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full guide.

---

## License

MIT — see [LICENSE](LICENSE).

---

<div align="center">

Built with Go · Bubbletea · Cobra · Lipgloss

[hydra.uvansa.com](https://hydra.uvansa.com)

</div>
