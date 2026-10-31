// SPDX-License-Identifier: MIT

package runid

import "testing"

// The env vars exist so an external orchestrator can group the Hydra
// invocations it spawns into one run. That only works if the entry point
// *resolves* instead of minting: resolve ranks explicit > env > generated, so
// passing a freshly generated id as the "explicit" value silently outranks the
// env var and makes it dead (#204).
//
// This pins the precedence that the CLI depends on.
func TestResolve_GeneratedValueMustNotBePassedAsExplicit(t *testing.T) {
	t.Setenv(EnvRunID, "orchestrator-run")

	// What cmd/hydra used to do.
	minted := New()
	if got := ResolveRun(minted); got != minted {
		t.Fatalf("ResolveRun(minted) = %q, want the explicit value, precedence changed", got)
	}
	if minted == "orchestrator-run" {
		t.Fatal("New() collided with the env value; test is meaningless")
	}

	// What it must do instead.
	if got := ResolveRun(""); got != "orchestrator-run" {
		t.Errorf("ResolveRun(\"\") = %q, want the env value %q, HYDRA_RUN_ID is being ignored",
			got, "orchestrator-run")
	}
}

func TestResolveTask_HonoursEnvWhenNotOverridden(t *testing.T) {
	t.Setenv(EnvTaskID, "orchestrator-task")

	if got := ResolveTask(""); got != "orchestrator-task" {
		t.Errorf("ResolveTask(\"\") = %q, want %q", got, "orchestrator-task")
	}
}
