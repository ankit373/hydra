// SPDX-License-Identifier: MIT

package main

import (
	"regexp"
	"strconv"
	"testing"
)

// docs/index.html is hand-maintained and served straight to users, with no
// build step and no test of any kind. Two classes of bug are both invisible in
// review and immediately visible to a visitor: an anchor that points at nothing,
// and a control that is painted over by something with a higher z-index.
//
// The second one shipped: "skip intro" sat at top:20px inside a hero whose
// stacking context is below .topbar (sticky, top:0, height:52px, z-index:60).
// The link was rendered underneath the bar, which has a background and a
// backdrop-filter, so every click landed on the bar and the button did nothing.

func indexHTML(t *testing.T) string {
	t.Helper()
	return repoFile(t, "docs", "index.html")
}

// cssPx pulls a pixel value for a property out of a CSS rule, e.g.
// cssPx(src, ".skip-intro", "top") for `.skip-intro{...top:68px;...}`.
func cssPx(t *testing.T, src, selector, prop string) (int, bool) {
	t.Helper()
	// Match the selector's rule body up to the closing brace.
	re := regexp.MustCompile(regexp.QuoteMeta(selector) + `\s*\{([^}]*)\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return 0, false
	}
	pv := regexp.MustCompile(`(?:^|[;\s])` + regexp.QuoteMeta(prop) + `\s*:\s*(-?\d+)px`)
	pm := pv.FindStringSubmatch(m[1])
	if pm == nil {
		return 0, false
	}
	n, err := strconv.Atoi(pm[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

func cssInt(t *testing.T, src, selector, prop string) (int, bool) {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(selector) + `\s*\{([^}]*)\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return 0, false
	}
	pv := regexp.MustCompile(`(?:^|[;\s])` + regexp.QuoteMeta(prop) + `\s*:\s*(-?\d+)`)
	pm := pv.FindStringSubmatch(m[1])
	if pm == nil {
		return 0, false
	}
	n, err := strconv.Atoi(pm[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// The hero's "skip intro" control must be clickable: either it sits clear of the
// header, or it stacks above it. Sitting inside the bar's band at a lower
// z-index is the bug. The header lives in the shared stylesheet.
func TestSkipIntro_IsNotBuriedUnderTheTopbar(t *testing.T) {
	src := indexHTML(t)
	css := repoFile(t, "docs", "assets", "site.css")

	hm := regexp.MustCompile(`--site-header-h:(\d+)px`).FindStringSubmatch(css)
	if hm == nil {
		t.Fatal("could not read --site-header-h from site.css, the header was restructured and this guard " +
			"is no longer measuring what it thinks it is")
	}
	barH, _ := strconv.Atoi(hm[1])
	barZ, ok := cssInt(t, css, ".site-header", "z-index")
	if !ok {
		t.Fatal("could not read .site-header z-index")
	}
	skipTop, ok := cssPx(t, src, ".skip-intro", "top")
	if !ok {
		t.Fatal("could not read .skip-intro top")
	}
	skipZ, ok := cssInt(t, src, ".skip-intro", "z-index")
	if !ok {
		t.Fatal("could not read .skip-intro z-index")
	}

	// .hero-pin creates no stacking context above the header, so a z-index
	// inside the hero only competes with the bar if it exceeds it outright.
	if skipZ > barZ {
		return // deliberately floated above the bar; clickable either way
	}
	if skipTop < barH {
		t.Errorf("skip-intro sits at top:%dpx, inside the header's 0-%dpx band, at z-index %d "+
			"vs the bar's %d, the bar paints over it and swallows the click.\n"+
			"Move it below the bar (top >= %dpx) or raise it above z-index %d.",
			skipTop, barH, skipZ, barZ, barH, barZ)
	}
}

// Every in-page anchor must resolve. A href="#thing" with no id="thing" is a
// link that silently does nothing, which is exactly how the skip-intro bug
// presented even though its target did exist.
func TestDocs_EveryInPageAnchorResolves(t *testing.T) {
	for _, page := range []string{"index.html"} {
		src := repoFile(t, "docs", page)

		ids := map[string]bool{}
		for _, m := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(src, -1) {
			ids[m[1]] = true
		}
		if len(ids) == 0 {
			t.Fatalf("%s: parsed no ids at all", page)
		}

		seen := map[string]bool{}
		for _, m := range regexp.MustCompile(`\bhref="#([^"]*)"`).FindAllStringSubmatch(src, -1) {
			frag := m[1]
			// href="#" is a deliberate no-op placeholder, not a broken link.
			if frag == "" || seen[frag] {
				continue
			}
			seen[frag] = true
			if !ids[frag] {
				t.Errorf("%s: href=\"#%s\" has no matching id, the link does nothing", page, frag)
			}
		}
		if len(seen) == 0 {
			t.Errorf("%s: found no in-page anchors; this guard has stopped guarding", page)
		}
		t.Logf("%s: %d in-page anchors, all resolve", page, len(seen))
	}
}

// The nav's targets are the site's structure. Losing one is a dead link in the
// most visible place on every page.
func TestDocs_TopbarNavTargetsExist(t *testing.T) {
	src := indexHTML(t)
	nav := regexp.MustCompile(`(?s)<header class="site-header".*?</header>`).FindString(src)
	if nav == "" {
		t.Fatal("could not locate the header markup")
	}
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(src, -1) {
		ids[m[1]] = true
	}
	var checked int
	for _, m := range regexp.MustCompile(`href="/?#([^"]+)"`).FindAllStringSubmatch(nav, -1) {
		checked++
		if !ids[m[1]] {
			t.Errorf("the header links to /#%s, which does not exist on the home page", m[1])
		}
	}
	if checked == 0 {
		t.Error("the header links nowhere on the home page; Get started (/#start) is gone")
	}
}
