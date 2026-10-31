// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"regexp"
	"testing"
)

// app.html's window demo fades one view in per slot of a single CSS loop, which
// makes the slot count load-bearing: a view added without its own
// animation-delay inherits 0s and renders on top of the first one. That is
// exactly what the sixth did, and nothing failed (#1108).
func TestDocs_AppDemoHasASlotPerView(t *testing.T) {
	src, err := os.ReadFile("../../docs/app.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(src)

	views := len(regexp.MustCompile(`<div class="view">`).FindAllString(html, -1))
	navs := len(regexp.MustCompile(`class="nav-item"`).FindAllString(html, -1))
	if views == 0 || navs == 0 {
		t.Fatalf("found %d views and %d nav items; the demo's markup moved", views, navs)
	}
	if views != navs {
		t.Errorf("%d views against %d nav items: every view needs the button that selects it", views, navs)
	}

	// Counted per selector, not as a total, because `.view` and `.nav-item`
	// each carry their own set and one can be bumped without the other.
	for _, c := range []struct {
		what string
		re   *regexp.Regexp
	}{
		{"view", regexp.MustCompile(`\.view:nth-child\((\d+)\)\{animation-delay:`)},
		{"nav-item", regexp.MustCompile(`\.nav-item:nth-of-type\((\d+)\)\{animation-delay:`)},
	} {
		if n := len(c.re.FindAllString(html, -1)); n != views {
			t.Errorf("%d %s animation-delay rules against %d views; the one with no slot "+
				"inherits 0s and renders over the first view", n, c.what, views)
		}
	}
}

// With prefers-reduced-motion the autoplay is off and the first view is pinned
// visible as the static fallback. That has to stop once nav.js takes over, or
// every click renders the clicked view on top of the first one. Measured: it
// did, for every view, not only the new one.
func TestDocs_ReducedMotionFallbackYieldsToTheClick(t *testing.T) {
	src, err := os.ReadFile("../../docs/app.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(src)

	// The fallback must be scoped to the pre-JS state. Asserting the selector
	// rather than its absence, since the rule has to exist to do its job.
	for _, want := range []string{
		`.win-body:not(.js-active) .view:nth-child(1){opacity:1;}`,
		`.win-body:not(.js-active) .nav-item:nth-of-type(1){`,
	} {
		if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(html) {
			t.Errorf("missing %q: the reduced-motion fallback must yield once nav.js is wired", want)
		}
	}
	// And the unscoped form must not come back.
	for _, bad := range []string{"\n  .view:nth-child(1){opacity:1;}", "\n  .nav-item:nth-of-type(1){background:"} {
		if regexp.MustCompile(regexp.QuoteMeta(bad)).MatchString(html) {
			t.Errorf("found the unscoped fallback %q, which pins the first view under every click", bad)
		}
	}
}
