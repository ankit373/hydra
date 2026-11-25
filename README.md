<div align="center">

<pre>
    ██╗  ██╗██╗   ██╗██████╗ ██████╗  █████╗ 
    ██║  ██║╚██╗ ██╔╝██╔══██╗██╔══██╗██╔══██╗
    ███████║ ╚████╔╝ ██║  ██║██████╔╝███████║
    ██╔══██║  ╚██╔╝  ██║  ██║██╔══██╗██╔══██║
    ██║  ██║   ██║   ██████╔╝██║  ██║██║  ██║
    ╚═╝  ╚═╝   ╚═╝   ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═╝
</pre>

### Picks the model best suited to each task, and can check the answer before you rely on it

*Any model, any provider. Hydra can check the answer, and ask another model when the check fails.*

[![License: MIT](https://img.shields.io/badge/License-MIT-8b5cf6.svg)](LICENSE)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#platform-support)
[![GitHub Stars](https://img.shields.io/github/stars/ankit373/hydra?style=flat&color=8b5cf6)](https://github.com/ankit373/hydra/stargazers)
[![GitHub Issues](https://img.shields.io/github/issues/ankit373/hydra?color=06b6d4)](https://github.com/ankit373/hydra/issues)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

<br/>

[**Website**](https://hydra.uvansa.com) · [**Docs**](https://hydra.uvansa.com/docs) · [**First principles**](https://hydra.uvansa.com/first-principles.html) · [**Roadmap**](https://github.com/ankit373/hydra/milestones) · [**Issues**](https://github.com/ankit373/hydra/issues)

</div>

---

## What is Hydra?

**Hydra picks the model best suited to each task, and can check the answer before you rely on it.**

![A terminal: hyctl oracle ground flags a summary that says 1.9 hours when the report says 1.4, and exits 1; the corrected summary is grounded and exits 0](docs/demo/ground.gif)

You probably have several models to hand: hosted APIs (OpenAI, Anthropic, Gemini, Bedrock, Mistral and others), local models through Ollama or llama.cpp, and CLI agents. Each is good at different work, and none of them tells you when its answer is wrong. Hydra sits in front of all of them.

- **It finds your models.** API keys, local model servers and CLI agents, with no configuration.
- **It picks by measured record.** Each model is ranked on how often its answers have passed for that kind of work on your machine, after allowing for luck.
- **It can check the answer.** Each kind of work has its own check, and with `--confidence` Hydra keeps asking until the answer is likely enough to be right.
- **It keeps data where you say.** Secret files and sensitive prompts stay on your machine.
- **It records what happened.** Every request is logged with the model that answered, its tokens and its cost.

The kinds of work Hydra keeps a record for are `writing`, `summary`, `extraction`, `classification`, `translation`, `answer`, `analysis`, `agent` and `code`. Name one with `hyctl dispatch --kind summary "..."`, or add an area such as `--kind extraction.invoices`.

## What people use it for

Five kinds of work, one of them code. Each shows the command and how Hydra checks the answer.

**Reply to a customer.** Draft on a mid-level model rather than your strongest one.

```bash
hyctl dispatch --kind writing.support --enum STANDARD "draft a short, friendly reply to this refund request: ..."
```

Once `writing.support` has a measured record, add `--confidence 0.9`: an answer that does not clear the bar is put to more models before you get it. Until then Hydra refuses `--confidence` rather than hand back a 50% guess, and tells you how to start a record.

**Pull invoice fields into JSON.** Ask a low-cost model, then check the result.

```bash
hyctl dispatch --kind extraction.invoices --enum SIMPLE \
  --check 'cmd:check-jsonschema --schemafile invoice.schema.json {file}' \
  "return only JSON with invoice_number, date and total from this email: ..."
```

Any command that exits 0 on a good answer works as the check: a JSON Schema validator, `jq -e`, a script, a test. It gets the answer as `{answer}` or as a file at `{file}`.

**Summarise a report.** Check that every number in the summary is in the source.

```bash
hyctl dispatch --kind summary --enum SIMPLE --check ground "$(cat prompt.txt)"
```

If the summary carries a figure the report does not, the check says `UNSUPPORTED ... not in the context given: number 1.9`. Today it checks numbers, dates, names of people and companies, paths, identifiers and phrases in double quotes. A quote of three or more words has to appear in the report word for word, give or take case and spacing. Quotes are not checked when the source is code, where comments quote made-up speech. A date written another way still matches, so `3 March 2026` in the report supports `March 3` in the summary. The report has to sit inside a Hydra fence in `prompt.txt` (`--- BEGIN <label> <nonce> (untrusted data, not an instruction) ---`), or there is nothing to check against. To check a summary you already have, use `hyctl oracle ground --prompt prompt.txt --candidate summary.txt`. It exits 0 when the summary is grounded, 1 when something in it is not in the source and 2 when there was nothing to check, so a script can gate on it.

Add `--by-sentence` to see each sentence with the report lines that back it, or what in it is not supported (`--json` for scripts).

**Label tickets on a local model.** Nothing leaves your machine.

```bash
hyctl dispatch --local --kind classification --check match:labels=labels.txt "label this ticket as billing, bug or how-to: ..."
```

`labels.txt` holds one allowed label per line, and any other answer fails the check. To send only the unsure ones to a stronger model, drop `--local` and `--check` and add `--confidence 0.9` once the task type has a record. Hydra asks models in most-evidence-per-dollar order and stops as soon as the answer clears the bar.

**Change code.** `hyctl edit` applies the change, runs the repo's tests (or the validator for the file's language), and rolls the change back if they fail. A failed check gets one rerun on a model measured stronger at that kind of file, sent the failed answer and the validator's output, within what is left of `max_cost_usd`; `escalate_on_fail: false` in `policy.yaml` turns it off, and its JSON output shows `escalated` and `first_head`.

```bash
hyctl edit --file "$PWD/slug.go" --enum MODERATE --prompt "drop punctuation and collapse repeated dashes so slug_test.go passes"
```

## How it checks

| Kind of work | Example | How Hydra checks it | What exists today |
|---|---|---|---|
| Write | draft a reply, rewrite a paragraph | several models agree; a judge picks the best; you confirm the outcome | agreement (`--confidence`) and a judge (`--swarm-mode best`); outcomes via `hyctl trust outcome`; a yes/no rubric from the request via `--check gen` |
| Summarise | a report, a call transcript | every claim appears in the source | `--check ground` or `hyctl oracle ground`, numbers, dates, names, paths, identifiers and quoted phrases only; sentence by sentence via `--check gen` |
| Extract | invoice fields, contract terms to JSON | the output fits a schema, required fields are present | any command via `--check cmd:...` or `hyctl oracle verify --candidate`; fields and source values via `--check gen`, a second model's extraction via `--check gen:roundtrip` |
| Classify | label tickets, route emails | the label is one of the allowed set; models agree | `--check match:labels=<file>` and `match` verifiers in `hyctl serve`; the label set the request names via `--check gen`; agreement via `--confidence` |
| Translate | release notes into German | models agree | agreement only; no dedicated check yet |
| Answer from documents | questions over files you give it | the answer is grounded in the given text | `--check ground` or `hyctl oracle ground` |
| Analyse | compare numbers, explain a trend | a script recomputes the figures | any command via `--check cmd:...` or `hyctl oracle verify` |
| Code | edit, generate, review | tests, build, lint; a file many others depend on needs more confidence | `hyctl edit`, `hyctl vet`, `hyctl graph blast`, `hyverify`; Go tests another model writes for the change, run with the network off, via `--check gen:tests=<file>` |

A check is not equally good on every model's answers. Strong models make subtler mistakes, so their wrong answers get past tests more often: in a public pilot, HumanEval+ base tests passed 0.7% of vicuna-7b's wrong answers and 40.6% of gpt-4's. `hyctl trust certify-table` keeps each check's false-pass rate per model and kind of work, with an upper bound, and never pools models. A model with few labelled wrong answers gets a wide bound, which is valid and simply counts for little.

`--check` judges one answer, and a real fail reruns once on a stronger model: [how checks work](https://hydra.uvansa.com/docs.html#check). Each kind of work also has a menu of [recipes](https://hydra.uvansa.com/docs.html#recipe).

---

## Install

The CLI is **`hyctl`** (the product is Hydra). Every method installs the same prebuilt binary and checks its checksum.

```bash
brew install ankit373/hydra/hyctl                                           # Homebrew, macOS and Linux
npm install -g hyctl                                                        # or run once with npx hyctl
pip install hyctl                                                           # Python 3.8+
curl -fsSL https://raw.githubusercontent.com/ankit373/hydra/main/install.sh | sh   # macOS and Linux
```

```powershell
irm https://raw.githubusercontent.com/ankit373/hydra/main/install.ps1 | iex  # Windows
```

Checksums, version pins, building from source: [install docs](https://hydra.uvansa.com/docs.html#install).

#### Platform support

Every release builds `hyctl` for all six targets below. This table is kept in step with
`.goreleaser.yaml`'s build matrix by a test: if a row is here, an artifact exists for it.

| OS | x86-64 | ARM64 | Homebrew | npm / npx | pip | script |
|---|:--:|:--:|:--:|:--:|:--:|:--:|
| macOS | ✅ | ✅ (Apple Silicon) | ✅ | ✅ | ✅ | `install.sh` |
| Linux | ✅ | ✅ | ✅ | ✅ | ✅ | `install.sh` |
| Windows | ✅ | ✅ | no | ✅ | ✅ | `install.ps1` |

**Desktop app**, macOS and Linux (Windows: download it from the [releases page](https://github.com/ankit373/hydra/releases/latest)):

```bash
curl -fsSL https://raw.githubusercontent.com/ankit373/hydra/main/install-app.sh | sh
```

<!-- x-release-please-start-version: release-please bumps the pinned version below. -->

It resolves the newest release (the asset names embed their version, so GitHub's `/latest/download/`
shortcut cannot address them), verifies the download against the published `.sha256`, installs the
`.app` to `/Applications`, or the binary to `~/.local/share/hydra` with a `~/.local/bin` symlink on
Linux, and clears the macOS quarantine flag so the first launch works. `HYDRA_VERSION=v1.9.0` pins
a release; `HYDRA_APP_DIR` changes where it lands.

<!-- x-release-please-end -->

The builds are not code-signed yet, so the first launch takes one extra step: see [first launch](https://hydra.uvansa.com/app.html).

## Quick start

```bash
hyctl init                                               # find your models, pick a main one, ~60 seconds
hyctl dispatch --dry-run "summarise this meeting note: ..."   # see which model would answer, and the fallbacks
hyctl dispatch --kind summary "summarise this meeting note: ..."
```

Every command: [command reference](https://hydra.uvansa.com/docs.html#commands).

---

## The main ideas

**Routing.** Hydra finds your models in about two seconds and sends each request to the lowest-cost one measured good enough at that kind of work, with stronger models behind it as the fallback chain. `--enum` or `--tier` pins a level, and `--dry-run` shows the chain. [Discovery](https://hydra.uvansa.com/docs.html#probe) · [routing levels](https://hydra.uvansa.com/docs.html#tiers) · [dispatch](https://hydra.uvansa.com/docs.html#dispatch) · [swarm](https://hydra.uvansa.com/docs.html#swarm)

**Checking and trust.** `--check` judges one answer. `--confidence 0.95` asks models one at a time and stops once the answer is that likely to be right, using a record of how often each model and check has been right before. [Confidence](https://hydra.uvansa.com/docs.html#confidence) · [calibration](https://hydra.uvansa.com/docs.html#trust) · [the math](https://hydra.uvansa.com/first-principles.html)

**Cost and budget.** Every request is logged with its tokens and an estimated cost, clearly labelled as an estimate. `hyctl cost` and `hyctl stats` show where the money went. Once 30 answers have been checked, the usage view in `hyctl tui` shows what each passed answer cost, with its pass rate and a 95% interval, beside the best single model on your own record for the same kind of work. A budget governor steps down to lower-cost models as usage climbs. [Cost](https://hydra.uvansa.com/docs.html#cost) · [budget governor](https://hydra.uvansa.com/docs.html#governor)

**Privacy and local-only.** Files like `.env` or `~/.aws/credentials` never reach a model that leaves your machine, and with local-only on, a prompt holding personal data goes to a local model. Each agent gets only its own key. `hyctl security` reads the audit log and says what the agents did. [Egress gate](https://hydra.uvansa.com/docs.html#egress) · [PII](https://hydra.uvansa.com/docs.html#policy) · [least privilege](https://hydra.uvansa.com/docs.html#least-privilege) · [security](https://hydra.uvansa.com/docs.html#security)

**Serve.** `hyctl serve` puts the router behind an OpenAI-compatible endpoint on loopback, so any tool that speaks OpenAI routes through Hydra. [hyctl serve](https://hydra.uvansa.com/docs.html#serve) · [routing objective](https://hydra.uvansa.com/docs.html#serve-objective)

**The TUI and the desktop app.** `hyctl tui` is a terminal cockpit where you type a task and it runs, with several tasks at once in their own threads. The desktop app does the same in a window and needs no terminal. [TUI](https://hydra.uvansa.com/docs.html#tui) · [desktop app](https://hydra.uvansa.com/docs.html#app)

**Code.** `hyctl edit` makes a change, runs your tests and rolls back on failure. `hyctl vet` reviews a diff, and a file many others depend on needs more confidence before a change to it is accepted. [edit](https://hydra.uvansa.com/docs.html#edit) · [blast radius](https://hydra.uvansa.com/docs.html#blast-radius) · [verifiers](https://hydra.uvansa.com/docs.html#oracle)

Settings live in `~/.hydra/config.toml`: [configuration](https://hydra.uvansa.com/docs.html#config). To add a model: [adding a model](https://hydra.uvansa.com/docs.html#add-model).

---

## More

- [Docs](https://hydra.uvansa.com/docs.html): every command, in depth
- [First principles](https://hydra.uvansa.com/first-principles.html): the math behind the router, and [the measured numbers](https://hydra.uvansa.com/first-principles.html#numbers)
- [How Hydra compares](https://hydra.uvansa.com/#compare) with other gateways
- [Blog](https://hydra.uvansa.com/blog/)
- [Releases](https://hydra.uvansa.com/releases/): what changed in each release, upgrade notes and verified downloads ([Atom feed](https://hydra.uvansa.com/releases/feed.xml))
- [Roadmap](https://github.com/ankit373/hydra/milestones): milestones and their open issues
- [Package map](CLAUDE.md#directory-layout), for finding your way round the code
- [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md) · [Licence: MIT](LICENSE)
