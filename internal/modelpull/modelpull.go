// SPDX-License-Identifier: MIT

// Package modelpull fetches a model into a local Ollama server, so Hydra can
// obtain the weights it already knows how to discover, score and route.
package modelpull

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// ErrServerDown is returned when the Ollama server cannot be reached. Kept
// distinct because the remedy is distinct: nothing about the ref is wrong, and
// telling someone their model name failed when their server is not running
// sends them to fix the wrong thing (#248).
var ErrServerDown = errors.New("ollama server not reachable")

// ErrTooLarge is returned when the download is bigger than this machine can
// run. Distinct for the same reason: the fix is a smaller quantization, not a
// retry.
var ErrTooLarge = errors.New("model is larger than this machine can run")

// maxLineBytes bounds one NDJSON progress line. Ollama's are well under a
// kilobyte; this exists so a server answering with something else cannot grow
// the buffer without limit.
const maxLineBytes = 64 << 10

// Progress is one update from a pull in flight.
type Progress struct {
	// Status is the server's own wording, e.g. "pulling manifest",
	// "verifying sha256 digest", "success".
	Status string `json:"status"`
	// Digest names the blob this update is about; empty on status-only lines.
	Digest string `json:"digest,omitempty"`
	// Total and Completed are bytes. Total is known from the first chunk of a
	// blob, which is what makes the size guard possible before the download is.
	Total     int64 `json:"total,omitempty"`
	Completed int64 `json:"completed,omitempty"`
}

// Done reports whether this is the server's final, successful update.
func (p Progress) Done() bool { return p.Status == "success" }

// Options configures a pull.
type Options struct {
	// UsableBytes is how much memory this machine can give a model. A pull
	// whose download exceeds it is refused as soon as the size is known,
	// rather than an hour later when the model will not load. Zero disables
	// the check, which is what unreadable hardware must mean: the absence of a
	// reading is not a verdict about the machine (#258).
	UsableBytes int64
	// OnProgress is called for every update. Never nil-checked in a loop, so a
	// nil one is replaced once at the start.
	OnProgress func(Progress)
}

// Pull fetches ref into the Ollama server at host, reporting progress as it
// goes. ref is whatever that server resolves: a library name (`qwen3:8b`) or a
// HuggingFace GGUF repo (`hf.co/<user>/<repo>`, `huggingface.co/...`), which
// Ollama has resolved natively since 0.3.13. HuggingFace is a ref shape here,
// not a mode, so nothing in this package knows about it.
func Pull(ctx context.Context, host, ref string, opts Options) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("no model ref given")
	}
	report := opts.OnProgress
	if report == nil {
		report = func(Progress) {}
	}

	body, err := json.Marshal(map[string]any{"model": ref, "stream": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(host, "/")+"/api/pull", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w at %s: %v", ErrServerDown, host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pull %s: server answered %d", ref, resp.StatusCode)
	}
	return consume(resp.Body, ref, opts.UsableBytes, report)
}

// consume reads the NDJSON progress stream. A stream that ends without a
// success line is a failure: an interrupted download leaves a model that is
// not there, and reporting that as done is the one thing this must never do.
func consume(r io.Reader, ref string, usableBytes int64, report func(Progress)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4<<10), maxLineBytes)

	// Blobs are reported repeatedly as they download, so size is summed per
	// digest rather than per line, or a 500 MB model would read as terabytes.
	seen := map[string]int64{}
	var done bool

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var p struct {
			Progress
			Error string `json:"error,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			// One unreadable line is not a failed download, and the server is
			// free to add fields. A line that cannot be parsed simply says
			// nothing, so nothing is what it contributes.
			continue
		}
		if p.Error != "" {
			return fmt.Errorf("pull %s: %s", ref, p.Error)
		}
		if p.Digest != "" && p.Total > 0 {
			seen[p.Digest] = p.Total
		}
		if err := checkSize(seen, usableBytes); err != nil {
			return err
		}
		report(p.Progress)
		done = done || p.Progress.Done()
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	if !done {
		return fmt.Errorf("pull %s: the stream ended before the server reported success", ref)
	}
	return nil
}

// checkSize refuses a download this machine cannot run, as soon as the size is
// known. Download size stands in for resident size, which is an approximation
// for GGUF rather than a prediction, so the caller reports it as one.
func checkSize(seen map[string]int64, usableBytes int64) error {
	if usableBytes <= 0 {
		return nil
	}
	var total int64
	for _, n := range seen {
		total += n
	}
	if total > usableBytes {
		return fmt.Errorf("%w: %s to download against %s usable",
			ErrTooLarge, HumanBytes(total), HumanBytes(usableBytes))
	}
	return nil
}

// HumanBytes renders a byte count the way a download is normally quoted.
func HumanBytes(n int64) string {
	const unit = 1000 // download sizes are quoted in decimal, not binary
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGT"[exp])
}

// Installed lists the model names this server currently serves.
//
// Exists so a caller can name the head a pull produced rather than guess it:
// Ollama rewrites a HuggingFace ref into a name of its own choosing, so
// deriving the head id from what the user typed would print an id the router
// does not have. The port provider builds head ids as "ollama/" + name from
// this same endpoint, which is what makes the delta exact.
func Installed(ctx context.Context, host string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(host, "/")+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w at %s: %v", ErrServerDown, host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: server answered %d", resp.StatusCode)
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(payload.Models))
	for _, m := range payload.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// Added returns the names in after that were not in before, sorted, which is
// what a pull actually produced. Empty when a model was already present, which
// is a re-pull rather than a failure.
func Added(before, after []string) []string {
	had := make(map[string]bool, len(before))
	for _, n := range before {
		had[n] = true
	}
	var added []string
	for _, n := range after {
		if !had[n] {
			added = append(added, n)
		}
	}
	sort.Strings(added)
	return added
}
