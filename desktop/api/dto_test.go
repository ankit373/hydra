// SPDX-License-Identifier: MIT

package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// frontend/src/types.ts mirrors these DTOs by hand, because Wails' generated
// bindings are a build artefact and are gitignored — the source tree could not
// typecheck against them. That mirror can drift, and it already did once:
// Agent.Tier and Agent.Confidence were omitempty in Go while TypeScript
// declared them required, so a tier-0 agent arrived as `undefined` where the
// view expected a number.
//
// This pins the rule that made that a bug: a numeric field the view compares
// must always be present on the wire. Strings may be omitempty — TypeScript
// models those naturally as optional.
func TestDTOs_NumericFieldsAreNeverOmitEmpty(t *testing.T) {
	for _, v := range []any{Fleet{}, Run{}, Agent{}, Dashboard{}, SpendPanel{}, GovernorPanel{}, TrustPanel{}, Breakdown{}, RecentCall{}} {
		rt := reflect.TypeOf(v)
		t.Run(rt.Name(), func(t *testing.T) {
			for i := range rt.NumField() {
				f := rt.Field(i)
				tag := f.Tag.Get("json")
				if !strings.Contains(tag, ",omitempty") {
					continue
				}
				switch f.Type.Kind() {
				case reflect.Int, reflect.Int64, reflect.Float64, reflect.Bool:
					t.Errorf("%s.%s is %s with omitempty — an absent key reaches TypeScript as undefined, "+
						"not as a zero the view can compare", rt.Name(), f.Name, f.Type.Kind())
				}
			}
		})
	}
}

// Every DTO must round-trip through JSON unchanged. A field with no json tag
// would ship a Go-cased key the hand-written mirror does not declare.
func TestDTOs_FieldNamesAreExplicitlyTagged(t *testing.T) {
	for _, v := range []any{Fleet{}, Run{}, Agent{}, Dashboard{}, SpendPanel{}, GovernorPanel{}, TrustPanel{}, Breakdown{}, RecentCall{}, Version{}} {
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			f := rt.Field(i)
			if f.Tag.Get("json") == "" {
				t.Errorf("%s.%s has no json tag — it would serialise as %q, which types.ts does not declare",
					rt.Name(), f.Name, f.Name)
			}
		}
	}
}

// A nil slice marshals to null, and the frontend iterates these fields
// directly — `for (const e of session.edges)` throws on null. Dashboard is the
// deliberate exception: its breakdowns are nil to mean "never ran", and
// types.ts declares them `Breakdown[] | null` so TypeScript forces the check.
func TestDTOs_SliceFieldsAreNeverNilOnTheWire(t *testing.T) {
	s, err := New().GetSession("")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"timeline", "agents", "edges"} {
		v, ok := m[key]
		if !ok {
			t.Errorf("Session omits %q", key)
			continue
		}
		if v == nil {
			t.Errorf("Session.%s serialised as null; the view iterates it and would throw", key)
		}
	}
}

// An empty fleet must serialise as valid JSON with the flags the view branches
// on, rather than as null.
func TestFleet_SerialisesEmptyStateForTheView(t *testing.T) {
	raw, err := json.Marshal(Fleet{GroupThreshold: GroupThreshold})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"hasRuns", "liveCount", "groupThreshold"} {
		if _, ok := m[key]; !ok {
			t.Errorf("empty Fleet omits %q, which the view branches on", key)
		}
	}
}
