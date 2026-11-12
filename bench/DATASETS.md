# Benchmark datasets

The raw data is **not** committed. `bench/datasets.lock` pins each file by commit and
sha256; `hyctl bench fetch` downloads it to `$HYDRA_HOME/bench/data/<suite>/` and refuses
any file whose hash differs. What is committed is derived: `bench/splits/<suite>.tsv`
(`id  split  rank  label`), generated once by `go generate ./internal/bench/suites`, and
`bench/configs/smoke.ids`, the first 100 test ids by rank per suite.

Both suites have been public since 2019–2020 and are **likely in pre-training data**.
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

## Duplicates and excluded items

Copies of one utterance are found under the split key below, which ignores case,
apostrophes, punctuation and spacing. Every copy shares a split. Copies whose gold labels
differ are label noise no arm can score, so every copy is `excluded`.

| Suite | Duplicate groups (items) | Conflicting groups (items excluded) |
|---|---|---|
| Banking77 | 62 (124) | 2 (4) |
| CLINC150 | 92 (188) | 4 (9) |

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
  only non-ASCII in either suite is `£`, `€`, `’`, a no-break space and `…`, so NFC adds
  nothing here, and the key needs no new dependency.
- `hyctl bench run` recomputes every row's split, rank and label from the data and refuses
  a TSV that disagrees.
- **Rank**: `sha256("rank\0" ‖ suite ‖ "\0" ‖ id)`, first 8 bytes. A tier takes the first
  *n* test items by rank, so tiers nest and the seed never chooses the sample.
- Upstream train/val/test is ignored: no arm trains.
