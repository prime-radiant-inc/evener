# Focus Sentence Design

**Status:** Approved from the visual spike on 2026-08-27

## Goal

Show the session’s current task and optional goal immediately above the compose card without obscuring compose controls or adding a second card.

## Data contract

The existing thread task aggregate gains an optional `current` summary containing the first `in_progress` task’s ID and description. This follows `TaskStore.CurrentInProgress`, which already defines “current” as the first in-progress task. Every `TASK_UPDATED` event carries the same summary, including an explicit absence when no task is in progress, so the frontend updates without polling or opening the Tasks panel. Live and persisted thread snapshots derive the same summary from the task file.

The existing goal state gains an optional `objective` string. Live snapshots read the objective, status, and iteration count from one goal-store snapshot. Persisted thread snapshots recover the same state from `SessionMeta.Goal`.

A new structured `GOAL_UPDATED` event reports every set, clear, iteration, and terminal transition. AppWire projects it as `evener/goal/updated`, with `goal: null` explicitly clearing the frontend model. The thread-envelope facet refresh runs before projection, so a thread read cannot lag a committed goal notification. The successful local `goal/set` action also updates its tracked `ThreadModel` immediately; the notification keeps other clients and engine-driven transitions current.

All wire fields are optional and additive. An older producer omits the current-task summary and goal objective, and the frontend omits unavailable text rather than inventing data. Older clients ignore the new notification while existing fields continue to decode.

## UI

A focused `CurrentWork` component renders inside `Composer`, after queue and attachment surfaces and immediately before the compose form. It is hidden while AskDock hides the compose form.

At widths above the composer’s existing 559px container boundary, the component renders one line:

`● WORKING ON <task> | ⚑ GOAL <objective>`

The task uses stronger text than the goal. The green ring marks a real in-progress task; the goal flag stays neutral. If the goal is absent, its divider, flag, label, and text do not render. If the task is absent but an objective exists, the component renders the goal by itself. If both are absent, the component renders nothing.

At 559px and below, the task remains the first row and the optional goal moves to a second indented row. Each value stays on one line and ellipsizes independently. The full value remains in the DOM and in a native title tooltip. The component uses the composer’s container width, not viewport width, so a narrow desktop dock behaves like mobile.

`ThreadModel` is the component’s sole state source. GoalControl uses the same model and no longer keeps a parallel module-level override cache.

The component is a polite atomic status region. Decorative glyphs are hidden from assistive technology; visible labels and a composed accessible name carry meaning without relying on color.

## Verification

Tests must prove:

- live and persisted snapshots carry the first in-progress task and goal objective;
- task notifications replace or clear the current task without polling;
- goal notifications set, advance, terminate, and clear the goal for every client;
- generated TypeScript matches the Go wire types;
- a successful local `goal/set` updates the tracked model without waiting for its push;
- the component handles task-plus-goal, task-only, goal-only, and empty states;
- `Composer` places the component directly before the compose form and hides it with AskDock;
- long task and goal text remains contained at desktop and narrow widths;
- existing frontend, browser overflow, Go, generation, lint, and vet gates stay green.
