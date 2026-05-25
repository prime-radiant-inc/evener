# OpenAI Spend Diagnostics

Serf records OpenAI API usage in two places under the Serf state directory:

- `<state-dir>/api.jsonl`: the process-wide API log written by `serf run`, `serf serve`, and the embedded TUI.
- `<state-dir>/sessions/*.transcript.jsonl`: per-session transcripts that include `api_call` records.

The analyzer reads either a single file or a directory. When given a directory,
it recursively finds both `api.jsonl` files and transcript JSONL files, then
deduplicates matching API calls that appear in both sources.

## Quick Audit

Run a per-session summary:

```sh
tools/api-log-analyze.py ~/.local/state/serf --summary
```

Look for large uncached prompt spikes:

```sh
tools/api-log-analyze.py ~/.local/state/serf --cache-spikes --spike-threshold 50000
```

Use `--session <id>` with either command to narrow the audit to one session.

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
- Explicit `prompt_cache_key` and `prompt_cache_retention` values are preserved.
- The allowlist is intentionally conservative: `gpt-5*` and `gpt-4.1*` model
  families.

The intent is to keep long agent sessions on a stable cache namespace without
changing callers that already selected explicit prompt-cache behavior.
