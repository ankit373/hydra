// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// looseDir is one state directory other users can reach, and how.
type looseDir struct {
	Path     string `json:"path"`
	Mode     string `json:"mode"`
	Writable bool   `json:"writable"`
}

// stateDirCheck reports whether Hydra's own state directory is reachable by
// other users on this machine.
//
// The report already says the head-binary baseline is forgeable by anything
// that can write ~/.hydra, and nothing checked whether anything can. Writable
// is the finding: head_binaries.json is the record that says no head changed,
// and mcp_ledger.jsonl.chainhash is the anchor that says nothing was removed
// from the ledger. Both are plain files (#928).
func stateDirCheck(root string) Check { return stateDirCheckOn(root, runtime.GOOS) }

func stateDirCheckOn(root, goos string) Check {
	c := Check{Name: "State directory reach"}
	// Windows carries no Unix mode bits, so the question is not answerable this
	// way there. Not evaluated, never a pass.
	if goos == "windows" {
		c.Status = "not evaluated"
		c.Detail = "Windows does not carry Unix mode bits, so this cannot be read from the mode alone"
		return c
	}
	if root == "" {
		c.Status = "not evaluated"
		c.Detail = "no state directory to examine"
		return c
	}

	loose, scanned, err := looseDirsUnder(root)
	if err != nil {
		c.Status = "not evaluated"
		c.Detail = fmt.Sprintf("%s could not be walked (%v), which is unchecked rather than clean", root, err)
		return c
	}
	if scanned == 0 {
		c.Status = "not evaluated"
		c.Detail = fmt.Sprintf("%s does not exist yet, so there is nothing to reach", root)
		return c
	}

	var writable, readable []string
	for _, d := range loose {
		if d.Writable {
			writable = append(writable, fmt.Sprintf("%s is %s", d.Path, d.Mode))
			continue
		}
		readable = append(readable, fmt.Sprintf("%s is %s", d.Path, d.Mode))
	}
	sort.Strings(writable)
	sort.Strings(readable)

	const caveat = ". Read from the mode alone, so a directory owned by another user, an ACL, " +
		"or a filesystem that does not carry Unix modes is outside what this saw"

	switch {
	case len(writable) > 0:
		c.Status = fmt.Sprintf("%d writable by others", len(writable))
		c.Detail = fmt.Sprintf("%s. Anything that can write here can replace head_binaries.json, "+
			"the baseline the integrity check compares against, and the ledger's chain anchor, so "+
			"both would then report what the writer chose%s",
			strings.Join(capped(writable, 3), "; "), caveat)
	case len(readable) > 0:
		c.Status = fmt.Sprintf("%d readable by others", len(readable))
		c.Detail = fmt.Sprintf("%s. The files themselves are not readable, so this leaks the "+
			"listing rather than the contents: which tasks are parked, how much was recorded, "+
			"when%s", strings.Join(capped(readable, 3), "; "), caveat)
	default:
		c.Status = "owner only"
		c.Detail = fmt.Sprintf("%d director(ies) under %s are reachable by their owner alone%s",
			scanned, root, caveat)
	}
	return c
}

// looseDirsUnder walks root and returns the directories group or others can
// reach, with how many were examined. Only directories are examined: a
// directory's mode is what decides whether another user can list or replace
// what is inside it, whatever the files themselves say.
func looseDirsUnder(root string) (loose []looseDir, scanned int, err error) {
	info, statErr := os.Stat(root)
	if os.IsNotExist(statErr) {
		return nil, 0, nil
	}
	if statErr != nil {
		return nil, 0, statErr
	}
	if !info.IsDir() {
		return nil, 0, fmt.Errorf("%s is not a directory", root)
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable subtree must not hide every other directory, the
			// same reasoning List uses for one unreadable workflow.
			if d != nil && d.IsDir() && path != root {
				return fs.SkipDir
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		fi, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		scanned++
		perm := fi.Mode().Perm()
		switch {
		case perm&0o022 != 0:
			loose = append(loose, looseDir{Path: path, Mode: fmt.Sprintf("%#o", perm), Writable: true})
		case perm&0o044 != 0:
			loose = append(loose, looseDir{Path: path, Mode: fmt.Sprintf("%#o", perm)})
		}
		return nil
	})
	if walkErr != nil {
		return nil, scanned, walkErr
	}
	return loose, scanned, nil
}
