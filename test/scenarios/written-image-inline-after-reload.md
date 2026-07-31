# written-image-inline-after-reload: keep structured written images inline after replay

**What this covers**: inline output-image v1, the durability half. A
`write_file` call that writes a supported image under the session cwd shows a
thumbnail inline under its own row while the turn is live, and the SAME
thumbnail is reconstructed after a reload — because the hub re-derives the
file-backed descriptor from the call's own `file_path` argument on both paths:
live via `enrichOutputImageNotification` (`cmd/serf-hub/output_images.go:98-152`)
and on reload via `enrichThreadFileBackedOutputImages`
(`cmd/serf-hub/app_threadread.go:519-554`, called from `app_rpc.go:210`). If
either half regresses, the image appears once and never again — the exact
regression this card exists for.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — its selector
map is the single place these hooks are maintained. The `.tool-output-images
.user-image-card img.user-image-thumb` selectors this card used to name died
with the vanilla frontend (`660376f78`).

## Pre-state

- A freshly built hub on an isolated `$HOME` and a kernel-assigned port — see
  the Setup checklist in `docs/agentic-testing.md`. Token at
  `$HOME/.serf/auth-token` (that isolated one).
- A hermetic `$WORK` as the session's `working_dir`, and a small valid PNG
  staged somewhere the model can copy or reproduce from.
- A cheap model, e.g. `anthropic/claude-haiku-4-5-20251001`.
- For the browser steps: `make build-web` before building the hub (rebuild
  matrix item 3 in the runbook).

## Steps

1. **Spawn (browser-free).** `POST $HUB/api/spawn` with `working_dir: $WORK`
   and a prompt that forces one `write_file` producing `out.png` with valid PNG
   bytes. Poll `GET $HUB/api/sessions/local:$SID` until `state` is `idle`.

2. **Confirm the file (browser-free).** `$WORK/out.png` exists, is a regular
   file, and sniffs as one of the four v1 types — PNG, JPEG, GIF, WebP
   (`supportedOutputImageMedia`, `output_images.go:239-254`; SVG is excluded by
   omission, deliberately).

3. **Reload descriptor — the exact assertion, no browser needed.** Dial
   `ws://127.0.0.1:$PORT/rpc` with `Authorization: Bearer $TOKEN`,
   `initialize`, then `thread/read{ref:"local:$SID", includeTurns:true}`. This
   is *literally* the reload path — the browser calls the same RPC on every
   cold load — so the durability claim is checkable with no Chrome at all.
   Find the `commandExecution` item whose `toolName` is `write_file` (by name,
   never by turn index) and read its `outputImages`. Re-issue the same
   `thread/read` a second time and diff: enrichment is idempotent
   (`TestEnrichThreadFileBackedOutputImagesIsIdempotent`, `output_images_test.go:185`).

4. **Fetch the bytes (browser-free).** `curl` the descriptor's `url` with
   `Authorization: Bearer $TOKEN`.

5. **Browser, live then reloaded (qualitative).** With the session open at
   `$HUB/auth?token=$TOKEN&next=/s/local:$SID`, expand the `write_file` row
   (`[data-testid="tool-call-item"][data-tool-name="write_file"]` — see Sharp
   edges) and note the thumbnail. Hard-refresh, expand the same row again, and
   compare:
   ```javascript
   ({
     port: location.port,                       // page-identity check, always
     thumbSrcs: Array.from(
       document.querySelectorAll('[data-testid="image-gallery-thumb"] img'),
       (i) => i.getAttribute("src")),
     caption: document.querySelector('[data-testid="image-gallery-caption"]')?.textContent,
   })
   ```

## Expected

- **Step 3 (descriptor, exact)**: exactly one `outputImages` entry on the
  `write_file` item, with `source: "written-file"` (`output_images.go:45-48`),
  `path: "out.png"` (cwd-relative, slash-separated, `output_images.go:273-286`),
  a `mediaType` starting `image/`, a 64-hex `sha`, and a `url` of the form
  `/doc/image?session=<escaped session>&path=out.png` — same-origin, relative,
  server-built, both query values escaped (`output_images.go:283`; shape pinned
  by `TestResolveOutputImageFileBuildsDescriptor`, `output_images_test.go:83`).
  The second `thread/read`
  returns the identical list. Falsify: no `outputImages` after reload (the
  image was live-only — the regression); a duplicate entry appearing on the
  second read (enrichment not idempotent); a `url` containing `://`; or any
  embedded bytes/base64 — `appwire.OutputImage` (`appwire/types.go:730-738`)
  has no field to carry them, so that could only mean the wire type changed.
- **Step 4 (bytes)**: 200 with an image `Content-Type`. `/doc/image` re-reads
  the file off disk inside the cwd boundary each time (`doc_serve.go:84-127`),
  which is exactly why this descriptor survives a reload while a sha-addressed
  one depends on bytes being in the transcript. Falsify: 404 (file moved or
  unsupported), 403 (path escaped the cwd — see `unsafe-image-path-ignored.md`).
- **Step 5 (browser)**: the same `src` string before and after the reload, and
  a caption of `out.png` (`captionFor`, `ImageGallery.tsx:56-58`). Falsify: the
  thumb is there live and gone after reload; the `src` changes across the
  reload; or the thumb attaches to a different row.

## Cleanup

`POST $HUB/api/sessions/local:$SID/shutdown` (the old `/s/$SID/shutdown` shim
404s silently and leaves the daemon up), kill the hub by the PID you captured,
and `rm -rf` `$WORK` plus your own run dir. Leave any real hub untouched.

## Sharp edges

- **A tool row starts COLLAPSED, and the gallery only exists while it is
  expanded.** `<ImageGallery images={item.outputImages} />` sits inside
  `{expanded && …}` (`ToolCallItem.tsx:270-286`); carrying images does not
  auto-expand a row (`:163-170`). Post-reload the row is collapsed again, so
  "gone after reload" is the default appearance — expand before concluding
  anything. This is the single easiest way to fake this card's own failure.
- **The reload path needs the session's cwd.** `enrichThreadFileBackedOutputImages`
  returns the thread untouched when `thread.CWD` is empty
  (`app_threadread.go:524-527`), and `/doc/image` refuses a session whose cwd
  it cannot resolve (`localSessionCWD`, `doc_serve.go:132-155`). A session
  spawned with no `working_dir` cannot pass this card.
- **`/doc/image` is LOCAL-session only** (`isLocalRouteID`, `doc_serve.go:133-135`).
  A remote/codex ref gets no file-backed descriptor at all.
- **The gallery silently drops any `src` the browser refuses** (`onError` →
  `markUnloadable`, `ImageGallery.tsx:73-77,140`). Deleting `out.png` between
  the two loads therefore reads as "reload lost the image" when it is really
  "the file is gone" — step 4's out-of-band fetch is what tells the two apart.
- `edit_file` takes the same path (`output_images.go:46-48`); `apply_patch`
  deliberately does not use its arguments, only its printed output
  (`output_images.go:62-65`), because it can touch several files.
- The resolver half is unit-tested: `TestOutputImagesForToolCallStructuredWriteFile`
  and `TestEnrichThreadFileBackedOutputImagesIsIdempotent` (`output_images_test.go:119,185`).
  If those pass and this card fails, the break is in the wiring.
