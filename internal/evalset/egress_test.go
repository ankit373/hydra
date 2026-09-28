// SPDX-License-Identifier: MIT

package evalset

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	corpusPkg  = "github.com/ankit373/hydra/internal/evalset"
	modulePath = "github.com/ankit373/hydra/"
)

// These are direct imports, not a transitive set, so the whole net family has
// to be named: banning only net misses a plain net/http, which is how an
// uploader would actually arrive. os/exec is the way around it, and net/url
// parses without sending, so it is the one deliberate allowance.
func egressVia(imports []string) string {
	for _, imp := range imports {
		switch {
		case imp == "os/exec", imp == "net":
			return imp
		case strings.HasPrefix(imp, "net/") && imp != "net/url":
			return imp
		}
	}
	return ""
}

// The corpus is the user's own code and prompts, kept verbatim and marked
// rather than redacted, so "it never leaves this machine" is load-bearing.
// Until now the rule was a comment naming an export path that never existed.
func TestNoHydraPackageReachableFromTheCorpusCanEgress(t *testing.T) {
	graph := importGraph(t)

	// A walk returning nothing, or only the root, would pass by asserting
	// nothing, which is how the shellpath guard went vacuous (#816). Naming a
	// particular dependency would break on a refactor that is allowed to.
	if !slices.ContainsFunc(graph, func(p pkg) bool { return p.path == "encoding/json" }) {
		t.Fatalf("walk returned %d packages and no encoding/json, so it proves nothing", len(graph))
	}
	ours := 0
	for _, p := range graph {
		if strings.HasPrefix(p.path, modulePath) {
			ours++
		}
	}
	if ours < 2 {
		t.Fatalf("walk found %d Hydra packages, so it never left the corpus itself", ours)
	}

	// Ours, not the whole graph: x/sys/windows imports net for its syscall
	// bindings, so a transitive rule is false on Windows and says nothing
	// about whether we added an uploader. TestCorpusThirdPartySurface pins it.
	for _, p := range graph {
		if !strings.HasPrefix(p.path, modulePath) {
			continue
		}
		if got := egressVia(p.imports); got != "" {
			t.Errorf("%s imports %s and is reachable from the corpus, which holds candidates "+
				"verbatim and marks PII rather than dropping it. Nothing we write here may "+
				"send it off the machine.", p.path, got)
		}
	}
}

// Everything the corpus rests on that we did not write, pinned so arriving at
// a socket through a new dependency is a decision rather than a side effect.
var corpusThirdParty = []string{
	"github.com/BurntSushi/toml",
	"golang.org/x/sys/unix",
	"golang.org/x/sys/windows",
	"gopkg.in/yaml.v3",
}

func TestCorpusThirdPartySurface(t *testing.T) {
	for _, p := range importGraph(t) {
		// A stdlib path's first segment has no dot, which also correctly
		// keeps vendor/golang.org/x/net out: that one ships inside the stdlib.
		if strings.HasPrefix(p.path, modulePath) || !strings.Contains(firstSegment(p.path), ".") {
			continue
		}
		if !slices.ContainsFunc(corpusThirdParty, func(a string) bool {
			return p.path == a || strings.HasPrefix(p.path, a+"/")
		}) {
			t.Errorf("%s is reachable from the corpus and is not in corpusThirdParty. "+
				"Add it deliberately, having checked what it can reach.", p.path)
		}
	}
}

// The mirror: if the match were broken the tests above would pass whatever the
// corpus imported, and report a promise they had not checked.
func TestEgressCheckCatchesWhatItClaimsTo(t *testing.T) {
	for _, banned := range []string{"net", "net/http", "net/smtp"} {
		if got := egressVia([]string{"encoding/json", banned}); got != banned {
			t.Errorf("%s was not caught (got %q), so the guard above proves nothing", banned, got)
		}
	}
	if got := egressVia([]string{"encoding/json", "os/exec"}); got != "os/exec" {
		t.Errorf("os/exec was not caught (got %q)", got)
	}
	if got := egressVia([]string{"encoding/json", "net/url", "path/filepath"}); got != "" {
		t.Errorf("a clean import set was reported as egress via %q", got)
	}
}

type pkg struct {
	path    string
	imports []string
}

func firstSegment(p string) string {
	seg, _, _ := strings.Cut(p, "/")
	return seg
}

// importGraph is every package reachable from the corpus with its own direct
// imports. go list reports a package's imports and never its test imports, so
// this file's os/exec is not in what it measures.
func importGraph(t *testing.T) []pkg {
	t.Helper()
	gocmd, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is not on PATH, so the import graph cannot be read: %v", err)
	}
	out, err := exec.Command(gocmd, "list", "-deps",
		"-f", `{{.ImportPath}} {{join .Imports " "}}`, corpusPkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", corpusPkg, err)
	}
	var graph []pkg
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			graph = append(graph, pkg{path: fields[0], imports: fields[1:]})
		}
	}
	return graph
}
