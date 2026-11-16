// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"log"
	"strings"

	"github.com/ankit373/hydra/internal/egress"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/policy"
)

// outgoing is what one head is sent. redacted says the text holds placeholders
// from the request's vault, so the answer must be restored.
type outgoing struct {
	prompt, system string
	msgs           []executor.Message
	parts          []egress.Part
	class          *policy.Classification
	redacted       bool
}

// redactOutgoing replaces every detected value with a vault placeholder. Parts
// are classified again, so the ledger and the egress gate see what leaves.
func redactOutgoing(o outgoing, v *policy.Vault, rules *egress.Rules) outgoing {
	o.prompt = policy.RedactReversible(o.prompt, v)
	o.system = policy.RedactReversible(o.system, v)
	if o.msgs != nil {
		msgs := make([]executor.Message, len(o.msgs))
		for i, m := range o.msgs {
			m.Content = policy.RedactReversible(m.Content, v)
			msgs[i] = m
		}
		o.msgs = msgs
	}
	parts := make([]egress.Part, len(o.parts))
	for i, p := range o.parts {
		if p.Content != "" {
			p = rules.ClassifyPart(egress.Part{
				Content: policy.RedactReversible(p.Content, v), Source: p.Source, Origin: p.Origin,
			})
		}
		parts[i] = p
	}
	o.parts = parts
	c := policy.Classify(o.prompt)
	o.class, o.redacted = &c, true
	return o
}

// restoreResponse puts the vault's values back into the answer and any tool
// call arguments. It returns the placeholders the head made up, and why the
// answer could not use a value it was given as a placeholder ("" when it could).
func restoreResponse(resp *executor.Response, v *policy.Vault) ([]string, string) {
	miss := cannotSee(resp.Output, v.Keys())
	var unknown []string
	resp.Output, unknown = v.Restore(resp.Output)
	for i := range resp.ToolCalls {
		var u []string
		resp.ToolCalls[i].Function.Arguments, u = v.Restore(resp.ToolCalls[i].Function.Arguments)
		unknown = append(unknown, u...)
	}
	if miss == "" && len(unknown) > 0 {
		miss = "the answer names " + strings.Join(unknown, ", ") + ", which Hydra never issued"
	}
	return unknown, miss
}

// cannotSeePhrases are how a model says a placeholder hid what it needed.
var cannotSeePhrases = []string{
	"cannot see", "can't see", "can not see", "unable to see", "not able to see",
	"cannot tell", "can't tell", "cannot determine", "can't determine",
	"redacted", "placeholder", "masked", "no way to know",
}

// cannotSee finds a sentence that names one of keys and says it cannot be read.
func cannotSee(answer string, keys []string) string {
	for _, sentence := range strings.FieldsFunc(answer, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n'
	}) {
		low := strings.ToLower(sentence)
		for _, k := range keys {
			if !strings.Contains(low, strings.ToLower(k)) {
				continue
			}
			for _, p := range cannotSeePhrases {
				if strings.Contains(low, p) {
					return "the answer says it cannot use <<" + k + ">>"
				}
			}
		}
	}
	return ""
}

// rerunLocal asks a local head the original request, when a hosted answer
// needed a value it only saw as a placeholder (#1884). A stream already sent
// cannot be taken back, so a streamed request is not rerun.
func (d *Dispatcher) rerunLocal(ctx context.Context, prompt string, opts Options, why string) (*Result, bool) {
	if opts.OnStream != nil {
		return nil, false
	}
	opts.LocalOnly, opts.Redact, opts.Vault, opts.Head, opts.TaskID = true, false, nil, "", ""
	r, err := d.Dispatch(ctx, prompt, opts)
	if err != nil {
		log.Printf("redaction: %s, and no local head could rerun it: %v", why, err)
		return nil, false
	}
	r.RedactionMiss = why
	return r, true
}
