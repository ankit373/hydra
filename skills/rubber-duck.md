# /rubber-duck — Cross-Model Reviewer Skill

Reviews output from one model family using a DIFFERENT model family.
This catches blind spots that same-family models share.

## When to Use
- After tiers 2-3 (agy Claude) produce output → review with tier 4 (GPT-OSS)
- After tier 1 (Claude Code) produces output → review with tier 4 (GPT-OSS)
- After tiers 7-10 produce output → review with tier 8 (Flash Med) — quick sanity only
- SKIP if claude_pct ≥ 75 (preserve tokens)

## Review Prompt Template
```
You are a code reviewer with a fresh perspective. The following was produced by a different AI model.
Find issues the original model may have missed due to shared training biases.

Focus on:
1. Logic errors or edge cases
2. Security vulnerabilities
3. Performance problems
4. Better approaches the author didn't consider
5. Unnecessary complexity
6. Missing error handling at system boundaries

CODE/OUTPUT TO REVIEW:
<output from previous tier>

TASK THAT PRODUCED IT:
<original task>

RESPOND WITH:
- APPROVED: <one line summary> — if no significant issues
- ISSUES: <bullet list> — if problems found (include severity: low/medium/high)
- SUGGEST: <alternative approach> — if a fundamentally better approach exists
```

## Dispatch
```bash
dispatch/route.sh --tier 4 --prompt "<review prompt above>"
```

## Acting on Results
- APPROVED → proceed
- ISSUES (low) → note for later, proceed
- ISSUES (medium) → fix before proceeding
- ISSUES (high) → escalate original task one tier up, redo
- SUGGEST → present tradeoff to user, let them decide
