// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/cost"
)

func TestCostTailArgParsing(t *testing.T) {
	cliSandbox(t)
	seedCostLog(t, []cost.Row{{
		TS: "2026-10-05T12:00:00Z", Tier: 1, Enum: "CORE", Model: "m",
		Executor: "http", Pool: "api", PromptTokens: 1, ResponseTokens: 1,
		EstCostUSD: 0.01, WallMS: 10,
	}})

	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "default", args: []string{"cost", "tail"}},
		{name: "three", args: []string{"cost", "tail", "3"}},
		{name: "zero", args: []string{"cost", "tail", "0"}},
		{name: "negative", args: []string{"cost", "tail", "--", "-5"}},
		{name: "abc", args: []string{"cost", "tail", "abc"}, wantErr: `"abc" is not a number of rows`},
		{name: "5x", args: []string{"cost", "tail", "5x"}, wantErr: `"5x" is not a number of rows`},
		{name: "3.9", args: []string{"cost", "tail", "3.9"}, wantErr: `"3.9" is not a number of rows`},
		{name: "empty", args: []string{"cost", "tail", ""}, wantErr: `"" is not a number of rows`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cobraOut, err := run(t, tc.args...)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("args %v: unexpected error %v (%s)", tc.args, err, cobraOut)
				}
				return
			}
			if err == nil {
				t.Fatalf("args %v: want error containing %q", tc.args, tc.wantErr)
			}
			msg := err.Error() + cobraOut
			if !strings.Contains(msg, tc.wantErr) {
				t.Fatalf("args %v: error %q does not contain %q", tc.args, msg, tc.wantErr)
			}
			if !strings.Contains(msg, "cost tail:") {
				t.Fatalf("args %v: error %q missing cost tail: prefix", tc.args, msg)
			}
		})
	}
}
