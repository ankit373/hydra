// SPDX-License-Identifier: MIT

// Package dispatch routes a prompt through policy evaluation, head selection,
// and execution — with automatic fallback on failure.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/a2a"
	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/ledger"
	"github.com/ankit373/hydra/internal/policy"
	"github.com/ankit373/hydra/internal/pricing"
	"github.com/ankit373/hydra/internal/probe"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
)

// ErrNoHeads marks a dispatch with no head to run, discovered or routable.
// Callers match it with errors.Is instead of the formatted message, so a
// CLI-flavored string (backtick-quoted `hyctl probe`, etc.) never has to leak
// into a GUI caller that has no terminal to point at (#452).
var ErrNoHeads = errors.New("no dispatchable heads")

// Options controls dispatch behaviour.
type Options struct {
	TierHint  string // tier name from config, e.g. "standard"
	LocalOnly bool   // override: force local heads only
	MaxTokens int
	System    string
	DryRun    bool   // print selected head without executing
	A2AFile   string // path to A2A handoff JSON; prepends structured context to prompt
	Enum      string // enum key (e.g. "SIMPLE") for cost logging

	// Resource is the file (or other resource) this dispatch acts on, if any —
	// e.g. the path editor.Edit is about to write. Passed to the ledger so a
	// policy rule can express least-privilege file scoping ("deny writes under
	// internal/auth/**"), not just per-head rules. Empty means no resource
	// concept applies (e.g. a plain text dispatch with no target file).
	Resource string

	// MaxCostUSD refuses a candidate whose estimated cost exceeds it before
	// executing — the same preflight guard swarm.Options.MaxEstCostUSD already
	// gives fan-out mode, extended to ordinary dispatch. 0 = no limit.
	MaxCostUSD float64

	// RunID groups every log row produced by one user-facing invocation;
	// TaskID groups the rows for one logical task inside it. Empty means
	// "derive one" (see runid.ResolveRun/ResolveTask) — pass them explicitly
	// when several dispatches belong to the same run, as a parallel batch or a
	// swarm does, otherwise each call is its own run.
	RunID  string
	TaskID string
}

// Result is the outcome of a successful dispatch.
type Result struct {
	Output    string
	Head      provider.Head
	Fallbacks []provider.Head // remaining candidates after the selected head
	Retries   int
	*executor.Response
}

// stateMu protects concurrent read-modify-write on state.json across goroutines.
var stateMu sync.Mutex

// Dispatcher holds resolved config and the probed head list.
type Dispatcher struct {
	cfg     *config.Config
	heads   []provider.Head
	policy  *policy.Engine
	pricing *pricing.DB
	budget  *budget.Registry
}

// Heads returns the probed head list for external callers (e.g. swarm).
func (d *Dispatcher) Heads() []provider.Head { return d.heads }

// PIILocalOnly reports whether the configured pii policy forces local-only
// routing. Exported so callers that bypass Dispatch — the SPRT/swarm branches
// in cmd/hydra's cmdDispatch — can still enforce it instead of acting only on
// --local, which was config-blind on that path (#500).
func (d *Dispatcher) PIILocalOnly() bool { return piiLocalOnly(d.cfg) }

// EstimateCost exposes per-tier cost estimation for external callers.
func (d *Dispatcher) EstimateCost(tier, inputTokens, outputTokens int) float64 {
	return d.estimateCost(tier, inputTokens, outputTokens)
}

// probeCacheTTL bounds how long a machine probe is reused across calls to New
// within one process. New is called once per CLI invocation, but also once per
// task inside a parallel batch and once per view inside a TUI snapshot — with
// no cache each of those redoes the same ~13 exec.LookPath calls and two
// network liveness checks. A short TTL collapses those into one real probe
// without leaving a long-running TUI session blind to a server that started
// or stopped mid-session.
const probeCacheTTL = 3 * time.Second

var (
	probeCacheMu     sync.Mutex
	probeCacheResult *probe.Result
	probeCacheAt     time.Time
	probeCacheEnv    string
)

// probeEnvFingerprint is HOME+PATH, the two environment variables that change
// what a probe discovers (CLI binaries on PATH; agy/models overlay under
// HOME). Any change invalidates the cache immediately regardless of TTL —
// without this, a cached result from one HOME (e.g. a test's temp dir with
// its own stub binaries) would be silently reused after HOME changes.
func probeEnvFingerprint() string {
	return os.Getenv("HOME") + "\x00" + os.Getenv("PATH")
}

// cachedProbe returns probe.Run's result, reusing it for probeCacheTTL as long
// as the environment it depends on hasn't changed.
func cachedProbe(ctx context.Context) *probe.Result {
	probeCacheMu.Lock()
	defer probeCacheMu.Unlock()
	env := probeEnvFingerprint()
	if probeCacheResult != nil && env == probeCacheEnv && time.Since(probeCacheAt) < probeCacheTTL {
		return probeCacheResult
	}
	probeCacheResult = probe.Run(ctx)
	probeCacheAt = time.Now()
	probeCacheEnv = env
	return probeCacheResult
}

// piiLocalOnly reports whether cfg's pii policy forces local-only routing.
func piiLocalOnly(cfg *config.Config) bool {
	p, ok := cfg.Policies["pii"]
	return ok && p.Action == "local-only"
}

// New builds a Dispatcher from the saved config and a (possibly cached) machine probe.
func New(ctx context.Context) (*Dispatcher, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("no hydra config — run: hyctl init")
	}

	result := cachedProbe(ctx)
	localOnly := piiLocalOnly(cfg)

	budgetReg := budget.NewRegistry(budget.LoadWindows(config.ScriptHome()))

	return &Dispatcher{
		cfg:     cfg,
		heads:   result.Heads,
		policy:  policy.New(policy.DefaultRules(localOnly)),
		pricing: pricing.Load(),
		budget:  budgetReg,
	}, nil
}

// Dispatch routes prompt through policy + tier selection + execution with fallback.
func (d *Dispatcher) Dispatch(ctx context.Context, prompt string, opts Options) (*Result, error) {
	// Resolve the hint to a capability number BEFORE the governor runs. A named
	// config tier ("expert") is otherwise opaque to claudeMode's Atoi, which
	// left the whole token-preservation table inert for every non-numeric hint
	// (#165). A hint that is invalid on its face — an unconfigured name or an
	// out-of-range number — is rejected here, before routability ever enters
	// the picture, so the error blames the actual cause (#451, #454).
	hint, err := d.resolveTierHint(opts.TierHint)
	if err != nil {
		return nil, err
	}

	// Apply claude% preservation — may downgrade tier or abort.
	tier, mode, pct := d.claudeMode(hint)
	switch mode {
	case "emergency":
		log.Printf("🚨 EMERGENCY: Claude at %d%% — routing to local tier only. Start a new session.", pct)
		tier = "10"
		opts.LocalOnly = true
	case "critical":
		log.Printf("🔴 CRITICAL: Claude at %d%% — downgrading tier by 2. Run /compact NOW.", pct)
	case "warning":
		log.Printf("🟠 WARNING: Claude at %d%% — downgrading tier by 1. Run /compact now.", pct)
	case "caution":
		log.Printf("🟡 CAUTION: Claude at %d%% — Run /compact immediately.", pct)
	case "compact":
		log.Printf("ℹ️  Claude at %d%% — Consider running /compact.", pct)
	}

	// Inject A2A handoff context into prompt if provided. --a2a names a file the
	// user explicitly asked for, so a read/parse failure must fail the dispatch
	// rather than silently running without the handoff context (#450).
	if opts.A2AFile != "" {
		injected, err := injectA2A(opts.A2AFile, prompt)
		if err != nil {
			return nil, fmt.Errorf("--a2a %s: %w", opts.A2AFile, err)
		}
		prompt = injected
	}

	req := policy.Request{Prompt: prompt, TierHint: tier}
	action := d.policy.Evaluate(req)

	if action.Deny {
		return nil, fmt.Errorf("dispatch denied by policy: %s", action.Reason)
	}

	localOnly := action.LocalOnly || opts.LocalOnly
	candidates := d.selectHeads(tier, localOnly)
	if len(candidates) == 0 {
		// Report the effective localOnly (policy may have forced it), not just
		// the caller's flag, or a PII-forced local-only run points the user at
		// the wrong cause.
		// Pointing at `hyctl probe` was actively misleading when the only
		// matching heads were ones probe listed but no executor can drive:
		// the user looks, sees the head, and learns nothing (#248). Name the
		// blocked heads and why instead.
		if blocked := d.blockedHeads(localOnly); blocked != "" {
			return nil, fmt.Errorf("%w: no routable heads for %s (localOnly=%v).\n%s",
				ErrNoHeads, tierClause(tier), localOnly, blocked)
		}
		return nil, fmt.Errorf("%w: no available heads for %s (localOnly=%v); "+
			"check `hyctl probe` and the tier names in your config",
			ErrNoHeads, tierClause(tier), localOnly)
	}

	if opts.DryRun {
		return &Result{Head: candidates[0], Fallbacks: candidates[1:]}, nil
	}

	// The run log records the shape of the run — which head was picked, when,
	// and how it ended — none of which the cost/dispatch outcome rows capture.
	// Observability must never fail the work, so every append error is ignored.
	rl := runlog.New(runid.ResolveRun(opts.RunID))
	taskID := runid.ResolveTask(opts.TaskID)

	var lastErr error
	for i, h := range candidates {
		_ = rl.Append(runlog.Event{
			Kind: runlog.KindHeadSelected, TaskID: taskID,
			Head: h.ID, Model: h.Name, Tier: rank.UITier(h),
			Detail: fmt.Sprintf("candidate %d of %d", i+1, len(candidates)),
		})
		// Fails closed like ledger.Check itself: a policy-check error (unreadable
		// policy file, ledger write failure) must deny, not silently let the
		// candidate through just because the decision couldn't be computed.
		// CheckAndRecordDispatch's LoadPolicy is itself mtime-cached, so trying
		// every candidate against the same prompt no longer re-reads and
		// re-parses the identical policy file from disk once per candidate.
		if decision, lerr := ledger.CheckAndRecordDispatch("hydra-dispatch", h.ID, opts.Resource, prompt); lerr != nil || decision == ledger.Deny {
			detail := "denied by ledger policy"
			if lerr != nil {
				detail = "ledger policy check failed: " + lerr.Error()
			}
			lastErr = fmt.Errorf("%s: head %s", detail, h.ID)
			_ = rl.Append(runlog.Event{
				Kind: runlog.KindError, TaskID: taskID,
				Head: h.ID, Model: h.Name, Tier: rank.UITier(h),
				Status: "denied", Detail: detail,
			})
			continue
		}
		if opts.MaxCostUSD > 0 {
			estInputTokens := len(prompt) / 4
			estCost := d.estimateCost(rank.UITier(h), estInputTokens, estInputTokens/2)
			if estCost > opts.MaxCostUSD {
				lastErr = fmt.Errorf("estimated cost $%.4f for head %s exceeds limit $%.4f", estCost, h.ID, opts.MaxCostUSD)
				_ = rl.Append(runlog.Event{
					Kind: runlog.KindError, TaskID: taskID,
					Head: h.ID, Model: h.Name, Tier: rank.UITier(h),
					Status: "denied", Detail: "exceeds cost ceiling",
				})
				// Shares the ledger's accountability trail with policy denials
				// (Reason distinguishes them) so `hyctl security` has one place
				// to find every kind of refused access, cost or policy.
				_ = ledger.Record(ledger.DefaultPath(), ledger.Event{
					Agent: "hydra-dispatch", Tool: h.ID, Decision: ledger.Deny,
					Reason: fmt.Sprintf("exceeds cost ceiling: estimated $%.4f > limit $%.4f", estCost, opts.MaxCostUSD),
				})
				continue
			}
		}
		started := time.Now()
		exec := executor.For(h)
		resp, err := exec.Execute(ctx, executor.Request{
			Prompt:    prompt,
			Head:      h,
			MaxTokens: opts.MaxTokens,
			System:    opts.System,
		})
		if err != nil {
			lastErr = err
			// A failed candidate is part of the run's shape: it is why the
			// fallback chain advanced, and nothing else records it.
			_ = rl.Append(runlog.Event{
				Kind: runlog.KindError, TaskID: taskID,
				Head: h.ID, Model: h.Name, Tier: rank.UITier(h),
				Status: "failed", DurationMS: time.Since(started).Milliseconds(),
				Detail: truncate(err.Error(), 200),
			})
			continue
		}
		r := &Result{Output: resp.Output, Head: h, Retries: i, Response: resp}
		_ = rl.Append(runlog.Event{
			Kind: runlog.KindDispatchFinished, TaskID: taskID,
			Head: h.ID, Model: resp.Model, Tier: rank.UITier(h), Status: "ok",
			CostUSD:    d.estimateCost(rank.UITier(h), resp.InputTokens, resp.OutputTokens),
			DurationMS: resp.Duration.Milliseconds(),
		})
		_ = d.logDispatch(r, prompt, opts)
		if from, err := d.writeHandoff(r, prompt); err == nil {
			// last_handoff.json keeps only the newest. Appending the handoff
			// here is what makes a *chain* of them reconstructable, which is
			// the stated purpose of KindHandoff and did not happen before #204.
			_ = rl.Append(runlog.Event{
				Kind: runlog.KindHandoff, TaskID: taskID,
				Agent: h.ID, Head: h.ID, Model: h.Name,
				Ref: from, Detail: "context handed to " + from,
			})
		}
		d.recordBudget(r)
		d.syncStateJSON(r)
		return r, nil
	}

	return nil, fmt.Errorf("all heads failed (tried %d): %w", len(candidates), lastErr)
}

// claudeMode reads claude_pct from state.json and returns the (possibly downgraded) tier,
// mode string, and raw percentage. The mode is rate-aware: a fast burn (high
// first-passage risk toward the 80% line) escalates the mode above its static
// level band, so the tier downgrades earlier. With no history the effective mode
// equals ModeFor(pct) — identical to the prior behavior.
func (d *Dispatcher) claudeMode(tierHint string) (tier string, mode string, pct int) {
	pct = readClaudePct()
	_, risk := budget.RiskFromHistory(readClaudePctHistory())
	mode = budget.EffectiveMode(pct, risk).String()
	tier = tierHint

	// Convert tier hint to int so we can downgrade numerically.
	t, err := strconv.Atoi(tierHint)
	if err != nil {
		return tier, mode, pct
	}
	switch mode {
	case "critical":
		t += 2
	case "emergency":
		t = 10
	case "warning":
		t++
	}
	// Only escalation overflow reaches this — resolveTierHint already rejects
	// any raw hint outside 1-10, so this clamps e.g. 9+2=11, never user input
	// silently getting relabelled as "10" (#454).
	if t > maxTier {
		t = maxTier
	}
	return strconv.Itoa(t), mode, pct
}

func readClaudePct() int {
	statePath := filepath.Join(config.Dir(), "logs", "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return 0
	}
	var s struct {
		ClaudePct int `json:"claude_pct"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0
	}
	return s.ClaudePct
}

// readClaudePctHistory reads the bounded claude_pct trajectory from state.json.
// A missing file or field yields nil, so callers get no rate signal.
func readClaudePctHistory() []int {
	statePath := filepath.Join(config.Dir(), "logs", "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}
	var s struct {
		History []int `json:"claude_pct_history"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	return s.History
}

// asInt coerces a value decoded from JSON (numbers arrive as float64) to an int.
func asInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}

// asIntSlice coerces a JSON-decoded array (elements arrive as float64) to []int.
func asIntSlice(v any) []int {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, e := range raw {
		out = append(out, asInt(e))
	}
	return out
}

// injectA2A reads a handoff JSON file and prepends a structured block to the prompt.
// The path always comes from the user-supplied --a2a flag, so unlike a2a.Load's
// "missing file = no prior handoff" contract for its other (auto-load) callers,
// a missing or malformed file here is a mistake the user needs to hear about.
func injectA2A(path, prompt string) (string, error) {
	h, err := a2a.Load(path)
	if err != nil {
		return prompt, fmt.Errorf("a2a: malformed handoff file %s: %w", path, err)
	}
	if h == nil {
		return prompt, fmt.Errorf("a2a: handoff file not found: %s", path)
	}
	return h.PromptBlock(prompt) + "\n\nADDITIONAL INSTRUCTION:\n" + prompt, nil
}

// writeHandoff saves last_handoff.json after a successful dispatch, advancing
// the vector clock so downstream agents inherit this dispatch's causal history.
// It returns the handoff's From identity so the caller can record the edge.
func (d *Dispatcher) writeHandoff(r *Result, prompt string) (string, error) {
	handoffPath := filepath.Join(config.Dir(), "logs", "last_handoff.json")

	// Inherit the prior handoff's clock (if any) and tick for this agent.
	var base a2a.Clock
	if prior, err := a2a.Load(handoffPath); err == nil && prior != nil {
		base = prior.Clock
	}
	from := fmt.Sprintf("hydra-tier-%d", rank.UITier(r.Head))
	// The clock is keyed on the head's own identity, not its tier bucket:
	// every LocalOnly head shares tier 10, so two different local models
	// would otherwise tick the same "hydra-tier-10" key and become
	// indistinguishable under Clock.Compare (#503). "From" keeps the
	// tier-bucket string — it is display text, not a clock key.
	agentKey := r.Head.ID

	h := a2a.Handoff{
		From:        from,
		Model:       r.Head.Name,
		Task:        prompt,
		PriorOutput: r.Response.Output,
		Clock:       base.Tick(agentKey),
	}
	return from, h.Save(handoffPath)
}

// minTier and maxTier bound the numeric --tier flag: rank.UITier never
// produces a value outside this range, so nothing above or below it could
// ever be routable (#454).
const (
	minTier = 1
	maxTier = 10
)

// resolveTierHint normalizes a tier hint to a capability number ("1".."10"),
// and reports the two ways a hint can be invalid on its face:
//   - numeric but outside [minTier,maxTier] — e.g. "0" and "15" both silently
//     behaved as "no tier" or got clamped to 10 with no indication anything
//     was wrong (#454).
//   - named but absent from cfg.Tiers entirely — a config/typo problem, not a
//     routability one, so the caller must not blame the head pool for it (#451).
//
// A name that IS configured but currently has no live heads is deliberately
// NOT an error here: it resolves unchanged, and selectHeads/blockedHeads
// report the routability gap instead — the tier itself was valid.
func (d *Dispatcher) resolveTierHint(hint string) (string, error) {
	if hint == "" {
		return "", nil
	}
	if n, err := strconv.Atoi(hint); err == nil {
		if n < minTier || n > maxTier {
			return "", fmt.Errorf("tier %d is out of range — valid tiers are %d-%d", n, minTier, maxTier)
		}
		return hint, nil
	}
	for _, t := range d.cfg.Tiers {
		if t.Name != hint {
			continue
		}
		ids := make(map[string]bool, len(t.Heads))
		for _, id := range t.Heads {
			ids[id] = true
		}
		strongest := 0
		for _, h := range d.heads {
			if !ids[h.ID] {
				continue
			}
			if n := rank.UITier(h); strongest == 0 || n < strongest {
				strongest = n
			}
		}
		if strongest > 0 {
			return strconv.Itoa(strongest), nil
		}
		return hint, nil // named tier has no live heads; let the caller report it
	}
	return "", fmt.Errorf("unknown tier %q — configured tiers: %s", hint, d.tierNameList())
}

// tierNameList formats cfg.Tiers' names for the "unknown tier" error, so the
// user sees what they could have typed instead of just what they got wrong.
func (d *Dispatcher) tierNameList() string {
	if len(d.cfg.Tiers) == 0 {
		return "(none configured — run `hyctl init`)"
	}
	names := make([]string, len(d.cfg.Tiers))
	for i, t := range d.cfg.Tiers {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

// tierClause phrases the tier for a no-heads error. An empty tier (no hint
// given at all — e.g. the desktop dock's "auto-route" default) would
// otherwise print as tier "" — a confusing artifact, not a routing outcome.
func tierClause(tier string) string {
	if tier == "" {
		return "no tier hint given"
	}
	return fmt.Sprintf("tier %q", tier)
}

// blockedHeads describes discovered heads that were excluded because no
// executor can drive them, formatted for an error message. Returns "" when
// nothing was excluded for that reason — in which case the real problem is the
// tier or the config, and the caller says so instead.
//
// Respects localOnly so a PII-forced local run does not list cloud heads the
// user was never going to be allowed to use anyway.
func (d *Dispatcher) blockedHeads(localOnly bool) string {
	var b strings.Builder
	for _, h := range d.heads {
		if localOnly && !h.LocalOnly {
			continue
		}
		if why := executor.Unroutable(h); why != "" {
			fmt.Fprintf(&b, "  %s (%s): %s\n", h.Name, h.Provider, why)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "Discovered, but not routable:\n" + b.String()
}

// selectHeads returns heads to try, in order of preference.
//
// No tier hint  → all probed heads ranked by score (config tiers ignored).
// Named tier    → heads assigned to that tier in config (e.g. "expert").
// Numeric tier  → heads whose capability tier is at or below the requested
//
//	strength, via rank.UITier. Lower number = stronger, so "10" yields the
//	cheapest local heads and "1" yields everything with the strongest first.
//
// Numeric hints must NOT be resolved through config tier names: config tiers
// are named (expert/complex/…) while enums resolve to "1".."10", so an exact
// name match can never succeed and the old fall-through silently returned the
// single most expensive head — the exact inverse of cost routing (#165).
//
// A hint that matches nothing returns no candidates; the caller reports that
// rather than silently widening to every head.
func (d *Dispatcher) selectHeads(tierHint string, localOnly bool) []provider.Head {
	filter := func(h provider.Head) bool {
		if !executor.Supports(h) {
			return false
		}
		if localOnly && !h.LocalOnly {
			return false
		}
		return true
	}

	// No tier specified — use every live head, best score first.
	if tierHint == "" {
		var all []provider.Head
		for _, h := range d.heads {
			if filter(h) {
				all = append(all, h)
			}
		}
		return all
	}

	// Numeric hint → select by capability tier, independent of config naming.
	if want, err := strconv.Atoi(tierHint); err == nil {
		var candidates []provider.Head
		for _, h := range d.heads {
			// d.heads is pre-sorted by CapScore (probe.Run → rank.ByCapScore),
			// so "at or below the requested strength" yields the strongest
			// eligible head first and weaker ones as the fallback chain.
			if filter(h) && rank.UITier(h) >= want {
				candidates = append(candidates, h)
			}
		}
		if len(candidates) > 0 {
			return candidates
		}
		// Nothing is that cheap. Degrade to the cheapest heads available —
		// ascending capability, so the fallback is the least expensive option
		// rather than the most. Silently escalating to the strongest head is
		// what made tier routing worthless in the first place (#165).
		for _, h := range d.heads {
			if filter(h) {
				candidates = append(candidates, h)
			}
		}
		if len(candidates) > 0 {
			slices.Reverse(candidates) // pre-sorted strongest-first → cheapest-first
			log.Printf("⚠️  no head at tier %d or cheaper — falling back to the cheapest available (%s)",
				want, candidates[0].ID)
		}
		return candidates
	}

	// Named tier → heads assigned to it in config.
	tierIDs := map[string]bool{}
	for _, t := range d.cfg.Tiers {
		if t.Name == tierHint {
			for _, id := range t.Heads {
				tierIDs[id] = true
			}
		}
	}

	var candidates []provider.Head
	for _, h := range d.heads {
		if filter(h) && tierIDs[h.ID] {
			candidates = append(candidates, h)
		}
	}
	return candidates
}

// recordBudget updates the budget registry with this call's token usage.
func (d *Dispatcher) recordBudget(r *Result) {
	if d.budget == nil || r.Response.InputTokens == 0 {
		return
	}
	// Estimated tokens (agy char/4) must not be booked as measured usage.
	source := "real"
	if r.Response.TokensEstimated {
		source = "estimate"
	}
	d.budget.Record(r.Head.ID, r.Response.InputTokens, source)
}

// syncStateJSON updates ~/.hydra/logs/state.json after a successful dispatch so
// the governor readouts reflect it — `hyctl status`, the cockpit's dashboard,
// and the desktop app all read that file.
func (d *Dispatcher) syncStateJSON(r *Result) {
	stateMu.Lock()
	defer stateMu.Unlock()

	stateDir := filepath.Join(config.Dir(), "logs")
	statePath := filepath.Join(stateDir, "state.json")

	existing := map[string]any{}
	if raw, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(raw, &existing)
	}

	existing["last_model"] = r.Head.Name
	existing["last_status"] = "ok"
	existing["last_tier"] = rank.UITier(r.Head)

	// Append the current orchestrator claude_pct (written by the orchestrator,
	// e.g. `jq '.claude_pct = 52'`) to a bounded history, so `hyctl status` can
	// compute a rate-aware first-passage risk from the session trajectory rather
	// than reacting only to the instantaneous level.
	if pct := asInt(existing["claude_pct"]); pct > 0 {
		existing["claude_pct_history"] = budget.AppendPctHistory(
			asIntSlice(existing["claude_pct_history"]), pct, budget.MaxPctHistory)
	}

	// Persist per-model budget snapshots so the TUI and status command can read them.
	if d.budget != nil {
		snaps := d.budget.All()
		budgetMap := map[string]any{}
		for _, s := range snaps {
			budgetMap[s.ModelID] = map[string]any{
				"pct":        s.Pct,
				"used":       s.Used,
				"window":     s.Window,
				"mode":       s.Mode.String(),
				"source":     s.Source,
				"updated_at": s.UpdatedAt.Format(time.RFC3339),
			}
		}
		existing["budget"] = budgetMap
	}

	updated, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return
	}

	_ = os.MkdirAll(stateDir, 0o700)
	tmp := statePath + ".tmp"
	if err := os.WriteFile(tmp, updated, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, statePath)
}

// logDispatch writes to dispatch.jsonl and cost.jsonl.
func (d *Dispatcher) logDispatch(r *Result, prompt string, opts Options) error {
	tier := rank.UITier(r.Head)
	wallMs := r.Response.Duration.Milliseconds()
	estCost := d.estimateCost(tier, r.Response.InputTokens, r.Response.OutputTokens)

	logDir := filepath.Join(config.Dir(), "logs")
	_ = os.MkdirAll(logDir, 0o700)

	// Resolve once so both rows carry the same identity. Before #181 these read
	// env vars nothing ever set, so every row logged run_id:"" / task_id:"".
	runID := runid.ResolveRun(opts.RunID)
	taskID := runid.ResolveTask(opts.TaskID)

	dispatchEntry := map[string]any{
		"ts":             time.Now().UTC().Format(time.RFC3339),
		"head":           r.Head.ID,
		"provider":       r.Head.Provider,
		"tier":           tier,
		"enum":           opts.Enum,
		"input_tokens":   r.Response.InputTokens,
		"output_tokens":  r.Response.OutputTokens,
		"duration_ms":    wallMs,
		"local":          r.Head.LocalOnly,
		"prompt_preview": truncate(prompt, 80),
		"task_id":        taskID,
		"run_id":         runID,
	}
	if err := appendJSONL(filepath.Join(logDir, "dispatch.jsonl"), dispatchEntry); err != nil {
		return err
	}

	// cost.jsonl only written when we have token data.
	if r.Response.InputTokens > 0 || r.Response.OutputTokens > 0 {
		// Provenance labels come from cost.SourceLabels so dispatch and swarm
		// stay in lock-step: tokens_source reflects whether the provider
		// reported usage or Hydra estimated it (agy char/4); cost_source is
		// always "estimated" (est_cost_usd is pricing × tokens, never billed);
		// the legacy `source` field mirrors tokens_source for older readers.
		tokensSource, costSource, legacySource := cost.SourceLabels(r.Response.TokensEstimated)
		breadcrumb, _ := config.Breadcrumb()
		costEntry := map[string]any{
			"ts":              time.Now().UTC().Format(time.RFC3339),
			"tier":            tier,
			"enum":            opts.Enum,
			"model":           r.Response.Model,
			"executor":        r.Head.Provider,
			"pool":            r.Head.Meta["token_pool"],
			"prompt_tokens":   r.Response.InputTokens,
			"response_tokens": r.Response.OutputTokens,
			"est_cost_usd":    estCost,
			"wall_ms":         wallMs,
			"tokens_source":   tokensSource,
			"cost_source":     costSource,
			"source":          legacySource,
			"task_id":         taskID,
			"run_id":          runID,
		}
		if breadcrumb != "" { // match the omitempty on cost.Row.Config
			costEntry["config"] = breadcrumb
		}
		_ = appendJSONL(filepath.Join(logDir, "cost.jsonl"), costEntry)
	}

	return nil
}

func appendJSONL(path string, entry map[string]any) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, string(raw))
	return err
}

// estimateCost returns $/call using the live pricing DB (OpenRouter + tier fallback).
func (d *Dispatcher) estimateCost(tier, inputTokens, outputTokens int) float64 {
	if d.pricing == nil {
		return 0
	}
	return d.pricing.EstimateCost(tier, inputTokens, outputTokens)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// EnumToTier maps a routing enum key (e.g. "SIMPLE") to a tier number string.
// Single source of truth — editor and parallel both delegate here.
func EnumToTier(enum string) string {
	const (
		GRUNT     = "10"
		TRIVIAL   = "9"
		SIMPLE    = "8"
		STANDARD  = "7"
		MODERATE  = "6"
		COMPLEX   = "5"
		HARD      = "4"
		VERY_HARD = "3"
		EXPERT    = "2"
		CORE      = "1"
	)
	switch enum {
	case "GRUNT":
		return GRUNT
	case "TRIVIAL":
		return TRIVIAL
	case "SIMPLE":
		return SIMPLE
	case "STANDARD":
		return STANDARD
	case "MODERATE":
		return MODERATE
	case "COMPLEX":
		return COMPLEX
	case "HARD":
		return HARD
	case "VERY_HARD":
		return VERY_HARD
	case "EXPERT":
		return EXPERT
	case "CORE":
		return CORE
	default:
		return ""
	}
}
