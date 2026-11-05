// SPDX-License-Identifier: MIT

package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The site had 381 colour literals and 57 font sizes before it moved onto the
// generated tokens (#1344). These guards keep it there: a colour comes from
// tokens.css, by role, in CSS or through Hydra.color() in script.

var (
	cssComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	jsComment   = regexp.MustCompile(`(?m)^\s*//.*$`)
	hexColour   = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	funcColour  = regexp.MustCompile(`\b(?:rgba?|hsla?)\(\s*\d`)
	fontSize    = regexp.MustCompile(`font-size\s*:\s*([^;}"]+)`)
	pxValue     = regexp.MustCompile(`(\d+(?:\.\d+)?)px`)
)

func siteSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, page := range docsPages(t) {
		out["docs/"+page] = repoFile(t, "docs", page)
	}
	for _, f := range []string{"site.css", "site.js"} {
		out["docs/assets/"+f] = repoFile(t, "docs", "assets", f)
	}
	return out
}

func stripComments(src string) string {
	return jsComment.ReplaceAllString(htmlComment.ReplaceAllString(cssComment.ReplaceAllString(src, ""), ""), "")
}

// colourLiterals finds hex and rgb()/hsl() colours, skipping what only looks like
// one: character references (&#9679;), in-page links (href="#a2a") and issue
// numbers in link text (>#263<).
func colourLiterals(src string) []string {
	var found []string
	for _, loc := range hexColour.FindAllStringIndex(src, -1) {
		n := loc[1] - loc[0] - 1
		if n != 3 && n != 4 && n != 6 && n != 8 {
			continue
		}
		if loc[1] < len(src) && (src[loc[1]] == '-' || src[loc[1]] == '_') {
			continue
		}
		if loc[0] > 0 {
			switch prev := src[loc[0]-1]; {
			case prev == '&' || prev == '>' || prev == '/' || prev == '_' || prev == '-':
				continue
			case prev >= '0' && prev <= '9', prev >= 'a' && prev <= 'z', prev >= 'A' && prev <= 'Z':
				continue
			}
		}
		if strings.HasSuffix(src[:loc[0]], `href="`) {
			continue
		}
		found = append(found, src[loc[0]:loc[1]])
	}
	return append(found, funcColour.FindAllString(src, -1)...)
}

func TestDocs_NoColourLiterals(t *testing.T) {
	for name, src := range siteSources(t) {
		if lits := colourLiterals(stripComments(src)); len(lits) > 0 {
			t.Errorf("%s carries %d colour literal(s), first %q.\n"+
				"Use a role from docs/assets/tokens.css: var(--hy-color-…) in CSS, Hydra.color('…') or "+
				"Hydra.rgba('…', a) in script. A new colour is a token change in design/tokens.",
				name, len(lits), lits[0])
		}
	}
}

// Every type size is a step of the token scale, 12px and up. A px literal is
// allowed only above the scale's 48px top, for fluid hero display sizes (§3.5).
func TestDocs_TypeSizesComeFromTheScale(t *testing.T) {
	for name, src := range siteSources(t) {
		for _, m := range fontSize.FindAllStringSubmatch(stripComments(src), -1) {
			for _, px := range pxValue.FindAllStringSubmatch(m[1], -1) {
				if v, _ := strconv.ParseFloat(px[1], 64); v <= 48 {
					t.Errorf("%s: font-size:%s uses a %spx literal; use var(--hy-font-size-…)",
						name, strings.TrimSpace(m[1]), px[1])
				}
			}
		}
	}
}

// The guard has to see the shapes it exists to catch, and not the ones it must not.
func TestDocs_ColourLiteralDetector(t *testing.T) {
	for _, c := range []struct {
		src  string
		want int
	}{
		{`a{color:#7FA8FF}`, 1},
		{`a{color:#fff;}`, 1},
		{`ctx.fillStyle='rgba(255,255,255,.5)'`, 1},
		{`<i style="color:hsl(200 50% 50%)">`, 1},
		{`&#9679; sample`, 0},
		{`<a href="#a2a">`, 0},
		{`<a href="x">#263</a>`, 0},
		{`Hydra.rgba('accent-default',.5)`, 0},
		{`color-mix(in srgb,var(--hy-color-accent-default) 28%,transparent)`, 0},
	} {
		if got := len(colourLiterals(c.src)); got != c.want {
			t.Errorf("colourLiterals(%q) found %d, want %d", c.src, got, c.want)
		}
	}
}
