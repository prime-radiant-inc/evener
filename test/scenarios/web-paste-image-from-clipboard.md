# web-paste-image-from-clipboard: paste an image into the spawn composer

**What this covers**: kata `2frx` (live e2e for image attachments) over the
kata `r6a1` paste handler. The `paste` event on the spawn prompt textarea
runs `imageFilesFromClipboard`
(`panes/session/composer/attachments/clipboard.ts:7-17`), which pulls every
`kind === "file" && type.startsWith("image/")` item off `clipboardData`;
each file is canvas-re-encoded to PNG by `reencodeToPng`
(`attachments/encodePng.ts:44`) and staged by `useAttachments.ingestFiles`.
On submit the bytes flow through the appwire `thread/start` and land as an
image part on the first user message.

Companion scenarios: `web-drag-drop-image.md`, `web-file-picker-image.md`,
`tui-paste-image-from-clipboard.md`, `tui-paste-image-path.md`.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained.
`form[data-spawn-form]`, `textarea[name='prompt']`,
`[data-composer-attachments]`, `[data-attachment]`, `.spawn-btn` and the
hidden `input[name=model]` all died with the vanilla frontend
(`660376f78`), along with `composer-attachments.js` and
`form.__composerPasteState`. The spawn card is
`[data-testid="spawn-prompt-card"]`, its textarea is `aria-label="Prompt"`
(`panes/spawn/Spawn.tsx:514,529`), and errors surface as toasts, not an
inline banner.

## Pre-state

- `serf-hub` running on an isolated `$HOME` and free port
  (never `9180`, Jesse's real one — see the Setup checklist in
  `docs/agentic-testing.md`). Token at `$HOME/.serf/auth-token`.
- `anthropic/claude-haiku-4-5-20251001` or `openai/gpt-5.5` reachable
  through configured credentials. Both accept image inputs (verified
  2026-05-18 against the live hub).
- `superpowers-chrome:browsing` skill (use_browser MCP) available, and a real
  SPA bundle — a checkout that has never run `make build-web` serves a
  one-line `frontend/dist/PLACEHOLDER` and no app at all.
- The CSP fix from kata `1pgw` is in effect — `img-src` must include
  `blob:` (`cmd/serf-hub/internal/httpsec/httpsec.go:40`). Otherwise every
  paste/drop/picker silently rejects images (see Sharp edges).

## Steps

1. **[browser] Open the spawn form.** The `/auth?token=…&next=/new` redirect
   sets the cookie so subsequent navigations don't 401:
   ```
   action: navigate
   payload: $HUB/auth?token=<TOKEN>&next=/new
   ```
   Confirm the card is mounted:
   ```
   action: eval
   payload: JSON.stringify({
     port: location.port,                        // page-identity check, always
     card: !!document.querySelector('[data-testid="spawn-prompt-card"]'),
     ta:   !!document.querySelector('textarea[aria-label="Prompt"]'),
     submit: !!document.querySelector('[data-testid="spawn-submit"]'),
   })
   // expect every field true
   ```

2. **[browser] Synthesize and paste an image programmatically.** The
   browser-level clipboard API only writes images via a real user gesture, so
   the scenario builds a tiny PNG via canvas, wraps it in a File +
   DataTransfer, and dispatches a synthetic `paste` ClipboardEvent on the
   textarea. React's `onPaste` (`Spawn.tsx:523,371-374`) reads the same
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
     const ta = document.querySelector('textarea[aria-label="Prompt"]');
     ta.focus();
     ta.dispatchEvent(new ClipboardEvent("paste", {
       bubbles: true, cancelable: true, clipboardData: dt,
     }));
     await new Promise(r => setTimeout(r, 800));
     const card = document.querySelector('[data-testid="spawn-prompt-card"]');
     return JSON.stringify({
       port: location.port,
       // No testid on the chip row; address it by the Chip's own remove
       // control, whose accessible name is "Remove <filename>".
       chipCount: document.querySelectorAll('button[aria-label^="Remove "]').length,
       chipLabels: Array.from(document.querySelectorAll('button[aria-label^="Remove "]'),
                              (b) => b.getAttribute("aria-label")),
       promptValue: document.querySelector('textarea[aria-label="Prompt"]').value,
       toast: document.querySelector('section[aria-label="Notifications"]')?.textContent || "",
     });
   })()
   ```

3. **[browser] Set the model and prompt, then submit.** There is no hidden
   `input[name=model]`; the model comes from the pane's own picker (the
   shared ARIA combobox, `role="option"` rows) and is a sticky default. Pick
   it through the UI, type the prompt into the same textarea (React state —
   assigning `.value` directly will NOT reach the component), then:
   ```
   action: click
   selector: [data-testid="spawn-submit"]
   ```

4. **[browser] Pull the new session id** from the post-spawn URL. `doSpawn`
   navigates via `paneToURL("session", { ref })` (`Spawn.tsx:421-422`), so
   the path is the **ref** form:
   ```
   action: eval
   payload: new Promise(r => setTimeout(r, 2500)).then(() => JSON.stringify({
     port: location.port,
     path: location.pathname,
     toast: document.querySelector('section[aria-label="Notifications"]')?.textContent || "",
   }))
   // path: "/s/local:<SID>", SID a 22-character UUIDv7 base62 payload
   // toast: ""
   ```

5. **[browser-free] Poll the session to idle and read the transcript.** This
   is where the exact assertion lives — the DOM can only hint at what
   reached the daemon:
   ```bash
   TOKEN=$(cat "$HOME/.serf/auth-token")
   SID=…  # from step 4
   for i in $(seq 1 60); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" \
       "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$state" = "idle" ] && break
     sleep 2
   done
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:20
   ```
   For the byte-level check that an image part really landed (the one case
   the runbook sanctions reading raw JSONL for), locate the file with
   `go run ./cmd/serf-doctor locate "$SID"` and grep it for `"kind":"image"`.

## Expected

- **Step 2 (chip render)**: exactly one attachment chip, whose remove button
  reads `Remove screenshot.png` (`widgets/chip/index.tsx:38-49`; the chip's
  own text is `item.name`, plus ` (processing…)` while the PNG round-trip is
  in flight, `Spawn.tsx:573-575`). `promptValue` contains the marker
  `[image 1]`, spliced in at the cursor synchronously
  (`attachments/textareaMarkers.ts:19-21`). `toast` is empty. Falsification:
  `chipCount` is 0, `promptValue` has no marker left in it, **and** the toast
  reads `screenshot.png (image decode failed)`
  (`attachments/useAttachments.ts:188`) — that is the kata `1pgw` failure
  mode (CSP blocks the `blob:` URL the re-encode pipeline uses; file the kata
  or apply the fix).
- **Step 4 (submit)**: the browser navigates from `/new` to
  `/s/local:<SID>`; `location.pathname` matches `^/s/local:[0-9A-Za-z]{22}$`.
  No toast. Falsification: `path` is still `/new`, or a toast appears
  (commonly `Image attachment is still processing.` from `Spawn.tsx:433-434`,
  or `Couldn't attach <name> (maximum 8 images)` / `(maximum 8 MB)` from
  `attachments/limits.ts:23-29` + `useAttachments.ts:193-199`).
- **Step 5 (transcript)**: the outline shows the first user turn carrying
  both the prompt text and an image part; the raw entry's image part has
  `media_type` `image/png` with non-empty `data`. A later assistant turn's
  `communicate` call references the image — verified live (2026-05-18)
  producing `"This is a solid red square."` against
  `claude-haiku-4-5-20251001`. Falsification: no image part on the first user
  turn (the staged items never reached the daemon), or the assistant
  describes a different colour/shape, or the model refuses with
  `I cannot see images`.

## Cleanup

```bash
TOKEN=$(cat "$HOME/.serf/auth-token")
curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" -d '{}' \
  "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
# Optional, for run-to-run hermeticity:
# find $HOME/.local/state/serf/projects -name "$SID*" -delete
```

## Sharp edges

- **CSP must allow `blob:` in `img-src`** (kata `1pgw`). `reencodeToPng`
  (`attachments/encodePng.ts:44-50`) calls `URL.createObjectURL(blob)` and
  loads the result into an `Image` element to derive width/height before
  re-encoding to PNG. Without `blob:`, `Image.onerror` fires, the re-encode
  rejects, and the `.catch` path (`useAttachments.ts:179-189`) strips the
  `[image N]` marker back out of the textarea, drops the chip, and toasts
  `<name> (image decode failed)` — for what is in fact a perfectly valid
  PNG. The message wording changed with the rewrite; the legacy
  `Not an image: <name>` text is gone. Unit tests stub the image decode, so
  only live browser verification catches this. The fix lives at
  `cmd/serf-hub/internal/httpsec/httpsec.go:40`
  (`img-src 'self' data: blob: https:;`) — `security.go` no longer exists.
- **The attachment chip has no `data-testid`.** Address it by the Chip
  widget's remove button, whose `aria-label` is `Remove <children>`
  (`widgets/chip/index.tsx:38-49`) — that is the only stable, semantic
  handle on the row today. If a future card needs a count without a
  filename, add a testid rather than reaching for a CSS-module class (the
  class names are hashed).
- **The pasted file keeps its own name now.** The legacy handler timestamped
  clipboard pastes as `paste-<ms>.png`; `ingestFiles` stores `file.name`
  verbatim (`attachments/useAttachments.ts:165`). A synthetic paste built
  with `new File([blob], "screenshot.png", …)` therefore shows
  `screenshot.png`, and a real OS paste shows whatever the browser named the
  clipboard file.
- **Setting `.value` on the React textarea does nothing.** The prompt is
  controlled state (`Spawn.tsx`'s `updatePrompt`). Type through the browser
  (`action: type`) or dispatch a real `input` event with the native setter;
  a bare assignment leaves the component's state empty and the spawn submits
  with no text.
- **Real Ctrl+V vs synthetic `paste` event.** `navigator.clipboard.read()`
  requires a real user gesture and a focused, secure-context page;
  agent-driven navigation does not qualify. The synthetic
  `new ClipboardEvent("paste", { clipboardData: dt })` route exercises the
  SAME `onPaste` handler React binds. The user experience on a real Ctrl+V is
  identical — the browser builds the DataTransfer from the OS clipboard, then
  dispatches the same `paste` event.
- **ClipboardEvent constructor support.** Chromium / Edge / recent Firefox
  accept `new ClipboardEvent("paste", {clipboardData: dt})`. Safari
  historically returned a read-only null `clipboardData` when the event was
  created from a constructor; if Safari coverage is needed, fall back to
  driving the OS clipboard and letting a real user gesture trigger the paste.
- **PNG re-encode is async.** The handler awaits a canvas `toBlob`
  round-trip, and the chip renders `pending: true` until it settles. A
  `setTimeout(0)` after the dispatch is not enough; 500–1000 ms is safe.
  Submitting while `attachments.hasPending` is refused outright with a toast
  (`Spawn.tsx:433-434`).
- **The paste handler does NOT `preventDefault`** when text is also present
  in the clipboard (`attachments/clipboard.ts:1-6` states the rule) —
  accompanying text
  is still inserted into the textarea by the browser's default handler. This
  is by design: the typical "see this:" + screenshot paste should leave the
  prose in the textarea AND attach the image alongside.
- **Caps are 8 attachments and 8 MiB per file**, client-side
  (`attachments/limits.ts:9-10,26-27`), with the message naming the file and
  the limit it broke. A 64×64 PNG is ~200 bytes; nowhere near. The server has
  its own separate ceilings (`hubcore.SendMaxImageBytes` is 8 MiB and `SendMaxRequestBytes` is 96 MiB,
  `internal/hubcore/types.go:13-14`, enforced in `web_session.go:29-49`) — don't conflate
  the two when a 413 shows up.
- **claude-haiku-4-5-20251001 sometimes tries `read_file` on the literal
  string "image"** before describing it. The call fails, the model falls back
  to the inline image, and the description appears via `communicate` in the
  same turn. gpt-5.5 skips this hop.
- **OpenAI ChatGPT-OAuth tokens** may reject image inputs depending on the
  OAuth scope. If `gpt-5.5` returns `unsupported content type` or refuses,
  fall back to `claude-haiku-4-5-20251001`.
