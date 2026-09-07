// SPDX-License-Identifier: MIT

package payload

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corpus reads this package's own source as realistic text: prose-and-code
// mixed, which is what a prompt carrying file context actually looks like.
// Generated filler compresses unrealistically well and would flatter every
// strategy equally.
func corpus(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".go" {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil || len(b) < 2000 {
			continue
		}
		out = append(out, string(b))
	}
	if len(out) < 3 {
		t.Skip("not enough source files to build a corpus")
	}
	return out
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutSegments_RoundTrips(t *testing.T) {
	s := newStore(t)
	files := corpus(t)
	segs := []Segment{
		{Label: "system", Content: "You are a careful engineer."},
		{Label: "prompt", Content: files[0]},
	}
	ref, err := s.PutSegments(segs, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ref)
	if err != nil {
		t.Fatal(err)
	}
	if want := segs[0].Content + segs[1].Content; got != want {
		t.Fatalf("round trip lost content: got %d bytes, want %d", len(got), len(want))
	}
}

// A viewer has to show a system prompt separately from the task, so the labels
// have to survive the round trip, not just the bytes.
func TestLoadSegments_KeepsLabelsApart(t *testing.T) {
	s := newStore(t)
	ref, err := s.PutSegments([]Segment{
		{Label: "system", Content: strings.Repeat("system rules. ", 500)},
		{Label: "prompt", Content: "do the thing"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	segs, err := s.LoadSegments(ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2", len(segs))
	}
	if segs[0].Label != "system" || segs[1].Label != "prompt" {
		t.Fatalf("labels lost: %q, %q", segs[0].Label, segs[1].Label)
	}
	if segs[1].Content != "do the thing" {
		t.Fatalf("second segment = %q", segs[1].Content)
	}
}

// An empty payload has no ref that could resolve to anything, so it is refused
// rather than stored as a manifest pointing at nothing.
func TestPutSegments_RefusesEmptyContent(t *testing.T) {
	s := newStore(t)
	if _, err := s.PutSegments([]Segment{{Label: "system", Content: ""}}, 1); err != ErrEmpty {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
}

// The dominant win: one system prompt repeated across every dispatch is stored
// once. Measured 21.6x against 2.6x for hashing the whole prompt as one blob.
func TestChunking_StoresARepeatedSystemPromptOnce(t *testing.T) {
	files := corpus(t)
	// A real system prompt is varied prose, not a repeated phrase: generated
	// filler compresses to nothing on its own, which hides the cost of storing
	// it once per call and flatters whole-blob hashing.
	sysPrompt, files := files[0], files[1:]

	const calls = 40
	whole, chunked := newStore(t), newStore(t)
	raw := 0
	for i := 0; i < calls; i++ {
		ctx := files[i%len(files)]
		task := fmt.Sprintf("Task %d: explain the tradeoffs.", i)
		raw += len(sysPrompt) + len(ctx) + len(task)

		if _, err := whole.Put(sysPrompt+ctx+task, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := chunked.PutSegments([]Segment{
			{Label: "system", Content: sysPrompt},
			{Label: "prompt", Content: ctx + task},
		}, 1); err != nil {
			t.Fatal(err)
		}
	}
	wRatio := storedRatio(t, whole, raw)
	cRatio := storedRatio(t, chunked, raw)
	t.Logf("whole blob %.1fx, chunked %.1fx (%.1fx better)", wRatio, cRatio, cRatio/wRatio)
	if cRatio < wRatio*3 {
		t.Fatalf("chunking gained only %.1fx over whole-blob hashing, want at least 3x", cRatio/wRatio)
	}
}

// The case that actually dominates agent work: the same file re-read after each
// small edit. Segment splitting alone barely helps here, because the context
// differs every time. Measured 8.5x against 3.1x.
func TestChunking_SurvivesSmallEditsToTheSameFile(t *testing.T) {
	files := corpus(t)
	base := files[0]
	for _, f := range files {
		if len(f) > len(base) {
			base = f
		}
	}
	lines := strings.Split(base, "\n")
	if len(lines) < 60 {
		t.Skip("corpus file too short to edit meaningfully")
	}

	const calls = 30
	whole, chunked := newStore(t), newStore(t)
	raw := 0
	cur := append([]string(nil), lines...)
	for i := 0; i < calls; i++ {
		cur[(i*17)%len(cur)] = fmt.Sprintf("\t// edit %d", i)
		ctx := strings.Join(cur, "\n")
		raw += len(ctx)

		if _, err := whole.Put(ctx, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := chunked.PutSegments([]Segment{{Label: "prompt", Content: ctx}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	wRatio := storedRatio(t, whole, raw)
	cRatio := storedRatio(t, chunked, raw)
	t.Logf("whole blob %.1fx, chunked %.1fx (%.1fx better)", wRatio, cRatio, cRatio/wRatio)
	if cRatio < wRatio*2 {
		t.Fatalf("chunking gained only %.1fx on near-duplicate context, want at least 2x", cRatio/wRatio)
	}
}

func storedRatio(t *testing.T, s *Store, raw int) float64 {
	t.Helper()
	st := s.Stat()
	if st.PackBytes == 0 {
		t.Fatal("nothing was stored")
	}
	return float64(raw) / float64(st.PackBytes)
}

// A boundary must depend on content, not position, or an insertion shifts every
// chunk after it and dedup collapses to nothing.
func TestChunk_BoundariesFollowContentNotOffset(t *testing.T) {
	files := corpus(t)
	base := files[0]
	for _, f := range files {
		if len(f) > len(base) {
			base = f
		}
	}
	if len(base) < minChunkBytes*8 {
		t.Skip("corpus file too short")
	}
	before := chunk(base)
	after := chunk("// one inserted line\n" + base)
	if len(before) < 3 {
		t.Skipf("only %d chunks; nothing to compare", len(before))
	}

	shared := map[string]bool{}
	for _, c := range before {
		shared[c] = true
	}
	kept := 0
	for _, c := range after {
		if shared[c] {
			kept++
		}
	}
	// A fixed-size splitter would keep roughly none of them.
	if kept*2 < len(before) {
		t.Fatalf("an insertion invalidated %d of %d chunks; boundaries are not content-defined",
			len(before)-kept, len(before))
	}
}

func TestChunk_RespectsSizeBounds(t *testing.T) {
	files := corpus(t)
	for _, f := range files {
		chunks := chunk(f)
		joined := strings.Join(chunks, "")
		if joined != f {
			t.Fatal("chunking is not lossless")
		}
		for i, c := range chunks {
			if len(c) > maxChunkBytes {
				t.Fatalf("chunk %d is %d bytes, over the %d cap", i, len(c), maxChunkBytes)
			}
			// The last chunk is whatever remains, so it alone may be short.
			if i < len(chunks)-1 && len(c) < minChunkBytes {
				t.Fatalf("chunk %d is %d bytes, under the %d floor", i, len(c), minChunkBytes)
			}
		}
	}
}
