# Task 1 Report: Inline task update cards

## Status

DONE

## Files changed

- `cmd/serf-hub/assets/renderer.js`
  - Failed `task_list` calls are deleted from pending state and render no card.
  - Successful mutations route to a new per-call card.
  - The persistent living-plan card and fold implementation were removed.
  - Cards use authoritative state when available, detect only additions/status transitions, add completion auto-activation only from authoritative state, render the progress meter, and include only touched rows.
- `cmd/serf-hub/jstest/test-renderer-plan.js`
  - Replaced living-plan scenarios with the seven required per-call scenarios and direct DOM assertions.
- `cmd/serf-hub/jstest/test-realistic-flow.js`
  - Updated only the superseded living-card assertions: 4 per-call cards and 12 top-level conversation children.

No other source or test files were changed. The pre-existing `.superpowers/sdd/progress.md` was not modified or used as requirements.

## Commits

- `d27f49c28` — `fix(hub): show inline task changes`

## Tests

TDD red baseline:

```text
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
```

Result: failed as expected against the old living-card implementation because aggregate/disclosure UI remained and update-only cards were not independent.

Final focused and related commands:

```text
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-task-updated-subscription.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-realistic-flow.js
```

Result: all passed, exit 0.

- Focused renderer test: CSS contract plus all 7 per-call scenarios passed.
- Subscription test: `PASS test-task-updated-subscription.js`.
- Realistic flow: `PASS realistic-flow — user message, assistant text (×2), tool cluster, task card, steering suppression`.
- `git diff --check`: passed before commit.
- Final `git status --short` after commit: clean before this report was written.

## Self-review

- Confirmed `livePlanCard`, `renderLivePlan`, and `taskFoldGroup` plus living-plan comments are absent from `renderer.js`.
- Confirmed failed calls are silent and pending entries are removed.
- Confirmed view calls return without rendering or refreshing.
- Confirmed non-status updates with authoritative tasks render a header with zero rows.
- Confirmed completion auto-activation is inferred only from authoritative state.
- Confirmed degraded replay does not invent auto-activation.
- Confirmed cards are appended independently and contain no summary, toggle, show-all, hidden-row, neighboring-row, or completion-prose UI.
- Confirmed the staged commit contained only the three intended Task 1 behavior/test files.

## Concerns

The commit command printed an `Operation not permitted` warning while attempting to create the repository-level `packed-refs.lock`, but it exited 0 and produced commit `d27f49c28`; `git log`, `git show`, and `git status --short` confirmed the commit and clean tracked worktree. This is an environment warning only, not a test or implementation failure.

## Review fix: malformed successful mutations

### Status

DONE

### Changes

- `cmd/serf-hub/jstest/test-renderer-plan.js`
  - Added red regression coverage for invalid JSON, an unknown action, malformed append payload, and malformed update payload on successful task calls.
  - Strengthened the append scenario with a seeded known task plus new tasks, asserting only six newly added rows and directly asserting the `Tasks` title.
- `cmd/serf-hub/assets/renderer.js`
  - Validates that only `append` with an array `tasks` or `update` with an array `updates` is eligible to render or refresh.
  - Keeps empty append silent, including when cached tasks exist.
  - Safely handles a JSON `null` argument payload.
  - Refreshes the task badge only when a valid mutation renders a card.

### Review-fix tests

TDD regression command (failed before the implementation fix):

```text
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
```

Result: failed at `malformed successful task mutations render and refresh no card`; the old path rendered four false cards.

Final review-fix commands:

```text
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-task-updated-subscription.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-realistic-flow.js
```

Result: all passed, exit 0.

- Focused test: CSS contract, mixed known/new append filtering, malformed mutation silence, and all valid per-call scenarios passed.
- Subscription test: `PASS test-task-updated-subscription.js`.
- Realistic flow: `PASS realistic-flow — user message, assistant text (×2), tool cluster, task card, steering suppression`.
- `git diff --check`: passed before the review-fix commit.

### Review-fix self-review

- Invalid JSON becomes an empty argument object and cannot pass mutation validation.
- Unknown actions cannot render or refresh.
- Append/update payloads must have the required array shape.
- Empty append returns before cache-based fallback can create a card.
- Valid non-status updates still render their authoritative progress header with zero rows.
- Failed calls still clear pending state and remain silent.
- The append test proves known IDs are excluded while new IDs render, and verifies the exact `Tasks` title.

### Review-fix concerns

None. The existing packed-refs warning was not reproduced by the tests; it remains an environment concern from the prior commit operation.

## Re-review fix: nested mutation entry validation

### Status

DONE

### Changes

- `cmd/serf-hub/jstest/test-renderer-plan.js`
  - Added explicit malformed nested append/update cases for null, primitive, missing-ID, invalid-ID, and invalid-status entries.
  - Instrumented task fetches and asserted malformed successful mutations produce neither cards nor badge refreshes.
  - Added a separate fresh append-of-`PLAN` assertion for seven rows while retaining the mixed known/new append assertion.
- `cmd/serf-hub/assets/renderer.js`
  - Defines structural mutation validation: each append/update entry must be a non-null object with a positive integer-coercible ID; update statuses, when present, must be `open`, `in_progress`, `done`, or `cancelled`.
  - Rejects the entire mutation before cache seeding, rendering, or badge refresh if any entry is malformed.
  - Preserves metadata-only valid updates as header-only cards.

### Re-review TDD evidence

Red command, run before the structural validation change:

```text
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
```

Result: failed at `malformed nested task mutations render and refresh no card`; malformed nested entries produced cards.

Green focused command:

```text
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
```

Result: all scenarios passed, including malformed nested silence, refresh suppression, fresh seven-row append, mixed known/new filtering, and metadata-only update behavior.

Related commands:

```text
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-task-updated-subscription.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-realistic-flow.js
cd ../..
git diff --check
```

Result: all tests passed with exit 0; `git diff --check` passed.

### Re-review self-review

- Validation is applied at TOOL_CALL_START before append cache seeding and again at the render boundary before any badge refresh.
- One malformed nested element rejects the entire mutation.
- IDs reject null, empty, booleans, non-numeric values, non-integers, and non-positive values.
- Status is constrained only when present, so notes/dependency/effort/prompt-only updates remain valid header-only mutations.
- Empty append remains silent.
- Existing failed-call suppression and view/unknown-action suppression remain intact.

### Re-review concerns

The commit operation may emit the pre-existing environment `packed-refs.lock` permission warning; tests and diff checks are unaffected.

## Re-review fix: accept server-assigned append IDs

### Status

DONE

### Changes

- Confirmed against `agent/session_tools_task.go:80-129` that append inputs are task objects without IDs; the server assigns IDs and returns them in authoritative state.
- Updated focused append scenarios to send legitimate ID-less fields (`description`, `prompt`, `type`, `depends_on`, and `reasoning_effort`) while `TOOL_CALL_END` state carries IDs.
- Kept the separate fresh seven-row append assertion and the mixed known/new append filtering assertion.
- Changed append validation to reject only empty/non-object entries; update validation still requires usable IDs and allows only valid statuses when status is present.

### TDD evidence

Red command, run with corrected ID-less append tests against the prior ID-required append validator:

```text
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
```

Result: failed because legitimate append calls rendered zero cards; the fresh append, mixed append, and dependent valid-card scenarios failed, while malformed silence remained passing.

Green commands after correcting append validation:

```text
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-renderer-plan.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-task-updated-subscription.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-realistic-flow.js
cd ../..
git diff --check
```

Result: all renderer tests passed with exit 0 and `git diff --check` passed.

### Self-review

- Append validation no longer invents or requires an ID.
- Valid append input fields are accepted and authoritative state supplies minted IDs/descriptions for rendering and `priorIds` filtering.
- Null, primitive, and empty append entries remain rejected without card or refresh.
- Update entries retain structural ID/status validation and metadata-only updates remain header-only.
- Fresh seven-row and mixed known/new append assertions both pass.

### Concerns

The commit operation may again report the pre-existing environment `packed-refs.lock` permission warning; no test or diff-check failure occurred.
