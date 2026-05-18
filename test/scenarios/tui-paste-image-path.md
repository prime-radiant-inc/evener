# tui-paste-image-path: bracketed-paste of a PNG path becomes an attachment

**What this covers**: kata `2frx` (live e2e for image attachments) over
kata `xy3t`'s pasted-path detection in
`cmd/serf-tui/hub_model.go:handleBracketedPaste`. When the user pastes
TEXT into the session composer (delivered as a bubbletea
`KeyMsg{Paste: true}`), the TUI inspects the payload with
`NormalizePastedPath` + `IsImageFile` + an `os.Stat` existence check.
If all three pass, the path is attached as a `PastedImage{Origin:
"path"}` and the text is NOT inserted into the textarea. On submit,
the bytes are read from disk and shipped through the daemon as a
`ContentImage` part.

Companion scenarios: `tui-paste-image-from-clipboard.md`,
`web-paste-image-from-clipboard.md`, `web-drag-drop-image.md`,
`web-file-picker-image.md`.

## Pre-state

- `tmux` installed (tested on tmux 3.4). `tmux load-buffer` +
  `tmux paste-buffer -p` is the path that emits bracketed-paste
  start/end markers — required for the TUI's `KeyMsg.Paste` branch
  to fire.
- `serf-hub` reachable on `127.0.0.1:9180`. Token at
  `~/.serf/auth-token`.
- `./serf-tui` and `./serf-hub` built in repo root.
- `anthropic/claude-haiku-4-5-20251001` (or `openai/gpt-5.5`)
  reachable through configured credentials.
- No leftover tmux session named `serf-e2e-imgpath`.
- **No display server or clipboard tool needed.** Unlike the
  clipboard-paste scenario, the path detection just reads from disk.

## Steps

1. **Write a fixture PNG to a known path.** Using a stable filename
   makes the recorded scenario re-runnable without templated paths:
   ```bash
   convert -size 64x64 xc:red /tmp/serf-e2e-test-image.png
   file /tmp/serf-e2e-test-image.png    # PNG image data, 64 x 64
   ```

2. **Spawn a dormant session** (single trivial first turn so we have
   `state=idle` to navigate to):
   ```bash
   WORKDIR=$(mktemp -d -t serf-tui-imgpath-XXXX)
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

3. **Launch serf-tui in tmux**:
   ```bash
   tmux new-session -d -s serf-e2e-imgpath -x 200 -y 50 \
     "./serf-tui --hub-addr 127.0.0.1:9180 --debug"
   sleep 1
   ```

4. **Navigate into the spawned session** via the command palette:
   ```bash
   tmux send-keys -t serf-e2e-imgpath "/"
   sleep 0.3
   tmux send-keys -t serf-e2e-imgpath -l "$SID"
   sleep 0.4
   tmux send-keys -t serf-e2e-imgpath Enter
   sleep 0.5
   tmux capture-pane -t serf-e2e-imgpath -p | head -25
   ```

5. **Bracketed-paste the PNG path** via tmux paste-buffer. The `-p`
   flag tells tmux to wrap the buffer in `ESC[200~ … ESC[201~`
   bracketed-paste markers; without `-p` the bytes arrive as
   keystrokes and bubbletea reports them as normal `KeyMsg`s
   (the `Paste:true` branch never fires):
   ```bash
   tmux set-buffer "/tmp/serf-e2e-test-image.png"
   tmux paste-buffer -t serf-e2e-imgpath -p
   sleep 0.4
   tmux capture-pane -t serf-e2e-imgpath -p | head -25
   ```
   The footer should now show:
   ```
   attachments  ctrl+backspace: drop last
   📎 serf-e2e-test-image.png [×]
   ```
   The textarea body should be empty — the path text was NOT
   inserted.

6. **Type a prompt and submit**:
   ```bash
   tmux send-keys -t serf-e2e-imgpath -l "describe this image in one sentence"
   sleep 0.3
   tmux send-keys -t serf-e2e-imgpath Enter
   sleep 8
   tmux capture-pane -t serf-e2e-imgpath -p | head -35
   ```

7. **Cross-check the transcript** — same Python loop as
   `tui-paste-image-from-clipboard.md` step 7.

## Expected

- **Step 5 (chip vs text)**: pane shows the chip
  `📎 serf-e2e-test-image.png [×]`. The textarea body is empty.
  Falsification:
  - Textarea contains `/tmp/serf-e2e-test-image.png` and NO chip:
    `handleBracketedPaste` didn't match (verify that the file
    exists, has an `.png` extension on the recognised list, and
    is not a directory — `mediaTypeForPath` and `IsImageFile`
    both gate on the extension; `os.Stat` gates on existence).
  - Pane has no chip AND no path: bubbletea didn't deliver a
    `Paste:true` KeyMsg. Most often a tmux terminfo issue —
    confirm tmux supports `setw -g bracket-paste-on` (default
    on tmux 3.x).
- **Step 6 (idle + reply)**: post-send pane shows the user message,
  a brief `read_file` probe at the actual workdir (e.g.
  `read /tmp/serf-tui-imgpath-XXXX: is a directory`; a harmless
  haiku-4-5 quirk), then a `communicate` with the description.
  State returns to `idle`. Falsification: model produces
  unrelated text or refuses.
- **Step 7 (transcript)**: at least one `USER_INPUT` row has
  `kind=text` + `kind=image` parts; the assistant's `communicate`
  call body describes the image. Verified live (2026-05-18)
  against `claude-haiku-4-5-20251001` producing `"A solid bright
  red square."`

## Cleanup

```bash
TOKEN=$(cat ~/.serf/auth-token)
curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" -d '{}' \
  "http://localhost:9180/s/$SID/shutdown" >/dev/null
tmux kill-session -t serf-e2e-imgpath 2>/dev/null
rm -rf "$WORKDIR"
rm -f /tmp/serf-e2e-test-image.png
# Optional, for hermeticity:
# find ~/.local/state/serf/projects -name "$SID*" -delete
```

## Sharp edges

- **Bracketed-paste detection lives in the SESSION composer only.**
  The handler is invoked from
  `cmd/serf-tui/hub_model.go:handleSessionInput` (line ~1457). The
  SPAWN form's prompt text field doesn't have a paste-path branch
  — pasting a path there inserts the literal text into the
  textarea. If you want "attach at spawn time", use the web /new
  form via the file picker.
- **`tmux paste-buffer -p` (the `-p` flag) is what triggers the
  branch.** Without it the bytes arrive as normal keystrokes and
  bubbletea never sets `KeyMsg.Paste=true`. `tmux send-keys -l
  "/tmp/serf-e2e-test-image.png"` similarly inserts the path as
  text without firing the detection.
- **Supported extensions** are pinned in
  `cmd/serf-tui/clipboard_paste.go:IsImageFile`:
  `.png .jpg .jpeg .gif .webp`. Other extensions pass through to
  the textarea unchanged. To attach a `.heic` or `.svg` today,
  copy the file to a `.png` (Pre-state) before pasting.
- **The TUI does NOT re-encode path-pastes to PNG.** The bytes go
  out as-is with `MediaType` derived from the extension
  (`mediaTypeForPath`). Clipboard pastes DO go through PNG
  re-encoding (see `tui-paste-image-from-clipboard.md`).
- **Paths are normalised**, not just trimmed. `NormalizePastedPath`
  handles `file://` URLs, `"quoted"` paths, Windows / WSL paths
  (`C:\foo` → `/mnt/c/foo`), and bare slashes. The unit tests in
  `clipboard_paste_test.go:TestNormalizePastedPath` enumerate the
  recognised forms.
- **`os.Stat` is a real syscall**. If the path resolves but the
  file doesn't exist, the bracketed-paste falls through to normal
  text insertion. The scenario relies on
  `/tmp/serf-e2e-test-image.png` actually being on disk; the
  setup step does that explicitly.
- **claude-haiku-4-5-20251001's `read_file` quirk** — same as the
  other image scenarios. The model sees the image and decides to
  "read the file" before describing; the read fails, then the
  model falls back to the inline image. Not a failure unless the
  `communicate` body never describes the image.
- **Auth + provider tax.** Burns one trivial setup turn and one
  image-describe turn.
- **No CSP issue here.** Unlike the web scenarios, this exercises
  no browser-side image decode pipeline.
