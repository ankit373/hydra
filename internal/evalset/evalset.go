// SPDX-License-Identifier: MIT

// Package evalset is Hydra's store of oracle-verified examples.
//
// A dispatch an oracle checked is a *labelled* example: a task, a candidate,
// and ground truth about whether it worked. That is the rarest and most
// valuable thing Hydra produces, and it is the only class of trace data worth
// keeping verbatim and forever, everything else is better as a statistic.
//
// It lives outside the trace store on purpose. Traces expire; this must not,
// or the router loses the only corpus it could ever be improved against.
package evalset

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/util"
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
	// kept, it is ground truth, and dropping it would bias the corpus toward
	// whatever contains no PII, but any export path must refuse it.
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

// The dedup sidecar holds only the two hashes Add compares, so a duplicate
// check reads tens of bytes per example rather than unmarshalling every stored
// candidate. Rescanning the corpus made filling it quadratic (#796).
const (
	idxMagic = "hydra-evalset-idx 1 "
	// Fixed width, so committing a new corpus size is one WriteAt.
	idxHdrLen = len(idxMagic) + 20 + 1
	// maxLineBytes bounds one record. A candidate is a source file, and a
	// bufio.Scanner refuses a longer line rather than truncating it.
	maxLineBytes = 16 << 20
)

func indexPath(corpus string) string { return corpus + ".idx" }

func idxHeader(corpusSize int64) []byte {
	return []byte(fmt.Sprintf("%s%020d\n", idxMagic, corpusSize))
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// dedupKey identifies an example for dedup. Not fixed width: a caller may set
// its own TaskHash, and a wrong-length key would silently corrupt the sidecar.
func dedupKey(taskHash, candidateHash string) string {
	return taskHash + ":" + candidateHash
}

// loadKeys returns the corpus's dedup keys, rebuilding the sidecar when it is
// absent, malformed, or does not describe this corpus size. The corpus is
// append-only, so size equality is exact currency and costs one stat.
func loadKeys(corpus string, corpusSize int64) (map[string]struct{}, error) {
	raw, err := os.ReadFile(indexPath(corpus))
	if err == nil && len(raw) >= idxHdrLen && string(raw[:len(idxMagic)]) == idxMagic {
		n, perr := strconv.ParseInt(strings.TrimSpace(string(raw[len(idxMagic):idxHdrLen])), 10, 64)
		if perr == nil && n == corpusSize {
			keys := make(map[string]struct{})
			for _, line := range strings.Split(string(raw[idxHdrLen:]), "\n") {
				if line != "" {
					keys[line] = struct{}{}
				}
			}
			return keys, nil
		}
	}
	return rebuildIndex(corpus, corpusSize)
}

// rebuildIndex derives the sidecar from the corpus, decoding only the two hash
// fields. Runs once per staleness: no sidecar yet, or a crash between the
// corpus append and the header commit.
func rebuildIndex(corpus string, corpusSize int64) (map[string]struct{}, error) {
	keys := make(map[string]struct{})
	var order []string

	f, err := os.Open(corpus)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var rec struct {
				TaskHash      string `json:"task_hash"`
				CandidateHash string `json:"candidate_hash"`
			}
			if json.Unmarshal([]byte(line), &rec) != nil {
				continue // a torn tail must not hide the corpus before it
			}
			k := dedupKey(rec.TaskHash, rec.CandidateHash)
			if _, seen := keys[k]; !seen {
				keys[k] = struct{}{}
				order = append(order, k)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return keys, writeIndex(corpus, order, corpusSize)
}

func writeIndex(corpus string, keys []string, corpusSize int64) error {
	var b bytes.Buffer
	b.Write(idxHeader(corpusSize))
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\n')
	}
	tmp := indexPath(corpus) + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, indexPath(corpus))
}

// commitKey appends the key, then rewrites the header. Header last, because it
// is the commit: a crash before it leaves the size stale and the next Add
// rebuilds, where a crash after would claim a key the corpus does not hold.
func commitKey(corpus, key string, corpusSize int64) error {
	// No O_CREATE: loadKeys has already written a headered sidecar, so a
	// missing one here is a real error rather than something to paper over
	// by writing a key where the header belongs.
	f, err := os.OpenFile(indexPath(corpus), os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		return err
	}
	_, err = f.WriteAt(idxHeader(corpusSize), 0)
	return err
}

// Add appends an example unless an identical (task, candidate) pair is already
// present, and reports whether it wrote. Re-running the same verification is
// normal and must not inflate the corpus, a duplicated example would weight
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
	raw, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	line := append(raw, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	// Corpus and sidecar must not diverge, and under O_APPEND two hyctl
	// processes can interleave: the kernel writes at the end as it is at write
	// time. The same hazard internal/payload takes a store-wide lock against.
	lock, err := util.Lock(util.LockPath(path))
	if err != nil {
		return false, err
	}
	defer lock.Unlock()

	size := fileSize(path)
	keys, err := loadKeys(path, size)
	if err != nil {
		return false, err
	}
	key := dedupKey(e.TaskHash, e.CandidateHash)
	if _, dup := keys[key]; dup {
		return false, nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	return true, commitKey(path, key, size+int64(len(line)))
}

// Load reads every example. A missing file is not an error, nothing has been
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
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
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
