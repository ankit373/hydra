// SPDX-License-Identifier: MIT

package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// maxFileBytes bounds a dataset file; the largest pinned one is ~2 MB.
const maxFileBytes = 64 << 20

// LockEntry is one pinned file of a dataset.
type LockEntry struct {
	Name   string `toml:"name"`
	URL    string `toml:"url"`
	SHA256 string `toml:"sha256"`
}

// Dataset is one suite's block of bench/datasets.lock.
type Dataset struct {
	Revision string `toml:"revision"`
	Licence  string `toml:"licence"`
	// LicenceVerified is "<LICENSE url at the pinned commit>, <date>", empty when
	// nobody checked it; `hyctl bench table` refuses to publish such a suite.
	LicenceVerified string      `toml:"licence_verified"`
	Files           []LockEntry `toml:"file"`
}

// ReadLock parses the lockfile, refusing a key it does not know rather than
// silently pinning less than it says.
func ReadLock(path string) (map[string]Dataset, error) {
	var lock map[string]Dataset
	md, err := toml.DecodeFile(path, &lock)
	if err != nil {
		return nil, fmt.Errorf("bench: %s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("bench: %s: unknown key %s", path, u[0])
	}
	for name, ds := range lock {
		if ds.Revision == "" || len(ds.Files) == 0 {
			return nil, fmt.Errorf("bench: %s: suite %s needs a revision and at least one file", path, name)
		}
		for _, f := range ds.Files {
			if f.Name == "" || f.URL == "" || len(f.SHA256) != sha256.Size*2 || filepath.Base(f.Name) != f.Name {
				return nil, fmt.Errorf("bench: %s: suite %s has an incomplete file entry %+v", path, name, f)
			}
		}
	}
	return lock, nil
}

// Names lists the lock's suites in a stable order.
func Names(lock map[string]Dataset) []string {
	out := make([]string, 0, len(lock))
	for n := range lock {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Open reads one pinned file and re-hashes it, refusing a mismatch: a stale hash
// cache is how a corrupted file gets scored, and ~3 MB costs well under 100 ms.
func (ds Dataset) Open(dataDir, name string) ([]byte, error) {
	e, ok := ds.entry(name)
	if !ok {
		return nil, fmt.Errorf("bench: %s is not pinned in the lockfile", name)
	}
	path := filepath.Join(dataDir, name)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bench: %w (run `hyctl bench fetch`)", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("bench: %s is larger than %d bytes", path, maxFileBytes)
	}
	if got := sha256Hex(b); got != e.SHA256 {
		return nil, fmt.Errorf("bench: %s has sha256 %s, the lockfile pins %s", path, got, e.SHA256)
	}
	return b, nil
}

// Hashes is file name → pinned sha256, as the breadcrumb records it.
func (ds Dataset) Hashes() map[string]string {
	out := make(map[string]string, len(ds.Files))
	for _, f := range ds.Files {
		out[f.Name] = f.SHA256
	}
	return out
}

func (ds Dataset) entry(name string) (LockEntry, bool) {
	for _, f := range ds.Files {
		if f.Name == name {
			return f, true
		}
	}
	return LockEntry{}, false
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// FileSHA256 hashes a whole file, for the split file and the run's breadcrumb.
func FileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}

// RequireFiles refuses a lock block that does not pin exactly the files a suite reads.
func (ds Dataset) RequireFiles(suite string, names ...string) error {
	var have []string
	for _, f := range ds.Files {
		have = append(have, f.Name)
	}
	sort.Strings(have)
	want := append([]string(nil), names...)
	sort.Strings(want)
	if strings.Join(have, ",") != strings.Join(want, ",") {
		return fmt.Errorf("bench: the lockfile pins %v for %s, the loader reads %v", have, suite, want)
	}
	return nil
}
