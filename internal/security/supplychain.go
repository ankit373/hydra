// SPDX-License-Identifier: MIT

package security

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
)

// Did the head you approved stay the head you run?
//
// The rug-pull pattern is a tool you vetted changing underneath you with no
// re-approval and no alert. For CLI-sourced heads Hydra already knows the
// resolved binary path, so the local form of that attack — an agent binary
// silently replaced or auto-updated — is detectable by fingerprinting it and
// noticing when the fingerprint moves.
//
// This is change detection, not provenance: it cannot tell a legitimate
// upgrade from a malicious swap, and it verifies nothing about where the
// binary came from. What it does is make the change *visible*, which is
// exactly what the pattern relies on not happening.
//
// Cost drove the design. Real agent binaries here are 150-270 MB and hash at
// ~500 ms each, which is unusable in a view the desktop polls every few
// seconds. A stat is ~1.6 µs. So the stored fingerprint is keyed on
// (size, mtime) and the content is only re-read when one of those moves —
// first run pays once per binary, every later run pays a stat. The honest
// limit of that trade is recorded in the check's own wording: a substitution
// that preserves both size and mtime is not re-hashed, so it is not seen.

// binaryStorePath is where head fingerprints persist.
func binaryStorePath() string {
	return filepath.Join(config.Dir(), "head_binaries.json")
}

// binaryRecord is one head binary as last observed.
type binaryRecord struct {
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	ModTime   string `json:"modTime"`
	FirstSeen string `json:"firstSeen"`
}

// HeadBinary is one CLI head's binary and whether it moved since last seen.
type HeadBinary struct {
	HeadID string `json:"headId"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// New is a binary with no prior fingerprint — a baseline, not a finding.
	New bool `json:"new,omitempty"`
	// Changed means the content hash differs from the stored one.
	Changed bool `json:"changed,omitempty"`
	// Previous is the hash this replaced, when Changed.
	Previous  string `json:"previous,omitempty"`
	FirstSeen string `json:"firstSeen,omitempty"`
}

// SupplyChain is the fingerprint state of every CLI head binary.
type SupplyChain struct {
	Binaries []HeadBinary `json:"binaries,omitempty"`
	New      int          `json:"new"`
	Changed  int          `json:"changed"`
	// Unfingerprintable counts heads whose binary could not be read at all.
	Unfingerprintable int `json:"unfingerprintable,omitempty"`
}

// FingerprintHeads hashes each CLI head's binary, compares it against the
// stored fingerprint, and persists the new state. Heads with no executable
// (API-key and port-sourced providers) are skipped — there is no local
// artifact to fingerprint.
func FingerprintHeads(heads []provider.Head) SupplyChain {
	store := loadBinaryStore(binaryStorePath())
	now := time.Now().UTC().Format(time.RFC3339)
	var sc SupplyChain

	for _, h := range heads {
		if h.Executable == "" {
			continue
		}
		info, err := os.Stat(h.Executable)
		if err != nil || info.IsDir() {
			sc.Unfingerprintable++
			continue
		}
		size, mod := info.Size(), info.ModTime().UTC().Format(time.RFC3339)
		prev, known := store[h.Executable]

		// The cheap fingerprint matched, so the content is taken to be
		// unchanged and not re-read. This is the documented limit above.
		if known && prev.Size == size && prev.ModTime == mod && prev.SHA256 != "" {
			sc.Binaries = append(sc.Binaries, HeadBinary{
				HeadID: h.ID, Path: h.Executable, SHA256: prev.SHA256,
				Size: size, FirstSeen: prev.FirstSeen,
			})
			continue
		}

		sum, err := hashFile(h.Executable)
		if err != nil {
			sc.Unfingerprintable++
			continue
		}
		hb := HeadBinary{HeadID: h.ID, Path: h.Executable, SHA256: sum, Size: size}
		switch {
		case !known:
			hb.New, hb.FirstSeen = true, now
			sc.New++
		case prev.SHA256 != sum:
			hb.Changed, hb.Previous, hb.FirstSeen = true, prev.SHA256, prev.FirstSeen
			sc.Changed++
		default:
			// Size or mtime moved but the content did not — a touch or a
			// reinstall of the same build. Not a finding.
			hb.FirstSeen = prev.FirstSeen
		}
		if hb.FirstSeen == "" {
			hb.FirstSeen = now
		}
		store[h.Executable] = binaryRecord{SHA256: sum, Size: size, ModTime: mod, FirstSeen: hb.FirstSeen}
		sc.Binaries = append(sc.Binaries, hb)
	}

	sort.Slice(sc.Binaries, func(i, j int) bool { return sc.Binaries[i].HeadID < sc.Binaries[j].HeadID })
	saveBinaryStore(binaryStorePath(), store)
	return sc
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// loadBinaryStore reads the fingerprint store; anything unreadable yields an
// empty store, which re-baselines rather than failing the report.
func loadBinaryStore(path string) map[string]binaryRecord {
	out := map[string]binaryRecord{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	if json.Unmarshal(raw, &out) != nil {
		return map[string]binaryRecord{}
	}
	return out
}

// saveBinaryStore persists best-effort: failing to record a fingerprint must
// never fail the security report it was gathered for.
func saveBinaryStore(path string, store map[string]binaryRecord) {
	raw, err := json.Marshal(store)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}

// supplyChainCheck reports head-binary integrity.
func supplyChainCheck(sc SupplyChain) Check {
	const name = "Head binary integrity"
	if len(sc.Binaries) == 0 {
		return Check{Name: name, Status: "no local binaries",
			Detail: "no CLI-sourced head was discovered, so there is no local artifact to fingerprint"}
	}
	if sc.Changed > 0 {
		return Check{Name: name, Status: fmt.Sprintf("%d binary(ies) CHANGED", sc.Changed),
			Detail: changedBinarySummary(sc) + " — an agent binary was replaced since it was last seen; " +
				"an upgrade looks identical to a swap here, so confirm it was one"}
	}
	if sc.New == len(sc.Binaries) {
		return Check{Name: name, Status: fmt.Sprintf("%d baselined", sc.New),
			Detail: "first run: fingerprints recorded, so a later change to any of these binaries will be reported"}
	}
	return Check{Name: name, Status: fmt.Sprintf("%d unchanged", len(sc.Binaries)-sc.New),
		Detail: "no head binary has changed since its fingerprint was recorded; note that a replacement " +
			"preserving both size and mtime would not be re-hashed"}
}

func changedBinarySummary(sc SupplyChain) string {
	var names []string
	for _, b := range sc.Binaries {
		if b.Changed {
			names = append(names, b.HeadID)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
