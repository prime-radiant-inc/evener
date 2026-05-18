# tui-workspace-navigation: serf-tui dashboard + session keyboard nav

**What this covers**: kata `57be`. The Bubble Tea TUI in
`cmd/serf-tui/` is fully covered by Go unit tests (`tmux_e2e_test.go`
and friends) but has no scenario-level guard. This scenario uses
`tmux` to drive the binary end-to-end against a real running hub and
verifies the keyboard event loop, focus management, and the
dashboard ↔ session transitions described in the action bars.

## Pre-state

- `tmux` installed (`which tmux` returns a path; tested on tmux 3.4).
- `serf-hub` reachable on `127.0.0.1:9180`. Token at
  `~/.serf/auth-token`.
- `./serf-tui` built and in repo root (`go build -o serf-tui ./cmd/serf-tui`).
- At least one live or recent session visible on the dashboard. If
  empty, spawn one first via `~/go/bin/serf spawn ...` or accept that
  the leaf-enter step is skipped.
- No leftover tmux session named `serf-test` (the scenario kills it
  first to be safe: `tmux kill-session -t serf-test 2>/dev/null`).

## Steps

Use `tmux send-keys -t serf-test KEY ...` to drive input and
`tmux capture-pane -t serf-test -p` to read the visible pane.

1. **Launch in tmux**:
   ```
   tmux new-session -d -s serf-test -x 200 -y 50 \
     "./serf-tui --hub-addr 127.0.0.1:9180 --debug"
   sleep 1
   tmux capture-pane -t serf-test -p
   ```
   Confirm the first line shows `serf live` and the hub URL + `N live`
   counter. A row marked `> ▾ ●` or `> ▸ ●` indicates a project header;
   `└─` and `├─` mark leaf sessions. Footer reads:
   `up/down select  enter open/toggle  n new  / palette  ctrl+o dashboard  q quit`.

2. **Dashboard arrow navigation**: send `Down Down` (or `j j`); recapture
   and confirm the `>` selection marker moved two rows.

3. **Project collapse/expand**: with selection on a project header
   (row starting `▾ ● <project>`), send `h` (or `Left`). The marker
   flips to `▸ ●` and the leaves disappear. Send `l` (or `Right`) to
   re-expand.

4. **Dashboard command palette**:
   ```
   tmux send-keys -t serf-test "/"
   tmux send-keys -t serf-test -l "tmp"
   ```
   Pane shows a bordered overlay titled `Command palette` with
   `Filter: tmp` and a filtered row list. Footer of the overlay reads
   `type filter  up/down navigate  enter select  esc close`. Press
   `Escape`; overlay disappears.

5. **Enter a live session**: with selection on a leaf row
   (`└─ ● idle …` or `├─ ● idle …`), press `Enter`. The composer view
   replaces the dashboard. Confirm the footer changes to:
   `enter: send  esc: browse  ctrl+p: palette  ctrl+o: dashboard  /help`
   and a `message` label sits above a prompt with cursor (`> █`).

6. **Browse mode**: press `Esc`. Footer changes to
   `esc/i/q: compose  ctrl+o: dashboard`. The selected message row gets a
   `▶` glyph (or whatever the current selection style is). Press `i` to
   return to compose mode; the composer prompt and footer return.

7. **Session command palette**: press `/` (with empty composer) or
   `Ctrl+P`. Overlay opens listing `/help`, `/dashboard`, `/auth`,
   `/login`, `/status`, `/details`, `/interrupt`, `/compact`, `/clear`,
   `/fork`, `/shutdown`, `/model`, `/theme`, etc. Capability-gated rows
   carry a `disabled: …` suffix (e.g. `disabled: source does not
   advertise interrupt`). Press `Esc` to close.

8. **Return to dashboard**: press `Ctrl+O`. Pane returns to the
   dashboard view; the previously opened session is still in the
   tree, and the selection is preserved on it.

9. **Double-Ctrl+C exit from a session**: enter a session again
   (steps 5). Send `C-c C-c` in one `send-keys` call (so they arrive
   within the 1-second window — see `hubCtrlCQuitWindow` in
   `cmd/serf-tui/hub_model.go`). The tmux session exits.

10. **`q` exit from dashboard**: relaunch (step 1). Press `q`. The
    tmux session ends.

11. **Cleanup**: `tmux kill-session -t serf-test 2>/dev/null`.

## Expected

- Step 1: pane shows `serf live` header, hub URL, project tree, and
  the dashboard footer hint. Falsification: blank pane, "connection
  refused", or no footer.
- Step 2: `>` marker moves with each `Down`/`j`; bounded at the last
  row (no panic, marker just stops).
- Step 3: `▾` ↔ `▸` flip is the only state change; selection stays
  on the project header.
- Step 4: filter narrows the rows in real time; `Esc` returns to the
  unfiltered dashboard.
- Step 5: composer renders with cursor visible; footer hint updates.
- Step 6: browse-mode footer and compose-mode footer are distinct.
- Step 7: palette overlay lists at least `/help` and `/dashboard`;
  capability-gated entries show their gate reason inline.
- Step 8: `Ctrl+O` is symmetric with leaf-Enter — returns to dashboard
  preserving project expansion state.
- Step 9: first `C-c` alone does not exit; the pair within 1s does.
- Step 10: `q` on dashboard exits cleanly (tmux session disappears,
  no traceback).
- Falsification anywhere: pane never updates after `sleep 0.2`
  (model frozen), `q` doesn't exit, double-`C-c` doesn't exit, or
  Enter on a leaf row toggles the project header instead of opening
  the session.

## Cleanup

- `tmux kill-session -t serf-test 2>/dev/null` (idempotent).

## Sharp edges

- **Tmux capture timing.** A `capture-pane` issued immediately after
  `send-keys` returns the *previous* frame. Always `sleep 0.1–0.3`
  before capturing. Bubble Tea coalesces redraws and tmux's
  scrape-the-screen model lags behind the program. 100 ms was enough
  in practice; 300 ms is the comfortable margin.
- **Enter on a project header toggles, doesn't open.** Project
  rows (`▾ ●` / `▸ ●`) treat Enter as "toggle expansion." Only
  leaves (`└─`, `├─`) open a session. So the canonical
  arrow-down-arrow-down-Enter recipe is brittle: count the rows
  carefully or use the filter palette to land on the right row.
- **Double-Ctrl+C window is 1 s.** `hubCtrlCQuitWindow = time.Second`
  in `cmd/serf-tui/hub_model.go`. Send both keys in one
  `send-keys -t ... C-c C-c` call to avoid races. A `sleep 0.5`
  between separate calls is usually fine; a `sleep 1.5` is not.
- **The `--debug` flag disables the alt-screen.** This makes
  `capture-pane` show the scrollback-style output, which is easier
  to grep. Without `--debug`, tmux still captures the same content
  for the visible pane — both work; `--debug` is just friendlier when
  diffing across captures.
- **`tmux send-keys -l <text>` sends text literally** (no key-name
  interpretation). Use it for typing into filters / composers. Use
  the un-flagged form for named keys (`Enter`, `Down`, `C-o`,
  `Escape`).
- **The dashboard "live" count in the header can change between
  steps** if real sessions are spawning/ending in parallel. Assert on
  static labels (project names, footer hints) rather than exact
  counts when verifying.
- **No assistant-response step is included.** The kata mentions
  "send a message; verify the agent responds," but doing that against
  the live hub burns API tokens on a random session and ties this
  scenario to flaky network/provider state. Sending text + Enter is
  exercised at the *event-loop* level by the existing Go
  `tmux_e2e_test.go` suite (`TestTUITmuxE2E_HubStreamingAssistantDeltaBeforeRefresh`
  etc.), which uses an in-process fake hub. Leave the model round-trip
  to those tests.
