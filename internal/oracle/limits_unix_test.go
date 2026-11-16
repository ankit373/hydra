// SPDX-License-Identifier: MIT

//go:build !windows

package oracle

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/sandbox"
	"github.com/ankit373/hydra/internal/trust"
)

// TestMain lets this test binary stand in for hyctl's rlimit wrapper, since
// sandbox.WithLimits re-executes os.Executable() to apply the limits.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.RlimitExecArg {
		fmt.Fprintln(os.Stderr, sandbox.RunRlimitExec(os.Args[3:]))
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// A CPU cap ends a spinning verifier as a failed verdict, long before the deadline.
func TestCommandOracle_CPUSecondsCapsTheCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	o := &CommandOracle{Args: []string{"sh", "-c", "while :; do :; done"}, CPUSeconds: 1}
	if v, err := o.Verify(ctx, "", trust.Task{}); err != nil || v.Passed {
		t.Fatalf("got %+v, %v; want the CPU limit to end it as a fail", v, err)
	}
}
