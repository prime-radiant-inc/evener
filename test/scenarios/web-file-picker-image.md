# web-file-picker-image: attach an image through the file picker, on both doors

**What this covers**: kata `2frx` (live e2e for image attachments) over
the kata `65mm` composer-attachments wiring. A hidden
`input[type="file"][accept="image/*"][multiple]` accepts an image, the
handler re-encodes it to PNG through an offscreen canvas, the staged
attachment renders, and on submit the appwire `thread/start` /
`turn/start` carries the bytes through to the agent as a `ContentImage`
part on the `USER_INPUT` message.

There are **two** file-picker doors and they share one implementation
(`useAttachments`, `panes/session/composer/attachments/useAttachments.ts`):

| Door | Trigger | Hidden input | Staged as |
|---|---|---|---|
| Spawn pane (`/new`) | `[data-testid="spawn-attach"]` (`panes/spawn/Spawn.tsx:543-551`) | `Spawn.tsx:568` | a `Chip` labelled with the filename (`Spawn.tsx:570-578`) |
| Session composer | `[data-testid="composer-attach"]` (`panes/session/composer/Composer.tsx:807`) | `Composer.tsx:886` | an `AttachmentTile` thumbnail + `W×H` overlay (`AttachmentTile.tsx:52-96`) |

Both legs are worth running: the spawn door is the one this card was
written for, and the composer door is where the dimension readout
actually lives.

Companion scenarios: `web-drag-drop-image.md`,
`web-paste-image-from-clipboard.md`, `tui-paste-image-from-clipboard.md`,
`tui-paste-image-path.md`.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map is where these hooks are maintained. Everything this card
used to name is gone with the vanilla frontend (`660376f78`):
`form[data-spawn-form]`, `[data-attach-trigger]`, `[data-file-picker]`,
`[data-composer-attachments]`, `[data-attachment]`,
`input[name="model"]`, `textarea[name="prompt"]`, `.spawn-btn`,
`window.SerfAppwire`. The spawn pane has no `<form>` element at all and
no hidden `name=model` input — the model is set through the shared ARIA
combobox, as a real gesture.

## Pre-state

- `serf-hub` running on an isolated `$HOME` and a kernel-assigned port
  (never `9180`, Jesse's real one — see the Setup checklist in
  `docs/agentic-testing.md`). Token at `$HOME/.serf/auth-token`.
- A frontend built with `make build-web` **before** the hub binary. A
  checkout that never ran it ships a one-line `frontend/dist/PLACEHOLDER`
  and serves no app (rebuild matrix item 3 in the runbook).
- A vision-capable model reachable through configured credentials
  (`anthropic/claude-haiku-4-5-20251001` and `openai/gpt-5.5` both
  accepted image inputs when this was recorded, 2026-05-18).
- `superpowers-chrome:browsing` (`use_browser` MCP) available. Claim your
  own Chrome profile with `set_profile <worktree-or-branch-name>` as the
  first `use_browser` call of the run (kata `8ecz`).
- `convert` (ImageMagick) or any tool that can produce a tiny PNG.

## Steps

1. **Build the fixture PNG.** A 64×64 solid-red PNG is enough — it
   travels in any path and any model can describe it succinctly, so the
   assistant response is easy to falsify:
   ```bash
   FIXDIR=$(mktemp -d -t serf-e2e-img-XXXX)
   convert -size 64x64 xc:red "$FIXDIR/red.png"
   file "$FIXDIR/red.png"     # PNG image data, 64 x 64
   ```

### Leg A — the spawn door

2. **Open the spawn pane.** `/auth` sets the cookie then redirects, so
   later navigations don't 401:
   ```
   action: navigate
   payload: $HUB/auth?token=<TOKEN>&next=/new
   ```
   Confirm it mounted:
   ```
   action: eval
   payload: ({
     port:    location.port,
     card:    !!document.querySelector('[data-testid="spawn-prompt-card"]'),
     attach:  !!document.querySelector('[data-testid="spawn-attach"]'),
     picker:  !!document.querySelector('input[type="file"][accept="image/*"]'),
     submit:  !!document.querySelector('[data-testid="spawn-submit"]'),
   })
   // expect every field true (and the port to be YOUR hub's)
   ```

3. **Upload the fixture through the hidden input.** Target the input
   itself, never the paperclip — clicking `spawn-attach` calls
   `fileInputRef.current?.click()` (`Spawn.tsx:550`), which opens a
   native OS dialog nothing can drive:
   ```
   action: file_upload
   selector: input[type="file"][accept="image/*"]
   payload: {"files": ["/tmp/serf-e2e-img-…/red.png"]}
   ```
   The PNG re-encode is asynchronous (canvas round-trip) — give it a
   moment, then read the staged state:
   ```
   action: eval
   payload: new Promise(r => setTimeout(r, 300)).then(() => ({
     removeButtons: [...document.querySelectorAll('button[aria-label^="Remove "]')]
                      .map(b => b.getAttribute("aria-label")),
     chipText: [...document.querySelectorAll('span')]
                 .map(s => s.textContent.trim())
                 .filter(t => t === "red.png" || t === "red.png (processing…)"),
     promptText: document.querySelector('[aria-label="Prompt"]')?.value,
   }))
   ```

4. **Set the model and prompt, then submit.** The model control is the
   shared combobox: click its trigger (the `<button>` whose accessible
   name ends `— change model`, `widgets/modelCatalog/index.tsx:380-398`),
   type the exact qualified id into the panel's
   `input[role="combobox"][aria-label="Model"]`, and press Enter —
   typing sets the active row to the first match, and Enter picks it
   (`index.tsx:231-236,204-218`). Then type the prompt into
   `[aria-label="Prompt"]` (append after the `[image 1]` marker; don't
   overwrite it — see Sharp edges) and click:
   ```
   action: click
   selector: [data-testid="spawn-submit"]
   ```

5. **Capture the new session ref** from the URL the browser landed on:
   ```
   action: eval
   payload: decodeURIComponent(location.pathname)   // "/s/local:<SID>"
   ```
   `paneToURL` percent-escapes the ref (`shell/routing.ts:93-96`), so
   the raw `location.pathname` reads `/s/local%3A<SID>`; decode before
   parsing. Take `SID` to the shell for the rest.

### Leg B — the composer door (same machinery, richer staging UI)

6. Staying on `/s/local:<SID>`, repeat the upload against the composer's
   own hidden input, then read the tile:
   ```
   action: file_upload
   selector: input[type="file"][accept="image/*"]
   payload: {"files": ["/tmp/serf-e2e-img-…/red.png"]}
   ```
   ```
   action: eval
   payload: new Promise(r => setTimeout(r, 300)).then(() => ({
     view:       document.querySelector('button[aria-label^="View "]')?.getAttribute("aria-label"),
     remove:     document.querySelector('button[aria-label^="Remove "]')?.getAttribute("aria-label"),
     thumbSrc:   (document.querySelector('button[aria-label^="View "] img')?.src || "").slice(0, 30),
     dimensions: [...document.querySelectorAll('div')]
                   .map(d => d.textContent.trim()).filter(t => /^\d+×\d+$/.test(t)),
     message:    document.querySelector('[aria-label="Message"]')?.value,
   }))
   ```
   Then type a describe-the-image prompt and click
   `[data-testid="composer-submit"]`.

### Verification (browser-free)

7. Poll to idle and read the transcript:
   ```bash
   for i in $(seq 1 60); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" \
       "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$state" = "idle" ] && break
     sleep 2
   done
   go run ./cmd/serf-doctor transcript "$SID" --state-dir "$state_dir" \
     --format outline --range last:30
   TFILE=$(find "$HOME/.local/state/serf/projects" -name "$SID.transcript.jsonl")
   jq -c 'select(.turn.kind=="USER_INPUT")
          | {kind: .turn.kind,
             parts: [.turn.message.content[]
                     | {kind, media: .image.media_type, bytes: (.image.data|length),
                        text: (.text // "" | .[0:60])}]}' "$TFILE"
   ```
   Field shapes: `schema.Turn` (`agent/schema/turn.go:123-126`),
   `llm.ContentPart` with `kind` selecting the payload
   (`llm/types.go:122-135`), `llm.ImageData.media_type`/`data`
   (`llm/types.go:137-143`), `TurnUserInput = "USER_INPUT"`
   (`agent/schema/turn.go:16`), `ContentImage = "image"`
   (`llm/types.go:35`). This is a byte-level structural read, which is
   what raw JSONL is for; use the `serf-doctor` outline above for
   comprehension.

## Expected

- **Step 3 (spawn staging)**: exactly one `Remove red.png` button
  (`Chip`'s remove label, `widgets/chip/index.tsx:38-40,49`), the chip
  text settles from `red.png (processing…)` to `red.png`
  (`Spawn.tsx:574`), and `promptText` contains the marker `[image 1]`
  spliced in at the cursor (`markerText`,
  `attachments/textareaMarkers.ts:19-21`). Falsify: no remove button
  (the picker isn't wired — the kata 65mm regression), or the chip is
  still `(processing…)` after a couple of seconds (the canvas re-encode
  never settled — check the CSP, first Sharp edge).
- **Step 4/5 (submit)**: the pane navigates away from `/new`;
  `decodeURIComponent(location.pathname)` matches `^/s/local:.{22}$`
  (`SID` is a 22-character UUIDv7 base62 payload). Falsify: it stays on
  `/new` with an error toast in
  `section[aria-live="polite"][aria-label="Notifications"]` — most
  usefully `Image attachment is still processing.` (`Spawn.tsx:434`,
  the `hasPending` submit gate) or a `Spawn failed: …` line.
- **Step 6 (composer staging)**: `view` is `View red.png`, `remove` is
  `Remove red.png`, `thumbSrc` starts `data:image/png;base64,`
  (`AttachmentTile.tsx:59`), and `dimensions` contains `64×64`
  (`AttachmentTile.tsx:80-84`). Falsify: dimensions absent while the
  thumbnail is present (the decode produced no width/height), or a
  `role="img"` placeholder labelled `red.png (still processing)` that
  never resolves (`AttachmentTile.tsx:67`).
- **Step 7 (the actual contract)**: at least one `USER_INPUT` turn whose
  `message.content` holds both a `kind=text` part carrying the prompt
  and a `kind=image` part with `media_type: "image/png"` and a
  non-empty `data` blob — one per leg, so two after both. A later
  assistant turn describes the image (the words `red`, `square`, or a
  synonymous colour/shape; both gpt-5.5 and claude-haiku-4-5 produced
  "red square" / "bright red square" during recording). Falsify: no
  image part on `USER_INPUT` (the staged items never reached the
  daemon), or the assistant describes a different colour/shape or
  refuses (`I cannot see images`).

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" -d '{}' \
  "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
rm -rf "$FIXDIR"
```

Kill the hub by the PID you captured and remove your `$run` dir. Note
the namespace: `/api/sessions/local:$SID/shutdown`. The old
`$HUB/s/$SID/shutdown` form-POST shim was deleted with the vanilla
frontend and now 404s silently, leaving the daemon running — see "The
REST surface, and what is no longer on it" in the runbook.

## Sharp edges

- **CSP `blob:` requirement** (kata `1pgw`). `reencodeToPng`
  (`panes/session/composer/attachments/encodePng.ts`) calls
  `URL.createObjectURL(blob)` and loads the result into an `Image`
  element to derive width/height before re-encoding to PNG. Without
  `blob:` in `img-src`, `Image.onerror` fires and every attachment is
  rejected. The directive lives at
  `cmd/serf-hub/internal/httpsec/httpsec.go:40` (`img-src 'self' data:
  blob: https:`) — relocated from the deleted `security.go`, and its own
  comment records the kata. **The user-visible symptom changed with the
  rewrite**: the failure is no longer an inline "Not an image: <name>"
  banner (that string is stale prose in the Go comment) but a toast
  reading `Couldn't attach red.png (image decode failed)` —
  `useAttachments`' decode `.catch` composes `<name> (image decode
  failed)` and the caller prefixes `Couldn't attach `
  (`useAttachments.ts` ingestFiles' catch + rejection join). Grep the
  toast region, not for the old banner.
- **`file_upload` targets the input, not the trigger.** `use_browser`'s
  `file_upload` drives CDP `Input.setFileInputFiles`, which sets `.files`
  on the node and dispatches `change` — exactly what
  `handleFilePicker`/`handleFilePickerChange` listen for. Clicking the
  paperclip instead opens a native dialog no driver can fill. There is
  exactly one `input[type="file"][accept="image/*"]` per page, so the
  bare attribute selector is unambiguous on both `/new` and a session
  page.
- **Don't overwrite the textarea; append to it.** `ingestFiles` splices
  an `[image N]` marker into the composer text synchronously at the
  cursor, before any async work, and the store treats that text as
  authoritative (`useAttachments.ts` ingestFiles). Blowing the textarea
  away with a wholesale value-set strips the marker. Also note the
  textarea is React-controlled: assigning `el.value` directly is
  silently reverted by React's controlled-input restoration the moment
  the file input's own `change` event fires — the hook's `TextEditor`
  seam exists specifically because of that, and it is why the driver
  should type rather than assign.
- **Limits are 8 images and 8 MiB per file, enforced on both sides.**
  Client: `rejectionReason`
  (`panes/session/composer/attachments/limits.ts`, `MAX_ATTACHMENTS = 8`,
  `MAX_ATTACHMENT_BYTES = 8 * 1024 * 1024`), which also rejects any
  non-image outright and surfaces `Couldn't attach <name> …`. Server:
  `validateAppWireInputItems`
  (`cmd/serf-hub/appwire_validation.go:10-28`) against
  `hubcore.SendMaxImageItems = 8` / `SendMaxImageBytes = 8 MiB` /
  `SendMaxRequestBytes = 96 MiB`
  (`cmd/serf-hub/internal/hubcore/types.go:12-14`). The old card's
  "12 MB per image / 40 MB per request" and its `web.go:sendMax*Bytes`
  citation were both wrong; there is no such symbol. Note the status
  codes differ by route: the browser goes over appwire, where an
  oversize payload comes back as a wire `InvalidParams` (`-32602`)
  toast (`app_threadlifecycle.go:37-39`), while `POST /api/spawn` returns
  a plain-text **413** (`web_spawn.go:74-77`). A 64×64 PNG is nowhere
  near either.
- **Staging looks different on the two doors, deliberately.** The spawn
  pane renders a text `Chip` (filename only, no dimensions); the
  composer renders an `AttachmentTile` (thumbnail, `W×H` overlay,
  lightbox on click). A dimensions assertion belongs on leg B only.
- **claude-haiku-4-5-20251001 sometimes tries `read_file` on the string
  "image" before describing it** — an artefact of the tool-use loop
  preferring to "look at the file" over trusting inline content. The
  call fails (no such file), then the model describes the inline image
  in the same turn. gpt-5.5 skips this hop. Not a failure as long as a
  later message carries the right description.
- **OpenAI ChatGPT-OAuth tokens may reject image inputs depending on the
  OAuth scope.** If `gpt-5.5` returns `unsupported content type` or
  refuses, fall back to `anthropic/claude-haiku-4-5-20251001`.
