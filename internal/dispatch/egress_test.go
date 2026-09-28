// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// The end-to-end guarantee: naming a .env as the dispatch resource routes the
// work to a local head instead of the higher-scoring one that leaves the
// machine. The router prefers capability, so a plain routing test would pick
// "remote" here; only the egress classification changes the answer.
func TestDispatch_SecretResourceReroutesToALocalHead(t *testing.T) {
	s := testutil.NewSandbox(t)

	away := echoHead(t, s, "away", 95)
	home := echoHead(t, s, "home", 40)
	home.LocalOnly = true

	res, err := liveDispatcher(away, home).Dispatch(context.Background(),
		"rotate the database password", Options{Resource: "deploy/.env"})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if res.Head.ID != "home" {
		t.Fatalf("answered by %q, want home: a .env resource must not reach a head that leaves the machine", res.Head.ID)
	}
}

// An ordinary source file is not secret, so routing is unchanged and the
// higher-scoring head still wins. Without this the test above would pass just
// as well if the gate rerouted everything.
func TestDispatch_OrdinaryResourceRoutesNormally(t *testing.T) {
	s := testutil.NewSandbox(t)

	away := echoHead(t, s, "away", 95)
	home := echoHead(t, s, "home", 40)
	home.LocalOnly = true

	res, err := liveDispatcher(away, home).Dispatch(context.Background(),
		"tidy this up", Options{Resource: "internal/parse/parse.go"})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if res.Head.ID != "away" {
		t.Fatalf("answered by %q, want away: an ordinary file must not be rerouted", res.Head.ID)
	}
}

// With nothing local routable, strict refuses rather than falling back to a
// head that leaves the machine. This is the case the toggle exists for, and
// the default has to be the one that does not leak.
func TestDispatch_SecretResourceRefusesWhenNothingLocalIsRoutable(t *testing.T) {
	s := testutil.NewSandbox(t)
	away := echoHead(t, s, "away", 95)

	_, err := liveDispatcher(away).Dispatch(context.Background(),
		"rotate the database password", Options{Resource: "deploy/.env"})
	if err == nil {
		t.Fatal("a secret resource was dispatched to a head that leaves the machine")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if !strings.Contains(err.Error(), "egress.strict") {
		t.Errorf("the refusal does not name the way out of it: %v", err)
	}
}

// egress.strict = false is the documented escape hatch, so it has to work.
func TestDispatch_StrictOffLetsSecretContentOut(t *testing.T) {
	s := testutil.NewSandbox(t)
	away := echoHead(t, s, "away", 95)

	d := liveDispatcher(away)
	off := false
	d.cfg.Egress.Strict = &off

	res, err := d.Dispatch(context.Background(), "rotate it", Options{Resource: "deploy/.env"})
	if err != nil {
		t.Fatalf("egress.strict = false still refused: %v", err)
	}
	if res.Head.ID != "away" {
		t.Fatalf("answered by %q, want away", res.Head.ID)
	}
}

// A config file predating the gate has no [egress] section, and that must read
// as strict rather than as an opt-out.
func TestDispatch_AbsentEgressSectionIsStrict(t *testing.T) {
	s := testutil.NewSandbox(t)
	away := echoHead(t, s, "away", 95)

	d := liveDispatcher(away) // cfg is &config.Config{}, no [egress] at all
	if _, err := d.Dispatch(context.Background(), "rotate it", Options{Resource: "deploy/.env"}); err == nil {
		t.Fatal("a config with no [egress] section allowed secret content out")
	}
}
