# shell-generated-image-path-inline: preview a shell-created image path

**What this covers**: inline output-image v1, the shell-inference half. A
shell command writes an image under the session cwd and prints its path; the
**server** — never the frontend — scans that output text for image-looking
paths, validates each against the cwd boundary and the four supported media
types, and only then mints a descriptor the browser renders. The scan is
`shellOutputImageCandidates` (`cmd/serf-hub/output_images.go#shellOutputImageCandidates`), reached
only for `shell` / `exec_command` (`:66-69`); the frontend has no shell-output
parser at all — `ImageGallery` renders `item.outputImages` and constructs no
URLs of its own (`ImageGallery.tsx:1-16`). If path inference leaks to the
client, or the server stops inferring, this catches it.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — its selector
map is the single place these hooks are maintained. The `.tool-output-images
.user-image-card img.user-image-thumb` selectors this card used to name died
with the vanilla frontend (`660376f78`).

## Pre-state

- A freshly built hub on an isolated `$HOME` and a kernel-assigned port — see
  the Setup checklist in `docs/agentic-testing.md`. Token at
  `$HOME/.serf/auth-token` (that isolated one).
- A hermetic `$WORK` as the session's `working_dir`, containing a small valid
  PNG the shell command can copy.
- A cheap model, e.g. `anthropic/claude-haiku-4-5-20251001`.
- For the browser step: `make build-web` before building the hub (rebuild
  matrix item 3 in the runbook).

## Steps

1. **Spawn (browser-free).** `POST $HUB/api/spawn` with `working_dir: $WORK`
   and a prompt that forces one shell call which creates the image and prints a
   plain relative path, e.g. *"Run `cp fixture.png plot.png && echo created
   plot.png` via exec_command, then stop."* Poll
   `GET $HUB/api/sessions/local:$SID` until `state` is `idle`.

2. **Confirm the file (browser-free).** `$WORK/plot.png` exists, is a regular
   file, and sniffs as PNG / JPEG / GIF / WebP (`supportedOutputImageMedia`,
   `output_images.go:239-254`).

3. **Descriptor — the exact assertions, no browser needed.** Dial
   `ws://127.0.0.1:$PORT/rpc` with `Authorization: Bearer $TOKEN`,
   `initialize`, then `thread/read{ref:"local:$SID", includeTurns:true}`. Find
   the `commandExecution` item whose `toolName` is `shell` or `exec_command`
   (by name, never by turn index) and read both its `output` and its
   `outputImages`.

4. **Fetch the bytes (browser-free).** `curl` the descriptor's `url` with
   `Authorization: Bearer $TOKEN`.

5. **Browser (qualitative).** Navigate
   `$HUB/auth?token=$TOKEN&next=/s/local:$SID`, find the shell row
   (`[data-testid="tool-call-item"][data-tool-name="exec_command"]` — use the
   `toolName` step 3 actually reported), **expand it** (see Sharp edges), and
   read:
   ```javascript
   ({
     port: location.port,                       // page-identity check, always
     bodyText: document.querySelector('[data-testid="tool-call-body"]')?.textContent,
     thumbSrcs: Array.from(
       document.querySelectorAll('[data-testid="image-gallery-thumb"] img'),
       (i) => i.getAttribute("src")),
     caption: document.querySelector('[data-testid="image-gallery-caption"]')?.textContent,
   })
   ```

## Expected

- **Step 3 (descriptor, exact)**: the item's `output` still contains the
  literal text `plot.png` — inference adds a descriptor, it never rewrites or
  replaces the tool's own output — **and** `outputImages` holds one entry with
  `source: "shell-path"` (`output_images.go:66-69`), `path: "plot.png"`, a
  `mediaType` starting `image/`, a 64-hex `sha`, and a same-origin relative
  `url` of the form `/doc/image?session=<escaped session>&path=plot.png`
  (`output_images.go:283`). Falsify: `outputImages` is empty while the file
  exists and the path is printed (inference regressed); the entry's `source` is
  anything other than `shell-path`; or the `url` contains `://`.
- **Step 4 (bytes)**: 200 with an image `Content-Type` — `/doc/image` re-reads
  the file inside the cwd boundary (`doc_serve.go:84-127`). This route works
  for a *live* session with no past index, off the roster's working dir
  (`localSessionCWD`, `doc_serve.go#localSessionCWD`;
  `TestDocImageServesLiveDescriptorURLWithoutPast`, `doc_serve_test.go#TestDocImageServesLiveDescriptorURLWithoutPast`).
- **Step 5 (browser)**: the expanded row shows both the original shell output
  text and one thumbnail whose `src` is exactly the `url` from step 3 — the
  component does no URL construction (`ImageGallery.tsx:1-16`), so a `src` that
  differs from the wire value is the client-side-inference regression this card
  exists to catch. Caption reads `plot.png` (`captionFor`,
  `ImageGallery.tsx:56-58`). Falsify: path text but no thumb after expanding;
  a thumb whose `src` was not on the wire; or a thumb on a row whose
  `outputImages` was empty.

## Cleanup

`POST $HUB/api/sessions/local:$SID/shutdown` (the old `/s/$SID/shutdown` shim
404s silently and leaves the daemon up), kill the hub by the PID you captured,
and `rm -rf` `$WORK` plus your own run dir. Leave any real hub untouched.

## Sharp edges

- **A tool row starts COLLAPSED, and the gallery only exists while it is
  expanded** (`ToolCallItem.tsx:270-286`; nothing about carrying images
  auto-expands, `:163-170`). A *failed* shell call is the exception — `shell`
  auto-expands on a nonzero exit (`tools/shellTool.tsx:133`) — so a command
  that succeeds is collapsed and a command that fails is open. Do not read
  "thumb visible" off a run whose command exited nonzero and conclude the
  collapsed case works.
- **The shell row has no open-beside control**, unlike `read_file` /
  `write_file` / `edit_file`. `openBesidePath` is defined only on those three
  (`tools/fsTools.tsx:64-67`, `tools/editTools.tsx:78,96`), and `apply_patch`
  opts out explicitly (`editTools.tsx:99`). Do not treat its absence here as a
  regression — see `output-image-lightbox-and-pane.md`, which is why that card
  cannot use this one as its source session.
- **Keep the printed path plain and relative.** The scanner is deliberately
  conservative: it takes bare, single-quoted, or double-quoted tokens ending in
  `.png/.jpg/.jpeg/.gif/.webp` (regex, `output_images.go:30`), drops anything
  containing `://` (`:224-226`), caps at 20 candidates (`:26`) and 8 rendered
  descriptors (`:27`). Traversal, out-of-cwd, missing, non-image and SVG
  candidates are `unsafe-image-path-ignored.md`'s subject.
- **Every `.png`-looking token in the output is a candidate**, so a chatty
  command that echoes several filenames produces several descriptors. Keep the
  command's output to one path if you want a one-thumb assertion.
- The gallery drops any `src` the browser refuses to load (`onError` →
  `markUnloadable`, `ImageGallery.tsx:73-77,140`), so a deleted file reads as
  "never rendered". Step 4's out-of-band fetch distinguishes them.
- Inference itself is unit-tested — `TestShellOutputImageCandidatesConservative`,
  `TestOutputImagesForToolCallShellOutput`,
  `TestOutputImagesForToolCallCapsRenderedShellImages`
  (`output_images_test.go:17,334,347`). If those pass and this card fails, the
  break is in the wiring, not the scanner.
