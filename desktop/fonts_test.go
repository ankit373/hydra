// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// tokens.css named Space Grotesk and JetBrains Mono from the day it was written
// and nothing ever loaded them: no @font-face, no link, no file in the bundle.
// Every released desktop build rendered in the OS default sans, and because a
// font-family list always falls back in silence, nothing failed and nobody
// noticed (#1060).
//
// Guarded from Go rather than from vitest, which resolves a `?raw` import of a
// .css file to the empty string because it handles CSS itself, so the assertion
// would pass against no file at all. Same reason cmd/hydra/docs_version_test.go
// reads the site's HTML from here.
func TestFontsDeclaredAreShipped(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("frontend", "src", "tokens.css"))
	if err != nil {
		t.Fatalf("read tokens.css: %v", err)
	}

	for _, f := range []struct{ family, file string }{
		{"Space Grotesk", "SpaceGrotesk.woff2"},
		{"JetBrains Mono", "JetBrainsMono.woff2"},
	} {
		t.Run(f.family, func(t *testing.T) {
			// The declaration and the file are asserted together, because
			// either one alone is exactly the state that shipped.
			face := regexp.MustCompile(`(?s)@font-face\s*\{[^}]*?"` + regexp.QuoteMeta(f.family) + `"[^}]*\}`)
			m := face.Find(css)
			if m == nil {
				t.Fatalf("tokens.css declares no @font-face for %q, so the app falls back to the OS default", f.family)
			}
			if want := "/fonts/" + f.file; !regexp.MustCompile(regexp.QuoteMeta(want)).Match(m) {
				t.Errorf("@font-face for %q does not load %s", f.family, want)
			}

			// public/ is copied to the bundle root verbatim, which is what
			// makes the absolute url above resolve inside the packaged app.
			// go:embed is `all:frontend/dist`, so a file that reaches dist
			// reaches the binary.
			p := filepath.Join("frontend", "public", "fonts", f.file)
			st, err := os.Stat(p)
			if err != nil {
				t.Fatalf("%s is declared but not shipped: %v", f.file, err)
			}
			if st.Size() < 1000 {
				t.Errorf("%s is %d bytes, too small to be a real font", f.file, st.Size())
			}

			// A shipped font nothing selects is dead weight, which is the
			// same defect pointing the other way.
			tok := regexp.MustCompile(`--hy-font-[a-z]+:\s*"` + regexp.QuoteMeta(f.family) + `"`)
			if !tok.Match(css) {
				t.Errorf("no --hy-font-* token names %q, so nothing renders in it", f.family)
			}
		})
	}
}
