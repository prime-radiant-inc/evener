# Read-only nudge removal design

Date: 2026-06-24
Branch: remove-read-only-nudge

## Summary

Remove the agent-side read-only streak nudge globally. This is the heuristic in `agent/session_tool_round.go` that tracks consecutive post-tool rounds where every tool call is registered `ReadOnly` and injects `SYSTEM-REMINDER` steering at streak counts 5 and 10.

The feature currently sends model-facing reminders such as:

- `You have spent several turns reading without writing or running anything...`
- `You have been reading for 10 turns without acting...`

Those reminders should no longer be appended to the transcript, emitted as steering events, or surfaced through human-facing projections.

## Scope

In scope:

- Remove read-only streak tracking from post-tool steering.
- Remove both read-only streak reminder messages.
- Remove the session state dedicated only to this feature (`readOnlyStreak`) and related comments.
- Remove human-facing UX for this feature by eliminating the underlying `EventSteeringInjected` emissions for these reminders.

Out of scope:

- Do not remove `ReadOnly` metadata from tools. It remains needed for scheduling and other tool semantics.
- Do not remove loop detection or stuck escalation.
- Do not remove task-list reminders or task steering.
- Do not remove `/goal` no-progress behavior.
- Do not remove other `SYSTEM-REMINDER` sources, including interrupts, compaction/transcript reminders, task auto-advance, explicit steering, watch/delegate delivery, or plugin steering.
- Do not change TUI read-only composer capability UX.

## Current behavior

`injectPostToolSteering` currently performs several independent post-tool-round actions:

1. Track tool-call signatures and run loop detection.
2. Track `readOnlyStreak` and inject read-only nudges at streak counts 5 and 10.
3. Drain watch/delegate callback steering.
4. Drain queued steering messages.
5. Inject task reminders.

The removal targets only step 2. The remaining steps should stay in the same order relative to each other.

## Desired behavior

After removal, consecutive read-only rounds are not special. A session can inspect files, transcripts, or other read-only data for any number of tool rounds without Serf injecting the read-only nudge messages.

If another mechanism injects steering, that mechanism still works. For example, loop detection can still emit its warning, queued steering can still drain, and task reminders can still be injected.

## Implementation plan

1. Delete the read-only streak block in `agent/session_tool_round.go` that:
   - computes `allReadOnly`,
   - increments or resets `s.readOnlyStreak`,
   - switches on streak counts 5 and 10,
   - appends the two read-only `SYSTEM-REMINDER` turns,
   - emits corresponding `EventSteeringInjected` events.
2. Delete `readOnlyStreak` from `Session` in `agent/session.go`.
3. Update comments near post-tool steering and session loop state so they no longer mention read-only streak tracking.
4. Search for the removed reminder strings and `readOnlyStreak` to ensure no references remain.

## Tests and verification

The user explicitly said negative tests proving the feature was removed are not required.

Verification should still include:

- Search checks confirming `readOnlyStreak` and the two removed reminder strings are gone.
- Targeted Go tests for the touched package, at minimum `go test ./agent`.
- Full repository test suite, `go test ./...`, before declaring completion.

Existing tests that assert adjacent steering behavior must continue passing. If tests fail because they expected the removed read-only nudge, update or delete only those expectations; do not weaken unrelated coverage.

## Self-review

- Placeholder scan: no TBD/TODO placeholders remain.
- Consistency: the spec targets only the read-only streak nudge in `injectPostToolSteering` and explicitly preserves other nudges/reminders.
- Scope: this is a single focused removal, not a broader steering, task, goal, or TUI rewrite.
- Ambiguity: the exact implementation target and exact strings to remove are named.
