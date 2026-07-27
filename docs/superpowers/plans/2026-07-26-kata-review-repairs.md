# Kata Review Repair Plan: Task Batch Atomicity and Reconnect Coverage

## Goal

Repair the four review findings on `fgqa` and `0rzj` with behavior-first tests,
preserving the task store's single-`in_progress` invariant and the existing
AppWire/frontend contracts.

## Root-cause findings

- `TaskStore.Update` validates a projected final state but applies duplicate
  IDs sequentially. The session tool currently classifies each request entry,
  so a duplicate `in_progress` then `done` batch can steer a task that ends
  completed and suppress auto-advance.
- `TaskStore.View` and `TaskStore.Update` take separate locks. A pre-state read
  followed by mutation is not an atomic classification boundary for a shared
  session store.
- The reconnect regression test calls `hydrateThread` directly twice and does
  not exercise the fake client, ready/reconnect lifecycle, notification route,
  or the rendered task badge.
- The `started` side-channel currently marks every explicitly updated ID,
  including final `open`, `done`, and `cancelled` statuses. The marker must
  describe only explicit updates whose relevant final status is
  `in_progress`.

## Constraints

- Keep `src/shell/rail/**` and every `Steering*` renderer/behavior untouched.
- Do not change the model-facing task schema or make status optional.
- Use a TaskStore API that owns the pre-state, validated mutation, and post-state
  under one lock; do not add an external lock around separate store calls.
- Preserve absent-vs-present-zero task aggregate semantics.
- Keep `fgqa`, `0rzj`, and the existing toolchain duplicate kata open; update
  only the two assigned kata comments after final verification.
- Generated files continue to be regenerated only with repository commands.

## Test-first implementation sequence

1. Add Go tests for duplicate-ID final-state classification, atomic pre/post
   mutation semantics, and marker scope. Run the focused tests red against the
   current implementation.
2. Add a frontend fake-client store-flow test that starts from a thread/read
   snapshot, routes a task notification, performs the ready/reconnect flow, and
   verifies both `ThreadModel.tasks` and the rendered Tasks `x/y` badge. Run it
   red before changing production code.
3. Add the smallest TaskStore mutation API returning pre/post snapshots from one
   locked validated mutation. Refactor the session tool to classify the final
   status per ID, emit steering only for a final explicit transition into
   `in_progress`, and include `started` only for explicit final
   `in_progress` updates.
4. Implement the fake-client reconnect test seam or use the existing store and
   ready/reconnect harness without broadening production behavior. Keep the
   reducer hydration implementation unchanged unless the integration test proves
   a real missing path.
5. Run focused green tests, then cumulative Go/frontend suites and the required
   typecheck, lint, production build, generation drift, runtime build, diff
   hygiene, and base-to-HEAD self-review checks.
6. Commit the repair in small, detailed commits and append substantive
   ready-for-controller-review comments to `fgqa` and `0rzj` without closing
   either issue.

## Explicit alternative not taken

Making `updates[].status` optional in the model-facing schema remains out of
scope and requires Jesse's approval; the repair instead derives classification
from the authoritative final batch state.
