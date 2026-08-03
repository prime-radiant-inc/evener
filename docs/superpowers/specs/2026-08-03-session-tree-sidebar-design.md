# Current-Session Activity Tree Design

**Date:** 2026-08-03  
**Status:** approved design, pending implementation plan  
**Scope:** replace the Web UI's flat Jobs sheet and jobs-list wire contract with a recursive activity tree for the current session, its subagents, and every retained shell job they own.

## Problem

The current `JobsPanel` lists one session's shell and delegate jobs as unrelated rows. It distinguishes job types, but it does not show which subagent owns a job, which delegate spawned a child session, or how work nests through multiple delegation levels. A user can inspect one job at a time, but cannot answer basic questions quickly:

- Which agent is doing this work?
- What shell commands has each agent run?
- Which work is active, complete, or failed?
- How did this nested agent originate?

The redesign must preserve shell jobs as first-class activity. A subagent-only tree would hide the operational work the panel exists to expose.

## Product Decisions

The panel becomes **Activity** and uses a delegation tree with a chronological activity stream under each session or subagent.

- The current session is the root.
- Shell jobs appear inline under the session or subagent that owns them.
- Delegate jobs appear at their true chronological position and introduce nested subagent nodes.
- The tree recurses without a fixed depth.
- Selecting any item inspects it inside the panel.
- The first view expands the root and every path that contains active work.
- Completed branches start collapsed and subdued, but all retained history remains available.
- The existing jobs endpoint is replaced in place. The client does not join multiple APIs or infer lineage.

The panel is an operational inspector, not a second global navigation tree. Opening a subagent transcript remains an explicit secondary action.

## Wire Contract and Server Architecture

Replace the flat `serf/jobs/list` response with a rooted activity-tree response for the requested session. Do not add a parallel endpoint.

The recursive response contains three concepts.

### Session node

A session node carries:

- stable session identity and transcript reference;
- display name or concise fallback label;
- aggregate state;
- active and failed descendant counts;
- an ordered list of activity entries;
- an optional branch-level read error when descendant history is unavailable.

### Shell-job entry

A shell-job entry carries:

- stable job identity;
- explicit owning-session identity;
- command and concise description;
- exact wire status and optional reason;
- start and end timestamps;
- exit code when present;
- output size and output-availability metadata;
- background state.

### Delegate entry

A delegate entry carries the delegate job's normal job metadata plus:

- stable delegate identity;
- explicit child-session identity;
- child transcript reference;
- mandate or task text;
- the child session node when readable;
- an explicit child-unavailable state when the child cannot be read.

This structure makes parentage authoritative. The server derives it from session and job records; the React client never guesses ownership from descriptions, task text, timestamps, or job ordering.

### Ordering and history

Within each owning session, activity entries retain durable append order, which represents start order for this surface. A delegate entry occupies its true position in that sequence. Expanding it reveals the child session's own ordered activity.

The server overlays live job records on durable history before projecting the response. One response therefore covers running work, completed work, and exited descendants. An unavailable child does not erase its delegate job; the delegate remains as a leaf with an inline availability error.

The tree builder is a bounded server-side unit responsible for:

1. lineage traversal;
2. live-over-durable record overlay;
3. deterministic ordering;
4. aggregate state and count calculation;
5. cycle and malformed-link protection;
6. partial descendant errors.

Stable IDs, rather than labels or array positions, anchor selection, disclosure state, reconciliation, and output requests.

## User Experience

### Trigger and sheet

Rename the trigger and sheet from **Jobs** to **Activity**. Once the client has loaded the tree, the trigger shows an honest active count, such as `Activity · 3`. A lifecycle push refreshes an established closed-trigger count so it does not report finished work as active.

The desktop sheet uses a master-detail layout.

- The **tree pane** shows the current session and nested activity.
- The **inspector pane** shows the selected item's details and retained output or report.

At narrow widths, selecting a row replaces the tree with the inspector. A clear Back control restores the tree. The UI does not squeeze two unreadable columns into a mobile sheet.

### Tree structure

Each session or subagent row contains:

- a disclosure control when it has activity;
- an honest status indicator;
- its concise name or mandate;
- a short activity summary;
- active, failed, and completed counts where useful.

Expanded agent nodes show a chronological stream:

- shell jobs render as first-class job rows;
- delegate jobs render as subagent rows and contain the child activity branch;
- faint guide rails and indentation express depth;
- labels and metadata remain readable at deep levels through controlled truncation, not horizontal scrolling.

On first open, the UI expands the root and every ancestor path containing active work. Completed-only branches start collapsed with a compact summary such as `6 completed · 1 failed`. User disclosure choices persist while the session pane remains mounted and survive tree refreshes.

All retained activity remains reachable. The design does not silently hide older jobs behind an implicit time cutoff.

### Selection and inspection

Selecting any row inspects it without navigating away.

A shell-job inspector shows:

- status, duration, and timestamps;
- command or description;
- exit code and terminal reason;
- output size;
- the lazily fetched retained output tail;
- a **Refresh output** action.

A subagent inspector shows:

- mandate;
- delegate and child status;
- timing;
- latest or final report when retained;
- child-activity availability errors;
- a secondary **Open transcript** action.

The root-session inspector summarizes the whole tree rather than duplicating transcript content.

### Visual language

Reuse the established token contract and shared status components.

- Green means running or working.
- Red means failure.
- Gray means completed, cancelled, stopped, idle, or ended.
- Blue means focus, selection, or a link.
- Amber means human input is required; it does not mean generic activity.

Unknown statuses render neutrally with their exact wire label. The UI never maps an unfamiliar status to running, complete, or failed.

Use spacing, typography, indentation, and subtle guide rails for hierarchy. Avoid decorative animation, shimmer, idle pulses, and simulated heartbeat. Any cadence or liveness indicator must reflect real events.

## Client Components

Keep responsibilities narrow.

### `ActivityPanel`

Owns sheet state, tree loading, refresh reconciliation, selection, and responsive master-detail transitions. It does not derive lineage.

### `ActivityTree`

Renders the recursive accessible tree and manages default expansion rules through stable node IDs. It receives the tree as data and emits selection changes.

### `ActivityRow`

Renders one root, delegate, or shell-job row. It owns no network state.

### `ActivityInspector`

Selects the appropriate root, subagent, or shell-job details view. It provides the explicit transcript action for subagents.

### Shell output view

Retains the current lazy-output boundary. It fetches output only for the selected shell job and contains its own loading, error, retry, and refresh states.

### Recursive parser

Validates the new wire response before rendering. A malformed sibling must not cause the parser to discard valid branches. Invalid parent-child links become explicit unavailable nodes or branch errors rather than invented lineage.

## Data Flow and Reconciliation

Opening **Activity** fetches the full tree. Each job lifecycle push updates `model.jobsUpdatedAt`. While the sheet is open, a new bump refetches the tree. Once the trigger has a count, lifecycle bumps also refresh it while the sheet is closed.

The client reconciles refreshed data by stable session and job IDs. It preserves:

- the selected item if it still exists;
- user disclosure choices;
- active-path defaults for newly appearing work;
- the last good tree during recoverable refresh failures.

If the selected item disappears because retained state was pruned, selection returns to the nearest surviving owner and explains that the item is no longer retained.

Output remains lazy. Selecting a shell job requests its output tail from the owning session, not automatically from the root session. The replaced tree contract therefore supplies enough owner identity for descendant output lookup. The output view stays stable while the user reads it; reselecting the row or using **Refresh output** fetches a new tail.

Subagent report and transcript fields come from the tree response. The client does not issue per-node discovery calls.

## Empty, Error, and Compatibility States

The panel distinguishes absence from failure.

- **No activity:** show a quiet empty state that says no shell or delegate jobs have run.
- **Initial load failure:** show one error state with **Try again**.
- **Refresh failure with retained data:** keep the tree, mark it stale, and offer retry.
- **One unreadable descendant:** keep healthy branches and the delegate row; show the failure at that branch.
- **Output failure:** preserve selection and metadata; replace only the output area with an error and retry.
- **Exited root session:** serve durable tree and output where retained. Label unavailable live-only fields rather than calling the session empty.
- **Unavailable child session:** preserve the delegate as a leaf with an explicit message.
- **Incompatible daemon or response:** use the existing capability-gap state. Do not render a misleading partial flat list.

Errors should state what failed and what remains available. A branch error must not become a global toast-only failure that leaves the tree unexplained.

## Accessibility and Keyboard Behavior

Use correct recursive tree semantics or an equivalent disclosure-list pattern whose keyboard behavior is fully tested. At minimum:

- every disclosure has an accessible name and expanded state;
- Up and Down move among visible rows;
- Right expands a collapsed branch or moves into an open branch;
- Left collapses an open branch or moves to its parent;
- Enter or Space selects the focused row;
- focus remains stable across refreshes when the item survives;
- status is conveyed in text, not color alone;
- inspector headings identify the selected item;
- the narrow-screen Back action restores focus to the originating row.

## Testing Strategy

Follow `docs/testing.md`. Default tests remain deterministic and require no provider credentials, network access, quota, model behavior, or ambient machine state. Use structured state and fake transports instead of sleeps or polling races.

### Server and tree-builder tests

Cover:

- a root with shell jobs only;
- shell jobs owned by nested subagents;
- delegates at their correct chronological positions;
- multiple delegation levels;
- live records overlaid on durable history;
- exited root and descendant sessions;
- a missing or unreadable child session;
- corrupt branch data without loss of healthy siblings;
- malformed links and cycle protection;
- deterministic aggregate states and counts;
- descendant output ownership and lookup.

### Wire and hub tests

Cover:

- the replaced endpoint's recursive response;
- live-daemon routing;
- exited-session fallback;
- descendant-owned output requests;
- incompatible responses and capability gaps;
- round-trip fidelity for every recursive response field.

Where two decoding or projection paths must agree, use the reflection-based round-trip method described in `docs/testing.md` so future fields cannot bypass the test silently. Prove the test by mutation.

### Parser tests

Cover:

- valid recursive trees;
- unknown statuses;
- missing optional metadata;
- malformed nodes and siblings;
- unavailable-child leaves;
- deep but valid nesting;
- stable identity extraction.

### React tests

Cover:

- active paths open by default;
- completed-only branches start collapsed;
- all retained history remains reachable;
- shell jobs appear under the correct owner;
- delegate rows occupy chronological positions;
- selection opens the correct in-place inspector;
- subagent transcript navigation remains secondary;
- refresh preserves selection and disclosure state;
- newly active paths become visible without reopening the panel;
- stale, partial, empty, terminal, incompatible, and output-error states;
- narrow-screen tree-to-inspector and Back behavior;
- accessible labels, tree semantics, focus restoration, and keyboard navigation;
- unknown statuses remain neutral and visible.

### Integration and browser checks

Use the scripted app transport and real reducer/store path to prove that nested shell and delegate lifecycle changes update the open tree and the established closed-trigger count.

Add or update real-browser guards when CSS behavior cannot be established in jsdom. In particular, prove deep rows do not create horizontal scrolling at supported widths and that the narrow sheet presents one readable pane at a time. Use registered production item types and mutation-test each guard as required by `docs/testing.md`.

## Out of Scope

This change does not add:

- job stop, message, or steering controls;
- activity search, filtering, or time windows;
- a redesign of the global project/session sidebar;
- changes to transcript subagent cards;
- a second jobs-tree endpoint;
- heuristic lineage recovery in React.

Those features can build on the explicit tree contract later, but they are not required for a clear, truthful current-session activity view.

## Acceptance Criteria

The redesign is complete when:

1. **Activity** shows the current session, every retained subagent level, and every retained shell job under its authoritative owner.
2. Delegate and shell entries preserve chronological placement within each owner.
3. Active paths open by default; completed history remains available without dominating the first view.
4. Selecting any item opens an in-place inspector, and descendant shell output loads from the correct owner.
5. Refreshes preserve stable user context and surface partial failures without destroying healthy data.
6. The visual treatment follows the existing color, motion, spacing, and accessibility rules.
7. The old flat jobs-list response no longer exists; the replacement recursive contract serves live and exited sessions.
8. Deterministic server, wire, parser, React, and browser tests prove the behavior.
