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

## Delegate navigation and reconnect follow-up

Using the same isolated real hub, the scripted provider issued the current
`delegate` tool with a leaf assignment. It created distinct children for each
native parent, and each child reported NATIVE-DELEGATE-CHILD-READY through
`communicate`. Android child: `local:034K2olsBMMCemqCadASi0`; iOS child:
`local:034K2p09OvyVcaii5Foii8`.

On both platforms, Work → Activity → inactive fold → delegate detail → Open
session reached the child's own transcript and displayed its report marker
with the expected hub name. iOS toolbar Back and Android system Back returned
to the respective parent. The web reference opens delegates in a read-only
transcript pane; these children advertise no composition capabilities.

This check exposed an editable Message field and attachment picker on a
session that could not send. Native now omits composition fields, attachment
controls and composer settings when all send/steer/queue/goal capabilities are
false. Reading/navigation controls and pending decisions remain available.
Stored drafts are untouched, and an unknown/loading conversation is not
classified as read-only. Both new Release builds succeeded, and both apps
were installed and restarted into the child: its report remains readable and
Message/Attach images/model controls are absent. Native 158 tests, TypeScript,
and touched-file Biome pass. This presentation change was checked manually;
no new automated UI-rendering coverage is claimed.

For reconnect acceptance, both parent Activity panels were expanded before
restarting the owned port-9199 WebSocket proxy with a controlled jobs/list
failure. Both panels retained their rows and fold state, displayed the exact
refresh error plus the stale-data notice, and recovered on Refresh activity
after the failure was removed. This establishes native activity-tree
retention and recovery; output-window reconnect/failure acceptance and
cross-hub faults are still open. Nested delegate branch paging, full metadata,
styled output, accessibility and performance acceptance remain outstanding.

## Continuation prefix repair — 7 September

The server's continuation cursor skips entries already returned. The shared
native/web merge treated the target session as a replacement, dropping its
loaded prefix. It also skipped child expansion when the target delegate's
projection revision was unchanged. The native test previously repeated the
prefix in its continuation fixture and therefore missed the loss.

Continuation now uses the existing ordered ID merge throughout the returned
branch: retain earlier entries, update overlaps, append new entries, and merge
children independently of the delegate metadata revision fence. Both callers
use this path only for continuations; ordinary root refresh continues to
replace missing entries. Root summary retention, advertised-cursor checks,
invalidation handling and ownership guards remain in effect.

The corrected native suffix fixture failed before the fix, as did four new
shared merge regressions for overlap, nested prefixes, equal-revision expansion
and newer delegate metadata. The focused frontend suites pass 22 tests, and
the full native suite passes 585 tests plus TypeScript. The canonical frontend
gate passes typecheck, tests and lint after formatting. Current-source real v4
device branch paging and the broader activity acceptance matrix remain open;
the installed iOS bundle still corresponds to the goal-qualified source.

## Direct v4 iOS paging and output — 7 September

Implementation source `cb0dca5cc` is installed as a Release app on iPhone 17 Pro /
iOS 26.5, bundle SHA
`22974d41d3b24501cb710c4f4da74eced60f7caaec5d0a769e7c709716e677ff`.
The direct owned hub at port 54211 runs `0f95ef200`. The independent SDK consumer
packs `7b48a7314`, tarball SHA
`6d5f65703f30ea7da2be5a610ef0894f2b4e008902bf4404e167d861e5b96046`.
The [receipt](assets/activity-output-v4-receipt.json) records artifact paths,
fixture Git provenance, exact session identities, structured page counts, UI
snapshots and retained draft hashes.

These are deliberately large **persisted read fixtures**. Newly minted sessions
and journals live only under the existing disposable hub's XDG state. The real
hub indexes, folds, projects and serves the data; the actual native controllers,
iOS app and packed SDK read it without a proxy. Seeded job/delegate records do
not prove shell execution or delegate processes. Header-only transcripts let
the native app open these saved sessions. No model/provider was called.

### Repairs exposed by the direct fixture

The 2000-work-unit limit produces suffix pages. A 2505-job root and 2505-job
child retained all IDs after the earlier prefix repair, but the merged root
still reported the first page's 2000 completed entries and incomplete coverage.
The child's last page reported only its 1011-entry suffix. Continuation merging
now derives counts and aggregate state from all retained entries and merged
children. Coverage completion means all branches are loaded; it does not mean
every job is terminal. Ordinary root refresh still replaces omitted entries.

A second fixture adds another child with one job. Loading the large child first
also exposed an empty ancestor branch object erasing the root's outstanding
cursor. Grafting now receives the requested node ID, replaces branch state within
that target, and preserves other pending branches. Removed targets are ignored.
These regressions failed before their repairs.

The output decoder now rejects unsafe, negative, fractional or inverted byte
offsets and malformed paging flags. Its required `truncated` field is validated;
omitted `hasEarlier` remains the current wire's false value. Native, web and the
new single-page SDK recipe share this decoder. A rejected page retains native
content and retries from the same server offset.

### Observed native journey

The second fixture reached these states on the actual iOS screen:

| Step | Root jobs | Large child jobs | Other child jobs | Remaining pages |
| --- | ---: | ---: | ---: | --- |
| Initial read | 2000 | 0 | 0 | Root |
| Load root | 2505 | 1494 | 0 | Root and child |
| Load child first | 2505 | 2505 | 0 | Root |
| Load root again | 2505 | 2505 | 1 | None |

The native-controller harness separately asserted exact ordered IDs with no
duplicates, final counts of 5011 completed jobs plus two nonterminal delegate
descriptors, foreign-ref cursor rejection, stale-cursor no-ops, and fresh-root
replacement back to the first 2000 entries.

![Two pending activity branches](assets/activity-v4-pending.jpg)
![All activity branches loaded](assets/activity-v4-complete.jpg)

iOS opened the large child's first job through its owning session. Load earlier
moved the displayed byte start from 235950 to 231855 for a 240046-byte Unicode
log; Refresh returned to 235950. Back retained the expanded job and loaded
activity. The controller harness reassembled the complete log exactly. The
separate packed SDK used four 64-KiB reads with server cursors, obtained the same
SHA-256, accepted empty output and a one-byte request at a multibyte boundary,
and rejected a child job requested through the root owner. Numeric output
offsets do not contain ownership; clients must retain the ref/job binding.

![Earlier Unicode output on iOS](assets/activity-v4-output.jpg)

### Verification and limits

593 native tests plus TypeScript, 131 SDK contracts across thirteen files,
isolated package qualification, the canonical web gate and all five browser
guards pass. Eighteen decoder regressions failed before validation was added.
Luna medium supplied implementation proposals and independent reviews; the
coordinator integrated and executed the checks. All six existing drafts and
the unrelated Apple project/plist patch remain byte-identical.

The automation tool's refreshed snapshot exceeded its 2500-ms settling deadline
when opening output and loading earlier; both actions succeeded and subsequent
snapshots verified the result. This is not performance qualification. Native
output faults/reconnect, cross-hub ownership races, real ongoing/delegated
execution on this build, complete metadata, styled ANSI, copy/selection,
reading-position behavior, accessibility, large text and device/performance
coverage remain open. SDK activity-tree traversal still lacks a packaged
workflow. This checkpoint qualifies these iOS reads, not v1 as a whole.
