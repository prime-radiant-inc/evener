# Native sandbox approvals

Verified 5 September 2026. A compact approval count beside the composer opens an iOS page sheet or Android full-screen modal. The sheet identifies the hub, tool, blocked path, sandbox mode, command, partial execution warning and available output. Allow once and Deny are explicit actions; dismissing does not decide. Resolutions received from another client remove the pending request while the sheet is open.

The shared conversation projection owns pending approvals. Initial hydration subscribes before reading, reconciles approval notifications and rereads for overlapping non-idempotent traffic. A resolution received during a later snapshot read cannot be resurrected by that older snapshot. Native decisions check the displayed request and live session binding, serialize dispatch, and refresh after acknowledgement. Uncertain failures stay visible without automatic replay.

## Evidence

- Native TypeScript and 94 tests across 17 files pass. Seven approval tests cover hydration, identity filtering, changed requests, stale/disposed bindings, concurrent decisions, uncertain failure, a stale rehydrate, initial snapshot overlap and failed initialization cleanup.
- Shared mobile TypeScript and 2,238 tests across 90 files pass. Touched native files pass Biome. Independent review closed initial notification loss, overlapping delta replay and failed initialization cleanup findings; the final bounded review reported no remaining findings.
- Both final Release builds installed successfully. On final sources, iOS dismissal preserved the pending request without dispatch. iOS Allow once sent one true decision and cleared both devices. After fixture reset, Android Deny sent one false decision and cleared both devices. Both then displayed No approvals pending without the obsolete waiting instruction.
- Inspected final screenshots on both simulators. iOS accessibility-large text reflowed and scrolling reached both decision actions; that capture precedes only notification buffering and empty-state copy changes. Text size was restored afterward. Android approval-specific enlarged-text and screen-reader acceptance remain open.
- Manual decisions used a controlled WebSocket fixture for the owned isolated-hub session. It injected one escalation and intercepted its decision; other traffic forwarded to the real isolated hub. No actual blocked command executed or resumed. The fixture was removed and the ordinary all-interface navigation proxy restored afterward. Production was untouched.

![iOS approval](assets/approval-ios.png)
![Android approval](assets/approval-android.png)
![iOS enlarged approval actions](assets/approval-ios-large.png)

## Remaining acceptance

Real sandbox execution/resumption, background/reconnect decision races, acknowledgement-loss injection in the native UI, screen readers and physical devices still need E2E acceptance. Structured questions are a separate unfinished workflow. This increment does not claim all user-decision surfaces, the full repository merge gate or release readiness.
