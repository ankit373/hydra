// SPDX-License-Identifier: MIT

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ankit373/hydra/registry"
)

// BreadcrumbFiles are the registry files that define this deployment's
// routing behavior; their combined bytes are the deployment's identity.
// pricing.yaml is included because it drives cost-based routing decisions.
var BreadcrumbFiles = []string{"routing.yaml", "models.yaml", "domains.yaml", "pricing.yaml"}

var (
	breadcrumbCacheMu     sync.Mutex
	breadcrumbCacheKey    string
	breadcrumbCacheResult string
	breadcrumbCacheErr    error
	breadcrumbCached      bool
)

// breadcrumbFingerprint is a cheap (stat-only) signal for whether Breadcrumb
// needs to actually recompute: for each registry file, either its on-disk
// mtime+size (an operator's override, which genuinely can change under a
// long-running process like the TUI) or a fixed "embedded" marker (the
// compiled-in copy, which cannot change without a binary rebuild). This is
// what makes caching safe where a blind process-lifetime cache would not be:
// an edit to an on-disk override changes the fingerprint and forces a real
// recompute on the very next call, so freshness is never traded away.
func breadcrumbFingerprint() string {
	home := ScriptHome()
	var b strings.Builder
	for _, name := range BreadcrumbFiles {
		info, err := os.Stat(filepath.Join(home, "registry", name))
		if err != nil {
			b.WriteString(name + "|embedded\x00")
			continue
		}
		fmt.Fprintf(&b, "%s|%s|%d\x00", name, info.ModTime(), info.Size())
	}
	return b.String()
}

// Breadcrumb is a SHA256 hex fingerprint of the registry files that define
// this Hydra deployment's routing behavior. It lets ledger/trust/cost log
// entries be tied back to the exact routing rules in effect when they were
// written, and lets logs from different machines — or from before/after a
// registry edit — be told apart.
//
// Cached, keyed on breadcrumbFingerprint (mtime+size for an on-disk override,
// a fixed marker for the embedded fallback) rather than memoized forever: a
// dispatch fallback loop or swarm fan-out calls this once per candidate head
// for the identical registry state, and re-reading + re-hashing ~36 KB that
// many times over was pure waste, but an edit to an on-disk override must
// still invalidate on the very next call, which the fingerprint guarantees.
// Reads each file through registry.Read, so an installed binary fingerprints
// its embedded rules and an operator with on-disk overrides fingerprints those
// instead — which is the distinction the breadcrumb exists to record. Before
// #238 this read disk only and returned an error on every installed binary, so
// the fingerprint was silently absent from exactly the logs it was added for.
func Breadcrumb() (string, error) {
	key := breadcrumbFingerprint()

	breadcrumbCacheMu.Lock()
	if breadcrumbCached && breadcrumbCacheKey == key {
		result, err := breadcrumbCacheResult, breadcrumbCacheErr
		breadcrumbCacheMu.Unlock()
		return result, err
	}
	breadcrumbCacheMu.Unlock()

	result, err := computeBreadcrumb()

	breadcrumbCacheMu.Lock()
	breadcrumbCacheKey, breadcrumbCacheResult, breadcrumbCacheErr, breadcrumbCached = key, result, err, true
	breadcrumbCacheMu.Unlock()

	return result, err
}

func computeBreadcrumb() (string, error) {
	home := ScriptHome()
	h := sha256.New()
	for _, name := range BreadcrumbFiles {
		raw, err := registry.Read(home, name)
		if err != nil {
			return "", err
		}
		// Length-prefix each file with its name. Plain concatenation is
		// ambiguous: moving a line from the end of routing.yaml to the start of
		// models.yaml would leave the byte stream — and so the fingerprint —
		// unchanged, while being a materially different deployment.
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(raw))
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
