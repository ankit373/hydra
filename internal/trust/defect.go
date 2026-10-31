// SPDX-License-Identifier: MIT

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

// defaultToleratedLeakUSD is the expected cost of leaked defects Hydra is
// willing to tolerate per task. RequiredConfidence sets the error probability α
// so that α × (defect cost) ≈ this constant: a mistake that costs 10× more must
// be 10× less likely. The baseline task (defect cost = Base) yields α = 0.10, i.e.
// a 90% target, matching the SPRT default.
const defaultToleratedLeakUSD = 0.10

// maxConfidence caps how sure Hydra will insist on being, so an enormous
// blast radius can't demand an unreachable (α→0) target.
const maxConfidence = 0.999

// DefectModel prices the cost of shipping an incorrect answer for a task. That
// cost sets how much confidence a task must clear before Hydra stops sampling
// (RequiredConfidence) and is surfaced in `hyctl dispatch --confidence --file`,
// `hyctl trust defect`, and `hyctl dispatch --dry-run`.
type DefectModel struct {
	W DefectWeights
	// ToleratedLeakUSD is the expected leaked-defect cost held constant by
	// RequiredConfidence. Zero uses defaultToleratedLeakUSD.
	ToleratedLeakUSD float64
}

// NewDefectModel returns a model using the default weights.
func NewDefectModel() *DefectModel {
	return &DefectModel{W: DefaultWeights()}
}

// RequiredConfidence maps a task's defect cost to the confidence-of-correctness
// Hydra should demand before it stops sampling. It holds the *expected leaked
// defect cost* constant: α = toleratedLeak / defectCost, so target = 1 − α. A
// costlier mistake linearly lowers the tolerated error probability; the result
// is clamped to [0.5, maxConfidence].
func (d *DefectModel) RequiredConfidence(t Task) float64 {
	leak := d.ToleratedLeakUSD
	if leak <= 0 {
		leak = defaultToleratedLeakUSD
	}
	cost := d.CostUSD(t)
	if cost <= 0 {
		return 0.5
	}
	alpha := leak / cost
	target := 1 - alpha
	if target > maxConfidence {
		target = maxConfidence
	}
	if target < 0.5 {
		target = 0.5
	}
	return target
}

// NormalizeBlastRadius applies the same clamp CostUSD uses internally: a
// non-positive radius is treated as 1.0 (local). Exported so a caller that
// displays BlastRadius alongside a derived cost/confidence, `hyctl trust
// defect`, shows the value the calculation actually used, not the raw input.
func NormalizeBlastRadius(blast float64) float64 {
	if blast <= 0 {
		return 1.0
	}
	return blast
}

// CostUSD = Base × blast × w_irrev × w_pii × w_prod, with each risk factor
// applied only when present. BlastRadius ≤ 0 is treated as 1.0 (local).
func (d *DefectModel) CostUSD(t Task) float64 {
	cost := d.W.Base

	cost *= NormalizeBlastRadius(t.BlastRadius)

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
