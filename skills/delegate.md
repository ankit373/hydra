# /delegate — Master Routing Skill

Invoked when Claude Code needs to delegate a task to the right model tier.

## Steps

1. **Identify domain** from the task description. Cross-reference `registry/domains.yaml` aliases.
2. **Identify task type** within that domain. Match against `task_routing` keys.
3. **Resolve enum key** → open `registry/routing.yaml`, read `routing_map.<KEY>` for tier number.
4. **Check system state**: run `dispatch/route.sh --status`. Apply preservation rules if needed.
5. **Build context**: collect relevant files, types, conventions into a context block or file.
6. **Dispatch**: `dispatch/route.sh --enum <KEY> --prompt "<task>" [--context <file>]`
7. **Review output**: validate correctness. Escalate if quality fails.
8. **Apply**: write output to disk. Log what tier handled it.

## Decision Shortcuts

| Task contains | Domain | Likely enum |
|--------------|--------|-------------|
| "DTO", "schema", "interface", "type" | backend/frontend | SIMPLE |
| "controller", "handler", "endpoint", "route" | backend | STANDARD |
| "service", "repository", "use case" | backend | MODERATE |
| "middleware", "guard", "auth", "interceptor" | backend | COMPLEX |
| "component" (simple) | frontend | SIMPLE |
| "component" (complex, stateful) | frontend | MODERATE |
| "test", "spec", "unit test" | qa_testing | SIMPLE |
| "e2e", "integration test" | qa_testing | MODERATE |
| "Dockerfile", "CI", "pipeline" | devops_sre | STANDARD |
| "k8s", "kubernetes", "helm" | devops_sre | MODERATE |
| "architecture", "design", "ADR" | any | EXPERT |
| "boilerplate", "scaffold", "stub", "empty" | any | GRUNT |
| "strategy", "roadmap", "vision" | product/cxo | EXPERT |
| "core business logic", "critical path" | any | CORE |

## Context Injection Template
```
CONVENTIONS:
<paste relevant types, base classes, naming patterns>

FILES IN SCOPE:
<list of @file references or inline snippets>

TASK:
<specific task — be precise>

OUTPUT FORMAT:
TypeScript only. No markdown fences. Match existing style.
```
