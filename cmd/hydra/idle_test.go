// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdleStatus_ShowsRefusalReason(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HYDRA_HOME", home)
	if out, _, err := run(t, "idle", "status"); err != nil || !strings.Contains(out, "no conversation seen yet") {
		t.Fatalf("empty status: %q, %v", out, err)
	}
	dir := filepath.Join(home, "idle", "support-42")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"status.json":          `{"key":"support-42","outcome":"none","reason":"on battery","at":"2026-11-24T03:00:00Z"}`,
		"pack-1.json":          `{"pack":{},"verdict":{"pass":true,"probes":10,"probe_agree":9,"tokens_before":9000,"tokens_after":1200}}`,
		"pack-2.rejected.json": `{}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err := run(t, "idle", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"support-42", "pack 1, probes 9/10, 9000 to 1200 tokens", "last reason: on battery", "1 rejected pack"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}
