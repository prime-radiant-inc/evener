# output-image-lightbox-and-pane: open previews in lightbox and side pane

**What this covers**: inline output-image v1, the two "look closer" surfaces.
(1) An output-image thumbnail opens the shared lightbox on click — the same
`Dialog` widget the rest of the app uses, with prev/next once a row carries
more than one image. (2) A *file-referencing* tool row offers "open beside",
which routes the file into the read-only doc pane; an image-extension path
lands in the pane's image view rather than its text view. If `ImageGallery`'s
lightbox wiring, `FileOpenBesideButton`'s cwd gate, or `DocPane`'s file/image
branch regresses, this catches it.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — its selector
map is the single place these hooks are maintained. `#image-lightbox`,
`.open-beside-btn`, `.tool-output-images .user-image-card` and
`window.SerfPanes` all died with the vanilla frontend (`660376f78`): there is
no pane handle on `window`, and the opener is `paneActions.openBeside`
(`shell/paneActions.ts:44-46`).

## Pre-state

- A session that already renders an output-image thumbnail on a
  **file-referencing** tool row: run `read-image-tool-result-inline.md` or
  `written-image-inline-after-reload.md` first and reuse its session, hub, and
  isolated `$HOME` (see the Setup checklist in `docs/agentic-testing.md`).
- **Not** `shell-generated-image-path-inline.md`. Its row structurally cannot
  produce an open-beside control: `openBesidePath` is defined only on
  `read_file` (`tools/fsTools.tsx:64-67`) and `edit_file`/`write_file`
  (`tools/editTools.tsx:78,96`), never on `shell`/`exec_command`, and
  `apply_patch` opts out explicitly (`editTools.tsx:99`).
- The full workspace shell (`/s/local:<SID>`), not a chrome-stripped route —
  `openBeside` needs the dockview host.
- `make build-web` before building the hub (rebuild matrix item 3 in the
  runbook). This card is browser-only; there is nothing here to check over REST.

## Steps

1. **Open the row.** Navigate `$HUB/auth?token=$TOKEN&next=/s/local:$SID`,
   locate the tool row
   (`[data-testid="tool-call-item"][data-tool-name="read_file"]`, or
   `write_file`), and expand it — the gallery only exists while the row is
   expanded (see Sharp edges).

2. **Lightbox.** Click `[data-testid="image-gallery-thumb"]`, then read:
   ```javascript
   ({
     port: location.port,                       // page-identity check, always
     path: location.pathname,                   // still /s/local:<SID>
     dialogs: document.querySelectorAll('[role="dialog"][aria-modal="true"]').length,
     lightboxSrc: document.querySelector('[data-testid="image-gallery-lightbox-img"]')
                    ?.getAttribute("src"),
     lightboxCaption: document.querySelector('[data-testid="image-gallery-lightbox-caption"]')
                        ?.textContent,
     nav: !!document.querySelector('[data-testid="image-gallery-next"]'),
   })
   ```
   Close with Escape and re-read.

3. **Open beside.** With the lightbox closed, find the control in the row's
   own summary line — it is an icon button whose accessible name is
   `Open beside: <cwd-relative path>` (`fileOpenBeside.tsx:114`, rendered
   through `IconButton`'s `aria-label`). Click it and read:
   ```javascript
   ({
     port: location.port,
     path: location.pathname,                   // MUST still be /s/local:<SID>
     docImage: !!document.querySelector('[data-testid="doc-image"]'),
     zoomButton: !!document.querySelector('[aria-label="Zoom image"]'),
     paneTitles: Array.from(document.querySelectorAll('[data-testid="pane-title-desktop"]'),
                            (n) => n.textContent),
     dialogs: document.querySelectorAll('[role="dialog"][aria-modal="true"]').length,
     rowStillOpen: !!document.querySelector('[data-testid="tool-call-body"]'),
   })
   ```

4. **Pane lightbox.** Click the `[aria-label="Zoom image"]` button in the doc
   pane and check for `[data-testid="doc-lightbox-img"]`.

## Expected

- **Step 2 (lightbox)**: exactly one `[role="dialog"][aria-modal="true"]`
  appears, containing `[data-testid="image-gallery-lightbox-img"]` whose `src`
  equals the thumbnail's `src` (the component renders one resolved `src` and
  builds no URLs, `ImageGallery.tsx:1-16,153-158`). Its caption, when present,
  is the same `name ?? path ?? source` the thumb showed (`captionFor`,
  `:56-58`). `nav` is present only when the row carried more than one image
  (`total > 1`, `:164`); when it is, Previous/Next and the Left/Right arrow
  keys step the same index and wrap at the ends (`:84-89,100-108`). Escape
  closes it and returns `dialogs` to 0 (`OverlayPanel.tsx:77`); so does the
  `[aria-label="Close"]` button. Falsify: no dialog; two dialogs; a `src`
  differing from the thumbnail's; or Escape leaving it open.
- **Step 3 (open beside)**: `location.pathname` is unchanged — this opens a
  pane, it never navigates. `docImage` and `zoomButton` are true; a pane title
  equal to the image's filename appears (`DocPane.tsx:133`, `PaneScaffold`'s
  `pane-title-desktop`); `dialogs` is 0 (open-beside is not the lightbox); and
  `rowStillOpen` is true — the button calls `stopPropagation` so opening a file
  never also toggles the row it lives in (`fileOpenBeside.tsx:104-108`). The
  pane also offers a `Back to <parent>` action (`backToParentAction.tsx:36,44`).
  Falsify: the main pane navigates away; clicking it opens the lightbox
  instead; the row collapses; or the doc pane renders the *file* view
  (`<pre>`/markdown) for a `.png` — `kind` is decided by extension,
  `IMAGE_EXT_RE` at `fileOpenBeside.tsx:80`, and only png/jpe?g/gif/webp are
  images.
- **Step 4 (pane lightbox)**: `[data-testid="doc-lightbox-img"]` appears inside
  its own `Dialog` titled with the filename (`DocPane.tsx:111-115`). Falsify:
  clicking the pane image does nothing.
- **Absence is meaningful only where the control is possible.** No open-beside
  control on a `shell` row is correct, not a regression (see Pre-state). No
  control on a `read_file`/`write_file` row whose path lies **outside** the
  session cwd is also correct — `cwdRelative` returns undefined and
  `FileOpenBesideButton` renders `null` (`fileOpenBeside.tsx:63-72,102`).
  Falsify: an open-beside control offered for an out-of-cwd path.

## Cleanup

Close the lightbox and the doc pane. If this card created its own session,
`POST $HUB/api/sessions/local:$SID/shutdown` (the old `/s/$SID/shutdown` shim
404s silently and leaves the daemon up), then kill the hub by the PID you
captured and remove your own run dir. Leave any real hub untouched.

## Sharp edges

- **The thumbnail lives inside the expanded row, the open-beside button does
  not.** The gallery renders only while the row is expanded
  (`ToolCallItem.tsx:270-286`); the open-beside control rides the row's summary
  via `trailing` (`:228,262`) and is therefore visible while collapsed. Two
  controls, two different visibility rules — do not conclude either is missing
  from a single read of a collapsed row.
- **The control is a row-level, file-level affordance, not an image-level
  one.** It comes from the tool descriptor's `file_path` argument, not from the
  image descriptor, so it appears exactly once per row regardless of how many
  thumbnails that row carries — and it can be present with no image at all
  (a `read_file` on a text file).
- **`cwd` has to be hydrated first.** `FileOpenBesideButton` reads the session's
  cwd out of the threads store and renders nothing until it is there
  (`fileOpenBeside.tsx:100-102`). A read taken before the AppWire socket
  hydrates looks identical to the out-of-cwd refusal.
- **The doc pane fetches its own bytes** from `/doc/image` (`docContent.ts:97-99`)
  and shows an "Image not available" empty state if that fetch fails
  (`DocPane.tsx:97-104`) — a visible, distinguishable outcome, unlike the
  transcript gallery, which silently drops an unloadable `src` (`onError` →
  `markUnloadable`, `ImageGallery.tsx:73-77,140`).
- **Address the row by its accessible name, not by CSS class.** Class names are
  hashed CSS-module output; `Open beside: <path>`, `Zoom image`, `Close`, and
  `Previous`/`Next` are the stable handles.
