---
name: MCP registry score dispute
about: Contest or report a problem with a server's score in the MCP trust registry
title: "[mcp-registry] "
labels: mcp-registry-dispute
assignees: ''
---

## What this is for

`hyctl mcp registry` computes a probabilistic trust signal from automated checks (known-vulnerability
cross-reference, maintenance recency, near-duplicate/typosquat detection, declared auth posture) — it
is **not a guarantee of safety** and it is **not a claim about a publisher's intent**. Every signal
behind a score is shown alongside it precisely so a wrong or unfair call can be identified and
corrected. This template is that correction path.

## Server in question

- Registry name (reverse-DNS, e.g. `io.github.foo/bar`): 
- Package identifier / ecosystem: 
- Current lifecycle state (new / provisional / trusted / flagged / quarantined / delisted): 

## What's wrong

<!-- Which signal(s) do you believe are incorrect, stale, or unfair? Quote the exact Detail text
     shown for the signal if you have it (from `hyctl mcp registry audit` / `list` / the exported
     directory page). -->

## Why

<!-- The evidence for your side — e.g. the CVE was fixed in a later version now published, the
     "near-duplicate" match is a legitimate fork with its own distinct maintainers, the repository
     moved and the new URL is actively maintained, etc. -->

## What you'd like to happen

<!-- E.g.: re-run the score, manually clear a quarantine flag, correct a signal's underlying data
     source. -->
