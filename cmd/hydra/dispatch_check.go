// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

// dispatchChecks are the checks that read the answer itself, so a dispatch,
// which writes no file, can still run them.
var dispatchChecks = []string{"ground"}

// parseChecks refuses an unknown --check before anything routes.
func parseChecks(specs []string) error {
	for _, s := range specs {
		if !slices.Contains(dispatchChecks, s) {
			return fmt.Errorf("unknown --check %q: dispatch supports %s", s, strings.Join(dispatchChecks, ", "))
		}
	}
	return nil
}

// runChecks judges a dispatch's answer and scores the span that produced it.
// It reports and never fails the dispatch: the answer was already paid for.
func runChecks(ctx context.Context, specs []string, prompt, answer, domain, runID, spanID string) {
	for _, spec := range specs {
		switch spec {
		case "ground":
			checkGround(ctx, prompt, answer, domain, runID, spanID)
		}
	}
}

func checkGround(ctx context.Context, prompt, answer, domain, runID, spanID string) {
	o := &ground.Oracle{Prompt: prompt}
	v, verr := o.Verify(ctx, answer, trust.Task{Domain: domain})
	src := o.Key()
	if verr != nil {
		printNotGrounded(src, verr)
		return
	}
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("calibration: "+err.Error()))
		return
	}
	measured, why := oracle.Measured(cal, src, domain)
	printGrounding(src, o.Last, v, measured, why, oracle.LLR(cal, src, domain, v))
	if spanID == "" {
		return // a cached answer has no span of its own to score
	}
	if err := scoreGrounding(runID, spanID, src, v.Passed, v.Detail); err != nil {
		fmt.Fprintf(stdout, "  %s\n", dimStyle.Render("span score: "+err.Error()))
	}
}
