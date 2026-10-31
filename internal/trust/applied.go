// SPDX-License-Identifier: MIT

package trust

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
)

// applied records which ensemble runs have already had their ledger replayed
// into calibration. One ledger is one body of evidence however many verdicts
// later land on its span, so replaying it twice counts every vote twice.
type applied struct {
	SpanID string `json:"span_id"`
	TS     string `json:"ts"`
}

// AppliedPath is where replayed spans are recorded (~/.hydra/trust_applied.jsonl).
func AppliedPath() string {
	return filepath.Join(config.Dir(), "trust_applied.jsonl")
}

// IsApplied reports whether this span's ledger has already been replayed. A
// missing file means nothing has, which is the state of every install.
func IsApplied(path, spanID string) (bool, error) {
	if spanID == "" {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var a applied
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			continue
		}
		if a.SpanID == spanID {
			return true, nil
		}
	}
	return false, sc.Err()
}

// MarkApplied records that this span's ledger has been replayed.
func MarkApplied(path, spanID string) error {
	if spanID == "" {
		return fmt.Errorf("trust: cannot mark an empty span as applied")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := json.Marshal(applied{SpanID: spanID, TS: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, string(raw))
	return err
}
