# Observer Provenance and Handle-Split Remediation Spec

Date: 2026-06-19
Status: remediation spec for implementation
Branch reviewed: `wip/job-control-handle-split-impl`
Builds on:
- `docs/job-control.md`
- `docs/superpowers/specs/2026-06-18-observer-watch-origin-loop-design.md`
- `docs/superpowers/plans/2026-06-18-job-control-handle-split.md`
- `docs/superpowers/plans/2026-06-18-observer-watch-causal-provenance.md`

## Problem

The branch implemented the broad shape of the design, but it is not shippable.
It fails the `agent` module test suite and still has provenance gaps on watch
delivery rails that the design specifically needed to make impossible.

The implementation did get one important architectural decision right: causal
watch provenance is represented as a deduped `(watch_id, watch_generation)` set,
with the ordered chain kept diagnostic-only. That part should be preserved.

The remediation goal is not to add more model-facing API. The goal is to make
the smaller API correct enough that observers are just ordinary composition:

1. `delegate` starts an observer conversation.
2. `job_watch` sends bounded frames to that observer or to the caller rail.
3. `delegate_send` handles observer comments back to the caller when needed.
4. The runtime guarantees provenance carriage and same-watch loop suppression.

## Non-Negotiables

- `go test ./agent/...` must pass before this branch is described as healthy.
- `go test ./...` from the workspace root is not sufficient, because the root
  module does not enumerate the nested `agent` module in the way this branch
  needs to be verified.
- Do not remove failing tests to make the suite green.
- Do not add a public `observer`, `observer_send`, `notify_agent`, or loop escape
  primitive.
- Do not add backward-compatibility behavior for old `job_send_message` or
  `job_id`-based delegate messaging unless Jesse explicitly approves it.
- Do not rely on prompts to prevent observer loops.
- Do not rely on `FromWatch` as a global guard. Same-watch suppression must be
  based on causal provenance membership.
- Do not make the diagnostic chain load-bearing. Suppression keys solely on
  `(watch_id, watch_generation)`.

## Required Outcome

After remediation:

- The handle-split contract is internally consistent across docs, tests, tool
  schema, runtime behavior, transcript rendering, and scenarios.
- Every allowed watch-send rail carries causal provenance from fire point to
  downstream effects.
- Same-watch loop suppression works for:
  - observer delegate sends to caller;
  - observer terminal notification acknowledgement;
  - caller-targeted watch-send frames;
  - output-triggered observer effects;
  - coalesced/batched watch deliveries;
  - restored pending watch deliveries after restart.
- Watch frames contain enough event content for the observer to reason about the
  event it observed, not just metadata.
- Live Kimi scenarios pass and transcripts are inspected for model confusion or
  stumble, not merely for process exit success.

## Current Failures To Remediate

### 1. `go test ./agent/...` is red

The explicit agent module suite fails. The observed failure cluster is:

- `TestCreateDelegateMarksChildConsumedAfterDurableFinish`
- `TestReconstructDelegateChildRegistryMismatchDoesNotRunRestoreSideEffects`
- `TestReconstructDelegateChildRegistryMismatchDoesNotReconcileChildJobs`
- `TestDelegateReconstructionRacingParentCloseDoesNotTrackOrRunSideEffects`
- `TestParentCloseWaitsForInFlightDelegateReconstructionClaim`
- `TestDelegateReconstructionParentCloseBeforeDeferredSideEffectsDoesNotRunThem`
- `TestTerminalDelegateRestoreRequiresStrictPreflightBeforeReconstruction`
- `TestTerminalDelegateRestoreUsesStrictPreflightHistory`
- `TestCounterReservesOnSpawnResumeDrive`

Most failures are caused by the new handle-split contract:

- `delegate_send` now defaults idle delegates to `on_idle="fail"`.
- `delegate_send` rejects `job_id` handles and requires `delegate_id`.

That contract appears intentional and is documented in `docs/job-control.md`.
The tests must be remediated to either:

- use `delegate_id` plus `on_idle="start"` when they are intentionally testing
  idle delegate resume/reconstruction behavior; or
- assert the new rejection behavior when they are intentionally testing handle
  misuse or omitted `on_idle`.

Do not change runtime behavior back to implicit idle resume unless Jesse
explicitly decides to reopen the API contract.

### 2. Caller-targeted watch-send notifications lose provenance

The implementation stores provenance on `jobstore.WatchSendState`, but the
caller notification token drops it before `acceptNotificationInput` adopts
notification provenance.

Current shape:

- `watchSendState` stores `Provenance`.
- `watchSendTokenNotification` builds a `jobNotification` for `send.to="caller"`.
- `filterDeliverableJobNotifications` resolves the token and copies only the
  frame into `n.watchSendFrame`.
- `acceptNotificationInput` unions only `n.notification.Provenance`.

Therefore the notification turn for caller-targeted frames does not inherit the
watch provenance, even though the pending state had it.

Required fix:

- `watchSendTokenNotification` should either copy `state.Provenance` into the
  returned `jobNotification`, or `filterDeliverableJobNotifications` must copy
  `state.Provenance` into `n.Provenance` immediately after resolving the token.
- Prefer copying at resolution time too, because restored or stale token paths
  must use the current pending state, not only the originally enqueued token.
- The active notification provenance adopted by `acceptNotificationInput` must
  include `WatchSendState.Provenance` for caller-targeted watch-send frames.

Required tests:

- A unit test that creates a caller-targeted watch-send pending state with
  provenance, enqueues the token, runs `acceptNotificationInput`, and asserts
  `activeCausalProvenance()` contains that watch key.
- A regression test that emits a downstream event from that notification turn
  and asserts the same watch does not fire.
- A restore-path test that folds a durable `watch_send_pending`, re-arms it,
  delivers the caller token, and asserts the adopted provenance survives restart.

### 3. `output_match` firing does not participate in same-watch suppression

`feedJobOutput` creates a synthetic root event whose provenance comes from the
watched job's stored start provenance. That is not enough. If a watch steers a
running delegate and the delegate writes matching output in response, the output
chunk is caused by the watch delivery, but the output matcher sees only the
job's original provenance.

Required invariant:

> Output appended by a running job must carry the causal provenance of the turn
> or steering message that caused that output.

Required design change:

- Add provenance to the output append path.
- `appendJobOutput` should accept an optional causal provenance argument or have
  a clearly named sibling such as `appendJobOutputWithProvenance`.
- Delegate output append sites must pass the provenance active in the child
  session when the output was produced.
- Shell output can continue to use the job record provenance unless shell input
  can be watch-steered; if a watch can steer shell-like output in the future,
  that rail must be covered before enabling it.
- `feedJobOutput` must receive the chunk provenance and suppress same-watch
  echoes before delivery accounting, just as `onSessionEvent` does.
- Coalescing output matches must union provenance if multiple chunks or matches
  are collapsed.

Required tests:

- A direct unit test where a watch on job output receives a chunk whose
  provenance already contains that same watch key; it must not enqueue a
  notification or pending send, and it must not increment delivery counters.
- A positive cross-watch test where output caused by watch A may still trigger
  watch B.
- A running delegate test where a watch send steers the delegate, the delegate
  writes matching output, and the original watch does not re-fire.
- A restart test for pending output-triggered watch sends, if the pending send is
  persisted before delivery.

### 4. Watch frames are not content-bearing for tool and result events

The current frame renderer includes a payload block only for `communicate`.
That is not enough for a real observer API. The snide observer scenario watches
`assistant.tool`, but the frame may contain only the event kind and trigger
metadata. An observer cannot make high-quality comments about a tool event
without at least the tool name, call id, status, and bounded output/error
summary.

Implementation note (2026-06-20): `assistant.message` is no longer a public
`job_watch` event kind. Models should watch `communicate` for explicit
result/status messages. Plain assistant prose remains an internal
transcript/UI event; if an internal diagnostic renders it, that does not make
it model-watchable.

Required content blocks:

- `communicate`
  - message excerpt;
  - `end_turn`;
  - truncation flag.
- `assistant.tool`
  - tool name;
  - call id;
  - success/error status;
  - bounded output or error excerpt;
  - truncation flag.
- `job.notification`
  - concrete job id when available;
  - job type;
  - status/reason;
  - output byte count;
  - transcript ref when available;
  - trigger/provenance summary if the notification itself was watch-caused.

Required safety:

- All frame fields must be bounded.
- Multi-line content must be indented or encoded so user/tool content cannot
  forge sibling frame fields.
- Do not expose new authority in the frame. A `watch_id`, `delivery_id`, or
  `transcript_ref` is diagnostic unless an existing tool already accepts it.

Required tests:

- Unit tests for each event kind's frame rendering.
- Multi-line injection tests for each content-bearing field.
- A scenario assertion that the snide observer can identify the actual tool name
  or command it observed, not merely that an `assistant.tool` event happened.

### 5. Live Kimi e2e coverage is not a substitute for transcript inspection

The existing markdown scenarios are useful, but they are not enough unless the
runner records and inspects both parent and observer transcripts.

Required live checks:

- Run the Monty Python injection scenario with Kimi.
- Run the snide observer own-thread scenario with Kimi.
- For each scenario, inspect:
  - parent transcript;
  - observer transcript;
  - durable `jobs.jsonl`;
  - watch-send pending/delivered/dropped events.
- Verify the model did not stumble:
  - no repeated observer deliveries caused by injected text;
  - no recursive acknowledgement loop;
  - no misuse of `job_id` where `delegate_id` is required after the handle split;
  - no confusion about whether snide comments belong in the parent or observer
    thread;
  - no tool/frame parsing mistakes caused by missing event content.

## Detailed Remediation Plan

### Phase 1: Make the suite honest

1. Run `go test ./agent/... -count=1` and save the exact failing test list.
2. Classify each failure:
   - stale test expecting old API;
   - runtime regression against the new API;
   - ambiguous contract gap that needs Jesse's decision.
3. For stale tests, update them to use the new contract:
   - idle delegate resume requires `Target: <delegate_id>` and
     `OnIdle: "start"`;
   - job/turn handles must not be used with `delegate_send`;
   - add explicit negative tests for omitted `on_idle` and `job_id` misuse.
4. For runtime regressions, fix behavior rather than weakening assertions.
5. Re-run `go test ./agent/... -count=1`.

Exit criteria:

- `go test ./agent/... -count=1` passes.
- The changed tests still assert meaningful behavior, not just error strings.
- New negative tests pin the handle-split contract.

### Phase 2: Close the caller watch-send provenance gap

1. Add a focused failing test for caller-targeted pending watch sends:
   - create a watch config with `send.to="caller"`;
   - fire it to record a pending `WatchSendState` with provenance;
   - enqueue or resolve the caller token through the real notification path;
   - run `acceptNotificationInput`;
   - assert active provenance contains the watch key.
2. Fix token notification provenance carriage.
3. Add a downstream same-watch suppression assertion.
4. Add durable restore coverage.

Exit criteria:

- The test fails before the code fix and passes after.
- The provenance source is the current pending `WatchSendState`, not stale token
  memory.
- Same-watch suppression happens before delivery accounting.

### Phase 3: Make output provenance load-bearing

1. Add a failing unit test for `feedJobOutput` with same-watch provenance.
2. Thread provenance into `appendJobOutput` and `feedJobOutput`.
3. Identify all append sites:
   - delegate final output;
   - delegate streaming/tool-summary output if present;
   - shell output;
   - nested forwarded output if present.
4. For delegate output, pass the child session's active provenance for the turn
   that produced the output.
5. Suppress output-match watches when the chunk provenance contains the same
   watch key.
6. Preserve cross-watch behavior.

Exit criteria:

- Same-watch output echoes do not fire.
- Cross-watch output echoes still fire.
- Existing output-match offset/catch-up tests still pass.
- No output matcher counter or pending-send delivery accounting increments for
  suppressed chunks.

### Phase 4: Finish content-bearing frames

1. Extend `writeWatchFrameEvent` beyond `CommunicateData`.
2. Add one small renderer function per event kind.
3. Keep field names stable and parser-friendly.
4. Bound and indent all user/model/tool-provided text.
5. Strengthen the snide scenario to assert the observer can identify concrete
   tool content.

Exit criteria:

- Unit coverage exists for all supported watch event kinds.
- Multi-line content cannot forge frame keys.
- Observer scenarios no longer pass with generic metadata-only commentary.

### Phase 5: Restart and coalescing audit

Audit every place that persists, folds, coalesces, or restores watch-driven
work:

- `Event.Provenance`
- `JobRecord.Provenance`
- `JobRecord.NotificationProvenance`
- `DelegateRestoreDescriptor.Provenance`
- `WatchSendState.Provenance`
- in-memory `jobNotification.Provenance`
- steering queue entries
- active turn provenance
- pending watch-send coalescing

Required invariant:

> If multiple watch-caused work items are collapsed into one delivery or one
> model turn, the resulting provenance is the union of every collapsed work item.

Exit criteria:

- Coalesced watch sends union provenance.
- Batched notification turns union provenance.
- Restored pending deliveries keep provenance.
- New top-level external input replaces active provenance with empty, so a later
  human trigger still legitimately fires the watch.

### Phase 6: Live e2e and transcript audit

Run both live scenarios with Kimi:

- `test/scenarios/job-watch-actually-monty-python-injection.md`
- `test/scenarios/job-watch-observer-snide-thread.md`

For each run, attach or record:

- parent session id;
- observer delegate id;
- observer transcript ref;
- exact model id;
- pass/fail notes;
- parent transcript findings;
- observer transcript findings;
- relevant `jobs.jsonl` watch-send events.

Exit criteria:

- Both scenarios pass with live Kimi.
- Both parent and observer transcripts were read after the run.
- The audit notes state whether the model stumbled, and how.
- No pending watch-send residue remains after successful delivery.

## Definition Of Done

This remediation is complete only when all of the following are true:

- `go test ./agent/... -count=1` passes.
- Root workspace tests still pass for packages affected by UI/tool rendering:
  `go test ./... -count=1`.
- There are focused regression tests for:
  - caller watch-send provenance adoption;
  - caller watch-send same-watch suppression after adoption;
  - output-match same-watch suppression;
  - output-match same-watch suppression across split lines and terminal flush;
  - output-match cross-watch allowed behavior;
  - watch-send frames render trigger provenance, while persisted pending sends
    carry delivery provenance for suppression;
  - `watch_id` clear drops detached terminal-flush pending sends;
  - terminal auto-removal durably clears the watch registry entry;
  - assistant tool frame content;
  - assistant message frame content;
  - communicate frame content;
  - job notification frame content;
  - active provenance reset on later external input.
- The Monty Python Kimi scenario passes and transcripts are inspected.
- The snide observer Kimi scenario passes and transcripts are inspected.
- `docs/job-control.md` matches the final behavior.
- The old `FromWatch` global early-return guard is not used for watch
  suppression.
- No new model-facing primitives were added.

## Reviewer Checklist

Use this checklist before accepting the remediation:

- Can any allowed watch delivery reach a model turn without provenance?
- Can any watch-caused output append trigger the same watch?
- Can any coalesced delivery drop one of its causal watch keys?
- Can a restart turn a pending delivery into an unprovenanced delivery?
- Does a later human message clear active provenance and re-trigger normally?
- Do tests prove behavior through real rails, or do they manually seed internal
  state in a way production does not?
- Does every frame kind include enough bounded content for the observer's job?
- Are errors teaching the model the correct handle type?
- Did live transcripts show the model using `delegate_id`, `watch_id`, and
  `job_id` for their distinct purposes?

## Sprout Comparison Note

Sprout's observer implementation is not a direct blueprint for Serf, but it
highlights the right standard. Sprout makes observer identity and caller routing
runtime-owned: observer telemetry is filtered before observer delivery, observer
handles are private and owner-checked, and observer comments use a trusted
`caller` route instead of a model-provided raw handle.

Serf can keep its smaller public API, but then provenance carriage must be
treated as a runtime invariant. If any rail drops provenance, Serf has neither
Sprout's explicit observer telemetry filter nor complete causal suppression.

## Implementation Notes

- Prefer small, direct fixes over abstraction. Add helper functions only where
  multiple rails need the same clone/union behavior.
- Keep provenance cloning defensive at every queue/store boundary.
- Do not store pointers from mutable runtime state into durable records.
- Keep diagnostic frame output concise; correctness comes from carried
  provenance, not from long frame prose.
- If a test must inspect JSONL, assert structured event fields, not large
  rendered strings.
- If any contract point is ambiguous, stop and ask Jesse before implementing a
  compatibility compromise.
