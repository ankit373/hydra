// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/egress"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// leakyHead answers with content the caller would not want written to disk.
func leakyHead(t *testing.T, s *testutil.Sandbox, id string) provider.Head {
	t.Helper()
	// A shape policy.DetectPII recognises, split so this file is not itself a
	// secret-looking blob to a scanner reading the repo.
	key := "AKIA" + "IOSFODNN7EXAMPLE"
	body := "#!/bin/sh\necho 'here you go: " + key + "'\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho here you go: " + key + "\r\n"
	}
	return provider.Head{
		ID: id, Name: id, Provider: "openai", Source: "cli",
		CapScore: 90, AuthReady: true,
		Executable: s.FakeBinary(t, "fake-head-"+id, body),
	}
}

// The asymmetry #740 is about: Hydra already fences a head's answer when the
// next *model* will read it, and hands it to the orchestrator, which has write
// access, as a bare trusted string. The answer now comes back classified.
func TestDispatch_ResponseComesBackClassified(t *testing.T) {
	s := testutil.NewSandbox(t)
	h := leakyHead(t, s, "chatty")

	res, err := liveDispatcher(h).Dispatch(context.Background(), "give me the key", Options{})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	if res.OutputProvenance.Source != egress.SourceHead {
		t.Errorf("Source = %q, want %q: the answer's origin is the head that wrote it",
			res.OutputProvenance.Source, egress.SourceHead)
	}
	if res.OutputProvenance.Origin != "chatty" {
		t.Errorf("Origin = %q, want the head id", res.OutputProvenance.Origin)
	}
	if res.OutputProvenance.Sens != egress.Secret {
		t.Fatalf("Sens = %v, want secret: the response carries an access key id",
			res.OutputProvenance.Sens)
	}
	if !strings.Contains(strings.Join(res.OutputProvenance.Reasons, ","), "aws access key id") {
		t.Errorf("Reasons = %v, want the detector that fired so a warning can name it",
			res.OutputProvenance.Reasons)
	}
	// Classified, never altered: the caller asked for the answer.
	if !strings.Contains(res.Output, "AKIA") {
		t.Errorf("the output was modified rather than classified: %q", res.Output)
	}
}

// An ordinary answer must not be flagged, or the warning is noise operators
// learn to scroll past.
func TestDispatch_OrdinaryResponseIsNotFlagged(t *testing.T) {
	s := testutil.NewSandbox(t)
	h := echoHead(t, s, "plain", 90)

	res, err := liveDispatcher(h).Dispatch(context.Background(), "review this", Options{})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if res.OutputProvenance.Sens != egress.Public {
		t.Errorf("Sens = %v with reasons %v, want public for an ordinary answer",
			res.OutputProvenance.Sens, res.OutputProvenance.Reasons)
	}
	if res.OutputProvenance.Source != egress.SourceHead {
		t.Errorf("Source = %q, want it set even when nothing fired: an unclassified "+
			"part is what the egress gate refuses", res.OutputProvenance.Source)
	}
}
