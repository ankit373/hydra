# AVER data prep

Scripts that rebuild the public answers behind the AVER pilot and turn them into the
replay rows `hyctl bench replay` reads (#1580). They run outside the Go binary, with
`python -I`. No model is called and nothing is bought: every answer was written by
someone else and is downloaded as it was published.

Downloaded data is never committed. Only `testdata/` is: five synthetic rows and what
the converter makes of them.

## Do these numbers match the research post?

Not claimed. The pilot's working directory was deleted, so these scripts were rebuilt
from the source list in #1645. The model lists, the check set, the answer keys and the
prices below were chosen again from public pages. Counts and rates can differ from the
ones the post cites. What could not be rebuilt:

- the pilot's exact model list and its total of 16,269 labelled answers
- the 2024 prices the pilot used; the table below holds the ones rebuilt from the archived pages
- any check that needs a model, such as the local judge
- code agreement by behaviour: the answer key for code is a hash of the whitespace-normalised
  source, so two models agree only when they wrote the same code

## Rebuild the four replay files

```bash
D=$HOME/aver-data

python -I bench/aver/prep_code.py fetch --data $D
bash bench/aver/run_evalplus.sh $D
python -I bench/aver/prep_code.py merge --data $D --out $D/code.prepped.jsonl
python -I bench/aver/prep_gsm8k.py --data $D --out $D/gsm8k.prepped.jsonl
python -I bench/aver/prep_rag.py --data $D --out $D/ragtruth.prepped.jsonl

python -I bench/aver/to_replay.py --in $D/code.prepped.jsonl --out $D/code.replay.jsonl
```

`code.prepped.jsonl` holds both HumanEval+ and MBPP+. Replay each set on its own: filter
by the `humaneval/` and `mbpp/` task prefix, or run `to_replay.py` once per suite. The
GSM8K and RAGTruth files convert the same way. Then:

```bash
hyctl bench replay --candidates $D/gsm8k.replay.jsonl --check format
```

- `prep_gsm8k.py` needs `pyarrow` (the leaderboard details are parquet). Run it with a
  Python that has it. The converter and the Go side stay free of parquet.
- `run_evalplus.sh` needs macOS `sandbox-exec`. Without it the script stops; it never runs
  generated code unsandboxed. It installs `evalplus==0.3.1` in `$D/venv`.
- EvalPlus has time limits. On a busy machine correct code times out and is labelled
  wrong. Score on an idle machine and read the timeout count `merge` prints.

## Sandbox

`sandbox.sb` denies all network and all file writes outside the data directory. The
profile is passed `WORK=$D`, and `HOME` and `TMPDIR` point inside it. The problem files
are downloaded before the sandboxed run, so scoring itself needs no network.
`EVALPLUS_MAX_MEMORY_BYTES=-1` is set because EvalPlus's memory cap fails on macOS, so
memory is not limited here.

## Sources, revisions and licences

Each licence below was read from the set's own repo or card at the revision named, on
2026-11-24. Every download is pinned by sha256 in `sources.json`, and the scripts refuse
a file whose hash differs.

| Set | What is used | Revision | Licence | Where it was read |
|---|---|---|---|---|
| HumanEval+ | greedy samples from `evalplus/evalplus` release `v0.1.0`; problems and tests from `evalplus/humanevalplus_release` `v0.1.10` | `evalplus` `26d6d00bb1fd`, release repo `200defce9e34` | Apache-2.0 | [LICENSE](https://github.com/evalplus/evalplus/blob/26d6d00bb1fd0fa37f39c99d5290da67891d1c5e/LICENSE), [LICENSE](https://github.com/evalplus/humanevalplus_release/blob/200defce9e3429d28ca215b6dd061c0f7f31c18b/LICENSE) |
| HumanEval | the base problems under HumanEval+ | `openai/human-eval` `b9453e8f1865` | MIT | [LICENSE](https://github.com/openai/human-eval/blob/b9453e8f1865df0a0057a1383767caa400acfbcf/LICENSE) |
| MBPP+ | greedy samples from `evalplus/evalplus` release `v0.2.0`; problems and tests from `evalplus/mbppplus_release` `v0.2.0` | `mbppplus_release` `dadf43da556a` | Apache-2.0 | [LICENSE](https://github.com/evalplus/mbppplus_release/blob/dadf43da556a00f7bacd71cb154f2932757d9144/LICENSE) |
| MBPP | the base problems under MBPP+ | `google-research-datasets/mbpp` `4bb6404fdc6c` | CC BY 4.0, from the dataset card's `license` field; I found no separate LICENSE file | [card](https://huggingface.co/datasets/google-research-datasets/mbpp/blob/4bb6404fdc6cacfda99d4ac4205087b89d32030c/README.md) |
| GSM8K | the gold answers, `test.jsonl` | `openai/grade-school-math` `3101c7d5072c` | MIT | [LICENSE](https://github.com/openai/grade-school-math/blob/3101c7d5072418e28b9008a6636bde82a006892c/LICENSE) |
| GSM8K answers | Open LLM Leaderboard v1 per-sample details, 8 models, latest 5-shot run each | see below | none stated | see below |
| RAGTruth | `dataset/response.jsonl` and `dataset/source_info.jsonl` from `ParticleMedia/RAGTruth` | `c103204b9ce2` | MIT | [LICENSE](https://github.com/ParticleMedia/RAGTruth/blob/c103204b9ce28d6bbad859304bf30de72b8ed8fe/LICENSE) |

Notes on the table:

- The pilot read RAGTruth from `wandb/RAGTruth-processed`. This folder reads the original
  repo instead, which is plain JSONL and carries the licence file itself.
- The leaderboard detail repos state no licence. Their cards say "More Information Needed"
  under Licensing and have no licence field. So the converted GSM8K rows are never
  committed. `prep_gsm8k.py` rebuilds them on your machine and the output stays there.
- MBPP's licence is the one I could confirm least strongly: the card says CC BY 4.0, and
  there is no licence file to check it against. Rows made from MBPP+ hold labels, a hash
  and token counts, not problem text or code.
- RAGTruth labels judge the dataset's own answers. It is replay only, never a live suite:
  there is no way to label a new answer.

Leaderboard detail repos used for GSM8K (`open-llm-leaderboard-old/<repo>`):

| Repo | Revision | Run |
|---|---|---|
| `details_meta-llama__Llama-2-7b-hf` | `297e285fda3d` | `2023-12-02T13-00-54.924067` |
| `details_meta-llama__Llama-2-13b-hf` | `0335fc4acc9a` | `2023-12-02T13-11-49.394544` |
| `details_meta-llama__Llama-2-70b-hf` | `101de062efac` | `2023-12-03T01-14-42.713769` |
| `details_mistralai__Mistral-7B-v0.1` | `7b4f5b61de1f` | `2023-12-02T13-02-14.153054` |
| `details_mistralai__Mixtral-8x7B-v0.1` | `16af2ad182b9` | `2024-01-04T16-34-48.985318` |
| `details_01-ai__Yi-6B` | `d7774e4e16ec` | `2023-11-08T12-58-11.094136` |
| `details_01-ai__Yi-34B` | `7ed6744a3c59` | `2023-11-08T19-46-38.378007` |
| `details_Qwen__Qwen-14B` | `8cccb84d56a6` | `2023-11-15T04-46-20.928178` |

## Labels and checks

A row is one model's answer to one task. `correct` is the set's own label. Checks are
recomputed with no model call.

| Set | `correct` | Checks |
|---|---|---|
| HumanEval+, MBPP+ | passes the base and the extra EvalPlus tests | `base_tests`: base tests pass. `compiles`: the source parses |
| GSM8K | the final `#### n` equals the gold number | `format`: a final answer is present. `arithmetic`: at least one `<<a*b=c>>` note, all of them right |
| RAGTruth | the annotators marked no hallucinated span | `numbers_in_source`: every number in the answer is in the prompt. `complete`: the dataset's quality label is `good` |

The grounded checks are weak on purpose; they are what can be computed without a model.

As a sanity check, `prep_gsm8k.py` re-scores Llama-2-7b at 191 of 1319, the same count the
leaderboard's own `acc` column gives for that run.

## Token counts and prices

Token counts are exact for GSM8K, from the leaderboard's own token ids. For code and
RAGTruth the source has no counts, so they are characters divided by 4, and each row says
which in `tokens_basis`. Cost is input tokens times the input price plus output tokens
times the output price. `to_replay.py` refuses a model that has no price.

Prices are archived 2024 list prices, in `prices.json`:

| Snapshot | Date | Page |
|---|---|---|
| OpenAI | 2024-05-02 | [web.archive.org](http://web.archive.org/web/20240502185457/https://openai.com/api/pricing) |
| Together AI | 2024-03-04 | [web.archive.org](http://web.archive.org/web/20240304022604/https://www.together.ai/pricing) |

| Model heads | Snapshot | Listed as | USD per 1M tokens, in / out | Basis |
|---|---|---|---|---|
| `gpt-4`, `gpt-4-0613` | OpenAI | `gpt-4` | 30 / 60 | listed; `gpt-4-0613` is the nearest listed model |
| `gpt-4-1106-preview` | OpenAI | `gpt-4-1106-preview` | 10 / 30 | listed |
| `gpt-3.5-turbo`, `chatgpt` | OpenAI | `gpt-3.5-turbo-0125` | 0.50 / 1.50 | nearest listed model |
| `gpt-3.5-turbo-0613` | OpenAI | `gpt-3.5-turbo-0613` | 1.50 / 2.00 | listed |
| Llama-2 and CodeLlama 7B | Together AI | Llama-2 and CodeLlama, 7B | 0.20 | listed, one rate for input and output |
| Llama-2 and CodeLlama 13B | Together AI | Llama-2 and CodeLlama, 13B | 0.225 | listed |
| CodeLlama 34B | Together AI | Llama-2 and CodeLlama, 34B | 0.776 | listed |
| Llama-2 70B | Together AI | Llama-2 and CodeLlama, 70B | 0.90 | listed |
| Mixtral-8x7B | Together AI | Mixture-of-experts, 8X 7B | 0.60 | listed |
| Mistral 7B, Vicuna 7B, Yi-6B | Together AI | 4.1B - 8B | 0.20 | size tier; the model is not named on the page |
| Vicuna 13B, Qwen-14B | Together AI | 8.1B - 21B | 0.30 | size tier |
| WizardCoder 34B, DeepSeek-Coder 33B, Yi-34B | Together AI | 21.1B - 41B | 0.80 | size tier |

`prices.json` has one entry per head name, and a test checks that every pinned model has one.

## Tests

```bash
python -I -m unittest discover -s bench/aver
go test ./internal/bench/replay/ -run ConverterFixture
```

`test_to_replay.py` checks the field mapping on the five-row fixture in `testdata/`, and
that converting `prepped.jsonl` gives exactly `replay.jsonl`. The Go test loads
`replay.jsonl` with `replay.Read`, so a change to the replay format breaks it.
