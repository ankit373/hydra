// SPDX-License-Identifier: MIT

package recipe

import (
	"slices"
	"strings"
	"testing"
)

func TestLoad_ShippedRecipesValidate(t *testing.T) {
	b, err := Load("")
	if err != nil {
		t.Fatalf("the shipped %s does not load: %v", File, err)
	}
	if got := b.Kinds(); !slices.Equal(got, []string{"classification", "code", "extraction"}) {
		t.Errorf("shipped menus for %v, want classification, code and extraction", got)
	}
	want := map[string][]string{
		"code":           {Single, "cheap_k_tests", "strong_once"},
		"extraction":     {Single, "cheap_k_schema", "strong_once"},
		"classification": {Single, "cheap_k_vote_label"},
	}
	for kind, names := range want {
		m := b.Menu(kind)
		if !slices.Equal(m.Recipes, names) || m.Default != Single {
			t.Errorf("menu %s = %+v, want %v with default single", kind, m, names)
		}
	}
}

func TestLoad_UnknownKindRefused(t *testing.T) {
	raw := "recipes: []\nmenus:\n  code:\n    default: single\n    recipes: [single]\n  poetry:\n    default: single\n    recipes: [single]\n"
	_, err := Parse([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "line 6") || !strings.Contains(err.Error(), `"poetry"`) {
		t.Fatalf("err = %v, want the unknown kind refused at line 6", err)
	}
}

// A bad entry has to name its line, or a long file is a hunt.
func TestLoad_BadRecipeNamesItsLine(t *testing.T) {
	for name, tc := range map[string]struct{ raw, want string }{
		"samples with no check": {
			"recipes:\n  - name: ok\n    samples: 1\n    stop: none\n  - name: vote\n    samples: 5\n    stop: all\n",
			"line 5"},
		"unknown check kind": {
			"recipes:\n  - name: x\n    samples: 2\n    checks: [vibes]\n    stop: first_pass\n",
			`unknown check "vibes"`},
		"unknown field": {
			"recipes:\n  - name: x\n    sample: 2\n    stop: none\n",
			"line 3"},
		"redefines single": {
			"recipes:\n  - name: single\n    samples: 1\n    stop: none\n",
			"built in"},
		"menu names a missing recipe": {
			"menus:\n  code:\n    default: single\n    recipes: [single, nope]\n",
			`"nope"`},
		"default not on the menu": {
			"recipes:\n  - name: x\n    samples: 1\n    stop: none\nmenus:\n  code:\n    default: x\n    recipes: [single]\n",
			"default"},
		"no name": {
			"recipes:\n  - samples: 1\n    stop: none\n",
			"no name"},
		"defined twice": {
			"recipes:\n  - name: x\n    samples: 1\n    stop: none\n  - name: x\n    samples: 1\n    stop: none\n",
			"line 5"},
		"empty menu": {
			"menus:\n  code:\n    default: single\n",
			"no recipes"},
		"empty file": {"", "empty"},
		"unknown objective": {
			"recipes:\n  - name: x\n    objective: vibes\n    samples: 1\n    stop: none\n",
			"objective"},
	} {
		if _, err := Parse([]byte(tc.raw)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %s", name, err, tc.want)
		}
	}
}

func TestMenuFor_FallsBackToSingle(t *testing.T) {
	b, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	menu, skipped := b.MenuFor("writing.general", []string{"ground"})
	if len(menu) != 1 || menu[0].Name != Single || len(skipped) != 0 {
		t.Fatalf("writing got %+v (skipped %v), want single only", menu, skipped)
	}
	if menu[0].Stop != StopFirstPass || !slices.Equal(menu[0].Checks, []string{"ground"}) {
		t.Errorf("single = %+v, want one call that stops on the request's check", menu[0])
	}
}

func TestMenuFor_SkipsRecipeWhoseCheckCannotRun(t *testing.T) {
	b, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	menu, skipped := b.MenuFor("classification.tickets", []string{"ground"})
	if len(menu) != 1 || menu[0].Name != Single {
		t.Errorf("menu %+v, want single only: the request has no label check", menu)
	}
	if len(skipped) != 1 || skipped[0].Name != "cheap_k_vote_label" || !strings.Contains(skipped[0].Reason, "match") {
		t.Errorf("skipped %+v, want cheap_k_vote_label for want of a match check", skipped)
	}

	labels := "match:labels=labels.txt"
	menu, skipped = b.MenuFor("classification", []string{"ground", labels})
	if len(skipped) != 0 || len(menu) != 2 {
		t.Fatalf("with a label check: menu %+v, skipped %+v", menu, skipped)
	}
	if !slices.Equal(menu[1].Checks, []string{labels}) {
		t.Errorf("cheap_k_vote_label checks %v, want the request's own label check", menu[1].Checks)
	}
}

func TestBook_RecipeByName(t *testing.T) {
	b, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := b.Recipe(Single); !ok || r.Samples != 1 {
		t.Errorf("Recipe(single) = %+v, %v", r, ok)
	}
	if r, ok := b.Recipe("strong_once"); !ok || r.Objective != "quality" {
		t.Errorf("Recipe(strong_once) = %+v, %v", r, ok)
	}
	if _, ok := b.Recipe("nope"); ok {
		t.Error("an undefined recipe was found")
	}
}
