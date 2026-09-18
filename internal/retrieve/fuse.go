// SPDX-License-Identifier: MIT

package retrieve

import "sort"

// RRFK damps the contribution of low ranks. 60 is the value from the original
// reciprocal-rank-fusion paper and the one every implementation since has used;
// it is not tuned here, and tuning it is a measurement to make against
// internal/evalset rather than a number to pick.
const RRFK = 60.0

// Result is one document and its score under whichever scorer produced it.
type Result struct {
	DocID string  `json:"doc_id"`
	Score float64 `json:"score"`
}

// Fuse combines ranked lists by reciprocal rank fusion.
//
// Ranks rather than scores, because a BM25 score and a cosine similarity are
// not on a common scale and never will be. A weighted sum of the two needs a
// normaliser, a normaliser needs a corpus to fit on, and the fusion quality
// then becomes a function of whatever happened to be in the store. Ranks are
// comparable by construction.
//
// One list in yields that list's own order, so a machine with no embedder needs
// no separate code path to be correct.
func Fuse(lists ...[]Result) []Result {
	merged := map[string]float64{}
	for _, list := range lists {
		for i, r := range list {
			merged[r.DocID] += 1 / (RRFK + float64(i+1))
		}
	}

	out := make([]Result, 0, len(merged))
	for id, s := range merged {
		out = append(out, Result{DocID: id, Score: s})
	}
	sortResults(out)
	return out
}

// Top truncates a ranked list to at most k.
func Top(rs []Result, k int) []Result {
	if k > 0 && len(rs) > k {
		return rs[:k]
	}
	return rs
}

// sortResults orders by score, breaking ties on the id so the same inputs
// always produce the same order. A comparator that leaves a pair undecided
// makes a ranking a coin flip per call, which is how routing became
// nondeterministic once before.
func sortResults(rs []Result) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Score != rs[j].Score {
			return rs[i].Score > rs[j].Score
		}
		return rs[i].DocID < rs[j].DocID
	})
}
