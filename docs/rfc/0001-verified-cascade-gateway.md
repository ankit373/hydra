# RFC 0001: Hydra as a verified cascade gateway

- **Status:** Draft
- **Date:** 2026-10-29
- **Author:** ankit373
- **Supersedes:** the "Trust Control Plane for AI Development" positioning in `README.md`, `docs/index.html`, `docs/llms.txt`

## 1. Summary

Hydra is an inference gateway for any model from any provider: hosted APIs, local servers, CLI agents and task-specific models. It sends each task to the model *measured* as most capable for that task class, verifies the output, and escalates when the verification fails.

- **Measured capability, not brand or size.** The model for a request is the one with the highest posterior pass rate for that task class on verified outcomes. That is often not the largest model: a specialised extractor or classifier can beat a frontier model on its own task.
- **Verified, not assumed.** Verifiers run on the output. Hydra stops at the first answer whose posterior probability of being correct crosses the caller's target. Otherwise it escalates to the next-best model for that task class.
- **Cost is a choice, not the default.** Callers set an objective (§5.0). `quality` is the default; `cost` and `balanced` trade capability for spend explicitly.
- **Learned from outcomes.** The posterior comes from a sensitivity/specificity matrix per (source, task class). The matrix is learned from verified outcomes, and its output is combined in a Wald sequential probability ratio test (SPRT).
- **Same binary everywhere.** One Go binary runs as a CLI, a sidecar, a self-hosted multi-tenant gateway, or the data plane of a managed control plane.

There is one-line category name for each product compared here:

| Product | What it is, technically | When it decides |
|---|---|---|
| ScaleDown CDM | Constrained-decoding task models (classify, extract, summarize) behind a metered API | At decode time, by restricting the output space |
| Jev / ScaleDown `/v1/scaledown` | "Decisions API": label selection as a service | At decode time |
| Not Diamond, RouteLLM, OpenRouter Auto | Learned predictor that picks one model per prompt | Before the call |
| LiteLLM, Portkey, Cloudflare/Vercel AI Gateway | Proxy with static fallback chains, keys and budgets | Before the call (rules), after it only on HTTP error |
| **Hydra** | **Selection by measured capability per task class, verifier-gated escalation, calibrated sequential stopping** | **Before the call, from measured pass rates; after it, from evidence about the output** |

Hydra's job is to pick the model from the outside. It does not train or serve models. CDMs, GLiNER, local models and frontier models are all just *sources* that it calibrates and calls.

## 2. Why reposition

There are three problems with the current product, each visible in the code.

### 2.1 It is Claude-centric

**Where it shows:**
- 29 of 278 non-test Go files reference Claude.
- The one session-level budget the router governs is `claude_pct`, which has 99 references. It is the share of the Claude Code context window used, modelled as a drift-diffusion process with an absorbing barrier at 80% (`internal/budget/risk.go`).
- `Dispatcher.claudeMode` (`internal/dispatch/dispatch.go:534`, `:984`) reads `claude_pct` from `state.json` and downgrades the tier from it.

**Consequence:** the budget the gateway enforces only means anything to someone running Claude Code.

### 2.2 It is coding-specific

The core mechanisms do not depend on code. The parts that feed them do:

- **`trust.Task`** (`internal/trust/types.go`) prices a wrong answer with `BlastRadius`, `Irreversible` and `Production`. Those are fields about code changes.
- **`trust.DomainForFile`** (`internal/trust/domain.go`) derives the calibration domain from a *file extension*. A request with no file has no task class.
- **`verify.Command`** (`internal/verify/verify.go`) only resolves `go test ./...` or a per-extension validator from `workspace.yaml`.
- **The only `oracle.Oracle` implementation** is `CommandOracle` (exit code 0 = pass).
- **Packages that only make sense for a git workspace:** `edit`, `vet`, `diff`, `editor`, `workspace`.

### 2.3 It is local-only

**Where it shows:**
- State is roughly 25 JSON/JSONL files under `~/.hydra`. Among them:
  - `calibration.jsonl`, `trust.jsonl`, `trust_applied.jsonl`
  - `examples.jsonl` (evalset), `dispatch.jsonl`, `cost.jsonl`, `ledger.jsonl`, `rollups.jsonl`
  - `state.json`
  - …
- `hyctl serve` has one shared bearer token and exposes only `/v1/chat/completions` and `/v1/models` (`internal/serve/serve.go:101`, `:108`).
- There are no tenants, no per-key budgets, and no store that two processes can share.

**Consequence:** calibration can only ever learn from one user's traffic. That is the main thing limiting the routing quality, as §5.3 explains.

## 3. What Hydra already is, mechanically

Every mechanism below already exists in the code. The repositioning generalises their inputs; it does not invent new machinery.

| Mechanism | Where | What it computes |
|---|---|---|
| Calibration | `internal/trust/calibration.go` | A confusion matrix per `(source, domain)` with a Laplace pseudo-count prior (a new source starts at se = sp = 0.5). The likelihood ratio of a "correct" verdict is LLR = ln(se / (1 − sp)). |
| Outcome training | `internal/trust/outcome.go` | `ApplyRunOutcome` updates every source's matrix once ground truth arrives. This is the only path that moves specificity (TN). |
| Sequential stopping | `internal/trust/sprt.go` | Wald SPRT over hypotheses (clusters of equivalent answers). The acceptance boundary is A = ln((1 − α)/α) with α = 1 − `Target.Confidence`. A hard spend ceiling is `Target.MaxCostUSD`. Sources are sampled in order of expected evidence per dollar (`evidencePerCost`), with a correlation discount for sources in the same model family (`Source.Family`). |
| Verifiers as sources | `internal/oracle/oracle.go` | An oracle verdict is evidence weighted by that oracle's own calibrated se/sp, like any model. `oracle.Measured` refuses to use an oracle that has not been calibrated. |
| Labelled corpus | `internal/evalset/evalset.go` | Append-only examples of (task hash, domain, source, head, enum, tier, candidate, passed), with the embedding and the embedding model recorded. |
| Pass-rate prediction | `internal/classify/classify.go` | kNN (k = 10, at least 3 neighbours per enum) over evalset embeddings, giving a Laplace-smoothed pass rate per routing enum. |
| Counterfactual evaluation | `internal/ope/` | Self-normalised inverse-propensity estimator over sampled logs, used to score "always tier N", "always local", "cheapest head measured competent" and similar policies without running them. |
| Output calibration | `internal/reliability/` | Brier score with the Murphy decomposition (reliability − resolution + uncertainty), ECE and MCE, over pairs of (stated P(correct), recorded verdict). It refuses to report below `MinObservations`. |
| Measured competence per domain | `internal/rank` (`EffectiveScoreIn`, `SortMeasured`, `Cheapest`), `internal/dispatch` (`constrain`) | Beta posterior of each head's in-domain pass rate, with its prior from the catalogue score and other domains, bounded at `MinCommitments` pseudo-observations. `SortMeasured` ranks on it. **Today** `constrain` then picks the *cheapest* head that clears `trust.RequiredConfidence`; §5.0 makes that one objective among three. |
| Grounding check | `internal/ground/` | Flags an answer that states a number, path, flag or symbol found nowhere in the prompt's fenced context. Its verdict reaches SPRT through `oracle.LLR`. |
| Gateway plumbing | `internal/serve`, `executor`, `cache`, `policy`, `egress`, `otlp` | OpenAI-compatible serving with streaming and tools. Native executors for OpenAI-compatible, Anthropic, Gemini, Cohere, Bedrock, Azure and CLI heads. Exact plus gated near-match cache, PII redaction, egress gate, OpenTelemetry export. |

So the claim "Hydra returns an answer that is P ≥ 0.95 correct" means something precise: the accumulated log-likelihood ratio crossed ln(19) ≈ 2.944 nats, using per-source matrices for that task class. `internal/reliability` can then check whether stated probabilities match observed accuracy.

## 4. Vocabulary

From here on, docs, CLI help and the site say what the mechanism is.

| Stop saying | Say |
|---|---|
| Trust Control Plane | verified cascade gateway |
| trust, trust layer | calibration (per source × task class), verification |
| confidence of correctness | posterior P(correct) for the task class, target α |
| provable trust | stated P(correct), with its measured reliability gap published next to it |
| domain (file extension) | task class |
| heads | sources (models, CLI agents, verifiers, judges) |
| 70–85% savings | cost per accepted answer vs. baseline X on benchmark Y (§7), or nothing |

The Go package `internal/trust` keeps its name for now; renaming it is churn with no user-visible effect.

## 5. Design changes

### 5.0 Routing objective: capability first

Today both selection paths are cost-first. `dispatch.constrain` picks the cheapest head over the competence bar, and SPRT samples in order of evidence per dollar (`evidencePerCost`). That answers "what is the cheapest acceptable answer", not "what is the best model for this". The objective becomes explicit, per route or per request (`X-Hydra-Objective`):

| Objective | First source | Escalation order | Use when |
|---|---|---|---|
| `quality` (default) | argmax P̂(pass \| head, task class) | descending P̂(pass), ties broken by latency, then price | correctness matters more than spend |
| `balanced` | argmax P̂(pass) − λ·cost, with λ set per route in $/point of pass rate | same utility | there is a known cost of being wrong (`DefectCostUSD`): λ = 1 / DefectCostUSD |
| `cost` | cheapest head with P̂(pass) ≥ target (today's `constrain`) | ascending price among eligible heads | high volume, low stakes |

**P̂(pass) comes from three sources, in order of how specific they are:**
1. `rank.EffectiveScoreIn` for (head, task class) when the head has `MinCommitments` observations there.
2. `classify.PredictPass` over the k nearest verified examples when the task class is resolved by embedding.
3. The catalogue capability score as a prior (`internal/capabilities`), marked `unmeasured` in the response so it never passes for a measurement.

**Changes:**
- `trust.Run`'s `sampleOrder` takes the objective. Under `quality` it orders by discrimination D (how much evidence a source's verdict carries) and P̂(pass), not D per dollar.
- `ope` gets an `ArgmaxMeasured` policy, so `quality`, `balanced` and `cost` can be compared from the same logs before any of them is made the default for a route.
- Exploration (`explore_rate`) stays the way unmeasured heads earn observations. Under `quality` that is the only path for a new or small model to prove it beats a larger one on a task class.

### 5.1 Make it provider-neutral (fixes 2.1)

1. **Replace `claude_pct` with provider-neutral quotas.**
   ```go
   type Quota struct {
       Scope       string        // "provider:anthropic", "key:<id>", "tenant:<id>", "session:<id>"
       Window      time.Duration // 0 = lifetime
       LimitUSD    float64
       LimitTokens int64
       // ContextFrac applies to any long-lived agent session, not just Claude Code.
       ContextFrac float64
   }
   ```
   - Keep the drift-diffusion risk model in `internal/budget/risk.go`, but feed it any `Quota` series.
   - `Dispatcher.claudeMode` becomes `quotaMode`, with the same downgrade logic, keyed by scope.
   - Migrate `claude_pct` in `state.json` to `Quota{Scope: "session:<id>", ContextFrac: ...}`.
2. **CLI agents are one executor kind among the others.** Claude Code, Codex and Gemini CLI are `executor/cli.go` sources. They are not the default entry point.
3. **Client-neutral ingress.** Hydra accepts the wire formats clients already speak. That is a compatibility detail, not the product: the model behind any of them can come from any provider.
   - Chat Completions (exists)
   - Messages (new)
   - Responses (new)
   - Gemini `generateContent` (new)

   Any tool or SDK reaches Hydra by pointing its base URL at it, with no special casing.

### 5.2 Make it domain-neutral (fixes 2.2)

1. **Task class instead of file extension.** Resolve it in this order:
   1. an explicit `X-Hydra-Task-Class` header or `metadata.task_class`
   2. kNN over the request embedding against evalset (`classify.Corpus.Near`), accepted only if the neighbourhood reaches `MinNeighbours`
   3. the route default
   4. `"default"`

   `DomainForFile` stays as the resolver inside the coding pack.
2. **Defect cost as a number, not code fields.**
   - Add `Task.DefectCostUSD`, set per route or per request.
   - `BlastRadius`, `Irreversible`, `Production` and `TouchesPII` move into a coding-pack function that *computes* `DefectCostUSD`.
   - SPRT's stopping rule is unchanged. The defect cost only enters through a route's choice of α, so the rule can later be extended to minimise expected cost (spend + P(wrong) × DefectCostUSD).
3. **Pluggable verifiers.** Keep the `Oracle` interface. Add `Score` to `Verdict` for graded checks; `Passed` stays the calibrated bit:
   ```go
   type Verdict struct {
       Passed bool
       Score  float64 // optional, [0,1]; never used in place of Passed for LLR
       Detail string
   }
   ```
   Every verifier is calibrated like a model, under `verifier:<kind>:<id>`. A weak verifier contributes little because its se/sp is low; it is not trusted by fiat.

   | Verifier | Task classes | Check |
   |---|---|---|
   | `command` (exists) | code | exit code of a test suite, compiler or linter |
   | `jsonschema` | extraction, structured output, tool args | output validates against the request's `response_format` schema |
   | `match` | classification, closed-form QA | exact or normalised match against a label set or regex |
   | `grounded` (`internal/ground` exists today) | RAG, summarisation | Today: no number, path, flag or symbol absent from the context. Extension: sentence-level BM25 support over `internal/retrieve` |
   | `judge` | open-ended text | an LLM judge with a rubric. **It is just another source**, so its se/sp is learned from outcomes like any other, which keeps judge bias measured rather than assumed |
   | `http` | anything | POST `{task, candidate}` to the customer's endpoint and read `{passed, score}` |
   | `agreement` (exists implicitly) | any | equivalent answers from independent families, already the SPRT hypothesis clustering |

4. **A delayed-outcome API.** `POST /v1/feedback {request_id, outcome: correct|incorrect, source?}` → `trust.ApplyRunOutcome`.
   - This is the general form of "tests passed later", "user accepted", "ticket reopened" or "refund issued".
   - It is also the only route to non-trivial specificity, as `outcome.go` explains: generator-only writes leave sp ≤ 0.5.
5. **Coding becomes a pack.**
   - `edit`, `vet`, `diff`, `editor`, `workspace`, `verify` and the `command` oracle move behind `packs/coding`.
   - New packs: `packs/extraction` (jsonschema + match), `packs/rag` (grounded + judge), `packs/classification` (match + feedback).
   - A pack is: a task-class resolver, a default verifier set, a defect-cost function and default α.

### 5.3 Make it deployable anywhere (fixes 2.3)

1. **Storage interface.** Put interfaces in front of the jsonl files. The jsonl backend stays the default for a single user; SQLite and Postgres are for shared deployments.
   ```go
   type CalibrationStore interface { Update(ctx, tenant, source, class string, saidCorrect bool, o Outcome) error; Rates(ctx, tenant, source, class string) (se, sp, n float64, err error) }
   type EvalStore        interface { Add(ctx, tenant string, e evalset.Example) (bool, error); Near(ctx, tenant string, vec []float32, k int) (Neighbourhood, error) }
   type RunLog           interface { Append(ctx, tenant string, r runlog.Record) error }
   type CostLedger       interface { Charge(ctx, scope string, usd float64, tokens int64) (Quota, error) }
   ```
   Postgres with pgvector backs `EvalStore.Near`; the in-memory brute-force kNN stays for jsonl.
2. **Tenancy and keys.**
   - Virtual keys map to `(tenant, project, quotas, allowed sources, egress policy)`.
   - The calibration key becomes `(tenant, source, class)`.
3. **Hierarchical calibration**, the technical reason a shared deployment routes better than a laptop.
   - Each tenant's matrix uses a Beta prior centred on the pooled global estimate for `(source, class)`. Its strength is m pseudo-observations instead of the flat Laplace prior.
   - A new tenant therefore starts from population se/sp, not 0.5, and moves to its own data as n ≫ m.
   - `m` is fit by empirical Bayes per class.
   - **Only confusion counts are pooled.** Prompts, candidates and embeddings never leave the tenant. That makes counts-only sync to a managed control plane possible without moving data.
4. **Three deployment modes, one binary.**

   | Mode | Process | Store | Auth |
   |---|---|---|---|
   | `embedded` | `hyctl` CLI | jsonl in `~/.hydra` | none (loopback) |
   | `gateway` | `hyctl serve --mode gateway` behind a load balancer | Postgres + pgvector | virtual keys, OIDC for admin |
   | `managed` | `gateway` data plane in the customer's VPC + hosted control plane | local Postgres; counts-only sync up | control-plane-issued keys |

### 5.4 The response contract

Every response carries the evidence, so a caller can act on it rather than take it on faith.

```
X-Hydra-Request-Id:   <id>                  # key for /v1/feedback
X-Hydra-Task-Class:   extraction.invoice
X-Hydra-P-Correct:    0.962                 # posterior at stop
X-Hydra-Target:       0.95
X-Hydra-Decision:     accept | budget_exhausted | no_evidence
X-Hydra-Sources:      ollama/qwen3:8b, verifier:jsonschema
X-Hydra-Cost-USD:     0.00031
X-Hydra-Escalations:  0
X-Hydra-Objective:    quality
X-Hydra-P-Pass-Basis: measured | neighbours | catalogue_prior
```

Non-streaming responses also carry the same data as `hydra` metadata in the body.

**Streaming tension:** a cascade cannot stream the first source's tokens and then retract them. A route therefore picks one of three options:
- **`verify_then_stream`** (default for non-chat classes): buffer, verify, then stream.
- **`stream_unverified`**: stream source 1, verify afterwards, report through headers/trailer and `/v1/feedback`.
- **`stream_final_only`**: stream only once a source is accepted.

### 5.5 Context compression

Full research, failure catalogue, economics and design: [`0001-appendix-compression.md`](0001-appendix-compression.md).

Compression is a transform on a source's input. Its effect on P(pass) is measured per (task class, strategy, ratio). It is never enabled because a paper or vendor reported a ratio.

**Why the bar is high.** No published work tests instruction retention, negation or number fidelity under compression. What is published is damaging:
- Token-level deletion corrupts numbers and identifiers (LLMLingua's own demos).
- It collapses agent action grammar (AGORA: 17 of 17 setups).
- It can be steered by untrusted input to strip guardrails (COMA: 44–63% success).

Hydra therefore never ships token-level deletion.

**Strategy ladder, safest first:**

| Rung | Transform | Default |
|---|---|---|
| 1a/1b | Exact dedup; whitespace outside code and indentation | On, after a shadow period |
| 1c | Stale tool output → retrieval handle + `hydra_fetch` tool (observation masking: ~−50% cost at equal SWE-bench solve rate) | Per class, after A/B |
| 2 | Deterministic tool-output compaction (logs, JSON, listings) | Per class, after A/B |
| 3 | Query-aware sentence extraction (BM25 + RRF, `internal/retrieve`) on the uncached tail | RAG classes, after A/B |
| 4 | Abstractive summary of old history | Off; never default |

**Invariants:**
- **Protected spans are byte-identical:**
  - system and developer messages
  - the latest user turn
  - tool schemas and arguments, and the last K tool results
  - numbers with units, identifiers, quoted strings and code fences
  - any sentence containing a negator or modal
  - anything a verifier checks
- **Every removal is reversible.** The original goes to a session-scoped vault and the head can fetch it. If the vault fails, nothing is removed.
- **A pre-send coverage check runs on every compressed request.** If any check fails, the original is sent instead.
  - Protected spans are present, in order.
  - Every sentence containing a query atom is kept or handled.
  - No new number or identifier appears.
  - No sentence lost a negator.
  - The injection scan is re-run on the compressed text.
- **Escalation after a failed verification resends uncompressed.** Verifiers judge against the original.
- **The cached prefix is never edited.** At current cache prices, re-compressing a cached prefix only pays above 92–98% removal. History folded once is memoised and becomes the new byte-stable prefix.

**Quality gate,** per (class, strategy, ratio bucket):
1. **Shadow:** log the plan, send the original.
2. **Offline:** paired A/B on a replay corpus.
3. **Online:** randomised at `compress.explore_rate`, with propensities logged for `internal/ope`.
4. **Promote** only if the one-sided 95% lower bound on Δpass is above −δ, where δ = 0.01, or 0.005 for extraction and classification.
5. **Roll back automatically** if the SPRT on discordant pairs accepts harm, or the coverage-failure or fetch rate doubles.

A compressed attempt calibrates under `<head>+c:<strategy>@<bucket>`, sharing the head's `Family`.

**Response contract** (extends §5.4):
- `X-Hydra-Compress`: `off | shadow | applied | fallback`
- `-Strategy`, `-Ratio`, `-Removed`, `-Coverage` and `-Basis`
- `hydra.compression` in the body lists every removed span with its handle and sha256.

**Prerequisites.** Cache plumbing first:
- `cache_control` on the Anthropic executor and `prompt_cache_key` on OpenAI
- cache read/write token counts in `executor.Response`; today `anthropicUsage` reads none
- cache rates in `pricing.ModelPrice`
- content-part messages, since `executor.Message.Content` is a string
- a replay store of (request, verifier) pairs, since `evalset` keeps no prompts

**Off when:**
- the class has no measured result
- the uncached tail is under 4k tokens
- the cost model says it doesn't pay
- the class is creative, translation, legal or medical
- the head can't use tools
- the client already compacts
- the route streams unverified

### 5.6 Task-specific models

Full design: [`0001-appendix-task-models.md`](0001-appendix-task-models.md).

Task-specific models (ScaleDown Classify/Extract, GLiNER-family encoders, NLI and fine-tuned classifiers, rerankers) are sources like any other: calibrated per (source, task class), selected by measured P̂(pass), and escalated past on failure.

**Why per class.** Published results point both ways:
- GLiNER2 matches GPT-4o on zero-shot NER (0.590 vs 0.599 F1 on CrossNER).
- It loses on fine-grained classification (0.70 vs 0.78 on Banking77).
- Fine-tuned small models beat zero-shot LLMs outright.

So no rule can replace per-class measurement.

**Design:**
- **Typed ingress:**
  - `/v1/classify` and `/v1/extract`, Hydra-native, with per-field evidence in the response.
  - `/v1/rerank` (Cohere shape) and `/v1/embeddings` (OpenAI shape).
  - Chat completions with `response_format: json_schema`, but only together with an explicit `X-Hydra-Task`.
  - `/v1/decisions` once that API leaves beta.
- **Embeddings are never cascaded.**
- **`internal/task`** defines typed `Request`/`Result` and adapters per wire dialect: ScaleDown, Jev, TEI, Cohere rerank, a local GLiNER HTTP sidecar, and `llm-structured` for any chat head. A head declares `Caps` in `registry/task_models.yaml`.
- **Normalisation:**
  - Answers decompose into claims, one per label or field, compared by exact equality after deterministic canonicalisation.
  - `trust.RunClaims` runs SPRT per claim key at α/k.
  - A head's native softmax only selects a calibration cell. It is never evidence on its own.
- **Verifiers:** `jsonschema`, `label_set`, `span_in_source`, `field_checks`, cross-family `agreement`, and per-field `/v1/feedback`.
- **Earning observations.** Under-observed cheap heads run as shadow co-votes. They do not change the served answer and are trained once outcomes arrive.
- **Licences are a routing constraint.** Non-commercial checkpoints are ineligible unless a route sets `allow_noncommercial`.

### 5.7 Agents and multi-step workflows

Full design: [`0001-appendix-agents.md`](0001-appendix-agents.md).

Hydra stays a gateway. It executes no client tools and orchestrates no workflows; it becomes trajectory-aware at the one point it sits on.

1. **Sessions.**
   - The session id comes from, in order: `X-Hydra-Session`, client-native headers (`x-claude-code-session-id`), Responses `previous_response_id`, or a rolling hash of the append-only history.
   - Each request is typed from its shape alone: `plan`, `act`, `act_forced`, `interpret`, `single`.
   - Calibration keys become `<task class>/<step type>` and `<task class>/session`.
2. **Routing per session first, per step by exception.**
   - The incumbent model is chosen at session start by argmax P̂(session success). This is the only place exploration happens.
   - The incumbent changes only when:
     - a step verifier fails, in which case Hydra escalates inside the same request;
     - at a turn boundary, a measured gain exceeds the cost of re-reading the prefix uncached plus an inconsistency margin;
     - the head fails, and failover goes only to heads that can read the session's reasoning state.
   - Per-step moves are allowed only under `balanced`/`cost` objectives, and are judged on live runs, never on log replay.
3. **Step verifiers are sources.**
   - `toolcall.schema`, `toolcall.grounded`, `loop`, `progress` and `final.claims` are each calibrated.
   - Ground truth arrives on two clocks: the next request's tool result, and `/v1/feedback {session_id}`.
   - `trust.ApplyTrajectoryOutcome` trains only what an outcome can attribute.
4. **Session budget.**
   - `Quota{Scope: "session:<id>"}` with `MaxUSD`, `MaxSteps`, `MaxToolCallsPerTool` and `MaxWall`.
   - A hard stop returns 403 `hydra_session_limit`, never 429 or 5xx, which SDKs would retry.
5. **Safety.**
   - A per-key `ledger` filters the tool list, frozen per session, and checks every emitted call.
   - Tool results become an `egress.Source`.
   - When private data, untrusted content and an exfiltration-capable tool are all present in a session, calls to that tool need approval.
   - Irreversible calls park in `pending`.
6. **API.**
   - `/v1/messages` and `/v1/responses` are lossless: tool-call ids, parallel calls, argument deltas, thinking/reasoning items and `cache_control` all pass through.
   - Chat Completions gains content-part arrays.
   - For Claude Code, Hydra routes among Claude models and hosts only.
7. **Durable workflows: integrate, do not build.**
   - Temporal is the reference; Restate is the single-binary alternative.
   - Hydra adds `Idempotency-Key`, byte-exact replay, approvals as signals, and session id = workflow id.
   - `internal/workflow` stays the CLI's linear runner.

**Evaluation.**
- Suites: τ²-bench (pass^k), BFCL v4 multi-turn, Terminal-Bench 2.0, TwinRouterBench, MCPMark, AgentDojo, and a SWE-bench Verified subset reported only as paired differences.
- **Default rule:** sticky routing becomes a class's default only if it is non-inferior to frontier end to end, within a 2-point margin, and has a lower cost per success.

### 5.8 Pricing and open-core split

Full analysis: [`0001-appendix-pricing.md`](0001-appendix-pricing.md).

**Principle.** Everything a single developer or a single self-hosted team needs is open. What only an organisation needs is paid.

**Licence.**
- Relicense the core from MIT to Apache-2.0, for the patent grant. The CLA permits it.
- Then switch from the CLA to the DCO, and publish `STEWARDSHIP.md`:
  - no open feature ever moves to paid
  - no limits in the open core
  - sync and telemetry stay opt-in
- Paid code lives in a separate repo. An `ee/` directory appears only on demand.
- Rejected: BSL/FSL, ELv2, AGPL.

| Open (Apache-2.0) | Paid |
|---|---|
| Calibration, SPRT, all objectives | Hosted control plane for `managed` mode |
| Every verifier and pack, `/v1/feedback`, response contract | Cross-org pooled-calibration network (free to contributors; read-only and private cohorts on Enterprise) |
| `ope`, `reliability`, the §7 harness | SAML, SCIM, enforced SSO, custom RBAC |
| Storage incl. Postgres, tenancy, keys, quotas | Audit export, retention, legal hold |
| Hierarchical calibration within a deployment | Multi-region control plane |
| OIDC with built-in roles, local audit log, Docker, Helm, single-region HA | SLA, DPA, named support |

**Billed unit and tiers.**
- **Unit:** one cascaded request, counted once however many escalations it used. BYOK only, with no fee on tokens.

| Tier | Price |
|---|---|
| Self-hosted | $0 |
| Cloud Free | 50k requests/mo |
| Team | $99/mo including 500k requests, then $10 per 100k |
| Enterprise | from $2,000/mo |

**Savings-share.** An Enterprise option, offered only after §7 numbers exist: about 15% of the measured saving against a pinned baseline model, on these terms:
- both sides priced at the same day's list prices
- a per-class quality gate via `ope.Evaluate`
- a shadow sample of at least 5% sent to the baseline model
- a task class with ESS < 10 bills zero
- the customer can recompute the invoice

**Pooled network.**
- Opt-in and counts-only.
- A cell is published only with at least 5 contributors.
- Each contributor's weight is capped.
- Custom task classes never leave the deployment.

**Sequencing.**
1. Charge nothing, ship §8 and the §7 benchmarks.
2. Paid design partners.
3. Hosted Free and Team tiers.
4. Enterprise on demand.

No hosted data plane holds provider keys until there is a second maintainer.

## 6. What we are not building

- **Task models.** CDMs, GLiNER and fine-tunes are sources to calibrate and route to, not something Hydra trains.
- **Gateway breadth for its own sake.** Provider count, UI polish and plugin marketplaces are where LiteLLM, Portkey (now Palo Alto) and Cloudflare already lead.
- **Prompt compression as a headline.** A `compress` stage can come later as a source transform. It is only worth shipping if a verifier gates it: compressed-prompt answers get calibrated like any other source.
- **The desktop app and TUI v2.** They are frozen until the gateway is adopted.

## 7. Claims require benchmarks

Nothing in §1 goes on the README until this harness publishes numbers.

**Metrics, per task class and per objective:**
- **Pass rate against always-frontier.** This is the headline metric under `quality`.
- **Cost per accepted answer.**
- **Accepted-answer error rate against target α**, plus Brier, ECE and resolution from `internal/reliability`. This is the number that makes "P ≥ 0.95" mean something.
- **Escalation rate** and the mean number of sources consulted.
- **Added latency** at p50 and p95 over a single direct call.

**Baselines:**
- always-frontier, the bar for `quality`: Hydra must match or beat its pass rate per task class
- always-cheapest
- a static fallback chain on HTTP error
- a threshold router on the cheapest model's own log-prob or self-reported confidence

**Suites, one per pack:**

| Pack | Suite | Verifier |
|---|---|---|
| coding | SWE-bench Verified subset (50–100 tasks) | repo tests |
| classification | CLINC150, Banking77 | label match |
| extraction | a public gold-field set (e.g. CORD receipts) | jsonschema + field match |
| rag | a QA set with gold passages | grounded + judge |

Results, configs and seeds go into `bench/` and are published even when they are unflattering.

## 8. Plan

| Window | Work | Exit criterion |
|---|---|---|
| Days 0–30 | Freeze features. Fix data-loss and egress bugs (#1150, #996, #1019, #1023, #977), and disable the cache by default until #1154–#1156 are fixed. `Quota` replaces `claude_pct`. Task-class resolver. `jsonschema`, `match` and `http` verifiers. `/v1/feedback`. `/v1/messages`. The response contract headers. | Classification and extraction benchmarks run end-to-end with no Claude-specific code on the path. |
| Days 31–60 | Storage interfaces with SQLite and Postgres/pgvector backends. Virtual keys, tenants and per-key quotas. `grounded` and `judge` verifiers. Coding moves into `packs/coding`. Streaming modes. Docker image and Helm chart. | One Postgres-backed gateway serves two tenants with isolated calibration. Benchmarks for all four packs are published. |
| Days 61–90 | Hierarchical (empirical-Bayes) calibration. OPE-driven route policy selection per task class (`internal/ope`). Counts-only sync to a control plane. OIDC admin, audit export from `internal/ledger`, per-tenant egress policy. Hosted beta (bring your own keys). | A new tenant's first-week reliability gap is smaller with pooled priors than with flat priors, measured. |

## 9. Open questions

1. **Equivalence for free-form answers.** SPRT clusters answers with `AnswerEquivalence`. Exact match fits classification; extraction needs field-wise comparison; prose needs an embedding threshold, which is itself a source of error that should be calibrated.
2. **Verifier independence.** A `judge` from the same family as the generator is correlated with it. Extend `Source.Family` to verifiers so that correlation discount applies there too.
3. **Cold start without outcomes.** Before any `/v1/feedback`, specificity is stuck at the prior. Should packs ship seed matrices from the §7 benchmarks?
4. **Pricing a hosted tier.** Per request, or a share of the measured saving against the always-frontier baseline. The second only works once §7 numbers exist per tenant.
