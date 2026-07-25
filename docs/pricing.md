# Pricing — Hydra

## Free (Open Source)

- **Price**: $0 — free forever
- **License**: MIT
- **Source**: https://github.com/ankit373/hydra
- **Install**: `brew tap ankit373/hydra && brew install hyctl`
- **Limits**: None — route as many tasks as you want
- **Support**: GitHub Issues and Discussions

## What You Need

- Go 1.22+ (only if building from source; Homebrew binary includes everything)
- Ollama (optional — for local model support at tiers 9-10)
- Your own API keys for cloud models (Anthropic, Google, or OpenRouter)

## Model Costs (You Pay the Providers Directly)

Hydra itself is free. The underlying models charge per token:

| Model | Tier | Input cost | Output cost | Notes |
|-------|------|-----------|-------------|-------|
| Claude Opus 4.7 | 1 | ~$15/M tokens | ~$75/M tokens | Anthropic |
| Claude Sonnet 4.6 | 2-3 | ~$3/M tokens | ~$15/M tokens | Anthropic |
| Gemini 2.5 Pro | 4-5 | ~$1.25/M tokens | ~$10/M tokens | Google |
| Gemini 2.0 Flash | 6-8 | ~$0.075/M tokens | ~$0.30/M tokens | Google |
| Qwen3 (local) | 9-10 | $0 | $0 | Ollama, runs locally |

Run `hyctl pricing list` for live rates fetched from OpenRouter.

## Typical Savings vs All-Claude Sessions

| Session type | Without Hydra | With Hydra | Saved |
|-------------|--------------|-----------|-------|
| Write DTO / interface | ~$0.003 | $0.000 (local) | 100% |
| CRUD controller + tests | ~$0.008 | ~$0.0001 | ~99% |
| Code review + refactor | ~$0.012 | ~$0.004 | ~67% |
| Architecture decision | ~$0.020 | ~$0.020 | 0% |
| **Typical 50-call session** | **~$0.22** | **~$0.04** | **~82%** |

Pricing based on OpenRouter rates as of June 2026.

## Enterprise / Commercial Support

No commercial offering currently. Hydra is maintained by Ankit Jha as an open source project.
For consulting or custom deployment help, open a GitHub Discussion.
