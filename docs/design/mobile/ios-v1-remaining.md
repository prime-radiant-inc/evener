# iPhone v1 remaining implementation and acceptance plan

The order is **usable, then useful, then good**. The current source implements much of the feature surface; the remaining work is to close review findings, ship the checkpoint, and prove the supported workflows on the actual iPhone artifact. A source route or unit test does not close a workflow.

iPad and dedicated accessibility work are paused by Jesse. Android and voice are deferred. Higher-level web state extraction into the TypeScript SDK is a subsequent architectural task, after the mobile checkpoint lands.

## 1. Usable: finish and distribute this checkpoint

### Land the current source

1. Resolve any new current-head findings in activity #1091, native #1096, environment #1098 and timing #1100. These heads currently have green CI and pending reviews; their implemented corrections are described in the [status page](status.md).
2. Land activity before native so the native PR contains only its own remaining work. Land the backend recovery prerequisites with both metadata exclusions retained when their changes meet in `conversationSignals`.
3. Require current-head CI and a genuinely clean current-head RoboRev verdict before each merge. Jesse has authorized skipping another human approval at that point.
4. Compare the resulting source with qualified integration `8081b4e63`. If production behavior changes during review or conflict resolution, run the relevant regression and repeat the affected combined/product checks.
5. Preserve the original two unfinished Apple project edits and their existing stash. They are separate from the Expo native landing and must not be discarded as cleanup.

### Produce the fresh internal TestFlight artifact

1. After native lands, retarget #1039 to main and refresh its source. Obtain current-head CI and review; the earlier stacked clean review does not substitute for the main-based run.
2. Requery App Store Connect and choose an unused build number. Build 4 is the planned next candidate while it remains unused.
3. Dispatch the main-only iOS workflow. Confirm locked dependencies, native/package gates, Xcode/signing preflight, archive/export, bundle identity and encryption metadata.
4. Confirm the upload is processed and the build is available to the internal group. Retain the source SHA, workflow run, archive/IPA hashes and Apple build identity in the release receipt.
5. If the upload succeeds but final verification fails, use the workflow's verification-only recovery against the existing artifact. Do not issue a duplicate upload.
6. Install or update from TestFlight on a physical iPhone, preserve existing drafts, and run the short smoke below. Simulator evidence does not close this row.

Internal availability can be completed before external beta review. Drew's external record currently reports `NOT_INVITED`; finish the required beta contact and demo information, submit the appropriate build for review, verify approval and group availability, and verify the resulting invitation/access state. Do not describe a registered tester record as an invitation.

### Establish the daily iPhone loop

Exercise one real hub and project through the supported loop, preserving existing user data:

- Connect or reconnect to the intended hub and verify its identity.
- Find the project and session, open it, read existing work, and return to the list.
- Create a session and send a message; observe streamed text and a completed answer.
- Queue a follow-up while work is active, steer active work, and stop a turn. Verify outcomes from the daemon as well as the UI.
- Background/foreground the app and cold-launch it with an unsent draft. Confirm it remains unsent and recoverable.
- Interrupt connectivity during a write and reconnect. Confirm the app distinguishes accepted, pending, rejected and uncertain outcomes without duplicate submissions.
- Restart the hub and restore a stopped/queued session. Confirm saved identity, queue ownership, draft and reader continuity.

The [final simulator receipt](assets/2026-09-10-paired-restart-8081.json) is the current baseline for those persistence checks: 32 saved canonical items, seven draft tables, eleven other reader positions, and no automatic send.

**Exit condition:** the current TestFlight build is internally available, a physical iPhone can complete the daily loop, and any failure has an explicit recovery path that preserves the user's work.

## 2. Useful: qualify the implemented feature surface

Use the same current artifact and record the hub version, fixture/data size, exact action, observed UI outcome and authoritative server outcome. Fix failures in small PRs with a regression at the appropriate boundary.

| Area | Current implementation surface | Remaining acceptance work |
| --- | --- | --- |
| Hub lifecycle | Saved connections, identity, switching, reconnect and removal | Two-hub identity isolation, credential rotation, unreachable hub recovery, and removal without deleting another hub's drafts |
| Project/session browsing | Project grouping, search and automatic list paging | Representative history sizes, scrolling across several pages, project transitions, stale responses and opening latency |
| Conversation reader | Paging, fragment hydration, live merge, stable item identity and clusters | Long transcripts, late lifecycle events, nested member updates during reread, older-page loading and reconnect |
| Reader position | Saved anchor/offset and Latest behavior | Cold launch after reading older content, new streamed content while scrolled away, image layout changes and return navigation |
| Composer and drafts | Hub/session-scoped text and image drafts, send/queue/steer/stop | Process death, uncertain writes, attachment retention and exact input preservation across session/hub changes |
| Questions and creation | Question batches and creation sheets with persistent drafts | Concurrent batches, stale rejected answers, reconnect, creation failure/retry and draft isolation |
| Approvals | Pending requests and allow/deny controls | Multiple outstanding requests, stale decisions, interrupted connectivity and server-confirmed resolution |
| Activity and tasks | Task/activity lists, delegates, continuations and output | Nested delegate paging, merged counts, empty/terminal states, failed outcomes, output paging and text selection |
| Images and rich content | Markdown, code, authenticated images and attachment updates | Streaming/image combinations, expired authenticated image recovery, removed/replaced images and long content |
| Providers and profiles | Provider/profile editing and sign-in flows | Browser/device authorization recovery, credential errors, endpoint clearing, rejected saves and reconnect |
| Plugins and marketplace | Discovery and lifecycle/settings surfaces | Install/update/remove results, failed operations, stale catalog state and recovery without false success |
| Navigation and settings | Browser/project navigation and hub-scoped preferences | Conflicting settings changes, interrupted navigation, pin/reveal continuity and two-hub isolation |
| TypeScript SDK | Packaged transport, notifications, mutations and shared helpers | Keep native and web consumers on the shared contracts; preserve installed-package qualification when extending the API |

Do not implement speculative features merely to fill the matrix. First determine whether a gap is missing behavior, an existing bug, or missing evidence. Keep source changes proportional to the demonstrated problem.

**Exit condition:** every supported iPhone workflow has current-artifact evidence for success and ordinary interruption recovery, or is explicitly excluded from v1 by an agreed scope decision.

## 3. Good: make the daily flows feel professional

Jesse's direct feedback determines the priority: project organization, automatic loading and session-opening speed come first.

1. Measure first useful list display, session opening, loading the next page and returning to a previously opened session. Record data size, network conditions, cold/warm state and device; do not turn simulator timings into physical-device claims.
2. Trace slow paths to their cause: unnecessary detail fetches, serial dependencies, avoidable rerenders, oversized history hydration or blocked first paint. Optimize the path that controls the user-visible delay, then repeat the same measurement.
3. Review the active iPhone screens against the established design: readable project/session hierarchy, restrained status text, useful previews, clear loading/empty/error states and obvious recovery actions.
4. Verify keyboard/composer geometry, send-control reachability, scroll stability during streaming and image loading, and the distinction between reading history and following the latest message.
5. Repeat the representative daily loop after changes so visual or performance work does not regress persistence and controls.

A dedicated accessibility or iPad qualification pass remains paused; do not assign it as a v1 completion gate during this phase.

**Exit condition:** representative measurements and the physical-device journey support the quality claim, and the remaining defects and limitations are documented alongside the exact release identity.

## Completion record

Keep the [checkpoint evidence map](2026-09-10-checkpoint.md) and status page tied to observed source, CI, review, simulator/device and Apple receipts. A passing combined gate, a successful upload or an available historical build is a partial milestone. Close the iPhone v1 checkpoint only after the supported workflows and intended delivery path meet their own acceptance conditions.
