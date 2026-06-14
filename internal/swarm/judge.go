package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
)

const defaultJudgeTimeout = 30 * time.Second

// JudgeVerdict is the structured outcome of a judge evaluation.
type JudgeVerdict struct {
	WinnerIndex int
	Scores      []int // 0-100 per attempt, same order as the attempts slice
	Reason      string
	Meta        JudgeMeta
}

// JudgeMeta records how the verdict was reached — LLM or fallback.
type JudgeMeta struct {
	Head           provider.Head
	InputTokens    int
	OutputTokens   int
	Duration       time.Duration
	UsedFallback   bool
	FallbackReason string
}

// Judge ranks a set of completed Attempts and returns a verdict.
type Judge interface {
	Judge(ctx context.Context, prompt string, attempts []Attempt) (*JudgeVerdict, error)
}

// ── LLMJudge ─────────────────────────────────────────────────────────────────

// LLMJudge dispatches a structured evaluation prompt to a configured head and
// parses {"winner":0,"scores":[85,72],"reason":"..."} from the response.
type LLMJudge struct {
	d       *dispatch.Dispatcher
	tier    string
	timeout time.Duration
}

func newLLMJudge(d *dispatch.Dispatcher, tierHint string, timeout time.Duration) *LLMJudge {
	if timeout <= 0 {
		timeout = defaultJudgeTimeout
	}
	return &LLMJudge{d: d, tier: tierHint, timeout: timeout}
}

func (j *LLMJudge) Judge(ctx context.Context, prompt string, attempts []Attempt) (*JudgeVerdict, error) {
	successful := successfulAttempts(attempts)
	if len(successful) == 0 {
		return nil, fmt.Errorf("judge: no successful attempts to evaluate")
	}
	if len(successful) == 1 {
		// Trivial case — no need to call the LLM.
		idx := successful[0]
		scores := make([]int, len(attempts))
		scores[idx] = attempts[idx].Head.CapScore
		return &JudgeVerdict{
			WinnerIndex: idx,
			Scores:      scores,
			Reason:      "only one successful response",
			Meta:        JudgeMeta{},
		}, nil
	}

	judgePrompt := buildJudgePrompt(prompt, attempts, successful)

	jCtx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()

	start := time.Now()
	result, err := j.d.Dispatch(jCtx, judgePrompt, dispatch.Options{
		TierHint: j.tier,
		System:   "You are a code and reasoning quality evaluator. Respond only with valid JSON.",
	})
	if err != nil {
		return nil, fmt.Errorf("judge dispatch: %w", err)
	}
	elapsed := time.Since(start)

	verdict, err := parseJudgeResponse(result.Output, len(attempts), successful)
	if err != nil {
		return nil, fmt.Errorf("judge parse: %w — raw: %.200s", err, result.Output)
	}

	verdict.Meta = JudgeMeta{
		Head:         result.Head,
		InputTokens:  result.InputTokens,
		OutputTokens: result.OutputTokens,
		Duration:     elapsed,
	}
	return verdict, nil
}

// buildJudgePrompt constructs the structured evaluation prompt.
func buildJudgePrompt(originalPrompt string, attempts []Attempt, successIdx []int) string {
	var sb strings.Builder
	sb.WriteString("You are evaluating responses to the following prompt:\n")
	sb.WriteString("---\n")
	sb.WriteString(originalPrompt)
	sb.WriteString("\n---\n\n")
	sb.WriteString("Below are the candidate responses, numbered from 0:\n\n")

	for _, idx := range successIdx {
		a := attempts[idx]
		fmt.Fprintf(&sb, "=== Response %d (model: %s) ===\n%s\n\n", idx, a.Head.Name, a.Output)
	}

	sb.WriteString(`Evaluate each response on: correctness, completeness, clarity, and conciseness.
Return ONLY valid JSON in this exact format (no prose, no code fences):
{"winner":<index>,"scores":[<score_0>,<score_1>,...],"reason":"<one sentence>"}

Rules:
- "winner" must be the index (0-based) of the best response
- "scores" must have one integer (0-100) for every response listed above, in the same order
- "reason" must be a single sentence explaining why the winner is best
- Indices refer to the numbers shown above (Response 0, Response 1, etc.)`)

	return sb.String()
}

// judgeJSON is the expected JSON shape from the LLM judge.
type judgeJSON struct {
	Winner int    `json:"winner"`
	Scores []int  `json:"scores"`
	Reason string `json:"reason"`
}

// parseJudgeResponse extracts the verdict JSON from potentially noisy LLM output.
func parseJudgeResponse(raw string, totalAttempts int, successIdx []int) (*JudgeVerdict, error) {
	// Strip markdown fences if present.
	clean := raw
	if i := strings.Index(clean, "{"); i >= 0 {
		clean = clean[i:]
	}
	if i := strings.LastIndex(clean, "}"); i >= 0 {
		clean = clean[:i+1]
	}

	var j judgeJSON
	if err := json.Unmarshal([]byte(clean), &j); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if len(j.Scores) != len(successIdx) {
		return nil, fmt.Errorf("scores length %d does not match response count %d", len(j.Scores), len(successIdx))
	}

	// Map scores back to full attempts slice (failed attempts get score 0).
	fullScores := make([]int, totalAttempts)
	for i, idx := range successIdx {
		fullScores[idx] = j.Scores[i]
	}

	// Validate winner index is in the successful set.
	isValid := false
	for _, idx := range successIdx {
		if j.Winner == idx {
			isValid = true
			break
		}
	}
	if !isValid {
		return nil, fmt.Errorf("winner index %d is not in the successful response set", j.Winner)
	}

	return &JudgeVerdict{
		WinnerIndex: j.Winner,
		Scores:      fullScores,
		Reason:      j.Reason,
	}, nil
}

// ── CapScoreJudge ─────────────────────────────────────────────────────────────

// CapScoreJudge is the deterministic fallback — no LLM call required.
// The winner is the successful attempt with the highest CapScore.
// Score = CapScore (0-100).
type CapScoreJudge struct{}

func (j *CapScoreJudge) Judge(_ context.Context, _ string, attempts []Attempt) (*JudgeVerdict, error) {
	successful := successfulAttempts(attempts)
	if len(successful) == 0 {
		return nil, fmt.Errorf("capscorer: no successful attempts")
	}

	sort.Slice(successful, func(i, k int) bool {
		return attempts[successful[i]].Head.CapScore > attempts[successful[k]].Head.CapScore
	})

	scores := make([]int, len(attempts))
	for _, idx := range successful {
		scores[idx] = attempts[idx].Head.CapScore
	}

	return &JudgeVerdict{
		WinnerIndex: successful[0],
		Scores:      scores,
		Reason:      fmt.Sprintf("ranked by capability score; %s scored highest (%d)", attempts[successful[0]].Head.Name, attempts[successful[0]].Head.CapScore),
		Meta:        JudgeMeta{UsedFallback: true},
	}, nil
}

// ── CompositeJudge ────────────────────────────────────────────────────────────

// CompositeJudge tries the primary judge, falls back to secondary on any error.
type CompositeJudge struct {
	primary  Judge
	fallback Judge
}

func newCompositeJudge(primary, fallback Judge) *CompositeJudge {
	return &CompositeJudge{primary: primary, fallback: fallback}
}

func (j *CompositeJudge) Judge(ctx context.Context, prompt string, attempts []Attempt) (*JudgeVerdict, error) {
	verdict, err := j.primary.Judge(ctx, prompt, attempts)
	if err == nil {
		return verdict, nil
	}

	verdict, fbErr := j.fallback.Judge(ctx, prompt, attempts)
	if fbErr != nil {
		return nil, fmt.Errorf("primary judge: %w; fallback judge: %v", err, fbErr)
	}

	verdict.Meta.UsedFallback = true
	verdict.Meta.FallbackReason = err.Error()
	return verdict, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// successfulAttempts returns the indices of attempts with StatusOK.
func successfulAttempts(attempts []Attempt) []int {
	var out []int
	for i, a := range attempts {
		if a.Status == StatusOK {
			out = append(out, i)
		}
	}
	return out
}
