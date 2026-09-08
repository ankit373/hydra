// SPDX-License-Identifier: MIT

package trust

import "fmt"

// ApplyRunOutcome trains calibration from a finished ensemble run once ground
// truth about its candidate is known, and is the only path that can move
// specificity.
//
// Every other writer records saidCorrect=true, because a generator that
// produced an answer implicitly asserts it. That only ever fills TP and FP, so
// TN keeps its bare prior and sp can never rise above 0.5, only decay as false
// positives land. Since LLR(said correct) = ln(se/(1-sp)), that holds every
// source under ln2 = 0.693 nats against the 2.944 a 95% target needs (#771).
// A source that *disagreed* with a candidate the answer turned out not to be is
// the negative observation the confusion matrix is otherwise missing.
//
// It returns how many observations were recorded, which is not len(ledger):
// entries whose verdict says nothing about the final candidate are skipped
// rather than guessed at.
func ApplyRunOutcome(cal *Calibrator, domain string, ledger []Evidence, actual Outcome) (int, error) {
	if cal == nil {
		return 0, fmt.Errorf("trust: nil calibrator")
	}
	if actual == OutcomeUnknown {
		return 0, nil
	}
	if len(ledger) == 0 {
		return 0, nil
	}
	// The candidate can change mid-run when Λ crosses the reject threshold, and
	// a pivot rewrites only the pivoting entry, so the last entry always holds
	// whichever answer the run ended on.
	final := ledger[len(ledger)-1].Candidate

	recorded := 0
	for _, e := range ledger {
		saidCorrect, ok := verdictOn(e, final)
		if !ok {
			continue
		}
		if err := cal.Update(e.Source, domain, saidCorrect, actual); err != nil {
			return recorded, err
		}
		recorded++
	}
	return recorded, nil
}

// verdictOn re-expresses one ledger entry as a verdict about final, the answer
// the run ended on. Agreed was recorded against whatever the candidate was at
// the time, which is not always the one that got verified.
func verdictOn(e Evidence, final string) (saidCorrect, ok bool) {
	switch {
	case e.Candidate == final:
		return e.Agreed, true
	case e.Agreed:
		// Asserted a candidate that was later superseded, so it asserted
		// something other than final: a negative verdict on final.
		return false, true
	default:
		// Disagreed with a superseded candidate. That rules out one answer and
		// says nothing about final, so there is no verdict to record.
		return false, false
	}
}
