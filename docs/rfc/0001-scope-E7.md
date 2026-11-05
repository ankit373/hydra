# RFC 0001 — Scope: E7 benchmarks and evaluation harness

Epic: ankit373/hydra#1197 (children #1198–#1202, #1219, #1220). Hypothesis register: #1196.
Status: draft, scope stage complete (2026-11-05); awaiting answers to §8

Related [eval] tickets whose thresholds this harness must be able to measure: #1213 (E1.E),
#1237 (E2.E), #1251 (E3.E), #1264 (E4.E), #1274 (E5.E).

## 1. Harness architecture (first layer)

Covers #1198 (E7.1), #1199 (E7.2) and #1201 (E7.4). The first layer is single-shot
requests only: one prompt in, one answer out, one verifier. Agent loops (#1202) are
checkpoint B (§5).

### 1.1 What exists, and what this reuses

| Need | Reuse | Gap the harness fills |
|---|---|---|
| Calibration error of stated P(correct) | `reliability.Evaluate([]Observation, bins)`: Brier, Murphy decomposition, ECE, MCE; refuses below `MinObservations` | Nothing. The harness builds `Observation{Predicted, Correct, Ref}` per accepted answer. |
| Per-source se/sp report | `reliability.EvaluateSources` | Nothing. Used for verifier se/sp (H6). |
| Bootstrap with a fixed seed | `ope.bootstrapInterval` (unexported, weighted percentile, seeded) | A *paired* bootstrap over item-level differences. Lift the resampler into a shared helper rather than write a second one (CLAUDE.md: no duplicate logic). |
| Counterfactual policy value from logs | `ope.Evaluate` (SNIPS + bootstrap, ESS gate) | Not used to score arms. Arms run live. `ope` stays for comparing objectives from production logs (§5.0 `ArgmaxMeasured`). |
| Labelled verdicts | `evalset.Example` (task, candidate, `Passed`, enum/head, vector) | The harness writes no evalset rows. Benchmark items are public data, and mixing them into a user's corpus would let `classify` fit on them. Results go only to `bench/results/`. |
| kNN pass prediction (H4) | `classify.Load`, `Corpus.Near`, `PredictPass` | A corpus built from the *calibration* split only, in a temp dir. |
| Command verifiers | `oracle.CommandOracle` (exit status → verdict), `oracle.LLR`, `oracle.Measured` | Label match and field match are in-process Go functions, not commands. They implement the same `oracle.Oracle` interface, so E1.4's `match`/`jsonschema` verifiers (#1209) are the ones the harness calls. There is no bench-only copy. |
| Standalone verdict recording | `cmd/hyverify` | Not on the first-layer path. It is the model for the coding suite's "verifier needs no router" rule at checkpoint B. |
| Gateway under test | `internal/serve` (`/v1/chat/completions`; the `model` field routes `hydra`, `hydra/<enum>`, `hydra/t<N>` or a head id; a request id comes back in the header and body) | See 1.3. Today serve passes no temperature, reads no objective and returns no evidence headers. |
| Config fingerprint | `config.Breadcrumb()`: sha256 over routing/models/domains/pricing/signals.yaml | Plus the harness's own fields (1.4). |
| Latency | `sketch` (DDSketch) | Used for p50/p95 per arm. |
| Synthetic SPRT sanity | `trust.Benchmark(trials, seed)` | Kept as a unit-level check that the SPRT stops early. It is not a suite. |

There is no `bench/` directory and there are no benchmark scripts (grep, 2026-11-05).
The only `*bench*` files are Go micro-benchmarks (`dispatch_bench_test.go`,
`falsehit_bench_test.go`, `workspace/bench_test.go`) and `trust/benchmark.go`.

### 1.2 Go, not scripts

The harness is Go: `internal/bench` plus `hyctl bench {fetch,run,report,table}`.
- **Repo convention.** Everything is native Go. The shell layer was removed (#86, #88).
  CI tooling is Go (`ci/covergate`). `scripts/` holds one release helper.
- **Reuse.** The metrics in 1.1 are Go packages. A Python harness would have to
  reimplement ECE and the bootstrap, and the two copies would drift.
- **#1198 acceptance** says "metrics match a hand-computed fixture to 1e-9" and
  "`go test -race ./...` green". That is a Go test.
- **Exception, checkpoint B only.** τ²-bench, BFCL and Terminal-Bench ship Python
  reference harnesses. #1202 runs *their* harness, pointed at a base URL, as a
  subprocess. It does not port them.

Three constraints from the repo:
- `ci/coverage-floors.txt` needs a line for `internal/bench`, or covergate refuses it (#805).
- `internal/bench` downloads data, so it imports `net`. Nothing reachable *from*
  `internal/evalset` may import it (`TestNoHydraPackageReachableFromTheCorpusCanEgress`).
  `bench` may import `evalset`, but not the other way round.
- **#1176 lesson.** No live network, model or wall-clock deadline in `go test ./...`.
  Unit tests use fixtures and a fake executor, as #1198 requires. Live runs need
  `hyctl bench run`; there is no `testing` build tag that turns them on. Timing tests
  assert ordering against a fake clock, never "finished within N s".

### 1.3 Suite definition

```go
type Item struct {
    ID     string          // stable upstream id, or sha256 of the canonical input
    Class  string          // task class, e.g. "classify/banking77"
    Split  Split           // Calibration | Test, from bench/splits, never computed at run time
    Input  json.RawMessage // suite-specific: utterance, or receipt OCR lines
    Gold   json.RawMessage // label, or field map
}
type Suite interface {
    Name() string
    Version() string                        // dataset revision + sha256 of the fetched files
    Load(dataDir string) ([]Item, error)
    Prompt(it Item) executor.Request         // identical rubric for every arm (task-models A.10)
    Verify(it Item, answer string) Verdict   // Passed, Score in [0,1], per-field detail
}
```

- **Loader.** `hyctl bench fetch <suite>` downloads to `$HYDRA_HOME/bench/data/<suite>/`
  (outside the repo), checks the sha256 pinned in `bench/datasets.lock` and refuses
  on mismatch. Data is never committed: CC BY needs attribution and SROIE's terms are
  unknown (§2). Only ids, splits and results are committed.
- **Task class.** One class per suite in the first layer
  (`classify/clinc150`, `classify/banking77`, `extract/cord`, `extract/sroie`).
  This is the key `trust` calibrates on. H4 (resolver accuracy) strips the header and
  checks whether the resolver recovers it.
- **Verifier.**
  - *classify:* the label after canonicalisation (casefold, NFC, strip) is exactly
    equal to gold. A label outside the label set is a fail, never a near-miss.
    CLINC's OOS counts as a label (`oos`); abstention is scored separately (§4).
  - *extract:* the answer must parse against the suite's JSON schema (a parse failure
    is a fail). Then field exact match after the task-models canonicalisers
    (date, money, id, casefold, NFC). Item pass means every required field matches;
    `Score` is the share of fields matched. Field-level results go in `detail` for
    E4's per-field claims.
- **Gold labels and splits.** Splits are assigned once from
  `sha256(suite‖item_id) mod 1000`, written to `bench/splits/<suite>.tsv` (id, split)
  and committed. A test asserts no id appears in both splits (#1201). The calibration
  split fits se/sp only. The test split is scored only. Upstream train/test is ignored
  for zero-shot arms, because no arm trains on it. Pooling them is what makes CORD and
  SROIE large enough (§3).

### 1.4 Arms

Every arm sees the same items, in the same order and the same prompt, at temperature 0.

| Arm | Path | Notes |
|---|---|---|
| `frontier` (B1) | direct: `internal/executor` → provider, no router | The bar under `quality`. Model named in the run config (§8 Q3). |
| `cheapest` (B2) | direct | Cheapest head that parses the output schema at all. |
| `static-fallback` (B3) | direct, ordered list, next model only on transport/HTTP error | The LiteLLM/Portkey shape. With no faults it is byte-identical to B1, so it also runs on a 200-item subset with injected 429/5xx (§3). |
| `threshold` | direct: cheapest model, escalate to frontier when its log-prob or self-reported confidence is below τ | τ is tuned on the calibration split. Required by #1213 threshold 8. Log-probs are unavailable on Anthropic/Bedrock, so self-report is the fallback, and the summary records which was used. |
| `oracle` | computed, no calls | Per-item best over the heads the other arms ran. Its cost is the cheapest passing head. Sanity check: it is ≥ every arm (#1198). |
| `hydra-quality`, `hydra-balanced`, `hydra-cost` | HTTP → `hyctl serve` on loopback, `model: hydra`, `X-Hydra-Objective: <o>`, `X-Hydra-Task: <class>` | Requires #1206 (objective) and #1210 (contract headers). Until then only the default route runs, recorded as `hydra-default`. |

Direct arms use `internal/executor`, not a second HTTP client. The wire code is then
the same code Hydra uses, so the paired difference measures routing, not a client.

What `hyctl serve` must gain for the harness (owned by E1, listed here so E1's scope
includes it):
- `temperature` passthrough. Today `serve.Request` has no field for it and dispatch
  never sets `executor.Request.Temperature`.
- `X-Hydra-Objective`.
- Per-response evidence: sources, accepted P(correct), cost, escalations (#1210).
  Until then the harness joins on the request id against `cost.jsonl` and the runlog.
- A way to pin `explore_rate = 0`. The second option is a seeded RNG:
  `dispatch/explore.go` uses the unseeded global `math/rand/v2`.

### 1.5 Reproducible, seeded runs, and the config breadcrumb

`hyctl bench run --suite S --arm A --seed N [--limit n] [--tier smoke|full]`
- **What the seed controls.** Item order, subsample selection, bootstrap resamples,
  τ tuning folds and Hydra's exploration RNG once seedable. It does **not** control
  model sampling: Anthropic and Bedrock take no seed, and executor does not plumb
  OpenAI's. Determinism comes from temperature 0. The residual API nondeterminism is
  *measured*: #1198's identical-rerun check, |Δpass| ≤ 0.5 pt on n = 200.
- **Isolation.** Each Hydra arm runs against a fresh `HYDRA_HOME`. The harness copies
  in only the calibration state fitted on the calibration split, or seed priors
  (E7.4). The developer's own calibration and evalset never leak in, and the run
  writes nothing back.
- **Resumable.** Results are appended per item to the `.jsonl` and fsynced. A killed
  run restarts at the first missing item id, the same pattern `internal/workflow`
  uses. This matters: the machine sleeps.
- **Budget cap.** `--max-usd` is checked before each call against the running total
  of *billed* cost. It fails closed, reusing `policy` caps semantics (#838).
- **Breadcrumb** in `summary.json`:
  - harness version and git sha (dirty flag)
  - `config.Breadcrumb()`
  - suite name, version and data sha256
  - split file sha256
  - arm config: models, objective, target, τ
  - seed
  - a dated price snapshot: `pricing.DB` rows used, with their source and fetch time
  - provider-reported model ids and versions as returned in responses
  - temperature actually sent
  - host load average and `sysinfo` at start (latency is noise on a loaded machine; #1176)

### 1.6 Paired statistics

All comparisons are paired by item against `frontier` unless stated. One implementation
in `internal/bench/stats.go`, tested against hand-computed fixtures to 1e-9.

- **Δpass (H1, H3, H14, H15).**
  - Per item, `d_i = pass_arm − pass_frontier ∈ {−1, 0, 1}`.
  - Point estimate `mean(d)`.
  - One-sided 95% lower bound by paired percentile bootstrap (10k resamples, seeded).
  - **Non-inferiority (margin δ).** Tango's (1998) score test on the discordant pairs
    (b, c) is the test of record, with H0: Δ ≤ −δ. Plain McNemar tests Δ = 0, so it
    cannot test a margin. The bootstrap bound must agree with it; if it does not, the
    result is "inconclusive" and the reason is recorded.
  - **Superiority** (H14 "beats", H16 T2 > T1): exact conditional McNemar on (b, c).
  - The decision rule is the one the [eval] ticket froze, e.g. lower bound > −2.0 pts
    *and* point estimate ≥ −1.0 (#1213).
- **Cost per accepted answer.** Σ billed USD ÷ #accepted. Paired bootstrap over items
  of the ratio difference, 10k resamples. "CI excludes 0" is two-sided 95%.
- **Accepted-answer error vs α (H2).** Among accepted answers, the error rate with a
  one-sided 95% Clopper–Pearson upper bound. Lift `entity.wilson` beside it rather
  than add a third interval. ECE and Brier come from `reliability.Evaluate`, 10 bins,
  reported with n.
- **Escalation rate and mean sources consulted.** Per arm, with Wilson intervals.
- **Added latency.** Per item, `t_hydra − t_direct`, with calls interleaved ABAB on
  the same item to cancel provider drift. p50/p95 via `sketch`, bootstrap CI.
  Concurrency is 1 for the latency subset; throughput runs are separate and report
  no latency.
- **pass^k (checkpoint B, τ²-bench).**
  `pass^k = mean over tasks of C(c_t, k) / C(n_t, k)`, with n_t trials and c_t
  successes, exactly as τ-bench defines it. Also pass@1.
- **Multiplicity.** One test per (H, suite) as pre-registered in §4. No arm is
  re-tested after seeing results. Thresholds are frozen at scope close (#1213
  acceptance criterion 1).

### 1.7 Output layout under `bench/`

```
bench/
  README.md               how to reproduce one row: one command per row
  DATASETS.md             suite, source URL, licence, size, citation (#1199)
  datasets.lock           suite → url, revision, sha256
  splits/<suite>.tsv      id, split (committed, hash-derived)
  seeds/<pack>/<class>.json   confusion counts, basis: benchmark:<suite>@<date> (#1201)
  configs/<tier>.toml     arms, models, n per suite, max_usd
  results/<track>/<suite>/<arm>/<date>-<seed>.jsonl   one row per item
  results/<track>/<suite>/<arm>/<date>-<seed>.summary.json
  INTEGRITY.md            the E7.R checklist (#1220)
```
- `<track>` is `core` (E1), `task-models` (E4), `storage` (E2), `compress` (E3) or
  `agents` (E5). These match each [eval] ticket's acceptance path.
- **Per-item row:**
  - item id and split
  - arm and request id
  - answer (or its hash)
  - verdict and score
  - sources consulted, accepted source and stated P(correct)
  - billed input/output/cache tokens and USD
  - latency, TTFT, escalations and error kind
- Answers to public items are committed. They are small, and publishing them is what
  makes the numbers auditable. `hyctl bench table` (#1219) reads only `summary.json`.

## 2. First suites

### 2.1 Checkpoint A: the smallest set that gives a fast non-coding signal

Licences were checked against the primary source on 2026-11-05 unless marked otherwise.

| Suite | Class | Size (upstream) | Licence | Verifier | Why |
|---|---|---|---|---|---|
| **Banking77** (PolyAI) | classify, 77 labels | 10,003 train + 3,080 test = 13,083 | CC BY 4.0 (HF dataset card, verified) | label match | Fine-grained. This is where zero-shot small models lose (H13 direction: GLiNER2 0.70 vs GPT-4o 0.78), so a cheap source cannot win by default. Cheapest prompt of the set. |
| **CORD v2** (Clova AI) | extract, receipts | 1,000 receipts: 800 train / 100 dev / 100 test | CC BY 4.0 (repo README, verified) | jsonschema + field exact match | Numbers, money and ids are the atoms canonicalisers exist for. Also E3's extraction stressor. |

These two are the **minimum**: one classification and one extraction set, as asked.
#1199 and #1213 name two more, which are cheap to add once the loader exists. They are
in checkpoint A's *full* tier but not the smoke tier:

| Suite | Class | Size | Licence | Notes |
|---|---|---|---|---|
| **CLINC150** (`data_full.json`) | classify, 150 intents + OOS | 15,000 / 3,000 / 4,500 in-scope + 100 / 100 / 1,000 OOS = 23,700 | CC BY 3.0 (repo LICENSE, verified) | OOS tests abstention. Includes the 10-intent + OOS subset that reproduces the independent ScaleDown result (n = 300). |
| **SROIE** (ICDAR 2019 task 3) | extract, 4 fields (company, address, date, total) | 626 train + 347 test = 973 receipts (commonly cited; one mirror says 600/400) | **unverified.** The official site (rrc.cvc.uab.es) failed TLS from here, and the mirrors state no data licence (the MIT on GitHub mirrors covers code only). | E4's ScaleDown comparison only (#1264). It stays out of anything published until the licence is confirmed (§8 Q5). |

**Notes on sizes:**
- **CORD.** The CORD paper speaks of "over 11,000 receipts", but only 1,000 are
  released. `store_info`, `payment_info` and `etc` were removed from the public labels
  for Indonesian legal reasons, and the README gives both 30 and 42 subclasses.
  `bench/DATASETS.md` should therefore list the field set the loader actually uses,
  counted from the data, not quoted.
- **Upstream splits are pooled for zero-shot arms** (§1.3). That gives CORD 1,000 and
  SROIE 973 usable items rather than 100 and 347. This is the only way either can
  reach the n in §3. Hash-split 30% calibration / 70% test.
- **Contamination.** All four are public since 2019–2020 and are almost certainly in
  frontier pre-training data. This favours the frontier and cheap LLM arms roughly
  equally, so paired Δ is less affected than absolute accuracy. It is still stated in
  §7 and in every results file.

### 2.2 Checkpoint B (follows the first layer)

| Suite | Owner ticket | Size used | Licence (as stated by maintainers; re-check in #1200/#1202) | Verifier | Notes |
|---|---|---|---|---|---|
| SWE-bench Verified subset | #1200 (E7.3) | 50–100 tasks, ids pinned in `bench/splits/swe.txt` | MIT (SWE-bench repo); task repos keep their own licences: **unverified** here | repo tests in a container with no network | **Paired differences only.** OpenAI's 2026 audit found material flaws in 59.4% of 138 hard problems (agents A10.1). Stratify the subset away from the flagged problems where the audit lists them. |
| τ²-bench (airline, retail; telecom later) | #1202 (E7.5) | all tasks × k = 4 trials | MIT: **unverified** here | DB end-state diff | pass^k. Also needs a user-simulator LLM, which is a cost line of its own (§3). |
| BFCL v4 multi-turn | #1202 | multi-turn categories | Apache-2.0: **unverified** here | AST/state checks | Calibrates `toolcall.schema`/`grounded` (H16 inputs). |
| Terminal-Bench 2.0 subset | #1202 | subset | Apache-2.0: **unverified** here | containerised task tests | Cache/stickiness costs. |
| Compression: planted-atom corpus | #1251 (E3.E) | ~10⁴ prompts | generated: no licence issue, *except* it is built over the replay store, which holds user prompts (§7) | exact match, deterministic | H10. Needs no model, so it costs $0 and can run every build. |
| Compression: IFEval padded 4k–32k, 3 placements | #1251 | 541 prompts × 3 placements × 2 (on/off) | Apache-2.0 (google-research): **unverified** here | IFEval's own checkers | Instruction retention under compression. |
| Compression: RULER multi-key/multi-value/variable tracking | #1251 | generated | Apache-2.0 (NVIDIA): **unverified** here | exact match | Not plain NIAH (compression A.5). |
| RAG: HotpotQA / MuSiQue dev subset | #1200 | subset | HotpotQA CC BY-SA 4.0, MuSiQue CC BY 4.0: **unverified** here | match + grounded + judge | H11 (rung 3 expected to fail multi-hop). |

E4-only suites (CrossNER for H13, AG News, SST-2, MASSIVE) are E4's scope (#1264).
E7 provides the loader interface and does not pick them.

## 3. Cost control

### 3.1 Power analysis for the RFC margins

**The H1/H15 test.**
- Paired binary outcomes, H0: Δ ≤ −δ with δ = 2 pts, one-sided α = 0.05,
  power 1 − β = 0.80, so (z_α + z_β)² = (1.645 + 0.842)² = 6.18.
- For paired data the variance of `d_i` is ≈ p_disc − Δ², where p_disc is the share
  of items on which the two arms disagree.
- So n ≈ 6.18 · p_disc / (δ + Δ_true)².

p_disc is the unknown that matters. Two things drive it:
- How often Hydra's first source is *not* the frontier model.
- How correlated two models' errors are.

On Banking77/CLINC, frontier models sit in the low to mid 90s, so disagreement is
mostly where one of them errs: p_disc ≈ 4–10%. The smoke tier (3.3) measures it before
any full run is sized.

| p_disc | n at Δ_true = 0 | n at Δ_true = −0.5 pt | n for δ = 1 pt (H14), Δ_true = 0 | n for δ = 0.5 pt (E3 extract/classify) |
|---|---|---|---|---|
| 0.04 | 618 | 1,099 | 2,472 | 9,888 |
| 0.06 | 927 | 1,648 | 3,708 | 14,832 |
| 0.10 | 1,545 | 2,747 | 6,180 | 24,720 |
| 0.15 | 2,318 | 4,120 | 9,270 | 37,080 |

**What this means:**
- **H1 at δ = 2 is affordable on the classification suites.** Plan n_test = 1,500 per
  suite. That is powered to p_disc ≈ 0.10 at Δ_true = 0. It is a pre-registered hash
  subsample of Banking77 (3,080 test plus pooled train) and CLINC150 (5,500 test).
  The smoke tier's measured p_disc can raise it before the full run. It cannot lower
  it below 1,000.
- **CORD and SROIE cannot reach 1,500.** CORD has 700 test items after the 30/70 split,
  enough only if p_disc ≤ ~0.045. SROIE has ~680.
  - Receipt-level H1 there is therefore reported, but **"inconclusive" is the expected
    verdict unless p_disc is small**. This must be written into #1213 before the run,
    not discovered after.
  - The field-level Δ (~8–12 fields per CORD receipt) has more power, but fields are
    clustered within a receipt. It uses a *cluster* bootstrap that resamples receipts,
    never fields.
- **H14's "within 1 pt"** (#1264) needs 2,500–6,000 items at realistic p_disc. Only
  CLINC150 can supply that.
  - Recommendation: #1264 reads "within 1 pt" as a **point-estimate** condition
    beside the cost test (CI excludes 0), not as a second non-inferiority test.
    Otherwise H14 is unfalsifiable on Banking77 and the extraction suites.
  - This is a threshold change, so it needs the user's sign-off (§8 Q6).
- **E3's δ = 0.5 pt for extraction/classification** (§5.5) is out of reach on any
  public suite: 10⁴–4×10⁴ items. It is also mostly moot, because compression is off
  below 4k uncached tokens and none of these prompts reach that. E3's scope should
  say so, rather than this harness pretending to test it.
- **H2** (accepted-answer error ≤ α + 2 pts = 7%, when the true rate is 5%):
  - n_accepted ≈ (1.645·√(.07·.93) + 0.842·√(.05·.95))² / 0.02² ≈ **910** accepted
    answers per suite.
  - ECE with 10 bins at n ≈ 1,000 has a sampling floor of roughly 0.01–0.02, under the
    0.05 threshold, so the threshold is testable.
- **H3** (cost per accepted answer lower, CI excludes 0): the cost differences are
  large and nearly deterministic, so a few hundred items suffice. It rides on the H1
  sample.
- **H4** (resolver ≥ 95% accuracy on non-abstained items, abstention ≤ 40%): a
  one-sided Wilson lower bound ≥ 0.95 at a true 0.97 needs ~700 non-abstained items.
  Draw them from the three suites' test splits.
- **#1198 determinism** (|Δpass| ≤ 0.5 pt on n = 200 identical reruns): at n = 200 one
  flipped item is 0.5 pt. So the gate is "≤ 1 discordant item". That is strict, but
  right: temperature-0 APIs that flip more than that make every other number noisier.

### 3.2 USD per suite × arm

**Assumptions.**
- **Prices** from the `pricing.DB` snapshot read on 2026-11-05 (OpenRouter list, USD per
  1M tokens, input/output):
  - Claude Opus 4.8: 5 / 25
  - Claude Sonnet 4.6: 3 / 15
  - GPT-5.4: 2.5 / 15
  - Claude Haiku 4.5: 1 / 5
  - GPT-5-nano: 0.05 / 0.40
  - Nova Micro: 0.035 / 0.14
  - local Qwen: 0

  Bedrock list prices are assumed equal to these, which is **unverified**.
- **Frontier = Opus 4.8** as the conservative, expensive case. Picking GPT-5.4 roughly
  halves every frontier line (§8 Q3).
- **Cheapest = Nova Micro**, reachable through the Bedrock head this machine already
  has, or local Qwen at $0.
- **No prompt caching assumed.** Cache plumbing is an E3 prerequisite. Caching the
  label-list prefix would cut classification input cost by a further ~50–80%.
- **Thinking/reasoning off.** Reasoning tokens are billed as output and can multiply
  output cost 10–50×. The harness records billed units and fails the run if a
  "no-reasoning" arm reports reasoning tokens.

**Tokens per item** (estimated; the smoke tier measures them):

| Suite | Input | Output | F = frontier $/item |
|---|---|---|---|
| Banking77 (77-label rubric) | ~600 | ~10 | $0.0033 |
| CLINC150 (151-label rubric) | ~900 | ~10 | $0.0048 |
| CORD (OCR lines + schema) | ~900 | ~250 | $0.0108 |
| SROIE (OCR + 4-field schema) | ~600 | ~80 | $0.0050 |

**Arm cost as a multiple of F per item** (estimates, to be replaced by smoke-tier
measurements):

| Arm | Multiple of F | Why |
|---|---|---|
| frontier | 1.0 | |
| cheapest | ~0.01 | |
| static fallback | 1.0 | |
| threshold | ~0.3 | cheap + ~25% escalation |
| quality | ~1.2 | best head first, plus occasional second source |
| balanced | ~0.5 | |
| cost | ~0.25 | |
| oracle | 0 | computed from the other arms |

**Full tier, checkpoint A, one seed** (n_test: 1,500 / 1,500 / 700 / 680; calibration
fit is about 1.6 F per calibration item, from frontier + one mid model + cheap):

| Suite | frontier | cheapest | static | threshold | quality | balanced | cost | calib. fit | **Suite total** |
|---|---|---|---|---|---|---|---|---|---|
| Banking77 | $4.9 | $0.03 | $4.9 | $1.5 | $5.9 | $2.4 | $1.2 | $2.6 (500) | **$23** |
| CLINC150 | $7.1 | $0.05 | $7.1 | $2.1 | $8.6 | $3.6 | $1.8 | $3.8 (500) | **$34** |
| CORD | $7.5 | $0.05 | $7.5 | $2.3 | $9.0 | $3.8 | $1.9 | $5.2 (300) | **$37** |
| SROIE | $3.4 | $0.02 | $3.4 | $1.0 | $4.0 | $1.7 | $0.8 | $2.4 (300) | **$17** |
| **All four** | | | | | | | | | **≈ $111** |

**Further costs:**
- **Determinism rerun** (200 items × frontier + quality on Banking77): ≈ $1.5.
- **B3 fault-injection subset** (200 items): ≈ $1.
- **Latency subset** (ABAB, 200 items × 2 arms × 2 suites): ≈ $5.
- **Checkpoint A total ≈ $120 per seed.** Budget $150 to allow a +25% token-estimate
  error.
- **Seeds.** At temperature 0, a second and third seed buy little beyond the
  determinism check, so **one full seed** is recommended for checkpoint A. The
  agents track's ≥ 3 seeds (A10.4) applies to checkpoint B, where trajectories really
  vary.
- **E4 arms** (each task model alone, the FrugalGPT cascade) add about one extra
  cheap-tier arm per suite. GLiNER local is $0. ScaleDown is priced by contract and
  is **unknown**.

**Checkpoint B (order of magnitude only; every figure here is an assumption to firm up
in #1200/#1202):**

| Suite | Assumption | Arms × seeds | Estimate |
|---|---|---|---|
| SWE-bench Verified, 75 tasks | ~$1–3 per task per arm with frontier | 4 × 3 | **$900–2,700** |
| τ²-bench airline + retail | ~$0.2–0.5 per episode incl. user simulator; k = 4 trials | 5 arms | **$650–1,600** |
| BFCL v4 multi-turn | ~$0.02–0.05 per item | 5 | **$80–200** |
| IFEval padded (541 × 3 × 2) | ~16k input tokens on frontier | 1 head | **~$260** |
| RULER | similar | 1 head | **~$200** |
| Planted-atom | no model | | $0 |

Checkpoint B is therefore a **$2–5k** exercise, dominated by SWE-bench and τ²-bench.
That is why §8 asks for a monthly cap, and why B runs only after A passes.

### 3.3 Tiers

| Tier | What | n | Arms | Cost (Opus frontier) |
|---|---|---|---|---|
| `fixture` | Unit tests: fake executor, golden results, metrics to 1e-9 | ~50 synthetic | all | **$0**, runs in `go test` |
| `local` | Real suites, local Qwen only, both arms the same head family | 200 per suite | cheapest, oracle, hydra-default (local) | **$0**, end-to-end plumbing check with no keys |
| **`smoke`** | Banking77 + CORD test-split hash subsample, 1 seed | **100 per suite** | frontier, cheapest, hydra-quality, hydra-cost | **≈ $3.5** (Banking77 $0.8, CORD $2.7). `--max-usd 5` enforced. |
| `full` | §3.2 | 1,500 / 1,500 / 700 / 680 | all | ≈ $120–150 |
| `B` | §2.2 | | | $2–5k |

What the smoke tier is for:
- It is not a verdict: 100 items give a ±6 pt interval.
- It proves the pipeline end to end, with real billing.
- It **measures p_disc, the token counts and the escalation rates** that §3.1 and §3.2
  assume. The full run is re-sized from those measurements before any money is spent
  on it.

## 4. Hypothesis mapping

Rows follow the register seeded in #1196. "Threshold" is the frozen one from the owning
[eval] ticket. "n" is what §3.1 says it needs, and **bold n** means the planned sample
probably cannot meet it. This harness supplies the instrument; the [eval] tickets own
the verdicts.

| H | Suite(s) | Metric (harness function) | Threshold (owner) | n needed / planned | Checkpoint |
|---|---|---|---|---|---|
| H1 quality ≥ frontier | Banking77, CLINC150, CORD (+SROIE for E4) | paired Δpass vs `frontier`; Tango NI test + bootstrap lower bound | LB > −2.0 pts, point ≥ −1.0 (#1213 t2; #1264 t1) | 1,000–1,550 / 1,500, 1,500, **700**, **680** | A |
| H2 error ≤ α, calibrated | same, accepted answers of `hydra-quality` at target 0.95 | accepted error + CP upper bound; ECE/Brier via `reliability.Evaluate` | error ≤ 7%, ECE ≤ 0.05 (#1213 t3; #1264 t2) | ~910 accepted / per suite | A |
| H3 cost cheaper | same | cost per accepted answer, paired bootstrap ratio | `cost` < frontier, CI excludes 0, pass within 3 pts (#1213 t4) | few hundred / 1,500 | A |
| H4 resolver | mixed header-less test items from the 3 suites | accuracy on non-abstained, abstention rate (Wilson) | ≥ 95%, abstain ≤ 40% (#1213 t5) | ~700 non-abstained / 1,500 mixed | A |
| H5 feedback moves sp | replay of 1,000 `/v1/feedback` events built from calibration-split verdicts | sp > 0.5 for ≥ 1 source per class; idempotent replay | #1213 t6 | 1,000 events / 1,000 | A (no model cost) |
| H6 verifier strength | Banking77/CORD; constrained heads | per-verifier se/sp and D via `reliability.EvaluateSources` | reported (#1264) | n ≥ 200 per verifier | A |
| H7 pooled priors | the E7.2 suites split into 5 pseudo-tenants | ECE of held-out tenant's first 500, pooled vs flat, paired bootstrap | CI excludes 0; error ≤ α + 2 (#1237 t4) | 500 per tenant / reuses A results, no new calls | A (E2) |
| H8 storage latency | n/a (load test, not a suite) | p95 per request | ≤ 10 ms at 50 rps (#1237 t3) | — | A (E2), harness supplies the latency sketch only |
| H9 observation masking | SWE-bench Verified subset (+ Terminal-Bench) | paired solve-rate Δ, cost per accepted incl. cache + fetch tokens | cost CI excludes 0; Δ LB > −1 pt (#1251 t5) | **75 tasks, so the Δ LB of −1 pt is unreachable** (needs thousands); report as inconclusive-by-design or relax | B |
| H10 protected spans | planted-atom corpus | retention rate, exact match | 100% (#1251 t1) | ~10⁴ prompts / 10⁴ | A ($0) |
| H11 multi-hop extraction fails | HotpotQA/MuSiQue subset | paired Δ EM, rung 3 vs off | expected to fail #1251 t3 | ~1,000 | B |
| H12 cached prefix | SWE-bench subset | cache-read share, compressed vs off | not reduced > 1 pt (#1251 t8) | 75 tasks × steps | B |
| H13 span encoders | CrossNER (E4 loader), Banking77 | F1 Δ GLiNER2 vs frontier | within 2 pts on CrossNER; below on Banking77 (#1264 t4) | CrossNER test ~ 1–2k spans / E4 | A (E4) |
| H14 calibrated vs threshold cascade | CLINC150, Banking77, CLINC-10+OOS, CORD, SROIE | cost per accepted, paired, under `balanced` vs `threshold` | CI excludes 0 at pass within 1 pt, on ≥ 2/3 classify + ≥ 1 extract (#1264 t3) | **2,500–6,000 for a 1 pt NI test**; see §3.1 recommendation | A (E4) |
| H15 sticky ≥ frontier | τ²-bench, BFCL v4, Terminal-Bench subset | McNemar/Tango NI, ≥ 3 seeds; cost per success bootstrap | NI 2 pts, cost CI excludes 0 (#1274 t1) | **τ²-bench airline + retail ≈ 164 tasks: NI at 2 pts is not reachable**; pass^k and pooled-trial analysis help only partially | B |
| H16 T2 > T1 | same | exact McNemar superiority / cost | #1274 t2 | same caveat | B |
| H17 latency | latency subset, ABAB | added p95 over direct | ≤ 250 ms, deterministic verifiers, accepted-first (#1213 t7) | 200 per suite | A |
| H18 pricing | none (reads A results) | cost per accepted vs frontier, as the savings basis | E6.E | — | after A |
| H19 seed matrices fix cold start | Banking77, CLINC150, CORD | accepted-answer ECE learning curve at n = 0, 20, 50, 100, 200 calibration observations | ECE(200) ≤ ECE(0) (#1201) | test split as above, 5 curve points × 1 arm | A (E7.4, reviewed in E7.R) |

**Three registered tests look underpowered at the planned n:**
- H1 on CORD/SROIE
- H9
- H15/H16

Each [eval] ticket should record, before the run, either:
- a larger sample (where data exists), or
- a relaxed margin with the user's agreement, or
- "will report the interval; verdict expected inconclusive".

This is a scope finding, not a harness fix (§8 Q6).

## 5. Deferred / out of scope

### 5.1 Deferred to checkpoint B (full build)

| Item | Ticket | Reason |
|---|---|---|
| Live agent arms, τ²-bench, BFCL v4, Terminal-Bench 2.0 | #1202 (E7.5) | They need sessions (E5), `/v1/messages`/Responses losslessness, cache-hit accounting (#1242) and the tool-cache fixes (#1222/#1223). Running them before then measures known bugs. Also $1–3k (§3.2). |
| SWE-bench Verified subset, RAG suite | #1200 (E7.3) | They need the container runner with network refused, plus `grounded`/`judge` verifiers (E1 days 31–60). |
| Compression suites (IFEval padded, RULER, planted-atom) | built by #1251 on this harness | They need E3's rungs and the replay store. Planted-atom is $0 and can land early if E3 is ready. |
| README table generation | #1219 (E7.6) | Blocked on E1.R (#1214) and the README rewrite (#1218) by design: no table before a reviewed verdict. |
| Record/replay proxy, so arms share identical head calls | not ticketed | It would cut cost to about one "head matrix" per suite. But it hides API nondeterminism and distorts latency, and Bedrock's SigV4 makes proxying non-trivial. Revisit if the §8 budget cap binds. |
| Multiple seeds for single-shot suites | — | At temperature 0 they buy little beyond the determinism check (§3.2). Revisit if the check fails. |
| Learning curves beyond n = 200, per-label calibration cells | #1201 follow-up | #1201 fixes n = 0..200. |

### 5.2 Out of scope

| Item | Reason |
|---|---|
| A Python port of the agent harnesses | §1.2: run the maintainers' harness, pointed at a base URL. A port would itself become an integrity risk (ABC, AgentRewardBench). |
| WebArena, OSWorld, GAIA | Agents A10.1: heavy environments, and image-heavy context the cost model has not measured. GAIA is optional and needs web tools. |
| AgentDojo | A safety axis, owned by E5 checkpoint B (#1274). |
| Training or fine-tuning anything on benchmark data (SetFit heads, etc.) | RFC §6 "not building task models"; task-models A.12 Q4 is open, not decided. |
| Writing benchmark items into the user's `evalset` | §1.1: public items would contaminate the corpus `classify` fits on. |
| Hosted or continuous benchmarking service | Pricing appendix: open harness, run locally. Hosted is E6's concern. |
| Leaderboard-style absolute numbers as headlines | RFC §7 and agents A10.1: paired differences against frontier are the claim. Absolute pass rates are context. |
| `hyctl bench` against CLI-agent heads (Claude Code, Codex, Antigravity) | No temperature control, no per-call billed units, and possibly subscription terms against automated benchmarking. They cannot give a "dated price snapshot" row. They are excluded from every arm, which leaves the API heads, Bedrock and local. |

## 6. Exit criteria (E7.1, E7.2, E7.4)

These are the tickets' acceptance criteria made checkable, plus what this scope adds
(marked **+**).

### E7.1 harness (#1198)
1. `hyctl bench run --suite S --arm A --seed N` runs every arm in §1.4 that its
   dependencies allow. `hydra-*` arms are refused with a named missing dependency
   (#1206/#1210), not silently skipped.
2. **Determinism:** two identical runs on a 200-item Banking77 sample differ by
   |Δpass| ≤ 0.5 pt (≤ 1 discordant item), frontier and hydra-quality arms.
3. The oracle arm is ≥ every other arm on every suite run.
4. Metrics match a hand-computed fixture to 1e-9: Δpass, Tango bound, McNemar p,
   bootstrap bound under a fixed seed, cost per accepted, CP bound, ECE/Brier via
   `reliability`, pass^k.
5. No network, model or wall-clock deadline in `go test ./...`; `go test -race ./...`
   green; `internal/bench` has a coverage floor line (**+**).
6. **+** A run killed mid-suite resumes at the first missing item and produces a
   byte-identical `summary.json` (apart from timestamps) to an uninterrupted run on a
   fake executor.
7. **+** `--max-usd` stops a run before the call that would cross it. Tested with a
   fake priced executor.
8. **+** `summary.json` carries every breadcrumb field in §1.5. A test fails if a
   field is empty.
9. **+** The smoke tier completes for ≤ $5 billed, and its measured p_disc, tokens per
   item and escalation rates are written to `bench/results/core/smoke/`.

### E7.2 classification and extraction suites (#1199)
1. Item counts match upstream: Banking77 13,083; CLINC150 full 23,700; CORD 1,000;
   SROIE 973 (or the count the official release gives, recorded with its source).
2. Splits are hash-derived, committed in `bench/splits/`, and stable across runs and
   platforms (a golden test on 20 ids per suite).
3. `bench/DATASETS.md` lists source, revision, sha256, licence (marked "verified" with
   a URL and date, or "unverified"), size and citation. **+** An unverified licence
   blocks `hyctl bench table` from publishing that suite.
4. Verifiers: label match and field exact match after the task-models canonicalisers.
   They are the same `oracle.Oracle` implementations as E1.4 (#1209); a test asserts
   no second copy exists.
5. `go test -race ./...` green.

### E7.4 splits, learning curves, seed matrices (#1201)
1. A test asserts no item id appears in both splits, for every suite.
2. se/sp are fitted on the calibration split only. **+** A test feeds a test-split item
   to the fitter and asserts it is refused.
3. Learning curves at n = 0, 20, 50, 100, 200 calibration observations per class, with
   accepted-answer ECE and pass rate on the test split at each point.
4. `bench/seeds/<pack>/<class>.json` loads into `trust.Calibrator` as a prior. The
   response reports `p_pass_basis: catalogue_prior`, never `measured`.
5. Measurement: ECE(n = 200) ≤ ECE(n = 0), else H19 fails and the failure is recorded
   in #1196.
6. `go test -race ./...` green.

## 7. Risks

| Risk | Detail | Mitigation |
|---|---|---|
| **Contamination** | Banking77, CLINC150, CORD and SROIE (2019–20) and SWE-bench are public and likely in pre-training. Absolute accuracy is inflated, more for frontier models. | Headline only paired Δ against frontier. State "public, likely contaminated" in every summary. Checkpoint B: prefer post-cutoff or private items where suites offer them, and take τ²-bench/Terminal-Bench at their latest versions. Hydra's own claim is relative, so contamination hurts it only if it is uneven across arms. Record each model's stated cutoff in the breadcrumb. |
| **Flawed benchmarks** | SWE-bench Verified: 59.4% of 138 hard problems materially flawed (OpenAI 2026, second-hand per agents A13). τ-bench credited empty responses. Rule-based checkers under-report (ABC, AgentRewardBench). CORD/CLINC label noise is unmeasured. | Paired differences only. Pin the harness version with maintainers' fixes. `INTEGRITY.md` checklist (E7.R, #1220): an empty answer must fail every verifier (a test), plus split hygiene and unpriced cache writes. Hand-audit 50 discordant items per suite at checkpoint A: a discordant pair where the "loser" is right is label noise, and is counted and reported. |
| **Leakage between splits** | A router fitted on its test set measures nothing (task-models A.10). Hydra's own `evalset`/calibration would carry state across runs. | Hash splits, committed. Fresh `HYDRA_HOME` per arm (§1.5). The fitter refuses test ids. The harness never writes the user's evalset. |
| **API nondeterminism** | Temperature 0 is not deterministic on hosted APIs (batching, MoE routing, silent model updates). Anthropic/Bedrock take no seed, and model aliases move. | Pin dated model ids where offered and record the ids the response returns. The determinism gate (§6 E7.1-2) runs before every published run, not once. Interleave arms per item (A, B, A, B) rather than run whole arms back to back, so drift hits both. A model id change mid-run aborts the run. |
| **Silent reasoning or billing differences** | Reasoning tokens billed as output; ScaleDown bills 2.4–2.8× raw tokens (task-models A.3.2); cache writes are billed above input. | Cost from *billed* units reported by the provider, never Hydra's estimate. Fail the run if a no-reasoning arm bills reasoning. The price snapshot is dated. |
| **Cost overrun** | Token estimates in §3.2 are guesses. Checkpoint B is $2–5k. A retry storm on 429 multiplies spend. | Smoke tier first, then re-size. `--max-usd` fails closed, per run and per suite. `health` already parks a head on 429 instead of hammering it. A monthly cap from the user (§8 Q1) is enforced in config, not by memory. |
| **Underpowered verdicts read as passes** | §3.1/§4: CORD/SROIE H1, H9, H15/H16. | "Inconclusive" is a first-class verdict in the result schema and in #1219's table. An interval crossing the margin is never rendered as a pass. |
| **Latency measured on a loaded or sleeping machine** | #1176 shows this machine's timings move under load. The machine also sleeps mid-run. | Latency only from the ABAB subset at concurrency 1, with load average recorded. Resumable runs (§1.5). A gap of more than 60 s between consecutive calls marks the affected latency rows invalid. |
| **Harness depends on unfinished E1 work** | `hydra-*` arms need #1206, #1210, temperature passthrough and a seedable `explore_rate`. | Direct arms, suites, stats and the smoke tier can land first. Hydra arms are refused with a named dependency until then. |
| **Licences** | SROIE is unverified. Some licences forbid publishing results (DeWitt clauses; pricing appendix F.3). Task repos in SWE-bench have their own licences. | Unverified blocks publication (§6 E7.2-3). Data is never committed. Re-check each B suite in its own ticket. |
| **Replay-store privacy** (compression suites) | The planted-atom corpus is built over real user prompts. | It stays local under evalset rules, opt-in, and is never committed. Only aggregate results are published. |



## 8. Questions only the user can answer

**What this machine can reach today.** `hyctl probe` on 2026-11-05 used the local
build `./hyctl2`; `hyctl` is not on PATH. It found:
- **Amazon Bedrock**, via `~/.aws/credentials`.
- **CLI agents:** Claude Code, OpenAI Codex, Antigravity (with its Claude/Gemini
  registry models). These are excluded from bench arms (§5.2).
- **Local:** Ollama `Qwen2.5-Coder:7b` (Q4_K_M), routable.
- **No provider API key is set in the environment.** That covers every variable
  `internal/provider/env` reads: `ANTHROPIC_`, `OPENAI_`, `OPENROUTER_`,
  `GEMINI_`/`GOOGLE_`, `XAI_`, `GROQ_`, `TOGETHER_`, `FIREWORKS_`, `MISTRAL_`,
  `DEEPSEEK_`, `AZURE_OPENAI_`, `PERPLEXITY_`, `COHERE_`, `REPLICATE_`.

So the only metered, temperature-controllable heads are Bedrock and local Qwen. Values
were checked for presence only and never printed.

**Questions:**

1. **Monthly benchmark budget cap (USD).** Checkpoint A is about $120–150 per full run
   plus a ~$3.5 smoke run. Checkpoint B is about $2–5k. Is there a hard monthly cap,
   and should B be split across months?
2. **Which provider keys will benchmarks use?**
   - Bedrock alone gives Claude (Opus/Sonnet/Haiku), Nova, Llama and Mistral. That is
     enough for frontier and cheapest arms, but every head is then Bedrock-hosted, so
     "provider-neutral" (#1213 t1) is tested only weakly.
   - Will you add an OpenAI and/or OpenRouter key (OpenRouter alone covers GPT,
     Gemini and Qwen), or a direct Anthropic key?
   - Should benchmark keys be separate from your day-to-day keys, so spend is
     attributable?
3. **Which model is "frontier"** for the `quality` bar? Opus 4.8 ($5/$25) is the
   conservative choice. GPT-5.4 ($2.5/$15) halves cost. The bar should be the best
   model a buyer would otherwise use, and this choice changes every H1 verdict.
4. **Is the Bedrock region and model access already enabled** for Opus/Sonnet/Nova
   (Bedrock model access is per account and region)? And are Bedrock list prices
   equal to the OpenRouter snapshot used in §3.2?
5. **SROIE.** Do you accept it as E4-only and unpublished until its licence is
   confirmed, or drop it for CORD alone?
6. **Underpowered thresholds (§3.1, §4).** For each, choose: more data, a relaxed
   margin, or a pre-registered "inconclusive expected".
   - H1 on CORD/SROIE at 2 pts
   - H14's "within 1 pt"
   - H9's −1 pt solve-rate bound on 75 SWE tasks
   - H15/H16 at 2 pts on ~164 τ²-bench tasks

   The thresholds are frozen at scope close, so this has to be decided now.
7. **Publishing model outputs.** Is committing per-item answers on public items to
   `bench/results/` acceptable, given provider terms on output use? The alternative is
   committing hashes and verdicts only, which is less auditable.
8. **Local runs while the machine sleeps.** Should full runs move to an always-on host
   (CI runner or VM), with secrets in its store? Or is a resumable laptop run
   acceptable?
