# Image attachments for serf composer (TUI + web)

**Status**: draft — pending kata filing + SDD implementation.

**Goal**: let the user attach images to a turn from either the TUI or
the web composer. Three primary entry surfaces:

1. **Clipboard paste** (Ctrl+V or Ctrl+Alt+V in TUI; Ctrl/⌘+V in web).
2. **Drag-and-drop** (web only — terminals don't have drag-drop).
3. **File picker / typed path** (both: a button on web; pasted-path
   detection in TUI).

The data model + wire format already exist in serf
(`llm.ImageData`, `appwire.InputItem`, `agent` tool-result image
plumbing). This spec covers the composer-side surfaces that today
have no image-attachment UX.

## Current state (verified by code reading)

- **`llm/types.go`** has `ContentImage` content kind + `ImageData{Data,
  MediaType}` content part.
- **`agent/session.go:1024-1031`** already builds image content parts
  from tool results' `ImageData`.
- **`appwire.InputItem`** (`internal/appwire/types.go:256`) has
  `Type`/`MediaType`/`Data`/`Path`/`URL` fields suitable for image
  payloads on the wire.
- **`ThreadStartParams` / `TurnStartParams`** carry `Items
  []InputItem` (line ~244 + ~344).
- **`cmd/serf-hub/web.go:3394`** has an `Items` append path on
  `/turn` — needs verification it threads image bytes correctly.
- **`cmd/serf-hub/assets/renderer.js:1311`** already filters input
  items by `kind === "file" && type.startsWith("image/")` for
  history rendering. No composer-side input flow yet.

**What's missing**: the composer/UI surfaces to PRODUCE these
`InputItem` entries from a user gesture.

## Research: how codex does it (TUI clipboard paste)

`inspo/codex/codex-rs/tui/src/clipboard_paste.rs` is the reference
implementation. Key design choices we adopt:

### Multi-source clipboard read

Image content on the clipboard can arrive two ways:

- **File list** (Finder/Explorer copies a `.png` file — clipboard has
  a path, not bytes).
- **Image data** (Chrome/screenshot tools — clipboard has raw bytes).

The implementation tries the file list first, then falls back to
image data. If both are present, files win (less re-encoding).

### Cross-platform clipboard access

Codex uses the `arboard` Rust crate which abstracts:
- macOS: `NSPasteboard`
- X11 / Wayland: `xcb`/`wayland-client`
- Windows: `OpenClipboard` / `GetClipboardData`

**Go equivalent**: `golang.design/x/clipboard` (v0.7+). Same coverage,
similar API, supports image format (`clipboard.Read(clipboard.FmtImage)`).
On Linux it requires `libx11-dev` at build time but ships with both
X11 and Wayland support. Does not natively support reading file
lists from the clipboard — see workaround below.

### WSL fallback (the "clever multi-platform magic")

On native Linux, arboard talks to the X server. **Inside WSL**, the
X server is the Linux side; the user's actual clipboard (the one
they pressed Ctrl+C in Chrome on Windows) lives on the Windows side.
arboard can't reach it.

Codex's fix (`try_wsl_clipboard_fallback`):
1. Detect WSL via `is_probably_wsl()` (`/proc/version` contains
   "Microsoft" or "WSL").
2. Shell out to PowerShell (`powershell.exe`, `pwsh`, or
   `powershell`) with this script:
   ```powershell
   [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
   $img = Get-Clipboard -Format Image
   if ($img -ne $null) {
     $p = [System.IO.Path]::GetTempFileName()
     $p = [System.IO.Path]::ChangeExtension($p, 'png')
     $img.Save($p, [System.Drawing.Imaging.ImageFormat]::Png)
     Write-Output $p
   } else { exit 1 }
   ```
3. Parse the printed Windows temp path.
4. Map `C:\Users\...\AppData\Local\Temp\foo.png` → `/mnt/c/Users/.../AppData/Local/Temp/foo.png`.
5. Read the file from the WSL mount.

We replicate this verbatim in Go using `os/exec` + path conversion.

### Re-encoding to PNG

Codex always re-encodes pasted clipboard image data to PNG using
the `image` crate. Reasons: JPEG-from-clipboard frequently has
unexpected color profiles; PNG is universally accepted by all the
LLM providers; size penalty is small for screenshots.

Go equivalent: standard library `image/png` + the source decoder
(e.g. `image/jpeg`). For unknown formats the Go `image` package's
`image.Decode` auto-detects via registered decoders.

### Temp file vs in-memory

Codex writes to a temp file with `tempfile::Builder` and reads
later when serializing for the API call. The intermediate temp
file is useful because:
- Lets the user PREVIEW the image (we render a small inline notice
  in the composer with filename + dimensions).
- Lets the user delete attachments before submit without recoding.
- The session loop reads the file on submit, so a slow paste
  doesn't block the UI.

We adopt this. Temp files go under `os.TempDir()` with prefix
`serf-clipboard-` and suffix `.png`. They're cleaned up:
- on successful submit (after the API call accepts the bytes)
- on session shutdown
- on TUI quit

### Pasted-path detection

Codex's `normalize_pasted_path()` recognizes:
- `file://` URLs (RFC 8089)
- Quoted paths (`"…"`, `'…'`)
- Shell-escaped single paths (`shlex` lib)
- Windows paths (`C:\…`, `\\server\share\…`) + WSL conversion

When the pasted TEXT looks like a path AND the file exists AND has
an image extension (`.png`/`.jpg`/`.jpeg`/`.gif`/`.webp`), treat
it as an image attachment instead of literal text in the composer.

We replicate this — minus the shlex-escaped variant for v1.
`encoding/url` handles `file://`. We add manual path-shape detection.

### Model capability check

Codex checks `current_model_supports_images()` before attaching. If
the active model doesn't (e.g. older completion-only models),
attaches are rejected with an inline warning. The composer state is
preserved (the typed text doesn't disappear).

We adopt this. The launch-check catalog already returns model
metadata; add a `supports_images` field if missing.

## Design

### Data model — already mostly in place

```go
// appwire.InputItem (existing)
type InputItem struct {
    Type      string            `json:"type"`       // "image" for image attachments
    MediaType string            `json:"mediaType,omitempty"`  // "image/png"
    Data      []byte            `json:"data,omitempty"`       // base64-encoded over the wire
    Path      string            `json:"path,omitempty"`       // local-only; not sent to model
    Name      string            `json:"name,omitempty"`       // display name in composer + transcript
    Metadata  map[string]string `json:"metadata,omitempty"`   // width, height
}
```

For images:
- `Type` = `"image"` (new convention)
- `MediaType` = `"image/png"` (canonical after re-encode) or
  upstream MIME for path-pastes
- `Data` carries the raw bytes for in-flight transport
- `Path` carries the temp file path locally (so the composer can
  reload / preview / show a delete button without re-reading
  bytes)
- `Metadata["width"]`, `Metadata["height"]` from decode

### Wire protocol additions

`appwire.TurnStartParams` already has `Items []InputItem` — verify
the daemon-side accepts type=image and turns it into a content part
in the user message. (May need a small handler update in
`cmd/serf/serve.go` or `agent/session.go`.)

For queued messages: `appwire.MethodTurnQueue` (added by 111a)
needs to accept items in addition to text. Add `Items []InputItem`
to its params.

For drain-as-steer: same — `MethodTurnDrainAsSteer` should drain
ALL queued items (text + images) as a steer event. The steer event
content kind list already supports images.

### TUI composer

New file: `cmd/serf-tui/clipboard_paste.go`.

```go
type PastedImage struct {
    Path        string  // temp file
    MediaType   string  // "image/png"
    Width, Height int
    Size        int     // bytes
    Origin      string  // "clipboard"|"path"|"wsl"
}

// PasteClipboardImage reads the system clipboard, writes a temp
// PNG, and returns metadata. Linux falls back to WSL PowerShell
// if the X clipboard is unavailable.
func PasteClipboardImage() (*PastedImage, error)

// NormalizePastedPath recognizes file://, quoted paths, raw
// Windows/WSL paths. Returns the resolved local path or "" if
// the text doesn't look like a path.
func NormalizePastedPath(text string) string

// IsImageFile checks the extension against a known list.
func IsImageFile(path string) bool
```

Keybind: **Ctrl+V** for clipboard image paste. (Codex also offers
Ctrl+Alt+V for WSL where Ctrl+V is captured by Windows; we add the
same.) Implemented in `hub_model.go`'s key dispatcher.

Composer panel (`composer_panel.go`):
- Below the textarea, before the queue preview, render an
  "Attachments" row:
  ```
  📎 screenshot-2026-05-18-104332.png (412×512)  [×]
  📎 paste-from-clipboard.png (1024×768)         [×]
  ```
- `[×]` is a clickable / focus-tab-target remove button.

Composer state additions:
- `pendingAttachments []*PastedImage` — list to ship with the next
  send/queue/steer.

Send flow:
- On submit, build the `[]InputItem` from `pendingAttachments` AND
  the textarea text (text becomes the first InputItem with
  `Type=text`, images follow). Clear pending after success.

Pasted-path detection:
- When the composer receives a bracketed-paste containing text
  that matches `NormalizePastedPath` + the file exists + has image
  extension: attach instead of inserting into textarea. Show
  inline notice "Treated pasted path as image attachment."

### Web composer

`cmd/serf-hub/assets/renderer.js`:

Clipboard paste:
- `textarea.addEventListener('paste', ...)` — inspect
  `e.clipboardData.items` for `item.type.startsWith('image/')`.
- Browser already returns the image blob; no platform branching.
- Convert to PNG via canvas (if not already PNG) for parity with
  TUI.
- Push to `pendingAttachments` array in composer state.

Drag-and-drop:
- `composer.addEventListener('dragover', ...)` to show drop zone.
- `composer.addEventListener('drop', ...)` to handle file drops.
- Same path: read blob, decode, re-encode to PNG, push to pending.

File picker:
- Button next to send: "Attach image" → `<input type="file"
  accept="image/*" multiple>`. Same path: blob → PNG → pending.

Submit flow:
- `appwire.startTurn()`, `appwire.queueTurn()`, `appwire.steer()`
  all need to accept attachments. Encode bytes as base64 in the
  JSON payload (browsers can't send Go-style `[]byte` directly).

Server-side decode at REST handler: base64 → []byte → InputItem.

CSS:
- Attachment chips below the textarea, similar to TUI layout but
  with thumbnails (data URL).

### /new spawn form

`cmd/serf-hub/assets/spawn.js`:
- Same paste / drag-drop / file picker on the prompt textarea.
- `pendingAttachments` survives into the `/api/spawn` request body
  as a new `items` field.

`spawnRequest` struct in `web.go`:
- Add `Items []InputItem ` json:"items,omitempty"` field.
- Translate to `ThreadStartParams.Items` when calling
  `hubThreadStart`.

## Out of scope (v1)

- **Non-image attachments** (PDF, code files). Different model
  surface; revisit.
- **Image editing / cropping** in the composer.
- **Pasted-image gallery** to re-attach previously-pasted images.
- **Maximum file size enforcement** beyond a hard cap (10 MB?). All
  three transports (clipboard, drag-drop, picker) hit this same
  limit.
- **Conversion of unsupported formats** (HEIC, SVG, RAW). Either
  the Go `image` package handles it or we reject with a clear
  error.
- **Multi-image batch operations** beyond "attach N, send" — no
  reordering UI, no individual edit.
- **Android-style "share to serf"** — irrelevant for the desktop
  TUI/web product.

## Test plan

Per the user's instruction: SDD + strict red-green TDD.

Each implementation kata starts by writing a failing test, then
making it pass with the minimum code. Reviewers verify the test
was actually failing before the implementation landed.

For TUI clipboard handling:
- Use a faked clipboard reader (interface) injected into the paste
  function. Test the file-list-first preference, the image-data
  fallback, the path normalization, the WSL detection branch.
- The actual `golang.design/x/clipboard` call only runs in
  production code paths.

For web:
- jsdom-based jstest with mocked `navigator.clipboard.read()` and
  DataTransfer.

End-to-end:
- Live scenarios under `test/scenarios/`:
  - `tui-paste-image-from-clipboard.md`
  - `web-paste-image-from-clipboard.md`
  - `web-drag-drop-image.md`
  - `web-file-picker-image.md`
  - `tui-paste-image-path.md` (typing/pasting a file path)

## Risks

- `golang.design/x/clipboard` adds a CGo dependency on Linux
  (libx11-dev / libwayland-client). Cross-compiling becomes more
  involved. Worth verifying the bundled-prebuilt path before
  committing.
- WSL detection via `/proc/version` is heuristic; might false-
  positive on some Linux distros that mention "Microsoft" in
  unrelated contexts (unlikely but worth a test).
- PowerShell fallback requires `powershell.exe` on PATH from within
  WSL. WSL2 default install includes Windows PATH but users can
  break it.
- The `image` Go package's decoders cover PNG/JPEG/GIF natively;
  WebP needs `golang.org/x/image/webp` (separate import). HEIC is
  out — Apple's encoder.

## Implementation plan (for the kata wave)

1. **Backend wire support** — verify `Items []InputItem` round-trips
   through `MethodTurnStart`, `MethodTurnQueue`, `MethodTurnDrainAsSteer`,
   and `/api/spawn`. Add tests if needed.
2. **TUI clipboard paste primitives** — `clipboard_paste.go` with
   the multi-platform clipboard read, PNG re-encode, temp file,
   WSL fallback. Pure-function tests with fake clipboard.
3. **TUI composer UI** — attachment chips, remove buttons, Ctrl+V
   keybind, pasted-path detection.
4. **TUI submit flow** — pendingAttachments → InputItem on send /
   queue / drain-as-steer.
5. **Web composer paste handler** — `paste` event in textarea +
   spawn form.
6. **Web composer drag-drop + file-picker** — additional surfaces.
7. **Web submit flow** — base64 encode + decode on REST side.
8. **Live scenarios** for each affordance.

Each step is a kata. Each kata is implemented with strict red-green
TDD via SDD.
