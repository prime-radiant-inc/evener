# Observer Sidecars: Live Examples and API Lessons

Date: 2026-06-19
Branch: `wip/job-control-handle-split-impl`

Observer sidecars ended up being simpler than the early designs suggested. The useful shape is:

```text
delegate       starts a durable observer conversation
job_watch      sends bounded frames to that observer when a condition fires
delegate_send  lets the observer report back to the caller when it has something useful
```

No separate observer primitive is required for v1. The hard part is not the public API. The hard part is preserving provenance through every queue and durable event so a watch does not observe its own descendants.

## Live Runs

These are real sessions from the sidecar/observer work. They are useful because they include the model behavior, not just unit-test scaffolding.

| Use case | Parent session | Observer session | Evidence |
| --- | --- | --- | --- |
| Snide commentary in observer's own thread | `01KVF48R8B3A15THEDMPRBQ9C2` | `01KVF4940RF5J956AG52MS314J` | Observer wrote `SNIDE_NOTE` in its own transcript and did not call `delegate_send`. |
| Monty Python injection on `actually` | `01KVF40N0MV1R492KM4QJY7QN0` | `01KVF4142QC88J1VX8QH9RQ715` | Parent received `PYTHON_QUOTE ... quote=Ni!` for external matching messages; same-watch provenance suppressed injected/ack traffic. |
| Output-match observer with read grant | `01KVF6E7HP0JMT2FPTWAJMK0NX` | `01KVF6JDEQRTVR76QDJG085QWY` | `watch_read_grant`, one delivery `wd_01KVF6JQCKJJ409A9KSDHP0G1P`, observer read the watched shell output and sent `OUTPUT_READ_GRANT_DONE`. |
| Job notification observer with Kimi | `01KVF85AV90RY3HPYVEPB352T7` | `01KVF898CSACJ9FSHFX5DT92J3` | One watch delivery ID `wd_01KVF8A19AJ23H2GK6ZBPPB6N2`; observer sent `NOTIFY_DONE`; observer delegate completion carried provenance and did not retrigger the watch. |
| Failed notification observer before fix | `01KVF6V91S6Q389NBJ9CB6MY1T` | observer delegate under that parent | The observer's own delegate completion looked external because `job_finished` lacked provenance, so the same `job.notification` watch fired again. |

## Example 1: Commentary Without Injection

Scenario: watch caller events and send frames to a sidecar, but keep the commentary in the observer's own transcript.

Pattern:

```text
delegate(task="First say SNIDE_READY. Later, for each Watch frame, write SNIDE_NOTE using communicate only.")
job_watch(target="caller", events=["assistant.tool", "communicate"], send={to: <observer delegate_id>})
```

What this proves:

- An observer does not have to inject anything into the caller to be useful.
- `communicate` inside the observer stays in the observer thread.
- The parent can audit the observer transcript by `transcript_ref`.
- The parent job log shows watch send lifecycle events, but no caller steering from the observer.

This is the safest default for commentary, review notes, lint-style observations, and "watch what I am doing and keep notes" workflows.

## Example 2: Conditional Caller Injection

Scenario: whenever the caller says the whole word `actually`, inject a Monty Python quote into the caller session.

Pattern:

```text
delegate(task="First say PYTHON_READY. Later, inspect event.message in the Watch frame. If it contains whole-word actually, delegate_send(to=\"caller\", message=\"PYTHON_QUOTE delivery=<delivery_id> quote=Ni!\").")
job_watch(target="caller", events=["communicate"], send={to: <observer delegate_id>, message: "Filter this caller message."})
```

What this proves:

- Caller-event frames contain the actual observed payload, not only metadata.
- A watch can inject steering through an observer when that is the intended policy.
- Same-watch provenance suppresses the observer's own injected steering and later acknowledgement traffic.
- A later human/top-level input resets active provenance, so another external `Actually ...` can legitimately trigger the watch again.

This is the shape for policy nudges, live coaching, style enforcement, and workflow automation. It is also the shape that makes loop suppression mandatory.

## Example 3: Output Observer With Read Grant

Scenario: an observer watches a shell job's output, then reads the watched job directly before reporting back.

Pattern:

```text
delegate(task="First say OBSERVER_READY. Later, read job_id from the Watch frame, call job_read_output(job_id), then delegate_send(to=\"caller\", ...).")
shell(background=true, command="... prints token ...")
job_watch(target=<shell job_id>, output_match="TOKEN", send={to: <observer delegate_id>, include_excerpt: true})
```

Live evidence from `01KVF6E7HP0JMT2FPTWAJMK0NX`:

- Shell job: `job_01KVF6JCYMPY55093NYD84ETYQ`
- Observer delegate: `dlg_01KVF6JCYVVXTD856KR5S6RHAF`
- Delivery: `wd_01KVF6JQCKJJ409A9KSBZ1H6RQ`
- Parent job log contained `watch_read_grant`, `watch_send_pending`, and `watch_send_delivered`.
- The observer read the parent-owned shell output and sent `OUTPUT_READ_GRANT_DONE`.

What this proves:

- The observer's authority is narrow: read the watched job output, not list/stop arbitrary parent jobs.
- The grant is keyed to the observer session, so it survives the observer's follow-up turn getting a new `job_id`.
- The frame carries a `delivery_id`, and the observer can echo that ID back so the parent can correlate frame, read, and comment.

This is the shape for build-log triage, server readiness observers, benchmark monitors, and "call me only when this output appears" agents.

## Example 4: Notification Observer and the Loop Bug

Scenario: a `job.notification` watch on `caller` sends completion frames to an observer, and the observer reports `NOTIFY_DONE`.

The first live Kimi run found a real bug. The shell completion fired the watch correctly. The observer processed the frame and completed its own delegate job. That delegate job completion emitted another `job.notification`, but the emitted `job_finished` event did not carry the watch provenance, so the same watch treated it as external and fired again.

Fix:

- `job_finished` events now carry the run's causal provenance.
- Reconciled lost-job terminal events and forward-failed terminal events carry provenance too.
- Running delegate sends fall back to the active turn provenance when the caller does not pass explicit provenance.
- Terminal watch auto-removal now appends durable `watch_cleared` before detaching in-memory state.

The fixed live Kimi run:

- Parent session: `01KVF85AV90RY3HPYVEPB352T7`
- Observer session: `01KVF898CSACJ9FSHFX5DT92J3`
- Watch: `watch_01KVF89SJY7JFZJJ4EQHT53518`
- Delivery: `wd_01KVF8A19AJ23H2GK6ZBPPB6N2`
- Trigger shell job: `job_01KVF8A12NXY93S0MZK0MKWX16`
- Observer follow-up job: `job_01KVF8AENZ2W3FV2MGCA4ZANQ3`

The corrected `jobs.jsonl` had one logical delivery ID. The observer follow-up `job_finished` carried provenance, and there was no second delivery ID caused by the observer's own completion.

One diagnostic wart remains: the log contained two identical `watch_send_pending` rows for the same delivery ID/update sequence before the delivered marker. That is not a loop, because the durable key and `update_seq` are identical and folding treats it as one current pending state. It is still noisy and worth cleaning up later.

## What Kimi Stumbled On

Kimi completed the fixed notification scenario, but the transcript shows several API ergonomics lessons:

- It needed explicit instruction to use `agent_type="default"`. In an earlier output-read scenario it chose the limited `subagent` role, which lacked `job_read_output`, `delegate_send`, and `job_watch`.
- It used the right `delegate_id` for `send.to` only because the prompt emphasized "not the job_id." The `job_list` recovery row is important because agents will lose this handle.
- It did not naturally know how to wait for an observer's `delegate_send(to="caller")` message. It used `job_list`, then the steering message arrived.
- After the main success response, ordinary terminal notifications for the observer and shell jobs woke another model turn. That is correct behavior, but models can treat those confirmations as new work unless prompts/docs say otherwise.
- Visible assistant text during the test made the transcript noisier. For clean scenario runs, prompts should ask the model to use tools and final response only, or the harness should inspect logs rather than transcript prose.

## API Shape That Feels Right

The right public surface is still the small one:

```text
delegate
job_watch
delegate_send
job_read_output
job_list
job_stop
```

Observers do not need a special public type. They need:

- a durable delegate conversation (`delegate_id`);
- a way to receive bounded frames (`job_watch(..., send={to:<delegate_id>})`);
- a way to report back (`delegate_send(to="caller")`);
- a narrow output read grant when watching a concrete job;
- causal provenance on events, durable notifications, steering, and delegate job records.

The safety boundary should stay implementation-level, not policy-level. A sidecar can inject memory, advice, snark, quotes, or nothing at all. Serf's job is to make those policies easy to implement without accidental loops, lost frame content, or ambiguous handles.

## Recommended Agent Playbook

For an observer that needs tools:

1. Start it with `agent_type="default"` unless a narrower role explicitly has the needed tools.
2. Make the first observer turn finish quickly with `READY`.
3. Install `job_watch` with `send.to` set to the observer `delegate_id`.
4. In the observer task, tell it exactly which frame fields to read and exactly when to stop.
5. Use `delivery_id` in observer reports so the parent can correlate events.
6. Clear long-lived session watches before continuing open-ended conversation if later acknowledgements should not be watched.

For a safe observer:

```text
Use communicate in the observer thread.
Do not call delegate_send(to="caller").
```

For an injecting observer:

```text
Call delegate_send(to="caller") only when the frame meets a narrow predicate.
Include delivery_id in the injected message.
Trust causal suppression to prevent same-watch descendants from retriggering, but keep the predicate narrow anyway.
```

## Definition of Done for Observer Readiness

Observer support is not "done" because one demo works. It is done when these stay true across unit tests, scenario docs, and live model runs:

- Watch frames contain the observed content needed by the observer.
- Same-watch descendants do not retrigger the watch.
- Later unrelated top-level user input can trigger again.
- Notification, steering, delegate-start, delegate-finish, and durable pending records all preserve provenance.
- Observer read grants work for concrete watched jobs and do not broaden into list/stop authority.
- `job_list` exposes enough delegate recovery state for an agent to find the correct `delegate_id`.
- Failure paths leave enough live/durable state to retry instead of corrupting recovery.
- The docs teach the idle-observer pattern and the difference between `delegate_id` and `job_id`.

