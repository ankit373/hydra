# /escalate — Escalation & Fallback Skill

Handles cases where a delegated output fails quality checks or a model returns ESCALATE.

## Triggers
- Model output contains `ESCALATE:` prefix
- Output fails syntax/type check
- Output is empty or truncated
- Rubber duck review returns ISSUES (high)
- Pool exhaustion (route.sh handles automatically)

## Escalation Protocol

1. Read the failure reason
2. Determine if it's a capability issue (→ go higher tier) or a context issue (→ add more context, retry same tier)
3. For capability: `dispatch/route.sh --tier $((failed_tier - 1)) --prompt "<same prompt>" --context <richer_context>`
4. For context: add missing files/types to the context block, retry same tier
5. Log: record what tier failed, what the reason was, what tier succeeded

## When NOT to Escalate
- Output has minor style differences → apply and move on
- Output is missing imports → add them yourself (tier 1 job)
- Output uses slightly different naming → rename yourself

## Hard Stop
If tier 2 (Opus Thinking) also fails → bring back to Claude Code (tier 1).
State what both models tried and why they failed.
Claude Code makes the final call on approach.
