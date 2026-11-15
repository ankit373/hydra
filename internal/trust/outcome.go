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
	recorded := 0
	for _, v := range VerdictsOnFinal(ledger) {
		if err := cal.Update(v.Source, domain, v.SaidCorrect, actual); err != nil {
			return recorded, err
		}
		recorded++
	}
	return recorded, nil
}

// SourceVerdict is one ledger entry re-expressed as a verdict on the answer
// the run ended on.
type SourceVerdict struct {
	Source      string
	SaidCorrect bool
}

// VerdictsOnFinal is every ledger entry that says something about the final
// answer, in ledger order. Entries that say nothing about it are skipped.
func VerdictsOnFinal(ledger []Evidence) []SourceVerdict {
	if len(ledger) == 0 {
		return nil
	}
	// The leading answer can change as votes arrive, and each entry records the
	// leader at its own moment, so the last entry always holds the final one.
	final := ledger[len(ledger)-1].Candidate
	out := make([]SourceVerdict, 0, len(ledger))
	for _, e := range ledger {
		if saidCorrect, ok := verdictOn(e, final); ok {
			out = append(out, SourceVerdict{Source: e.Source, SaidCorrect: saidCorrect})
		}
	}
	return out
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
