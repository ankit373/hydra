// SPDX-License-Identifier: MIT

package sandbox

import (
	_ "embed"
	"fmt"
	"os/exec"
)

// NetworkMethod names how Offline cuts the network here.
const NetworkMethod = "sandbox-exec with a profile that denies network*"

const sandboxExec = "/usr/bin/sandbox-exec"

//go:embed offline.sb
var offlineProfile string

func isolate(cmd *exec.Cmd) {
	cmd.Args = append([]string{sandboxExec, "-p", offlineProfile, cmd.Path}, cmd.Args[1:]...)
	cmd.Path = sandboxExec
}

// probeIsolation applies the profile once. It fails when sandbox-exec is
// gone or the caller is already sandboxed, which forbids a nested profile.
func probeIsolation() error {
	cmd := exec.Command("/usr/bin/true")
	isolate(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sandbox-exec: %v: %s", err, out)
	}
	return nil
}
