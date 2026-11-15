# bench

The benchmark harness (RFC 0001, E7). Run every command from the repository root, since the
default paths below are repo-relative. Datasets and their splits are described in
[DATASETS.md](DATASETS.md).

```bash
hyctl bench fetch                                  # pinned data into $HYDRA_HOME/bench/data, sha256-checked
hyctl bench run --config bench/configs/smoke.toml --dry-run   # worst-case cost, no call
hyctl bench run --config bench/configs/smoke.toml             # resumable; the same command continues
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
- False accept per task is wrong accepted answers over all test tasks, with a two-sided 95%
  Clopper-Pearson interval. The table shows the mean over splits, the first seed's interval,
  the worst split and the share of splits over alpha, then refusal, cost per task and cost per
  correct accepted answer (`n/a` when none was correct). `--json` adds every split.
