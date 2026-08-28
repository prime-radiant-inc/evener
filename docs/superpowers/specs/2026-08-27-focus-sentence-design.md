# Focus Sentence Design

**Status:** Approved from the visual spike on 2026-08-27

## Goal

Show the session’s current task and optional goal immediately above the compose card without obscuring compose controls or adding a second card.

## Data contract

The existing thread task aggregate gains an optional `current` summary containing the first `in_progress` task’s ID and description. `task.Summarize` is the shared definition for live and persisted producers: it reports total and done counts and selects the first in-progress task in list order. Every typed task update carries the complete aggregate, including an explicit absence when no task is in progress, so the frontend updates without polling or opening the Tasks panel.

Current work is initialized before incremental updates begin. The root envelope first receives a pre-bridge seed: tasks come from the live task source and goal objective, status, and iterations come from structured `SessionMeta.Goal`. `SessionStartData.CurrentWork` then carries a self-contained task/goal seed and patches the root or descendant cache inside the first `CommitProjection`. Root templates populate before that start event. A descendant starts before its templates populate, so successful post-start population emits an ordered corrective task update. Every start and task update also identifies the task-store owner internally; one committed task mutation fans out to the root and every cached descendant sharing that owner. Persisted hub reads derive tasks from the persisted task file and goals from `SessionMeta.Goal`, using the same summary and goal shapes as live reads.

The optional outer start seed preserves compatibility and gives goal initialization an explicit tri-state. An absent `CurrentWork` means an old or unknown producer and leaves cached state unchanged. A present seed with `goal: null` authoritatively clears the goal; a present goal object replaces it. Within a present seed, absent tasks mean task state is unavailable, while a present zero aggregate is authoritative empty state.

Structured goal updates report every set, clear, iteration, and terminal transition. AppWire projects them as `evener/goal/updated`, with `goal: null` explicitly clearing live state. Typed task and goal carriers are the state patches: the server applies them to the root/descendant cache inside the same `CommitProjection` critical section that records and routes their notifications, without re-pulling either store. Session-start, session-end, and turn-end checkpoints retain sampling for initialization and old-producer recovery.

All wire fields are optional and additive. An older producer omits the current-task summary and goal objective, and the frontend omits unavailable text rather than inventing data. Older clients ignore the new notification while existing fields continue to decode.

## UI

A focused `CurrentWork` component renders inside `Composer`, after queue and attachment surfaces and immediately before the compose form. It is hidden while AskDock hides the compose form.

At widths above the composer’s existing 559px container boundary, the component renders one line:

`● WORKING ON <task> | ⚑ GOAL <objective>`

The task uses stronger text than the goal. The green ring marks a real in-progress task; the goal flag stays neutral. If the goal is absent, its divider, flag, label, and text do not render. If the task is absent but an objective exists, the component renders the goal by itself. If both are absent, the component renders nothing.

At 559px and below, the task remains the first row and the optional goal moves to a second indented row. Each value stays on one line and ellipsizes independently. The full value remains in the DOM and in a native title tooltip. The component uses the composer’s container width, not viewport width, so a narrow desktop dock behaves like mobile.

`ThreadModel` is the sole state source for both `CurrentWork` and `GoalControl`. A successful local `goal/set` response may synthesize an immediate goal only as a fallback; an accepted goal notification or authoritative hydration invalidates that fallback and wins. No component keeps a parallel goal cache.

The inline task action means show and focus Tasks, so repeated activation is idempotent; the Tasks item in `SessionMenu` retains toggle-open/toggle-closed behavior. Editing a goal replaces composer state through one canonical operation that handles ordinary and recovery drafts, settled and pending attachments, slash completion, stale submission state, draft persistence, and deferred focus before writing `/goal <objective>`.

The component is a polite atomic status region. Decorative glyphs are hidden from assistive technology; visible labels and a composed accessible name carry meaning without relying on color.

## Verification

Tests must prove:

- live and persisted snapshots carry the first in-progress task and goal objective;
- task notifications replace or clear the current task without polling;
- goal notifications set, advance, terminate, and clear the goal for every client;
- generated TypeScript matches the Go wire types;
- a successful local `goal/set` response supplies only a fallback, and an accepted notification or hydration remains authoritative;
- the component handles task-plus-goal, task-only, goal-only, and empty states;
- `Composer` places the component directly before the compose form and hides it with AskDock;
- the inline Tasks action is idempotent while the menu action still toggles, and goal editing uses canonical draft replacement;
- long task and goal text remains contained at desktop and narrow widths;
- existing frontend, browser overflow, Go, generation, lint, and vet gates stay green.
