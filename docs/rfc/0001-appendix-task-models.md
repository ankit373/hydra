# RFC 0001, Appendix A: Task-specific models as sources

- **Status:** Draft, companion to `0001-verified-cascade-gateway.md` (§5.6)
- **Date:** 2026-10-29
- **Scope:** what task-specific models exist, what the evidence says about when they beat general LLMs, and how Hydra routes, verifies and calibrates them alongside LLMs.

Every claim below carries a source. Sources are marked **[P]** for primary (paper, official docs, model card), **[V]** for a vendor's claim about its own product, **[I]** for an independent third party, and **[S]** for a secondary aggregator. Numbers marked [V] or [S] are not measurements Hydra should rely on; §7 is how Hydra gets its own. All URLs were accessed on 2026-10-29.

---

## A.1 Summary of findings

1. **No single kind of model wins.** Who wins depends on the task, the label set and whether the model was fine-tuned:
   - **Zero-shot NER:** small span encoders are at parity with frontier LLMs. GLiNER2 (205M) averages 0.590 F1 on CrossNER against GPT-4o's 0.599 [P, 2507.18546].
   - **Zero-shot fine-grained classification:** the same encoders lose clearly. On Banking77, GLiNER2 scores 0.70, DeBERTa-NLI 0.42 and GLiClass 0.21, against GPT-4o's 0.78 [P, 2507.18546].
   - **Fine-tuned small models:** these beat zero-shot LLMs. Fine-tuned BERT reaches 93.66% on Banking77 [P via 2023.finnlp-1.7]. 4-bit LoRA fine-tunes beat GPT-4 by about 10 points on average across 31 tasks [P, 2405.00732]. Fine-tuned BERT-class models beat zero-shot GPT-4 and Claude Opus on every task tested [P, 2406.08660].

   So "task model vs LLM" is not a property of the model. It is a property of the (model, task class, label set) cell, which is what Hydra's per-class calibration already measures.
2. **"Constrained decoding model" is a product category, not a technique.**
   - ScaleDown's Classify scores a caller-supplied label set and softmax-normalises the result.
   - Its Extract returns NER-style spans with character offsets and confidences.
   - Its Summarize is generative [P, docs.scaledown.ai; I, ibl.ai].

   Constrained decoding as a technique masks logits on any autoregressive model. It guarantees form, not content: schema validity rises to 100%, but semantic errors survive [P, arXiv 2609.23742].

   The consequence for Hydra: a schema or label-set verifier has almost no discriminating power over a head whose output space is constrained by construction.
3. **The independent ScaleDown test is favourable but narrow** [I, dev.to/ishank-dev].
   - **Classification:** 96.7–97.3% on a 10-intent + OOS CLINC150 subset, against 90.7% for a local Qwen3-1.7B.
   - **Receipt extraction:** a tie (89.5% each).
   - **Long documents:** summaries failed at 369k tokens.
   - **Baseline:** it did not measure frontier-model accuracy, only frontier prices.
   - **Label limits:** ScaleDown caps labels at 20 per request (16 multi-label) [P, docs], so full CLINC150 (150 intents) and Banking77 (77) cannot be sent to it as one request.
4. **A typed "decisions" interface has emerged in 2026**, but it is not one shape:
   - **Jev-style** (`state` + a `questions` map with `choice`/`noul`/`score`): TypeSafe Jev, and ScaleDown `/v1/scaledown`.
   - **OpenAI-style** (`input` + a `questions` array with `predicate`/`choice`/`score`): OpenAI Decisions, and vLLM `/v1/decisions`.
   - **vLLM-native:** vLLM additionally has its own `/v1/systemone` and `/classify`.

   Two parts of the stack are stable de facto standards:
   - **Rerank:** the Cohere shape (LiteLLM, vLLM, llama.cpp, Jina).
   - **Embeddings:** the OpenAI shape.

   Extraction has no common shape:
   - ScaleDown overloads chat completions with `response_format`.
   - GLiNER2 and NuExtract use their own schema dialects.
5. **No gateway we found treats task models and LLMs as interchangeable sources for the same typed request.** This is a search result, not a proof.
   - LiteLLM and vLLM normalise *within* an endpoint type (rerank to rerank).
   - vLLM Semantic Router uses ModernBERT classifiers as *routing signals*, not as answer sources.
   - OrcaRouter states explicitly that it does not host ScaleDown's task models.
6. **Licences differ even within one model family:**
   - GLiNER v0/v1 checkpoints are CC BY-NC 4.0; v2/v2.1 and GLiNER2/2.5 are Apache-2.0.
   - LayoutLMv3 is CC BY-NC-SA 4.0.
   - jina-reranker-v3 is CC BY-NC 4.0.
   - NuExtract 2.0 4B is under a research licence, while 2B and 8B are MIT.

   A head's licence is therefore a routing constraint, not documentation.

---

## A.2 Landscape

### A.2.1 Classification and zero-shot labels

| Model / API | Interface | Open? | Size, latency | Price | Accuracy evidence |
|---|---|---|---|---|---|
| **ScaleDown `/classify`** | `{text, labels:[{name,rubric}], multi_label, threshold}` → `{top_label, scores{label:p}, labels[], results[]}`. Single-label scores sum to 1; multi-label scores are independent [P, [docs](https://docs.scaledown.ai/api-reference/classify.md)] | Hosted; API key via sales; self-hosted container licence, not open weights [I, [ibl.ai](https://ibl.ai/blog/constrained-decoding-models-not-a-new-architecture)] | Classify p50 134 ms [I] | $0.05/M input tokens, output $0 [V/S]; bills ~481 input tokens where the raw prompt is ~172 [I] | 96.7% on a CLINC150 subset (10 intents + OOS, n=300); 90.9% on Bitext [I, [dev.to/ishank-dev](https://dev.to/ishank-dev/scaledown-cdms-fast-cheap-classify-extract-and-summarize-models-tested-1adp)]. Vendor: "+8.72% vs OpenAI" with no stated sample size [V, [benchmarks](https://docs.scaledown.ai/benchmarks/overview.md)] |
| **ScaleDown Decisions `/v1/scaledown`** (Jev-compatible) | `{state, questions:{name:{type: choice\|noul\|score, criteria, instructions}}}` → `answers{choice, probabilities, confidence}` [P, [docs](https://docs.scaledown.ai/api-reference/decisions.md)] | Hosted | p50 162 ms [I] | as above | 97.3% on the CLINC subset, 92.7% on Bitext [I] |
| **TypeSafe Jev** | Same Jev shape; on OpenRouter at `POST /api/alpha/decisions`, model `typesafe/jev-1.13` [P, [OpenRouter](https://openrouter.ai/docs/guides/community/jev-tutorial)] | Hosted | n/a | ~$0.042/M input [S] | No independent benchmark found |
| **OpenAI Decisions** | `POST /v1/decisions {model, input, questions:[{type: predicate\|choice\|score, …}]}`; choice takes 2–255 values, score 2–10 levels [S, [promptfoo](https://www.promptfoo.dev/docs/providers/openai-decisions/)] | Hosted, public beta, `gpt-6-luna` [S] | "up to 10x faster than via Responses" [V] | $0.10/M input [S] | None found |
| **vLLM `/v1/decisions`, `/v1/systemone`** | systemone reads label-token logprobs and softmaxes over the labels; it reports `label_mass`. Max 64 questions, 26 choices, 10 levels [P, [vLLM](https://docs.vllm.ai/en/latest/models/structured_decisions_models/)] | Apache-2.0 server, any supported model | Model-dependent | Self-host | n/a; the technique turns any LLM into a label distribution |
| **NLI zero-shot** (DeBERTa-v3-large-zeroshot-v2.0, `-c` variant) | One (text, hypothesis) pass **per label** → entailment score | MIT base. The non-`-c` variant was trained on some non-commercial data; `-c` uses commercial-friendly data only [P, model card via [S](https://huggingface.co/MoritzLaurer/bge-m3-zeroshot-v2.0-c)] | 435M. Throughput falls with the label count: deberta-v3-base drops from 24.55 to 0.47 ex/s going from 1 to 128 labels [P, [GLiClass 2508.07662](https://www.arxiv.org/pdf/2508.07662)] | Self-host | 0.682 avg F1 on the GLiClass suite [P]; Banking77 0.42 against GPT-4o's 0.78 [P, 2507.18546] |
| **GLiClass** v3 (Knowledgator) | Joint text+labels encoder, one pass for all labels | Apache-2.0 (v3.0 card) [P, [HF](https://huggingface.co/knowledgator/gliclass-base-v3.0/blob/main/README.md)] | edge to large; 97.3 to 25.2 ex/s on an A6000; only −7 to −20% throughput from 1 to 128 labels [P, 2508.07662] | Self-host | large-v3.0 averages 0.719 against 0.682 for the best cross-encoder; +0.11 to +0.21 from 8-shot adaptation [P]. Banking77 (older version) 0.21 against GPT-4o's 0.78 [P, 2507.18546] |
| **SetFit** | Contrastive fine-tune of a sentence encoder plus a head; **fixed label set** | Apache-2.0 library | 110M–355M | Self-host | Beat GPT-3 on 7 of 11 RAFT tasks at 355M [P/V, [HF blog](https://huggingface.co/blog/setfit), [2209.11055](https://arxiv.org/pdf/2209.11055)] |
| **ModernBERT** classifiers | Fine-tuned `*ForSequenceClassification`; fixed `id2label` | Apache-2.0 [S] | 149M / 395M, 8192-token context [P, 2412.13663 via [S](https://hyper.ai/en/papers/2412.13663)] | Self-host | Base beats DeBERTaV3-base on GLUE [P] |
| **Fine-tuned small LMs** (BERT, LoRA 7B) | Fixed labels (encoder) or constrained generation (LoRA) | Varies | — | Self-host | BERT reaches 93.66% on full-data Banking77 [P via [FinNLP](https://preview.aclanthology.org/moar-dois/2023.finnlp-1.7.pdf)]; LoRA beats GPT-4 by +10 avg [P, [2405.00732](https://arxiv.org/abs/2405.00732)]; fine-tuned beats zero-shot GPT-4 and Claude on every task tested [P, [2406.08660](https://arxiv.org/abs/2406.08660)] |
| **GLiNER2.5-Decide** (Fastino) | Text plus a schema of typed questions → selected answers, a distribution, and a feasibility flag | Apache-2.0 [V, [Fastino](https://fastino.ai/blog/gliner-2-5-decide-open-weight-decision-model)] | 340M; 167 ms p50 on a 48-vCPU Xeon, ~43 ms on T4/L4 [V] | Self-host or hosted | 60.1% on an internal 17-dataset suite against 57.5% for an open Jev reproduction [V]; no comparison with an LLM |

### A.2.2 Extraction, NER and structured fields

| Model / API | Interface | Open? | Size, latency | Price | Accuracy evidence |
|---|---|---|---|---|---|
| **GLiNER** (urchade) | `predict_entities(text, labels, threshold)` → spans (text, label, start, end, score) | Library Apache-2.0. **Checkpoints differ:** v0/v1 CC BY-NC 4.0, v2/v2.1 Apache-2.0 [S, [search](https://huggingface.co/urchade/gliner_medium-v1)]. ONNX export is supported [P, [repo](https://github.com/urchade/GLiNER)] | 50M / 90M / 300M | Self-host | GLiNER-L averages 60.9 F1 on the zero-shot OOD benchmark against 47.5 for ChatGPT (2023), 55.6 for UniNER-13B and 58.0 for GoLLIE; across 20 datasets, 47.8 against 36.5 for ChatGPT [P, [NAACL 2024](https://aclanthology.org/2024.naacl-long.300.pdf)]. UniNER-7B beats it on CoNLL03 (72.2 vs 64.6) [P] |
| **GLiNER2** (Fastino) | `extract_entities`, `classify_text`, `extract_json(schema "name::type::desc")`; `include_confidence` and `include_spans` give character offsets [P, [repo](https://github.com/fastino-ai/GLiNER2)] | Apache-2.0 | 205M (base) to 340M (large); CPU 130–208 ms for 5–50 labels against 358–463 ms for GPT-4o through its API [P, [2507.18546](https://arxiv.org/html/2507.18546v1)] | Self-host or Fastino API | CrossNER avg 0.590 against 0.599 for GPT-4o; classification avg 0.72 against 0.84 for GPT-4o [P] |
| **GLiNER2.5** | Boundary prediction, no maximum span width, joint relations [V, [Fastino](https://fastino.ai/blog/gliner2-5-span-free-information-extraction)] | Apache-2.0 | 74M / 194M / 287M | Self-host | 16-dataset macro F1: multi 56.17 vs GLiNER2 56.09; **extraction is lower** than GLiNER2 (46.40 vs 47.56) [V]. No LLM comparison |
| **ScaleDown Extract** | OpenAI chat completions with `response_format: json_schema`. Each property is an entity type and its `description` is the criterion. Returns `entities[{text,type,confidence,start,end,context}]` plus `structured_result` as a JSON string in `content`. No `$ref`; max 30 values per scalar; an enum property is routed to the classifier (2–20 labels); docs call offsets both "character" and "byte" [P, [docs](https://docs.scaledown.ai/api-reference/extract.md)] | Hosted; also `scaledown/extract` on OpenRouter [P, [OpenRouter](https://openrouter.ai/provider/scaledown)] | p50 1.43 s on receipts [I] | $0.05/M input [V/S]; ~1,380 billed tokens against 571 raw [I] | SROIE (n=50) 89.5% field accuracy, the same as Qwen3-1.7B; totals 100% vs 78%; company names 80% vs 88%; spans 100% verbatim vs 94% [I]. Vendor: CUAD EM 0.4961 vs 0.4196 for GPT-5.4 Mini [V via ibl.ai]; +0.2 pp over GLiNER2.5 with the configuration unpublished [V via ibl.ai] |
| **NuExtract 2.0** (NuMind) | Document (text or image) plus a JSON template with types incl. `verbatim-string`, enums and `null` for missing → JSON | 2B MIT, **4B research licence**, 8B MIT; PRO is API-only [V, [NuMind](https://numind.ai/blog/outclassing-frontier-llms-nuextract-2-0)] | 2.2B / 3.75B / 8.3B, 32k context | Self-host or API | 8B: 73 F-score on an internal 21-problem set; PRO "+9 over GPT-4.1" [V] |
| **spaCy / Flair** | Fixed label set (OntoNotes / CoNLL types) | MIT | Small; CPU-fast | Self-host | Supervised in-domain; fixed labels, so not zero-shot |
| **LayoutLMv3 / Donut** | Fine-tuned token classification over OCR boxes (LayoutLMv3) or OCR-free image→JSON (Donut) | **LayoutLMv3 CC BY-NC-SA 4.0** [P, [HF](https://huggingface.co:443/microsoft/layoutlmv3-base)]; Donut: licence not verified here | 133M–368M | Self-host | Fine-tuned per form type; FUNSD/CORD numbers not re-verified here |
| **Reducto / LlamaParse / Unstructured** | Hosted parse → markdown/JSON, plus a schema extract endpoint (Reducto, LlamaExtract) | Hosted (Unstructured also has an OSS library) | Seconds per page | Reducto ~$0.015–0.03/page; LlamaParse 1–45 credits/page at $1 per 1k credits; Unstructured ~$0.015/page [S, [ecorpit](https://ecorpit.com/rag-document-parsing-llamaparse-unstructured-docling-2026/), [usecarly](https://www.usecarly.com/blog/reducto-alternatives/)] | Vendor comparisons only |

### A.2.3 Constrained or structured generation (a technique, any model)

| Engine | What it does | Where |
|---|---|---|
| Outlines | Regex or JSON Schema compiled to an FSM; logits masked at each step | Python library; one vLLM backend |
| XGrammar | Pushdown-automaton grammar engine with precomputed token masks | vLLM, SGLang, MLC |
| llguidance / Guidance | Earley-style grammar engine (Rust) | llama.cpp, vLLM |
| vLLM structured outputs | `guided_json` / `guided_choice` / `guided_regex` / grammar, selectable backend | vLLM server |
| Provider structured outputs | OpenAI strict `json_schema`; Gemini `responseSchema`; Ollama `format` | Hosted APIs |

**Evidence:**
- **Form, not content.** Across 5 small models and 14 tasks, schema validity rose from 78.6–92.9% to 100%, but semantic errors stayed; "CD's reach ends exactly where schema conformance ends" [P, [2609.23742](https://arxiv.org/abs/2609.23742)].
- **Coverage gaps.** JSONSchemaBench (10k real schemas) shows that frameworks differ in which schema features they support and how efficiently [P, [2501.10868](https://arxiv.org/abs/2501.10868)].
- **Effect on reasoning is disputed.** Format restrictions were reported to hurt reasoning while helping classification [P, [2408.02442](https://arxiv.org/pdf/2408.02442)]. dottxt attributes that result to prompting and parsing artefacts [V, [dottxt](https://blog.dottxt.ai/say-what-you-mean.html)].

**How a "CDM" differs from constrained decoding on any model.** ibl.ai's critique [I, but the publisher sells a competing platform] makes four points:
- Constrained decoding is a decode-time mask that can be applied to any LM.
- ScaleDown's Classify and Extract are *non-generative*: label scoring and span tagging, where the output space is fixed by the model head rather than by a mask.
- The "new architecture" framing is a launch claim.
- On ScaleDown's own page, the paid extractor leads Apache-2.0 GLiNER2.5 by 0.2 pp.

For Hydra the useful distinction is three output mechanisms, because each needs different normalisation:

| Mechanism | Native output | Example |
|---|---|---|
| (a) label scoring | A distribution over the caller's labels | ScaleDown Classify, NLI, GLiClass, vLLM systemone |
| (b) span tagging | Offsets plus a score per span | GLiNER, ScaleDown Extract |
| (c) masked generation | Text that is valid by construction | Any LLM under Outlines, XGrammar or structured outputs |

### A.2.4 Embeddings and rerankers

| Model / API | Interface | Open? | Price | Notes |
|---|---|---|---|---|
| **Cohere Rerank** | `POST /v2/rerank {model, query, documents[], top_n, max_tokens_per_doc}` → `results[{index, relevance_score∈[0,1]}]`; v5 adds `relevant_spans` with code-point offsets [P, [docs](https://docs.cohere.com/reference/rerank)] | Hosted | ~$2.00 per 1k searches (≤100 docs; docs over 500 tokens are split) [S] | Scores "are not ratios" [P]; they are not comparable across models |
| **Voyage rerank-2.5 / -lite** | Query + documents → scores | Hosted | $0.05 / $0.02 per M tokens; billed as query tokens × docs + doc tokens [P, [pricing](https://docs.voyageai.com/pricing)] | — |
| **jina-reranker-v3** | Listwise, up to 64 docs in a 131k context | **CC BY-NC 4.0**; commercial use needs a contract [P, [HF](https://huggingface.co/jinaai/jina-reranker-v3)] | Self-host or API | BEIR nDCG@10 61.94 [V] |
| **bge-reranker-v2-m3** | Cross-encoder; raw logit, or sigmoid with `normalize=True` | Apache-2.0, 0.6B, multilingual [P, [HF](https://huggingface.co/BAAI/bge-reranker-v2-m3)] | Self-host | — |
| **Serving surfaces** | Cohere-shape `/rerank` in LiteLLM [P, [docs](https://docs.litellm.ai/docs/rerank)], vLLM `/rerank` `/v1/rerank` `/v2/rerank` [P, [vLLM](https://docs.vllm.ai/en/latest/serving/online_serving/)], llama.cpp `/v1/rerank` with `--reranking --pooling rank` [S]; TEI `/rerank {query, texts}`, `/predict`, `/embed` [P, [TEI](https://github.com/huggingface/text-embeddings-inference)] | | | |

### A.2.5 Other task families

| Family | Representative models | Interface | Licence / price | Evidence and caveats |
|---|---|---|---|---|
| **PII detection** | Presidio (regex + checksum + NER recognisers) | spans (entity_type, start, end, score) | MIT [P, [repo](https://github.com/microsoft/presidio)] | Accuracy depends on the plugged-in NER [S] |
| | NVIDIA GLiNER-PII | spans | NVIDIA Open Model Licence | Strict F1 0.70 Argilla / 0.64 AI4Privacy / 0.87 Nemotron-PII at threshold 0.3 [P, model card via [S](https://grepture.com/blog/best-open-source-models-pii-redaction)] |
| | `fastino/gliner2-privacy-filter-PII-multi` | spans | Apache-2.0; 205M; 42 types | — |
| **Guard / safety classifiers** | Llama Guard 4 | category labels | 12B; Llama 4 Community Licence [S] | — |
| | Llama Prompt Guard 2 | binary | 86M (mDeBERTa) / 22M (DeBERTa-xsmall, English only) [P, [Meta](https://developer.meta.com/ai/docs/model-cards-and-prompt-formats/prompt-guard/)] | Card: 86M has 0.998 AUC and 97.5% recall at 1% FPR. **Independent:** it caught 6 of 629 AgentDojo injections buried in tool output, and the 22M caught 0 [I/S, [ecosistemastartup](https://ecosistemastartup.com/prompt-guard-2-de-meta-detecta-1-de-ataques-reales-a-ai-agents/)]. This is distribution shift in its plainest form |
| | GLiGuard | safety labels + PII in one pass | 145–209M | 193 req/s on an A100 with P99 under 1 s [P, [2605.05277](https://arxiv.org/abs/2605.05277)] |
| | ShieldGemma | — | — | Not verified in this pass |
| **Speech** | Whisper large-v3 / large-v3-turbo | transcript (+ timestamps) | MIT weights [S]; hosted ~$0.006/min [S] | `/v1/audio/transcriptions` in vLLM [P] |
| **Vision OCR / document parsing** | olmOCR-Bench (7,010 unit tests on 1,403 PDFs) | — | — | Top open models score 83–88 [S, [codesota](https://www.codesota.com/ocr/benchmark/olmocr-bench)]. OmniDocBench is a different scale: GPT-5.2 86.59, Mistral OCR 85.66 [S] |
| **Summarisation** | ScaleDown Summarize | generative, chat completions | — | QMSum (n=6) ROUGE-L 0.230 vs 0.201 for Qwen3-1.7B; 0/6 summaries with invented facts vs 2/6; on a 369k-token document it covered one of two meeting series [I] |
| **Code** | Small code LMs (e.g. Qwen-Coder sizes) | generation | — | Already routable as chat heads; outside this appendix |

### A.2.6 Hosting surfaces and their API shapes

| Surface | Task endpoints | Shape |
|---|---|---|
| OpenRouter | chat completions for everything (incl. `scaledown/extract`, `classify-1`, `summarize`); `POST /api/alpha/decisions` for Jev | OpenAI chat, plus a Jev-shape alpha [P] |
| Hugging Face Inference Endpoints | Any container (TEI, vLLM, custom handler) | Billed per instance-hour while a replica runs: T4 $0.5/h, L4 $0.8/h, CPU from $0.032/core-h [P, [HF](https://huggingface.co/docs/inference-endpoints/en/pricing)] |
| vLLM (self-host) | chat, `/classify`, `/score`, `/rerank`, `/pooling`, `/v1/embeddings`, `/v1/decisions`, `/v1/systemone`, `/v1/audio/transcriptions` | One server per model; per-endpoint shapes [P] |
| TEI (self-host) | `/embed`, `/rerank`, `/predict`, `/embed_sparse` | Apache-2.0 [P] |
| llama.cpp server | chat, `/embedding`, `/v1/rerank` | OpenAI + Cohere-like [S] |
| Ollama | chat, embeddings, `format` (JSON schema) | No rerank or classify endpoint in what we checked |
| Replicate | predictions API (async poll) | Per-model input/output JSON; Hydra's executor already polls it |
| Together, Fireworks | chat, embeddings, rerank (Cohere-shape via LiteLLM) [P, LiteLLM provider list] | — |
| Local ONNX | GLiNER exports to ONNX [P]; GLiNER2's README does not mention ONNX | Needs a tokenizer + runtime (cgo in Go) |

---

## A.3 Evidence: when specialised models win, and where they fail

### A.3.1 Accuracy

| Setting | Specialised | LLM | Source |
|---|---|---|---|
| Zero-shot NER, CrossNER 5-domain avg | GLiNER-M 0.615, GLiNER2 0.590 | GPT-4o 0.599 | [P] 2507.18546 T3 |
| Zero-shot NER, OOD 7-dataset avg | GLiNER-L 60.9 | ChatGPT (2023) 47.5, GoLLIE-7B 58.0 | [P] NAACL 2024 T1 |
| Zero-shot classification, 7-dataset avg | GLiNER2 0.72, DeBERTa-NLI 0.69, GLiClass 0.63 | GPT-4o 0.84 | [P] 2507.18546 T2 |
| Zero-shot Banking77 | GLiNER2 0.70, DeBERTa-NLI 0.42, GLiClass 0.21 | GPT-4o 0.78 | [P] 2507.18546 T2 |
| Fine-tuned Banking77 | BERT 93.66% | — | [P] Casanueva 2020 via FinNLP 2023 |
| Fine-tuned vs zero-shot (sentiment, emotion, stance) | fine-tuned BERT-class wins every case | GPT-3.5/4, Claude Opus | [P] 2406.08660 |
| 31 tasks, LoRA 7B vs GPT-4 | +10 points avg | — | [P] 2405.00732 |
| CLINC150 subset (10 + OOS, n=300) | ScaleDown 96.7–97.3% | Qwen3-1.7B 90.7% (no frontier accuracy measured) | [I] |
| SROIE receipts (n=50) | ScaleDown 89.5% | Qwen3-1.7B 89.5% | [I] |
| Information extraction overall | SOTA supervised IE methods | "visible performance gap" for GPT-4 | [P] [2305.14450](https://arxiv.org/abs/2305.14450) |

**The reading for Hydra:**
- **Span extraction with a declared schema:** small encoders are at parity zero-shot.
- **Fine-grained classification with many labels:** zero-shot encoders lose unless fine-tuned. With a fixed label set and training data, fine-tuned small models win outright.
- **The winning configuration is per cell.** A static "classification → small model" rule would be wrong on Banking77-like traffic and right on sentiment-like traffic. Only measurement per task class separates the two.

### A.3.2 Latency and cost

- **Encoders on CPU.** GLiNER2 on CPU is 2.6× faster than a GPT-4o API call [P]. Decision encoders reach ~40–170 ms [V].
- **ScaleDown against a local 1.7B model** [I]:
  - **Speed:** 1.8× (classify) to 37× (summarise) faster.
  - **Cost:** about equal to gpt-5-nano per call, and 45–200× cheaper than flagship models.
- **ScaleDown's own ratios are not reproducible as stated.** Its "200×/467×" figures depend on an unnamed baseline that uses reasoning tokens [I].
- **Billed tokens are not prompt tokens.** ScaleDown bills 2.4–2.8× the raw prompt [I]. Hydra must price on *billed* units, not on its own token estimate.

### A.3.3 Failure modes

| Failure | Evidence | Hydra mitigation |
|---|---|---|
| Distribution shift | Prompt Guard 2: 97.5% recall on its own test set, ~1% on injections buried in tool output [P vs I] | Per-class calibration; never import a vendor's accuracy as a prior without marking it `catalogue_prior` |
| Label set outside the trained or allowed range | ScaleDown caps at 20 (16 multi-label); vLLM systemone 26; NLI cost grows linearly with labels; fixed-label classifiers know only their `id2label` | Eligibility check on declared limits before routing (§A.5.2) |
| Wrong role, right span | ScaleDown returned the *buyer's* details for the seller on one receipt; returned parent company instead of brand [I] | A span-in-source check cannot catch this; cross-family agreement and outcomes can (§A.7) |
| Normalisation drift | Dropped times from dates; omitted company registration numbers [I] | Field types with canonicalisers (date, money, id) before equivalence |
| Long documents | GLiNER2 chunks at 384 words with 64 overlap [P]; ScaleDown summary covered one of two series at 369k tokens [I] | `max_input_tokens` per head; chunking is the head's job, coverage is a verifier's job |
| Multilingual | Prompt Guard 22M is English-only [P]; GLiNER2 base is English, `-multi` is separate [P]; GLiNER2.5 multi's extraction is below GLiNER2's [V] | `languages` in head caps, checked against `language.script` / `language.code` signals |
| OOS over-rejection | 6 in-scope CLINC queries labelled OOS [I] | OOS is a label; its recall and precision are measured like any other |
| Offset units | ScaleDown docs say "character" and "byte" in different places [P]; Cohere uses code points [P]; Go strings index bytes | Normalise every offset to Unicode code points at the adapter; verify `source[start:end] == text` |
| Native confidence miscalibrated | Modern NNs are miscalibrated, and temperature scaling fixes much of it [P, [1706.04599](https://arxiv.org/abs/1706.04599)]; LLM verbalised confidence is overconfident [P, [2306.13063](https://arxiv.org/abs/2306.13063)]; vLLM states its decisions `confidence` "is not calibrated" [P] | A native score selects a calibration cell; it never enters the LLR directly (§A.6.3) |

---

## A.4 Interface heterogeneity: how others normalise

| System | What it normalises | What it does not |
|---|---|---|
| LiteLLM | One shape per endpoint type (`/rerank` in Cohere shape, `/embeddings` in OpenAI shape); fallbacks within a type [P] | No classify or extract type; never crosses types |
| vLLM | Per-endpoint shapes; one model per server [P] | No routing |
| TEI | `/predict`, `/rerank`, `/embed` [P] | No routing |
| OpenRouter | Everything through chat completions; Jev through an alpha decisions route [P] | Task semantics (labels, spans) live inside a JSON string in `content` |
| vLLM Semantic Router | ModernBERT classifiers (domain, PII, jailbreak) as *signals* for choosing an LLM [S, [docs](https://llm-semantic-router.readthedocs.io/en/latest/architecture/system-architecture/)] | Classifiers never answer the user's request |
| OrcaRouter | 200+ models behind one key | "We do not host ScaleDown Classify, Extract or Summarize" [V, [blog](https://www.orcarouter.ai/blog/scaledown-classify-extract-summarize)] |
| ScaleDown SLM Agent | Offline migration of call sites to ScaleDown models [P, docs] | Not a runtime router |

**Gap Hydra fills:** none of these accepts one typed request, sends it to an encoder or a decoder interchangeably, and compares their answers as evidence. The prerequisite is a canonical, typed answer, which is §A.6.

---

## A.5 Design: typed task interfaces

### A.5.1 Task kinds

| Kind | Caller supplies | Canonical answer | Verifiable per call? | Routable per call? |
|---|---|---|---|---|
| `classify` | text or document, labels (+ rubric), single or multi | one label, or a set of labels | yes (agreement, outcomes) | yes |
| `extract` | text or document, schema (fields, types, `verbatim`) | a record of field → value(s), each with offsets | yes (span-in-source, schema, agreement) | yes |
| `rerank` | query, documents, top_n | ordered indices | weakly (top-1 agreement) | yes, by measured nDCG |
| `embed` | inputs | vectors | no | **no**: vectors from different models are different spaces; route only between replicas of one model+version |
| `summarize` | text, length | text | weakly (`grounded`, `judge`) | yes |
| `chat` | messages | text / tool calls | as today | as today |

`embed` is deliberately excluded from cascades. An index built with model A cannot be queried with model B. `internal/evalset` already enforces the same rule ("two models are two spaces"). Hydra proxies `embed`, pins the model per route, and fails over only to another deployment of the *same* model id and version.

### A.5.2 API surface: the recommendation

**Recommendation: typed endpoints are canonical, and two compatibility ingresses map onto the same internal request.** This follows RFC §5.1.3: wire formats are compatibility details, and the router sees one typed request.

1. **Hydra-native endpoints** (Phase 1):
   - `POST /v1/classify`
   - `POST /v1/extract`
   - `POST /v1/rerank` (Cohere request/response shape, already the de facto standard)
   - `POST /v1/embeddings` (OpenAI shape, pinned model)

   The native shapes exist because only they have a place in the body for the response contract (§5.4 of the RFC): `p_correct`, sources and per-field evidence. Vendor shapes can carry it only in headers.
2. **Chat completions shim:** `POST /v1/chat/completions` with `response_format: {type: json_schema}` **and** an explicit `metadata.hydra_task: "extract"|"classify"` (or `X-Hydra-Task`). This is the shape ScaleDown Extract itself uses, so an OpenAI SDK pointed at Hydra works unchanged.
   - Hydra does **not** infer a task from `response_format` alone. A JSON-schema chat request may be generation ("write three slogans as JSON"), where a span-in-source verifier would reject every correct answer.
   - Without the hint, a `json_schema` request gets the `jsonschema` verifier and stays a chat task. Task-model heads are never eligible for it.
3. **Decisions ingress** (Phase 2): `POST /v1/decisions` in the OpenAI shape, mapped to `classify` (`choice` → single-label, `predicate` → one binary label, `score` → ordinal labels).
   - The shape is in public beta and documented here only through third parties, so it waits until it is stable.
   - The Jev shape (`state`/`questions` map) is an **egress** dialect Hydra speaks to ScaleDown and Jev heads. It is not offered as an ingress.

`POST /v1/classify` (native):

```json
{
  "task_class": "support.intent",
  "text": "my card was charged twice",
  "labels": [
    {"name": "billing", "rubric": "Is this about a charge, refund or invoice?"},
    {"name": "technical"},
    {"name": "account"}
  ],
  "multi_label": false,
  "allow_none": true,
  "target": 0.95,
  "objective": "quality"
}
```

The response:

```json
{
  "labels": ["billing"],
  "scores": {"billing": 0.91, "technical": 0.05, "account": 0.04},
  "scores_basis": "source:scaledown/classify-1",
  "hydra": {
    "request_id": "…",
    "decision": "accept",
    "p_correct": 0.971,
    "target": 0.95,
    "sources": ["gliner2.5-decide@local", "scaledown/classify-1"],
    "p_pass_basis": "measured"
  }
}
```

`scores` are the accepted source's native scores and are labelled as such. `p_correct` is Hydra's posterior, and it is the only calibrated number in the body.

`POST /v1/extract` (native):

```json
{
  "task_class": "extract.receipt",
  "text": "…",
  "document": null,
  "schema": {
    "type": "object",
    "properties": {
      "vendor": {"type": "string", "description": "seller company name", "x-verbatim": true},
      "total":  {"type": "string", "description": "grand total incl. tax", "x-verbatim": true, "x-canon": "money"},
      "date":   {"type": "string", "description": "purchase date", "x-canon": "date"}
    },
    "required": ["vendor", "total"]
  }
}
```

The response returns `record`, plus `spans[{field, text, value, start, end}]` with code-point offsets and `hydra.fields{name: {p_correct, decision, sources}}`.

- The record is accepted only when every required field is.
- Optional fields that miss the target are returned with `decision: "below_target"` rather than dropped. A caller that wants only accepted fields filters them.

### A.5.3 Eligibility

A head is a candidate for a typed request only if all of the following hold:

1. **Task support.** `kind ∈ head.Tasks`.
2. **Label count within the head's limit:**
   - For `classify`: `len(labels) ≤ MaxLabels`, or `≤ MaxLabelsMulti` when multi-label.
   - A **fixed-label** head is eligible only if every request label maps through its configured `label_map` to one of its `id2label` labels.
3. **Schema features the head supports.** For `extract`: `$ref`, nesting and arrays must be features the head supports. ScaleDown refuses `$ref` [P].
4. **Input size.** `estimated input ≤ MaxInputTokens`, and `document` only if `Document` is true.
5. **Language.** `language` ∈ `Languages`, when the `language.*` signal is present. An absent signal never excludes.
6. **Policy:** licence and data residency.
   - `licence.commercial = false` excludes the head unless the route sets `allow_noncommercial`.
   - `local_only` routes exclude hosted heads (the PII policy already does this).

An ineligible head is reported in `X-Hydra-Sources` diagnostics with its reason, the way `executor.Unroutable` reports one today. This keeps "discovered but unusable for this request" visible.

---

## A.6 Design: the task-model executor

### A.6.1 Go types (new package `internal/task`)

```go
package task

// Kind is what a request asks for; it decides the canonical answer shape.
type Kind string

const (
	Classify  Kind = "classify"
	Extract   Kind = "extract"
	Rerank    Kind = "rerank"
	Embed     Kind = "embed"
	Summarize Kind = "summarize"
	Chat      Kind = "chat"
)

type Label struct{ Name, Rubric string }

type ClassifyRequest struct {
	Labels     []Label
	MultiLabel bool
	AllowNone  bool // adds the reserved label "∅" (out of scope)
}

// Field is one schema property. Canon names the canonicaliser applied before
// equivalence; Verbatim fields must appear in the source.
type Field struct {
	Name, Type, Description string
	Enum                    []string
	Canon                   string // "", "date", "money", "number", "id", "casefold"
	Verbatim, Required      bool
	Repeated                bool
	Fields                  []Field
}

type ExtractRequest struct {
	Fields      []Field
	Instruction string
	Raw         json.RawMessage // the caller's JSON Schema, passed through to heads that take one
}

type RerankRequest struct {
	Query     string
	Documents []string
	TopN      int
}

type Document struct {
	MIME string
	Data []byte
}

type Request struct {
	Kind     Kind
	Class    string // task class: the calibration domain
	Text     string
	Document *Document
	Classify *ClassifyRequest
	Extract  *ExtractRequest
	Rerank   *RerankRequest
	Chat     *executor.Request
}

// Span offsets are Unicode code points into Request.Text, never bytes.
type Span struct {
	Field, Text, Value string
	Start, End         int
	HasOffsets         bool
	Score              float64 // native, uncalibrated; -1 when the head gives none
}

type Result struct {
	Labels   []string           // classify
	Scores   map[string]float64 // classify, native
	Spans    []Span             // extract
	Order    []int              // rerank
	Text     string             // summarize/chat
	Usage    Usage
	Duration time.Duration
}

// Usage is what the head billed, in the unit it bills in.
type Usage struct {
	Unit     string // "token", "search", "page", "minute", "instance_hour"
	Quantity float64
	CostUSD  float64
	Measured bool
}

// Caps is what a head declares; eligibility (§A.5.3) reads only this.
type Caps struct {
	Tasks          []Kind
	MaxLabels      int
	MaxLabelsMulti int
	FixedLabels    []string          // non-empty: closed label set (fine-tuned head)
	LabelMap       map[string]string // request label -> head label
	SchemaRef      bool
	Nested         bool
	Document       bool
	MaxInputTokens int
	Languages      []string // BCP-47; empty = unknown, never "all"
	Offsets        string   // "codepoint" | "byte" | "utf16" | "none"
	Commercial     bool
}

// Executor runs one typed request against one head. Adapters translate the
// wire dialect; nothing above this interface sees a vendor shape.
type Executor interface {
	Run(ctx context.Context, h provider.Head, r Request) (Result, error)
}
```

LLM heads become task-capable through one generic adapter, `llm-structured`:
- **Prompt.** It renders a task prompt (labels and rubrics, or field descriptions).
- **Decoding constraint.** It requests the provider's structured output (`json_schema` strict, Gemini `responseSchema`, Ollama `format`, vLLM `guided_json`/`guided_choice`).
- **Parsing.** It parses the output into the same `Result`.
- **Offsets.** It locates offsets for `Verbatim` fields by searching the source. Zero matches means `HasOffsets=false`. Several matches means the first match, flagged `ambiguous_offset`.

That is how a frontier LLM and GLiNER answer the same `Request`.

### A.6.2 Adapters

| Adapter | Heads | Wire |
|---|---|---|
| `scaledown-classify` | `/classify` | ScaleDown native; `reasoning:false` always (an extra model call Hydra does not use) |
| `jev-decisions` | ScaleDown `/v1/scaledown`, TypeSafe Jev (direct or OpenRouter `/api/alpha/decisions`) | Jev shape |
| `openai-decisions` | OpenAI `/v1/decisions`, vLLM `/v1/decisions` | OpenAI shape (Phase 2) |
| `scaledown-extract` | `extract`, `extract-1`, OpenRouter `scaledown/extract` | chat completions + `response_format`; content parsed as `{entities, structured_result}`; offsets normalised to code points |
| `gliner-sidecar` | GLiNER, GLiNER2, GLiNER2.5, GLiNER2.5-Decide, GLiClass | Hydra's own typed JSON over HTTP (below) |
| `tei` | TEI `/predict` (fixed-label classifiers), `/rerank`, `/embed` | TEI |
| `cohere-rerank` | Cohere, vLLM, llama.cpp, Jina, Together, Fireworks | Cohere `/v2/rerank` |
| `llm-structured` | every existing chat head | existing `executor.Execute` + structured output |

**GLiNER: an HTTP sidecar, not in-process ONNX.**
- **The cost of in-process ONNX.** The ONNX route would put cgo (onnxruntime + a tokenizer library) in the one Go binary, and would reimplement GLiNER's span decoding in Go. GLiNER2's README does not mention ONNX at all, and GLiNER2.5 changed the decoding (boundary proposal plus reranking).
- **What the sidecar is.** A ~150-line Python service (`sidecars/gliner/`, not part of the Go build) that loads any GLiNER-family checkpoint and serves `POST /v1/classify` and `POST /v1/extract` in Hydra's native shape. Since it speaks the native shape, it is also the reference implementation of that shape.
- **Discovery.** The port provider discovers it the way it discovers Ollama:
  - The address comes from `HYDRA_GLINER_HOST` (default `127.0.0.1:8790`).
  - It is **identified, not assumed from its port**: it must answer `GET /hydra/task-sidecar` with `{protocol: 1, models: [{id, tasks, caps, licence}]}`, the rule llama.cpp discovery already follows.
  - It is `LocalOnly: true`, so `local-only` PII routes can use it.
- **HF Inference Endpoints.** A Hugging Face Inference Endpoint running the same container is the same adapter with a remote `Endpoint` and `LocalOnly: false`.

### A.6.3 Output normalisation so SPRT, verifiers and calibration work

**Claims.** Every `Result` is decomposed into atomic claims `(key, value)`:

| Kind | Claims |
|---|---|
| classify, single | `("label", "billing")`; `("label", "∅")` for out of scope |
| classify, multi | one binary claim per label: `("label:billing", "yes")`, `("label:refund", "no")`. Set equality would make every distinct subset its own hypothesis and split the votes |
| extract | one claim per field: `("vendor", canon(value))`. A missing field is the value `∅`, and two heads agreeing a field is absent is evidence. Repeated fields: `("items[*]", canon(sorted multiset))` in v1; per-item alignment is an open question (§A.11) |
| rerank | `("top1", "7")` |

**Answer equivalence:**
- **Labels:** exact match on the request's label key.
- **Spans:** equality of `canon(value)`. Two heads that pick different occurrences of the same total agree. Offsets are evidence for the span-in-source verifier, not part of equivalence.
- **The canonicalisers are deterministic and have no thresholds:**

  | Canon | Rule |
  |---|---|
  | `date` | ISO 8601 date; the time is dropped only if the schema type is `date` |
  | `money` | decimal + ISO currency when stated |
  | `number` | — |
  | `id` | strip separators |
  | `casefold` | — |

  Anything else gets whitespace collapse plus NFC.
- **No fuzzy matching.** There is no "within 0.85 similarity" rule, for the reason `internal/cache` and `internal/vet` already state: a ratio needs a threshold nobody measured.

**Field-wise SPRT.** `trust.Run` samples a source and gets one answer text. For typed tasks, one call answers every field, so the run must accumulate per claim key. The addition to `internal/trust`:

```go
// Claim is one atomic, independently verifiable part of a typed answer.
type Claim struct {
	Key, Value string
	CalKey     string // calibration cell this vote is weighed under; "" = Source.ID
	Abstain    bool   // contributes no LLR (e.g. field outside the head's caps)
}

type ClaimExecutor interface {
	ExecuteClaims(ctx context.Context, src Source, task Task) (claims []Claim, costUSD float64, err error)
}

type ClaimsResult struct {
	Keys     map[string]*Result // per key: leader, Λ, hypotheses, ledger
	Decision Decision           // accept only if every required key accepted
	SpentUSD float64
	Samples  int
}

// RunClaims samples each source once and scores every key's hypotheses with
// scoreHypotheses. Required keys each test at α/k (union bound), so the record
// is jointly correct with P ≥ 1−α without assuming fields are independent.
func RunClaims(ctx context.Context, task Task, required, optional []string, sources []Source,
	exec ClaimExecutor, cal *Calibrator, t Target, opts ...RunOption) (*ClaimsResult, error)
```

**Calibration cells:**
- The domain is the task class (`extract.receipt`). v1 calibrates per (source, class), pooled over fields.
- A per-field cell (`extract.receipt#total`) is consulted once it has `rank.MinCommitments` observations. Below that, it borrows from the class cell with the same bounded borrowing `rank.EffectiveScoreIn` uses. A field cell never invents a measurement.
- **Label-set drift.** A label-set change is recorded as a label-set fingerprint on every observation. When the fingerprint changes, the class's cells keep their counts but `X-Hydra-P-Pass-Basis` reports `measured_other_labelset` until `MinCommitments` observations exist under the new set.

**Native confidence: a feature that selects a cell, never a source by itself.**
- **Why not a source.** A head's softmax is a number the head produced about itself. Treating it as evidence would weigh the same model twice (its answer and its opinion of its answer), and the literature says that number is miscalibrated in both encoders [P, 1706.04599] and LLMs [P, 2306.13063].
- **How it is used.** It becomes evidence only through measured rates, by histogram binning:
  1. Until a (head, class) cell has 200 observations, there is one cell, and the native score is recorded but unused.
  2. At 200, the cell's own native-score terciles are frozen as edges. They are versioned (`@t3.v1`) and stored with the calibration.
  3. From then on the adapter sets `Claim.CalKey = "<head>@t<bin>.v1"`. The low, middle and high bins each learn their own se/sp from outcomes, as ordinary sources.
  4. A high-score bin with high measured sp yields a large LLR; a low-score bin yields a small or negative one. The measured rates decide, not the score.
- **Diagnostics.** `internal/reliability` reports the native score's own Brier/ECE against outcomes as a diagnostic (`hyctl trust reliability --native`), so a head whose scores are informative is visible. That report never enters the LLR.
- **Abstention.** A score below a head's declared `threshold` (GLiNER, ScaleDown multi-label) is not a "no" vote. It is the claim `∅`, and `∅` is calibrated like any value.

**Families.** Today `swarm/sprt.go` sets `Family: h.Provider`. For task heads, family must mean **lineage**:
- `gliner` for GLiNER, GLiNER2, GLiNER2.5 and GLiClass, whatever their provider.
- `deberta-nli`.
- `scaledown` for ScaleDown, with lineage unknown.

An unknown lineage is not assumed independent. `trust/coupling.go` measures co-agreement, and its discount applies once measured.

**The concrete risk:** ScaleDown Extract leads GLiNER2.5 by 0.2 pp [V]. Near-identical scores are consistent with a shared lineage. Agreement between them must not count as two independent votes until coupling says so.

---

## A.7 Verifiers for typed tasks

All of these are `oracle.Oracle` implementations, calibrated under `verifier:<kind>`. A weak one contributes little because its measured D is small, not because it is configured to.

| Verifier | Kinds | Check | Expected strength |
|---|---|---|---|
| `jsonschema` | extract, chat+schema | the output validates against the request schema | Near zero D for constrained heads (always pass); real D for unconstrained LLM output. Measured, not assumed |
| `label_set` | classify | every label ∈ the request labels ∪ {∅} | Same as above |
| `span_in_source` | extract | for each `Verbatim` field: `canon(source[start:end]) == canon(text)` when offsets are present, otherwise `canon(text)` occurs in `canon(source)`. Offsets in code points | Catches invention: ScaleDown's 100% vs Qwen3's 94% verbatim [I]. Blind to a wrong-role span (buyer for seller), so its sp is bounded and outcomes will show it |
| `field_checks` | extract | Route-declared invariants in the existing `signals` expression language (`sum(items.amount) == total`). Run as the `http` verifier until the expression language has aggregates | High sp where an invariant exists |
| `agreement` | all | Cross-family agreement through `RunClaims` | The strongest cheap evidence when families are measured independent; encoder and decoder errors are plausibly less correlated, but that must be measured |
| `grounded` | summarize | the existing `internal/ground` (numbers, paths, identifiers absent from the input) | As measured in #907 |
| `feedback` | all | `/v1/feedback` per field: `{request_id, field?, outcome}` | The only source of specificity (#771); a field-level outcome trains only that key's voters |

---

## A.8 The cascade in practice

**Example route:** `extract.receipt`, `target 0.95`, three eligible heads:

| Head | Cost/call | p50 | Measured P̂(pass), n | se / sp (agreement vote) |
|---|---|---|---|---|
| `gliner2.5-base@local` (sidecar) | $0 | 0.3 s | 0.86, n=300 | 0.90 / 0.60 |
| `scaledown/extract` | $0.00007 | 1.4 s | 0.90, n=120 | 0.93 / 0.70 |
| `openai/gpt-5.4` (llm-structured) | $0.004 | 3.0 s | 0.92, n=40 | 0.95 / 0.70 |

All numbers are illustrative. The LLRs for an agreeing vote are ln(se/(1−sp)): 0.81, 1.13 and 1.15 nats. Accepting at α/k with k=2 required fields needs ln((1−0.025)/0.025) = 3.66 nats per field.

**Under `quality`** (RFC §5.0):
1. **Answer source.** The order is descending P̂(pass), so GPT-5.4 is the answer source.
2. **Co-voters.** Free local heads (cost 0, `LocalOnly`) are **co-voters**: they run concurrently with the first paid head, so they add evidence without adding latency.
3. **When they agree.** If GPT and GLiNER agree on a field, the field has 1.96 nats, short of 3.66.
4. **Escalation.** The next source by P̂ is ScaleDown. Its agreement brings the field to 3.09, still short, so the run continues while sources remain, and stops at `budget_exhausted` with the field `below_target`.
5. **Reading that result.** This is the honest outcome: three moderately calibrated sources do not make 97.5% per field. Raising sp, which only outcomes do, is what makes the target reachable.
6. **Disagreement.** If GLiNER disagrees, its −LLR enters, and the claim's hypotheses are scored per §778's multi-hypothesis rule.

**Under `balanced` / `cost`:**
- The order is ascending price among eligible heads, so the run is GLiNER → ScaleDown → GPT. It stops at the first point where every required field crosses its boundary.
- **Latency stacking** is the cost: the sequential p50 is 0.3 + 1.4 + 3.0 s.
- **Mitigation: hedging.** Launch the next head when the current one has not returned by the route's `hedge_after_ms` (default: the current head's measured p50), and cap the whole run at `max_latency_ms`.
- **Mitigation: the free floor runs concurrently.** Free heads always run alongside the first paid head; they never sit in front of it.

**How specialised heads earn observations.** Exploration (`explore_rate`) changes which answer the caller receives. For typed tasks there is a better instrument: **shadow co-votes**.
1. **What runs.** A head that is eligible, free or under `shadow_max_usd`, and below `MinCommitments` in this class is run alongside the dispatch. Its claims enter the ledger with `Shadow: true`.
2. **What it changes.** Its votes contribute no LLR to *this* run's decision, so the served answer and its propensity are unchanged.
3. **How it trains.** When an outcome arrives (`/v1/feedback`, or a high-sp verifier verdict), `trust.ApplyRunOutcome` trains the shadow head's cell like any voter's. A shadow vote that disagreed with an answer later judged correct is exactly the observation that moves specificity (#771).
4. **What it enables offline.** Every shadowed row holds the shadow head's answer, so "always head X" for that class is evaluated with full information. `internal/ope`'s IPW is needed only for heads that were not shadowed.
5. **Seeding.** §A.10's benchmark runs write seed observations per (head, class), marked `basis: benchmark:<suite>@<date>`, answering RFC open question 3 for typed packs.

---

## A.9 Registry and config

**Discovery stays where it is.** Hosted task heads come from keys (`SCALEDOWN_API_KEY`, `COHERE_API_KEY`, …) via the env provider. Local ones come from the port provider (the GLiNER sidecar, TEI, vLLM). **Caps** come from a new embedded registry file, overridable at `$HYDRA_HOME/registry/task_models.yaml` like every other registry file:

```yaml
# registry/task_models.yaml: declared caps per task head.
version: "1.0"
heads:
  scaledown/classify-1:
    adapter: jev-decisions          # or scaledown-classify
    family: scaledown               # lineage, not provider
    tasks: [classify]
    caps: {max_labels: 20, max_labels_multi: 16, document: true, max_input_tokens: 1000000,
           languages: [], commercial: true}
    billing: {unit: token, input_per_mtok: 0.05, output_per_mtok: 0, billed_tokens_measured: true}
    source: "docs.scaledown.ai/api-reference/classify.md@2026-10-29"
  scaledown/extract:
    adapter: scaledown-extract
    family: scaledown
    tasks: [extract]
    caps: {schema_ref: false, nested: true, document: true, offsets: unknown,
           max_values_per_field: 30, commercial: true}
    billing: {unit: token, input_per_mtok: 0.05, output_per_mtok: 0}
  sidecar/gliner2.5-base-v1:
    adapter: gliner-sidecar
    family: gliner
    tasks: [extract, classify]
    caps: {offsets: codepoint, max_input_tokens: 0, chunking: "384w/64", languages: [en],
           commercial: true, licence: Apache-2.0}
    billing: {unit: none}
  sidecar/gliner_medium-v1:
    family: gliner
    caps: {commercial: false, licence: CC-BY-NC-4.0}   # ineligible unless allow_noncommercial
  tei/banking77-bert:
    adapter: tei
    family: bert
    tasks: [classify]
    caps: {fixed_labels_from: id2label}
    label_map: {card_payment_wrong_amount: card_payment_wrong_amount}   # request label -> head label
    billing: {unit: instance_hour, usd_per_hour: 0.5}   # marginal 0, reported amortised
```

**Per-route config** (`config.toml`):

```toml
[[routes]]
task       = "extract"
class      = "extract.receipt"
objective  = "balanced"
target     = 0.95
defect_cost_usd    = 2.0
max_latency_ms     = 6000
hedge_after_ms     = 0            # 0 = the running head's measured p50
shadow_max_usd     = 0.0          # free heads only
allow_noncommercial = false
verifiers  = ["jsonschema", "span_in_source"]
```

**What goes in `internal/capabilities`.**
- **What it holds today.** `Entry` holds one `CapScore` per model, which is a chat-quality prior.
- **The addition.** It gains `Tasks map[Kind]TaskPrior{Score int; Basis string}`, a per-task catalogue prior with its provenance (`vendor`, `paper:<id>`, `benchmark:<suite>@<date>`).
- **The rule.** A vendor number may seed a prior. It is always reported as `p_pass_basis: catalogue_prior`, and it moves only by measured outcomes.
- **Where the limits live.** Limits and billing live in `task_models.yaml`, not in capabilities, because they are facts about an API version rather than about quality.

**Pricing.** `internal/pricing.DB` gains a `Billing` per head with `unit ∈ {token, search, page, minute, instance_hour, none}`:
- **Cost estimation.** For `token`, it uses *billed* tokens from the response when present. ScaleDown's are 2.4–2.8× the prompt [I], so Hydra's own token estimate under-prices it.
- **Fixed-cost heads.** `instance_hour` heads are marginal-$0 for routing, like local heads, and amortised in `hyctl cost`.

---

## A.10 Benchmark plan

**Protocol** (extends RFC §7):
- Temperature 0 (#1041).
- Identical label rubrics and field descriptions across engines.
- Billed units recorded, with a dated price snapshot.
- **Calibration split ≠ test split.** Hydra's se/sp are fit on a disjoint calibration split, and learning curves are reported at n = 0, 20, 50, 100, 200 observations per class. A router fitted on its own test set measures nothing.
- **Seeds and reporting.** Seeds are fixed, and results go to `bench/` whether or not they flatter Hydra.

| Kind | Suites | Why these | Metrics |
|---|---|---|---|
| classify | CLINC150 (150 intents + OOS), Banking77, a CLINC 10-intent subset (to reproduce [I]), AG News, SST-2, MASSIVE (multilingual intents) | Label-count strata 2 / 4 / 10 / 20 / 77 / 150 expose cap and accuracy cliffs; OOS tests abstention | accuracy, macro-F1, OOS recall/precision, ECE of `p_correct`, ECE of native scores |
| extract (NER) | CoNLL-2003, CrossNER (5 domains), MIT Movie/Restaurant, MultiCoNER II / WikiANN | In-domain vs zero-shot OOD vs multilingual | strict span F1 (boundary + type), relaxed F1 |
| extract (documents) | SROIE, CORD, FUNSD, CUAD | Receipts → forms → long contracts; CUAD is ScaleDown's own suite | field exact match after `canon`, entity F1, per-field accept rate at target |
| rerank | BEIR subset, MIRACL | English + multilingual | nDCG@10, top-1 agreement |
| summarize | QMSum | ScaleDown's suite; long inputs | ROUGE-L, `grounded` flag rate |
| PII | ai4privacy, presidio-synthetic | For local-only routes | span F1 per entity type |

**Baselines:**

| Baseline | What it is |
|---|---|
| always-frontier | `llm-structured` on a frontier model; the bar for `quality` |
| always-cheapest-LLM | gpt-5-nano-class, or local Qwen3 small |
| each task model alone | GLiNER2.5, GLiNER2, ScaleDown, DeBERTa-v3-large-zeroshot-v2.0-c, GLiClass-large, SetFit at 8 per class, NuExtract-2.0-8B |
| static fallback chain | — |
| FrugalGPT-style threshold cascade | escalate when the native top score is below τ, with τ tuned on the calibration split; **the baseline Hydra must beat**, since it uses the same native scores without calibration |
| oracle router | the per-item best head; the upper bound |

**Reported per (suite, objective):**
- pass rate
- accepted-answer error rate against α
- cost per accepted answer
- escalation rate
- mean sources
- p50/p95 added latency
- the share of items where a task model was the accepted source

---

## A.11 Risks

| Risk | Detail | Mitigation |
|---|---|---|
| **Vendor lock-in** | ScaleDown keys come via a sales contact [P]; self-hosting is a licensed container, not open weights [I]; credits ran out at ~1M tokens with `402` [I] | Typed internal request with thin adapters; Jev and OpenAI decisions shapes are portable; an Apache-2.0 head (GLiNER2.x) is always eligible on the same classes. `402` is a `health` failure kind, not a model verdict |
| **Label-set mismatch** | 20-label cap; fixed-label heads; NLI cost linear in labels | Eligibility (§A.5.3) refuses before routing; label-set fingerprint in calibration; no silent label truncation |
| **Latency stacking** | Sequential cascade p50 sums to 4.7 s in §A.8 | Hedging, a concurrent free floor, `max_latency_ms`; `quality` puts the best head first |
| **Licences** | GLiNER v0/v1 CC BY-NC; LayoutLMv3 CC BY-NC-SA; jina-reranker-v3 CC BY-NC; NuExtract 2.0 4B research-only; DeBERTa zeroshot non-`-c` trained partly on NC data; Llama Guard community licence; NVIDIA GLiNER-PII under NVIDIA's licence | `commercial` cap per head, default deny; `hyctl security` lists the licences in use |
| **Multilingual** | English-default checkpoints; 22M guard English-only; GLiNER2.5 multi's extraction is below GLiNER2 [V] | `languages` cap against the language signal; calibration per class already separates languages when the class does |
| **Distribution shift** | Prompt Guard 2: 97.5% in-distribution, ~1% buried [P vs I] | Vendor numbers are priors only; shadow co-votes and feedback measure the real distribution |
| **Correlated task models** | Lineage unknown for hosted models; ScaleDown and GLiNER2.5 score within 0.2 pp [V] | Lineage families; measured co-agreement discount (`trust/coupling.go`) |
| **Offsets** | Byte vs code point vs UTF-16 | Adapters normalise to code points; `span_in_source` checks `source[start:end]` |
| **Specificity cold start** | Constrained heads are always "confident", and generator-only writes leave sp ≤ 0.5 (#771) | Shadow co-votes plus field-level `/v1/feedback`; benchmark seed matrices |
| **Data egress** | Hosted task models see the text | `local_only` routes see only sidecar/local heads; the existing egress gate covers the new executors |
| **Benchmark claims** | Vendor accuracy (ScaleDown "+8.72%", NuExtract "+9 F", Fastino suites) is self-reported with configurations unpublished | §A.10 reproduces on public suites before any README claim (RFC §7) |

---

## A.12 Open questions

1. **Repeated fields.** Line items need alignment before per-item equivalence. A Hungarian match on canonical keys has no threshold, but needs a declared item key per schema.
2. **Rerank verification.** Top-1 agreement is weak evidence. Should rerank be routed by offline nDCG only, with SPRT disabled?
3. **Summaries.** Is `grounded` plus `judge` enough evidence to cascade summarisers, or should `summarize` be single-source with failover only?
4. **Fixed-label heads in multi-class routes.** A Banking77 fine-tune is the likely winner on Banking77 traffic. Should Hydra help users *produce* such heads (SetFit on `/v1/feedback`-labelled data), or is that the "we don't train models" line in RFC §6? The current answer is no, but the evidence in §A.3.1 is the strongest argument for revisiting it.

---

## A.13 Unverified claims in this appendix

These came only from secondary sources, or from pages whose relevant parts we could not read:
- the OpenAI Decisions API shape, limits and price (we did not read developers.openai.com directly)
- Jev pricing
- Cohere Rerank pricing
- Reducto, LlamaParse and Unstructured pricing
- the ModernBERT licence
- Whisper large-v3-turbo's licence and price
- the GLiNER v2.1 checkpoint licence (README and listings, not each card)
- the Llama Guard 4 licence terms
- the SetFit "+8.6 over GPT-3" margin
- olmOCR-Bench and OmniDocBench leaderboard numbers
- NVIDIA GLiNER-PII figures (via an aggregator quoting the card)
- the 2026 dates of GLiNER2.5, GLiNER2.5-Decide and ScaleDown's launch (taken from the pages themselves)
- the Prompt Guard 2 buried-injection result (Spanish-language secondary write-up)
- ShieldGemma and Donut, which were not checked
