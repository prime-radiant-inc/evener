# Mobile workflow studies

**Design inputs, not approved screens · 5 September 2026**

The [style guide](style-guide.md) needs to hold up against the work below. This document identifies the content and transitions to design before implementing more visual components. Current-source audit: `fabdb86db`, including the generated AppWire method and notification catalog. The [coverage map](../../superpowers/specs/2026-09-05-native-mobile-coverage.md) records implementation and evidence separately.

## Study 1: Find the right work

Use two fictional hubs, **Studio** and **Workshop**, both containing a session titled **Improve connection recovery** with the same fixture-local reference `session-42`. Studio has 120 sessions, several long project paths, one failed session and one needing approval. Workshop is disconnected. Include pinned sections, archived sessions and an empty search result.

Show the starting list, search results, a project grouping, a selected session and return navigation. The person must identify the destination without relying on color, reach sessions beyond the first page, and recognize what needs attention. Returning should preserve filter and position. Explore the navigation placement in screen studies; do not assume an aggregated list or simultaneous hub connections is already approved or implemented.

Protocol scope: `thread/list`, `evener/navigation/read`, `evener/search`, favorite/archive, pin assignment and section management. Account for `evener/navigation/invalidated` and `evener/attention/changed`, not just refresh-on-open.

## Study 2: Read and inspect substantial work

Use the following conversation content consistently on both platforms:

> **Jesse:** Make reconnect reliable without losing the message I was typing. Keep the current hub selected and explain what happens if a send times out.
>
> **Assistant:** I found two separate state lifetimes: the connection and the draft. Replacing the connection currently replaces the draft’s owner.
>
> ### Proposed behavior
> - Keep the draft under the hub and session identity.
> - Show an uncertain send separately from new text.
> - Reconcile with the transcript before offering another submission.
>
> ```ts
> const destination = { hubId: selectedHub.id, sessionRef: session.ref };
> await drafts.save(destination, composerText);
> ```
>
> | Event | What stays available |
> | --- | --- |
> | Reconnect | Draft and reading position |
> | Switch hub | Each hub’s own draft |
> | Uncertain send | Original text and reconciliation action |
>
> **Jesse:** Keep the newer draft too. Don’t replace it with the message whose delivery is uncertain.

Surround this content with 23 internal setup entries, six tool calls, a long tool output, a warning, an image attachment and a streamed response. These are fictional study data, not claims about completed implementation. Show default and expanded states, copying code, opening the attachment, loading older history, following new output and reading earlier content while output arrives.

Protocol scope: `thread/read`, turns/items pagination, transcript listing, unsubscribe, item deltas/resets, warnings and resync. Rich display must handle the canonical projected content; do not invent new wire representations for a mockup. A terminal-style viewer and an ordinary answer need different presentation while retaining the same underlying information.

## Study 3: Act while work is running

Start with active work, one queued message and a new draft. Show steering, queuing, queue inspection, cancelling a queued item, promoting it to steering and stopping. Include an action capability changing while the composer is visible. Then disconnect immediately after a submission: its outcome is unknown, and the person has typed a newer draft.

The design must distinguish the primary action, secondary queue operations and stop without requiring a permanent wall of buttons. It must preserve both texts and avoid suggesting blind resubmission. Render keyboard-open and large-text variants. Demonstrate native back and cancelled gestures with a draft in progress.

Protocol scope: `turn/start`, `turn/steer`, `turn/queue`, `turn/interrupt`, `turn/drainAsSteer`, `turn/promoteQueuedAsSteer`, `turn/cancelQueued`, status and queue notifications. Persistent drafts and delivery reconciliation are client responsibilities as well as protocol concerns.

## Study 4: Make a decision in context

Include a sandbox escalation requested for a specific tool, a question with recommended and alternative answers, a free-text response, a decision resolved from another client and a request invalidated by session change. Include a tool failure that requires no approval, so the visual system cannot confuse warnings with decisions.

Show enough context to understand the requested action and destination. Keep the decision visible without turning routine activity into equally prominent cards. When the state changes remotely, remove obsolete choices and explain the outcome. Verify the supported question-answer contract before implementation; the presence of displayed options alone does not prove structured response support.

Protocol scope: `evener/sandbox/escalation/resolve` and requested/resolved notifications; question content and response flow through the existing conversation contract. Goals, task updates, jobs and delegate status can also direct attention and must fit the same hierarchy.

## Study 5: Start, configure and manage work

Show creation with a recent project, path completion, directory validation, a large model catalog, optional harness/reasoning choices and a repository trust decision. Include a failed creation and an ambiguous timeout. Then show the management surface for renaming, forking, resuming, clearing, compacting and shutting down a session.

Management actions need a mobile home without making every conversation show every command. Destructive effects must be explained accurately. Clearing and replacing a session must respect stable workspace identity and avoid carrying retired content into the replacement.

Protocol scope: thread start/resume/fork/clear/name/model/reasoning/vision/compact/shutdown; projects, paths, directory creation and git head; harness/model catalogs; launch resolution, schema, layers and trust. The creation prototype implements only a subset.

## Study 6: Administer a hub from a phone

Show a saved hub needing credential recovery, a provider requiring device authorization, an instance selection, an installed plugin with an available update and a failed marketplace refresh. Include transcript display preferences and hub upgrade status. Distinguish device-local hub credentials from provider credentials stored by the hub.

These workflows may be less frequent, but feature completeness requires understandable native access. A generic RPC form is not the design. Make long-running operations, errors and destructive choices comprehensible on a small screen.

Protocol scope: mobile pairing; provider auth status/test/login/logout/API-key/device flows; instances; plugin and marketplace lifecycle; commands; settings overview/transcript preferences; upgrade. React to auth, launch, marketplace, plugin and preference notifications so settings do not silently become stale.

## Review record required for each study

Record the reviewed design revision, platforms and screen sizes, fixture content, task outcome, discovered problems and resulting decisions. Include light/dark appearance, large text, screen-reader order and reduced motion. Later implementation evidence must link to a tested source revision and distinguish scripted plumbing tests, simulator/emulator manual interaction and physical-device results.

No study is approved or implemented by its presence here. Screen composition follows Jesse’s review of the philosophy and lookbook; these scenarios ensure that review leads to a complete mobile product rather than a beautiful happy path.
