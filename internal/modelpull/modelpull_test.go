// SPDX-License-Identifier: MIT

package modelpull

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ndjson serves a canned progress stream, which is the shape Ollama 0.33.2
// actually sends (captured from a real pull against hf.co).
func ndjson(lines ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		for _, l := range lines {
			fmt.Fprintln(w, l)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
}

const (
	manifest = `{"status":"pulling manifest"}`
	blob     = `{"status":"pulling 74a4da8c9fdb","digest":"sha256:74a4","total":491400032,"completed":42592437}`
	blobDone = `{"status":"pulling 74a4da8c9fdb","digest":"sha256:74a4","total":491400032,"completed":491400032}`
	success  = `{"status":"success"}`
)

func TestPull_ReportsProgressAndSucceeds(t *testing.T) {
	srv := ndjson(manifest, blob, blobDone, success)
	defer srv.Close()

	var got []Progress
	err := Pull(context.Background(), srv.URL, "hf.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF", Options{
		OnProgress: func(p Progress) { got = append(got, p) },
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d updates, want 4", len(got))
	}
	if got[0].Status != "pulling manifest" {
		t.Errorf("first update is %q", got[0].Status)
	}
	if got[1].Total != 491400032 || got[1].Completed != 42592437 {
		t.Errorf("byte counts lost: %+v", got[1])
	}
	if !got[3].Done() {
		t.Error("last update does not report success")
	}
}

// The one thing a pull must never do: report success for a model that is not
// there. An interrupted download ends the stream without a success line.
func TestPull_StreamEndingEarlyIsNotSuccess(t *testing.T) {
	srv := ndjson(manifest, blob) // no success line
	defer srv.Close()

	err := Pull(context.Background(), srv.URL, "qwen3:8b", Options{})
	if err == nil {
		t.Fatal("an interrupted download reported success")
	}
	if !strings.Contains(err.Error(), "before the server reported success") {
		t.Errorf("error %q does not say what went wrong", err)
	}
}

// An error line mid-stream is the server refusing, and must surface verbatim:
// a nonexistent HuggingFace repo answers exactly this way.
func TestPull_ServerErrorLineSurfaces(t *testing.T) {
	srv := ndjson(manifest, `{"error":"pull model manifest: file does not exist"}`)
	defer srv.Close()

	err := Pull(context.Background(), srv.URL, "hf.co/nobody/nothing", Options{})
	if err == nil {
		t.Fatal("a refused pull reported success")
	}
	if !strings.Contains(err.Error(), "file does not exist") {
		t.Errorf("error %q drops what the server said", err)
	}
}

// A server that is not running is a different problem from a model that does
// not exist, and sending someone to fix the wrong one wastes their time (#248).
func TestPull_ServerDownIsItsOwnError(t *testing.T) {
	srv := ndjson()
	addr := srv.URL
	srv.Close() // nothing is listening now

	err := Pull(context.Background(), addr, "qwen3:8b", Options{})
	if !errors.Is(err, ErrServerDown) {
		t.Fatalf("got %v, want ErrServerDown", err)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error %q does not name the host it tried", err)
	}
}

// The guard that makes this worth having: refuse from the reported size, which
// arrives on the first chunk, rather than an hour later when the model will
// not load.
func TestPull_RefusesAModelTooLargeToRun(t *testing.T) {
	srv := ndjson(manifest, blob, blobDone, success)
	defer srv.Close()

	var updates int
	err := Pull(context.Background(), srv.URL, "hf.co/big/model", Options{
		UsableBytes: 100 << 20, // 100 MB against a 491 MB model
		OnProgress:  func(Progress) { updates++ },
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
	if !strings.Contains(err.Error(), "491.4 MB") {
		t.Errorf("error %q does not say how big it was", err)
	}
	// Refused on the first chunk carrying a size, not after the whole stream.
	if updates > 1 {
		t.Errorf("reported %d updates before refusing; the size was known at the first", updates)
	}
}

// Hardware that could not be read is not a verdict about the machine, so it
// must refuse nothing (#258).
func TestPull_UnknownHardwareRefusesNothing(t *testing.T) {
	srv := ndjson(manifest, blob, blobDone, success)
	defer srv.Close()

	if err := Pull(context.Background(), srv.URL, "hf.co/big/model", Options{UsableBytes: 0}); err != nil {
		t.Fatalf("refused a pull with no hardware reading: %v", err)
	}
}

// One blob is reported many times as it downloads. Summing lines rather than
// distinct digests would read a 491 MB model as gigabytes and refuse it.
func TestPull_RepeatedBlobLinesAreOneBlob(t *testing.T) {
	lines := []string{manifest}
	for range 20 {
		lines = append(lines, blob)
	}
	srv := ndjson(append(lines, blobDone, success)...)
	defer srv.Close()

	// A budget just above the real size: only double counting can fail this.
	err := Pull(context.Background(), srv.URL, "hf.co/x/y", Options{UsableBytes: 500 << 20})
	if err != nil {
		t.Fatalf("Pull: %v (the same blob was counted more than once)", err)
	}
}

// The server is free to add fields, and one unreadable line says nothing
// rather than failing a download that is going fine.
func TestPull_UnparseableLineIsIgnored(t *testing.T) {
	srv := ndjson(manifest, "not json at all", "", blobDone, success)
	defer srv.Close()

	if err := Pull(context.Background(), srv.URL, "qwen3:8b", Options{}); err != nil {
		t.Fatalf("a junk line failed the pull: %v", err)
	}
}

func TestPull_EmptyRefIsRefused(t *testing.T) {
	if err := Pull(context.Background(), "http://localhost:1", "  ", Options{}); err == nil {
		t.Fatal("an empty ref was accepted")
	}
}

func TestPull_NonOKStatusIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := Pull(context.Background(), srv.URL, "qwen3:8b", Options{})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("got %v, want the status code reported", err)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:       "512 B",
		491400032: "491.4 MB",
		1500:      "1.5 kB",
		45 << 30:  "48.3 GB",
	}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// tagsServer serves /api/tags, which is the endpoint the port provider builds
// head ids from, so a delta taken here matches what discovery will find.
func tagsServer(names ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		parts := make([]string, len(names))
		for i, n := range names {
			parts[i] = fmt.Sprintf(`{"name":%q}`, n)
		}
		fmt.Fprintf(w, `{"models":[%s]}`, strings.Join(parts, ","))
	}))
}

func TestInstalled_ListsModelNames(t *testing.T) {
	srv := tagsServer("qwen3:8b", "hf.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF:latest")
	defer srv.Close()

	got, err := Installed(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if len(got) != 2 || got[1] != "hf.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF:latest" {
		t.Errorf("got %v, want both names verbatim", got)
	}
}

func TestInstalled_EmptyServerIsNotAnError(t *testing.T) {
	srv := tagsServer()
	defer srv.Close()

	got, err := Installed(context.Background(), srv.URL)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want an empty list and no error", got, err)
	}
}

// Same distinction Pull draws: a server that is not running is its own
// problem, and the caller turns it into the one actionable line (#248).
func TestInstalled_ServerDownIsItsOwnError(t *testing.T) {
	srv := tagsServer()
	addr := srv.URL
	srv.Close()

	if _, err := Installed(context.Background(), addr); !errors.Is(err, ErrServerDown) {
		t.Fatalf("got %v, want ErrServerDown", err)
	}
}

func TestInstalled_NonOKStatusIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	if _, err := Installed(context.Background(), srv.URL); err == nil {
		t.Fatal("a refusing server read as an empty model list")
	}
}

func TestAdded(t *testing.T) {
	cases := []struct {
		name          string
		before, after []string
		want          []string
	}{
		{"one new model", []string{"a"}, []string{"a", "b"}, []string{"b"}},
		{"nothing new is a re-pull", []string{"a"}, []string{"a"}, nil},
		{"first model on an empty server", nil, []string{"a"}, []string{"a"}},
		{"sorted, since map order is not an answer", nil, []string{"c", "a", "b"}, []string{"a", "b", "c"}},
		// A model removed elsewhere while this pull ran is not something this
		// pull did, so it must not be reported either way.
		{"a removal is not an addition", []string{"a", "b"}, []string{"a"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Added(tc.before, tc.after)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
