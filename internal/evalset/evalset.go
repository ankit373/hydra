// SPDX-License-Identifier: MIT

// Package evalset is Hydra's store of oracle-verified examples.
//
// A dispatch an oracle checked is a *labelled* example: a task, a candidate,
// and ground truth about whether it worked. That is the rarest and most
// valuable thing Hydra produces, and it is the only class of trace data worth
// keeping verbatim and forever — everything else is better as a statistic.
//
// It lives outside the trace store on purpose. Traces expire; this must not,
// or the router loses the only corpus it could ever be improved against.
package evalset

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
)

// SchemaVersion is stamped on every example so readers can branch, not guess.
const SchemaVersion = 1

// ErrNoCandidate reports an example with nothing to learn from. A verdict with
// no candidate is a statistic, and belongs in calibration rather than here.
var ErrNoCandidate = errors.New("evalset: example has no candidate")

// Example is one labelled observation.
type Example struct {
	V  int    `json:"v"`
	TS string `json:"ts"`

	TaskHash      string `json:"task_hash"`
	Domain        string `json:"domain"`
	Source        string `json:"source"` // the oracle that produced the verdict
	Head          string `json:"head,omitempty"`
	CandidateHash string `json:"candidate_hash"`
	Candidate     string `json:"candidate"`
	Passed        bool   `json:"passed"`
	Detail        string `json:"detail,omitempty"`

	// Config is the deployment-identity breadcrumb, so an example can be tied
	// back to the routing rules in effect when it was produced. Without it a
	// corpus spanning a config change is summarising two different systems.
	Config string `json:"config,omitempty"`

	// PII marks a candidate that tripped policy detection. The example is still
	// kept — it is ground truth, and dropping it would bias the corpus toward
	// whatever contains no PII — but any export path must refuse it.
	PII bool `json:"pii,omitempty"`
}

// DefaultPath is where the eval set lives. Deliberately not under logs/:
// nothing that prunes logs may ever walk this directory.
func DefaultPath() string {
	return filepath.Join(config.Dir(), "evalset", "examples.jsonl")
}

// Hash is the canonical content hash used for both task and candidate.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// Add appends an example unless an identical (task, candidate) pair is already
// present, and reports whether it wrote. Re-running the same verification is
// normal and must not inflate the corpus — a duplicated example would weight
// that case twice in anything computed from the set.
func Add(path string, e Example) (bool, error) {
	if strings.TrimSpace(e.Candidate) == "" {
		return false, ErrNoCandidate
	}
	e.V = SchemaVersion
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	e.CandidateHash = Hash(e.Candidate)
	if e.TaskHash == "" {
		e.TaskHash = Hash(e.Domain + "\x00" + e.Source)
	}
	if !e.PII {
		e.PII = policy.Classify(e.Candidate).PII
	}

	existing, err := Load(path)
	if err != nil {
		return false, err
	}
	for _, x := range existing {
		if x.TaskHash == e.TaskHash && x.CandidateHash == e.CandidateHash {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	raw, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	_, err = fmt.Fprintln(f, string(raw))
	return err == nil, err
}

// Load reads every example. A missing file is not an error — nothing has been
// verified yet.
func Load(path string) ([]Example, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Example
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Example
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // a torn tail must not hide the corpus before it
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// DomainStat summarises one domain's examples.
type DomainStat struct {
	Domain   string  `json:"domain"`
	Total    int     `json:"total"`
	Passed   int     `json:"passed"`
	Failed   int     `json:"failed"`
	PassRate float64 `json:"pass_rate"`
	WithPII  int     `json:"with_pii"`
}

// Stats summarises the corpus by domain, largest first.
func Stats(examples []Example) []DomainStat {
	acc := map[string]*DomainStat{}
	for _, e := range examples {
		d := e.Domain
		if d == "" {
			d = "(none)"
		}
		s := acc[d]
		if s == nil {
			s = &DomainStat{Domain: d}
			acc[d] = s
		}
		s.Total++
		if e.Passed {
			s.Passed++
		} else {
			s.Failed++
		}
		if e.PII {
			s.WithPII++
		}
	}
	out := make([]DomainStat, 0, len(acc))
	for _, s := range acc {
		if s.Total > 0 {
			s.PassRate = float64(s.Passed) / float64(s.Total)
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Domain < out[j].Domain
	})
	return out
}
