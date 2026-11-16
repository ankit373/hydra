// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ankit373/hydra/internal/checkgen"
	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
)

// chunks is --schema's field list split for --max-fields; one chunk or none means no split.
func (g genChecks) chunks(max int) [][]checkgen.Field { return checkgen.ChunkFields(g.schema, max) }

type dispatchFunc func(ctx context.Context, prompt string, opts dispatch.Options) (*dispatch.Result, error)

// chunkedDispatch asks for a wide schema one chunk at a time, every chunk on the head that
// answered the first, and merges the objects. The result's cost and tokens are the chunks' sum.
// problems names a chunk that did not answer with an object and a key two chunks both gave.
func chunkedDispatch(ctx context.Context, chat dispatchFunc, prompt string, opts dispatch.Options, chunks [][]checkgen.Field) (res *dispatch.Result, problems []string, err error) {
	parts := make([]map[string]any, 0, len(chunks))
	var usage executor.Response
	for i, chunk := range chunks {
		r, err := chat(ctx, checkgen.ChunkPrompt(prompt, chunk), opts)
		if err != nil {
			return nil, nil, fmt.Errorf("chunk %d of %d: %w", i+1, len(chunks), err)
		}
		if res == nil {
			res, opts.Head = r, r.Head.ID
		} else {
			res.CostUSD += r.CostUSD
		}
		if r.Response != nil {
			usage.InputTokens += r.InputTokens
			usage.OutputTokens += r.OutputTokens
			usage.Duration += r.Duration
		}
		obj, ok := checkgen.DecodeObject(r.Output)
		if !ok {
			problems = append(problems, fmt.Sprintf("chunk %d did not answer with a JSON object", i+1))
		}
		parts = append(parts, obj) // nil for a chunk that failed, so conflicts keep chunk numbers
	}
	merged, conflicts := checkgen.MergeObjects(parts)
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if res.Response != nil {
		usage.TTFT = res.TTFT
	}
	res.Output, res.Response = string(out), &usage
	return res, append(problems, conflicts...), nil
}
