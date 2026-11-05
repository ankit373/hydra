// SPDX-License-Identifier: MIT

package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// fetchBackoff is the wait before each retry. A 404 or a hash mismatch is final,
// since retrying cannot change it; only transport errors and 5xx are retried.
var fetchBackoff = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

// Fetcher downloads pinned files. Client and Sleep are seams, so a test serves
// fixtures from httptest and never waits on a real clock.
type Fetcher struct {
	Client *http.Client
	Sleep  func(context.Context, time.Duration) error
}

// DefaultFetcher has no proxy surprises beyond the environment's and a whole-file timeout.
func DefaultFetcher() Fetcher {
	return Fetcher{Client: &http.Client{Timeout: 5 * time.Minute}, Sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// DataDir is where a suite's fetched files live, outside the repo.
func DataDir(root, suite string) string { return filepath.Join(root, "bench", "data", suite) }

// Fetch makes dir hold e verified, downloading only when it does not already.
// It reports whether it downloaded.
func (f Fetcher) Fetch(ctx context.Context, dir string, e LockEntry) (bool, error) {
	if ok, err := present(dir, e); err != nil || ok {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	var err error
	for attempt := 0; ; attempt++ {
		var retry bool
		retry, err = f.download(ctx, dir, e)
		if err == nil || !retry || attempt == len(fetchBackoff) {
			return err == nil, err
		}
		if serr := f.Sleep(ctx, fetchBackoff[attempt]); serr != nil {
			return false, serr
		}
	}
}

// present reports whether dir already holds e with the pinned hash. A file with
// another hash is not an error here: it is fetched again and replaced.
func present(dir string, e LockEntry) (bool, error) {
	got, err := FileSHA256(filepath.Join(dir, e.Name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == e.SHA256, nil
}

// download streams into <name>.part, hashing as it goes, and renames into place
// only on a match. Nothing is fixed up: a mismatch deletes the partial file.
func (f Fetcher) download(ctx context.Context, dir string, e LockEntry) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.URL, nil)
	if err != nil {
		return false, err
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return ctx.Err() == nil, fmt.Errorf("bench: fetch %s: %w", e.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode >= 500, fmt.Errorf("bench: fetch %s: status %d", e.URL, resp.StatusCode)
	}
	part := filepath.Join(dir, e.Name+".part")
	out, err := os.Create(part)
	if err != nil {
		return false, err
	}
	keep := false
	defer func() {
		if !keep {
			out.Close()
			os.Remove(part)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return ctx.Err() == nil, fmt.Errorf("bench: fetch %s: %w", e.URL, err)
	}
	if n > maxFileBytes {
		return false, fmt.Errorf("bench: %s is larger than %d bytes", e.URL, maxFileBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != e.SHA256 {
		return false, fmt.Errorf("bench: %s has sha256 %s, the lockfile pins %s", e.URL, got, e.SHA256)
	}
	if err := out.Sync(); err != nil {
		return false, err
	}
	if err := out.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(part, filepath.Join(dir, e.Name)); err != nil {
		return false, err
	}
	keep = true
	return false, nil
}
