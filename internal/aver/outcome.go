// SPDX-License-Identifier: MIT

package aver

import "fmt"

// The two ways an answer is certified. Anything else is not verified.
const (
	ModeIndependence  = "under independence" // product of e-values, with its co-failure gate
	ModeAnyDependence = "any dependence"     // joint pass pattern, valid however checks relate
)

// Why an answer is not verified.
const (
	ReasonTooFewWrong = "too few labelled wrong answers"
	ReasonFutile      = "futility"
	ReasonNoCheck     = "no check for this kind"
)

// Outcome is what certified acceptance decided for one answer and the numbers
// behind it. Alpha bounds the chance of accepting a wrong answer, per task.
type Outcome struct {
	Mode    string  `json:"mode,omitempty"`
	Alpha   float64 `json:"alpha"`
	Bar     float64 `json:"bar,omitempty"`
	Product float64 `json:"product,omitempty"`
	Steps   []Step  `json:"steps,omitempty"`
	// StepsOmitted counts steps cut to keep the outcome inside one log line.
	StepsOmitted int    `json:"steps_omitted,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Have         int    `json:"have,omitempty"`
	Need         int    `json:"need,omitempty"`
}

// Certified says the answer cleared its bar in one of the two modes.
func (o Outcome) Certified() bool { return o.Mode != "" }

// Line is the outcome in one line, with its reason when not verified.
func (o Outcome) Line() string {
	if o.Certified() {
		return fmt.Sprintf("certified (%s) at alpha %g: product %.3g >= bar %.3g", o.Mode, o.Alpha, o.Product, o.Bar)
	}
	switch o.Reason {
	case ReasonTooFewWrong:
		return fmt.Sprintf("not verified: %d labelled wrong answers, need %d", o.Have, o.Need)
	case ReasonFutile:
		return fmt.Sprintf("not verified: futility, product %.3g cannot reach bar %.3g", o.Product, o.Bar)
	}
	return "not verified: " + o.Reason
}
