# unsafe-image-path-ignored: do not preview out-of-cwd image candidates

**What this covers**: inline output-image v1, the containment half — the one
card here whose value is entirely a **negative** assertion. A shell command
prints an out-of-cwd path, a `../` traversal, an external URL, a missing file,
a non-image, or an SVG; none of them may ever become a preview, and none of
them may fail, blank, or error the tool row that printed them. The boundary is
`fspaths.ResolveInRoot` (`cmd/serf-hub/internal/fspaths/paths.go:81-113`) —
lexical containment *and* symlink-resolved containment, each independently
sufficient — the same check `/doc/file` uses. Failing candidates are simply
skipped (`output_images.go:74-77`), and a notification with nothing to add is
returned untouched (`:137-139`), so the row is unaffected.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — its selector
map is the single place these hooks are maintained. The `.tool-output-images` /
`img.user-image-thumb` selectors this card used to name died with the vanilla
frontend (`660376f78`); "no thumbnail" now means no
`[data-testid="image-gallery-thumb"]`.

## Pre-state

- A freshly built hub on an isolated `$HOME` and a kernel-assigned port — see
  the Setup checklist in `docs/agentic-testing.md`. Token at
  `$HOME/.serf/auth-token` (that isolated one).
- A hermetic `$WORK` as the session's `working_dir`, and a sibling `$OUTSIDE`
  **not** under `$WORK` holding a valid `outside.png`.
- Inside `$WORK`: `notes.txt` (a real file, not an image) and `vector.svg` (a
  real SVG). No valid `.png/.jpg/.jpeg/.gif/.webp` file at all.
- A cheap model, e.g. `anthropic/claude-haiku-4-5-20251001`.
- For the browser step: `make build-web` before building the hub (rebuild
  matrix item 3 in the runbook).

## Steps

1. **Spawn (browser-free).** `POST $HUB/api/spawn` with `working_dir: $WORK`
   and a prompt that forces one shell call printing every unsafe candidate and
   creating no valid image, e.g. *"Run `echo $OUTSIDE/outside.png; echo
   ../outside.png; echo https://example.invalid/outside.png; echo missing.png;
   echo notes.txt; echo vector.svg` via exec_command, then stop."* Poll
   `GET $HUB/api/sessions/local:$SID` until `state` is `idle`.

2. **Descriptor — the exact assertion, no browser needed.** Dial
   `ws://127.0.0.1:$PORT/rpc` with `Authorization: Bearer $TOKEN`,
   `initialize`, then `thread/read{ref:"local:$SID", includeTurns:true}`. Find
   the `commandExecution` item whose `toolName` is `shell` or `exec_command`
   and read its `output`, `status`, `error`, and `outputImages`.

3. **Probe the routes directly (browser-free).** With
   `Authorization: Bearer $TOKEN`:
   - `GET $HUB/doc/image?session=$SID&path=../outside.png`
   - `GET $HUB/doc/image?session=$SID&path=$OUTSIDE/outside.png` (absolute,
     outside the cwd)
   - `GET $HUB/doc/image?session=$SID&path=vector.svg`
   - `GET $HUB/doc/image?session=$SID&path=notes.txt`
   - `GET $HUB/doc/image?session=$SID&path=missing.png`
   - `GET $HUB/s/$SID/images/notasha`
   Optionally add a symlink inside `$WORK` pointing at `$OUTSIDE/outside.png`
   and request it by its in-cwd name — the second containment layer
   (`paths.go:103-111`) is what refuses that one.

4. **Browser (qualitative).** Navigate
   `$HUB/auth?token=$TOKEN&next=/s/local:$SID`, find the shell row, **expand
   it** (see Sharp edges — a collapsed row proves nothing here, in either
   direction), and read:
   ```javascript
   ({
     port: location.port,                       // page-identity check, always
     rowPresent: !!document.querySelector('[data-testid="tool-call-item"]'),
     bodyText: document.querySelector('[data-testid="tool-call-body"]')?.textContent,
     thumbs: document.querySelectorAll('[data-testid="image-gallery-thumb"]').length,
     failedRows: document.querySelectorAll('[data-testid="tool-call-item"][data-failed="true"]').length,
     toast: document.querySelector('[aria-label="Notifications"]')?.textContent ?? "",
   })
   ```

## Expected

- **Step 2 (descriptors, exact)**: `outputImages` is **absent or empty** on
  that item, while `output` still contains every candidate string verbatim and
  `error` is empty. Each candidate is refused by a different, independently
  sufficient gate:
  - `https://example.invalid/outside.png` never becomes a candidate at all —
    anything containing `://` is dropped during the scan
    (`output_images.go:224-226`).
  - `../outside.png` and the absolute `$OUTSIDE/outside.png` fail
    `fspaths.ResolveInRoot` (`output_images.go:257`; `paths.go:99-101,109-111`).
  - `missing.png` fails the stat/read (`readOutputImageFile`,
    `output_images.go:289-299` — regular files only, and at most 8 MiB, `:28`).
  - `notes.txt` is not a candidate shape and would fail media sniffing anyway;
    `vector.svg` is neither: SVG is excluded from `supportedOutputImageMedia`
    by omission (`output_images.go:239-254`), deliberately — an inline-rendered
    SVG is script-capable.

  Falsify: **any** `outputImages` entry on this item. There is no acceptable
  descriptor here; one is a containment failure, not a cosmetic one.
- **Step 3 (routes)**: `../outside.png` → **403**; the absolute out-of-cwd path
  → **403**; a symlink escape → **403** (all three are `ErrPathEscapesRoot`,
  `doc_serve.go:104-106`). `vector.svg`, `notes.txt`, `missing.png` → **404**
  (`doc_serve.go:112-121`). `/s/$SID/images/notasha` → **400** (`sha` must be
  64 lowercase hex, `image_serve.go:20,32-35`). Falsify: any 200 body, or a 404
  where a 403 is specified — the status distinction is the boundary reporting
  *why* it refused, and collapsing it hides an escape as a typo.
- **Step 4 (browser)**: the shell row is present with its full text output,
  `thumbs` is **0**, `failedRows` is 0, and no toast fired. Falsify: any
  thumbnail at all; the row missing, blanked, or marked failed; or an error
  banner — an invalid candidate must never damage the row that printed it.
  This scenario passes by omission: the correct outcome is *no preview*, not an
  error surface.
- **Nothing client-side may re-derive a preview from the text.** `ImageGallery`
  renders only `item.outputImages` and builds no URLs
  (`ImageGallery.tsx:1-16`); there is no shell-output parser in
  `cmd/serf-hub/frontend/src`. Falsify: a thumb rendered for a path the wire
  never described.

## Cleanup

`POST $HUB/api/sessions/local:$SID/shutdown` (the old `/s/$SID/shutdown` shim
404s silently and leaves the daemon up), kill the hub by the PID you captured,
and `rm -rf` `$WORK`, `$OUTSIDE`, and your own run dir. Leave any real hub
untouched.

## Sharp edges

- **A tool row starts COLLAPSED, and the gallery only renders while it is
  expanded** (`ToolCallItem.tsx:270-286`). For this card that cuts the
  dangerous way: a collapsed row shows zero thumbs whether the boundary held or
  not, so an unexpanded read is a **false pass**. Expand before asserting zero.
  Run `shell-generated-image-path-inline.md` in the same session first if you
  want positive proof that this hub renders thumbs at all — a card that only
  ever asserts absence cannot tell "correctly refused" from "rendering broken".
- **The gallery drops any `src` the browser refuses** (`onError` →
  `markUnloadable`, `ImageGallery.tsx:73-77,140`), which is a *second* reason a
  browser-only run can read as a pass. Step 2's wire assertion is the load
  bearing one: no descriptor may exist, regardless of whether it would render.
- **`/doc/image` is LOCAL-session only** (`isLocalRouteID`,
  `doc_serve.go:132-135`) and needs a resolvable cwd; an unknown or remote
  session 404s before any path check runs, which can mask a boundary bug behind
  a plausible-looking 404. Confirm the same session id serves a *valid* in-cwd
  image 200 before trusting the 403/404s above.
- The gates are unit-tested individually —
  `TestShellOutputImageCandidatesRejectsEmbeddedURLs`,
  `TestOutputImagesForToolCallRejectsUnsafeAbsoluteOutsidePath`,
  `TestOutputImagesForToolCallOmitsMissingAndNonImageCandidates`,
  `TestSupportedOutputImageMediaAcceptsV1FormatsAndRejectsSVG`
  (`output_images_test.go:31,228,241,58`), `TestDocImageRejectsTraversalAndSVG`
  (`doc_serve_test.go:195`), and `TestDocFile_RejectsSymlinkEscape`
  (`doc_serve_test.go:246`). Those pin the boundary; this card pins that
  nothing downstream re-opens it.
