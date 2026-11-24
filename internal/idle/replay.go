// SPDX-License-Identifier: MIT

package idle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/ankit373/hydra/internal/entropy"
)

// Arms compared by Replay.
const (
	ArmNone      = "none"
	ArmMask      = "masking"
	ArmChecked   = "pack, checked"
	ArmUnchecked = "pack, unchecked"
)

var armOrder = []string{ArmNone, ArmMask, ArmChecked, ArmUnchecked}

// ReplayRounds is how many times a conversation is compacted; the arms are
// scored after rounds 1, 3 and 5.
const ReplayRounds = 5

var scoredRounds = []int{1, 3, 5}

// Needle is a fact or rule planted in turn At that the conversation asks for
// later: the question is put to each arm's context, and the answer must hold Expect.
type Needle struct {
	At       int    `json:"at"`
	Kind     string `json:"kind"` // "rule" or "fact"
	Question string `json:"question"`
	Expect   string `json:"expect"`
}

// held reports whether reply contains Expect, or any of its alternatives
// when Expect lists several with "|". Case is ignored, and so is any reasoning
// block, so a model that thinks aloud is not credited for what it only considered.
func (n Needle) held(reply string) bool {
	got := normalise(reply)
	return slices.ContainsFunc(strings.Split(n.Expect, "|"), func(alt string) bool { return strings.Contains(got, normalise(alt)) })
}

// Fixture is one recorded conversation with the needles planted in it.
type Fixture struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"` // support, research, writing or agent loop
	License string   `json:"license"`
	Turns   []Turn   `json:"turns"`
	Needles []Needle `json:"needles"`
}

// LoadFixtures reads every *.json in dir, in name order. Turns get their ref and place.
func LoadFixtures(dir string) ([]Fixture, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []Fixture
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var f Fixture
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("idle: %s: %w", p, err)
		}
		if f.Name == "" || f.Kind == "" || f.License == "" || len(f.Turns) < ReplayRounds || len(f.Needles) == 0 {
			return nil, fmt.Errorf("idle: %s needs a name, kind, license, %d turns or more and a needle", p, ReplayRounds)
		}
		for i := range f.Turns {
			f.Turns[i].Seq, f.Turns[i].Ref = i, fmt.Sprintf("t%d", i)
		}
		for _, n := range f.Needles {
			if n.At < 0 || n.At >= len(f.Turns) || n.Question == "" || n.Expect == "" {
				return nil, fmt.Errorf("idle: %s has a needle outside its turns or without a question", p)
			}
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("idle: no fixtures (*.json) in %s", dir)
	}
	return out, nil
}

// Row is one fixture, one scored round and one arm.
type Row struct {
	Fixture, Kind string
	Round         int
	Arm           string
	Tokens        int
	Probes        int // probe questions asked of the pack; 0 for arms that have no pack
	ProbeAgree    int
	Passed        bool // the pack passed its checks this round (pack arms only)
	FellBack      bool // the checked arm sent the full turns because the pack failed
	Recalled      int
	Needles       int
}

// ReplayConfig sets what a replay compares.
type ReplayConfig struct {
	KeepLast int     // turns masking leaves whole at the end
	MinAgree float64 // probe agreement a pack needs, 0 for the default
}

// Replay compacts each fixture ReplayRounds times, on head, and scores every
// arm after rounds 1, 3 and 5 on the needles planted so far. One chain of packs
// is built, each on the one before; the checked arm uses a round's pack only
// when it passes, and sends the full turns otherwise, as a swap would.
func Replay(ctx context.Context, head Head, fixtures []Fixture, cfg ReplayConfig) ([]Row, error) {
	if cfg.KeepLast <= 0 {
		cfg.KeepLast = 6
	}
	var rows []Row
	for _, fx := range fixtures {
		var prev *Pack
		for round := 1; round <= ReplayRounds; round++ {
			end := len(fx.Turns) * round / ReplayRounds
			cur := Turns{Turns: fx.Turns[:end]}
			pack, err := Build(ctx, head, cur, prev, false)
			if err != nil {
				return nil, fmt.Errorf("idle: replay %s round %d: %w", fx.Name, round, err)
			}
			v := Verify(pack, cur, func(string) bool { return true })
			if v.Pass {
				if v, err = RunProbes(ctx, head, pack, cur, v, cfg.MinAgree); err != nil {
					return nil, fmt.Errorf("idle: replay %s round %d: %w", fx.Name, round, err)
				}
			}
			prev = &pack
			if !slices.Contains(scoredRounds, round) {
				continue
			}
			arms := map[string]string{
				ArmNone:      renderTurns(cur.Turns),
				ArmMask:      renderTurns(Mask(cur, cfg.KeepLast).Turns),
				ArmUnchecked: packContext(pack),
			}
			arms[ArmChecked] = arms[ArmUnchecked]
			if !v.Pass {
				arms[ArmChecked] = arms[ArmNone]
			}
			for _, arm := range armOrder {
				row := Row{Fixture: fx.Name, Kind: fx.Kind, Round: round, Arm: arm, Tokens: entropy.TokenEstimate(arms[arm])}
				if arm == ArmChecked || arm == ArmUnchecked {
					row.Probes, row.ProbeAgree, row.Passed = v.Probes, v.ProbeAgree, v.Pass
					row.FellBack = arm == ArmChecked && !v.Pass
				}
				if row.Recalled, row.Needles, err = recall(ctx, head, fx.Needles, end, arms[arm]); err != nil {
					return nil, fmt.Errorf("idle: replay %s round %d: %w", fx.Name, round, err)
				}
				rows = append(rows, row)
			}
		}
	}
	return rows, nil
}

func packContext(p Pack) string {
	rules, state := p.Blocks()
	return RulesHeading + rules + StateHeading + state
}

// recall asks each needle planted before end of the context, and counts the
// answers that hold what it expects.
func recall(ctx context.Context, head Head, needles []Needle, end int, ctxText string) (recalled, asked int, err error) {
	for _, n := range needles {
		if n.At >= end {
			continue
		}
		reply, err := answer(ctx, head, n.Question, ctxText)
		if err != nil {
			return 0, 0, err
		}
		asked++
		if n.held(reply) {
			recalled++
		}
	}
	return recalled, asked, nil
}

var thinking = regexp.MustCompile(`(?s)<think>.*?</think>`)

func normalise(s string) string {
	return strings.ToLower(strings.TrimSpace(thinking.ReplaceAllString(s, "")))
}

type cell struct{ tokens, probes, agree, recalled, needles, n, passed, fell int }

// Tables groups rows by kind of chat: one table per kind, a line per scored
// round and arm, the fixtures of that kind pooled, then whether the checked
// pack met the bar against masking.
func Tables(rows []Row) string {
	kinds := map[string]map[[2]string]*cell{}
	var order []string
	for _, r := range rows {
		if kinds[r.Kind] == nil {
			kinds[r.Kind] = map[[2]string]*cell{}
			order = append(order, r.Kind)
		}
		k := [2]string{fmt.Sprint(r.Round), r.Arm}
		c := kinds[r.Kind][k]
		if c == nil {
			c = &cell{}
			kinds[r.Kind][k] = c
		}
		c.tokens += r.Tokens
		c.probes += r.Probes
		c.agree += r.ProbeAgree
		c.recalled += r.Recalled
		c.needles += r.Needles
		c.n++
		if r.Passed {
			c.passed++
		}
		if r.FellBack {
			c.fell++
		}
	}
	slices.Sort(order)
	var b strings.Builder
	for _, kind := range order {
		fmt.Fprintf(&b, "%s\n", kind)
		w := tabwriter.NewWriter(&b, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "round\tarm\ttokens\tprobes agreed\trecall\tchecks passed")
		for _, round := range scoredRounds {
			for _, arm := range armOrder {
				c := kinds[kind][[2]string{fmt.Sprint(round), arm}]
				if c == nil {
					continue
				}
				fmt.Fprintf(w, "%d\t%s\t%d\t%s\t%s\t%s\n", round, arm, c.tokens/c.n, ratio(c.agree, c.probes),
					ratio(c.recalled, c.needles), passes(arm, c))
			}
		}
		w.Flush()
		fmt.Fprintf(&b, "%s\n\n", verdict(kinds[kind]))
	}
	return b.String()
}

func ratio(a, b int) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", a, b)
}

func passes(arm string, c *cell) string {
	if arm != ArmChecked && arm != ArmUnchecked {
		return "-"
	}
	return fmt.Sprintf("%d/%d", c.passed, c.n)
}

// verdict is the bar: after every scored round the checked pack recalls no
// fewer needles than masking, in fewer tokens.
func verdict(cells map[[2]string]*cell) string {
	for _, round := range scoredRounds {
		pack, mask := cells[[2]string{fmt.Sprint(round), ArmChecked}], cells[[2]string{fmt.Sprint(round), ArmMask}]
		if pack == nil || mask == nil {
			continue
		}
		switch {
		case pack.recalled*mask.needles < mask.recalled*pack.needles:
			return fmt.Sprintf("Bar not met: after round %d masking recalls %s and the checked pack %s. Use masking plus the typed state.",
				round, ratio(mask.recalled, mask.needles), ratio(pack.recalled, pack.needles))
		case pack.tokens >= mask.tokens:
			return fmt.Sprintf("Bar not met: after round %d the checked pack is no smaller than masking (%d and %d tokens). Use masking plus the typed state.",
				round, pack.tokens/pack.n, mask.tokens/mask.n)
		}
	}
	return "Bar met: the checked pack recalled no fewer needles than masking, in fewer tokens, after every scored round."
}
