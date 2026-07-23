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

```bash
brew install --HEAD Formula/hydra.rb && hydra init
```

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
  ┌───────────────────────────────────────────────────────────────────┐
  │                         hydra dispatch                            │
  └───────────────────────────────┬───────────────────────────────────┘
                                  │
                    ┌─────────────▼──────────────┐
                    │          Hydra              │
                    │                             │
                    │  ┌─────────────────────┐    │
                    │  │   Policy Engine     │    │
                    │  │  PII detection      │    │
                    │  │  cost ceiling       │    │
                    │  │  local-only routing │    │
                    │  └──────────┬──────────┘    │
                    │             │               │
                    │  ┌──────────▼──────────┐    │
                    │  │    Tier Router      │    │
                    │  │  score → tier       │    │
                    │  │  fallback chains    │    │
                    │  └──────────┬──────────┘    │
                    └────────┬────┴────────────────┘
                             │
           ┌─────────────────┼─────────────────┐
           │                 │                 │
    ┌──────▼──────┐   ┌──────▼──────┐   ┌──────▼──────┐
    │  CLI Heads  │   │  API Heads  │   │ Local Heads │
    │             │   │             │   │             │
    │ Claude Code │   │   OpenAI    │   │   Ollama    │
    │    Codex    │   │  Anthropic  │   │  LM Studio  │
    │   Cursor    │   │    Groq     │   │             │
    │    Kiro     │   │  Together   │   │             │
    │  Windsurf   │   │   + more    │   │             │
    └─────────────┘   └─────────────┘   └─────────────┘
      score: 60–95      score: 70–95      score: 50–72
```

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

Every dispatch is logged to `~/.hydra/dispatch.jsonl` with model, tier, token counts, estimated cost, and fallback chain. Run `hydra stats` to see where your budget is going.

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

---

## Comparison

| | Hydra | Manual routing | LiteLLM | RouteLLM |
|---|:---:|:---:|:---:|:---:|
| Auto-discovers tools on your machine | ✅ | ❌ | ❌ | ❌ |
| Hardware-aware local model selection | ✅ | ❌ | ❌ | ❌ |
| PII detection + local enforcement | ✅ | ❌ | ❌ | ❌ |
| Fallback chains | ✅ | ❌ | ✅ | ✅ |
| Per-dispatch cost logging | ✅ | ❌ | ✅ | ❌ |
| Works with CLI tools (not just APIs) | ✅ | ✅ | ❌ | ❌ |
| Zero config to start | ✅ | ✅ | ❌ | ❌ |
| MCP server registry *(v2)* | 🔨 | ❌ | ❌ | ❌ |
| Central security agent *(v2)* | 🔨 | ❌ | ❌ | ❌ |

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
hydra init                              # first-run wizard
hydra probe                             # scan and display all available models
hydra status                            # live system state
hydra dispatch --prompt "..."           # route a prompt to the best model
hydra dispatch --dry-run --prompt "..." # preview routing without executing
hydra dispatch --local --prompt "..."   # local models only, no API calls
hydra cost                              # cost summary by model and day
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

| Feature | Release | Status |
|---------|---------|--------|
| Auto-discovery: CLI, API keys, local ports | v1.0 | ✅ Shipped |
| Hardware-aware Ollama model selection | v1.0 | ✅ Shipped |
| PII detection + local-only enforcement | v1.0 | ✅ Shipped |
| Tier routing with automatic fallback chains | v1.0 | ✅ Shipped |
| First-run TUI wizard (`hydra init`) | v1.0 | ✅ Shipped |
| `hydra cost` — cost breakdown by model and day | v1.1 | ✅ Shipped |
| Ollama model deduplication (binary vs models) | v1.1 | 🔨 Building |
| MCP Server Registry | v2.0 | 📋 [#9](https://github.com/ankit373/hydra/issues/9) |
| Per-model MCP access controls | v2.0 | 📋 [#10](https://github.com/ankit373/hydra/issues/10) |
| Central security agent | v2.0 | 📋 [#10](https://github.com/ankit373/hydra/issues/10) |
| Real-time cost dashboard | v2.0 | 📋 [#11](https://github.com/ankit373/hydra/issues/11) |
| Swarm dispatch — parallel multi-model tasks | v2.0 | 📋 [#11](https://github.com/ankit373/hydra/issues/11) |
| Web UI | v3.0 | 📋 [#12](https://github.com/ankit373/hydra/issues/12) |
| Compliance reporting (SOC 2, GDPR) | v3.0 | 📋 Planned |

See the full [Hydra Roadmap project board](https://github.com/users/ankit373/projects/2).

### v2: Beyond Routing — The Full Control Plane

The current layer answers: *which model handles this task?*

v2 extends the control plane to answer: *what is each model allowed to touch?*

**MCP Server Registry** — a central inventory of every MCP server connected to your AI tools. Today there is no way to know which model is talking to which server, what operations it has been authorized, or what it has actually called. Hydra v2 changes that: one place to see, grant, and revoke access across every model.

**Central Security Agent** — a policy enforcement point between your models and your MCP servers. Even if a model has filesystem or GitHub MCP access configured, the security agent can block writes, deletes, or network calls based on centrally-defined rules. Deny rules apply regardless of what the individual model thinks it is authorized to do.

**Cost Dashboard** — real-time spend tracking with per-model and per-task-type breakdowns, budget alerts, and a local vs cloud ratio so you know exactly how much you are saving by running locally.

---

## Project Structure

```
hydra/
├── cmd/hydra/                   # CLI entry point (Cobra)
├── internal/
│   ├── capabilities/            # Capability scoring — data.json, no hardcoded logic
│   ├── config/                  # TOML config at ~/.hydra/config.toml
│   ├── dispatch/                # Tier routing + fallback chains + cost logging
│   ├── executor/                # CLI and HTTP executors for every head type
│   ├── policy/                  # PII detection + routing enforcement
│   ├── probe/                   # Concurrent multi-provider discovery
│   ├── provider/                # CLI, env, and port discovery providers
│   │   ├── cli/                 # PATH scanner (13+ tools)
│   │   ├── env/                 # API key scanner (14 providers)
│   │   └── port/                # Local server scanner (Ollama, LM Studio)
│   ├── rank/                    # Deduplication and capability ranking
│   ├── sysinfo/                 # Hardware detection + 7-day memory history
│   └── tui/                     # Bubbletea init wizard + install guide
├── dispatch/                    # Shell dispatch layer (agy + Ollama wrappers)
├── registry/                    # Routing YAML, model definitions, policy config
├── skills/                      # Skill prompts (delegate, escalate, rubber-duck)
├── docs/                        # GitHub Pages (hydra.uvansa.com)
└── Formula/hydra.rb             # Homebrew formula (auto-updated by goreleaser)
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
