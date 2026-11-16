// SPDX-License-Identifier: MIT

//go:build linux || darwin

package sandbox

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

const (
	dialHelperEnv = "HYDRA_TEST_DIAL_ADDR"
	secretEnv     = "HYDRA_TEST_SECRET_TOKEN"
)

// TestMain lets this binary stand in for hyctl's rlimit wrapper and for a
// generated test that tries to dial out.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == RlimitExecArg {
		fmt.Fprintln(os.Stderr, RunRlimitExec(os.Args[3:]))
		os.Exit(1)
	}
	if addr := os.Getenv(dialHelperEnv); addr != "" {
		dialHelper(addr)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// dialHelper prints what a child could do: reach addr, see a credential, and
// its CPU limit.
func dialHelper(addr string) {
	dial := "ok"
	if c, err := net.Dial("tcp", addr); err != nil {
		dial = err.Error()
	} else {
		c.Close()
	}
	_, secret := os.LookupEnv(secretEnv)
	var cpu syscall.Rlimit
	_ = syscall.Getrlimit(syscall.RLIMIT_CPU, &cpu)
	fmt.Printf("dial=%s\nsecret=%t\ncpu=%d\n", dial, secret, cpu.Cur)
}

func runDialHelper(t *testing.T, addr string, offline bool) map[string]string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := Confine(exec.CommandContext(t.Context(), self), nil)
	cmd.Env = append(cmd.Env, dialHelperEnv+"="+addr)
	if offline {
		if err := WithLimits(cmd, 0, 60); err != nil {
			t.Fatal(err)
		}
		if err := Offline(cmd); err != nil {
			t.Fatal(err)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v: %s", err, out)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		got[k] = v
	}
	return got
}

// The listener is on the host's loopback, so a dial that fails proves the
// child sees no host network at all, not just no internet.
func TestOffline_ConfinedCommandCannotDialOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Setenv(secretEnv, "not-a-real-secret")
	addr := ln.Addr().String()

	if got := runDialHelper(t, addr, false); got["dial"] != "ok" {
		t.Fatalf("control: an unconfined dial to %s failed (%q), so the test proves nothing", addr, got["dial"])
	}

	if err := NetworkIsolation(); err != nil {
		cmd := exec.Command("/bin/echo", "hi")
		path, args := cmd.Path, append([]string{}, cmd.Args...)
		if got := Offline(cmd); !errors.Is(got, ErrNoNetworkIsolation) || cmd.Path != path || !reflect.DeepEqual(cmd.Args, args) {
			t.Fatalf("Offline = %v and cmd %q %v; want a refusal that leaves cmd alone", got, cmd.Path, cmd.Args)
		}
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatalf("CI must prove the dial fails, but isolation is unavailable: %v", err)
		}
		return
	}

	got := runDialHelper(t, addr, true)
	if got["dial"] == "ok" {
		t.Errorf("an offline command reached %s on the host", addr)
	}
	if got["secret"] != "false" {
		t.Errorf("an offline command saw %s; credentials must stay stripped", secretEnv)
	}
	if got["cpu"] != "60" {
		t.Errorf("an offline command had a CPU limit of %s, want 60", got["cpu"])
	}
}
