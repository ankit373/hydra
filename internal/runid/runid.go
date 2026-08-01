// SPDX-License-Identifier: MIT

// Package runid mints the identifiers that correlate a single logical unit of
// work across Hydra's logs.
//
// Two levels exist. A *run* is one user-facing invocation — `hyctl dispatch`,
// one `hyctl parallel` batch, one swarm. A *task* is one logical piece of work
// inside it; a run has one task in the simple case and several in a parallel
// batch. Every attempt a swarm or SPRT ensemble makes on the same task shares
// that task's ID, which is what lets a reader group the rows that belong
// together.
//
// IDs are timestamp-prefixed so they sort chronologically and are greppable in
// a jsonl file by eye, with random bytes so two processes starting in the same
// second cannot collide.
package runid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// Env vars an external orchestrator can set to group Hydra invocations it
// spawns into one run. Read as a fallback only — an explicit value always wins,
// because env is process-global and cannot distinguish concurrent runs inside a
// single long-lived host process.
const (
	EnvRunID  = "HYDRA_RUN_ID"
	EnvTaskID = "HYDRA_TASK_ID"
)

// New returns a fresh identifier, e.g. "20260801T104530Z-3f9c1a".
func New() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not a reason to lose correlation entirely;
		// the timestamp alone still groups a run in practice.
		return time.Now().UTC().Format("20060102T150405Z")
	}
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b[:]))
}

// ResolveRun returns the run ID to log: an explicit value, else HYDRA_RUN_ID,
// else a fresh one. It never returns empty — before this existed every log row
// carried run_id:"" and nothing could be correlated (#181).
func ResolveRun(explicit string) string { return resolve(explicit, EnvRunID) }

// ResolveTask is ResolveRun for the task level.
func ResolveTask(explicit string) string { return resolve(explicit, EnvTaskID) }

func resolve(explicit, envKey string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return New()
}
