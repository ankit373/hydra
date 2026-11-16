// SPDX-License-Identifier: MIT

package dispatch

import (
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
// call arguments, and returns the placeholders the head made up.
func restoreResponse(resp *executor.Response, v *policy.Vault) []string {
	var unknown []string
	resp.Output, unknown = v.Restore(resp.Output)
	for i := range resp.ToolCalls {
		var u []string
		resp.ToolCalls[i].Function.Arguments, u = v.Restore(resp.ToolCalls[i].Function.Arguments)
		unknown = append(unknown, u...)
	}
	return unknown
}
