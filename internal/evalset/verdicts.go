// SPDX-License-Identifier: MIT

package evalset

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

const (
	verdictSchema = 1
	kindVerdict   = "verdict"
	kindTruth     = "truth"

	// OutcomeCorrect and OutcomeIncorrect are the only values a Truth carries.
	OutcomeCorrect   = "correct"
	OutcomeIncorrect = "incorrect"
)

var (
	// ErrNoAnswerKey reports a line with neither span nor candidate hash, which
	// could never be joined to anything else said about the same answer.
	ErrNoAnswerKey = errors.New("evalset: verdict names no span or candidate hash")
	ErrNoCheck     = errors.New("evalset: verdict names no check")
	ErrBadOutcome  = errors.New("evalset: truth outcome must be correct or incorrect")
)

// Verdict is one check's judgement of one answer. It holds the candidate's
// hash, never its text, so the log carries no answer content.
type Verdict struct {
	V             int    `json:"v"`
	TS            string `json:"ts"`
	SpanID        string `json:"span_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	TaskHash      string `json:"task_hash,omitempty"`
	CandidateHash string `json:"candidate_hash,omitempty"`
	Head          string `json:"head,omitempty"`
	Domain        string `json:"domain,omitempty"`
	Check         string `json:"check"`
	Passed        bool   `json:"passed"`
	Detail        string `json:"detail,omitempty"`
	// GenFamily is the family that wrote a generated check, set only on those.
	GenFamily string `json:"gen_family,omitempty"`
}

// Truth is ground truth about an answer that arrives after its checks ran.
type Truth struct {
	V             int    `json:"v"`
	TS            string `json:"ts"`
	SpanID        string `json:"span_id,omitempty"`
	CandidateHash string `json:"candidate_hash,omitempty"`
	Outcome       string `json:"outcome"`
	Source        string `json:"source,omitempty"`
}

// Answer is everything the log says about one answer: its checks in append
// order and the latest ground truth, if any.
type Answer struct {
	Key    string
	Head   string
	Domain string
	Checks []Verdict
	Truth  *Truth

	bySpan bool
}

type verdictLine struct {
	Kind string `json:"kind"`
	Verdict
}

type truthLine struct {
	Kind string `json:"kind"`
	Truth
}

// VerdictsPath is the verdict log. Beside the eval set and outside logs/ for
// the same reason: nothing that prunes logs may reach it.
func VerdictsPath() string {
	return filepath.Join(filepath.Dir(DefaultPath()), "verdicts.jsonl")
}

// AddVerdict appends one check's verdict.
func AddVerdict(path string, v Verdict) error {
	if v.SpanID == "" && v.CandidateHash == "" {
		return ErrNoAnswerKey
	}
	if strings.TrimSpace(v.Check) == "" {
		return ErrNoCheck
	}
	v.V, v.TS = verdictSchema, stamp(v.TS)
	return appendLine(path, verdictLine{Kind: kindVerdict, Verdict: v})
}

// AddTruth appends ground truth for an answer.
func AddTruth(path string, t Truth) error {
	if t.SpanID == "" && t.CandidateHash == "" {
		return ErrNoAnswerKey
	}
	if t.Outcome != OutcomeCorrect && t.Outcome != OutcomeIncorrect {
		return ErrBadOutcome
	}
	t.V, t.TS = verdictSchema, stamp(t.TS)
	return appendLine(path, truthLine{Kind: kindTruth, Truth: t})
}

func stamp(ts string) string {
	if ts != "" {
		return ts
	}
	return time.Now().UTC().Format(time.RFC3339)
}

func appendLine(path string, rec any) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	ignoreCorpusDir(filepath.Dir(path))
	lock, err := util.Lock(util.LockPath(path))
	if err != nil {
		return err
	}
	defer lock.Unlock()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Joint folds the log into one Answer per answer key, the span id when the
// line has one and the candidate hash otherwise. Answers keep first-seen order.
func Joint(path string) ([]Answer, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Answer
	idx := map[string]int{}
	at := func(span, hash string) *Answer {
		key := span
		if key == "" {
			key = hash
		}
		i, ok := idx[key]
		if !ok {
			i = len(out)
			idx[key] = i
			out = append(out, Answer{Key: key, bySpan: span != ""})
		}
		return &out[i]
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		var probe struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(line, &probe) != nil {
			continue // a torn tail must not hide the log before it
		}
		switch probe.Kind {
		case kindVerdict:
			var l verdictLine
			if json.Unmarshal(line, &l) != nil {
				continue
			}
			a := at(l.SpanID, l.CandidateHash)
			if a.Head == "" {
				a.Head = l.Head
			}
			if a.Domain == "" {
				a.Domain = l.Domain
			}
			a.Checks = append(a.Checks, l.Verdict)
		case kindTruth:
			var l truthLine
			if json.Unmarshal(line, &l) != nil {
				continue
			}
			t := l.Truth
			at(l.SpanID, l.CandidateHash).Truth = &t
		}
	}
	return out, sc.Err()
}

// VerdictBySpan says whether each span-keyed answer passed. Ground truth wins
// when present; otherwise every check must pass, the rule runlog.Verdict uses.
func VerdictBySpan(path string) (map[string]bool, error) {
	answers, err := Joint(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(answers))
	for _, a := range answers {
		if !a.bySpan {
			continue
		}
		out[a.Key] = a.passed()
	}
	return out, nil
}

// ByCheck is one verdict per check. A check that ran twice passed only if it
// passed both times, the same rule as a span's verdict.
func (a Answer) ByCheck() map[string]bool {
	out := make(map[string]bool, len(a.Checks))
	for _, c := range a.Checks {
		prev, seen := out[c.Check]
		out[c.Check] = c.Passed && (!seen || prev)
	}
	return out
}

func (a Answer) passed() bool {
	if a.Truth != nil {
		return a.Truth.Outcome == OutcomeCorrect
	}
	for _, c := range a.Checks {
		if !c.Passed {
			return false
		}
	}
	return true
}

// AddModelVotes adds each ensemble run's votes, read from the trust log, as
// model:<source> checks ahead of the answer's own, since the vote came first.
// Runs with no span predate #1144 and cannot join; they are skipped and counted.
func AddModelVotes(answers []Answer, runs []trust.RunLog) ([]Answer, int) {
	idx := make(map[string]int, len(answers))
	for i, a := range answers {
		if a.bySpan {
			idx[a.Key] = i
		}
	}
	// Newest run per span, the same choice trust.FindRunBySpan makes.
	latest := map[string]int{}
	var order []string
	skipped := 0
	for i, r := range runs {
		if r.SpanID == "" {
			skipped++
			continue
		}
		if _, seen := latest[r.SpanID]; !seen {
			order = append(order, r.SpanID)
		}
		latest[r.SpanID] = i
	}
	for _, span := range order {
		r := runs[latest[span]]
		votes := trust.VerdictsOnFinal(r.Ledger)
		if len(votes) == 0 {
			continue
		}
		i, ok := idx[span]
		if !ok {
			i = len(answers)
			idx[span] = i
			answers = append(answers, Answer{Key: span, bySpan: true})
		}
		a := &answers[i]
		checks := make([]Verdict, 0, len(votes)+len(a.Checks))
		for _, v := range votes {
			checks = append(checks, Verdict{
				V: verdictSchema, TS: r.TS, SpanID: span, Domain: r.Domain,
				Check: "model:" + v.Source, Passed: v.SaidCorrect,
			})
		}
		a.Checks = append(checks, a.Checks...)
		if a.Domain == "" {
			a.Domain = r.Domain
		}
	}
	return answers, skipped
}
