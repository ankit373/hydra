//go:build groundbench

// SPDX-License-Identifier: MIT

// Build-tagged: this walks the whole repository, which is far too slow for CI
// and is a measurement rather than a guard. Run it by hand when the claim
// extraction changes:
//
//	go test ./internal/ground -tags groundbench -run TestMeasure -v
package ground

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/util"
)

// A case is one (context, answer) pair with a label: is the answer actually
// grounded in the context?
type labelled struct {
	name     string
	context  string
	answer   string
	grounded bool
}

// Built from the repository's own doc comments, which is real text on both
// sides rather than a corpus written to be classified.
//
//   - positive: a function's doc comment against its own file's code
//   - easy negative: the same comment against an unrelated package's code
//   - hard negative: the comment against its own file, with one identifier
//     swapped for a real identifier from elsewhere in the tree
//
// The hard negatives are the only synthetic half, and they are labelled as
// such, because the perturbation is the failure the check exists to catch and
// measuring only the easy ones would flatter it.
func TestMeasure(t *testing.T) {
	files := goFiles(t, "../..")
	if len(files) < 20 {
		t.Fatalf("only %d files found; run this from the repository", len(files))
	}

	type doc struct {
		file, pkg, comment, code string
	}
	var docs []doc
	for _, f := range files {
		c, code, ok := docAndCode(f)
		if !ok {
			continue
		}
		docs = append(docs, doc{file: f, pkg: filepath.Dir(f), comment: c, code: code})
	}
	t.Logf("%d files, %d with a usable doc comment", len(files), len(docs))

	var cases []labelled
	for i, d := range docs {
		other := docs[(i+len(docs)/2)%len(docs)]
		cases = append(cases,
			labelled{"positive/" + d.file, wrap(d.code), d.comment, true},
			labelled{"easy/" + d.file, wrap(other.code), d.comment, false},
		)
		if swapped, ok := perturb(d.comment, other.comment); ok {
			cases = append(cases, labelled{"hard/" + d.file, wrap(d.code), swapped, false})
		}
	}

	type bucket struct{ tp, fn, tn, fp int }
	buckets := map[string]*bucket{}
	get := func(k string) *bucket {
		if buckets[k] == nil {
			buckets[k] = &bucket{}
		}
		return buckets[k]
	}

	var examples []string
	vacuous := 0
	byKind := map[Kind]int{}
	hashNum := 0
	for _, c := range cases {
		v := Check(c.answer, c.context)
		if !v.Checked {
			t.Fatalf("%s: the fence was not recognised", c.name)
		}
		if v.Claims == 0 {
			// Nothing to verify, so this pair is evidence about nothing. The
			// oracle returns ErrNoClaims here rather than a pass, and counting
			// it as one would be counting a vacuous verdict as a correct one.
			vacuous++
			continue
		}
		kind := strings.SplitN(c.name, "/", 2)[0]
		b := get(kind)
		switch {
		case c.grounded && v.Grounded:
			b.tp++
		case c.grounded && !v.Grounded:
			b.fn++
			for _, u := range v.Unsupported {
				byKind[u.Kind]++
				if strings.Contains(c.answer, "#"+u.Text) {
					hashNum++
				}
			}
			if len(examples) < 6 {
				examples = append(examples, fmt.Sprintf("false alarm on %s: %s", c.name, v.Detail()))
			}
		case !c.grounded && !v.Grounded:
			b.tn++
		default:
			b.fp++
		}
	}

	fmt.Printf("\n  %d of %d pairs stated no checkable claim and produced no verdict\n", vacuous, len(cases))
	fmt.Printf("\n  %-10s %8s %8s %8s %8s\n", "set", "pass", "flag", "correct", "rate")
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var se, sp float64
	for _, k := range keys {
		b := buckets[k]
		n := b.tp + b.fn + b.tn + b.fp
		right := b.tp + b.tn
		fmt.Printf("  %-10s %8d %8d %8d %7.1f%%\n", k, b.tp+b.fp, b.fn+b.tn, right, 100*float64(right)/float64(n))
		if k == "positive" {
			se = float64(b.tp) / float64(b.tp+b.fn)
		}
	}
	tn, fp := 0, 0
	for _, k := range []string{"easy", "hard"} {
		if b := buckets[k]; b != nil {
			tn += b.tn
			fp += b.fp
		}
	}
	sp = float64(tn) / float64(tn+fp)
	fmt.Printf("\n  sensitivity (a grounded answer passes)     %.3f\n", se)
	fmt.Printf("  specificity (an ungrounded answer fails)  %.3f\n\n", sp)
	fmt.Printf("  false alarms by kind: %v, of which #-cited: %d\n\n", byKind, hashNum)
	for _, e := range examples {
		fmt.Printf("  %s\n", e)
	}
	fmt.Println()
}

func wrap(code string) string {
	return "Answer the question from this file.\n\n" + util.WrapUntrusted("CONTEXT", code)
}

// docAndCode splits a file into its longest doc comment and the code with all
// comments removed, so a positive case is never trivially contained in its own
// context.
func docAndCode(path string) (comment, body string, ok bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return "", "", false
	}
	for _, d := range f.Decls {
		fn, isFn := d.(*ast.FuncDecl)
		if !isFn || fn.Doc == nil {
			continue
		}
		if text := fn.Doc.Text(); len(text) > len(comment) {
			comment = text
		}
	}
	if len(strings.Fields(comment)) < 12 {
		return "", "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	// The file minus the answer itself. Stripping *every* comment was the
	// first cut and it was wrong: it deleted the file's own references to
	// sibling packages, config files and issues, so a comment mentioning
	// routing.yaml was scored against a context from which routing.yaml had
	// been removed. That measured the harness, not the check.
	body = string(raw)
	for _, line := range strings.Split(comment, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			body = strings.Replace(body, "// "+t, "", 1)
		}
	}
	return comment, body, true
}

// perturb swaps one checkable token in text for one from elsewhere, which is
// the confident-contradiction case: right subject, wrong detail.
func perturb(text, donor string) (string, bool) {
	var from, to string
	for _, tok := range tokenize(text) {
		if _, ok := claimKind(tok); ok {
			from = tok
			break
		}
	}
	for _, tok := range tokenize(donor) {
		if _, ok := claimKind(tok); ok && tok != from {
			to = tok
			break
		}
	}
	if from == "" || to == "" {
		return "", false
	}
	return strings.Replace(text, from, to, 1), true
}

func goFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	sort.Strings(out)
	return out
}
