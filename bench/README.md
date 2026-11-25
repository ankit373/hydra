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
- `grader_model = "<provider>:<model id>"` in a config adds a second model to the grade, for
  a suite that can use one (snips). It re-extracts each answer's fields from the utterance, and
  a field it reads differently is flagged, on top of the fields check. The table then adds, under
  each field row, a `<arm> + second model` row with the catch rate and the cost per verified
  field with it. The first row's cost leaves the second model's calls out, so the two compare.
  It is unset by default, so no run pays for one it did not name. `ollama:<model>` uses the
  local Ollama server (`$OLLAMA_HOST`) and costs nothing, for example `grader_model =
  "ollama:qwen2.5:7b"` (the model has to be pulled first). A hosted one is `openrouter:` or
  `bedrock:`, and needs a `[cutoffs]` entry and a price like any arm. Its calls count against
  `--max-usd` and show in `--dry-run`. A second model that does not answer leaves that row
  unscored, and the next `run` asks it again, as for an arm.
- `smoke.toml` refuses to load until the Opus and Haiku Bedrock ids are pinned (Q4).

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
- `aver_product_perhead` and `aver_product` are certified acceptance (#1549): each verdict is an
  e-value from a per-check bound on false passes (`hyctl trust certify-table`), and an answer is
  accepted once their product reaches its candidate's bar, 1/(alpha w_j) for the j-th answer bought.
  The weights are fixed from the head count before any answer is seen: `spend` (the default) gives
  1/2, 1/4, ... and the last the rest; `bonferroni` gives 1/N and is refused past 3 heads. Name the
  rule as a suffix, `--policy aver_product_perhead:bonferroni`; the table prints each policy's bars.
  `aver_product_perhead` fits a bound per head, `aver_product` one pooled over heads. Checks run strongest evidence per
  dollar first, a head that cannot reach the bar is not bought, and an answer stops as soon as the
  bar is out of reach. Bounds hold with chance 1 - `--delta` (default 0.05).
- `aver_gated` is `aver_product_perhead` that first tests each pair of checks on the wrong answers
  both ran on (`aver.Gate`): a pair that passes together more often than q-bar_a x q-bar_b allows,
  or has under 20 shared wrong answers, is linked, and linked checks count once, through the one
  with the best e on a pass. Compare it with the plain product at a small alpha.
- `aver_pattern` certifies under any dependence: per head, it bounds how often a wrong answer
  matches a pattern of up to 3 features (checks, plus at most one "at least m earlier answers
  agree"), with delta split over every pattern and head, and accepts an answer matching the pattern
  chosen on calibration. It buys heads in order without skipping, so agreement means the same on
  both halves. Without enough wrong answers it refuses and says how many it would need, about
  ln(patterns/delta) x bar.
- In the AVER policies a check missing from `verdicts` did not run, unlike the older policies where
  it did not pass: calibration only sees verdicts that exist, so a missing one has no calibrated e.
- False accept per task is wrong accepted answers over all test tasks, with a two-sided 95%
  Clopper-Pearson interval. The table shows the mean over splits, the first seed's interval,
  the worst split and the share of splits over alpha, then refusal, cost per task and cost per
  correct accepted answer (`n/a` when none was correct). `--json` adds every split.

The pilot's public answers (HumanEval+, MBPP+, GSM8K, RAGTruth) are rebuilt into these rows by the
scripts in `bench/aver/`; see its README for sources, licences and prices.
