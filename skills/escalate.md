# /escalate, Escalation & Fallback Skill

Handles cases where a delegated output fails quality checks or a model returns ESCALATE.

## Triggers
- Model output contains `ESCALATE:` prefix
- Output fails its check (syntax, schema, label set, a `--check` verdict)
- Output is empty or truncated
- Rubber duck review returns ISSUES (high)
- Pool exhaustion (`hyctl dispatch` falls back automatically)

## Escalation Protocol

1. Read the failure reason
2. Determine if it's a capability issue (→ go higher tier) or a context issue (→ add more context, retry same tier)
3. For capability: `hyctl dispatch --tier <failed_tier - 1> --system "<richer context>" "<same prompt>"`
4. For context: add missing material (files, glossary, examples) to the context block, retry same tier
5. Log: record what tier failed, what the reason was, what tier succeeded

## When NOT to Escalate
- Output has minor style differences → apply and move on
- Output is missing an import or a closing line → fix it yourself (tier 1 job)
- Output uses slightly different naming or wording → change it yourself

## Hard Stop
If tier 2 also fails → bring it back to the orchestrator (tier 1).
State what both models tried and why they failed.
The orchestrator makes the final call on approach.
