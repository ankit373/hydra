package trust

// DefectWeights are the multipliers that turn a task's risk attributes into the
// dollar cost of shipping a wrong answer. They are deliberately simple and
// tunable; the values below are illustrative defaults, not measured constants.
// Graphify will supply real blast-radius multipliers in Phase 3.
type DefectWeights struct {
	Base         float64 // baseline cost of any wrong answer, USD
	Irreversible float64 // multiplier when the change can't be cheaply undone
	PII          float64 // multiplier when personal data is involved
	Production   float64 // multiplier when the target is production
}

// DefaultWeights returns the built-in defect weights. A local, reversible,
// non-PII, non-prod mistake costs Base; each risk factor scales it up.
func DefaultWeights() DefectWeights {
	return DefectWeights{
		Base:         1.0,
		Irreversible: 5.0,
		PII:          10.0,
		Production:   3.0,
	}
}

// DefectModel prices the cost of shipping an incorrect answer for a task. That
// cost sets how much confidence a task must clear before Hydra stops sampling
// (Phase 2) and is surfaced in `hydra dispatch --dry-run` / `hydra trust defect`.
type DefectModel struct {
	W DefectWeights
}

// NewDefectModel returns a model using the default weights.
func NewDefectModel() *DefectModel {
	return &DefectModel{W: DefaultWeights()}
}

// CostUSD = Base × blast × w_irrev × w_pii × w_prod, with each risk factor
// applied only when present. BlastRadius ≤ 0 is treated as 1.0 (local).
func (d *DefectModel) CostUSD(t Task) float64 {
	cost := d.W.Base

	blast := t.BlastRadius
	if blast <= 0 {
		blast = 1.0
	}
	cost *= blast

	if t.Irreversible {
		cost *= d.W.Irreversible
	}
	if t.TouchesPII {
		cost *= d.W.PII
	}
	if t.Production {
		cost *= d.W.Production
	}
	return cost
}
