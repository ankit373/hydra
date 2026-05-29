---
name: Bug report
about: Something is broken or behaving unexpectedly
title: "[bug] "
labels: bug
assignees: ''
---

## What happened?

<!-- A clear description of the bug. -->

## Steps to reproduce

1. 
2. 
3. 

## Expected behavior

<!-- What should have happened? -->

## Actual behavior

<!-- What actually happened? Include any error output. -->

## Environment

- OS: <!-- e.g. macOS 14.5 -->
- Shell: <!-- e.g. zsh 5.9 -->
- Install method: <!-- Homebrew / install.sh / manual -->
- `jq --version`:
- `yq --version`:
- `bun --version`:
- Hydra version / commit: <!-- run: git -C $(which hydra | xargs readlink -f | xargs dirname)/.. rev-parse --short HEAD -->

## Relevant logs

<!-- Paste relevant lines from ~/.hydra/logs/dispatch.log — redact any sensitive prompts or API responses. -->

```
paste logs here
```

## Additional context

<!-- Anything else that might help — screenshots, config snippets, etc. -->
