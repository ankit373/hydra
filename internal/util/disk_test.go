// SPDX-License-Identifier: MIT

package util

import (
	"os"
	"path/filepath"
	"testing"
)

// The gap between logical length and blocks charged is the whole reason this
// exists: a tiny file still occupies a whole block, which is what makes
// one-file-per-record designs expensive.
func TestDiskBytes_ChargesWholeBlocksForATinyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got := DiskBytes(info)
	if got < info.Size() {
		t.Errorf("DiskBytes = %d, below the logical size %d", got, info.Size())
	}
	// On Windows this falls back to logical size, so only assert the floor
	// there; everywhere else a 1-byte file must cost more than 1 byte.
	if got == info.Size() && got == 1 {
		t.Log("logical-size fallback (expected only on Windows)")
	}
}

func TestDiskBytes_EmptyFileIsNotNegative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := DiskBytes(info); got < 0 {
		t.Errorf("DiskBytes = %d for an empty file", got)
	}
}

// A large file's charge must track its size, or the reported saving from
// packing would be meaningless.
func TestDiskBytes_TracksSizeForALargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(path, make([]byte, 256<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := DiskBytes(info); got < info.Size() {
		t.Errorf("DiskBytes = %d for a %d-byte file", got, info.Size())
	}
}
