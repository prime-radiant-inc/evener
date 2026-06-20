# Sidecar scenario Kimi fluency notes

Date: 2026-06-20
Branch: `wip/passive-observer-sidecars`
Model: `kimi/kimi-for-coding`

These notes record the live sidecar scenario attempts used to seed the
scenario cards in `test/scenarios/sidecar-*.md`. The first pass used an
ad hoc runner; that was the wrong long-term shape. The durable sessions
below were then audited with `serf-doctor`, which is the supported
inspection surface for transcripts, watches, and observer topology.

## Summary

Kimi can execute the sidecar pattern, but fluency depends heavily on
the watch shape:

- `assistant.tool` watches with an explicit `event_filter` were the
  most reliable.
- `communicate` watches exposed a common wrong-trigger failure: Kimi
  sometimes created `events: [assistant.message]`, which woke the
  observer on the parent's own tool-call turns. The follow-up API
  cleanup removes `assistant.message` from the public `job_watch`
  event vocabulary, so this should now fail fast and recover with a
  `communicate` watch.
- Readiness matters. If the parent creates a watch before the
  observer's first `*_READY` turn is terminal, the real frame can be
  consumed by setup behavior.
- Parent `SCENARIO_DONE` markers are not enough evidence. The audit
  must check the watch lifecycle and observer transcript.

## Runs

| Scenario | Parent session | Doctor-audited result |
|---|---:|---|
| Approval broker | `01KVHN0SQ01XSBBEKQG30B13DK` | Good watch shape: `events: [communicate]`; one delivered frame; observer sent `APPROVAL_PACKET`. |
| Drift detector | `01KVHN25H2W2297CZAX98MNDWH` | Fluency failure: first watch used `events: [assistant.message]`, causing repeated ignored frames and one dropped frame during cleanup. |
| Artifact freshness | `01KVHN6HTH35N6SFA1EG5FQZS6` | Fluency failure: used `events: [assistant.message]`; many irrelevant deliveries; did not cleanly exercise the intended artifact frame. |
| Memory reminder | `01KVHNAS2E6TR4CNQBYN3JKTYQ` | Runtime watch filter worked, but scenario failed semantically: watch fired before observer readiness was complete, so the observer only produced `MEMORY_READY`. |
| Secrets monitor | `01KVHNEJE2WPC0K19NDPBW03RZ` | Pass with notes: filtered `read_file` watch delivered; observer sent redacted `SECRET_FINDING`; parent duplicated a read and emitted extra prose after completion. |
| Stuckness error | `01KVHNH1NB30XZERAKPP4B6GGZ` | Recovered pass with notes: first observer used a leaf agent without `delegate_send`; parent restarted with a default agent and completed. |
| Test triage | `01KVHNKYWK1ADH74A6G88YEGDD` | Pass with notes: output-match watch delivered; observer triaged; parent emitted extra narrative after completion. |
| Progress digest | `01KVHNPZVFZRZZ8XV9GZVJB4GV` | Pass with notes: output-match watch delivered; parent used one extra `job_list`. |
| Handoff packager | `01KVHNRQK1PAFPBC8GMJX3YV1Y` | Pass with notes: `job.notification` watch delivered; worker and observer children were both visible in `serf-doctor tree`. |
| Runbook capture | `01KVHNVJMKY9AZ2NBQ1W6D0MTJ` | Pass with notes: output-match watch delivered; parent used extra waiting/summary turns. |
| Feedback governor | `01KVHNXA99NENT08QQJV7DVYFC` | Pass: `events: [communicate]` watch delivered and was cleared. |
| Quality auditor | `01KVHPNCYTGQE00HZ7HK8H04E9` | Pass with notes: tightened manual run used `events: [communicate]`; observer sent `QUALITY_FINDING` with one `delegate_send`, two `communicate` calls, and no `job_list`. |

## Tooling conclusion

The current scenario corpus is human/agent-readable markdown, and
`serf-doctor` is the correct audit surface. The missing tool is a
first-class live scenario runner that can:

1. create hermetic workdirs and fixtures from a structured scenario
   definition;
2. spawn a session with a chosen model;
3. wait for terminal state with bounded cleanup;
4. run `serf-doctor` audits against the resulting parent and observer
   sessions; and
5. report semantic pass/fail plus fluency notes.

Until that exists, do not build new one-off JSONL parsers around these
scenarios. Run the markdown cards manually and audit with `serf-doctor`.

## Final callback-fluency retest

After the passive-observer fixes, one remaining Kimi failure was root
caused to the built-in `subagent` role. When the parent explicitly used
`agent_type:"subagent"`, that role froze its allowed tools without
`delegate_send`. The observer prompt therefore listed `delegate_send`
as unavailable, and Kimi correctly finished with `communicate` instead
of calling back to the parent. The fix is to include `delegate_send` in
the built-in `subagent` tools and to state positively that reports use
`communicate` while observer callbacks use `delegate_send(to="caller")`.

Fresh binaries were rebuilt and the memory/approval sidecar scenarios
were rerun through the hub REST shim, with `serf-doctor` transcript
audits:

| Model | Scenario | Parent session | Observer session | Audit result |
|---|---|---:|---:|---|
| `kimi/kimi-for-coding` | Memory reminder | `01KVJ13N9GN8WBK3SHZ53PVX1T` | `01KVJ13V30DBVN9HXANTM16EMS` | Pass: observer `delegate_send: 1`, `communicate: 2`; parent `job_list: 0`, `job_read_output: 0`; parent completed from `MEMORY_REMINDER`. |
| `kimi/kimi-for-coding` | Approval broker | `01KVJ16BSQX55EACFWPSVEBT15` | `01KVJ16J39T79M9FQT0X5KGV89` | Pass: watch used `events:["communicate"]`; observer `delegate_send: 1`; parent `job_list: 0`, `job_read_output: 0`; parent completed from `APPROVAL_PACKET`. |
| `openai/gpt-5.4-mini` | Memory reminder | `01KVJ1D17EFGPQGQZ38Q5JJD68` | `01KVJ1DPZ71JS11GPBJWBYDHVV` | Pass: observer `delegate_send: 1`, `communicate: 2`; parent `job_list: 0`, `job_read_output: 0`; first watch attempt used invalid `include_excerpt` on `caller`, then repaired before triggering. |
| `openai/gpt-5.4-mini` | Approval broker | `01KVJ1G8VD357V506TPHX5TECE` | `01KVJ1HG3FG9JN45ZQC9EPSZSH` | Pass: watch used `events:["communicate"]`; observer `delegate_send: 1`; parent `job_list: 0`, `job_read_output: 0`; parent completed from `APPROVAL_PACKET`. |

The first GPT retest attempt (`01KVJ18NH2PWG29XWMVMY0024G`) failed
before any assistant turn because the daemon inherited an exhausted
`OPENAI_API_KEY`. Clearing only `OPENAI_API_KEY` while leaving normal
OpenAI OAuth state visible allowed the GPT runs above to complete.

Residual fluency notes:

- Kimi and GPT both sometimes emit an extra summary after the observer
  terminal notification confirms `MEMORY_RECORDED` or
  `APPROVAL_RECORDED`. That is not polling; it is a noisy reaction to a
  later confirmation notification.
- GPT invoked `use_skill` because the Superpowers prompt required it.
  That is unrelated to sidecar waiting, but it adds an extra tool turn
  in live runs with the plugin enabled.
- GPT's memory run repaired an invalid `include_excerpt` watcher. The
  validation error was specific and the model corrected the watch before
  triggering `read_file`.
