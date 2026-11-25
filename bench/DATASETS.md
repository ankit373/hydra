# Benchmark datasets

The raw data is **not** committed, and for FaithBench neither are its splits. `bench/datasets.lock` pins each file by commit and
sha256; `hyctl bench fetch` downloads it to `$HYDRA_HOME/bench/data/<suite>/` and refuses
any file whose hash differs. What is committed is derived: `bench/splits/<suite>.tsv`
(`id  split  rank  label`; a Snips label is its gold fields as JSON), generated once by
`go generate ./internal/bench/suites`, and `bench/configs/smoke.ids`, the first 100 test ids
by rank per suite.

Every suite is public and **likely in pre-training data**. `public_since` in `bench/datasets.lock` is the year each
suite's items first appeared (HumanEval+ counts from HumanEval, 2021), and the table footer prints one line per suite.
Paired differences against the frontier arm are the claim; absolute rates are context.

## Banking77

| | |
|---|---|
| Source | https://github.com/PolyAI-LDN/task-specific-datasets (`banking_data/`) |
| Revision | `57ec275d8078af65b7731c2a98be812d844a6d6b` |
| `train.csv` | `b06e26ac675513959a63135f11b94ea7786ed02da65db93a5650d8838cbc664b` |
| `test.csv` | `d12d6e3bc4c3103966ae786dc435913c0c563dfa328f5a3646d0e62cfeeb474d` |
| `categories.json` | `53261da888122daf2d120d925458631d9619e15d82e56052e7a42e535ce32b63` |
| Licence | CC BY 4.0, **verified**: LICENSE at the pinned commit, 2026-11-05 |
| Items | 10,003 train + 3,080 test = **13,083**, parsed with `encoding/csv` (13 utterances hold a quoted newline). No exact duplicates; 62 groups differ only in case or punctuation |
| Labels | 77, from `categories.json` |
| Split | 3,982 calibration · 9,097 test · **4 excluded** |
| Ids | `train/00000`–`train/10002`, `test/0000`–`test/3079`: the 0-based data row after the header |
| Citation | Casanueva et al., *Efficient Intent Detection with Dual Sentence Encoders*, NLP4ConvAI 2020 |

## CLINC150

| | |
|---|---|
| Source | https://github.com/clinc/oos-eval (`data/data_full.json`) |
| Revision | `828f8093932c8fe6ca7936c3d2e52903b1c523de` |
| `data_full.json` | `36923c3705a59e08fe9c3883d8bc2dd966ef93e22cb78ac41171782a698d56e0` |
| Licence | CC BY 3.0, **verified**: LICENSE at the pinned commit, 2026-11-05 |
| Items | train 15,000 · val 3,000 · test 4,500 · oos_train 100 · oos_val 100 · oos_test 1,000 = **23,700** |
| Labels | 150 intents + `oos`, an ordinary label |
| Split | 7,034 calibration · 16,657 test · **9 excluded** |
| Ids | `<key>/<index>`, e.g. `oos_test/0999` |
| Citation | Larson et al., *An Evaluation Dataset for Intent Classification and Out-of-Scope Prediction*, EMNLP 2019 |

## FaithBench (claims)

| | |
|---|---|
| Source | https://github.com/vectara/FaithBench (`data_for_release/batch_<n>.json`) |
| Revision | `cf89797d82812c23b5d5e5c121f1d9b8983bbbce` |
| Files | fifteen, `batch_1` to `batch_16` with no `batch_13`; each sha256 is in `bench/datasets.lock` |
| Licence | CC BY-NC-SA 4.0, **verified**: LICENSE at the pinned commit, 2026-11-16. Non-commercial and share-alike, so **no row, label or split file is committed**. `local_splits = true` in the lock: `hyctl bench run` derives the split file into `$HYDRA_HOME/bench/data/faithbench/splits.tsv` from the fetched data and then checks it like a committed one. The test fixture is synthetic text written for this repository |
| Items | 750 summaries, 3,631 sentences by `ground.Sentences`. **3,149** get a label: 2,480 supported, 669 not supported. 482 are left out |
| Sentence label | the annotations are character spans of the summary, counted in Unicode code points, from several annotators. A sentence is `not_supported` when any annotator's span with an `Unwanted` or `Unwanted.*` label overlaps it, `supported` when no span touches it, and left out when only `Questionable`, `Benign` or unlabelled spans touch it. This rule is a choice: one annotator is enough to mark a sentence |
| Split | 952 calibration · 2,188 test · **9 excluded**. The split key is the sentence with combining marks dropped plus the first 8 bytes of its source's sha256, so one sentence about two sources is two items. A few summaries spell an accent with a combining mark, and the key has no NFC |
| Ids | `batch_<n>/<sample_id>/<sentence>`, e.g. `batch_3/41/2`; the sentence counts from 1 |
| Class | `claims.faithbench` |
| Prompt | the claims check's judge question for one sentence, exactly as the judge variant sends it to a chat head (`checkgen.ClaimsJudgeChat`) |
| Arms | `ground`, a check arm: the claims check's ground variant on the sentence against the fenced source, no model. A judge arm is any model arm; its reply is read by its first word, yes or no |
| Grade | the sentence is flagged when the answer is `not_supported` or no. `nothing_to_check` (ground found no number, name, date or quote) is not a flag. A reply that is neither yes nor no is a format fail and counts as not flagged. Recall is flagged over not-supported sentences; the false-flag rate is flagged over supported ones, each with a 95% Wilson interval |
| Citation | Bao et al., *FaithBench: A Diverse Hallucination Benchmark for Summarization by Modern LLMs*, NAACL 2025 |

## GSM8K

| | |
|---|---|
| Source | https://github.com/openai/grade-school-math (`grade_school_math/data/test.jsonl`) |
| Revision | `3101c7d5072418e28b9008a6636bde82a006892c` |
| `test.jsonl` | `3730d312f6e3440559ace48831e51066acaca737f6eabec99bccb9e4b3c39d14` |
| Same data as | Hugging Face `openai/gsm8k` at `740312add88f781978c0658806c59bc2815b9866`, `main/test-00000-of-00001.parquet` (sha256 `ee7b8da9…4e4f59`): the same 1,319 question and answer pairs in the same order, checked 2026-11-15. The JSONL is pinned because reading it needs no Parquet dependency |
| Licence | MIT, **verified**: LICENSE at the pinned commit, 2026-11-15 |
| Items | test only: **1,319**. Train is not pinned, since no arm trains |
| Gold | the number after `####` in the reference answer, with commas dropped, and whole values printed without a decimal point (`1,300` is `1300`) |
| Split | 410 calibration · 909 test · **0 excluded** |
| Ids | `test/0000`–`test/1318`: the 0-based line in `test.jsonl` |
| Class | `answer.gsm8k` |
| Grade | the answer's `#### <number>` line, or its last number when there is none (the rule the pilot used); no number at all is a format fail |
| Checks | `GSM8KFormatCheck` (the answer ends in a `####` line) and `GSM8KArithmeticCheck` (every `<<expression=result>>` note recomputes; no notes is a fail). Both are free and run on the answer text alone. The prompt asks for both, with no worked examples |
| Citation | Cobbe et al., *Training Verifiers to Solve Math Word Problems*, 2021 |

## HumanEval+

| | |
|---|---|
| Source | https://github.com/evalplus/humanevalplus_release, release `v0.1.10` asset `HumanEvalPlus.jsonl.gz` |
| Revision | tag `v0.1.10`, commit `ea5804b59b8a7837cb2faa1b10273fa804cdea5c`. The asset is pinned, not the file committed at the tag: the asset is what the `evalplus` package downloads, and the two differ byte for byte |
| `HumanEvalPlus.jsonl.gz` | `272720b90ac375502c8ed23cd791c2a93dfb22a911641a494da74a426c09f101` |
| Licence | Apache-2.0, **verified**: LICENSE at the pinned commit, 2026-11-15. HumanEval itself is MIT |
| Items | **164** tasks |
| Split | 55 calibration · 109 test · **0 excluded** |
| Ids | the upstream `task_id`, e.g. `HumanEval/0` |
| Class | `code.humanevalplus` |
| Grade | every base and plus input gives the reference solution's output, as EvalPlus scores "plus". `BaseTests`, the cheap check, runs the base inputs alone. Output comparison, the float tolerance and the `find_zero` special case follow `evalplus.eval.unsafe_execute`; per-input time limits are EvalPlus's, 4x the reference's time with a 1 s floor |
| Runner | `python3 -I -B` on an embedded script, under `sandbox.WithLimits` (2 GB memory, 120 s CPU), a 2 minute deadline and a process-group kill. An audit hook refuses sockets, DNS, child processes, `ctypes` and writes outside the run's temp directory. That is a guard inside the interpreter, not a kernel sandbox. Linux and macOS only |
| Timeouts | a fail, marked `timeout` on the row and counted as `timeouts` per arm in `summary.json` |
| Checked | every canonical solution passes all its inputs. On the pilot's GPT-4 and CodeGen-2B samples the runner agreed with EvalPlus 0.3.1 on 655 of 656 base and plus verdicts; the one difference was a plus timeout |
| Citation | Liu et al., *Is Your Code Generated by ChatGPT Really Correct?*, NeurIPS 2023; Chen et al., *Evaluating Large Language Models Trained on Code*, 2021 |

## Snips (extraction)

| | |
|---|---|
| Source | https://github.com/sonos/nlu-benchmark (`2017-06-custom-intent-engines/<intent>/validate_<intent>.json`) |
| Revision | `b86ac7f1577868c42158d0dec77db50956046696` |
| Files | seven, one per intent; each sha256 is in `bench/datasets.lock` |
| Licence | CC0 1.0, **verified**: LICENSE at the pinned commit, 2026-11-16 |
| Items | the validate split only, 100 per intent: **700**. The larger train files are not pinned, since no arm trains |
| Gold | the slots each utterance fills, as fields: `{"city": "Lisbon", "timeRange": "7 pm"}`. No utterance fills one slot twice; the loader refuses one that does |
| Split | 188 calibration · 512 test · **0 excluded** |
| Ids | `<intent>/<row>`, e.g. `GetWeather/038`: the 0-based record in that intent's file |
| Class | `extraction.snips` |
| Prompt | the utterance, fenced, and every key that intent's utterances fill. The answer is one JSON object with only the keys the message gives a value for |
| Grade | every gold field present and equal after the fields check's normalising (`checkgen.SameValue`: text folded for case and spacing, numbers by value), and no other key with a value. An answer that is not a JSON object is a format fail |
| Citation | Coucke et al., *Snips Voice Platform: an embedded Spoken Language Understanding system for private-by-design voice interfaces*, 2018 |

## Duplicates and excluded items

Copies of one utterance are found under the split key below, which ignores case,
apostrophes, punctuation and spacing. Every copy shares a split. Copies whose gold labels
differ are label noise no arm can score, so every copy is `excluded`.

| Suite | Duplicate groups (items) | Conflicting groups (items excluded) |
|---|---|---|
| Banking77 | 62 (124) | 2 (4) |
| CLINC150 | 92 (188) | 4 (9) |
| GSM8K | 0 | 0 |
| HumanEval+ | 0 | 0 |
| Snips | 5 (10) | 0 |
| FaithBench | not counted | 9 items excluded |

| Suite | Utterances | Labels | Ids |
|---|---|---|---|
| Banking77 | Has my top up / top-up gone through? | `pending_top_up` / `top_up_reverted` | `train/02002`, `train/03160` |
| Banking77 | What are the fees for top-ups / top ups? | `top_up_by_bank_transfer_charge` / `top_up_by_card_charge` | `test/0618`, `test/2838` |
| CLINC150 | what is on my to do list (×2), to-do list | `todo_list` / `reminder` / `todo_list` | `train/07424`, `val/1011`, `test/2236` |
| CLINC150 | turn up your volume | `whisper_mode` / `change_volume` | `train/11130`, `val/1794` |
| CLINC150 | what's your designation | `what_is_your_name` / `user_name` | `train/12066`, `test/0938` |
| CLINC150 | where did you grow up | `how_old_are_you` / `where_are_you_from` | `train/14018`, `test/0599` |

## How a split is assigned

- **Split key**: `sha256("split\0" ‖ suite ‖ "\0" ‖ key(text))`, first 8 bytes, mod 1000;
  under 300 is calibration, the rest test. `key` lowercases, drops apostrophes, turns every
  other non-letter, non-digit (punctuation, symbols, any Unicode space) into a break, and
  collapses whitespace, so "what's my balance?" and "whats my balance" share a split. It is
  a leakage key, not the grader's label normaliser, which must keep `-` significant. The
  only non-ASCII in Banking77 and CLINC150 is `£`, `€`, `’`, a no-break space and `…`, and
  GSM8K adds `¾`, curly quotes and dashes, and HumanEval+ `≤` and `➞`, and Snips precomposed Latin
  letters (`é`, `ñ`, `Å`, `Ō`, `ş`). NFC changes none of them, so the key needs no new dependency.
- `hyctl bench run` recomputes every row's split, rank and label from the data and refuses
  a TSV that disagrees.
- **Rank**: `sha256("rank\0" ‖ suite ‖ "\0" ‖ id)`, first 8 bytes. A tier takes the first
  *n* test items by rank, so tiers nest and the seed never chooses the sample.
- Upstream train/val/test is ignored: no arm trains.

## Idle replay fixtures

`bench/idle-replay/*.json` are four conversations written for `hyctl idle replay` (#1639), one each for
support, research, writing and an agent loop. They are committed whole, since they are small and made up:
no real customer, paper or person, licence MIT, stated in each file. Each has about 30 turns, some with
pasted logs or OCR lines so there is something to compact, and eight needles: rules and facts planted in
one turn and asked for later. They are not a public set of long chats; replaying such a set is one
`hyctl idle replay <dir>` away once it is converted to this shape.
