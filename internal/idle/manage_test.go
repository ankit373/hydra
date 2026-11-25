// SPDX-License-Identifier: MIT

package idle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingUnpin struct {
	owner string
	err   error
}

func (u *recordingUnpin) Unpin(owner string) error { u.owner = owner; return u.err }

// stagedRig stages one pack for conv1 and returns the rig.
func stagedRig(t *testing.T) *tickRig {
	t.Helper()
	r := newRig(t, newTickHead(goodState))
	if res := tick(t, r); res.Outcome != OutcomeStaged {
		t.Fatalf("staging: %+v", res)
	}
	return r
}

func TestPacks_ListsWhatWasStagedAndUsed(t *testing.T) {
	r := stagedRig(t)
	at := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := MarkUsed(r.env.Dir, "conv1", 1, at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	packs, err := Packs(r.env.Dir, "conv1")
	if err != nil || len(packs) != 1 {
		t.Fatalf("packs %+v, %v", packs, err)
	}
	p := packs[0]
	if p.ID != "conv1/1" || p.Number != 1 || p.Used != 2 || !p.LastUsed.Equal(at.Add(time.Minute)) || !p.Verdict.Pass || p.Covers.To != 4 {
		t.Fatalf("pack %+v", p)
	}
	if got, err := Packs(r.env.Dir, "never"); err != nil || got != nil {
		t.Fatalf("a conversation with no directory: %v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(r.env.Dir, "conv1", "pack-2.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Packs(r.env.Dir, "conv1"); err == nil {
		t.Fatal("a corrupt pack listed fine")
	}
	if err := os.Remove(filepath.Join(r.env.Dir, "conv1", "pack-2.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(r.env.Dir, "conv1", "pack-3.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Packs(r.env.Dir, "conv1"); err == nil {
		t.Fatal("a pack that cannot be read listed fine")
	}
}

func TestFindPackAndResolve(t *testing.T) {
	r := stagedRig(t)
	if st, err := FindPack(r.env.Dir, "conv1/1"); err != nil || st.Pack.State.Goal == "" {
		t.Fatalf("find: %v, %v", st, err)
	}
	for _, id := range []string{"conv1/9", "conv1", "conv1/x", "/1", "../conv1/1", "a/b/1", ".."} {
		if _, err := FindPack(r.env.Dir, id); !errors.Is(err, ErrNoPack) {
			t.Errorf("FindPack(%q) = %v, want ErrNoPack", id, err)
		}
	}
	if err := os.WriteFile(filepath.Join(r.env.Dir, "conv1", "pack-4.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FindPack(r.env.Dir, "conv1/4"); err == nil || errors.Is(err, ErrNoPack) {
		t.Fatalf("a corrupt pack: %v", err)
	}
	if err := os.Mkdir(filepath.Join(r.env.Dir, "conv1", "pack-5.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := FindPack(r.env.Dir, "conv1/5"); err == nil || errors.Is(err, ErrNoPack) {
		t.Fatalf("an unreadable pack: %v", err)
	}
	if key, ok := Resolve(r.env.Dir, "conv1"); !ok || key != "conv1" {
		t.Fatalf("resolve by key: %q %v", key, ok)
	}
	if _, ok := Resolve(r.env.Dir, "nope"); ok {
		t.Fatal("an unknown conversation resolved")
	}
}

func TestRestore_StopsSwapsAndBuilds(t *testing.T) {
	r := stagedRig(t)
	u := &recordingUnpin{}
	if Restored(r.env.Dir, "conv1") {
		t.Fatal("restored before it was asked")
	}
	if err := Restore(r.env.Dir, "conv1", u); err != nil || u.owner != PinOwner("conv1") || !Restored(r.env.Dir, "conv1") {
		t.Fatalf("restore: owner %q, err %v", u.owner, err)
	}
	// More text arrives; a restored conversation is still left alone.
	first, _ := r.env.Transcript("conv1")
	grown := Turns{Turns: append(append([]Turn{}, first.Turns...),
		Turn{Role: "user", Text: strings.Repeat("Please also check the ledger again. ", 60), Ref: "u2", Seq: 4})}
	r.env.Transcript = func(string) (Turns, error) { return grown, nil }
	if res := tick(t, r); res.Outcome != OutcomeNone {
		t.Fatalf("tick %+v, want a restored conversation not rebuilt", res)
	}
	if err := Restore(r.env.Dir, "never", u); err == nil {
		t.Fatal("restoring a conversation with nothing staged worked")
	}
	u.err = errors.New("store locked")
	if err := Restore(r.env.Dir, "conv1", u); err == nil {
		t.Fatal("an unpin failure was lost")
	}
	if err := os.Chmod(filepath.Join(r.env.Dir, "conv1"), 0o500); err == nil && os.Geteuid() != 0 {
		defer os.Chmod(filepath.Join(r.env.Dir, "conv1"), 0o700)
		if err := Restore(r.env.Dir, "conv1", u); err == nil {
			t.Fatal("a marker that could not be written was ignored")
		}
	}
}
