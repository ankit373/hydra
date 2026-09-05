// SPDX-License-Identifier: MIT

package util

import "strings"

// SplitLines splits already-in-memory text into lines.
//
// It exists because bufio.Scanner refuses any token longer than its buffer,
// 64 KiB by default, and reports that by simply stopping. Code that scans a
// string and ignores Err() therefore sees a *silently truncated* result, which
// is how a >64 KiB line could erase the file being edited (#168). Raising the
// buffer only moves the cliff; splitting a string that is already in memory
// needs no size limit at all.
//
// The line semantics match bufio.ScanLines exactly, so it is a drop-in for that
// use: a trailing "\r" is stripped from each line, and a final newline does not
// produce a trailing empty line.
func SplitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}
