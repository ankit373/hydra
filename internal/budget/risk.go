package budget

import "math"

// The orchestrator's claude_pct is the one genuinely session-cumulative budget
// quantity: it only climbs as the context window fills. Modelling it as a
// drift-diffusion process on the utilisation fraction x ∈ [0,1] lets the
// governor act on the *rate* of burn (and its noise), not just the current
// level — a session climbing fast at 55% is riskier than one parked at 74%,
// which the static band table (ModeFor) cannot see. The 80% emergency line is
// an absorbing barrier; each recorded claude_pct observation is one step.
const (
	emergencyFrac = 0.80 // absorbing barrier b (matches ModeEmergency at 80%)
	riskHorizon   = 3.0  // look-ahead in observations ("within the next ~3 claude_pct updates")
	MaxPctHistory = 12   // bounded claude_pct series kept in state.json

	// Risk floors: a high probability of hitting the 80% barrier within the
	// horizon imposes a minimum mode regardless of the current level, so a fast
	// burn is caught before it crosses a static threshold.
	riskWarnAt = 0.5 // P ≥ this → at least ModeWarning
	riskCritAt = 0.8 // P ≥ this → at least ModeCritical
)

// AppendPctHistory returns hist with pct appended, but only when pct differs
// from the last entry (the series tracks the claude_pct trajectory, so flat
// periods add no points), trimmed to the newest max observations. A non-positive
// pct (unknown) is ignored. Pure, so the state.json writer can stay a thin shell.
func AppendPctHistory(hist []int, pct, max int) []int {
	if pct <= 0 {
		return hist
	}
	if n := len(hist); n > 0 && hist[n-1] == pct {
		return hist // no change → no new observation
	}
	hist = append(hist, pct)
	if max > 0 && len(hist) > max {
		hist = hist[len(hist)-max:]
	}
	return hist
}

// RiskFromHistory estimates the burn rate and the probability of hitting the
// 80% emergency line within the horizon, from a claude_pct series. burnRatePct
// is the mean per-observation increment in percentage points; risk is the
// first-passage probability. With fewer than two observations there is no rate
// signal and both are zero, so the governor falls back to the level band.
func RiskFromHistory(hist []int) (burnRatePct, risk float64) {
	if len(hist) < 2 {
		return 0, 0
	}
	// Increments in fraction units.
	n := len(hist) - 1
	deltas := make([]float64, n)
	var sum float64
	for i := 0; i < n; i++ {
		deltas[i] = float64(hist[i+1]-hist[i]) / 100
		sum += deltas[i]
	}
	mu := sum / float64(n)
	var ss float64
	for _, d := range deltas {
		ss += (d - mu) * (d - mu)
	}
	sigma := math.Sqrt(ss / float64(n)) // population stddev of increments

	x := float64(hist[len(hist)-1]) / 100
	risk = FirstPassageProb(emergencyFrac-x, mu, sigma, riskHorizon)
	return mu * 100, risk
}

// FirstPassageProb returns the probability that a Brownian motion with per-step
// drift mu and per-step volatility sigma, starting dist below an absorbing
// barrier, reaches that barrier within horizon steps. This is the closed-form
// first-passage-time CDF for drifted Brownian motion (reflection principle):
//
//	P(τ ≤ H) = Φ( (μH − d)/(σ√H) ) + exp(2μd/σ²)·Φ( (−μH − d)/(σ√H) )
//
// with d = dist. Degenerate inputs collapse to the deterministic answer: no
// distance left → already absorbed (1); no horizon → 0; zero volatility → pure
// drift (1 iff μH ≥ d). The result is clamped to [0,1]; it is a governor
// heuristic, so numerically extreme regimes clamp rather than error.
func FirstPassageProb(dist, mu, sigma, horizon float64) float64 {
	if dist <= 0 {
		return 1 // already at or past the barrier
	}
	if horizon <= 0 {
		return 0
	}
	sqrtH := math.Sqrt(horizon)
	expo := 2 * mu * dist / (sigma * sigma)
	if sigma <= 0 || math.IsInf(expo, 0) {
		// No (or negligible) volatility → deterministic drift.
		if mu > 0 && mu*horizon >= dist {
			return 1
		}
		return 0
	}
	z1 := (mu*horizon - dist) / (sigma * sqrtH)
	z2 := (-mu*horizon - dist) / (sigma * sqrtH)
	// The reflection term exp(expo)·Φ(z2) is Inf·0 for strong drift (expo huge,
	// z2 far in the left tail). Evaluate it in log-space so it underflows to 0
	// cleanly instead of producing NaN.
	term2 := math.Exp(expo + logNormCDF(z2))
	p := normCDF(z1) + term2
	if math.IsNaN(p) || p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// normCDF is the standard-normal CDF Φ(z) via the complementary error function.
func normCDF(z float64) float64 { return 0.5 * math.Erfc(-z/math.Sqrt2) }

// logNormCDF is log Φ(z), stable in the far-left tail where Φ(z) underflows to
// 0. For z ≥ −1 it takes the direct log; below that it uses the asymptotic
// log φ(z) − log(−z) (Mills-ratio leading term), which keeps the reflection
// term finite when combined with a large positive exponent.
func logNormCDF(z float64) float64 {
	if z >= -1 {
		return math.Log(normCDF(z))
	}
	const logSqrt2Pi = 0.9189385332046727 // 0.5·ln(2π)
	return -0.5*z*z - logSqrt2Pi - math.Log(-z)
}

// riskFloor maps a first-passage risk to the minimum mode it justifies on its
// own. Below riskWarnAt it imposes nothing (ModeNormal), so a calm session is
// governed purely by its level band.
func riskFloor(risk float64) Mode {
	switch {
	case risk >= riskCritAt:
		return ModeCritical
	case risk >= riskWarnAt:
		return ModeWarning
	default:
		return ModeNormal
	}
}

// EffectiveMode combines the level band ModeFor(pct) with the rate-driven risk
// floor: the governor acts on whichever is more urgent. With risk 0 (flat or
// unknown history) it is exactly ModeFor(pct) — backward compatible.
func EffectiveMode(pct int, risk float64) Mode {
	band := ModeFor(pct)
	if f := riskFloor(risk); f > band {
		return f
	}
	return band
}
