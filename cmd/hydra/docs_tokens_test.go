// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"regexp"
	"sort"
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
	for _, f := range []string{"site.css", "site.js", "print.css"} {
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

// printSheet is the one source the colour guard skips: a printed post is black on
// white whatever the screen theme. The exemption is this file only, and only while
// every page loads it with media="print" (TestDocs_PrintSheetLoadsOnlyForPrint).
const printSheet = "docs/assets/print.css"

// colourViolations reports each source that carries a colour literal.
func colourViolations(sources map[string]string) []string {
	var out []string
	for name, src := range sources {
		if name == printSheet {
			continue
		}
		if lits := colourLiterals(stripComments(src)); len(lits) > 0 {
			out = append(out, fmt.Sprintf("%s carries %d colour literal(s), first %q", name, len(lits), lits[0]))
		}
	}
	sort.Strings(out)
	return out
}

func TestDocs_NoColourLiterals(t *testing.T) {
	for _, v := range colourViolations(siteSources(t)) {
		t.Errorf("%s.\nUse a role from docs/assets/tokens.css: var(--hy-color-…) in CSS, Hydra.color('…') or "+
			"Hydra.rgba('…', a) in script. A new colour is a token change in design/tokens.", v)
	}
}

// The print exemption covers print.css by name and nothing else: a hex colour in
// site.css, or in a page, still fails, even inside its own @media print block.
func TestDocs_ColourExemptionIsThePrintSheetOnly(t *testing.T) {
	got := colourViolations(map[string]string{
		printSheet:                  `@media print{body{color:#000}}`,
		"docs/assets/site.css":      `a{color:#7FA8FF}`,
		"docs/blog/post.html":       `<style>@media print{h1{color:#000}}</style>`,
		"docs/assets/print.css.bak": `a{color:#fff}`,
	})
	want := []string{"docs/assets/print.css.bak", "docs/assets/site.css", "docs/blog/post.html"}
	if len(got) != len(want) {
		t.Fatalf("got %d violations, want %d: %q", len(got), len(want), got)
	}
	for i, name := range want {
		if !strings.HasPrefix(got[i], name+" ") {
			t.Errorf("violation %d = %q, want one for %s", i, got[i], name)
		}
	}
}

// A page that linked print.css without media="print" would put its fixed colours on screen.
func TestDocs_PrintSheetLoadsOnlyForPrint(t *testing.T) {
	link := regexp.MustCompile(`<link[^>]*print\.css[^>]*>`)
	linked := 0
	for _, page := range docsPages(t) {
		for _, tag := range link.FindAllString(repoFile(t, "docs", page), -1) {
			linked++
			if !strings.Contains(tag, `media="print"`) {
				t.Errorf("docs/%s loads print.css without media=\"print\": %s", page, tag)
			}
		}
	}
	if linked == 0 {
		t.Fatal("no page links print.css; this guard is reading the wrong thing")
	}
	if !onlyMediaPrint(cssComment.ReplaceAllString(repoFile(t, "docs", "assets", "print.css"), "")) {
		t.Error("print.css must keep every rule inside one @media print block, so a wrong link still cannot reach the screen")
	}
}

// onlyMediaPrint reports whether css is a single @media print block and nothing else.
func onlyMediaPrint(css string) bool {
	css = strings.TrimSpace(css)
	if !strings.HasPrefix(css, "@media print") {
		return false
	}
	depth := 0
	for i, r := range css {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strings.TrimSpace(css[i+1:]) == ""
			}
		}
	}
	return false
}

func TestDocs_OnlyMediaPrint(t *testing.T) {
	for css, want := range map[string]bool{
		`@media print{a{color:#000}}`:          true,
		`@media print{a{color:#000}} b{x:y}`:   false,
		`a{color:#000}`:                        false,
		`@media print{@page{size:A4}a{x:y}}  `: true,
	} {
		if got := onlyMediaPrint(css); got != want {
			t.Errorf("onlyMediaPrint(%q) = %v, want %v", css, got, want)
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
