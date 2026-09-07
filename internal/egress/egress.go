// SPDX-License-Identifier: MIT

// Package egress decides whether content may leave the machine.
//
// The gate sits at the executor boundary rather than at the HTTP call,
// because agy and cli heads make their own network calls in a process Hydra
// does not control. The last moment Hydra has authority over a payload is the
// instant it hands it to an executor, so that is where this runs.
package egress

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ankit373/hydra/internal/glob"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/registry"
	"gopkg.in/yaml.v3"
)

// Source is where a piece of content came from. It is not optional: a Part
// with no Source is refused, which is what stops a new code path from
// shipping content it never declared the origin of.
type Source string

const (
	SourceUser Source = "user" // typed by the human at the CLI
	SourceFile Source = "file" // read from disk
	SourceHead Source = "head" // a model's output
	SourceMCP  Source = "mcp"  // an MCP tool result
	SourceWeb  Source = "web"  // fetched over the network
	SourceEnv  Source = "env"  // an environment variable
)

// Sensitivity is how far a piece of content may travel. Ordered, so the
// classification of a payload is the maximum over its parts.
type Sensitivity int

const (
	Public Sensitivity = iota
	Internal
	Secret // must not reach a head that leaves the machine
)

func (s Sensitivity) String() string {
	switch s {
	case Secret:
		return "secret"
	case Internal:
		return "internal"
	default:
		return "public"
	}
}

// Part is one span of a payload with its provenance and classification.
type Part struct {
	Content string
	Source  Source
	Origin  string // file path, head id, url; empty for SourceUser
	Sens    Sensitivity
	Reasons []string // which rule fired, recorded so a denial can be explained
}

// SinkKind is where a payload is about to go.
type SinkKind string

const (
	SinkLocal     SinkKind = "local"     // a LocalOnly head; never leaves the machine
	SinkRemote    SinkKind = "remote"    // an API or subprocess head that reaches the network
	SinkTelemetry SinkKind = "telemetry" // an OTLP endpoint
	SinkDisk      SinkKind = "disk"      // the payload store
)

// Sink identifies the destination for attribution in the ledger.
type Sink struct {
	Kind SinkKind
	Head string
	Host string
}

// Decision is the gate's answer. It deliberately mirrors ledger.Decision's
// vocabulary without importing it, so egress stays a leaf package that the
// ledger and dispatch can both depend on.
type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
)

// Verdict is Guard's answer plus everything needed to explain it to a human
// and record it in the ledger.
type Verdict struct {
	Decision Decision
	Sens     Sensitivity
	Reason   string
	// Origins names the parts that produced the classification, so a denial
	// says which file was the problem rather than only that one existed.
	Origins []string
}

// Rules is the path-based classification shipped in registry/sensitivity.yaml.
type Rules struct {
	Secret []string `yaml:"secret"`
	Allow  []string `yaml:"allow"`
}

var (
	rulesMu    sync.Mutex
	rulesCache = map[string]*Rules{}
)

// LoadRules reads sensitivity.yaml, preferring an operator's on-disk copy.
//
// Cached per home. The embedded copy cannot change while the process runs,
// and an edited override taking effect on restart matches how every other
// registry file behaves.
func LoadRules(home string) (*Rules, error) {
	rulesMu.Lock()
	defer rulesMu.Unlock()
	if r, ok := rulesCache[home]; ok {
		return r, nil
	}
	raw, err := registry.Read(home, "sensitivity.yaml")
	if err != nil {
		return nil, fmt.Errorf("egress: read sensitivity.yaml: %w", err)
	}
	var r Rules
	if err := yaml.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("egress: parse sensitivity.yaml: %w", err)
	}
	if len(r.Secret) == 0 {
		// An empty rule set is indistinguishable from a working one until
		// something leaks, so refuse it rather than run wide open.
		return nil, fmt.Errorf("egress: sensitivity.yaml declares no secret patterns")
	}
	rulesCache[home] = &r
	return &r, nil
}

// ClassifyPath rates a file by where it is rather than what is in it. This is
// the rule that closes a category: a `.env` is refused whether or not anything
// inside it matches a detector.
func (r *Rules) ClassifyPath(path string) (Sensitivity, []string) {
	if path == "" {
		return Public, nil
	}
	p := strings.TrimSpace(path)
	for _, pat := range r.Allow {
		if glob.Match(pat, p) {
			return Public, nil
		}
	}
	for _, pat := range r.Secret {
		if glob.Match(pat, p) {
			return Secret, []string{"path:" + pat}
		}
	}
	return Public, nil
}

// ClassifyContent rates text by what a detector recognizes in it. Strictly a
// second opinion: it can only catch shapes someone wrote a pattern for, which
// is why ClassifyPath exists.
func ClassifyContent(content string) (Sensitivity, []string) {
	names := policy.DetectPII(policy.Request{Prompt: content})
	if len(names) == 0 {
		return Public, nil
	}
	reasons := make([]string, 0, len(names))
	for _, n := range names {
		reasons = append(reasons, "content:"+n)
	}
	return Secret, reasons
}

// ClassifyPart fills in Sens and Reasons. An already-classified part is left
// alone, so a caller that knows more than the detectors can say so.
//
// Which classifier runs depends on the source, and the split is deliberate:
//
//   - SourceFile gets path rules. Deterministic and closed-category, which is
//     what makes "no local config leaves the machine" a guarantee.
//   - SourceHead, SourceMCP and SourceWeb get content rules. This is content
//     Hydra did not originate and has no path for, so a heuristic is all there
//     is, and forwarding a key a model just emitted is worth catching.
//   - SourceUser gets neither. A typed prompt is already governed by the
//     configurable pii policy in internal/policy, and re-deciding it here
//     would override a setting the operator chose and scan the same string
//     twice (#522).
func (r *Rules) ClassifyPart(p Part) Part {
	if p.Sens != Public || len(p.Reasons) > 0 {
		return p
	}
	switch p.Source {
	case SourceFile:
		p.Sens, p.Reasons = r.ClassifyPath(p.Origin)
	case SourceHead, SourceMCP, SourceWeb:
		// An allow-listed origin is a deliberate exemption, so content
		// detection does not get to override it back to Secret.
		if !r.allowed(p.Origin) {
			p.Sens, p.Reasons = ClassifyContent(p.Content)
		}
	}
	return p
}

func (r *Rules) allowed(path string) bool {
	if path == "" {
		return false
	}
	for _, pat := range r.Allow {
		if glob.Match(pat, path) {
			return true
		}
	}
	return false
}

// Guard decides whether parts may reach sink.
//
// It fails closed in both directions that matter: a payload declaring no
// provenance is refused, and so is a part whose Source is empty. Those two
// rules are what make the gate impossible to bypass by adding a code path
// that forgets it, since the omission denies instead of leaking.
//
// strict governs only the case where content classifies Secret and no local
// head was routable. With it on (the default) that is a refusal; with it off
// the payload goes out and the caller is responsible for saying so loudly.
func Guard(parts []Part, sink Sink, strict bool) Verdict {
	if len(parts) == 0 {
		return Verdict{Decision: Deny, Reason: "no provenance declared for this payload"}
	}

	worst, reasons, origins := Public, []string(nil), []string(nil)
	for i, p := range parts {
		if p.Source == "" {
			return Verdict{
				Decision: Deny,
				Reason:   fmt.Sprintf("part %d declares no source", i),
			}
		}
		if p.Sens > worst {
			worst = p.Sens
		}
		if p.Sens == Secret {
			reasons = append(reasons, p.Reasons...)
			origins = append(origins, describe(p))
		}
	}

	if worst < Secret {
		return Verdict{Decision: Allow, Sens: worst}
	}
	switch sink.Kind {
	case SinkLocal, SinkDisk:
		// A local head and the on-disk payload store both stay on the machine.
		return Verdict{Decision: Allow, Sens: worst, Origins: origins}
	case SinkTelemetry:
		return Verdict{
			Decision: Deny, Sens: worst, Origins: origins,
			Reason: "secret content cannot be exported as telemetry (" + strings.Join(reasons, ", ") + ")",
		}
	default:
		if !strict {
			return Verdict{
				Decision: Allow, Sens: worst, Origins: origins,
				Reason: "egress.strict is off, secret content is leaving the machine (" +
					strings.Join(reasons, ", ") + ")",
			}
		}
		return Verdict{
			Decision: Deny, Sens: worst, Origins: origins,
			Reason: fmt.Sprintf("secret content cannot reach %s, no local head was routable (%s)",
				sinkName(sink), strings.Join(reasons, ", ")),
		}
	}
}

// HasSecret reports whether any part classifies Secret, the question dispatch
// asks before head selection so it can prefer a local head rather than
// discovering the problem at the gate.
func HasSecret(parts []Part) bool {
	for _, p := range parts {
		if p.Sens == Secret {
			return true
		}
	}
	return false
}

// Origins names the secret-classified parts, for the notice shown when a run
// is rerouted to a local head.
func Origins(parts []Part) []string {
	var out []string
	for _, p := range parts {
		if p.Sens == Secret {
			out = append(out, describe(p))
		}
	}
	return out
}

func describe(p Part) string {
	if p.Origin != "" {
		return p.Origin
	}
	return string(p.Source)
}

func sinkName(s Sink) string {
	if s.Head != "" {
		return s.Head
	}
	if s.Host != "" {
		return s.Host
	}
	return string(s.Kind)
}
