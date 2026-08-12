// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/provider"
)

// Least-privilege review, and the AI-BOM.
//
// Two questions a CISO asks that nothing here answered:
//
//	"What can each agent actually do, versus what does it actually need?"
//	"What models are in my estate, where did they come from, and do they
//	 leave the building?"
//
// Both are answerable from records already kept. The first is an entitlement
// review: compare what an agent was *allowed* to touch against what it
// actually touched, and an agent permitted far more than it uses is
// over-permissioned — the classic least-privilege finding. The second is an
// inventory with provenance, which is what an AI bill of materials is.

// AgentPrivilege is one agent's observed footprint.
type AgentPrivilege struct {
	Agent string `json:"agent"`
	// Allowed and Denied are access counts, not permissions.
	Allowed int `json:"allowed"`
	Denied  int `json:"denied"`
	// Resources and Actions are what it actually touched.
	Resources []string `json:"resources,omitempty"`
	Actions   []string `json:"actions,omitempty"`
	// Heads are the models it drove.
	Heads []string `json:"heads,omitempty"`
	// Unscoped is true when no policy rule names this agent, so it is
	// governed only by the default — the practical definition of
	// over-permissioned here, since Hydra's default is allow.
	Unscoped bool `json:"unscoped"`
	// WritesOrExecs counts the operations that can change state, which is
	// what makes an unscoped agent consequential rather than merely untidy.
	WritesOrExecs int `json:"writesOrExecs"`
}

// ReviewPrivilege builds the entitlement review from the ledger and policy.
func ReviewPrivilege(events []ledger.Event, pol ledger.Policy) []AgentPrivilege {
	scoped := map[string]bool{}
	for _, r := range pol.Rules {
		if r.Agent != "" && r.Agent != "*" {
			scoped[r.Agent] = true
		}
	}

	type acc struct {
		allowed, denied, mutating int
		resources, actions, heads map[string]bool
	}
	byAgent := map[string]*acc{}
	for _, e := range events {
		if e.Agent == "" {
			continue
		}
		a, ok := byAgent[e.Agent]
		if !ok {
			a = &acc{resources: map[string]bool{}, actions: map[string]bool{}, heads: map[string]bool{}}
			byAgent[e.Agent] = a
		}
		if e.Decision == ledger.Deny {
			a.denied++
		} else {
			a.allowed++
		}
		if e.Resource != "" {
			a.resources[e.Resource] = true
		}
		if e.Action != "" {
			a.actions[string(e.Action)] = true
		}
		if e.Tool != "" {
			a.heads[e.Tool] = true
		}
		if e.Action == ledger.Write || e.Action == ledger.Exec || e.Action == ledger.Network {
			a.mutating++
		}
	}

	out := make([]AgentPrivilege, 0, len(byAgent))
	for name, a := range byAgent {
		out = append(out, AgentPrivilege{
			Agent: name, Allowed: a.allowed, Denied: a.denied,
			Resources: sortedSet(a.resources), Actions: sortedSet(a.actions),
			Heads:    sortedSet(a.heads),
			Unscoped: !scoped[name], WritesOrExecs: a.mutating,
		})
	}
	// Most consequential first: an unscoped agent that changes state.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Unscoped != out[j].Unscoped {
			return out[i].Unscoped
		}
		return out[i].WritesOrExecs > out[j].WritesOrExecs
	})
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// privilegeCheck reports the least-privilege posture.
func privilegeCheck(ps []AgentPrivilege) Check {
	const name = "Least privilege"
	if len(ps) == 0 {
		return Check{Name: name, Status: "no agent activity",
			Detail: "no ledger event names an agent, so there is no footprint to review"}
	}
	var unscoped []string
	for _, p := range ps {
		if p.Unscoped && p.WritesOrExecs > 0 {
			unscoped = append(unscoped, fmt.Sprintf("%s (%d state-changing)", p.Agent, p.WritesOrExecs))
		}
	}
	if len(unscoped) == 0 {
		return Check{Name: name, Status: fmt.Sprintf("%d agent(s) reviewed", len(ps)),
			Detail: "every agent performing state-changing operations is named by a policy rule"}
	}
	return Check{Name: name, Status: fmt.Sprintf("%d agent(s) unscoped", len(unscoped)),
		Detail: "no policy rule names these agents, so they run under the default while changing state: " +
			strings.Join(unscoped, ", ")}
}

// BOMEntry is one model in the estate.
type BOMEntry struct {
	HeadID   string `json:"headId"`
	Name     string `json:"name,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Source is how it was discovered: cli, env, port.
	Source string `json:"source,omitempty"`
	// Origin is "builtin" (curated catalog) or "user" (added at runtime).
	Origin string `json:"origin,omitempty"`
	// Local is true when the head never routes over the network.
	Local bool `json:"local"`
	// Fingerprint is the binary hash where one exists.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Used is true when the ledger shows this head actually handling work —
	// an inventory of what is installed is less useful than one that says
	// what is live.
	Used bool `json:"used"`
}

// BuildBOM inventories the estate: what is installed, where it came from,
// whether it leaves the machine, and whether it is actually being used.
func BuildBOM(heads []provider.Head, events []ledger.Event, sc SupplyChain) []BOMEntry {
	used := map[string]bool{}
	for _, e := range events {
		if e.Tool != "" {
			used[e.Tool] = true
		}
	}
	prints := map[string]string{}
	for _, b := range sc.Binaries {
		prints[b.HeadID] = b.SHA256
	}

	out := make([]BOMEntry, 0, len(heads))
	for _, h := range heads {
		out = append(out, BOMEntry{
			HeadID: h.ID, Name: h.Name, Provider: h.Provider, Source: h.Source,
			Origin: h.Meta["model_source"], Local: h.LocalOnly,
			Fingerprint: prints[h.ID], Used: used[h.ID],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HeadID < out[j].HeadID })
	return out
}

// bomCheck summarises the estate.
func bomCheck(bom []BOMEntry) Check {
	const name = "Model inventory (AI-BOM)"
	if len(bom) == 0 {
		return Check{Name: name, Status: "no heads discovered",
			Detail: "nothing was found to inventory — run `hyctl probe`"}
	}
	var remote, unvetted, used int
	for _, b := range bom {
		if !b.Local {
			remote++
		}
		if b.Origin == "user" {
			unvetted++
		}
		if b.Used {
			used++
		}
	}
	detail := fmt.Sprintf("%d head(s): %d route off-machine, %d used in recorded work", len(bom), remote, used)
	if unvetted > 0 {
		detail += fmt.Sprintf(", %d added at runtime rather than from the curated catalog", unvetted)
	}
	return Check{Name: name, Status: fmt.Sprintf("%d head(s), %d remote", len(bom), remote), Detail: detail}
}
