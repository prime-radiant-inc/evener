# Inline Task Update Card Design

## Goal

Make each inline `task_list` mutation card report what that tool call changed. Keep the progress header, and remove aggregate status and full-plan controls from the inline card.

## Scope

This change applies to the conversation's inline `task_list` tool-use renderer. It does not change task state, task ordering, the task sidebar, or `task_list` tool behavior.

A `task_list` view remains silent. Each successful append or update call creates its own inline card at that call's position in the conversation.

## Card Content

The card retains its header:

- `Tasks`
- settled task count over total task count
- progress meter

Below the header, the card shows only changes from that tool call:

- tasks newly marked `done`
- tasks newly marked `cancelled`
- tasks explicitly marked `in_progress`
- the task automatically activated after a completion
- tasks created by an append call

The card omits unchanged tasks. A notes-only, dependency-only, effort-only, or prompt-only update shows the progress header without a task row.

The inline card contains no aggregate `N done` or `N up next` summary, neighboring context rows, `show all`, `more`, or other full-plan disclosure. The sidebar remains the full-plan view.

## Data Flow

The renderer continues to suppress the generic tool-call row for `task_list` calls and caches each call's arguments until completion.

When the call completes:

1. Read the authoritative task snapshot returned by the tool.
2. Compute the header's settled count, total count, and progress from that snapshot.
3. Use update arguments to identify explicit status changes.
4. When an update completes a task, include the snapshot's `in_progress` task as the automatically activated change.
5. Use append arguments and the pre-call ID set to identify newly created tasks.
6. Render changed rows with descriptions and final statuses from the authoritative snapshot.

For older transcripts without an authoritative snapshot, use the existing task-description cache and mutation arguments. This degraded path renders only changes it can establish; it does not invent transitions.

## Rendering Boundaries

The implementation will use the existing per-call task update renderer rather than the persistent living-plan renderer. It will simplify that renderer to changed rows only and remove its hidden-row disclosure behavior.

The persistent living-plan path will no longer handle inline `task_list` mutations. Any code and styles made unreachable by that routing change may be removed only when repository searches and tests show they have no other consumer.

## Error Handling

Failed or malformed calls must not invent a successful update card. Empty or unusable task state follows the existing silent behavior unless mutation arguments provide enough information for the degraded path.

## Testing

Renderer tests will cover:

1. Completing a task shows that task and the task automatically activated next.
2. Explicitly activating a task shows the activated task.
3. A non-status update shows only the progress header.
4. An append shows the newly added tasks.
5. Inline cards contain no aggregate done/up-next summary or full-plan disclosure.
6. Consecutive mutations create separate cards whose rows describe their respective calls.
7. Replay without authoritative state renders only changes supported by mutation arguments and cached task details.

Tests will assert the DOM contract rather than visual pixel output. The focused JavaScript renderer suite and the repository's broader prescribed checks must pass.
