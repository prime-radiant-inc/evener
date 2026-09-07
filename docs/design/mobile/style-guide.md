# Mobile style guide

**Proposed foundations · 5 September 2026 · Review before implementation**

These are Evener design proposals informed by the [research](sources.md). Numerical values are starting points for screen studies, not measured results or universal platform requirements.

## Visual foundations

| Element | Starting rule |
|---|---|
| Canvas | Quiet neutral surface; light `#FAFAF8`, dark `#121417` candidates |
| Primary text | Light `#202326`, dark `#F1F2F3` candidates |
| Secondary text | Light `#62676D`, dark `#A6ADB5` candidates; never use opacity alone as a contrast strategy |
| Accent | Light `#315AD7`, dark `#9CB4FF` candidates; reserve strong filled accent for a primary action |
| Dividers | Sparse, subtle separators between meaningful groups; not a box around every row |
| Status | Semantic success, warning, danger and informational roles; always paired with words or symbols |
| Typography | System fonts first; prose around 17 pt on iOS and 16 sp on Android, adapting to user settings |
| Metadata | Around 13 platform text units at default size; grow with accessibility settings |
| Code | Platform monospace, selectable and copyable; long lines scroll inside a bounded code region |
| Spacing | A small 4/8/12/16/24/32 scale, adjusted for native control geometry |
| Content inset | Begin at 16–20 logical units; align titles, text and controls consistently |
| Radius | Follow component purpose and platform; ordinary transcript content has no enclosing radius |
| Icons | Platform-appropriate symbols with consistent optical size and weight; labels for ambiguous actions |

Validate every final foreground/background pair. Target 4.5:1 for ordinary text and 3:1 for essential non-text UI. The palette above is provisional; colors are not an accessibility certification. Use semantic tokens, including elevated surfaces and increased contrast variants, rather than scattered literals.

## Density and hierarchy

A session row should present title first, then a short useful preview or context line. A status indication can share that hierarchy; timestamps, hub names and counts should not all compete at the same size. Begin around 56–72 logical units for a two-line session row at default text size, then let content and accessibility determine height.

Use at least 44 pt interactive regions on iOS as our proposed minimum and 48 dp on Android. The visible icon or text can be substantially smaller than its touch region. Do not add a full extra touch-sized margin around an already accessible row. Separate turns by roughly 24 units; keep related text and activity closer, around 8–12. These relationships matter more than rigid heights.

## Component contracts

### Navigation and hub identity

Use the OS navigation structure, safe areas, back behavior, menus and sheets. The selected hub must be discoverable at the root and explicit in destination-sensitive actions. For aggregated lists, include a readable hub name. Keep normal connection health quiet; elevate loss of connectivity or uncertain delivery. Avoid a permanent strip of low-value status controls.

Use one session-actions overflow icon in the conversation header, preserving space for the title. Expose Session details, Tasks and Activity directly through the native iOS menu and Android action sheet. The Android sheet includes the full session and hub names; it must scroll with large text. Keep a stable route back to saved hubs and ongoing sessions. Setup explains where execution happens and what must be available on the host. Receiving events, establishing a connection and confirming a submitted action are different states; the interface must not substitute one for another.

### Conversation

Assistant prose sits directly on the reading surface. Distinguish speakers with alignment, attribution and spacing; investigate a restrained user-message surface in screen studies. Render headings, lists, links, quotes, code and tables as content, with purposeful overflow handling. Do not repeat a large role label before every fragment of one response.

Group routine activity with a concise, understandable summary and an accessible expansion control. Expanded content stays near its origin. Keep actionable errors, questions and approvals visible. A summary cannot erase warnings or pretend unfinished work has completed. Screen readers receive state and expansion semantics without announcing every streamed token.

### Composer and running-session actions

Keep the input and its main action visually unified. Respect safe areas and the actual keyboard transition. Use native text editing, selection and dictation affordances. Attachment controls and secondary actions must not overpower the text. While work is running, distinguish steer, queue and stop through clear action placement and labels; do not imply a send succeeded before confirmation.

Model and reasoning are composer controls, not session-management settings. Show the current session choices in the input footer, using compact labels with full accessible names and native-size touch regions. Give the draft the full available width at every text size. Place the primary submit action on the controls row with attachment access and model/reasoning, with Stop and Queue available when applicable. At large text sizes, give model/reasoning their own row above the shared attachment and submission controls; keep attachment and Submit together where they fit, and preserve the full-width draft. This follows Jesse’s correction recorded in MOB-011; earlier same-row draft/submit studies are superseded. Keep transcript navigation outside the writing surface. Keep model and reasoning separately tappable: model opens a searchable catalog, reasoning opens a short choice sheet. Their visible presence must not add a permanent extra toolbar row at ordinary text sizes. Let the footer wrap at accessibility sizes instead of hiding the controls or shrinking touch targets. Bound the composer within the keyboard-visible area; if its contents exceed that space, let the draft and controls scroll. Keep the command results list outside that scroll view so both regions retain native scrolling. Bound the whole suggestion panel to the available keyboard-visible height, including its header. Let the header scroll with results in short viewports so both suggestions and Dismiss remain reachable; reserve space for editing and actions.

These choices persist for subsequent session work; they are not per-message overrides. Preserve the draft when opening and dismissing either picker. Block sending during a settings mutation, show its pending state in the composer if its sheet is dismissed, and retain a visible route to any failure. Treat the server projection as authoritative, including capability and reasoning-ladder changes. Session management contains name, context and runtime controls.

Scope drafts to hub and session. Preserve them through settings visits, backgrounding and relaunch; keep an uncertain submission distinct from a newer draft. With long dictation, pasted logs or large text, bound the input's growth and retain a reachable submit action above the keyboard. Ordinary OS dictation does not require the deferred interactive voice feature.

### Decisions and failures

An approval shows the requested action, destination, relevant consequence and available choices together. Give dangerous actions appropriate emphasis without turning the entire conversation red. Questions remain answerable in context. A connection failure explains recovery; uncertain delivery offers reconciliation rather than blind repeated submission.

Give consequential decisions more visual weight than routine transcript activity. Closing a sheet must not hide an unanswered decision: retain an accessible route to it in the session. Reconcile answers made on another device so resolved decisions cannot invite duplicate action.

### Reviewing changes

Study a changed-file summary leading to readable diffs, with line wrapping and contextual discussion. Keep code selection and copying native where possible. Verify Evener's supported review operations before offering controls; these are screen-study requirements, not claims of implemented protocol capability.

### Lists and settings

Prefer clear section titles and aligned rows over a card for each setting. Search, filtering, pagination and bulk operations need designed empty, loading and error states. Swipe actions supplement discoverable controls. Never require a gesture as the only way to perform a task.

## Motion and fluency

Use native interactive navigation and keyboard transitions wherever possible. Preserve gesture cancellation and OS back behavior. Content disclosures may begin with a short 180–240 ms transition, but adjust through device testing. Do not animate every token or move the viewport during active reading. Reduced motion uses stable replacements and avoids large spatial transitions.

Measure long-list scrolling, streaming updates, keyboard opening and navigation on representative devices. Inspect dropped frames and input latency. At 60 Hz a frame has about 16.7 ms; at 120 Hz about 8.3 ms. These are budgets, not evidence that the app currently meets them. Avoid layering expensive visual effects before measuring their cost.

## Platform adaptation

| Concern | iOS | Android |
|---|---|---|
| Navigation | Native back gesture, navigation bars and modal conventions | Native back/predictive-back behavior and Material navigation conventions where supported |
| Materials | System materials for appropriate navigation/control surfaces; no glass transcript cards | Material surface hierarchy and selective expressive emphasis |
| Text and input | Dynamic Type, native editing and keyboard behavior | Font scaling, native editing and keyboard/inset behavior |
| Actions | Familiar context menus and action sheets | Familiar menus and bottom sheets |
| Shared contract | Same destinations, meaning, safeguards and restored context | Same destinations, meaning, safeguards and restored context |

Native library support must be verified; a design aspiration is not proof that a React Native component implements an OS behavior.

## Required review fixtures

Use identical content across comparisons: a long session title; several hubs with overlapping names; a prose response with headings and code; a burst of tool activity; an approval; a question; a blocked operation; an offline session; uncertain delivery; a draft while switching sessions; keyboard open; largest accessibility text; screen-reader navigation; reduced motion; light and dark appearances.

Record task completion, accidental taps, lost context, and reading interruptions during review. Proposed targets: no lost draft or reading position on ordinary return, no automatic scroll away from earlier content, and a visible path to the next required decision. Record failures as product work rather than explaining them away as polish.

The [agent-product research](agent-mobile-research.md) adds interruption sequences to these fixtures: background during a pending approval; answer it on another device; reconnect after missing events; visit settings with a long draft; relaunch with an uncertain submission; and return from a notification to the exact hub and session. Exercise long input with the keyboard open on both platforms. Historical competitor reports motivate these scenarios; they do not establish defects in current releases.
# Session search

Search the hub, not just loaded rows. Keep the submitted query visible while results load and when returning from a conversation. Use keyboard Search and a visible action; Clear restores browsing. At accessibility text sizes, give the input its own row. Never advertise Load more until the hub can return a correct continuation across its combined sources.

## Project browsing

Use server project names and keys. Show current, recent and archived work as selected tabs, with shape and accessibility selection in addition to color. Keep loaded pages when returning from a conversation. Refresh replaces the list; Load more appends only within the same server generation and resource revision. Explain changed-list conflicts instead of silently mixing snapshots.

Live list changes should not move a row under the person’s finger. Preserve the visible snapshot, show a quiet update prompt, and pause continuation until Refresh. Treat ordinary invalidation as new information, reserving error treatment for failed reads.

Related sessions use a separate disclosure action so opening a conversation never also expands it. Keep expanded destinations in the same scrolling list, cap indentation to retain reading width, and preserve expansion on return. State omitted content plainly rather than implying the visible tree is complete.

## Organization actions

Use a quiet More affordance beside project/session rows with a full platform touch target and an accessible name containing the item title. Show the selected title and hub in the action surface. Archive and restore belong together through the Archived view. Keep organization separate from runtime stop and deletion. A successful mutation must be reflected by a server-confirmed read; retain visible failure information when that cannot be established.

## Approval decisions

Keep pending decisions discoverable beside the composer through a compact count. Open a dedicated platform sheet so the command, blocked path and partial execution warning can be read before deciding. Use explicit Allow once and Deny labels. Dismissal preserves the request. Update an open sheet when another client resolves it; an empty sheet should say no approvals remain. Let enlarged text wrap and scroll to actions rather than shrinking the decision context.

## Structured questions

Show pending questions through a compact composer entry. Keep suggested choices and a written alternative together. Recommendations must never become automatic answers. Distinguish single and multiple selection through native accessibility roles, checked state and visible marks. Require explicit resolution, including Skip, for every question in a batch. Preserve the ordinary message draft and separate confirmed delivery from the following refresh. Optional notes and delegation belong to the same question context.

Persist unfinished question selections separately from the ordinary message draft. Restore only when the hub, session and complete question definitions still match. Show failed saves explicitly and retain edits for retry; never silently enable sending after a failed load.


## Resource settings: scan first, inspect on demand

A resource row leads with its filename, directory name, or MCP server name. A
short parent-folder or executable hint distinguishes entries without repeating
full paths. Tapping the row reveals exact path or command/argument details in
place. The disclosure exposes its expanded state and has a native-sized touch
target. Remove remains a separate adjacent action. Large text may grow the row;
never shrink text to preserve a fixed height.

Resolved effective values live in a separate disclosure, initially collapsed,
with their entry count. Expanded effective entries use the same inspectable rows.
Do not repeat effective and editable full paths by default. Verify viewport
allocation with the same content before/after, keyboard reachability, and access
to the entire expanded value. This direction does not constitute native visual
acceptance.


## New-session composition

Treat the opening prompt as a composer, with its model and reasoning choices and
Create action in a footer beneath full-width text. Use a searchable model sheet
rather than an inline catalog. Display the actual effective selection, including
advanced overrides; changing a composer choice must replace the corresponding
hidden override. Collapse harness choices until requested. Recent-project labels
can show directory names, retaining the full destination as the accessible label.
On prompt focus and keyboard layout changes, reveal the composer footer without
requiring the user to dismiss the keyboard. Large text may wrap the footer into
multiple control rows; no control consumes the input's horizontal space.


### Creation configuration grouping

Keep related directory controls close together. At ordinary text sizes, pair
project settings with harness selection and plugins with session options in
wrapping rows. Preserve native touch targets while removing redundant space
between them. Expanded configuration uses the full content width. The opening
prompt and composer controls remain a separate, full-width writing surface.

At large text sizes, creation uses the same two-part footer as conversation:
model/reasoning above, attachment and Create together below. Bound the opening
prompt so it scrolls internally and cannot consume the whole keyboard-visible
area. Keep the focused composer visible when text metrics change.
