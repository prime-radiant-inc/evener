# Issue #4562: ATIF final_metrics prompt-side wire volume

Status: revised after PAR, 2026-10-23. The issue's explicit acceptance criteria authorize this bounded export correction.

## The undercount

`agent/internal/atif/atif.go` builds `final_metrics.total_prompt_tokens` by summing each assistant turn's `llm.Usage.InputTokens`. `llm/types.go` defines that field as "new input tokens billed at the full input rate", i.e. cache-miss input: adapters are required to subtract cache reads and cache writes. Cache reads, 5-minute cache writes, and 1-hour cache writes are separate `llm.Usage` fields and are added to `Usage.TotalTokens`, not to `InputTokens`.

The exporter already reports the cache-read term as `final_metrics.total_cached_tokens`, but it publishes no prompt-side total that includes the cached volume, and the ATIF viewer copies `total_prompt_tokens` alone as its prompt figure. In the Terminal-Bench 4.0 run from #4480 the export read 12,955,081 prompt tokens while the prompt-side wire volume was 12,955,081 cache-miss + 1,471,103,232 cache-read = 1,484,058,313 tokens: a 114.6x undercount (the issue's "~128x" does not match the arithmetic of its own two figures). `meta.json`'s `cumulative_usage` already carries the missing term as `cache_read_tokens`.

## Contract

Existing `final_metrics` fields keep their current names and values:

- `total_prompt_tokens` — cache-miss input only, the sum of `llm.Usage.InputTokens`. It remains cache-miss-only by this issue's constraint; the divergence from ATIF's inclusive definition is tracked in #4924.
- `total_completion_tokens` — the sum of `llm.Usage.OutputTokens`.
- `total_cached_tokens` — cache-read input, the sum of `llm.Usage.CacheReadTokens`; this is the ATIF-standard cached-token field and remains the source of truth for the cache-read term.
- `total_steps` — the number of emitted trajectory steps.

Two fields are added to `final_metrics.extra`:

- `cache_read_tokens` (integer) — the same cache-read sum as `total_cached_tokens`, named after `cumulative_usage.cache_read_tokens` in `meta.json` and in the issue. It is an explicit alias required by the issue, not a second measurement.
- `prompt_wire_tokens` (integer) — the prompt-side wire volume: `total_prompt_tokens + total_cached_tokens +` the sums of `llm.Usage.CacheWriteTokens` and `llm.Usage.CacheWrite1hTokens`. For turns whose adapter honors the `llm.Usage` invariant below, this is every cache-miss, cache-read, and cache-write input token the counted turns sent to the provider.

The wire total follows `llm.Usage`'s documented invariant (`TotalTokens = InputTokens + OutputTokens + cache reads + cache writes`). It is defined from the components, not from `TotalTokens - OutputTokens`: Anthropic's streaming path updates `OutputTokens` without recomputing `TotalTokens` (`llm/providers/anthropic/stream.go:283-291`), so that subtraction is not a valid oracle. The sums use the same per-turn gate as the existing totals: a turn contributes only when it produced a `StepMetrics` block, which `convertAssistantTurn` creates when `InputTokens > 0 || OutputTokens > 0`. A response with cache tokens but both counts zero is not counted; no supported provider produces one.

One supported adapter currently violates the invariant: Google's `parseUsage` takes `InputTokens` from `promptTokenCount`, which includes cached content, so adding `CacheReadTokens` double-counts cache reads for Gemini. That adapter defect is out of scope here and is filed as #4923; the field is correct for the Anthropic and OpenAI-compatible cached runs this issue measured.

Both keys are always present when `final_metrics` is emitted, including when they are zero, so an absent key identifies an older exporter rather than a zero measurement.

## ATIF definition divergence

ATIF RFC 0001 defines `total_prompt_tokens` as including cached tokens and `total_cached_tokens` as its subset, so the RFC's `non_cached_total = total_prompt_tokens - total_cached_tokens` is already negative for this exporter in cached runs. The issue's constraint preserves the existing field and its meaning, so this change is additive: `prompt_wire_tokens` carries the RFC-intended prompt total under a new, non-standard name. Aligning or documenting the standard field's meaning is tracked separately in #4924. Consumers that need the true prompt volume must read `prompt_wire_tokens`; the third-party ATIF viewer is not changed here.

## Why `extra`

ATIF v1.7 defines `extra` on `FinalMetrics` as the extension point for custom aggregate metrics. Harbor's `src/harbor/models/trajectories/final_metrics.py` declares `model_config = {"extra": "forbid"}` for every non-`extra` field, and `TrajectoryValidator` reports `extra_forbidden`, so new top-level keys would make an otherwise valid trajectory fail Harbor's validation. The new names therefore live inside the existing `final_metrics.extra` object; no existing top-level name or value changes and no schema version bump is needed.

## Ownership and seam

The only production change is the final-metrics accumulation in `ConvertTranscriptWithOptions` in `agent/internal/atif/atif.go`: it accumulates the cache-read and cache-write terms alongside the existing prompt, completion and cached totals and populates `FinalMetrics.Extra`. Per-step metrics, step construction, transcript reading, session metadata, `meta.json`, and all usage accounting are untouched.

## Scope and non-goals

Allowed changes are this spec, the ATIF final-metrics mapping in `agent/internal/atif/atif.go`, and its tests. No change to the meaning of `total_prompt_tokens`, no new top-level ATIF fields, no change to per-step `metrics` or its `extra`, no change to `cumulative_usage` or any provider adapter, no viewer or Harbor change, no labels, and no unrelated cleanup.

A fold copy re-appended by compaction carries `OriginalOrdinal` and the original turn's usage, and the exporter currently counts those copies in its totals; that is a separate accounting defect tracked in #4925 and is not changed here. Until it is fixed, `prompt_wire_tokens` is the prompt-side volume of the same turn set the existing totals count, which can exceed the session's real spend in compacted sessions.

## Verification

1. Add a red-first test in `agent/internal/atif/atif_test.go` with assistant turns whose accumulated usage has cache-miss input, cache-read input, cache-write input, and cache-write-1h input. Assert the existing `total_prompt_tokens` (cache-miss only), `total_cached_tokens`, and `total_completion_tokens` values, plus `FinalMetrics.Extra["cache_read_tokens"]` and `FinalMetrics.Extra["prompt_wire_tokens"]`. Round-trip the trajectory through JSON and assert the same values under `final_metrics` and `final_metrics.extra`, including that both keys are present when their values are zero.
2. Show the test failing before the production change and passing after it; retain both outputs.
3. Run `go test ./agent/internal/atif` and `make build`. Do not run the full suite; CI is the source of truth.
4. Freeze and review this spec with two independent competing PAR reviewers. Review the immutable implementation diff with another two-reviewer PAR round, run simplify-code, commit and branch RoboRev, then push and shepherd a PR. Fetch current main before the final push and merge it if it moved.
