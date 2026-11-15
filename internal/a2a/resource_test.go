// SPDX-License-Identifier: MIT

package a2a

import "testing"

func concurrent(files, resources []string) *Handoff {
	return &Handoff{From: "b", Files: files, Resources: resources, Clock: Clock{"b": 1}}
}

func TestConflictsWith_DocSectionKey(t *testing.T) {
	base := &Handoff{From: "a", Resources: []string{"doc:replies#ticket-12"}, Clock: Clock{"a": 1}}
	for _, tc := range []struct {
		other string
		want  bool
	}{
		{"doc:replies#ticket-12", true},
		{"doc:replies", true}, // the whole document holds the section
		{"doc:replies#ticket-13", false},
		{"doc:summary#ticket-12", false},
	} {
		if got := base.ConflictsWith(concurrent(nil, []string{tc.other})); got != tc.want {
			t.Errorf("doc:replies#ticket-12 against %s: %v, want %v", tc.other, got, tc.want)
		}
	}
}

// Existing handoffs name bare paths in Files; a resource naming the same path
// as a file key must meet them, and a bare resource path is a file too.
func TestConflictsWith_BarePathIsAFile(t *testing.T) {
	base := &Handoff{From: "a", Files: []string{"CHANGELOG.md"}, Clock: Clock{"a": 1}}
	for _, r := range []string{"file:CHANGELOG.md", "CHANGELOG.md"} {
		if !base.ConflictsWith(concurrent(nil, []string{r})) {
			t.Errorf("Files CHANGELOG.md does not conflict with resource %q", r)
		}
	}
	if Key(`C:\repo\a.go`) != `file:C:\repo\a.go` {
		t.Errorf("a Windows path read as a scheme: %s", Key(`C:\repo\a.go`))
	}
	// A # in a file name is not a section.
	if Overlap([]string{"file:notes#1.md"}, []string{"file:notes"}) {
		t.Error("file:notes#1.md overlapped file:notes")
	}
}

func TestConflictsWith_RecordKeysDiffer(t *testing.T) {
	base := &Handoff{From: "a", Resources: []string{"record:tickets/12"}, Clock: Clock{"a": 1}}
	if base.ConflictsWith(concurrent(nil, []string{"record:tickets/13"})) {
		t.Error("two different records conflicted")
	}
	if !base.ConflictsWith(concurrent(nil, []string{"record:tickets/12"})) {
		t.Error("the same record did not conflict")
	}
}
