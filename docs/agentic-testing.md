# Agentic end-to-end testing

This is a practical guide for an AI agent (or human) running an
end-to-end scenario from `test/scenarios/` against a live `serf-hub` +
`serf` daemon. The scenario files describe *what* to verify; this
document describes *how* — patterns, recipes, and footguns collected
from real sessions.

If you are writing a new scenario, see `test/scenarios/README.md` for
the file structure. This document is the runbook side.

## Setup checklist

Before running anything:

```bash
# 1. Build fresh binaries from the branch under test.
go build -o /tmp/serf-hub-test ./cmd/serf-hub
go build -o /tmp/serf-test ./cmd/serf
go build -o /tmp/serf-tui-test ./cmd/serf-tui

# 2. Start the hub. -addr binds it locally; -serf points at the daemon
#    binary the hub spawns per session.
/tmp/serf-hub-test -addr 127.0.0.1:9180 -serf /tmp/serf-test &
sleep 2

# 3. Confirm hub is up. Returns 401 (auth required) — that's success;
#    means it answered.
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:9180/  # → 401

# 4. Grab the auth token. Hub reads it from this path; the browser
#    needs it in the URL query and the curl REST shim needs it as a
#    Bearer header.
TOKEN=$(cat ~/.serf/auth-token)
HUB=http://127.0.0.1:9180
```

The hub picks up provider credentials from env (`OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`) and/or `~/.serf/credentials.toml`. If a scenario
needs a specific provider, check `./serf <provider> status` first.

OpenAI footgun: an inherited `OPENAI_API_KEY` takes precedence over
stored OAuth. If `serf openai status` is signed in but a GPT run fails
with API-key quota, start the test hub with the key cleared:

```bash
OPENAI_API_KEY= /tmp/serf-hub-test -addr 127.0.0.1:9180 -serf /tmp/serf-test &
```

Do not also isolate `XDG_STATE_HOME` for that run, because OpenAI OAuth
state lives under the normal user state home.

## Hermetic workdir per scenario

Every scenario runs in its own scratch directory so reruns don't
inherit prior state and the spawned session's `working_dir` is
contained:

```bash
tmpdir=$(mktemp -d -t serf-e2e-XXXXX)
```

For transcript isolation, pass a per-scenario `SERF_STATE_DIR` in
`launch_overrides.env`. This keeps the spawned daemon's sessions and
logs under one directory while still using the hub REST shim:

```bash
state=$(mktemp -d -t serf-e2e-state-XXXXX)
body=$(jq -n \
  --arg prompt "$prompt" \
  --arg model "$model" \
  --arg wd "$tmpdir" \
  --arg state "$state" \
  '{
    prompt:$prompt,
    model:$model,
    working_dir:$wd,
    harness:"serf",
    branch:"",
    access_mode:"full",
    agent:"default",
    launch_overrides:{env:{SERF_STATE_DIR:$state}}
  }')
```

If the scenario needs an `AGENTS.md` (see pacing trick below), write
it before spawning. Pass `tmpdir` as `working_dir` in the spawn
payload.

## Spawning a session via the REST shim

The hub's `/api/spawn` endpoint creates a session and starts a turn.
Copy-paste skeleton:

```bash
resp=$(curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d "{
    \"prompt\":\"please run \\\"echo hello\\\" via exec_command then stop\",
    \"model\":\"anthropic/claude-haiku-4-5-20251001\",
    \"working_dir\":\"$tmpdir\",
    \"harness\":\"serf\",
    \"branch\":\"\",
    \"access_mode\":\"full\",
    \"agent\":\"default\",
    \"launch_overrides\":{}
  }" \
  "$HUB/api/spawn")
SID=$(echo "$resp" | jq -r '.session_id')
```

`SID` is a ULID like `01KRYW3VGR1J5XJH131A9KMDGR`. The session's
appwire ref is `local:$SID`.

### Polling for state transitions

The session's state field walks through `idle` → `processing` → … →
`idle` (or `awaiting_input`, or `closed`). Wait for the state the
scenario needs:

```bash
for i in $(seq 1 60); do
  state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
            | jq -r '.state // ""' 2>/dev/null)
  [ "$state" = "idle" ] && break          # change "idle" to "processing" as needed
  sleep 1
done
echo "state=$state"
```

The same `/api/sessions/local:$SID` response carries
`capabilities.steer`, `capabilities.queue`, etc. — useful for
asserting daemon-side gating (kata `wymv`).

## Auditing sidecar scenarios

For observer sidecar scenarios, do not trust the final scenario marker
alone. Audit the parent and observer transcripts:

```bash
/tmp/serf-doctor-test tree "$SID" --state-dir "$state" --observers
/tmp/serf-doctor-test transcript "$SID" --state-dir "$state" --format outline --range last:80
/tmp/serf-doctor-test transcript "$SID" --state-dir "$state" --count job_list
/tmp/serf-doctor-test transcript "$SID" --state-dir "$state" --count job_read_output
/tmp/serf-doctor-test transcript "$OBSERVER_SID" --state-dir "$state" --format outline --range last:80
/tmp/serf-doctor-test transcript "$OBSERVER_SID" --state-dir "$state" --count delegate_send
```

For the happy path, the parent should use the current delegate result,
watch result, watched event result, and observer callback as working
signals. `job_list` and `job_read_output` should be zero before the
callback unless the scenario is explicitly testing diagnosis. A later
terminal notification from the observer delegate is confirmation; it is
not the signal the parent should poll for.

## AGENTS.md pacing trick — keeping a turn in `processing`

The model finishes a trivial prompt in seconds. If the scenario needs
to click steer / interrupt / drain mid-turn, the model has to be busy
long enough for a browser-driving agent to observe and act.

Drop an `AGENTS.md` into `$tmpdir` that instructs the model to sleep
between every action:

```bash
cat > "$tmpdir/AGENTS.md" <<'EOF'
# Working agreement

For every user request, follow these procedural rules EXACTLY:

1. Pause between every action by calling exec_command with
   bash -c "sleep 8".
2. Insert these sleep pauses between EVERY paragraph and EVERY
   tool call. There must be at least 4 sleep calls per turn.
EOF
```

Then prompt with something that triggers tool use:

> "Read AGENTS.md if it exists in your cwd. Then write a long
> 5-paragraph essay about software engineering. Follow the pacing
> rules in AGENTS.md exactly."

`anthropic/claude-haiku-4-5-20251001` honors this reliably. Cheap
enough to use freely.

## Driving the web UI with superpowers-chrome:browsing

The browsing skill exposes one tool (`mcp__plugin_superpowers-chrome_chrome__use_browser`) with action verbs. After the auth-token redirect lands, the renderer constructs the singleton `window.SerfRenderer`; you drive it through `eval`.

### Authenticated navigation

```text
navigate http://127.0.0.1:9180/auth?token=<TOKEN>&next=/s/<SID>
await_element [data-steer-trigger]      # or any element you expect
```

The `/auth` route sets the session cookie then redirects to `next`.
Use the literal token from `~/.serf/auth-token`, not the path. If you
get `"invalid token"` rendered, you passed the path.

### Synchronous vs. async assertion shape

For any "did the optimistic UI update happen before the RPC
resolved?" scenario, this is the pattern. Fire the action, **don't
await it**, then take a synchronous DOM snapshot. Then await and take
a post-ack snapshot:

```javascript
(async () => {
  const before = {
    pendingChips: document.querySelectorAll(".optimistic-pending").length,
  };
  // Fire — capture promise but don't await yet.
  const promise = window.SerfAppwire.steer(
    window.SerfRenderer.sessionId, "", "this should fail visibly"
  ).catch(e => e);
  // Synchronous: pending placeholder is in the DOM RIGHT NOW.
  const sync = {
    pendingChips: document.querySelectorAll(".optimistic-pending").length,
    pendingText: document.querySelector(".optimistic-pending")?.textContent,
  };
  await promise;
  await new Promise(r => setTimeout(r, 200));  // let DOM settle
  const after = {
    pendingChips: document.querySelectorAll(".optimistic-pending").length,
    failedChips: document.querySelectorAll(".optimistic-failed").length,
    reason: document.querySelector(".optimistic-failed-reason")?.textContent,
  };
  return JSON.stringify({ before, sync, after }, null, 2);
})()
```

Without the no-await capture, the test can't distinguish "pending
chip rendered and was reconciled" from "pending chip never rendered."

### Probing internal renderer state

`window.SerfRenderer` is the renderer's singleton. Useful introspection:

```javascript
JSON.stringify({
  state: window.SerfRenderer?.state,         // idle | processing | …
  activeTurnId: window.SerfRenderer?.activeTurnId,
  appwireHydrated: window.SerfRenderer?.appwireHydrated,
  pendingType: typeof window.SerfRenderer?.pending,
  windowKeys: Object.keys(window).filter(k => k.toLowerCase().includes("serf")),
})
```

If `appwireHydrated` is `false` after navigation, the appwire
WebSocket didn't connect — check the hub log. If `pendingType` is
`"undefined"` when you expect an object, the renderer's
optimistic-rendering registry never installed (this was the kata
`wymv` debug entry point).

## Driving the TUI with tmux

Each scenario gets its own tmux session. Naming matters — the
cleanup step needs a deterministic name:

```bash
tmux kill-session -t serf-test 2>/dev/null   # idempotent: prior run
tmux new-session -d -s serf-test -x 200 -y 50 \
  "/tmp/serf-tui-test --hub-addr 127.0.0.1:9180 --debug 2>/tmp/tui-stderr.log"
sleep 2
```

`-x 200 -y 50` forces a stable terminal size so capture-pane is
deterministic. `--debug` disables the bubbletea AltScreen so
capture-pane sees output as plain text rather than the screen-buffer
escape sequences.

Redirect stderr to a file — most TUI panics, log lines, and any
`fmt.Fprintln(os.Stderr, ...)` debug probes you add land there.

### Form fill: tmux send-keys patterns

`send-keys` parses keystrokes by name (`Enter`, `BTab`, `C-u`) and
literal text. Use `-l` to disable parsing for the literal-text path:

```bash
tmux send-keys -t serf-test "n"                # tap "n" for new session
sleep 1
tmux send-keys -t serf-test BTab               # shift-tab back one field
sleep 0.3
tmux send-keys -t serf-test C-u                # clear the current line
sleep 0.3
tmux send-keys -t serf-test -l "$tmpdir"       # literal — no key parsing
sleep 0.3
tmux send-keys -t serf-test Tab                # forward to next field
sleep 0.3
tmux send-keys -t serf-test -l "Read AGENTS.md and write a long essay."
sleep 0.3
tmux send-keys -t serf-test Enter              # spawn
```

`-l` matters: without it, a literal `/tmp/foo/AGENTS.md` containing
a `/` gets parsed as an arrow-key escape. Always `-l` for
user-typed strings.

`sleep 0.3` between keys is usually enough; bumps to 0.5–1.0s for
field transitions where the UI re-renders.

### Polling capture-pane for state

`capture-pane -p` dumps the visible pane to stdout. Grep for the
state line in the workspace header:

```bash
for i in $(seq 1 30); do
  pane=$(tmux capture-pane -t serf-test -p)
  echo "$pane" | grep -q "state: processing" && { echo "i=$i processing"; break; }
  sleep 1
done
```

For optimistic-rendering scenarios, take two captures — one
synchronously after the keypress, one after a reconcile window —
mirroring the web sync/async pattern:

```bash
tmux send-keys -t serf-test -l "Stop and write a haiku."
sleep 0.5
tmux send-keys -t serf-test C-s
sleep 0.3
echo "=== synchronous ===" ; tmux capture-pane -t serf-test -p | grep -E "draining|⠋|Force-steer"
sleep 6
echo "=== reconciled  ===" ; tmux capture-pane -t serf-test -p | grep -E "draining|⠋" || echo "[no pending — reconciled]"
```

### ANSI styling and Unicode glyphs

`tmux capture-pane -p` drops ANSI styling unless you pass `-e`. The
TUI uses faint vs bold-red glyphs to signal pending vs failed —
**grep on the glyph itself, not on the color**:

- pending: `⠋` (UTF-8 `e2 a0 8b` — Braille pattern dots-1-2-3)
- failed: `✗` (UTF-8 `e2 9c 97`)
- success/reconciled: glyph removed, no marker

## Inspecting transcript and meta on disk

Every session writes a JSONL transcript and meta sidecar under
`~/.local/state/serf/projects/<project-hash>/sessions/`:

```bash
TS=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
META=$(find ~/.local/state/serf/projects -name "$SID.meta.json")

# Locate the canonical files for a session selector.
go run ./cmd/serf-doctor locate "$SID"

# Read a compact, typed turn outline instead of raw JSONL.
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:20

# Count structural tool invocations. This does not confuse tool-name
# mentions in prompts/API payloads with real tool calls.
go run ./cmd/serf-doctor transcript "$SID" --count delegate_send
```

Useful when a scenario's web/TUI assertion is ambiguous. The on-disk
transcript is the daemon's authoritative record of what happened, but
do not hand-parse transcript JSONL for comprehension; use
`serf-doctor transcript`. Raw `jsonl` reads are for byte-level replay
or debugging the transcript format itself.

## Falsification debugging — when an assertion fails

If a scenario's assertion fails and the failure isn't obvious from
the captured pane / DOM, the next move is to add a stderr probe to
the offending layer, rebuild, rerun, grep the log. The full pipeline
has six rebuild points:

1. **Daemon** — `cmd/serf/` and `agent/`. Rebuild: `go build -o /tmp/serf-test ./cmd/serf`. The hub re-spawns it per session, so the next spawned session picks up the new binary.
2. **Hub** — `cmd/serf-hub/` and `server/`. Rebuild + kill the running hub: `pkill -f serf-hub-test; go build -o /tmp/serf-hub-test ./cmd/serf-hub && /tmp/serf-hub-test -addr 127.0.0.1:9180 -serf /tmp/serf-test &`.
3. **Web renderer** — `cmd/serf-hub/assets/*.js`. These files are embedded into `serf-hub`; rebuild and restart the hub, then refresh the browser tab.
4. **TUI** — `cmd/serf-tui/`. Rebuild: `go build -o /tmp/serf-tui-test ./cmd/serf-tui`. The running TUI keeps the old binary in memory — kill the tmux session and restart for the new code.
5. **AppWire types** — `internal/appwire/`. Both daemon and hub statically link these; rebuild both.
6. **Pending-coordinator / pending-registry** — owned by the TUI and the renderer respectively; same rebuild rules as 3 and 4.

Stderr probes are cheap and unambiguous. Example from the kata `wymv`
debug session — wanted to know whether the TUI's reconcile path was
reaching `applyHubNotification`:

```go
// In cmd/serf-tui/hub_model.go applyHubNotification
matched := m.notificationMatchesCurrentSession(notification)
fmt.Fprintf(os.Stderr, "DEBUG applyHubNotification method=%s matched=%v detailRef=%q\n",
    notification.Method, matched, m.detail.Ref)
if !matched {
    return nil
}
```

Then run the TUI with `2>$TUI_LOG`, grep for `DEBUG` after the
keypress. Same shape for the renderer side using `console.warn` in
JS — though the browsing skill's `*-console.txt` capture is a stub
in some versions; redirecting via `eval` to a `window.__DEBUG_LOG`
array and reading it via a follow-up `eval` works around that.

Strip the probes before committing. They're not artifacts; they're
scaffolding.

## Cleanup recipe

Idempotent cleanup that won't fail if anything's already gone:

```bash
# Shut down any sessions you spawned.
for sid in $SID1 $SID2 $SID3; do
  curl -s -X POST -H "Content-Type: application/json" \
    -H "Authorization: Bearer $TOKEN" -d '{}' \
    "$HUB/s/$sid/shutdown" >/dev/null 2>&1
done

# Kill any tmux sessions you opened.
for name in serf-test serf-test-2; do tmux kill-session -t $name 2>/dev/null; done

# Remove the hermetic workdirs.
rm -rf /tmp/serf-e2e-*

# Kill the hub if you started it; leave alone if it predates this run.
pkill -f serf-hub-test
```

If you skip cleanup, the next run inherits a half-shutdown daemon
fleet and `state: idle` polling can return false-positives from
prior sessions.

## Inspecting watch sidecars

Watch/observer scenarios should assert against durable state, not only
the parent's final prose. Use `serf-doctor` instead of custom JSONL
parsers:

```bash
# Parent watch lifecycle, coalesced delivery counts, dropped sends, and
# self-loop verdicts.
go run ./cmd/serf-doctor watches "$SID"

# Parent/delegate/observer topology. Use observer transcript refs from
# this output when you audit sidecar behavior.
go run ./cmd/serf-doctor tree "$SID" --observers

# Parent and observer turns. The observer transcript should show whether
# a frame caused useful work, a no-op text response, or unwanted tool churn.
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30

# Structural tool counts when the scenario cares about fluency.
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count delegate_send
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count communicate
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count job_list
```

This matters when an observer calls `delegate_send(to="caller")`: the
caller steering can be consumed before the parent emits a scripted
"done" marker, or the model may choose to stop after handling the
steering. Treat the durable watch rows and observer transcript as the
contract; treat final parent text as a convenience signal only.

For sidecar fluency, record these separately from pass/fail:

- **Invalid event selection**: the parent tries
  `events: [assistant.message]` when the scenario requires
  `events: [communicate]`. That event should be rejected; a fluent
  agent should recover by creating a `communicate` watch.
- **Observer readiness race**: the parent creates the watch and triggers
  it before the observer's first `*_READY` turn has finished, so the
  real frame is consumed by setup behavior.
- **Parent polling wait**: after installing a watch and triggering it,
  the parent polls with `job_list`, `job_read_output`, or transcript
  tools to wait for the observer. The fluent path is to continue from
  the observer's `delegate_send(to="caller")` callback steering.
- **Tool churn**: the observer uses `job_list`, `read_session_transcript`,
  or another harmless tool only to acknowledge a frame that required no
  action.
- **Weak marker**: the parent emits `SCENARIO_DONE` even though the
  observer never sent the expected finding/reminder/package.

## The over-specification trap

A scenario can describe a behavior that production gating prevents.
Example: `tui-steer-in-idle-fails-fast.md` documents what Ctrl+S in
an IDLE session *should* look like — but `handleSessionForceSteer`
in `cmd/serf-tui/hub_model.go` early-returns when the composer mode
isn't `queue`. So Ctrl+S in IDLE is a silent no-op, not a visible
failure chip.

When you hit this:

1. Confirm the gating in the source. Don't burn time trying to drive
   around it via tmux send-keys.
2. Verify the underlying behavior the scenario was meant to assert
   via the corresponding **unit test**. If a unit test doesn't exist,
   write one — that's where the assertion belongs once a UI gate
   makes the live path unreachable.
3. Update the scenario file to note the gating, or — if the gate is
   the intent — close out the scenario as covered-by-unit-test.

If the gating itself is the bug (e.g. the daemon allowed the action
in IDLE but the UI gated the keypress, masking a daemon-side issue),
file a kata. Don't try to drive past the gate from the scenario.

## Quick reference

- **Hub address**: `127.0.0.1:9180`
- **Auth token**: `~/.serf/auth-token`
- **Follow-up turn** (after the initial spawn prompt): `POST /s/<SID>/send` with body `{"text":"..."}` (the spawn only starts turn 1; subsequent user turns go here).
- **Recursion opt-in** (delegate subagents that can themselves delegate): per-spawn `launch_overrides.maxSubagentDepth:N` raises the root's own delegation allowance to N. Omitted/default is 1 (a root may delegate, but its delegates are leaves) — recursion is dark without this.
- **Per-session transcript**: `~/.local/state/serf/projects/<hash>/sessions/<SID>.transcript.jsonl`
- **Per-session meta**: same dir, `<SID>.meta.json`
- **TUI debug stderr** (when launched with `--debug`): redirect via `tmux new-session -d -s <name> "/tmp/serf-tui-test --hub-addr 127.0.0.1:9180 --debug 2>$LOG"`
- **Browser console capture**: `~/.cache/superpowers/browser/<date>/<session>/<NNN>-<action>-console.txt`
- **Kata CLI**: `~/go/bin/kata` (see `kata create --help`)
