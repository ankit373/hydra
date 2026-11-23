# bench

The benchmark harness (RFC 0001, E7). Run every command from the repository root, since the
default paths below are repo-relative. Datasets and their splits are described in
[DATASETS.md](DATASETS.md).

```bash
hyctl bench fetch                                  # pinned data into $HYDRA_HOME/bench/data, sha256-checked
hyctl bench run --config bench/configs/smoke.toml --dry-run   # worst-case cost, no call
hyctl bench run --config bench/configs/smoke.toml             # resumable; the same command continues
hyctl bench run --config bench/configs/faith.toml             # claims check recall and false flags, no model
hyctl bench report <run-id>                        # recompute summary.json, print the table
hyctl bench table <run-id>                         # write bench/results/<run-id>/table.md
```

- `run` writes `results/<run-id>/run.json` before the first call and one row per
  (item, arm) to `items.jsonl`. The run id hashes the config, the item ids and the data, but
  not prices or the cap. Prices are frozen into `run.json` on the first start, so a resumed
  run is priced exactly as it began.
- `--max-usd` is checked before every call. Every call that may have been billed is
  appended to `items.jsonl` as its own spend row the moment it returns, so even a hard
  kill cannot make a resume spend the cap twice. Result rows list their calls but carry
  no money. Exit 2 means the cap stopped the run; exit 4 means the provider broke the
  contract mid-run.
- A 429, 5xx or network error is retried with backoff. If it still fails, the pair is
  recorded as unscored, and the next `run` asks it again. Any other error fails the pair
  and is not retried. It is charged its ceiling, since the harness cannot tell a request
  that never left the machine from a billed answer that failed to decode. A run with any
  pair still unscored is reported as INCOMPLETE, with counts but no paired statistics.
- `--rerun` resumes the latest `-r<k>` while it is incomplete, and starts the next one
  only once it is done.
- A grade that ran out of time is a fail, marked `timeout` on its row and counted as
  `timeouts` per arm in `summary.json`, so a loaded machine shows up rather than reading
  as worse answers.
- A config's `checks` names arms the suite answers itself with a check of its own, calling no
  model and spending nothing. On a check suite (FaithBench) every row records whether the gold
  had something to catch and whether the arm flagged it, and `run` and `report` print recall
  and the false-flag rate per arm, each with a 95% Wilson interval.
- `faith.toml` runs the claims check's `ground` variant on FaithBench. A `judge` variant is a
  normal model arm on the same suite: add a pinned model to it, as the file's comment says.
- An extraction suite (snips) also runs the fields check on each answer, holding every
  value to the utterance, and counts each field right or wrong against the gold. The table
  then adds a second table per arm: field accuracy, the share of wrong fields the check
  flagged (95% Wilson interval), the share of right fields it flagged, and the cost per
  field it did not flag. Each arm gets a row for all fields and one per kind of gold value:
  number, code (one token with a digit, or in capitals, like `INV-2041`) and text, since a
  check can catch wrong amounts and still miss a misread code. An answer that is not JSON
  is a format fail and adds no fields.
- A model is `bedrock:<id>`, `openrouter:<id>` or `ollama:<name>`. An `ollama:` model runs on
  the Ollama server at `OLLAMA_HOST` (default `localhost:11434`) with no key and with thinking
  off (`reasoning_effort: none`), since a suite allows only a few answer tokens. Its price row is
  `local` at $0, so it needs no `[prices]` entry, and the detection table prints it as
  `<name> (local)`. One id cannot be both local and hosted in one config, since prices are
  keyed by id. A local number is a number for that local model, not for a hosted one.
- `smoke.toml` refuses to load until the Opus and Haiku Bedrock ids are pinned (Q4).

## redaction

`hyctl bench redaction` measures what `--redact` costs in answer quality. It asks one model
each task in `redaction/tasks.jsonl` twice at temperature 0: as is, and with every value
redaction replaces turned into a placeholder and the answer restored, the way dispatch does
for a hosted model. Both answers get the same checks: the task's `match` pattern and, for
summaries, `ground` against the source.

```bash
hyctl bench redaction --model ollama:qwen3:8b                    # local, $0
hyctl bench redaction --model openrouter:<id> --max-usd 0.50      # hosted, worst case printed first
```

- The 30 tasks are made up (support, HR, finance). Each plants emails, phones, IBANs, card
  numbers, SSNs or IPs, and says what the answer needs: `copy` repeats a value, `read` needs
  a fact inside one (a domain, the last four digits, a country), `around` needs none of them.
- The table gives the pass rate off and on per kind of work and per need, each with a 95%
  Wilson interval, the pairs only one side passed, and the exact McNemar p on those pairs.
- `--out` writes every task's answers and grades as JSONL.

## replay

`hyctl bench replay` judges acceptance policies on answers you already have, so many
calibrate/test splits cost nothing. Each seed (0 to `--splits` - 1) splits the tasks 50/50,
calibrates every policy on one half and runs it on the other. Each kind is replayed on its own.

```bash
hyctl bench replay --candidates answers.jsonl --check tests
hyctl bench replay --candidates answers.jsonl --policy always_strongest --alpha 0.05 --splits 20 --json
```

One JSON line per (task, head) candidate:

```json
{"task": "gsm8k/0412", "kind": "math", "head": "qwen-72b", "cost_usd": 0.0009, "correct": false,
 "answer_key": "42", "verdicts": {"format": true, "arith": false}, "check_cost_usd": {"arith": 0}}
```

- `correct` is the label. `answer_key` lets an agreement check compare two answers without their text.
- A check missing from `verdicts` did not pass. A repeated (task, head) or a task under two kinds is refused.
- Heads are tried cheapest first, by mean cost on the calibration half.
- Policies: `always_strongest` (the head most often correct on calibration, accepted unchecked)
  and `first_check_pass` (the cheapest answer whose `--check` passes).
- False accept per task is wrong accepted answers over all test tasks, with a two-sided 95%
  Clopper-Pearson interval. The table shows the mean over splits, the first seed's interval,
  the worst split and the share of splits over alpha, then refusal, cost per task and cost per
  correct accepted answer (`n/a` when none was correct). `--json` adds every split.
