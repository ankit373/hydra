// SPDX-License-Identifier: MIT

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// NetworkMethod names how Offline cuts the network here.
const NetworkMethod = "a network namespace of its own"

// isolate gives the child a new user and network namespace. The user
// namespace is what lets an unprivileged user create the network one.
func isolate(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	a := cmd.SysProcAttr
	a.Cloneflags |= syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET
	a.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}}
	a.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}
	a.GidMappingsEnableSetgroups = false
}

// probeIsolation clones into the namespaces and execs a path that cannot
// exist: ENOENT comes only from execve, so every step before it worked.
func probeIsolation() error {
	cmd := &exec.Cmd{Path: "/nonexistent/hydra-netns-probe", Args: []string{"hydra-netns-probe"}}
	isolate(cmd)
	err := cmd.Start()
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	return err
}
