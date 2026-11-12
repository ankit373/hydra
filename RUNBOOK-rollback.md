# Rolling back a release

Use this when a published release is broken: it will not install, it crashes on start, it loses data or it has a security hole.

A rollback takes the bad version off the defaults. It does not delete anything. npm and PyPI never let a version number be used twice ([npm unpublish policy](https://docs.npmjs.com/policies/unpublish), [PyPI file name reuse](https://pypi.org/help/#file-name-reuse)), so the real fix is always a new patch release.

## 1. Hold the next release

If the release smoke test caught it, this is already done: there is an open issue labelled `release:blocked` and the open release PR has the same label.

If you found it yourself, open an issue that says what is broken, label it `release:blocked`, and add `release:blocked` to the open release PR (`autorelease: pending`). Mergify will not queue a release PR with that label.

## 2. Dry run

Actions, "Roll back a release", Run workflow:

- `tag`: the bad tag, for example `v1.7.0`
- `reason`: one line, shown to anyone who installs the bad version from npm
- `dry_run`: leave it ticked

Or from a terminal:

```sh
gh workflow run rollback.yml -f tag=v1.7.0 -f reason="crashes on start"
```

The run summary names the previous stable release it will fall back to and every command it would run. Check that the fallback is the version you expect. It is the highest stable tag below the bad one.

## 3. Roll back

Run it again with `dry_run` unticked (`-f dry_run=false`). Each channel is its own job, so one failing does not stop the others.

| Channel | What the workflow does | What users see |
|---|---|---|
| GitHub release | marks the previous release as latest, then marks the bad one as a prerelease ([gh release edit](https://cli.github.com/manual/gh_release_edit)) | `install.sh` with no `HYDRA_VERSION` installs the previous release. A pinned `HYDRA_VERSION` still gets what it asks for |
| npm | `npm deprecate hyctl@BAD`, and moves the `latest` dist-tag back ([npm deprecate](https://docs.npmjs.com/cli/v11/commands/npm-deprecate), [npm dist-tag](https://docs.npmjs.com/cli/v11/commands/npm-dist-tag)) | `npm i -g hyctl` installs the previous version. Installing the bad one prints the reason |
| Homebrew | regenerates the tap formula for the previous release with `scripts/update-tap-formula.sh` | new installs get the previous release. Brew does not downgrade people who already upgraded; they run `brew reinstall hyctl` |
| PyPI | nothing, see step 4 | |

The GitHub, npm and Homebrew jobs only move "latest" when the bad version is the current latest. Rolling back an older patch release marks it as bad without pulling everything else back to its line.

The npm job needs the `NPM_TOKEN` secret. Trusted publishing only covers `npm publish` and dist-tags from publish.yml ([npm trusted publishers](https://docs.npmjs.com/trusted-publishers)). If the token is missing or expired, the job prints the two commands; run them yourself after `npm login`.

## 4. Yank on PyPI by hand

PyPI documents yanking only in its web UI, and twine has no yank command, so a person does this.

1. Open https://pypi.org/manage/project/hyctl/releases/.
2. Options next to the bad version, then Yank. Give the same reason.

Installers then skip the yanked version unless it is the only match for a `==` pin ([PyPI yanking](https://docs.pypi.org/project-management/yanking/), [PEP 592](https://peps.python.org/pep-0592/)).

## 5. Ship the fix

1. Land the fix on main as `fix(...)`. The release PR becomes a patch.
2. Remove `release:blocked` from the release PR and approve it.
3. Wait for the release smoke test on the new version to pass.
4. Close the `release:blocked` issue.

Do not "un-roll back" the bad version afterwards. Once a newer release is out, it is latest everywhere and the bad one stays deprecated and yanked.
