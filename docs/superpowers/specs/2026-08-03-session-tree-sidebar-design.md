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

The recursive response contains three concepts. It represents one subagent
conversation once, even when that conversation has several delegate turns.

### Session node

A session node carries:

- stable session identity and transcript reference;
- display name or concise fallback label;
- aggregate state and complete/incomplete-count metadata;
- recursive active, failed, and completed work-unit counts;
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

A delegate entry represents one subagent conversation and carries:

- stable delegate identity;
- explicit child-session identity;
- child transcript reference;
- mandate or task text;
- every retained delegate-turn job for this delegate, in start order;
- the child session node when readable;
- an explicit child-unavailable state when the child cannot be read.

The UI renders the delegate entry and its child session as **one subagent row**,
keyed as `delegate:<delegateID>`. The child session node supplies the disclosed
branch but does not render a second header row. Selecting the row inspects the
subagent conversation and its delegate-turn history. Its displayed status comes
from the current delegate turn when one exists, otherwise the latest turn. The
child session's aggregate state appears separately in the inspector, so a failed
turn and still-readable child history do not collapse into one ambiguous label.

This structure makes parentage authoritative. The server derives it from session and job records; the React client never guesses ownership from descriptions, task text, timestamps, or job ordering.

### Ordering and history

Within each owning session, activity entries retain durable append order, which
represents start order for this surface. A delegate conversation is anchored at
its first delegate turn and appears once at that position. Later turns remain in
the row's turn history rather than duplicating the subagent branch. Expanding the
row reveals the child session's ordered activity.

The server overlays live job records on durable history before projecting the response. One response therefore covers running work, completed work, and exited descendants. An unavailable child does not erase its delegate job; the delegate remains as a leaf with an inline availability error.

The tree builder is a bounded server-side unit responsible for:

1. lineage traversal;
2. live-over-durable record overlay;
3. deterministic ordering;
4. aggregate state and count calculation;
5. cycle and malformed-link protection;
6. partial descendant errors.

### Count and aggregate semantics

Counts measure **work units**, not visible rows. A shell job is one work unit,
and each delegate turn is one work unit. Session nodes and subagent-conversation
rows are not additional units, so the tree never double-counts a delegate turn
and its child session.

The server supplies each work unit's exact status plus an authoritative
`terminal` flag and outcome category. Categories are `success`, `failure`, and
`neutral`; an unfamiliar status keeps its exact label and uses no invented
outcome. Counts recurse through all loaded descendants:

- **active** counts every non-terminal work unit, including an unfamiliar
  non-terminal status;
- **failed** counts terminal `failed` and `exhausted` work units;
- **completed** counts every other terminal work unit, including `cancelled`
  and `stopped`.

Aggregate state uses this precedence: active work → `working`; otherwise any
failure → `failed`; otherwise an incomplete or unreadable branch →
`unavailable`; otherwise any retained work → `ended`; otherwise `idle`.
Unavailable or truncated branches never contribute guessed counts. The response
marks aggregate counts incomplete, and the UI labels them as partial instead of
presenting them as totals. Human-attention state is not inferred from job
status; amber remains reserved for a separate authoritative needs-input signal.

### Traversal bounds and continuation

The logical tree has no product-level depth limit, but one response must have
resource limits. The server stops a response at 2,000 work units, 32 newly
expanded delegation levels, or 4 MiB of encoded tree data, whichever comes
first. A cut branch returns an explicit `truncated` marker and opaque
continuation token. **Load more activity** calls the same `serf/jobs/list`
method with that token and grafts the returned branch by stable ID. Tokens are
scoped to the root session and rejected if used elsewhere.

This is pagination of the replacement endpoint, not a second tree endpoint.
It keeps all retained history reachable without allowing one old session to
produce an unbounded response. Cycle detection produces a branch error rather
than a continuation token.

### Supported sources

The recursive contract is a Serf-local capability. A live local root resolves
live descendants through their delegate transcript references. An exited local
root resolves durable descendants only within the same configured Serf state
directory; a missing past-index or child record becomes an unavailable branch.
Non-Serf sources, foreign state directories, and sources that do not implement
the replacement contract use the capability-gap state. The server never crosses
source or state-directory boundaries while following a retained transcript
reference.

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
- delegate-turn history and the latest retained report when available;
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

Renders one root, subagent-conversation, or shell-job row. It owns no network
state. A subagent row owns the disclosure for its child-session branch; the
child session does not render a duplicate row.

### `ActivityInspector`

Selects the appropriate root, subagent, or shell-job details view. It provides the explicit transcript action for subagents.

### Shell output view

Retains the current lazy-output boundary. It fetches output only for the selected shell job and contains its own loading, error, retry, and refresh states.

### Recursive parser

Validates the new wire response before rendering. A malformed sibling must not cause the parser to discard valid branches. Invalid parent-child links become explicit unavailable nodes or branch errors rather than invented lineage.

## Data Flow and Reconciliation

Opening **Activity** fetches the first bounded page of the rooted tree. Each job lifecycle push updates `model.jobsUpdatedAt`. While the sheet is open, a new bump refetches the loaded portion of the tree. Once the trigger has a count, lifecycle bumps also refresh it while the sheet is closed.

Direct root-job notifications keep their existing behavior. Descendant job and
delegate lifecycle changes also emit a root-scoped `serf/jobs/treeUpdated`
notification containing the root ref and an opaque monotonic tree revision. The
hub routes that notification to every open client watching the root. The reducer
stores the revision and bumps `jobsUpdatedAt` on the root model. This ancestor
invalidation, rather than child-model notifications, keeps the open tree and
closed trigger count current when a nested shell job starts or finishes.

The client reconciles refreshed data by stable session and job IDs. It preserves:

- the selected item if it still exists;
- user disclosure choices;
- active-path defaults for newly appearing work;
- the last good tree during recoverable refresh failures.

If the selected item disappears because retained state was pruned, selection returns to the nearest surviving owner and explains that the item is no longer retained.

Output remains lazy. Selecting a shell job requests its output tail from the
owning session, not automatically from the root session. Selecting a subagent
loads the latest delegate turn's retained output through the same output method;
that bounded text is the latest/final report shown by the inspector. The tree
response carries report availability and the relevant owner/job IDs, but not
report bodies. The output view stays stable while the user reads it; reselecting
the row or using **Refresh output** fetches a new tail.

Transcript links and report-availability metadata come from the tree response.
The client does not issue per-node lineage or transcript-discovery calls.

The live/durable merge is a union keyed by job ID. Durable append sequence wins
when present. A live-only job is inserted by start timestamp with job ID as a
stable tie-breaker, then reconciles in place when its durable start event becomes
visible. Duplicate IDs never create duplicate work units. The normal write path
persists a start event before notification, but the union rule makes recovery
and partial-write behavior explicit.

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
- **Bounded response:** show the loaded branch with partial counts and an inline
  **Load more activity** control at each truncated edge.

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
- live-only record insertion and durable reconciliation without duplicates;
- response node, depth, and byte bounds plus continuation-token scoping;
- descendant output ownership and lookup.

### Wire and hub tests

Cover:

- the replaced endpoint's recursive response;
- live-daemon routing;
- exited-session fallback;
- descendant-owned output requests;
- root-scoped descendant invalidation and monotonic tree revisions;
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
- truncated branches and partial-count metadata;
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
- repeated turns of one delegate render one subagent row with ordered turn
  history;
- truncated branches load through the same endpoint without losing selection;
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
8. Nested lifecycle changes invalidate the root tree and closed trigger count.
9. Large trees remain fully reachable through explicit continuation on the same
   endpoint, and partial counts are never presented as totals.
10. Deterministic server, wire, parser, React, and browser tests prove the behavior.
