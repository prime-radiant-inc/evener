# web-drag-drop-image: drop an image onto the spawn composer

**What this covers**: kata `2frx` (live e2e for image attachments) over
the kata `65mm` drop-zone wiring. The
`[data-drop-zone]` element (the spawn form's `.spawn-input-wrap`)
listens for `dragenter` / `dragover` / `dragleave` / `drop`. On `drop`
the helper enumerates `dataTransfer.files`, splits into image vs non-
image, canvas-re-encodes each image to PNG, and pushes onto
`form.__composerPasteState.items`. On submit the bytes flow through the
appwire `thread/start` (or REST `/api/spawn` fallback) and land as a
`ContentImage` part on the first `USER_INPUT` message.

Companion scenarios: `web-paste-image-from-clipboard.md`,
`web-file-picker-image.md`, `tui-paste-image-from-clipboard.md`,
`tui-paste-image-path.md`.

## Pre-state

- `serf-hub` running on `0.0.0.0:9180`. Token at `~/.serf/auth-token`.
- `anthropic/claude-haiku-4-5-20251001` or `openai/gpt-5.5` reachable
  through configured credentials. Both accept image inputs (verified
  2026-05-18 against the live hub).
- `superpowers-chrome:browsing` skill (use_browser MCP) available.
- The CSP fix from kata `1pgw` is in effect — `img-src` must include
  `blob:`. Without it the drop succeeds at the dataTransfer layer but
  the re-encode pipeline rejects every image with "Not an image:
  <name>" (see Sharp edges).

## Steps

1. **Open the spawn form** in the browser:
   ```
   action: navigate
   payload: http://localhost:9180/auth?token=<TOKEN>&next=/new
   ```
   Confirm wiring:
   ```
   action: eval
   payload: JSON.stringify({
     form:  !!document.querySelector("form[data-spawn-form]"),
     drop:  !!document.querySelector("[data-drop-zone]"),
     ct:    !!document.querySelector("[data-composer-attachments]"),
     state: !!(document.querySelector("form[data-spawn-form]")||{}).__composerPasteState,
   })
   ```

2. **Synthesize and drop an image programmatically.** A real drop is
   an OS-level gesture; the scenario builds a PNG via canvas, wraps it
   in a File + DataTransfer, and dispatches the full
   dragenter / dragover / drop sequence on `[data-drop-zone]`. The
   helper attaches the same listeners a real drop would fire:
   ```
   action: eval
   payload: (async () => {
     const c = document.createElement("canvas");
     c.width = 64; c.height = 64;
     c.getContext("2d").fillStyle = "red";
     c.getContext("2d").fillRect(0, 0, 64, 64);
     const blob = await new Promise(r => c.toBlob(r, "image/png"));
     const file = new File([blob], "dropped.png", { type: "image/png" });
     const dt = new DataTransfer();
     dt.items.add(file);
     const dz = document.querySelector("[data-drop-zone]");
     dz.dispatchEvent(new DragEvent("dragenter",
       { bubbles: true, cancelable: true, dataTransfer: dt }));
     dz.dispatchEvent(new DragEvent("dragover",
       { bubbles: true, cancelable: true, dataTransfer: dt }));
     const onHover = dz.classList.contains("drop-active");
     dz.dispatchEvent(new DragEvent("drop",
       { bubbles: true, cancelable: true, dataTransfer: dt }));
     await new Promise(r => setTimeout(r, 800));
     return JSON.stringify({
       onHover,
       afterDrop: dz.classList.contains("drop-active"),
       chipCount: document.querySelectorAll("[data-composer-attachments] [data-attachment]").length,
       chipLabel: (document.querySelector("[data-composer-attachments] [data-attachment]")?.textContent || "").trim(),
       errBanner: document.querySelector("[data-attachment-error]")?.textContent || "",
     });
   })()
   // onHover: true     (dragenter set the visual-feedback class)
   // afterDrop: false  (drop handler removes it)
   // chipCount: 1
   // chipLabel: "📎 dropped.png (64×64)×"
   // errBanner: ""
   ```

3. **Set the model and prompt, then submit**:
   ```
   action: eval
   payload: (() => {
     const form = document.querySelector("form[data-spawn-form]");
     form.querySelector('input[name="model"]').value = "anthropic/claude-haiku-4-5-20251001";
     form.querySelector('input[name="working_dir"]').value = "/tmp";
     form.querySelector('textarea[name="prompt"]').value = "describe this image in one sentence";
     return "ready";
   })()

   action: click
   selector: .spawn-btn
   ```

4. **Pull the new session id** from the post-spawn URL:
   ```
   action: eval
   payload: new Promise(r => setTimeout(r, 2500)).then(() => JSON.stringify({
     path: location.pathname,
     err: document.querySelector("[data-spawn-error]")?.textContent || "",
   }))
   ```

5. **Poll the session to idle and read the transcript** — same bash
   block as `web-paste-image-from-clipboard.md` step 5.

## Expected

- **Step 2 (drop UX + chip)**: `onHover` is true (dragenter added
  `.drop-active` to the drop zone for visual feedback). `afterDrop`
  is false (drop handler removed it). Exactly one chip renders with
  label `📎 dropped.png (64×64)`. `errBanner` is empty.
  Falsification:
  - `chipCount === 0 && errBanner === "Not an image: dropped.png"`
    → kata `1pgw` CSP `img-src` doesn't include `blob:` (see
    Sharp edges).
  - `chipCount === 0 && errBanner === ""` → the drop zone isn't
    wired (kata 65mm regression); check the `init()` block in
    `spawn.js` calls `attachComposerDropHandlers`.
  - `onHover === false` → `dragenter` listener missing.
- **Step 3 (submit)**: navigation to `/s/<SID>` with no
  `[data-spawn-error]`.
- **Step 5 (transcript)**: `USER_INPUT` has a `kind=image` part with
  non-empty `image.data`; the assistant's `communicate` references
  the image. Verified 2026-05-18 against
  `claude-haiku-4-5-20251001` producing
  `"The image is a solid red square."`

## Cleanup

Same as `web-paste-image-from-clipboard.md`: shutdown the session via
`POST /s/$SID/shutdown`.

## Sharp edges

- **CSP `blob:` requirement** (kata `1pgw`). Identical to the
  paste scenario — `URL.createObjectURL` from the dropped File
  produces a `blob:` URL that the re-encode pipeline loads into
  an `Image` element. Without `blob:` in `img-src` the Image
  refuses to load and the file is reported as
  "Not an image: <name>".
- **`DragEvent` constructor accepts `dataTransfer:`**. Chromium /
  recent Firefox honour `new DragEvent("drop", {dataTransfer: dt})`;
  Safari historically yielded a read-only null dataTransfer. For
  real-browser parity in a test agent, Chromium is the well-trodden
  path.
- **`dragenter` only**, not `dragover`, adds `.drop-active`. The
  helper's `dragover` handler exists ONLY to call
  `preventDefault` (without which the browser would refuse to fire
  `drop`). Don't expect a `drop-active` class change from
  `dragover` alone.
- **`dragleave` is unconditional**. The drop-zone's `dragleave`
  handler removes `.drop-active` without inspecting
  `relatedTarget`. In a real cursor-driven workflow with nested
  elements this can flicker; for the synthetic event sequence
  used in this scenario it doesn't matter (dragenter restores the
  class if needed).
- **Non-image rejection banner** uses `surfaceRejections`. A drop
  of a `.txt` next to a `.png` results in the image chip plus a
  banner reading `Not an image: foo.txt`. The banner stays until
  the next ingest call.
- **Per-image cap 12 MB; per-request cap 40 MB.** Hit by neither
  the test fixture (~200 bytes) nor typical screenshots; refactor
  with care.
- **Real-user multi-file drag-drop**. `dataTransfer.files` can hold
  N images; the helper re-encodes all of them sequentially and
  pushes N chips. The submit flow happily ships them all.
- **The 📎 attach button is sibling to the drop zone**; it shares
  the same `pendingState`, so dropping after clicking-and-picking
  appends rather than replaces.
- **claude-haiku-4-5-20251001 vs gpt-5.5 quirks** — same as the
  paste scenario. claude-haiku-4-5 sometimes calls `read_file`
  on the literal "image" before describing; not a failure.
