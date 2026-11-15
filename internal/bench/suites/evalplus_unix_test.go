// SPDX-License-Identifier: MIT

//go:build !windows

package suites

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvalPlus_PassingSolutionPasses(t *testing.T) {
	s, it := loadEvalPlus(t)
	answer := "Here it is:\n```python\ndef add(a, b):\n    return a + b\n```"
	for name, run := range map[string]func() (bool, error){
		"grade": func() (bool, error) { g, err := s.Grade(t.Context(), it, answer); return g.Passed, err },
		"base":  func() (bool, error) { g, err := s.BaseTests(t.Context(), it, answer); return g.Passed, err },
	} {
		if ok, err := run(); err != nil || !ok {
			t.Errorf("%s: passed=%v, %v", name, ok, err)
		}
	}
}

func TestEvalPlus_BaseOnlySolutionFailsPlus(t *testing.T) {
	s, it := loadEvalPlus(t)
	answer := "def add(a, b):\n    return abs(a) + abs(b)\n" // right only on non-negative inputs
	base, err := s.BaseTests(t.Context(), it, answer)
	if err != nil || !base.Passed {
		t.Fatalf("base: %+v, %v", base, err)
	}
	plus, err := s.Grade(t.Context(), it, answer)
	if err != nil || plus.Passed || plus.Timeout || !strings.Contains(plus.Detail, "wrong output") {
		t.Fatalf("plus: %+v, %v; want a plain fail", plus, err)
	}
}

func TestEvalPlus_TimeoutCountedApart(t *testing.T) {
	s, it := loadEvalPlus(t)
	s.deadline = 5 * time.Second
	for name, body := range map[string]string{
		"the runner's limit": "    while True:\n        pass\n",
		"a swallowed alarm":  "    import time\n    while True:\n        try:\n            time.sleep(1)\n        except BaseException:\n            pass\n",
		// This loop never lets Python run the alarm handler, so the deadline ends it.
		"the deadline": "    while True:\n        try:\n            pass\n        except BaseException:\n            pass\n",
	} {
		g, err := s.Grade(t.Context(), it, "def add(a, b):\n"+body)
		if err != nil || g.Passed || !g.Timeout || !strings.HasPrefix(g.Detail, "timeout:") {
			t.Errorf("%s: got %+v, %v; want a fail marked as a timeout", name, g, err)
		}
	}
}

func TestEvalPlus_NoNetwork(t *testing.T) {
	s, it := loadEvalPlus(t)
	escape := filepath.Join(t.TempDir(), "escaped.txt")
	for name, body := range map[string]string{
		"socket":     "    import socket\n    socket.socket().connect(('127.0.0.1', 9))\n    return a + b\n",
		"dns":        "    import socket\n    socket.getaddrinfo('example.com', 80)\n    return a + b\n",
		"subprocess": "    import subprocess\n    subprocess.run(['true'])\n    return a + b\n",
		"ctypes":     "    import ctypes\n    return a + b\n",
		"write":      "    open(" + "'" + escape + "'" + ", 'w').write('x')\n    return a + b\n",
	} {
		g, err := s.Grade(t.Context(), it, "def add(a, b):\n"+body)
		if err != nil || g.Passed || g.Timeout || !strings.Contains(g.Detail, "sandbox:") {
			t.Errorf("%s: got %+v, %v; want a fail from the sandbox", name, g, err)
		}
	}
	if _, err := os.Stat(escape); !os.IsNotExist(err) {
		t.Errorf("a solution wrote outside its directory: %v", err)
	}
}

// A grader that cannot run says so; it never reads as a failed answer.
func TestEvalPlus_NoVerdictIsAnErrorNotAFail(t *testing.T) {
	s, it := loadEvalPlus(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if g, err := s.Grade(ctx, it, "def add(a, b):\n    return a + b\n"); err == nil {
		t.Errorf("a cancelled grade returned %+v", g)
	}
	t.Setenv("PATH", t.TempDir())
	if g, err := s.Grade(t.Context(), it, "def add(a, b):\n    return a + b\n"); err == nil || !strings.Contains(err.Error(), "python3") {
		t.Errorf("no python3 gave %+v, %v", g, err)
	}
}
