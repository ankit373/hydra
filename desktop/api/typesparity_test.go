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
		{"Run", Run{}},
		{"Fleet", Fleet{}},
		{"DiffLine", DiffLine{}},
		{"ReviewOutcome", ReviewOutcome{}},
		{"MCPServer", MCPServer{}},
		{"MCPPanel", MCPPanel{}},
		{"MCPSyncResult", MCPSyncResult{}},
		{"Head", Head{}},
		{"HeadPanel", HeadPanel{}},
		{"Span", Span{}},
		{"Diff", Diff{}},
		{"Session", Session{}},
		{"ChatReply", ChatReply{}},
		{"PendingQuestion", PendingQuestion{}},
		{"QuestionQueue", QuestionQueue{}},
		{"HyctlStatus", HyctlStatus{}},
		{"InstallResult", InstallResult{}},
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
		{"Run", Run{}},
		{"Fleet", Fleet{}},
		{"DiffLine", DiffLine{}},
		{"ReviewOutcome", ReviewOutcome{}},
		{"MCPServer", MCPServer{}},
		{"MCPPanel", MCPPanel{}},
		{"MCPSyncResult", MCPSyncResult{}},
		{"Head", Head{}},
		{"HeadPanel", HeadPanel{}},
		{"Span", Span{}},
		{"Diff", Diff{}},
		{"Session", Session{}},
		{"ChatReply", ChatReply{}},
		{"PendingQuestion", PendingQuestion{}},
		{"QuestionQueue", QuestionQueue{}},
		{"HyctlStatus", HyctlStatus{}},
		{"InstallResult", InstallResult{}},
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

// The tests above guard field *names*. They say nothing about the *values* a
// string enum carries, and a value is just as load-bearing: the Audit view
// looked two checks up by names internal/security never emitted, and both
// cards silently never rendered (#634). Those were free strings rather than a
// declared union, but a union can drift the same way — a TypeScript union is
// only a compile-time promise about the frontend, never a claim about Go.
//
// So: every union in types.ts that mirrors a Go string type must hold exactly
// that type's values.

var tsUnionRE = func(name string) *regexp.Regexp {
	return regexp.MustCompile(`export type ` + regexp.QuoteMeta(name) + ` =([^\n]+)`)
}

// tsUnionValues returns the quoted members of a TypeScript string union.
func tsUnionValues(t *testing.T, src, name string) map[string]bool {
	t.Helper()
	m := tsUnionRE(name).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("types.ts declares no union %q", name)
	}
	out := map[string]bool{}
	for _, q := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(m[1], -1) {
		out[q[1]] = true
	}
	return out
}

func TestTypesTS_UnionValuesMatchGo(t *testing.T) {
	src := readTypesTS(t)

	for _, c := range []struct {
		union  string
		goVals []string
	}{
		{"Verdict", []string{
			string(security.VerdictActNow),
			string(security.VerdictAttention),
			string(security.VerdictOK),
		}},
		{"Severity", []string{
			string(security.SeverityCritical),
			string(security.SeverityHigh),
			string(security.SeverityMedium),
			string(security.SeverityLow),
		}},
		{"CoverageStatus", []string{
			string(security.Enforced),
			string(security.Configured),
			string(security.Gap),
			string(security.NotApplicable),
		}},
		{"ActionPriority", []string{
			string(security.PriorityNow),
			string(security.PrioritySoon),
			string(security.PriorityWatch),
		}},
	} {
		t.Run(c.union, func(t *testing.T) {
			ts := tsUnionValues(t, src, c.union)
			for _, v := range c.goVals {
				if !ts[v] {
					t.Errorf("Go emits %s %q but types.ts does not list it — "+
						"a value the view can receive and never match", c.union, v)
				}
				delete(ts, v)
			}
			for extra := range ts {
				t.Errorf("types.ts lists %s %q, which Go never emits — "+
					"any branch on it is unreachable", c.union, extra)
			}
		})
	}
}
