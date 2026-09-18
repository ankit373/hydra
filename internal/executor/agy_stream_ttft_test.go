//go:build !windows

// SPDX-License-Identifier: MIT

package executor

import (
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/testutil"
)

// Build-tagged rather than skipped: the fixture needs a real `sleep` to put
// time between when output arrives and when the process exits, and the suite's
// skip budget exists to stop exactly that being papered over.

// agyHoldSeconds is how long the fake keeps running after printing. Long enough
// that the gap between arrival and release cannot be confused with scheduling
// noise, short enough not to slow the suite.
const agyHoldSeconds = 1

// slowAgy prints one line, waits, then exits. Fewer than agyAuthLines newlines,
// so the gate can only settle at exit, which is what separates the arrival
// moment from the release moment. /bin/sleep by absolute path: the sandbox's
// PATH is its own bin directory, and a bare `sleep` is simply not found, which
// is how the first version of this fixture failed to delay at all.
func slowAgy(t *testing.T, s *testutil.Sandbox) {
	t.Helper()
	s.FakeBinary(t, "agy", "#!/bin/sh\nprintf 'the answer\\n'\n/bin/sleep 1\nexit 0\n")
}

// TTFT must be when output arrived, not when the gate let it through. The
// hold-back is Hydra's own latency, and charging it to the model would make
// every agy call look a second slower than it is.
//
// The first version of this test used the ordinary fake, which prints and exits
// at once: arrival and release were microseconds apart, so it passed with TTFT
// read straight off the sink and the mutation went uncaught.
func TestAgyStream_TTFTIsArrivalNotRelease(t *testing.T) {
	s := testutil.NewSandbox(t)
	slowAgy(t, s)

	_, resp, err := collect(t, Request{Prompt: "hi", Head: agyHead("gemini-3-pro")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TTFT <= 0 {
		t.Fatalf("TTFT = %v, want a real measurement", resp.TTFT)
	}
	// The fixture has to have actually delayed, or the comparison below holds
	// for the wrong reason and the test is decorative.
	if resp.Duration < agyHoldSeconds*time.Second {
		t.Fatalf("Duration = %v, under the %ds the fake holds for: the fixture did not "+
			"delay, so this proves nothing", resp.Duration, agyHoldSeconds)
	}
	// The **gap**, not a ratio of the duration. TTFT legitimately includes
	// process spawn, so on a loaded machine it is a large fraction of a run
	// that spent most of its time starting up, and `TTFT > Duration/2` duly
	// failed once in a full-suite run at 7.53s while passing 5/5 alone. What is
	// invariant is that the fake keeps running for a second *after* it prints:
	// measured at arrival that second falls after TTFT, measured at release it
	// falls before, whatever the spawn cost was.
	const floor = agyHoldSeconds*time.Second - 100*time.Millisecond
	if gap := resp.Duration - resp.TTFT; gap < floor {
		t.Errorf("Duration %v minus TTFT %v is %v, under the %v the fake held for after "+
			"printing: TTFT is the release moment rather than the arrival one, so the "+
			"hold-back is being charged to the model", resp.Duration, resp.TTFT, gap, floor)
	}
}
