# Hub Composer and Status Compaction Design

**Date:** 2026-07-10

## Goal

Reduce the Hub workspace composer and nearby status chrome so the transcript retains more vertical space, especially in non-maximized windows, while retaining the operational telemetry needed during active work.

The compact default must keep a concise running state, one authoritative task count, grouped project location (`branch`, `worktree`, and `cwd`), and a compact context-window/token figure. It must remove redundant or low-value default telemetry, including `src serf`, directional input/output token detail, cost, and turn count.

## Context

The current workspace bottom dock stacks several independently styled elements: conditional attachment and queue blocks, a task row, a bordered composer card, its controls, and a separate input-status strip. This makes the dock visually heavy and consumes vertical space even before a long draft expands.

The workspace also currently exposes overlapping activity and task signals. In particular, the task affordance can repeat task-count information, while running state can be represented in more than one nearby surface. This design treats the dock as one compact hierarchy rather than a collection of unrelated rows.

## Scope

### In scope

- Compact presentation of the normal Hub composer and adjacent status telemetry.
- Replacing the normal composer card border with a restrained raised/light surface treatment.
- A single-line, adaptive telemetry rail below the composer controls.
- Grouping `branch`, `worktree`, and `cwd` into one location item.
- Retaining a compact context-window/token figure on the default rail.
- Removing `src serf` from the default status rail.
- Removing cost, turn count, and directional input/output token figures from the default rail.
- Showing task count exactly once, in the task trigger.
- Eliminating duplicate running/working wording across composer-adjacent surfaces.
- Deterministic Hub JSDOM/CSS/template coverage.

### Out of scope

- AppWire, server, or session-status protocol changes.
- Changing the status-refresh event name, its event-driven behavior, or its active-only freshness tick.
- Removing detailed telemetry from the existing Details surface.
- Changing normal composer attachments, queueing, steering, stopping, model selection, capability gating, or drag/drop behavior.
- Changing the mobile safe-area, visual-viewport, or pending `ask_user` dock lifecycle contracts.
- A live-browser visual test requirement.

## Design

### Composer surface

The normal composer remains the same semantic form and control hierarchy. It retains its textarea, attachments, queue preview, task trigger, model selector, capability controls, file picker, and status-refresh wiring.

Presentation changes:

- Replace the conspicuous all-around composer-card border with a quiet raised/light background treatment and restrained radius.
- Retain a clear focus-visible treatment for editable and interactive controls; removing the container border must not remove keyboard focus affordance.
- Tighten vertical padding and gaps in the normal composer card and control row.
- Conditional attachment, attachment-error, queue-preview, and task elements consume no reserved block height while absent or hidden.
- Preserve textarea auto-grow behavior, including its existing baseline, upper bound, reset-after-successful-send behavior, and failure preservation behavior.
- Preserve the `data-composer-surface` boundary so ask-response mode can continue to hide and inert the normal composer safely.

### One-line telemetry rail

The separate composer-adjacent status presentation becomes one compact rail directly below the composer controls. It is a single visual line in normal desktop, constrained desktop, phone, and short-landscape layouts; it must not add wrapped metadata rows to the dock.

The default visual priority is:

1. **State/activity:** one concise, authoritative state indicator. While active, show one short working message or active-task summary, not duplicated working text in multiple neighboring surfaces.
2. **Location:** one grouped location item containing `branch`, `worktree`, and `cwd`, separated visually but exposed as one accessible unit.
3. **Context window:** a compact `used / window` value such as `42k / 128k`.

The rail does not show `src serf`, separate input/output token totals, cost, or turn count by default. The model remains available through its composer control. Full operational telemetry remains available through Details.

### Task count and active task

The task trigger remains available and retains its accessible status semantics.

- There is one authoritative numeric task count: the task trigger itself.
- The telemetry rail does not repeat that count, and the task trigger does not show the same count through both its label and a second badge/value.
- An active task may provide the concise activity text in the rail, but it must not restate the numeric task count.
- The task panel data flow and trigger behavior remain unchanged.

### Adaptive-width behavior

The rail uses horizontal priority and truncation instead of wrapping.

- **Wide workspace:** state/activity, complete grouped location, and `used / window` are visible.
- **Constrained desktop:** location text truncates first, preserving state/activity and `used / window`.
- **Phone and short landscape:** state/activity and `used / window` remain visible; the location item reduces to the most useful available identifier without wrapping. Existing phone safe-area, visual-viewport, and ask-response dock sizing remain authoritative.
- Full branch, worktree, CWD, and compacted telemetry values remain available through native titles and/or accessible labels when visual text is truncated.

### Accessibility and semantics

- Retain the textarea, button, file-picker, and task-trigger semantics already used by the workspace form.
- Preserve accessible names for icon-only composer controls.
- Preserve the task trigger's accessible status while ensuring its numeric count is not voiced redundantly.
- Truncated location and token content remains programmatically available through an accessible name or descriptive title.
- Status changes continue to use the established status-update mechanism; presentation compaction must not suppress meaningful state announcements or make focused controls inaccessible.

## Data flow and lifecycle

This is a presentation and rendering-boundary change. The existing status partial, status model, and refresh behavior remain authoritative.

- `serf-hub:status-refresh` continues to be triggered through `window.htmx.trigger(document.body, ...)` at its existing lifecycle points.
- The active-only ten-second freshness tick remains unchanged.
- Server-provided source, state, location, token, task, and cost data remain available to the template/Details surface even if they are no longer default-visible in the compact rail.
- Ask-response mode continues to replace the normal composer surface and must not create a competing normal composer/status interaction.

## Implementation boundaries

| File area | Responsibility |
| --- | --- |
| `cmd/serf-hub/templates/partials/workspace.html` and status partials | Compact semantic grouping and removal of duplicated visible task/status fields without changing status data flow. |
| `cmd/serf-hub/assets/style.css` | Raised borderless composer treatment, compact spacing, one-line rail, truncation and responsive priority rules. |
| `cmd/serf-hub/assets/renderer.js` | Only if needed to supply a concise active-task/state presentation while preserving existing refresh behavior and composer/ask lifecycle. |
| `cmd/serf-hub/jstest/test-input-area.js` | Composer DOM structure, attachment/queue/control behavior, and input-area contract coverage. |
| `cmd/serf-hub/jstest/test-status-refresh.js` and new focused status/layout tests if needed | Refresh behavior, status hierarchy, task-count de-duplication, and compact telemetry DOM/CSS contracts. |
| `cmd/serf-hub/web_test.go` | Template/render contracts if server-rendered markup changes. |

## Deterministic verification

### Composer and status presentation tests

Add or extend deterministic tests to assert:

- The normal composer retains textarea auto-grow, attachments, queue preview, capability controls, model control, and drag/drop hooks.
- The normal composer has a compact raised/light presentation and no longer relies on a full surrounding border as its primary separation treatment.
- The telemetry rail has a stable hook and does not wrap into a second metadata row under constrained-width CSS contracts.
- The visible default rail contains one state/activity item, one grouped location item, and one `used / window` context item.
- `src serf`, directional token totals, cost, and turn count are absent from the default rail but remain available through the existing detailed state surface where applicable.
- Branch, worktree, and CWD occur once in the rail's grouped location presentation and retain full-value accessibility/title behavior under truncation.
- Task count appears exactly once in the task trigger and is not repeated in the rail or a second task-count element.
- Active work renders a single concise activity message rather than repeated neighboring working text.

### Behavior-regression tests

Retain and run existing deterministic coverage proving:

- Status refresh uses `window.htmx.trigger(document.body, "serf-hub:status-refresh")` for relevant lifecycle signals.
- The active-only freshness tick starts and stops at the correct state transitions.
- Textarea auto-grow/reset and send failure behavior remain unchanged.
- Send/queue capability state remains correct through active/idle transitions.
- Existing ask-response dock lifecycle and visual-viewport tests remain green.

### Commands

Run at minimum:

```sh
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-input-area.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-status-refresh.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-mobile-css.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 180 sh cmd/serf-hub/jstest/run-all.sh
GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub -count=1
git diff --check
```

## Acceptance criteria

1. The workspace composer uses materially less vertical chrome without losing normal composer, attachment, queue, capability, model, or ask-dock behavior.
2. The composer surface has a quiet raised/light visual treatment rather than a prominent container border, while focus affordances remain clear.
3. One nonwrapping telemetry rail shows one concise state/activity item, grouped branch/worktree/CWD, and compact `used / window` context telemetry.
4. `src serf`, cost, turn count, and directional input/output token figures do not consume default rail space.
5. The task count appears exactly once, on the task trigger; active-task wording does not repeat that number.
6. Running status wording appears once in the composer-adjacent hierarchy.
7. On constrained desktops, phones, and short landscape, the rail remains one line and prioritizes state/activity plus context-window telemetry over location detail; it does not add vertical wrapping.
8. Existing status-refresh behavior, detailed telemetry availability, textarea behavior, capability behavior, and mobile/ask-response dock contracts remain unchanged.
9. Focused deterministic tests, the complete Hub JSDOM suite, Hub Go tests, and whitespace checks pass.
