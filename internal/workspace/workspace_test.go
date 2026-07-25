// SPDX-License-Identifier: MIT

package workspace

import "testing"

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		rel     string
		pattern string
		want    bool
	}{
		{"dispatch/route.sh", "dispatch/**", true},
		{"dispatch/agy.sh", "dispatch/**", true},
		{"registry/models.yaml", "dispatch/**", false},
		{"registry/models.yaml", "registry/**", true},
		{"logs/state.json", "**/logs/**", true},
		{"deep/nested/logs/state.json", "**/logs/**", true},
		{".env", "**/.env*", true},
		{".env.local", "**/.env*", true},
		{"node_modules/foo/bar.js", "**/node_modules/**", true},
		{"src/node_modules/baz.ts", "**/node_modules/**", true},
		{"dispatch/route.sh", "**/node_modules/**", false},
		{"core/api/users.go", "core/**", true},
		{"scripts/build.sh", "scripts/**", true},
		{"CLAUDE.md", "dispatch/**", false},
	}

	for _, c := range cases {
		got := matchGlob(c.rel, c.pattern)
		if got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.rel, c.pattern, got, c.want)
		}
	}
}
