# Security Policy

## Supported Versions

| Version | Supported |
| ------- | --------- |
| `main`  | Yes       |

Hydra follows a rolling-release model on `main`. Only the latest commit receives security fixes.

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report it privately through GitHub: [open a draft security advisory](https://github.com/ankit373/hydra/security/advisories/new).
Only you and the maintainer can see it. You need a GitHub account. There is no security email address.

Include:
- A description of the vulnerability and its potential impact
- Steps to reproduce (proof-of-concept or exploit code if applicable)
- Any suggested mitigations you have in mind

You will receive a response within **48 hours** acknowledging receipt.  
We aim to release a patch within **14 days** of confirming a valid report.

We will credit reporters in the release notes unless you prefer to remain anonymous.

## Threat Model

Hydra is a **local CLI tool**, it runs on your machine, reads your filesystem, and calls external AI APIs on your behalf. Keep this in mind:

- **API keys**: Hydra reads credentials from environment variables or tool-managed config files (`~/.gemini/antigravity-cli/settings.json`). **Never commit API keys to any repository.**
- **Prompt injection**: Prompts sent to models may include untrusted content, such as file contents. Hydra can write a model's edits to files in the workspace and run the configured validators on the result. Review what a model changed before you rely on it.
- **Local file access**: `internal/workspace` checks every path Hydra edits and refuses one outside its workspace. Workspaces are listed in `registry/workspace.yaml`. Do not point `HYDRA_HOME` at a sensitive system directory.
- **Logs**: `$HYDRA_HOME/logs` (default `~/.hydra/logs`) holds run logs and model outputs. They stay on your machine but may contain sensitive context, so treat them accordingly.

## Security Best Practices for Contributors

- Never log, print, or commit API keys, tokens, passwords, or personal data.
- Resolve Hydra state paths through `config.Dir()` (`$HYDRA_HOME`, default `~/.hydra`), never hardcode `/Users/<name>/`.
- Validate all external inputs before passing them to shell commands. Prefer `printf '%s'` quoting over bare variable interpolation.
- Run `shellcheck` on any new or modified `.sh` file before submitting a PR.
