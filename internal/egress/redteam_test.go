// SPDX-License-Identifier: MIT

package egress

import (
	"strings"
	"testing"
)

// These are attacks, not unit tests. Each one is a way content could reach a
// head that leaves the machine, and each must be refused. The OWASP coverage
// score in internal/security is computed from whether they pass, so a
// regression here shows up as a lower reported posture rather than as a green
// build with a quiet hole in it.

func rules(t *testing.T) *Rules {
	t.Helper()
	r, err := LoadRules("")
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	return r
}

func remote() Sink { return Sink{Kind: SinkRemote, Head: "openai/gpt-5"} }

// The headline guarantee: a config file cannot reach a head that leaves the
// machine, whatever its contents look like.
func TestRedTeam_LocalConfigCannotReachARemoteHead(t *testing.T) {
	r := rules(t)
	for _, path := range []string{
		".env",
		"app/.env",
		"/Users/x/proj/.env.production",
		"/Users/x/.aws/credentials",
		"/Users/x/.ssh/id_ed25519",
		"/etc/ssl/private/server.key",
		"config/secrets/db.yml",
		"/Users/x/.kube/config",
		"infra/terraform.tfstate",
		"/Users/x/.git-credentials",
		"/Users/x/.hydra/cost.jsonl",
	} {
		part := r.ClassifyPart(Part{Source: SourceFile, Origin: path})
		if part.Sens != Secret {
			t.Errorf("%s classified %s, want secret", path, part.Sens)
			continue
		}
		if v := Guard([]Part{part}, remote(), true); v.Decision != Deny {
			t.Errorf("%s was allowed to reach a remote head", path)
		}
	}
}

// The same file is fine on a head that never leaves the machine. A gate that
// blocks local work too is one people turn off.
func TestRedTeam_SecretIsAllowedToStayLocal(t *testing.T) {
	r := rules(t)
	part := r.ClassifyPart(Part{Source: SourceFile, Origin: "/Users/x/.aws/credentials"})

	if v := Guard([]Part{part}, Sink{Kind: SinkLocal, Head: "ollama/qwen3"}, true); v.Decision != Allow {
		t.Fatalf("secret content refused to a local head: %s", v.Reason)
	}
}

// A committed template matches a secret pattern and carries nothing. Blocking
// it only teaches people to turn the gate off.
func TestRedTeam_PublishedTemplatesAreNotSecrets(t *testing.T) {
	r := rules(t)
	for _, path := range []string{
		".env.example",
		"app/.env.sample",
		"deploy/.env.template",
		"config/secrets/README.md",
	} {
		if part := r.ClassifyPart(Part{Source: SourceFile, Origin: path}); part.Sens != Public {
			t.Errorf("%s classified %s, want public", path, part.Sens)
		}
	}
}

// One secret part among many innocuous ones still denies: the payload's
// classification is the maximum over its parts, not the average or the first.
func TestRedTeam_OneSecretPartPoisonsTheWholePayload(t *testing.T) {
	r := rules(t)
	parts := []Part{
		{Content: "refactor the parser", Source: SourceUser},
		r.ClassifyPart(Part{Source: SourceFile, Origin: "internal/parse/parse.go"}),
		r.ClassifyPart(Part{Source: SourceFile, Origin: "deploy/.env"}),
		{Content: "keep the tests passing", Source: SourceUser},
	}

	v := Guard(parts, remote(), true)
	if v.Decision != Deny {
		t.Fatal("a payload containing one secret part was allowed out")
	}
	if !strings.Contains(strings.Join(v.Origins, ","), ".env") {
		t.Errorf("the denial does not name the offending file: %v", v.Origins)
	}
}

// A key a model just emitted must not be forwarded to the next one. Head
// output has no path, so this is the one place content detection carries the
// decision.
func TestRedTeam_HeadOutputCarryingASecretIsNotForwarded(t *testing.T) {
	r := rules(t)
	part := r.ClassifyPart(Part{
		Content: "here is the key you asked for: AKIAIOSFODNN7EXAMPLE",
		Source:  SourceHead,
		Origin:  "ollama/qwen3",
	})
	if part.Sens != Secret {
		t.Fatalf("head output carrying an AWS key classified %s", part.Sens)
	}
	if v := Guard([]Part{part}, remote(), true); v.Decision != Deny {
		t.Error("head output carrying an AWS key was forwarded to a remote head")
	}
}

// Fail closed: a payload that declares nothing about where it came from is
// refused. This is what makes the gate impossible to bypass by adding a code
// path that forgets it, since the omission denies instead of leaking.
func TestRedTeam_UndeclaredProvenanceIsRefused(t *testing.T) {
	if v := Guard(nil, remote(), true); v.Decision != Deny {
		t.Error("a payload with no provenance was allowed out")
	}
	if v := Guard([]Part{}, remote(), true); v.Decision != Deny {
		t.Error("an empty provenance slice was allowed out")
	}
	// A part that exists but names no source is the same failure one layer in.
	undeclared := []Part{{Content: "whatever this is", Origin: "somewhere"}}
	if v := Guard(undeclared, remote(), true); v.Decision != Deny {
		t.Error("a part with no declared source was allowed out")
	}
}

// Telemetry leaves the machine too. It is a separate sink because it is a
// separate code path, and a reader who only hardened dispatch would miss it.
func TestRedTeam_SecretIsNeverExportedAsTelemetry(t *testing.T) {
	r := rules(t)
	part := r.ClassifyPart(Part{Source: SourceFile, Origin: "deploy/.env"})

	v := Guard([]Part{part}, Sink{Kind: SinkTelemetry, Host: "localhost:4318"}, true)
	if v.Decision != Deny {
		t.Error("secret content was allowed into a telemetry export")
	}
	// Strict is about the no-local-head fallback, not about telemetry: turning
	// it off must not open this.
	if v := Guard([]Part{part}, Sink{Kind: SinkTelemetry}, false); v.Decision != Deny {
		t.Error("egress.strict = false opened the telemetry path")
	}
}

// Turning strict off is the documented escape hatch, and it has to say so out
// loud rather than allowing silently.
func TestRedTeam_StrictOffAllowsButExplainsItself(t *testing.T) {
	r := rules(t)
	part := r.ClassifyPart(Part{Source: SourceFile, Origin: "deploy/.env"})

	v := Guard([]Part{part}, remote(), false)
	if v.Decision != Allow {
		t.Fatal("egress.strict = false still refused")
	}
	if !strings.Contains(v.Reason, "egress.strict") {
		t.Errorf("the allow does not explain itself: %q", v.Reason)
	}
	if len(v.Origins) == 0 {
		t.Error("the allow does not name what is leaving")
	}
}

// A typed prompt stays governed by the configurable pii policy. Re-deciding it
// here would override a setting the operator chose and scan the string twice.
func TestRedTeam_UserPromptIsNotReclassifiedHere(t *testing.T) {
	r := rules(t)
	part := r.ClassifyPart(Part{
		Content: "my key is AKIAIOSFODNN7EXAMPLE",
		Source:  SourceUser,
	})
	if part.Sens != Public {
		t.Errorf("a user prompt was reclassified to %s, which overrides [policies.pii]", part.Sens)
	}
}
