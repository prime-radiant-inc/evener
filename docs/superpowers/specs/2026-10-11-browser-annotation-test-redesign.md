# Browser annotation rendering and test redesign

Date: 2026-10-11

Jesse approved this direction with “do it” after the in-channel recommendation:
retain text anchors and persistence, replace excess annotation-test machinery,
and try browser-native highlight painting. This document records that direction;
it does not claim that the implementation or the experiment has passed review.

## Authority and scope

This is a bounded amendment to the existing browser delivery, not a restart.
Keep the [browser behavior](2026-10-05-markdown-document-annotations-browser-design.md)
and [session-revision contract](2026-10-06-browser-annotation-draft-revisions-design.md).
It replaces their mandatory physical-phone release gate and the old annotation
guard's prescribed implementation, not their user-facing guarantees.

Physical-phone evidence is a follow-up, not a qualification, merge or deployment
blocker. Emulated touch must still be identified as emulation. Native-app work,
its unmerged lane and its retained evidence remain deferred and untouched.

Do not change source coordinates, reference formatting, persistence schemas,
draft authority, expiry, operation receipts, recipient routing or ordinary input.
Keep Marked 18.0.6 and the existing Chrome automation utilities. No new test
framework, compatibility subsystem, server API or browser-version restriction
is authorized by this amendment.

## Rendering

Annotations remain anchored by document identity/revision and source ranges,
with selected text and context. The existing mapper supplies DOM ranges.

Try CSS Custom Highlight painting using those ranges without changing text
nodes. Adoption requires evidence for exact highlighting through pane resizing,
selection and Copy, overlap, and retained hover, focus and tap access. Do not
depend on `highlightsFromPoint` being universally available. Browser-native
painting alone does not provide accessible interaction controls.

Use ordinary scrollable note controls where possible, using the existing pane
layout rather than pointer-transparent note overlays and forwarded wheel events.
Keep the document's reading position, visible comment controls, literal notes,
copyable pending references, full note reachability and every overlapping note.
Revealing a note must not steal focus or interfere with selection adjustment.

If the experiment cannot preserve those guarantees without substantial new
machinery or a new support policy, stop that experiment and report the concrete
limitation. Continue the independent test redesign and fix the confirmed pane
resize and nested scrolling defects in the existing implementation. Do not turn
the rendering experiment into a prerequisite for retiring the old test harness.

## Test responsibilities

| Layer | Owns |
| --- | --- |
| Existing unit/integration tests | Source mapping, literal references, recipient/source lifetime, ordered once-only insertion, draft revisions, expiry, storage recovery and preservation |
| One real full-stack browser journey | Actual selection and comment/save, exact original-session draft, saved-note access, unchanged reopen, native IndexedDB and zero input mutations |
| Small real-browser component cases | Selection, Copy, overlap/accessibility, pane resizing, visible controls, reading position, wrapped/reachable notes and nested scrolling |

The full-stack journey retains the actual hub/daemon/provider-boundary observer
and authoritative before/after input state. Client DOM assertions cannot replace
that proof. Keep meaningful browser-storage/reload coverage; fake-indexeddb unit
tests do not establish native browser storage behavior.

Component cases mount production components and CSS with explicit fixture data.
They use genuine browser mouse, keyboard and emulated-touch input. Do not replace
selection with injected Selection objects, dispatch synthetic input events, mock
product logic or reproduce the reader in hand-authored HTML.

Expected new text and records come independently from fixture inputs and the
contract. Observed baselines may establish preservation of existing unrelated
work. Geometry checks use actual client clips and browser ranges, preserving the
existing one-pixel tolerance where pixel geometry is the contract. Native-paint
qualification must also observe painting, not only registry bookkeeping.

Await real completion where available, otherwise bounded observable conditions.
Do not simulate a browser scheduler, count arbitrary paints, widen timeouts,
force clicks or scroll a control into view before testing its visibility.

## Execution and acceptance

Refine the existing approved browser plan's qualification work in these units:

1. Establish the small production-component browser cases and their independent
   expectations. Demonstrate the pane-resize and nested-scroll defects against
   the current product. Preserve working selection/access cases. Keep this
   checkpoint explicitly red for those product defects; do not repair obsolete
   harness tests as an admission requirement.
2. Implement the bounded rendering experiment and the simplest supported fixes,
   test-first. Reuse the same defect tests for green evidence. Retain ordinary
   selection, focus, Copy, overlap and reading-position behavior.
3. Retain and simplify the full-stack journey, then retire each obsolete check
   after naming where its unique guarantee is covered. Remove source-extraction
   tests, caller-loop simulations, diagnostic-order/classification matrices and
   duplicate storage checks that serve only the retired harness. Register the
   resulting browser checks in the standard browser gate and update the owning
   annotation/test documentation and subsystem responsibilities.

Each unit has scoped implementation review. Preserve TDD, independent final
review, simplification, matching-head CI/RoboRev and verified merge requirements.
Stop a twice-rejected review loop rather than recreating the old repair process.

Run targeted checks as each unit lands, then `make test-api-package`,
`make test-web`, `make test-web-browser`, and an agentic whole-application browser
journey. Inspect commands and actual outputs. Report product failures separately
from test-tool failures. Keep only one browser/build workload active on the
currently loaded host; use CI for the long full-suite work.

Completion requires preserved product guarantees, passing replacement checks,
and removal of redundant old machinery. Merely adding a second harness beside
the first does not complete this redesign.
