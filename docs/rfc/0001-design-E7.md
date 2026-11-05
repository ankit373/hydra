# RFC 0001 — Design: E7 Benchmark harness (first layer)

Status: draft (design stage, 2026-11-05) · Epic #1197 · Tickets #1198 (E7.1), #1199 (E7.2),
#1201 (E7.4), with #1219 (E7.6) touched by `hyctl bench table`.
Contract: `0001-scope-E7.md` and its Decisions, narrowed by `0001-scope-E1.md` Decision 1.
Verified against branch `docs/rfc-0001-verified-cascade-gateway` at `6aea7ac`.

**What this design takes as fixed:**
- Checkpoint A is **classification only**: Banking77 (the gate) and CLINC150 (reported).
  This comes from E1 Decision 1. CORD moves to B and SROIE is dropped, so there is no
  extraction suite, no `jsonschema` verifier and no cluster bootstrap in A.
- The providers are Bedrock plus OpenRouter. OpenRouter is not configured yet, so every
  tier must also run on **Bedrock alone**.
- The budget for A is $150. The smoke tier comes first and is capped at $5.
- Task classes use E1's dotted keys (`classification.banking77`,
  `classification.clinc150`), not the E7 scope's `classify/banking77`. The calibration
  store keys on this string, and E1 §5 requires the dot.

**Corrections to the E7 scope (K1–K7), found while designing:**

| # | Scope says | What the code says | Effect on this design |
|---|---|---|---|
| K1 | "`serve` passes no temperature … dispatch never sets `executor.Request.Temperature`" | `executor.Request.Temperature *float64` already exists (`internal/executor/executor.go:28`, #1041). Only `serve.Request` and `chatRequest` lack it. | Direct arms need no executor change. Serve still needs passthrough (dependency **E1-D4**, §3.4). |
| K2 | "A way to pin `explore_rate = 0`" | `explore_rate` defaults to 0 (`config.go:75`, omitempty), and a fresh `HYDRA_HOME` has no `config.toml`. | No dependency. The harness writes `explore_rate = 0` explicitly anyway, so the breadcrumb shows it. |
| K3 | Bedrock offers "Claude (Opus/Sonnet/Haiku), Nova, Llama" to the router | Discovery emits **one** Bedrock head, `env/bedrock`, and its model comes from `BEDROCK_MODEL_ID` (`provider/env/provider.go:44-53`, `executor/http.go:937`). There is no Bedrock allowlist like `[openrouter] models`. | A direct arm can call any Bedrock model, because `executor.modelFor` reads `Meta["model"]`. On Bedrock alone, the **router** sees only one Bedrock model (§3.3, §9 R1). |
| K4 | Cost is "billed" | `cost.Row.CostSource` is always `"estimated"` (`cost/cost.go:43`). `pricing.DB` is keyed by OpenRouter ids, and a Bedrock id such as `anthropic.claude-…` falls through to tier pricing (`pricing.go:108-114`). | "Billed USD" here means the token counts the provider reported, times a dated price row the run pins. The harness prices every arm itself (§1.5). |
| K5 | Excluding CLI-agent heads from Hydra arms is assumed to be possible | `hyctl serve` has no head allowlist. CLI heads are found with `exec.LookPath`, and a bare `PATH` makes `shellpath.Adopt` re-add the login shell's `PATH` (`shellpath.go:44-49`). Stripping `PATH` therefore does not work. | New dependency **E1-D5**: a head allowlist for serve (§3.4). |
| K6 | "Arms run on 1 seed; seed controls item order and subsample selection" | A seed that picks the subsample lets someone shop for seeds. | The subsample is fixed by the committed hash rank. The seed controls only the execution order, the arm rotation and the bootstrap (§4.3). |
| K7 | Item id "stable upstream id, or sha256 of the canonical input" | CLINC150 has five duplicated utterances, four of them with **conflicting** labels (§2.3). Banking77 has no ids at all. | Ids are positional within a pinned file. The split is keyed on the text, and conflicting duplicates are excluded. |

## 1. Package layout and types

### 1.1 Packages

```
internal/stats/            pure maths, no I/O. Lifted helpers live here (§1.4).
internal/bench/            core types, runner, resume, budget, splits, fetch, summary, verdicts
internal/bench/suites/     banking77.go, clinc150.go, and a Registry of them
cmd/hydra/bench.go         hyctl bench {fetch,run,report,table}
bench/                     committed: datasets.lock, DATASETS.md, splits/, configs/, results/
```

- **Why `internal/stats` and not `internal/bench/stats`.** The scope says to lift
  `ope.bootstrapInterval` and `entity.wilson` rather than write second copies. Their
  production callers (`internal/ope`, `internal/entity`) would otherwise import a
  package under `bench`, and `bench` imports `net`. A neutral leaf package keeps the
  dependency direction clean. It imports only `math`, `math/rand/v2` and `sort`.
- **Why arms are not a sub-package.** There are three arm kinds (direct, served,
  computed), and all of them need `Item`, `Attempt` and the price snapshot. A sub-package
  would export those types just to cross a boundary.
- **Egress.** `internal/bench` imports `net/http` for fetch and serve. Nothing reachable
  from `internal/evalset` imports `internal/bench`, and the existing
  `TestNoHydraPackageReachableFromTheCorpusCanEgress` keeps it that way. The harness
  never writes to an evalset (scope §1.1).
- **Coverage.** `ci/coverage-floors.txt` gains lines for `internal/stats` (floor 95),
  `internal/bench` (85) and `internal/bench/suites` (90), each in the PR that creates it.

### 1.2 Core types (`internal/bench`)

```go
package bench

// Split is fixed by bench/splits/<suite>.tsv, never computed at run time.
type Split uint8

const (
	SplitCalibration Split = iota + 1
	SplitTest
	SplitExcluded // in the upstream file, scored by nobody (conflicting duplicate, §2.3)
)

// Item is one benchmark question. Input and Gold stay raw so a later extraction
// suite needs no change here.
type Item struct {
	ID    string          // "<upstream-file>/<row>", e.g. "test/0412" (§2.3)
	Class string          // E1 task class, e.g. "classification.banking77"
	Split Split
	Rank  uint64          // first 8 bytes of sha256("rank\x00"‖suite‖"\x00"‖ID); tiers take a prefix
	Input json.RawMessage // {"text": "..."}
	Gold  json.RawMessage // {"label": "card_arrival"}
}

// Suite is one dataset with one verifier. Prompt is identical for every arm.
type Suite interface {
	Name() string                    // "banking77"
	Class() string                   // "classification.banking77"
	Lock() []LockEntry               // the files it needs, from bench/datasets.lock
	Load(dataDir, splitsFile string) ([]Item, error)
	Labels() []string                // canonical label set, read from the data
	Prompt(it Item) executor.Request // Temperature = &0, MaxTokens = 32, no System override per arm
	Grade(ctx context.Context, it Item, answer string) (oracle.Verdict, error)
}

// Arm answers one item. Price returns the worst case before the call, so the
// budget can refuse it; Call returns what the provider reported.
type Arm interface {
	Name() string    // "frontier", "cheapest", "static-fallback", "hydra-quality", ...
	Config() ArmConfig
	Preflight(ctx context.Context) error // nil, or *MissingDependency (§3.4)
	WorstCaseUSD(req executor.Request) (float64, error)
	Call(ctx context.Context, it Item, req executor.Request) (Attempt, error)
}

type ArmConfig struct {
	Kind      string   `json:"kind"`             // "direct" | "served" | "computed"
	Models    []string `json:"models"`           // provider model ids, in fallback order
	Heads     []string `json:"heads,omitempty"`  // served: the router heads allowed (E1-D5)
	Objective string   `json:"objective,omitempty"`
	Target    float64  `json:"target,omitempty"` // served: P(correct) target, 0.95
	FaultRate float64  `json:"fault_rate,omitempty"`
}

// Attempt is one arm's answer to one item, everything a provider or serve reported.
type Attempt struct {
	Answer        string        `json:"answer"`
	RequestID     string        `json:"request_id,omitempty"` // serve's X-Request-Id
	Calls         []Call        `json:"calls"`                // every billed call, escalations included
	Sources       []string      `json:"sources,omitempty"`    // X-Hydra-Sources, in order consulted
	Accepted      string        `json:"accepted,omitempty"`   // the source whose answer was returned
	Decision      string        `json:"decision"`             // "accept" | "abstain" | "exhausted"; direct arms: "accept"
	PCorrect      *float64      `json:"p_correct,omitempty"`  // X-Hydra-P-Correct; nil when not stated
	PPassBasis    string        `json:"p_pass_basis,omitempty"`
	Escalations   int           `json:"escalations"`
	Latency       time.Duration `json:"latency_ns"`
	TTFT          time.Duration `json:"ttft_ns,omitempty"` // 0 = unknown, never instant
	ErrKind       string        `json:"err_kind,omitempty"` // "transport" | "status:<code>" | "format" | "budget" | ""
}

// Call is one provider call, priced by the harness from the snapshot.
type Call struct {
	Head            string  `json:"head"`
	Model           string  `json:"model"`            // as the provider reported it
	InputTokens     int     `json:"input_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	TokensEstimated bool    `json:"tokens_estimated"` // true refuses the row for a direct arm (§1.5)
	CostUSD         float64 `json:"cost_usd"`
	PriceRef        string  `json:"price_ref"`        // the snapshot row used
}

// Result is one line of items.jsonl: one (item, arm) pair, appended and fsynced.
type Result struct {
	Seq     int            `json:"seq"`      // position in the deterministic schedule (§4.3)
	ItemID  string         `json:"item_id"`
	Split   string         `json:"split"`    // "calibration" | "test"
	Arm     string         `json:"arm"`
	Attempt                                 // embedded
	Passed  bool           `json:"passed"`
	Score   float64        `json:"score"`    // 1/0 for classification
	Detail  string         `json:"detail,omitempty"`
	CostUSD float64        `json:"cost_usd"` // Σ Calls[].CostUSD
	Started time.Time      `json:"started"`
}

// Run is the frozen description of one run, written to run.json before the first
// call. Its hash is the run id's suffix, so the same command resumes the same run.
type Run struct {
	ID       string             `json:"id"`        // "<track>-<suite>-<tier>-s<seed>-<cfg8>[-r<k>]"
	Track    string             `json:"track"`     // "core" in A
	Suite    string             `json:"suite"`
	Tier     string             `json:"tier"`      // "fixture" | "smoke" | "full"
	Seed     uint64             `json:"seed"`
	MaxUSD   float64            `json:"max_usd"`
	Arms     map[string]ArmConfig `json:"arms"`
	ArmOrder []string           `json:"arm_order"`
	ItemIDs  []string           `json:"item_ids"`  // the scheduled test items, in Rank order
	CalibIDs []string           `json:"calib_ids,omitempty"`
	Prices   PriceSnapshot      `json:"prices"`
	Crumb    Breadcrumb         `json:"breadcrumb"`
}
```

- `oracle.Verdict` gains `Score float64` in E1 step 4 (#1209). Until then a suite fills
  `Passed` and the harness derives `Score` as 1 or 0. No bench-side verdict type exists.
- `Result` keeps `Calls` per attempt, because H3 counts the cost of escalations and of
  failed calls, not only the cost of the accepted answer.

### 1.3 `hyctl bench`

```
hyctl bench fetch [<suite>...] [--data-dir DIR]
hyctl bench run   --config bench/configs/<tier>.toml [--suite S] [--arms a,b] [--seed N]
                  [--max-usd X] [--limit n] [--rerun] [--dry-run]
hyctl bench report <run-id> [--json]
hyctl bench table  <run-id>... [--out FILE]
```

| Command | Network | What it does |
|---|---|---|
| `fetch` | yes (dataset hosts) | Downloads each file in `bench/datasets.lock`, checks its sha256, and writes it into the data dir (§2). It is idempotent and verifies files already present. |
| `run` | yes (providers) | Writes `run.json`, runs the schedule, appends `items.jsonl`, then writes `summary.json`. The run id is derived from the config, so running the same command again **resumes** (§4). `--rerun` starts a fresh run with suffix `-r<k>` (used for the determinism check). `--dry-run` prints the schedule and the worst-case cost and makes no call. |
| `report` | no | Recomputes `summary.json` from `items.jsonl` and `run.json`, then prints it. Running it twice gives byte-identical output. |
| `table` | no | Reads only `summary.json` files and writes the markdown table (§5.3). |

Exit codes:
- 0: done.
- 1: error.
- 2: stopped by `--max-usd`. The run can be resumed with a higher cap.
- 3: an arm was refused for a missing dependency.
- 4: the contract was violated mid-run (§3.4).

The commands are wired in `cmd/hydra/main.go`'s `root.AddCommand` as `cmdBench()`. That
function is the seam, in the same way as `serveRouter`: `internal/bench` takes a
`Discover func(env []string) ([]provider.Head, error)` and an `ExecFor func(provider.Head)
executor.Executor`, so its tests need no provider.

### 1.4 Statistics (`internal/stats`)

Everything here is deterministic given its inputs and seed. Paired inputs are aligned by
item id before they reach this package.

```go
package stats

// Discordant counts a paired binary comparison of arm A against reference R.
type Discordant struct {
	N int // items both arms answered
	B int // A passed, R failed
	C int // A failed, R passed
}
func (d Discordant) Delta() float64 // (B − C) / N

// PairedDiscordant aligns two pass vectors. It is an error if their lengths differ.
func PairedDiscordant(a, r []bool) (Discordant, []float64, error) // d_i ∈ {−1,0,1}

// Bootstrap is a sorted sample of a resampled statistic. Undefined resamples are
// dropped and counted, never replaced with 0.
type Bootstrap struct {
	Values  []float64
	Dropped int
}
func (b Bootstrap) Quantile(q float64) float64 // nearest rank, as ope.percentile does today

// Resample draws n indices with replacement, `resamples` times, from
// rand.NewPCG(seed, 0x9E3779B97F4A7C15), the stream ope uses today. It returns
// the sorted values of stat. ok=false drops that resample.
func Resample(n, resamples int, seed uint64, stat func(idx []int) (v float64, ok bool)) Bootstrap

// WeightedMean is ope's existing estimator, rewritten over Resample.
// ope.bootstrapInterval becomes WeightedMean(...).Quantile(tail), Quantile(1−tail).
func WeightedMean(values, weights []float64) func(idx []int) (float64, bool)

// PairedMeanDiff is the H1 statistic: the mean of d over the resampled items.
func PairedMeanDiff(d []float64) func(idx []int) (float64, bool)

// CostPerAcceptedDiff is the H3 statistic: Σcost_A/Σacc_A − Σcost_R/Σacc_R over the
// same resampled items. ok=false when either arm has no accepted answer in the resample.
func CostPerAcceptedDiff(costA, costR []float64, accA, accR []bool) func(idx []int) (float64, bool)

// TangoNI is Tango's (1998) score test for paired proportions. H0: Δ ≤ −margin.
// It returns the score z and the one-sided p = 1 − Φ(z). It is an error if N = 0,
// margin ∉ [0,1), or the restricted MLE is undefined.
func TangoNI(d Discordant, margin float64) (z, p float64, err error)

// TangoLowerBound inverts TangoNI. It returns the smallest −margin that the test
// does not reject at alpha, found by bisection to 1e-12 on (−1, Delta()].
func TangoLowerBound(d Discordant, alpha float64) (float64, error)

// McNemarExact is the conditional binomial test on B of B+C at p = ½.
// OneSided is P(X ≥ B), the "A beats R" direction. TwoSided = min(1, 2·min tail).
func McNemarExact(d Discordant) (oneSided, twoSided float64)

// ClopperPearsonUpper is the exact one-sided upper bound on a binomial rate. k == n gives 1.
func ClopperPearsonUpper(k, n int, alpha float64) (float64, error)
func ClopperPearsonLower(k, n int, alpha float64) (float64, error)

// Wilson is lifted from internal/entity (wilson, measure.go:96). entity then calls
// Wilson(k, n, 1.959964).
func Wilson(k, n int, z float64) (lo, hi float64)

// Trials is one task's repeated attempts, for pass^k (checkpoint B, τ²-bench).
type Trials struct{ N, C int }

// PassHatK is τ-bench's pass^k = mean over tasks of C(c,k)/C(n,k). It is an
// error if any task has N < k. The ratio is computed as a product, so it never overflows.
func PassHatK(tasks []Trials, k int) (float64, error)

func NormalCDF(x float64) float64      // 0.5·(1+erf(x/√2))
func NormalQuantile(p float64) float64 // √2·math.Erfinv(2p−1)
```

**Tango's statistic, as implemented.** First is the arm and second is the reference.
Write n₁₂ = B, n₂₁ = C and θ₀ = −margin. Then:
- A = 2N
- B̃ = −B − C + (2N − B + C)·θ₀
- C̃ = −C·θ₀·(1 − θ₀)
- p̃₂₁ = (−B̃ + √(B̃² − 4AC̃)) / 2A
- z = (B − C − Nθ₀) / √(N·(2p̃₂₁ + θ₀ − θ₀²))

At θ₀ = 0 this reduces exactly to McNemar's z = (B − C)/√(B + C). That identity is a test.

**Cost accounting (`internal/bench`, `cost.go`):**

```go
// Price is one dated row. It is copied into run.json, so a later pricing refresh
// cannot change an old run's numbers.
type Price struct {
	Model     string    `json:"model"`      // the id sent to the provider
	Ref       string    `json:"ref"`        // pricing.DB key used, or "override"
	InPerM    float64   `json:"in_per_mtok"`
	OutPerM   float64   `json:"out_per_mtok"`
	Source    string    `json:"source"`     // "openrouter" | "config:<url>"
	FetchedAt time.Time `json:"fetched_at"` // pricing_cache.json fetched_at, or the override's date
}
type PriceSnapshot map[string]Price // keyed by Model

// Snapshot resolves every model a run can call, before the run starts. A model it
// cannot price is an error, so a run never starts that it cannot account for.
func Snapshot(db *pricing.DB, fetchedAt time.Time, models []string, refs map[string]string,
	overrides map[string]Price) (PriceSnapshot, error)
func (s PriceSnapshot) Cost(model string, in, out int) (float64, error)

// Budget enforces --max-usd before each call. It starts with what items.jsonl
// already spent, so a resumed run cannot spend the cap twice.
type Budget struct { /* max, spent float64; mu sync.Mutex */ }
var ErrBudget = errors.New("bench: the next call could exceed --max-usd")
func NewBudget(maxUSD, alreadySpent float64) *Budget
func (b *Budget) Reserve(worstCase float64) error // ErrBudget if spent + worstCase > max
func (b *Budget) Settle(reserved, actual float64)
func (b *Budget) Spent() float64
```

- **Worst case per call:** `(est_input_tokens × in_price + MaxTokens × out_price) / 1e6`.
  - The input estimate is `len(prompt)/3`. That is deliberately above the `len/4` that
    dispatch uses, so the guard errs toward refusing.
  - A served arm multiplies this by `1 + max_escalations` and takes the most expensive
    head it allows.
- **Settling.** A call is settled at its actual price, computed from the token counts the
  provider reported. A call that errors after the provider may have billed it is settled
  at its reservation, because the cost is unknown and the guard fails closed.
- **Cost per accepted answer** = Σ `CostUSD` over all rows of the arm, failed and errored
  ones included, ÷ #accepted. A direct arm accepts every item it answered. A served arm
  accepts where `Decision == "accept"`.
- Every arm, served ones included, is priced from tokens × snapshot, never from
  `X-Hydra-Cost-USD`. Each arm is therefore priced the same way. The header is recorded
  and cross-checked; a disagreement over 1e-6 is flagged in `summary.json` (§9 R4).

## 2. Dataset fetching and splits

### 2.1 Pinned sources (`bench/datasets.lock`)

Commits were resolved with `git ls-remote` on 2026-11-05. Each file was downloaded once at
that commit and hashed. Counts are from parsing the files, not quoted from upstream.

```toml
[banking77]
revision = "PolyAI-LDN/task-specific-datasets@57ec275d8078af65b7731c2a98be812d844a6d6b"
licence  = "CC-BY-4.0"   # LICENSE at that commit, verified 2026-11-05
[[banking77.file]]
name   = "train.csv"
url    = "https://raw.githubusercontent.com/PolyAI-LDN/task-specific-datasets/57ec275d8078af65b7731c2a98be812d844a6d6b/banking_data/train.csv"
sha256 = "b06e26ac675513959a63135f11b94ea7786ed02da65db93a5650d8838cbc664b"
[[banking77.file]]
name   = "test.csv"
url    = "https://raw.githubusercontent.com/PolyAI-LDN/task-specific-datasets/57ec275d8078af65b7731c2a98be812d844a6d6b/banking_data/test.csv"
sha256 = "d12d6e3bc4c3103966ae786dc435913c0c563dfa328f5a3646d0e62cfeeb474d"
[[banking77.file]]
name   = "categories.json"
url    = "https://raw.githubusercontent.com/PolyAI-LDN/task-specific-datasets/57ec275d8078af65b7731c2a98be812d844a6d6b/banking_data/categories.json"
sha256 = "53261da888122daf2d120d925458631d9619e15d82e56052e7a42e535ce32b63"

[clinc150]
revision = "clinc/oos-eval@828f8093932c8fe6ca7936c3d2e52903b1c523de"
licence  = "CC-BY-3.0"   # LICENSE at that commit, verified 2026-11-05
[[clinc150.file]]
name   = "data_full.json"
url    = "https://raw.githubusercontent.com/clinc/oos-eval/828f8093932c8fe6ca7936c3d2e52903b1c523de/data/data_full.json"
sha256 = "36923c3705a59e08fe9c3883d8bc2dd966ef93e22cb78ac41171782a698d56e0"
```

| Suite | Items (parsed) | Labels | Notes |
|---|---|---|---|
| Banking77 | 10,003 train + 3,080 test = **13,083** (matches upstream) | 77 (= `categories.json`) | 13 utterances contain an embedded newline inside CSV quotes, so the loader uses `encoding/csv`, never line splitting. No duplicate texts. |
| CLINC150 `data_full.json` | train 15,000 · val 3,000 · test 4,500 · oos_train 100 · oos_val 100 · oos_test 1,000 = **23,700** (matches) | 150 + `oos` | Five duplicated utterances, four with conflicting labels (§2.3). |

`bench/DATASETS.md` carries, per suite:
- the URL;
- the revision;
- each file's sha256;
- the licence, marked *verified* with the LICENSE URL at the pinned commit and the date;
- the counts above;
- the citation (Casanueva et al. 2020; Larson et al. 2019);
- "public since 2019–2020, likely in pre-training data".

**Licence obligations.** CC BY 3.0 and CC BY 4.0 both allow redistribution with
attribution, so committing per-item answers and ids is allowed (Decision 3). The raw data
is still **not** committed: the scope says so, and the lockfile is enough to reproduce it.
`hyctl bench table` refuses to render a suite whose licence is not marked verified
(scope §6, E7.2-3).

### 2.2 Fetching and caching

- **Data dir.** The default is `$HYDRA_HOME/bench/data/<suite>/`, resolved through
  `config.Dir()` so `HYDRA_HOME` is honoured. `--data-dir` overrides it. Data lives
  outside the repo.
- **Download.** The file goes to `<name>.part` in the same directory, and the sha256 is
  computed while streaming.
  - If the hash matches, the file is fsynced and renamed into place.
  - If it does not, the `.part` file is deleted and the error names the expected hash,
    the actual hash and the URL. Nothing is "fixed up".
- **Retry.** Three attempts with 2 s, 4 s, then 8 s backoff, on transport errors and 5xx
  only. A 404 or a hash mismatch is final, because retrying it cannot help.
- **Verify on load.** `Suite.Load` re-hashes every file on each run (≈ 3 MB, well under
  100 ms) and refuses on a mismatch. `run.json` records the hashes. A hash cache was
  rejected: the cost is negligible, and a stale cache is how a corrupted file gets scored.
- **Size bound.** `io.LimitReader` at 64 MB per file. The largest file is ~2 MB.
- **Never in `go test`.** Tests serve fixture files from `httptest.Server`. The default
  base URL is a package variable that tests replace, the same pattern as
  `update.ReleaseURL`.

### 2.3 Item ids and the committed splits

- **Ids are positional in the pinned file**:
  - Banking77: `train/00000`–`train/10002` and `test/0000`–`test/3079`, as the 0-based
    data row after the header.
  - CLINC150: `<key>/<index>`, e.g. `oos_test/0999`.

  The sha256 pins the file, so a position is stable. A text hash would collide on
  CLINC's duplicates.
- **Split key.** The split is computed from
  `sha256("split\x00" ‖ suite ‖ "\x00" ‖ canon(text))`, **not** from the id. Two copies of
  the same utterance therefore always land in the same split, so a calibration copy cannot
  leak an answer into the test split. `canon` is NFC, case-folded, with whitespace
  collapsed: the same canonicaliser the grader uses (§3.1).
- **Bucket.** Let `bucket` be the first 8 bytes of the split hash, mod 1000.
  `bucket < 300` gives calibration and the rest gives test, a 30/70 split (scope §2.1).
  Upstream train/val/test is ignored, because no arm trains (scope §1.3).
- **Excluded items.** Utterances whose duplicates carry **different** gold labels are
  `excluded` in both copies. They are label noise that no arm can score.
  - CLINC150 has four such pairs, eight items in total:
    - "what is on my to do list": `reminder` / `todo_list`
    - "turn up your volume": `change_volume` / `whisper_mode`
    - "what's your designation": `what_is_your_name` / `user_name`
    - "where did you grow up": `how_old_are_you` / `where_are_you_from`
  - The agreeing pair ("hey what's up", `greeting` twice) is kept, and the split key
    places both copies in the same split.
  - Banking77 has none.
- **Rank.** `Rank` comes from a separate hash, `sha256("rank\x00"‖suite‖"\x00"‖id)`.
  - A tier takes the first *n* test items in rank order. Smoke and full are therefore
    nested, and smoke's 100 items are the first 100 of full's 1,500.
  - Calibration takes items in rank order, **round-robin over gold labels**, so 500
    calibration items cover every label: 6–7 per Banking77 label and 3–4 per CLINC150
    label. This is E1's "stratified by label".
- **`bench/splits/<suite>.tsv`** is committed, with columns `id  split  rank  label`.
  - Banking77 is 13,083 rows (~0.5 MB) and CLINC150 is 23,700 rows (~0.9 MB).
  - The label column lets a reviewer audit the stratification without fetching the data.
  - The file is generated once, by `go generate` in `internal/bench/suites`, from the
    fetched data. It is never computed at run time. `Load` joins on the id and refuses
    if a data id is missing from the TSV or a TSV id is missing from the data.
- **The split file's sha256 goes into `run.json`.**

**Guards:**
- Each test is described in §7.
- A test asserts that no id, and no canonical text, appears in both calibration and test,
  for every suite.
- A golden test pins the split and rank of 20 named ids per suite, so they stay stable
  across platforms.
- The calibration filler refuses any item whose `Split != SplitCalibration` (§3.3).

## 3. Arms

### 3.1 The prompt and the grader, shared by every arm

- **Prompt.** `Suite.Prompt` returns one `executor.Request` for each item:
  - `System`: the rubric, which lists every label one per line and says "Answer with
    exactly one label from the list, and nothing else."
  - `Prompt`: the utterance, inside `util.WrapUntrusted`.
  - `Temperature: &0`.
  - `MaxTokens: 32`. The longest label is under 15 tokens.

  Every arm sends this same request. A served arm sends it as `system` and `user`
  messages with the same strings.
- **Grader.** `Suite.Grade` builds E1's `match` oracle (#1209) with the singleton set
  `{gold}` and calls `Verify(ctx, answer, trust.Task{Domain: class})`. Correctness and the
  serve-side "is this a valid label" check therefore share one canonicaliser. It does NFC,
  casefolds, trims Unicode whitespace, and strips one layer of matching quotes or
  backticks. Nothing else is lenient.
  - An answer that is not in the label set is a `format` fail. It is counted separately
    in `summary.json` (`format_fail_rate`), so verbosity cannot hide as wrongness.
  - An empty answer fails (the INTEGRITY rule; it is a test).
  - CLINC's `oos` is an ordinary label.
- **Who owns `internal/oracle/match.go`.** E1 step 4 lists `match.go` under #1209, and
  the scope requires "no second copy". The pure part has no dependencies: the
  canonicaliser and an exact match over a label set. It lands first, as PR 0 (§8), under
  #1209's number. E1 step 4 then only wires it into dispatch and route config.
  **Blocking question Q1.**

### 3.2 Direct arms

A direct arm builds a `provider.Head` and calls `executor.For(head).Execute`, the same
wire code that Hydra uses (scope §1.4).

```go
type Direct struct {
	name   string
	heads  []provider.Head // len 1, except static-fallback
	exec   func(provider.Head) executor.Executor
	prices PriceSnapshot
	faults *FaultPlan // static-fallback only; nil otherwise
}
```

| Arm | Heads | Behaviour |
|---|---|---|
| `frontier` (B1) | one, the frontier model | One call. Its error is the item's `ErrKind`, and the item fails. |
| `cheapest` (B2) | one, the cheapest model that parsed ≥ 95% of smoke answers into the label set | One call. |
| `static-fallback` (B3) | an ordered list, frontier first | Moves to the next model only on a transport error, a 429 or a 5xx (`executor.StatusError`). A 4xx or a wrong answer never moves. |
| `oracle` | none (computed) | Defined per item over the direct arms run in the same run. It passes if any of them passed, and its cost is the cheapest passing call. It is computed in `report`, never called. |
| `threshold` | cheapest, then frontier | **Not in A.** E1 freezes T8 as "not run" at A. Its design note is in §9 R7. |

- **Building a head:**
  - **Bedrock:** `Head{ID: "bench/bedrock/<model>", Provider: "bedrock", Source: "env",
    Meta: {"model": <id>}}`. `executor.modelFor` sends `Meta["model"]` (K3), so any
    enabled Bedrock model is reachable, with credentials from `internal/awsconf`.
  - **OpenRouter:** `Head{ID: "openrouter/<lower id>", Provider: "openrouter", Source:
    "env", Meta: {"model": <id>}}`. This is the shape `provider/env/openrouter.go:55`
    discovers.
- **Unroutable heads.** Before any call, the arm asks `executor.Unroutable(head)` for
  each head and refuses with that reason if one is unroutable. A missing key or region is
  named up front, not discovered on item 1.
- **Fault injection for B3.** The injector sits in the harness, wrapped around the first
  head's executor.
  - Item *i* is faulted when the first 8 bytes of `sha256("fault\x00"‖seed‖"\x00"‖id)`,
    taken mod 10⁴, are below `fault_rate·10⁴`.
  - It returns a synthetic `executor.StatusError{Code: 503}` (or 429 with
    `Retry-After: 0`) **without calling the provider**, so an injected fault costs nothing.
  - It runs on a 200-item subset in the full tier only. With no faults B3 equals B1, so
    without injection it measures nothing.
- **What a direct arm refuses:**
  - A call with `TokensEstimated = true`. Bedrock and OpenRouter report usage, so an
    estimate means something is wrong. The run aborts with exit 4.
  - A call that reports `OutputTokens > MaxTokens` (32). Hidden reasoning is billed as
    output (scope §3.2), and `executor.Response` has no reasoning-token field, so this is
    the only signal available. The run aborts with exit 4.
  - A provider-reported model id that differs from the first call's. The model moved
    mid-run, so the run aborts (scope §7, API nondeterminism).

### 3.3 Hydra arms: `hyctl serve` on a fresh `HYDRA_HOME`

```go
type Served struct {
	name      string           // "hydra-quality" | "hydra-cost" | "hydra-default"
	objective string           // "quality" | "cost" | "" for hydra-default
	home      string           // $HYDRA_HOME/bench/runs/<run-id>/homes/<arm>
	heads     []string         // the router heads allowed, from config (E1-D5)
	base      string           // http://127.0.0.1:<port>/v1, chosen by the harness
	client    *http.Client     // no proxy, no redirects, 120 s timeout
}
```

**Lifecycle per served arm:**

1. **Make the home.** `home = $HYDRA_HOME/bench/runs/<run-id>/homes/<arm>/` sits under
   the *operator's* Hydra dir, outside the repo, and persists so the run can resume.
   - It gets a `config.toml` with these lines and nothing else:
     - `explore_rate = 0`
     - `cache_answers = false`
     - `capture_payloads = false`
     - `capture_embeddings = false`
     - `[openrouter] models = [...]`, from the bench config, only when OpenRouter heads
       are configured
   - No `registry/` override, so the embedded registry is used and `config.Breadcrumb()`
     names it.
   - No `logs/state.json`, so `claude_pct` reads 0 and `claudeMode`'s emergency
     `LocalOnly` (E1 C12) cannot fire.
   - The answer cache stays off explicitly. One cached answer served for another item
     would be one arm answering with another item's output.
2. **Copy in calibration.** `calibration.jsonl` comes from the run's calibration
   template, `homes/_calibration/` (step 3 below). Its sha256 goes into `summary.json`.
   Nothing else is copied: not the developer's calibration, evalset, ledger or trust log.
3. **Start serve.** The harness picks a free loopback port by listening on `:0` and
   closing. It then runs `os.Executable() serve --addr 127.0.0.1:<port>`, and retries with
   a new port up to three times if the bind loses the race. Serve's banner is never
   parsed.
   - The flags: `--task-class <class>`, `--heads <ids>` (E1-D5), and `--enum` left unset,
     so objective routing applies.
   - The environment: `HYDRA_HOME=home`, `HYDRA_NO_UPDATE_CHECK=1`, and
     `BEDROCK_MODEL_ID=<the configured Bedrock router model>`. The parent's `PATH` and its
     provider keys pass through; keys are never logged.
   - serve dies with the harness, via `exec.CommandContext` plus process-group kill on
     exit.
4. **Preflight** (§3.4). It costs nothing, and refuses the arm if anything is missing.
5. **Each item.** `POST /v1/chat/completions` with:
   - `model: "hydra"`
   - `temperature: 0`
   - `max_tokens: 32`
   - headers `X-Hydra-Objective: <o>` and `X-Hydra-Task-Class: <class>`

   The harness reads the answer, `X-Request-Id`, and the evidence headers (#1210):
   `-Sources`, `-P-Correct`, `-Decision`, `-Escalations`, `-Cost-USD` and
   `-P-Pass-Basis`.

   `Calls` comes from `home/logs/cost.jsonl`, the rows whose `run_id` equals the
   request id. serve sets the run id to the request id (`cmd/hydra/serve.go:53`). Each
   row gives head, model and tokens, and the harness prices them from the snapshot.
   - The home is private to the arm, so the join is unambiguous.
   - A request whose rows cannot be found is `ErrKind: "unpriced"`. It is reserved at
     its worst case and counted, never priced at $0.

**Filling calibration from the calibration split.** This is E1's
"`hyctl trust record --said-correct` on the calibration split", with no new endpoint.

- **What it runs.** `hyctl bench run` runs a `calibrate` phase before any served arm. It
  writes `calib.jsonl` in the run dir, with the same `Result` rows, `Split =
  "calibration"` and `Arm = "calib:<head>"`.
- **Which heads.** The phase discovers heads in-process with the served arm's
  environment, through the same `Discover` that serve will run. It keeps the heads in
  `heads`. The Head structs are then the router's own, with the router's IDs.
- **What it records.** For each such head *H* and each calibration item, it calls *H*
  directly (the direct-arm path), grades the answer, and then calls:

  ```go
  cal, _ := trust.New(filepath.Join(template, "calibration.jsonl"))
  cal.Update(H.ID, class, true, outcome)                         // = hyctl trust record --source H --domain class --said-correct --outcome …
  cal.Update("verifier:match:"+suite, class, matchVerdict.Passed, outcome) // the verifier's own se/sp (H6)
  ```

  These are the exact calls `cmdTrustRecord` makes (`cmd/hydra/main.go:5304-5312`), into
  a file the harness owns. The verifier row is the only source of true negatives in A: a
  generator always says `said-correct=true` (#771).
- **One function, two callers.** `cmdTrustRecord` and the filler both call
  `trust.RecordOutcome(path, source, domain string, said bool, o Outcome) error`. This is
  a tiny extraction from `cmdTrustRecord`, so the two cannot drift. A subprocess per row
  would be 1,000–3,000 `hyctl` spawns and would change nothing about the result.
- **How many.** `n_cal` per class is 40 in smoke and 500 in full. 40 is at least
  `rank.MinCommitments` (20), so every head in `heads` reads as `measured` (E1 §1). The
  calibration items are scheduled, resumed and budgeted like test items.
- **Refusal.** The filler refuses any item whose split is not calibration, with
  `ErrTestItemInCalibration`. A test asserts it (scope §6, E7.4-2).

**Non-vacuity.** For each item the harness records whether the first of `Sources`
differs from the frontier's router head. The frontier's router head is the head in
`heads` whose model equals the frontier model, named in config, so the comparison is
exact. Below 10%, H1's verdict is `inconclusive-vacuous` (§5.4).

### 3.4 Refusing an arm when an E1 dependency is missing

`Preflight` runs before any call, and every check costs $0. A missing dependency returns a
typed error. The run prints it and exits 3, and **no** row is written for that arm. Other
arms still run.

```go
type MissingDependency struct {
	Arm    string
	Ticket string // "#1206"
	What   string // "X-Hydra-Objective is not honoured"
	Probe  string // what was sent and what came back
}
```

| Id | Needed for | Ticket | Free preflight probe | Checked again on the first real response |
|---|---|---|---|---|
| E1-D1 | objective routing on `model: "hydra"` | #1206 | `POST` with `X-Hydra-Objective: __bench_probe__` must return **400** before routing (E1: "unknown value → 400"). A 200 or a timeout means unsupported. | `X-Hydra-Objective` echoes the value sent |
| E1-D2 | task class reaches routing | #1208 | `hyctl serve --help` lists `--task-class` (parsed from the same binary) | `X-Hydra-Task-Class` equals the class, and its basis is `header` |
| E1-D3 | evidence headers | #1210 | none for free | `-Sources`, `-Decision`, `-P-Correct`, `-Cost-USD`, `-Escalations` and `-P-Pass-Basis` are all present |
| E1-D4 | `temperature` passthrough | **not in any E1 ticket** (§9 Q2) | `POST` with `temperature: "x"` must return 400 (the field is parsed). Today it is silently ignored. | `X-Hydra-Temperature` echoes 0, a header to add with #1210 |
| E1-D5 | head allowlist, so CLI agents are excluded | **new, not ticketed** (§9 Q3) | `GET /v1/models` lists only `heads` plus routing ids. `hyctl probe --json` under the same env shows that every routable head outside `heads` is unreachable to serve. | `X-Hydra-Sources` ⊆ `heads` on every item; otherwise exit 4 |
| E1-D6 | `match` verifier in the route config | #1209 | `hyctl serve --help` lists the route verifier flag or config key (named by #1205) | the sources include `verifier:match:<suite>` |

- **Refuse or skip.** A missing dependency **refuses** the arm (exit 3, named in the
  output and in `summary.json` under `refused`). It never runs the arm in a degraded form.
  - The one sanctioned fallback is `hydra-default`. If only E1-D1 is missing, it runs
    `model: "hydra"` with no objective header. Its rows are recorded as `hydra-default`
    and never as `hydra-quality` (scope §1.4).
- **A check that fails mid-run aborts the run with exit 4.** That covers a missing header
  on any item, or a source outside `heads`. The bad item's row is written with
  `ErrKind: "contract"`, so the spend is recorded, and the run is resumable once the
  cause is fixed.
- **Probing the binary.** The probes ask the binary under test, `os.Executable()`, rather
  than checking Go symbols, so `internal/bench` does not import E1's new API. The Hydra
  arms PR can therefore merge before E1 does, and it refuses until E1 lands (§8).

## 4. Run resumability and budget

### 4.1 Layout of one run

```
bench/results/<run-id>/              committed (Decision 3: answers on public items are committed)
  run.json                           frozen config, written before the first call (§1.2 Run)
  calib.jsonl                        calibration-phase rows (served arms only)
  items.jsonl                        one Result per (test item, arm), append-only
  summary.json                       derived from the three files above by `report`
  table.md                           written by `hyctl bench table`
$HYDRA_HOME/bench/runs/<run-id>/     not committed
  homes/<arm>/                       fresh HYDRA_HOME per served arm (§3.3)
  homes/_calibration/                the calibration template
  run.lock                           util.Lock: one writer per run
```

**Run id:** `<track>-<suite>-<tier>-s<seed>-<cfg8>`, for example
`core-banking77-smoke-s1-3f9a2c1d`.
- `cfg8` is the first 8 hex of the sha256 of the canonical JSON of `run.json`'s inputs:
  arms, models, prices, item ids, the split file hash and the data hashes. `max_usd` is
  left out on purpose (§4.4).
- There is no date in the id. Running the same command again resolves to the same id and
  resumes, even across midnight.
- `--rerun` appends `-r2`, `-r3`, and so on, for the determinism check.

### 4.2 Resume

- **Discovering the run.** If `bench/results/<run-id>/run.json` exists, the run is
  resumed. The existing `run.json` must hash to the same `cfg8`; anything else is
  refused. Changing a price, a model or the item list is a new run by construction.
- **Appending.** Each `Result` is marshalled to one line under `MaxLineBytes` (64 KB;
  answers are capped by `MaxTokens`) and written with a single `write(2)` on an
  `O_APPEND` file, then `f.Sync()`. One line per write is the guarantee `runlog` already
  relies on.
- **Torn tail.** On open, the reader scans to the last `\n`.
  - If any bytes follow it, they are a write the kill interrupted. The file is truncated
    back to that newline, and the truncation is reported (`resume: dropped a partial
    line of N bytes`).
  - Any line that fails to parse **before** the tail is corruption. It is refused, not
    skipped.
- **Done set.** `{(item_id, arm)}` is read from the file. The schedule (§4.3) is walked,
  and every pair already present is skipped.
  - Each pair is written at most once, so a resume never double-counts spend.
  - A pair present twice, possible only through a bug or a hand edit, is refused.
- **What gets retried.** A row with an error kind is a **result**: the arm failed that
  item, and the failure counts. Resume does not retry it.
  - The exception is `ErrKind ∈ {"budget", "contract"}`. Those rows are written only to
    record spend, and are marked `retryable: true`. Resume retries them, and the summary
    keeps only the last row per pair while adding up the cost of all of them.
- **Served arms on resume.** serve is restarted on the same home. The calibration
  template is not rebuilt if `calib.jsonl` is complete. A resumed served arm checks that
  its `homes/<arm>/calibration.jsonl` hash still equals the template's, since serve does
  not write it in A, and refuses on a mismatch.
- **Identical output.** `summary.json` is a pure function of (`run.json`, `calib.jsonl`,
  `items.jsonl`), with rows sorted by (item rank, arm) before any statistic. Its
  `timing` block (wall time, load average, start and end times) is the only part that
  differs between an interrupted run and an uninterrupted one. The test for this is in §7.

### 4.3 Deterministic order

- **Item selection.** The items are the first `n` test items by `Rank`, and the first
  `n_cal` calibration items by `Rank` round-robin over labels (§2.3). The seed does not
  choose them, so the sample is pre-registered.
- **Execution order.** The selected items are shuffled with Fisher–Yates on
  `rand.NewPCG(seed, 0xB5AD4ECEDA1CE2A9)`. The order is a pure function of the seed. The
  shuffle spreads provider drift evenly across label regions.
- **Arm interleaving.** For the *j*-th item in execution order, the arms run in
  `ArmOrder` rotated by `j mod len(ArmOrder)`: ABAB for two arms, ABC/BCA/CAB for three.
  Drift therefore hits every arm evenly (scope §7).
- **Concurrency.** It defaults to 1 and is required to be 1 on the latency subset.
  `--concurrency k` is allowed only off that subset. With k > 1, the order in which
  results are written is not deterministic, but the summary sorts by (rank, arm), so the
  summary is still deterministic.
- **Sleep gaps.** If consecutive calls are more than 60 s apart (the machine slept),
  the next row gets `latency_valid: false`. Latency statistics skip such rows, and their
  count is reported (scope §7).

### 4.4 `--max-usd`

- **Where the cap comes from.** It is `--max-usd` if given, otherwise the config's
  `max_usd`. A run with no cap is refused, since there is no "unlimited" spelling.
- **Before each call.** `Budget.Reserve(arm.WorstCaseUSD(req))`.
  - On `ErrBudget`, no call is made. The run stops with exit 2 and prints the spend so
    far and what the next call would have needed.
  - Rows already written stand. Resuming with a higher `--max-usd` continues from the
    same place. `run.json` records `max_usd`, and a changed cap does **not** change
    `cfg8`, so raising it resumes the same run. The cap history is appended to
    `run.json`'s `max_usd_history`.
- **Starting balance.** `NewBudget(max, spent)` is seeded with Σ `cost_usd` over
  `calib.jsonl` and `items.jsonl` on resume, so the cap covers the whole run.
- **Served arms.** Each request carries `max_cost` (E1's `MaxCostUSD` passthrough, which
  is not yet in serve, §9 Q2), set to what the budget has left, so serve can refuse an
  escalation the harness would refuse. Until that exists, the reservation uses the worst
  case for `1 + max_escalations` calls (§1.4).
- **Calibration** is reserved and settled through the same `Budget`.

### 4.5 Breadcrumb (`run.json` → `summary.json`)

```go
type Breadcrumb struct {
	HarnessVersion string            `json:"harness_version"` // build.Version
	GitSHA         string            `json:"git_sha"`         // build.Commit
	Dirty          bool              `json:"dirty"`           // from debug.ReadBuildInfo vcs.modified
	Config         string            `json:"config"`          // config.Breadcrumb() of each served home; "" for direct-only
	Suite          string            `json:"suite"`
	SuiteRevision  string            `json:"suite_revision"`
	DataSHA256     map[string]string `json:"data_sha256"`     // file → hash, re-hashed at load
	SplitSHA256    string            `json:"split_sha256"`
	Seed           uint64            `json:"seed"`
	Arms           map[string]ArmConfig `json:"arms"`
	Prices         PriceSnapshot     `json:"prices"`          // dated rows
	ProviderModels map[string][]string `json:"provider_models"` // model ids reported back, per arm
	Temperature    float64           `json:"temperature"`     // the value sent; served arms: X-Hydra-Temperature
	CalibSHA256    string            `json:"calib_sha256,omitempty"`
	Host           HostInfo          `json:"host"`            // internal/sysinfo + load average at start
	ModelCutoffs   map[string]string `json:"model_cutoffs,omitempty"` // stated training cutoffs (contamination)
}
```

A test fails if any field is empty for a run with a served arm (scope §6, E7.1-8). For a
direct-only run, only `Config` and `CalibSHA256` may be empty, and the test says so.

## 5. Output formats and verdicts

### 5.1 `items.jsonl`

There is one `Result` per line (§1.2). Field names are fixed by the json tags, and
`hyctl bench report` refuses an unknown `schema` value. The first line of every file is a
header:

```json
{"schema":"hydra.bench.items/1","run_id":"core-banking77-smoke-s1-3f9a2c1d"}
```

A typical row:

```json
{"seq":17,"item_id":"test/0412","split":"test","arm":"frontier","answer":"card_arrival",
 "calls":[{"head":"bench/bedrock/<opus-id>","model":"<reported id>","input_tokens":611,
 "output_tokens":4,"tokens_estimated":false,"cost_usd":0.003155,"price_ref":"anthropic/claude-opus-4.8"}],
 "decision":"accept","escalations":0,"latency_ns":812000000,"passed":true,"score":1,
 "cost_usd":0.003155,"started":"2026-11-06T10:02:11.511Z"}
```

`calib.jsonl` uses the same schema with `"split":"calibration"` and `"arm":"calib:<head>"`.

### 5.2 `summary.json`

```go
type Summary struct {
	Schema     string                 `json:"schema"`  // "hydra.bench.summary/1"
	RunID      string                 `json:"run_id"`
	Tier       string                 `json:"tier"`
	Breadcrumb Breadcrumb             `json:"breadcrumb"`
	Arms       map[string]ArmSummary  `json:"arms"`
	Paired     map[string]PairedStats `json:"paired"`   // key: "<arm>-vs-frontier"
	Verdicts   []Verdict              `json:"verdicts"` // empty unless Tier == "full" (§5.4)
	Refused    []MissingDependency    `json:"refused,omitempty"`
	Smoke      *SmokeMeasurements     `json:"smoke,omitempty"`  // §6
	Caveats    []string               `json:"caveats"`  // always includes the contamination note
	Timing     Timing                 `json:"timing"`   // excluded from the resume byte-identity test
}

type ArmSummary struct {
	N, Passed, Accepted, Errors, FormatFails int
	PassRate         float64            `json:"pass_rate"`
	PassWilson       [2]float64         `json:"pass_wilson95"`
	CostUSD          float64            `json:"cost_usd"`
	CostPerAccepted  *float64           `json:"cost_per_accepted"` // nil when Accepted == 0
	TokensIn, TokensOut struct{ Mean, P95 float64 }
	EscalationRate   *float64           `json:"escalation_rate,omitempty"`  // served
	MeanSources      *float64           `json:"mean_sources,omitempty"`     // served
	FirstSourceNotFrontier *float64     `json:"first_source_not_frontier,omitempty"` // served (non-vacuity)
	AcceptedError    *RateBound         `json:"accepted_error,omitempty"`   // served: k/n + CP upper 95
	Reliability      *reliability.Report `json:"reliability,omitempty"`     // served, ≥ 500 accepted, 10 bins
	LatencyMS        LatencyStats       `json:"latency_ms"`                 // sketch p50/p95, valid rows only
	HeaderCostMismatch int              `json:"header_cost_mismatch,omitempty"`
}

type PairedStats struct {
	Discordant     stats.Discordant `json:"discordant"`
	PDisc          float64          `json:"p_disc"`          // (B+C)/N, what §3.1 of the scope sizes n from
	DeltaPass      float64          `json:"delta_pass"`      // points
	BootLower95    float64          `json:"boot_lower95"`    // one-sided, 10k resamples, seed
	TangoZ, TangoP float64          // at the frozen margin
	TangoLower95   float64          `json:"tango_lower95"`
	McNemarP2      float64          `json:"mcnemar_p_two_sided"`
	CostPerAccDiff *Interval        `json:"cost_per_accepted_diff,omitempty"` // two-sided 95% bootstrap
	BootDropped    int              `json:"boot_dropped"`
}
```

- **Stats parameters.** The bootstrap uses 10,000 resamples and seed = `run.Seed`. Every
  number is a float64 written by `encoding/json`, with no rounding. Rounding happens only
  in `table.md`.
- **Reliability.** `reliability.Evaluate` is called only when Accepted ≥ 500 (E1 T3).
  Below that, `reliability` is null and the reason is a caveat. `MinObservations` (20) is
  too few for 10 bins.

### 5.3 `table.md` (`hyctl bench table`)

`table` reads one or more `summary.json` files and nothing else. It writes one row per
(suite, arm), in this format:

```markdown
| Suite | Arm | n | Pass % (95% CI) | Δ vs frontier, pts (one-sided 95% LB) | $ / accepted | Escalation % | Verdict |
|---|---|---|---|---|---|---|---|
| Banking77 | frontier | 1500 | 93.1 (91.7–94.3) | — | 0.00325 | — | — |
| Banking77 | hydra-quality | 1500 | 92.8 (91.4–94.0) | −0.3 (−1.4) | 0.00211 | 18.0 | H1 pass |
```

Footer lines name:
- the run ids;
- the git sha;
- the price snapshot date;
- the contamination caveat;
- "paired differences are the claim; absolute rates are context" (RFC §7).

**Formatting rules.**
- Percentages have one decimal place and dollars have five significant figures.
- An interval that crosses the margin is never rendered as a pass: the verdict column is
  copied verbatim from `summary.json`.
- An unverified licence, or a `smoke` tier, prints the row with the verdict "not a
  verdict (smoke)" or "unpublishable (licence)".
- `table` writes only to `--out`, by default `bench/results/<run-id>/table.md`. Putting
  it in the README is #1219, which is blocked on E1.R by design.

### 5.4 Hypothesis verdicts

```go
type Outcome string

const (
	Pass                Outcome = "pass"
	Fail                Outcome = "fail"
	Inconclusive        Outcome = "inconclusive"
	InconclusiveVacuous Outcome = "inconclusive-vacuous"
)

type Verdict struct {
	Hypothesis string  `json:"hypothesis"` // "H1"
	Threshold  string  `json:"threshold"`  // "E1 T2"
	Ticket     string  `json:"ticket"`     // "#1213"
	Suite, Arm string
	Gate       bool    `json:"gate"`       // true for T1/T2/T4 at A, false for report-only
	Outcome    Outcome `json:"outcome"`
	Expected   Outcome `json:"expected,omitempty"` // pre-registered "inconclusive" (Decision 3)
	Reason     string  `json:"reason"`     // always set, including on a pass
	Estimate, Lower, Upper float64
	N          int
}
```

Verdicts are computed only for `tier == "full"`. Smoke and fixture runs report the
estimates and leave `verdicts` empty (§6). Rules are evaluated in this order, and the
first match wins. δ = 2 pts.

| H (E1 threshold) | inconclusive-vacuous | pass | fail | inconclusive |
|---|---|---|---|---|
| **H1** (T2), `hydra-quality` vs `frontier` | `first_source_not_frontier < 0.10` (E1 T2 non-vacuity) | Tango rejects H0: Δ ≤ −δ at one-sided α = 0.05 **and** bootstrap LB > −δ **and** Δ̂ ≥ −1.0 pt | Δ̂ < −1.0 pt, **or** the one-sided 95% *upper* bound < −δ | otherwise. This includes Tango and the bootstrap disagreeing, and the reason names which said what. |
| **H3** (T4), `hydra-cost` vs `frontier` | — | the cost-per-accepted difference has a two-sided 95% CI entirely < 0 **and** Δpass bootstrap LB > −3.0 pts | the CI is entirely ≥ 0, **or** the Δpass upper bound < −3.0 pts | otherwise, or either arm accepted nothing |
| **H2** (T3, report only) | — | n_accepted ≥ 500, CP upper(error) ≤ 0.07 and ECE ≤ 0.05 | the error point estimate > 0.07, or ECE − its bootstrap half-width > 0.05 | n_accepted < 500, or neither of the other cases |
| **T1** (provider-neutral) | — | the run has no Anthropic API head and no Claude CLI head in `heads`, and there were 0 HTTP 5xx | any 5xx, or such a head is present | — |

- **Reporting A.** CLINC150 is reported against Banking77 (E1 §4). If Banking77 passes
  and CLINC150 fails, the A verdict is `inconclusive`. That is computed by `report` over
  both suites' summaries, as `checkpoint.json` (the same `Verdict` type, with Suite
  `"*"`).
- **No re-testing.** Verdicts are computed once per (H, suite). Rerunning `report` on the
  same files gives the same verdict. The thresholds live in `internal/bench/verdict.go`
  as constants that cite the frozen ticket. Changing them is a reviewed diff, made before
  results exist (E1 §4 last line).

## 6. Smoke tier

The smoke tier is not a verdict: 100 items give about ±6 pts. It has three jobs:
- prove the pipeline end to end, with real billing;
- **measure** p_disc, tokens per item, format-fail rates and escalation rates, so the full
  run can be re-sized before money is spent on it;
- run on **Bedrock alone**, with OpenRouter optional.

### 6.1 Items

| Suite | Test items | Calibration items (served stage only) |
|---|---|---|
| Banking77 | the first **100** test-split ids by `Rank` | the first **40** calibration ids by `Rank`, round-robin over labels (40 of the 77 labels, one item each) |
| CLINC150 | the first **100** test-split ids by `Rank` (`oos` included, at its natural ~4% share) | the first **40** calibration ids, as above |

The ids are committed as part of `bench/splits/*.tsv`, and are the first rows of the full
tier's sample (§2.3). The PR that adds the splits also lists the 200 smoke ids in
`bench/configs/smoke.ids` so a reviewer can read them.

### 6.2 Arms and stages

The smoke tier runs in two stages, because its served arms depend on E1.

| Stage | When | Arms | Router heads (`heads`) |
|---|---|---|---|
| **S1, direct** | as soon as PR 3 merges (§8) | `frontier`, `cheapest`, `mid`, plus `oracle` (computed) | none |
| **S2, served** | after E1-D1…D6 land | `hydra-quality`, `hydra-cost` (+ the S1 arms, resumed, not re-run) | Bedrock alone: `env/bedrock` (model = frontier) and `ollama/qwen2.5-coder:7b`. With OpenRouter: also `openrouter/anthropic/claude-haiku-4.5` and `openrouter/openai/gpt-5-nano` |

`bench/configs/smoke.toml`:

```toml
tier = "smoke"
track = "core"
suites = ["banking77", "clinc150"]
n_test = 100
n_cal = 40
seed = 1
max_usd = 5.0
max_escalations = 2

[models]                         # filled in once Q4 is answered; every id is pinned and dated
frontier = "bedrock:<strongest Opus id enabled in the account's region>"
cheapest = "bedrock:amazon.nova-micro-v1:0"
mid      = "bedrock:<Claude Haiku 4.5 id>"

[price_refs]                     # pricing.DB keys; Bedrock ids are not in it (K4)
"<opus id>"                 = "anthropic/claude-opus-4.8"
"amazon.nova-micro-v1:0"    = "amazon/nova-micro-v1"
"<haiku id>"                = "anthropic/claude-haiku-4.5"
# [prices."<id>"] in_per_mtok/out_per_mtok/source/date overrides a ref when Bedrock list ≠ OpenRouter (Q4)

[served]
heads = ["env/bedrock", "ollama/qwen2.5-coder:7b"]
bedrock_model = "<opus id>"      # exported to serve as BEDROCK_MODEL_ID
frontier_head = "env/bedrock"    # used for non-vacuity
target = 0.95
```

- **Bedrock alone.** Every S1 arm is a direct Bedrock call, and no OpenRouter key is
  read. S2 on Bedrock alone gives the router only two heads, because discovery emits one
  Bedrock head (K3).
  - `hydra-quality` will then almost always choose `env/bedrock`, which is the frontier
    model. That is the vacuous case E1 T2 anticipates.
  - In smoke this costs nothing to discover. In full it would make H1
    `inconclusive-vacuous` by construction, so the full tier needs OpenRouter, or a
    Bedrock model allowlist (§9 Q5).
- **Local Qwen** is $0 and is allowed: T1 forbids only Anthropic API and Claude CLI
  heads. Its latency on a loaded laptop is noise, which is why latency comes only from
  the ABAB subset.

### 6.3 Expected cost

Token counts were measured from the real rubrics (§3.1 prompt):
- The Banking77 rubric is 1,793 characters for 77 labels, and the CLINC150 rubric is
  2,066 characters for 151 labels.
- That gives ≈ 560–650 rubric tokens. Add the utterance (39–54 chars) and the fence, for
  **≈ 650 input tokens on Banking77 and ≈ 700 on CLINC150**.
- Output is ≤ 10 tokens; the longest label is 48 characters.

Prices are the scope's 2026-11-05 snapshot. Bedrock is assumed equal to OpenRouter list
prices (Q4).

| Line | Calls | $/call (B77 / CLINC) | Cost |
|---|---|---|---|
| S1 `frontier` (Opus, $5/$25) | 100 + 100 | 0.00350 / 0.00375 | **$0.73** |
| S1 `mid` (Haiku 4.5, $1/$5) | 200 | 0.00070 / 0.00075 | **$0.15** |
| S1 `cheapest` (Nova Micro, $0.035/$0.14) | 200 | 0.000024 / 0.000026 | **$0.01** |
| S2 calibration, Bedrock alone (Opus on 40 + 40 items; Qwen $0) | 80 | 0.0036 | **$0.29** |
| S2 `hydra-quality`, worst case (every item takes the frontier, plus 20% escalations) | ≤ 240 | 0.0036 | **≤ $0.87** |
| S2 `hydra-cost`, worst case (every item escalates to the frontier) | ≤ 200 | 0.0036 | **≤ $0.73** |
| S2 extra calibration with OpenRouter (Haiku + nano, 80 each) | 160 | 0.0007 / 0.00004 | **$0.06** |
| **Total, worst case** | | | **≈ $2.84** |
| **With a +25% token-estimate error** | | | **≈ $3.55** |

- `--max-usd 5` is enforced, so the worst case cannot exceed $5 whatever the token counts
  turn out to be.
- S1 alone is ≈ $0.89 (≈ $1.11 with the margin), so it can run long before E1 lands.

### 6.4 What smoke writes

`summary.json` gets a `smoke` block:

```go
type SmokeMeasurements struct {
	PDisc          map[string]float64 // "<arm>-vs-frontier" → (B+C)/N
	TokensPerItem  map[string]struct{ InMean, InP95, OutMean, OutP95 float64 }
	FormatFailRate map[string]float64
	EscalationRate map[string]float64 // served arms
	RecommendedN   map[string]int     // per suite: max(1000, ⌈6.18·p_disc/0.02²⌉), capped at the test split size
	FullTierUSD    float64            // the §3.2 table of the scope recomputed from measured tokens
}
```

- **S1's p_disc is a proxy.** S1 measures it between the frontier and `mid` and
  `cheapest`, the likely first sources. S2 measures it against the served arms directly.
- **The full tier is sized from S2 when S2 exists, and from S1 otherwise.** The
  recommendation is a number for a person to accept. Nothing re-sizes `full.toml`
  automatically.

## 7. Test plan

**Rules for every test below:**
- No network, model or wall-clock deadline in `go test ./...` (#1176).
- `go test -race ./...` must be green.
- Every test that touches `HYDRA_HOME` uses `testutil.NewSandbox`, which also points
  `BEDROCK_MODEL_ID`, the OpenRouter variables and the local-server hosts at dead
  addresses.
- Time comes from an injected `clock func() time.Time`, never `time.Now` in assertions.

### 7.1 Statistics golden tests (`internal/stats`)

- **Where the numbers come from.** There is no scipy on this machine. The values below
  were computed in exact rational arithmetic (`fractions`, `math.comb`) or by closed form.
  The generating script is committed as `internal/stats/testdata/gold.py` and is run
  **by hand**, never in CI.
- **scipy cross-check.** PR 1 re-confirms each value with `scipy.stats.binomtest`,
  `scipy.stats.beta.ppf` and `statsmodels`' `mcnemar(exact=True)` in a throwaway venv, and
  pastes the output into the PR body.
- **Tolerance.** 1e-12 for exact values, 1e-9 for iterative ones.

| Function | Case | Expected | Source |
|---|---|---|---|
| `McNemarExact` | B = 10, C = 2 | one-sided 79/4096 = **0.019287109375**; two-sided **0.03857421875** | exact binomial; = `binomtest(2, 12, 0.5)` |
| `McNemarExact` | B = 6, C = 8 | two-sided **0.79052734375** | exact |
| `McNemarExact` | B = C = 0 | 1, 1 | definition |
| `TangoNI` | B = 6, C = 8, N = 200, δ = 0.02 | z = **0.5264247260767538**, p = **0.29929656985372144** | closed form (§1.4) |
| `TangoNI` | B = 40, C = 45, N = 1500, δ = 0.02 | z = **2.6057938891628436**, p = **0.004583080034926135** | closed form |
| `TangoNI` | B = 10, C = 2, N = 100, δ = 0.02 | z = **2.7454601989185186**, p = **0.0030213048301711565** | closed form |
| `TangoNI` | B = 10, C = 2, N = 100, δ = 0 | z = (B−C)/√(B+C) = **2.3094010767585034** | identity with McNemar's z |
| `TangoNI` | Tango (1998), *Stat Med* 17:891, worked example | the paper's printed z and CI | **to transcribe in PR 1** (the paper was not reachable from here); if it cannot be obtained, the test cites Liu et al. (2002) instead |
| `TangoLowerBound` | each case above | `TangoNI(d, −LB)` has p = α to 1e-9, and the bound is monotone in α | inversion property |
| `ClopperPearsonUpper` | k = 0, n = 100, α = .05 | **0.029513049607039932** = 1 − 0.05^{1/100} | closed form |
| `ClopperPearsonUpper` | k = 3, n = 50 | **0.1478371763641812** | bisection on the exact CDF |
| `ClopperPearsonUpper` | k = 35, n = 500 | **0.09174006190473744** | same |
| `ClopperPearsonUpper` | k = 64, n = 910 (H2's sized n) | **0.08588608387949787** | same |
| `ClopperPearsonUpper` | k = n | 1 | definition |
| `Wilson` | k = 81, n = 263, z = 1.959964 | (**0.25528851948901576**, **0.3662095774580194**) | closed form; the entity tests keep passing after the lift |
| `PassHatK` | n = 4, c = {4, 3, 2, 0}, k = 2 | **5/12** | τ-bench definition |
| `PassHatK` | same, k = 1 | **9/16** (= pass@1) | |
| `PassHatK` | N < k | error | |
| `Resample` + `PairedMeanDiff` | all d_i = 0.3 | every quantile = 0.3 exactly | degenerate |
| `Resample` + `PairedMeanDiff` | 2,000 d_i drawn with a known p_disc, Δ | LB within 0.002 of Δ̂ − 1.645·σ̂/√n | normal approximation, a property test |
| `Resample` | same seed twice / a different seed | identical / different | determinism |
| `Resample` | regression fixture: 50 hand-written d_i, seed 7, 10,000 resamples | Quantile(0.05) pinned to 1e-12 | recorded from the first implementation (scope §6, E7.1-4: "bootstrap bound under a fixed seed") |
| `ope.Evaluate` | its existing tests | unchanged | proves the lift changed nothing |

### 7.2 Suites and splits

- **Loading.** Small fixture files sit under `internal/bench/suites/testdata/`, served by
  `httptest.Server` for `fetch`.
  - The Banking77 fixture includes a quoted field with an embedded newline.
  - The CLINC fixture includes a conflicting duplicate and an agreeing one.
- **Fetch.** A hash mismatch deletes the `.part` file and errors. A 404 is not retried. A
  503 is retried three times on a fake backoff. A file already present and verified is
  not downloaded again (a request counter).
- **Splits:**
  - No id and no canonical text appears in both calibration and test, checked on the
    fixture and, in PR 2, on the committed TSVs.
  - The 20 golden ids per suite keep their (split, rank).
  - Conflicting duplicates come out `excluded`, and agreeing ones share a split.
- **Grader:**
  - An empty answer fails.
  - A label outside the set is `format`.
  - `" Card_Arrival "` passes for `card_arrival`, but `card arrival` and
    `card_arrival because…` fail.
  - A test asserts that `internal/bench` contains no canonicaliser of its own (scope §6,
    E7.2-4). It does this with a `go/parser` scan: no `internal/bench/...` non-test file
    imports `golang.org/x/text/unicode/norm`. The split key calls the oracle's
    `CanonicalLabel` function and does not reimplement it.

### 7.3 Arms with fake executors

```go
type fakeExec struct { // scripted per (head, item text): answer, tokens, error, delay on a fake clock
	script map[string]fakeReply
	calls  atomic.Int64
}
```

- **Direct arms:**
  - `static-fallback` moves on a 503 and a 429 and does not move on a 400 or a wrong
    answer.
  - An injected fault makes no executor call (counter = 0).
  - `TokensEstimated`, `OutputTokens > MaxTokens` and a model-id change each abort with
    exit 4.
  - The oracle arm is ≥ every arm on every fixture (scope §6, E7.1-3).
- **Served arms.** A fake `hyctl serve` is an `httptest.Server` implementing the
  chat-completions contract. It is reached through a `Launch func(home string, env []string)
  (baseURL string, stop func(), err error)` seam, so no subprocess is needed. Its cases:
  - with each of the E1-D1…D6 behaviours removed, the arm is refused with that ticket,
    zero chat calls are made, and the exit is 3;
  - a missing header on item 3, or a source outside `heads`, aborts with exit 4 and writes
    an `ErrKind: "contract"` row;
  - cost rows are joined on the request id from a fake `cost.jsonl` in the arm's home, and
    a missing row reads `unpriced`, reserved at the worst case.
- **Calibration fill:**
  - A test-split item is refused with `ErrTestItemInCalibration`.
  - After a fill, the template's `calibration.jsonl` gives `CommitmentsIn(head, class)`
    equal to the graded counts.
  - The filler and `hyctl trust record` produce byte-identical files for the same inputs,
    because they share one function (§3.3).
- **Isolation.** After a served run on a sandbox, the operator's
  `$HYDRA_HOME/calibration.jsonl`, `evalset/` and `logs/` are unchanged (hashed before and
  after).

### 7.4 Resumability after a kill

- **In-process kill.** A runner on a fake executor runs 60 items × 3 arms. A
  `context.CancelFunc` fires inside `Call` at a scripted (item, arm), and a fault-injected
  writer truncates the last line mid-way. Then:
  - Resume reports the dropped partial line and finishes.
  - The final `items.jsonl` has exactly one row per pair.
  - `summary.json` minus `timing` is **byte-identical** to an uninterrupted run with the
    same seed (scope §6, E7.1-6).
  - Σ `cost_usd` equals the uninterrupted run's, so nothing was billed twice.
- **Real process kill.** The test binary re-execs itself as the harness (the existing
  `rlimit_exec_e2e_test.go` pattern), sends SIGKILL after the *k*-th fsync, which it learns
  from a pipe and not a timer, and resumes. Unix only, since it skips on Windows.
- **Corruption.** A malformed line in the middle is refused. A duplicate pair is refused.
  A `run.json` whose `cfg8` differs is refused.
- **Budget:**
  - A fake priced executor at a known cost per call is given `--max-usd` just under N
    calls. The run stops before call N+1 with exit 2, and the executor's counter equals
    N.
  - Resuming with a higher cap completes, and the total equals the uninterrupted run's
    (scope §6, E7.1-7).
  - The starting balance on resume includes `calib.jsonl`.
- **Order.** The same seed gives the same `seq` → (item, arm) schedule. A different seed
  gives a different order over the same item set.

### 7.5 Report, table and verdicts

- Constructed `summary` fixtures reach each verdict branch of §5.4: vacuous, pass, fail
  by the point estimate, fail by the upper bound, inconclusive because Tango and the
  bootstrap disagree, and inconclusive because n is under 500 for H2.
- `report` twice gives byte-identical `summary.json`.
- `table` matches a golden markdown file, and it refuses a smoke or unverified-licence
  suite as described in §5.3.
- The breadcrumb test fails on any empty field (§4.5).

## 8. PR plan

**Conventions.** Every PR targets `develop`, and every title is a conventional commit.

**Which PR carries `Closes`.** `close-shipped-issues.yml` closes every `Closes #n` it
finds in a release. If an early PR of a multi-PR ticket said `Closes`, the ticket would
close at the next release with its work unfinished. So **only the last PR of each ticket
carries `Closes #n`**, and the earlier ones say `Part of #n`. If you would rather have
sub-issues so that every PR can close one, see §9 Q7.

**Files every new subcommand must update.** CLAUDE.md's docs-site rule says a new
`hyctl` subcommand updates `docs/index.html` (CLI tab) and `docs/llms.txt`. That
happens in PR 3, the first PR that ships `hyctl bench run`.

| # | Branch | Title | Body | Depends on | Spend |
|---|---|---|---|---|---|
| 0 | `feature/1209-match-oracle` | `feat(oracle): exact label-match verifier with one canonicaliser` | Part of #1209. Adds `internal/oracle/match.go` (`CanonicalLabel`, `Match{Labels}`) and tests. Wires nothing into dispatch. Promotes `golang.org/x/text` to a direct dependency and bumps it from v0.3.8 (§9 R6). | none | $0 |
| 1 | `feature/1198-stats` | `feat(stats): paired bootstrap, Tango, McNemar, Clopper–Pearson and pass^k` | Part of #1198. New `internal/stats`. `ope.bootstrapInterval` and `entity.wilson` are rewritten onto it, with no behaviour change: their tests stay byte-identical. Adds a coverage floor. | none | $0 |
| 2 | `feature/1199-bench-suites` | `feat(bench): pinned dataset fetch, Banking77 and CLINC150 loaders, committed splits` | Closes #1199 (once it is narrowed, Q6). Adds `internal/bench` (Item, Suite, the lockfile, fetch, splits), `internal/bench/suites`, `bench/datasets.lock`, `DATASETS.md`, `splits/*.tsv` and `configs/smoke.ids`, plus `hyctl bench fetch`. | 0 | $0 |
| 3 | `feature/1198-bench-runner` | `feat(bench): resumable runner with budget cap and direct arms` | Part of #1198. Adds Arm, Attempt, Result, Run, the schedule, resume, Budget, PriceSnapshot, the breadcrumb, the direct arms (frontier, cheapest, mid, static-fallback with fault injection) and `hyctl bench run`, plus the docs-site CLI entries. | 2 | $0 |
| 4 | `feature/1198-bench-report` | `feat(bench): summary, hypothesis verdicts, report and table` | Part of #1198, Part of #1219. Adds `summary.json`, `checkpoint.json`, the verdict rules in §5.4, and `hyctl bench report` and `table`, which write files only and never the README. | 1, 3 | $0 |
| 5 | `chore/1198-smoke-direct` | `chore(bench): smoke tier S1 results, direct arms on Bedrock` | Part of #1198. Commits `bench/results/core-*-smoke-s1-*/`. The PR body states p_disc and the recommended n. | 4; working Bedrock credentials (Q4) | ≈ $0.9 |
| 6 | `feature/1198-bench-served-arms` | `feat(bench): Hydra arms through hyctl serve with an isolated HYDRA_HOME` | Part of #1198. Adds the Served arm, the Launch seam, the preflight probes and `MissingDependency`, the contract checks, the cost join, and the extraction of `trust.RecordOutcome` together with the calibration filler. **Mergeable before E1**: until E1 lands, it refuses with exit 3 and names the tickets. | 3 | $0 |
| 7 | `feature/1201-bench-calibration-curves` | `feat(bench): calibration-split fill, learning curves and seed matrices` | Closes #1201. Adds learning curves at n = 0/20/50/100/200, `bench/seeds/<pack>/<class>.json`, and loading a seed matrix as a `catalogue_prior` prior. | 6, plus E1 #1206, #1208, #1210 | in the full run |
| 8 | `chore/1198-smoke-served` | `chore(bench): smoke tier S2 with Hydra arms and the determinism check` | **Closes #1198.** S2 smoke, plus the 200-item Banking77 determinism rerun (`--rerun`), frontier and hydra-quality, gated on ≤ 1 discordant item. | 6; E1-D1…D6 merged | ≈ $2 + ≈ $1.5 |
| — | (under #1213, not E7) | the full checkpoint A run | owned by E1.E. It uses `bench/configs/full.toml`, sized from PR 5 or PR 8. | 8 | ≤ $150 total |

**What can merge first.** PRs 0, 1 and 2 depend on no E1 work, and PRs 3, 4, 5 and 6 depend
only on earlier E7 PRs:
- 0 and 1 can merge in parallel, then 2, then 3.
- 4 and 6 can go in parallel after 3.
- 5 follows 4.

PRs 7 and 8 are the only ones that wait for E1.

**How this maps to E1's order (E1 §6).** PR 8 needs E1 steps 2 (#1208), 3 (#1206),
4 (#1209) and 5 (#1210), plus E1-D4 (temperature) and E1-D5 (head allowlist), which are not yet
in any E1 ticket (Q2, Q3). Step 1 (#1207 Quota) is **not** a dependency: the fresh home
keeps `claude_pct` at 0. PR 0 overlaps E1 step 4's `match.go`, which is why Q1 asks for
that ownership to be agreed first.

**Commit size.** None of these PRs touches `internal/dispatch` or `internal/serve`. The
harness reaches serve only over HTTP and the `os.Executable()` probe, so E7 cannot
conflict with E1's hotspot files (E1 §6 conflicts 3–4). The only shared edits are
`cmd/hydra/main.go` (one `AddCommand` line, and the `trust.RecordOutcome` call in
`cmdTrustRecord`) and `ci/coverage-floors.txt`.

## 9. Risks and blocking questions

### 9.1 Risks

| # | Risk | Evidence | Mitigation in this design |
|---|---|---|---|
| R1 | **On Bedrock alone, H1 is vacuous by construction.** | Discovery emits one Bedrock head (K3). With `heads = {env/bedrock = Opus, local Qwen}`, `quality` picks Opus almost every time. | Smoke tolerates it, and the full tier must not. The full tier needs OpenRouter heads, or a Bedrock allowlist (Q6). The non-vacuity check (§5.4) makes this outcome impossible to misread as a pass. |
| R2 | **The Bedrock credentials on this machine do not authenticate right now.** | Read-only `aws bedrock list-foundation-models` on 2026-11-05 returned `UnrecognizedClientException: The security token included in the request is invalid`, three times, with region `ap-south-1`. No secret was printed. | S1 cannot run until this is fixed (Q5). `Direct.Preflight` makes one ≈ $0.00003 Nova Micro call before the schedule, so a bad credential costs one call, not a run. Opus in `ap-south-1` is probably reachable only through a cross-region inference profile id, which has to be pinned. |
| R3 | **H3's cost may be under-counted for served arms.** | The harness joins `cost.jsonl` on `run_id`. It is not established that dispatch writes a cost row for a **failed** or **escalated** attempt that the provider still billed. | A served item whose header `X-Hydra-Cost-USD` exceeds the joined total is flagged (`header_cost_mismatch`). Any flag makes H3 `inconclusive` with that reason. E1 #1210 should guarantee one cost row per billed call, and this should be added to its acceptance. |
| R4 | **Hydra may route `cost` on wrong prices for Bedrock.** | `pricing.DB` misses Bedrock ids and falls back to tier pricing (K4). `constrain` and `rank.Cheapest` read `EstimateCost`. | Out of E7's scope, but it decides H3. The harness prices from its own snapshot, so the *measurement* is right even when the routing is not. Reported to E1 as a finding: a Bedrock price map, or `[prices]` in the config. |
| R5 | **API nondeterminism and silent model updates.** | Scope §7. | Pinned ids. A reported model id change aborts the run. ABAB rotation. The ≤ 1-discordant determinism gate runs in PR 8 and again before the full run. |
| R6 | **NFC needs `golang.org/x/text`, today an indirect v0.3.8 (2022).** | `go.mod`. | In A every label is ASCII snake_case, so NFC changes only model answers that contain non-ASCII text. PR 0 promotes x/text to a direct dependency at a current version. If a new direct dependency is unwelcome, `CanonicalLabel` can refuse non-ASCII instead and the answer is graded `format`. That is a decision for E1's #1205, flagged here. |
| R7 | **The threshold arm (T8) is not built.** | E1 freezes T8 as "not run" at A. | Deferred to B. Design note: log-probs are unavailable on Bedrock, so it uses self-report, with τ tuned on calibration-split folds seeded from `run.Seed`. |
| R8 | **Pricing snapshot timestamp.** | `pricing.DB` does not expose `fetched_at`, which lives in `pricing_cache.json`, and `Load()` may refresh in the background. | PR 3 adds `func (db *DB) FetchedAt() time.Time`, read once. The snapshot is frozen into `run.json`, and a background refresh cannot change a running run. |
| R9 | **Under-counted billing for the hydra arms before #1210.** | No evidence headers exist yet. | E1-D3 is a hard preflight requirement, so served arms do not run without evidence. |
| R10 | **Committed answers versus provider output terms.** | Decision 3 accepted this. | Only public items are committed, at about 20 bytes per answer. |
| R11 | **The calibration sample is thin in smoke.** | 40 items over 77 or 151 labels. | Smoke only proves the plumbing, and full uses 500. Learning curves (#1201) measure what n buys. |
| R12 | **Contamination.** | Both suites have been public since 2019–20. | A caveat in every summary. Model cutoffs go in the breadcrumb, and only paired Δ is headlined. |

### 9.2 Blocking questions

Each question names the PR it blocks.

1. **Q1, `internal/oracle/match.go` ownership (blocks PR 0, 2).**
   - E1 step 4 (#1209) lists this file, and E7.2 needs it first.
   - Recommendation: E7 lands the pure canonicaliser and matcher as PR 0 under #1209's
     number. E1 step 4 then only wires it into dispatch and route config.
   - Alternatively, E7 waits for #1209, which delays PR 2 onwards.
2. **Q2, `temperature` (and `max_cost`) passthrough in serve (blocks PR 8).**
   - E7 scope §1.4 asks for temperature passthrough, but E1 §1 does not list it under any
     ticket.
   - Recommendation: add both to #1210 (E1.5), echoed as `X-Hydra-Temperature`. Without
     temperature 0, the determinism gate measures sampling, not Hydra.
3. **Q3, a head allowlist for `hyctl serve` (E1-D5; blocks PR 8).**
   - CLI-agent heads (Claude Code, Codex, Antigravity) must be excluded (scope §5.2), and
     stripping `PATH` cannot do it (K5).
   - Recommendation: a new E1 ticket for `hyctl serve --heads a,b` / `[serve] heads`. The
     router may choose and fall back only within the list. The same feature makes a
     deployment's head set explicit, which is useful beyond the bench.
4. **Q4, Bedrock access (blocks PR 5).**
   - Fix the credentials (R2).
   - Confirm the pinned Bedrock ids, inference profiles included, for the strongest Opus,
     Claude Haiku 4.5 and Nova Micro in the account's region, and that model access is
     enabled for each.
   - Confirm whether Bedrock prices equal the OpenRouter snapshot. If not, they go in
     `[prices]` with a source URL and date.
5. **Q5, the router's model set on Bedrock (blocks the full tier, not smoke).**
   - Either wait for the OpenRouter key, or add a `[bedrock] models` allowlist that
     mirrors `[openrouter] models` (a small `provider/env` change, needing an E1 or new
     ticket).
   - Without one of these, H1 is `inconclusive-vacuous` (R1).
6. **Q6, narrowing #1199 (blocks closing it in PR 2).**
   - #1199 still lists CORD and SROIE. Move CORD to a checkpoint-B ticket and record
     SROIE as dropped (Decision 3), so PR 2 can close #1199.
7. **Q7, the `Closes` policy (affects every PR body).**
   - Recommendation: keep `Part of #n` on all but the last PR per ticket (§8).
   - Alternatively, file a sub-issue per PR, so each one can carry its own `Closes`.
8. **Q8, the cost definition (affects H3).**
   - E1 §4 defines cost as Σ `X-Hydra-Cost-USD`. This design prices every arm from the
     provider's token counts and one dated snapshot, and uses the header only as a
     cross-check (R3, R4).
   - Recommendation: adopt the harness definition in #1213 before results exist. Pricing
     the frontier arm and Hydra's arms through two different code paths would make H3
     measure a pricing difference.
