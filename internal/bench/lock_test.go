// SPDX-License-Identifier: MIT

package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodLock = `
[s]
revision = "r@1"
licence = "CC-BY-4.0"
[[s.file]]
name = "a.txt"
url = "http://x/a.txt"
sha256 = "%s"
`

func writeLock(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "datasets.lock")
	os.WriteFile(p, []byte(body), 0o644)
	return p
}

func TestReadLock_AcceptsAPinAndRefusesAnythingLess(t *testing.T) {
	sum := sha256Hex([]byte("a"))
	lock, err := ReadLock(writeLock(t, strings.Replace(goodLock, "%s", sum, 1)))
	if err != nil || lock["s"].Files[0].SHA256 != sum || Names(lock)[0] != "s" {
		t.Fatalf("ReadLock = %v, %v", lock, err)
	}
	if lock["s"].Hashes()["a.txt"] != sum {
		t.Fatal("Hashes lost the pin")
	}
	for name, body := range map[string]string{
		"unknown key": strings.Replace(goodLock, "%s", sum, 1) + "mirror = \"x\"\n",
		"short hash":  strings.Replace(goodLock, "%s", "abc", 1),
		"path name":   strings.Replace(strings.Replace(goodLock, "%s", sum, 1), `"a.txt"`, `"../a.txt"`, 1),
		"no revision": strings.Replace(strings.Replace(goodLock, "%s", sum, 1), `revision = "r@1"`, "", 1),
		"not toml":    "[[[",
	} {
		if _, err := ReadLock(writeLock(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDatasetOpen_RehashesOnEveryLoad(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	ds := Dataset{Files: []LockEntry{{Name: "a.txt", SHA256: sha256Hex([]byte("a"))}}}
	if b, err := ds.Open(dir, "a.txt"); err != nil || string(b) != "a" {
		t.Fatalf("Open = %q, %v", b, err)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("b"), 0o644)
	if _, err := ds.Open(dir, "a.txt"); err == nil || !strings.Contains(err.Error(), "pins") {
		t.Fatalf("a changed file was read: %v", err)
	}
	if _, err := ds.Open(dir, "other.txt"); err == nil {
		t.Fatal("an unpinned file was read")
	}
	if _, err := ds.Open(t.TempDir(), "a.txt"); err == nil {
		t.Fatal("a missing file was read")
	}
	if err := ds.RequireFiles("s", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := ds.RequireFiles("s", "a.txt", "b.txt"); err == nil {
		t.Fatal("a lock pinning less than the loader reads was accepted")
	}
	if _, err := FileSHA256(filepath.Join(dir, "none")); err == nil {
		t.Fatal("hashed a missing file")
	}
}
