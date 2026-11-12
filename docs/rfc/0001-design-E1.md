# RFC 0001 — E1 Design (Checkpoint A)

Ticket: #1205 (E1.D) · Stage: design · Contract: `docs/rfc/0001-scope-E1.md`, including its Decisions.
Verified against `docs/rfc-0001-verified-cascade-gateway` at `6aea7ac`. Its code is identical to `develop` `b76d105`, because only `docs/` differs.
Line numbers below are at that commit.

**Scope of this design.** Checkpoint A covers classification only (Banking77 gates, CLINC150 is reported). `model:"hydra"` routes by objective, with `quality` as the default. `hydra/standard` keeps the old pinned behaviour. Everything below is the smallest change that lets E7 measure T1, T2 and T4 through `hyctl serve`.

**Three design choices shape every ticket:**

1. **No `Options.TaskClass`.** `dispatch.Options.Domain` (`dispatch.go:89-93`) already is "the calibration domain this dispatch routes for, the same key internal/trust records outcomes under", and `routingDomain` (`:1585`) already reads it first. A second field would be the same key under two names. The serve path sets `Domain`; the CLI path is untouched.
2. **One cascade, in `internal/decide`.** The chat cascade is a new type, `decide.Cascade`, beside `decide.Router`. It shares three things with `Router`:
   - the `trust.Target` it stops at;
   - the stop rule "nothing calibrated can move the posterior, so stop asking" (`decide.go:87-90`);
   - a single `decide.Decision` vocabulary that both `/v1/chat/completions` and `/v1/decisions` emit.

   `internal/dispatch` gains only a hook that says "try the next candidate". It does not learn what a verifier or a posterior is.
3. **One header vocabulary.** The new `X-Hydra-*` names are the ones both endpoints use. The old `X-Hydra-Decision-Sources` and `X-Hydra-Decision-Confidence` stay for one release as aliases (§4). Without this, `X-Hydra-Decision` (the verdict) would sit beside `X-Hydra-Decision-*` (the endpoint) and mean something else.

**Deviations from the scope table, each the smaller change:**

| Scope says | Design does | Why |
|---|---|---|
| #1208: add `Options.TaskClass` | reuse `Options.Domain` | It is the same key (choice 1). |
| #1208: the resolver includes `DomainForFile` when a file is named | the serve resolver is header → route default → `default`. The file step stays where it already is, `routingDomain`'s `Resource` branch (`:1589-1591`). | serve never has a file, so a file branch in the serve resolver would be dead code. The CLI path keeps resolving exactly as today, which is the regression the scope asks for. |
| #1209: `Verdict.Score` in A | deferred to #1209-B | No A consumer exists. `match` is pass/fail, and the field-wise graded score belongs to `jsonschema`/CORD. An exported field nobody reads breaks CLAUDE.md's "exported symbols must be used". |
| #1209: `match` has exact or normalised modes | one mode: the label set is compared after `NormaliseLabel`, or a regex is applied | Normalisation is idempotent on already-normal labels such as `card_arrival`, so no `exact` switch is needed for either suite. |

## 1. Per-ticket design

### 1.1 #1207-A Quota (provider-neutral governor)

*Updated to the code shipped in #1330 (checkpoint A only, "Part of #1207"); the follow-ups are #1326. See "As shipped" at the end for what changed from the first draft.*

**New file `internal/budget/quota.go`:**

```go
// SessionClaudeCode is the scope the orchestrator protocol's legacy key feeds.
const SessionClaudeCode = "session:claude-code"

// Quota is one scope's pressure. Session scopes only for now; limits in USD or
// tokens arrive with the cost ledger that would enforce them. No JSON tags: nothing
// reads or writes a Quota as JSON at checkpoint A.
type Quota struct {
	Scope       string
	ContextFrac float64 // 0..1
	History     []int   // percent points, newest last, ≤ MaxPctHistory
}

func (q Quota) Pct() int                      // int(math.Round(ContextFrac*100)); the clamp is on the read, below
func (q Quota) Mode() Mode                    // EffectiveMode(q.Pct(), risk of History)
func (q Quota) Burn() (ratePct, risk float64) // RiskFromHistory(q.History), for `hyctl status`

// State is state.json decoded once, for the governor and `hyctl status` alike.
type State struct {
	Quotas []Quota // only the claude_pct alias at checkpoint A
	budget json.RawMessage
}

// Budget is the per-model snapshot table, decoded on demand because dispatch never reads it.
// A null entry stays an empty row, as it always rendered; a non-object entry is skipped.
func (s State) Budget() map[string]map[string]any

// ParseState reads a state.json body. A field of the wrong type reads as absent rather
// than failing the file; malformed JSON yields the zero State. The `quotas` key is ignored.
func ParseState(raw []byte) State

// Governing is the most urgent quota (highest Mode, ties by Scope) and its mode,
// ok=false when there are none.
func Governing(qs []Quota) (q Quota, mode Mode, ok bool)

// MirrorLegacyHistory appends the orchestrator's level to its bounded history,
// so the rate-aware governor and the frozen legacy readers see the trajectory.
func MirrorLegacyHistory(state map[string]any)
```

The alias is unexported: `legacySessionQuota(pct, history any)` derives `SessionClaudeCode` on every read, `parseHistory` reads the series and `legacyLevel` reads one percentage. The legacy key names are unexported constants in this file, so `quota.go` is the only non-test Go outside the frozen readers that names them.

`MaxPctHistory`, `AppendPctHistory`, `RiskFromHistory` and `EffectiveMode` in `risk.go` are reused unchanged. #1330 does not edit `risk.go` (#1327 changes only its `normCDF`, to `stats.NormalCDF`). `Window`, `LimitUSD` and `LimitTokens` from RFC §5.1 are **not** added. Nothing in A would read them, and E2 adds them along with their enforcement.

**Changed: `internal/dispatch/dispatch.go`:**

- **`:535-547`.** `d.claudeMode(hint)` becomes `tier, q, mode := d.quotaMode(hint)`. The switch is on `budget.Mode` constants rather than strings. The downgrade logic is identical:
  - emergency: tier 10 and `LocalOnly`;
  - critical: +2;
  - warning: +1.

  The log lines move into `governorLog(q, mode, fromTier, toTier)` and keep develop's wording verbatim (`🚨 EMERGENCY: Claude at %d%%, routing to local tier only. Start a new session.` and so on), because `session:claude-code` is the only scope at checkpoint A. **One deliberate difference:** critical and warning claim "downgrading tier by N" only when the tier actually moved; otherwise they print `🔴 CRITICAL: Claude at N%. Run /compact NOW.` (or the warning equivalent).
- **`:984-1047`.** `claudeMode`, `readClaudePct` and `readClaudePctHistory` are deleted. `quotaMode(tierHint string) (string, budget.Quota, budget.Mode)` reads `state.json` once (develop read it twice), then calls `budget.ParseState` and `budget.Governing`. No quota at all is `ModeNormal`. The `Atoi`/clamp body is kept as it is.
- **`:1627-1634`.** This becomes `budget.MirrorLegacyHistory(existing)`. The comment loses the legacy key name.
- **`asInt`/`asIntSlice` (`:1050-1082`).** They are deleted, not moved. `budget`'s `legacyLevel` and `parseHistory` replace them, and read stricter: a non-integer level is unset, and one non-integer history point makes the whole history unknown, where `asInt` truncated a fraction and read a `null` point as 0.

**Changed: `cmd/hydra/main.go`.** In `printBudgetStatus` (`:701-732`), the `ClaudePct*` fields go and `budget.ParseState(raw)` is called once. The function prints one line per quota with `Pct() > 0`, **in develop's format**: `  Claude  :  <bar>  <pct>%  <mode>` plus the burn note when the rate-aware mode is above the static band. The label stays `Claude` at checkpoint A (it is the only scope); printing `<scope>` waits for a second scope. The per-model table below it reads `state.Budget()` and is otherwise unchanged; a `null` model entry still prints as a row of zeros.

**Data flow.** `state.json` → `budget.ParseState` (the `claude_pct` alias only) → `Governing` → `quotaMode` → tier downgrade / `LocalOnly`. The writer side is unchanged: the orchestrator still writes `claude_pct` by `jq`, and `syncStateJSON` still mirrors its history.

**Error cases:**

- **A missing `state.json`** yields no quotas, so mode is `normal`. This is develop's `readClaudePct() == 0`.
- **Malformed JSON** yields no quotas. This is develop's behaviour, unchanged.
- **A negative, over-100 or huge `claude_pct`.** `legacyLevel` clamps it to 0..100 **before** the int conversion, so `1e19` reads as 100 rather than wrapping.
- **A non-integer `claude_pct`** (`79.6`, or a string) reads as unset, so there is no quota. develop decoded the key into an `int`, which refused a fraction, and dispatch, the TUI and the desktop app must keep agreeing.
- **A non-integer or `null` history point** makes the history unknown (nil), so the static band governs alone. develop read such a point as 0, which faked a burst of burn.
- **A `quotas` key** is ignored entirely at checkpoint A. Nothing writes it, and reading it safely needs validation, timestamps and expiry, which are #1326 (E2).

**T1 effect.** Outside `internal/budget` and the frozen TUI (`internal/tui`) and desktop app (`desktop/api`), no non-test Go in the repository holds a `claude_pct` string literal or a `claudeMode` identifier; G2 scans the whole tree, not only `internal/dispatch`, `internal/serve` and `cmd/hydra`. The frozen readers still read the legacy key, which stays mirrored (RFC §6, scope C11).

### 1.2 #1208-A Task class (resolver + basis), temperature and max-cost passthrough

**New in `internal/trust/domain.go`** (beside `DomainForFile`, `:43`):

```go
// ErrTaskClass refuses a class outside the key grammar.
var ErrTaskClass = errors.New("task class must be dotted lowercase, e.g. classification.banking77")

// TaskClass normalises a client- or route-supplied class (trim, lowercase) and requires
// at least one dot, so it can never collide with a DomainForFile cell (#888).
func TaskClass(s string) (string, error)
```

Grammar: `^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)+$`, at most 128 bytes. A bare `go` or `default` is refused on the serve path. Extension classes stay valid **internal** keys, reachable from `--file` as today. A client that wants the `default` cell omits the header.

**Changed: `internal/serve/serve.go`:**

- `chatRequest` (`:146-159`) gains these fields:

  ```go
  Temperature *float64          `json:"temperature"`
  Metadata    map[string]string `json:"metadata"`
  ```

- `Request` (`:54-70`) gains `TaskClass string` (validated, possibly empty), `Temperature *float64` and `MaxCostUSD float64` (0 = the client set no ceiling).
- `chat` (`:173`) resolves the explicit class after `parseRoute` and before minting the id. It uses `explicitClass(req.Header, in.Metadata)`:
  - `X-Hydra-Task-Class`, else `X-Hydra-Domain` (alias, C9), else `metadata.task_class`;
  - when more than one is present and they differ after `trust.TaskClass` → 400;
  - grammar failure → 400 with `ErrTaskClass`'s message.
- `chat` reads `X-Hydra-Max-Cost-USD` (`MaxCostHeader`, declared in `contract.go` beside `TaskClassHeader`). It must parse as a finite number ≥ 0, else 400.
- `request` (`:266`) copies `TaskClass`, `Temperature` and `MaxCostUSD`, so the streamed and buffered paths still cannot disagree.
- A temperature outside `[0, 2]` or non-finite → 400 (OpenAI's range).

**Changed: `cmd/hydra/serve.go`:**

- `serveRouter` (`:27-31`) gains `defaultClass string`.
- In `Chat` (`:33-72`), the class and its basis are resolved first:

  ```go
  class, basis := req.TaskClass, "header"
  if class == "" && r.defaultClass != "" { class, basis = r.defaultClass, "route" }
  if class == "" { class, basis = trust.DefaultDomain, "default" }
  ```

  `Options.Domain = class` is set when the route is **unpinned**, or when `basis == "header"`. A header-less `hydra/<enum>`, `hydra/t<N>` or head id therefore keeps `Domain == ""` and routes byte-identically to today (the Decisions' `hydra/standard` rule). `Options.Temperature = req.Temperature`.
- `Options.MaxCostUSD = req.MaxCostUSD`. The chat path passes no ceiling today, so a request can only add one. #1209 (§1.4) then lowers it against the policy ceiling, and a client can never raise or lift that ceiling. The header is how E7 hands serve what its `--max-usd` budget has left (E7 §4.4).
- `cmdServe` (`:208`) gains `--task-class` (validated by `trust.TaskClass` before binding, like `--enum` at `:245`).

**Changed: `internal/dispatch/dispatch.go`:**

- `Options` gains `Temperature *float64`. Its comment says that nil means the provider default.
- The `executor.Request` literal (`:880-890`) gains `Temperature: opts.Temperature`.

  `executor.Request.Temperature` already exists (`executor.go:25-28`). All HTTP dialects already send it (`http.go:122,268,388,495,525,657`, `http_stream.go:91`). The CLI and agy executors ignore it, which is a known gap (§7).

**Echo.** #1208 creates `serve.Evidence` (§1.5) with only `TaskClass` and `TaskClassBasis`, plus `Answer.Evidence *Evidence`, and writes `X-Hydra-Task-Class` and the body's `hydra.task_class`/`hydra.task_class_basis`. #1210 adds the remaining fields to the same struct and writer, so there is never a second carrier for the class.

**Error cases:**

| Input | Result |
|---|---|
| bad grammar | 400 `invalid_request_error` |
| header and alias disagree | 400 naming both values |
| `--task-class` bad | the startup error before binding |
| temperature out of range | 400 |
| `X-Hydra-Max-Cost-USD` negative, non-finite or unparseable | 400 |
| a CLI head given a temperature | answers unpinned (no refusal in A, §7 R4; E7 excludes them with `--heads`, §1.6) |

### 1.3 #1206-A Objective, unpinned serve default, P̂ basis, seeded exploration

**New in `internal/rank/constraint.go`** (beside `Cheapest`, `:36`):

```go
// Objective is what an unpinned route optimises. The zero value is today's cost constraint.
type Objective string

const (
	Quality Objective = "quality"
	Cost    Objective = "cost"
)

// ParseObjective accepts "", "quality" or "cost", case-insensitively; anything else errors.
func ParseObjective(s string) (Objective, error)

// Strongest orders candidates by measured in-domain competence (Score.Effective, highest
// first), then known price ascending (unknown last), then ID: a total order, so equal
// inputs give equal output. Fills Score.CostUSD like Cheapest.
func Strongest(heads []provider.Head, scores map[string]Score, price Price) ([]provider.Head, map[string]Score)

// Measured reports whether this score rests on ≥ MinCommitments in-domain observations.
func (s Score) Measured() bool { return s.InDomain >= MinCommitments }
```

`Cheapest` (`:53`) replaces its inline `sc.InDomain >= MinCommitments` with `sc.Measured()`, so the measured/unmeasured line is drawn in one place. The P̂ basis header and the eligibility gate then cannot disagree.

**Changed: `internal/dispatch/dispatch.go`:**

- `Options` gains `Objective rank.Objective`.
- **At `:677-683`.** When `tier == "" && opts.Head == ""`, the order depends on the objective:
  - `rank.Quality`: `candidates, scores = rank.Strongest(candidates, d.scoresFor(candidates, domain), d.price)`.
  - Otherwise (`""` or `cost`): `constrain` exactly as today.

  `scoresFor` is `explain`, falling back to `rank.Scores(candidates, nil)` (declared = effective) when there is no domain or calibrator, so `Strongest` always orders on something.
- **At `:681-683`.** `explain` runs on every pinned dispatch, not only `DryRun`, because serve needs P̂ for the answering head. It is an in-memory lookup per candidate.
- **The success `Result` (`:931`).** It now carries `Domain: domain, Scores: scores` (today only the dry run does, `:694-697`).

**Seeded exploration (`internal/dispatch/explore.go`):**

- `config.Config` gains `ExploreSeed uint64 `toml:"explore_seed,omitempty"``. Zero means unseeded.
- `Dispatcher` gains `rng *lockedRand`, built in `New` (`:490`):
  - non-zero seed: `rand.New(rand.NewPCG(seed, seed))` behind a `sync.Mutex`;
  - zero seed: the package-global source (today's behaviour).
- `pick` (`:28`, `:31`) calls `d.rng.Float64()` and `d.rng.IntN(n)` instead of the globals. With a non-zero seed and sequential requests, the exploration draws repeat exactly. Under concurrent requests the per-request assignment depends on scheduling (§7 R6).

**Changed: `internal/serve/serve.go`.** `Request` gains `Objective string`. `chat` reads `X-Hydra-Objective`; `rank.ParseObjective` failure → 400 listing `quality, cost`.

**Changed: `cmd/hydra/serve.go`:**

- **`routeToDispatch` (`:155-181`), default case.** It returns `"", "", "", nil` (no tier, no enum), unless `--enum` was set explicitly (`c.Flags().Changed("enum")`), in which case it keeps today's pin. The signature becomes `routeToDispatch(rt serve.Route, pinnedEnum string)`. `""` means unpinned.
- **`serveRouter`** gains `objective rank.Objective` (the route default from `--objective`, default `quality`) and sets `Options.Objective` to the header's value or the default. A pinned route passes no objective.
- **`cmdServe`** gains `--objective`. The `--enum` default stays `STANDARD`, but it applies only when explicitly passed. The help text and banner say `default routing  objective quality (hydra/standard pins the old route)`.

**P̂ and its basis**, for the head that answered (and, under #1209, for every step):

```
P̂    = Scores[head].Effective / 100, clamped to [0.01, 0.99]
basis = "measured"        if Scores[head].Measured()
        "catalogue_prior" otherwise
```

The clamp keeps the logit finite for a catalogue score of 100 or 0.

**Error cases:**

- **An unknown objective** → 400.
- **An unpinned route with no routable head** → today's `ErrNoHeads` path → 502. With `--local`, the error already names the blocked heads.
- **A pinned route plus an objective header.** The header is ignored, and `X-Hydra-Objective` is omitted, because nothing was ordered by it (§2).

### 1.4 #1209-A `match` verifier, one cost value, verifier-gated escalation

**New file `internal/oracle/match.go`** (shipped in #1328, PR 0):

```go
// MatchOracle passes an answer that is one of a closed label set, or matches a pattern.
type MatchOracle struct {
	Source  string              // "verifier:match:<id>"
	labels  map[string]struct{} // normalised
	pattern *regexp.Regexp      // anchored, so the whole answer must match
}

// NewMatch builds from exactly one of labels (non-empty) or pattern (compiles); else errors.
func NewMatch(id string, labels []string, pattern string) (*MatchOracle, error)

// Verify: labels → NormaliseLabel(candidate) ∈ set; pattern → the whole answer, without format
// runes and edge whitespace and otherwise not rewritten, matches. Empty always fails.
func (o *MatchOracle) Verify(_ context.Context, candidate string, _ trust.Task) (Verdict, error)

// NormaliseLabel lowercases, drops format runes, maps Unicode dashes to '-', strips edge whitespace,
// wrappers, a '*' pair and trailing dots to a fixed point, then folds interior space or '_' runs
// to one '_'. '-' and a lone '*' are significant. E7 scores Pass with this same function.
func NormaliseLabel(s string) string
```

**`NormaliseLabel`, the contract E7 grades with:**

1. Lowercase.
2. Drop every Unicode format rune (category Cf: BOM, zero-width space, and so on), wherever it is.
3. Map the Unicode dashes U+2010–U+2015 and the minus sign U+2212 to ASCII `-`.
4. Repeat until nothing changes: strip edge whitespace and the wrappers `"` `'` `` ` `` `“` `”` `‘` `’` `_` and the bullet `•` (U+2022, the only bullet), then one trailing `.`, then one **matched** pair of `*` around the whole string.
5. Fold every interior run of whitespace or `_` to a single `_`.

**`-` is always significant.** It is never folded and never stripped as a bullet, so `card-arrival` ≠ `card_arrival`, and `A-`, `-1` and `- 3` (→ `-_3`) keep their sign. A lone `*` is significant too (`A*` → `a*`, `* x` → `*_x`); only a matched pair is emphasis (`**Card Arrival**` → `card_arrival`). The function is idempotent, so normalising the gold label and the answer can never disagree with normalising either twice.

Examples: `"Card Arrival."`, `` `card_arrival` ``, `“Card Arrival”`, `• card_arrival`, `card​_arrival` → `card_arrival`; `card-arrival` and `Card-Arrival.` → `card-arrival`; `−1` (U+2212) → `-1`.

**Pattern mode** is anchored as `^(?:p)$`, so the whole answer must match. The answer is only `strings.TrimSpace`d after its Cf runes are dropped; it is not lowercased or otherwise rewritten. An empty answer fails before the regex sees it, since `.*` matches the empty string. `NewMatch` compiles the **raw** pattern first and then the wrapped one: `a)|(.*` is unbalanced alone but balances inside `^(?:…)$`, so it would escape the anchors, and a pattern nested one level below the parser's limit fails only once wrapped. Both failures are errors, never a panic.

**`NewMatch` refuses at construction:** both labels and a pattern, or neither; an empty label set; a label that normalises to empty (it would let an empty or all-punctuation answer pass); two labels that normalise to the same key (naming both, since one answer would satisfy either); and a pattern that fails either compile.

`Detail` on a fail is `not one of N labels: "<first 80 bytes>"` (or `does not match <pattern>: "…"`). The quoted head of the answer is always kept, with the truncation marker outside the quotes; the check description is what gets cut to fit. `oracle.go` gained one rune-safe truncation policy (`truncate`/`clip`, also used by `firstLine`). The `verifier:<kind>:<id>` key needs no exemption work, because `trust.UnreadableSourceKey` already passes `verifier:` (`domain.go:58-59`).

**New in `internal/config/config.go`** (route config). `Serve` and `Config.Serve` already exist, added by #1325 (#1329) with `Heads` (§1.6); PR 4 adds `Verifiers` beside it:

```go
// ServeVerifier attaches one check to one task class on `hyctl serve`.
type ServeVerifier struct {
	Class   string `toml:"class"`             // trust.TaskClass grammar
	Kind    string `toml:"kind"`              // "match" in A
	ID      string `toml:"id"`                // → verifier:match:<id>
	Labels  string `toml:"labels,omitempty"`  // file, one label per line, '#' comments
	Pattern string `toml:"pattern,omitempty"`
}

type Serve struct {
	Heads     []string        `toml:"heads,omitempty"`     // shipped in #1329 (§1.6)
	Verifiers []ServeVerifier `toml:"verifiers,omitempty"` // PR 4
}

// Config already has (since #1329):
Serve Serve `toml:"serve,omitempty"`
```

Here is an example, which the E7 harness writes into its fresh `HYDRA_HOME/config.toml`:

```toml
[[serve.verifiers]]
class  = "classification.banking77"
kind   = "match"
id     = "banking77"
labels = "/abs/path/banking77.labels"
```

**One cost value (`internal/dispatch/dispatch.go`):**

```go
// CallCost prices one call to h: by its model when pricing knows it, else by its tier.
// The single derivation for ordering (headPrice), cost rows, and E7's direct arms.
func CallCost(db *pricing.DB, h provider.Head, inputTokens, outputTokens int) float64
```

It is `db.CostForModel(h.Meta["model"], rank.UITier(h), in, out)`, and `0` for a nil `db`. Its uses:

- **`headPrice` (`:1526-1541`)** keeps its known/unknown check, and its value comes from `CallCost(db, h, nominalTokens, nominalTokens/2)`.
- **`Result` gains `CostUSD float64`.** It is set once, at `:931`, as `CallCost(d.pricing, h, resp.InputTokens, resp.OutputTokens)`.

  `remember` (`:939`), the `KindDispatchFinished` event (`:945`) and `logDispatch` (`:1676`, `est_cost_usd`) all read `r.CostUSD`. Today the same number is computed three times. It is also computed by tier only, while `headPrice` orders by model price, so a cost row could disagree with the price the router chose on. #1242 then changes `CallCost`'s inputs (cached tokens) in one place, and `X-Hydra-Cost-USD` follows with no serve change.
- **Every other dispatch-side recomputation reads the one value.**
  - The `MaxCostUSD` preflight (`:831`) uses `CallCost(d.pricing, h, estIn, estIn/2)`, so the ceiling and the row price a call the same way.
  - `cmd/hydra/vet.go:170` and `cmd/hydra/workflow.go:117` recompute a dispatch's cost from its tokens. They read `res.CostUSD` instead.
  - With no callers left, `Dispatcher.EstimateCost` (`:396`) and `estimateCost` (`:1769`) are deleted.
  - `internal/swarm` (`cost.go:41,70`, `sprt.go:93`) keeps its own tier pricing. It is not on the A path, and #1242 must be told about it (§6).

**Escalation hook (`internal/dispatch/dispatch.go`):**

```go
// Options gains:
// Escalate judges an answer before it is final. true moves on to the next candidate, as
// a failure would, and the answer is logged and costed but neither cached nor handed off.
// Nil keeps today's behaviour: the first answer stands.
Escalate func(ctx context.Context, r *Result) (next bool, reason string)
```

The attempt loop (`:930-971`) is restructured, with no new loop:

1. After `d.health.Pass` and building `r` (with `CostUSD`, `Domain`, `Scores`), the following run for every answer, as today:
   - `OutputProvenance`;
   - `recordOutputFinding`;
   - `recordContextEcho`;
   - `capturePayloads`;
   - `captureRecall`;
   - the `KindDispatchFinished` event;
   - `logDispatch`;
   - `recordBudget`.

   An escalated answer was paid for, so it must reach `cost.jsonl` and the runlog.
2. `if opts.Escalate != nil` and it returns `next == true` and `i < len(candidates)-1`:
   - append `Attempt{Head, Model, Tier, Reason: "escalated: " + reason}`;
   - remember `last = r`;
   - `continue`.
3. Otherwise the existing tail runs: `remember`, `writeHandoff`, `syncStateJSON`, `KindTaskFinished ok`, then return `r`.
4. When candidates run out after at least one answer (a failure after an escalation), the dispatch returns `last, nil`, not an error. The hook's owner decides which answer to serve.

`serveRouter` sets `NoCache: true` whenever it passes `Escalate`, so an answer that was escalated past, or served unaccepted, can never be cached.

**New file `internal/decide/cascade.go`:**

```go
// Decision is how a routed request ended; X-Hydra-Decision on both endpoints.
type Decision string

const (
	Accepted        Decision = "accept"
	BudgetExhausted Decision = "budget_exhausted"
	NoEvidence      Decision = "no_evidence"
)

// Verifier is one check and the calibration key it is weighed under.
type Verifier struct {
	Source string
	Oracle oracle.Oracle
}

// Answer is one source's reply as the cascade judges it.
type Answer struct {
	Source   string
	Text     string
	PPass    float64 // P̂(pass | source, class), clamped to [0.01, 0.99]
	Measured bool    // P̂ basis
	CostUSD  float64
}

// Check is one verifier's verdict on one answer.
type Check struct {
	Source   string
	Passed   bool
	LLR      float64
	Measured bool
	Err      string
}

// Step is an answer and what was concluded about it.
type Step struct {
	Answer
	Checks     []Check
	Confidence float64
}

// Cascade judges answers in the order they arrive and says when to stop. One per request.
type Cascade struct {
	Target         trust.Target // the same type decide.Router stops at
	MaxEscalations int
	Cal            *trust.Calibrator
	Domain         string
	Verifiers      []Verifier

	steps    []Step
	spent    float64
	decision Decision
}

// Judge runs every verifier on a, computes the posterior, and reports whether to ask the next source.
func (c *Cascade) Judge(ctx context.Context, a Answer) (next bool, reason string)

// Outcome is the served step and the evidence for the response contract and trust log.
func (c *Cascade) Outcome() ChatOutcome

type ChatOutcome struct {
	Served      Step
	Index       int     // which step is served
	Steps       []Step
	Decision    Decision
	Confidence  float64 // Served.Confidence
	CostUSD     float64 // Σ steps
	Escalations int     // len(Steps)-1
}
```

**`Judge`, step by step:**

1. Run each verifier. An oracle error is recorded as `Check{Err}` and contributes 0.
2. `LLR = oracle.LLR(cal, v.Source, domain, verdict)`, used only when `oracle.Measured(cal, v.Source, domain)` is true. Otherwise it contributes 0 and `Measured = false` (scope: an uncalibrated verifier contributes 0).
3. `Λ = logit(PPass) + Σ LLR`, and `Confidence = σ(Λ)`. The step is appended and `spent += CostUSD`.
4. If `Confidence ≥ Target.Confidence` → `Accepted`, stop.
5. If no verifier is measured for this class → `NoEvidence`, stop. This is `decide.Router`'s rule at `decide.go:87-90`: with nothing calibrated, no further answer can move the posterior, so asking would only spend.
6. If `len(steps) > MaxEscalations`, or `Target.MaxCostUSD > 0 && spent ≥ Target.MaxCostUSD` → `BudgetExhausted`, stop.
7. Otherwise, escalate with reason `"P(correct) 0.912 < 0.95"`.

**`Outcome`:**

- **The served step:**
  - `Accepted`: the last step.
  - `NoEvidence`: the first step.
  - `BudgetExhausted`, or the candidates ran out with no decision: the highest `Confidence`, earliest on a tie.
- **The decision.** It is `BudgetExhausted` when `Judge` never set one, which happens when the candidates ran out.

**`decide.Router` reuse (#1210 wires its header).** `decide.Outcome` gains `Decision Decision`, set as follows:

| How `Router.Decide` ends | `Decision` |
|---|---|
| The first answer clears the target (`:80`) | `Accepted` |
| A single head that does not clear it | `BudgetExhausted` (no source left) |
| `trust.ErrNoEvidence` (`:88`) | `NoEvidence` |
| `res.Samples == 0` (`:96`) | `BudgetExhausted` |
| The SPRT ended | its `res.Decision` mapped: `DecisionAccept` → `Accepted`, `DecisionStoppedOnBudget` → `BudgetExhausted` |

Today `:99` returns the SPRT's candidate even when it stopped on budget. That stays, and it is now labelled.

**cmd glue (`cmd/hydra/serve.go`).** `cmdServe` does the following at startup:

- loads one `trust.Calibrator`, shared with `newDecisionRouter` (which loads its own today, `serve_decisions.go:44`);
- builds `map[class][]decide.Verifier` from `cfg.Serve.Verifiers`;
- reads `--max-escalations` (default 2);
- reuses `--confidence` (`:274`, already the decisions target) and the policy cost ceiling (`costCeiling`, `serve_decisions.go:55`) as `trust.Target`.

Per request, `Target.MaxCostUSD` and `Options.MaxCostUSD` are the smaller positive of the policy ceiling and the request's `X-Hydra-Max-Cost-USD` (§1.2), or whichever one is set.

E7 starts one `hyctl serve` per served arm, so `--confidence` and `--max-escalations` are per-arm values. The harness fixes them in the run config before any call (§7 R1).

`serveRouter.Chat` builds one `Cascade` per buffered request whose class has verifiers. It passes `Escalate` as an adapter that fills `decide.Answer` from `r.Head.ID`, `r.Output`, P̂/basis (§1.3) and `r.CostUSD`, and keeps each `*Result` for tokens and model.

**Error cases:**

| Case | Result |
|---|---|
| A verifier config with an unknown `kind`, a bad class grammar, both or neither of `labels`/`pattern`, an unreadable or empty labels file, a label that normalises to empty, two labels that normalise to the same key, a regex compile error (raw or anchored), or a duplicate `(class, id)` | a startup error naming the entry index; nothing binds |
| A verifier errors on one answer | `Check.Err`, LLR 0, and the request continues |
| Every candidate failed before any answer | today's 502 |
| The cascade stops on `BudgetExhausted` | 200 with the best step and `X-Hydra-Decision: budget_exhausted`. This is not an error: the client reads the decision. |

### 1.5 #1210-A Response contract and per-request trust log

**New file `internal/serve/contract.go`:**

```go
const (
	TaskClassHeader   = "X-Hydra-Task-Class"
	TaskClassAlias    = "X-Hydra-Domain" // accepted on input; DomainHeader is renamed to this
	ObjectiveHeader   = "X-Hydra-Objective"
	PCorrectHeader    = "X-Hydra-P-Correct"
	TargetHeader      = "X-Hydra-Target"
	DecisionHeader    = "X-Hydra-Decision"
	SourcesHeader     = "X-Hydra-Sources"
	CostHeader        = "X-Hydra-Cost-USD"
	EscalationsHeader = "X-Hydra-Escalations"
	BasisHeader       = "X-Hydra-P-Pass-Basis"
	MaxCostHeader     = "X-Hydra-Max-Cost-USD" // request only, added by #1208 with TaskClassHeader
	// Deprecated names kept one release on /v1/decisions (removal: follow-up issue).
	legacySourcesHeader    = "X-Hydra-Decision-Sources"
	legacyConfidenceHeader = "X-Hydra-Decision-Confidence"
)

// Evidence is what the router concluded; nil on an Answer means it reported none.
type Evidence struct {
	TaskClass      string  `json:"task_class"`
	TaskClassBasis string  `json:"task_class_basis"`
	Objective      string  `json:"objective,omitempty"` // empty on a pinned route
	PCorrect       float64 `json:"p_correct"`
	Target         float64 `json:"target"`
	Decision       string  `json:"decision"`
	Sources        []string `json:"sources"`
	CostUSD        float64 `json:"cost_usd"`
	Escalations    int     `json:"escalations"`
	PPassBasis     string  `json:"p_pass_basis"`
	Steps          []EvidenceStep `json:"steps"`
}

type EvidenceStep struct {
	Source     string          `json:"source"`
	PPass      float64         `json:"p_pass"`
	PPassBasis string          `json:"p_pass_basis"`
	Checks     []EvidenceCheck `json:"checks,omitempty"`
	Confidence float64         `json:"confidence"`
	CostUSD    float64         `json:"cost_usd"`
}

type EvidenceCheck struct {
	Source   string  `json:"source"`
	Passed   bool    `json:"passed"`
	LLR      float64 `json:"llr"`
	Measured bool    `json:"measured"`
	Error    string  `json:"error,omitempty"`
}

func writeEvidence(h http.Header, e *Evidence) // no-op on nil
```

**Formats:**

- `P-Correct` and `Target`: `%.4f`.
- `Cost-USD`: `%.6f`, the same precision `pricing.round6` keeps.
- `Sources`: comma-joined with no spaces, matching `decisions.go:81`. Each step's head is followed by its verifiers in order, for example `bedrock/llama-3-8b,verifier:match:banking77,bedrock/claude-haiku,verifier:match:banking77`.

**Changed: `internal/serve/serve.go`:**

- `Answer` gains `Evidence *Evidence`.
- `chat` (`:205-237`) calls `writeEvidence(w.Header(), ans.Evidence)` before `writeJSON`, and adds `"hydra": ans.Evidence` to the body map. It is omitted when nil, so a router reporting nothing keeps today's exact body.
- `usage` is the sum over the steps (§2).
- The streamed path is unchanged: SSE headers are already on the wire (B, #1226).

**Changed: `internal/serve/decisions.go`:**

- `:70` reads `TaskClassHeader`, falling back to `TaskClassAlias`. There is no grammar check, because a decision domain predates the grammar, and breaking existing callers is out of scope.
- `:81-82` set `SourcesHeader`, `PCorrectHeader`, `DecisionHeader` (`out.Decision`) and `TaskClassHeader` (`trust.Domain(call.Domain)`). They also set the two legacy names, with the same values, for one release.

**Changed: `cmd/hydra/serve.go`.** `serveRouter.Chat` does the following:

- builds `serve.Evidence` from `ChatOutcome`, or from a single implicit step when the class has no verifier: `PCorrect = P̂`, `Decision = no_evidence`;
- takes `Output`, `Head`, `Model` and `ToolCalls` from the **served** step's `*Result`;
- writes one `trust.RunLog` per request:

  ```go
  trust.LogRun(trust.DefaultLogPath(), trust.RunLog{
      TaskHash: trust.TaskHash(prompt), Domain: class,
      TargetConf: target, FinalConf: o.Confidence, Samples: len(o.Steps),
      Models: heads, CostUSD: o.CostUSD, CostSource: costSource,
      Decision: string(o.Decision), Ledger: o.Ledger(), SpanID: runlog.SpanIDFor(taskID),
  })
  ```

**New in `internal/decide/cascade.go`:**

```go
// Ledger is the run's evidence in trust.ApplyRunOutcome's shape: per step the head
// (Agreed=true, it asserted its answer) then each verifier (Agreed=Passed), Candidate = the
// step's text, with the served step last so ApplyRunOutcome's "final" is the answer served.
func (o ChatOutcome) Ledger() []trust.Evidence
```

A rejected earlier step's head is then re-expressed by `verdictOn` (`outcome.go:51-66`) as a negative on the served answer. Its verifier's fail is skipped, because it ruled out a superseded answer. That is the existing semantics, unchanged. B's `/v1/feedback` reaches this run through the request id: `X-Request-Id` → run → task span → `trust.FindRunBySpan`.

**Error cases.** A failure to write the trust log never fails the request. This matches `logTrustRun`, which also ignores `LogRun`'s error (`main.go:4159`). Evidence is not written on an error response, which carries only `X-Request-Id` as today.

### 1.6 #1325 Serve head allowlist (`hyctl serve --heads`)

This ticket was added in the 2026-11-05 reconciliation with E7 (E7 §3.4, E1-D5). It is independent of the other E1 tickets.

**Why.** E7 must keep CLI-agent heads (Claude Code, Codex, Antigravity) out of every arm (E7 scope §5.2). They are found with `exec.LookPath`, and `shellpath.Adopt` re-adds the login shell's `PATH`, so stripping `PATH` does not remove them (E7 K5). It is also what makes a deployment's head set explicit.

*Updated to the code shipped in #1329 ("Closes #1325", branch `feature/1325-serve-head-allowlist`). See "As shipped" at the end for what changed from the first draft.*

**Changed: `internal/dispatch/dispatch.go`.**

```go
// ServeHeads is config.toml's `[serve] heads`, read from the config New already loaded.
func (d *Dispatcher) ServeHeads() []string

// Allows reports whether head id may answer here: any head, unless RestrictHeads set a list.
func (d *Dispatcher) Allows(id string) bool

// RestrictHeads routes among only the named heads, in CapScore order; embedding lookups keep
// the whole discovery. It refuses an unknown, repeated or unroutable id (an embedding-only one
// may be listed), or a list with no routable head, or no local one under localOnly.
func (d *Dispatcher) RestrictHeads(ids []string, localOnly bool) error
```

- **Input.** Each id is whitespace-trimmed and blanks are dropped; a list with no ids left restricts nothing.
- **What it narrows.** It replaces `d.heads` (`:327`) once at startup with a new slice of the listed heads, in discovery (CapScore) order, keeps the full discovery in `d.discovered`, and records the list in `d.allowed`. Every routing reader goes through `d.heads` or `Heads()` (`:371`): selection and fallback (`:1419-1451`), `blockedHeads` (`:1260`), `/v1/models` (`advertisable`, `serve.go:136`) and the banner. So the router can neither choose, fall back to, nor advertise a head outside the list.
- **`localOnly` is `--local` only.** Under `--local` the list must hold at least one routable local head. The `pii` policy's `local-only` action is **not** treated as local-only for every request: it binds only prompts classified as PII, so a hosted-only list is accepted under it.
- **Pinned heads.** A pinned head id that `Allows` refuses gets `ErrNoHeads` with `` %q is outside this server's `hyctl serve --heads` allowlist ``, so the error names the allowlist instead of reading as an unknown head.
- **Answer cache.** `fromCache` checks `Allows(hit.Head)`. A hit produced by a head off the list is treated as a **miss**, because it is an answer this server may not give.
- **Embedding lookups use the full discovered set.** The cache's embedder (`cache.go:35`), the corpus embedder (`:377`) and recall (`capture_recall.go:22`, `retrieve.Open`) resolve through `embedHeads()`, which returns `d.discovered` when a list is set. An embedding head, including an untagged `embed_model` head, therefore stays usable without becoming routable. `embed.Resolve` reaches only local endpoints, as on develop.

**Changed: `internal/config/config.go`.** `Config` gains `` Serve Serve `toml:"serve,omitempty"` ``, with `` Serve.Heads []string `toml:"heads,omitempty"` ``, so the list can live in `config.toml` as `[serve] heads = [...]`. (#1209's `Verifiers` joins the same struct, §1.4.)

**Changed: `cmd/hydra/serve.go`.** `cmdServe` gains `--heads a,b` (a string slice). `restrictServeHeads` runs right after `dispatch.New` and before the decision router is built and the server binds:

- the flag, when given (`c.Flags().Changed("heads")`), replaces `[serve] heads` for this run, and `--heads ""` clears it;
- otherwise `[serve] heads` from `config.toml` applies;
- a refusal is the startup error `head allowlist: <reason>`, before binding.

When neither is set, behaviour is unchanged.

**`/v1/decisions` is restricted too.** `newDecisionRouter(ctx, confidence, localOnly, d.Allows)` drops every decision source the allowlist does not allow. Decision ids come from a separate inventory (`executor.DecisionHeads`) and are never dispatch head ids, so with any list set `/v1/decisions` has no sources. The banner then prints `decisions  none: --heads names no decision source, so /v1/decisions has none (N dropped)`; it reports an emptied endpoint, not a partly filtered one.

**Error cases (all startup errors before binding, naming the id):**

- An unknown id → `` no discovered head with id "x"; run `hyctl probe` to see what this machine has ``.
- A duplicated id (after trimming) → `head "b" is listed twice`.
- An unroutable id → `head "x" cannot be routed: <unroutable_reason>`. An embedding-only head may be listed.
- A list with no routable head (for example only an embedding head) → `no listed head can be routed`.
- `--local` with no routable local head in the list → `no listed head is a routable local head, and this server is local-only (--local)`.

A refused list leaves the inventory unchanged.

**Known follow-up (#1329).** When the cache's best hit came from an excluded head, the lookup is a miss even if a listed head's entry would also have matched. That loses a possible hit and leaks nothing.

## 2. Request path end to end (`hyctl serve`, `model:"hydra"`)

The example is a buffered Banking77 request against a server started as `hyctl serve --task-class classification.banking77`, with `[[serve.verifiers]]` naming `match:banking77`. Each step is marked **[decide]** where `internal/decide` is reused, and **[new]** or **[existing]** otherwise.

```
client ── POST /v1/chat/completions  {model:"hydra", temperature:0, messages:[…]}
          X-Hydra-Task-Class: classification.banking77   X-Hydra-Objective: quality
```

1. **Parse** [existing + new]. In `serve.chat`:
   - `readBody` and `json.Unmarshal` run.
   - `parseRoute("hydra")` returns `Route{}`, which is unpinned.
   - `explicitClass` gives `classification.banking77` (via `trust.TaskClass`).
   - `rank.ParseObjective("quality")` succeeds.
   - The temperature range is checked.
   - `runid.New()` is minted as the `X-Request-Id`.

   Any refusal here is a 400 that still carries `X-Request-Id`.
2. **Resolve the task class** [new]. `serveRouter.Chat` sets `class = classification.banking77`, `basis = header`.
   - Without the header, the route default is used, with basis `route`.
   - Without a route default, the class is `default`, with basis `default`.

   The route is unpinned, so `Options.Domain = class`.
3. **Choose the objective** [new]. The header gives `quality`; without it, the route default (`--objective`, default `quality`). The route is unpinned, so `Options.Objective = quality` and `TierHint = Enum = ""`.
4. **Open the run** [existing]. `runlog.DeclareRun(requestID, taskID, prompt)`.
5. **Set up the cascade** [decide]. The class has verifiers, and this is not a stream, so:

   ```
   casc := &decide.Cascade{Target: {0.95, ceiling}, MaxEscalations: 2, Cal, Domain: class,
                            Verifiers: [verifier:match:banking77]}
   Options.Escalate = adapter(casc)
   Options.NoCache  = true
   ```

   Without verifiers there is no `Escalate`, and step 9 runs once.
6. **Dispatch preamble** [existing]. `d.Dispatch` runs:
   - signals `Decide`;
   - `quotaMode` (no tier to downgrade; emergency still forces local, §7 R2);
   - `CheckAskable`;
   - `policy.Classify` / `Evaluate`;
   - the cache, skipped because of `NoCache`;
   - egress provenance;
   - `selectHeads("", localOnly, class)`, which returns every live head, `rerankFor(class)`.
7. **Order candidates by P̂** [new]. `tier == "" && Head == ""` and the objective is `Quality`, so:

   ```
   scores = explain(candidates, class)        // rank.Scores → EffectiveScoreIn per head
   candidates = rank.Strongest(candidates, scores, d.price)
   ```

   The order is `Effective` descending, then known price ascending, then ID: a total order. The **basis** of each head's P̂ is `scores[h].Measured()`, meaning ≥ 20 judged answers in `classification.banking77`. Then `pick` runs:
   - it is the identity at `explore_rate = 0`;
   - otherwise it draws from the seeded RNG.
8. **First call** [existing]. The attempt loop runs per candidate, in order:
   - the ledger check and the egress guard;
   - the cost preflight;
   - `executor.Stream` with `Temperature: 0` on head H1.

   A transport failure is a **fallback**, not an escalation: the breaker records it, an `Attempt` is added, and the loop continues.
9. **Answer recorded, then judged** [existing + new].
   - **Recorded.** `r.CostUSD = CallCost(pricing, H1, in, out)`, the one value. `KindDispatchFinished` (CostUSD) and `logDispatch` (`cost.jsonl` row, `est_cost_usd = r.CostUSD`) are written, and `recordBudget` runs.
   - **Judged** [decide]. The adapter calls `casc.Judge(ctx, Answer{H1, text, P̂(H1), basis, r.CostUSD})`:
     - `MatchOracle.Verify(text)` checks `NormaliseLabel(text) ∈ labels`.
     - `LLR = oracle.LLR(cal, "verifier:match:banking77", class, verdict)`, if `oracle.Measured`; otherwise 0.
     - `conf = σ(logit(P̂) + ΣLLR)`.
     - Stop rules, in order: `conf ≥ 0.95` → accept. No measured verifier → no_evidence (`decide.Router`'s rule). `steps > 2` or spend ≥ ceiling → budget_exhausted. Otherwise escalate.
10. **Escalate** [new hook, existing loop]. When `next == true`:
    - `Attempt{H1, "escalated: P(correct) 0.912 < 0.95"}` is recorded;
    - nothing is cached, handed off or synced to state;
    - the loop continues to H2 and repeats 8–9.

    Under `quality`, H2 is the next head by P̂. Under `cost`, it is the next head in `constrain`'s order: cheapest competent first, then the measured order.
11. **Finish dispatch** [existing]. When the cascade stops, or the candidates run out:
    - `writeHandoff`, `syncStateJSON` and `KindTaskFinished ok` run;
    - the last `*Result` is returned.
12. **Outcome** [decide]. `casc.Outcome()` returns the served step, `Decision`, `Confidence`, `CostUSD = Σ steps` and `Escalations = steps − 1`.

    `serveRouter` builds `serve.Answer` as follows:
    - `Output`, `Head`, `Model` and `ToolCalls` come from the served step's `*Result`;
    - `usage` is the sum over all steps;
    - `Evidence` is filled in.
13. **Trust log entry** [new, decide-built]. `trust.LogRun(trust.jsonl, RunLog{TaskHash, Domain: class, TargetConf: 0.95, FinalConf, Samples: steps, Models: heads, CostUSD, Decision, Ledger: o.Ledger(), SpanID: SpanIDFor(taskID)})`. Its error is ignored.
14. **Response** [new]. Status 200, with headers then body:

    ```
    X-Request-Id: <run id>
    X-Hydra-Task-Class: classification.banking77
    X-Hydra-Objective: quality
    X-Hydra-P-Pass-Basis: measured
    X-Hydra-P-Correct: 0.9731
    X-Hydra-Target: 0.9500
    X-Hydra-Decision: accept
    X-Hydra-Sources: H1,verifier:match:banking77,H2,verifier:match:banking77
    X-Hydra-Cost-USD: 0.000412
    X-Hydra-Escalations: 1
    {"id":…, "model":"H2", "choices":[…], "usage":{…summed…},
     "hydra":{"task_class":…, "task_class_basis":"header", "steps":[…], …}}
    ```

**Where `internal/decide` is reused, and where it is not.** The cascade's stopping rule, its no-evidence rule, its `Target` type and its `Decision` vocabulary are `decide`'s. The SPRT (`trust.Run`) is **not** used on the chat path in A. A's cascade judges each source's answer on its own (P̂ prior + verifier evidence) and never clusters answers, so the multi-hypothesis test would have nothing to compare (scope §3, Q1). `trust.Run` with `sampleOrder(obj)` joins the chat path at B.

**Variants:**

- **`hydra/standard`.** `TierHint` comes from `STANDARD`, with no `Domain` unless the header named a class, and no `Objective`. `X-Hydra-Objective` is omitted. All the other headers are present (with no verifier for `default`: `no_evidence`, the P̂ of the answering head).
- **`stream: true`.** Steps 1–8 run, and objective ordering applies. There is no cascade and there are no evidence headers, because SSE headers are already sent (trailers are B, #1226). The trust log is still written, with one step and `no_evidence`.
- **No route verifier for the class.** One step. `Decision: no_evidence`, `P-Correct` is P̂ of the answering head, and `Sources` is that head alone.

## 3. Harness hooks for E7

| Hook E7 needs | Where it lands | What the harness does |
|---|---|---|
| **Temperature and max-cost passthrough** (E7 §1.4: "serve passes no temperature"; E7 Q2) | #1208-A (PR 2), both: `chatRequest.Temperature` → `serve.Request` → `dispatch.Options.Temperature` → `executor.Request.Temperature`, and `X-Hydra-Max-Cost-USD` → `Options.MaxCostUSD` (and, from #1209, the cascade's `Target.MaxCostUSD`), which can only lower the policy ceiling (§1.2). The HTTP dialects already send the temperature. There is no echo header; G7 is what proves the temperature reaches the wire. | Send `"temperature": 0`, recorded in the breadcrumb as sent. That holds for every HTTP head; CLI/agy heads ignore it and are kept out by `--heads` (#1325). Send `X-Hydra-Max-Cost-USD` set to what the run's `--max-usd` budget has left. |
| **Head allowlist** (E7 K5, E1-D5; E7 Q3) | #1325 (§1.6, PR 6, shipped as #1329): `hyctl serve --heads a,b`, or `[serve] heads` in `config.toml`. | Start each served arm's `hyctl serve` with `--heads` set to the arm's router heads (the flag overrides any `[serve] heads`). `/v1/models` then lists only those heads plus the routing ids. An unknown, duplicate or unroutable id fails serve at startup, which the harness reports as a refused arm. |
| **Objective header** (E7: "serve reads no objective header") | #1206-A: `X-Hydra-Objective: quality\|cost`, echoed. An unknown value → 400. | Arms `hydra-quality` and `hydra-cost` send the header. `hydra-balanced` does not exist in A (scope §2a). |
| **Seedable exploration RNG** (E7: "`dispatch/explore.go` uses an unseeded global RNG") | #1206-A: `config.toml explore_seed = <uint64>`, a mutex-guarded PCG on the `Dispatcher`. 0 means unseeded (today). | Write `explore_rate = 0` (pure argmax) into the fresh `HYDRA_HOME/config.toml`. This is enough for A, because every exploration-free run is already deterministic. `explore_seed` is for a run that deliberately explores. Its draws repeat only when requests are sequential (§7 R6). |
| **Deterministic ordering** | #1206-A: `rank.Strongest` is a total order (Effective, price, ID). `constrain`/`Cheapest` already is (`cheaperFirst`, `constraint.go:70-79`). `SortMeasured`'s `rankLess` ends on ID (`rank.go:85`). | Isolation is already in E7 §1.5. In addition: (a) fill calibration **before** starting `hyctl serve`, because the `Dispatcher` and the cascade load calibration once at start (`dispatch.go:482`), so a restarted serve (resume) reads the same file again (E7 §4.2); (b) record `X-Hydra-Sources` per item, because the breaker can park a head mid-run after a 429 and change the order for the following items (§7 R5). |
| **Answer cache off** | #1209-A sets `NoCache` only when it passes `Escalate` (§1.4), so a request with no verifier (`hydra-default`, a class with none) is still cacheable when `cache_answers` is on. | Write `cache_answers = false` explicitly into the fresh home's `config.toml` (it is also the default), so no arm can be served another item's answer. |
| **Per-arm cascade parameters** (§7 R1) | #1209-A: `--confidence` and `--max-escalations` on `hyctl serve`. | Fix both per served arm in the run config before any call, pass them as flags to that arm's `hyctl serve`, and freeze them in `run.json`. |
| **Cost: one value in Hydra, one measurement in the harness** (E7 Q8) | #1209-A: `dispatch.CallCost(db, head, in, out)` → `Result.CostUSD`, read by `cost.jsonl`, the runlog, the cache and serve's `X-Hydra-Cost-USD` (Σ steps). | **The measured cost for #1213's thresholds (T4/H3) is the harness's:** provider-reported tokens × the dated price snapshot frozen in `run.json` (E7 §1.4), applied identically to every arm, direct and served. This supersedes the E1 scope's "Σ `X-Hydra-Cost-USD`" (scope §4) and must be written into #1213 before results exist. `X-Hydra-Cost-USD` is a **cross-check**: per served request, \|header − harness\| ≤ max($0.000001, 1% of the harness value). The check applies to heads whose model `pricing.DB` prices by name (OpenRouter allowlist heads) and to local heads ($0 on both sides). `env/bedrock` is tier-priced by `CallCost` (R3), so its rows are counted as unchecked, not as mismatches. Any checked request outside the tolerance makes H3 `inconclusive` with that reason. E7 therefore does **not** call `CallCost`; #1242 still changes Hydra's cost in one place. |
| Evidence headers | #1210-A, §2 step 14 | Read `X-Hydra-Decision`, `-P-Correct`, `-Cost-USD`, `-Sources`, `-Escalations`, `-P-Pass-Basis`, and `hydra.steps` from the body. Join on `X-Request-Id`, not the deleted `X-Hydra-Request-Id` (scope C9). |
| Label verifier and Pass | #1209-A, PR 0 (`feature/1209-match-verifier`): `oracle.NewMatch`, `MatchOracle` and `oracle.NormaliseLabel` | **Pass** = `NormaliseLabel(answer) == NormaliseLabel(gold)`, with the same function the served verifier uses (E7 §1.1: "There is no bench-only copy"). E7 imports it and never copies it. A **format** fail is the served verifier's own verdict: `NewMatch` over the suite's full label set fails the answer. The harness writes `[[serve.verifiers]]` with the suite's label file. |
| Calibration fill | the existing CLI semantics, no behaviour change | On the calibration split, per item and per head: `hyctl trust record --source <head-id> --domain classification.banking77 --said-correct --outcome correct\|incorrect` fills `CommitmentsIn`, hence P̂. For the verifier: `--source verifier:match:banking77 --said-correct=<verdict.Passed> --outcome <gold match>`, where the verdict is the full-label-set `MatchOracle`'s. This needs both verdict directions, or `oracle.Measured` stays false and the cascade reads `no_evidence`. E7 PR 6 extracts `trust.RecordOutcome` from `cmdTrustRecord` so the harness and the CLI share one writer; that is a refactor with no behaviour change. |

**Name alignment.** The E1 contract wins here, because it is the shipped API. E7 scope §1.3/§1.4 writes the following:

| E7 scope (or the E7 design draft) writes | E1 uses |
|---|---|
| `X-Hydra-Task` | `X-Hydra-Task-Class` |
| `classify/banking77` | `classification.banking77` (dotted; a `/` is refused by the grammar) |
| `hydra-balanced` arm | not in A |
| `Decision` `accept`/`abstain`/`exhausted` | `accept`, `budget_exhausted`, `no_evidence` (§1.4) |

The E7 design (`0001-design-E7.md`) uses the E1 names (reconciled 2026-11-05).

## 4. Backwards compatibility and migration

This section follows scope §5. Nothing on disk is rewritten.

**`~/.hydra/logs/state.json` (#1207-A)**

- **Read alias, not migration.** `budget.ParseState` derives `Quota{Scope:"session:claude-code", ContextFrac: claude_pct/100, History: claude_pct_history}` on every read, through the unexported `legacySessionQuota`. It is a pure function of the file, so it is idempotent by construction. The next `jq '.claude_pct = 52'` from the orchestrator protocol is picked up with no migration step. `claude_pct` and each history point are read as integers only, as develop's `int` decode read them (§1.1 error cases).
- **No `quotas` key at A.** `ParseState` ignores a `quotas` key entirely, so `session:claude-code` always comes from the alias and no other scope exists yet. Reading it safely needs validation, timestamps and expiry, so the reader moved to #1326 (E2).
- **Writer.** `syncStateJSON` keeps mirroring `claude_pct_history` through `budget.MirrorLegacyHistory`. It never deletes the legacy keys, so an older `hyctl`, the frozen TUI (`internal/tui/cockpit.go:257`) and the desktop app (`desktop/api/dashboard.go:212`) keep reading them.
- **Downgrade.** The governing quota's `Mode` drives `quotaMode` with the same thresholds and the same tier arithmetic as `claudeMode`. A golden old file yields identical mode and tier (test G1, §5).
- **Retirement** of the alias happens when `CLAUDE.md`'s protocol writes through a `hyctl quota set`. That is a follow-up, not E1.

**Calibration and evalset files.** There is no rewrite, and keys stay `(source, domain)`.

- Coding classes are byte-identical. The CLI never calls the serve resolver, and `routingDomain` (`dispatch.go:1585-1594`) is unchanged.
- Serve classes must be dotted (`trust.TaskClass`), and `DomainForFile` can never produce a dot (`filepath.Ext` returns the last extension only). So new and old cells cannot collide.
- `default` keeps its meaning: a header-less, route-default-less unpinned serve request reads the `default` cell, and says so with `task_class_basis: default`.
- Verifier keys: new ones are `verifier:match:<id>`. The existing `verifier:<cmd>`, `verifier:grounding` and `verifier:predicate:<id>` are untouched.
- `trust.jsonl` gains chat runs. `RunLog`'s schema is unchanged, and `Decision` takes the values `accept`, `budget_exhausted` and `no_evidence`. `trust.Aggregate` counts only `accept` (`log.go:150`), so `hyctl trust stats` is unaffected in meaning. A chat run's `Samples` counts steps, which matches "sources asked".

**`hyctl serve` routing default.** This is the deliberate breaking change from Decision 2.

| Client sends | Before | After |
|---|---|---|
| `model: "hydra"` or `""` | pinned `--enum` (default `STANDARD`) | unpinned, ordered by objective (`quality` unless `X-Hydra-Objective` or `--objective` says otherwise) |
| `model: "hydra/standard"` (or any `hydra/<enum>`, `hydra/t<N>`, head id) | pinned | pinned, byte-identical `dispatch.Options` when no class header is sent (test G4) |
| `hyctl serve --enum X` passed explicitly | pins X | pins X for `hydra`: the operator's explicit opt-out |

- **Commit and CHANGELOG.** The commit is `feat(serve)!: route model "hydra" by objective, quality by default`, with a `BREAKING CHANGE:` footer that names both opt-outs (`hydra/standard` per request, `--enum STANDARD` per server). release-please then puts it in the CHANGELOG as breaking.
- **`/v1/models`.** It is unchanged: `hydra` and `hydra/<name>` are still advertised. The banner's "default routing" line changes text.

**Header vocabulary**

- **`/v1/chat/completions` (buffered).** It gains nine `X-Hydra-*` headers and a `hydra` body key. Both are additive; OpenAI SDKs ignore unknown headers and keys. A router that returns no evidence (`Evidence == nil`) yields exactly today's body.
- **`/v1/decisions` and `/v1/systemone`.**
  - They gain `X-Hydra-Sources`, `X-Hydra-P-Correct`, `X-Hydra-Decision` and `X-Hydra-Task-Class`.
  - `X-Hydra-Decision-Sources` and `X-Hydra-Decision-Confidence` are still sent, with equal values, for one release. A follow-up issue, filed with #1210's PR, removes them.
  - `X-Hydra-Task-Class` is accepted as input there too, with `X-Hydra-Domain` still read as its alias.
- `X-Request-Id` is unchanged, and no `X-Hydra-Request-Id` is added.
- One request-only header is new: `X-Hydra-Max-Cost-USD` (#1208, §1.2). It can only lower the server's policy ceiling, so a client that does not send it sees no change.

**Cost rows (`cost.jsonl` `est_cost_usd`).** For heads that name a model (`Meta["model"]`, which is OpenRouter, LiteLLM and llama.cpp), the value moves from the tier price to the model price. The router already ordered on the model price, so the row now states what the router believed. Every other head is unchanged. This is a `fix` line in #1209's PR body, because `hyctl cost` totals for those heads change.

## 5. Test plan

**Rules for every test:**

- They run under `go test -race ./...`.
- There is no live network, model, or wall-clock deadline (#1176). HTTP heads are `httptest` servers on loopback, as in `dispatch/stream_test.go:23-60`, and CLI heads are `testutil` fake binaries.
- Every package touched keeps its `ci/coverage-floors.txt` floor.

**Guard tests.** Each is a test whose failure means a contract broke. The *Breaks* column is the regression it catches, and the *Prints* column is its `t.Errorf` message.

### 5.1 #1207-A `internal/budget/quota_test.go`, `internal/dispatch/quota_test.go`

*As shipped in #1330.*

**Table-driven tests:**

- **`TestParseState_ReadsEveryShape`.** Rows:
  - no file;
  - malformed JSON;
  - legacy keys only;
  - a negative `claude_pct` (clamped to 0);
  - `claude_pct = 140` (clamped to 100);
  - a string `claude_pct` (absent);
  - a fractional `claude_pct` such as `79.6` (unset, as develop's `int` decode left it);
  - `claude_pct = 1e19` (clamped before conversion).
- **`TestParseState_NonNumericHistoryIsNoHistory`.** `[60,null,62]`, `[60,"x",62]`, `[60,61.5,62]` and a string history each give no history and mode `compact` at 62.
- **`TestParseState_IgnoresTheQuotasKey`.** A `quotas` key, alone or beside `claude_pct`, adds no quota and does not override the alias (#1326 reads it).
- **`TestParseState_Budget`.** A `null` model entry stays an empty row; a non-object entry is skipped.
- **`TestGoverning_PicksTheMostUrgent`.** Ties are broken by scope.
- **`TestMirrorLegacyHistory`.**
- **`TestQuotaMode_DowngradesLikeClaudeMode`.** It runs per mode × tier hint (`""`, `"3"`, `"9"`, `"10"`), covering the +1, +2 and →10 cases and the clamp at 10.
- **`TestQuotaMode_FastBurnEscalatesAboveTheStaticBand`.**
- **`TestGovernorLog_KeepsDevelopsWording`** pins every governor log line to develop's text, and **`TestGovernorLog_ClaimsADowngradeOnlyWhenTheTierMoved`** pins the one deliberate difference.
- **`cmd/hydra`:** `TestPrintBudgetStatus_KeepsDevelopsSessionLine` (`  Claude  :  █████░░░░░  52%  compact`, and no line at 0%) and `TestPrintBudgetStatus_NullModelEntryIsAZeroRow`.

**Sync test.** `TestSyncStateJSON_InProcessSyncsNeverRollTheLevelBack` runs 32 writers × 16 rounds that share `stateMu` with `syncStateJSON`, and asserts no sync writes back a stale level. It fails 20 times out of 20 without `stateMu`. It covers in-process concurrency only; cross-process writes (`jq`, a second `hyctl`) are #1326.

| Guard | Breaks | Prints |
|---|---|---|
| **G1** `TestQuotaMode_GoldenLegacyStateRoutesAsBefore` (testdata `state_pre_e1_steady.json`, `state_pre_e1_burning.json`) | the alias and the old governor disagree on a real pre-E1 `state.json` | `legacy state.json: claudeMode gave mode=%s tier=%s, quotaMode gives mode=%s tier=%s` |
| **G2** `TestT1_NoLegacyKeyOutsideBudget` (in `internal/budget`, a `go/ast` walk of every non-test `.go` file from the repo root; comments are ignored; `internal/budget`, `internal/tui` and `desktop/api` are allow-listed; it fails if it scanned fewer than 100 files) | someone reintroduces a `claude_pct` string literal or a `claudeMode` identifier outside the alias and the frozen readers (T1) | `%s names %q; only internal/budget may read the legacy key (RFC 0001 T1)` |
| **G3** `TestQuotaMode_EmergencyStillForcesLocal` | the emergency branch loses `LocalOnly`, which is what protects a session at 80% | `quota at %d%% (emergency) dispatched to non-local head %s` |

### 5.2 #1208-A `internal/trust/domain_test.go`, `internal/serve/class_test.go`, `cmd/hydra/serve_cli_test.go`

**Table-driven tests:**

- **`TestTaskClass_Grammar`.**
  - Accepted: `classification.banking77`; `Classification.Banking77 ` (normalised).
  - Refused: `go`, `default`, `classify/banking77`, `.x`, `a..b`, and anything over 128 bytes.
- **`TestExplicitClass_HeaderAliasMetadata`.** Rows:
  - each source alone;
  - two that agree;
  - two that disagree → 400 naming both.
- **`TestChat_TemperatureRange`.** `nil`, `0`, `2` and `0.7` are accepted. `-0.1`, `2.1`, and NaN sent as a string are refused with 400.
- **`TestChat_MaxCostHeader`.** Absent → 0; `0.25` → 0.25; `-1`, `NaN`, `abc` → 400.

| Guard | Breaks | Prints |
|---|---|---|
| **G4** `TestServeRouter_HydraStandardRoutesAsBefore` (captures `dispatch.Options` via a recording seam on `serveRouter`) | `hydra/standard` stops being the old pinned route (Decision 2) | `hydra/standard built %+v, want TierHint=%q Enum=STANDARD Domain="" Objective=""` |
| **G5** `TestRoutingDomain_CodingPathUnchanged` (table: `--file x.go`, `x.GO`, `Makefile`, `--domain go`, nothing) | the CLI resolves a different calibration cell after E1 (#888's shape) | `routingDomain(%+v) = %q, was %q before E1: history under the old cell is orphaned` |
| **G6** `TestTaskClass_CannotCollideWithAFileDomain` (property: every `DomainForFile` output is refused by `TaskClass`) | a serve class and a coding cell share a key | `TaskClass accepted %q, which DomainForFile also produces` |
| **G7** `TestDispatch_TemperatureReachesTheWire` (`httptest` head decoding the body) | temperature is dropped anywhere between `chatRequest` and the provider body | `head received temperature %v, client sent %v` |
| **G7b** `TestServeRouter_MaxCostOnlyLowersTheCeiling` (recording seam; #1208 adds the no-policy-ceiling row, #1209 adds policy below and above the header) | a client raises or lifts the operator's cost ceiling, or the header never reaches `Options.MaxCostUSD` | `policy ceiling %v, header %v gave MaxCostUSD %v, want %v` |

### 5.3 #1206-A `internal/rank/constraint_test.go`, `internal/dispatch/objective_test.go`, `internal/dispatch/explore_test.go`

**Table-driven tests:**

- **`TestStrongest_Orders`.** Rows:
  - measured above declared;
  - equal Effective broken by price;
  - an unknown price sorted last;
  - equal everything broken by ID;
  - nil scores fall back to declared.
- **`TestParseObjective`.** Accepted: `""`, `quality`, `QUALITY`, `cost`. Refused: `balanced` and `fast`.
- **`TestScoreMeasured_MatchesCheapestGate`.**

**Race test.** `TestPick_SeededRNGIsSafeUnderConcurrentDispatch` runs 32 goroutines × 100 `pick` calls on one seeded `Dispatcher`.

| Guard | Breaks | Prints |
|---|---|---|
| **G8** `TestStrongest_IsATotalOrder` (100 seeded shuffles of 8 heads with ties) | quality ordering depends on input order, so two identical runs can route differently | `shuffle %d ordered %v, shuffle 0 ordered %v` |
| **G9** `TestDispatch_QualityPutsTheMeasuredBestFirst` (two `httptest` heads, a calibrator with 25 in-domain commitments) | `quality` does not reach the attempt loop, or reads the wrong cell | `objective quality ran %s first; measured best in %s is %s (effective %d vs %d)` |
| **G10** `TestDispatch_NoObjectiveIsTodaysConstraint` | `hyctl dispatch` (no objective) changes order after E1 | `objective "" ordered %v, constrain alone orders %v` |
| **G11** `TestPick_SameSeedSameDraws` (`explore_rate 0.5`, seed 42, 200 sequential picks twice) | exploration is not reproducible under a seed | `seed 42 run 2 diverged at pick %d: %s vs %s` |
| **G12** `TestRouteToDispatch_HydraIsUnpinnedUnlessEnumSet` | `model:"hydra"` silently pins again, or an explicit `--enum` stops pinning | `model %q with --enum changed=%v gave tier=%q enum=%q` |
| **G13** `TestPPassBasis_MeasuredOnlyAtMinCommitments` (19 → `catalogue_prior`, 20 → `measured`) | the basis header calls a prior a measurement | `%d in-domain observations reported basis %q` |

### 5.4 #1209-A `internal/oracle/match_test.go`, `internal/decide/cascade_test.go`, `internal/dispatch/escalate_test.go`, `internal/config`

**Table-driven tests:**

`match_test.go` (`TestNormaliseLabel`, `TestNormaliseLabel_Idempotent`, `TestMatchOracle_Verify`, `TestNewMatch_RefusesBadConfig`, G19 and `ExampleNormaliseLabel`) shipped with `match.go` in PR 0 (#1328); the rest lands in PR 4.

- **`TestNormaliseLabel`.** Rows include:
  - `"Card Arrival."`, `` `card_arrival` ``, `"Card_Arrival"`, curly quotes, `**Card Arrival**`, `__card_arrival__`, `• card_arrival`, Unicode whitespace and zero-width runes → `card_arrival`;
  - `card-arrival` → `card-arrival` (a `-` is never folded to `_`), `card‑arrival` (U+2011) → `card-arrival`, `−1` (U+2212) → `-1`;
  - `- card_arrival` → `-_card_arrival`, `A-` → `a-`, `-1.` → `-1`, `- 3` → `-_3` (a leading `-` is a sign, not a bullet);
  - `*` → `*`, `A*` → `a*`, `* card_arrival` → `*_card_arrival` (only a matched `*` pair strips);
  - `x..` and `x. .` → `x`; empty and `` "". `` → empty.
- **`TestNormaliseLabel_Idempotent`.** Normalising any row's output again changes nothing.
- **`TestMatchOracle_Verify`.** Rows:
  - label hit and miss, including a sign set `{-1, 0, 1}` and a grade set `{A, A-, B+}`;
  - pattern hit and miss, anchored (`maybe 42 or 7` fails `\d+`);
  - empty answer → fail, not an error;
  - a failing Detail always quotes the head of the answer.
- **`TestNewMatch_RefusesBadConfig`.** Both of labels and pattern, neither, an empty label set, a label that normalises to empty, two labels that normalise to the same key, a bad regex, a pattern that balances only inside the anchors (`a)|(.*`), and a pattern that fails only once wrapped (nesting depth).
- **`TestServeVerifiers_Validate`.** An unknown kind, bad grammar, an unreadable file, and a duplicate.
- **`TestCascade_Judge`.** Rows (P̂, verifier measured?, verdict, target, spent, ceiling, max) → (next, decision):
  - accept on the first;
  - escalate on a fail;
  - no_evidence when unmeasured;
  - budget_exhausted at the ceiling;
  - budget_exhausted at the max count;
  - an oracle error contributes 0.
- **`TestCascade_OutcomeServesTheRightStep`.** Accepted → last, no_evidence → first, exhausted → max confidence (tie → earliest).
- **`TestCallCost`.** Model priced, tier fallback, nil DB → 0.

**Race test.** `TestCascade_ConcurrentRequestsShareNothing` runs 16 requests through one `serveRouter` with verifiers and a shared `Calibrator` under `-race`.

| Guard | Breaks | Prints |
|---|---|---|
| **G14** `TestOneCostValue_RowRunlogAndResultAgree` (`httptest` head returning usage, pricing with a model entry) | a second cost derivation creeps back in (the CLAUDE.md "no duplicate logic" rule; #1242 must be one edit) | `Result.CostUSD=%v, cost.jsonl est_cost_usd=%v, runlog CostUSD=%v: three numbers for one call` |
| **G15** `TestEscalate_EscalatedAnswerIsCostedNotCached` (cache on, hook escalates the first) | a rejected answer is cached or a paid call vanishes from `cost.jsonl` | `escalated answer from %s: cost rows=%d (want 1), cached=%v (want false)` |
| **G16** `TestEscalate_ExhaustedReturnsLastAnswerNotError` | all-rejected turns into a 502 with the answer discarded | `every candidate answered and was escalated; Dispatch returned err=%v, want the last answer` |
| **G17** `TestCascade_UncalibratedVerifierCannotEscalate` | an unmeasured verifier spends on further heads (decide's no-evidence rule lost) | `no measured verifier, yet the cascade asked %d sources` |
| **G18** `TestCascade_SameRuleAsDecideRouter` (one table fed to both `decide.Router` and `decide.Cascade`) | the two endpoints come to stop on different rules | `Router decided %s, Cascade decided %s for the same evidence` |
| **G19** `TestMatchOracle_VerifyAgreesWithNormaliseLabel` (shipped in #1328 under this name; for each of a list of answers, `Verify` passes exactly when `NormaliseLabel(answer)` is in the normalised set, so it is neither more nor less lenient; `ExampleNormaliseLabel` documents the same contract) | a bench-only normaliser diverges from the served verifier | `MatchOracle accepted %q but NormaliseLabel(%q)=%q is not in the set` (and `MatchOracle refused %q but …`) |

### 5.5 #1210-A `internal/serve/contract_test.go`, `internal/serve/decisions_test.go`, `internal/decide/cascade_test.go`

**Table-driven tests:**

- **`TestWriteEvidence_Formats`.** Precision, comma join, Objective omitted on a pinned route, and nil → no headers.
- **`TestDecide_OutcomeDecision`.** One row per `Router.Decide` exit (§1.4 table).
- **`TestChatOutcome_LedgerEndsOnTheServedAnswer`.** Then `trust.ApplyRunOutcome` on it records the expected (source, saidCorrect) pairs.

| Guard | Breaks | Prints |
|---|---|---|
| **G20** `TestChat_EveryContractHeaderIsPresent` (fake Router with full Evidence; asserts all nine `X-Hydra-*` names plus `X-Request-Id`) | a header from RFC §5.4 / scope #1210 goes missing | `response lacks %s; the E7 harness reads it` |
| **G21** `TestChat_NoEvidenceKeepsTodaysBody` (golden of today's body) | a router reporting nothing changes the OpenAI body | `body without evidence differs from pre-E1 golden:\n%s` |
| **G22** `TestDecisions_OldAndNewHeaderNamesAgree` | the one-release alias drifts from the new name | `%s=%q but legacy %s=%q` |
| **G23** `TestServeRouter_OneTrustRunPerRequest` (asserts `LoadRuns` grows by exactly 1 per request, `SpanID == SpanIDFor(taskID)`, the last ledger `Candidate` == the served text) | `/v1/feedback` (B) would have no ledger, or the wrong final answer, to replay | `request %s wrote %d trust runs (want 1); last ledger candidate %q, served %q` |

### 5.6 Integration: the serve path end to end, no network (`cmd/hydra/serve_cascade_test.go`)

**`TestServe_ClassificationCascadeEndToEnd`.**

**Setup:**

- `testutil.NewSandbox`.
- `HYDRA_LMSTUDIO_HOST` points at one `httptest` server. Its `/v1/models` lists `first` and `second`. Its `/v1/chat/completions` answers by model:
  - `first` → `"not a label"`;
  - `second` → `"Card Arrival"`.

  It records each body's `temperature`.
- `config.toml`:
  - `explore_rate = 0`;
  - `[[serve.verifiers]]` `match` over a 3-label file in `t.TempDir()`.
- Calibration seeded through `trust.Calibrator.Update`:
  - both heads at 25 in-domain commitments, `first` with the higher rate (P̂ ≈ 0.9 vs 0.8), so `quality` tries it first;
  - `verifier:match:t` with both verdict directions, se=sp=0.95.

Then `serve.Handler(serveRouter{…})` runs behind `httptest.NewServer`, and a POST is sent with `model:"hydra"`, `X-Hydra-Task-Class: classification.test`, `temperature: 0`.

**Asserts:**

- 200, and `choices[0].message.content == "Card Arrival"` from `model: second`.
- `X-Hydra-Decision: accept`, `X-Hydra-Escalations: 1`, and `X-Hydra-Sources` lists `first`, the verifier, `second`, the verifier.
- `X-Hydra-P-Pass-Basis: measured`, `X-Hydra-Objective: quality`, `X-Hydra-Task-Class` echoed.
- `X-Hydra-Cost-USD` equals the sum of the two `cost.jsonl` rows.
- The stub saw `temperature: 0` twice.
- `trust.jsonl` gained one run with `Decision: accept`.
- There is no `cache` entry.

**Sub-tests (same fixture):**

- **`hydra/standard`** pins and emits no `Objective`.
- **An unknown objective** gives 400 carrying `X-Request-Id`.
- **The verifier uncalibrated** gives `no_evidence` and one source, with only `first` called.
- **`--max-escalations 0`** gives `budget_exhausted`, serving `first` with its confidence.

Local heads price at $0, so a second fixture uses an `openai`-shaped stub head with `pricing` holding a model entry to assert non-zero cost. That is unit-level in **G14**, not here.

### 5.7 #1325 `internal/dispatch/restrict_test.go`, `cmd/hydra/serve_cli_test.go`

*As shipped in #1329.* The tests are in `internal/dispatch/restrict_test.go`, `cmd/hydra/serve_cli_test.go` and `cmd/hydra/serve_decisions_test.go`.

- **`TestRestrictHeads`.** Rows: no list and a blank-only list keep the inventory; a known subset keeps discovery order; whitespace around an id is trimmed; an embedding head may be listed; a local head under `--local`; a hosted-only list under the `pii` local-only policy is accepted (it binds only PII prompts). Refused, naming the id or the reason: an unknown id, a duplicate, an unroutable head, a list of only an embedding head, and no local head under `--local`. A refused list leaves the inventory unnarrowed.
- **`TestRestrictHeads_KeepsTheEmbedder`** and **`TestRestrictHeads_KeepsAnUntaggedEmbedModel`.** Embedding lookups still resolve against the full discovery.
- **`TestPinHead_OutsideTheAllowlistSaysSo`.** The pin error names the allowlist.
- **`TestServeHeads_AnUnknownIdStopsStartup`** (flag and `[serve] heads` both) and **`TestServeHeads_AnEmptyFlagClearsTheConfigList`.**
- **`TestNewDecisionRouter_TheAllowlistDropsUnlistedSources`** and **`TestPrintServeBanner_WarnsWhenTheAllowlistDropsDecisionSources`.**

| Guard | Breaks | Prints |
|---|---|---|
| **G24** `TestServeHeads_NoHeadOutsideTheList` (`--heads` names two heads that both answer 503; the excluded head is live and also has a cache entry seeded for the same prompt) | the router chooses or falls back to a head the operator excluded, `/v1/models` advertises it, or the cache serves its answer | `--heads %v, yet %s was %s` (`advertised by /v1/models`, `what answered model …`, `sent a request …`) |

## 6. PR plan

Every PR targets `develop` and squash-merges only on all-`SUCCESS` CI. **Only the final PR of a ticket says `Closes #n`; an earlier PR of the same ticket says `Part of #n`** (`close-shipped-issues.yml` closes every `Closes #n` at release, so an early `Closes` would close an unfinished ticket). #1209 has two PRs (0 and 4), and #1207's PR 1 covers checkpoint A only (its remainder is #1326); every other ticket has one. In this order:

| # | Branch | Title | Body | Depends on | Touches |
|---|---|---|---|---|---|
| 0 | `feature/1209-match-verifier` | `feat(oracle): a match verifier for label answers` (opened as #1328) | Part of #1209. `MatchOracle`, `NewMatch`, `NormaliseLabel` and their tests only; wires nothing into dispatch or config. Lands first so E7 imports it (E7 Q1). No new dependency. | — | `internal/oracle/match.go` (new), `internal/oracle/match_test.go` (new), `internal/oracle/oracle.go` (one rune-safe `truncate`/`clip`, also used by `firstLine`) |
| 1 | `feature/1207-provider-neutral-quota` | `refactor(budget,dispatch): a provider-neutral Quota replaces claude_pct in routing` (opened as #1330) | Part of #1207 (checkpoint A only). The remaining #1207 work (history reset after `/compact`, level timestamps and expiry, cross-process `state.json` writes, history units, reading the `quotas` key) is tracked in #1326 (E2); whichever PR finishes #1207 carries the `Closes`. | — | `internal/budget/quota.go` (new), `internal/dispatch/dispatch.go` (535-547, 984-1082, 1627-1634), `cmd/hydra/main.go` (701-732, 796-803), `CLAUDE.md` (the budget row), tests and `internal/dispatch/testdata/state_pre_e1_*.json` |
| 2 | `feature/1208-task-class-resolver` | `feat(serve): task class by header, metadata or route default, and temperature and max-cost passthrough` | Closes #1208 | — (parallel with 1) | `internal/trust/domain.go`, `internal/serve/serve.go` (`chatRequest`, `Request`, `chat`, `request`), `internal/serve/contract.go` (new, task-class and max-cost headers only), `cmd/hydra/serve.go` (`serveRouter`, `Chat`, `--task-class`), `internal/dispatch/dispatch.go` (`Options`, `:880` literal) |
| 3 | `feature/1206-routing-objective` | `feat(serve)!: route model "hydra" by objective, quality by default` (`BREAKING CHANGE:` footer naming `hydra/standard` and `--enum`) | Closes #1206 | 2 | `internal/rank/constraint.go`, `internal/dispatch/dispatch.go` (`Options`, 677-697, success `Result`), `internal/dispatch/explore.go`, `internal/config/config.go` (`explore_seed`), `internal/serve/serve.go`, `cmd/hydra/serve.go` (`routeToDispatch`, flags, banner) |
| 4 | `feature/1209-match-verifier-escalation` | `feat(dispatch): verifier-gated escalation on the serve path, one cost value` | Closes #1209 (notes the `est_cost_usd` model-price fix) | 0, 3 | `internal/config/config.go` (`Serve`), `internal/decide/cascade.go` (new), `internal/dispatch/dispatch.go` (`CallCost`, `headPrice`, `Result.CostUSD`, `Options.Escalate`, 930-981, `logDispatch` 1676), `internal/dispatch/cache.go` caller, `cmd/hydra/vet.go:170`, `cmd/hydra/workflow.go:117`, `cmd/hydra/serve.go` (incl. the max-cost/policy minimum), `cmd/hydra/serve_decisions.go` (shared calibrator) |
| 5 | `feature/1210-response-contract` | `feat(serve): evidence headers, hydra body metadata and a trust run per request` | Closes #1210 | 4 | `internal/serve/contract.go`, `internal/serve/serve.go` (`Answer`, `chat`), `internal/serve/decisions.go` (16-23, 70, 81-82), `internal/decide/decide.go` (`Outcome.Decision`), `internal/decide/cascade.go` (`Ledger`), `cmd/hydra/serve.go`, and a filed follow-up issue for removing the legacy decision headers |
| 6 | `feature/1325-serve-head-allowlist` | `feat(serve): a head allowlist so hyctl serve routes among named heads only` (opened as #1329) | Closes #1325 | — (independent) | `internal/dispatch/dispatch.go` (`RestrictHeads`, `Allows`, `ServeHeads`, `embedHeads`, the pin check), `internal/dispatch/cache.go` (excluded-head hits are misses; embedder from the full discovery), `internal/dispatch/capture_recall.go`, `internal/config/config.go` (`Serve.Heads`), `cmd/hydra/serve.go` (`--heads`, startup, banner), `cmd/hydra/serve_decisions.go` (decision sources filtered) |

**Why this order.**

- PRs 2→5 all edit `cmd/hydra/serve.go` and `internal/serve/serve.go`, so they are strictly sequential to keep conflicts out of review.
- PR 0 is pure `internal/oracle` and merges first, so E7's PR 2 (suites and grader) can import `NormaliseLabel` without waiting for PR 4.
- PR 1 touches disjoint regions of `dispatch.go` and can merge at any point.
- PR 6 (#1325) depends on nothing. It touches `cmdServe`'s flag block, `printServeBanner`'s signature, the `Serve` config struct and `dispatch/cache.go`, so its conflicts with PRs 2–5 are textual; whichever lands second rebases. PR 4 adds `Verifiers` to the `Serve` struct PR 6 created.
- PR 3 needs PR 2's `Domain` on the serve path, or `quality` would rank on the pooled order.
- PR 4 needs PR 3's order to escalate along.
- PR 5 needs PR 4's outcome to report.
- `CHANGELOG.md` is release-please's, so no PR edits it. PR 3's breaking footer is what produces the entry.
- CLAUDE.md's `internal/serve` row (stale per scope C20) is corrected in PR 5, the PR that changes what serve returns.

**Conflict notes with #1242 (E3.P1) and the parallel tracks:**

- **#1211 (P1, content parts) and #1242.** `chatRequest.Messages` decoding (P1) sits beside PR 2's and PR 5's new fields. The fields are additive and the conflict is textual. Whichever lands second rebases. The scope's advice stands: do P1's `chatRequest` change before PR 5 if possible.
- **#1242 and PR 4.** #1242 adds cached-token fields to `executor.Response` and prices them. After PR 4, that is **one edit**: `CallCost` gains the cached counts (or takes `*executor.Response`). `X-Hydra-Cost-USD`, `cost.jsonl` and the runlog follow, and G14 fails if #1242 prices anywhere else.

  #1242 should branch after PR 4 merges, or rebase onto it; the `logDispatch` region (`:1673+`) is touched by both. `internal/swarm`'s tier pricing (`cost.go:41,70`, `sprt.go:93`) is a second cost derivation that E1 does not touch. #1242 must either route it through `CallCost` or fix cached pricing there as well. Until #1242 lands, `X-Hydra-Cost-USD` overstates cost on cached calls. Banking77 prompts are uncached, so A is unaffected, and the A results note must say so (scope §6.2).
- **#1212 (`/v1/messages`).** It adds a route to `serve.Handler`. Evidence for it reuses `writeEvidence`, with no second vocabulary.
- **E7.** The harness's `hydra-*` arms are refused with a named dependency until PRs 2–5 and PR 6 (#1325) merge (E7 §3.4). The direct arms, the stats and the smoke tier's stage 1 can land first. E7 imports `oracle.NormaliseLabel` from PR 0 and never copies it. E7 does not call `dispatch.CallCost`: its measured cost is its own frozen snapshot, and `X-Hydra-Cost-USD` is the cross-check (§3, E7 Q8).

## 7. Risks and open questions

### Risks

| # | Risk | Effect | Mitigation |
|---|---|---|---|
| R1 | **`match` checks that the answer is a valid label, not the right one.** On intent suites most wrong answers are still valid labels, so P(pass \| wrong) ≈ 1, sp is low, and LLR(pass) ≈ 0 once calibrated. | The posterior stays ≈ P̂. If every head's P̂ is under the target, every request escalates to `--max-escalations`, which can triple the cost arm's spend and fail T4 for a reason that is not routing. **So at checkpoint A any `quality` gain comes from per-class measured competence on the calibration split, not from verification.** | (1) E7's run config fixes `--confidence` and `--max-escalations` **per served arm** before any run, and `run.json` freezes them. (2) The smoke tier reports each served arm's escalation rate; if any exceeds **20%**, the full run is blocked pending review (E7 §6.4). (3) **Hypothesis caveat for H1 and H3**, written into #1213 and into every A `summary.json`: "`match` checks label validity, not correctness; a pass or fail of H1/H3 at A measures routing on calibrated per-class competence, not verifier-gated escalation." H6 reports the verifier's measured D (scope §4). The design does not special-case it: a weak verifier earning little evidence is the calibration working (RFC §5.2.3). |
| R2 | **Quota emergency still forces `LocalOnly` on serve** (scope C12). | A stale `claude_pct ≥ 80` on a dev machine turns a run local-only. | E7 runs in a fresh `HYDRA_HOME` (E7 §1.5). The log line keeps develop's wording (`Claude at N%`), since `session:claude-code` is the only scope at A (#1330). G3 keeps the behaviour deliberate. Scoping session quotas to their own session is E5's work; level expiry is #1326. |
| R3 | **Only heads that name a model (`Meta["model"]`: OpenRouter allowlist, LiteLLM, llama.cpp) are priced per model.** Bedrock, OpenAI, Anthropic and other key-derived heads are one head per key, priced by tier (`env/provider.go:101`; no `Meta["model"]`). | `X-Hydra-Cost-USD` for those heads is a tier price, and a key-derived provider offers the router one model, not several. | **Bedrock yields one router head**, `env/bedrock`, whose model is `BEDROCK_MODEL_ID`. The full tier's multi-model Hydra arms therefore require the OpenRouter allowlist (`[openrouter] models`). That is a **run prerequisite, not a code change**: no `[bedrock] models` allowlist is planned in E1. Smoke stage 1 (direct Bedrock calls only) can run once AWS credentials work; on 2026-11-05 they fail with `UnrecognizedClientException` (E7 R2). E7 records each call's price row (`price_ref`), and the cost cross-check skips tier-priced `env/bedrock` rows (§3). |
| R4 | CLI and agy heads ignore `Temperature`. | A served answer from one is unpinned. | They are excluded from every E7 arm (E7 scope §5.2), enforced by `hyctl serve --heads` (#1325, §1.6). A refusal rule like the tools one (`dispatch.go:762-768`) would be a behaviour change for every CLI user with no A consumer. |
| R5 | **The breaker parks a head after a 429/5xx** (`health`, #1111). | Later items route differently within one run. | The harness records `X-Hydra-Sources` per item. E7's determinism gate (identical rerun, \|Δpass\| ≤ 0.5 pt) catches a material drift. |
| R6 | A seeded RNG under concurrent requests. | The draw-to-request assignment depends on scheduling. | Determinism is claimed for sequential requests only (G11). E7 runs items sequentially, interleaving arms per item (E7 §7). `explore_rate = 0` needs no seed at all. |
| R7 | **A catalogue score read as a probability prior** (scope C7). | `P-Correct` for a `catalogue_prior` step is not a measured probability. On an unmeasured machine, `quality` is always-strongest. | `X-Hydra-P-Pass-Basis` labels it. T2's non-vacuity condition refuses a vacuous "held". A test fails if the basis ever says `measured` below 20 (G13). |
| R8 | `trust.jsonl` gains one line per served request, and `LoadRuns` reads the whole file. | A long-lived server makes `hyctl trust stats/explain` slower over time. | It is negligible at checkpoint A's ~4k requests. Retention or sealing belongs with B's `/v1/feedback`, which is the reader that needs it. |
| R9 | **Calibration is loaded once when `hyctl serve` starts** (`dispatch.go:482`, `serve_decisions.go:44`). | `hyctl trust record` while serving is not seen until restart. | The harness fills calibration, then starts serve (§3). B's `/v1/feedback` writes through the server's own `Calibrator`. |
| R10 | `dispatch.go` is the hotspot for PRs 1, 3 and 4 and for #1242. | Textual conflicts. | The regions are disjoint (§6). PR 1 can merge at any point. #1242 rebases after PR 4. |

### Open questions that block implementation

None. Each scope question is answered by the 2026-11-05 Decisions or by this design. Three choices were made here, and the user can veto any of them without reopening the design:

- `usage` on an escalated request is summed over all steps, matching `X-Hydra-Cost-USD`, not only the served step's tokens.
- `X-Hydra-Objective` is omitted, not set to a sentinel, on a pinned route.
- `est_cost_usd` moves to the model price for heads that name a model (§4). This is a `fix` that changes `hyctl cost` totals for those heads.

Run prerequisites for #1213, none of them code: working AWS credentials (R3); an OpenRouter key and allowlist for the full tier (R3); the 20% smoke escalation bound (R1); and #1213 recording the harness cost definition and the H1/H3 caveat before results exist (§3, R1).

## Reconciliation (2026-11-05)

Cross-checked against `0001-design-E7.md`. Changes to this document:

1. **#1208 gains max-cost passthrough** beside temperature (E7 Q2): request header `X-Hydra-Max-Cost-USD` → `serve.Request.MaxCostUSD` → `Options.MaxCostUSD`, and from #1209 the cascade's `Target.MaxCostUSD`. A client can only lower the policy ceiling (§1.2, §1.4, §1.5 constants, §4, test `TestChat_MaxCostHeader`, guard G7b). There is no `X-Hydra-Temperature` echo header; G7 is the proof.
2. **New ticket #1325, `hyctl serve --heads`** (E7 Q3): §1.6 design (`Dispatcher.RestrictHeads`), §5.7 tests (guard G24), PR 6 in §6, independent of the other PRs.
3. **`internal/oracle/match.go` lands first** as PR 0, `feature/1209-match-verifier`, "Part of #1209" (E7 Q1). PR 4 no longer creates it and now depends on PR 0 as well as PR 3; its title changed to `feat(dispatch): …` since the oracle is no longer in it.
4. **`Closes` policy** (E7 Q7): only the final PR of a ticket says `Closes #n`; earlier ones say `Part of #n`. §6 now has a Body column.
5. **Cost definition** (E7 Q8): the measured cost for #1213 is the harness's tokens × frozen snapshot; `X-Hydra-Cost-USD` is a cross-check within max($0.000001, 1%) per request, skipping tier-priced `env/bedrock` rows. E7 no longer imports `dispatch.CallCost` (§3, §6). This supersedes scope §4's cost line for #1213.
6. **Weak `match` evidence** (R1): per-arm `--confidence`/`--max-escalations` fixed before any run, a 20% smoke escalation bound that blocks the full run pending review, and an explicit H1/H3 caveat (§1.4 cmd glue, §3, R1).
7. **Bedrock** (R3): one router head from `BEDROCK_MODEL_ID`; the full tier needs the OpenRouter allowlist; smoke stage 1 needs working AWS credentials. Recorded as run prerequisites, not code.
8. **§3 harness table**: added rows for the head allowlist, cache off (E1 sets `NoCache` only with `Escalate`, so E7 writes `cache_answers = false`), per-arm cascade parameters, and the calibration-reload-on-restart note; removed the `threshold` arm from the direct arms (not in A); format fails come from the full-label-set `MatchOracle`; the verifier's calibration verdict is the full-label-set one; E7's `trust.RecordOutcome` extraction is noted.
9. **Name alignment** now also lists the decision vocabulary (`accept`/`budget_exhausted`/`no_evidence`), and the E7 design is marked as using the E1 names.

## As shipped (2026-11-05)

Three E1 PRs are open against `develop`: #1328 (PR 0), #1330 (PR 1) and #1329 (PR 6). The sections above now describe their code. This list records where that code departs from the first draft of this design, and which PR made each change. The Reconciliation above is left as it was written.

**#1328, `feat(oracle): a match verifier for label answers` (PR 0, Part of #1209):**

1. **`-` is always significant.** The draft folded runs of space **or `-`** to `_`. The shipped `NormaliseLabel` never folds or strips `-`, so `card-arrival` ≠ `card_arrival`, and `A-`, `-1` and `- 3` keep their sign (§1.4). Only whitespace and `_` runs fold to `_`.
2. **`*` is stripped only as a matched pair** around the whole string (`**Card Arrival**` → `card_arrival`); a lone `*` is part of the label (`A*` → `a*`).
3. **`•` (U+2022) is the only bullet** stripped from the edges. A leading `- ` is a sign, not a bullet.
4. **Cf runes are dropped** anywhere in the answer (BOM, zero-width space), in both label and pattern mode.
5. **Unicode dashes become `-`**: U+2010–U+2015 and the minus sign U+2212.
6. **Edge stripping repeats to a fixed point**, over quotes, curly quotes, backticks, `_`, `•`, trailing dots and a `*` pair, so the function is idempotent.
7. **Pattern mode is anchored** (`^(?:p)$`) and only `TrimSpace` plus Cf removal is applied to the answer. `NewMatch` also compiles the raw pattern, so `a)|(.*` cannot escape the anchors, and a wrap that fails to compile is an error, never a panic.
8. **Colliding or empty labels are refused** by `NewMatch`: two labels that normalise to the same key, or a label that normalises to empty (§1.4, and the `[[serve.verifiers]]` startup errors).
9. **G19 was renamed** from `TestMatchOracle_SameNormaliserAsE7Pass` to `TestMatchOracle_VerifyAgreesWithNormaliseLabel`, and `ExampleNormaliseLabel` is a separate example test (§5.4).
10. **`internal/oracle/oracle.go` is also touched**, for one rune-safe truncation policy (`truncate`/`clip`, used by `firstLine` too). `CommandOracle` details can be up to 3 bytes shorter.
11. Known follow-up: `-*1*` (a sign outside an emphasis pair) normalises to `-*1` and does not match `-1`.

**#1330, `refactor(budget,dispatch): a provider-neutral Quota replaces claude_pct in routing` (PR 1, Part of #1207, checkpoint A only):**

1. **The API is `ParseState(raw) State` and `Governing(qs) (Quota, Mode, bool)`**, not `Quotas(state) []Quota` and `Governing(qs) (Quota, bool)`. `State.Budget()` decodes the per-model table on demand for `hyctl status` (§1.1).
2. **The `quotas` key is ignored at checkpoint A.** Reading it, and the `quotas`/alias precedence, moved to #1326 (E2), which adds validation, timestamps and expiry (§1.1, §4). `TestParseState_IgnoresTheQuotasKey` replaces the draft's `quotas` rows.
3. **`claude_pct` is read as an integer only**, in parity with develop's `int` decode: a fraction or a string is unset, and the clamp to 0..100 runs before the int conversion. One non-integer history point makes the history unknown rather than 0 (§1.1).
4. **develop's wording is kept.** `hyctl status` still prints `Claude  :`, and the governor log lines are develop's (`Claude at N%`), not `<scope> at N%`. The one deliberate change: "downgrading tier" is claimed only when the tier moved (§1.1, R2).
5. **`LegacySessionQuota` is not exported**, and `asInt`/`asIntSlice` were deleted rather than moved; `legacyLevel` and `parseHistory` in `quota.go` replace them. `Quota` has no JSON tags, and `Pct()` rounds without clamping (the clamp is on the read).
6. **G2 scans the whole tree** with `go/ast` (string literals and the `claudeMode` identifier, comments ignored), allow-listing `internal/budget`, `internal/tui` and `desktop/api`, rather than walking three directories (§5.1).
7. **The race test** is `TestSyncStateJSON_InProcessSyncsNeverRollTheLevelBack`, which covers in-process writers only; cross-process `state.json` races are #1326 (§5.1).
8. **Body and title.** "Part of #1207", not "Closes #1207", and the title is a `refactor(budget,dispatch)` (§6).

**#1329, `feat(serve): a head allowlist so hyctl serve routes among named heads only` (PR 6, Closes #1325):**

1. **`RestrictHeads(ids, localOnly)`** takes a second argument. Under `--local` the list must keep a routable local head (§1.6).
2. **The `pii` policy is not local-only for all.** Its `local-only` action binds only PII prompts, so a hosted-only list is accepted under it; only `--local` makes `localOnly` true (§1.6).
3. **`[serve] heads` was added** as the config spelling. `--heads` overrides it for one run, and `--heads ""` clears it. The draft said there was no config spelling in A (§1.6).
4. **`/v1/decisions` is restricted.** The draft left it unrestricted. Decision ids are never dispatch heads, so with any list set it has no sources, and the banner says so when that happens (§1.6).
5. **Embedding lookups use the full discovered set** (`embedHeads()`), including an untagged `embed_model` head. The draft made an unlisted embedding head unavailable to them (§1.6).
6. **Cache hits from excluded heads are treated as misses**, and a pinned head off the list gets an error naming the allowlist rather than the generic unknown-head refusal (§1.6).
7. **Also refused:** a list with no routable head (for example only an embedding head); an embedding-only head may itself be listed (§1.6).
8. **Branch** `feature/1325-serve-head-allowlist`, not `feature/1325-serve-heads`, and the PR touches `cache.go`, `capture_recall.go`, `config.go` and `serve_decisions.go` as well (§6).

**#1327 (E7 PR 1, `internal/stats`)** changes no E1 interface. It replaces `budget`'s private `normCDF` with `stats.NormalCDF` in `risk.go`, which #1330 does not touch, so the two do not conflict.
