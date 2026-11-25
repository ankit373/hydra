// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/payload"
)

func TestContextPacks_Lists(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	if out, _, err := run(t, "context", "packs"); err != nil || !strings.Contains(out, "no staged packs") {
		t.Fatalf("empty: %q, %v", out, err)
	}
	msgs := swapMessages()
	stagePack(t, msgs, passed)
	if _, c := coldCompactor(swapColdOnly).apply(msgs, swapConv, ""); c == nil {
		t.Fatal("the pack was not used")
	}
	for _, args := range [][]string{{"context", "packs"}, {"context", "packs", swapConv}} {
		out, _, err := run(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{swapConv + "\n", "conv-a/1", "turns 0-3", "probes 9/10", "used 1 times"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: output missing %q:\n%s", args, want, out)
			}
		}
	}
	if _, _, err := run(t, "context", "packs", "nope"); err == nil {
		t.Fatal("an unknown conversation listed fine")
	}
	out, _, err := run(t, "context", "show-pack", "conv-a/1")
	if err != nil || !strings.Contains(out, "Never promise a refund over $200") || !strings.Contains(out, "refund one charge, because both lines are the same invoice") {
		t.Fatalf("show-pack: %q, %v; want the rules and the decision as the model sees them", out, err)
	}
	for _, id := range []string{"conv-a/9", "conv-a", "../x/1"} {
		if _, _, err := run(t, "context", "show-pack", id); err == nil {
			t.Errorf("show-pack %q read fine", id)
		}
	}
}

func TestContextRestore_NextRequestIsOriginal(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	msgs := swapMessages()
	stagePack(t, msgs, passed)
	if _, c := coldCompactor(swapColdOnly).apply(msgs, swapConv, ""); c == nil {
		t.Fatal("a swap should happen before the restore")
	}
	out, _, err := run(t, "context", "restore", swapConv)
	if err != nil || !strings.Contains(out, "next request goes out uncompacted") {
		t.Fatalf("restore: %q, %v", out, err)
	}
	got, c := coldCompactor(swapColdOnly).apply(msgs, swapConv, "")
	if c != nil || len(got) != len(msgs) || got[1].Content != msgs[1].Content {
		t.Fatalf("after restore the request was changed: %v", c)
	}
	if out, _, _ := run(t, "context", "packs"); !strings.Contains(out, "restored") {
		t.Fatalf("packs does not say the conversation was restored:\n%s", out)
	}
	if _, _, err := run(t, "context", "restore", "nope"); err == nil {
		t.Fatal("restoring an unknown conversation worked")
	}
}

func TestContextRestore_KeepsSharedPins(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	stagePack(t, swapMessages(), passed)
	store, err := payload.Open(payload.Dir())
	if err != nil {
		t.Fatal(err)
	}
	shared, err := store.Put("You are the billing assistant for Acme.", 1)
	if err != nil {
		t.Fatal(err)
	}
	only, err := store.Put("Only conversation A has this text.", 1)
	if err != nil {
		t.Fatal(err)
	}
	for owner, refs := range map[string][]string{"idle:" + swapConv: {shared, only}, "idle:conv-b": {shared}} {
		if err := store.Pin(owner, refs); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := run(t, "context", "restore", swapConv); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(payload.Dir(), "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string][]string
	if err := json.Unmarshal(raw, &pins); err != nil {
		t.Fatal(err)
	}
	if _, still := pins["idle:"+swapConv]; still || len(pins["idle:conv-b"]) == 0 {
		t.Fatalf("pins %v, want conv-a's dropped and conv-b's kept", pins)
	}
	if got := pins["idle:conv-b"]; !strings.Contains(strings.Join(got, ","), shared) {
		t.Fatalf("conv-b's pins %v lost the shared text", got)
	}
}
