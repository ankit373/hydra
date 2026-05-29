# Security Policy

## Supported Versions

| Version | Supported |
| ------- | --------- |
| `main`  | Yes       |

Hydra follows a rolling-release model on `main`. Only the latest commit receives security fixes.

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Please report security issues by emailing **security@hydra-ai.dev** (or open a [GitHub private security advisory](https://github.com/ankit373/hydra/security/advisories/new)).

Include:
- A description of the vulnerability and its potential impact
- Steps to reproduce (proof-of-concept or exploit code if applicable)
- Any suggested mitigations you have in mind

You will receive a response within **48 hours** acknowledging receipt.  
We aim to release a patch within **14 days** of confirming a valid report.

We will credit reporters in the release notes unless you prefer to remain anonymous.

## Threat Model

Hydra is a **local CLI tool** — it runs on your machine, reads your filesystem, and calls external AI APIs on your behalf. Keep this in mind:

- **API keys**: Hydra reads credentials from environment variables or tool-managed config files (`~/.gemini/antigravity-cli/settings.json`). **Never commit API keys to any repository.**
- **Prompt injection**: Prompts dispatched to lower-tier models may include untrusted content (e.g., file contents). Outputs are treated as text, not executable, but always review AI-generated code before applying it.
- **Local file access**: `dispatch/scope.sh` enforces workspace boundaries. Do not override `HYDRA_HOME` or `HYDRA_DATA` to point at sensitive system directories.
- **Logs**: `logs/` contains task histories and model outputs. These are excluded from the repository via `.gitignore` but may contain sensitive context — treat them accordingly.

## Security Best Practices for Contributors

- Never log, print, or commit API keys, tokens, passwords, or personal data.
- Use `$HOME` and `$HYDRA_DATA` for all user-owned paths — never hardcode `/Users/<name>/`.
- Validate all external inputs before passing them to shell commands. Prefer `printf '%s'` quoting over bare variable interpolation.
- Run `shellcheck` on any new or modified `.sh` file before submitting a PR.
