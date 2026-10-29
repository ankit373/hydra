# RFC 0001: Hydra as a verified cascade gateway

- **Status:** Draft
- **Date:** 2026-10-29
- **Author:** ankit373
- **Supersedes:** the "Trust Control Plane for AI Development" positioning in `README.md`, `docs/index.html`, `docs/llms.txt`

## 1. Summary

Hydra is an inference gateway with OpenAI- and Anthropic-compatible endpoints.

- **Escalation, not prediction.** For each request it calls models cheapest-first and runs verifiers on their outputs. It stops at the first answer whose posterior probability of being correct, for that task class, crosses a target set by the caller. It does not try to predict up front which model will do best.
- **Learned from outcomes.** The posterior comes from a sensitivity/specificity matrix per (source, task class). The matrix is learned from verified outcomes, and its output is combined in a Wald sequential probability ratio test (SPRT).
- **Same binary everywhere.** One Go binary runs as a CLI, a sidecar, a self-hosted multi-tenant gateway, or the data plane of a managed control plane.

There is one-line category name for each product compared here:

| Product | What it is, technically | When it decides |
|---|---|---|
| ScaleDown CDM | Constrained-decoding task models (classify, extract, summarize) behind a metered API | At decode time, by restricting the output space |
| Jev / ScaleDown `/v1/scaledown` | "Decisions API": label selection as a service | At decode time |
| Not Diamond, RouteLLM, OpenRouter Auto | Learned predictor that picks one model per prompt | Before the call |
| LiteLLM, Portkey, Cloudflare/Vercel AI Gateway | Proxy with static fallback chains, keys and budgets | Before the call (rules), after it only on HTTP error |
| **Hydra** | **Verifier-gated model cascade with calibrated sequential stopping** | **After each call, on evidence about the output** |

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
| Cheapest competent head | `internal/rank` (`Cheapest`), `internal/dispatch` (`constrain`) | With no tier pinned, the head chosen is the cheapest whose posterior in-domain competence clears `trust.RequiredConfidence`, among heads with at least `MinCommitments` in-domain observations. |
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
3. **Client-neutral ingress.** Add `/v1/messages` (Anthropic) and `/v1/responses` (OpenAI Responses) next to `/v1/chat/completions`. Claude Code, Codex, Cursor and any SDK app become clients through `*_BASE_URL`, with no special casing.

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
```

Non-streaming responses also carry the same data as `hydra` metadata in the body.

**Streaming tension:** a cascade cannot stream the first source's tokens and then retract them. A route therefore picks one of three options:
- **`verify_then_stream`** (default for non-chat classes): buffer, verify, then stream.
- **`stream_unverified`**: stream source 1, verify afterwards, report through headers/trailer and `/v1/feedback`.
- **`stream_final_only`**: stream only once a source is accepted.

## 6. What we are not building

- **Task models.** CDMs, GLiNER and fine-tunes are sources to calibrate and route to, not something Hydra trains.
- **Gateway breadth for its own sake.** Provider count, UI polish and plugin marketplaces are where LiteLLM, Portkey (now Palo Alto) and Cloudflare already lead.
- **Prompt compression as a headline.** A `compress` stage can come later as a source transform. It is only worth shipping if a verifier gates it: compressed-prompt answers get calibrated like any other source.
- **The desktop app and TUI v2.** They are frozen until the gateway is adopted.

## 7. Claims require benchmarks

Nothing in §1 goes on the README until this harness publishes numbers.

**Metrics, per task class:**
- **Cost per accepted answer.**
- **Accepted-answer error rate against target α**, plus Brier, ECE and resolution from `internal/reliability`. This is the number that makes "P ≥ 0.95" mean something.
- **Escalation rate** and the mean number of sources consulted.
- **Added latency** at p50 and p95 over a single direct call.

**Baselines:**
- always-frontier
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
