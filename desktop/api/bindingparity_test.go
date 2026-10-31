// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"os"
	"reflect"
	"regexp"
	"sort"
	"testing"
)

// bindings.ts hand-declares this API's method list and nothing was checking it.
// typesparity_test.go guards the fields on the wire; this guards the calls.
//
// The bindings are read off `window.go.api.API` at call time, so a name that
// does not exist on the Go side is not a compile error, not a typecheck error
// and not a build error. It is a runtime "is not a function" in a shipped app.

// A method the frontend can call takes only values that survive JSON. A
// context or a func parameter is Wails wiring (Startup, SetEmit), so the
// exemption is a property of the signature rather than a name on a list: a
// guard that catches only what its author enumerated is what #753 was about.
func callableFromJS(m reflect.Method) bool {
	ctx := reflect.TypeOf((*context.Context)(nil)).Elem()
	// In(0) is the receiver.
	for i := 1; i < m.Type.NumIn(); i++ {
		switch in := m.Type.In(i); {
		case in.Kind() == reflect.Func, in == ctx, in.Implements(ctx):
			return false
		}
	}
	return true
}

func goAPIMethods(t *testing.T) (frontend, wiring map[string]bool) {
	t.Helper()
	frontend, wiring = map[string]bool{}, map[string]bool{}
	rt := reflect.TypeOf(&API{})
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		if callableFromJS(m) {
			frontend[m.Name] = true
		} else {
			wiring[m.Name] = true
		}
	}
	if len(frontend) == 0 {
		t.Fatal("reflected no callable methods on *API; the type moved")
	}
	return frontend, wiring
}

// Methods declared on the bridge interface, which is the list the frontend
// actually calls through.
var tsMethodRE = regexp.MustCompile(`(?m)^\s+([A-Z][A-Za-z0-9_]*)\s*\([^)]*\)\s*:\s*Promise<`)

func tsBoundMethods(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../frontend/src/bindings.ts")
	if err != nil {
		t.Fatalf("cannot read the bindings: %v", err)
	}
	block := regexp.MustCompile(`(?s)interface WailsGo \{(.*?)\n\}`).FindStringSubmatch(string(raw))
	if block == nil {
		t.Fatal("bindings.ts declares no WailsGo interface; the bridge moved")
	}
	out := map[string]bool{}
	for _, m := range tsMethodRE.FindAllStringSubmatch(block[1], -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("parsed no methods out of the WailsGo interface")
	}
	return out
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestBindings_MirrorEveryCallTheAPIOffers(t *testing.T) {
	frontend, wiring := goAPIMethods(t)
	bound := tsBoundMethods(t)

	// A declared name with nothing behind it ships an app that throws when the
	// feature is used, having passed every check in CI.
	for _, name := range sorted(bound) {
		if frontend[name] {
			continue
		}
		if wiring[name] {
			t.Errorf("bindings.ts declares %s, which is Wails wiring and takes a value JSON cannot carry", name)
			continue
		}
		t.Errorf("bindings.ts declares %s, which *API does not have: the call throws at runtime", name)
	}

	// And the other direction: a method nothing declares is a capability that
	// shipped with no way to reach it, which is the commonest defect here.
	for _, name := range sorted(frontend) {
		if !bound[name] {
			t.Errorf("*API offers %s and bindings.ts declares no way to call it", name)
		}
	}
}

// The exemption has to be derived, or it is just a list with extra steps.
func TestCallableFromJS_ExemptsWiringBySignature(t *testing.T) {
	_, wiring := goAPIMethods(t)
	for _, name := range []string{"Startup", "SetEmit"} {
		if !wiring[name] {
			t.Errorf("%s was classed as frontend-callable; it takes a value JSON cannot carry", name)
		}
	}
	// A plain reader must not be swept up by the same rule.
	frontend, _ := goAPIMethods(t)
	for _, name := range []string{"GetDashboard", "GetSettings", "SaveSettings"} {
		if !frontend[name] {
			t.Errorf("%s was classed as wiring; the exemption is too wide", name)
		}
	}
}
