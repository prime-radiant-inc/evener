# tui-steer-live-turn: inject a steer mid-turn from serf-tui

**What this covers**: kata `mn4z`. The hub TUI has a "steer"
composer mode that lets the user nudge a running turn without
queueing a brand-new prompt
(`cmd/serf-tui/composer_panel.go:hubComposerModeSteer`,
`cmd/serf-tui/hub_model.go:1419-1430`,
`cmd/serf-tui/hub_commands.go:sendHubSteer`). When the composer
auto-switches to steer mode during a processing turn, Enter
fires `turn/steer` against the daemon and the agent should
receive a `STEERING` transcript entry that it acts on before
the turn ends. This scenario exercises that round trip
end-to-end against a real model.

## Pre-state

- `tmux` installed (tested on tmux 3.4).
- `serf-hub` reachable on `127.0.0.1:9180`. Token at
  `~/.serf/auth-token`.
- `./serf-tui` and `./serf` (or `./serf-hub`) built in repo root.
  (`go build -o serf-tui ./cmd/serf-tui` and a hub binary, but
  the hub only needs to be running — not rebuilt here.)
- Anthropic OAuth or API key configured so the default
  `anthropic/claude-haiku-4-5-20251001` model can be invoked
  (any provider/model the local hub can talk to is fine; the
  scenario just needs a real model that emits enough tokens
  for the steer to land mid-stream).
- No leftover tmux session named `serf-steer-test`
  (`tmux kill-session -t serf-steer-test 2>/dev/null`).

## Steps

Use `tmux send-keys -t serf-steer-test KEY ...` to drive input
and `tmux capture-pane -t serf-steer-test -p` to read the
visible pane. Named keys (`Enter`, `Tab`, `BTab`, `C-u`) are
sent as keynames; the prompt and steer text are sent with
`-l <text>` so they go through literally.

1. **Prepare a hermetic workdir with a README to read**:
   ```
   WORKDIR=$(mktemp -d -t serf-steer-XXXX)
   cp /home/jesse/git/prime-radiant/serf/README.md "$WORKDIR/README.md"
   ```

2. **Launch in tmux**:
   ```
   tmux new-session -d -s serf-steer-test -x 200 -y 50 \
     "./serf-tui --hub-addr 127.0.0.1:9180 --debug"
   sleep 1
   tmux capture-pane -t serf-steer-test -p
   ```
   Confirm the dashboard header reads `serf live` + `… live`
   counter and the footer hint reads `up/down select  enter
   open/toggle  n new  / palette  ctrl+o dashboard  q quit`.

3. **Open the spawn form**:
   ```
   tmux send-keys -t serf-steer-test "n"
   sleep 0.5
   ```
   Confirm pane shows `serf / new session` with five fields
   (`Harness`, `Model`, `Project`, `Dir`, `Prompt`). Focus
   marker `>` is on `Prompt`. Footer: `tab: next field
   shift+tab: previous  enter: spawn  ctrl+j: newline  esc:
   cancel  ctrl+o: dashboard`.

4. **Retarget the working dir at the hermetic workdir**:
   ```
   tmux send-keys -t serf-steer-test BTab        # back to Dir
   tmux send-keys -t serf-steer-test C-u         # clear the field
   tmux send-keys -t serf-steer-test -l "$WORKDIR"
   tmux send-keys -t serf-steer-test Tab         # forward to Prompt
   ```
   Confirm `Dir: <WORKDIR>` and `> Prompt (optional):` rows.
   `Ctrl+U` is wired explicitly in
   `cmd/serf-tui/hub_model.go:978` for this field.

5. **Type a multi-round prompt and submit**:
   ```
   tmux send-keys -t serf-steer-test -l "Read the README.md file in the current directory. Then write a 5-paragraph essay about its main themes. Use formal prose."
   tmux send-keys -t serf-steer-test Enter
   sleep 1.5
   tmux capture-pane -t serf-steer-test -p
   ```
   Confirm the view switches to `serf / session / <ULID>`,
   the status row reads
   `state: processing  model: claude-haiku-4-5-…`, the second
   status row reads
   `status: hub connected  provider: anthropic  steer: ready  busy: turn_1`,
   and the composer label is now `steer` (not `message`) with
   footer `enter: steer  esc: browse  ctrl+p: palette
   ctrl+o: dashboard  /help`. This is the auto-switch:
   `sessionComposerMode` returns `hubComposerModeSteer`
   whenever the turn is processing **and** the source
   advertises the `Steer` capability and `ActiveTurnID` is
   non-empty (`cmd/serf-tui/composer_panel.go:24-38`).

6. **Wait for the model to actually start producing tokens**
   so the steer is unambiguously mid-turn:
   ```
   sleep 3
   tmux capture-pane -t serf-steer-test -p
   ```
   Confirm a `▸ read_file  README.md` tool-call row and some
   assistant prose has appeared (e.g. `**Serf** …`). The
   `steer: ready` capability stays on while the turn streams.

7. **Inject the steer** (composer-mode path; no slash command,
   no palette needed):
   ```
   tmux send-keys -t serf-steer-test -l "Change of plans: write only a single haiku (3 lines, 5-7-5 syllables) instead of the essay. Disregard the essay instruction."
   tmux send-keys -t serf-steer-test Enter
   sleep 1
   tmux capture-pane -t serf-steer-test -p
   ```
   A `Steering sent.` system line appears below the partial
   assistant output (per `cmd/serf-tui/hub_model.go:298-299`,
   reached via the `hubActionMsg{action:"steer"}` reply from
   `sendHubSteer`).

8. **Wait for the turn to wrap and verify the model adjusted**:
   ```
   sleep 8
   tmux capture-pane -t serf-steer-test -p
   ```
   The closing assistant output is a single haiku (three
   short lines), not a fifth essay paragraph. The composer
   label flips back from `steer` to `message` and the footer
   from `enter: steer` to `enter: send` — the active turn
   has ended cleanly (session stays alive, not terminated).

9. **Cross-check the transcript on disk** (project hash dir
   under `~/.local/state/serf/projects/<hash>/sessions/`):
   ```
   SID=$(tmux capture-pane -t serf-steer-test -p | \
     grep -oE '01[0-9A-Z]{24}' | head -1)
   TS=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
   grep -oE '"kind":"STEERING"' "$TS"
   ```
   At least one match. Inspect the full row:
   ```
   grep '"kind":"STEERING"' "$TS" | head -1
   ```
   Confirm the `text` payload contains the exact steer message
   sent in step 7 (`"Change of plans: write only a single
   haiku …"`). The `message.role` is `user`. The agent's next
   `ASSISTANT` entry should issue a `communicate` tool call
   whose `arguments.message` is the haiku.

10. **Exit and clean up**:
    ```
    tmux send-keys -t serf-steer-test "i"      # leave browse mode if entered
    tmux send-keys -t serf-steer-test C-c C-c  # double Ctrl+C, in one call
    tmux kill-session -t serf-steer-test 2>/dev/null
    rm -rf "$WORKDIR"
    ```

## Expected

- Step 5: composer auto-switches to `steer` mode. The
  signature is two correlated changes — label `steer` above
  the prompt **and** footer `enter: steer …`. Falsification:
  label stays `message` while the turn is processing (means
  the source isn't advertising `Steer` capability — file a
  kata).
- Step 7: `Steering sent.` system line appears within ~1 s
  of pressing Enter. Falsification: nothing changes, or the
  steer text shows up as a new `USER` turn (kind `USER_INPUT`)
  in the transcript instead of `STEERING` (means the
  composer-mode gating regressed and Enter routed through
  `sendHubInput` rather than `sendHubSteer`).
- Step 8: post-steer assistant output is short and references
  the steer (in our recording: `Agent loops fast, / Files and
  commands flow freely— / Code writes itself now.`). Exact
  wording will vary — what matters is that it's a 3-line
  haiku-shaped thing, not a fifth essay paragraph.
  Falsification: model finishes the essay anyway (would
  indicate the STEERING entry was persisted but not surfaced
  to the LLM's next round — file a regression).
- Step 9: transcript has at least one `{"kind":"STEERING"}`
  entry whose `message.content[0].text` equals the steer
  string from step 7. Followed by an `ASSISTANT` entry with a
  `communicate` tool call whose `arguments.message` is the
  haiku. Falsification: no `STEERING` entry, or the steer
  text appears as a `USER_INPUT` entry queued after the turn.

## Cleanup

- `tmux kill-session -t serf-steer-test 2>/dev/null`.
- `rm -rf "$WORKDIR"` (the `mktemp -d` from step 1).
- The spawned session ULID is left on the hub. Optionally
  `~/go/bin/serf` or a manual delete to remove it; the
  scenario otherwise leaves no garbage.

## Sharp edges

- **No dedicated steer keybind.** There is no `s` / `Ctrl+S`
  to "enter steer mode"; the composer mode is *derived*, not
  toggled. `sessionComposerMode()` reads
  `sessionTurnActionState()` (processing/awaiting/active/
  running/working) and `Capabilities.Steer` plus a non-empty
  `ActiveTurnID`. If you press Enter the moment after
  spawning, the daemon may not yet have published
  `ActiveTurnID` — the composer will still show `message`
  and Enter will *queue* a new user turn rather than
  steering. The 3-second wait in step 6 is what makes this
  scenario reliable.
- **No `/steer` slash command.** Despite a stale boundary
  test reference at
  `cmd/serf-tui/hub_backend_boundary_test.go:66` (`"/steer"`),
  the Ctrl+P palette has no `steer` entry — `hub_commands.go`
  registers steer only as `sendHubSteer`, not as a
  slash-command `hubCommand`. The composer-mode-during-
  processing path is the **only** way to steer from the TUI
  today. The web UI does expose `steer` via its palette
  (`cmd/serf-hub/assets/search.js:245`), so users coming from
  the web UI may look for the same affordance here. If a
  `/steer <text>` palette entry is desired for parity, that's
  a new kata.
- **The steer text is sent verbatim, not wrapped.** The
  transcript entry's `message.content[0].text` is exactly
  what you typed. Hub-side wrappers like `STEERING_INJECTED`
  (`cmd/serf-hub/web.go:1162`) are an SSE transport detail
  for the browser; the on-disk transcript stores it as a
  `kind: "STEERING"` turn with a user-role message.
- **Cancelled / empty drafts do not send.** Same guard as
  send mode (`strings.TrimSpace(text) == ""` returns early in
  `cmd/serf-tui/hub_model.go:1411`).
- **The 8-second post-steer wait is model-dependent.** Haiku
  is fast; slower models may need more. If the capture in
  step 8 still shows the composer in `steer` mode, sleep
  longer before asserting on the final shape of the assistant
  output. Falsification only applies once the turn has
  finished (composer label back to `message`).
- **Auth + provider tax.** This scenario *does* burn real
  API tokens (one full turn that streams an essay and then
  abandons it for a haiku). Acceptable cost — under a penny
  on Haiku 4.5 — but unlike `tui-workspace-navigation.md`,
  it can't be run hermetically against an in-process fake.
