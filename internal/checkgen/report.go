// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"

	"github.com/ankit373/hydra/internal/ground"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
)

// Report is the per-sentence trace as JSON. Scripts read it, so its shape is a contract: bump V to change it.
type Report struct {
	V              int              `json:"v"`
	Sentences      []ReportSentence `json:"sentences"`
	Supported      int              `json:"supported"`
	NotSupported   int              `json:"not_supported"`
	NothingToCheck int              `json:"nothing_to_check"`
}

// ReportSentence is one sentence of a Report.
type ReportSentence struct {
	N           int            `json:"n"`
	Text        string         `json:"text"`
	Status      string         `json:"status"`
	Unsupported []ground.Claim `json:"unsupported"`
	Sources     []ReportSource `json:"sources"`
}

// ReportSource is a span's block and lines; byte offsets stay out of the contract.
type ReportSource struct {
	Label     string `json:"label"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// NewReport builds the Report for a run's verdicts, with empty lists rather than null.
func NewReport(vs []SentenceVerdict) Report {
	r := Report{V: 1, Sentences: make([]ReportSentence, 0, len(vs))}
	for _, v := range vs {
		s := ReportSentence{N: v.N, Text: v.Text, Status: v.Status,
			Unsupported: append([]ground.Claim{}, v.Unsupported...), Sources: make([]ReportSource, 0, len(v.Sources))}
		for _, sp := range v.Sources {
			s.Sources = append(s.Sources, ReportSource{Label: sp.Label, StartLine: sp.StartLine, EndLine: sp.EndLine})
		}
		r.Sentences = append(r.Sentences, s)
		switch v.Status {
		case StatusSupported:
			r.Supported++
		case StatusNotSupported:
			r.NotSupported++
		default:
			r.NothingToCheck++
		}
	}
	return r
}

// GroundBySentence runs the claims check's ground variant on answer, so a caller gets each sentence's verdict.
func GroundBySentence(ctx context.Context, prompt, answer string) ([]SentenceVerdict, oracle.Verdict, error) {
	o := &claimsOracle{check: groundSentence(prompt)}
	v, err := o.Verify(ctx, answer, trust.Task{})
	return o.Last, v, err
}
