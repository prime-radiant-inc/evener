# Task Status “Up Next” Design

## Goal

Make the collapsed live task card identify the current task as “up next” instead of describing the whole open queue as “N up next.”

## Current Behavior

`renderLivePlan` partitions tasks into in-progress, open, done, and cancelled groups. It renders the in-progress task as the card’s visible frontier, then adds every open task to a collapsed summary such as “4 up next.” This count makes the queue, rather than the current task, the subject of the status.

## Design

When an in-progress task exists, render a quiet “up next” label immediately before its existing task row. Keep using the shared task-row renderer for the task description and status styling.

Remove the open-task count from the collapsed summary when an in-progress task exists. Preserve done and cancelled counts in that summary.

Keep the expanded body unchanged. Open tasks remain ordered by task ID in the existing “Up next · N” group behind the “show all” control. The change affects only the collapsed frontier; it does not alter task state, ordering, or sidebar rendering.

## Edge Cases

- A plan with no in-progress task does not receive an invented “up next” label.
- A completed plan retains its “all N done” message.
- Legacy or degraded states that contain open tasks but no in-progress task retain an open-task summary so those tasks remain discoverable.
- Done and cancelled counts retain their current wording and behavior.

## Implementation Boundary

Change `renderLivePlan` in `cmd/serf-hub/assets/renderer.js` and its deterministic jsdom coverage in `cmd/serf-hub/jstest/test-renderer-plan.js`.

Reuse existing task-card typography where practical. Add CSS only if no existing class can present the label correctly.

## Testing

Use test-driven development:

1. Change or add a jsdom assertion that fails under the current renderer.
2. Assert that an in-progress task has an “up next” label and its description remains visible through the shared task row.
3. Assert that the collapsed card does not report the open queue as “N up next.”
4. Retain assertions for the expanded “Up next · N” queue, done and cancelled counts, and completed plans.
5. Run the focused renderer-plan test, then the complete hub JavaScript test suite.
