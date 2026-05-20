# tui-paste-image-from-clipboard: Ctrl+V attaches a clipboard image in serf-tui

**What this covers**: kata `2frx` (live e2e for image attachments) over
the TUI clipboard pipeline — kata `c7pv` (the multi-source clipboard
read + WSL fallback in `clipboard_system.go`), kata `xy3t` (the
composer chip rendering + Ctrl+V keybind in `hub_model.go`), and
kata `re91` (submit/queue/drain-as-steer carrying attachments). When
the user is in the SESSION composer and the system clipboard holds a
PNG/JPEG/etc., Ctrl+V (or Ctrl+Alt+V) reads the bytes, writes a
`serf-clipboard-*.png` temp file, and pushes a `PastedImage` onto
`pendingAttachments`. The next `enter` send/queue ships those bytes
through the daemon to the model as a `ContentImage` part.

Companion scenarios: `web-paste-image-from-clipboard.md`,
`web-drag-drop-image.md`, `web-file-picker-image.md`,
`tui-paste-image-path.md`.

## Pre-state

- `tmux` installed (tested on tmux 3.4).
- `serf-hub` reachable on `127.0.0.1:9180`. Token at
  `~/.serf/auth-token`.
- `./serf-tui` and `./serf-hub` built in repo root.
- `anthropic/claude-haiku-4-5-20251001` (or `openai/gpt-5.5`)
  reachable through configured credentials; both accept image
  inputs.
- A display server reachable from the TUI process AND a clipboard
  tool serf-tui knows about — one of:
  - X11 with `xclip` on PATH (handled by
    `clipboard_system.go:readImageBytesX11`).
  - Wayland with `wl-paste` on PATH (`readImageBytesWayland`).
  - macOS with `osascript` (`readImageBytesMacOS`).
  - WSL with `powershell.exe` reachable (the
    `ReadWindowsClipboardViaPowerShell` fallback in
    `clipboard_system.go`).

  On a headless Linux host without an X server, start `Xvfb`
  first and export `DISPLAY` into the TUI's environment.
- No leftover tmux session named `serf-e2e-clip`.

## Steps

1. **Build a fixture PNG** and seed the system clipboard. The fixture
   needs to live on disk only long enough to load into the clipboard
   — the TUI re-saves the bytes into its own temp file on paste:
   ```bash
   FIXDIR=$(mktemp -d -t serf-e2e-clip-XXXX)
   convert -size 64x64 xc:red "$FIXDIR/red.png"
   # Linux / X11 — for Wayland use: wl-copy --type image/png < red.png
   # For macOS use: osascript -e 'set the clipboard to (read POSIX file "/path/red.png" as «class PNGf»)'
   xclip -selection clipboard -t image/png -i < "$FIXDIR/red.png"
   xclip -selection clipboard -t TARGETS -o    # should list "image/png"
   ```

2. **Prepare a hermetic workdir and spawn a dormant session.** A
   single trivial-first-turn session gives us a real
   `state=idle` to navigate into. The model + working-dir are
   chosen so the assistant's first message is short and the test
   spends most of its time on the image turn:
   ```bash
   WORKDIR=$(mktemp -d -t serf-tui-clip-work-XXXX)
   TOKEN=$(cat ~/.serf/auth-token)
   HUB=http://localhost:9180
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"reply with the literal word: ready\",\"harness\":\"serf\",\"model\":\"anthropic/claude-haiku-4-5-20251001\",\"working_dir\":\"$WORKDIR\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     "$HUB/api/spawn")
   SID=$(echo "$resp" | python3 -c "import json,sys; print(json.load(sys.stdin)['session_id'])")
   for i in $(seq 1 30); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" \
              "$HUB/api/sessions/local:$SID" \
              | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 1
   done
   ```

3. **Launch serf-tui in tmux** with the display server visible to its
   subprocess (else `xclip` will return `ErrClipboardUnavailable`):
   ```bash
   tmux new-session -d -s serf-e2e-clip -x 200 -y 50 \
     "DISPLAY=:99 ./serf-tui --hub-addr 127.0.0.1:9180 --debug"
   sleep 1
   ```
   (On native X11 with `DISPLAY=:0` already set, omit the `DISPLAY=:99`
   prefix.)

4. **Navigate into the spawned session** via the command palette
   (filtering on the SID suffix is enough to make the row unique):
   ```bash
   tmux send-keys -t serf-e2e-clip "/"
   sleep 0.3
   tmux send-keys -t serf-e2e-clip -l "$SID"
   sleep 0.4
   tmux send-keys -t serf-e2e-clip Enter
   sleep 0.5
   tmux capture-pane -t serf-e2e-clip -p | head -20
   ```
   The captured pane should show
   `serf / session / <SID>` in the title bar and a
   `composer: message` footer.

5. **Press Ctrl+V** to paste the clipboard image:
   ```bash
   tmux send-keys -t serf-e2e-clip C-v
   sleep 0.5
   tmux capture-pane -t serf-e2e-clip -p | head -30
   ```
   The footer should now include an `attachments` row with a chip
   like `📎 serf-clipboard-3431062677.png [×]`. The composer textarea
   should still be empty — Ctrl+V did NOT insert characters into the
   text input.

6. **Type a prompt and submit**:
   ```bash
   tmux send-keys -t serf-e2e-clip -l "describe this image in one sentence"
   sleep 0.3
   tmux send-keys -t serf-e2e-clip Enter
   sleep 8    # haiku-4-5 typically replies within a few seconds
   tmux capture-pane -t serf-e2e-clip -p | head -35
   ```

7. **Cross-check the transcript on disk**:
   ```bash
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

- **Step 5 (chip render)**: pane shows a chip
  `📎 serf-clipboard-<n>.png [×]` beneath the composer textarea.
  The textarea body is empty (Ctrl+V did not insert character bytes).
  Falsification:
  - Pane shows `Clipboard paste failed: …` system-line — likely
    `ErrClipboardUnavailable` (no display server / wrong DISPLAY)
    or `ErrNoClipboardImage` (the clipboard doesn't actually hold
    an image; re-seed via xclip).
  - The textarea contains `^V` or `\x16` — the Ctrl+V keybind isn't
    being interpreted by the TUI (terminal or tmux mode issue —
    confirm tmux `default-terminal` supports xterm escape sequences).
- **Step 6 (turn settles to idle)**: post-send pane shows the user
  message, possibly a brief `read_file` tool call probing the
  literal "image" path (claude-haiku-4-5 quirk; harmless), then a
  `communicate` whose body describes the image. State returns to
  `idle`. Falsification: state stays `active` for >30 s with no
  output, OR the assistant text describes a different colour /
  shape, OR the model refuses with `I cannot see images`.
- **Step 7 (transcript)**: at least one `USER_INPUT` row has a
  `kind=image` part with `image.media_type=image/png` and a
  non-empty `image.data` blob. The assistant's `communicate` call
  references the image content. Verified live (2026-05-18) against
  `claude-haiku-4-5-20251001` producing `"The image is a solid
  bright red square."`

## Cleanup

```bash
TOKEN=$(cat ~/.serf/auth-token)
curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" -d '{}' \
  "http://localhost:9180/s/$SID/shutdown" >/dev/null
tmux kill-session -t serf-e2e-clip 2>/dev/null
rm -rf "$FIXDIR" "$WORKDIR"
# Optional, for hermeticity:
# find ~/.local/state/serf/projects -name "$SID*" -delete
```

## Sharp edges

- **`DISPLAY` must be exported into the TUI's process**, not just the
  shell that called `tmux new-session`. The session-creation form
  `tmux new-session -d -s … "DISPLAY=:99 ./serf-tui …"` does this.
  If you instead `tmux new-session -d -s … "./serf-tui …"`, the
  process inherits the controlling terminal's environment — which
  in a sandboxed agent context often lacks `DISPLAY`, and `xclip
  -selection clipboard -t image/png -o` will fail with
  `Error: Can't open display: (null)`. The TUI surfaces this as
  `Clipboard paste failed: clipboard unavailable`.
- **`xclip` and an X server are an Ubuntu-host need.** On native
  X11 desktops both come with the user's environment; on a
  headless CI host install `xclip` via apt and run `Xvfb :99
  -screen 0 800x600x24 &` first. On macOS / Wayland use the
  matching path (osascript / wl-paste); the
  `clipboard_system.go` GOOS branches handle all three.
- **The TUI re-saves bytes to its own temp file.** The chip's
  filename is `serf-clipboard-<n>.png`, not whatever you `xclip`'d.
  That's intentional — the TUI's paste path always re-encodes via
  Go's `image/png` so JPEG-from-clipboard, colour-profile
  weirdness, and EXIF strip away cleanly. Cleanup happens on
  send success (`re91`); a paste-then-quit leaves the temp file
  behind under `os.TempDir()`.
- **Ctrl+V works only in the SESSION composer, not the SPAWN form.**
  The handler lives in
  `cmd/serf-tui/hub_model.go:handleSessionInput` (line ~1454).
  The spawn-form text field has no paste-clipboard binding; for
  the "attach image at spawn time" UX, use the web `/new` form
  (covered by `web-file-picker-image.md`,
  `web-paste-image-from-clipboard.md`,
  `web-drag-drop-image.md`).
- **Ctrl+Alt+V is the WSL-friendly alias** (`isAltVKey`). On WSL
  the Windows Terminal swallows plain Ctrl+V for its own paste
  handling before it reaches the TUI; Ctrl+Alt+V is reserved for
  the TUI to pick up.
- **claude-haiku-4-5-20251001 sometimes calls `read_file` on the
  literal string "image"** before the description. The call fails
  (`open …/image: no such file or directory`), then the model falls
  back to the inline content and describes the image via
  `communicate`. gpt-5.5 skips this hop. The transcript will show
  a benign tool-call/tool-result pair before the `communicate`.
- **Auth + provider tax.** This scenario burns real API tokens (one
  trivial setup turn and one image-describe turn).
- **No CSP issue here.** The CSP kata `1pgw` affects ONLY the
  browser composer (the TUI never loads images through a
  blob:-URL'd Image element). Even on a host with the older CSP,
  this scenario passes.
