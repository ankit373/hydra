// SPDX-License-Identifier: MIT

// Package replay runs acceptance policies over cached, labelled answers, so a stop
// rule can be judged on many calibrate/test splits without buying an answer twice.
package replay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"

	"github.com/ankit373/hydra/internal/aver"
	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/stats"
)

// Row is one cached candidate: one head's answer to one task, already labelled.
// AnswerKey lets an agreement check compare candidates without their text.
type Row struct {
	Task         string             `json:"task"`
	Kind         string             `json:"kind"`
	Head         string             `json:"head"`
	CostUSD      float64            `json:"cost_usd"`
	Correct      bool               `json:"correct"`
	AnswerKey    string             `json:"answer_key"`
	Family       string             `json:"family,omitempty"` // vendor family, for the sprt co-agreement discount
	Verdicts     map[string]bool    `json:"verdicts"`
	CheckCostUSD map[string]float64 `json:"check_cost_usd"`
}

// Read parses JSONL candidates. A repeated (task, head), a task under two kinds or
// a negative cost is refused, since each would silently change what is counted.
func Read(r io.Reader) ([]Row, error) {
	var rows []Row
	seen := map[[2]string]bool{}
	kinds := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var row Row
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&row); err != nil {
			return nil, fmt.Errorf("replay: line %d: %w", line, err)
		}
		if err := row.check(); err != nil {
			return nil, fmt.Errorf("replay: line %d: %w", line, err)
		}
		key := [2]string{row.Task, row.Head}
		if seen[key] {
			return nil, fmt.Errorf("replay: line %d: task %q has a second answer from %q", line, row.Task, row.Head)
		}
		seen[key] = true
		if k, ok := kinds[row.Task]; ok && k != row.Kind {
			return nil, fmt.Errorf("replay: line %d: task %q is both %q and %q", line, row.Task, k, row.Kind)
		}
		kinds[row.Task] = row.Kind
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("replay: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("replay: no candidates")
	}
	return rows, nil
}

func (r Row) check() error {
	if r.Task == "" || r.Kind == "" || r.Head == "" {
		return errors.New("task, kind and head are required")
	}
	if r.CostUSD < 0 {
		return fmt.Errorf("cost_usd %v is negative", r.CostUSD)
	}
	for c, v := range r.CheckCostUSD {
		if v < 0 {
			return fmt.Errorf("check_cost_usd[%s] %v is negative", c, v)
		}
	}
	return nil
}

// Split is one seeded 50/50 division of the tasks; test gets the odd one out.
type Split struct {
	Seed        uint64
	Calibration []string
	Test        []string
}

// SplitTasks orders tasks by a hash of (seed, task), so a split depends on the seed
// and the task names only, never on file order or a PRNG's version.
func SplitTasks(rows []Row, seed uint64) Split {
	var tasks []string
	seen := map[string]bool{}
	for _, r := range rows {
		if !seen[r.Task] {
			seen[r.Task] = true
			tasks = append(tasks, r.Task)
		}
	}
	salt := "replay/" + strconv.FormatUint(seed, 10)
	rank := make(map[string]uint64, len(tasks))
	for _, t := range tasks {
		rank[t] = bench.Rank(salt, t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if rank[tasks[i]] != rank[tasks[j]] {
			return rank[tasks[i]] < rank[tasks[j]]
		}
		return tasks[i] < tasks[j]
	})
	h := len(tasks) / 2
	return Split{Seed: seed, Calibration: tasks[:h], Test: tasks[h:]}
}

// CheapestFirst is the order heads are tried in: mean calibration cost, then name.
// A head with no calibration answer is left out, since nothing measured it.
func CheapestFirst(cal []Row) []string {
	sum, n := map[string]float64{}, map[string]int{}
	for _, r := range cal {
		sum[r.Head] += r.CostUSD
		n[r.Head]++
	}
	heads := make([]string, 0, len(n))
	for h := range n {
		heads = append(heads, h)
	}
	mean := func(h string) float64 { return sum[h] / float64(n[h]) }
	sort.Slice(heads, func(i, j int) bool {
		if mean(heads[i]) != mean(heads[j]) {
			return mean(heads[i]) < mean(heads[j])
		}
		return heads[i] < heads[j]
	})
	return heads
}

// Result is one (kind, policy, alpha, split). FalseAccept is wrong accepted over all
// test tasks, with a two-sided 95% Clopper–Pearson interval.
type Result struct {
	Kind            string    `json:"kind"`
	Policy          string    `json:"policy"`
	Alpha           float64   `json:"alpha"`
	Seed            uint64    `json:"seed"`
	Tasks           int       `json:"tasks"`
	Accepted        int       `json:"accepted"`
	WrongAccepted   int       `json:"wrong_accepted"`
	FalseAccept     float64   `json:"false_accept_per_task"`
	FalseAcceptLo   float64   `json:"false_accept_lo"`
	FalseAcceptHi   float64   `json:"false_accept_hi"`
	Refusal         float64   `json:"refusal"`
	SpentUSD        float64   `json:"spent_usd"`
	CostPerTask     float64   `json:"cost_per_task_usd"`
	CorrectAccepted int       `json:"correct_accepted"`
	CostPerCorrect  USD       `json:"cost_per_correct_usd"`
	Bars            []float64 `json:"bars,omitempty"`             // per candidate, AVER policies only
	Untestable      int       `json:"untestable_pairs,omitempty"` // aver_gated only
}

// Summary folds one (kind, policy, alpha) over its splits. The interval is the
// first seed's: splits share tasks, so their intervals do not combine.
type Summary struct {
	Kind             string    `json:"kind"`
	Policy           string    `json:"policy"`
	Alpha            float64   `json:"alpha"`
	Splits           int       `json:"splits"`
	MeanFalseAccept  float64   `json:"mean_false_accept_per_task"`
	FirstLo          float64   `json:"first_split_lo"`
	FirstHi          float64   `json:"first_split_hi"`
	WorstFalseAccept float64   `json:"worst_false_accept_per_task"`
	OverAlpha        float64   `json:"share_of_splits_over_alpha"`
	Refusal          float64   `json:"refusal"`
	CostPerTask      float64   `json:"cost_per_task_usd"`
	CostPerCorrect   USD       `json:"cost_per_correct_usd"`
	Bars             []float64 `json:"bars,omitempty"`             // the first split's
	Untestable       int       `json:"untestable_pairs,omitempty"` // the first split's
}

// Report is everything one replay printed, seeds included so it can be repeated.
type Report struct {
	Seeds     []uint64  `json:"seeds"`
	Results   []Result  `json:"results"`
	Summaries []Summary `json:"summaries"`
}

// USD is a cost that may be undefined (NaN); JSON has no NaN, so it is null.
type USD float64

func (u USD) MarshalJSON() ([]byte, error) {
	if math.IsNaN(float64(u)) {
		return []byte("null"), nil
	}
	return json.Marshal(float64(u))
}

// Config is what one replay runs: every named policy at every alpha on every seed.
type Config struct {
	Policies []string
	Alphas   []float64
	Seeds    []uint64
	Check    string  // first_check_pass's primary check
	Delta    float64 // AVER bound error; 0 means aver.DefaultDelta
}

// Run replays each kind on its own, since a check's evidence does not carry across kinds.
func Run(rows []Row, cfg Config) (Report, error) {
	if len(cfg.Policies) == 0 || len(cfg.Alphas) == 0 || len(cfg.Seeds) == 0 {
		return Report{}, errors.New("replay: name at least one policy, alpha and seed")
	}
	for _, a := range cfg.Alphas {
		if !(a > 0 && a < 1) {
			return Report{}, fmt.Errorf("replay: alpha %v is outside (0, 1)", a)
		}
	}
	if cfg.Delta == 0 {
		cfg.Delta = aver.DefaultDelta
	}
	if !(cfg.Delta > 0 && cfg.Delta < 1) {
		return Report{}, fmt.Errorf("replay: delta %v is outside (0, 1)", cfg.Delta)
	}
	byKind := map[string][]Row{}
	for _, r := range rows {
		byKind[r.Kind] = append(byKind[r.Kind], r)
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	rep := Report{Seeds: cfg.Seeds}
	for _, kind := range kinds {
		byTask := map[string][]Row{}
		for _, r := range byKind[kind] {
			byTask[r.Task] = append(byTask[r.Task], r)
		}
		for _, name := range cfg.Policies {
			for _, alpha := range cfg.Alphas {
				var res []Result
				for _, seed := range cfg.Seeds {
					r, err := replayOne(byKind[kind], byTask, name, Options{Alpha: alpha, Delta: cfg.Delta, Check: cfg.Check}, seed)
					if err != nil {
						return Report{}, fmt.Errorf("replay: %s, %s: %w", kind, name, err)
					}
					res = append(res, r)
				}
				rep.Results = append(rep.Results, res...)
				rep.Summaries = append(rep.Summaries, summarise(res))
			}
		}
	}
	return rep, nil
}

func replayOne(rows []Row, byTask map[string][]Row, name string, opts Options, seed uint64) (Result, error) {
	sp := SplitTasks(rows, seed)
	if len(sp.Calibration) == 0 {
		return Result{}, errors.New("needs at least two tasks to split")
	}
	var cal []Row
	for _, t := range sp.Calibration {
		cal = append(cal, byTask[t]...)
	}
	p, err := New(name, opts)
	if err != nil {
		return Result{}, err
	}
	if err := p.Calibrate(cal); err != nil {
		return Result{}, err
	}
	order := CheapestFirst(cal)
	out := make([]Outcome, 0, len(sp.Test))
	for _, t := range sp.Test {
		out = append(out, p.Decide(byTask[t], order))
	}
	r, err := Score(out)
	if err != nil {
		return Result{}, err
	}
	r.Kind, r.Policy, r.Alpha, r.Seed = rows[0].Kind, name, opts.Alpha, seed
	if u, ok := p.(interface{ Untestable() int }); ok {
		r.Untestable = u.Untestable()
	}
	if b, ok := p.(interface{ Bars() []float64 }); ok {
		r.Bars = b.Bars()
	}
	return r, nil
}

// Score turns one split's outcomes, one per test task, into its numbers.
func Score(out []Outcome) (Result, error) {
	n := len(out)
	if n == 0 {
		return Result{}, errors.New("replay: no test tasks to score")
	}
	r := Result{Tasks: n}
	for _, o := range out {
		r.SpentUSD += o.Spent
		if o.Accepted == nil {
			continue
		}
		r.Accepted++
		if o.Accepted.Correct {
			r.CorrectAccepted++
		} else {
			r.WrongAccepted++
		}
	}
	lo, err := stats.ClopperPearsonLower(r.WrongAccepted, n, 0.025)
	if err != nil {
		return Result{}, err
	}
	hi, err := stats.ClopperPearsonUpper(r.WrongAccepted, n, 0.025)
	if err != nil {
		return Result{}, err
	}
	r.FalseAccept = float64(r.WrongAccepted) / float64(n)
	r.FalseAcceptLo, r.FalseAcceptHi = lo, hi
	r.Refusal = 1 - float64(r.Accepted)/float64(n)
	r.CostPerTask = r.SpentUSD / float64(n)
	r.CostPerCorrect = perCorrect(r.SpentUSD, r.CorrectAccepted)
	return r, nil
}

func perCorrect(spent float64, correct int) USD {
	if correct == 0 {
		return USD(math.NaN())
	}
	return USD(spent / float64(correct))
}

// summarise pools spend and counts across splits; rates are means over splits.
func summarise(res []Result) Summary {
	s := Summary{Kind: res[0].Kind, Policy: res[0].Policy, Alpha: res[0].Alpha, Splits: len(res),
		FirstLo: res[0].FalseAcceptLo, FirstHi: res[0].FalseAcceptHi, Bars: res[0].Bars, Untestable: res[0].Untestable}
	var spent float64
	var tasks, correct, over int
	for _, r := range res {
		s.MeanFalseAccept += r.FalseAccept
		s.WorstFalseAccept = math.Max(s.WorstFalseAccept, r.FalseAccept)
		s.Refusal += r.Refusal
		if r.FalseAccept > r.Alpha {
			over++
		}
		spent += r.SpentUSD
		tasks += r.Tasks
		correct += r.CorrectAccepted
	}
	k := float64(len(res))
	s.MeanFalseAccept /= k
	s.Refusal /= k
	s.OverAlpha = float64(over) / k
	s.CostPerTask = spent / float64(tasks)
	s.CostPerCorrect = perCorrect(spent, correct)
	return s
}
