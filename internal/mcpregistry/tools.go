// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/util"
)

// Tool is one entry of an MCP server's tools/list result: the text and schema
// the model reads when it decides what to call.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// UnmarshalJSON is the one place a tool enters from disk, a snapshot or the saved state.
// The indented state file must hash the same as a compact snapshot, and names reach the terminal.
func (t *Tool) UnmarshalJSON(raw []byte) error {
	type plain Tool
	var p plain
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	p.Name = util.SafeTerminal(p.Name)
	p.InputSchema = canonicalSchema(p.InputSchema)
	*t = Tool(p)
	return nil
}

// ToolHash fingerprints everything the model reads about one tool, so a rewritten
// description is a change even when the server keeps its version.
func ToolHash(t Tool) string {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(t.Name), []byte(t.Description), t.InputSchema} {
		h.Write(part)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ToolsSnapshotPath is where audit looks for a server's saved tools/list result.
// Audit never launches a server to ask: that runs unvetted code with the client's env.
func ToolsSnapshotPath(server string) string {
	return filepath.Join(config.Dir(), "mcp_tools", server+".json")
}

// loadToolSnapshot returns nil, nil when there is no snapshot: tools not observed
// this run is not the same as a server that lists no tools.
func loadToolSnapshot(server string) (map[string]Tool, error) {
	// A config key with a path separator could point the read outside mcp_tools.
	if server == "" || server != filepath.Base(server) || server == ".." {
		return nil, nil
	}
	path := ToolsSnapshotPath(server)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// A pointer, so a saved JSON-RPC response with no top-level list is an error, not zero tools.
	var result struct {
		Tools *[]Tool `json:"tools"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("tools snapshot %s: %w", path, err)
	}
	if result.Tools == nil {
		return nil, fmt.Errorf(`tools snapshot %s: no "tools" list, want {"tools": [...]}`, path)
	}
	tools := make(map[string]Tool, len(*result.Tools))
	for _, t := range *result.Tools {
		if t.Name == "" {
			return nil, fmt.Errorf("tools snapshot %s: a tool has no name", path)
		}
		tools[t.Name] = t
	}
	return tools, nil
}

// canonicalSchema re-encodes a schema with sorted keys and no spacing, so a
// server that only reorders or reformats its JSON does not read as a change.
func canonicalSchema(raw json.RawMessage) json.RawMessage {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// ToolChange is one tool that differs from the last observed list.
type ToolChange struct {
	Tool   string `json:"tool"`
	Before *Tool  `json:"before,omitempty"` // nil when the tool is new
	After  *Tool  `json:"after,omitempty"`  // nil when the tool was removed
}

// ToolDrift is the last tool change that cost a server its trust. Kept until the
// server earns trust again, so the before and after stay visible in every audit.
type ToolDrift struct {
	At      time.Time    `json:"at"`
	Changes []ToolChange `json:"changes"`
}

// DiffTools lists every tool added, removed or rewritten between two observations,
// sorted by name so audit output is stable run to run.
func DiffTools(before, after map[string]Tool) []ToolChange {
	names := make(map[string]Tool, len(before)+len(after))
	maps.Copy(names, before)
	maps.Copy(names, after)
	var changes []ToolChange
	for _, name := range slices.Sorted(maps.Keys(names)) {
		old, hadOld := before[name]
		cur, hasCur := after[name]
		if hadOld && hasCur && ToolHash(old) == ToolHash(cur) {
			continue
		}
		change := ToolChange{Tool: name}
		if hadOld {
			change.Before = &old
		}
		if hasCur {
			change.After = &cur
		}
		changes = append(changes, change)
	}
	return changes
}

// Lines renders a change as the before and after of each part that moved.
func (c ToolChange) Lines() []string {
	switch {
	case c.Before == nil:
		return []string{fmt.Sprintf("%s added: %q", c.Tool, c.After.Description)}
	case c.After == nil:
		return []string{fmt.Sprintf("%s removed, was: %q", c.Tool, c.Before.Description)}
	}
	lines := []string{c.Tool + " changed"}
	if c.Before.Description != c.After.Description {
		lines = append(lines,
			fmt.Sprintf("  description before: %q", c.Before.Description),
			fmt.Sprintf("  description after:  %q", c.After.Description))
	}
	if string(c.Before.InputSchema) != string(c.After.InputSchema) {
		lines = append(lines,
			fmt.Sprintf("  input schema before: %s", c.Before.InputSchema),
			fmt.Sprintf("  input schema after:  %s", c.After.InputSchema))
	}
	return lines
}
