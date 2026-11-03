// SPDX-License-Identifier: MIT

package reliability

import (
	"errors"
	"sort"
)

// Key names one decision source's record in one calibration domain. A source
// can be honest on one kind of work and not on another, so the two never pool.
type Key struct {
	Source string `json:"source"`
	Domain string `json:"domain"`
}

// Tally is what was recorded under one key: the forecasts something has
// judged, and how many stated a probability that nothing has judged yet.
type Tally struct {
	Observations []Observation
	Unscored     int
}

// SourceReport is one key's verdict. Report is nil below MinObservations, with
// Need the verdicts still missing, or when the record is unusable, with Err why.
type SourceReport struct {
	Key
	Scored   int     `json:"scored"`
	Unscored int     `json:"unscored"`
	Need     int     `json:"need,omitempty"`
	Err      string  `json:"error,omitempty"`
	Report   *Report `json:"report,omitempty"`
}

// EvaluateSources scores every key on its own, so one broken source cannot hide
// the rest. Ordered by domain then source, so like work sits side by side.
func EvaluateSources(tallies map[Key]*Tally, bins int) []SourceReport {
	out := make([]SourceReport, 0, len(tallies))
	for key, tally := range tallies {
		sr := SourceReport{Key: key, Scored: len(tally.Observations), Unscored: tally.Unscored}
		rep, err := Evaluate(tally.Observations, bins)
		switch {
		case errors.Is(err, ErrTooFew):
			sr.Need = MinObservations - sr.Scored
		case err != nil:
			sr.Err = err.Error()
		default:
			sr.Report = &rep
		}
		out = append(out, sr)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].Source < out[j].Source
	})
	return out
}
