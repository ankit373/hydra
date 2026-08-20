// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/security"
)

// types.ts is a hand-written mirror and nothing was checking it, so it drifted:
// attestation, bom and privilege were computed, shipped on the wire, and
// invisible to the desktop because TypeScript had never heard of them (#435).
//
// dto_test.go guards the Go side of the wire. This guards the mirror.

var tsFieldRE = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_]+)\??:`)

// tsInterface returns the field names declared on a TypeScript interface.
func tsInterface(t *testing.T, src, name string) map[string]bool {
	t.Helper()
	re := regexp.MustCompile(`(?s)export interface ` + regexp.QuoteMeta(name) + ` \{(.*?)\n\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("types.ts declares no interface %q", name)
	}
	out := map[string]bool{}
	for _, f := range tsFieldRE.FindAllStringSubmatch(m[1], -1) {
		out[f[1]] = true
	}
	return out
}

// jsonFields returns the wire names of a Go struct, skipping anything the
// encoder drops entirely.
func jsonFields(v any) map[string]bool {
	out := map[string]bool{}
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = true
	}
	return out
}

func readTypesTS(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../frontend/src/types.ts")
	if err != nil {
		t.Fatalf("cannot read the TypeScript mirror: %v", err)
	}
	return string(raw)
}

func TestTypesTS_MirrorsEveryFieldOnTheWire(t *testing.T) {
	src := readTypesTS(t)

	for _, c := range []struct {
		iface string
		goVal any
	}{
		{"SecurityReport", security.Report{}},
		{"Attestation", security.Attestation{}},
		{"AttestedEvidence", security.AttestedEvidence{}},
		{"BOMEntry", security.BOMEntry{}},
		{"AgentPrivilege", security.AgentPrivilege{}},
		{"Incident", security.Incident{}},
		{"Risk", security.Risk{}},
		{"RiskRegister", security.RiskRegister{}},
		{"Posture", security.Posture{}},
		{"Model", Model{}},
		{"Pool", Pool{}},
		{"ModelRegistry", ModelRegistry{}},
		{"GovernorPanel", GovernorPanel{}},
	} {
		t.Run(c.iface, func(t *testing.T) {
			ts := tsInterface(t, src, c.iface)
			for name := range jsonFields(c.goVal) {
				if !ts[name] {
					t.Errorf("%s.%s ships on the wire but types.ts does not declare it — "+
						"the desktop cannot render a field it has never heard of",
						c.iface, name)
				}
			}
		})
	}
}

// The reverse direction: a TypeScript field with no Go counterpart is always
// undefined at runtime, which typechecks and then renders nothing.
func TestTypesTS_DeclaresNoFieldTheBackendNeverSends(t *testing.T) {
	src := readTypesTS(t)
	for _, c := range []struct {
		iface string
		goVal any
	}{
		{"SecurityReport", security.Report{}},
		{"Attestation", security.Attestation{}},
		{"BOMEntry", security.BOMEntry{}},
		{"AgentPrivilege", security.AgentPrivilege{}},
		{"Model", Model{}},
		{"Pool", Pool{}},
		{"ModelRegistry", ModelRegistry{}},
		{"GovernorPanel", GovernorPanel{}},
	} {
		t.Run(c.iface, func(t *testing.T) {
			g := jsonFields(c.goVal)
			for name := range tsInterface(t, src, c.iface) {
				if !g[name] {
					t.Errorf("types.ts declares %s.%s but the backend never sends it — "+
						"it is permanently undefined", c.iface, name)
				}
			}
		})
	}
}
