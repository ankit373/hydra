// SPDX-License-Identifier: MIT

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// benchLock writes a lockfile pinning one file served by a local httptest server.
func benchLock(t *testing.T) (lockPath string, hits *atomic.Int64) {
	t.Helper()
	body := "fixture"
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256([]byte(body))
	lockPath = filepath.Join(t.TempDir(), "datasets.lock")
	lock := fmt.Sprintf("[s]\nrevision = \"r@1\"\nlicence = \"x\"\n[[s.file]]\nname = \"f.txt\"\nurl = %q\nsha256 = %q\n",
		srv.URL+"/f.txt", hex.EncodeToString(sum[:]))
	if err := os.WriteFile(lockPath, []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	return lockPath, &n
}

func TestBenchFetch_DownloadsIntoHydraHomeThenVerifies(t *testing.T) {
	sb := testutil.NewSandbox(t)
	lock, hits := benchLock(t)
	_, out, err := run(t, "bench", "fetch", "--lock", lock)
	if err != nil || !strings.Contains(out, "s/f.txt  downloaded") {
		t.Fatalf("first fetch: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(sb.HydraHome, "bench", "data", "s", "f.txt")); err != nil {
		t.Fatalf("data not under $HYDRA_HOME/bench/data: %v", err)
	}
	_, out, err = run(t, "bench", "fetch", "s", "--lock", lock)
	if err != nil || !strings.Contains(out, "s/f.txt  verified") || hits.Load() != 1 {
		t.Fatalf("second fetch: %v after %d requests\n%s", err, hits.Load(), out)
	}
	dir := t.TempDir()
	if _, _, err := run(t, "bench", "fetch", "--lock", lock, "--data-dir", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bench", "data", "s", "f.txt")); err != nil {
		t.Fatalf("--data-dir ignored: %v", err)
	}
}

func TestBenchFetch_RefusesAnUnknownSuiteOrLock(t *testing.T) {
	testutil.NewSandbox(t)
	lock, _ := benchLock(t)
	if _, _, err := run(t, "bench", "fetch", "cord", "--lock", lock); err == nil {
		t.Fatal("fetched a suite the lock does not pin")
	}
	if _, _, err := run(t, "bench", "fetch", "--lock", filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("fetched with no lockfile")
	}
}
