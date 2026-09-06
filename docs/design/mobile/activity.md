# Native activity and job output

Product authority: current web `ActivityPanel.tsx`, `ActivityTree.tsx`,
`ActivityRowDetail.tsx`, `activityData.ts`, `activityRows.ts`, and
`panes/transcript/JobLog.tsx`; AppWire `evener/jobs/list`,
`evener/jobs/output`, job lifecycle/tree notifications, and thread resync.
The old mobile UI is not a feature reference.

## Native behavior

Work opens the platform chooser for Tasks and Activity, leaving the composer
and Session controls in place. Activity uses the current web parser, row
hierarchy, inactive folds, delegate revision fencing, and continuation grafting.
The merge functions and job-output parser are extracted into pure modules used
by both clients, without changing the web behavior.

Rows disclose command or mandate, status, available output/exit information,
and server-provided reasons. An explicit action opens a server-provided job
transcript or delegated session. There is no invented job-stop operation.
Partial branches retain their notices and continuation controls.

Each activity reader belongs to one hub/session connection lifetime. Matching
notifications coalesce into a fresh root read; a root invalidation supersedes
an in-flight continuation. Older revisions, foreign roots, and disposed
responses cannot replace displayed activity. Refresh failures retain the last
tree with an error. Reconnect can seed the new reader with that retained tree.

Output is fetched against its owning session. Load earlier uses the server's
byte offset, not the rendered character count. A non-advancing page ends
paging without duplicating content. Failed reads retain output with an error;
Refresh replaces the loaded window with the latest tail. Output lines are
virtualized, selectable terminal text; ANSI parsing currently preserves text
but does not render its colors or emphasis.

## Verification, 6 September 2026

- Native TypeScript and targeted Biome pass; all 158 native tests pass.
  Six new transport-boundary tests cover root retention/revision guards,
  continuation ownership/count retention, invalidation coalescing, disposal,
  output byte paging, failure retention, and non-advancing pages.
- The existing activity-store tests pass after pure helper extraction.
  `make test-web` passes typecheck, tests, and lint. All five
  `make test-web-browser` guards pass.
- Both Release builds succeeded: iPhone 17 Pro / iOS 26.5 and Pixel 7 /
  Android API 35. Both were installed and launched.
- Each native composer submitted NATIVE-JOB-HARNESS to its owned session on
  the isolated real hub at port 56491, through the port 9199 AppWire proxy.
  A scripted provider issued a real background shell call that emitted
  220000 bytes, remained running for 45 seconds, then emitted its end marker.
  No production hub session or live model was used.
- Both platforms displayed the running job, opened its output, and loaded
  earlier output from byte 215904 to 211808. Both subsequently showed the
  inactive fold after the job completed without manually refreshing the tree.
  iOS expanded that fold and reopened completed output. Android system Back
  returned from output to the activity tree.
- The final Work-menu build was manually exercised on both platforms. iOS
  uses the native action sheet; Android uses the native chooser dialog.

## Remaining acceptance

This is an implemented increment, not complete activity parity or release
acceptance. Still required: real delegated-session navigation and nested
branch paging, native reconnect/fault testing, cross-hub action ownership,
complete delegate metadata and timing presentation, styled ANSI output,
output copy/selection and reading-position behavior, large-text and screen
reader coverage, long-log memory/performance, and representative devices.
