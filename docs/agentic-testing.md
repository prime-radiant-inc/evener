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
echo "$HUBPID" >"$run/hub.pid"   # so a later shell can kill it by pid, not by pattern

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

### Handing this hub to a sibling card

Some cards share one hub on purpose. `ask-web-answer.md` stands one up and
`ask-restart-rederive.md`, `ask-cross-session-notify.md`,
`ask-two-clients.md`, `ask-noninteractive-invisible.md`,
`ask-subagent-invisible.md` and `ask-tui-answer.md` all say "reuse that
card's hub if it's still running" rather than each paying for its own hub,
credential export, and authenticated browser tab. They used to do that by
**agreeing on a number** — one hand-picked port, written into all six — which
is exactly the collision domain kata 68fm removed everywhere else: two agents
running the ask set at the same time contend for one listener, and the loser
gets a bind failure or, worse, the winner's sessions.

**The thing you hand over is the run directory, not the port.** Everything a
sibling needs is already under `$run`, and the recipe above already wrote all
of it down, so the owning card exports one variable and the sibling
re-derives the rest:

```bash
# Owning card, once the checklist above has run:
export SERF_E2E_RUN="$run"

# Sibling card — works in the owning shell or a fresh one:
run=${SERF_E2E_RUN:?run ask-web-answer.md's Pre-state first, then export SERF_E2E_RUN="$run"}
export HOME="$run/home"
unset XDG_STATE_HOME
PORT=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub.log" | grep -oE '[0-9]+$' | tail -1)
HUB=http://127.0.0.1:$PORT
TOKEN=$(cat "$HOME/.serf/auth-token")
HUBPID=$(cat "$run/hub.pid")
kill -0 "$HUBPID" 2>/dev/null || { echo "that hub is gone — re-run the owning card's Pre-state" >&2; exit 1; }
```

No new file and no new vocabulary: `$run/hub.log` is the port's only
authority (step 5 above reads the same line, and a second copy in a
`hub.port` file would be a second thing to go stale), and `$run/hub.pid` is
what lets a shell that never backgrounded the hub still tear it down by pid
instead of by `pkill -f`. Those are the same two files
`scripts/e2e-ratelimited-provider.sh` writes so its `--stop` works from a
fresh invocation. `tail -1` on the log matters: a hub restarted mid-run
(rebuild matrix item 2) appends a second `listening on` line and the last one
is the live one.

Whoever started the hub owns tearing it down. A sibling card that reused a
running hub leaves it up; a sibling card that had to start its own (because
`SERF_E2E_RUN` was unset) kills it in its own Cleanup, by that pid.

The alternative — make every card self-sufficient and delete the reuse
language — was considered and rejected. The reuse costs nothing in test
value (each of these cards spawns its own sessions; none reads state another
one wrote), and paying for six hub starts, six credential exports and six
fresh browser authentications buys nothing back. The number was the problem,
not the sharing.

The hub picks up provider credentials from env (`OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`) and/or `$HOME/.serf/credentials.toml` (the isolated
one — copy in a scratch `credentials.toml` first if a scenario needs a
specific provider's stored key; see `credentials-page-displays-sources.md`
for the pattern). If a scenario needs a specific provider, check
`./serf <provider> status` first.

Restarting the stack mid-run (e.g. to pick up a rebuilt binary), two
measured traps:

- **Swap binaries with a fresh inode** (`rm` then `cp`, or `cp` to a
  temp name and `mv`), never `cp` over the existing file: macOS caches
  the code signature by inode, and a binary that has already been
  exec'd gets SIGKILLed on its next exec after an in-place overwrite —
  surfacing as the hub's opaque `serf launch-check failed: signal:
  killed`.
- **Re-export the provider keys in the relaunch shell** (`set -a; .
  .env; set +a` again): the keys lived only in the original launch
  shell's environment, and a bare relaunch fails `thread/start` with
  `provider credentials missing` even though the first hub worked.

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
`$HOME` means this hub *does* share Jesse's real credentials/providers/auth-token/hub.lock
for the duration of the run (kata `66mb` flagged this as a narrower,
separate hazard from the port issue; kata `93f5`/`keyb` worked out the
following two mitigations, which don't require solving the credential
sharing itself):

- **Check for the flock before you start, don't debug it after.** The
  host-wide lock at `~/.serf/hub.lock` is exclusive regardless of any
  other override, so this hub *cannot start at all* while Jesse's real
  one already holds it — that failure looks like a hang or a generic
  bind error, not an obvious "already running" message. Check first:
  `pgrep -f 'serf-hub.*:9180'` (matches however the real hub was
  launched — flag form, bind address, and all — because it keys on the
  port, not the invocation shape).
- **Session history does not have to be shared even here.** Exporting
  `XDG_STATE_HOME` to a scratch directory before starting the hub
  relocates every session it spawns away from Jesse's real
  `~/.local/state/serf/projects` (`cmd/serf-hub/config.go`'s
  `DefaultStateGlob` prefers `XDG_STATE_HOME` over `$HOME/.local/state`
  when it's set) — with zero effect on the credentials/token/lock paths
  above, since those are keyed off `$HOME` directly (`DefaultHubStateRoot`),
  not off `XDG_STATE_HOME`. `rm -rf` the scratch dir in Cleanup.
  `test/scenarios/web-goal-set-and-complete.md` and
  `sidecar-approval-broker-communicate.md` use this pattern.

## Testing against a rate-limited provider

Some scenarios need a provider that is ACTUALLY throttling — verifying the
retry backoff, the honest-liveness "may be stalled" line, or a TUI
notification for a model-call retry (kata 4zn8, e79v) — for as long as the
check takes. A real provider will not reliably do that on demand.
`test/e2e/fake429` is a fake OpenAI-compatible backend that answers
`/v1/models` normally (so launch-check and model discovery succeed) and
429s every completion request with a configurable `Retry-After`.

`scripts/e2e-ratelimited-provider.sh` wraps it around the Setup checklist
above — same HOME isolation, same kernel-assigned ports, never a real
provider credential — and points the isolated hub's `providers.toml` at it:

```bash
scripts/e2e-ratelimited-provider.sh --retry-after 5
```

prints the run directory, fake429's address, and the exact `serf-tui
--hub-addr ... --auth-token ... --no-auto-start-hub` command to attach.
Spawn a session with `"model":"ratelimited/fake-model"` (see "Spawning a
session via the REST shim" below) and every completion call it makes will
429. Tear down with `scripts/e2e-ratelimited-provider.sh --stop RUN_DIR`
(kills fake429 and the hub, removes the run directory).

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

The state vocabulary is fixed and shared by the web rail, the TUI, and
this REST shim: `idle`, `active`, `awaiting`, `warning`, `errored`,
`ended`, `notLoaded` (`hubcore.NormalizeState`,
`cmd/serf-hub/internal/hubcore/tree.go:391-414`, normalizing
`appwire.ThreadStatus*`, `appwire/types.go:138-145`). A running turn is
**`active`**, never `processing` — `processing` is not a wire value at
all, and `test/scenarios/scenario_docs_test.go`'s
`TestScenarioDocsUseCanonicalActiveState` fails the build on any card
that writes `state=processing`. Wait for the state the scenario needs:

```bash
for i in $(seq 1 60); do
  state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
            | jq -r '.state // ""' 2>/dev/null)
  [ "$state" = "idle" ] && break          # change "idle" to "active" as needed
  sleep 1
done
echo "state=$state"
```

The same `/api/sessions/local:$SID` response carries
`capabilities.steer`, `capabilities.queue`, etc. — useful for
asserting daemon-side gating (kata `wymv`). It also carries
`active_turn_id`, which the steer paths need: a turn is only truly in
flight once both the status flip and the turn id have landed
(`submitRouting.ts:48-50`'s `isTurnActive`).

### The REST surface, and what is no longer on it

There is exactly one session REST namespace now: `/api/sessions/<ref>`,
where `<ref>` is the canonical `local:<SID>` form. The dispatcher is
`handleAPISession` (`cmd/serf-hub/web_api_tree.go:1360-1419`) and the
whole verb list is:

| Route | Method | Notes |
|---|---|---|
| `/api/sessions/local:$SID` | GET | the detail object polled above |
| `/api/sessions/local:$SID/details` | GET | same payload |
| `/api/sessions/local:$SID/send` | POST | `{"text":"…"}` — a follow-up user turn |
| `/api/sessions/local:$SID/interrupt` | POST | |
| `/api/sessions/local:$SID/compact` | POST | the one action that can resume an ended session |
| `/api/sessions/local:$SID/shutdown` | POST | |
| `/api/sessions/local:$SID/clear` | POST | |
| `/api/sessions/local:$SID/fork` | POST | |
| `/api/sessions/local:$SID/model` | POST | |
| `/api/sessions/local:$SID/reasoning-effort` | POST | |
| `/api/sessions/local:$SID/rename` | POST | |
| `/api/sessions/local:$SID/delete` | POST | |
| `/api/sessions/local:$SID/tasks` | GET | |

**The old `/s/<id>/<action>` form-POST shim is gone** — commit
`660376f78` deleted it along with the vanilla-JS frontend, and
`web_workspace.go:16-22` says so in a comment: `/s/<id>` now serves only
the SPA shell and `/s/<id>/images/<sha>`, and every other sub-path
returns 404. A card that still curls `$HUB/s/$SID/shutdown` gets a 404
and a silently-not-shut-down session, which then poisons the next run's
`state: idle` poll.

**There is no REST route for steer, queue, or drain-as-steer at all.**
Those three live only on the AppWire WebSocket as `turn/steer`,
`turn/queue`, and `turn/drainAsSteer` (`appwire/types.go:24,26-27`), so
a scenario that needs the *user-visible* behaviour has to drive the
composer in a browser — see "Driving the web UI" below.
`capabilities.steer`/`capabilities.queue` on the detail object still
report whether the daemon *would* accept them, which is enough for a
gating-only assertion without a browser. For the wire contract itself,
dial the socket directly:

### Driving AppWire directly (the browser-free lever)

`ws://127.0.0.1:$PORT/rpc`, `Authorization: Bearer $TOKEN`, then
`initialize` before anything else. This is how a card asserts a
daemon-side contract — windowed reads, CAS preconditions, refusal
messages — exactly, without a browser or a bundle.

**Frames carry no `jsonrpc` field.** This is the one footgun, and it is
expensive because of how it fails: `Message.UnmarshalJSON` rejects the
frame outright (`"jsonrpc field is not part of AppWire"`,
`appwire/jsonrpc.go:164-166`), `Recv` returns the error, and the receive
loop closes the socket. What you observe is a connection that drops the
moment you send — which reads as a network or auth problem, not as a
malformed request. Send:

```json
{"id": 1, "method": "initialize",
 "params": {"protocolVersion": "serf-appwire-v2",
            "clientInfo": {"name": "scenario", "version": "0"},
            "capabilities": {"experimentalApi": false}}}
```

Notifications for other threads arrive interleaved with your responses,
so match on `id` rather than reading the next frame and hoping.

A session to aim a gating assertion at costs nothing and needs no
provider credential: spawn with an empty `prompt` and the daemon launches
without running a turn — a *dormant* session, which reports `state:"idle"`
like any other quiet session and is only distinguishable by the `dormant`
field (`hubapi/types.go:115-119`). No completion request is ever made.

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

The browsing skill exposes one tool (`mcp__plugin_superpowers-chrome_chrome__use_browser`) with action verbs.

**There is no renderer handle to drive.** The vanilla-JS frontend that
published `window.SerfRenderer` and `window.SerfAppwire` was deleted
wholesale at commit `660376f78` (2026-07-22), and the React app that
replaced it exposes nothing on `window` — the only `window.*` reference
left in `cmd/serf-hub/frontend/src` outside tests is an `AudioContext`
lookup (`notifications/channels.ts:45`). Anything in an older card that
reads `window.SerfRenderer?.state` or calls `window.SerfAppwire.steer(…)`
returns `undefined` / throws, and an `eval` that does so **fails open**:
it reports "no chips found", which reads exactly like a real regression.

So the driving surface is the DOM, and only the DOM:

1. `data-testid` hooks, for controls whose accessible name is ambiguous.
2. Accessible names and visible text, for everything else — this is what
   `test/scenarios/README.md` already asks for ("prefer labels the user
   sees").
3. `localStorage` under the `serf.prefs.*` / `serf.rail.*` contracts, for
   preconditions, seeded **before** the first page load.
4. The REST shim and the on-disk transcript, for anything the DOM can
   only hint at.

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

**The human's keyboard wins.** A visible shared Chrome takes real window
focus, so a human using the same machine can interleave keystrokes or a
paste with your `type` action — measured once as a vite dev-server URL
landing inside a word mid-type, storing
`askttp://192.168.118.83:5173/_user` where `ask_user` was typed, all the
way into the daemon transcript. When a human may be active, either drive
an isolated profile or read the field (or the stored turn) back and
compare against what you sent before trusting any result derived from
typed input.

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
navigate $HUB/auth?token=<TOKEN>&next=/s/local:<SID>
await_element [data-testid="composer-input-card"]
```

The `/auth` route (`web.go:174`) sets the session cookie then redirects
to `next`. Use the literal token from `$HOME/.serf/auth-token` (the
isolated `$HOME` from the Setup checklist), not the path. If you get
`"invalid token"` rendered, you passed the path.

**A session URL is `/s/<hostID>:<sessionID>`, and a bare session id is
not a URL.** `isRef` (`shell/routing.ts:29-32`) requires a colon with
non-empty text on both sides; `urlToPane` returns `null` for anything
else and `AppShell` renders `NotFound` — the words "Page not found" and
"This link doesn't match anything in serf." (`shell/NotFound.tsx:16-17`).
That is deliberate (commit `8cea30ca6`, "no back-compat"): the rail
opens sessions as `local:<id>`, so a bare-id deep link used to open the
same session a second time in a second pane. The Go side is not the
gate — `/s/` serves the SPA shell for any id (`web_workspace.go:38-39`) —
so the 404 you see is client-side, and `curl`ing `/s/<bare-id>` still
returns 200 with the shell. Assert the rendered text, not the status code.

**Decode `location.pathname` before comparing it.** `paneToURL` builds
the path with `encodeURIComponent(ref)` (`shell/routing.ts:93-96`), which
escapes the colon, so a path the app *pushed* reads
`/s/local%3A<SID>` — while a path you navigated to yourself keeps the
literal colon you typed. Both are the same route; only one of them
matches a naive `=== "/s/local:<SID>"`. Compare
`decodeURIComponent(location.pathname)`.

### The selector map

Every hook below was read out of the current tree. Prefer these over
inventing a CSS path; if you need one that isn't here, grep
`data-testid` in `cmd/serf-hub/frontend/src` rather than guessing.

**Composer** (`panes/session/composer/Composer.tsx`):

| Hook | What it is |
|---|---|
| `[data-testid="composer-input-card"]` | the prompt card; the textarea inside is `[aria-label="Message"]`, placeholder `Message the agent…` (`:783-784`) |
| `[data-testid="composer-submit"]` | **Send**. Routes to `turn/queue` while a turn runs, `turn/start` otherwise (`submitRouting.ts:19-23`) — one label, two timings |
| `[data-testid="composer-steer"]` | **Steer**. Renders only while `busy && capabilities.steer` (`:382`) |
| `[data-testid="composer-stop"]` | **Stop** (interrupt) |
| `[data-testid="composer-attach"]` | the paperclip; opens the hidden `input[type=file]` |
| `[data-testid="pending-chips"]` | optimistic in-flight chips, labelled `Sending` / `Steering` / `Draining` (`pending/PendingChips.tsx:38-42,56`) |

Shift+Enter is the Steer chord and reaches `handleSteerClick`
(`Composer.tsx:685-689`) **even when the Steer button is not rendered** —
that is the only way to attempt a steer against an idle session from the
UI. `/` in an empty composer opens the command palette (`:673-676`).

**Queue strip** (`composer/queue/QueueStrip.tsx`) has no testids; address
it by its text: the heading `Queued messages (N)` (`:278`), the
drain-everything button `Steer queue now` (`:282`), and per-row actions
`Steer now`, `Edit message`, `Remove from queue` (`:305-333`). Rows for
mutations whose fate is unknown read `Delivery uncertain — <text>` with a
`Retry` button; rows whose session was deleted read `Destination
deleted — <text>` with `Copy` (`:349-374`).

**Rail** (`shell/rail/RailRow.tsx`, `Rail.tsx`): a session row is
`[data-session-ref="local:<SID>"]` (`RailRow.tsx:509`) — note
`data-session-ref`, not the legacy `data-ref`, and there is no `.sb-row`
class anywhere. Inside it: `[data-testid="rail-row-activity"]` (the
second, gloss line — this is where the state word lands, **lowercased**
by `humanizeState`, unlike `hubapi.StateWord`'s sentence case),
`[data-testid="rail-row-time"]`, `[data-testid="rail-row-not-started"]`,
`[data-testid="favorite-star"]`, `[data-testid="rail-row-overflow"]`.
Rail chrome: `[data-testid="rail-search"]`, `[data-testid="rail-settings"]`,
`[data-testid="rail-brand"]`, `[data-testid="rail-chevron"]`. There is no
separate "needs you" section — the Rail deliberately does not build one
(`Rail.tsx:563`).

**Transcript** (`panes/session/transcript/`): `[data-testid="turn-block"]`
(carries `data-turn-id`), `[data-testid="user-message-item"]`,
`[data-testid="agent-message-item"]`, `[data-testid="steering-item"]`,
`[data-testid="tool-row"]`, `[data-testid="tool-call-item"]` (carries
`data-tool-name`), `[data-testid="think-block"]`,
`[data-testid="turn-failure"]`, `[data-testid="system-notice-line"]`,
`[data-testid="notification-card"]`, `[data-testid="image-gallery-thumb"]`
and `[data-testid="image-gallery-lightbox-img"]`,
`[data-testid="load-older-row"]` / `[data-testid="load-older-sentinel"]` /
`[data-testid="load-older-retry"]` (`flow/LoadOlderRow.tsx:82-91`),
`[data-testid="new-content-pill"]`, `[data-testid="seen-divider"]`.

**Session chrome** (`panes/session/chrome/`):
`[data-testid="session-chrome"]`, `[data-testid="status-row"]` and its
facts (`status-row-effort`, `status-row-context`, `status-row-cost`,
`status-row-queue`, `status-row-work-time`, `status-row-failures`),
`[data-testid="model-switch-trigger"]` / `[data-testid="model-switch-value"]`,
`[data-testid="goal-popover"]`, `[data-testid="task-row"]`.

**Spawn** (`panes/spawn/Spawn.tsx`): `[data-testid="spawn-prompt-card"]`,
`[data-testid="spawn-submit"]`, `[data-testid="spawn-attach"]`,
`[data-testid="spawn-branch"]`. The model picker is the shared ARIA
combobox in `widgets/modelCatalog/` — `role="option"` rows, not the
legacy `.chip-picker-*` classes.

**Toasts** are the error channel for actions that fail without a
dedicated surface: `section[aria-live="polite"][aria-label="Notifications"]`
(`widgets/toast/index.tsx:36`).

### Seeding preferences before the first load

Preferences are flat `localStorage` keys under `serf.prefs.<name>`,
hydrated **once at module load** (`stores/prefs.ts:110-125`), so a write
after the page is up does not retroactively change behavior — navigate to
any authenticated page first, write the keys, then reload:

```javascript
// values are the strings "1" / "0", never JSON
localStorage.setItem("serf.prefs.notificationsTitle", "1");
localStorage.setItem("serf.prefs.notificationsFavicon", "1");
```

Notification prefs are `notificationsTitle`, `notificationsFavicon`,
`notificationsOs`, `notificationsSound` (`prefs.ts:223-228`) and **all
four default to OFF** (`:266-273`) — a card that expects a tab-title
badge without opting in is asserting the wrong default. There is no
`serf-hub.notifications` JSON blob any more. Rail expansion state is one
JSON blob under `serf.rail.expanded.v1` (`shell/rail/railExpansion.ts:19`).

### Synchronous vs. async assertion shape

For any "did the optimistic UI update happen before the RPC resolved?"
scenario, this is still the pattern — but the trigger is now a real
click, because there is no promise-returning API to hold. Click without
awaiting anything, snapshot synchronously, then settle and snapshot
again:

```javascript
(async () => {
  const chips = () => Array.from(
    document.querySelectorAll('[data-testid="pending-chips"] li'),
    (li) => li.textContent,
  );
  const before = chips();
  document.querySelector('[data-testid="composer-steer"]').click();
  // Synchronous: the optimistic chip is in the DOM RIGHT NOW.
  const sync = chips();
  await new Promise((r) => setTimeout(r, 2000)); // let the ack land
  const after = chips();
  const toast = document.querySelector('[aria-label="Notifications"]')?.textContent;
  return JSON.stringify({ port: location.port, before, sync, after, toast }, null, 2);
})()
```

Without the no-await capture, the test can't distinguish "pending chip
rendered and was reconciled" from "pending chip never rendered." A chip
that is still present after the settle window is the failure signal:
reconciliation is driven by the `clientMutationId` coming back on
`thread/queueChanged`, `serf/steering/injected`, `item/*` or `turn/*`
(`stores/threads.ts:797-810`), so a stuck chip means the ack never
arrived.

### Probing without a renderer handle

`window.SerfRenderer` is gone; ask the DOM and the server instead.

```javascript
JSON.stringify({
  port: location.port,                     // page-identity check, always
  path: location.pathname,                 // should be /s/local:<SID>
  steerRendered: !!document.querySelector('[data-testid="composer-steer"]'),
  stopRendered: !!document.querySelector('[data-testid="composer-stop"]'),
  turns: document.querySelectorAll('[data-testid="turn-block"]').length,
  activity: document.querySelector('[data-testid="rail-row-activity"]')?.textContent,
  queueHeading: document.querySelector("h3")?.textContent,   // "Queued messages (N)"
})
```

`steerRendered` is the closest thing left to the old `activeTurnId`
probe: Steer renders only while the turn is genuinely in flight
(`Composer.tsx:382`). If it never appears while
`/api/sessions/local:$SID` reports `state=active`, the AppWire socket
did not hydrate — check `$run/hub.log`, and confirm the page is really
the one you think it is via the `location.port` assertion above.

The authoritative counterpart to any of this is the REST detail object
and the on-disk transcript. When a DOM read is ambiguous, do not add
more selectors — cross-check `/api/sessions/local:$SID` and
`serf-doctor transcript`, which cannot be fooled by a stale tab.

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
3. **Web UI** — `cmd/serf-hub/frontend/src/` (TypeScript/React). Two steps, and skipping the first is the classic "my change didn't take": `make build-web` compiles it into `cmd/serf-hub/frontend/dist`, which `webnext.go`'s `//go:embed all:frontend/dist` bakes into the hub binary. So rebuild the frontend, **then** rebuild and restart the hub, then hard-refresh the tab. A checkout that has never run `make build-web` has a one-line `dist/PLACEHOLDER` and serves no app at all. Agent worktrees symlink `node_modules` to a shared install — `make web-preflight` refuses to `npm ci` through that symlink on purpose (it would empty the install for every other worktree); refresh it at the target instead.
4. **TUI** — `cmd/serf-tui/`. Rebuild: `go build -o "$run/serf-tui" ./cmd/serf-tui`. The running TUI keeps the old binary in memory — kill the tmux session (`tmux kill-session -t "$TMUX_SESSION"`) and restart for the new code.
5. **AppWire types** — `appwire/`. Both daemon and hub statically link these; rebuild both. The generated TypeScript mirror (`frontend/src/protocol/types.gen.ts`) is a third consumer — a wire change that only rebuilds the Go side leaves the browser decoding the old shape.
6. **Optimistic-mutation plumbing** — the TUI's pending coordinator and the web's durable outbox (`frontend/src/stores/mutationOutbox.ts`, `panes/session/composer/queue/pendingTurnsStore.ts`); same rebuild rules as 4 and 3 respectively.

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
# Shut down any sessions you spawned. The canonical ref form is
# local:<SID> and the namespace is /api/sessions — the old
# /s/<id>/shutdown shim 404s silently, leaving the daemon running.
for sid in $SID1 $SID2 $SID3; do
  curl -s -X POST -H "Content-Type: application/json" \
    -H "Authorization: Bearer $TOKEN" -d '{}' \
    "$HUB/api/sessions/local:$sid/shutdown" >/dev/null 2>&1
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
- **Follow-up turn** (after the initial spawn prompt): `POST /api/sessions/local:<SID>/send` with body `{"text":"..."}` (the spawn only starts turn 1; subsequent user turns go here). See "The REST surface" above for the full verb list and for the three verbs — steer, queue, drain-as-steer — that have no REST route at all.
- **Session URL**: `/s/local:<SID>`. A bare `/s/<SID>` renders "Page not found" client-side, by design.
- **Recursion opt-in** (delegate subagents that can themselves delegate): per-spawn `launch_overrides.maxSubagentDepth:N` raises the root's own delegation allowance to N. Omitted/default is 1 (a root may delegate, but its delegates are leaves) — recursion is dark without this.
- **Per-session transcript**: `$HOME/.local/state/serf/projects/<project-id>/sessions/<SID>.transcript.jsonl`
- **Per-session meta**: same dir, `<SID>.meta.json`
- **TUI debug stderr** (when launched with `--debug`): redirect via `tmux new-session -d -s "$TMUX_SESSION" "$run/serf-tui --hub-addr 127.0.0.1:$PORT --debug 2>$run/tui-stderr.log"`
- **Browser console capture**: `~/.cache/superpowers/browser/<date>/<session>/<NNN>-<action>-console.txt`
- **Kata CLI**: `~/go/bin/kata` (see `kata create --help`)
- **Rate-limited provider for retry/liveness checks**: `scripts/e2e-ratelimited-provider.sh` — see "Testing against a rate-limited provider" above.
- **Browser profile** (own-Chrome-instance isolation): `set_profile` with a name derived from your worktree/branch — see "Driving the web UI" below. Do this before the first `use_browser` call of the run; a shared default profile is the root cause of kata `8ecz`.
