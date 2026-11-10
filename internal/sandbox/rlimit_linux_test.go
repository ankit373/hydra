// SPDX-License-Identifier: MIT

//go:build linux

// Linux only: darwin applies no memory limit, and its 1 MB ARG_MAX refuses this environment.
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A large environment makes the exec's own string copies need fresh memory, which
// RLIMIT_AS refused once it applied: 0.35% of CI runs, 16 in 50 at this size before the fix.
func TestRunRlimitExec_LargeEnvironment(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestRlimitExecHelperProcess$")
	cmd.Env = append(os.Environ(),
		"HYDRA_TEST_RLIMIT_HELPER=1",
		"HYDRA_RLIMIT_MEM_MB=64",
	)
	for i := 0; i < 16; i++ {
		cmd.Env = append(cmd.Env, fmt.Sprintf("HYDRA_TEST_PAD_%d=%s", i, strings.Repeat("x", 64<<10)))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper process failed: %v, output: %.300s", err, out)
	}
}
