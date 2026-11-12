# Pricing: Hydra

## Free (Open Source)

- **Price**: $0, free forever
- **License**: MIT
- **Source**: https://github.com/ankit373/hydra
- **Install**: `brew install ankit373/hydra/hyctl` (also npm/npx/pip/curl)
- **Limits**: None, route as many tasks as you want
- **Support**: GitHub Issues and Discussions

## What You Need

- Go 1.26+ (only if building from source; Homebrew binary includes everything)
- Ollama or llama.cpp (optional, for free local models)
- Your own API keys for any hosted provider you want to use (for example OpenAI, Anthropic, Google, Mistral, AWS Bedrock or OpenRouter)

## Model Costs (You Pay the Providers Directly)

Hydra itself is free. The underlying models charge per token:

| Model (examples) | Input cost | Output cost | Notes |
|-------|-----------|-------------|-------|
| Claude Opus 4.6 | ~$5/M tokens | ~$25/M tokens | Anthropic |
| Claude Sonnet 4.6 | ~$3/M tokens | ~$15/M tokens | Anthropic |
| Gemini 3.1 Pro | $1.25-2.00/M tokens | $5-12/M tokens | Google, via Antigravity |
| Gemini 3.8 Flash | $0.075-0.825/M tokens | $0.63-4.13/M tokens | Google, via Antigravity |
| Qwen3 (local) | $0 | $0 | Ollama, runs locally |

Example rates only, OpenRouter list prices as of 2026-11-12. Run `hyctl pricing list` for live rates for every model, fetched from OpenRouter.

Hydra publishes no savings percentage. What you spend depends on which models you have and what you ask them; `hyctl cost` and `hyctl stats` report your own spend.

## Enterprise / Commercial Support

No commercial offering currently. Hydra is maintained by Ankit Jha as an open source project.
For consulting or custom deployment help, open a GitHub Discussion.
