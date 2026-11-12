// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// --local keeps a hosted decision source out of the router, as it does for chat.
func TestNewDecisionRouter_LocalDropsHostedSources(t *testing.T) {
	testutil.NewSandbox(t)
	t.Setenv("TYPESAFE_API_KEY", "ts-test")

	all, err := newDecisionRouter(context.Background(), 0.9, false, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.router.Heads) != 1 || all.router.Heads[0].ID != "decision/typesafe" {
		t.Errorf("heads = %+v, want decision/typesafe", all.router.Heads)
	}
	local, err := newDecisionRouter(context.Background(), 0.9, true, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.router.Heads) != 0 {
		t.Errorf("heads under --local = %+v, want none", local.router.Heads)
	}
}

func allowAll(string) bool { return true }

// The serve allowlist binds /v1/decisions too: a source it does not name is never asked.
func TestNewDecisionRouter_TheAllowlistDropsUnlistedSources(t *testing.T) {
	testutil.NewSandbox(t)
	t.Setenv("TYPESAFE_API_KEY", "ts-test")

	r, err := newDecisionRouter(context.Background(), 0.9, false, func(id string) bool { return id == "ollama/a" })
	if err != nil {
		t.Fatal(err)
	}
	if len(r.router.Heads) != 0 {
		t.Errorf("heads with an allowlist naming none of them = %+v, want none", r.router.Heads)
	}
	if r.unlisted != 1 {
		t.Errorf("unlisted = %d, want 1, or the banner cannot say /v1/decisions lost its source", r.unlisted)
	}
}

// The operator is told at startup that --heads left /v1/decisions with nothing to ask.
func TestPrintServeBanner_WarnsWhenTheAllowlistDropsDecisionSources(t *testing.T) {
	var warned, quiet bytes.Buffer
	printServeBanner(&warned, "127.0.0.1:8787", "STANDARD", false, false, nil, 2)
	if !strings.Contains(warned.String(), "--heads names no decision source, so /v1/decisions has none (2 dropped)") {
		t.Errorf("the banner does not warn that /v1/decisions has no sources:\n%s", warned.String())
	}
	printServeBanner(&quiet, "127.0.0.1:8787", "STANDARD", false, false, nil, 0)
	if strings.Contains(quiet.String(), "decisions") {
		t.Errorf("a server with no allowlist warned about decisions:\n%s", quiet.String())
	}
}

func TestNewDecisionRouter_RefusesAConfidenceOutsideTheOpenInterval(t *testing.T) {
	testutil.NewSandbox(t)
	for _, c := range []float64{0, 1, 1.5} {
		if _, err := newDecisionRouter(context.Background(), c, false, allowAll); err == nil {
			t.Errorf("--confidence %v was accepted", c)
		}
	}
}
