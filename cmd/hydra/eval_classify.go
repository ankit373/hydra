// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"fmt"

	"github.com/ankit373/hydra/internal/classify"
)

// classifyRefusal turns each way the corpus cannot answer into what would make
// it able to. A bare "too few examples" is true of four different situations
// with four different remedies.
func classifyRefusal(err error) error {
	switch {
	case errors.Is(err, classify.ErrNoVectors):
		return fmt.Errorf("no example carries an embedding, so there is nothing to measure.\n" +
			"  One is recorded per validated edit when an embedding model is on the machine.\n" +
			"  `hyctl eval training` reports what the corpus holds")
	case errors.Is(err, classify.ErrTooFew):
		return fmt.Errorf("too few embedded examples to hold any out.\n"+
			"  A neighbourhood needs more than %d, and the evaluation needs neighbourhoods "+
			"it can score", classify.MinNeighbours)
	case errors.Is(err, classify.ErrNoNeighbourhood):
		return fmt.Errorf("nothing in the corpus is near anything else in it, so no " +
			"neighbourhood exists to read")
	}
	return err
}

func printClassifyResult(res classify.Result) {
	r := res.Report
	fmt.Printf("corpus      %d embedded examples in %s\n", res.Corpus, res.Model)
	fmt.Printf("coverage    %.0f%%  (%d answered, %d refused)\n",
		res.Coverage()*100, res.Answered, res.Refused)
	fmt.Printf("brier       %.4f  (base rate %.3f)\n", r.Brier, r.BaseRate)
	fmt.Printf("resolution  %.4f  %s\n", r.Resolution, dimStyle.Render(resolutionNote(res)))
	fmt.Printf("reliability %.4f  %s\n", r.Reliability,
		dimStyle.Render("how far the stated rates are from the observed ones, lower is better"))
	fmt.Printf("ece         %.4f  (worst bin %.4f)\n", r.ECE, r.MCE)
	fmt.Printf("\n%s\n", dimStyle.Render(
		"resolution is the number that matters: an estimator restating the corpus average is "+
			"perfectly calibrated and no use to a router"))
}

func resolutionNote(res classify.Result) string {
	if res.Beats() {
		return "similarity carries signal about the outcome"
	}
	return "no signal: this is the corpus average wearing a hat"
}
