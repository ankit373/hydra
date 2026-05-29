# Hydra — Multi-Model AI Orchestration System

Hydra is a robust, multi-model AI routing and orchestration system designed to run as a hierarchy of cooperative AI agents. In this setup, the Claude Code CLI functions as the **Brain** (Orchestrator / Tier 1), while cheaper, faster, or specialized models (served via the Antigravity CLI and local Ollama) act as **Heads** (Tiers 2-10) to execute specific subtasks.

By delegating tasks based on complexity, Hydra optimizes API costs, preserves Claude's context window, and accelerates developer velocity.

---

## Architecture & Directory Layout

The project is structured into three main layers: **Dispatchers** (execution binaries/scripts), **Registries** (YAML configuration files defining system policy, routing, and workspaces), and the **TUI** (an Ink/React terminal dashboard interface).

```
hydra/
├── README.md               # Main repository documentation
├── AGENTS.md               # Antigravity agent instructions & execution rules
├── CLAUDE.md               # Claude Code orchestrator instructions & reference
├── hydra-ui                # Entry script to launch the React TUI
├── dispatch/               # Core routing, execution, and utility scripts
├── registry/               # Policy, pricing, routing, and workspace configuration files
├── skills/                 # System prompts for specific agent capabilities
├── context/                # Project context files injected into agent environments
├── scripts/                # Housekeeping and maintenance scripts
└── ui/                     # Ink-based React terminal application source code
```

---

## File-by-File Reference

### Root Directory

*   **[AGENTS.md](AGENTS.md)**: Rules and guidelines for delegated Antigravity heads. Standardizes output formats, error propagation, and the protocol for escalating tasks back to the orchestrator.
*   **[CLAUDE.md](CLAUDE.md)**: Master guidelines for Claude Code (the Brain). Contains the classification flow, token preservation limits (with warnings at 75% and emergency thresholds at 80%), rubber duck review protocols, and CLI usage commands.
*   **[hydra-ui](hydra-ui)**: A startup wrapper shell script that boots the interactive Ink React Terminal User Interface (TUI) via Bun.

---

### Dispatch Layer (`dispatch/`)

This directory houses the execution primitives that form the backbone of Hydra's routing and manipulation capabilities.

*   **[dispatch/hydra.sh](dispatch/hydra.sh)**: The primary CLI entry point for developers. Provides quick subcommands for checking system status (`hydra status`), displaying active model tiers (`hydra list`), executing prompts (`hydra do`), running rubber-duck reviews (`hydra duck`), and managing token state.
*   **[dispatch/route.sh](dispatch/route.sh)**: The core Hydra router. Resolves routing keys (e.g., `SIMPLE`, `COMPLEX`) to numeric model tiers, manages cascading fallback lists on tool/API failure, skips exhausted API quotas, and monitors state for Claude token protection.
*   **[dispatch/agy.sh](dispatch/agy.sh)**: A wrapper script around `agy --print` (Antigravity). Dynamically swaps target models in `~/.gemini/antigravity-cli/settings.json`, detects when browser authentication is required, and runs a token-estimation sidecar to write token stats on exit.
*   **[dispatch/ollama.sh](dispatch/ollama.sh)**: Executes prompts using local models running on Ollama (used for Tier 10 `GRUNT` work). Automatically checks server health, starts the Ollama daemon if inactive, and parses JSON output to capture exact token usage.
*   **[dispatch/scope.sh](dispatch/scope.sh)**: A workspace validation engine. Ensures file modification requests target absolute paths within defined workspace roots and conform to configured path glob boundaries (`allowed_globs` and `denied_globs`).
*   **[dispatch/decide.sh](dispatch/decide.sh)**: Evaluates task metadata (file size, task type, extension) against rules in `registry/policy.yaml` to dynamically build execution instructions for downstream editing scripts.
*   **[dispatch/edit.sh](dispatch/edit.sh)**: An atomic file writer. Backs up target files, requests the model edit, extracts changed code, performs code block application, and runs a validation check. If validation fails, it automatically rolls back changes.
*   **[dispatch/review.sh](dispatch/review.sh)**: A git-and-backup validation harness. Allows the orchestrator to list modified files, view diffs, approve changes, or reject and roll them back safely. Can also dispatch a diff to a QA model tier for critique.
*   **[dispatch/parallel.sh](dispatch/parallel.sh)**: Fans out multiple independent tasks (either text prompts or file edits) concurrently using background jobs, gathering execution states into a unified JSON array.
*   **[dispatch/company.sh](dispatch/company.sh)**: Coordinates multi-stage playbook execution. Resolves workspace contexts, maintains stage states (todo, in-progress, completed, blocked), posts comments to GitHub Issues or Jira tickets, and updates a markdown progress ledger (`HYDRA.md`).
*   **[dispatch/repo-map.sh](dispatch/repo-map.sh)**: Scans workspace directories and builds a compressed representation of code signatures and declarations (classes, functions, interfaces) to supply context to model prompts without exceeding token limits.
*   **[dispatch/STRATEGY_RESEARCH.md](dispatch/STRATEGY_RESEARCH.md)**: Research notes detailing strategies from industry tools (Aider, Composer, Cursor) mapping their application to Hydra's architectural goals.
*   **[dispatch/PHASE2_NOTES.md](dispatch/PHASE2_NOTES.md)**: Planning and status document detailing Phase 1 delivery items and Phase 2 items such as search-replace diff blocks, unified diff strategies, and automatic git commits.
*   **[dispatch/cost.sh](dispatch/cost.sh)**: Parses `logs/cost.jsonl` to aggregate and render financial tables grouping cost by tier, token count, task_id, run_id, or day.

---

### Configuration & Registry Layer (`registry/`)

YAML configuration files that govern routing, access control, cost estimation, and model setups.

*   **[registry/routing.yaml](registry/routing.yaml)**: Defines the central complexity enum (`GRUNT` to `CORE`) mapping to specific tier numbers. Configures fallback routing arrays and defines cross-model review mappings.
*   **[registry/models.yaml](registry/models.yaml)**: Configures all available models. Defines token quotas, pricing profiles, and groups models into token pools (e.g., `agy_flash`, `agy_gemini_pro`, `agy_claude`) to track and limit concurrent usage.
*   **[registry/domains.yaml](registry/domains.yaml)**: Maps developer task domains (e.g., `auth`, `api`, `data`) to complexity enum keys. This is the first file the orchestrator reads during task classification to determine routing.
*   **[registry/workspace.yaml](registry/workspace.yaml)**: Sets directory scopes, file edit exclusion patterns (e.g., ignoring `.env` files and `node_modules`), and maps file extensions to validation syntax check commands.
*   **[registry/policy.yaml](registry/policy.yaml)**: Implements condition checks for routing tasks. Dictates when to apply search-replace diff block formats versus full file rewrites.
*   **[registry/playbooks.yaml](registry/playbooks.yaml)**: Predefined sequences of stages used by `company.sh` to automate complex developer workflows (e.g., refactoring paths, code quality sweeps).
*   **[registry/pricing.yaml](registry/pricing.yaml)**: Defines pricing multipliers used by routing scripts to estimate runtime API costs.
*   **[registry/workforce.yaml](registry/workforce.yaml)**: Configures agent profiles and workspace roles.

---

### Ink React TUI (`ui/`)

A React-based Terminal User Interface built with Ink that acts as a real-time command dashboard for monitoring Hydra's system state.

*   **[ui/package.json](ui/package.json)**: Declares TUI package specifications and scripts (`dev`, `start`), using Bun for runtime execution and Ink for console UI rendering.
*   **[ui/tsconfig.json](ui/tsconfig.json)**: TypeScript configuration defining compilation parameters.
*   **[ui/src/index.tsx](ui/src/index.tsx)**: Entry point that initializes React and mounts the main Ink TUI component in the shell.
*   **[ui/src/App.tsx](ui/src/App.tsx)**: Main TUI application component. Manages application state, handles keyboard listeners (using `Tab` to cycle model tiers and `Esc` to quit), and processes slash commands (`/status`, `/tiers`, `/set-pct`).
*   **[ui/src/state.ts](ui/src/state.ts)**: Interacts with shell utilities. Spawns `route.sh` as a child process and reads configuration logs to update the UI with token percent usage and cost estimations.
*   **[ui/src/types.ts](ui/src/types.ts)**: Contains TypeScript type definitions, mappings from routing keys to tier indices, and tier metadata.
*   **[ui/src/components/StatusPanel.tsx](ui/src/components/StatusPanel.tsx)**: Displays the top header in the TUI, showing current Claude context token percentage, active workspace paths, and available API token pools.
*   **[ui/src/components/ChatView.tsx](ui/src/components/ChatView.tsx)**: Renders a scrollable console representation of the chat history, highlighting outputs by model role (system, user, assistant, error).
*   **[ui/src/components/InputBar.tsx](ui/src/components/InputBar.tsx)**: Renders the shell input bar at the bottom of the interface, showcasing the currently selected model tier.

---

### Skills & Context

*   **[skills/delegate.md](skills/delegate.md)**: System instruction prompt directing models on how to divide a large chore into isolated subtasks, formatting them as a JSON list for `parallel.sh`.
*   **[skills/escalate.md](skills/escalate.md)**: Guidance defining when a model should fail-fast and escalate a task to a higher tier due to design ambiguity or cross-file dependencies.
*   **[skills/rubber-duck.md](skills/rubber-duck.md)**: Review instructions for model-based critique. Focuses on detecting tradeoffs, identifying edge cases, and verifying syntax correctness.
*   **[scripts/maintenance.sh](scripts/maintenance.sh)**: A simple cleanup tool that resets state files, wipes execution logs, and rotates temporary backup copies.

---

## Model Tier Map

Tasks are classified and dispatched to one of the following ten tiers:

| Tier | Enum Key | Target Model | Executor | Role |
| :--- | :--- | :--- | :--- | :--- |
| **1** | `CORE` | Claude Code | Direct CLI | Brain (Orchestrator, does not route) |
| **2** | `EXPERT` | Claude Opus 4.6 (Thinking) | Antigravity | Architecture decisions, systems design |
| **3** | `VERY_HARD` | Claude Sonnet 4.6 (Thinking) | Antigravity | Complex refactors, multi-step logic |
| **4** | `HARD` | GPT-OSS 120B (Medium) | Antigravity | Tradeoff reviews, Rubber Duck evaluations |
| **5** | `COMPLEX` | Gemini 3.1 Pro (High) | Antigravity | Middleware, security, APIs |
| **6** | `MODERATE` | Gemini 3.1 Pro (Low) | Antigravity | Service components, validation code |
| **7** | `STANDARD` | Gemini 3.5 Flash (High) | Antigravity | Controllers, basic CRUD, handlers |
| **8** | `SIMPLE` | Gemini 3.5 Flash (Medium) | Antigravity | DTOs, interfaces, database schemas |
| **9** | `TRIVIAL` | Gemini 3.5 Flash (Low) | Antigravity | Constants, configurations, helper updates |
| **10** | `GRUNT` | Qwen 2.5 Coder 7B | Ollama | Local boilerplate, mock data, simple scripts |

---

## Getting Started

### Installation

Ensure you have [Bun](https://bun.sh) and the [Antigravity CLI](https://antigravity.google) installed.

1. Clone the repository and navigate to its root:
   ```bash
   git clone https://github.com/ankit373/hydra.git
   cd hydra
   ```

2. Install the TUI dependencies:
   ```bash
   cd ui
   bun install
   cd ..
   ```

3. Launch the interactive TUI interface:
   ```bash
   ./hydra-ui
   ```
