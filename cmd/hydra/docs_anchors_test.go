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
// topbar, or it stacks above it. Sitting inside the bar's band at a lower
// z-index is the bug.
func TestSkipIntro_IsNotBuriedUnderTheTopbar(t *testing.T) {
	src := indexHTML(t)

	barH, ok := cssPx(t, src, ".topbar", "height")
	if !ok {
		t.Fatal("could not read .topbar height — the topbar was restructured and this guard " +
			"is no longer measuring what it thinks it is")
	}
	barZ, ok := cssInt(t, src, ".topbar", "z-index")
	if !ok {
		t.Fatal("could not read .topbar z-index")
	}
	skipTop, ok := cssPx(t, src, ".skip-intro", "top")
	if !ok {
		t.Fatal("could not read .skip-intro top")
	}
	skipZ, ok := cssInt(t, src, ".skip-intro", "z-index")
	if !ok {
		t.Fatal("could not read .skip-intro z-index")
	}

	// .hero-pin creates no stacking context above the topbar, so a z-index
	// inside the hero only competes with the bar if it exceeds it outright.
	if skipZ > barZ {
		return // deliberately floated above the bar; clickable either way
	}
	if skipTop < barH {
		t.Errorf("skip-intro sits at top:%dpx, inside .topbar's 0–%dpx band, at z-index %d "+
			"vs the bar's %d — the bar paints over it and swallows the click.\n"+
			"Move it below the bar (top >= %dpx) or raise it above z-index %d.",
			skipTop, barH, skipZ, barZ, barH, barZ)
	}
}

// The mobile override must clear the bar too: only .topbar nav is hidden under
// 720px, the bar keeps its height.
func TestSkipIntro_MobileOverrideAlsoClearsTheTopbar(t *testing.T) {
	src := indexHTML(t)
	barH, ok := cssPx(t, src, ".topbar", "height")
	if !ok {
		t.Fatal("could not read .topbar height")
	}

	// The media-block override, e.g. `@media(max-width:720px){ … .skip-intro{top:64px;} }`
	re := regexp.MustCompile(`@media\(max-width:720px\)\{[^@]*?\.skip-intro\s*\{([^}]*)\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return // no mobile override is fine; the base rule applies
	}
	pv := regexp.MustCompile(`top\s*:\s*(-?\d+)px`)
	pm := pv.FindStringSubmatch(m[1])
	if pm == nil {
		return
	}
	top, err := strconv.Atoi(pm[1])
	if err != nil {
		t.Fatalf("unparsable mobile top %q", pm[1])
	}
	if top < barH {
		t.Errorf("mobile .skip-intro top:%dpx is inside .topbar's 0–%dpx band. Only the bar's "+
			"nav is hidden under 720px — the bar keeps its height.", top, barH)
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
				t.Errorf("%s: href=\"#%s\" has no matching id — the link does nothing", page, frag)
			}
		}
		if len(seen) == 0 {
			t.Errorf("%s: found no in-page anchors; this guard has stopped guarding", page)
		}
		t.Logf("%s: %d in-page anchors, all resolve", page, len(seen))
	}
}

// The nav's targets are the site's structure. Losing one is a dead link in the
// most visible place on the page.
func TestDocs_TopbarNavTargetsExist(t *testing.T) {
	src := indexHTML(t)

	navRe := regexp.MustCompile(`(?s)<div class="topbar".*?</div>`)
	nav := navRe.FindString(src)
	if nav == "" {
		t.Fatal("could not locate the topbar markup")
	}
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(src, -1) {
		ids[m[1]] = true
	}
	var checked int
	for _, m := range regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(nav, -1) {
		checked++
		if !ids[m[1]] {
			t.Errorf("topbar links to #%s, which does not exist", m[1])
		}
	}
	if checked == 0 {
		t.Log("topbar has no in-page links")
	}
}
