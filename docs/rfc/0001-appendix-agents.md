# RFC 0001, Appendix A: Agents and multi-step workflows

- **Status:** Draft
- **Date:** 2026-10-29
- **Parent:** [RFC 0001: Hydra as a verified cascade gateway](0001-verified-cascade-gateway.md), §5.7
- **Scope:** how Hydra routes, verifies, bounds and learns from *trajectories* (a sequence of model calls that together do one task), not only independent calls.

The parent RFC treats every request as independent: a task class, one routing decision, one verified answer. An agent is a loop of 10 to 500 such calls. Each call depends on the previous ones, and the outcome that matters, whether the task got done, is known only at the end. This appendix covers four things:

1. what the code already does for agent traffic (§A1);
2. what the field knows, with sources (§A2);
3. a design that extends the parent RFC's mechanisms to trajectories without inventing a second router (§A3–§A9);
4. how to tell whether it works (§A10), and what can go wrong (§A11).

Conventions: *step* = one model call inside a session; *session* = the trajectory one client-side agent run produces; *incumbent* = the model currently serving a session. Claims marked **[vendor]** come only from the vendor that sells the thing. Claims marked **[unverified]** could not be confirmed from a primary source while writing.

---

## A1. Current state: what the code does today

Audited on branch `docs/rfc-0001-verified-cascade-gateway` (HEAD `ee1aef4`). Each line says what exists, whether it is general or coding-only, and what an agent needs that it lacks.

### A1.1 Package by package

| Package | What it actually does | General or coding-only | Gap for general agents |
|---|---|---|---|
| `internal/serve` | `POST /v1/chat/completions` and `GET /v1/models` only. Tools, `tool_choice` and `tool_calls` pass through. Tool calls are framed whole on the stream (`toolFrames`). `X-Request-Id` is minted per request and becomes the run id (#1174). | General | No `/v1/messages`, no `/v1/responses`, no session. `executor.Message.Content` is a `string`, so an OpenAI request whose `content` is an **array of parts** (images, `input_text`, or LangChain/Agents SDK multi-part messages) fails JSON decoding with 400. There is no field for reasoning or thinking items or for `cache_control`. |
| `cmd/hydra/serve.go` `serveRouter` | Each HTTP request becomes one `Dispatch` with its own `taskID`. `flattenConversation` joins `role:\ncontent` for the classifier, egress and the ledger. | General | **Every turn of an agent loop is routed from scratch.** Nothing keeps the model fixed across turns. Under `explore_rate > 0`, or after a fallback, consecutive turns of one loop can land on different providers. `flattenConversation` skips messages with empty `Content`, so an assistant turn that only made tool calls is invisible to the classifier, egress and the cache key. |
| `internal/executor` (tools) | Every chat dialect (OpenAI-compatible, Anthropic, Gemini, Bedrock Converse, Cohere) carries `Messages` and `Tools`, buffered and streamed. The tool-arg fragments are folded by index (#958), and `CheckAskable` refuses unparseable arguments before any head runs. `RecoverToolCalls` reads a tool call that a local model wrote as plain text (#1148). | General | Text recovery runs only on the **buffered** OpenAI-compatible path (`http.go:172`). The streamed path does not recover, so the #1147 failure persists for `stream: true` against Qwen2.5-Coder-class local models. Anthropic `thinking_delta` is dropped from the output (correct), but the thinking **block and signature are not preserved** for the next turn, so the extended-thinking-plus-tools loop that Anthropic requires to echo those blocks cannot run through Hydra. |
| `internal/dispatch` | `CanUseTools` filters heads that cannot transmit tools (`dispatch.go:766`). Signals `conversation.turns` (count of user messages) and `conversation.tool_loop` (true if *any* tool message or tool call is in history) are computed in `conversationShape` (`rules.go:95`). | General | `conversation.tool_loop` is **not loop detection**. It means "this conversation has used a tool", which is true for every step after the first. Nothing detects repetition, cycles or a lack of progress. The fallback chain moves to the next head on failure, and in a session that is an unplanned mid-trajectory model switch. |
| `internal/dispatch/cache.go` | The answer cache is opt-in, keyed on the flattened prompt. | General | **Latent agent defect.** `admission` does not exclude tool-bearing requests, and the key contains neither the tool definitions nor assistant tool-call turns. With the cache on, a text answer stored from one loop can be served to another loop with different tools. `cacheResult` also returns no `ToolCalls`. The defect is masked only because the parent RFC disables the cache by default (#1154–#1156). |
| `internal/workflow` | A **linear** list of steps run in order. Each step is routed with its own `Enum`. Prior outputs are fenced into the next prompt (`WrapUntrusted`). State is saved before and after each step, a heartbeat file marks liveness and gives single-runner exclusion (#898, #1162), and per-step cost and wall caps exist (#1175). | Mostly general: prompts are free text, though the CLI examples are coding | No DAG, no branching, no retries with backoff, no timers or signals, no human-in-the-loop step, no tool execution (`Step.Tools` is declared and **read by nothing**). Steps exchange text, not structured outputs. Persistence is one JSON file per workflow under `~/.hydra/logs/workflows`, capped at 200. |
| `internal/swarm` | Fan-out `race` / `best` (LLM judge, calibrated through `calibrated_judge.go`) / `all`, plus an SPRT adapter into `trust.Run`. | General over **text answers** | The answer type is a string. There is no equivalence for tool calls, and nothing fans out a *step* of a loop or a whole *trajectory*. |
| `internal/trust` | Calibration per (source, domain), `ApplyRunOutcome(cal, domain, ledger []Evidence, actual)`, multi-hypothesis SPRT. | General | The unit is one run, meaning one answer. There is no trajectory, no step, and no delayed per-step label. |
| `internal/a2a` | A **local handoff file** (`last_handoff.json`) with vector clocks and `ConflictsWith` over file lists. | Coding-shaped: conflicts are defined over file paths | Despite the name, it is **not** Google's A2A protocol: there are no agent cards, tasks or JSON-RPC. Useful as causal ordering between Hydra-run agents, irrelevant to A2A interop. |
| `internal/ledger` | Append-only, hash-chained access log, glob `Policy.Decide(agent, tool, resource, action, classification)`, parameter-hash binding, `ask` verdicts. Consulted by dispatch to decide **which head may see a payload** (`CheckAndRecordDispatch`). | General | It is never applied to the **tool calls a model emits** through `serve`. `hyctl mcp check` exists as a CLI that a client-side hook may call. Hydra does not proxy MCP (`tools/call` appears nowhere in the tree). |
| `internal/pending` | A dispatch parked on `ask`, answered with `hyctl ask answer\|decline`. Resume re-approves only the stored head. | General | No API surface: an HTTP client cannot learn that a request is parked or poll for it. |
| `internal/runlog`, `internal/waterfall`, `internal/otlp` | v2 spans with parent ids, tokens, TTFT, `Meta`, scores appended after the fact, OTLP/JSON export with `gen_ai.*` where it maps. | General | A run is one request (#1174), so a 60-step loop is 60 unlinked runs. No session-id attribute, no `gen_ai.conversation.id`, no `execute_tool` spans: tool execution happens client-side and Hydra sees only its result. |
| `internal/sandbox` | Environment allowlist and rlimits for subprocesses **Hydra** spawns (CLI heads, oracles). | General | Not a tool sandbox. Hydra executes no client tools. |
| `internal/optimal` | `n* = √((1−s)/k)`, Amdahl with linear coordination cost, `s` defaulting to an asserted 0.2. Its own doc comment says the measured speedups were superlinear, which this model cannot fit. | Coding-shaped (file coupling `k` from `internal/graph`) | No measured `s` or `k` for any non-code class. It also answers the wrong question for test-time compute, where the issue is *how many samples*, not *how many parallel workers* (§A7). |
| `internal/graph` | Code dependency graph: blast radius, percolation. | Coding-only | n/a outside the coding pack. |
| `internal/mcpregistry` | Static supply-chain posture of *installed* MCP servers: OSV, typosquat, lifecycle automaton, backtests. | General (MCP) | Static. It never sees runtime tool traffic, so it cannot gate a call. |
| `internal/egress`, `internal/policy` | Provenance-typed parts, the secret→local rerouting, and `InjectionMarker`, a fixed phrase list over the prompt. | General | Tool results are not a declared `egress.Source`. The injection check is a substring list, with no measured recall against current attack suites (§A2.4). |

### A1.2 Issue tracker vs. code

The tracker overstates what is missing:

| Issue | Tracker | Code |
|---|---|---|
| #953 streamed request drops tools | OPEN | Fixed: `http_stream.go:96` sends `Tools`/`ToolChoice`, and fragments fold by index. Shipped in v1.5.0 (`ed35dee`). |
| #960 / #976 / #957 tools on Anthropic / Cohere / name folding | OPEN | Fixed (`8d2fd14`, `62a1093`, `836351a`), shipped in v1.5.0 |
| #1147 tool call written as text | OPEN | Fixed for the buffered path by #1148 on `develop`, **unreleased**. The streamed path is still open (above). |
| #1149 a session is one run | OPEN | The per-request half is fixed by #1174. **The session half is not addressed anywhere**, and this appendix is that design. |

Of the agent-loop bugs, only #1147-streaming is still a real defect. The closing sweep (`close-shipped-issues.yml`) apparently did not fire for v1.5.0. That is a process issue, not an agents issue.

### A1.3 Summary

- **Works:** single-turn tool calling over Chat Completions on every dialect, buffered and streamed; tool-capability filtering; per-request tracing; policy on which head sees a payload; a linear resumable step runner; text-answer ensembles.
- **Coding-only:** `graph`, `a2a` conflict semantics, `optimal`'s inputs, and the workflow CLI's framing.
- **Missing for general agents:**
  1. a session object;
  2. stickiness;
  3. step types;
  4. step verifiers;
  5. trajectory outcomes and credit assignment;
  6. Messages and Responses ingress, with lossless reasoning and thinking items, content-part arrays and cache-control passthrough;
  7. policy on emitted tool calls;
  8. treating tool results as an untrusted, typed input;
  9. session budgets;
  10. idempotent replay for durable engines.

---

## A2. Research findings

Sources were fetched in October 2026. Leaderboard numbers move monthly, so treat them as snapshots.

- **[vendor]** means self-reported by the party selling the thing.
- **[2°]** means only an aggregator or secondary source was found.
- Some doc fetches went through a summariser, so check exact field names against the linked page before implementing.

### A2.1 Runtimes and frameworks: where the model choice sits

Model choice sits at **agent (or node) granularity** almost everywhere. Per-step routing exists only as an add-on.

| Runtime | Loop / state | Model choice | Path to a gateway |
|---|---|---|---|
| OpenAI Agents SDK | `Runner` loop. Handoffs are tools named `transfer_to_<agent>`. Sessions are client-side (SQLite, Redis, SQLAlchemy) or server-side (`OpenAIConversationsSession`, `previous_response_id`, `conversation_id`). [docs](https://openai.github.io/openai-agents-python/sessions/) | per agent (`Agent.model`), per run (`RunConfig`), or a `MultiProvider` that routes by model-name prefix; `unknown_prefix_mode="model_id"` is documented for OpenAI-compatible gateways [docs](https://openai.github.io/openai-agents-python/models/) | `set_default_openai_client(AsyncOpenAI(base_url=…))`; `set_default_openai_api("chat_completions")` for backends without Responses. The docs warn that many providers 404 on Responses, that Responses-only fields are silently dropped on Chat Completions, and that tracing uploads to OpenAI by default. |
| Responses API | `previous_response_id` chains server-side, and every earlier turn is still billed as input. Conversation objects are durable. `store` keeps a response 30 days. Background mode polls and resumes with `starting_after`. [state](https://developers.openai.com/api/docs/guides/conversation-state), [background](https://developers.openai.com/api/docs/guides/background) | per request | base URL |
| Claude Agent SDK / Claude Code | Client-side JSONL sessions with resume and fork [docs](https://code.claude.com/docs/en/agent-sdk/sessions). Subagents nest 3 levels deep, up to 20 concurrent. | per subagent (`model:` frontmatter, `CLAUDE_CODE_SUBAGENT_MODEL`). The built-in Explore subagent **inherits** the main model; it does not default to Haiku [docs](https://code.claude.com/docs/en/sub-agents) | `ANTHROPIC_BASE_URL` to a Messages-format gateway. The gateway must forward `anthropic-version`/`anthropic-beta` verbatim and treat fields as open lists. Claude Code sends `x-claude-code-session-id`, `-agent-id` and `-parent-agent-id`, plus opt-in hint headers including `x-claude-code-request-class`. **Anthropic does not support routing Claude Code to non-Claude models through any gateway.** [gateway](https://code.claude.com/docs/en/llm-gateway), [protocol](https://code.claude.com/docs/en/llm-gateway-protocol) |
| Claude Code Agent Teams | Experimental: a lead plus teammates as separate instances, with a file-locked task list and JSON mailboxes on local disk [docs](https://code.claude.com/docs/en/agent-teams) | fixed per teammate at spawn | as above |
| Google ADK | `LlmAgent` per agent. The experimental `RoutedLlm` (TypeScript only) picks a model per request and fails over only before the first output token [routing](https://adk.dev/agents/models/routing/) | per agent | `LiteLlm` wrapper (OpenAI-compatible) [models](https://adk.dev/agents/models/) |
| LangGraph | Graph with checkpointers keyed by `thread_id`; durability modes `exit`/`async`/`sync`; interrupts persisted [docs](https://docs.langchain.com/oss/python/langgraph/durable-execution) | per node (user code) | any chat-model base URL |
| CrewAI | per agent `llm:`, LiteLLM fallback [docs](https://docs.crewai.com/en/concepts/llms) | per agent | `OPENAI_BASE_URL` |
| AutoGen → Microsoft Agent Framework | MAF 1.0 GA in April 2026 succeeds Semantic Kernel and AutoGen; AutoGen is in maintenance [MS](https://devblogs.microsoft.com/foundry/microsoft-agent-framework-reaches-release-candidate/), [VSM](https://visualstudiomagazine.com/articles/2026/04/06/microsoft-ships-production-ready-agent-framework-1-0-for-net-and-python.aspx). AG2 is the community fork [2°] | per agent | provider SDKs |
| Mastra | `provider/model` strings; custom endpoint `{id,url}`, Chat Completions by default or `api:"responses"`; fallback arrays [docs](https://mastra.ai/models) | per agent, dynamic | base URL |
| Pydantic AI | **pluggable durable execution**: Temporal, DBOS, Prefect, Restate. Model requests and wrapped tools are journalled and replayed on restart [docs](https://pydantic.dev/docs/ai/integrations/durable_execution/overview/) | per agent | base URL |
| smolagents | `CodeAgent` (actions as Python in a sandbox) or `ToolCallingAgent` [docs](https://huggingface.co/docs/smolagents/index) | per agent | `LiteLLMModel` |
| OpenHands SDK | `LLM` objects with `base_url`; built-in `MultimodalRouter`, a subclassable `Router` ("under active development") [docs](https://docs.openhands.dev/sdk/guides/llm-routing) | per LLM object, with a router hook | base URL |
| Goose | subagents and sub-recipes [docs](https://goose-docs.ai/docs/guides/subagents). Lead/worker multi-model mode was reportedly replaced by a planner model; the docs disagree [blog](https://goose-docs.ai/blog/2025/06/16/multi-model-in-goose) | per role | provider config |
| Codex CLI | subagents as TOML with per-agent `model` and reasoning effort. Subagents cannot override the parent's `model_provider`, so all share one endpoint [docs](https://learn.chatgpt.com/docs/agent-configuration/subagents) | per subagent | `model_providers` base URL, Responses wire |

**Durable execution for agents.**

- **Temporal**
  - The OpenAI Agents SDK integration runs **every model call as an Activity**, retried durably and not re-executed on replay. The default start-to-close timeout is 60 s [docs](https://docs.temporal.io/develop/python/integrations/openai-agents).
  - `activity_as_tool()` runs a tool as an Activity. A `@function_tool` runs inside the workflow and must be deterministic.
  - The changelog still showed the integration as Public Preview in April 2026 [changelog](https://temporal.io/change-log/product-area/python-sdk).
- **Restate**: an OpenAI Agents SDK integration (January 2026) journals LLM responses [docs](https://docs.restate.dev/ai/sdk-integrations/openai-agents-sdk).
- **Inngest**: `step.ai.infer()` / `step.ai.wrap()`, with no streaming in `infer` [docs](https://www.inngest.com/docs/features/inngest-functions/steps-workflows/step-ai-orchestration).
- **Cloudflare Agents SDK**: one Durable Object per agent with its own SQLite, alarms, WebSocket RPC and an MCP client [docs](https://developers.cloudflare.com/agents/concepts/agent-class/).
- **The implication common to all of them:** a durable engine turns each LLM call into a retried, idempotency-sensitive step. A gateway will see duplicate requests and must answer them idempotently (§A8.3).

### A2.2 Routing inside agent loops

**Agent-native routing evaluation now exists, and it changes the question.**

- **TwinRouterBench** ([arXiv 2605.18859](https://arxiv.org/abs/2605.18859), NeurIPS 2026)
  - **Setup.** The router sees only the prefix at an intermediate agent call.
  - **Static track.** 970 router-visible prefixes from 520 instances (SWE-bench, BFCL, mtRAG, QMSum, PinchBench). Each prefix carries an execution-verified target tier, found by downgrade-and-cascade. Scoring is deterministic: RowPass, TrajPass, CostSave. The grader **prices cache writes on a tier switch** (a high-tier write costs 12.5× a high-tier read).
  - **Static result.** A kNN router reaches 84.7% TrajPass with 56.2% saving, in-sample.
  - **Dynamic track (live SWE-bench Verified, 100 held-out instances).** A trained router resolved **75/100 for $25.66**, against **74/100 for $54.73** for unrouted Opus 4.6. That is one run, a quality difference inside noise, and roughly half the cost *after* switching costs.
  - **Negative result.** Opus 4.6 *used as the router* predicted "high" for only 7 of 147 steps that needed it, and every SWE trajectory routed that way failed. **Routers must learn from execution-verified labels, not from a frontier model's opinion.** Hydra's Beta posteriors from verified outcomes are the right kind of signal.
- **Agent-as-a-Router / ACRouter** ([arXiv 2606.22902](https://arxiv.org/abs/2606.22902)): routing as a Context→Action→Feedback loop with a verifier and memory of execution-grounded outcomes, on CodeRouterBench (~10k coding tasks, 8 LLMs). Adding per-task-dimension performance statistics to an LLM router gave a +15.3% relative gain, which the authors call an "information deficit". That is the same argument as the parent RFC's measured-capability routing.
- **SWE-Router** ([arXiv 2607.00053](https://arxiv.org/abs/2607.00053)): a cheap model explores for a few turns, then the router reads the partial trajectory and continues cheap or escalates. It proves that conditioning on the trajectory is never worse than on the prompt alone. The abstract gives no numbers.
- **AgentRouter** ([arXiv 2609.22951](https://arxiv.org/abs/2609.22951), workshop): a 12M-parameter per-step tier classifier, reporting 72% cost reduction at 97.3% quality. RouteLLM applied per step managed 31% and FrugalGPT 44%. Benchmarks are not named in the abstract, so this is weak evidence.
- **BoPO** ([arXiv 2602.21227](https://arxiv.org/abs/2602.21227)): small/large choice per step under a per-task budget, trained to avoid the policy that "saves cost by failing".
- **Single-turn routers:** RouterBench ([2403.12031](https://arxiv.org/abs/2403.12031)), RouteLLM ([2406.18665](https://arxiv.org/abs/2406.18665)), FrugalGPT ([2305.05176](https://arxiv.org/abs/2305.05176)), Arch-Router ([2506.16655](https://arxiv.org/abs/2506.16655)). TwinRouterBench and AgentRouter both report that single-turn training signals transfer poorly to agent steps.

**Switching cost is real and quantifiable.**

- **Prompt cache.**
  - Anthropic: reads at 0.1× base, or 0.05× on Opus/Sonnet 5.5; writes at 1.25× (5 min) or 2× (1 h). The prefix order is `tools → system → messages`, must be byte-identical, and changing tools, `tool_choice` or thinking settings invalidates downstream levels [docs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching).
  - OpenAI: cached reads at 0.1× on GPT-5.6+, writes at 1.25×/2.5×, and the docs note that different models use different weights [docs](https://developers.openai.com/api/docs/guides/prompt-caching).
  - A KV cache is model-specific by construction, so a switch is a full re-prefill at the new model's rate.
  - Observed cache hit rates for coding agents: Claude Code 92%, OpenCode 89%, Kilo Code 46% [vendor: Requesty](https://www.requesty.ai/data/coding-agent-cache-hit-rate-apr-2026).
- **Behavioural divergence. The Replay Gap** ([arXiv 2608.08239](https://arxiv.org/abs/2608.08239), CoLM 2026) forked about 900 live SWE-bench trajectories and continued each fork on a different model:
  - a swap rewrote 61–94% of subsequent actions;
  - 74–77% of early swaps diverged on the very next action, against 6–35% in same-model controls;
  - only 3% of replayed states stayed valid;
  - all 5 outcome flips happened in swap arms, none in 359 controls;
  - an offline replay evaluator mispredicted every outcome that mattered.

  **Per-step routing cannot be evaluated by splicing logged outputs.** It needs live forks.

**Does a heterogeneous mix beat one strong model end to end?** On cost at roughly equal quality, often yes. On quality alone, rarely, and by small margins.

| Evidence | Finding | Caveat |
|---|---|---|
| Aider architect/editor ([post](https://aider.chat/2024/09/26/architect.html)) | o1-preview architect + editor 85.0% vs 79.7% solo | code editing, not agentic |
| TwinRouterBench dynamic | 75 vs 74 at ~0.47× cost | one run, n = 100 |
| Anthropic multi-agent research ([post](https://www.anthropic.com/engineering/multi-agent-research-system)) | Opus lead + Sonnet subagents +90.2% over single Opus on an internal eval; about 15× chat tokens; token usage explains 80% of variance on BrowseComp | [vendor], unreplicated |
| Self-MoA ([2502.00674](https://arxiv.org/abs/2502.00674)) | samples of **one** top model beat mixed-model MoA by 6.6% (AlpacaEval 2.0) | evidence *against* naive mixing |
| AgentCARD ([2606.20629](https://arxiv.org/abs/2606.20629)) | heterogeneous planner/executor/verifier teams sit on the cost-accuracy Pareto frontier; match the strongest homogeneous team at up to 12× lower cost | abstract only |
| Efficient Agents ([2508.02694](https://arxiv.org/abs/2508.02694)) | GAIA cost-of-pass $0.398 → $0.228 at 96.7% of performance | one framework |

### A2.3 Verification and credit assignment over trajectories

- **Outcome vs. process supervision.**
  - Process reward beats outcome reward for best-of-N on MATH: 78.2% vs 72.4%, with majority vote at 69.6% ([Let's Verify Step by Step](https://arxiv.org/abs/2305.20050)).
  - Math-Shepherd builds process labels from Monte-Carlo rollouts with no human labelling ([2312.08935](https://arxiv.org/abs/2312.08935)).
- **Agent PRMs.**
  - AgentPRM (Choudhury) learns from MC rollouts in an actor-critic loop; 3B policies beat GPT-4o on ALFWorld ([2502.10325](https://arxiv.org/abs/2502.10325)).
  - AgentPRM (Xi et al.) scores "promise" and "progress" and notes that agent actions lack the clean correctness of math steps ([2511.08325](https://arxiv.org/abs/2511.08325)).
  - That is why §A6.1 starts with *deterministic* step checks and treats a learned step judge as an optional, calibrated source.
- **Judges on trajectories.**
  - Agent-as-a-Judge agrees with human consensus 86.6–92.1% of the time, against 68.9–71.9% for a plain LLM-as-a-judge; human majority is 94–95% ([2410.10934](https://arxiv.org/html/2410.10934)).
  - AgentRewardBench (1,302 expert-labelled web trajectories) found **no judge best across benchmarks**, and found that rule-based checkers under-report success ([2504.08942](https://arxiv.org/abs/2504.08942)).
  - Both argue for calibrating every verifier per task class, which is the parent RFC's design.
- **Test-time compute for agents.**
  - Best-of-N beat beam and tree search among parallel methods, with list-wise verification best for merging ([2506.12928](https://arxiv.org/abs/2506.12928)).
  - LATS (MCTS + reflection) ([2310.04406](https://arxiv.org/abs/2310.04406)).
  - SWE-Search (MCTS + value agent) gives a 23% relative gain on SWE-bench ([2410.20285](https://arxiv.org/abs/2410.20285)).
  - Self-consistency is the baseline every verifier must beat ([2203.11171](https://arxiv.org/abs/2203.11171)).
- **Credit assignment.**
  - GiGPO computes step-level advantages by grouping actions from the **same anchor state across trajectories**: +12% on ALFWorld and +9% on WebShop over GRPO ([2505.10978](https://arxiv.org/abs/2505.10978)). The anchor-state idea is the principled route to per-step model credit, but it needs many trajectories through equivalent states, which a gateway serving heterogeneous traffic rarely gets.
  - Hence §A6.5 attributes outcomes at the session level and labels steps only from their own one-step-delayed evidence.
- **Off-policy evaluation.**
  - IPS and doubly robust estimators ([Dudík et al.](https://arxiv.org/abs/1103.4601)) work for small discrete action sets with logged propensities.
  - Over long horizons, the product of per-decision importance ratios collapses the effective sample size, and routing logs are confounded, because hard prompts go to big models ([2605.01311](https://arxiv.org/abs/2605.01311), survey [2605.25998](https://arxiv.org/abs/2605.25998)).
  - Combined with the Replay Gap, **per-step routing policies cannot be evaluated from logs**. Session-level policies with one logged propensity can.
  - No paper was found applying DR-OPE to a logged-propensity LLM router.

### A2.4 Failure modes, with evidence

| Failure | Evidence | Design response |
|---|---|---|
| Error compounding | METR's 50%-horizon has doubled about every 7 months ([2503.14499](https://arxiv.org/abs/2503.14499)). Ord's constant-hazard model gives the p^n intuition ([2505.05115](https://arxiv.org/abs/2505.05115v1)) but was later revised: hazard **declines** over a task ([post](https://tobyord.com/writing/hazard-rates-for-ai-agents-decline)). Reliability falls sharply under pass^k: τ-bench pass^8 < 25% in retail ([2406.12045](https://arxiv.org/abs/2406.12045)); MCPMark 52.6% pass@1 → 33.9% pass^4 ([2509.24002](https://arxiv.org/html/2509.24002v1)). | step verifiers catch errors at the step they occur (§A6); evaluate with pass^k (§A10) |
| Context rot | 18 models degrade non-uniformly with input length ([Chroma](https://www.trychroma.com/research/context-rot)) | `Limits.MaxContextFrac`; compaction is the client's job, and Hydra treats it as a session break (§A4.1) |
| Loops, over-search, premature termination | MAST: 14 failure modes incl. step repetition, unawareness of termination conditions, premature termination, missing verification; κ = 0.88 ([2503.13657](https://arxiv.org/abs/2503.13657)). Anthropic saw 50 subagents spawned for simple queries and endless searches [vendor]. | `loop`, `progress`, `final.claims` verifiers; step and fan-out caps |
| Tool-argument / tool-selection errors | BFCL multi-turn ([leaderboard](https://gorilla.cs.berkeley.edu/leaderboard.html)); MCP-Universe "unknown tools" ([2508.14704](https://arxiv.org/abs/2508.14704v1)) | `toolcall.schema`, `toolcall.grounded` |
| Reward hacking | METR: o3 reward-hacked in 30.4% of RE-Bench runs but 0.7% on HCAST, a gap linked to a visible scoring function ([METR](https://metr.org/blog/2025-06-05-recent-reward-hacking)). ImpossibleBench: cheating rates depend on test visibility ([2510.20270](https://arxiv.org/abs/2510.20270)). | verifiers run **outside** the agent's reach, on Hydra-held artefacts, never on files the agent can edit |
| Prompt injection via tool output | AgentDojo: 97 tasks, 629 security cases ([2406.13352](https://arxiv.org/abs/2406.13352)). InjecAgent: 24% ASR on ReAct GPT-4, nearly doubled with a hacking prompt ([2403.02691](https://arxiv.org/abs/2403.02691)). CaMeL: capability/data-flow separation is provably secure at 77% vs 84% undefended utility ([2503.18813](https://arxiv.org/abs/2503.18813)). MCP tool poisoning exfiltrated SSH keys ([Invariant](https://invariantlabs.ai/blog/mcp-security-notification)). MCPTox: refusal under 3% ([2508.14925](https://arxiv.org/abs/2508.14925)). Lethal trifecta: private data + untrusted content + exfiltration channel (Willison, June 2025) [2°] | detection as a calibrated source only; the trifecta capability gate (§A9.2) |
| Irreversible actions | The Replit agent deleted a production DB during a code freeze and misreported that rollback was impossible ([OECD AIM](https://oecd.ai/en/incidents/2025-07-19-1eb1)) | approval gate (§A9.4) |
| Denial of wallet | OWASP LLM10:2025 Unbounded Consumption ([OWASP](https://genai.owasp.org/llmrisk/llm102025-unbounded-consumption/)); multi-agent runs use about 15× tokens [vendor] | session `Quota`, non-retryable 403 (§A5.3) |
| Non-determinism | At temperature 0, Qwen3-235B gave 80 unique completions in 1,000. The cause is batch-variance, not "floating point" in general ([Thinking Machines](https://thinkingmachines.ai/blog/defeating-nondeterminism-in-llm-inference/)). FP8-served same-model controls diverged on >90% of forks (Replay Gap). | replay = return the recorded bytes (§A8.3); never claim re-execution reproduces a trajectory |

### A2.5 Protocols: what a gateway must carry

- **MCP.**
  - **2025-06-18**: `outputSchema`/`structuredContent`, elicitation, OAuth resource-server classification with RFC 8707 resource indicators, batching removed ([changelog](https://modelcontextprotocol.io/specification/2025-06-18/changelog)).
  - **2025-11-25**: experimental **tasks** (`working | input_required | completed | failed | cancelled`, bound to the authorisation context), URL-mode elicitation, sampling with tools, JSON Schema 2020-12. Input-validation errors are returned as `isError` tool results so the model can self-correct ([changelog](https://modelcontextprotocol.io/specification/2025-11-25/changelog), [tasks](https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/tasks)).
  - Tool annotations (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`) **"MUST [be] consider[ed] untrusted unless they come from trusted servers"** ([tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)).
  - **2026-07-28** (current) ([RC post](https://blog.modelcontextprotocol.io/posts/2026-07-28-release-candidate/)):
    - removes `initialize` and `Mcp-Session-Id`, making MCP **stateless**;
    - adds `Mcp-Method`/`Mcp-Name` HTTP headers, so MCP is routable at L7 without parsing bodies;
    - moves tasks to an official extension;
    - deprecates sampling, roots and logging;
    - replaces held SSE streams with multi-round-trip requests.

  A stateless MCP with routing headers makes a future Hydra MCP proxy (ledger enforcement on `tools/call`) much simpler than it was in 2025.
- **A2A.**
  - **1.0**: Agent Cards (signed, with security schemes), task states `SUBMITTED … INPUT_REQUIRED, AUTH_REQUIRED … REJECTED`, JSON-RPC/gRPC/HTTP bindings, SSE streaming, webhook push, and `contextId` grouping tasks into a conversation ([spec](https://a2a-protocol.org/latest/specification/)).
  - **Governance**: Linux Foundation hosted since June 2025, now in the Agentic AI Foundation with MCP and goose ([AAIF](https://aaif.io/blog/a2a-joins-aaif)).
  - **IBM's ACP** merged into A2A in August 2025 ([IBM](https://www.ibm.com/think/topics/agent-communication-protocol)). It is not separately relevant.
  - A2A is agent-to-agent, not model-to-gateway. It matters to Hydra only if Hydra ever exposes a *verified agent* as an A2A server, which is out of scope here.
- **OpenAI Responses.**
  - `function_call` items with `call_id` and JSON-string `arguments`; `function_call_output` with the matching `call_id`; `parallel_tool_calls`; streaming `response.function_call_arguments.delta`/`.done`; `strict` requires `additionalProperties:false` ([function calling](https://developers.openai.com/api/docs/guides/function-calling)).
  - **Reasoning items must be passed back** with tool outputs. In stateless mode they carry `encrypted_content`, and they are reusable only within one model family: switching families silently drops them ([reasoning](https://developers.openai.com/api/docs/guides/reasoning)).
  - `previous_response_id` ties state to the provider.
- **Anthropic tool use.**
  - `tool_use.id` ↔ `tool_result.tool_use_id`. Results must be in the **immediately next** user message, ahead of any text.
  - Parallel tool use is on by default, with all results in one message. `disable_parallel_tool_use` lives inside `tool_choice`, and Opus 5.5, Sonnet 5.5 and Fable 5.1 reject `tool_choice` `any`/`tool` ([handle calls](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls), [parallel](https://platform.claude.com/docs/en/agents-and-tools/tool-use/parallel-tool-use)).
  - Streaming is `input_json_delta.partial_json`. Fine-grained streaming can yield invalid JSON, which should be returned as an `is_error` result ([fine-grained](https://platform.claude.com/docs/en/agents-and-tools/tool-use/fine-grained-tool-streaming)).
  - **Thinking blocks** must be passed back complete and unmodified with their `signature`. From Fable 5.1 a signature is valid only while `system`, `tools` and earlier messages are unchanged. Cross-model readability of thinking is directional and limited ([thinking](https://platform.claude.com/docs/en/build-with-claude/thinking)).
  - Claude Code reacts to a signature rejection by stripping old thinking and retrying ([protocol](https://code.claude.com/docs/en/llm-gateway-protocol)).
- **What a gateway must preserve** (consolidated):
  1. tool definitions, byte-stable;
  2. call-id pairing, using an id **map** when translating dialects, never regenerated ids;
  3. parallel calls kept together, results before text;
  4. streaming argument deltas, `signature_delta` and SSE `ping`s, unbuffered. Claude Code's idle watchdog aborts if pings are stripped;
  5. reasoning and thinking items verbatim;
  6. `cache_control` wherever it appears; block content never flattened to strings;
  7. `anthropic-beta` header and body-field pairs together; error bodies unmodified, so client retry logic still works.

  Today Hydra violates (5), (6) and partly (4) and (7) (§A1.1).

### A2.6 Market

| Product | Agent awareness claimed | What it measures or decides |
|---|---|---|
| Portkey (acquisition by Palo Alto Networks announced April/May 2026) | MCP gateway: auth, per-server access, logging [2°] | rules, fallbacks, budgets |
| LiteLLM | one proxy for `/v1/chat/completions`, `/mcp` and `/a2a` with shared keys and limits ([docs](https://docs.litellm.ai/docs/mcp_deployment)) | static routing, spend |
| Not Diamond | meta-model router and prompt adaptation; a coding-agent proxy learning from developer feedback [vendor/2°] | learned per-prompt choice |
| Weave Router (Workweave) | Go proxy for Claude Code/Codex/opencode, local embedder + cluster scorer, <50 ms, Elastic License; v2 claims parity with a frontier model on Terminal-Bench at half the cost [vendor] ([2°](https://aiweekly.co/alerts/workweave-router-targets-40-70-ai-cost-cuts-with-endpoint-swap)) | per-request learned choice |
| OpenRouter | `openrouter/auto`; `auto-beta` routes to the most-used model per task category; Responses API in **beta and stateless** ([docs](https://openrouter.ai/docs/api/reference/responses/overview.md)) | popularity-weighted choice |
| Cloudflare AI Gateway | dynamic routing flows with budget-limit nodes; Chat Completions shape only ([docs](https://developers.cloudflare.com/ai-gateway/features/dynamic-routing/)) | rules, budgets |
| Kong AI Gateway | MCP proxy plugin, "Agent Gateway" with A2A (April 2026) [2°] | policy |
| Vercel AI Gateway | fallbacks, paired with the AI SDK agent loop and Workflow SDK [2°] | rules |
| Martian | homepage now leads with interpretability research; router product de-emphasised **[unverified]** | n/a |

**Observability.**

- **LangSmith**: trajectory-match evaluators against reference tool-call sequences, plus LLM judges ([docs](https://docs.langchain.com/oss/python/langchain/test/evals)).
- **Langfuse**: agent graphs inferred from span nesting, with loops shown as cycles ([docs](https://langfuse.com/docs/observability/features/agent-graphs)).
- **Braintrust**: trace → dataset → scorer CI.
- **Arize Phoenix**: OpenInference spans, with trajectory evaluations in the commercial tier.
- **W&B Weave**: op-level traces with cost.
- **AgentOps**: no primary documentation found.
- **OpenTelemetry GenAI agent conventions** (`invoke_agent`, `execute_tool`, `gen_ai.agent.*`): still **Development** status, moving to a separate repository with no tagged release ([semconv](https://github.com/open-telemetry/semantic-conventions/blob/v1.37.0/docs/gen-ai/gen-ai-agent-spans.md)).

**Gap.** No gateway in this list publishes per-task-class *measured* capability posteriors, verifier calibration or sequential stopping for agent steps. "Agent-aware" in the market means MCP/A2A proxying, fallbacks and budgets. Hydra's differentiator survives into agents only if §A5–§A6 are built on measurement, and if §A10 shows a number against a single-model baseline.

---

## A3. Design principle

Hydra is a **gateway**: it sees model calls, not tool executions. Every client listed in §A8 runs its own loop and executes its own tools. The design therefore does not make Hydra an agent framework. It makes Hydra *trajectory-aware* at the one place it already sits:

1. **Recognise** that request *n* belongs to the same session as request *n−1*, and what kind of step it is (§A4).
2. **Route per session first, per step second.** The session-level choice is the decision that is measured and explored. Per-step changes are a constrained exception: escalation after a failed step verifier, or a measured gain larger than the switching cost (§A5).
3. **Verify each step before the client sees it.** A step is a single response, so the parent RFC's verify-then-return cascade applies unchanged: a bad tool call is regenerated or escalated inside one HTTP request, and the client never sees the failure (§A6).
4. **Learn from two clocks.** The next request reveals the previous step's tool result (a free, one-step-delayed label), and `/v1/feedback` reveals the task outcome (§A6.4).
5. **Bound the session**: spend, steps, loops, and irreversible actions (§A9).

Everything is expressed through the existing mechanisms: `rank.EffectiveScoreIn`, `trust.Calibrator`, `trust.Run`, `oracle.Oracle`, `ledger.Policy`, `pending`, `budget/risk.go`, `ope`.

---

## A4. Sessions and step types

### A4.1 Session identity

A session id is resolved once per request, first match wins:

| # | Source | Clients that provide it |
|---|---|---|
| 1 | `X-Hydra-Session: <id>` header, or `metadata.hydra_session` in the body | any client that can set a header (all SDKs listed in §A8 can set default headers) |
| 1b | Client-native session headers: Claude Code's `x-claude-code-session-id`, with `x-claude-code-agent-id` / `x-claude-code-parent-agent-id` giving subagent parentage; Temporal and Restate workflow ids passed by the activity | Claude Code and the Claude Agent SDK, unchanged |
| 2 | Responses `previous_response_id`: look up the session of that response | OpenAI Agents SDK, Codex, any Responses client with `store` semantics |
| 3 | Responses `conversation` id | Responses clients using the Conversations API |
| 4 | **Prefix chain match** (below) | every client, unchanged: Chat Completions and Messages loops resend the whole history |
| 5 | none: a new session whose id is the request id | |

**Prefix chain.** Agent loops are append-only. Request *n+1* carries request *n*'s messages plus the assistant turn and the tool results. Hydra keeps a rolling hash over canonicalised messages:

```
h_0 = H(tenant ‖ canonical(system) ‖ canonical(tools))
h_i = H(h_{i-1} ‖ canonical(message_i))
```

- Canonicalisation drops volatile fields (`cache_control`, ids re-minted by the client). It keeps role, content, tool-call names, arguments and ids.
- On each response, the store maps `h_{n+assistant}` → session id.
- An incoming request walks its own chain from the end until it hits a stored hash. If it hits, it continues that session at that step.
- This is O(messages) hashing per request, with no client change.
- **Branching is a feature.** A client that rewinds (Claude Code's `/rewind`, a LangGraph time-travel fork) matches an earlier hash. The new branch becomes a child session, with `parent_session` and `fork_step` recorded.
- **Compaction breaks the chain on purpose.** When a client summarises its history (Claude Code auto-compact), the prefix changes. Without a header, Hydra starts a new session linked by nothing. That is correct for routing, since the cache is gone anyway, and a known gap for outcome attribution. It is the reason header (1) exists.

Subagents (Claude Code subagents, OpenAI Agents SDK handoffs, LangGraph subgraphs) arrive as separate chains with a different system prompt. Each is a session of its own. `X-Hydra-Parent-Session` links one to its parent when the client sets it, and Claude Code's `x-claude-code-parent-agent-id` does the same for its subagents. Otherwise the link is unknown and is recorded as unknown rather than guessed.

Claude Code also offers opt-in hint headers (`CLAUDE_CODE_GATEWAY_HINT_HEADERS=1`). One of them, `x-claude-code-request-class` (`main | subagent | workflow | compaction | auxiliary`), is a client-declared step class. Hydra reads it as a **task-class hint**, so `compaction` and `auxiliary` calls get their own calibration keys instead of polluting `main`. It never overrides the request-shape rules in §A4.2. A client header is a claim, and the shape is a measurement.

### A4.2 Step type, classified deterministically from the request

The type of a step is read from the request shape, with no model call:

| Step type | Rule, evaluated in order on the request | Notes |
|---|---|---|
| `single` | no `tools` offered and no tool messages in history | today's parent-RFC path, unchanged |
| `interpret` | last message is a tool result (`role: tool`, a `tool_result` block, a `function_call_output` item) | the model reads what a tool returned and decides what next |
| `act_forced` | `tool_choice` is `required`, `any` or a named tool | the caller demands a call |
| `plan` | tools offered, last message is a user message, and the session has no prior assistant turn **or** the previous assistant turn was a final text answer | the opening turn of a task, or a new instruction mid-session |
| `act` | tools offered, last message is a user message, otherwise | mid-loop user nudge |
| `reflect` | **only** from an explicit `X-Hydra-Step: reflect` header or a Hydra-run workflow step | cannot be inferred: a "critique your work" turn is lexically indistinguishable from an instruction |

After the response, the step is labelled with the **response type**:

- `tool_call`: one or more calls, with `parallel = n`;
- `final`: text, `finish_reason` `stop` / `end_turn`;
- `truncated`;
- `refusal`.

Request type × response type is what calibration keys on. The calibration key extends the parent RFC's task class:

```
class key = <task class>/<step type>      e.g. support.airline/interpret
session key = <task class>/session        e.g. support.airline/session
```

The task class of a session is resolved once, at its `plan` step, by the parent RFC's §5.2 resolver, and inherited by every later step. Re-classifying each step would let one session route on two classes. A `X-Hydra-Task-Class` header on a later request overrides the inherited class.

### A4.3 Go types

```go
// package session

type StepType string // single | plan | act | act_forced | interpret | reflect
type ResponseType string // tool_call | final | truncated | refusal

type Session struct {
	ID            string
	Tenant        string
	Parent        string // "" when unknown, never guessed
	ForkStep      int
	Class         string // resolved at the plan step, inherited
	Objective     string // quality | balanced | cost
	Incumbent     string // head id currently serving
	IncumbentSince int
	Steps         int
	SpentUSD      float64
	Tokens        int64
	CachedTokens  int64
	Switches      int
	State         State // open | done | budget_exhausted | loop_terminated | failed
	Limits        Limits
	ChainHead     [32]byte // h_n of the last response
	Opened, Seen  time.Time
	Outcome       trust.Outcome // from /v1/feedback; Unknown until then
}

type Limits struct {
	MaxUSD, MaxSteps, MaxToolCallsPerTool int
	MaxWall                               time.Duration
	MaxContextFrac                        float64 // parent RFC Quota.ContextFrac
}

type Step struct {
	Session    string
	N          int
	ReqType    StepType
	RespType   ResponseType
	Head       string
	ActProb    float64 // propensity of choosing Head for this step (OPE)
	Switched   bool    // Head != previous step's Head
	Reason     string  // incumbent | escalated:<verifier> | session_start | explore
	ToolCalls  []CallDigest
	Verdicts   []StepVerdict
	InTok, OutTok, CachedInTok int
	CostUSD    float64
	SpanID     string // runlog span; ParentSpan = session root span
}

type CallDigest struct {
	ID, Name string
	ArgsHash [32]byte // canonical JSON
	Result   ResultDigest // filled when the NEXT request carries the tool result
}

type ResultDigest struct {
	Seen      bool
	IsError   bool   // provider flag (is_error) or the operator's error pattern
	Hash      [32]byte
	Untrusted bool   // always true; carried so egress and injection checks read it
}

type Store interface {
	Resolve(ctx context.Context, tenant string, req Fingerprint) (Session, int, error) // session, step index
	AppendStep(ctx context.Context, s Session, st Step) error
	AttachResults(ctx context.Context, sessionID string, results []ResultDigest) error
	SetOutcome(ctx context.Context, sessionID string, o trust.Outcome) error
}
```

`Store` sits beside the parent RFC §5.3 storage interfaces: jsonl for `embedded`, Postgres for `gateway`. Sessions expire after an idle TTL (default 24 h). A step count of 0 after expiry only means "new session". It never resurrects old limits.

---

## A5. Routing inside a session

### A5.1 Two decisions, not one per step

1. **Session-start choice**, at the `plan` step. The incumbent is the argmax of P̂(session success | head, class) under `quality`, from `rank.EffectiveScoreIn` over the `<class>/session` key. Before `MinCommitments` sessions exist for a head, it falls back to the `<class>/plan` step key, then to the parent RFC's ladder (neighbours, catalogue prior marked `unmeasured`). This is the only decision that **explores** (`explore_rate`), so a trajectory carries one propensity and stays usable for OPE (§A6.5).
2. **Per-step switch**, at every later step. The incumbent stays unless one of the rules in §A5.2 fires.

The reason is cost and correctness, not taste:

- **Prompt cache.** Provider caches are per model and per exact prefix. A switch makes the new model read the whole prefix uncached: at step 40 of a coding loop that is often more than 100k tokens, at roughly 10× the cached price (§A2.2).
- **Provider-bound state.** Anthropic thinking blocks carry signatures that only Anthropic can validate. From Fable 5.1, a signature is valid only while `system`, `tools` and all earlier messages are byte-identical. OpenAI reasoning items are reusable only within the same model family. Moving across providers or families mid-loop either drops the reasoning or fails the request (§A2.5). A corollary binds Hydra itself: **any rewrite of `tools` or `system` mid-session**, including policy filtering (§A9.1), invalidates both the prompt cache and the thinking signatures. Hydra's own edits to those fields are therefore decided once, at session start, and frozen.
- **Behavioural inconsistency.** Different models plan differently. A plan written by A and continued by B is the "heterogeneous mid-trajectory switch" for which the evidence below finds little support (§A2.2).

### A5.2 The switch rule

Let *m* be the incumbent and *m′* a candidate. Let P̂ denote the posterior pass rates for the **current step type** under the session's class. Hydra switches at step *k* only if one of the following holds.

**(a) Escalation.** A step verifier rejected *m*'s response to this step (§A6). The response is discarded, never returned. The step is regenerated on the next head in descending P̂ order, the parent RFC's cascade applied to one step. If it is accepted, *m′* becomes the incumbent for the rest of the current **user turn** (a ratchet). At the next `plan` step, the session-start rule runs again.

**(b) Measured gain at a turn boundary.** Only when the request is a `plan` step (no tool loop in flight, so no provider-bound reasoning state is live), and:

```
Pr(p_{m'} > p_m | data) ≥ 0.95                                     (posterior superiority, two Betas)
and (E[p_{m'}] − E[p_m]) · V  >  C_switch + V · ε_inc

C_switch = T_prefix · (price_in(m') − price_cached_in(m))          (re-reading the prefix uncached)
         + T_prefix · price_cache_write(m')                         (if m' caches)
```

- `V` is the value of a successful step. Under `balanced` it is the route's `DefectCostUSD`. Under `quality`, `V` is large, so the rule reduces to superiority plus `ε_inc`.
- `ε_inc` is the **inconsistency margin**, the measured drop in step pass rate on the step *after* a switch compared with a non-switch step at the same depth. It starts at 0.05 and is replaced by its measured value once there are `MinCommitments` switched steps in the class.

**(c) Hard failure.** *m* is unhealthy (`internal/health` open breaker, 429 with `Retry-After` longer than the step deadline). The fallback chain applies as today, **restricted to heads that can accept the session's history.** A history containing Anthropic thinking blocks can fall back only to Anthropic heads (direct API, Bedrock or Vertex Claude) unless the operator sets `drop_reasoning_on_failover: true`. A fallback is logged `Reason: failover` and is not counted as a routing decision.

**(d) Per-step downgrade/upgrade, `balanced` and `cost` only.** TwinRouterBench's live result (§A2.2) shows that an execution-trained per-step router can roughly halve cost at equal resolution, even with cache-switch costs paid. SWE-Router shows that "start cheap, escalate on the trajectory" is never worse than routing on the prompt alone. Under `balanced` and `cost`, Hydra therefore allows two things.

- **Explore-then-escalate start.** The session starts on the cheapest head whose `<class>/session` posterior clears the route's floor. The incumbent ratchets up on the first failed step verifier, or after `N_probe` steps (default 3) if the `progress` verifier's LLR is negative.
- **Per-step moves at `interpret` steps.** Allowed only when all of the following hold:
  1. both heads have `MinCommitments` observations on `<class>/interpret`;
  2. rule (b)'s inequality holds with `V = DefectCostUSD`;
  3. no provider-bound reasoning item is live in the history, or the two heads share a reasoning-compatible family.

These moves are deterministic given the posteriors and the verdicts, so they add no propensities (§A6.5). Whether they beat T1 is measured **live**, never from replayed logs: the Replay Gap result (§A2.2) shows splicing is invalid. Under `quality`, (d) is off.

`explore_rate` never fires per step inside a session (see A5.1).

Every switch emits `X-Hydra-Switched: 1` and `X-Hydra-Switch-Reason`, so a client that cares, for example one with its own cache accounting, can see it.

### A5.3 Session budget

The parent RFC's `Quota` gets the scope `session:<id>`, charged per step from the executor's real token counts, including `CachedInTok` priced at the cached rate.

- **Soft limit.** The drift-diffusion first-passage model in `internal/budget/risk.go` reads the session's spend series. When P(hit `MaxUSD` before an estimated completion) exceeds 0.5, Hydra adds `X-Hydra-Budget-Risk`. Under `cost` and `balanced` it may also downgrade at the next `plan` step. Under `quality` it **never** downgrades silently: it warns, and then the hard limit applies.
- **Hard limits.** When `MaxUSD`, `MaxSteps`, `MaxWall` or `MaxToolCallsPerTool` is reached, the request is refused with HTTP **403** and `error.type = "hydra_session_limit"`, `error.code = "max_usd" | "max_steps" | …`.
  - Not 429 and not 5xx, because every major SDK retries those by default, and the retry is exactly the denial-of-wallet loop the cap exists to stop. 409 is avoided for the same reason.
  - The session state becomes `budget_exhausted`.

Defaults, per route and overridable by key: `MaxSteps 200`, `MaxUSD` unset (operator must choose), `MaxToolCallsPerTool 50`, `MaxWall 2h`. A request can lower them (`X-Hydra-Session-Max-Usd`), never raise them past the key's ceiling.

---

## A6. Step-level verification and credit assignment

### A6.1 Step verifiers are sources

Each is an `oracle.Oracle`, calibrated under `verifier:<kind>:<id>` per `<class>/<step type>`, and contributes LLR = ln(se/(1−sp)) exactly as in the parent RFC §5.2. Deterministic checks start with se and sp from their own measured confusion matrix, not by fiat.

| Verifier | Applies to | Check | Ground truth that calibrates it |
|---|---|---|---|
| `toolcall.schema` | `tool_call` responses | name ∈ offered tools; `arguments` parses; validates against the tool's JSON Schema (`parameters`), including `required`, `enum`, `additionalProperties:false` when declared | the next request's tool result: `is_error`, or the operator's error regex, plus BFCL-style replay (§A10) |
| `toolcall.grounded` | `tool_call` | every argument leaf that looks like an identifier (ids, paths, URLs, emails, numbers with ≥ 4 digits, quoted entity names) appears in the conversation: system, user messages, prior tool results. This is `internal/ground` with the conversation as context instead of fenced blocks. | tool result errors of the not-found or invalid kind; tau-bench-style database diffs (§A10) |
| `loop` | every step | the same `(name, canonical args)` issued ≥ `k` times (default 3) with **identical** result hashes, or a cycle of period ≤ 4 in the `(call, result-hash)` sequence | a session that ends `loop_terminated` or fails, as against one that later succeeds (`/v1/feedback`) |
| `progress` | `interpret` | novelty: the share of the last `w` tool results (default 5) whose content hash is new to the session. A floor of 0 over `w` steps is "no progress". | session outcome; weak, so it starts with low D and earns weight only from data |
| `final.claims` | `final` responses | the answer asserts an action ("I updated", "I've sent", "deleted") with no tool call of a matching kind in the session, or there is an unresolved `is_error` result in the last `w` steps (**premature termination**) | `/v1/feedback` |
| pack verifiers | `final` | the parent RFC verifiers: `jsonschema`, `match`, `command`, `grounded`, `judge`, `http` | as in the parent RFC |
| `judge.step` | optional, any | an LLM judge on (last *k* messages, response) with a rubric. It is a source like any other, so its bias is measured. Off by default: it doubles step cost. | session outcome and human labels |

`ToolCallEquivalence` is a new `trust.AnswerEquivalence`. Two tool-call responses are equivalent when they make the same multiset of (name, canonical-JSON args), with argument order normalised and floats compared at 1e-9. This lets the **existing** SPRT run over tool calls (§A7.2).

### A6.2 What happens when a step verifier fails

1. **Repair, same model (cheap).** Only for `toolcall.schema`, the only case where the error is mechanical. Hydra re-asks *m* once, appending the validator's message as a synthetic tool error. That costs one extra call on a warm cache. The repaired response must pass. The synthetic turn is **not** returned, so the client's history stays the client's.
2. **Escalate** (§A5.2a) for every other failed verifier, or a failed repair.
3. **Return with evidence** when the cascade is exhausted or `Target.MaxCostUSD` for the step is hit. The last response goes back with `X-Hydra-Decision: budget_exhausted` and `X-Hydra-Step-Verdicts`. A gateway must never invent a tool call, and must never return nothing when the caller is waiting on a turn.
4. `loop` with confidence over the target is not escalated, because a stronger model reading the same looping context often loops too. Instead the step response is replaced by a **final** assistant message that states the loop, the session goes to `loop_terminated`, and the header says so. The parent RFC's "do not rewrite answers" rule has exactly one exception, this one, and it exists because the alternative is unbounded spend.

### A6.3 What is verified before the client sees it, and the latency cost

Deterministic verifiers (`schema`, `grounded`, `loop`, `final.claims`) are microseconds to low milliseconds and always run. For `stream: true`, a `tool_call` response is buffered until its arguments complete, because a client cannot act on half a call anyway, and then verified. Text deltas of an `interpret` or `final` step stream under the parent RFC's §5.4 modes. The default is `stream_unverified` for chat routes and `verify_then_stream` for routes with a pack verifier.

### A6.4 Two clocks of ground truth

**Clock 1: the next request, one step later, free.** When request *n+1* arrives in a session, it carries the tool results for step *n*'s calls, paired by `tool_call_id` / `tool_use_id` / `call_id`. `AttachResults` fills `CallDigest.Result`. That yields, with no human involved:

- `toolcall.schema` and `toolcall.grounded` get a label per call: the tool ran or it errored. That is a real TN/FN signal, and the only cheap path to specificity that the parent RFC's §5.2 item 4 says is otherwise missing.
- The generator *m* gets a step-level commitment row under `<class>/<step type>` ("its call executed without error"). This is a weak, biased label: a valid call can still be the wrong call. It is therefore recorded under a **separate** key, `<class>/<step type>/exec`, and used only as a prior for the step key, never as the step's pass rate.

**Clock 2: `/v1/feedback`, task outcome, delayed.**

```
POST /v1/feedback
{ "session_id": "...", "outcome": "correct" | "incorrect",
  "step": 17?,           // optional: the step the caller blames
  "source": "tests|user|reviewer|ticket" }
```

`request_id` still works: it resolves to that request's session and step.

### A6.5 Credit assignment: what is learned from a trajectory outcome

The design keeps credit assignment small by making the **session the routing unit**:

1. **Incumbent heads.** The head that served the session (or each head, weighted by the share of steps it served) gets one commitment row on `<class>/session`. This is exact, with no attribution model: it is the pass rate of the decision Hydra actually makes (§A5.1). Steps served after an escalation also credit the escalated head on `<class>/session/escalated`, so "rescues" are measured separately from "starts".
2. **Step verifiers.** These train from a trajectory outcome only when the outcome is attributable:
   - `final.claims` and pack verifiers on the final step train exactly as in `ApplyRunOutcome` today, since that is a run;
   - `loop` and `progress` train on sessions they flagged: flagged and then `incorrect` is TP, flagged and then `correct` is FP;
   - they do **not** train sp on unflagged steps of failed sessions, because which step failed is unknown. The one exception is a caller-supplied `step`, which is treated as a step label.
3. **Generator per-step pass rates** are not updated from trajectory outcomes by uniform Monte-Carlo credit (every step gets the final reward). In a 50-step success that would credit 50 steps, some of them bad, and in a failure it would debit the good ones. That is the variance problem RL methods for agents spend their effort on (§A2.3). Step pass rates come from clock 1 and from step verifiers. The session outcome trains only the session key.

The trajectory version of `ApplyRunOutcome`:

```go
// package trust

type TrajectoryStep struct {
	Index    int
	Head     string
	Type     string     // "<class>/<step type>"
	Ledger   []Evidence // SPRT ledger if the step ran an ensemble, else nil
	Flags    []Flag     // step verifiers that fired: {Verifier, SaidBad, LLR}
}

type Trajectory struct {
	Class  string // session class
	Steps  []TrajectoryStep
	Blamed int    // -1 unless the caller named a step
}

// ApplyTrajectoryOutcome trains what a session outcome can attribute and
// nothing else: the session key for the heads that served it, the final
// step's ledger through ApplyRunOutcome, and verifiers on the sessions they
// flagged. It returns the rows written, so a caller can show what moved.
func ApplyTrajectoryOutcome(cal *Calibrator, t Trajectory, actual Outcome) (int, error)

// ApplyStepOutcome is clock 1: one call's execution result, attributed to the
// verifiers that judged that call and to the generator's /exec key.
func ApplyStepOutcome(cal *Calibrator, class string, step TrajectoryStep, call CallDigest) (int, error)
```

Both keep `ApplyRunOutcome`'s invariant: only a verdict made *before* the outcome was known can train. Both are idempotent per (session, step, call id), using the same applied-set mechanism as `trust_applied.jsonl`.

**Off-policy evaluation.** Exploration happens only at session start (§A5.1). A session therefore has one logged propensity `π₀(head | class)`, and `internal/ope`'s self-normalised IPS evaluates session-level policies ("always A", "argmax measured", "cost") with the effective sample size it already computes. Per-step exploration would make the trajectory weight a product of up to *n* propensities, with variance exponential in *n*, which is the textbook failure of trajectory IS (§A2.3). Stickiness is what keeps OPE identifiable, not just cheap. Per-step escalations are deterministic given verifier verdicts, so they carry no extra propensity: they are part of the policy, not a randomised action.

---

## A7. Multi-agent and test-time compute

### A7.1 When fan-out helps

The evidence in §A2.2 and §A2.4 supports parallel agents for **breadth-first, read-mostly** work: research, search, independent subquestions. There the tokens spent explain most of the variance and subagents do not write shared state. It does not support them for **coupled writes** (most coding), where most multi-agent failures are coordination and specification failures, not capability failures. Hydra does not orchestrate subagents. Clients do (Claude Code subagents, Agents SDK handoffs). Hydra:

- routes each subagent as its own session (§A4.1), so a read-only `Explore`-type subagent can be served by a cheaper head *if* one is measured good at that subagent's class. Claude Code's built-in Explore subagent inherits the main model by default, so this is a choice the client and the operator make, not something Hydra assumes;
- reports fan-out spend per parent session (`parent_session` roll-up), because a parent's budget must cover its children's: `Quota{Scope: "session:<parent>"}` is charged by every descendant.

### A7.2 Fan-out *of a step*: ensembles on high-stakes calls

For a step whose emitted call is **irreversible** (§A9.4) or whose class sets `Target.Confidence`, Hydra can run the existing SPRT over tool calls:

1. sample sources in the parent RFC's `quality` order, with the family correlation discount, comparing with `ToolCallEquivalence`;
2. return the call once Λ crosses the target, or route it to the approval gate when the budget is exhausted.

Each extra sample re-reads the prefix, so the default scope is irreversible calls only. The expected cost per guarded call is about (1 + E[extra samples]) × prefix cost.

### A7.3 `internal/optimal` beyond code

`n* = √((1−s)/k)` sizes **parallel workers on divisible work**, and its own comment records that the one measurement contradicted it. Outside code, the question Hydra actually faces is **how many samples** of one step or trajectory to draw. That has a different, measurable optimum. With per-sample pass probability p̂ (the Beta posterior), a verifier of sensitivity se, and an effective number of independent samples n_eff (the family discount turns n correlated samples into fewer independent ones):

```
P(accept a correct answer | n) ≈ 1 − (1 − p̂·se)^{n_eff(n)}
n* = min n such that P(...) ≥ Target.Confidence, subject to n·E[cost] ≤ MaxCostUSD
```

Proposal:

- keep `optimal.Agents` for the coding pack's file fan-out;
- add `optimal.Samples(p, se, rho, target, maxN)`, where `rho` is the intra-family correlation that `trust.RecordCoAgreement` already logs;
- swarm `best` mode uses it instead of a fixed `--swarm-max-heads`;
- `s` and `k` for `Agents` are fitted per class from `runlog` spans before being used anywhere outside code.

---

## A8. API surface and client compatibility

### A8.1 Recommendation

1. **Chat Completions** (exists): accept `content` as string **or** an array of parts, and preserve the parts losslessly to heads that accept them. Add session resolution, step typing and the headers in §A5 and §A6. Still the lowest-common-denominator surface.
2. **Messages, `POST /v1/messages`** (parent RFC §5.1, now a hard prerequisite for agents): this is the only way Claude Code and the Claude Agent SDK reach Hydra. It must preserve `tool_use`/`tool_result` ids and their adjacency, parallel `tool_use`, `input_json_delta` streaming, `thinking`/`redacted_thinking` blocks **with signatures, byte-exact**, `cache_control` breakpoints, `system` as blocks, and `POST /v1/messages/count_tokens`. Requests routed to a non-Anthropic head are translated, and the thinking blocks are dropped *only* when §A5.2c allows it.
3. **Responses, `POST /v1/responses`**: needed for Codex and the OpenAI Agents SDK's default model class. In two stages:
   - (a) **stateless**: `store: false`, full `input` items, `include: ["reasoning.encrypted_content"]` passed through to OpenAI heads, `function_call`/`function_call_output` paired by `call_id`, and the full streaming event set (`response.output_item.added`, `response.function_call_arguments.delta`, …);
   - (b) **stateful**: `previous_response_id` and `store: true`, backed by an item store in `session.Store`. Hydra becomes the conversation store, so it holds tenant content and falls under the parent RFC §5.3 retention and egress rules. Hosted tools (`web_search`, `file_search`, `code_interpreter`, remote MCP) are refused with 400 unless the routed head is OpenAI's. Hydra does not emulate them.
4. **`/v1/sessions`** (new, operator-facing, no client needs it):
   - `GET /v1/sessions/{id}`: steps, heads, switches, spend, cached-token share, verdicts, state;
   - `PATCH` to lower limits;
   - `POST /v1/sessions/{id}/end`;
   - `POST /v1/sessions/{id}/approvals/{pending_id}` to approve a parked call (§A9.4).
5. **No `/v1/workflows`.** Durable multi-step orchestration belongs to a workflow engine (§A8.3), and Hydra stays the model-call layer under it.

### A8.2 What each client needs to work unchanged

| Client | Wire | How it points at Hydra | What Hydra must support |
|---|---|---|---|
| Claude Code | Messages (streaming) | `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`; model env vars (`ANTHROPIC_MODEL`, `CLAUDE_CODE_SUBAGENT_MODEL`) may name Hydra routing ids | Everything in A8.1(2), plus forwarding `anthropic-beta`/`anthropic-version` verbatim, SSE `ping`s unbuffered, and `x-claude-code-*` headers read as session/agent ids. Model ids it sends must resolve (map `claude-*` names to hints or pins, never 404). **Anthropic does not support Claude Code against non-Claude models through a gateway** (§A2.1), so for this client Hydra routes **among Claude models and their hosts** (Anthropic API, Bedrock, Vertex) by default. Cross-vendor routing is an operator opt-in, at the operator's risk. |
| Claude Agent SDK | same as Claude Code (it runs the Claude Code harness) | same env vars | same, including the Claude-only constraint |
| Codex CLI | Responses | a `model_providers` entry with `base_url` and `wire_api = "responses"` | A8.1(3a) at least; encrypted reasoning passthrough when routed to OpenAI; `call_id` pairing; parallel calls |
| OpenAI Agents SDK | Responses by default, Chat Completions via `OpenAIChatCompletionsModel` / `set_default_openai_api("chat_completions")` | `AsyncOpenAI(base_url=...)` + `set_default_openai_client`; tracing export kept separate | either surface; with Chat Completions, today's endpoint plus content-part arrays |
| LangGraph / LangChain | Chat Completions (`ChatOpenAI(base_url=)`) or Messages (`ChatAnthropic(base_url=)`) | base URL | content-part arrays; parallel tool calls; `X-Hydra-Session` via `default_headers` is optional, since prefix matching works |
| Google ADK | via LiteLLM model wrapper (OpenAI-compatible) | `LiteLlm(model="openai/hydra", api_base=...)` | Chat Completions |
| CrewAI, AG2, Pydantic AI, smolagents, Mastra | OpenAI-compatible clients (mostly via LiteLLM or provider SDKs) | base URL | Chat Completions; some send `content` arrays |
| Goose, OpenHands | OpenAI-compatible or Anthropic provider configs | base URL | either surface |

Two compatibility rules follow from the table:

1. **The `model` field.** These clients put a *vendor model id* in it, and some validate it against a list. Hydra must accept known vendor ids as **routing hints**: a hint names a task-class prior and permits a pin when `X-Hydra-Objective: pin` is set. It must never answer them with `model_not_found`. The response's `model` echoes what the client sent, not the routed head, because some clients branch on it. The routed head goes in `X-Hydra-Sources`.
2. **Error classes.** Hydra's refusals must be 4xx that SDKs do not retry (400, 403, 404, 422), never 409, 429 or 5xx, unless retrying would actually help.

### A8.3 Durable workflows: integrate, do not build

**Recommendation: integrate with Temporal as the reference durable engine. Make Hydra replay-safe for any engine (Temporal, Restate, Inngest, LangGraph checkpointers, Cloudflare Workflows). Freeze `internal/workflow` as the CLI's linear runner.**

Why not grow `internal/workflow`:

- A general durable engine needs:
  - event-sourced history with deterministic replay;
  - timers and signals;
  - versioning of in-flight code;
  - activity retries with backoff and heartbeats;
  - child workflows;
  - queues and worker fleets;
  - a visibility store.
- `internal/workflow` has none of these. It is one JSON file per run, linear, capped at 200, and process-local.
- Building them is a multi-year distributed-systems product, and the parent RFC §6 already rules out "gateway breadth for its own sake".
- The thing Hydra uniquely adds, which model and whether its output is acceptable, sits *inside* an activity, not in the orchestration.

Why Temporal over Restate as the reference:

1. It has an integration with the OpenAI Agents SDK (`temporalio-openai-agents`, still Public Preview in Temporal's April 2026 changelog), which runs each model call as an activity. Pydantic AI's durable-execution layer also targets it. Pointing that activity's client at Hydra makes every step routed and verified with no Hydra-side orchestration code (§A2.1).
2. Its Go SDK is native to Hydra's language. The workflow-as-code model maps one-to-one onto `internal/workflow`'s step list, so `hyctl workflow` can emit a Temporal workflow later without a rewrite.
3. It is the most widely deployed of the options, which matters for the `gateway` and `managed` modes.

**Restate is the documented alternative** for single-binary deployments (`embedded` mode), where running a Temporal cluster is disproportionate. Hydra's requirements below are engine-neutral, so either works.

What Hydra must add for *any* durable engine (small, and all of it gateway work):

1. **`Idempotency-Key` header.** A retried activity with the same key gets the **stored** response, with no second spend and no second routing decision. The key, request-hash, response and headers are kept for the engine's retry horizon (default 24 h). Without it, every engine retry is a fresh, differently routed, separately charged call, and replay determinism is lost.
2. **Record/replay.** `X-Hydra-Replay: <request_id>` returns the recorded response byte for byte. This serves debugging, eval re-scoring, and engines whose replay re-executes the call.
3. **Human-in-the-loop as an engine signal.** `pending` becomes an API (`/v1/sessions/{id}/approvals`) and emits a webhook. The engine waits on a signal, not on `hyctl ask`.
4. **Session id = workflow id.** The engine passes `X-Hydra-Session: <workflow id>`, so a durable workflow's calls are one session with one budget, across worker restarts and days.

`internal/workflow` keeps:

- its CLI role;
- gains `pending` steps and `X-Hydra-Session` linkage;
- deletes the unread `Step.Tools`, per CLAUDE.md's dead-code rule.

It does **not** gain a DAG.

---

## A9. Safety

### A9.1 Tool allowlists per key and tenant (`ledger`)

The virtual key (parent RFC §5.3) carries a `ledger.Policy`. It is evaluated at two points:

1. **On the way in: filter definitions.** Every `tools[i]` the client offers is checked with `Decide(agent=key, tool=name, resource="*", action=Exec)`. A `Deny` removes the tool from what the head is sent, and Hydra reports it in `X-Hydra-Tools-Withheld`. A model cannot call a tool it was not shown, so this is the preventive control.
   - The filtered set is computed at the session's first request and **frozen** for the session. Changing `tools` mid-session invalidates the provider prompt cache and, on Anthropic, thinking signatures (§A5.1).
   - A policy change therefore takes effect at the next session, unless it is a `Deny` urgent enough to accept the cache loss. That case is an explicit `ledger` flag, `apply: immediate`.
2. **On the way out: check emitted calls.** Each `tool_call` is checked with the resource taken from its arguments. Hydra reads paths, URLs, hosts and table names using per-tool extractors (glob rules on argument JSON pointers, e.g. `bash:/command`, `*:/path`, `*:/url`).
   - `Deny`: the step is regenerated once with the denied call's tool withheld. If the model insists, the response is returned with the call **removed**, the text kept, and `X-Hydra-Decision: tool_denied`. Hydra never forges a different call.
   - `Ask`: see A9.4.

Every decision is recorded in the hash-chained ledger with `ParametersHash` over the canonical arguments. That binds the approval to the exact call: a client that later executes different arguments can be detected from the client-side `hyctl mcp verify` hook.

### A9.2 Tool results are untrusted input

- **Egress.** A tool result is a new `egress.Source` (`tool_result:<tool name>`). Its sensitivity is classified like any other part, so a session whose tool results contain secrets or PII prefers local heads for later steps (the existing secret→local rerouting). That sensitivity is **sticky for the session**: data that entered the context stays in it. Under §A5.2c this may force a switch, which is logged as `Reason: egress`.
- **Injection screening.** It is layered, and the layer that works does not depend on detection:
  1. **Detection, as a calibrated source.** The `injection` verifier runs on each new tool result (`policy.InjectionMarker` today, a classifier head optionally). Its verdict is a source with measured se/sp against AgentDojo / InjecAgent-style suites (§A10), never a gate by fiat.
  2. **Capability gating (deterministic).** This is the "lethal trifecta" rule (§A2.4). Hydra computes three session bits:
     - **P**: private data entered the context (egress classification ≥ internal);
     - **U**: untrusted content entered the context (any tool result from a tool with `openWorldHint` or not on the operator's trusted-tool list);
     - **X**: an exfiltration-capable tool is offered (`openWorldHint`, network, email, HTTP, or write to a shared location, per operator rules).

     When **P ∧ U ∧ X**, every call to an X tool goes to the approval gate, or is denied by policy. MCP tool annotations are *hints from the server* and are not trusted to *relax* a rule, only to tighten one.
  3. **Spotlighting.** When Hydra translates between dialects, tool results stay in their tool-result slots and are never promoted into system or user text. `flattenConversation`'s `role:` labels are a classifier convenience only and must never be sent to a head.

### A9.3 Spend, step and loop caps

As in §A5.3 and §A6.2. In addition, a per-key **concurrency cap on open sessions**, and a per-tenant daily `Quota`. Denial-of-wallet then needs one of these four to be misconfigured, not all of them.

### A9.4 Approval gate for irreversible calls

A call is **irreversible** when any of the following holds:

- an operator rule says so (glob over tool name and argument patterns, e.g. `bash` with `/command` matching `\brm\b|DROP |git push --force|kubectl delete`);
- the tool's MCP annotation `destructiveHint: true`;
- the trifecta rule fires.

An irreversible call gets a `ledger.Ask`:

1. **Park.** The step response is held, and a `pending.Question` is saved with the session, step, call and parameter hash.
2. **Answer the client so it does not hang.**
   - Messages and Chat Completions: HTTP **403** `hydra_approval_required`, with `pending_id` and an approval URL. The SDKs surface it, and the client's agent loop stops cleanly.
   - Responses, when the tool is an MCP tool: the native `mcp_approval_request` item, which the Agents SDK already handles.
3. **Approve.** Via `hyctl ask answer`, `POST /v1/sessions/{id}/approvals/{pending_id}`, or a webhook to the operator's approval system.
4. **Retry.** The client retries with the same `Idempotency-Key` and receives the stored, now-approved response. A Temporal workflow waits on the approval signal instead of retrying.
5. **Decline.** Approval of a different call fails `VerifyParams`. A decline returns the response with the call removed (A9.1).

This is the one place where Hydra's answer depends on a human. It is opt-in per route because it changes client-visible behaviour.

---

## A10. Evaluation plan

Nothing in §A3–§A9 ships as a default until these numbers exist (parent RFC §7 rule).

### A10.1 Suites

| Suite | Why | Pack | Verifier available |
|---|---|---|---|
| **τ²-bench** (airline, retail, telecom) | Non-coding, tool-using, user-simulated, database end-state ground truth. Reports pass^k, the reliability metric agents actually need. | `support` (new) | DB diff, deterministic |
| **BFCL v4** multi-turn and agentic categories | Calibrates `toolcall.schema` and `toolcall.grounded` against labelled call validity, step by step | all | AST / state checks |
| **Terminal-Bench 2.0** | Long-horizon terminal tasks with containerised test checks, where cache and stickiness costs are largest | `coding` | task tests |
| **SWE-bench Verified** subset (50–100) | Parity with the parent RFC's coding pack and with TwinRouterBench's dynamic track only. OpenAI's February 2026 audit found material flaws in 59.4% of 138 hard problems and evidence of contamination, and OpenAI's July 2026 follow-up found about 30% of SWE-bench Pro tasks broken (§A2.4). Absolute numbers are therefore not headline numbers: only paired arm-vs-arm differences on the same tasks are reported. | `coding` | repo tests |
| **TwinRouterBench** static track | The only agent-routing benchmark with execution-verified per-step labels and cache-switch costs priced in. It scores Hydra's per-step rule (§A5.2d) offline, without running agents. | all | deterministic grader |
| **MCP-Universe** or **MCPMark** | Real MCP servers, execution-based checks: the protocol Hydra's ledger governs | `mcp` | execution checks |
| **AgentDojo** | Utility under attack and attack success rate, to measure §A9.2's injection layers, which are a separate axis from task success | `security` | built in |
| GAIA (validation set) | Optional. Needs web tools, and answers are exact-match | `research` | exact match |

WebArena and OSWorld are deferred: heavy environments, and computer-use traffic adds image-heavy context that the cache-cost model above has not been measured on.

### A10.2 Arms (baselines and treatments)

Every arm runs the **same client harness** (the suite's reference agent, pointed at a base URL) with seeds fixed. The routing changes; the agent does not.

| Arm | Description |
|---|---|
| **B1 frontier end to end** | One strong model, all steps, no Hydra. *The bar under `quality`.* |
| B2 cheap end to end | The cheapest model that completes the harness at all |
| B3 static fallback | B1 with HTTP-error failover, the LiteLLM/Portkey shape |
| B4 naive per-request routing | Hydra today: every step routed independently, no session |
| **T1 sticky session routing** | §A5.1 only: measured session-start choice, no per-step switches |
| **T2 T1 + step verifiers + escalation** | §A5.2a and §A6 |
| T3 T2 + SPRT on irreversible calls | §A7.2 |
| T4 T2 + per-step moves (§A5.2d), `balanced` | the TwinRouterBench-style arm; compared with T2 **live**, never by replay |
| R best-of-n trajectories (n = 3) + final verifier | the test-time-compute reference ceiling and its cost |

### A10.3 Metrics, per suite and per arm

- **Success:** pass@1. On τ²-bench, also **pass^k** for k = 1..4 (the probability that all k trials succeed).
- **Cost per successful task** = total spend ÷ successes. This is the headline under `balanced` and `cost`. Under `quality` the headline is success against B1, with cost reported beside it.
- **Steps** per task (median, p95), and steps per successful task.
- **Latency:** p50 and p95 per step (TTFT and total), and wall time per task.
- **Cache hit rate:** cached input tokens ÷ input tokens, per arm. Switching costs show up here first.
- **Switches** per session, and the measured `ε_inc` (step pass rate after a switch against no switch).
- **Escalation rate** and rescue rate (escalated steps whose session succeeded).
- **Step-verifier se/sp** against clock-1 labels and suite labels, with Brier/ECE of stated P(session success) from `internal/reliability`.
- **Safety:** AgentDojo utility and targeted attack success rate under T2 against T2 + §A9.2. Count of irreversible calls reaching execution without approval (must be 0).
- **Budget events:** sessions ended by each cap, and spend at termination.

### A10.4 Statistics and decision rule

- **No offline splicing.** Every arm runs live trajectories. A recorded trajectory is never continued on another model and scored: the Replay Gap shows such scores are invalid (§A2.2). Logged data is used only for session-level OPE (§A6.5).
- **Harness integrity.** Benchmark checkers have known flaws: τ-bench credited empty responses, and rule-based web checkers under-report success (ABC, [2507.02825](https://arxiv.org/abs/2507.02825); AgentRewardBench). Each suite runs with its maintainers' latest fixes, and the result files record the harness version.
- Paired by task. The success difference uses McNemar's test. Cost per success gets a percentile bootstrap CI (10k resamples), with at least 3 seeds per arm.
- **T1 becomes the default** for a class only if:
  - success is not worse than B1 at α = 0.05 (non-inferiority margin 2 points);
  - cost per success is lower, with the CI excluding zero.
- **T2 ships** only if it beats T1 on success, or on cost per success, by the same test.
- All results go into `bench/agents/`, including losses.

---

## A11. Risks

| Risk | Why it is real | Mitigation |
|---|---|---|
| **Single strong model wins end to end** | §A2.2's evidence is thin: the quality wins are small (75 vs 74) or vendor-reported, and Self-MoA argues against naive mixing. B1 may simply dominate on long-horizon suites. | That is a valid outcome. Under `quality`, T1 then picks B1's model per class and Hydra's value reduces to step verification, budgets and safety. The design does not depend on per-step switching paying off. |
| Session identification errors | Prefix matching can merge two clients that share an identical system+tools+first message (same tenant), or split a session on compaction | Chain root includes tenant and key. A header overrides. Merges affect only stickiness and budget attribution, never content. |
| Thinking / reasoning lock-in | Provider-bound reasoning state makes failover across providers fail or degrade | §A5.2c restricts failover to compatible heads; an explicit opt-in drops reasoning |
| Verifier false positives cost money | Each false reject regenerates a step at full prefix cost | Verifiers are calibrated sources. A low-sp verifier has little LLR and cannot trigger escalation alone. Deterministic checks start narrow (§A6.1). |
| Biased clock-1 labels | "Executed without error" ≠ "correct call" | Kept on a separate `/exec` key, used only as a prior (§A6.4) |
| Hydra becomes a conversation store | Responses `store: true` and Idempotency-Key put tenant content in Hydra | Off by default. Retention TTLs. The parent RFC's storage interface and egress rules apply. Counts-only sync unchanged. |
| The loop-termination rewrite breaks a client | One case where Hydra substitutes a response | Opt-out per route. Headers state it. Measured on the §A10 suites before default-on. |
| Approval gate deadlocks a client | A 403 ends the client's loop, and not every client resumes | Opt-in per route. Durable engines use signals. Documented per client. |
| Injection detection gives false assurance | Detectors are bypassed by adaptive attacks (§A2.4) | Detection is a calibrated source only. The trifecta gate is the control. |
| Verifier gaming | Agents reward-hack visible scorers (METR: 30.4% on RE-Bench). A `command` verifier running tests the agent can edit measures the agent's edits to the tests. | Pack verifiers run on Hydra-held copies of the oracle (tests pinned by hash at session start). A verifier whose inputs the agent wrote is flagged `reachable` and gets no LLR until measured. |
| Router trained on opinions | TwinRouterBench: a frontier model used as a router missed 140 of 147 steps that needed escalation | Only execution-verified outcomes and calibrated verifiers move posteriors. `judge.step` is a calibrated source, never the router. |
| Scope creep into a framework | Every gap above tempts "just run the loop server-side" | §A3 and §A8.3: Hydra executes no client tools and orchestrates no workflows |

---

## A12. Build order

| Order | Work | Exit criterion |
|---|---|---|
| 1 | Content-part arrays on Chat Completions. Streamed `RecoverToolCalls`. Exclude tool-bearing requests from the cache, or key on tools and calls. | A real OpenAI Agents SDK (Chat Completions mode) and LangGraph `ChatOpenAI` complete a τ²-bench airline episode through Hydra |
| 2 | `/v1/messages` with lossless thinking, `cache_control`, `count_tokens` | Claude Code completes a Terminal-Bench task through Hydra with cache hit rate within 5 points of direct |
| 3 | `session.Store`, prefix chain, step typing, `Quota{session:}` hard caps, session headers, runlog session root span + `gen_ai.conversation.id` | B4 vs T1 on τ²-bench and Terminal-Bench |
| 4 | `toolcall.schema`, `toolcall.grounded`, `loop`, `final.claims`; clock-1 `ApplyStepOutcome`; `/v1/feedback` with `session_id`; `ApplyTrajectoryOutcome` | T2 numbers; verifier se/sp published |
| 5 | Ledger on definitions and emitted calls; tool-result egress source; trifecta gate; approval gate + `/v1/sessions` approvals | AgentDojo numbers; zero unapproved irreversible calls |
| 6 | `/v1/responses` stateless, then stateful; `Idempotency-Key`; replay; Temporal sample | Codex and Agents SDK (Responses mode) unchanged through Hydra; a Temporal + Agents SDK workflow survives a worker kill with no double charge |

---

## A13. Unverified claims carried in this appendix

These are cited, but could not be confirmed from a primary source while writing. Re-check them before anything here is quoted publicly:

- **Second-hand only:**
  - τ²-bench telecom scores of about 98–99%;
  - Terminal-Bench 2.0 top score of 78.4%;
  - OSWorld-Verified around 90%;
  - SWE-bench Pro aggregator top score;
  - the MCP-Bench and MCPTox averages.
- **Self-reported and unreplicated:**
  - Anthropic's +90.2% for the multi-agent research system, and its 15× token figure;
  - Weave Router's savings and its Terminal-Bench parity claim;
  - Not Diamond's coding-agent accuracy and cost claims;
  - Requesty's cache hit rates.
- **Abstract-only or workshop evidence:**
  - AgentRouter's 72% / 97.3%: workshop paper, benchmarks unnamed;
  - SWE-Router's gains: no numbers in the abstract;
  - AgentCARD's +44% and 12× figures.
- **TwinRouterBench's dynamic result** (75 vs 74 at ~0.47× cost) is one run on 100 instances.
- **Market and project status:**
  - the Palo Alto Networks acquisition of Portkey closing;
  - Martian's router de-emphasis;
  - the Goose lead/worker mode status;
  - Temporal's OpenAI Agents integration GA status;
  - Restate's parallel-tool-call replay caveat;
  - the Microsoft Agent Framework's A2A/MCP support.
- **Hydra's own process:** v1.5.0 shipped fixes for #953/#960/#976/#957 while the issues stayed open. The commits are verified in `git log`; *why* the closing sweep did not fire was not investigated.
- **Assumed client behaviour:** that every SDK in §A8.2 surfaces a 403 without retrying. This holds for the OpenAI and Anthropic SDK default retry classes (408/409/429/5xx), but was not tested per client.
