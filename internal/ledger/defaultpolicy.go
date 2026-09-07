// SPDX-License-Identifier: MIT

package ledger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Classification strings are duplicated as literals rather than imported from
// internal/mcpregistry, which imports this package. They are part of the
// on-disk policy format either way, so the file is the contract, not the
// constant.
const (
	classQuarantined    = "mcp-quarantined"
	classFlagged        = "mcp-flagged"
	classBehaviorChange = "mcp-behavior-change"
)

// DefaultPolicy is what a fresh install starts with.
//
// It is deliberately default-allow. The classifications that would justify a
// default-deny (file provenance, egress sensitivity) do not exist yet, and a
// deny that stops every dispatch gets uninstalled rather than tuned. What it
// does do is refuse what Hydra can already prove, and exist on disk so the
// vocabulary is visible and editable instead of implied by a missing file.
//
// FailOpen stays true under this policy and hyctl security withholds the
// coverage score because of it. That is the intended reading: nothing is
// being enforced at the egress boundary yet.
func DefaultPolicy() Policy {
	return Policy{
		Default: Allow,
		Rules: []Rule{
			// A quarantined server is one a confirmed finding took out of
			// service. Quarantine has no automatic exit, so this is the one
			// classification where denying outright is unambiguous.
			{Classification: classQuarantined, Decision: Deny, Framework: "owasp:llm03"},
			{Classification: classFlagged, Decision: Ask, Framework: "owasp:llm03"},
			{Classification: classBehaviorChange, Decision: Ask, Framework: "owasp:llm03"},
		},
	}
}

// EnsurePolicy writes DefaultPolicy to path when nothing is there yet,
// reporting whether it created the file.
//
// It never overwrites. An operator's tuned policy outranks a shipped default,
// and silently replacing a file whose whole job is to say no is the worst
// available behaviour. A file that exists but does not parse is also left
// alone: LoadPolicy already refuses to run on it, which is the loud failure
// this should not paper over.
func EnsurePolicy(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	raw, err := json.MarshalIndent(DefaultPolicy(), "", "  ")
	if err != nil {
		return false, err
	}
	// Temp-then-rename so a crash mid-write cannot leave a half-parsed policy,
	// which LoadPolicy would reject and take the whole gate down with it.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}
