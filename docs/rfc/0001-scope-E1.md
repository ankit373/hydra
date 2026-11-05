# RFC 0001: scope for E1, core generalisation

Epic: ankit373/hydra#1203. Scope ticket: #1204 (E1.S). Hypothesis register: #1196 (E0.1). Eval: #1213 (E1.E).
Status: DRAFT (scope stage). Verified against `docs/rfc-0001-verified-cascade-gateway` at `b1db9d1`, which is rebased on `develop` `b76d105` (#1314) with nothing on `origin/develop` ahead of it.

**Goal of checkpoint A.** E7's harness (#1197) can measure **H1** (`quality` is non-inferior to always-frontier) and **H3** (`cost` is cheaper per accepted answer) end to end through `hyctl serve`, on one non-coding task class. Everything else waits for checkpoint B.

**One finding drives most of this scope.** Today no measured routing reaches `hyctl serve` at all (§7, C1):

- serve always pins a tier;
- serve passes no domain;
- no verifier verdict reaches any dispatch path.

So the smallest slice is not "add an objective". It is "make the serve path reach the router's measured ordering, give it a task class and a verifier, and report what happened".

## 1. In scope for the first layer (checkpoint A)

**Task class for A: `classification`, suite Banking77.** CLINC150 runs on the same machinery at no extra build cost, so it is a second row rather than a second class of work. The class is named per suite (`classification.banking77`, `classification.clinc150`) and supplied by header. CORD, and with it extraction, moves to B (§2).

| Ticket | Parts in A | Parts not in A |
|---|---|---|
| **#1207 (E1.2) Quota** | `budget.Quota` type; `session:` scope only; `claudeMode` → `quotaMode` with identical downgrade logic; a **read alias**, so `claude_pct` in `state.json` feeds `Quota{Scope:"session:claude-code"}` (§5); `hyctl status` prints quotas by scope. Required for threshold T1. It also matters for correctness: `claudeMode`'s emergency branch forces `LocalOnly` on **every** dispatch, serve included (`internal/dispatch/dispatch.go:536-539`), so a stale `claude_pct ≥ 80` in the operator's `state.json` would silently turn a benchmark run local-only. | `provider:`, `key:` and `tenant:` scopes, and `LimitUSD`/`LimitTokens` enforcement. Those need E2's keys/tenants and a cost ledger (`CostLedger`, §5.3.1). |
| **#1208 (E1.3) task class** | A resolver with basis, in this order: `X-Hydra-Task-Class` / `metadata.task_class` → route default (`hyctl serve --task-class`) → `DomainForFile` when a file is named → `"default"`. `Options.TaskClass` reaches `routingDomain` (`dispatch.go:1585`). The class and its basis are echoed. The regression test for "existing coding dispatches resolve the same domain" is included, and costs nothing because the coding path is unchanged. | The kNN step (`classify.Corpus.Near`) and H4, because `classify` cannot yet return a class (§7, C6). `Task.DefectCostUSD`, whose only consumer outside the coding pack is `balanced`. |
| **#1206 (E1.1) objective** | `Objective` = `quality` (default) and `cost`; `X-Hydra-Objective` header and a per-route default; unknown value → 400. `quality` = order by `rank.EffectiveScoreIn` for the resolved class, ties broken by price. `cost` = today's `rank.Cheapest` via `constrain`. **serve stops pinning a tier when the client named no routing key** (`cmd/hydra/serve.go:178-179`), so the objective applies on the serve path; `hydra/<enum>`, `hydra/t<N>` and head ids still pin. `X-Hydra-P-Pass-Basis` ∈ {`measured`, `catalogue_prior`}, where `measured` means `InDomainTotal ≥ rank.MinCommitments` (20). | `balanced`; the `neighbours` basis (`classify.PredictPass` is per enum, not per head, §7 C6); `trust.sampleOrder` taking the objective, since serve does not run the SPRT in A; `ope.ArgmaxMeasured`, which is unidentifiable on logs written at `explore_rate = 0` (§2); the latency tie-break, which needs a per-head latency read from `rollup`. |
| **#1209 (E1.4) verifiers** | `Verdict.Score`; verifier source ids `verifier:<kind>:<id>`; a `match` verifier, built per request like `ground.Oracle` (a label set or regex from the route config, exact or normalised). **Verifier-gated escalation on the single-dispatch path.** The posterior is prior log-odds from P̂(pass) plus Σ `oracle.LLR` over verifier verdicts. Stop at the target, at `MaxCostUSD`, or at a max escalation count; otherwise try the next source in objective order. An uncalibrated verifier contributes 0 (`oracle.Measured`). No ticket owns this loop today (§7, C3). It is assigned here because it is the verifier's only consumer. | `jsonschema` (needs `response_format`, which serve drops today, §7 C10) and `http` (needs the egress gate work). Both belong to extraction and customer checks, which are B. `Source.Family` for verifiers (§9 Q2), which matters only with a `judge`. |
| **#1210 (E1.5) contract** | Headers on **buffered** `/v1/chat/completions`: `-Task-Class`, `-P-Correct`, `-Target`, `-Decision`, `-Sources`, `-Cost-USD`, `-Escalations`, `-Objective`, `-P-Pass-Basis`, plus the same data under `hydra` in the body. Keep the existing `X-Request-Id` (#1174) as the request id; do not add `X-Hydra-Request-Id` (§7, C9). Write one `trust.RunLog` per served request, holding the source's and the verifiers' evidence, so that B's `/v1/feedback` has a ledger to replay (§7, C8). | `POST /v1/feedback` and H5. In A, the harness fills calibration with the existing `hyctl trust record --said-correct` on its calibration split, which is enough for H1/H3. Trailers on streamed responses. |
| **#1213 (E1.E)** | Checkpoint A run: thresholds T1, T2 (H1) and T4 (H3) are gates; H2 and H17 are reported but do not gate (§4). | Everything else is gated at B. |

**What E7's harness needs from E1 in A:**

- `hyctl serve` reachable with `model: "hydra"`;
- headers `X-Hydra-Task-Class` and `X-Hydra-Objective`;
- response headers giving cost, sources, decision and basis;
- a route config that names the `match` verifier and the label file per class;
- calibration filled offline through `hyctl trust record` on the calibration split, at ≥ 20 judged answers per (head, class) for any head expected to read as `measured`.

## 2. Deferred to the full build (checkpoint B), with reasons

### 2a. The rest of the first layer: built in parallel, not gating A, measured at B

| Item | Reason for deferring from A |
|---|---|
| #1208 kNN resolver step + H4 | `classify.Vectored` keeps only `Vec`, `Enum` and `Passed` (`internal/classify/classify.go:49-53`). `Load` drops any example with no `Enum` (`:96`). A neighbourhood therefore carries no class label to vote with, and the extension is needed first. It also needs an embedding model on the machine (`internal/embed`); without one the resolver always abstains. Not needed when the harness sends the class by header. |
| #1208 `Task.DefectCostUSD` | Its only new consumer is `balanced`. The coding path already prices defects in dollars (`trust.DefectModel.CostUSD`, `internal/trust/defect.go:92`). |
| #1206 `balanced` + `ope.ArgmaxMeasured` + `sampleOrder(obj)` | `balanced` needs `DefectCostUSD`. OPE of a different argmax policy is unidentifiable on logs written at the default `explore_rate = 0` (`internal/ope` refuses that class), so the harness measures the arms directly in A. `sampleOrder` only matters once serve runs the SPRT (the `--confidence` path and B's `stream_*` modes). |
| #1206 `neighbours` P̂ basis | `classify.PredictPass` (`classify.go:269`) gives a pass rate per **enum**, not per head. It needs a per-head variant over the same neighbourhood. |
| #1209 `jsonschema`, `http` | Extraction (CORD) and customer verifiers. `jsonschema` needs `response_format` parsed by serve (§7, C10). `http` needs the egress-gate and no-redirect work. |
| #1210 `/v1/feedback` + H5; streamed headers and trailers | Calibration for A is filled offline. The feedback endpoint needs the per-request `RunLog` that A writes, so it lands immediately after. Streaming-mode semantics are #1226. |
| #1211 (E1.9) content parts | The harness sends string content. It is on E3's and E5's critical path (#1242 is blocked on it), so **start it early, in parallel with A**. It is simply not needed for H1/H3. |
| #1212 (E1.10) `/v1/messages` | Not needed for a classification benchmark. Its measurement (Claude Code cache-hit rate within 5 pts of direct) needs #1242's `cache_control` passthrough, so it should land **after** #1242 (§6). |
| CORD row (extraction) | Needs `jsonschema` plus field-wise comparison. |

### 2b. Full-build tickets, gated by #1214 (E1.R) as already filed

| Ticket | Reason |
|---|---|
| #1224 (E1.6) extraction/classification/rag packs, `grounded`-BM25, `judge` | Depends on H1 holding (#1214). Note that `judge` can build on the shipped `oracle.PredicateOracle` + decision heads (#1305, #1309) rather than starting from scratch. |
| #1225 (E1.7) `packs/coding` | A refactor with no measurable claim. Doing it before A would put churn in exactly the files A edits (`dispatch`, `trust`, `oracle`). |
| #1226 (E1.8) streaming modes | Streaming exists (`internal/serve/stream.go`), so A only needs buffered. The per-mode TTFT report is B. |
| #1227 (E1.11) `/v1/responses` stateless | Only agent clients need it. Measured in E5 (#1274). |

## 3. Out of scope, with reasons

| Item | Reason |
|---|---|
| Gemini `generateContent` ingress | No client in agents A8.2 needs it. Follow-up after #1214 if one does. **(Decision for #1204's AC: out.)** |
| §9 Q1, equivalence for free-form answers | Not needed by any E1 class. Classification compares exactly. A's cascade is one source plus verifiers, so it never clusters answers. Field-wise extraction belongs to E4's claim decomposition (`trust.RunClaims`), and prose to `rag` (#1224, E4). **(Decision for #1204's AC: out of E1.)** |
| Expected-cost stopping rule (§5.2.2 "later") | It changes the SPRT rule. E1 keeps the rule and only feeds α. |
| Renaming `internal/trust` | §4: churn with no user-visible effect. |
| Tenant in the calibration key; `key:`/`tenant:`/`provider:` quota enforcement | Owned by E2 (#1228). E1 keeps the `(source, class)` key. |
| Sessions, stickiness, `X-Hydra-Objective: pin`, step types | Owned by E5 (#1268). |
| Compression headers, cache plumbing | Owned by E3 (#1241, #1242). |
| Seed matrices for cold start (§9 Q3) | Owned by E7.R (#1220). |
| Migrating the TUI and desktop off `claude_pct` | Frozen by RFC §6. They keep reading the legacy key, which stays mirrored (§5). |

## 4. Exit criteria and frozen numeric thresholds (#1213 (E1.E))

**Common terms:**
- A **suite** is one task class.
- **Arms**, all through the same `hyctl serve` build:
  - `quality`
  - `cost`
  - always-frontier: one pinned head id, named in the bench config before the run
  - always-cheapest
  - static fallback (B also adds `balanced` and the log-prob router)
- **Splits.** The calibration split fills calibration (≥ 20 judged answers per (head, class); recommended 500 items per suite, stratified by label). The test split is scored once.
- **Pass** = the served answer equals the gold label after the `match` normalisation.
- **Accepted** = `X-Hydra-Decision: accept`.
- **Cost** is the sum of `X-Hydra-Cost-USD` against one dated price snapshot.
- **Statistics.** Paired bootstrap, 10,000 resamples, fixed seed recorded in `bench/core/`.

| # | Hypothesis | Threshold (frozen when #1204 closes) | A | B |
|---|---|---|---|---|
| T1 | Provider-neutral path | 0 occurrences of `claude_pct`/`claudeMode` in non-test Go under `internal/dispatch` and `internal/serve`; a full run with **no Anthropic API head and no Claude CLI head** configured completes with no 5xx | gate | gate |
| T2 | **H1** | Under `quality`, per suite: one-sided 95% lower bound of paired Δpass (quality − always-frontier) > −2.0 pts, and point estimate ≥ −1.0 pts. **Non-vacuity:** `quality`'s first source differs from the frontier head on ≥ 10% of test items. Below that, the verdict is *inconclusive (vacuous)*, never *held*, because with nothing measured `EffectiveScoreIn` returns the catalogue score and `quality` reduces to always-strongest by construction (`internal/rank/measured.go:125-127`). | gate (Banking77, CLINC150) | gate (+CORD, rag, coding) |
| T3 | **H2** | Accepted-answer error at target 0.95 ≤ 7%; ECE (10 equal-width bins) of `X-Hydra-P-Correct` ≤ 0.05 over ≥ 500 accepted answers per suite (not `reliability.MinObservations` = 20, which is too few for 10 bins) | report | gate |
| T4 | **H3** | Under `cost`, per suite: cost per accepted answer < always-frontier, with the 95% bootstrap CI of the difference excluding 0, **and** the one-sided 95% lower bound of Δpass (cost − always-frontier) > −3.0 pts. A failure is recorded, not hidden. | gate | gate |
| T5 | **H4** | Header-less test prompts mixed across suites: accuracy ≥ 95% on non-abstained, abstention ≤ 40% | not run | gate |
| T6 | **H5** | Replaying 1,000 `/v1/feedback` events moves sp > 0.5 for ≥ 1 source per class; a second replay writes 0 rows | not run | gate |
| T7 | **H17** | Added latency over a direct call to the same head, p95 ≤ 250 ms, for deterministic verifiers on requests accepted at the first source | report | gate |
| T8 | (baseline) | Beats the log-prob threshold router on cost per accepted answer at equal pass rate (Δpass lower bound > −1.0), or on pass rate at equal cost (±5%) | not run | gate |
| — | **H6** | Measured D per verifier on constrained vs unconstrained heads | report | report (no gate in E1, per #1209) |

**Checkpoint A passes** when T1, T2 and T4 pass on Banking77. CLINC150 is reported and must not contradict Banking77; if it does, the verdict is *inconclusive*. Any change to a number above is recorded in #1213 with a reason and a date before results are seen.

## 5. Backwards compatibility and migration

**`state.json`.** The real path is `~/.hydra/logs/state.json` (`$HYDRA_HOME/logs/`), not the `~/.hydra` root listed in RFC §2.3. It holds:
- `claude_pct` (int 0–100) and `claude_pct_history` (≤ 12 ints, `budget.MaxPctHistory`);
- `last_*`;
- `budget`.

**A one-shot migration is not enough, because the legacy key keeps arriving:**
- The orchestrator protocol in `CLAUDE.md` tells Claude Code to write `claude_pct` by hand (`jq '.claude_pct = 52'`).
- `syncStateJSON` appends the history on every dispatch (`dispatch.go:1631-1634`).
- The frozen TUI (`internal/tui/cockpit.go:257`) and desktop app (`desktop/api/dashboard.go:212`) read it.

**The plan:**

1. **Alias, not migration, for the Claude Code scope.**
   - `Quota{Scope:"session:claude-code", ContextFrac: claude_pct/100}` is derived from the legacy key on every read. Its history is derived from `claude_pct_history`.
   - Only one function in `internal/budget` (e.g. `budget.LegacySessionQuota`) names `claude_pct`. That function and its test are the only references T1 allows, and they sit outside `internal/dispatch` and `internal/serve`.
2. **New quotas live under a new key.**
   - New quotas go in `quotas: {<scope>: {context_frac, history, ...}}`.
   - `syncStateJSON` keeps mirroring `claude_pct_history` while the legacy readers exist. Writing the new key never deletes the old one, so a downgrade to an older `hyctl` still reads its state.
   - Idempotent by construction: the alias is a pure function of the file.
3. **Required test.** A golden old `state.json` (legacy keys only) yields the same mode and tier downgrade through `quotaMode` as `claudeMode` gave. A file with both keys yields the `quotas` entry for non-Claude scopes and the alias for `session:claude-code`.
4. **Retiring the alias** happens once `CLAUDE.md`'s protocol writes through a `hyctl quota set` command instead of `jq`. That is a follow-up, not E1.

**Calibration (`calibration.jsonl`, snapshot, `trust.jsonl`, `coagreement.jsonl`) and the evalset (`evalset/examples.jsonl`, `.idx`).** These are keyed by `(source, domain)`, where domain is a lowercased file extension or `default`. **No rewrite.**

1. The coding resolver returns exactly what `DomainForFile` returns today. Extension classes (`go`, `ts`, …) stay valid task classes verbatim, so no history is orphaned. This is the #888 lesson: a key-shape change splits cells.
2. New classes are **dotted** (`classification.banking77`). `DomainForFile` can never produce a dot, because `filepath.Ext` returns only the last extension (`internal/trust/domain.go:43-46`). So new and old classes cannot collide, and #1205's key grammar should require the dot for non-coding classes.
3. `default` keeps its meaning. **Known cost:** a header-less serve request with no route default reads the `default` cell, which holds history from file-less CLI dispatches. That is acceptable because such a request is unclassified by definition, and the basis header says `default`.
4. Verifier keys:
   - The existing `verifier:<cmd>` (`hyctl oracle verify`), `verifier:grounding` (`internal/ground/oracle.go:35`) and `verifier:predicate:<id>` keys stay as they are.
   - New verifiers use `verifier:<kind>:<id>`.
   - `trust.UnreadableSourceKey` already exempts `verifier:`.
5. `trust_applied.jsonl` stays keyed by span id. B's feedback maps request id → run → spans through `trust.FindRunBySpan`, with no change.
6. The tenant prefix (`<tenant>/<source>/<class>`) is E2's migration, not E1's.

**`hyctl serve` routing default.** Today a request whose `model` names no routing key runs pinned to `--enum` (default `STANDARD`). Under A it is routed by objective (`quality`) instead. That changes behaviour for every existing client of `model: "hydra"`:
- `--enum` keeps its meaning when set explicitly;
- `hydra/<enum>` still pins;
- the change goes in the CHANGELOG as a `feat` with the opt-out spelled out.

See question Q2.

## 6. Implementation order inside E1

The design note #1205 (E1.D) precedes all of these. It must also fix the gaps found in §7 (C3, C6, C8, C9).

| Step | Work | Depends on | Files / packages |
|---|---|---|---|
| 1 | #1207-A `Quota` + `quotaMode` + legacy alias | #1205 | `internal/budget` (new `quota.go`, `risk.go` takes a series), `internal/dispatch/dispatch.go` (535-547, 984-1047, 1627-1634), `cmd/hydra/main.go` (status, ~700-810) |
| 2 | #1208-A resolver (header / route / file / default) + basis | #1205 | `internal/trust/domain.go` (resolver beside `DomainForFile`), `internal/dispatch` (`Options.TaskClass`, `routingDomain` :1585), `internal/serve/serve.go` (header, `metadata`), `cmd/hydra/serve.go` (`--task-class`) |
| 3 | #1206-A objective (`quality`, `cost`), unpinned serve default, P̂ basis | 2 | `internal/dispatch/dispatch.go` (677-680, `constrain` 1491), `internal/rank` (an argmax-measured order beside `Cheapest`), `cmd/hydra/serve.go` (`routeToDispatch` :155), `internal/serve/serve.go` (header) |
| 4 | #1209-A `Verdict.Score`, `match`, verifier-gated escalation | 3 (order to escalate along) | `internal/oracle` (`oracle.go:60`, new `match.go`), `internal/dispatch/dispatch.go` (the attempt loop), route config in `internal/config` |
| 5 | #1210-A headers, body `hydra`, per-request `RunLog` | 3, 4 | `internal/serve/serve.go` (`Answer` gains evidence; `chat` sets headers), `cmd/hydra/serve.go`, `internal/trust/log.go` |
| **A** | **#1213 checkpoint A** (with E7.2 #1199, E7.4 #1201) | 1–5 | `bench/core/` |
| 6 | #1210-B `/v1/feedback` + H5 | 5 | `internal/serve` (new route), `internal/trust/outcome.go`, `applied.go` |
| 7 | #1208-B kNN step (extend `classify.Vectored` with `Domain`, `Head`; class vote; per-head `PredictPass`) + #1206 `neighbours` basis | 2 | `internal/classify/classify.go`, `internal/trust` resolver, `internal/embed` |
| 8 | #1208-B `DefectCostUSD` + #1206-B `balanced`, `sampleOrder(obj)`, `ope.ArgmaxMeasured` | 3, 7 | `internal/trust/types.go`, `defect.go`, `sprt.go:155,276,295`, `internal/ope/policy.go`, `cmd/hydra` trace evaluate |
| 9 | #1209-B `jsonschema` (with serve parsing `response_format`), `http` | 4 | `internal/oracle`, `internal/serve/serve.go` (`chatRequest`), `internal/egress` |
| P1 | #1211 content parts (**start in parallel with step 1**) | #1205 | `internal/executor/tools.go:18-24` and every dialect: `anthropic.go`, `anthropic_stream.go`, `http.go`, `http_stream.go`, `gemini*.go`, `bedrock.go`, `cohere_stream.go`; `internal/serve/serve.go`; `cmd/hydra/serve.go` (`flattenConversation`); `internal/egress`, `internal/ledger`, `internal/dispatch/context_echo.go` |
| P2 | **E3.P1 #1242** cache plumbing | P1 | `internal/executor` (same dialect files + `Response` in `executor.go:49`), `internal/pricing/pricing.go:16`, `internal/dispatch/dispatch.go` (`logDispatch` :1673, cost row), `internal/cost` |
| P3 | #1212 `/v1/messages` | P1, P2, step 5 | `internal/serve` (new route + SSE writer), `cmd/hydra/serve.go`, `internal/executor/anthropic*.go` |
| **B** | **#1213 checkpoint B**, then #1214 → #1224 / #1225 / #1226 / #1227 | all of the above | |

**Conflicts with E3.P1 (#1242):**

1. **Executor dialect files.** #1211 (P1) and #1242 (P2) both rewrite how each dialect builds its request and reads usage. #1242 is already blocked by #1211, so keep it strictly sequential: P2 branches from `develop` only after P1 merges. #1212 (P3) edits `anthropic*.go` too, and its acceptance needs `cache_control` passthrough, so order it **P1 → P2 → P3**, not P1 → P3 → P2 as the dependency graph alone would allow.
2. **`executor.Response`** (`executor.go:49`). P1 does not need to touch it. P2 adds cached-token fields. E1 step 5 should read cost from one `dispatch.Result` cost value rather than recomputing it in serve. Then #1242 corrects it in one place, and `X-Hydra-Cost-USD` picks up cache pricing without a serve change. Until P2 lands, the header overstates cost on cached calls. That does not matter for checkpoint A, whose classification prompts are uncached, but it **must be noted in the A results**.
3. **`internal/dispatch/dispatch.go`** is the hotspot. Steps 1, 3 and 4 and P2 (`logDispatch`, cost row) all edit it. Land steps 1, 3 and 4 before P2 starts, or rebase P2 on them. The regions are disjoint (535-547/984-1047 vs 677-690 + the attempt loop vs 1673+), so conflicts are textual, not semantic.
4. **`internal/serve/serve.go`.** Steps 2, 3 and 5 edit `chat`, `chatRequest` and `request`. P1 changes `chatRequest.Messages` decoding. P3 adds a route. Do P1's `chatRequest` change before step 5 adds headers, or keep step 5's edits to `chat`'s response half only.

## 7. Corrections to the RFC and tickets

Line numbers are at `b1db9d1`.

**Substantive (they change scope or design):**

- **C1. No measured routing reaches `hyctl serve` (RFC §3, "Measured competence per domain"; §5.0).**
  - `routeToDispatch` maps an unnamed model to `EnumToTier(defaultEnum)` (`cmd/hydra/serve.go:178-179`; `--enum` defaults to `STANDARD`, `:272`).
  - `constrain` runs only when `tier == "" && opts.Head == ""` (`internal/dispatch/dispatch.go:677`).
  - serve passes no `Domain`/`Resource` (`cmd/hydra/serve.go:46-58`), so `routingDomain` is empty and `rerankFor` is a no-op too (`dispatch.go:1473-1474`).
  - **Consequence:** E1.1 must change serve's default routing, not only `constrain`.
- **C2. The only `oracle.Oracle` implementation is no longer `CommandOracle` (RFC §2.2; #1209).** `ground.Oracle` (`internal/ground/oracle.go:44`, #907) and `oracle.PredicateOracle` (`internal/oracle/predicate.go:39`, #1305, backed by decision heads #1309) also implement it. `PredicateOracle` is the natural base for #1224's `judge`.
- **C3. Verifier verdicts never reach a routing decision (RFC §3, "Verifiers as sources").**
  - `trust.Run` takes evidence only from source votes (`internal/trust/sprt.go`).
  - `internal/oracle` is imported only by `cmd/hydra` (oracle verify/ground), `internal/ground` and `internal/tui/chat_exec.go`.
  - `oracle.LLR` is computed for display only (`cmd/hydra/main.go:2137`, `oracle_ground.go:99`).
  - No E1 ticket owns "escalate when the verifier fails". Assigned to #1209 here (§1).
- **C4. `trust.Run`'s boundary is not only A = ln((1−α)/α) (RFC §3).** #1306 added a held-out accept threshold that overrides it (`sprt.go:150-153`, `cfg.accept`) and a per-source isotonic correction (`internal/trust/isotonic.go`, `threshold.go`).
- **C5. A cascade already exists for decisions.** `internal/decide` (#1310) answers from the first source when its stated confidence clears the target, and otherwise runs the SPRT (`internal/decide/decide.go:59-102`). It serves `/v1/decisions` and `/v1/systemone`. #1205 should make E1's chat cascade share this shape rather than add a second one.
- **C6. `classify` cannot do what §5.0 and §5.2.1 ask of it.**
  - `Vectored` holds only `Vec`, `Enum` and `Passed` (`internal/classify/classify.go:49-53`), so `Corpus.Near` returns no class to resolve to.
  - `PredictPass(n, enum)` (`:269`) is P̂(pass | **enum**), not P̂(pass | **head**, class).
  - `Load` drops examples with no `Enum` (`:96`).
  - The data exists on disk: `evalset.Example` carries `Domain` and `Head` (`internal/evalset/evalset.go:58,60`). The code just discards it.
- **C7. P̂ from `EffectiveScoreIn` is an integer percent with the catalogue score as its prior.** Below `MinCommitments` (20) in-domain observations it returns the declared catalogue score unchanged (`internal/rank/measured.go:106-127`). So on an unmeasured machine `quality` is always-strongest by construction. That is why T2 needs a non-vacuity condition.
- **C8. `/v1/feedback` has no ledger to replay for chat requests (#1210).** `trust.jsonl` is written only by `logTrustRun` for SPRT runs (`cmd/hydra/main.go:4148`; callers `main.go:1160`, `vet.go:107`). A served chat completion writes none. E1.5 must write a `RunLog` per request.
- **C9. Header names partly exist already, with different spellings (RFC §5.4; #1210).**
  - The request id is `X-Request-Id` (`internal/serve/serve.go:39`, #1149/#1174).
  - `/v1/decisions` already sends `X-Hydra-Domain`, `X-Hydra-Decision-Sources` and `X-Hydra-Decision-Confidence` (`internal/serve/decisions.go:17-22`).
  - #1205 must pick one vocabulary across both endpoints. Recommendation:
    - keep `X-Request-Id`;
    - accept `X-Hydra-Domain` as an alias of `X-Hydra-Task-Class`;
    - rename the decisions headers to `X-Hydra-Sources` and `X-Hydra-P-Correct`, keeping the old names for one release. Otherwise `X-Hydra-Decision` (the verdict) sits beside `X-Hydra-Decision-*` (the endpoint) and means something else.
- **C10. serve silently drops `response_format`.** `chatRequest` has no such field (`internal/serve/serve.go:149-160`). The `jsonschema` verifier (#1209) and the `X-Hydra-Task` + `response_format` ingress (§5.6) need it parsed.
- **C11. #1207's AC "zero references to `claude_pct` outside the migration" is unreachable as written.**
  - The frozen TUI (`internal/tui/cockpit.go:257`, `view_usage.go:212`, `chrome.go:51`) and desktop (`desktop/api/dashboard.go:212`) read it.
  - `CLAUDE.md`'s protocol writes it.
  - "Migrate once" is defeated by the next `jq` write.
  - **Fix:** scope the AC to `internal/dispatch`, `internal/serve` and `cmd/hydra`, and use a read alias (§5).
- **C12. `claudeMode` is not only a downgrade.** Its emergency branch forces `LocalOnly` on every dispatch regardless of the tier hint (`dispatch.go:536-539`), serve included. RFC §2.1 understates its reach.
- **C13. `balanced`'s λ has inconsistent units (RFC §5.0 table; #1206).** "$/point of pass rate" and "λ = 1/DefectCostUSD" disagree:
  - 1/DefectCostUSD is probability per dollar, so it applies to P̂ as a probability, not in points.
  - Utility P̂·DefectCostUSD − cost ⇔ P̂ − cost/DefectCostUSD.
  - Also, the defect cost already exists as `trust.DefectModel.CostUSD` (`internal/trust/defect.go:92`), with `RequiredConfidence` mapping it to α = 0.10/cost (`:59`). `DefectCostUSD` is an override input to that, not a new concept.

**Stale facts and line numbers:**

- **C14. RFC §2.1 counts.**
  - "29 of 278 non-test Go files reference Claude" → now 33 of 317.
  - "`claude_pct` … 99 references" → 88 lines repo-wide, 47 in non-test Go.
  - "`dispatch.go:534`, `:984`" → call at `:535`, func at `:989` (`:984` is its comment).
- **C15. RFC §2.3.**
  - serve now exposes four routes: `/v1/models` `:101`, `/v1/chat/completions` `:108`, `/v1/decisions` `:115`, `/v1/systemone` `:116`.
  - `state.json` is at `~/.hydra/logs/state.json`.
- **C16. RFC §3, `classify` row.** "at least 3 neighbours per enum" → `MinNeighbours = 5` (`classify.go:24`).
- **C17. #1206.**
  - `dispatch.constrain` is defined at `dispatch.go:1491`; `:678` is its call site.
  - `sampleOrder` is defined at `sprt.go:276` (called at `:155`).
  - `evidencePerCost` is at `sprt.go:295`, not `:291`.
  - #1205 cites `sprt.go:151` for `sampleOrder`; it should be `:276`.
- **C18. #1205.**
  - `oracle.go:66` is the `Oracle` interface; `Verdict` is at `:60`.
  - `tools.go:18` is the `Message` struct line; `Content` is at `:20`.
- **C19. #1242.** `anthropicUsage` is declared at `internal/executor/anthropic_stream.go:47`; `:21` is a field that uses it.
- **C20. `CLAUDE.md` (not edited here).**
  - The `internal/serve` row says "`stream: true` is refused". Streaming has shipped (`internal/serve/serve.go:200`, `stream.go`).
  - The row also omits `/v1/decisions`, though the directory-layout block mentions it.
- **Verified unchanged:**
  - `trust.Task` fields (`internal/trust/types.go:35-47`);
  - `DomainForFile` (`domain.go:43`);
  - `classify.Corpus.Near` (`classify.go:153`);
  - `oracle.LLR` / `Measured` (`oracle.go:259`, `:279`);
  - `ApplyRunOutcome` (`outcome.go:22`);
  - `rank.MinCommitments = 20` (`measured.go:20`);
  - `EffectiveScoreIn` (`:106`), `Cheapest` (`constraint.go:36`), `SortMeasured` (`measured.go:154`);
  - `pricing.ModelPrice` (`pricing.go:16`, input/output only);
  - `executor.Message.Content` is a `string`;
  - `internal/ground` is as described (`Check`, `ground.go:80`; not run automatically).

**Develop has moved since the RFC (all relevant to E1):**
- #1305 `PredicateOracle`;
- #1306 per-source isotonic correction and a held-out accept threshold;
- #1307 per-source reliability audit (`hyctl trust reliability --source`), which E1's H2 report can reuse;
- #1309 decision heads;
- #1310 `/v1/decisions` + `/v1/systemone` via `internal/decide`.

None of these is reflected in RFC §3 or the E1 tickets.

## 8. Questions only the user can answer

1. **Narrow checkpoint A to classification only?** That means Banking77 as the gate and CLINC150 reported. CORD/`jsonschema`/`http`, `/v1/feedback` (H5), the kNN resolver (H4), content parts and `/v1/messages` would all be measured at checkpoint B. This narrows #1204's "first layer" and #1213's checkpoint A list (thresholds 3 and 5–8 move to B). The alternative is to keep #1213 as filed and accept that A waits for every first-layer ticket.
2. **Change `hyctl serve`'s default routing?** Today `model: "hydra"` (and an empty model) runs pinned to `--enum STANDARD`. A requires it to route by objective (`quality`), with `hydra/<enum>` and an explicit `--enum` still pinning. This changes behaviour for existing clients such as open-code-review pointed at `hydra`. Is that acceptable as a non-breaking `feat`, or should the objective default only on a new id (e.g. `hydra/auto`) and leave `hydra` pinned?

## Decisions (2026-11-05, ankit373)

1. **Checkpoint A is classification only:** Banking77 and CLINC150. CORD, the `jsonschema`/`http` verifiers, `/v1/feedback`, kNN, content parts and `/v1/messages` move to checkpoint B.
2. **`model: "hydra"` on `hyctl serve` switches from a pinned `STANDARD` to objective routing (`quality`).** This is a deliberate breaking change.
   - The CHANGELOG must say so.
   - The old behaviour stays reachable with `hydra/standard`.
