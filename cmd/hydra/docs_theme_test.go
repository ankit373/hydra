// SPDX-License-Identifier: MIT

package main

import (
	"regexp"
	"strings"
	"testing"
)

// themeScript applies a viewer's saved theme before any stylesheet paints, so a
// page never flashes the other theme (#1355). site.js owns the choice afterwards.
const themeScript = `<script>try{var t=localStorage.getItem('hydra-theme');if(t==='light'||t==='dark')document.documentElement.dataset.theme=t}catch(e){}</script>`

var stylesheetLink = regexp.MustCompile(`<link rel="stylesheet"`)

// Every page pinned data-theme="dark" until the light theme shipped. A pin, or a
// page whose saved choice applies only after its styles, is what this keeps out.
func TestDocs_ThemeFollowsTheViewer(t *testing.T) {
	pin := regexp.MustCompile(`<html[^>]*\sdata-theme=`)
	for _, page := range docsPages(t) {
		src := repoFile(t, "docs", page)
		if pin.MatchString(src) {
			t.Errorf("docs/%s pins data-theme on <html>; the theme belongs to the viewer", page)
		}
		at, css := strings.Index(src, themeScript), stylesheetLink.FindStringIndex(src)
		switch {
		case at < 0:
			t.Errorf("docs/%s lacks the inline theme script, so a saved choice applies late or never:\n%s", page, themeScript)
		case css != nil && at > css[0]:
			t.Errorf("docs/%s runs the theme script after a stylesheet, so the other theme paints first", page)
		}
	}
	root := regexp.MustCompile(`(?s):root\{(.*?)\}`).FindStringSubmatch(stripComments(repoFile(t, "docs", "assets", "site.css")))
	if root == nil || !strings.Contains(root[1], "color-scheme:light dark;") {
		t.Error("site.css's :root must declare color-scheme:light dark, or form controls and scrollbars keep one theme")
	}
}
