// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"strings"

	"github.com/ankit373/hydra/internal/ledger"
)

// ClassMCPBehaviorChange flags an MCP tool call whose Action type has never
// appeared before in this server's own ledger history, the local,
// backend-free slice of the design doc's Phase 6 ("ledger-as-flywheel")
// idea. The original design needed two things that don't exist: a
// cross-user aggregation backend, and per-tool declared-capability data the
// official registry doesn't publish. A server's own action history, already
// recorded by internal/ledger for every call, is itself a real baseline,
// no external data or cross-machine aggregation required. A server that has
// only ever shown ledger.Read suddenly showing ledger.Network is exactly
// postmark-mcp's real attack shape (an email tool that starts silently
// BCC'ing, a network-shaped action), catchable from local history alone,
// before any CVE exists for it.
const ClassMCPBehaviorChange = "mcp-behavior-change"

// ObservedActions is the set of ledger.Action values ever recorded for one
// MCP server alias.
type ObservedActions map[ledger.Action]bool

// BehaviorProfiles builds a per-server-alias action history from a slice of
// ledger events, purely local, no external data.
func BehaviorProfiles(events []ledger.Event) map[string]ObservedActions {
	profiles := make(map[string]ObservedActions)
	for _, e := range events {
		alias, ok := ParseMCPToolName(e.Tool)
		if !ok {
			continue
		}
		alias = strings.ToLower(alias)
		if profiles[alias] == nil {
			profiles[alias] = ObservedActions{}
		}
		profiles[alias][e.Action] = true
	}
	return profiles
}

// BehaviorClassification reports ClassMCPBehaviorChange, true when action
// would be the first of its kind ever recorded for tool's MCP server alias,
// given priorEvents (everything logged strictly before this call). Returns
// ("", false) for a non-MCP tool name, or when the server has no prior
// history at all, a server's very first-ever call has nothing to compare
// against, and flagging it as "novel" would be noise, not a signal.
func BehaviorClassification(priorEvents []ledger.Event, tool string, action ledger.Action) (string, bool) {
	alias, ok := ParseMCPToolName(tool)
	if !ok {
		return "", false
	}
	alias = strings.ToLower(alias)

	profile, seenBefore := BehaviorProfiles(priorEvents)[alias]
	if !seenBefore {
		return "", false
	}
	if profile[action] {
		return "", false
	}
	return ClassMCPBehaviorChange, true
}
