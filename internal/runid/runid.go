// SPDX-License-Identifier: MIT

// Package runid mints the identifiers that correlate a single logical unit of
// work across Hydra's logs.
//
// Two levels exist. A *run* is one user-facing invocation, `hyctl dispatch`,
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
// spawns into one run. Read as a fallback only, an explicit value always wins,
// because env is process-global and cannot distinguish concurrent runs inside a
// single long-lived host process.
const (
	EnvRunID  = "HYDRA_RUN_ID"
	EnvTaskID = "HYDRA_TASK_ID"
)

// randBytes is the width of New's random suffix.
//
// The timestamp only resolves to the second, so every ID minted inside the same
// second collides unless the suffix separates them, and a parallel batch or a
// swarm mints many in one second by design. At the original 3 bytes the
// birthday bound gave 0.7% for 500 IDs and 52% for 5000, which is not a
// theoretical risk: it silently merged two unrelated runs' rows in cost.jsonl,
// the exact correlation failure #181 set out to fix (#198). At 8 bytes, 10,000
// IDs in one second collide with probability ~3e-12.
const randBytes = 8

// New returns a fresh identifier, e.g. "20260801T104530Z-3f9c1a4b5c6d7e8f".
func New() string {
	var b [randBytes]byte
	// rand.Read never returns an error (it panics if the system source fails),
	// so there is no fallback path to write here.
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b[:]))
}

// ResolveRun returns the run ID to log: an explicit value, else HYDRA_RUN_ID,
// else a fresh one. It never returns empty, before this existed every log row
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
