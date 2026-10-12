# documentannotationguard — browser annotation checks

Two browser checks for document annotations, both registered in the standard
browser gate (`make test-web-browser`):

- **One full-stack journey** (`run.mjs`, driven by the Go test
  `TestDocumentAnnotationBrowserJourney`) drives the **real** hub, SPA,
  IndexedDB and AppWire in real Chrome; **only the LLM provider is scripted**.
- **Small production-component cases** (`components.mjs`) mount the real
  scaffold, markdown renderer, source binding, saved-highlight overlay and
  comment controls through Vite and drive them with genuine CDP input.

## The full-stack journey

It opens the actual document from a real session file card, selects the literal
FIRST passage with real CDP mouse input, and then runs the whole journey: the
annotation comment Send, the literal saved note, the independent clipped
geometry, and the unchanged-source reopen.

### What it proves

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
  append before the zero-input checks. The whole `composerDrafts` store is
  compared, so every unrelated draft is preserved by value, and the target draft
  row must route to the original session ref.
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

The five assertion names the Go test validates independently are
`finished-selection`, `exact-draft`, `exact-highlight`, `literal-note` and
`unchanged-reopen`.

### Input-observation limits

The passive listeners are installed before the composer input and the drag, but
`Input.insertText` fires no pointer events, so the composer typing is visible
only as `selectionchange` strings; `selectionchange` carries no trust flag, so
no trust is claimed for those entries. Pointer trust and the `buttons===1` drag
move are asserted from the `pointerdown`/`pointerup`/`mousemove` records.

## The production-component cases

`components.mjs` mounts the production document-annotation entry
(`src/dev/documentannotationguard-entry.tsx`) in a real pane and drives these
cases with genuine input. All seven are expected-pass:

- `pane-resize` — a pane-only width change (no browser viewport resize) reflows
  the text; the saved highlight's painted extent must follow the browser's own
  text ranges within a one-pixel tolerance, and the selected glyphs must stay
  visible.
- `nested-scroll` — a genuine wheel over a pending reference nested in a long
  scrollable note stack scrolls that reference, leaves the outer stack and
  document unmoved, and is not cancelled; the outer stack also reaches its
  independently computed maximum through genuine wheel input.
- `selection` — genuine forward and reverse drags across a saved band.
- `selection-shift` — genuine Shift+Arrow adjustment of the selection.
- `copy` — genuine Ctrl+C read back at the browser clipboard boundary.
- `overlap-access` — hover, keyboard and emulated-tap access, each from a fresh
  unrevealed state, with before/after focus and reading-position checks. Touch
  is explicitly emulation, not physical-phone behavior.
- `comment-controls` — the note editor and Send sit inside the real footer,
  pane and viewport clips, and a long saved note really wraps and stays
  reachable inside the stack's client clip.

The harness serves its page through Vite's `/@fs/` route rather than its bare
path: the dev server proxies every URL whose prefix is `/doc` to a running
hub, and the authorized guard page is named `documentannotationguard.html`, so
the bare URL would be answered by the hub with 401. `/@fs/` serves the same file
from the allowed frontend root with no proxy prefix.

## The component page's accessible contract

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
- The full-stack journey needs the production frontend built and embedded:
  `make build-web`. The component cases mount components from source through
  Vite and do not need the dist.

## Run

The full-stack journey, from the repository root:

```sh
go test -tags browserguard ./cmd/evener-hub -run '^TestDocumentAnnotationBrowserJourney$' -count=1 -v
```

The component cases, from `cmd/evener-hub/frontend`:

```sh
npm run documentannotationguard
```

Both are part of `make test-web-browser`. The gate runs real browsers under
load-aware slots.

### Two distinct visibility roots

The journey measures containment against TWO different roots on purpose:

- **Pane root** (`__annotationDocumentScope`, the scroll owner's parent): the
  visible pane that legally contains the scaffold footer. Footer controls — the
  note editor, `Send`, `Cancel` — are checked against this root.
- **Scroll-body root** (`__annotationDocumentScroll`): the overflow-clipped body
  that actually clips the document content. The revealed note stack lives inside
  it and is checked against this smaller root, so a note rectangle that extends
  into the footer is *not* accepted as visible.

The pure containment rules live in `assertions.mjs`; the regression
`assertions.test.mjs` exercises them directly (pane-inside/body-outside rejected,
ordinary contained accepted, empty rectangles rejected, the 1 CSS pixel
tolerance honored without broadening). It runs under both the package's Vitest
runner and `node --test`:

```sh
node --test scripts/documentannotationguard/assertions.test.mjs
```

## Expected result

The full-stack journey passes its five assertions; `result.json` shows each one
`true`, `failure` `null`, `blocked` empty, `forbiddenMutationCalls` `0` and
`consoleErrors` `0`. The component cases report seven PASS verdicts and exit
zero. A compile error, a missing element before the drag, or a timeout before
selection is a **setup** failure, not an assertion failure.

## Artifacts

The full-stack journey writes into a kept temp directory path printed as
`annotation browser evidence: …`:

- Success receipts: `result.json` (assertion inventory, failure, blocked,
  forbidden-mutation and console-error counts), `source-identity.json` (base
  commit, worktree dirtiness, fixture identity, embedded frontend hash),
  `doc-reads.json`, `rpc-frames.json`, `console.json`, `input-observations.json`,
  `geometry-preflight.json`, `highlight-geometry.json`, `storage-baseline.json`,
  `drafts-baseline.json`, `drafts-final.json`, `storage-final.json`,
  `server-baseline.json`, `server-final.json`, and `driver.log` (the driver's
  stdout/stderr).
- Failure-only: `failure.png` and `failure.html` are captured only when the
  journey throws; a passing run writes neither.

Set `ANNOTATIONGUARD_KEEP_ARTIFACTS=1` to keep the artifact directory on a pass.

The component cases write their receipts to a temporary directory (created with
`mkdtempSync`), never into the repository or `.superpowers/`. Set
`DOCUMENTANNOTATIONGUARD_EVIDENCE_DIR` to keep a run's receipts somewhere
specific.

## Cleanup

The journey driver owns and stops its own Chrome/profile; the hub and daemon are
reaped by `startHubStack` and `thread/shutdown`. On a clean pass the artifact
directory is removed (unless `ANNOTATIONGUARD_KEEP_ARTIFACTS=1` is set); on
failure it is kept. The component harness owns and stops its own Chrome, profile
and Vite dev server.

## Scope and limits

These desktop checks do **not** qualify the broader fault, multi-tab or
responsive matrix, and they do **not** prove genuine physical-phone selection or
an OS on-screen keyboard; those remain later work. The component touch case is
emulation. This is not a full feature release claim. Native is deferred.

## Layout

- `run.mjs` — the full-stack journey driver.
- `components.mjs` — the production-component cases.
- `component-fixtures.mjs` — literal fixtures for the component cases.
- `fixtures.mjs` — literal-only fixtures for the journey (`SOURCE`, `FIRST`, the
  literal SHA256, `JOURNEY_REFERENCE`/`JOURNEY_APPENDED`, `NOTE`). Nothing here
  derives from production mapper/renderer/context/formatter code.
- `assertions.mjs` — the pure containment rules for the two visibility roots.
- `assertions.test.mjs` — the regression for those rules.
- `README.md` — this document.
