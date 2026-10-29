# RFC 0001 appendix: Context compression

- **Status:** Draft, research appendix to [RFC 0001](0001-verified-cascade-gateway.md) §5.5
- **Date:** 2026-10-29
- **Author:** ankit373
- **Scope:** what is known about shrinking a prompt before it reaches a closed-API model, what goes wrong, how to measure it, when it pays, and how Hydra would do it without silently dropping something important.

Conventions:
- **[PUB]** means a method, table or repo you can check.
- **[VENDOR]** means a vendor's number with no reproducible method behind it.
- **UNVERIFIED** means the claim could not be traced to a primary source. §A.8 lists all of these.
- "Ratio" means original tokens / compressed tokens unless stated. "Kept" means compressed / original.
- Prices were read from provider pages on 2026-10-29 and go stale; `internal/pricing` is the runtime authority.

## A.0 Findings in one page

1. **Nothing in the literature tests the property we need.** No paper found evaluates instruction retention (IFEval-style constraints), negation preservation or number fidelity under a prompt compressor. Papers report EM, ROUGE or BERTScore on QA and summarisation; Selective Context even scores against the target model's own full-context output, not ground truth. "Little quality loss" in a paper is therefore not evidence that constraints survive.
2. **Token-level deletion is the wrong tool for a gateway.**
   - It is query-agnostic in its fast form (LLMLingua-2) and slow in its query-aware form (LongLLMLingua is about 194 s per 50 samples).
   - It visibly corrupts numbers. LLMLingua's own README demo prints "12 * 300ters"; its paper's Fig. 10 turns GADGET-2 into "GGET2", and the summary built on it is fluent and wrong. `force_reserve_digit` defaults to off.
   - It destroys agent action grammar. AGORA reports 17 of 17 agent configurations collapsing to mean reward ≤ 0.05. ACON's table has LLMLingua on AppWorld history at 39.3 accuracy against 56.0 uncompressed.
   - It can be steered by an attacker. COMA reaches a 0.44–0.63 success rate at corrupting system-prompt guardrails across six compressors.
3. **Sentence-level, query-aware extraction is the best-evidenced lossy method for RAG.** Examples are EXIT, RECOMP-extractive and Provence. Even so, it fails on multi-hop: RECOMP-extractive scores 11.2 EM on 2WikiMQA against 16.1 uncompressed. Provence is CC BY-NC-ND, so it cannot ship.
4. **For agents, deterministic observation masking matches LLM summarisation at lower cost.** Masking means replacing old tool outputs with a placeholder. The Complexity Trap measured about −50% cost at equal or better solve rate on SWE-bench Verified. Summarisation also lengthened trajectories by 13–15%, because summaries "mask failure signals". ACON's learned compression lengthened them by 41%.
5. **Abstractive summaries lose facts and invent them.**
   - MemGPT's summary-only baseline scores 32.1% against 92.5% with retrievable history.
   - RECOMP's abstractive summaries were judged 67% faithful on HotpotQA.
   - ACE documents "context collapse": a single rewrite took 18,282 tokens down to 122, and accuracy dropped below the no-context baseline.
6. **Reversibility is the common thread among what works:** MemGPT's retrievable history, Manus's "restorable" compression, DietCode's `sd_retrieve`, LiteLLM's `litellm_content_retrieve` and Headroom's `headroom_retrieve`.
7. **Caching decides the economics.**
   - Re-compressing an already-cached prefix loses money unless more than 92–98% of it is removed. The exact threshold depends on the model's read and write multipliers.
   - The money is in the uncached tail and in folding history once into a byte-stable form.
   - Hydra today sends no `cache_control`, reads no cached-token counts and stores no cache prices (§A.7.1). It cannot yet compute whether compression pays.
8. **Recommended default set.**
   - **On (after a shadow period):** lossless dedup and conservative whitespace.
   - **Opt-in per class, after an A/B on the evalset:** pointer-replacement of stale tool outputs with a fetch tool, and deterministic tool-output compaction.
   - **Opt-in, RAG classes only, after an A/B:** query-aware sentence extraction.
   - **Off, never default:** abstractive summarisation.
   - **Not shipped at all:** token-level deletion.

## A.1 Technique families

Soft or learned methods are listed for completeness only. They need the target model's embeddings, KV cache or fine-tuned weights, so they cannot run in front of a closed API, and their encoders must be retrained whenever the decoder changes (NAACL 2025 survey §6.1).

| Method | Paper | Query-aware | Kind | Reported ratio | Measured quality change (benchmark) | Compressor and cost | Licence |
|---|---|---|---|---|---|---|---|
| Selective Context | [2310.06201](https://arxiv.org/abs/2310.06201) EMNLP'23 | no | token/phrase deletion by self-information | 20–80% removed | At 50% removed: BLEU .347 → .179, BERTScore .909 → .887, measured **against the model's own full-context answer**. Unfaithful tuples .027 → .086 and refusals 0 → 27/200 as removal rises to 80% (Tables 1, 3) | LLaMA-7B; 15.5–15.9 s per MeetingBank prompt, slower than the uncompressed pipeline (LLMLingua-2 Table 5) | no LICENSE file in repo; PyPI says MIT |
| LLMLingua | [2310.05736](https://arxiv.org/abs/2310.05736) EMNLP'23 | no | token deletion by small-LM perplexity, with demo-level budget | 3–20x | GSM8K 78.85 → 77.33 at 20x, but by dropping redundant CoT demonstrations. BBH 70.07 → 61.60 at 5x and 56.85 at 7x. The paper says instructions and questions "necessitate a lower compression ratio" (§5.4) | Alpaca-7B; 0.2–0.8 s on GSM8K, 1.5–2.9 s on MeetingBank (V100) | MIT |
| LongLLMLingua | [2310.06839](https://arxiv.org/abs/2310.06839) ACL'24 | **yes** | token deletion plus document reordering | 4–6x | NQ 20-doc, worst position: 54.1 → 71.2–75.5. LongBench 44.0 → 48.3 at 6x | LLaMA-2-7B; about 2x LLMLingua's compute; 194 s per 50 samples (Provence Table 2); cannot be cached across queries | MIT |
| LLMLingua-2 | [2403.12968](https://arxiv.org/abs/2403.12968) ACL Findings'24 | no | per-token keep/drop classifier distilled from GPT-4, MeetingBank only | 2–14x | LongBench 44.0 → 39.1 at 5x; synthetic tasks 37.8 → 21.3. BBH 70.07 → 61.94 at 5x. No analysis of numbers or negation | xlm-roberta-large (355M) or mBERT (110M); 0.4–0.5 s per prompt on V100, 2.1 GB peak | MIT (XLM-R model); Apache-2.0 (mBERT model) |
| RECOMP-extractive | [2310.04408](https://arxiv.org/abs/2310.04408) | **yes** | sentence selection (Contriever dual encoder) | about 18x (660 → 37 tokens) | NQ 39.39 → 36.57 EM; HotpotQA 32.80 → 30.40. **2WikiMQA 16.1 → 11.2** with Llama-3.1-8B (EXIT Table 1) | 110M; 12 s per 50 samples | MIT |
| Provence | [2501.16214](https://arxiv.org/abs/2501.16214) ICLR'25 | **yes** | sentence labelling plus reranker | about 2x (49%) | "Negligible" drop, shown in figures only. **Misses needles at the leftmost and rightmost positions** | DeBERTa-v3, 430M, 512-token context, English only; 25 s per 50 samples | **CC BY-NC-ND 4.0** (non-commercial) |
| EXIT | [2412.12559](https://arxiv.org/abs/2412.12559) ACL Findings'25 | **yes** | context-aware sentence classifier | n/a | Average EM 34.4 → 37.9 (8B reader); 38.8 → 42.5 (70B) | Gemma-2B; 0.8 s end to end against 1.0 s uncompressed | none declared |
| FILCO | [2311.08377](https://arxiv.org/abs/2311.08377) | **yes** | sentence filter | 44–64% removed | Also fine-tunes the generator, so not usable with closed APIs as published | — | CC-BY-SA-4.0 |
| RECOMP-abstractive | [2310.04408](https://arxiv.org/abs/2310.04408) | **yes** | T5-large summary | about 18x | HotpotQA 32.80 → 28.20 EM. Human faithfulness 80% (NQ), 83% (TQA), **67% (HotpotQA)**, n = 30 | 775M; 499 s per 50 samples | MIT |
| CompAct | [2407.09014](https://arxiv.org/abs/2407.09014) EMNLP'24 | **yes** | iterative abstractive | about 48x | HotpotQA 29.4 → 35.5, but NQ 39.0 → 38.4 and TQA 68.9 → 65.4 | Mistral-7B fine-tune; 8.4 s against 1.0 s | none declared |
| Running-summary memory (MemGPT) | [2310.08560](https://arxiv.org/abs/2310.08560) | n/a | summary vs paged, retrievable history | n/a | Deep memory retrieval: **summary-only 32.1%**, retrievable history 92.5% (GPT-4) | — | Apache-2.0 (Letta) |
| Observation masking | [2508.21433](https://arxiv.org/abs/2508.21433) DL4C@NeurIPS'25 | no | deterministic placeholder for tool output older than M = 10 turns | about 2x cost | SWE-bench Verified: Qwen3-Coder 53.4 → 54.8 at −52.7% cost; Gemini 2.5 Flash 32.8 → 35.6 at −56.1% | none (rule) | MIT |
| LLM trajectory summary | same | no | summarise older turns | about 2x cost | Similar solve rate; **13–15% longer trajectories**; summary calls up to 7.2% of cost; significant drop on Gemini-Flash-thinking (40.4 → 31.4) | an LLM call | MIT |
| ACON | [2510.00615](https://arxiv.org/abs/2510.00615) ICML'26 | learned guidelines | LLM history and observation compression, distilled | peak tokens −26–54% | AppWorld 56.0 → 56.5 but **41% more steps**. OfficeBench 76.84 → 72.63–74.74. Same table: LLMLingua on history 39.3, on observations 32.1 | gpt-4.1 or distilled small model | MIT |
| AGORA | [2605.26596](https://arxiv.org/abs/2605.26596) preprint | step-level | structural parser + always-keep floor + 125M scorer | — | Token-level compressors: **17 of 17 agent cells collapse to reward ≤ 0.05**. AGORA keeps ≥ 75% of uncompressed reward in 8 of 9 cells; the structural floor is the main factor | about 2 ms per step | MIT |
| ACE | [2510.04618](https://arxiv.org/abs/2510.04618) ICLR'26 | n/a | incremental delta updates instead of rewriting | — | Documents "brevity bias" and "context collapse" (18,282 → 122 tokens, 66.7 → 57.1 accuracy, below the 63.7 baseline) | — | Apache-2.0 |
| Gist tokens, ICAE, AutoCompressors, xRAG, 500xCompressor, OSCAR | [2304.08467](https://arxiv.org/abs/2304.08467), [2307.06945](https://arxiv.org/abs/2307.06945), [2305.14788](https://arxiv.org/abs/2305.14788), [2405.13792](https://arxiv.org/abs/2405.13792), [2408.03094](https://arxiv.org/abs/2408.03094), [2504.07109](https://arxiv.org/abs/2504.07109) | varies | soft or learned (needs weights) | 4x to 480x | xRAG's "+10%" is against *no retrieval*; against full RAG it is 43.63 → 42.46 average and 38.79 → 34.05 on HotpotQA. 500xCompressor keeps "62.26–72.89%" of capability | — | **not applicable to closed APIs** |

**Surveys and characterisations.**
- Jha et al., [2407.08892](https://arxiv.org/abs/2407.08892) (ES-FoMo@ICML'24): extractive reranking "often outperforms all the other approaches", up to 10x with minimal loss. Token pruning "often lag[s] behind", and LongLLMLingua shows "the worst behavior", attributed to grammar disruption.
- Li et al., [2410.12388](https://aclanthology.org/2025.naacl-long.368) (NAACL'25 survey): hard prompts "may disrupt grammatical correctness and provide an unfamiliar input distribution".
- Kummer et al., [2604.02985](https://arxiv.org/abs/2604.02985) (ECIR'26): LLMLingua's end-to-end speed-up reaches 18% only when prompt length, ratio and hardware line up. Outside that window, compression time "dominates and cancels out the gains".

## A.2 Products and provider-native features

Every row separates what can be checked from what is only asserted.

| Product | Mechanism | Query-aware | Cache-aware | Published | Only claimed |
|---|---|---|---|---|---|
| **ScaleDown** `/compress/raw/` ([docs](https://docs.scaledown.ai/api-reference/compress-overview.md)) | Compresses `context` against a `prompt` that is kept. The OSS README says it "rewrites prompts", so it is probably abstractive. Has a `temperature` parameter | probably (the LangChain middleware says "relative to the latest user question") | not documented; `temperature` suggests output is not deterministic | API shape; [OSS repo](https://github.com/scaledown-team/scaledown), whose licence is inconsistent (README says MIT, metadata says GPL-3.0) | [VENDOR] "50–70%" with no dataset; pricing by quote |
| **ScaleDown DietCode** ([repo](https://github.com/scaledown-team/DietCode), MIT) | Proxy for Claude Code: system and tools untouched, a running summary of old turns, then the last 4 turns verbatim. Folds at 50k tokens. Folded turns retrievable via `sd_retrieve(id)`. Structural compaction of `ls`/`grep`/`git` output. Fails open | partly | **by design** ("byte-stable" summary); no hit-rate data | the code | [VENDOR] "50–70%". Open bugs: [#16](https://github.com/scaledown-team/DietCode/issues/16), retrieval id points at the summary rather than the original; [#17](https://github.com/scaledown-team/DietCode/issues/17), summaries reused across conversations sharing a prefix. **Both are exactly the reversibility and isolation failures §A.7.5 guards against** |
| **The Token Company** ([site](https://thetokencompany.com)) | Delete-only: a classifier scores tokens and drops the least important; order kept; `aggressiveness` knob | generic models no; `bear-2-finance` yes | [VENDOR] "deterministic … caches stay valid". True only if the same prefix bytes are compressed identically and breakpoints still align | SDK docs | [VENDOR] CoQA +2.0 pts at −8.2% tokens; SQuAD 2.0 +4.0; FinanceBench no loss at 50%; "<50 ms" alongside "p95 150 ms" on the same page; LongBench v2 figure UNVERIFIED. No eval code. Billing on tokens removed. Not open source. No protected-region syntax documented |
| **Inworld Router** ([docs](https://docs.inworld.ai/router/capabilities/prompt-compression.md)) | "Shortens long messages"; model and method undisclosed; `aggressiveness` 0–1; skips text under 250 tokens and tool results; warns about JSON, XML and code | undisclosed | undisclosed. Compressing system templates sits on the cache prefix | parameters | an illustrative 190 → 102 tokens only |
| **Kong AI Prompt Compressor** ([docs](https://developer.konghq.com/plugins/ai-prompt-compressor/)) | LLMLingua-2 (`llmlingua-2-xlm-roberta-large-meetingbank`) in a private Docker service; `rate` or `target_token`; `compression_ranges`; `<LLMLINGUA>` tags limit compression to tagged text (only via the RAG Injector template); `message_type` defaults to `["user"]` | no | no | config reference | nothing beyond the LLMLingua-2 paper. **Enterprise only** |
| **LiteLLM** `compress()` ([docs](https://docs.litellm.ai/docs/completion/prompt_compression.md), beta) | Above 200k tokens, BM25 (plus optional embeddings) ranks messages and replaces low-relevance ones with stubs. Exposes `litellm_content_retrieve`. Always keeps system, last user and last assistant messages | yes | partial: the Headroom integration skips messages carrying `cache_control` | method | [PUB, n = 5] SWE-bench Lite: tokens −77.7%, but **hunk overlap 0.582 → 0.361**, a quality loss. `trim_messages` truncates to fit the window |
| **Portkey** ([docs](https://portkey.ai/docs/integrations/guardrails/headroom.md)) | No native compressor; a Headroom guardrail hook, with `audit` mode for stats only | — | via Headroom | — | [VENDOR] "typically 40–95%" |
| **Headroom** ([repo](https://github.com/headroomlabs-ai/headroom), Apache-2.0) | Routes by content type: JSON (keeps errors, outliers, first and last items), AST-aware code, a prose model. CacheAligner leaves the frozen prefix byte-identical. Reversible via `headroom_retrieve` | partly | **yes, by design** | self-run benchmark script with a seed: GSM8K unchanged (N = 100); BFCL 97% at 32% compression | 60–95% on secondary sites; 15–20% on coding traffic per a deployer |
| **Morph Compact** ([docs](https://docs.morphllm.com/api-reference/endpoint/compact.md)) | Line-level delete-only; surviving lines byte-identical; `<keepContext>` tags; `compress_system_messages` defaults to false; optional "(filtered N lines)" markers | yes (`query`, defaults to the last user message) | undocumented; query-aware output cannot be a stable prefix | API | [VENDOR] "98% verbatim accuracy"; throughput 33k vs 15k vs 3.3k tok/s across Morph pages |
| **Compresr** ([LiteLLM guardrail](https://docs.litellm.ai/docs/proxy/guardrails/compresr.md)) | Query-aware span deletion; by default only tool outputs ≥ 500 chars; dynamic 1.5–10x; in-memory retrieval store for 15 min (forces one worker); fails closed (502) | yes | no | integration docs | [VENDOR] "2x–10x without quality loss" |
| **OpenRouter** `context-compression` plugin, formerly `middle-out` ([docs](https://openrouter.ai/docs/guides/features/message-transforms)) | Removes or truncates *middle* messages until the prompt fits the window. **On by default for endpoints with ≤ 8k context** | no | no | behaviour | — |
| **Anthropic context editing** ([docs](https://platform.claude.com/docs/en/build-with-claude/context-editing)) | `clear_tool_uses_20250919`: `trigger` (default 100k), `keep` (3), `clear_at_least`, `exclude_tools`, `clear_tool_inputs`. `clear_thinking_20251015` | no | **explicitly breaks it.** "Invalidates cached prompt prefixes when content is cleared". `clear_at_least` exists to decide "if context clearing is worth breaking your prompt cache" | docs | — |
| **Anthropic compaction** ([threshold](https://platform.claude.com/docs/en/build-with-claude/compaction-threshold), [on-demand](https://platform.claude.com/docs/en/build-with-claude/compaction-on-demand)) | `compact_20260112` (trigger default 150k, minimum 50k) or `compaction:{type:"summarize"}`. Returns a **readable** summary block, which can carry `cache_control`. Costs an extra sampling step. Images and documents in the summarised range are lost | no (custom `instructions`) | yes: the docs recommend a breakpoint after the system prompt | docs | — |
| **Anthropic memory tool** ([docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool)) | Client-side `/memories` file operations; meant to pair with editing or compaction | — | — | docs | — |
| **Claude Code auto-compact** ([docs](https://code.claude.com/docs/en/prompt-caching)) | Window configurable (`autoCompactWindow`, env var). Compaction "invalidates the conversation layer" while system and project layers stay cached. The summary request reuses the warm prefix | — | yes ("cache-safe fork", [blog](https://claude.dev/blog/lessons-from-building-claude-code-prompt-caching-is-everything/)) | docs | default threshold percentage not published |
| **OpenAI** ([compaction](https://developers.openai.com/api/docs/guides/compaction)) | `context_management:[{type:"compaction", compact_threshold}]` and stateless `/responses/compact`. The item is **opaque** (`encrypted_content`). `truncation:"auto"` drops items from the beginning and is now deprecated | — | undisclosed | docs | billing and model list undisclosed. GPT-5.1-Codex-Max "natively trained" across windows [VENDOR] |
| **Gemini** | No request-side compression. The Live API has a `slidingWindow` `contextWindowCompression` with undocumented drop rules | — | — | — | — |

What the field converged on independently:
- Protect the system prompt and recent turns (DietCode, LiteLLM, Headroom, Morph's defaults).
- Leave the cached prefix byte-identical (DietCode, Headroom, Claude Code).
- Make removal reversible with a fetch tool (DietCode, LiteLLM, Headroom, Compresr).
- Treat tool output, not instructions, as the main target (Compresr, Headroom, Anthropic context editing).

None publishes an instruction-retention or negation-preservation evaluation.

## A.3 Failure-mode catalogue

| Failure | Evidence | Mechanism | Hydra control |
|---|---|---|---|
| **Negation and modality loss** | No quantitative flip rate published for any prompt compressor (gap). Summarisation typologies list negation errors ([FRANK](https://arxiv.org/abs/2104.13346), [SummaC](https://arxiv.org/abs/2111.09525)). [COMA](https://arxiv.org/abs/2510.22963) builds its guardrail set from "don't" / "under no circumstances" sentences and strips them | "not" and "never" are short, frequent, low-surprisal tokens, exactly what perplexity- or self-information-based deletion removes first. This is an inference, consistent with AGORA's finding about low-information action tokens | Sentences with negators or modals are protected whole (§A.7.3); coverage check 4 (§A.7.6) |
| **Numbers, units, IDs, identifiers corrupted** | LLMLingua README demo: "Sam bought 1 boxes x00 oflters", "12 * 300ters", "3030 highlighters". Paper Fig. 10: GADGET-2 → "GGET2". `force_reserve_digit=False` by default. [AGORA](https://arxiv.org/abs/2605.26596): identifiers and brackets deleted first. ACON: "losing a single detail such as a file path or an API parameter can derail the entire workflow" | Sub-word deletion splits and fuses digit tokens | Number, identifier and quote spans protected; coverage check 3 rejects any *new* number or identifier |
| **Instruction and constraint deletion** | LLMLingua §5.4: instructions and questions are the most compression-sensitive. COMA system-prompt corruption, 0.44–0.63 success across SC, LLMLingua, LLMLingua-2 and small summarisers. Inworld and Kong compress system or user text by default | Instructions are short and generic, so they score as low-information | System and developer messages and the latest user turn are never compressed; instruction-shaped sentences in history are protected |
| **Lost in the middle, position effects** | [Liu et al.](https://arxiv.org/abs/2307.03172) (TACL): GPT-3.5 multi-doc QA drops by more than 20 points mid-context, below closed-book. LongLLMLingua Table 1: 75.7 at position 1 vs 54.1 at position 10. Provence misses needles at the edges. [Chroma "Context Rot"](https://www.trychroma.com/research/context-rot) (technical report): 18 models degrade with length; focused about 300-token prompts beat about 113k full prompts on LongMemEval | Attention allocation, plus compressor-specific blind spots | This argues *for* removing distractors, but a removal must not move surviving content, since reordering changes what the model attends to. The ladder never reorders. OpenRouter's middle-out deletes exactly the region attention already neglects, so failures there are invisible |
| **Multi-hop facts split across chunks** | RECOMP-extractive on 2WikiMQA: 16.1 → 11.2 EM (8B), 20.8 → 13.8 (70B). RECOMP-abstractive is 67% faithful on HotpotQA. xRAG HotpotQA 38.79 → 34.05 | Relevance is scored per sentence against the query, but the bridge entity is not in the query | Coverage check 2 keeps *every* sentence containing a query atom. A bridge entity not in the query is the documented residual risk that the A/B must measure |
| **Tool-call argument and schema damage** | AGORA: 17 of 17 cells collapse. ACON: LLMLingua on AppWorld 56.0 → 39.3 (history) and 32.1 (observations). Inworld warns on JSON, XML and code | Token deletion breaks brackets, keys and verbs | Tool schemas, tool-call arguments and the latest K tool results are protected; JSON compaction is structural (rung 2), never token-level |
| **Safety-instruction removal and injection** | [COMA](https://arxiv.org/abs/2510.22963): up to 32 attacker tokens in *untrusted* input raise attack success to 0.71 overall, against 0.21 for the best non-compression baseline. Defence finding: "isolating trusted inputs from untrusted inputs during compression improves robustness." Separately, deletion can *create* an injection phrase ("do not ignore previous instructions" minus "not") | The compressor scores trusted and untrusted text jointly, so untrusted text can shift what is deemed important | Trusted segments are never inputs to scoring; only untrusted tail segments are scored, each against the query alone. `policy.InjectionMarker` re-run on the output (coverage check 5). Fences are atomic |
| **Summarisation hallucination** | [FRANK](https://arxiv.org/abs/2104.13346): 60% of annotated summaries had at least one error (2020-era models; an upper reference). RECOMP Table 4: GPT-3.5 summaries 74% faithful on HotpotQA. MemGPT summary-only baseline 32.1% vs 92.5%. ACE "context collapse" | Rewriting adds content | Rung 4 off by default; when on, the summary must pass `ground.Check` against the replaced text (no new number or symbol), and the original is vaulted |
| **Fluent but wrong** | LLMLingua Fig. 10: "perturbed Lemaître-Lemaître metric". AGORA: "grammatical English but ungrammatical agent commands". COMA: benign compression on SQuAD already fails **60% of previously correct items at 20% kept, 29% at 40% kept** | Downstream model papers over gaps | Nothing lexical catches this in general; this is why the gate in §A.7.7 measures pass-rate delta against a verifier rather than trusting coverage |
| **Behavioural side effects** | Selective Context: refusals 0 → 27/200, answers about 35% shorter. Trajectories 13–15% longer under summaries (Complexity Trap) and 41% more steps under ACON | Missing context reads as uncertainty or as an unfinished task | Track refusal rate, output length, turn count and `hydra_fetch` rate per strategy; any of these regressing blocks promotion |
| **Cross-lingual collapse** | [2608.26175](https://arxiv.org/abs/2608.26175) (single-author preprint): at 33% kept, English retains 57–62% of context utilisation, Lithuanian 10–24%, Chinese "essentially none". XProvence v2 "empties 92% of Chinese contexts … without any warning" | Compressors trained on English | Compression off unless `language.script` is Latin and the class's A/B was run in that language |
| **Isolation bugs in shipped systems** | DietCode #16 (retrieval id resolves to the summary) and #17 (summaries reused across conversations sharing a prefix) | Session keying | The vault is keyed by session *and* content hash; handles are never resolved across sessions |

## A.4 Why token-level deletion is excluded

Four independent lines of evidence, any one of which would be enough for a gateway whose worst failure is a dropped constraint:
1. Number and identifier corruption is visible in the reference implementation's own demo output, and digit preservation is off by default.
2. Agent action grammar collapses: AGORA (17/17 cells) and ACON (LLMLingua 56.0 → 39.3 / 32.1).
3. The method is adversarially steerable through untrusted input (COMA, 0.44–0.63 system-prompt corruption).
4. The one gateway product built on it (Kong) is not query-aware and compresses user messages by default.

The fast variant also needs a GPU-class sidecar for a 355M encoder, which the single-binary deployment model in RFC §5.3 does not have. Sentence-granularity removal gives up some ratio and keeps sentences intact, which is what makes protected-span and coverage checks possible at all.

## A.5 Evaluation protocol: "nothing important lost"

No single benchmark answers the question, so the protocol layers four kinds of test, from cheapest to most decisive.

1. **Planted-atom retention (unit level, every build).**
   - A generated corpus: real prompts from the replay store (§A.7.11) with planted atoms in history, roughly 10⁴ prompts.
   - Planted atoms include constraints ("never call `drop_table`", "at most 3 retries", "respond in French"), numbers with units, IDs, negated facts, and a fact split across two distant messages.
   - Metric: the fraction of planted atoms present after compression, by exact match.
   - Gate: **100% for protected categories**; other categories are reported.
   - This is deterministic and needs no model.
2. **Needle and long-context suites (model level).**
   - [RULER](https://arxiv.org/abs/2404.06654), multi-key, multi-value and variable-tracking variants, not single NIAH. Plain NIAH is "nearly perfect" while only half of 17 models are satisfactory at 32k, so NIAH alone is a weak gate.
   - [LongBench](https://arxiv.org/abs/2308.14508) and [LongBench v2](https://arxiv.org/abs/2412.15204). The synthetic subset is where LLMLingua-2 lost most (37.8 → 21.3), so report it separately.
3. **Instruction retention under compression.**
   - [IFEval](https://arxiv.org/abs/2311.07911): about 500 prompts and 25 verifiable instruction types, with programmatic checkers.
   - Its prompts are short, so each is embedded in 4k–32k tokens of realistic history (tool outputs, earlier turns). The instruction is placed in (a) the system prompt, (b) an early user turn, (c) the latest turn.
   - Metric: IFEval strict accuracy, uncompressed vs compressed, paired.
   - Case (b) is the one only the instruction-shaped-sentence protection covers.
   - No published study does this. It is the test closest to the user's requirement.
4. **Faithfulness of what was sent.** These apply only to abstractive rungs.
   - NLI entailment of each removed-or-rewritten span by the compressed text: [SummaC](https://arxiv.org/abs/2111.09525), 74.4% balanced accuracy on its benchmark, which is imperfect; or [AlignScore](https://arxiv.org/abs/2305.16739), 355M, MIT.
   - QA-based preservation, [QAGS](https://arxiv.org/abs/2004.04228)-style: generate questions from the original, answer them from the original and from the compressed text, and score the agreement.
   - These are *sources with their own se/sp*, calibrated like any verifier (RFC §5.2.3), never gates by fiat.
5. **Downstream task delta (decisive).**
   - Paired pass-rate difference Δ on the class's own verifier, with interval and non-inferiority bound (§A.7.7).
   - Also report: cost per accepted answer, turns per task, refusal rate, output length, `hydra_fetch` rate.

**Building a per-task-class benchmark.**
- Take verified requests of the class from the replay store.
- Stratify by prompt length and by position of the decisive evidence.
- Add planted-atom variants.
- Run each item uncompressed and under each (strategy, bucket) on the same head with the same seed and temperature 0 where supported.
- Score with the class verifier.
- Report Δ with a paired bootstrap interval.
- The A/B unit is the item, never the token.

## A.6 Economics

Prices per MTok, read 2026-10-29 from [Anthropic](https://platform.claude.com/docs/en/about-claude/pricing), [OpenAI](https://developers.openai.com/api/docs/pricing) and [Gemini](https://ai.google.dev/gemini-api/docs/pricing):

| Model | Base input | Cache write | Cache read | Notes |
|---|---|---|---|---|
| Claude Sonnet 5.5 | $2.00 | $2.50 (5 min, 1.25x) / $4.00 (1 h, 2x) | $0.10 (**0.05x**) | Opus 5.5 also 0.05x; Fable/Mythos 5.1 0.025x; others 0.1x |
| Claude Sonnet 4.6 | $3.00 | $3.75 / $6.00 | $0.30 (0.1x) | |
| Claude Haiku 5.5 | $0.10 (≤ 100k prompt) / $0.50 (> 100k) | $0.125 / $0.20 (≤ 100k) | $0.01 / $0.05 | the > 100k rate applies to **every** token of the prompt |
| gpt-6.1-sol | $2.00 | $2.50 (30 min) / $5.00 (12 h) | $0.10 (0.05x) | GPT-5.6 and later charge cache writes |
| gpt-5.4 | $2.50 | none | $0.25 | pre-5.6: no write charge (UNVERIFIED against raw page) |
| Gemini 2.5 Pro | $1.25 (≤ 200k) / $2.50 | explicit cache storage $4.50 per MTok-hour | $0.125 / $0.25 | implicit caching; the 90% discount is inferred from the table |

Cache mechanics that matter here:
- **Anthropic** ([prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)):
  - Prefix order is tools → system → messages, and "changes at each level invalidate that level and all subsequent levels".
  - At most 4 breakpoints.
  - Minimum cacheable length is 512–4,096 tokens depending on the model; shorter prompts silently do not cache.
- **OpenAI** ([prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)):
  - Caching is automatic from 1,024 tokens.
  - Requests are routed by a hash of the initial tokens.
  - `prompt_cache_key` influences routing but does not guarantee hits.

**Worked example.** A 100k-token request: an 80k cached prefix P and a 20k uncached tail T, compression keeping 50%, compressor cost excluded. The tail is billed as plain input; if the client moves the breakpoint to the end each turn, it is billed as a write instead and every figure rises by about 20%.

| Model | (i) no compression, prefix hit | (ii) compress everything 50%: prefix miss + rewrite | (iii) compress the tail only |
|---|---|---|---|
| Sonnet 5.5 | 80k × $0.10 + 20k × $2.00 = **$0.048** | 40k × $2.50 + 10k × $2.00 = **$0.120** (2.5x worse) | $0.008 + $0.020 = **$0.028** (−42%) |
| Sonnet 4.6 | $0.024 + $0.060 = **$0.084** | $0.150 + $0.030 = **$0.180** (2.1x worse) | $0.024 + $0.030 = **$0.054** (−36%) |
| gpt-5.4 | $0.020 + $0.050 = **$0.070** | 50k × $2.50 = **$0.125** (1.8x worse) | $0.020 + $0.025 = **$0.045** (−36%) |

**Break-even rules.** Let ρ be the read multiplier, w the write multiplier (1 with no write fee), r the fraction removed, and k the compressor cost per original token as a fraction of the base input price.

1. **Re-compressing a warm prefix, first request.**
   - It pays only if r > 1 − (ρ − k)/w.
   - With k = 0, that is r > 0.92 at (ρ = 0.1, w = 1.25); r > 0.96 at (0.05, 1.25); r > 0.95 at (0.1, 2.0); r > 0.98 at (0.05, 2.5).
   - No method in §A.1 removes 92–98% without large loss. **Hydra never edits a cached prefix.**
2. **If the compressed prefix is then byte-stable and cached**, each later hit saves r·ρ·P.
   - The one-time penalty is repaid after N > ((1 − r)·w − ρ)/(r·ρ) further hits within the TTL.
   - That is 11 hits at (r = 0.5, w = 1.25, ρ = 0.1) and 23 at ρ = 0.05.
   - So it can pay eventually, but only for a long-lived, stable prefix, and only by about 0.1–0.2 cents per 80k per hit.
3. **Compressing at prefix creation**, before the first write, always saves r·(w + Nρ)·P minus the compressor cost. This is what DietCode, Headroom and Claude Code do: fold history once, then freeze the folded form. §A.7.8 adopts it.
4. **Query-aware compression of a prefix is a permanent miss.** The output changes with every query, so it never caches. Per request it loses unless r > 1 − ρ: above 90% at ρ = 0.1, above 95% at ρ = 0.05. Query-aware rungs apply to the tail only.
5. **Price-tier cliffs are the one place whole-prompt compression wins outright.**
   - Haiku 5.5 is 5x above 100k tokens. A 110k prompt costs 110k × $0.50 = $0.055; compressed to 99k it costs $0.0099.
   - Gemini Pro is 2x above 200k, and OpenAI 2x above 272k.
   - The plan computes this explicitly, but it does not relax any safety rule to reach a cliff.

**Where the money is.**
- Agent workloads are input-dominated. Manus reports an input-to-output ratio around 100:1 and calls the KV-cache hit rate "the single most important metric" ([blog](https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus), self-reported).
- Cache reads are 96% of input token volume in a Claude Code repair study ([2511.11012](https://arxiv.org/abs/2511.11012)), and dominate dollar cost across 8 models on SWE-bench Verified ([2604.22750](https://arxiv.org/abs/2604.22750)).
- Illustration: with 96% of input tokens read at 0.05x and 4% written at 1.25x, reads are about half of input dollars ((0.96 × 0.05)/(0.96 × 0.05 + 0.04 × 1.25) ≈ 49%).
- **The lever is therefore the *size of the stable prefix that is re-read every turn*, not the per-request prompt.** It is shrunk only by folding once (rule 3) and by not letting stale tool output enter the prefix in the first place (rung 1c applied at the moment a tool result ages out, after which the replaced form is frozen).

**Compressor latency is not free.**
- LLMLingua-2 takes 0.4–0.5 s per MeetingBank prompt on a V100; LLMLingua 1.5–2.9 s; Selective Context 15.5–15.9 s; LongLLMLingua about 3.9 s per sample; RECOMP-abstractive about 10 s per sample (Provence Table 2, 50 samples).
- Kummer et al. find the end-to-end gain disappears outside a narrow window.
- Rungs 1–2 are string operations (microseconds to milliseconds). Rung 3 with in-memory BM25 is milliseconds; with an embedding call it is one local model round trip. Rung 4 is a full LLM call.
- Added p95 latency is a promotion criterion alongside Δ.

## A.7 Design for hydra

### A.7.1 What the code already gives us, and what it does not

| Package | Reused for | Gap found while reading it |
|---|---|---|
| `internal/ground` | `tokenize` and `claimKind` are a measured, deterministic detector for numbers (≥ 2 digits), paths, flags, dotted/slashed and camelCase symbols. They become the number/identifier half of the protected-span detector and the coverage check. | Unexported. `claimKind` deliberately skips 1-digit integers ("three of the four"), which is right for grounding and wrong for protection: "retry at most 3 times" must be kept. Protection needs its own, wider rule. |
| `internal/retrieve` | BM25 (`Index.Lexical`) and RRF (`Fuse`) for query-aware sentence scoring on rung 3, with no model required. `Tokenize` splits camelCase, which is what scoring wants. | `Index` is a persistent on-disk store with a byte budget. Per-request scoring needs an in-memory index over one prompt's sentences; `score`/`idf` are unexported. Export a `retrieve.Rank(query string, docs []string) []Result` that runs BM25 in memory. |
| `internal/payload` | Content-addressed storage for originals, so a pointer can name exactly what was removed. | Four properties make it unfit as-is for reversibility: it is **off by default**; it **redacts before hashing**, so a fetched original is not the original; it **evicts oldest packs** by byte budget, so a handle can dangle mid-session; and `KeepProb < 1` admits sampling. See §A.7.5. |
| `internal/policy` | `Redact`, `DetectPII`, `InjectionMarker`. | Run once on the original in `Dispatch` (`dispatch.go:585`). Deletion can *create* an injection phrase ("do **not** ignore previous instructions" minus one token), so the marker scan must also run on the compressed text. |
| `internal/util` | `WrapUntrusted`/`Unwrap` fences. | The fence nonce is SHA-256 of the fenced content (`untrusted.go:30`). Compressing inside a fence breaks the nonce, `Unwrap` stops recognising the span, and `ground.Check` returns `ErrNoContext`. Either compress before wrapping or re-wrap after; never edit inside a fence. |
| `internal/entropy` | `SignalDensity` (gzip ratio) is a cheap, query-independent trigger: a low ρ says the window is redundant, which predicts that rung 1 (dedup) will pay. | Not on the dispatch path today (only `cmd/hydra` imports it). It says nothing about *importance*; it must never select what to delete. |
| `internal/trust` | Calibration per `(source, domain)`; SPRT. A compressed attempt becomes its own source key, so its se/sp is learned, not assumed (RFC §6). | Keys are flat strings; a new key starts at se = sp = 0.5. Without a prior borrowed from the uncompressed head the compressed key would be refused for want of evidence for a long time (§A.7.7). |
| `internal/evalset` | Verified `(task, head, passed)` rows, i.e. the labels for the A/B. | Stores `TaskHash` and the *candidate*, **not the prompt**. The evalset alone cannot be replayed under compression; a replay corpus needs the prompts (§A.7.10). |
| `internal/ope` | `Evaluate` with bootstrap intervals and an ESS floor for "what would compression-on have scored". | Needs propensities: compression must be randomised (an explore rate on the transform), or the counterfactual is unidentifiable exactly as `ErrInsufficientSupport` says. |
| `internal/cache` | Untouched. | Must key on the **original** prompt. `cache.Normalize` collapses all whitespace (`match.go:23`), which is fine for a hash and wrong as a transform: it would break Python, YAML and Markdown tables. Do not reuse it as rung 1. |
| `internal/serve`, `executor` | The ingress and the per-head request. | `executor.Message.Content` is a `string` (`tools.go:20`), so content-part arrays (and any client `cache_control`) are not representable. No executor sends `cache_control`, and `anthropicUsage` reads only `input_tokens`/`output_tokens` (`anthropic_stream.go:47`): cache reads and writes are invisible to cost logging. `pricing.ModelPrice` has only input and output rates. **Hydra cannot currently compute the economics in §A.6, so it cannot decide whether compression pays.** |
| `internal/dispatch` | The per-candidate loop (`dispatch.go:721`) calling `executor.Stream` (`:880`) is the insertion point: compression depends on the head (tokenizer, window, provider cache). | Policy, cache and egress classification (`:585–620`) run on the original and must stay before compression. |

### A.7.2 Data flow

```
serve.chat ──► Dispatch(prompt, opts)
                 │ policy.Evaluate, cache lookup, egress.provenance   (on the ORIGINAL, unchanged)
                 │ selectHeads / constrain
                 ▼
         for each candidate h:
                 │ plan := compress.Plan(req, h, classStats)        ── off? → send original
                 │   1. segment: system | tools | history[i] | tool_result[i] | latest_user
                 │   2. protect: mark protected spans (§A.7.3)
                 │   3. cache boundary: bytes [0, B) frozen (§A.7.8)
                 │   4. ladder: apply enabled rungs to the tail only (§A.7.4)
                 │   5. vault originals of every removed span (§A.7.5)
                 │   6. coverage := compress.Cover(orig, out, query)  ── fail → send original
                 │   7. policy.InjectionMarker(out) must not newly match
                 ▼
         executor.Stream(compressed req + hydra_fetch tool)
                 │ if the head calls hydra_fetch(handle): answer from the vault, loop
                 ▼
         verify (oracle / ground against ORIGINAL) → trust.Update(source = h.ID+"+c:"+strategy)
                 │ fail → next candidate with compression OFF
                 ▼
         response + X-Hydra-Compress-* headers + hydra.compression metadata
```

Two rules fall out of the flow and are not negotiable:
- **An escalation after a failed verification sends the uncompressed prompt.** Otherwise the cascade cannot tell "the model was wrong" from "the compressor removed the evidence", and the second source's evidence is confounded with the first's.
- **Verifiers judge against the original.** `ground.Check(answer, original)` is the truth reference. `ground.Check(answer, compressed)` is run as well; a claim present in the original, absent from the compressed text and not fetched through the vault is a *compression-induced guess*, which is logged as a coverage miss even when the answer was right.

### A.7.3 Protected spans

Protection is decided by deterministic rules, never by the compressor's own scores. A span that is protected is passed through byte-identical; the compressor sees it only as an opaque placeholder and cannot delete, reorder or rewrite it.

| Protected | Deterministic detection |
|---|---|
| System and developer instructions | `Message.Role ∈ {system, developer}`, and `opts.System`. Whole segment. |
| The latest user turn | The last `Message` with `Role == "user"`. Whole segment. |
| Tool schemas | `opts.Tools` (never part of `Messages`; untouched by construction). |
| Tool-call arguments | `Message.ToolCalls[*].Function.Arguments`. Whole value. |
| The most recent `K` tool results (default 1) | By position among `Role == "tool"` messages. |
| Numbers and units | Regex over the tail: `[-+]?\d[\d,_]*(\.\d+)?([eE][-+]?\d+)?\s?(%|[a-zA-Zµ°]{1,6})?` plus the token that precedes a number when it is a comparator (`≤ ≥ < > at most at least no more than exactly`). Unlike `ground.claimKind`, single digits count. |
| IDs and code identifiers | `ground.claimKind` → `KindSymbol` (paths, flags, dotted, camelCase, `snake_case`), plus UUID, hex ≥ 7 chars (commit SHAs), `[A-Z]{2,}-\d+` (ticket keys), email, URL. |
| Quoted strings and code | Anything inside `"…"`, `'…'`, `` `…` `` and fenced code blocks. Rungs 2–4 never edit inside a fence; rung 1 only drops an *entire* duplicate fence. |
| Negations and modals | A sentence containing any of `not, no, never, n't, none, nor, without, except, unless, only, must, shall, should, may, might, required, forbidden, prohibited, do not, don't, cannot, avoid, ensure, always` is protected **as a whole sentence**. Token-level deletion of a negator flips meaning, so the unit is the sentence, not the word. |
| Instruction-shaped sentences anywhere | Imperative sentence start (verb-first after optional "please"), or a sentence with "you", "your", "the assistant", "the model". Coarse on purpose: false positives cost tokens, false negatives cost correctness. |
| Verifier-referenced content | `response_format` / JSON schema field names and enum values; the `match` verifier's label set; anything a route declares in `compress.protect` (regex list). |
| Untrusted fences | `util.Unwrap` spans are atomic: kept whole or replaced whole by a handle (rung 1 only), never edited inside (§A.7.1). |
| PII-bearing spans | `policy.DetectPII` hits. Kept inline, never vaulted (the vault would store a redacted copy) and never summarised (a summary can launder it). |

Protected-span detection is itself tested: a corpus of instructions with planted constraints ("never call `delete_user`", "reply in fewer than 120 words", "the limit is 2,048 tokens", "do not use the `requests` library") must yield 100% protection recall before any rung ships. Precision is reported but not gated.

### A.7.4 Strategy ladder

| Rung | Transform | Loses information? | Default |
|---|---|---|---|
| 0 | **Off.** Send the original. | no | **on** for every class without a measured result |
| 1a | **Exact dedup.** Replace a byte-identical repeat of a span ≥ 256 B (a re-read file, a repeated tool result) with `[see ⟨handle⟩, identical to message #i]`. | no (identical content still present once) | **on** after shadow, for all classes |
| 1b | **Whitespace, conservative.** Trailing spaces, CRLF→LF, runs of ≥ 3 blank lines → 2. Never inside fences, never leading indentation. | no | **on** after shadow |
| 1c | **Pointer-replace.** A stale tool result (older than the last `K`) becomes `[tool result ⟨handle⟩: <name>, <n> lines, <first line>; call hydra_fetch to read]`. | reversible, not lossless: the model must choose to fetch | **on** for `conversation.tool_loop` classes after the A/B; off otherwise |
| 2 | **Deterministic tool-output compaction.** Rule-based, per content type: collapse repeated log lines with counts, keep head/tail of long listings, strip ANSI and progress bars, keep every line matching error/warn/fail/panic/assert/exception, keep JSON keys and truncate long arrays with `…(+n items, handle)`. No model. | yes, by rule, and the rule is inspectable | **off**; per class only after the A/B passes |
| 3 | **Query-aware extractive pruning.** Score unprotected sentences of the tail against the latest user turn with in-memory BM25 + optional dense cosine, fused with RRF (`internal/retrieve`); drop the lowest until the target ratio; dropped sentences are vaulted. Never reorders. Sentence granularity only: token-level deletion (the LLMLingua family) is excluded from every rung, for the reasons in §A.4. | yes | **off**; opt-in per class, never for `creative`, `extraction` or `code` without a passing A/B |
| 4 | **Abstractive summarisation.** A small model rewrites old history into a summary. | yes, and can introduce content that was never there | **off**; never default; allowed only for history older than the last `N` turns, only when the summary passes `ground.Check` against the text it replaces (every number/symbol in the summary must appear in the source), and only on classes where the A/B passed at the stricter bound in §A.7.7 |

Rationale for the ordering: rungs 0–1b cannot change meaning; 1c cannot lose data but can lose *attention* (the model has to know to fetch); 2 loses data by a fixed, auditable rule; 3 loses data by a learned or statistical score; 4 can add false data. The safety argument for each rung is strictly weaker than for the one above it, so the evidence demanded rises with it.

### A.7.5 Reversibility: the vault and `hydra_fetch`

Every span removed by rungs 1c–4 is stored before the request is sent and named by a handle the model can resolve.

- **Store.** A new session-scoped store, `compress.Vault`, keyed by `sha256(original span)`, holding the **unredacted** span in memory (gateway mode: Postgres row with TTL) for the lifetime of the conversation. Not `internal/payload` as it stands, for the four reasons in §A.7.1. `payload.PutSegments` still records the original prompt for the trace when capture is on, exactly as today.
- **Fail closed.** If the vault cannot store a span, the span is not removed.
- **Tool.** When any span was vaulted and the head `CanUseTools`, Hydra appends one tool definition:
  ```json
  {"type":"function","function":{"name":"hydra_fetch",
   "description":"Return the full original text that was elided and replaced by a handle like ⟨h:3f2a91⟩.",
   "parameters":{"type":"object","properties":{"handle":{"type":"string"}},"required":["handle"]}}}
  ```
  Hydra answers `hydra_fetch` calls itself and loops, without returning them to the client. The client never sees the tool.
- **Heads without tools** (CLI heads, Replicate): rungs 1c, 3 and 4 are disabled for them, since the content is unreachable.
- **Appending a tool changes the tools block**, which sits first in Anthropic's cache prefix order (tools → system → messages). The definition is byte-constant and is added on every request of a session once compression is enabled for its class, never toggled per request, or the whole prefix is invalidated each time.
- **Fetch rate is a measured signal.** A high fetch rate means the compressor is removing what the model needs; a fetch that returns content later cited in the answer is a coverage miss the pre-send check did not catch.

### A.7.6 The pre-send coverage check

`compress.Cover` runs after the transform and before the send. If it fails, the original is sent and the failure is logged; it never "repairs" the compressed text.

1. **Protected spans are present, byte-identical, in order.** Every span marked in §A.7.3 is found by exact substring search in the output, in the original order.
2. **Query-relevant atoms survive.** Extract atoms from the latest user turn: numbers, identifiers, quoted strings, and content words after stopword removal (`cache`'s stopword list). For each atom, every sentence of the original that contains it must be present in the output, or be vaulted and represented by a handle placed where the sentence was. This is the multi-hop guard: a query atom that appears in three places keeps all three.
3. **No new atoms.** The output contains no number or identifier that the original lacks (`ground.Check(output, original)` with the output as the "answer"). This is what catches a rung-4 summary that invented a figure, or a rung-3 deletion that fused "1" and "024" into "1024".
4. **Negators survive per sentence.** For every surviving sentence, its negator set equals that of the original sentence (sentence-aligned diff). A sentence that lost a negator fails the check.
5. **No new injection marker.** `policy.InjectionMarker` on the output matches nothing that it did not match on the original.
6. **Ratio sanity.** Achieved ratio within `[0.5×, 1.5×]` of the target; anything else means the transform misbehaved.

Coverage is necessary, not sufficient. It catches deletions of things that are lexically detectable. It does not catch a deleted sentence whose importance is semantic ("the second approach was abandoned"). That residual is what the quality gate in §A.7.7 measures.

### A.7.7 The quality gate

Compression is a transform on the input of a source. Its effect on P(pass) is measured per `(task class, strategy, ratio bucket)` like any other source property, and nothing is enabled because a paper or vendor reported a number.

**Calibration key.** A compressed attempt calibrates under `source = h.ID + "+c:" + strategy + "@" + bucket` (bucket ∈ {0.9, 0.7, 0.5, 0.3} of tokens kept). It shares `Family` with `h`, so SPRT's correlation discount applies between a compressed and an uncompressed vote from the same model. Its prior borrows from `h`'s cell for the same class with at most `rank.MinCommitments` pseudo-observations, the bound `EffectiveScoreIn` already uses, so it neither starts at 0.5 nor inherits the uncompressed head's record wholesale.

**Stages, per (class, strategy, bucket):**
1. **Shadow.** The original is sent and answered as today. The compressor runs, the coverage check runs, and the plan is logged (removed spans, ratio, coverage verdict, tokens that would have been saved). No user-visible effect. Exit: coverage failure rate measured on ≥ 500 requests, and protected-span recall = 100% on the planted corpus.
2. **Offline A/B on the replay corpus** (§A.7.10): each item run uncompressed and compressed against the same head with the class's verifier. Paired design, so the comparison is per item.
3. **Online randomised A/B.** With probability `compress.explore_rate` (default 0.05) a request in an eligible class is compressed; the propensity is written on the dispatch row (`act_prob`) so `internal/ope` can evaluate "compression on for this class" counterfactually.
4. **Promotion** requires a non-inferiority decision on the paired pass-rate difference Δ = P(pass | compressed) − P(pass | original):
   - Bound: the one-sided 95% lower bound on Δ must be above −δ, with δ = 0.01 by default and δ = 0.005 for `extraction`, `classification` and any class with `DefectCostUSD` above a route threshold.
   - Test: McNemar on the discordant pairs for the offline corpus; for the online stream, a Wald SPRT on the discordant pairs with H₀: p(compressed fails | discordant) = 0.5 + δ′ versus H₁: = 0.5, α = β = 0.05. Discordant pairs are what carry the information; concordant ones are discarded, which keeps the test cheap.
   - Sample size, stated so nobody is surprised: detecting a 1-point drop at a 90% baseline needs on the order of 10³–10⁴ paired items per (class, strategy, bucket). Classes without that traffic stay at rung 0–1, which is the intended outcome.
5. **Automatic rollback.** Once promoted, the online SPRT keeps running on the explore slice. If it accepts "worse by ≥ δ", or the coverage-failure rate or `hydra_fetch` rate rises above twice its shadow baseline, the strategy is demoted to shadow for that class and `hyctl trust` shows why.

**Response contract**, extending RFC §5.4:

```
X-Hydra-Compress:          off | shadow | applied | fallback
X-Hydra-Compress-Strategy: dedup,ws,pointer          # rungs actually applied, in order
X-Hydra-Compress-Ratio:    0.62                      # tokens sent / tokens in the original
X-Hydra-Compress-Removed:  spans=14 tokens=18233 vaulted=14
X-Hydra-Compress-Coverage: pass | fail:<check>       # §A.7.6 result; fail implies fallback
X-Hydra-Compress-Basis:    measured | shadow | unmeasured
X-Hydra-Compress-Fetches:  2                         # hydra_fetch calls answered
```

The body's `hydra.compression` object lists every removed span as `{handle, segment, rung, bytes, sha256}`, so a caller can audit exactly what the model did not see.

### A.7.8 Prompt-cache-aware placement

- **Freeze the prefix.** The cacheable prefix is everything up to the last cache breakpoint the client set or Hydra would set: tools, system, and all history before the most recent `M` messages. Compression applies only to bytes after it.
- **Compress once, then freeze the result.** A history span compressed on turn *t* is replaced by the same bytes on turn *t+1*: the plan is memoised per `(session, span hash, strategy)` so the compressed history becomes the new stable prefix. Re-compressing on each turn (different query, different extraction) would produce a different prefix and a cache miss every turn.
- **Executor support needed.** Emit `cache_control` on the Anthropic executor at the frozen boundary; send `prompt_cache_key` on OpenAI; read `cache_read_input_tokens`/`cache_creation_input_tokens` and `prompt_tokens_details.cached_tokens` into `executor.Response`; add `CacheReadPerMillion`/`CacheWritePerMillion` to `pricing.ModelPrice` (OpenRouter's catalogue publishes `input_cache_read`/`input_cache_write`). Without these the gate in §A.7.9 has nothing to compute with.

### A.7.9 When compression is off

Compression is off for a request when any of these holds:

- the class has no `measured` result for the strategy (`X-Hydra-Compress-Basis` would be `unmeasured`); rungs 1a/1b are the only exception, after shadow;
- the uncached tail is under `min_tail_tokens` (default 4,000): the saving cannot cover the coverage check and the risk;
- the expected saving is negative: `saved_tokens × p_in < compressor_cost + p_in × E[fetch tokens] + Δcache`, where `Δcache` is the cache-write premium on any prefix bytes the transform would change (§A.6);
- the class is `creative`, `translation`, `legal`, `medical`, or any class the route marks `compress: never`, because wording itself is the content;
- the head cannot use tools and the plan needs rungs 1c, 3 or 4;
- the request carries `X-Hydra-Compress: off`, or a `response_format` schema whose fields cannot be mapped to protected spans;
- the task target is above 0.99, or the route uses `stream_unverified` (no verifier means no evidence that compression did no harm);
- the client already compacts: the request carries an Anthropic `compaction` block or `context_management`, an OpenAI compaction item (`encrypted_content`), or a client-side summary marker. Compressing a summary compounds two lossy steps nobody measured together, and an opaque item cannot be inspected at all;
- the request is under a provider's minimum cacheable length *and* the class has no measured result: nothing to protect, nothing measured, no reason to act.

### A.7.10 Go surface

```go
package compress

// Segment is one addressable unit of a request. Protected segments are
// passed through byte-identical.
type Segment struct {
	Kind      Kind   // System, Tools, User, Assistant, ToolResult, LatestUser
	Index     int    // position in Messages
	Text      string
	Protected []Span // byte ranges no transform may touch
	Cached    bool   // inside the frozen prefix
}

type Span struct {
	Start, End int
	Reason     string // "number", "negation", "identifier", "schema", ...
}

// Transform is one rung. It edits unprotected, uncached bytes only and
// reports every removal, so Cover and the vault can account for it.
type Transform interface {
	Name() string // "dedup", "ws", "pointer", "toolcompact", "extract", "summarise"
	Rung() int
	Apply(ctx context.Context, segs []Segment, q Query, target float64) ([]Segment, []Removal, error)
}

type Removal struct {
	Segment int
	Span    Span
	Handle  string // sha256 prefix; "" for rungs 1a/1b, which lose nothing
	Rung    int
}

type Query struct {
	Text  string   // the latest user turn
	Atoms []string // numbers, identifiers, quoted strings, content words
}

// Coverage is the pre-send check of §A.7.6. A failed check is a reason to
// send the original, never something to repair.
type Coverage struct {
	Passed bool
	Failed string   // which check
	Missing []string
}

func Cover(orig, out []Segment, q Query) Coverage

// Vault keeps removed originals, unredacted, for the session.
type Vault interface {
	Put(session, text string) (handle string, err error)
	Get(session, handle string) (string, error)
}

// Plan is the decision for one candidate head: which transforms, what
// boundary, and why compression is off when it is.
type Plan struct {
	Off       string // non-empty reason when compression is off
	Mode      Mode   // Off, Shadow, Applied
	Rungs     []Transform
	Boundary  int    // index of first compressible message
	Target    float64
}

func Decide(class string, h provider.Head, req Request, st Stats) Plan
func Run(ctx context.Context, p Plan, req Request, v Vault) (Request, Report, error)
```

Config (`config.toml`):

```toml
[compress]
enabled        = false          # master switch; shadow needs it too
mode           = "shadow"       # shadow | ab | on
explore_rate   = 0.05
min_tail_tokens = 4000
keep_last_tool_results = 1
keep_last_turns = 2
delta          = 0.01           # non-inferiority margin on pass rate
vault          = "memory"       # memory | postgres

[compress.classes."agent.coding"]
rungs  = ["dedup", "ws", "pointer"]
target = 0.7

[compress.classes."creative"]
rungs  = []                     # never
```

### A.7.11 Benchmark plan

Extends RFC §7. Results go in `bench/compress/`, published whether or not they flatter.

| Class | Suite | Verifier | Compression stressor |
|---|---|---|---|
| agent.coding | SWE-bench Verified subset (50–100), full trajectories | repo tests | long tool outputs, re-read files |
| rag | LongBench v2 subset, HotpotQA/MuSiQue multi-hop | match + grounded | multi-hop atoms in separate chunks |
| extraction | CORD receipts | jsonschema + field match | numbers, units, IDs |
| instruction | IFEval, with each prompt padded by 4k–32k tokens of distractor history | IFEval's own checkers | constraints far from the query |
| retention | RULER (NIAH variants, multi-key, multi-value), planted-constraint corpus (§A.7.3) | exact match | a single fact anywhere in the window |
| safety | system prompts with explicit refusals + injected tool outputs ("ignore the above …") | judge + rule | guardrail survival, injection creation |

Per (class, strategy, bucket), report: pass rate uncompressed vs compressed with the paired Δ and its 95% interval; coverage-failure rate; `hydra_fetch` rate; tokens sent; cache-read share; **cost per accepted answer** including compressor cost and cache effects; added latency p50/p95. The headline number is Δ, not the ratio.

**Replay corpus.** A new `evalset`-adjacent store, `~/.hydra/evalset/replay/`, holding `(task_hash, full request, verifier spec)` for requests whose verdict is in the evalset. Same rules as `evalset`: outside `logs/`, no `net` imports, PII marked, opt-in. Without it, §A.7.7 stage 2 cannot run.

## A.8 Claims not verified

Each of these is either reported second-hand or contradicted by its own source. None of them supports a decision in this appendix.

**Missing evidence**
1. No published quantitative negation-flip rate for any prompt compressor.
2. No IFEval-under-compression study. The closest, CDCT ([2512.17920](https://arxiv.org/abs/2512.17920)), varies prompt length rather than running a compressor.

**Vendor numbers without a method**
3. ScaleDown: every effectiveness number ("50–70%") has no method behind it.
   - Whether `/compress/raw/` is deterministic or query-conditioned is not stated.
   - Its OSS licence is inconsistent (MIT in the README, GPL-3.0 in the repo metadata).
   - Pricing is by quote; an aggregator's $0.05/M figure is unconfirmed.
4. The Token Company: every benchmark figure, including bear-1's LongBench v2 "66%, +1.1%".
   - "Cache-safe" is asserted from determinism alone.
   - The $0.05/1M price appears only in a third-party snippet.
5. Inworld Router: the compression mechanism and its interaction with caching are not disclosed.
6. Kong: whether the service is stock LLMLingua-2; Kong publishes no evals.
7. Morph: its pages give conflicting throughput (33k / 15k / 3.3k tok/s) and context window (262K vs 1M). "98% verbatim accuracy" has no method.
8. Compresr's SEC-filings result (106K → 10.5K tokens, 72.3% → 74.5%) is found only on a third-party market map.
9. Headroom's secondary-site "60–95%". Its own benchmark is self-run, though the script and seed are published.

**Provider details not disclosed**
10. OpenRouter: whether the legacy `transforms:["middle-out"]` still works.
11. OpenAI: compaction billing, its supported models, and its cache interaction.
    - Codex auto-compact defaults are known only from community posts.
    - Pre-5.6 OpenAI prices (gpt-5.4, gpt-4.1) were not checked against the raw page.
12. Claude Code: the default auto-compact percentage is not in official docs.
13. Gemini: the implicit-cache discount (90% inferred from the price table) and the Live API sliding-window defaults.

**Venues, licences and derivations**
14. Venues for RECOMP, FILCO, MemGPT, 500xCompressor, OSCAR and COMA.
15. Licences: EXIT, CompAct, AutoCompressors, xRAG and 500xCompressor declare none, which is not the same as permissive. Selective Context has no LICENSE file.
16. LongLLMLingua's "up to 21.4%" is read here as 21.4 *points* (75.5 − 54.1, Table 1); the paper says "%".
17. Provence's quality loss is shown only in figures.
18. Chroma's LongMemEval percentages are not given in its text.
19. The sources disagree on LLMLingua-2's speed-up. The abstract says "3x–6x faster than existing methods"; LLMLingua-2's own Table 5 shows 4–6x against LLMLingua.
20. Per-1k-token compressor latency is not derivable from the published per-prompt figures.
