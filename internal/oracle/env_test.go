// SPDX-License-Identifier: MIT

//go:build !windows

package oracle

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// An oracle and a --check cmd: run model output, so they see no credential.
func TestCommandOracle_SeesNoCredential(t *testing.T) {
	probe := testutil.LeakProbe(t)
	for name, o := range map[string]*CommandOracle{
		"args":     {Args: []string{probe}},
		"template": {Template: probe + " {file}"},
	} {
		v, err := o.Verify(context.Background(), "answer", trust.Task{})
		if err != nil || !v.Passed {
			t.Errorf("%s: a credential reached the oracle: %+v %v", name, v, err)
		}
	}
}
