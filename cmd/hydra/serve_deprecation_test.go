// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/serve"
	"github.com/ankit373/hydra/internal/testutil"
)

// On a server started with no flags, `hydra` must build exactly the options
// develop built before #1206: the --enum default, pinned, and no objective.
func TestServeRouter_DefaultHydraBuildsWhatDevelopBuilt(t *testing.T) {
	testutil.NewSandbox(t)
	c := cmdServe()
	if err := c.Flags().Parse(nil); err != nil {
		t.Fatal(err)
	}
	enum, _ := c.Flags().GetString("enum")
	obj, err := serveObjective(c, "")
	if err != nil {
		t.Fatal(err)
	}
	r := serveRouter{defaultEnum: enum, objective: obj, implicit: implicitDefault(c, obj)}

	msgs := []executor.Message{{Role: "user", Content: "hi"}}
	got, ev, err := r.options(serve.Request{Messages: msgs, MaxTokens: 64, RequestID: "run-x"})
	if err != nil {
		t.Fatal(err)
	}
	want := dispatch.Options{
		TierHint: dispatch.EnumToTier("STANDARD"), Enum: "STANDARD",
		MaxTokens: 64, Messages: msgs, RunID: "run-x",
	}
	if !reflect.DeepEqual(got, want) || ev != nil {
		t.Errorf("default `hydra` built %+v evidence %+v, want develop's %+v and no evidence", got, ev, want)
	}
}

// The warning is for a request that relies on the default v2.0.0 changes, and
// only that: any explicit choice, the client's or the operator's, silences it.
func TestServeRouter_DeprecatedOnlyOnTheImplicitDefault(t *testing.T) {
	implicit := serveRouter{defaultEnum: "STANDARD", implicit: true}
	for _, tc := range []struct {
		name string
		r    serveRouter
		req  serve.Request
		want bool
	}{
		{"hydra on a default server", implicit, serve.Request{}, true},
		{"header objective", implicit, serve.Request{Objective: rank.Quality}, false},
		{"hydra/standard", implicit, serve.Request{Route: serve.Route{Enum: "STANDARD"}}, false},
		{"pinned tier", implicit, serve.Request{Route: serve.Route{Tier: "4"}}, false},
		{"pinned head", implicit, serve.Request{Route: serve.Route{Head: "h1"}}, false},
		{"--enum passed", serveRouter{defaultEnum: "STANDARD"}, serve.Request{}, false},
		{"--objective passed", serveRouter{defaultEnum: "STANDARD", objective: rank.Cost}, serve.Request{}, false},
	} {
		if got := tc.r.Deprecated(tc.req); got != tc.want {
			t.Errorf("%s: Deprecated = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// implicit is read off the flags: passing either one is the operator's choice.
func TestImplicitDefault_OnlyWithNeitherFlag(t *testing.T) {
	for _, tc := range []struct {
		flags []string
		want  bool
	}{
		{nil, true},
		{[]string{"--enum", "STANDARD"}, false},
		{[]string{"--objective", "quality"}, false},
		{[]string{"--task-class", "a.b"}, true},
	} {
		c := cmdServe()
		if err := c.Flags().Parse(tc.flags); err != nil {
			t.Fatal(err)
		}
		obj, _ := c.Flags().GetString("objective")
		o, err := serveObjective(c, obj)
		if err != nil {
			t.Fatal(err)
		}
		if got := implicitDefault(c, o); got != tc.want {
			t.Errorf("%q: implicit = %v, want %v", tc.flags, got, tc.want)
		}
	}
}

// End to end through the adapter: the default route answers as before with no
// objective claimed, and the stderr line is printed once per server, not per request.
func TestServeRouter_WarnsOncePerServerOnTheImplicitDefault(t *testing.T) {
	dispatchable(t, "the head's answer")
	ctx := context.Background()
	d, err := dispatch.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var buf bytes.Buffer
	r := serveRouter{d: d, defaultEnum: "MODERATE", implicit: true, notice: &deprecationNotice{w: &buf}}
	ask := func(req serve.Request) serve.Answer {
		t.Helper()
		req.RequestID = runid.New()
		req.Messages = []executor.Message{{Role: "user", Content: "hi"}}
		ans, err := r.Chat(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return ans
	}

	if ans := ask(serve.Request{Objective: rank.Cost}); buf.Len() != 0 {
		t.Errorf("an explicit objective printed %q", buf.String())
	} else if ans.Evidence == nil || ans.Evidence.Objective != "cost" {
		t.Errorf("opt-in evidence %+v, want objective cost", ans.Evidence)
	}
	for range 2 {
		if ans := ask(serve.Request{}); ans.Evidence != nil {
			t.Errorf("the default route claimed evidence %+v", ans.Evidence)
		}
	}
	if n := strings.Count(buf.String(), implicitDefaultWarning); n != 1 {
		t.Errorf("warning printed %d times over two default requests, want once:\n%s", n, buf.String())
	}
}

func TestNewDeprecationNotice_SilencedByEnv(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("HYDRA_NO_DEPRECATION_WARNINGS", "1")
	newDeprecationNotice(&buf).warn()
	if buf.Len() != 0 {
		t.Errorf("HYDRA_NO_DEPRECATION_WARNINGS=1 still printed %q", buf.String())
	}
	t.Setenv("HYDRA_NO_DEPRECATION_WARNINGS", "")
	newDeprecationNotice(&buf).warn()
	if !strings.Contains(buf.String(), "v2.0.0") || !strings.Contains(buf.String(), "--objective quality") {
		t.Errorf("warning reads %q, want the version and how to opt in", buf.String())
	}
}
