# Adaptive OpenAI Wire Protocol

## Goal

Let OpenAI-shaped provider instances select the runtime wire protocol safely:
Responses API first when configured for OpenAI or `api_style = "auto"`, with a
deterministic fallback to Chat Completions when the Responses endpoint indicates
the model or endpoint is unsupported.

## Tasks

- [x] Add config support for `api_style = "auto"` on `type = "openai"`.
- [x] Register the first-party OpenAI adapter for the `auto` style.
- [x] Add non-streaming `Complete` fallback from Responses to Chat Completions.
- [x] Make env-seeded `openai-compatible` adaptive for both `Complete` and
      `Stream` while preserving explicit `api_style = "chat-completions"` as
      forced Chat Completions.
- [x] Guard fallback so Responses-only continuation state is not silently
      downgraded to Chat Completions.
- [x] Add deterministic tests for config registration, non-stream fallback, and
      continuation fallback blocking.
- [x] Run focused package tests and commit the implementation.

## Notes

- Do not change legacy `api_style = "chat-completions"` behavior; that remains
  the explicit OpenAI-compatible adapter path.
- Env-seeded `openai-compatible` now probes `/responses` before falling back to
  `/chat/completions`; compatible-provider quirks stay on the Chat fallback.
- Do not enable generic continuation on Chat Completions fallback.
- Live provider tests stay opt-in only.
