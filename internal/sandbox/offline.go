// SPDX-License-Identifier: MIT

package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
)

// ErrNoNetworkIsolation means the network could not be turned off here, so
// the command must not run: report "not checked", never a pass.
var ErrNoNetworkIsolation = errors.New("no network isolation")

var probeOnce = sync.OnceValue(probeIsolation)

// NetworkIsolation reports whether Offline can cut the network on this
// machine, and why not. Probed once per process.
func NetworkIsolation() error {
	if err := probeOnce(); err != nil {
		return fmt.Errorf("%w: %v", ErrNoNetworkIsolation, err)
	}
	return nil
}

// Offline makes cmd run with no network, loopback included: a network
// namespace on Linux, a sandbox profile on macOS. Elsewhere, or when the
// probe fails, it leaves cmd untouched and returns ErrNoNetworkIsolation.
func Offline(cmd *exec.Cmd) error {
	if err := NetworkIsolation(); err != nil {
		return err
	}
	isolate(cmd)
	return nil
}
