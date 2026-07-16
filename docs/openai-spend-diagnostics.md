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

`input_tokens` in JSON output (`in_tok` in the human table) is Serf's normalized
uncached input count. The OpenAI adapter has already subtracted the provider's
cached-token subset. `uncached_input_tokens` in JSON (`uncached` in the human
table) is the same normalized value and is what `--cache-spikes` compares with
the threshold; it does not subtract cache reads a second time.

`cache_read_tokens` is the number of prompt tokens served from provider prompt
cache (`cache_read` in the summary). It is reported separately from
`input_tokens`. Cache reads are still billable on OpenAI, but they are expected
to be cheaper than uncached prompt input. A rising cache-read count usually
means the session is reusing stable prompt prefix content.

The summary prints calls, empty responses, errors, normalized input tokens,
output tokens, cache-read tokens, `total`, and average latency. Here `total` is
Serf's normalized input-plus-output total; it does not add cache-read tokens.
The command does not calculate cache-write totals or a cache-hit percentage.

The detailed human table prints one row per provider attempt. It includes the
attempt and group IDs, attempt index, provider/model, outcome, settlement state,
settlement-derived final attempt count, latency, token counts, text length, and
tool-call count. A `-` final-attempt count means no count was established: a
clean EOF is `unsettled`, while a partial tail is `unknown_outside_range`.

`--cache-spikes` filters that same attempt table to rows whose normalized
uncached input meets or exceeds the threshold. These are candidates for context
churn. A spike can be expected on the first call of a session or after major
context changes, but repeated spikes in the same session usually deserve a look
at transcript growth, changing system/developer prompt content, tool-result
bulk, or missing cache-key continuity.

## OpenAI Prompt Cache Defaults

Serf applies conservative OpenAI prompt-cache defaults at the session request
boundary:

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
