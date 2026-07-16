# OpenAI Spend Diagnostics

Serf records OpenAI provider attempts in the canonical private per-session API log:
`<state-dir>/sessions/<session-id>.api.jsonl`. Semantic transcripts do not contain
provider attempt records or bodies. Use `serf-doctor apilog` so selectors and project
buckets resolve through Serf's own state model.

## Quick Audit

Run a per-session summary:

```sh
serf-doctor apilog <selector> --summary
```

Look for large uncached prompt spikes:

```sh
serf-doctor apilog <selector> --cache-spikes --threshold 50000
```

`<selector>` may be a session ID or transcript ref. Add `--state-dir <path>` when
inspecting an override/scratch state root.

## Metric Interpretation

`input_tokens` is uncached input after provider cache reads have been subtracted
by the OpenAI adapter. These tokens are the primary prompt cost signal when a
request misses cache.

`cache_read_tokens` is the number of prompt tokens served from provider prompt
cache. Cache reads are still billable on OpenAI, but they are expected to be
cheaper than uncached prompt input. A rising cache-read count usually means the
session is reusing stable prompt prefix content.

`cache_write_tokens` and `cache_write_1h_tokens` are prompt tokens newly written
to provider cache. Treat cache writes as prompt volume that may be useful later,
not as immediate savings. `cache_write_1h_tokens` is the one-hour retention
variant reported by providers that expose it.

Effective prompt volume is the total prompt material involved in a session:

```text
input_tokens + cache_read_tokens + cache_write_tokens + cache_write_1h_tokens
```

The summary command uses that effective prompt volume to calculate `Hit%`:

```text
cache_read_tokens / effective prompt volume
```

A high cache hit percentage does not guarantee low spend. It can coexist with
high spend when the effective prompt volume is large, when each call still has a
large uncached tail, or when the model's cached-input price remains material.
For operational audits, inspect both `Hit%` and absolute `InTok` /
`CacheRead` totals.

`UNCACHED_SPIKE` lines from `--cache-spikes` identify calls whose
`input_tokens` meet or exceed the configured threshold. These are candidates for
context churn: large prompt segments that were not served from cache on that
call. A spike can be expected on the first call of a session or after major
context changes, but repeated spikes in the same session usually deserve a look
at transcript growth, changing system/developer prompt content, tool result
bulk, or missing cache-key continuity.

## OpenAI Prompt Cache Defaults

This branch adds conservative OpenAI prompt-cache defaults at the session
request boundary:

- OpenAI requests for allowlisted model families receive a stable per-session
  `prompt_cache_key` of `serf-session-<session-id>` when the request does not
  already set one.
- OpenAI requests for those same allowlisted model families receive
  `prompt_cache_retention=24h` when the request does not already set retention.
- Explicit `prompt_cache_key` values are preserved. Explicit
  `prompt_cache_retention` values are preserved for public API-key transport.
- The allowlist is intentionally conservative: `gpt-5*` and `gpt-4.1*` model
  families.
- OAuth-backed ChatGPT/Codex transport omits `prompt_cache_retention` on the
  wire because that backend rejects the public Responses API field.

The intent is to keep long agent sessions on a stable cache namespace without
changing callers that already selected explicit prompt-cache behavior.
