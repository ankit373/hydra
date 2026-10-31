// SPDX-License-Identifier: MIT

package cache

import (
	"strings"

	"github.com/ankit373/hydra/internal/config"
)

// DefaultThreshold is the cosine a near match must reach before the
// content-token gate is even consulted. Conservative on purpose: vLLM SR's own
// configuration uses 0.85 generally and 0.95 for maths, and the pairs measured
// in #911 that must never be served sit at 0.8557 and 0.8598, so anything at
// their default would serve them.
const DefaultThreshold = 0.95

// Enabled reports whether answers are cached at all. Off unless someone chose
// it, like payload capture: a cache returns an answer to a question that was
// never asked of a head, which is not a default anyone should inherit.
func Enabled(cfg *config.Config) bool { return cfg != nil && cfg.CacheAnswers }

// Budget is the configured byte bound, or the built-in one.
func Budget(cfg *config.Config) int64 {
	if cfg == nil || cfg.CacheBudgetMB <= 0 {
		return DefaultBudgetBytes
	}
	return int64(cfg.CacheBudgetMB) << 20
}

// Threshold is the similarity this enum demands. Per enum because the enums
// are not equally forgiving: two nearly identical CORE prompts are a different
// risk from two nearly identical GRUNT ones. An unlisted enum gets the
// configured default, and a configured value outside (0,1] is ignored rather
// than clamped, since a threshold of 0 would serve everything.
func Threshold(cfg *config.Config, enum string) float64 {
	thr := DefaultThreshold
	if cfg == nil {
		return thr
	}
	if usable(cfg.CacheThreshold) {
		thr = cfg.CacheThreshold
	}
	if per, ok := cfg.CacheThresholds[strings.ToUpper(strings.TrimSpace(enum))]; ok && usable(per) {
		thr = per
	}
	return thr
}

func usable(t float64) bool { return t > 0 && t <= 1 }

// Request is what the cache has to know about a dispatch before it may answer
// from an earlier one.
type Request struct {
	// PII and Injection come from policy.Classify, already computed once per
	// dispatch.
	PII       bool
	Injection bool
	// Pinned is a caller naming its head. Someone who asked for a model wants
	// that model's answer, and a stored one from a different head is not it.
	Pinned bool
	// NoCache is a caller that knows its prompt must not be served from
	// history: the swarm judge and the SPRT equivalence judge, both of which
	// are asking about two particular answers rather than about the question
	// those answers are to.
	NoCache bool
}

// Servable reports whether this dispatch may be answered from the cache, and
// says why not when it may not. The reason is returned rather than logged
// because it belongs beside the routing decision.
//
// A confidence target needs no rule here, and deliberately does not have one:
// `hyctl dispatch --confidence` runs the SPRT ensemble, which samples heads
// through internal/executor and never calls Dispatch at all, so a stored
// answer cannot reach a stopping rule. Only the ensemble's two judges come
// back through here, and both set NoCache. A flag would suggest the guarantee
// rests on someone remembering to pass it.
func Servable(r Request) (bool, string) {
	switch {
	case r.NoCache:
		return false, "the caller asked for a fresh answer"
	case r.PII:
		return false, "the prompt carries personal data"
	case r.Injection:
		return false, "the prompt carries an injection marker"
	case r.Pinned:
		return false, "a head was pinned, and a stored answer may have come from another"
	}
	return true, ""
}

// Open loads the cache when it is on, and reports nil when it is not. A nil
// store is the off switch everywhere downstream, so no caller needs to ask
// twice.
func Open(cfg *config.Config) (*Store, error) {
	if !Enabled(cfg) {
		return nil, nil
	}
	s, err := OpenDir(Dir())
	if err != nil {
		return nil, err
	}
	s.SetBudget(Budget(cfg))
	return s, nil
}
