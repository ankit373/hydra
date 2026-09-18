// SPDX-License-Identifier: MIT

package trust

import "math"

// Commitments counts what a source's own answers did once something judged
// them, pooled across every domain: total is how many times it backed an
// answer that was later verified, correct how many of those held up.
//
// This reads the "said correct" row rather than se/sp because it answers a
// different question. Sensitivity and specificity say how good a source's
// verdict is *as evidence*, which is what the SPRT ensemble needs. A router
// choosing who should write the answer is asking when this head commits, how
// often is it right, and that is the positive row alone.
//
// Pooled because a head ranking has no task and so no domain. The per-domain
// read already exists and is what --confidence uses through LLR and D.
func (c *Calibrator) Commitments(source string) (correct, total int) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var tp, fp float64
	for k, conf := range c.store {
		if k.source != source {
			continue
		}
		tp += conf.TP - laplacePrior
		fp += conf.FP - laplacePrior
	}
	return int(math.Round(tp)), int(math.Round(tp + fp))
}
