// SPDX-License-Identifier: MIT

//go:build linux

// Linux only: darwin applies no memory limit, and its 1 MB ARG_MAX refuses this environment.
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// A 1 MB environment still execs, and the child still sees the memory cap.
func TestRunRlimitExec_LargeEnvironment(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestRlimitExecHelperProcess$")
	cmd.Env = append(os.Environ(),
		"HYDRA_TEST_RLIMIT_HELPER=1",
		"HYDRA_RLIMIT_MEM_MB="+strconv.Itoa(helperMemMB),
	)
	for i := 0; i < 16; i++ {
		cmd.Env = append(cmd.Env, fmt.Sprintf("HYDRA_TEST_PAD_%d=%s", i, strings.Repeat("x", 64<<10)))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v, output: %.300s", err, out)
	}
	wantChildLimits(t, out, helperMemMB, "unlimited")
}

// A cap far below the wrapper's own address space must still reach the child: the
// wrapper maps more than 16 MB, so any mapping it made after the cap would kill it.
func TestRunRlimitExec_CapBelowWrapperSize(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestRlimitExecHelperProcess$")
	cmd.Env = append(os.Environ(), "HYDRA_TEST_RLIMIT_HELPER=1", "HYDRA_RLIMIT_MEM_MB=16")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v, output: %.300s", err, out)
	}
	wantChildLimits(t, out, 16, "unlimited")
}
