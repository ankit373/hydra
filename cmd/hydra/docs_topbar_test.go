// SPDX-License-Identifier: MIT

package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// maxTopbarLinks is what the header fits on one line at its own breakpoint.
// Measured: 11 nav links plus the ghost button need 904px, and the nav hides at
// 900. A twelfth means re-measuring, not raising this number on faith.
const maxTopbarLinks = 11

var topbarRe = regexp.MustCompile(`(?s)<div class="topbar"[^>]*>(.*?)</div>`)

func topbarMarkup(t *testing.T) string {
	t.Helper()
	m := topbarRe.FindStringSubmatch(repoFile(t, "docs", "index.html"))
	if m == nil {
		t.Fatal("docs/index.html has no .topbar block; this guard is reading the wrong thing")
	}
	return m[1]
}

// The header used to overflow the viewport from 721px to 890px, because the nav
// grew to 11 links while the breakpoint that hides it stayed at 720. At 768 the
// page scrolled 121px sideways and the GitHub button was off-screen (#1017).
// The count is what regressed, so the count is what is guarded.
func TestDocs_TopbarFitsItsBreakpoint(t *testing.T) {
	links := strings.Count(topbarMarkup(t), "<a ")
	if links > maxTopbarLinks {
		t.Errorf("the header has %d links, more than the %d that fit at the 900px breakpoint.\n"+
			"Re-measure the bar's width on one line and move the breakpoint, or drop a link. "+
			"Raising the constant without measuring is how #1017 shipped.", links, maxTopbarLinks)
	}
}

// One href, one place in the header. GitHub was in the nav and on the ghost
// button beside it, same URL about 4px apart, costing 65px of a bar that had
// none to spare.
func TestDocs_TopbarLinksEachDestinationOnce(t *testing.T) {
	href := regexp.MustCompile(`href="([^"]+)"`)
	seen := map[string]int{}
	for _, m := range href.FindAllStringSubmatch(topbarMarkup(t), -1) {
		seen[m[1]]++
	}
	for url, n := range seen {
		if n > 1 {
			t.Errorf("the header links %s %d times; one destination, one link", url, n)
		}
	}
}

// The nav must hide before it cannot fit, so the two breakpoints have to stay
// in the order the fix put them in: tighten first, then hide.
func TestDocs_TopbarHidesBeforeItOverflows(t *testing.T) {
	css := repoFile(t, "docs", "index.html")
	hide := regexp.MustCompile(`@media\(max-width:(\d+)px\)\{\.topbar nav\{display:none;\}\}`)
	m := hide.FindStringSubmatch(css)
	if m == nil {
		t.Fatal("docs/index.html no longer hides .topbar nav at a max-width; #1017's fix is gone")
	}
	hideAt, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	// 904px is the narrowest the tightened bar fits in, measured.
	if hideAt < 900 {
		t.Errorf("the nav hides at %dpx but does not fit below 904px, so it overflows in between",
			hideAt)
	}
	if !strings.Contains(css, ".topbar nav a{color:var(--dim);transition:color .15s;white-space:nowrap;}") {
		t.Error("the nav links lost white-space:nowrap, so a two-word link wraps inside the 52px bar")
	}
}
