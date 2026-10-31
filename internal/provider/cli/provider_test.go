// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"runtime"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// Discovery's whole job is answering "what is installed on this machine", so it
// is the layer most likely to answer with the *developer's* machine. Every test
// here runs inside a sandbox with an empty PATH, so what it finds is exactly
// what the test planted, on Linux, macOS and Windows alike.

func TestDiscover_EmptyPathFindsNothing(t *testing.T) {
	testutil.NewSandbox(t)

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 0 {
		t.Errorf("discovered %d heads with an empty PATH: %+v", len(heads), heads)
	}
}

// The .exe/.bat resolution difference is the single most likely way discovery
// diverges by platform, and it had no test at all before #269.
func TestDiscover_FindsPlantedBinaryOnEveryOS(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.FakeBinary(t, "claude")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 {
		t.Fatalf("got %d heads, want exactly the planted one: %+v", len(heads), heads)
	}
	h := heads[0]
	if h.ID != "claude" {
		t.Errorf("ID = %q, want %q", h.ID, "claude")
	}
	if h.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", h.Provider, "anthropic")
	}
	if h.Source != "cli" {
		t.Errorf("Source = %q, want %q", h.Source, "cli")
	}
	if h.Executable == "" {
		t.Error("Executable is empty, the head cannot be run")
	}
	if !h.AuthReady {
		t.Error("AuthReady = false; a CLI agent on PATH carries its own auth")
	}
}

// A discovered head must carry whether its capability entry is embedded or
// user-added (Meta["model_source"]), the "managed vs. discovered" split a
// security dashboard reports on, otherwise invisible once discovery discards
// the capabilities.Entry it came from.
func TestDiscover_StampsModelSource(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.FakeBinary(t, "claude")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 {
		t.Fatalf("got %d heads, want 1: %+v", len(heads), heads)
	}
	if got := heads[0].Meta["model_source"]; got != "builtin" {
		t.Errorf("Meta[model_source] = %q, want builtin, claude is in the embedded catalog", got)
	}
}

// LocalOnly drives tier-10 placement in rank.UITier (#248) and the --local
// routing gate. Getting it wrong sends work that must stay local to a paid API.
func TestDiscover_LocalOnlyMatchesTheTable(t *testing.T) {
	for _, c := range knownCLIs {
		t.Run(c.binary, func(t *testing.T) {
			s := testutil.NewSandbox(t)
			s.FakeBinary(t, c.binary)

			heads, err := (&Provider{}).Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(heads) != 1 {
				t.Fatalf("got %d heads, want 1", len(heads))
			}
			if heads[0].LocalOnly != c.local {
				t.Errorf("LocalOnly = %v, want %v, %q is declared local=%v in knownCLIs",
					heads[0].LocalOnly, c.local, c.binary, c.local)
			}
		})
	}
}

// Two agents installed must produce two heads, not one and a silent drop.
func TestDiscover_ReportsEveryInstalledAgent(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.FakeBinary(t, "claude")
	s.FakeBinary(t, "ollama")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 {
		t.Fatalf("got %d heads, want 2: %+v", len(heads), heads)
	}
	seen := map[string]bool{}
	for _, h := range heads {
		seen[h.ID] = true
	}
	for _, want := range []string{"claude", "ollama"} {
		if !seen[want] {
			t.Errorf("%q not discovered; got %v", want, seen)
		}
	}
}

// Every entry must be reachable: a typo'd binary name is a head that can never
// be discovered, and nothing else would ever say so.
func TestKnownCLIs_EntriesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range knownCLIs {
		if c.binary == "" {
			t.Error("entry with an empty binary name")
		}
		if c.providerID == "" {
			t.Errorf("%q has no providerID; pricing and capability lookups key off it", c.binary)
		}
		if seen[c.binary] {
			t.Errorf("%q listed twice, it would be discovered as two identical heads", c.binary)
		}
		seen[c.binary] = true
		if runtime.GOOS == "windows" {
			continue // extension resolution is exec.LookPath's business, tested above
		}
		if c.binary != sanitised(c.binary) {
			t.Errorf("%q is not a bare binary name; exec.LookPath will never find it", c.binary)
		}
	}
}

// sanitised strips anything a PATH lookup would choke on. A binary name with a
// separator, a space or an extension is not something LookPath resolves.
func sanitised(s string) string {
	for _, bad := range []string{"/", `\`, " ", ".exe", ".bat"} {
		for i := 0; i+len(bad) <= len(s); i++ {
			if s[i:i+len(bad)] == bad {
				return ""
			}
		}
	}
	return s
}
