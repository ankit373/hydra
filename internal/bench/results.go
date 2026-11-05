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
	// CostUSD is Σ Calls[].CostUSD, failed and errored calls included.
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

// readResults reads items.jsonl back. A partial last line is a write the kill
// interrupted: it is truncated away and its size reported. Anything malformed before
// it is corruption and refused, as is a pair recorded twice.
func readResults(path, runID string) (rows []Result, dropped int, err error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if end := bytes.LastIndexByte(b, '\n') + 1; end < len(b) {
		dropped = len(b) - end
		if err := os.Truncate(path, int64(end)); err != nil {
			return nil, 0, err
		}
		b = b[:end]
	}
	lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
	if len(b) == 0 {
		return nil, dropped, nil
	}
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
	return rows, dropped, nil
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
	if _, err := f.Write(append(b, '\n')); err == nil {
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
