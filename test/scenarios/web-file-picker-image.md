# web-file-picker-image: attach an image via the spawn form's file picker

**What this covers**: kata `2frx` (live e2e for image attachments) over
the kata `65mm` web composer-attachments wiring on `/new`. The hidden
`<input type=file data-file-picker>` accepts an image, the JS handler
canvas-re-encodes to PNG, the chip renders below the textarea, and on
submit the appwire `thread/start` (or the REST `/api/spawn` fallback)
carries the bytes through to the agent as a `ContentImage` part on the
first `USER_INPUT` message.

Companion scenarios: `web-drag-drop-image.md`, `web-paste-image-from-clipboard.md`,
`tui-paste-image-from-clipboard.md`, `tui-paste-image-path.md`.

## Pre-state

- `serf-hub` running on `0.0.0.0:9180`. Token at `~/.serf/auth-token`.
- Either `anthropic/claude-haiku-4-5-20251001` or `openai/gpt-5.5`
  reachable through configured credentials. Both are known to accept
  image inputs (verified live 2026-05-18).
- `superpowers-chrome:browsing` skill (use_browser MCP) available.
- `convert` (ImageMagick) or any tool that can produce a tiny PNG.
- CSP fix from kata `1pgw` is in effect — `img-src` must include
  `blob:`. Otherwise the chip renders as "Not an image: <name>"
  (see Sharp edges).

## Steps

1. **Build the fixture PNG.** A 64×64 solid-red PNG is sufficient — it
   travels in any path and any model can describe it succinctly so the
   assistant response is easy to falsify:
   ```bash
   FIXDIR=$(mktemp -d -t serf-e2e-img-XXXX)
   convert -size 64x64 xc:red "$FIXDIR/red.png"
   ls -la "$FIXDIR/red.png"   # ~166 bytes
   file "$FIXDIR/red.png"     # PNG image data, 64 x 64
   ```

2. **Open the spawn form** in the browser via `use_browser`. The
   `/auth?token=…&next=/new` redirect sets the cookie so subsequent
   navigations don't 401:
   ```
   action: navigate
   payload: http://localhost:9180/auth?token=<TOKEN>&next=/new
   ```
   Confirm the form is mounted:
   ```
   action: eval
   payload: ({
     form:      !!document.querySelector("form[data-spawn-form]"),
     trigger:   !!document.querySelector("[data-attach-trigger]"),
     picker:    !!document.querySelector("[data-file-picker]"),
     attachCt:  !!document.querySelector("[data-composer-attachments]"),
   })
   // expect every field true
   ```

3. **Upload the fixture through the hidden file input.** `use_browser`
   exposes a `file_upload` action that sets `.files` on a selector and
   dispatches a `change` event — exactly what the composer-attachments
   helper listens for (`attachComposerFilePickerHandlers`, kata 65mm):
   ```
   action: file_upload
   selector: [data-file-picker]
   payload: {"files": ["/tmp/serf-e2e-img-…/red.png"]}
   ```
   The async PNG re-encode runs through a canvas — give it a moment:
   ```
   action: eval
   payload: new Promise(r => setTimeout(r, 200)).then(() => ({
     chipCount: document.querySelectorAll("[data-composer-attachments] [data-attachment]").length,
     chipLabel: (document.querySelector("[data-composer-attachments] [data-attachment]")
                  ?.textContent || "").trim(),
   }))
   // chipCount: 1
   // chipLabel: contains "📎" and "(64×64)"
   ```

4. **Set the model and prompt, then submit.** The site uses chip-style
   model selection; the hidden `name=model` input is the authoritative
   field. Set it directly to bypass the picker UI:
   ```
   action: eval
   payload: (() => {
     const form = document.querySelector("form[data-spawn-form]");
     form.querySelector('input[name="model"]').value = "anthropic/claude-haiku-4-5-20251001";
     form.querySelector('[data-chip-value-model]').textContent = "claude-haiku-4-5-20251001";
     form.querySelector('textarea[name="prompt"]').value = "describe this image in one sentence";
     return "ready";
   })()
   ```
   Click the spawn button. The submit handler builds `attachments`
   from `form.__composerPasteState.items` and ships them through
   `SerfAppwire.startThread` (or the REST fallback when appwire is
   absent — both routes are exercised by the same UI gesture):
   ```
   action: click
   selector: .spawn-btn
   ```

5. **Pull the new session id from the post-spawn URL** the browser
   landed on:
   ```
   action: eval
   payload: location.pathname  // "/s/<SID>"
   ```
   Capture `SID` for the polling step. (Use the bash tool — the
   browser tab is no longer needed for verification.)

6. **Poll the session to idle and read the transcript**:
   ```bash
   TOKEN=$(cat ~/.serf/auth-token)
   for i in $(seq 1 60); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" \
       "http://localhost:9180/api/sessions/local:$SID" \
       | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 2
   done
   TFILE=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
   python3 - <<EOF
   import json
   for i, line in enumerate(open("$TFILE")):
       j = json.loads(line)
       turn = j.get("turn", {})
       kind = turn.get("kind", "")
       for c in turn.get("message", {}).get("content", []):
           if c.get("kind") == "image":
               print(f"[{i}] IMAGE part on {kind}: media={c['image'].get('media_type')} bytes={len(c['image'].get('data', ''))}")
           elif c.get("kind") == "text" and kind in ("USER_INPUT",):
               print(f"[{i}] {kind} text: {c.get('text','')[:120]!r}")
           elif c.get("kind") == "tool_call" and c.get("tool_call", {}).get("name") == "communicate":
               print(f"[{i}] communicate: {c['tool_call']['arguments'].get('message','')[:200]!r}")
   EOF
   ```

## Expected

- **Step 3 (chip render)**: `[data-composer-attachments]` contains
  exactly one `[data-attachment]` chip. Its label includes the
  `📎` glyph and the dimensions `(64×64)`. Falsification: chip count is
  0 (the helper isn't wired — kata 65mm regression) or the dimensions
  show `(0×0)` (the canvas re-encode dropped the image data).
- **Step 4 (submit)**: the browser navigates from `/new` to
  `/s/<SID>`; `location.pathname` matches `^/s/[0-9A-Z]{26}$`. The hub
  returns 200 from `/api/spawn`. Falsification: stays on `/new`, or
  the spawn-form `[data-spawn-error]` shows a banner — most commonly
  `text or images required` (means the items list was empty) or `413
  Request Entity Too Large` (a per-image > 12 MB or total > 40 MB cap
  was hit; not possible with a 64×64 PNG).
- **Step 6 (transcript)**: there is at least one `USER_INPUT` row in
  the transcript whose `message.content` array contains both a
  `kind=text` part `"describe this image in one sentence"` and a
  `kind=image` part with `image.media_type=image/png` and a non-empty
  `image.data` blob. A later `ASSISTANT` row contains a `communicate`
  tool call whose `arguments.message` references the image content
  (the words `red`, `square`, or a synonymous colour/shape description
  — both gpt-5.5 and claude-haiku-4-5 produced "red square" /
  "bright red square" during recording). Falsification: no `image`
  part on `USER_INPUT` (the items list never reached the daemon),
  or the assistant text talks about a different colour / shape /
  refuses (`I cannot see images`), or the model erroneously calls
  `read_file` for a path called "image" — that's a known quirk of
  claude-haiku-4-5 (see Sharp edges) and is not a failure as long as
  a later turn carries the right describe-the-image message.

## Cleanup

```bash
TOKEN=$(cat ~/.serf/auth-token)
curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" -d '{}' \
  "http://localhost:9180/s/$SID/shutdown" >/dev/null
rm -rf "$FIXDIR"
# Optional, for hermeticity across runs:
# find ~/.local/state/serf/projects -name "$SID*" -delete
```

## Sharp edges

- **CSP `blob:` requirement** (kata `1pgw`). The composer-attachments
  helper (`cmd/serf-hub/assets/composer-attachments.js:reencodeToPng`)
  calls `URL.createObjectURL(blob)` and loads the result into an
  `Image` element to derive width/height before re-encoding to PNG.
  With `img-src 'self' data:` (no `blob:`) the `Image.onerror` fires,
  the helper rejects every image as decode-failed, and the banner
  reads `Not an image: <name>`. Kata `1pgw` adds `blob:` to
  `cmd/serf-hub/security.go:CSPMiddleware`. The jstest harness stubs
  `window.Image` so the failure is invisible to unit tests; only
  live browser verification catches it.
- **`file_upload` (CDP) vs synthetic DataTransfer.** The
  `use_browser` `file_upload` action uses Chrome DevTools Protocol
  `Input.setFileInputFiles` which Chromium honours for headless
  driving. An eval-only `element.files = dt.files` works in
  Chromium too — both paths exercise the same change-event handler
  the helper wires in. Real Firefox is stricter and rejects the
  direct assignment; if Firefox coverage is needed, CDP is the
  only portable path.
- **The PNG re-encode is async.** `attachComposerFilePickerHandlers`
  awaits a canvas `toBlob` round-trip, then pushes to
  `pendingState.items`. A `setTimeout(0)` after the upload click is
  usually enough; the recording used 200 ms.
- **The `name=model` hidden input is what the form actually sends.**
  Setting only the visible chip text (`data-chip-value-model`)
  without updating the input leaves an empty `model` field — the
  spawn would 400 with `model required`.
- **Both submit branches carry the bytes.** When `window.SerfAppwire`
  is present (the normal case) the request goes through
  `SerfAppwire.startThread` which base64-encodes the ArrayBuffer at
  the wire boundary (`appwire.js:encodeAttachmentData`). When it's
  not, the REST fallback in `spawn.js:611` base64-encodes via
  `spawnEncodeAttachmentData` and POSTs to `/api/spawn` with an
  `items: []` field of the same shape. Both paths land at
  `appwire.InputItem.Data` server-side.
- **claude-haiku-4-5-20251001 sometimes tries `read_file` on the
  string "image" before describing it.** This is an artefact of the
  Anthropic tool-use loop seeing the image in the message and the
  model preferring to "look at the file" rather than trust the
  inline content. The `read_file` call fails (no file named
  "image"), then the model falls back to describing the inline
  image. The describe message still appears in the same turn via
  `communicate`. gpt-5.5 skips this hop.
- **OpenAI ChatGPT-OAuth tokens may reject image inputs depending on
  the OAuth scope.** If `gpt-5.5` returns `unsupported content type`
  or refuses to describe the image, fall back to
  `anthropic/claude-haiku-4-5-20251001`. Both worked during the
  scenario recording (2026-05-18).
- **Per-image size cap is 12 MB; per-request cap is 40 MB.** A 64×64
  PNG is ~200 bytes; nowhere near. If a scenario gets refactored to
  a larger fixture, mind the limits in `web.go:sendMax*Bytes` and
  the matching `/api/spawn` handler.
- **The `data-composer-attachments` container is shared between the
  workspace composer and the spawn form** — both wire it via
  `composer-attachments.js`. If the chip is rendering on the wrong
  pane after a navigate, ensure the page is actually `/new` (not a
  back-button restore of a stale session view).
