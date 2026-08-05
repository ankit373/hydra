// SPDX-License-Identifier: MIT

package env

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// Every test runs inside a sandbox that clears all provider credentials, so
// what discovery reports is exactly what the test set — not whatever the
// developer happens to have exported. Without that, this file passes on a
// laptop with no keys and fails on one with them.

func TestDiscover_NoKeysFindsNothing(t *testing.T) {
	testutil.NewSandbox(t)

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 0 {
		t.Errorf("discovered %d heads with no credentials set: %+v", len(heads), heads)
	}
}

func TestDiscover_OneKeyYieldsOneHead(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "ANTHROPIC_API_KEY", "sk-test")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 {
		t.Fatalf("got %d heads, want 1: %+v", len(heads), heads)
	}
	h := heads[0]
	if h.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", h.Provider, "anthropic")
	}
	if h.ID != "env/anthropic" {
		t.Errorf("ID = %q, want %q — the env/ prefix is what keeps it distinct from the CLI head", h.ID, "env/anthropic")
	}
	if h.Source != "env" {
		t.Errorf("Source = %q, want %q", h.Source, "env")
	}
	if !h.AuthReady {
		t.Error("AuthReady = false, but a key was present")
	}
	if h.LocalOnly {
		t.Error("LocalOnly = true for an API head — --local would route paid work as if it were free")
	}
}

// anyOf: either variable alone is enough. Requiring both would silently hide a
// head from anyone who set only one.
func TestDiscover_AnyOfNeedsOnlyOneVar(t *testing.T) {
	for _, v := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		t.Run(v, func(t *testing.T) {
			s := testutil.NewSandbox(t)
			s.SetKey(t, v, "k")

			heads, err := (&Provider{}).Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(heads) != 1 || heads[0].Provider != "google" {
				t.Fatalf("with only %s set, got %+v; want exactly one google head", v, heads)
			}
		})
	}
}

// The AND case: a half-configured provider must not be advertised. A head with
// an access key but no secret is discovered, dispatched to, and fails at the
// point of use — the failure #248 was about, one layer earlier.
func TestDiscover_AndKeysNeedAllVars(t *testing.T) {
	cases := []struct {
		name string
		set  map[string]string
		want int
	}{
		{"aws id only", map[string]string{"AWS_ACCESS_KEY_ID": "id"}, 0},
		{"aws secret only", map[string]string{"AWS_SECRET_ACCESS_KEY": "sec"}, 0},
		{"aws both", map[string]string{"AWS_ACCESS_KEY_ID": "id", "AWS_SECRET_ACCESS_KEY": "sec"}, 1},
		{"azure key only", map[string]string{"AZURE_OPENAI_API_KEY": "k"}, 0},
		{"azure both", map[string]string{"AZURE_OPENAI_API_KEY": "k", "AZURE_OPENAI_ENDPOINT": "https://e"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewSandbox(t)
			for k, v := range tc.set {
				s.SetKey(t, k, v)
			}
			heads, err := (&Provider{}).Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(heads) != tc.want {
				t.Errorf("got %d heads, want %d: %+v", len(heads), tc.want, heads)
			}
		})
	}
}

// An empty string is not a credential. Exporting KEY= to *disable* a provider is
// ordinary shell practice, and treating it as set advertises a head that cannot
// authenticate.
func TestDiscover_EmptyValueIsNotACredential(t *testing.T) {
	s := testutil.NewSandbox(t)
	s.SetKey(t, "OPENAI_API_KEY", "")

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 0 {
		t.Errorf("an empty OPENAI_API_KEY produced %+v", heads)
	}
}

func TestKnownKeys_EntriesAreWellFormed(t *testing.T) {
	seenProvider := map[string]bool{}
	for _, k := range knownKeys {
		if k.providerID == "" {
			t.Errorf("entry with vars %v has no providerID", k.envVars)
		}
		if len(k.envVars) == 0 {
			t.Errorf("provider %q has no env vars — it can never be detected", k.providerID)
		}
		if seenProvider[k.providerID] {
			t.Errorf("providerID %q listed twice — it would yield two heads with the same ID", k.providerID)
		}
		seenProvider[k.providerID] = true
		if len(k.envVars) > 1 && !k.anyOf {
			continue // AND is deliberate for AWS/Azure
		}
		if len(k.envVars) > 1 && k.anyOf {
			continue // OR is deliberate for google
		}
	}
}
