# 2026-06-20 Kimi For Coding Tool Fluency Run

## Setup

- Branch: `wip/tool-fluency-framework`
- Model: `kimi/kimi-for-coding`
- Runner: `go run ./tools/tool-fluency/cmd/serf-fluency`
- Full result directory: `/tmp/serf-fluency-kimi-all-1`

Catalog command:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency catalog --model kimi/kimi-for-coding
```

Run command:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run \
  --build \
  --model kimi/kimi-for-coding \
  --probe all \
  --timeout 10m \
  --out /tmp/serf-fluency-kimi-all-1
```

## Summary

- Cataloged model-facing tools: 22
- Probes run: 20
- Passed: 18
- Failed: 1
- Skipped unavailable: 1

`web_search.current` was skipped because the Kimi profile does not advertise
Serf's `web_search` function on this surface.

## Failure

### `job_watch.observer_callback`

Status: failed.

Evidence:

- Full run session: `01KVK05XEKEXR7MFAGYSY6W7MP`
- State dir: `/tmp/serf-fluency-kimi-all-1/job_watch.observer_callback/rep-01/state`
- Calls: `delegate:1`, `job_watch:1`, `read_file:1`
- `serf-doctor watches` showed one matching delivery, delivered, with no self-loop.
- The watch-delivered delegate resume was cancelled with `stopped_by_parent`
  during CLI teardown.
- Kimi also rewrote the delegated observer task to say
  `communicate OBSERVER_READY with await_reply=false`, despite the system prompt
  saying observer readiness that keeps waiting should use `await_reply=true`.

Root cause:

The runtime delivered the watch frame, but the current CLI harness closed the
session before the observer callback could complete. Kimi also made the
readiness liveness mistake in the parent-authored delegate task, so this probe
captures both the same CLI/live-session harness gap and a Kimi-specific
instruction-transformation issue.

## Notable Differences From GPT-5.4 Mini

- `web_fetch.example` passed with one `web_fetch` call. GPT-5.4 Mini skipped
  `web_fetch` and answered from model knowledge.
- `list_dir.inventory` used canonical `list_dir`, while GPT-5.4 Mini surfaced
  the OpenAI alias issue by satisfying the probe through canonical `glob`.
- `job_watch.observer_callback` did not trigger a `job_watch` validation error;
  GPT-5.4 Mini sometimes tried invalid `include_excerpt` on `target="caller"`
  before repairing.

## Recommended Next Work

1. Build a live-session or hub-backed harness before treating observer callback
   probes as model-only failures.
2. Add a metric for delegate task preservation so parent agents that rewrite
   callback/readiness instructions are visible.
3. Compare `web_fetch` behavior across less familiar URLs; Kimi selected the
   tool correctly on `example.com`, GPT-5.4 Mini did not.
