## Summary

<!-- What does this PR do? One paragraph max. -->

## Related issue

Closes #<!-- issue number -->

<!--
The keyword does not fire on a develop-targeted PR: GitHub only honours it into
the default branch. close-shipped-issues.yml reads it back out of this body at
release time, so a PR with no keyword is an issue that never closes.
-->

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Refactor (no behaviour change)
- [ ] Documentation
- [ ] CI / tooling

## Checklist

- [ ] `gofmt -l ./cmd ./internal` prints nothing
- [ ] `go vet ./...` and `staticcheck ./...` are clean
- [ ] `go test ./... -race -count=1` passes
- [ ] Desktop module, if touched: `go -C desktop test ./api/... -race`
- [ ] Frontend, if touched: `npm --prefix desktop/frontend run typecheck && npm --prefix desktop/frontend test`
- [ ] `registry/` YAML validated, if touched: `yq '.' <file> > /dev/null`
- [ ] Docs shipped with the change, not left for later: README, `docs/llms.txt`, `CLAUDE.md`
- [ ] No API keys, tokens, personal paths, or other secrets added
- [ ] I have signed the [CLA](../CLA.md) and am listed in `CONTRIBUTORS.md`

## If this PR adds a guard

<!--
A test that cannot fail is not a test. If you added one to prevent a class of
bug, say how you made it go red: what you broke, and what it printed. A guard
nobody has seen fail is a guard nobody has checked.
-->

## Testing done

<!-- Commands run, output observed. Paste the output rather than describing it. -->

## Screenshots / output (if applicable)
