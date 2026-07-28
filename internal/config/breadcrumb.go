// SPDX-License-Identifier: MIT

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// BreadcrumbFiles are the registry files that define this deployment's
// routing behavior; their combined bytes are the deployment's identity.
var BreadcrumbFiles = []string{"routing.yaml", "models.yaml", "domains.yaml"}

// Breadcrumb is a SHA256 hex fingerprint of the registry files that define
// this Hydra deployment's routing behavior. It lets ledger/trust/cost log
// entries be tied back to the exact routing rules in effect when they were
// written, and lets logs from different machines — or from before/after a
// registry edit — be told apart.
//
// Deliberately not memoized: it is read once per logged event (never inside a
// loop — the swarm writer hoists it), the registry is ~36 KB, and a
// process-lifetime cache would serve a stale fingerprint to the long-running
// TUI after `hyctl init` or a registry edit. Freshness is worth more than the
// microseconds. Revisit only with a profile that says otherwise.
func Breadcrumb() (string, error) {
	dir := filepath.Join(ScriptHome(), "registry")
	h := sha256.New()
	for _, name := range BreadcrumbFiles {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
