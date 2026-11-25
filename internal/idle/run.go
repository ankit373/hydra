// SPDX-License-Identifier: MIT

package idle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/entropy"
	"github.com/ankit373/hydra/internal/payload"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
)

// Default caps on idle work: one job, and one day's total.
const (
	DefaultMaxJob   = 10 * time.Minute
	DefaultMaxNight = 2 * time.Hour
	// defaultWindow is the window a conversation fills when no head it was
	// served by is known.
	defaultWindow = 200000
)

// Seen is a conversation as the run log shows it: cheap to list, so Tick only
// reads the transcript of one that has been quiet long enough.
type Seen struct {
	Key       string
	LastEvent time.Time
	Head      string // head of its last answer, for the window it fills
}

// Pins is the part of the payload store a pack's originals are held by.
type Pins interface {
	Pin(owner string, refs []string) error
	Has(ref string) bool
}

// Env is everything Tick reads from the machine, so a test can supply it.
type Env struct {
	Dir         string // $HYDRA_HOME/idle
	Config      Config
	MaxJob      time.Duration
	MaxNight    time.Duration
	MinAgree    float64
	Poll        time.Duration // how often a running job looks for a new request
	Seen        func() ([]Seen, error)
	Activity    func() (time.Time, error) // newest request of any conversation
	Heads       func() []provider.Head
	Windows     func(heads []provider.Head) map[string]int
	Machine     func() Machine
	Transcript  func(key string) (Turns, error)
	NewHead     func(provider.Head) Head
	Store       Pins
	Log         func(runlog.Event)
	ConvWindows map[string]int // window by head id, for the fill of a conversation
}

// Scheduler runs at most one compaction job at a time.
type Scheduler struct {
	env       Env
	busy      atomic.Bool
	mu        sync.Mutex
	lastEvent map[string]time.Time // newest event per conversation, from this tick's Seen
}

// NewScheduler fills the caps and poll interval env leaves unset.
func NewScheduler(env Env) *Scheduler {
	if env.MaxJob <= 0 {
		env.MaxJob = DefaultMaxJob
	}
	if env.MaxNight <= 0 {
		env.MaxNight = DefaultMaxNight
	}
	if env.Poll <= 0 {
		env.Poll = 2 * time.Second
	}
	return &Scheduler{env: env}
}

// Outcome is what a tick did.
type Outcome string

const (
	OutcomeNone      Outcome = "none"
	OutcomeStaged    Outcome = "staged"
	OutcomeRejected  Outcome = "rejected"
	OutcomeCancelled Outcome = "cancelled"
)

// Result is one tick: the conversation it worked on, if any, and why it ended.
type Result struct {
	Conv    string
	Outcome Outcome
	Reason  string
	Pack    int // number of the staged or rejected pack
}

// Staged is a pack on disk with the verdict that let it through.
type Staged struct {
	Pack    Pack    `json:"pack"`
	Verdict Verdict `json:"verdict"`
}

// Status is the last thing that happened to a conversation.
type Status struct {
	Key     string    `json:"key"`
	Outcome Outcome   `json:"outcome"`
	Reason  string    `json:"reason,omitempty"`
	At      time.Time `json:"at"`
	// LastEvent is the conversation's newest event when this was noted, so a
	// rejected pack is not rebuilt until the conversation moves.
	LastEvent time.Time `json:"last_event"`
}

// Tick looks at every conversation and compacts at most one, the one with the
// most new tokens. A job stops, nothing staged, the moment a request arrives.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) (Result, error) {
	if !s.busy.CompareAndSwap(false, true) {
		return Result{Outcome: OutcomeNone, Reason: "a job is already running"}, nil
	}
	defer s.busy.Store(false)
	seen, err := s.env.Seen()
	if err != nil {
		return Result{}, err
	}
	// Rebuilt each tick, so a long-running server does not keep every key it ever saw.
	s.lastEvent = make(map[string]time.Time, len(seen))
	for _, c := range seen {
		s.lastEvent[c.Key] = c.LastEvent
	}
	heads := s.env.Heads()
	windows := s.env.Windows(heads)
	machine := s.env.Machine()
	cfg := s.env.Config.orDefault()

	var best struct {
		job   Job
		conv  Conversation
		turns Turns
	}
	for _, c := range seen {
		if quiet := now.Sub(c.LastEvent); quiet < cfg.IdleAfter {
			s.note(c.Key, OutcomeNone, fmt.Sprintf("active %s ago; idle after %s", quiet.Round(time.Second), cfg.IdleAfter), now)
			continue
		}
		// Nothing new since a pack was staged or refused: skip reading the transcript.
		if st := readStatus(s.env.Dir, c.Key); (st.Outcome == OutcomeRejected || st.Outcome == OutcomeStaged) && st.LastEvent.Equal(c.LastEvent) {
			continue
		}
		turns, err := s.env.Transcript(c.Key)
		if err != nil {
			s.note(c.Key, OutcomeNone, err.Error(), now)
			continue
		}
		conv := s.observe(c, turns)
		job, ok, why := Eligible(conv, heads, windows, machine, now, cfg)
		if !ok {
			s.note(c.Key, OutcomeNone, why, now)
			continue
		}
		if conv.NewTokens > best.conv.NewTokens {
			best.job, best.conv, best.turns = job, conv, turns
		}
	}
	if best.job.Conv == "" {
		return Result{Outcome: OutcomeNone, Reason: "nothing to compact"}, nil
	}
	left := s.nightLeft(now)
	if left <= 0 {
		s.note(best.job.Conv, OutcomeNone, "the cap for one night is used up", now)
		return Result{Conv: best.job.Conv, Outcome: OutcomeNone, Reason: "the cap for one night is used up"}, nil
	}
	return s.run(ctx, best.job, best.turns, now, min(s.env.MaxJob, left)), nil
}

// observe turns what the log shows into what Eligible needs: how full the
// window is and how many tokens no staged pack covers yet.
func (s *Scheduler) observe(c Seen, t Turns) Conversation {
	window, ok := s.env.ConvWindows[c.Head]
	if !ok {
		window = defaultWindow
	}
	covered := 0
	if prev, _, err := LatestPack(s.env.Dir, c.Key); err == nil && prev != nil && prev.Covers.To <= len(t.Turns) {
		covered = prev.Covers.To
	}
	total, fresh := entropy.TokenEstimate(spanText(t.Turns)), entropy.TokenEstimate(spanText(t.Turns[covered:]))
	return Conversation{Key: c.Key, LastEvent: c.LastEvent, NewTokens: fresh,
		Quota: budget.Quota{Scope: c.Key, ContextFrac: float64(total) / float64(window)}}
}

func (s *Scheduler) run(ctx context.Context, job Job, turns Turns, now time.Time, limit time.Duration) Result {
	res := Result{Conv: job.Conv, Outcome: OutcomeCancelled}
	started := time.Now()
	defer func() { s.addSpent(now, time.Since(started)) }()

	jobCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var arrived atomic.Bool
	go s.watch(jobCtx, cancel, now, &arrived)

	s.log(runlog.KindIdleStarted, job, map[string]any{"tokens": job.Tokens})
	prev, _, _ := LatestPack(s.env.Dir, job.Conv)
	head := s.env.NewHead(job.Head)
	pack, err := Build(jobCtx, head, turns, prev, false)
	if err != nil {
		res.Reason = interrupted(jobCtx, &arrived, limit, err)
		if jobCtx.Err() == nil {
			return s.reject(job, res, Pack{Notes: pack.Notes}, Verdict{Reason: res.Reason}, now)
		}
		s.note(job.Conv, OutcomeCancelled, res.Reason, now)
		return res
	}
	v := Verify(pack, turns, s.env.Store.Has)
	if v.Pass {
		if v, err = RunProbes(jobCtx, head, pack, turns, v, s.env.MinAgree); err != nil {
			res.Reason = interrupted(jobCtx, &arrived, limit, err)
			s.note(job.Conv, OutcomeCancelled, res.Reason, now)
			return res
		}
	}
	if !v.Pass {
		res.Reason = v.Reason
		return s.reject(job, res, pack, v, now)
	}
	return s.stage(job, res, pack, v, turns, now)
}

// watch cancels the job when any request lands after the tick began.
func (s *Scheduler) watch(ctx context.Context, cancel context.CancelFunc, since time.Time, arrived *atomic.Bool) {
	t := time.NewTicker(s.env.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if at, err := s.env.Activity(); err == nil && at.After(since) {
				arrived.Store(true)
				cancel()
				return
			}
		}
	}
}

func interrupted(ctx context.Context, arrived *atomic.Bool, limit time.Duration, err error) string {
	switch {
	case arrived.Load():
		return "a request arrived"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("ran past the %s limit", limit.Round(time.Second))
	case ctx.Err() != nil:
		return "stopped"
	}
	return err.Error()
}

func (s *Scheduler) reject(job Job, res Result, p Pack, v Verdict, now time.Time) Result {
	n, err := nextNumber(s.env.Dir, job.Conv)
	if err == nil {
		err = writeJSON(filepath.Join(convDir(s.env.Dir, job.Conv), fmt.Sprintf("pack-%d.rejected.json", n)), Staged{p, v})
	}
	res.Outcome, res.Pack = OutcomeRejected, n
	if err != nil {
		res.Reason += " (not saved: " + err.Error() + ")"
	}
	s.log(runlog.KindIdleRejected, job, verdictMeta(v))
	s.note(job.Conv, OutcomeRejected, res.Reason, now)
	return res
}

func (s *Scheduler) stage(job Job, res Result, p Pack, v Verdict, t Turns, now time.Time) Result {
	s.log(runlog.KindIdleVerified, job, verdictMeta(v))
	n, err := nextNumber(s.env.Dir, job.Conv)
	if err == nil {
		err = s.env.Store.Pin(PinOwner(job.Conv), originalRefs(t.Turns[p.Covers.From:p.Covers.To], t.Files, p))
	}
	if err == nil {
		err = writeJSON(filepath.Join(convDir(s.env.Dir, job.Conv), fmt.Sprintf("pack-%d.json", n)), Staged{p, v})
	}
	if err != nil {
		res.Outcome, res.Reason = OutcomeRejected, "not staged: "+err.Error()
		s.log(runlog.KindIdleRejected, job, map[string]any{"reason": res.Reason})
		s.note(job.Conv, OutcomeRejected, res.Reason, now)
		return res
	}
	res.Outcome, res.Pack, res.Reason = OutcomeStaged, n, ""
	s.log(runlog.KindIdleStaged, job, verdictMeta(v))
	s.note(job.Conv, OutcomeStaged, "", now)
	return res
}

// PinOwner is the payload pin owner for a conversation's pack.
func PinOwner(conv string) string { return "idle:" + conv }

// originalRefs are the refs a pack stands on: its span's turns and files, and
// anything the pack itself cites.
func originalRefs(span []Turn, files []File, p Pack) []string {
	var refs []string
	for _, t := range span {
		refs = append(refs, t.Ref)
	}
	for _, f := range files {
		refs = append(refs, f.Ref)
	}
	refs = append(refs, p.refs()...)
	slices.Sort(refs)
	return slices.DeleteFunc(slices.Compact(refs), func(r string) bool { return r == "" })
}

func verdictMeta(v Verdict) map[string]any {
	return map[string]any{
		"pass": v.Pass, "reason": v.Reason, "probes": v.Probes, "probe_agree": v.ProbeAgree,
		"partial": v.Partial, "same_head": v.SameHead, "tokens_before": v.TokensBefore, "tokens_after": v.TokensAfter,
	}
}

func (s *Scheduler) log(kind runlog.Kind, job Job, meta map[string]any) {
	if s.env.Log == nil {
		return
	}
	meta["conversation"] = job.Conv
	s.env.Log(runlog.Event{Kind: kind, Head: job.Head.ID, Meta: meta})
}

// safeKey matches a conversation key that is already a safe directory name.
var safeKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

// convDir is where a conversation's packs live: its key, or a hash of it when
// the key holds characters a path should not.
func convDir(dir, conv string) string {
	if safeKey.MatchString(conv) {
		return filepath.Join(dir, conv)
	}
	return filepath.Join(dir, "h-"+payload.Hash(conv)[:16])
}

var packFile = regexp.MustCompile(`^pack-(\d+)(\.rejected)?\.json$`)

// packNumbers lists the pack numbers in a conversation's directory.
func packNumbers(dir, conv string, rejected bool) ([]int, error) {
	entries, err := os.ReadDir(convDir(dir, conv))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []int
	for _, e := range entries {
		if m := packFile.FindStringSubmatch(e.Name()); m != nil && (m[2] != "") == rejected {
			n, _ := strconv.Atoi(m[1])
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out, nil
}

func nextNumber(dir, conv string) (int, error) {
	n := 0
	for _, rej := range []bool{false, true} {
		nums, err := packNumbers(dir, conv, rej)
		if err != nil {
			return 0, err
		}
		if len(nums) > 0 {
			n = max(n, nums[len(nums)-1])
		}
	}
	return n + 1, nil
}

// LatestPack is the newest staged pack of conv and its number; nil, 0 when none.
func LatestPack(dir, conv string) (*Pack, int, error) {
	nums, err := packNumbers(dir, conv, false)
	if err != nil || len(nums) == 0 {
		return nil, 0, err
	}
	n := nums[len(nums)-1]
	raw, err := os.ReadFile(filepath.Join(convDir(dir, conv), fmt.Sprintf("pack-%d.json", n)))
	if err != nil {
		return nil, 0, err
	}
	var st Staged
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, 0, fmt.Errorf("idle: pack %d of %s: %w", n, conv, err)
	}
	return &st.Pack, n, nil
}

func (s *Scheduler) note(conv string, o Outcome, reason string, now time.Time) {
	last := s.lastEvent[conv]
	_ = writeJSON(filepath.Join(convDir(s.env.Dir, conv), "status.json"), Status{conv, o, reason, now.UTC(), last})
}

func readStatus(dir, conv string) Status {
	var st Status
	if raw, err := os.ReadFile(filepath.Join(convDir(dir, conv), "status.json")); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

type spent struct {
	Day     string `json:"day"`
	Seconds int64  `json:"seconds"`
}

func (s *Scheduler) spentPath() string { return filepath.Join(s.env.Dir, "spent.json") }

// nightLeft is how much of today's cap is unused.
func (s *Scheduler) nightLeft(now time.Time) time.Duration {
	var sp spent
	if raw, err := os.ReadFile(s.spentPath()); err == nil {
		_ = json.Unmarshal(raw, &sp)
	}
	if sp.Day != now.Format("2006-01-02") {
		return s.env.MaxNight
	}
	return s.env.MaxNight - time.Duration(sp.Seconds)*time.Second
}

func (s *Scheduler) addSpent(now time.Time, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	used := s.env.MaxNight - s.nightLeft(now) + d
	_ = writeJSON(s.spentPath(), spent{now.Format("2006-01-02"), int64(used / time.Second)})
}

// writeJSON replaces path through a temp file, so a reader never sees half a file.
func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(raw, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return werr
	}
	return os.Rename(tmp.Name(), path)
}

// Summary is one conversation for `hyctl idle status`: its last outcome and
// the newest staged pack.
type Summary struct {
	Status
	Pack     int      // number of the newest staged pack; 0 when none
	Verdict  *Verdict // the verdict that let it through
	Rejected int      // rejected packs kept beside it
}

// Summaries reads every conversation that has a directory under dir.
func Summaries(dir string) ([]Summary, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name(), "status.json"))
		if err != nil || !e.IsDir() {
			continue
		}
		var sm Summary
		if json.Unmarshal(raw, &sm.Status) != nil {
			continue
		}
		if nums, _ := packNumbers(dir, sm.Key, false); len(nums) > 0 {
			sm.Pack = nums[len(nums)-1]
			if raw, err := os.ReadFile(filepath.Join(dir, e.Name(), fmt.Sprintf("pack-%d.json", sm.Pack))); err == nil {
				var st Staged
				if json.Unmarshal(raw, &st) == nil {
					sm.Verdict = &st.Verdict
				}
			}
		}
		rej, _ := packNumbers(dir, sm.Key, true)
		sm.Rejected = len(rej)
		out = append(out, sm)
	}
	return out, nil
}
