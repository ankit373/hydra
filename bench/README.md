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
- `--max-usd` is checked before every call. Every billed call is recorded, including one
  interrupted by a kill, so a resume cannot spend the cap twice. Exit 2 means the cap
  stopped the run; exit 4 means the provider broke the contract mid-run.
- A 429, 5xx or transport error is retried with backoff. If it still fails, the pair is
  recorded as unscored, and the next `run` asks it again. A run with any pair still
  unscored is reported as INCOMPLETE, with counts but no paired statistics.
- `smoke.toml` refuses to load until the Opus and Haiku Bedrock ids are pinned (Q4).
