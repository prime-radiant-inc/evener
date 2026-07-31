# web-drag-drop-image: drop an image onto the spawn composer

**What this covers**: kata `2frx` (live e2e for image attachments) over the
drop-zone wiring. The `Dropzone` widget (`widgets/dropzone/index.tsx:27-71`)
wraps the spawn pane's prompt card and hands every dropped `File` to
`useAttachments.ingestFiles` (`Spawn.tsx:512`). That splices an `[image N]`
marker into the prompt text synchronously, then canvas-re-encodes each image to
PNG off-thread (`attachments/encodePng.ts:43-73`). On submit the base64 bytes
ride an appwire `thread/start` as `{type:"image", …}` input items
(`panes/spawn/startThread.ts:39-46,66`) and land as an image part on the first
`USER_INPUT` turn.

Companion scenarios: `web-paste-image-from-clipboard.md`,
`web-file-picker-image.md`, `tui-paste-image-from-clipboard.md`,
`tui-paste-image-path.md`.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — its selector
map is the single place these hooks are maintained. `form[data-spawn-form]`,
`[data-drop-zone]`, `[data-composer-attachments]`, `[data-attachment]`,
`[data-attachment-error]`, `.spawn-btn`, `.drop-active`, `window.SerfAppwire`
and `assets/spawn.js` all died with the vanilla frontend (`660376f78`). There
is no REST fallback on this path either: `startThread` goes to appwire
`thread/start`, **not** `/api/spawn` (`startThread.ts:1-5`).

## Pre-state

- `serf-hub` running on an isolated `$HOME` and a kernel-assigned port
  (never `9180`, Jesse's real one — see the Setup checklist in
  `docs/agentic-testing.md`). Token at `$HOME/.serf/auth-token`.
- `make build-web` before building the hub; a checkout that has never run it
  ships a one-line `frontend/dist/PLACEHOLDER` and serves no app (rebuild
  matrix item 3 in the runbook).
- An image-capable model reachable through configured credentials, e.g.
  `anthropic/claude-haiku-4-5-20251001`.
- `superpowers-chrome:browsing` available, with your own Chrome profile claimed
  via `set_profile` before the first `use_browser` call (kata `8ecz`).
- The CSP must include `blob:` in `img-src`
  (`cmd/serf-hub/internal/httpsec/httpsec.go:38`) — the re-encode pipeline
  loads a `URL.createObjectURL(blob)` reference into an `Image`
  (`encodePng.ts:45,71`), and without it every drop fails decode. See Sharp
  edges.

## Steps

1. **Open the spawn pane** (browser): navigate
   `$HUB/auth?token=$TOKEN&next=/new` and wait for
   `[data-testid="spawn-prompt-card"]`. The prompt textarea inside it is
   `[aria-label="Prompt"]` (`Spawn.tsx:529`).

2. **Synthesize and drop an image** (browser). A real drop is an OS-level
   gesture, so build a PNG via canvas, wrap it in a File + DataTransfer, and
   dispatch the full dragenter / dragover / drop sequence on the Dropzone —
   the element that *wraps* the prompt card, i.e. its parent. The zone toggles
   a hashed CSS-module class, so assert on the **class-token count changing**
   rather than on a class name (`Dropzone` sets `zone` alone or `zone active`,
   `widgets/dropzone/index.tsx:62`):
   ```javascript
   (async () => {
     const c = document.createElement("canvas");
     c.width = 64; c.height = 64;
     c.getContext("2d").fillStyle = "red";
     c.getContext("2d").fillRect(0, 0, 64, 64);
     const blob = await new Promise((r) => c.toBlob(r, "image/png"));
     const file = new File([blob], "dropped.png", { type: "image/png" });
     const dt = new DataTransfer();
     dt.items.add(file);
     const dz = document.querySelector('[data-testid="spawn-prompt-card"]').parentElement;
     const ev = (t) => new DragEvent(t, { bubbles: true, cancelable: true, dataTransfer: dt });
     const atRest = dz.classList.length;
     dz.dispatchEvent(ev("dragenter"));
     const onHover = dz.classList.length;
     dz.dispatchEvent(ev("dragover"));
     dz.dispatchEvent(ev("drop"));
     await new Promise((r) => setTimeout(r, 0));      // let React commit, nothing more
     const markerEarly = document.querySelector('[aria-label="Prompt"]').value;
     const tilesEarly = document.querySelectorAll('button[aria-label^="Remove "]').length;
     await new Promise((r) => setTimeout(r, 1500));   // let the re-encode land
     return JSON.stringify({
       port: location.port,                     // page-identity check, always
       atRest, onHover, afterDrop: dz.classList.length,
       markerEarly, tilesEarly,
       markerAfter: document.querySelector('[aria-label="Prompt"]').value,
       removeButtons: Array.from(
         document.querySelectorAll('button[aria-label^="Remove "]'), (b) => b.getAttribute("aria-label")),
       toast: document.querySelector('[aria-label="Notifications"]')?.textContent ?? "",
     }, null, 2);
   })()
   ```

3. **Type a prompt and submit** (browser). Set the model via the picker (the
   shared ARIA combobox in `widgets/modelCatalog/`, `role="option"` rows), type
   into `[aria-label="Prompt"]`, then click `[data-testid="spawn-submit"]`
   (labelled **Spawn**, `Spawn.tsx:558-563`). Read `location.pathname` and the
   toast region afterwards.

4. **Poll and read the transcript** (browser-free):
   ```bash
   SID=…   # the id half of the /s/local:<SID> path from step 3
   for i in $(seq 1 60); do
     s=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$s" = "idle" ] && break
     sleep 2
   done
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:20
   TS=$(find "$HOME/.local/state/serf/projects" -name "$SID.transcript.jsonl")
   jq -r 'select(.turn.kind=="USER_INPUT") | .turn.message.content[]
          | select(.kind=="image")
          | "image media=\(.image.media_type) bytes=\(.image.data|length)"' "$TS"
   ```

## Expected

- **Step 2 (drop UX + staging)**: `onHover > atRest` (dragenter added the
  active class, `dropzone/index.tsx:30-33`) and `afterDrop === atRest` (the
  drop handler cleared it, `:46-52`). `markerEarly` already contains
  `[image 1]` and `tilesEarly` is already 1: the marker is spliced and the item
  staged **before** `reencodeToPng` is even called
  (`useAttachments.ts:156-171`, `textareaMarkers.ts:19-21`), so the early read
  — taken a macrotask after the drop, long before a canvas round-trip could
  finish — distinguishes "staged, then settled" from "never staged".
  `removeButtons` includes `Remove dropped.png`; while the decode is still in
  flight the spawn pane's chip reads `dropped.png (processing…)`
  (`Spawn.tsx:570-577`), so the label settles to the bare filename.
  `toast` is empty. Falsify:
  - a toast reading `Couldn't attach dropped.png (image decode failed)` →
    the CSP `blob:` failure (`useAttachments.ts:188`, and see Sharp edges);
  - a toast reading `Couldn't attach dropped.png` with no parenthetical →
    the file was rejected as non-image by `rejectionReason`
    (`attachments/limits.ts:23-29`);
  - no marker and no toast → the Dropzone isn't wired to the card;
  - `onHover === atRest` → the `dragenter` handler is gone.
- **Step 3 (submit)**: the pane navigates to `/s/local:<SID>` — the qualified
  ref form, verbatim from the wire (`startThread.ts:48-67`,
  `shell/routing.ts:69-70`). A bare `/s/<SID>` would render "Page not found"
  by design. The toast region is empty. Falsify: still on `/new`; a toast
  reading `Spawn failed: …` (`Spawn.tsx:460`) or `Image attachment is still
  processing.` (`:433-435`, meaning submit raced the decode).
- **Step 4 (transcript, exact)**: a `USER_INPUT` turn carries both a `text`
  part with the typed prompt and an `image` part whose `media_type` is
  `image/png` and whose `data` is non-empty — every attachment is re-encoded to
  PNG regardless of input type (`encodePng.ts:1-14`). A later assistant turn
  refers to the image. Falsify: no `image` part on `USER_INPUT` (the items
  never reached the daemon), or the model describes something other than a
  solid red square.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" -d '{}' \
  "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
```
Then kill the hub by the PID you captured and remove your own run dir. The old
`/s/$SID/shutdown` form-POST shim is gone — it 404s silently and leaves the
daemon running, poisoning the next run's `idle` poll.

## Sharp edges

- **CSP must allow `blob:` in `img-src`** (kata `1pgw`). `reencodeToPng` calls
  `URL.createObjectURL(blob)` and loads the result into an `Image` to decode it
  before re-encoding (`encodePng.ts:45,71`); without `blob:` the `onerror` path
  fires, the promise rejects, `ingestFiles` strips the marker back out and
  toasts `Couldn't attach <name> (image decode failed)`
  (`useAttachments.ts:179-189`) for a perfectly valid PNG. The directive lives
  at `cmd/serf-hub/internal/httpsec/httpsec.go:38`.
- **The spawn pane and the session composer stage attachments differently.**
  Spawn renders a `Chip` per item (`Spawn.tsx:570-577`); the session composer
  renders an `AttachmentTile` with a thumbnail
  (`composer/AttachmentTile.tsx:52-95`, `Composer.tsx:749-755`). Neither has a
  `data-testid`; both expose `button[aria-label="Remove <name>"]`, which is why
  this card addresses them that way.
- **Do not assert a thumbnail in the session transcript afterwards.** The
  replayed `InputItem` for a user-attached image carries no `url`, `path`, or
  `name` (`internal/apptranscript/apptranscript.go:248-253`; the hub stamps
  only `metadata.sha`/`size`, `app_threadread.go:510-517`), and
  `reducer.ts:44-47` resolves an image `src` from `url ?? path ?? name` only.
  There is no fetchable URL on the wire for it, so the transcript — not the
  DOM — is the assertion for step 4.
- **`DragEvent` must carry `dataTransfer:`.** Chromium honours
  `new DragEvent("drop", { dataTransfer: dt })`; Safari historically yields a
  read-only null. Chromium is the well-trodden path for an agent.
- **`dragover` exists only to `preventDefault`** (`dropzone/index.tsx:35-39`) —
  without it the browser refuses to fire `drop` at all. It does not toggle the
  active class; only `dragenter` does.
- **`dragleave` is unconditional** (`:41-44`): it clears the active class
  without inspecting `relatedTarget`, so a real cursor crossing nested children
  can flicker. Harmless for the synthetic sequence above.
- **Caps**: 8 attachments and 8 MiB per file client-side
  (`attachments/limits.ts:9-10`); the hub's own send path allows 8 MiB per
  image and 96 MiB per request (`internal/hubcore/types.go:13-14`). The count
  cap is cumulative across paste + drop + picker in one composer session, and a
  single multi-file drop counts each file within that batch
  (`useAttachments.ts:135-154`). Rejections from one drop are combined into a
  single toast, never one per file (`:193-199`).
- **Multi-file drops work**: `Dropzone` forwards every `File` in order and has
  no type opinion of its own (`dropzone/index.tsx:7-11,50-51`); the image-only
  rule lives in `rejectionReason`.
