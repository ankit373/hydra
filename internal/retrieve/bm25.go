// SPDX-License-Identifier: MIT

package retrieve

import "math"

// BM25 parameters, the standard defaults from the original derivation. Named
// rather than inlined so a reviewer can see they were not invented here.
const (
	// K1 bounds how much repeating a term can raise a score.
	K1 = 1.2
	// B is how strongly a long document is penalised. 0 is no normalisation,
	// 1 is full.
	B = 0.75
)

// idf is the inverse document frequency of a term appearing in df of n
// documents.
//
// The +1 inside the log is what keeps a term present in more than half the
// corpus from scoring negative, which the textbook form allows and which would
// let a common term subtract from a document's relevance rather than merely
// not adding to it.
func idf(n, df int) float64 {
	if n <= 0 || df <= 0 {
		return 0
	}
	return math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
}

// score is one document's BM25 score against the query's terms.
//
// terms is sorted, and that is load-bearing rather than tidy: floating-point
// addition is not associative, so summing the same contributions in map order
// gives a score that differs in the last place between calls. Two documents
// within an ULP of each other then sort differently per call, which is a
// ranking that changes when nothing else did.
//
// avgLen is the corpus mean document length; a zero or negative one disables
// length normalisation rather than dividing by it.
func score(terms []string, doc map[string]int, docLen int, n int, df map[string]int, avgLen float64) float64 {
	var total float64
	norm := 1.0
	if avgLen > 0 {
		norm = 1 - B + B*float64(docLen)/avgLen
	}
	for _, term := range terms {
		f := float64(doc[term])
		if f == 0 {
			continue
		}
		total += idf(n, df[term]) * (f * (K1 + 1)) / (f + K1*norm)
	}
	return total
}
