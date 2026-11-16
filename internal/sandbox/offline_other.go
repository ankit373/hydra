// SPDX-License-Identifier: MIT

//go:build !linux && !darwin

package sandbox

import (
	"fmt"
	"os/exec"
	"runtime"
)

// NetworkMethod is empty: there is no way to cut the network here.
const NetworkMethod = ""

// isolate is never reached: probeIsolation always fails here.
func isolate(*exec.Cmd) {}

func probeIsolation() error {
	return fmt.Errorf("no way to turn the network off for a command on %s", runtime.GOOS)
}
