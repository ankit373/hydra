// SPDX-License-Identifier: MIT

//go:build !linux && !darwin

package sandbox

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

// With no way to cut the network, the command must not run at all.
func TestOffline_RefusesWithoutIsolation(t *testing.T) {
	cmd := exec.Command("hyctl", "version")
	path, args := cmd.Path, append([]string{}, cmd.Args...)
	if err := Offline(cmd); !errors.Is(err, ErrNoNetworkIsolation) {
		t.Fatalf("Offline = %v, want ErrNoNetworkIsolation", err)
	}
	if cmd.Path != path || !reflect.DeepEqual(cmd.Args, args) || cmd.SysProcAttr != nil {
		t.Fatalf("Offline changed a command it refused: %q %q", cmd.Path, cmd.Args)
	}
}
