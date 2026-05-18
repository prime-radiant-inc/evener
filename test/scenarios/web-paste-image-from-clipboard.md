# web-paste-image-from-clipboard: paste an image into the spawn composer

**What this covers**: kata `2frx` (live e2e for image attachments) over
the kata `r6a1` paste handler. The `paste` event on the prompt textarea
inspects `e.clipboardData.items` for `kind="file" && type starts with
"image/"`, canvas-re-encodes each one to PNG, and pushes it onto
`form.__composerPasteState.items`. On submit the bytes flow through the
appwire `thread/start` (or REST `/api/spawn` fallback) and land as a
`ContentImage` part on the first `USER_INPUT` message.

Companion scenarios: `web-drag-drop-image.md`, `web-file-picker-image.md`,
`tui-paste-image-from-clipboard.md`, `tui-paste-image-path.md`.

## Pre-state

- `serf-hub` running on `0.0.0.0:9180`. Token at `~/.serf/auth-token`.
- `anthropic/claude-haiku-4-5-20251001` or `openai/gpt-5.5` reachable
  through configured credentials. Both accept image inputs (verified
  2026-05-18 against the live hub).
- `superpowers-chrome:browsing` skill (use_browser MCP) available.
- The CSP fix from kata `1pgw` is in effect — `img-src` must include
  `blob:`. Otherwise every paste/drop/picker silently rejects images
  with "Not an image: <name>" (see Sharp edges).

## Steps

1. **Open the spawn form** in the browser via `use_browser`. The
   `/auth?token=…&next=/new` redirect sets the cookie so subsequent
   navigations don't 401:
   ```
   action: navigate
   payload: http://localhost:9180/auth?token=<TOKEN>&next=/new
   ```
   Confirm the form is mounted:
   ```
   action: eval
   payload: JSON.stringify({
     form: !!document.querySelector("form[data-spawn-form]"),
     ta:   !!document.querySelector("textarea[name='prompt']"),
     ct:   !!document.querySelector("[data-composer-attachments]"),
     state: !!(document.querySelector("form[data-spawn-form]")||{}).__composerPasteState,
   })
   // expect every field true
   ```

2. **Synthesize and paste an image programmatically.** The browser-
   level clipboard API only writes images via a real user gesture, so
   the scenario builds a tiny PNG via canvas, wraps it in a File +
   DataTransfer, and dispatches a synthetic `paste` ClipboardEvent on
   the textarea. The composer-attachments helper reads the same
   `e.clipboardData.items` a real Ctrl+V would yield:
   ```
   action: eval
   payload: (async () => {
     const c = document.createElement("canvas");
     c.width = 64; c.height = 64;
     c.getContext("2d").fillStyle = "red";
     c.getContext("2d").fillRect(0, 0, 64, 64);
     const blob = await new Promise(r => c.toBlob(r, "image/png"));
     const file = new File([blob], "screenshot.png", { type: "image/png" });
     const dt = new DataTransfer();
     dt.items.add(file);
     const ta = document.querySelector("textarea[name='prompt']");
     ta.focus();
     ta.dispatchEvent(new ClipboardEvent("paste", {
       bubbles: true, cancelable: true, clipboardData: dt,
     }));
     await new Promise(r => setTimeout(r, 800));
     return JSON.stringify({
       chipCount: document.querySelectorAll("[data-composer-attachments] [data-attachment]").length,
       chipLabel: (document.querySelector("[data-composer-attachments] [data-attachment]")?.textContent || "").trim(),
       errBanner: document.querySelector("[data-attachment-error]")?.textContent || "",
     });
   })()
   // chipCount: 1
   // chipLabel: includes "📎" + "(64×64)"; the name will be "paste-<ms>.png"
   //            because the handler timestamps clipboard pastes.
   // errBanner: ""
   ```

3. **Set the model and prompt, then submit.** The chip-style model
   selector renders a label; the hidden `name=model` input is the
   field the form actually submits. Set it directly:
   ```
   action: eval
   payload: (() => {
     const form = document.querySelector("form[data-spawn-form]");
     form.querySelector('input[name="model"]').value = "anthropic/claude-haiku-4-5-20251001";
     form.querySelector('input[name="working_dir"]').value = "/tmp";
     form.querySelector('textarea[name="prompt"]').value = "describe this image in one sentence";
     return "ready";
   })()
   ```
   Click the spawn button:
   ```
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
   // path: "/s/<SID>" (a 26-char ULID)
   // err:  ""
   ```

5. **Poll the session to idle and read the transcript** (bash):
   ```bash
   TOKEN=$(cat ~/.serf/auth-token)
   SID=…  # from step 4
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
               print(f"[{i}] IMAGE on {kind}: media={c['image'].get('media_type')} bytes={len(c['image'].get('data', ''))}")
           elif c.get("kind") == "text" and kind == "USER_INPUT":
               print(f"[{i}] {kind} text: {c.get('text','')[:120]!r}")
           elif c.get("kind") == "tool_call" and c.get("tool_call", {}).get("name") == "communicate":
               print(f"[{i}] communicate: {c['tool_call']['arguments'].get('message','')[:200]!r}")
   EOF
   ```

## Expected

- **Step 2 (chip render)**: `[data-composer-attachments]` contains
  exactly one `[data-attachment]` chip. Label includes `📎` and the
  dimensions `(64×64)`. `errBanner` is empty. Falsification: chip
  count is 0 AND `errBanner` reads `Not an image: <name>` — that is
  the kata `1pgw` failure mode (CSP blocks the `blob:` URL the
  re-encode pipeline uses; file the kata or apply the fix).
- **Step 4 (submit)**: browser navigates from `/new` to
  `/s/<SID>`; `location.pathname` matches `^/s/[0-9A-Z]{26}$`. The
  spawn-error banner is empty. Falsification: `path` is still `/new`,
  or `err` contains anything (commonly `model required`, `text or
  images required`, or `413 Request Entity Too Large`).
- **Step 5 (transcript)**: at least one `USER_INPUT` row has
  `message.content` with both a `kind=text` part
  `"describe this image in one sentence"` and a `kind=image` part
  whose `image.media_type` is `image/png` and whose `image.data` is
  non-empty. A later `ASSISTANT` row's `communicate` tool call has
  `arguments.message` referencing the image — verified live
  (2026-05-18) producing `"This is a solid red square."` against
  `claude-haiku-4-5-20251001`. Falsification: no `image` part on
  `USER_INPUT` (the items list never reached the daemon), or the
  assistant text describes a different colour/shape, or the model
  refuses with `I cannot see images`.

## Cleanup

```bash
TOKEN=$(cat ~/.serf/auth-token)
curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" -d '{}' \
  "http://localhost:9180/s/$SID/shutdown" >/dev/null
# Optional, for run-to-run hermeticity:
# find ~/.local/state/serf/projects -name "$SID*" -delete
```

## Sharp edges

- **CSP must allow `blob:` in `img-src`** (kata `1pgw`). The
  composer-attachments helper (`cmd/serf-hub/assets/composer-
  attachments.js:reencodeToPng`) calls
  `URL.createObjectURL(blob)` and loads the result into an
  `Image` element to derive width/height before re-encoding to
  PNG. With `img-src 'self' data:` (no `blob:`) the `Image.onerror`
  fires, `reencodeToPng` rejects, and `ingestFiles` routes the
  file to its `rejected[]` list — banner reads
  `Not an image: <name>` for what is in fact a perfectly valid
  PNG. The jstest harness stubs `window.Image` so the failure
  is invisible to unit tests; only live browser verification
  catches it. The kata `1pgw` fix sets
  `img-src 'self' data: blob:;` in
  `cmd/serf-hub/security.go:CSPMiddleware`.
- **Real Ctrl+V vs synthetic `paste` event.** The browser's
  clipboard read API (`navigator.clipboard.read()`) requires a real
  user gesture and a focused, secure-context page; agent-driven
  navigation does not qualify. The synthetic
  `new ClipboardEvent("paste", { clipboardData: dt })` route lets a
  test agent exercise the SAME handler `attachComposerImageHandlers`
  attaches without needing OS-level clipboard plumbing. The user
  experience on a real Ctrl+V is identical — the browser builds
  the DataTransfer from the OS clipboard, then dispatches the
  same `paste` event.
- **ClipboardEvent constructor support.** Chromium / Edge / recent
  Firefox accept `new ClipboardEvent("paste", {clipboardData: dt})`.
  Safari historically returned a read-only null clipboardData when
  the event was created from a constructor; if Safari coverage is
  needed, fall back to driving the OS clipboard via OS APIs and
  letting a real user gesture trigger the paste.
- **PNG re-encode is async.** `attachComposerImageHandlers` awaits a
  canvas `toBlob` round-trip. A `setTimeout(0)` after the dispatch
  is not always enough; 500–1000 ms is safe.
- **`paste-<ms>.png` filename**. The paste handler timestamps the
  attachment name because the clipboard payload itself has no
  filename. Drag-drop and the file picker preserve the original
  filename (`screenshot.png`, etc.).
- **paste handler does NOT `preventDefault`** when text is also
  present in the clipboard — any accompanying text is still
  inserted into the textarea by the browser's default handler.
  This is by design: the typical "see this:" + screenshot paste
  should leave the prose in the textarea AND attach the image
  alongside.
- **Per-image cap is 12 MB; per-request cap is 40 MB.** See
  `cmd/serf-hub/web.go:sendMax*Bytes` and the matching `/api/spawn`
  handler. A 64×64 PNG is ~200 bytes; nowhere near.
- **claude-haiku-4-5-20251001 sometimes tries `read_file` on the
  literal string "image"** before describing it. The call fails,
  the model falls back to the inline image, and the description
  appears via `communicate` in the same turn. gpt-5.5 skips this
  hop.
- **OpenAI ChatGPT-OAuth tokens** may reject image inputs depending
  on the OAuth scope. If `gpt-5.5` returns `unsupported content
  type` or refuses, fall back to `claude-haiku-4-5-20251001`.
