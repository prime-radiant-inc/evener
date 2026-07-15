# Web UI Subagent Sidebar Mockup Design

**Date:** 2026-07-14
**Status:** Approved for implementation planning

**Scope:** Standalone interactive mockup for the Serf web hub

## Problem

The web UI sidebar no longer reliably exposes a session's subagents. Users need to see active work under its parent session, reveal inactive children on demand, and open child sessions beside their parents without creating duplicate panes.

The production client already contains pieces of this behavior: recursive `children` nodes, child disclosures, session status icons, and an `Open beside` action. Recent work on another branch also fixes running subagent rows and in-process status retention. This phase will not merge or rewrite that production behavior. It will establish and validate the intended interaction through a focused mockup.

## Goal

Create a standalone interactive mockup that demonstrates:

- current subagents directly beneath their parent session;
- terminal subagents within a collapsed disclosure owned by that parent;
- recursive parent-child nesting;
- parent-relative pane placement;
- stable pane identity when users reopen a child; and
- deterministic lifecycle transitions between current and inactive groups.

## Non-goals

This phase will not:

- connect the mockup to `/api/tree` or other production endpoints;
- modify production sidebar, pane, roster, or session-tree code;
- resolve the known running-subagent regression;
- merge commits from another branch;
- define a new server payload; or
- replace the inline transcript subagent module.

## Chosen Approach

Add one focused prototype at:

`docs/web-ui/mockups/23-subagent-sidebar.html`

The document will import `docs/web-ui/mockups/tokens.css`, use fixture data that covers every required status and scenario below, and contain its own small interaction controller. It will resemble the production sidebar and pane workspace without depending on production JavaScript or APIs.

Standalone means the HTML works when opened directly and depends only on existing static assets under `docs/web-ui/mockups`. It requires no build, server, production JavaScript, or API.

This approach provides enough fidelity to test hierarchy and interactions while keeping design work separate from production behavior.

## Information Architecture

### Current subagents

A parent session renders each non-terminal direct child immediately beneath its row. Current children do not sit behind a group disclosure.

The only current statuses are:

- `running`, displayed as `Running`;
- `awaiting`, displayed as `Awaiting input`;
- `retained-idle`, displayed as `Idle · resumable`; and
- `unknown`, displayed as `Unknown`.

Retained-idle rows use the label `Idle · resumable`. Unknown rows use a neutral `Unknown` treatment. Neither status moves into inactive history merely because it is not working at that moment.

### Inactive subagents

A parent with terminal direct children renders `Inactive subagents (N)` after its current children. `N` counts only that parent's terminal direct children. The disclosure starts collapsed.

The only terminal statuses are:

- completed;
- failed;
- cancelled; and
- stopped.

Terminal rows retain distinct icons and status text within the disclosure. A failure remains recognizable as a failure rather than becoming a generic inactive row.

A parent with no terminal children renders no inactive disclosure. A parent with only terminal children renders the disclosure without an empty current group.

### Recursive nesting

The sidebar partitions only direct children at each parent. A current direct child and its recursively rendered subtree appear directly beneath that parent. A terminal direct child's row and recursively rendered subtree appear inside that parent's inactive disclosure. Within either subtree, every descendant remains nested under its direct parent, and that parent independently partitions its direct children. Expanding a disclosure never hoists a sibling or descendant into the wrong parent's group.

## Components

The mockup contains four bounded units.

### Fixture session store

A normalized map holds one record per session:

- `id`;
- `parentId`;
- `title`; and
- `status`.

A separate value identifies the main session. Derived selectors compute direct children and partition them into current and inactive lists. The store does not duplicate records in display groups.

### Sidebar tree

The sidebar renders the main session and its recursive descendants. It owns disclosure state keyed by parent ID and the selected-row state keyed by session ID.

Rows include:

- status icon and text;
- title;
- nesting treatment; and
- selected or focus-visible treatment.

### Pane workspace

The workspace renders a horizontal sequence of thread panes. The main session remains leftmost and cannot close. Child panes show a title, status, fixed mock transcript text labelled as fixture content, and a close control. Transcript behavior is not under review.

### Interaction controller

A small local controller handles:

- disclosure toggles;
- row activation;
- pane insertion and removal;
- focus restoration;
- selected-row synchronization; and
- preset lifecycle scenarios.

The controller remains independent of production sidebar and pane modules.

## Interaction Model

### Open a child

Activating a child that does not already have a pane recursively opens any missing ancestor panes, then inserts the child pane immediately to the right of its direct parent pane and shifts later panes right.

### Focus an existing child

If the child already has a pane, activation does not create another pane. The controller:

1. selects the existing pane;
2. selects its sidebar row;
3. scrolls the pane into view; and
4. focuses the pane root, which has `tabindex="-1"`, an accessible name derived from the session title, and a visible focus indicator.

A session ID identifies one pane.

### Close a pane

Closing a child pane also closes its open descendant panes because those panes would otherwise lose their visible parent context. Sibling and ancestor panes remain open.

After the close, focus returns to the closed pane's originating sidebar row. If that row is hidden inside a collapsed inactive disclosure, the controller expands the owning disclosure before restoring focus.

### Change lifecycle state

Preset scenario buttons change fixture statuses synchronously. The mockup uses no timers.

A state change re-partitions the affected parent's direct children. A child that becomes terminal moves under that parent's inactive disclosure without changing its session identity. A `completed`, `failed`, `cancelled`, or `stopped` child that changes to `retained-idle` returns to the current rows.

Disclosure state is independent per parent. A disclosure created when its count changes from zero to nonzero starts collapsed. An existing disclosure retains its own expanded or collapsed state while its nonzero count changes. Removing the disclosure at count zero discards that state.

Open panes remain open across status changes. The sidebar location and status presentation update without duplicating the pane.

## Empty and Degraded States

- A session with no children renders no subagent chrome.
- An empty inactive disclosure disappears.
- A session with only inactive children renders one collapsed inactive disclosure.
- Failed, cancelled, and stopped rows remain distinguishable.
- Retained-idle rows remain current and state that they are resumable.
- Unknown status remains current and uses neutral text and color.

## Accessibility

The mockup will use semantic controls and explicit state:

- disclosure controls are buttons with `aria-expanded` and an associated region;
- session rows support keyboard activation;
- status meaning appears in text as well as color;
- focus indicators remain visible;
- the active sidebar row exposes `aria-current="true"`;
- each pane root is labelled by its visible title;
- pane close controls have specific accessible names; and
- closing a pane restores focus to its sidebar row.

## Visual Treatment

The prototype follows the existing web UI token system and the selected hierarchy direction:

- current child rows appear directly under the parent;
- a subtle branch line or indentation communicates lineage;
- the inactive disclosure is visually quieter than current rows;
- `running`, `awaiting`, `retained-idle`, `unknown`, `completed`, `failed`, `cancelled`, and `stopped` use the exact labels defined above; and
- the main session remains visually dominant over its children.

The mockup should look production-shaped, not like a new design system.

## Deterministic Scenarios

The mockup will include controls sufficient to demonstrate exactly these required scenarios; additional scenarios are out of scope:

1. one parent with `running`, `awaiting`, `retained-idle`, and `unknown` direct children plus `completed`, `failed`, `cancelled`, and `stopped` direct children;
2. nested subagents, including a terminal child with its own current and terminal children and independent inactive disclosures;
3. transition from `running` to `completed`;
4. transition from `completed` to `retained-idle`;
5. a child already open in a pane; and
6. a parent pane with open descendants that closes as a subtree.

## Verification

The mockup will include a small self-check panel and a manual browser checklist.

The checks will verify:

- current rows render without a wrapper disclosure;
- inactive disclosures start collapsed;
- each parent owns only its terminal direct children;
- recursive nesting preserves parentage;
- a child opens immediately to the right of its direct parent;
- reopening a child creates no duplicate pane;
- closing a pane closes its descendants;
- status changes move rows without changing session identity;
- the current set equals exactly `{running, awaiting, retained-idle, unknown}`;
- the terminal set equals exactly `{completed, failed, cancelled, stopped}`;
- inactive counts include terminal direct children only;
- a new inactive disclosure starts collapsed, existing nonempty disclosure state persists, and removal at zero clears that state;
- empty disclosures disappear;
- keyboard disclosure and row activation work;
- focus returns to the originating row after close; and
- status remains understandable without color.

Implementation verification will also:

- load the file with zero console errors and run self-check assertions for required controls, ARIA relationships, and unique session and pane IDs;
- inspect the page at 1280×800 and 390×844 CSS pixels;
- confirm the self-check panel passes; and
- confirm that only the mockup and its approved design or plan documents changed.

## Acceptance Criteria

The mockup is complete when:

1. `docs/web-ui/mockups/23-subagent-sidebar.html` works when opened directly, without a server, API, or production JavaScript.
2. For every parent, direct children in exactly `running`, `awaiting`, `retained-idle`, and `unknown` render as unwrapped current rows; direct children in exactly `completed`, `failed`, `cancelled`, and `stopped` render only inside that parent's initially collapsed inactive disclosure.
3. `Inactive subagents (N)` counts terminal direct children only, is absent at zero, and expands independently per parent.
4. Recursive fixtures preserve every direct-parent relationship, including descendants of a terminal child; no descendant is flattened or hoisted.
5. Activating an unopened child inserts its pane immediately to the right of its direct parent pane and opens missing ancestors first.
6. Pointer or keyboard activation of an already-open child leaves one pane for that session ID, scrolls it into view, and focuses its pane root.
7. Closing a pane closes its open descendants and restores focus to the originating sidebar row.
8. The `running` to `completed` and `completed` to `retained-idle` scenarios move only the affected row, preserve recursive parentage and pane identity, and apply the disclosure-state rules.
9. Status appears in text as well as color, disclosure buttons expose `aria-expanded`, keyboard operation works, and focus indicators remain visible.
10. All deterministic self-checks pass, and the implementation changes no production code or assets.

## Follow-up Production Work

Production integration and regression analysis form a separate post-approval phase. They are not part of this mockup's implementation or acceptance.
