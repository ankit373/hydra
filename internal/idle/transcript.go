// SPDX-License-Identifier: MIT

package idle

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ankit373/hydra/internal/payload"
	"github.com/ankit373/hydra/internal/runlog"
)

// ErrNoOriginals reports turns whose text is not stored. A pack is never built
// from part of a conversation, so this stops the job rather than degrading it.
var ErrNoOriginals = errors.New("idle: the original turns are not stored")

// Turns is a conversation or run read back in order, with the files it touched.
type Turns struct {
	Turns []Turn
	Files []File
}

// File actions, so a reader can tell a tool's edit from a path someone typed.
const (
	FileEdited = "edited"
	FileNamed  = "named"
)

// Transcript reads key back as ordered turns. A run id gives each finished
// dispatch in order; a conversation key (#1627) gives its newest run, whose
// input already holds the whole history, plus that run's answer.
func Transcript(key string) (Turns, error) {
	if key == "" {
		return Turns{}, errors.New("idle: transcript needs a run id or a conversation key")
	}
	store, err := payload.Open(payload.Dir())
	if err != nil {
		return Turns{}, err
	}
	events, err := runlog.Load(key)
	if err != nil {
		return Turns{}, err
	}
	if len(events) > 0 {
		return runTranscript(store, key, events)
	}
	runs, err := runlog.Conversation(key)
	if err != nil {
		return Turns{}, err
	}
	if len(runs) == 0 {
		return Turns{}, fmt.Errorf("%w: no run carries %s", ErrNoOriginals, key)
	}
	return conversationTranscript(store, runs)
}

func runTranscript(store *payload.Store, run string, events []runlog.Event) (Turns, error) {
	var out Turns
	system := ""
	for _, e := range events {
		if e.Kind != runlog.KindDispatchFinished {
			continue
		}
		sys, prompt, answer, err := exchange(store, run, e)
		if err != nil {
			return Turns{}, err
		}
		// The system prompt repeats on every call; it is one turn until it changes.
		if sys != "" && sys != system {
			out.Turns = append(out.Turns, Turn{Role: "system", Text: sys, Ref: e.InputRef})
			system = sys
		}
		out.Turns = append(out.Turns, Turn{Role: "user", Text: prompt, Ref: e.InputRef})
		if answer != "" {
			out.Turns = append(out.Turns, Turn{Role: "assistant", Text: answer, Ref: e.OutputRef})
		}
	}
	if len(out.Turns) == 0 {
		return Turns{}, fmt.Errorf("%w: run %s finished no dispatch", ErrNoOriginals, run)
	}
	out.finish(events)
	return out, nil
}

func conversationTranscript(store *payload.Store, runs []string) (Turns, error) {
	newest := runs[len(runs)-1]
	events, err := runlog.Load(newest)
	if err != nil {
		return Turns{}, err
	}
	// The last finished dispatch is the answer served; earlier ones were passed over.
	var last *runlog.Event
	for i := range events {
		if events[i].Kind == runlog.KindDispatchFinished {
			last = &events[i]
		}
	}
	if last == nil {
		return Turns{}, fmt.Errorf("%w: run %s finished no dispatch", ErrNoOriginals, newest)
	}
	sys, prompt, answer, err := exchange(store, newest, *last)
	if err != nil {
		return Turns{}, err
	}
	var out Turns
	if sys != "" {
		out.Turns = append(out.Turns, Turn{Role: "system", Text: sys, Ref: last.InputRef})
	}
	out.Turns = append(out.Turns, splitFlattened(prompt, last.InputRef)...)
	if answer != "" {
		out.Turns = append(out.Turns, Turn{Role: "assistant", Text: answer, Ref: last.OutputRef})
	}
	// Files come from every run of the conversation, oldest first.
	var all []runlog.Event
	for _, run := range runs[:len(runs)-1] {
		earlier, err := runlog.Load(run)
		if err != nil {
			return Turns{}, err
		}
		all = append(all, earlier...)
	}
	out.finish(append(all, events...))
	return out, nil
}

// exchange reads one dispatch's system prompt, prompt and answer from the store.
func exchange(store *payload.Store, run string, e runlog.Event) (system, prompt, answer string, err error) {
	if e.InputRef == "" {
		return "", "", "", fmt.Errorf("%w: run %s stored no prompt; payload capture was off or sampled it out", ErrNoOriginals, run)
	}
	segs, err := store.LoadSegments(e.InputRef)
	if err != nil {
		return "", "", "", fmt.Errorf("%w: prompt %s of run %s: %v", ErrNoOriginals, e.InputRef, run, err)
	}
	for _, s := range segs {
		switch s.Label {
		case "system":
			system = s.Content
		case "prompt":
			prompt = s.Content
		}
	}
	if e.OutputRef != "" {
		if answer, err = store.Load(e.OutputRef); err != nil {
			return "", "", "", fmt.Errorf("%w: answer %s of run %s: %v", ErrNoOriginals, e.OutputRef, run, err)
		}
	}
	return system, prompt, answer, nil
}

// roleLine matches the "role:" line flattenConversation (cmd/hydra/serve.go)
// writes before each message, which starts the text or follows a blank line.
var roleLine = regexp.MustCompile(`(?:\A|\n\n)(system|developer|user|assistant|tool):\n`)

// splitFlattened undoes flattenConversation. Text with no role lines is one user turn.
func splitFlattened(flat, ref string) []Turn {
	marks := roleLine.FindAllStringSubmatchIndex(flat, -1)
	if len(marks) == 0 || marks[0][0] != 0 {
		marks = append([][]int{{0, 0, -1, -1}}, marks...)
	}
	out := make([]Turn, 0, len(marks))
	for i, m := range marks {
		end := len(flat)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		role := "user"
		if m[2] >= 0 {
			role = flat[m[2]:m[3]]
		}
		if text := flat[m[1]:end]; text != "" {
			out = append(out, Turn{Role: role, Text: text, Ref: ref})
		}
	}
	return out
}

// pathToken is a word that reads as a file path: a dir/name, or a name with a
// letter-led extension, so "3.5" and "e.g" do not count.
var pathToken = regexp.MustCompile(`^(?:[\w.-]+/)+[\w.-]+$|^[\w-]{2,}\.[A-Za-z][A-Za-z0-9]{0,7}$`)

// finish numbers the turns and lists files: edits from the run log first, then
// paths named in the turns, each once per action.
func (t *Turns) finish(events []runlog.Event) {
	seen := map[[2]string]bool{}
	add := func(f File) {
		if k := [2]string{f.Path, f.Action}; !seen[k] {
			seen[k] = true
			t.Files = append(t.Files, f)
		}
	}
	for _, e := range events {
		if e.Kind == runlog.KindEdit && e.File != "" {
			add(File{Path: e.File, Action: FileEdited, Ref: e.Ref})
		}
	}
	for i := range t.Turns {
		t.Turns[i].Seq = i
		for _, w := range strings.Fields(t.Turns[i].Text) {
			w = strings.Trim(w, "`'\"()[]{}<>,;:!?")
			w = strings.TrimSuffix(w, ".")
			if !strings.Contains(w, "://") && pathToken.MatchString(w) {
				add(File{Path: w, Action: FileNamed, Ref: t.Turns[i].Ref})
			}
		}
	}
}
