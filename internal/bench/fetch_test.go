// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeServer serves body at /f, failing with each status in fails first.
func fakeServer(t *testing.T, body string, fails ...int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i < len(fails) {
			w.WriteHeader(fails[i])
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func testFetcher(slept *[]time.Duration) Fetcher {
	return Fetcher{Client: http.DefaultClient, Sleep: func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)
		return nil
	}}
}

func TestFetch_DownloadsVerifiesAndDoesNotRefetch(t *testing.T) {
	srv, n := fakeServer(t, "hello")
	dir := filepath.Join(t.TempDir(), "d")
	e := LockEntry{Name: "f.txt", URL: srv.URL + "/f", SHA256: sha256Hex([]byte("hello"))}
	var slept []time.Duration
	f := testFetcher(&slept)
	got, err := f.Fetch(context.Background(), dir, e)
	if err != nil || !got {
		t.Fatalf("first fetch = %v, %v; want a download", got, err)
	}
	got, err = f.Fetch(context.Background(), dir, e)
	if err != nil || got || n.Load() != 1 {
		t.Fatalf("second fetch = %v, %v after %d requests; want verified with no request", got, err, n.Load())
	}
	// A file with another hash is replaced, not trusted.
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("tampered"), 0o644)
	if got, err := f.Fetch(context.Background(), dir, e); err != nil || !got || n.Load() != 2 {
		t.Fatalf("tampered file: %v, %v, %d requests; want a fresh download", got, err, n.Load())
	}
}

func TestFetch_HashMismatchDeletesThePartialFileAndNamesBothHashes(t *testing.T) {
	srv, n := fakeServer(t, "not what was pinned")
	dir := t.TempDir()
	e := LockEntry{Name: "f.txt", URL: srv.URL + "/f", SHA256: sha256Hex([]byte("hello"))}
	var slept []time.Duration
	_, err := testFetcher(&slept).Fetch(context.Background(), dir, e)
	if err == nil || !strings.Contains(err.Error(), e.SHA256) || !strings.Contains(err.Error(), sha256Hex([]byte("not what was pinned"))) {
		t.Fatalf("err = %v, want both hashes named", err)
	}
	if n.Load() != 1 || len(slept) != 0 {
		t.Fatalf("a hash mismatch was retried: %d requests", n.Load())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left %v behind", entries)
	}
}

func TestFetch_404IsFinal(t *testing.T) {
	srv, n := fakeServer(t, "x", 404)
	var slept []time.Duration
	_, err := testFetcher(&slept).Fetch(context.Background(), t.TempDir(), LockEntry{Name: "f", URL: srv.URL + "/f", SHA256: sha256Hex([]byte("x"))})
	if err == nil || n.Load() != 1 || len(slept) != 0 {
		t.Fatalf("404: err=%v after %d requests, %v slept; want one request", err, n.Load(), slept)
	}
}

func TestFetch_503IsRetriedThreeTimesOnTheBackoff(t *testing.T) {
	srv, n := fakeServer(t, "x", 503, 503, 503, 503)
	var slept []time.Duration
	e := LockEntry{Name: "f", URL: srv.URL + "/f", SHA256: sha256Hex([]byte("x"))}
	if _, err := testFetcher(&slept).Fetch(context.Background(), t.TempDir(), e); err == nil {
		t.Fatal("four 503s succeeded")
	}
	if n.Load() != 4 || !reflect.DeepEqual(slept, fetchBackoff) {
		t.Fatalf("%d requests, slept %v; want 4 and %v", n.Load(), slept, fetchBackoff)
	}
	srv2, n2 := fakeServer(t, "x", 502, 500)
	slept = nil
	e.URL = srv2.URL + "/f"
	if got, err := testFetcher(&slept).Fetch(context.Background(), t.TempDir(), e); err != nil || !got || n2.Load() != 3 {
		t.Fatalf("recovering server: %v %v after %d requests", got, err, n2.Load())
	}
}

func TestFetch_TransportErrorsRetryAndCancellationStops(t *testing.T) {
	srv, _ := fakeServer(t, "x")
	url := srv.URL + "/f"
	srv.Close() // connection refused
	var slept []time.Duration
	e := LockEntry{Name: "f", URL: url, SHA256: sha256Hex([]byte("x"))}
	if _, err := testFetcher(&slept).Fetch(context.Background(), t.TempDir(), e); err == nil || len(slept) != 3 {
		t.Fatalf("refused connection: err=%v, slept %v; want three retries", err, slept)
	}
	f := Fetcher{Client: http.DefaultClient, Sleep: func(context.Context, time.Duration) error { return context.Canceled }}
	if _, err := f.Fetch(context.Background(), t.TempDir(), e); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled backoff returned %v", err)
	}
	if _, err := testFetcher(&slept).Fetch(context.Background(), t.TempDir(), LockEntry{Name: "f", URL: "://bad", SHA256: e.SHA256}); err == nil {
		t.Fatal("an unparseable URL was fetched")
	}
}

func TestSleepCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepCtx on a cancelled context = %v", err)
	}
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if f := DefaultFetcher(); f.Client == nil || f.Sleep == nil {
		t.Fatal("DefaultFetcher left a seam nil")
	}
}
