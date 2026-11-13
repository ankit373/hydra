// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// The desktop app had 27 font sizes, 128 of them under the 12px floor, and no
// spacing, radius or motion tokens in use (#1432). These guards keep app.css on
// the scales in design/tokens. Widths and heights stay literal: they are the
// size of a thing, not a step on a scale.

var (
	appDecl   = regexp.MustCompile(`(?:^|[^\w-])(font-size|padding(?:-[a-z]+)?|margin(?:-[a-z]+)?|gap|row-gap|column-gap|border(?:-[a-z]+)*-radius|transition(?:-[a-z]+)?|animation(?:-[a-z]+)?)\s*:\s*([^;{}]+)`)
	appLength = regexp.MustCompile(`(?:^|[^\w.(-])-?(\d*\.?\d+)(px\b|r?em\b|%)`)
	appTime   = regexp.MustCompile(`(?:^|[^\w.(-])(\d*\.?\d+)m?s\b`)
	appCurve  = regexp.MustCompile(`cubic-bezier\(|\bease(?:-in|-out|-in-out)?\b`)
)

// appTokenViolations lists each declaration in css that sets a size, space,
// radius or motion value without a token. A 1px hairline, or the 1px that
// pulls a box over its neighbour's border, is a line and not a step.
func appTokenViolations(css string) []string {
	var out []string
	for _, m := range appDecl.FindAllStringSubmatch(stripComments(css), -1) {
		prop, val := m[1], strings.TrimSpace(m[2])
		bad := false
		switch {
		case strings.HasPrefix(prop, "transition"), strings.HasPrefix(prop, "animation"):
			bad = appTime.MatchString(val) || appCurve.MatchString(val)
		case strings.HasPrefix(prop, "padding"), strings.HasPrefix(prop, "margin"), strings.HasSuffix(prop, "gap"):
			for _, l := range appLength.FindAllStringSubmatch(val, -1) {
				if l[2] != "px" || l[1] != "1" {
					bad = true
				}
			}
		default:
			bad = appLength.MatchString(val)
		}
		if bad {
			out = append(out, fmt.Sprintf("%s: %s", prop, val))
		}
	}
	return out
}

func TestAppCSS_SizeSpaceAndMotionComeFromTokens(t *testing.T) {
	css := repoFile(t, "desktop", "frontend", "src", "app.css")
	for _, v := range appTokenViolations(css) {
		t.Errorf("desktop/frontend/src/app.css sets %q.\nUse var(--hy-font-size-…), var(--hy-space-…), "+
			"var(--hy-radius-…) or var(--hy-motion-…). A new step is a token change in design/tokens.", v)
	}
	if n := strings.Count(css, "var(--hy-font-size-"); n < 100 {
		t.Fatalf("app.css reads only %d type sizes from tokens; this guard is reading the wrong file", n)
	}
}

// The guard has to see the shapes it exists to catch, and not the ones it must not.
func TestAppCSS_TokenDetector(t *testing.T) {
	for _, c := range []struct {
		src  string
		want int
	}{
		{`a{font-size:10.5px}`, 1},
		{`a{font-size:var(--hy-font-size-xs)}`, 0},
		{`a{padding:7px 11px}`, 1},
		{`a{padding:1px var(--hy-space-1)}`, 0},
		{`a{margin:-1px}`, 0},
		{`a{margin:-3px 0}`, 1},
		{`a{gap:var(--hy-space-2);padding-top:12vh}`, 0},
		{`a{margin:0 auto}`, 0},
		{`a{border-radius:50%}`, 1},
		{`a{border-top-left-radius:4px}`, 1},
		{`a{border-radius:var(--hy-radius-card)}`, 0},
		{`a{transition:opacity .12s}`, 1},
		{`a{transition:transform var(--hy-motion-duration-slow) cubic-bezier(.2,0,0,1)}`, 1},
		{`a{transition:background var(--hy-motion-duration-fast) ease}`, 1},
		{`a{animation:spin var(--hy-motion-duration-loop) linear infinite}`, 0},
		{`a{animation:caret var(--hy-motion-duration-loop) steps(2, start) infinite}`, 0},
		{`/* font-size: 9px */ a{width:7px}`, 0},
		{`a:hover{padding:var(--hy-space-1)}`, 0},
	} {
		if got := len(appTokenViolations(c.src)); got != c.want {
			t.Errorf("appTokenViolations(%q) found %d, want %d", c.src, got, c.want)
		}
	}
}
