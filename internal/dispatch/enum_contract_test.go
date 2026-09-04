// SPDX-License-Identifier: MIT

package dispatch

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ankit373/hydra/registry"
)

// Hydra has two descriptions of the same fact.
//
// registry/routing.yaml calls itself "THE ENUM FILE — this is your single enum,
// change a value here → every domain updates". EnumToTier is a hardcoded Go
// switch. Only the switch runs. Editing the YAML alone changes nothing about
// how a dispatch routes, and nothing anywhere compared the two — which is bug
// #165, and why CLAUDE.md carries a standing warning about it.
//
// The right fix would be one source of truth. Until then this is the next best
// thing: drift becomes a test failure instead of a silent misroute, in both
// directions. An operator who retunes the YAML and sees no behaviour change has
// no way to tell whether they mis-edited it or whether the file is inert.
func TestEnumToTier_MatchesRoutingYAML(t *testing.T) {
	raw, err := registry.Read("", "routing.yaml")
	if err != nil {
		t.Fatalf("reading embedded routing.yaml: %v", err)
	}

	var doc struct {
		RoutingMap map[string]int `yaml:"routing_map"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing routing.yaml: %v", err)
	}
	if len(doc.RoutingMap) == 0 {
		t.Fatal("routing_map is empty — this guard has stopped guarding " +
			"(key renamed or file restructured?)")
	}

	// Every enum the YAML documents must resolve, and to the tier it names.
	for enum, wantTier := range doc.RoutingMap {
		got := EnumToTier(enum)
		if got == "" {
			t.Errorf("routing.yaml documents %s → tier %d, but EnumToTier(%q) returns \"\" — "+
				"a dispatch with --enum %s would not route at all", enum, wantTier, enum, enum)
			continue
		}
		want := itoa(wantTier)
		if got != want {
			t.Errorf("%s: routing.yaml says tier %s, EnumToTier says tier %s. "+
				"The switch is what runs, so the file is lying to whoever reads it.",
				enum, want, got)
		}
	}

	// …and every enum the switch accepts must be documented, or an operator
	// reading routing.yaml has an incomplete picture of what the CLI accepts.
	for _, enum := range knownEnums() {
		if _, ok := doc.RoutingMap[enum]; !ok {
			t.Errorf("EnumToTier accepts %q but routing.yaml does not document it", enum)
		}
	}
}

// A tier number outside 1–10 is not routable: rank.UITier only ever produces
// that range, so nothing would ever match.
func TestEnumToTier_ProducesRoutableTiers(t *testing.T) {
	for _, enum := range knownEnums() {
		tier := EnumToTier(enum)
		n, err := atoiStrict(tier)
		if err != nil {
			t.Errorf("EnumToTier(%q) = %q, which is not a number", enum, tier)
			continue
		}
		if n < 1 || n > 10 {
			t.Errorf("EnumToTier(%q) = %d, outside the routable range 1–10", enum, n)
		}
	}
}

// Unknown input must return "", not a default tier. Silently routing a typo'd
// enum to some tier spends money on a task the caller never described.
func TestEnumToTier_UnknownEnumDoesNotRoute(t *testing.T) {
	for _, bad := range []string{"", "simple", "Simple", "SIMPLE ", "NOPE", "11", "0", "TRIVIALX"} {
		if got := EnumToTier(bad); got != "" {
			t.Errorf("EnumToTier(%q) = %q, want \"\" — an unrecognised enum must not route", bad, got)
		}
	}
}

// EnumToTier's "" result is ambiguous between "no enum given" and
// "unrecognized key" — IsKnownEnum is how a caller (cmd/hydra's --enum flag)
// tells the two apart instead of silently routing a typo unrestricted (#501).
func TestIsKnownEnum(t *testing.T) {
	for _, enum := range knownEnums() {
		if !IsKnownEnum(enum) {
			t.Errorf("IsKnownEnum(%q) = false, want true", enum)
		}
	}
	for _, bad := range []string{"", "simple", "NOPE", "TRIVIALX"} {
		if IsKnownEnum(bad) {
			t.Errorf("IsKnownEnum(%q) = true, want false", bad)
		}
	}
}

// The ordering the enum names promise must hold: a harder task must never
// resolve to a cheaper (higher-numbered) tier than an easier one.
func TestEnumToTier_HarderWorkGetsAStrongerTier(t *testing.T) {
	// Hardest first. Lower tier number = stronger head.
	order := []string{
		"CORE", "EXPERT", "VERY_HARD", "HARD", "COMPLEX",
		"MODERATE", "STANDARD", "SIMPLE", "TRIVIAL", "GRUNT",
	}
	prev := 0
	for _, enum := range order {
		n, err := atoiStrict(EnumToTier(enum))
		if err != nil {
			t.Fatalf("EnumToTier(%q) = %q: %v", enum, EnumToTier(enum), err)
		}
		if n <= prev {
			t.Errorf("%s resolves to tier %d, not weaker than the previous enum's tier %d — "+
				"the enum ladder is out of order", enum, n, prev)
		}
		prev = n
	}
}

// knownEnums is the list the switch accepts. Declared here rather than exported
// from dispatch: it exists to check the switch, so deriving it from the switch
// would make the test vacuous.
func knownEnums() []string {
	return []string{
		"GRUNT", "TRIVIAL", "SIMPLE", "STANDARD", "MODERATE",
		"COMPLEX", "HARD", "VERY_HARD", "EXPERT", "CORE",
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// atoiStrict rejects anything that is not a plain non-negative integer, so a
// tier of "8 " or "eight" fails loudly rather than parsing as 8 or 0.
func atoiStrict(s string) (int, error) {
	if s == "" {
		return 0, errNotANumber
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errNotANumber
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

var errNotANumber = errString("not a plain integer")

type errString string

func (e errString) Error() string { return string(e) }
