// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ankit373/hydra/internal/provider"
)

// Boundary is the limit of what the egress gate can enforce, stated in words
// rather than left to be inferred from a percentage.
//
// The gate sees the request Hydra composes. It does not see inside a CLI-agent
// head, which is a separate program that reads files and reaches the network
// on its own account. Every posture number in this report is about the first
// kind of head, and a reader who does not know that will read "enforced" as a
// guarantee it is not.
type Boundary struct {
	// Governed heads receive a request Hydra builds itself, over HTTP, so the
	// gate sees every byte before it leaves.
	Governed []string `json:"governed"`

	// Opaque heads are separate programs handed a prompt. Hydra guarantees
	// what it passes them and nothing about what they read or send next.
	Opaque []string `json:"opaque"`

	// OpaqueLocalOnly is the subset of Opaque the catalog declares never
	// leaves the machine. That is a claim about another program rather than
	// something Hydra observes, so it narrows the hole without closing it.
	OpaqueLocalOnly []string `json:"opaqueLocalOnly"`
}

// Total reports whether "nothing leaves this machine" is enforced here rather
// than merely configured. It holds only when nothing opaque can be routed to,
// because for every other head the gate is the thing that decides.
func (b Boundary) Total() bool { return len(b.Opaque) == 0 }

// AssessBoundary classifies the discovered heads. An Executable is the
// evidence, not a name list: it means Hydra hands the work to another program
// instead of composing the request itself, which is exactly where the gate
// stops being able to see.
func AssessBoundary(heads []provider.Head) Boundary {
	var b Boundary
	for _, h := range heads {
		if h.Executable == "" {
			b.Governed = append(b.Governed, h.ID)
			continue
		}
		b.Opaque = append(b.Opaque, h.ID)
		if h.LocalOnly {
			b.OpaqueLocalOnly = append(b.OpaqueLocalOnly, h.ID)
		}
	}
	sort.Strings(b.Governed)
	sort.Strings(b.Opaque)
	sort.Strings(b.OpaqueLocalOnly)
	return b
}

// boundaryCheck renders the boundary as one of the report's checks, naming the
// heads rather than counting them: "3 heads are opaque" is not actionable, and
// which three is.
func boundaryCheck(b Boundary) Check {
	const name = "Enforcement boundary"
	if len(b.Governed) == 0 && len(b.Opaque) == 0 {
		return Check{Name: name, Status: "no heads discovered",
			Detail: "nothing was found to classify, run `hyctl probe`"}
	}
	if b.Total() {
		return Check{Name: name,
			Status: fmt.Sprintf("total, %d head(s) all governed", len(b.Governed)),
			Detail: "every head takes a request Hydra composes, so the egress gate sees everything that leaves"}
	}

	detail := fmt.Sprintf("Hydra guarantees what it sends to %s, not what they read or send on their own account",
		HeadList(b.Opaque))
	if n := len(b.Governed); n > 0 {
		detail += fmt.Sprintf(". The gate governs the other %d head(s), where Hydra composes the request itself", n)
	}
	if n := len(b.OpaqueLocalOnly); n > 0 {
		verb := "are"
		if n == 1 {
			verb = "is"
		}
		detail += fmt.Sprintf(". Of those, %s %s declared local-only, which is a claim about the program itself rather than something Hydra verifies",
			strings.Join(b.OpaqueLocalOnly, ", "), verb)
	}
	return Check{Name: name,
		Status: fmt.Sprintf("partial, %d of %d head(s) are separate programs",
			len(b.Opaque), len(b.Opaque)+len(b.Governed)),
		Detail: detail}
}

// HeadList renders a head list for prose, bounded so a machine with forty
// heads does not turn one sentence into a wall. Exported because the CLI
// prints the same list in its own summary line, and two bounds that drift
// apart is how one surface starts wrapping and the other does not.
func HeadList(ids []string) string {
	const max = 4
	if len(ids) <= max {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(ids[:max], ", "), len(ids)-max)
}
