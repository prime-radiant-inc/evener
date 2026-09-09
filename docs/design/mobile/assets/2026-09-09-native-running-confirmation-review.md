# Native running confirmation review

Scope: bounded read-only confirmation of the signed native artifact `5c253d512435fd80fee3407284ede5a54eef786d` using the supplied light/dark captures, AX JSON, installation receipt, held readback, and recovery-pass receipts. No source, fixture, simulator, or Git changes were made.

## Verdict

The narrow running and recovery correction is confirmed for the supplied iPhone specimen. No actionable issue was observed in this confirmation pass.

## Busy running state

The light and dark keyboard-open captures show the same readable hierarchy: the
draft remains full width; `1 queued` and `Goal · active` are separate metadata
actions; the settings values (`study-model`, `(default)`, `Vision`) occupy their
own readable row; and `Steer`, `Stop`, and `Queue` remain distinct actions. AX
confirms the corresponding enabled 44-point controls (`1 queued` and `Goal ·
active` at y=334, Message at y=382, and the three actions at y=478). The
connected running state presents the intended primary `Steer` action for the
active turn.

The dark and light captures are stable enough to assess layout and action
labels. No contrast finding is raised from this set.

## Recovery confirmation

The recovery-pass receipt records the same conversation, draft text
`keep this draft local until delivery is confirmed.`, and anchor bounds before
disconnect, after the disconnect settled, immediately after tapping
`Reconnect`, and after recovery. The tracked anchor remains exactly
`y=267.33331298828125`, with `localActionDidNotMoveReader: true` and
`resumedSameProcess: true`. The provider request count is 9 before and after;
the receipt records `providerRequestsUnchanged: true`.

The immediate-after-tap and post-tap AX captures show the explicit `Reconnect`
control, an `Updating session…` status, the preserved Message draft, and a
disabled Send while the session is unavailable. The recovered dark capture
shows Connected, the same draft, and an enabled Send. The recovery source fix
reserves the connection row height at 44pt on iOS (48pt on Android), matching
the observed stable anchor behavior.

The earlier failed final attempt was an automation decimal-coordinate parser
failure, not an application failure. The second rounded-coordinate run is the
passing recovery evidence represented by `recovery-result.json`.

## Provenance

- `install.json` binds the installed app to source `5c253d512435fd80fee3407284ede5a54eef786d`.
- `held-readback.json` binds the resumed conversation to the owned local hub and preserves its active session data.
- `recovery-pass/recovery-result.json` binds the disconnect, explicit reconnect tap, same-process resume, unchanged anchor, unchanged provider count, and unchanged draft.
- The reviewed source artifact hashes are recorded separately in the implementation review; this report is bound to the signed source identity above.

## Evidence limits

This confirmation does not close the full Task 15 specimen, actual uncertain-delivery modal coverage, project/queue return flow, physical-device or performance qualification, accessibility/iPad review, or TestFlight acceptance. Screenshots and AX cannot establish arbitrary backgrounding, rotation, dynamic-type scales, tap latency, or provider behavior outside the recorded run.

## Project refresh confirmation

The project-specific runtime confirmation is bound to installed source
`c5fb198a92520c4f807999a3102c1ce473af6332` by `confirmation-project/install.json`.
The rename → refresh → restore-name → refresh sequence produced exactly one
project update notice and one named `Refresh` action, with no false
`Could not load more sessions` / `Retry` rows. `project-result.json` records
`oneProjectStaleNotice: true`, `oneProjectRefresh: true`,
`noFalseRetryRows: true`, the changed name after refresh, and restoration of
the original study name after the second refresh.

The light and dark stale captures show the notice in the expanded project while
the project row continues to expose its attention count. Refreshed AX confirms
the project action `evener-iphone, 1 needs attention`, a 68-point session row,
and no retry action for the stale condition. The separate global
`Projects have changed` / `Refresh projects` footer remains a catalog-level
notice; the per-project refresh is intentionally one notice per expanded
project rather than one global notice.

`preservation.json` confirms all seven original draft-table rows are unchanged
(new fixture rows are isolated), all original reader entries are unchanged,
other storage values are exactly equal, the original reader location is
restored, and the protected Apple diff hash is preserved. No source or runtime
regression was found in this bounded project confirmation. Root owns the final
combined gates and public evidence distinction.
