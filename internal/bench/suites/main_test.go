// SPDX-License-Identifier: MIT

package suites

import (
	"fmt"
	"os"
	"testing"

	"github.com/ankit373/hydra/internal/sandbox"
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
