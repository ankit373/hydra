// SPDX-License-Identifier: MIT

package main

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// maxTopbarLinks is what the header fits on one line above its breakpoint:
// logo, six nav links and the call to action. Measured: the widest page's bar
// (first-principles, with its tag) needs 845px, and the nav folds at 900.
// A ninth link means re-measuring, not raising this number on faith.
const maxTopbarLinks = 8

var topbarRe = regexp.MustCompile(`(?s)<header class="site-header"[^>]*>(.*?)</header>`)

func topbarMarkup(t *testing.T, page string) string {
	t.Helper()
	m := topbarRe.FindStringSubmatch(repoFile(t, "docs", page))
	if m == nil {
		t.Fatalf("docs/%s has no .site-header block; this guard is reading the wrong thing", page)
	}
	return m[1]
}

// docsPages is every page a visitor can land on, so a new page cannot skip the guards.
func docsPages(t *testing.T) []string {
	t.Helper()
	var pages []string
	for _, glob := range []string{"*.html", "blog/*.html"} {
		ms, err := filepath.Glob(filepath.Join("..", "..", "docs", glob))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			rel, _ := filepath.Rel(filepath.Join("..", "..", "docs"), m)
			if strings.HasPrefix(filepath.Base(rel), "google") { // search-console verification stub
				continue
			}
			pages = append(pages, filepath.ToSlash(rel))
		}
	}
	if len(pages) < 9 {
		t.Fatalf("found %d pages under docs/; the glob is wrong", len(pages))
	}
	return pages
}

// The header used to overflow the viewport from 721px to 890px, because the nav
// grew while the breakpoint that hides it stayed put (#1017). The count is what
// regressed, so the count is what is guarded.
func TestDocs_TopbarFitsItsBreakpoint(t *testing.T) {
	links := strings.Count(topbarMarkup(t, "index.html"), "<a ")
	if links > maxTopbarLinks {
		t.Errorf("the header has %d links, more than the %d that fit at the 900px breakpoint.\n"+
			"Re-measure the bar's width on one line and move the breakpoint, or drop a link. "+
			"Raising the constant without measuring is how #1017 shipped.", links, maxTopbarLinks)
	}
}

// One href, one place in the header.
func TestDocs_TopbarLinksEachDestinationOnce(t *testing.T) {
	href := regexp.MustCompile(`href="([^"]+)"`)
	seen := map[string]int{}
	for _, m := range href.FindAllStringSubmatch(topbarMarkup(t, "index.html"), -1) {
		seen[m[1]]++
	}
	for url, n := range seen {
		if n > 1 {
			t.Errorf("the header links %s %d times; one destination, one link", url, n)
		}
	}
}

// Six header variants were the finding (WEB-09): every page carries the same
// navigation, so a visitor can reach every page from any page.
func TestDocs_EveryPageSharesOneHeader(t *testing.T) {
	nav := func(page string) string {
		m := regexp.MustCompile(`(?s)<nav class="site-nav"[^>]*>(.*?)</nav>`).FindStringSubmatch(topbarMarkup(t, page))
		if m == nil {
			t.Fatalf("docs/%s: the header has no .site-nav", page)
		}
		return strings.Join(strings.Fields(strings.ReplaceAll(m[1], ` aria-current="page"`, "")), " ")
	}
	want := nav("index.html")
	for _, page := range docsPages(t) {
		if got := nav(page); got != want {
			t.Errorf("docs/%s has its own navigation:\n got %s\nwant %s", page, got, want)
		}
		src := repoFile(t, "docs", page)
		for _, must := range []string{`class="skip-link" href="#main"`, `<main[^>]* id="main"`, `assets/site\.css"`, `class="site-footer"`} {
			if !regexp.MustCompile(must).MatchString(src) {
				t.Errorf("docs/%s is missing %s", page, must)
			}
		}
	}
}

// The nav must fold before it cannot fit, and its links must not wrap inside the bar.
func TestDocs_TopbarHidesBeforeItOverflows(t *testing.T) {
	css := repoFile(t, "docs", "assets", "site.css")
	fold := regexp.MustCompile(`@media\(max-width:(\d+)px\)\{[^@]*?\.js \.site-nav\{display:none;`)
	m := fold.FindStringSubmatch(css)
	if m == nil {
		t.Fatal("site.css no longer folds .site-nav behind the menu at a max-width; #1017's fix is gone")
	}
	foldAt, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	// 845px is the widest bar on one line, measured on first-principles.
	if foldAt < 845 {
		t.Errorf("the nav folds at %dpx but needs 845px on one line, so it overflows in between", foldAt)
	}
	if !regexp.MustCompile(`\.site-nav a\{[^}]*white-space:nowrap`).MatchString(css) {
		t.Error("the nav links lost white-space:nowrap, so a two-word link wraps inside the bar")
	}
}
