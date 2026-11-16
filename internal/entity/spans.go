// SPDX-License-Identifier: MIT

package entity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SpanSystem asks for the personal details themselves, quoted exactly, so the
// caller finds them in the text rather than trusting offsets a model counted.
const SpanSystem = "You find personal data in text. Reply with only a JSON array of " +
	"strings: every person's name, street address, or other direct personal " +
	"identifier in the text, each copied exactly as written. Reply [] if there is none."

// ErrUnreadableSpans reports a span answer that was not a JSON array of strings.
var ErrUnreadableSpans = errors.New("entity: the head did not answer with a JSON array of strings")

// Spans asks one head for the personal details in text. Only strings found in
// text verbatim are returned; an unreadable answer is an error, never "none".
func Spans(ctx context.Context, text string, ask Ask) ([]string, error) {
	if ask == nil {
		return nil, ErrNoHead
	}
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	raw, err := ask(ctx, SpanSystem, text)
	if err != nil {
		return nil, err
	}
	start, end := strings.IndexByte(raw, '['), strings.LastIndexByte(raw, ']')
	var found []string
	if start < 0 || end < start || json.Unmarshal([]byte(raw[start:end+1]), &found) != nil {
		return nil, ErrUnreadableSpans
	}
	var out []string
	for _, s := range found {
		if s = strings.TrimSpace(s); len(s) > 1 && strings.Contains(text, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// Measured is a head's report as of when it was measured on this machine.
type Measured struct {
	Report     Report    `json:"report"`
	MeasuredAt time.Time `json:"measured_at"`
}

// LoadReports reads the reports `hyctl policy ner` saved, by head id. A missing
// file is no measurement, not an error.
func LoadReports(path string) (map[string]Measured, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Measured{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]Measured{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SaveReport records head's report, keeping every other head's, in a 0600 file.
func SaveReport(path, head string, r Report, at time.Time) error {
	all, err := LoadReports(path)
	if err != nil {
		return err
	}
	all[head] = Measured{Report: r, MeasuredAt: at.UTC()}
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}
