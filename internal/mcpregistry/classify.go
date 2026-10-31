// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/ankit373/hydra/internal/config"
)

// aliasEntry is the minimum needed to classify a local server alias without
// re-running a full audit: which registry entry it resolved to (if any) and
// whether it resolved at all.
type aliasEntry struct {
	RegistryName string `json:"registry_name,omitempty"`
	Status       Status `json:"status"`
}

func aliasPath() string {
	return filepath.Join(config.Dir(), "mcp_registry_aliases.json")
}

// LoadAliases reads the persisted local-alias -> registry-resolution map,
// keyed lowercase. A missing file yields an empty map.
func LoadAliases() (map[string]aliasEntry, error) {
	raw, err := os.ReadFile(aliasPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]aliasEntry{}, nil
		}
		return nil, err
	}
	var m map[string]aliasEntry
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]aliasEntry{}
	}
	return m, nil
}

// SaveAliases writes the alias map atomically.
func SaveAliases(m map[string]aliasEntry) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := aliasPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	defer func() { _ = os.Remove(tmp) }()
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return os.WriteFile(path, raw, 0o600)
	}
	return nil
}

// ParseMCPToolName extracts the server alias from an MCP-style tool name,
// the "mcp__<alias>__<tool>" convention Claude Code and other MCP clients
// expose tools under. Returns ok=false for anything that doesn't match,
// including a bare "mcp__" with no alias.
func ParseMCPToolName(tool string) (alias string, ok bool) {
	const prefix = "mcp__"
	if !strings.HasPrefix(tool, prefix) {
		return "", false
	}
	rest := tool[len(prefix):]
	parts := strings.SplitN(rest, "__", 2)
	if parts[0] == "" {
		return "", false
	}
	return parts[0], true
}

// Ledger classification tags for MCP-derived risk, distinct from
// policy's "pii" tag, and distinct from each other so a policy author can
// gate differently on "not even in the registry" vs. "in the registry but
// currently flagged/quarantined".
const (
	ClassMCPUnverified  = "mcp-unverified"
	ClassMCPFlagged     = "mcp-flagged"
	ClassMCPQuarantined = "mcp-quarantined"
)

// ClassificationForTool is Phase 4's wiring point (design doc §6/§13): a low
// trust score or an unresolved server becomes an auto-derived ledger
// classification, the same mechanism policy.ContainsPII already uses for
// content sensitivity, applied to MCP server risk instead. Returns
// ok=false when tool isn't an MCP tool name, when the alias has never been
// seen by an audit run, or when the server is trusted/provisional/new (no
// flag warranted), callers should fall through to their own classification
// logic in all of those cases, not treat false as an error.
func ClassificationForTool(tool string) (classification string, ok bool) {
	alias, isMCP := ParseMCPToolName(tool)
	if !isMCP {
		return "", false
	}

	aliases, err := LoadAliases()
	if err != nil {
		return "", false
	}
	entry, found := aliases[strings.ToLower(alias)]
	if !found {
		return "", false
	}
	if entry.Status == StatusUnresolved {
		return ClassMCPUnverified, true
	}

	states, err := LoadStates()
	if err != nil {
		return "", false
	}
	state, hasState := states[entry.RegistryName]
	if !hasState {
		return "", false
	}
	switch state.State {
	case StateQuarantined, StateDelisted:
		return ClassMCPQuarantined, true
	case StateFlagged:
		return ClassMCPFlagged, true
	default:
		return "", false
	}
}
