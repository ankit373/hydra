# /delegate, Master Routing Skill

Invoked when the orchestrator needs to hand a task to the right routing level.

## Steps

1. **Identify domain** from the task description. Cross-reference `registry/domains.yaml` aliases.
2. **Identify task type** within that domain. Match against `task_routing` keys.
3. **Resolve enum key**: `registry/routing.yaml` maps `routing_map.<KEY>` to a routing level.
4. **Check system state**: run `hyctl status`. Apply preservation rules if needed.
5. **Build context**: collect the files, examples, glossary or conventions the task needs.
6. **Dispatch**: `hyctl dispatch --enum <KEY> [--kind <kind>] [--system "<context>"] "<task>"`.
   Add `--dry-run` first to see which model it picks and the fallback chain.
7. **Review output**: check it does what was asked. Escalate if quality fails.
8. **Apply**: use or write the output. Note which routing level handled it.

## Decision Shortcuts

| Task contains | Domain | Likely enum |
|--------------|--------|-------------|
| "tag", "dedupe", "reformat", "fill the template" | any | GRUNT |
| "FAQ", "how do I", "reset my password" | support | SIMPLE |
| "refund", "billing dispute", "complaint" | support | STANDARD |
| "summarise", "abstract", "TL;DR" | research | SIMPLE |
| "extract", "pull the fields", "invoice total" | finance_ops / legal_contracts | GRUNT |
| "label", "classify", "qualify this lead" | sales / support | SIMPLE |
| "translate", "localise" (UI strings) | translation | SIMPLE |
| "translate" (contract, terms) | translation | EXPERT |
| "what is the notice period", clause question | legal_contracts | SIMPLE |
| "review this contract", "risks in this agreement" | legal_contracts | EXPERT |
| "blog post", "outreach email", "job post" | marketing_content / sales / hr | STANDARD |
| "unit test", "DTO", "schema" | qa_testing / backend | SIMPLE |
| "endpoint", "handler", "Dockerfile" | backend / devops_sre | STANDARD |
| "auth middleware", "e2e suite" | backend / qa_testing | COMPLEX |
| "architecture", "strategy", "positioning" | any | EXPERT |
| "the one call you cannot get wrong" | any | CORE |

## Context Injection Template
```
CONVENTIONS:
<style guide, glossary, tone, types or naming patterns that apply>

MATERIAL IN SCOPE:
<the ticket, document, data or files the task is about>

TASK:
<specific task, be precise>

OUTPUT FORMAT:
Match the format the task asks for. No preamble.
```
