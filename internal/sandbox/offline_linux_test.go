// SPDX-License-Identifier: MIT

package sandbox

import (
	"os/exec"
	"syscall"
	"testing"
)

// Harden used to replace SysProcAttr, so Offline then Confine silently ran
// the command with the network on.
func TestHarden_KeepsOfflineNamespaces(t *testing.T) {
	cmd := exec.Command("/bin/true")
	isolate(cmd)
	Harden(cmd)
	a := cmd.SysProcAttr
	if want := uintptr(syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET); a.Cloneflags&want != want || !a.Setpgid {
		t.Fatalf("Cloneflags=%#x Setpgid=%t; want both namespaces and a process group", a.Cloneflags, a.Setpgid)
	}
}
