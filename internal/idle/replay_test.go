// SPDX-License-Identifier: MIT

package idle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// replayFixture has a rule in the system turn, a fact in a user turn and a
// fact only an assistant turn states, which masking cuts.
func replayFixture(t *testing.T) []Fixture {
	t.Helper()
	bulk := strings.Repeat("ledger line without digits. ", 60)
	turns := []map[string]string{
		{"role": "system", "text": "Never promise a refund over $200 without a manager."},
		{"role": "user", "text": "I was charged twice on invoice 4417. Only email me.\n" + bulk},
		{"role": "assistant", "text": "The duplicate was charge ch71a09, a gateway retry."},
		{"role": "tool", "text": bulk},
		{"role": "user", "text": "Thanks, any news?"},
		{"role": "assistant", "text": "Not yet, I will email you."},
		{"role": "tool", "text": bulk},
		{"role": "user", "text": "ok"},
		{"role": "assistant", "text": "The refund is pending."},
		{"role": "tool", "text": bulk},
		{"role": "user", "text": "Great."},
		{"role": "assistant", "text": "Anything else?"},
	}
	doc := map[string]any{"name": "support-small", "kind": "support", "license": "MIT, written for this test",
		"turns": turns,
		"needles": []Needle{
			{At: 0, Kind: "rule", Question: "What is the largest refund that may be promised alone?", Expect: "$200"},
			{At: 1, Kind: "fact", Question: "Which invoice was charged twice?", Expect: "4417"},
			{At: 2, Kind: "fact", Question: "Which charge was the duplicate?", Expect: "ch71a09"},
		}}
	raw, _ := json.Marshal(doc)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "support-small.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	fx, err := LoadFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fx
}

// replayHead answers by lookup; factRef is the turn its pack cites for the duplicate.
func replayHead(factRef string) *tickHead {
	h := &tickHead{lookupHead: &lookupHead{keyword: [][2]string{
		{"rules", "Never promise"}, {"refund", "Never promise"}, {"promised", "Never promise"},
		{"invoice", "4417"}, {"duplicate", "ch71a09"},
	}}}
	h.state = `{"goal": "refund the duplicate charge on invoice 4417",
 "facts": [{"text": "the duplicate was charge ch71a09, a gateway retry", "ref": "` + factRef + `"}]}`
	return h
}

func pick(rows []Row, round int, arm string) Row {
	for _, r := range rows {
		if r.Round == round && r.Arm == arm {
			return r
		}
	}
	return Row{}
}

func TestReplay_ArmsOnFixture(t *testing.T) {
	// The pack cites turn 2 for its fact, which round 1 (two turns) does not hold:
	// the checked arm falls back to the full turns there, the unchecked one does not.
	rows, err := Replay(context.Background(), replayHead("t2"), replayFixture(t), ReplayConfig{KeepLast: 2})
	if err != nil || len(rows) != 12 {
		t.Fatalf("%d rows, %v; want 12 (3 scored rounds, 4 arms)", len(rows), err)
	}
	type want struct{ recalled, needles int }
	for _, c := range []struct {
		round int
		arm   string
		want  want
	}{
		{1, ArmNone, want{2, 2}}, {1, ArmMask, want{2, 2}}, {1, ArmChecked, want{2, 2}}, {1, ArmUnchecked, want{2, 2}},
		{3, ArmNone, want{3, 3}}, {3, ArmMask, want{2, 3}}, {3, ArmChecked, want{3, 3}}, {3, ArmUnchecked, want{3, 3}},
		{5, ArmNone, want{3, 3}}, {5, ArmMask, want{2, 3}}, {5, ArmChecked, want{3, 3}}, {5, ArmUnchecked, want{3, 3}},
	} {
		if r := pick(rows, c.round, c.arm); (want{r.Recalled, r.Needles}) != c.want {
			t.Errorf("round %d %s: recalled %d of %d, want %d of %d", c.round, c.arm, r.Recalled, r.Needles, c.want.recalled, c.want.needles)
		}
	}
	r1 := pick(rows, 1, ArmChecked)
	if r1.Passed || !r1.FellBack || r1.Tokens != pick(rows, 1, ArmNone).Tokens {
		t.Errorf("round 1 checked arm %+v, want a fallback to the full turns", r1)
	}
	if u := pick(rows, 1, ArmUnchecked); u.Tokens >= r1.Tokens {
		t.Errorf("round 1 unchecked pack has %d tokens, want fewer than the %d it avoided", u.Tokens, r1.Tokens)
	}
	for _, round := range []int{3, 5} {
		pack := pick(rows, round, ArmChecked)
		if !pack.Passed || pack.FellBack || pack.Probes == 0 || pack.ProbeAgree != pack.Probes {
			t.Errorf("round %d checked arm %+v, want a pack that passed with every probe agreeing", round, pack)
		}
		if pack.Tokens >= pick(rows, round, ArmMask).Tokens || pick(rows, round, ArmMask).Tokens >= pick(rows, round, ArmNone).Tokens {
			t.Errorf("round %d tokens: pack %d, masking %d, none %d; want each smaller than the next", round, pack.Tokens,
				pick(rows, round, ArmMask).Tokens, pick(rows, round, ArmNone).Tokens)
		}
	}
	out := Tables(rows)
	if !strings.HasPrefix(out, "support\n") || !strings.Contains(out, "Bar not met: after round 1 the checked pack is no smaller than masking") {
		t.Fatalf("tables:\n%s", out)
	}
}

func TestReplay_BarMetWhenEveryRoundPasses(t *testing.T) {
	rows, err := Replay(context.Background(), replayHead("t1"), replayFixture(t), ReplayConfig{KeepLast: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out := Tables(rows); !strings.Contains(out, "Bar met") || !strings.Contains(out, "2/2") {
		t.Fatalf("tables:\n%s", out)
	}
	// Masking recalling more than the pack is a miss, whatever the tokens.
	for i := range rows {
		if rows[i].Arm == ArmChecked && rows[i].Round == 5 {
			rows[i].Recalled = 1
		}
	}
	if out := Tables(rows); !strings.Contains(out, "Bar not met: after round 5 masking recalls 2/3 and the checked pack 1/3") {
		t.Fatalf("tables:\n%s", out)
	}
}

func TestReplay_NeedleRecallCountsRulesAndFacts(t *testing.T) {
	needles := []Needle{
		{At: 0, Kind: "rule", Question: "What is the largest refund that may be promised alone?", Expect: "$200"},
		{At: 1, Kind: "fact", Question: "Which invoice was charged twice?", Expect: "INVOICE 4417"},
		{At: 8, Kind: "fact", Question: "Which charge was the duplicate?", Expect: "ch71a09"},
	}
	context := "system: Never promise a refund over $200 without a manager.\nuser: invoice 4417 was charged twice.\n"
	got, asked, err := recall(t.Context(), replayHead("t1"), needles, 5, context)
	if err != nil || got != 2 || asked != 2 {
		t.Fatalf("recalled %d of %d, %v; want 2 of 2: the third needle is planted after the context ends", got, asked, err)
	}
	// What a model only thinks about is not an answer.
	think := &thinkingHead{replayHead("t1")}
	if got, asked, _ := recall(t.Context(), think, needles[:1], 5, context); got != 0 || asked != 1 {
		t.Fatalf("a rule mentioned only inside <think> counted: %d of %d", got, asked)
	}
	if !(Needle{Expect: "9 per cent|9%"}).held("It went up 9%.") {
		t.Fatal("an alternative after | was not accepted")
	}
}

type thinkingHead struct{ *tickHead }

func (thinkingHead) Ask(context.Context, string) (string, error) {
	return "<think>the limit is $200</think>I do not know.", nil
}

func TestLoadFixtures_RefusesWhatIsNotAFixture(t *testing.T) {
	if _, err := LoadFixtures(t.TempDir()); err == nil {
		t.Fatal("an empty directory loaded")
	}
	write := func(body string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	turns := `[{"role":"user","text":"a"},{"role":"user","text":"b"},{"role":"user","text":"c"},{"role":"user","text":"d"},{"role":"user","text":"e"}]`
	for name, body := range map[string]string{
		"not json":                `{`,
		"no license":              `{"name":"x","kind":"support","turns":` + turns + `,"needles":[{"at":0,"question":"q","expect":"a"}]}`,
		"too few turns":           `{"name":"x","kind":"support","license":"MIT","turns":[{"role":"user","text":"a"}],"needles":[{"at":0,"question":"q","expect":"a"}]}`,
		"needle off the end":      `{"name":"x","kind":"support","license":"MIT","turns":` + turns + `,"needles":[{"at":9,"question":"q","expect":"a"}]}`,
		"needle with no question": `{"name":"x","kind":"support","license":"MIT","turns":` + turns + `,"needles":[{"at":0,"expect":"a"}]}`,
	} {
		if _, err := LoadFixtures(write(body)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if _, err := LoadFixtures(filepath.Join(t.TempDir(), "[")); err == nil {
		t.Fatal("a bad pattern loaded")
	}
}

// The fixtures shipped in bench/idle-replay must load and plant what they ask for.
func TestShippedFixtures_PlantTheirNeedles(t *testing.T) {
	fx, err := LoadFixtures(filepath.Join("..", "..", "bench", "idle-replay"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, f := range fx {
		kinds[f.Kind] = true
		for _, n := range f.Needles {
			if !n.held(f.Turns[n.At].Text) {
				t.Errorf("%s: turn %d does not hold %q", f.Name, n.At, n.Expect)
			}
		}
	}
	if len(kinds) != 4 {
		t.Fatalf("kinds %v, want support, research, writing and agent loop", kinds)
	}
}
