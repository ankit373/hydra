// SPDX-License-Identifier: MIT

package trust

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

// defaultCorrelationDiscount is FamilyDiscount's fallback below minCoAgreementSamples.
const defaultCorrelationDiscount = 0.5

// minCoAgreementSamples: below this many same-family pairs, trust the flat default.
const minCoAgreementSamples = 20

// criticalCoupling: past this measured excess agreement, a family is flagged
// as effectively one vote. A practical threshold, not one derived from an
// Ising phase-transition calculation.
const criticalCoupling = 0.7

// ClusterByAgreement greedily groups indices into texts whose entries equiv
// treats as the same answer, shared by Run's own co-agreement recording and
// any ensembling caller (e.g. swarm.CalibratedJudge) that needs the same grouping.
func ClusterByAgreement(texts []string, equiv AnswerEquivalence) [][]int {
	if equiv == nil {
		equiv = TextEquivalence
	}
	var groups [][]int
	for i, t := range texts {
		placed := false
		for gi, g := range groups {
			if equiv(texts[g[0]], t) {
				groups[gi] = append(g, i)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, []int{i})
		}
	}
	return groups
}

type coAgreementSource struct {
	ID      string `json:"id"`
	Family  string `json:"family"`
	Cluster int    `json:"cluster"`
}

// coAgreementRecord is one task's participants, grouped by agreement.
type coAgreementRecord struct {
	TS      string              `json:"ts"`
	Domain  string              `json:"domain"`
	Sources []coAgreementSource `json:"sources"`
}

// DefaultCoAgreementPath is where co-agreement observations are persisted.
func DefaultCoAgreementPath() string {
	return filepath.Join(config.Dir(), "coagreement.jsonl")
}

// RecordCoAgreement clusters one task's answers by agreement and appends the
// observation, best-effort, since a logging failure must never affect the
// ensemble it observes. Sources with an empty Family are dropped: a repeat
// vote is only ever discounted when a family is known, so an unfamilied
// source carries no correlation signal to record.
func RecordCoAgreement(path, domain string, ids, families, texts []string, equiv AnswerEquivalence) {
	if path == "" || len(ids) < 2 {
		return
	}
	groups := ClusterByAgreement(texts, equiv)
	clusterOf := make([]int, len(texts))
	for gi, g := range groups {
		for _, idx := range g {
			clusterOf[idx] = gi
		}
	}
	rec := coAgreementRecord{TS: time.Now().UTC().Format(time.RFC3339), Domain: domain}
	for i := range ids {
		if families[i] == "" {
			continue
		}
		rec.Sources = append(rec.Sources, coAgreementSource{ID: ids[i], Family: families[i], Cluster: clusterOf[i]})
	}
	if len(rec.Sources) < 2 {
		return
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(string(raw) + "\n")
}

type coAgreementCacheEntry struct {
	records []coAgreementRecord
	modTime time.Time
	size    int64
}

var (
	coAgreementCacheMu sync.Mutex
	coAgreementCache   = map[string]coAgreementCacheEntry{}
)

// loadCoAgreement reads and caches coagreement.jsonl per path, keyed on
// mtime+size. One SPRT run can call FamilyDiscount/FamilyCoupling once per
// repeated-family source while the file is otherwise untouched (the run's own
// RecordCoAgreement append happens only after sampling finishes), and swarm's
// judge does the same, without this, every one of those calls re-reads and
// re-parses the same file from scratch. A real append (mtime or size changes)
// invalidates the cache on the next call.
func loadCoAgreement(path string) []coAgreementRecord {
	info, statErr := os.Stat(path)
	if statErr == nil {
		coAgreementCacheMu.Lock()
		if cached, ok := coAgreementCache[path]; ok && cached.modTime.Equal(info.ModTime()) && cached.size == info.Size() {
			coAgreementCacheMu.Unlock()
			return cached.records
		}
		coAgreementCacheMu.Unlock()
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []coAgreementRecord
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r coAgreementRecord
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		out = append(out, r)
	}

	if statErr == nil {
		coAgreementCacheMu.Lock()
		coAgreementCache[path] = coAgreementCacheEntry{records: out, modTime: info.ModTime(), size: info.Size()}
		coAgreementCacheMu.Unlock()
	}
	return out
}

// FamilyCoupling reports family's measured excess same-family agreement: the
// empirical rate at which two DIFFERENT same-family sources agree, minus the
// rate at which two different-family sources agree, the measurable
// definition of "these are correlated, not independently confirming." ok is
// false below minCoAgreementSamples same-family pairs.
func FamilyCoupling(path, family string) (j float64, ok bool) {
	var sameAgree, sameTotal, diffAgree, diffTotal int
	for _, r := range loadCoAgreement(path) {
		for i := 0; i < len(r.Sources); i++ {
			for k := i + 1; k < len(r.Sources); k++ {
				a, b := r.Sources[i], r.Sources[k]
				agreed := a.Cluster == b.Cluster
				if a.Family == family && b.Family == family {
					sameTotal++
					if agreed {
						sameAgree++
					}
				} else {
					diffTotal++
					if agreed {
						diffAgree++
					}
				}
			}
		}
	}
	if sameTotal < minCoAgreementSamples {
		return 0, false
	}
	sameRate := float64(sameAgree) / float64(sameTotal)
	diffRate := 0.0
	if diffTotal > 0 {
		diffRate = float64(diffAgree) / float64(diffTotal)
	}
	j = sameRate - diffRate
	switch {
	case j < 0:
		j = 0
	case j > 1:
		j = 1
	}
	return j, true
}

// FamilyCouplingResult is one family's measured coupling, as returned by
// AllFamilyCoupling. Warn mirrors FalseConsensusWarning's threshold check
// (OK && J >= criticalCoupling) so callers don't need criticalCoupling
// exported to reproduce it themselves.
type FamilyCouplingResult struct {
	J    float64
	OK   bool
	Warn bool
}

// AllFamilyCoupling computes FamilyCoupling for every family observed in the
// co-agreement log in a single pass, instead of the F full rescans a caller
// gets from calling FamilyCoupling once per KnownFamilies entry (every real
// caller of FalseConsensusWarning does exactly that today).
//
// The key simplification: family F's "diff" bucket (rate at which anything
// NOT a same-F pair agrees) is, by construction, every pair that isn't a
// same-F pair, so diffTotal[F] = (all pairs) - sameTotal[F], and likewise
// for the agreed counts. That means one global pass collecting each family's
// own same-family tally, plus one grand total, is enough to derive every
// family's J, no per-family rescan needed.
func AllFamilyCoupling(path string) map[string]FamilyCouplingResult {
	type tally struct{ sameAgree, sameTotal int }
	same := map[string]*tally{}
	var totalPairs, totalAgree int

	for _, r := range loadCoAgreement(path) {
		for i := 0; i < len(r.Sources); i++ {
			for k := i + 1; k < len(r.Sources); k++ {
				a, b := r.Sources[i], r.Sources[k]
				agreed := a.Cluster == b.Cluster
				totalPairs++
				if agreed {
					totalAgree++
				}
				if a.Family == b.Family {
					t := same[a.Family]
					if t == nil {
						t = &tally{}
						same[a.Family] = t
					}
					t.sameTotal++
					if agreed {
						t.sameAgree++
					}
				}
			}
		}
	}

	out := make(map[string]FamilyCouplingResult, len(same))
	for fam, t := range same {
		if t.sameTotal < minCoAgreementSamples {
			out[fam] = FamilyCouplingResult{}
			continue
		}
		sameRate := float64(t.sameAgree) / float64(t.sameTotal)
		diffTotal := totalPairs - t.sameTotal
		diffRate := 0.0
		if diffTotal > 0 {
			diffRate = float64(totalAgree-t.sameAgree) / float64(diffTotal)
		}
		j := sameRate - diffRate
		switch {
		case j < 0:
			j = 0
		case j > 1:
			j = 1
		}
		out[fam] = FamilyCouplingResult{J: j, OK: true, Warn: j >= criticalCoupling}
	}
	return out
}

// FamilyDiscount replaces the flat correlation constant: 1-J, so a family
// with no measured excess correlation (J=0) is not discounted at all, and a
// family whose members are nearly always identical (J→1) contributes almost
// nothing on a repeat vote. Falls back to defaultCorrelationDiscount below
// minCoAgreementSamples.
func FamilyDiscount(path, family string) float64 {
	j, ok := FamilyCoupling(path, family)
	if !ok {
		return defaultCorrelationDiscount
	}
	return 1 - j
}

// FalseConsensusWarning reports whether family's measured coupling has
// crossed criticalCoupling, its members are effectively one vote.
func FalseConsensusWarning(path, family string) (j float64, warn bool) {
	j, ok := FamilyCoupling(path, family)
	return j, ok && j >= criticalCoupling
}

// KnownFamilies returns the distinct families observed in the co-agreement
// log, sorted, for a caller (e.g. `hyctl trust calibration`) that wants to
// check every family's FalseConsensusWarning without knowing names up front.
func KnownFamilies(path string) []string {
	seen := map[string]bool{}
	for _, r := range loadCoAgreement(path) {
		for _, s := range r.Sources {
			seen[s.Family] = true
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
