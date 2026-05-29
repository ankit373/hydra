## Summary

<!-- What does this PR do? One paragraph max. -->

## Related issue

Closes #<!-- issue number -->

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Refactor (no behaviour change)
- [ ] Documentation
- [ ] CI / tooling

## Checklist

- [ ] `shellcheck dispatch/*.sh` passes with zero errors
- [ ] `cd ui && bun run typecheck` passes (if UI files changed)
- [ ] No API keys, tokens, personal paths, or other secrets added
- [ ] `logs/` and `*.local` files are excluded via `.gitignore`
- [ ] `registry/` YAML validated with `yq '.' <file> > /dev/null`
- [ ] `CHANGELOG.md` updated under `[Unreleased]`

## Testing done

<!-- Describe how you tested this. Commands run, output observed. -->

## Screenshots / output (if applicable)

<!-- Paste terminal output or attach screenshots. -->
