// SPDX-License-Identifier: MIT

package trust

import (
	"strings"
	"testing"
)

func TestKind_KeysPassTaskClassGrammar(t *testing.T) {
	for _, k := range Kinds {
		for _, area := range []string{"", "go", "invoices"} {
			key, err := Kind(k, area)
			if err != nil {
				t.Fatalf("Kind(%q, %q): %v", k, area, err)
			}
			if got, err := TaskClass(key); err != nil || got != key {
				t.Errorf("Kind(%q, %q) = %q, which TaskClass reads as %q, %v", k, area, key, got, err)
			}
		}
	}
	if key, _ := Kind("Summary", ""); key != "summary.general" {
		t.Errorf("Kind(Summary) = %q, want summary.general", key)
	}
	if key, _ := ParseKind("code.go"); key != "code.go" {
		t.Errorf("ParseKind(code.go) = %q, want code.go", key)
	}
}

func TestKind_RefusesUnknownKind(t *testing.T) {
	for _, bad := range []string{"", "summarise", "go", "default"} {
		_, err := ParseKind(bad)
		if err == nil {
			t.Fatalf("ParseKind(%q) was accepted", bad)
		}
		if !strings.Contains(err.Error(), strings.Join(Kinds, ", ")) {
			t.Errorf("refusal does not list the valid kinds: %v", err)
		}
	}
	if _, err := Kind("summary", "Not Valid/area"); err == nil {
		t.Error("an area outside the key grammar was accepted")
	}
}

// A kind key and a file cell must never share a calibration row (#888).
func TestKind_NeverCollidesWithAnExtensionCell(t *testing.T) {
	files := []string{"a.go", "b.ts", "c.py", "Makefile", "", "x.general", "summary"}
	for _, k := range Kinds {
		key, err := Kind(k, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range append(files, key, "x."+key) {
			if DomainForFile(f) == key {
				t.Errorf("DomainForFile(%q) = %q, the same cell as kind %q", f, key, k)
			}
		}
	}
}
