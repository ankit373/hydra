// SPDX-License-Identifier: MIT

package bench

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ankit373/hydra/internal/sketch"
	"github.com/ankit373/hydra/internal/stats"
	"github.com/ankit373/hydra/internal/util"
)

const (
	summarySchema = "hydra.bench.summary/1"
	// Frontier is the arm every paired statistic is measured against.
	Frontier = "frontier"
	// OracleArm is computed per item from the direct arms, never called.
	OracleArm = "oracle"
	// 10k seeded resamples, and δ = 2 pts, the non-inferiority margin #1213 froze (H1/T2).
	resamples = 10000
	margin    = 0.02
	alpha     = 0.05
	wilsonZ   = 1.959964
)

// contamination is in every summary's caveats and every table's footer.
const contamination = "both suites have been public since 2019–2020 and are likely in pre-training data; " +
	"paired differences are the claim, absolute rates are context"

// Summary is summary.json: a pure function of run.json and items.jsonl. Only
// Execution differs between an interrupted run and an uninterrupted one.
type Summary struct {
	Schema     string     `json:"schema"`
	RunID      string     `json:"run_id"`
	Tier       string     `json:"tier"`
	Breadcrumb Breadcrumb `json:"breadcrumb"`
	// Incomplete: some scheduled (item, arm) pair has no scored row yet. Counts are
	// reported; paired statistics and smoke measurements are not.
	Incomplete bool                   `json:"incomplete"`
	Unscored   int                    `json:"unscored_pairs"`
	Arms       map[string]ArmSummary  `json:"arms"`
	Paired     map[string]PairedStats `json:"paired"` // "<arm>-vs-frontier"
	Smoke      *SmokeMeasurements     `json:"smoke,omitempty"`
	Caveats    []string               `json:"caveats"`
	Execution  Execution              `json:"execution"`
}

// ArmSummary is one arm over its scored rows. A direct arm accepts every item it
// answered without an error; a transient failure is unscored and asked again.
type ArmSummary struct {
	N               int          `json:"n"`
	Passed          int          `json:"passed"`
	Accepted        int          `json:"accepted"`
	Errors          int          `json:"errors"`
	FormatFails     int          `json:"format_fails"`
	PassRate        float64      `json:"pass_rate"`
	PassWilson      [2]float64   `json:"pass_wilson95"`
	CostUSD         float64      `json:"cost_usd"`
	CostPerAccepted *float64     `json:"cost_per_accepted"` // nil when nothing was accepted
	TokensIn        Moments      `json:"tokens_in"`
	TokensOut       Moments      `json:"tokens_out"`
	LatencyMS       LatencyStats `json:"latency_ms"`
}

// Moments is a mean and a nearest-rank p95.
type Moments struct {
	Mean float64 `json:"mean"`
	P95  float64 `json:"p95"`
}

// LatencyStats skips rows after a sleep gap and rows that errored, and counts them.
type LatencyStats struct {
	Valid   int      `json:"valid"`
	Skipped int      `json:"skipped"`
	P50     *float64 `json:"p50,omitempty"`
	P95     *float64 `json:"p95,omitempty"`
}

// PairedStats compares an arm with the frontier on the items both answered. An
// interval the data cannot support is nil, and Refused says why: never a 0.
type PairedStats struct {
	Discordant     stats.Discordant `json:"discordant"`
	PDisc          float64          `json:"p_disc"`
	DeltaPass      float64          `json:"delta_pass"`
	BootLower95    *float64         `json:"boot_lower95"`
	BootUpper95    *float64         `json:"boot_upper95"`
	TangoZ         *float64         `json:"tango_z"`
	TangoP         *float64         `json:"tango_p"`
	TangoLower95   *float64         `json:"tango_lower95"`
	McNemarP2      float64          `json:"mcnemar_p_two_sided"`
	CostPerAccDiff *Interval        `json:"cost_per_accepted_diff"`
	BootDropped    int              `json:"boot_dropped"`
	Refused        []string         `json:"refused,omitempty"`
}

// Interval is a point estimate over every item with its two-sided 95% bootstrap interval.
type Interval struct {
	Estimate float64 `json:"estimate"`
	Lower    float64 `json:"lower"`
	Upper    float64 `json:"upper"`
}

// SmokeMeasurements is what the smoke tier exists to measure before money is spent on full.
type SmokeMeasurements struct {
	PDisc          map[string]float64 `json:"p_disc"`
	TokensIn       map[string]Moments `json:"tokens_in"`
	TokensOut      map[string]Moments `json:"tokens_out"`
	FormatFailRate map[string]float64 `json:"format_fail_rate"`
	// RecommendedN is max(1000, ⌈6.18·p_disc/δ²⌉) at the largest arm-vs-frontier p_disc,
	// capped at the test split: a number for a person to accept, never applied.
	// Nil, with the reason, when no frontier arm ran.
	RecommendedN        *int   `json:"recommended_n"`
	RecommendedNRefused string `json:"recommended_n_refused,omitempty"`
}

// Execution is how the run went, not what it measured: when it ran, and every dollar
// billed, retried and interrupted calls included. Excluded from the resume identity test.
type Execution struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	WallSeconds float64   `json:"wall_seconds"`
	SpendUSD    float64   `json:"spend_usd"`       // Σ every row's cost_usd
	SpendRows   int       `json:"spend_only_rows"` // unscored rows kept for their spend
	TornBytes   int       `json:"torn_tail_bytes,omitempty"`
}

// Report recomputes summary.json and writes nothing else: items.jsonl is only read,
// and only once the run's lock is free, so a live run is never read mid-append.
func Report(dir, stateDir string) (Summary, error) {
	var run Run
	if err := readJSON(filepath.Join(dir, "run.json"), &run); err != nil {
		return Summary{}, fmt.Errorf("bench: %s: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, run.ID), 0o755); err != nil {
		return Summary{}, err
	}
	lk, err := util.Lock(filepath.Join(stateDir, run.ID, "run.lock"))
	if err != nil {
		return Summary{}, err
	}
	defer lk.Unlock()
	rows, torn, err := parseResults(filepath.Join(dir, "items.jsonl"), run.ID)
	if err != nil {
		return Summary{}, err
	}
	s, err := Summarize(&run, rows)
	s.Execution.TornBytes = torn
	if err != nil {
		return Summary{}, err
	}
	return s, writeJSON(filepath.Join(dir, "summary.json"), &s)
}

// ReadSummary reads a summary.json back, refusing a schema it was not written for.
func ReadSummary(path string) (Summary, error) {
	var s Summary
	if err := readJSON(path, &s); err != nil {
		return Summary{}, fmt.Errorf("bench: %s: %w", path, err)
	}
	if s.Schema != summarySchema {
		return Summary{}, fmt.Errorf("bench: %s has schema %q, want %s", path, s.Schema, summarySchema)
	}
	return s, nil
}

// Summarize scores each (item, arm) by its last scored row; a retryable row is spend
// only. Rows are ordered by (rank, arm) before any statistic, so the output is
// deterministic, and a run missing any scored pair is incomplete: counts, no pairs.
func Summarize(run *Run, rows []Result) (Summary, error) {
	order := map[string]int{}
	for i, id := range run.ItemIDs {
		order[id] = i
	}
	scored := map[pairKey]Result{}
	pairSpend, armSpend := map[pairKey]float64{}, map[string]float64{}
	var e Execution
	for _, r := range rows {
		if _, ok := order[r.ItemID]; !ok {
			return Summary{}, fmt.Errorf("bench: a row names %s, which run %s did not schedule", r.ItemID, run.ID)
		}
		e.SpendUSD += r.CostUSD
		pairSpend[pairKey{r.ItemID, r.Arm}] += r.CostUSD
		armSpend[r.Arm] += r.CostUSD
		if r.Retryable {
			e.SpendRows++
		} else {
			scored[pairKey{r.ItemID, r.Arm}] = r
		}
		if e.Start.IsZero() || r.Started.Before(e.Start) {
			e.Start = r.Started
		}
		if end := r.Started.Add(r.Latency); end.After(e.End) {
			e.End = end
		}
	}
	e.WallSeconds = e.End.Sub(e.Start).Seconds()
	// A scored pair costs everything billed for it, retries and earlier attempts included.
	for k, r := range scored {
		r.CostUSD = pairSpend[k]
		scored[k] = r
	}
	s := Summary{Schema: summarySchema, RunID: run.ID, Tier: run.Tier, Breadcrumb: run.Crumb,
		Arms: map[string]ArmSummary{}, Paired: map[string]PairedStats{}, Caveats: []string{contamination}, Execution: e}
	for _, id := range run.ItemIDs {
		for _, arm := range run.ArmOrder {
			if _, ok := scored[pairKey{id, arm}]; !ok {
				s.Unscored++
			}
		}
	}
	s.Incomplete = s.Unscored > 0
	byArm := map[string][]Result{}
	for _, r := range scored {
		byArm[r.Arm] = append(byArm[r.Arm], r)
	}
	if len(byArm) > 1 && !s.Incomplete {
		byArm[OracleArm] = oracleRows(byArm)
		for _, r := range byArm[OracleArm] {
			armSpend[OracleArm] += r.CostUSD
		}
	}
	for _, rs := range byArm {
		sort.Slice(rs, func(i, j int) bool { return order[rs[i].ItemID] < order[rs[j].ItemID] })
	}
	s.Breadcrumb.ProviderModels = providerModels(byArm)
	for arm, rs := range byArm {
		a, err := summarizeArm(rs, armSpend[arm])
		if err != nil {
			return Summary{}, err
		}
		s.Arms[arm] = a
		if arm != Frontier && len(byArm[Frontier]) > 0 && !s.Incomplete {
			s.Paired[arm+"-vs-"+Frontier] = paired(rs, byArm[Frontier], run.Seed)
		}
	}
	if s.Incomplete {
		s.Caveats = append(s.Caveats, fmt.Sprintf("incomplete: %d scheduled pairs have no scored row; resume the run", s.Unscored))
	} else if run.Tier == "smoke" {
		s.Smoke = smoke(s, run.TestSplitSize)
	}
	return s, nil
}

// oracleRows is the per-item best of the direct arms: it passes if any arm passed, at
// the cheapest passing call's cost. It is ≥ every arm by construction, and a test says so.
func oracleRows(byArm map[string][]Result) []Result {
	arms := make([]string, 0, len(byArm))
	for a := range byArm {
		arms = append(arms, a)
	}
	sort.Strings(arms) // a tie keeps the first arm by name, never a map's order
	best := map[string]*Result{}
	var ids []string
	for _, arm := range arms {
		for _, r := range byArm[arm] {
			o := best[r.ItemID]
			if o == nil {
				ids = append(ids, r.ItemID)
				c := r
				c.Arm, c.Calls = OracleArm, nil
				best[r.ItemID] = &c
				continue
			}
			if (r.Passed && !o.Passed) || (r.Passed == o.Passed && r.CostUSD < o.CostUSD) {
				*o = r
				o.Arm, o.Calls = OracleArm, nil
			}
		}
	}
	out := make([]Result, 0, len(ids))
	for _, id := range ids {
		out = append(out, *best[id])
	}
	return out
}

// summarizeArm scores rs, an arm's scored rows; spend is everything the arm was billed,
// unscored and spend rows included, so a flaky arm's cost per accepted answer shows it.
func summarizeArm(rs []Result, spend float64) (ArmSummary, error) {
	a := ArmSummary{N: len(rs), CostUSD: spend}
	var in, out []float64
	lat := sketch.New(sketch.DefaultAlpha)
	for _, r := range rs {
		if r.Passed {
			a.Passed++
		}
		if r.Format {
			a.FormatFails++
		}
		if r.ErrKind != "" {
			a.Errors++
		} else {
			a.Accepted++
		}
		var ti, to int
		for _, c := range r.Calls {
			ti, to = ti+c.InputTokens, to+c.OutputTokens
		}
		in, out = append(in, float64(ti)), append(out, float64(to))
		if r.LatencyInvalid || r.ErrKind != "" {
			a.LatencyMS.Skipped++
			continue
		}
		a.LatencyMS.Valid++
		lat.Add(float64(r.Latency) / float64(time.Millisecond))
	}
	if a.N > 0 {
		a.PassRate = float64(a.Passed) / float64(a.N)
	}
	lo, hi, err := stats.Wilson(a.Passed, a.N, wilsonZ)
	if err != nil {
		return a, err
	}
	a.PassWilson = [2]float64{lo, hi}
	if a.Accepted > 0 {
		c := a.CostUSD / float64(a.Accepted)
		a.CostPerAccepted = &c
	}
	a.TokensIn, a.TokensOut = moments(in), moments(out)
	if a.LatencyMS.Valid > 0 {
		p50, p95 := lat.Quantile(0.5), lat.Quantile(0.95)
		a.LatencyMS.P50, a.LatencyMS.P95 = &p50, &p95
	}
	return a, nil
}

func moments(v []float64) Moments {
	if len(v) == 0 {
		return Moments{}
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	var sum float64
	for _, x := range s {
		sum += x
	}
	return Moments{Mean: sum / float64(len(s)), P95: s[int(math.Ceil(0.95*float64(len(s))))-1]}
}

// paired aligns an arm with the frontier on the items both answered. Every interval
// comes from internal/stats; one the data cannot support is refused, not zeroed.
func paired(arm, ref []Result, seed uint64) PairedStats {
	byID := map[string]Result{}
	for _, r := range ref {
		byID[r.ItemID] = r
	}
	var pa, pr, aa, ar []bool
	var ca, cr []float64
	for _, r := range arm {
		f, ok := byID[r.ItemID]
		if !ok {
			continue
		}
		pa, pr = append(pa, r.Passed), append(pr, f.Passed)
		aa, ar = append(aa, r.ErrKind == ""), append(ar, f.ErrKind == "")
		ca, cr = append(ca, r.CostUSD), append(cr, f.CostUSD)
	}
	var ps PairedStats
	d, diff, _ := stats.PairedDiscordant(pa, pr) // equal lengths by construction
	ps.Discordant = d
	if d.N == 0 {
		ps.Refused = append(ps.Refused, "no item was answered by both arms")
		return ps
	}
	ps.PDisc = float64(d.B+d.C) / float64(d.N)
	ps.DeltaPass, _ = d.Delta()
	if b, err := stats.Resample(stats.PairedMeanDiff(diff), resamples, seed); err == nil && len(b.Values) > 0 {
		lo, hi := b.Quantile(alpha), b.Quantile(1-alpha)
		ps.BootLower95, ps.BootUpper95 = &lo, &hi
		ps.BootDropped += b.Dropped
	} else {
		ps.Refused = append(ps.Refused, "Δpass bootstrap: every resample was undefined")
	}
	if z, p, err := stats.TangoNI(d, margin); err == nil {
		ps.TangoZ, ps.TangoP = &z, &p
	} else {
		ps.Refused = append(ps.Refused, "Tango: "+err.Error())
	}
	if lb, err := stats.TangoLowerBound(d, alpha); err == nil {
		ps.TangoLower95 = &lb
	} else {
		ps.Refused = append(ps.Refused, "Tango bound: "+err.Error())
	}
	_, ps.McNemarP2, _ = stats.McNemarExact(d) // valid counts by construction
	st, _ := stats.CostPerAcceptedDiff(ca, cr, aa, ar)
	b, err := stats.Resample(st, resamples, seed)
	if est, ok := st.All(); ok && err == nil && len(b.Values) > 0 {
		ps.CostPerAccDiff = &Interval{Estimate: est, Lower: b.Quantile(alpha / 2), Upper: b.Quantile(1 - alpha/2)}
		ps.BootDropped += b.Dropped
	} else {
		ps.Refused = append(ps.Refused, "cost per accepted: no resample had an accepted answer on both arms")
	}
	return ps
}

func providerModels(byArm map[string][]Result) map[string][]string {
	out := map[string][]string{}
	for arm, rs := range byArm {
		seen := map[string]bool{}
		for _, r := range rs {
			for _, c := range r.Calls {
				if c.Model != "" && !seen[c.Model] {
					seen[c.Model] = true
					out[arm] = append(out[arm], c.Model)
				}
			}
		}
		sort.Strings(out[arm])
	}
	return out
}

// smoke sizes the full tier from what was measured: n ≈ 6.18·p_disc/δ² (one-sided α =
// .05, power .8), never under 1000 and never over the test split.
func smoke(s Summary, testSplit int) *SmokeMeasurements {
	m := &SmokeMeasurements{PDisc: map[string]float64{}, TokensIn: map[string]Moments{},
		TokensOut: map[string]Moments{}, FormatFailRate: map[string]float64{}}
	var worst float64
	measured := false
	for k, p := range s.Paired {
		m.PDisc[k] = p.PDisc
		if k != OracleArm+"-vs-"+Frontier { // the oracle is computed, not a candidate first source
			worst, measured = math.Max(worst, p.PDisc), true
		}
	}
	for arm, a := range s.Arms {
		if arm == OracleArm {
			continue
		}
		m.TokensIn[arm], m.TokensOut[arm] = a.TokensIn, a.TokensOut
		if a.N > 0 {
			m.FormatFailRate[arm] = float64(a.FormatFails) / float64(a.N)
		}
	}
	if !measured {
		m.RecommendedNRefused = "no arm was paired with a frontier arm, so no p_disc was measured"
		return m
	}
	n := min(max(1000, int(math.Ceil(6.18*worst/(margin*margin)))), testSplit)
	m.RecommendedN = &n
	return m
}
