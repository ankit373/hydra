// SPDX-License-Identifier: MIT

package bench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	itemsSchema = "hydra.bench.items/1"
	// MaxLineBytes bounds one row; answers are capped by MaxTokens, so a longer line is a bug.
	MaxLineBytes = 64 << 10
)

// spendRow is the ErrKind of a spend row: one billed call, Retryable, never a result.
const spendRow = "spend"

// Result is one line of items.jsonl: one (item, arm) pair, appended and fsynced.
type Result struct {
	Seq    int    `json:"seq"` // position in the deterministic schedule
	ItemID string `json:"item_id"`
	Split  string `json:"split"`
	Arm    string `json:"arm"`
	Attempt
	Passed bool    `json:"passed"`
	Score  float64 `json:"score"` // 1/0 for classification
	Format bool    `json:"format_fail,omitempty"`
	Detail string  `json:"detail,omitempty"`
	// CostUSD is money: only a spend row carries it, one per call that may have been
	// billed, written as the call returns. A result row lists its calls and adds 0.
	CostUSD float64   `json:"cost_usd"`
	Started time.Time `json:"started"`
	// LatencyInvalid marks a row whose call followed a gap over a minute: the machine slept.
	LatencyInvalid bool `json:"latency_invalid,omitempty"`
	// Retryable rows record spend only; resume asks again and the summary keeps the last.
	Retryable bool `json:"retryable,omitempty"`
}

type itemsHeader struct {
	Schema string `json:"schema"`
	RunID  string `json:"run_id"`
}

// pairKey identifies one (item, arm) cell.
type pairKey struct{ item, arm string }

// readResults is the runner's read, under run.lock: a partial last line is a write a
// kill interrupted, and it is truncated away so the next append starts on a line.
func readResults(path, runID string) (rows []Result, dropped int, err error) {
	rows, dropped, err = parseResults(path, runID)
	if err == nil && dropped > 0 {
		st, serr := os.Stat(path)
		if serr != nil {
			return nil, 0, serr
		}
		err = os.Truncate(path, st.Size()-int64(dropped))
	}
	return rows, dropped, err
}

// parseResults reads items.jsonl without changing it: a partial last line is left on
// disk and ignored. Anything malformed before it is corruption, as is a pair recorded twice.
func parseResults(path, runID string) (rows []Result, torn int, err error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if end := bytes.LastIndexByte(b, '\n') + 1; end < len(b) {
		torn, b = len(b)-end, b[:end]
	}
	if len(b) == 0 {
		return nil, torn, nil
	}
	lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
	var h itemsHeader
	if err := json.Unmarshal(lines[0], &h); err != nil || h.Schema != itemsSchema || h.RunID != runID {
		return nil, 0, fmt.Errorf("bench: %s does not start with the %s header for %s", path, itemsSchema, runID)
	}
	done := map[pairKey]bool{}
	for i, line := range lines[1:] {
		var r Result
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&r); err != nil {
			return nil, 0, fmt.Errorf("bench: %s line %d is corrupt: %w", path, i+2, err)
		}
		k := pairKey{r.ItemID, r.Arm}
		if done[k] {
			return nil, 0, fmt.Errorf("bench: %s records %s on %s twice", path, r.Arm, r.ItemID)
		}
		done[k] = !r.Retryable
		rows = append(rows, r)
	}
	return rows, torn, nil
}

// resultsWriter appends one row per write(2), then fsyncs, so a kill leaves at most
// one partial line at the tail.
type resultsWriter struct{ f *os.File }

func openResults(path, runID string) (*resultsWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	w := &resultsWriter{f: f}
	if st, err := f.Stat(); err != nil || st.Size() == 0 {
		if err == nil {
			err = w.write(itemsHeader{itemsSchema, runID})
		}
		if err != nil {
			f.Close()
			return nil, err
		}
	}
	return w, nil
}

func (w *resultsWriter) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b)+1 > MaxLineBytes {
		return fmt.Errorf("bench: a %d-byte row is over MaxLineBytes", len(b)+1)
	}
	if _, err := w.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return w.f.Sync()
}

func (w *resultsWriter) Close() error { return w.f.Close() }

// writeJSON writes a whole file atomically: temp, fsync, rename.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
