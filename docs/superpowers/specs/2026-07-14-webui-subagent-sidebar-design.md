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

The document will import `docs/web-ui/mockups/tokens.css`, use realistic fixture data, and contain its own small interaction controller. It will resemble the production sidebar and pane workspace without depending on production JavaScript or APIs.

This approach provides enough fidelity to test hierarchy and interactions while keeping design work separate from production behavior.

## Information Architecture

### Current subagents

A parent session renders each non-terminal direct child immediately beneath its row. Current children do not sit behind a group disclosure.

These statuses remain current:

- running or active;
- awaiting input;
- retained-idle and resumable; and
- unknown.

Retained-idle rows use the label `Idle · resumable`. Unknown rows use a neutral `Unknown` treatment. Neither status moves into inactive history merely because it is not working at that moment.

### Inactive subagents

A parent with terminal direct children renders `Inactive subagents (N)` after its current children. The disclosure starts collapsed.

These statuses are terminal:

- completed;
- failed;
- cancelled; and
- stopped.

Terminal rows retain distinct icons and status text within the disclosure. A failure remains recognizable as a failure rather than becoming a generic inactive row.

A parent with no terminal children renders no inactive disclosure. A parent with only terminal children renders the disclosure without an empty current group.

### Recursive nesting

The sidebar preserves direct parentage. A subagent's children appear beneath that subagent, using the same current and inactive rules. The mockup does not flatten descendants under the main session.

Each inactive disclosure belongs to one parent. Expanding a parent's history never reveals terminal siblings or descendants owned by another parent.

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

The workspace renders a horizontal sequence of thread panes. The main session remains leftmost and cannot close. Child panes show a title, status, representative transcript content, and a close control.

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

Clicking or keyboard-activating a child row opens that session beside its direct parent pane.

Before opening the child, the controller walks its ancestry and ensures that each required parent pane is open. The new pane appears immediately after the direct parent's currently open descendant block. This keeps each subtree contiguous and preserves ancestry from left to right.

### Focus an existing child

If the child already has a pane, activation does not create another pane. The controller:

1. selects the existing pane;
2. selects its sidebar row;
3. scrolls the pane into view; and
4. moves focus into that pane.

A session ID identifies one pane.

### Close a pane

Closing a child pane also closes its open descendant panes because those panes would otherwise lose their visible parent context. Sibling and ancestor panes remain open.

After the close, focus returns to the closed pane's originating sidebar row. If that row is hidden inside a collapsed inactive disclosure, the controller expands the owning disclosure before restoring focus.

### Change lifecycle state

Preset scenario buttons change fixture statuses synchronously. The mockup uses no timers.

A state change re-partitions the affected parent's direct children. A child that becomes terminal moves under that parent's inactive disclosure without changing its session identity. A terminal child that becomes resumable returns to the visible current rows.

Open panes remain open across status changes. The sidebar location and status presentation update without duplicating the pane.

## Empty and Degraded States

- A session with no children renders no subagent chrome.
- An empty inactive disclosure disappears.
- A session with only inactive children renders one collapsed inactive disclosure.
- Failed, cancelled, and stopped rows remain distinguishable.
- Retained-idle rows remain current and state that they are resumable.
- Unknown status remains current and uses neutral text and color.
- Fixture inconsistencies, such as a missing parent, render a visible mockup diagnostic instead of silently dropping the row.

## Accessibility

The mockup will use semantic controls and explicit state:

- disclosure controls are buttons with `aria-expanded` and an associated region;
- session rows support keyboard activation;
- status meaning appears in text as well as color;
- focus indicators remain visible;
- selected rows and panes expose their selected state;
- pane close controls have specific accessible names; and
- closing a pane restores focus to its sidebar row.

## Visual Treatment

The prototype follows the existing web UI token system and the selected hierarchy direction:

- current child rows appear directly under the parent;
- a subtle branch line or indentation communicates lineage;
- the inactive disclosure is visually quieter than current rows;
- active, awaiting, failed, cancelled, stopped, idle, and unknown states use the existing status vocabulary where possible; and
- the main session remains visually dominant over its children.

The mockup should look production-shaped, not like a new design system.

## Deterministic Scenarios

The mockup will include controls for at least these fixtures:

1. mixed current and inactive direct children;
2. nested subagents with separate inactive disclosures;
3. a retained-idle child that remains current;
4. failed, cancelled, and stopped inactive rows;
5. transition from running to completed;
6. transition from terminal to resumable;
7. a child already open in a pane; and
8. a parent pane with open descendants that closes as a subtree.

## Verification

The mockup will include a small self-check panel and a manual browser checklist.

The checks will verify:

- current rows render without a wrapper disclosure;
- inactive disclosures start collapsed;
- each parent owns only its terminal direct children;
- recursive nesting preserves parentage;
- a child opens beside its direct parent;
- reopening a child creates no duplicate pane;
- closing a pane closes its descendants;
- status changes move rows without changing session identity;
- empty disclosures disappear;
- keyboard disclosure and row activation work;
- focus returns to the originating row after close; and
- status remains understandable without color.

Implementation verification will also:

- validate the HTML structure with an available validator or parser;
- inspect the page in a browser at desktop and narrow widths;
- confirm the self-check panel passes; and
- confirm that only the mockup and its approved design or plan documents changed.

## Acceptance Criteria

The mockup is complete when a reviewer can:

1. see current subagents directly below each parent;
2. expand each parent's inactive subagents independently;
3. follow nested lineage in the sidebar;
4. open a child beside its direct parent;
5. click an already-open child and focus its existing pane;
6. close a parent pane and observe descendant panes close;
7. run lifecycle scenarios and see rows move between current and inactive groups; and
8. complete the accessibility and deterministic self-checks without production data.

## Follow-up Production Work

After the mockup is approved, implementation planning should compare the prototype with the production paths in:

- `cmd/serf-hub/assets/sidebar.js`;
- `cmd/serf-hub/assets/panes.js`;
- `cmd/serf-hub/assets/style.css`;
- `cmd/serf-hub/web_api_tree.go`;
- `cmd/serf-hub/internal/hubcore/tree.go`; and
- existing sidebar child and pane tests.

The plan should also assess the unmerged commits that retain running in-process subagent status and display running subagents on their own rows. Those changes may solve part of the regression, but they do not replace review against this approved interaction model.
