# /rubber-duck, Cross-Model Reviewer Skill

Reviews output from one model family using a DIFFERENT model family.
This catches blind spots that same-family models share.

## When to Use
- After tiers 2-3 produce output → review with tier 4, from a different model family
- After tier 1 (the orchestrator) produces output → review with tier 4
- After tiers 7-10 produce output → review with tier 8, quick sanity only
- SKIP if claude_pct ≥ 75 (preserve tokens)

## Review Prompt Template
```
You are a reviewer with a fresh perspective. The following was produced by a different AI model.
Find issues the original model may have missed due to shared training biases.

Focus on:
1. Factual or logic errors, missed edge cases
2. Security or privacy problems
3. Anything that does not answer the task as asked
4. Better approaches the author didn't consider
5. Unnecessary complexity
6. Missing error handling at system boundaries (for code)

OUTPUT TO REVIEW:
<output from previous tier>

TASK THAT PRODUCED IT:
<original task>

RESPOND WITH:
- APPROVED: <one line summary>, if no significant issues
- ISSUES: <bullet list>, if problems found (include severity: low/medium/high)
- SUGGEST: <alternative approach>, if a fundamentally better approach exists
```

## Dispatch
```bash
hyctl dispatch --tier 4 "<review prompt above>"
```

## Acting on Results
- APPROVED → proceed
- ISSUES (low) → note for later, proceed
- ISSUES (medium) → fix before proceeding
- ISSUES (high) → escalate original task one tier up, redo
- SUGGEST → present tradeoff to user, let them decide
