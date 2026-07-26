# Agentic end-to-end testing

This is a practical guide for an AI agent (or human) running an
end-to-end scenario from `test/scenarios/` against a live `serf-hub` +
`serf` daemon. The scenario files describe *what* to verify; this
document describes *how* — patterns, recipes, and footguns collected
from real sessions.

If you are writing a new scenario, see `test/scenarios/README.md` for
the file structure. This document is the runbook side.

## Setup checklist

**Never Jesse's real hub, never his port `9180`.** His `serf-hub` runs
there for real, host-wide flock'd at `~/.serf/hub.lock`, with his real
auth token, credentials, providers, and session history under `~/.serf`
and `~/.local/state/serf`. A test hub started with a bare `$HOME` and
the doc's old literal `9180` would frequently fail to bind at all
(the flock is exclusive) — but if the check below can't tell *whose*
hub answered, an agent that doesn't notice the failure goes on to spawn
real sessions, with a real token, against Jesse's live hub.

Every artifact below — binaries, `$HOME`, the hub's port — is named from
a single `mktemp`-generated run directory, never from a fixed string or a
port a human assigned in a dispatch prompt. Two agents running this
recipe at the same time, even for the same kata, get disjoint paths and
disjoint ports by construction; there is no shared prefix to collide on
and no convention to remember:

```bash
# 1. One unique run directory. Everything this recipe creates lives
#    under it, so nothing here can collide with another concurrent run
#    (a second scenario, another agent, or Jesse's real hub).
run=$(mktemp -d -t serf-e2e-XXXXXX)

# 2. Build fresh binaries from the branch under test, into the run dir —
#    not a fixed /tmp/serf-hub-test that a second concurrent build would
#    overwrite mid-run (kata k2rx: this is exactly the shared-tmp-path
#    collision that clobbered another agent's binaries and providers.toml).
go build -o "$run/serf-hub" ./cmd/serf-hub
go build -o "$run/serf" ./cmd/serf
go build -o "$run/serf-tui" ./cmd/serf-tui

# 3. Isolate. A throwaway $HOME keeps auth-token, credentials.toml,
#    providers.toml, hub.lock, and session history off Jesse's real
#    ~/.serf and ~/.local/state/serf entirely — unsetting
#    XDG_STATE_HOME too, in case the ambient shell already points it
#    somewhere real (DefaultStateGlob prefers XDG_STATE_HOME over
#    $HOME/.local/state when it's set).
export HOME="$run/home"
mkdir -p "$HOME"
unset XDG_STATE_HOME

# 4. Start the hub with -addr 127.0.0.1:0 — never a hardcoded or
#    dispatch-assigned port. serf-hub binds the listener itself and
#    reports the address it actually got (kata 68fm: this used to be
#    the literal string ":0", not a dialable port — "-addr 127.0.0.1:0"
#    was silently useless before the fix). Binding happens inside the
#    hub process itself, so there's no separate "probe a free port with a
#    throwaway socket, then hope nothing grabs it before the real bind"
#    step and no TOCTOU window — the port the hub logs is the port it is
#    already listening on. -serf points at the daemon binary the hub
#    spawns per session.
"$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" 2>"$run/hub.log" &
HUBPID=$!

# 5. Read the real port back from the hub's own startup log line.
for i in $(seq 1 50); do
  PORT=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub.log" 2>/dev/null | grep -oE '[0-9]+$') || true
  [ -n "$PORT" ] && break
  kill -0 "$HUBPID" 2>/dev/null || { echo "hub exited before it started listening:" >&2; cat "$run/hub.log" >&2; exit 1; }
  sleep 0.1
done
[ -n "$PORT" ] || { echo "hub never logged a listening port" >&2; exit 1; }
HUB=http://127.0.0.1:$PORT

# 6. Confirm THIS hub is the one that answered, not some other process
#    already on $PORT. Check the backgrounded PID is still alive
#    before trusting the HTTP response — a dead PID with a 401
#    anyway means something else is listening at $PORT. (Belt-and-braces
#    only now that the port itself is kernel-assigned rather than
#    guessed — kept because a dead PID + something else answering is
#    still worth distinguishing from a live hub.)
kill -0 "$HUBPID" || { echo "hub failed to start on $PORT" >&2; exit 1; }
curl -s -o /dev/null -w "%{http_code}\n" "$HUB/"  # → 401 (auth required; means it answered)

# 7. Grab the auth token from the isolated $HOME. The browser needs it
#    in the URL query and the curl REST shim needs it as a Bearer
#    header.
TOKEN=$(cat "$HOME/.serf/auth-token")
```

The hub picks up provider credentials from env (`OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`) and/or `$HOME/.serf/credentials.toml` (the isolated
one — copy in a scratch `credentials.toml` first if a scenario needs a
specific provider's stored key; see `credentials-page-displays-sources.md`
for the pattern). If a scenario needs a specific provider, check
`./serf <provider> status` first.

OpenAI footgun: an inherited `OPENAI_API_KEY` takes precedence over
stored OAuth. If `serf openai status` is signed in but a GPT run fails
with API-key quota, start the test hub with the key cleared:

```bash
OPENAI_API_KEY= "$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" 2>"$run/hub.log" &
```

This is the one deliberate exception to step 3: a scenario that needs
an *already signed-in* OpenAI OAuth session has to run with the real
`$HOME` (OAuth state lives under the normal user state home, not
somewhere a fresh isolated `$HOME` would have it), so do not export a
throwaway `$HOME` for that run. It must still always bind `-addr
127.0.0.1:0` — never port `9180`, as usual. Running with the real
`$HOME` means this hub *does* share Jesse's real credentials/providers/
session history for the duration of the run; kata `66mb` flagged this
as a narrower, separate hazard from the port issue and left it for a
follow-up (see `kata show` for the filed ID) rather than solving it
here.

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

`SID` is a 22-character UUIDv7 base62 payload. The session's
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
go run ./cmd/serf-doctor tree "$SID" --state-dir "$state" --observers
go run ./cmd/serf-doctor transcript "$SID" --state-dir "$state" --format outline --range last:80
go run ./cmd/serf-doctor transcript "$SID" --state-dir "$state" --count job_list
go run ./cmd/serf-doctor transcript "$SID" --state-dir "$state" --count job_read_output
go run ./cmd/serf-doctor transcript "$OBSERVER_SID" --state-dir "$state" --format outline --range last:80
go run ./cmd/serf-doctor transcript "$OBSERVER_SID" --state-dir "$state" --count delegate_send
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

### Claim your own Chrome instance first (kata `8ecz`)

`use_browser` auto-starts Chrome on first use under whatever profile is
currently set — the shared default (`superpowers-chrome`) if nothing
ever set one. Every concurrent browsing agent that skips this joins the
*same* Chrome process and shares its tabs: `new_tab` followed by an
`eval` can land on another agent's tab, `switch_tab` can land you on a
backgrounded tab whose `requestAnimationFrame`/`ResizeObserver` silently
never fire (a confidently wrong measurement, not a visible failure),
and one agent's `navigate` can pull the page out from under another
mid-measurement. `set_profile` refuses once Chrome is already running,
so this has to happen before your first `use_browser` call of the
session — there's no fixing it after the fact, and killing the shared
Chrome to reprofile would disrupt every other agent using it:

```text
set_profile <profile-name>     # e.g. the worktree/branch name — literally
                                # the first use_browser call of the run
```

Use the git worktree or branch name as the profile name: git already
guarantees it's unique among your concurrent siblings (`git worktree add
-b <name>` refuses a name already in use), so it costs nothing to derive
and can't collide the way a name picked "by taste" can. This gives every
agent its own Chrome process — its own tabs, its own profile directory —
so tab-stealing and cross-agent navigation become structurally
impossible rather than merely detectable.

Keep asserting `location.port` (or another page-identity check) inside
`eval` payloads regardless. A unique profile stops *other* agents from
landing on your tabs; it doesn't stop your own script from targeting the
wrong tab within your own profile (e.g. after a `new_tab` you forgot to
`switch_tab` to). The assertion still converts a wrong measurement into
a loud failure — it just no longer has to defend against the whole
fleet.

**Report the gap, don't paper over it.** If a browser step is skipped,
degraded, or gives an ambiguous read (an assertion failed, a tab looked
foreign, a screenshot got discarded), say so explicitly in your
completion report — e.g. `Browser-verified: no (assertion failed on tab
N, treated as unit-tested only)` — rather than reporting the underlying
code change as "done" with the gap left implicit. A fleet-level ledger
that can't tell a browser-verified fix from an unverified one is exactly
what let this kata's incidents go unnoticed until measured after the
fact.

### Authenticated navigation

```text
navigate $HUB/auth?token=<TOKEN>&next=/s/<SID>
await_element [data-steer-trigger]      # or any element you expect
```

The `/auth` route sets the session cookie then redirects to `next`.
Use the literal token from `$HOME/.serf/auth-token` (the isolated
`$HOME` from the Setup checklist), not the path. If you get
`"invalid token"` rendered, you passed the path.

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

Each scenario gets its own tmux session. Naming matters — the cleanup
step needs a deterministic name, and it needs to be **your** name: two
agents both launching a literal `-t serf-test` will `kill-session` each
other's TUI (tmux session names are as collision-prone as a fixed port
or a fixed `/tmp` path, for the same reason — nothing makes them unique
by construction). Derive it from `$run`, which `mktemp` already made
unique:

```bash
TMUX_SESSION="serf-test-$(basename "$run")"
tmux kill-session -t "$TMUX_SESSION" 2>/dev/null   # idempotent: prior run
tmux new-session -d -s "$TMUX_SESSION" -x 200 -y 50 \
  "$run/serf-tui --hub-addr 127.0.0.1:$PORT --debug 2>$run/tui-stderr.log"
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
tmux send-keys -t "$TMUX_SESSION" "n"                # tap "n" for new session
sleep 1
tmux send-keys -t "$TMUX_SESSION" BTab               # shift-tab back one field
sleep 0.3
tmux send-keys -t "$TMUX_SESSION" C-u                # clear the current line
sleep 0.3
tmux send-keys -t "$TMUX_SESSION" -l "$tmpdir"       # literal — no key parsing
sleep 0.3
tmux send-keys -t "$TMUX_SESSION" Tab                # forward to next field
sleep 0.3
tmux send-keys -t "$TMUX_SESSION" -l "Read AGENTS.md and write a long essay."
sleep 0.3
tmux send-keys -t "$TMUX_SESSION" Enter              # spawn
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
  pane=$(tmux capture-pane -t "$TMUX_SESSION" -p)
  echo "$pane" | grep -q "state: processing" && { echo "i=$i processing"; break; }
  sleep 1
done
```

For optimistic-rendering scenarios, take two captures — one
synchronously after the keypress, one after a reconcile window —
mirroring the web sync/async pattern:

```bash
tmux send-keys -t "$TMUX_SESSION" -l "Stop and write a haiku."
sleep 0.5
tmux send-keys -t "$TMUX_SESSION" C-s
sleep 0.3
echo "=== synchronous ===" ; tmux capture-pane -t "$TMUX_SESSION" -p | grep -E "draining|⠋|Force-steer"
sleep 6
echo "=== reconciled  ===" ; tmux capture-pane -t "$TMUX_SESSION" -p | grep -E "draining|⠋" || echo "[no pending — reconciled]"
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
`$HOME/.local/state/serf/projects/<project-id>/sessions/` (the
isolated `$HOME` from the Setup checklist):

```bash
TS=$(find "$HOME/.local/state/serf/projects" -name "$SID.transcript.jsonl")
META=$(find "$HOME/.local/state/serf/projects" -name "$SID.meta.json")

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

1. **Daemon** — `cmd/serf/` and `agent/`. Rebuild: `go build -o "$run/serf" ./cmd/serf`. The hub re-spawns it per session, so the next spawned session picks up the new binary.
2. **Hub** — `cmd/serf-hub/` and `server/`. Rebuild + kill the running hub by PID (not `pkill -f`, which would also kill any other concurrent agent's hub), then restart it the same way as step 4 of the setup checklist — it binds a *new* ephemeral port, so re-read `$run/hub.log` for the new `PORT`/`HUB`: `kill "$HUBPID"; go build -o "$run/serf-hub" ./cmd/serf-hub && "$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" 2>"$run/hub.log" & HUBPID=$!`.
3. **Web renderer** — `cmd/serf-hub/assets/*.js`. These files are embedded into `serf-hub`; rebuild and restart the hub, then refresh the browser tab.
4. **TUI** — `cmd/serf-tui/`. Rebuild: `go build -o "$run/serf-tui" ./cmd/serf-tui`. The running TUI keeps the old binary in memory — kill the tmux session (`tmux kill-session -t "$TMUX_SESSION"`) and restart for the new code.
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

# Kill the hub you started, by the PID you captured — not by a
# `pkill -f serf-hub-test` pattern match, which would also kill any other
# concurrent agent's test hub (they're all named "serf-hub" now that each
# one lives under its own $run dir instead of a fixed /tmp/serf-hub-test).
kill "$HUBPID" 2>/dev/null

# Remove this run's own directory — not a `rm -rf /tmp/serf-e2e-*` glob,
# which would also delete every other concurrent scenario's workdir (kata
# k2rx: a wildcard cleanup is exactly as collision-prone as a wildcard
# create). $run already covers the hermetic workdir/state dirs below if
# you made them under it; otherwise remove those explicitly too.
rm -rf "$run"
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

This matters when an observer calls `communicate(end_turn=true)`: the
observer callback can be consumed before the parent emits a scripted
"done" marker, or the model may choose to stop after handling the
callback. Treat the durable watch rows and observer transcript as the
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
  the observer's `communicate(end_turn=true)` callback.
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

- **Hub address**: `127.0.0.1:$PORT` — read back from `$run/hub.log`
  after starting the hub with `-addr 127.0.0.1:0` per the Setup
  checklist, never Jesse's real `9180`.
- **Auth token**: `$HOME/.serf/auth-token` — the isolated `$HOME` from
  the Setup checklist, never Jesse's real one.
- **Follow-up turn** (after the initial spawn prompt): `POST /s/<SID>/send` with body `{"text":"..."}` (the spawn only starts turn 1; subsequent user turns go here).
- **Recursion opt-in** (delegate subagents that can themselves delegate): per-spawn `launch_overrides.maxSubagentDepth:N` raises the root's own delegation allowance to N. Omitted/default is 1 (a root may delegate, but its delegates are leaves) — recursion is dark without this.
- **Per-session transcript**: `$HOME/.local/state/serf/projects/<project-id>/sessions/<SID>.transcript.jsonl`
- **Per-session meta**: same dir, `<SID>.meta.json`
- **TUI debug stderr** (when launched with `--debug`): redirect via `tmux new-session -d -s "$TMUX_SESSION" "$run/serf-tui --hub-addr 127.0.0.1:$PORT --debug 2>$run/tui-stderr.log"`
- **Browser console capture**: `~/.cache/superpowers/browser/<date>/<session>/<NNN>-<action>-console.txt`
- **Kata CLI**: `~/go/bin/kata` (see `kata create --help`)
- **Browser profile** (own-Chrome-instance isolation): `set_profile` with a name derived from your worktree/branch — see "Driving the web UI" below. Do this before the first `use_browser` call of the run; a shared default profile is the root cause of kata `8ecz`.
