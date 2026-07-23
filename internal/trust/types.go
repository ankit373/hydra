// Package trust is the confidence layer of the Trust Control Plane: it measures
// how much each evidence source's verdict is actually worth (calibration) and
// how costly a wrong answer is for a given task (defect cost). Phase 1 ships the
// calibration engine and the defect-cost model; the SPRT optimal-stopping
// ensemble that consumes them lands in Phase 2.
package trust

// Outcome is the ground-truth-ish label used to train calibration.
type Outcome int

const (
	// OutcomeUnknown means no ground truth is available yet — it never trains.
	OutcomeUnknown Outcome = iota
	// OutcomeCorrect: tests passed / user approved / not reverted within N days.
	OutcomeCorrect
	// OutcomeIncorrect: tests failed / user rejected / reverted.
	OutcomeIncorrect
)

// ParseOutcome maps a CLI string to an Outcome. Unrecognized → OutcomeUnknown.
func ParseOutcome(s string) Outcome {
	switch s {
	case "correct", "pass", "approve", "approved", "ok":
		return OutcomeCorrect
	case "incorrect", "fail", "reject", "rejected", "revert", "reverted":
		return OutcomeIncorrect
	default:
		return OutcomeUnknown
	}
}

// Task describes the work whose wrong-answer cost the DefectModel prices.
type Task struct {
	Domain string
	// BlastRadius scales cost by how much other code a wrong answer would break.
	// 0 or 1 = local/self-contained. Graphify supplies real values in Phase 3;
	// until then callers leave it at the default (treated as 1.0).
	BlastRadius float64
	// Irreversible: the change cannot be cheaply undone (data migration, deploy).
	Irreversible bool
	// TouchesPII: the task handles personal data — a wrong answer is a leak risk.
	TouchesPII bool
	// Production: the change targets a production surface, not a scratch/dev one.
	Production bool
}
