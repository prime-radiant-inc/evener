# documentannotationguard — early browser annotation journey

The early production-stack guard for the document-annotation journey. It drives
the **real** hub, SPA, IndexedDB and AppWire in real Chrome; **only the LLM
provider is scripted**. It opens the actual document from a real session file
card, selects the literal FIRST passage with real CDP mouse input, and then runs
the whole journey: the annotation comment Send, the literal saved note, the
independent clipped geometry, and the unchanged-source reopen.

## What it proves

- The production stack boots for one real session whose scripted opening turn
  calls `read_file` on the fixture `NOTES.md` and finishes through
  `communicate`.
- The real file card renders its Open-beside control; the production document
  viewer opens; `/doc/file?format=raw` returns the fixture bytes with a
  whole-file SHA256 ETag equal to the literal
  `4808d04cf747af87429a6d65d3c229bba77e2ce07a9145e520400d9fdebff0a6`.
- A genuine CDP mouse drag selects `same` at source offsets 18..22
  (`NOTES.md:1:19-1:22`). The guard pins the selection to the **specific literal
  text node and offsets** (anchor/focus nodes and 18/22 offsets), and records
  trusted `pointerdown`/`pointerup`/`mousemove` observations captured by passive
  listeners installed **before** the input; the `selectionchange` trace holds
  selected strings only and carries no trust flag.
- The guard reacquires the actual document scope AND its child scroll owner from
  the rendered literal text (never the optional `data-pane-scaffold` marker),
  and runs the SAME independent Range/actual-clip measurement used as the later
  geometry comparison — finite positive clipped fragments, 1 CSS pixel bound.
- `exact-draft` — the document-scoped comment editor takes the literal note and
  its `Send` appends the literal reference to the **original mounted session
  composer** draft, which then renders exactly `JOURNEY_APPENDED` (bounded wait,
  then exact equality). The real `composerDrafts` row text equals the literal
  `JOURNEY_APPENDED`, with exactly one `annotationPayloads` row (`sourceRange`
  FIRST, positions 1:19–1:22, selected text `same`, context before/after
  `"First occurrence: "`/`""`, revision the literal SHA256, note the literal
  note, destination `browser-origin`) and one `applied` `annotationOperations`
  row.
- `literal-note` — the saved note is reached through a genuine highlight click
  and the note surface renders the literal note, with a bounded wait before
  exact equality — not through the editor.
- `exact-highlight` — FIRST's independent DOM Range, intersected with the
  document's actual child-scroll clip, is compared to the saved bands at the
  unchanged 1 CSS pixel bound. The `highlight-geometry.json` receipt records the
  saved band's viewport rectangle and inline position, the overlay and its
  positioned containing block, the scroll clip, offsets and computed
  position/padding/border, and the independently measured expected rectangle.
- `unchanged-reopen` — reload **reacquires** the document scope on the new
  navigation, waits for a **fresh** successful `/doc/file` response, then
  compares the retained original operation/capture/note/destination and draft
  token (and applied draft revision) against fresh rows, re-runs the rendered
  draft, literal note and clipped geometry assertions, and requires no second
  append before the zero-input checks.
- Only the LLM provider is scripted; the annotation action is proven to send no
  agent input. The durable storage read refuses a missing
  `evener-mutation-outbox` database or a missing required store instead of
  reading empty arrays, waits boundedly on the committed initial `Existing
  draft` row before the baseline and on the committed exact draft/payload/applied
  receipt after Send (matching the document key to the fixture
  namespace/origin/session/path), and compares the FULL
  outbox/optimistic/recovery/sequence rows before and after. The authoritative
  server thread state is read through the real public seam before and after the
  driver, and the provider observer is joined at an explicit boundary.

The five plan-named assertions the Go test validates independently are
`finished-selection`, `exact-draft`, `exact-highlight`, `literal-note` and
`unchanged-reopen`.

### Input-observation limits

The passive listeners are installed before the composer input and the drag, but
`Input.insertText` fires no pointer events, so the composer typing is visible
only as `selectionchange` strings; `selectionchange` carries no trust flag, so
no trust is claimed for those entries. Pointer trust and the `buttons===1` drag
move are asserted from the `pointerdown`/`pointerup`/`mousemove` records.

## Accessible contract the reader exposes

The reader UI exposes this contract and does not require a new production hook
just to be testable:

1. The document pane (its real child scroll owner found by `overflow-y: auto`,
   reacquired on each navigation, not the optional `data-pane-scaffold` marker)
   contains a note editor with value semantics — `textarea`/`input` `.value` or
   contenteditable `textContent` — distinct from the session composer.
2. A `Send` control inside that same document scope.
3. Highlight bands carrying `data-document-highlight` and `data-operation-id`
   (one element per clipped fragment).
4. A note surface carrying `data-annotation-note` that a highlight interaction
   opens.

## Prerequisites

- Go toolchain, Node, and Chrome/Chromium on `PATH`.
- The production frontend built and embedded: `make build-web`.

## Run

From the repository root:

```sh
go test -tags browserguard ./cmd/evener-hub -run '^TestDocumentAnnotationBrowser(Journey|LongDocument|NoteStack)$' -count=1 -v
```

### Two distinct visibility roots

The guard measures containment against TWO different roots on purpose:

- **Pane root** (`__annotationDocumentScope`, the scroll owner's parent): the
  visible pane that legally contains the scaffold footer. Footer controls — the
  note editor, `Send`, `Cancel` — are checked against this root.
- **Scroll-body root** (`__annotationDocumentScroll`): the overflow-clipped body
  that actually clips the document content. The revealed note stack lives inside
  it and is checked against this smaller root, so a note rectangle that extends
  into the footer is *not* accepted as visible.

The pure containment rules live in `assertions.mjs`; the nested Vitest suite
`assertions.test.mjs` exercises them directly (pane-inside/body-outside rejected,
ordinary contained accepted, empty rectangles rejected, the 1 CSS pixel tolerance
honored without broadening). Run it through the package script:

```sh
cd cmd/evener-hub/frontend && npm test -- scripts/documentannotationguard/assertions.test.mjs
```

`TestDocumentAnnotationBrowserJourney` is the short-document sentinel above.
`TestDocumentAnnotationBrowserLongDocument` passes `"scenario": "long"` in the
driver's fixture input (test-only) so the same driver runs the same real stack
against the overflowing `LONG_SOURCE` fixture in `fixtures.mjs`:

- `finished-selection` — a genuine CDP drag selects the early `same` at
  1:31-1:34.
- `controls-visible` — observed BEFORE any scrolling/focusing helper: the note
  editor and the Send and Cancel controls are each fully inside the visible
  PANE root (the footer is legal there), the finished selection did not focus
  the editor, and the reading position moved at most 1 CSS pixel. A normal-flow
  box after the whole document reports all three controls outside the pane.
- `exact-draft` — the same real save/literal-draft/storage oracles as the short
  journey, against the long fixture's own literal SHA256 and reference.
- `note-visible` — the note is reached through a genuine highlight click, and
  the revealed `[data-annotation-note]` rectangle is fully inside the actual
  SCROLL-BODY clip without the reveal jumping the reading position to the
  document bottom.
- `exact-highlight` — the unchanged independent clipped-band oracle.

Each test independently validates its own assertion names. The long journey adds
`controls-visibility.json` and `note-visibility.json` receipts (clip, element
rectangles, active element, reading position) beside the shared artifacts.
A missing control element before the drag is a **setup** failure, not an
assertion failure.

`TestDocumentAnnotationBrowserNoteStack` passes `"scenario": "notestack"` and
runs the same real stack against the same overflowing fixture, but saves three
overlapping notes through real finished selections, the editor and `Send`, then
reveals the stack through a real highlight click and checks the required
long-note behavior on the unchanged source:

- `stack-bounded` — the complete revealed stack is inside the actual scroll-body
  clip; a long multiline note or enough overlaps otherwise run past the body.
- `unbroken-wrapped` — an unbroken token wraps with no hidden horizontal
  remainder (`scrollWidth` within the client width).
- `stack-reachable` — across bounded, settled genuine wheel steps, each note's
  OWN beginning text and end marker must become visible, measured as DOM Range
  fragments inside the intersection of the note stack, the actual scroll-body
  clip and the browser viewport (`markerVisible`). A tall note's beginning and
  end appear at different internal scroll offsets, and the whole note is never
  required to fit at once. A non-scrollable or stuck stack trips a bounded
  failure guard instead of looping. The document reading position must stay
  within 1 CSS pixel before and after the reveal and at each step.

Creating the overlapping stack surfaced a real setup problem: the preserved
diagnostic milestones show a planned drag endpoint inside a saved band and a
later Selection focus of BUTTON, while the passive input receipt stops at the
earlier drag. That supports overlay interference as the cause but does not by
itself prove the second release target or the whole root cause. The scenario
picks endpoints outside every earlier band; selecting text underneath a saved
band remains an unqualified Task6 selection/copy obligation, not resolved here.

## Expected result

The journey passes all five assertions: `result.json` shows them all `true`,
`failure` `null`, `blocked` empty, `forbiddenMutationCalls` `0` and
`consoleErrors` `0`. A compile error, a missing element before the drag, or a
timeout before selection is a **setup** failure, not an assertion failure.

## Artifacts

Written into a kept temp directory path printed as
`annotation browser evidence: …`:

- Success receipts: `result.json` (assertion inventory, failure, blocked,
  forbidden-mutation and console-error counts), `source-identity.json` (base
  commit, worktree dirtiness, fixture identity, embedded frontend hash),
  `doc-reads.json`, `rpc-frames.json`, `console.json`, `input-observations.json`,
  `geometry-preflight.json`, `highlight-geometry.json`, `storage-baseline.json`,
  `storage-final.json`, `server-baseline.json`, `server-final.json`, and
  `driver.log` (the driver's stdout/stderr).
- Failure-only: `failure.png` and `failure.html` are captured only when the
  journey throws; a passing run writes neither.

Set `ANNOTATIONGUARD_KEEP_ARTIFACTS=1` to keep the artifact directory on a pass.

## Cleanup

The driver owns and stops its own Chrome/profile; the hub and daemon are reaped
by `startHubStack` and `thread/shutdown`. On a clean pass the artifact directory
is removed (unless `ANNOTATIONGUARD_KEEP_ARTIFACTS=1` is set); on failure it is
kept.

## Scope and limits

This early desktop journey does **not** qualify the broader fault, multi-tab or
responsive matrix, and it does **not** prove genuine physical-phone selection or
an OS on-screen keyboard; those remain later work. It is not a full feature
release claim. Native is deferred.

## Layout

- `run.mjs` — the journey driver.
- `fixtures.mjs` — literal-only fixtures (`SOURCE`, `FIRST`, the literal SHA256,
  `JOURNEY_REFERENCE`/`JOURNEY_APPENDED`, `NOTE`). Nothing here derives from
  production mapper/renderer/context/formatter code.
- `assertions.mjs` — the pure containment rules for the two visibility roots.
- `assertions.test.mjs` — the Vitest regression for those rules (nested scripts
  suite, collected by the package's Vitest runner).
- `README.md` — this document.
