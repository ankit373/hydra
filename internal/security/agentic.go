// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"os"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/graph"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/workspace"
)

// OWASP split the problem in two. The Top 10 for LLM Applications governs what
// a model says; the Top 10 for Agentic Applications (ASI01-ASI10, published 9
// December 2025) governs what a system does. Hydra is squarely the second and
// scored itself only against the first, so seven of the ten risks that
// actually describe it were not assessed at all.
//
// Same discipline as owasp.go: every status is read from observable state, and
// a category with no mechanism says Gap rather than borrowing credit from a
// neighbouring one.

// AgenticCoverage is Hydra's posture against the OWASP Top 10 for Agentic
// Applications.
type AgenticCoverage struct {
	Categories     []Category `json:"categories"`
	Applicable     int        `json:"applicable"`
	Covered        int        `json:"covered"`
	Partial        int        `json:"partial"`
	PercentCovered float64    `json:"percentCovered"`
}

// computeAgentic classifies ASI01-ASI10 against Hydra's real state. pol, sc and
// integrityIntact are what Build already loaded, passed in rather than
// rederived, the same arrangement computeCoverage uses.
func computeAgentic(pol ledger.Policy, sc SupplyChain, integrityIntact bool) AgenticCoverage {
	cats := []Category{
		asi01GoalHijack(),
		asi02ToolMisuse(pol),
		asi03IdentityAbuse(),
		asi04SupplyChain(sc),
		asi05CodeExecution(),
		asi06MemoryPoisoning(),
		asi07InterAgentComms(),
		asi08CascadingFailures(),
		asi09HumanTrust(pol),
		asi10RogueAgents(sc, integrityIntact),
	}

	applicable, covered, partial, pct := tally(cats)
	return AgenticCoverage{
		Categories: cats, Applicable: applicable,
		Covered: covered, Partial: partial, PercentCovered: pct,
	}
}

// asi01: content Hydra passes between agents is fenced with a content-derived
// nonce, so it cannot close its own fence. Head output on the way back to the
// orchestrator is not, and CLAUDE.md's own protocol says to apply it to disk.
func asi01GoalHijack() Category {
	return Category{
		ID: "ASI01", Name: "Agent Goal Hijack", Status: Partial,
		Detail: "every point where one model's output becomes another's prompt is fenced as data with a " +
			"content-derived nonce (a2a, parallel, the swarm judge, workflow steps), but the answer an " +
			"orchestrator reads carries only a convention and a credential warning, and the " +
			"injection-marker scan is a keyword heuristic",
	}
}

// asi02: the ledger gate is what stands between a head and an action. A
// fail-open policy means it records without deciding, which is not a gate.
func asi02ToolMisuse(pol ledger.Policy) Category {
	c := Category{ID: "ASI02", Name: "Tool Misuse and Exploitation"}
	def := pol.Default
	if def == "" {
		def = ledger.Allow
	}
	switch {
	case len(pol.Rules) == 0 && def == ledger.Allow:
		c.Status = Gap
		c.Detail = "no access policy is in force: every dispatch and tool call is allowed by default"
	case def == ledger.Allow:
		c.Status = Partial
		c.Detail = fmt.Sprintf("%d rule(s) gate tool use, but the default is allow, so anything no rule "+
			"names is permitted", len(pol.Rules))
	default:
		c.Status = Configured
		c.Detail = fmt.Sprintf("%d rule(s) gate tool use over a default-%s policy, and the check fails "+
			"closed when a decision cannot be computed", len(pol.Rules), def)
	}
	return c
}

// asi03: the finding was that cmd.Env was set in no executor at all, so every
// head held every provider's credential. Now enforced by construction.
func asi03IdentityAbuse() Category {
	return Category{
		ID: "ASI03", Name: "Identity and Privilege Abuse", Status: Enforced,
		Detail: "a head subprocess receives its own provider's credential and no other, plus a base " +
			"environment carrying no secrets; AWS, SSH agent and every other provider's key are withheld",
	}
}

// asi04: change detection over head binaries and local model weights. Not
// provenance, and the baseline is a plain file, so never Enforced.
func asi04SupplyChain(sc SupplyChain) Category {
	c := Category{ID: "ASI04", Name: "Agentic Supply Chain"}
	if len(sc.Binaries) == 0 {
		c.Status = Gap
		c.Detail = "nothing is fingerprinted, so a replaced agent binary or swapped model would go unnoticed"
		return c
	}
	c.Status = Configured
	c.Detail = fmt.Sprintf("%d artifact(s) fingerprinted and MCP servers carry a trust lifecycle, though "+
		"origin is not verified and the stored baseline is not itself tamper-evident", len(sc.Binaries))
	return c
}

// asi05: validators check what a model produced. Nothing confines the
// validator itself, and its command line comes from the repo.
func asi05CodeExecution() Category {
	c := Category{ID: "ASI05", Name: "Unexpected Code Execution"}
	reg, err := workspace.Load(config.ScriptHome())
	if err != nil || !reg.HasAnyValidator() {
		c.Status = Gap
		c.Detail = "no workspace validator runs, so model output reaches disk unchecked, and nothing " +
			"confines the subprocesses Hydra spawns"
		return c
	}
	c.Status = Partial
	c.Detail = "a validator runs after every edit and rolls back on failure, but validator and oracle " +
		"commands come from the repo and run unconfined at full user privilege"
	return c
}

// asi06: last_handoff.json is persistent cross-run memory that any local
// process can write. Fencing makes it data; nothing makes it authentic.
func asi06MemoryPoisoning() Category {
	return Category{
		ID: "ASI06", Name: "Memory and Context Poisoning", Status: Partial,
		Detail: "every handoff field is fenced as untrusted data, so a poisoned one cannot issue " +
			"instructions, but nothing authenticates the file and any local process can write it",
	}
}

// asi07: vector clocks give causal ordering and conflict detection, which is
// integrity of *ordering*. Nothing authenticates the sender.
func asi07InterAgentComms() Category {
	return Category{
		ID: "ASI07", Name: "Insecure Inter-Agent Communication", Status: Partial,
		Detail: "handoffs carry vector clocks for causal ordering and concurrent-edit conflict detection, " +
			"and are written 0600, but carry no signature, so the sender is asserted rather than proven",
	}
}

// asi08: the graph is what turns "this edit is risky" into a number. Without
// graph.json there is no blast radius and a hub file looks like a leaf.
func asi08CascadingFailures() Category {
	c := Category{ID: "ASI08", Name: "Cascading Failures"}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	g, gErr := graph.Load(graph.DefaultPath(cwd))
	if gErr != nil || g.Empty() {
		c.Status = Gap
		c.Detail = "no graph.json here, so blast radius is unknown and an edit to a widely-depended-on " +
			"file demands no more confidence than an edit to a leaf"
		return c
	}
	c.Status = Configured
	c.Detail = fmt.Sprintf("a dependency graph raises the confidence bar by blast radius (Molloy-Reed "+
		"kappa %.2f, %s) and fallback chains bound a single head's failure",
		g.Kappa(), percolationPhrase(g.Percolates()))
	return c
}

// percolationPhrase says what kappa means without making the reader look it
// up: at or above 2 a giant component exists, so one bad edit can cascade.
func percolationPhrase(percolates bool) string {
	if percolates {
		return "cascade-capable core"
	}
	return "no cascade-capable core"
}

// asi09: the risk that an agent controls what a human sees at approval time.
// Hydra's exposure here is its own dashboard, and the honest-reporting work is
// the mitigation: a withheld score, a Partial status, sanitised ledger text.
func asi09HumanTrust(pol ledger.Policy) Category {
	c := Category{ID: "ASI09", Name: "Human-Agent Trust Exploitation", Status: Configured}
	c.Detail = "every ledger-derived string is stripped of control characters before printing, so a " +
		"hostile tool name cannot rewrite the line it appears on; the coverage score is withheld " +
		"rather than shown while the policy is fail-open, and a detective-only control reports partial"
	if len(pol.Rules) > 0 {
		c.Detail += "; an ask verdict names the head and resource it is asking about"
	}
	return c
}

// asi10: an agent operating outside policy while looking legitimate. The
// tamper-evident log is what makes drift visible after the fact.
func asi10RogueAgents(sc SupplyChain, integrityIntact bool) Category {
	c := Category{ID: "ASI10", Name: "Rogue Agents"}
	if !integrityIntact {
		c.Status = Gap
		c.Detail = "the ledger chain is broken, so the record that would show an agent acting outside " +
			"policy cannot be trusted"
		return c
	}
	if sc.Changed > 0 {
		c.Status = Partial
		c.Detail = fmt.Sprintf("the ledger is hash-chained and intact, but %d fingerprinted artifact(s) "+
			"changed since last seen and have not been reviewed", sc.Changed)
		return c
	}
	c.Status = Configured
	c.Detail = "the ledger is hash-chained and intact, head binaries and model weights match their " +
		"baselines, and MCP servers drop to provisional on any version bump"
	return c
}
