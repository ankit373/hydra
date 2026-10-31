// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMCPToolName(t *testing.T) {
	tests := []struct {
		tool      string
		wantAlias string
		wantOK    bool
	}{
		{"mcp__grafana__query_prometheus", "grafana", true},
		{"mcp__chrome-mcp__chrome_click", "chrome-mcp", true},
		{"mcp__x__", "x", true}, // empty tool suffix still yields a valid alias
		{"mcp__", "", false},
		{"shell", "", false},
		{"read_file", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		alias, ok := ParseMCPToolName(tt.tool)
		if alias != tt.wantAlias || ok != tt.wantOK {
			t.Errorf("ParseMCPToolName(%q) = (%q, %v), want (%q, %v)", tt.tool, alias, ok, tt.wantAlias, tt.wantOK)
		}
	}
}

func TestLoadAliases_MissingFileYieldsEmptyMap(t *testing.T) {
	withTempHydraHome(t)
	aliases, err := LoadAliases()
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 0 {
		t.Errorf("expected empty map, got %v", aliases)
	}
}

func TestSaveLoadAliases_RoundTrip(t *testing.T) {
	withTempHydraHome(t)
	want := map[string]aliasEntry{"grafana": {RegistryName: "docker.io/grafana/mcp-grafana", Status: StatusVerified}}
	if err := SaveAliases(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAliases()
	if err != nil {
		t.Fatal(err)
	}
	if got["grafana"].RegistryName != "docker.io/grafana/mcp-grafana" {
		t.Errorf("round-tripped alias = %+v", got["grafana"])
	}
}

func TestLoadAliases_CorruptFileIsAnError(t *testing.T) {
	withTempHydraHome(t)
	path := aliasPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAliases(); err == nil {
		t.Fatal("a corrupt aliases file must surface as an error, not silently yield an empty map")
	}
}

func TestClassificationForTool_DelistedServerIsClassifiedAsQuarantined(t *testing.T) {
	withTempHydraHome(t)
	if err := SaveAliases(map[string]aliasEntry{"gone": {RegistryName: "io.github.x/gone", Status: StatusVerified}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveStates(map[string]ServerState{"io.github.x/gone": {State: StateDelisted}}); err != nil {
		t.Fatal(err)
	}
	got, ok := ClassificationForTool("mcp__gone__do_thing")
	if !ok || got != ClassMCPQuarantined {
		t.Errorf("got (%q, %v), want (%q, true), delisted is grouped with quarantined", got, ok, ClassMCPQuarantined)
	}
}

func TestClassificationForTool_CorruptAliasesFileFailsClosed(t *testing.T) {
	withTempHydraHome(t)
	path := aliasPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ClassificationForTool("mcp__anything__tool"); ok {
		t.Error("a corrupt aliases file must fail closed (no classification), not error out of the ledger check path")
	}
}

func TestClassificationForTool_CorruptStatesFileFailsClosed(t *testing.T) {
	withTempHydraHome(t)
	if err := SaveAliases(map[string]aliasEntry{"x": {RegistryName: "io.github.x/x", Status: StatusVerified}}); err != nil {
		t.Fatal(err)
	}
	path := statePath()
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ClassificationForTool("mcp__x__tool"); ok {
		t.Error("a corrupt states file must fail closed (no classification), not error out of the ledger check path")
	}
}

func TestClassificationForTool_NonMCPToolIsUnclassified(t *testing.T) {
	withTempHydraHome(t)
	if _, ok := ClassificationForTool("shell"); ok {
		t.Error("a non-MCP tool name should never be classified")
	}
}

func TestClassificationForTool_NeverAuditedIsUnclassified(t *testing.T) {
	withTempHydraHome(t)
	if _, ok := ClassificationForTool("mcp__never-seen__tool"); ok {
		t.Error("a server that's never been audited should not be classified, that's not the same claim as 'unverified'")
	}
}

func TestClassificationForTool_UnresolvedServerIsUnverified(t *testing.T) {
	withTempHydraHome(t)
	if err := SaveAliases(map[string]aliasEntry{"chrome-mcp": {Status: StatusUnresolved}}); err != nil {
		t.Fatal(err)
	}
	got, ok := ClassificationForTool("mcp__chrome-mcp__chrome_click")
	if !ok || got != ClassMCPUnverified {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, ClassMCPUnverified)
	}
}

func TestClassificationForTool_QuarantinedServerIsClassified(t *testing.T) {
	withTempHydraHome(t)
	if err := SaveAliases(map[string]aliasEntry{"evil": {RegistryName: "io.github.x/evil", Status: StatusVerified}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveStates(map[string]ServerState{"io.github.x/evil": {State: StateQuarantined}}); err != nil {
		t.Fatal(err)
	}
	got, ok := ClassificationForTool("mcp__evil__do_thing")
	if !ok || got != ClassMCPQuarantined {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, ClassMCPQuarantined)
	}
}

func TestClassificationForTool_FlaggedServerIsClassified(t *testing.T) {
	withTempHydraHome(t)
	if err := SaveAliases(map[string]aliasEntry{"sketchy": {RegistryName: "io.github.x/sketchy", Status: StatusVerified}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveStates(map[string]ServerState{"io.github.x/sketchy": {State: StateFlagged}}); err != nil {
		t.Fatal(err)
	}
	got, ok := ClassificationForTool("mcp__sketchy__do_thing")
	if !ok || got != ClassMCPFlagged {
		t.Errorf("got (%q, %v), want (%q, true)", got, ok, ClassMCPFlagged)
	}
}

func TestClassificationForTool_TrustedServerIsNotClassified(t *testing.T) {
	withTempHydraHome(t)
	if err := SaveAliases(map[string]aliasEntry{"good": {RegistryName: "io.github.x/good", Status: StatusVerified}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveStates(map[string]ServerState{"io.github.x/good": {State: StateTrusted}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ClassificationForTool("mcp__good__do_thing"); ok {
		t.Error("a trusted server should not be flagged with any classification")
	}
}

func TestClassificationForTool_CaseInsensitiveAliasLookup(t *testing.T) {
	withTempHydraHome(t)
	if err := SaveAliases(map[string]aliasEntry{"grafana": {Status: StatusUnresolved}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ClassificationForTool("mcp__Grafana__query"); !ok {
		t.Error("alias lookup should be case-insensitive")
	}
}
