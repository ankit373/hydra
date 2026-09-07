// SPDX-License-Identifier: MIT

package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeOverride puts a routing.yaml in a fresh home and returns that home.
// A distinct directory per case matters: EnumTiers caches per home, so reusing
// one would answer the first case's question for every later one.
func writeOverride(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routing.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// allTen renders a complete routing_map, with overrides applied on top.
func allTen(over map[string]int) string {
	base := map[string]int{
		"GRUNT": 10, "TRIVIAL": 9, "SIMPLE": 8, "STANDARD": 7, "MODERATE": 6,
		"COMPLEX": 5, "HARD": 4, "VERY_HARD": 3, "EXPERT": 2, "CORE": 1,
	}
	for k, v := range over {
		base[k] = v
	}
	var b strings.Builder
	b.WriteString("routing_map:\n")
	for _, k := range sortedKeys(base) {
		b.WriteString("  " + k + ": " + itoa(base[k]) + "\n")
	}
	return b.String()
}

func itoa(n int) string {
	if n >= 10 {
		return string(rune('0'+n/10)) + string(rune('0'+n%10))
	}
	return string(rune('0' + n))
}

// The shipped file must be loadable, or an installed binary cannot route at
// all. Nothing else in this package is meaningful if this fails.
func TestEnumTiers_EmbeddedCopyIsUsable(t *testing.T) {
	tiers, err := EnumTiers("")
	if err != nil {
		t.Fatalf("embedded routing.yaml does not load: %v", err)
	}
	if got := tiers["SIMPLE"]; got != 8 {
		t.Errorf("SIMPLE = %d, want 8 from the shipped routing.yaml", got)
	}
	if len(tiers) != 10 {
		t.Errorf("got %d enums, want the 10 routing.yaml declares", len(tiers))
	}
}

// The defect: routing.yaml's header promises that changing a value there
// changes routing. Nothing read it.
func TestEnumTiers_AnOverrideActuallyChangesRouting(t *testing.T) {
	home := writeOverride(t, allTen(map[string]int{"SIMPLE": 10}))
	tiers, err := EnumTiers(home)
	if err != nil {
		t.Fatalf("a complete override was refused: %v", err)
	}
	if tiers["SIMPLE"] != 10 {
		t.Errorf("SIMPLE = %d, want 10; the override did not take effect, "+
			"which is the whole bug", tiers["SIMPLE"])
	}
	if tiers["CORE"] != 1 {
		t.Errorf("CORE = %d, want 1; an override must not disturb keys it did "+
			"not change", tiers["CORE"])
	}
}

// A missing key would keep routing by the shipped rule with nothing to say so,
// which is the same silent-staleness shape as #714.
func TestEnumTiers_RefusesAPartialOverride(t *testing.T) {
	home := writeOverride(t, "routing_map:\n  SIMPLE: 10\n")
	_, err := EnumTiers(home)
	if err == nil {
		t.Fatal("a routing_map with one key was accepted; the other nine would " +
			"silently route by the shipped rule")
	}
	for _, want := range []string{"missing", "CORE", "GRUNT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q, so it cannot be acted on: %v", want, err)
		}
	}
}

// A typo must not mint a new enum, which is what IsKnownEnum exists to catch.
func TestEnumTiers_RefusesAnUnrecognizedKey(t *testing.T) {
	home := writeOverride(t, allTen(nil)+"  SIMPEL: 8\n")
	_, err := EnumTiers(home)
	if err == nil {
		t.Fatal("a misspelled enum was accepted as a new routing key")
	}
	if !strings.Contains(err.Error(), "SIMPEL") {
		t.Errorf("error does not name the offending key: %v", err)
	}
}

func TestEnumTiers_RefusesATierOutsideTheRange(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"zero", allTen(map[string]int{"CORE": 0})},
		{"eleven", allTen(map[string]int{"GRUNT": 11})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EnumTiers(writeOverride(t, tc.body))
			if err == nil {
				t.Fatal("a tier outside 1-10 was accepted")
			}
			if !strings.Contains(err.Error(), "outside the valid range") {
				t.Errorf("error does not say what is wrong: %v", err)
			}
		})
	}
}

func TestEnumTiers_RefusesMalformedAndEmpty(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"not yaml", "routing_map: [this is: a list", "not valid YAML"},
		{"no routing_map", "version: \"1.0\"\n", "no routing_map"},
		{"empty map", "routing_map:\n", "no routing_map"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EnumTiers(writeOverride(t, tc.body))
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error mentioning %q, got: %v", tc.want, err)
			}
		})
	}
}

// A home with no registry/ at all is the normal installed case: Read falls back
// to the embedded copy and routing must work.
func TestEnumTiers_NoOverrideFallsBackToEmbedded(t *testing.T) {
	tiers, err := EnumTiers(t.TempDir())
	if err != nil {
		t.Fatalf("a home with no override must still route: %v", err)
	}
	if tiers["SIMPLE"] != 8 {
		t.Errorf("SIMPLE = %d, want the shipped 8", tiers["SIMPLE"])
	}
}

// The map reaches the router on every dispatch. Handing out the cached one
// would put a global retune one careless write away.
func TestEnumTiers_ReturnsACopy(t *testing.T) {
	home := t.TempDir()
	first, err := EnumTiers(home)
	if err != nil {
		t.Fatal(err)
	}
	first["SIMPLE"] = 1
	second, err := EnumTiers(home)
	if err != nil {
		t.Fatal(err)
	}
	if second["SIMPLE"] != 8 {
		t.Errorf("SIMPLE = %d after a caller mutated its copy; the cache is shared",
			second["SIMPLE"])
	}
}

// Weakest first is the order a picker offers, so GRUNT (tier 10) leads and
// CORE (tier 1) is last.
func TestEnumKeys_OrderedWeakestFirst(t *testing.T) {
	keys, err := EnumKeys("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"GRUNT", "TRIVIAL", "SIMPLE", "STANDARD", "MODERATE",
		"COMPLEX", "HARD", "VERY_HARD", "EXPERT", "CORE"}
	if len(keys) != len(want) {
		t.Fatalf("got %d keys, want %d", len(keys), len(want))
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("position %d is %s, want %s", i, keys[i], want[i])
		}
	}
}

// The order must follow the file, not a literal: an override that inverts the
// tiers inverts the picker.
func TestEnumKeys_FollowsTheFileNotAHardcodedOrder(t *testing.T) {
	home := writeOverride(t, allTen(map[string]int{"CORE": 10, "GRUNT": 1}))
	keys, err := EnumKeys(home)
	if err != nil {
		t.Fatal(err)
	}
	if keys[0] != "CORE" {
		t.Errorf("first key is %s, want CORE now that it is tier 10", keys[0])
	}
	if keys[len(keys)-1] != "GRUNT" {
		t.Errorf("last key is %s, want GRUNT now that it is tier 1", keys[len(keys)-1])
	}
}
