// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ankit373/hydra/internal/provider"
)

// LocalServer is what a scan of one local model server found. Plain data: the
// probing lives in internal/probe, so this package keeps the no-network
// invariant Build rests on and stays testable on literals.
type LocalServer struct {
	Endpoint string `json:"endpoint"`
	Kind     string `json:"kind"`
	Version  string `json:"version,omitempty"` // empty is unread, never assumed
	// OffHost is a non-loopback address of this machine that answered the same
	// identity probe. Set means the server is not only serving this machine.
	OffHost string `json:"offHost,omitempty"`
	// Tried counts the non-loopback addresses the scan could reach for. At zero
	// an empty OffHost says nothing at all, which the detail has to admit.
	Tried int `json:"tried"`
}

// localServerCheck reports whether the model servers Hydra routes to are
// serving more than this machine, and what version they are.
//
// Ollama ships without authentication and prints no warning when bound to
// 0.0.0.0, so "local-only" was a claim about the program rather than something
// Hydra had looked at (#923).
func localServerCheck(local int, servers []LocalServer) Check {
	c := Check{Name: "Local server exposure"}
	switch {
	case local == 0:
		c.Status = "not evaluated"
		c.Detail = "no local model server was discovered on this machine"
		return c
	case servers == nil:
		c.Status = "not scanned"
		c.Detail = fmt.Sprintf("%d local head(s) discovered, and this view did not scan their "+
			"server(s); `hyctl security` does", local)
		return c
	}

	exposed := make([]LocalServer, 0, len(servers))
	var tried int
	for _, s := range servers {
		tried += s.Tried
		if s.OffHost != "" {
			exposed = append(exposed, s)
		}
	}
	sort.Slice(exposed, func(i, j int) bool { return exposed[i].Endpoint < exposed[j].Endpoint })

	if len(exposed) > 0 {
		c.Status = fmt.Sprintf("%d exposed", len(exposed))
		parts := make([]string, 0, len(exposed))
		for _, s := range exposed {
			parts = append(parts, fmt.Sprintf("%s answers on %s", describeServer(s), s.OffHost))
		}
		c.Detail = strings.Join(parts, "; ") + ". That address is not loopback, so the server is " +
			"serving more than this machine, and neither Ollama nor vLLM requires authentication " +
			"by default. Bind it to 127.0.0.1, or put something in front of it that authenticates"
		return c
	}

	if tried == 0 {
		c.Status = "not evaluated"
		c.Detail = fmt.Sprintf("%s; this machine has no non-loopback address to probe from, so "+
			"nothing was ruled out", serverList(servers))
		return c
	}
	c.Status = "loopback only"
	c.Detail = fmt.Sprintf("%s answered only on loopback across %d address(es). A host firewall "+
		"that drops the probe produces this same result, so it is weaker evidence than the "+
		"exposed case", serverList(servers), tried)
	return c
}

// describeServer names a server the way the reader would recognise it, with the
// version when it could be read and nothing where it could not.
func describeServer(s LocalServer) string {
	if s.Version == "" {
		return fmt.Sprintf("%s (%s, version unread)", s.Kind, s.Endpoint)
	}
	return fmt.Sprintf("%s %s (%s)", s.Kind, s.Version, s.Endpoint)
}

func serverList(servers []LocalServer) string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, describeServer(s))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// localHeads counts heads discovered by dialling this machine, the set a scan
// would have had something to say about. Keyed on Source rather than on a list
// of server kinds, so the count cannot drift from what the port provider finds.
func localHeads(heads []provider.Head) int {
	var n int
	for _, h := range heads {
		if h.Source == "port" {
			n++
		}
	}
	return n
}
