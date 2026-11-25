// SPDX-License-Identifier: MIT

package idle

import (
	"os"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
)

// RunPrefix names the run idle events are written to, so they never mix with
// a user's runs and are skipped when looking for activity.
const RunPrefix = "idle-"

// Lookback is how far back a conversation is looked for. Older runs are
// sealed, and a conversation quiet for a day has no cache worth saving.
const Lookback = 24 * time.Hour

// RunLogSeen lists the conversations with a loose run file touched since
// now-Lookback, by the newest event of each.
func RunLogSeen(now time.Time) ([]Seen, error) {
	ids, err := looseRuns(now.Add(-Lookback))
	if err != nil {
		return nil, err
	}
	byKey := map[string]*Seen{}
	for _, id := range ids {
		events, err := runlog.Load(id)
		if err != nil {
			return nil, err
		}
		key, last, head := "", time.Time{}, ""
		for _, e := range events {
			if e.Kind == runlog.KindTaskStarted {
				if k, ok := e.Meta[runlog.MetaConversation].(string); ok {
					key = k
				}
			}
			if ts, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && ts.After(last) {
				last = ts
			}
			if e.Kind == runlog.KindDispatchFinished && e.Head != "" {
				head = e.Head
			}
		}
		if key == "" {
			continue
		}
		if s, ok := byKey[key]; !ok || last.After(s.LastEvent) {
			byKey[key] = &Seen{Key: key, LastEvent: last, Head: head}
		}
	}
	out := make([]Seen, 0, len(byKey))
	for _, s := range byKey {
		out = append(out, *s)
	}
	return out, nil
}

// RunLogActivity is the newest write to any run that is not idle's own. A job
// watching it stops for a request in any conversation, not only the one it works on.
func RunLogActivity() (time.Time, error) {
	var newest time.Time
	err := walkRuns(func(id string, mod time.Time) {
		if mod.After(newest) {
			newest = mod
		}
	})
	return newest, err
}

func looseRuns(since time.Time) ([]string, error) {
	var ids []string
	err := walkRuns(func(id string, mod time.Time) {
		if mod.After(since) {
			ids = append(ids, id)
		}
	})
	return ids, err
}

func walkRuns(visit func(id string, mod time.Time)) error {
	entries, err := os.ReadDir(runlog.Dir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok || e.IsDir() || strings.HasPrefix(id, RunPrefix) {
			continue
		}
		if info, err := e.Info(); err == nil {
			visit(id, info.ModTime())
		}
	}
	return nil
}
