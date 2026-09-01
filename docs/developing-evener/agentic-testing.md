# Agentic end-to-end testing

This is a practical guide for an AI agent (or human) running an
end-to-end scenario from `test/scenarios/` against a live `evener hub` +
`evener` daemon. The scenario files describe *what* to verify; this
document describes *how* — patterns, recipes, and footguns collected
from real sessions.

If you are writing a new scenario, see `test/scenarios/README.md` for
the file structure. This document is the runbook side.

## Setup checklist

**Never Jesse's real hub, never his port `9180`.** His `evener hub` runs
there for real, host-wide flock'd at `~/.local/state/evener/hub.lock`, with
his real auth token, credentials, providers, and session history under
`~/.config/evener` and `~/.local/state/evener`. A test hub started with a bare `$HOME` and
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
run=$(mktemp -d -t evener-e2e-XXXXXX)

# 2. Build fresh binaries from the branch under test, into the run dir —
#    not a fixed /tmp/evener-hub-test that a second concurrent build would
#    overwrite mid-run (kata k2rx: this is exactly the shared-tmp-path
#    collision that clobbered another agent's binaries and providers.toml).
#
#    make build-web comes first because the hub SERVES the SPA out of its
#    own binary: frontend/dist is not tracked (only a one-line
#    PLACEHOLDER) and webnext.go embeds it at compile time
#    (`//go:embed all:frontend/dist`), so a bare `go build ./cmd/evener/`
#    in a fresh checkout or worktree embeds nothing and every page route
#    answers `503 evener hub web app not built: run 'make build-web' and
#    rebuild`. /api/* still answers normally, so the first symptom is a
#    browser step failing minutes in (kata a6k8). frontend/dist is the one
#    artifact that lands in the worktree instead of under $run — it is an
#    input to the hub binary, not run state. A card with no browser steps
#    can skip this line; the make-based builds (make build / build-hub /
#    build-runtime) already depend on build-web and never need it. A
#    worktree that symlinks node_modules to the shared install builds
#    straight through; if web-preflight refuses because that install was
#    built from a different package-lock.json, see the rebuild matrix
#    under "Falsification debugging".
make build-web
go build -o "$run/evener" ./cmd/evener
go build -o "$run/evener" ./cmd/evener

# 3. Isolate. A throwaway $HOME keeps hub.lock, auth-token, and session
#    history off Jesse's real ~/.local/state/evener, and credentials.toml/
#    providers.toml off his real ~/.config/evener — unsetting both
#    XDG_STATE_HOME and XDG_CONFIG_HOME too, in case the ambient shell
#    already points either somewhere real (DefaultStateGlob prefers
#    XDG_STATE_HOME over $HOME/.local/state when it's set, and
#    cmdutil.DefaultConfigRoot prefers XDG_CONFIG_HOME over $HOME/.config
#    the same way).
export HOME="$run/home"
mkdir -p "$HOME"
unset XDG_STATE_HOME
unset XDG_CONFIG_HOME

# 4. Start the hub with -addr 127.0.0.1:0 — never a hardcoded or
#    dispatch-assigned port. evener hub binds the listener itself and
#    reports the address it actually got (kata 68fm: this used to be
#    the literal string ":0", not a dialable port — "-addr 127.0.0.1:0"
#    was silently useless before the fix). Binding happens inside the
#    hub process itself, so there's no separate "probe a free port with a
#    throwaway socket, then hope nothing grabs it before the real bind"
#    step and no TOCTOU window — the port the hub logs is the port it is
#    already listening on. -evener points at the daemon binary the hub
#    spawns per session.
"$run/evener" hub -addr 127.0.0.1:0 -evener "$run/evener" 2>"$run/hub.log" &
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
#    in the URL query; HTTP and AppWire clients use it as a Bearer
#    header.
TOKEN=$(cat "$HOME/.local/state/evener/auth-token")
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
export EVENER_E2E_RUN="$run"

# Sibling card — works in the owning shell or a fresh one:
run=${EVENER_E2E_RUN:?run ask-web-answer.md's Pre-state first, then export EVENER_E2E_RUN="$run"}
export HOME="$run/home"
unset XDG_STATE_HOME
unset XDG_CONFIG_HOME
PORT=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub.log" | grep -oE '[0-9]+$' | tail -1)
HUB=http://127.0.0.1:$PORT
TOKEN=$(cat "$HOME/.local/state/evener/auth-token")
HUBPID=$(cat "$run/hub.pid")
kill -0 "$HUBPID" 2>/dev/null || { echo "that hub is gone — re-run the owning card's Pre-state" >&2; exit 1; }
```

No new file and no new vocabulary: `$run/hub.log` is the port's only
authority (step 5 above reads the same line, and a second copy in a
`hub.port` file would be a second thing to go stale), and `$run/hub.pid` is
what lets a shell that never backgrounded the hub still tear it down by pid
instead of by `pkill -f`. Those are the same two files
`scripts/e2e/e2e-ratelimited-provider.sh` writes so its `--stop` works from a
fresh invocation. `tail -1` on the log matters: a hub restarted mid-run
(rebuild matrix item 2) appends a second `listening on` line and the last one
is the live one.

Whoever started the hub owns tearing it down. A sibling card that reused a
running hub leaves it up; a sibling card that had to start its own (because
`EVENER_E2E_RUN` was unset) kills it in its own Cleanup, by that pid.

The alternative — make every card self-sufficient and delete the reuse
language — was considered and rejected. The reuse costs nothing in test
value (each of these cards spawns its own sessions; none reads state another
one wrote), and paying for six hub starts, six credential exports and six
fresh browser authentications buys nothing back. The number was the problem,
not the sharing.

The hub picks up provider credentials from env (`OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`) and/or `$HOME/.config/evener/credentials.toml` (the isolated
one — copy in a scratch `credentials.toml` first if a scenario needs a
specific provider's stored key; see `credentials-page-displays-sources.md`
for the pattern). If a scenario needs a specific provider, check
`./evener <provider> status` first.

Restarting the stack mid-run (e.g. to pick up a rebuilt binary), two
measured traps:

- **Swap binaries with a fresh inode** (`rm` then `cp`, or `cp` to a
  temp name and `mv`), never `cp` over the existing file: macOS caches
  the code signature by inode, and a binary that has already been
  exec'd gets SIGKILLed on its next exec after an in-place overwrite —
  surfacing as the hub's opaque `evener launch-check failed: signal:
  killed`.
- **Re-export the provider keys in the relaunch shell** (`set -a; .
  .env; set +a` again): the keys lived only in the original launch
  shell's environment, and a bare relaunch fails `thread/start` with
  `provider credentials missing` even though the first hub worked.

OpenAI footgun: an inherited `OPENAI_API_KEY` takes precedence over
stored OAuth. If `evener openai status` is signed in but a GPT run fails
with API-key quota, start the test hub with the key cleared:

```bash
OPENAI_API_KEY= "$run/evener" hub -addr 127.0.0.1:0 -evener "$run/evener" 2>"$run/hub.log" &
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
  host-wide lock at `~/.local/state/evener/hub.lock` is exclusive regardless of any
  other override, so this hub *cannot start at all* while Jesse's real
  one already holds it — that failure looks like a hang or a generic
  bind error, not an obvious "already running" message. Check first:
  `pgrep -f 'evener.*hub.*:9180'` (matches however the real hub was
  launched — flag form, bind address, and all — because it keys on the
  port, not the invocation shape).
- **Session history does not have to be shared even here.** Exporting
  `XDG_STATE_HOME` to a scratch directory before starting the hub
  relocates every session it spawns away from Jesse's real
  `~/.local/state/evener/projects` (`cmd/evener-hub/config.go`'s
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

`scripts/e2e/e2e-ratelimited-provider.sh` wraps it around the Setup checklist
above — same HOME isolation, same kernel-assigned ports, never a real
provider credential — and points the isolated hub's `providers.toml` at it:

```bash
scripts/e2e/e2e-ratelimited-provider.sh --retry-after 5
```

prints the run directory, fake429's address, and the exact `evener tui
--hub-addr ... --auth-token ... --no-auto-start-hub` command to attach.
Start a session with `model: "ratelimited/fake-model"` through AppWire
`thread/start` (see "Starting a session via AppWire" below) and every
completion call it makes will 429. Tear down with
`scripts/e2e/e2e-ratelimited-provider.sh --stop RUN_DIR`
(kills fake429 and the hub, removes the run directory).

## Hermetic workdir per scenario

Every scenario runs in its own scratch directory so reruns don't
inherit prior state and the spawned session's `working_dir` is
contained:

```bash
tmpdir=$(mktemp -d -t evener-e2e-XXXXX)
```

For transcript isolation, pass a per-scenario `EVENER_STATE_DIR` in
`launch_overrides.env`. This keeps the spawned daemon's sessions and
logs under one directory while still using the typed AppWire launch path:

```bash
state=$(mktemp -d -t evener-e2e-state-XXXXX)
```

If the scenario needs an `AGENTS.md` (see pacing trick below), write
it before starting. Pass `tmpdir` as `cwd` in the `thread/start` params.

## Starting a session via AppWire

The shipped web UI and TUI start sessions with the typed AppWire
`thread/start` method. For a browser-free scenario, build the method's
camelCase parameter object and send it over the authenticated AppWire socket
described in [Driving AppWire directly](#driving-appwire-directly-the-browser-free-lever):

```bash
params=$(jq -n \
  --arg prompt "$prompt" \
  --arg model "$model" \
  --arg cwd "$tmpdir" \
  --arg state "$state" \
  '{
    harness:"evener",
    cwd:$cwd,
    input:(if $prompt == "" then [] else [{type:"text", text:$prompt}] end),
    model:$model,
    launchOverrides:{env:{EVENER_STATE_DIR:$state}}
  }')
```

Send `{"id":2,"method":"thread/start","params":<params>}` after
`initialize`. The response's `result.thread.evener.ref` is the canonical
session reference; a local session has the form `local:<SID>`.

### Polling for state transitions

The state vocabulary is fixed and shared by the web rail, the TUI, and
AppWire: `idle`, `active`, `awaiting`, `warning`, `errored`,
`ended`, `notLoaded` (`hubcore.NormalizeState`,
`cmd/evener-hub/internal/hubcore/tree.go#NormalizeState`, normalizing
`appwire.ThreadStatus*`, `appwire/types.go:138-145`). A running turn is
**`active`**, never `processing` — `processing` is not a wire value at
all, and `test/scenarios/scenario_docs_test.go`'s
`TestScenarioDocsUseCanonicalActiveState` fails the build on any card
that writes `state=processing`. Poll by sending `thread/read` on the
authenticated AppWire socket:

```json
{"id":3,"method":"thread/read",
 "params":{"ref":"local:<SID>","includeTurns":false}}
```

Read `result.thread.status.type`; repeat until it reaches the state the
scenario needs. The same response carries
`result.thread.evener.capabilities` and
`result.thread.evener.activeTurnId`. A turn is only truly in flight once
both the `active` status and the active turn id have landed
(`submitRouting.ts:48-50`'s `isTurnActive`).

### Session operations

Session reads and lifecycle actions use AppWire. The current methods are:

| Operation | AppWire method |
|---|---|
| read status/details | `thread/read` |
| list tasks | `evener/tasks/list` |
| start a follow-up turn | `turn/start` |
| interrupt | `turn/interrupt` |
| compact | `thread/compact/start` |
| shutdown | `thread/shutdown` |
| clear | `thread/clear` |
| fork | `thread/fork` |
| change model | `thread/model/set` |
| change reasoning effort | `thread/reasoning-effort/set` |

There are no session REST operations. The old `/api/sessions/<ref>`
namespace is no longer registered; session reads, lifecycle actions, rename,
and deletion all use AppWire.

Rename is AppWire-only: send `evener/thread/name/set` with
`{"ref":"local:<SID>","name":"<new name>"}` on the authenticated `/rpc`
connection described below.

Deleting one session is not part of this REST namespace. The WebUI uses the
hub-scoped typed AppWire method `evener/session/delete` with
`{"ref":"local:$SID"}`; live or concurrently reserved targets are returned in
the response's `skipped` array.

**The old `/s/<id>/<action>` form-POST shim is gone** — commit
`660376f78` deleted it along with the vanilla-JS frontend, and
`web_workspace.go:16-22` says so in a comment: `/s/<id>` now serves only
the SPA shell and `/s/<id>/images/<sha>`, and every other sub-path
returns 404.

Steer, queue, and drain-as-steer likewise use `turn/steer`,
`turn/queue`, and `turn/drainAsSteer` (`appwire/types.go:24,26-27`).
A scenario that needs the *user-visible* behaviour should drive the composer
in a browser; a wire-contract or gating assertion should drive AppWire
directly.

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
 "params": {"protocolVersion": "evener-appwire-v2",
            "clientInfo": {"name": "scenario", "version": "0"},
            "capabilities": {"experimentalApi": false}}}
```

Notifications for other threads arrive interleaved with your responses,
so match on `id` rather than reading the next frame and hoping.

Navigation reads use the same socket and method catalog. Send
`evener/navigation/read` with one of these parameter shapes, then inspect the
response envelope's `data` field:

```json
{"resource":"manifest"}
{"resource":"section","section":"live","offset":0,"limit":50}
{"resource":"pin_catalog","offset":0,"limit":100}
{"resource":"pin_section","sectionId":"<id>","offset":0,"limit":50}
{"resource":"catalog","catalog":"projects","offset":0,"limit":100}
{"resource":"project","projectKey":"<key>"}
{"resource":"project_page","projectKey":"<key>","tier":"current","offset":0,"limit":50}
{"resource":"location","ref":"local:<session-id>"}
```

The response has `status`, `generationId`, `revision`, and `etag`; `status`
`not_modified` omits `data`. There is no HTTP `/api/navigation` equivalent.

A session to aim a gating assertion at costs nothing and needs no
provider credential: start with an empty AppWire `input` and the daemon
launches without running a turn — a *dormant* session, which reports `state:"idle"`
like any other quiet session and is only distinguishable by the `dormant`
field (`hubapi/types.go:115-119`). No completion request is ever made.

## Auditing sidecar scenarios

For observer sidecar scenarios, do not trust the final scenario marker
alone. Audit the parent and observer transcripts:

```bash
go run ./cmd/evener doctor tree "$SID" --state-dir "$state" --observers
go run ./cmd/evener doctor transcript "$SID" --state-dir "$state" --format outline --range last:80
go run ./cmd/evener doctor transcript "$SID" --state-dir "$state" --count job_list
go run ./cmd/evener doctor transcript "$SID" --state-dir "$state" --count read_transcript
go run ./cmd/evener doctor transcript "$OBSERVER_SID" --state-dir "$state" --format outline --range last:80
go run ./cmd/evener doctor transcript "$OBSERVER_SID" --state-dir "$state" --count delegate_send
```

For the happy path, the parent should use the current delegate result,
watch result, watched event result, and observer callback as working
signals. `job_list` and `read_transcript` should be zero before the
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
published `window.EvenerRenderer` and `window.EvenerAppwire` was deleted
wholesale at commit `660376f78` (2026-07-22), and the React app that
replaced it exposes nothing on `window` — the only `window.*` reference
left in `cmd/evener-hub/frontend/src` outside tests is an `AudioContext`
lookup (`notifications/channels.ts:45`). Anything in an older card that
reads `window.EvenerRenderer?.state` or calls `window.EvenerAppwire.steer(…)`
returns `undefined` / throws, and an `eval` that does so **fails open**:
it reports "no chips found", which reads exactly like a real regression.

So the driving surface is the DOM, and only the DOM:

1. `data-testid` hooks, for controls whose accessible name is ambiguous.
2. Accessible names and visible text, for everything else — this is what
   `test/scenarios/README.md` already asks for ("prefer labels the user
   sees").
3. `localStorage` under the `evener.prefs.*` / `evener.rail.*` contracts, for
   preconditions, seeded **before** the first page load.
4. AppWire `thread/read` and the on-disk transcript, for anything the DOM can
   only hint at.

### Coordinate browser ownership first (kata `8ecz`)

The shared `use_browser` service drives one Chrome process. Concurrent
browser agents therefore share its tabs: `new_tab` followed by an `eval`
can land on another agent's tab, and `switch_tab` can land on a backgrounded
tab whose `requestAnimationFrame`/`ResizeObserver` never fire. A successful
measurement can consequently be for the wrong page.

`set_profile` does **not** create a private browser and must not be called. It
changes one sticky value on the shared MCP server process, redirecting every
agent that uses that server. The authoritative measured behavior and fleet
rule are in `docs/developing-evener/conventions/agent-fleets.md` under “Chrome is one shared
instance.”

Use one of these ownership modes before browser verification:

1. Serialize browser verification to one designated agent at a time. While
   holding that exclusive slot, use only a new run tab and its unique-port
   origin. Never close or reprofile the shared browser/server, clear its
   profile, or mutate any pre-existing tab.
2. If the tooling provides it, launch a genuinely distinct browser server and
   process for the run, with its exact PID and data directory owned by the run.
   A profile name on the existing shared server does not qualify.

If neither mode is available, skip the browser and use a focused static-file
harness, then report that live browser verification was not performed. In
either browser mode, assert `location.port` (or another page-identity check)
inside every `eval`; this converts a wrong-tab measurement into a loud
failure.

**The human's keyboard wins.** A visible shared Chrome takes real window
focus, so a human using the same machine can interleave keystrokes or a
paste with your `type` action — measured once as a vite dev-server URL
landing inside a word mid-type, storing
`askttp://192.168.118.83:5173/_user` where `ask_user` was typed, all the
way into the daemon transcript. When a human may be active, either use a
genuinely distinct browser process, or hold the designated serialized slot
and read the field (or the stored turn) back and compare against what you sent
before trusting any result derived from typed input.

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
navigate $HUB/auth/<TOKEN>?next=/s/local:<SID>
await_element [data-testid="composer-input-card"]
```

The `/auth` route (`web.go:174`) sets the session cookie then redirects
to `next`. Use the literal token from `$HOME/.local/state/evener/auth-token`
(the isolated `$HOME` from the Setup checklist), not the path. If you get
`"invalid token"` rendered, you passed the path.

**A session URL is `/s/<hostID>:<sessionID>`, and a bare session id is
not a URL.** `isRef` (`shell/routing.ts:29-32`) requires a colon with
non-empty text on both sides; `urlToPane` returns `null` for anything
else and `AppShell` renders `NotFound` — the words "Page not found" and
"This link doesn't match anything in evener." (`shell/NotFound.tsx:16-17`).
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
`data-testid` in `cmd/evener-hub/frontend/src` rather than guessing.

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

**Spawn** (`panes/spawn/Spawn.tsx`): `[data-testid="spawn-prompt-card"]`
and its control row `[data-testid="spawn-controls"]`,
`[data-testid="spawn-submit"]`, `[data-testid="spawn-attach"]`,
`[data-testid="spawn-branch"]`. Two model controls, and only one of them
is ever on screen: `[data-testid="spawn-desktop-model"]` wraps the
desktop Model field's `ModelCatalog` trigger, while
`[data-testid="spawn-model-trigger"]` (readout
`[data-testid="spawn-model-value"]`) is the prompt card's own
`ModelSwitchTrigger`, which the phone uses and a desktop width hides
behind `[data-testid="spawn-model-slot"]`. Both carry the `— change
model` screen-reader suffix, so address them by testid or filter on
`offsetParent`. The picker itself is the shared ARIA combobox in
`widgets/modelCatalog/` — `role="option"` rows, not the legacy
`.chip-picker-*` classes.

**Toasts** are the error channel for actions that fail without a
dedicated surface: `section[aria-live="polite"][aria-label="Notifications"]`
(`widgets/toast/index.tsx:36`).

### Seeding preferences before the first load

Preferences are flat `localStorage` keys under `evener.prefs.<name>`,
hydrated **once at module load** (`stores/prefs.ts:110-125`), so a write
after the page is up does not retroactively change behavior — navigate to
any authenticated page first, write the keys, then reload:

```javascript
// values are the strings "1" / "0", never JSON
localStorage.setItem("evener.prefs.notificationsTitle", "1");
localStorage.setItem("evener.prefs.notificationsFavicon", "1");
```

Notification prefs are `notificationsTitle`, `notificationsFavicon`,
`notificationsOs`, `notificationsSound` (`prefs.ts:223-228`) and **all
four default to OFF** (`:266-273`) — a card that expects a tab-title
badge without opting in is asserting the wrong default. There is no
`evener-hub.notifications` JSON blob any more. Rail expansion state is one
JSON blob under `evener.rail.expanded.v1` (`shell/rail/railExpansion.ts:19`).

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
`thread/queueChanged`, `evener/steering/injected`, `item/*` or `turn/*`
(`stores/threads.ts:797-810`), so a stuck chip means the ack never
arrived.

### Probing without a renderer handle

`window.EvenerRenderer` is gone; ask the DOM and the server instead.

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
`thread/read` reports `result.thread.status.type=active`, the AppWire socket
did not hydrate — check `$run/hub.log`, and confirm the page is really
the one you think it is via the `location.port` assertion above.

The authoritative counterpart to any of this is the AppWire `thread/read` response
and the on-disk transcript. When a DOM read is ambiguous, do not add
more selectors — cross-check `thread/read` and
`evener doctor transcript`, which cannot be fooled by a stale tab.

## Driving the TUI with tmux

Each scenario gets its own tmux session. Naming matters — the cleanup
step needs a deterministic name, and it needs to be **your** name: two
agents both launching a literal `-t evener-test` will `kill-session` each
other's TUI (tmux session names are as collision-prone as a fixed port
or a fixed `/tmp` path, for the same reason — nothing makes them unique
by construction). Derive it from `$run`, which `mktemp` already made
unique:

```bash
TMUX_SESSION="evener-test-$(basename "$run")"
tmux kill-session -t "$TMUX_SESSION" 2>/dev/null   # idempotent: prior run
tmux new-session -d -s "$TMUX_SESSION" -x 200 -y 50 \
  "$run/evener" tui --hub-addr 127.0.0.1:$PORT --debug 2>$run/tui-stderr.log"
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
`$HOME/.local/state/evener/projects/<project-id>/sessions/` (the
isolated `$HOME` from the Setup checklist):

```bash
TS=$(find "$HOME/.local/state/evener/projects" -name "$SID.transcript.jsonl")
META=$(find "$HOME/.local/state/evener/projects" -name "$SID.meta.json")

# Locate the canonical files for a session selector.
go run ./cmd/evener doctor locate "$SID"

# Read a compact, typed turn outline instead of raw JSONL.
go run ./cmd/evener doctor transcript "$SID" --format outline --range last:20

# Count structural tool invocations. This does not confuse tool-name
# mentions in prompts/API payloads with real tool calls.
go run ./cmd/evener doctor transcript "$SID" --count delegate_send
```

Useful when a scenario's web/TUI assertion is ambiguous. The on-disk
transcript is the daemon's authoritative record of what happened, but
do not hand-parse transcript JSONL for comprehension; use
`evener doctor transcript`. Raw `jsonl` reads are for byte-level replay
or debugging the transcript format itself.

Five forensics facts that have each closed an investigation:

- **The transcript is lossy for broken tool calls.** A tool call whose
  arguments failed JSON parsing is stored with `arguments: {}` — the raw
  broken bytes are NOT in the transcript. They are in
  `<session>.api.jsonl` (same directory), which records every API
  attempt with the raw SSE stream in `response.body.data`: reassemble
  the call's `input_json_delta` parts to see exactly what the model
  sent.
- **Check `stop_reason` in api.jsonl before believing an error's
  self-description.** A batch of "JSON escaping" tool failures was
  actually `stop_reason: max_tokens` truncation at an output cap; the
  malformed JSON was the symptom of the cut, not the cause.
- **The client-mutation journal proves delivery.** The daemon writes
  `<state-dir>/mutations/<SID>.json` recording every accepted AND
  rejected client mutation. A mutation absent from that journal never
  reached the daemon — which localizes a "queued reply never sent" bug
  to the client (e.g. the web outbox) without reading a line of client
  code.
- **An `ASSISTANT` turn is not proof the user was answered.** When a
  provider grinds through failing attempts and evener stops early, the
  round settles whatever the model had already streamed as a normal
  assistant turn, followed by a `STEERING` turn explaining the failure
  and the `TURN_FAILURE` marker. In `--format outline` that reads as
  `ASSISTANT "…"` / `STEERING "The transport cut off your response
  after ~90s of streaming, twice. …"` / `TURN_FAILURE "provider
  unhealthy after 2 stream failures …"`. The draft was never delivered
  to anyone — it is persisted so the NEXT turn's model can reuse it
  instead of regenerating it. A scenario that counts assistant turns, or
  reads the last one as the reply, has to skip these. The same trio with
  no assistant turn (`STEERING "The provider stopped responding
  mid-stream, 4 times over …"` / `TURN_FAILURE`) is the shape that
  produced nothing worth keeping.
- **A stream that died mid-flight still names its cause in
  api.jsonl.** Meta-providers (openrouter, lunarouter) report upstream
  rejections as an `{"error": ...}` chunk on an HTTP 200 SSE stream,
  which the openai-compat adapter decodes into a typed error, so the
  attempt's `error_message` carries the provider's own code and message
  rather than the generic `stream ended without completion`. Read them
  with `evener doctor apilog "$SID" --errors`, or `--health` for the
  per-session verdict (`errors_by_class`, `retry_storm_groups` — attempt
  groups of three or more). The raw chunk is in `response.body.data` if
  the decoded message is not enough.

## Falsification debugging — when an assertion fails

If a scenario's assertion fails and the failure isn't obvious from
the captured pane / DOM, the next move is to add a stderr probe to
the offending layer, rebuild, rerun, grep the log. The full pipeline
has six rebuild points:

1. **Daemon** — `cmd/evener/` and `agent/`. Rebuild: `go build -o "$run/evener" ./cmd/evener`. The hub re-spawns it per session, so the next spawned session picks up the new binary.
2. **Hub** — `cmd/evener-hub/` and `server/`. Rebuild + kill the running hub by PID (not `pkill -f`, which would also kill any other concurrent agent's hub), then restart it the same way as step 4 of the setup checklist — it binds a *new* ephemeral port, so re-read `$run/hub.log` for the new `PORT`/`HUB`: `kill "$HUBPID"; go build -o "$run/evener" ./cmd/evener && "$run/evener" hub -addr 127.0.0.1:0 -evener "$run/evener" 2>"$run/hub.log" & HUBPID=$!`.
3. **Web UI** — `cmd/evener-hub/frontend/src/` (TypeScript/React). Two steps, and skipping the first is the classic "my change didn't take": `make build-web` compiles it into `cmd/evener-hub/frontend/dist`, which `webnext.go`'s `//go:embed all:frontend/dist` bakes into the hub binary. So rebuild the frontend, **then** rebuild and restart the hub, then hard-refresh the tab. When in doubt whether a live tab is running the fix, grep the served bundle for a symbol the fix introduced (`curl -s "$HUB/assets/…"` or view-source) — a tab picks nothing up until the hub binary was rebuilt, restarted, AND the tab reloaded. A checkout that has never run `make build-web` has a one-line `dist/PLACEHOLDER` and serves no app at all. Agent worktrees symlink `node_modules` to a shared install; `make web-preflight` accepts that install when the `package-lock.json` beside it is byte-identical to this worktree's (mtime cannot answer the question — a fresh worktree's lockfile is always newer than the shared install). When the two lockfiles differ it refuses to `npm ci` through the symlink on purpose, because that would empty the install for every other worktree: refresh the shared install where it lives, or give this worktree its own real `node_modules`.
5. **AppWire types** — `appwire/`. Both daemon and hub statically link these; rebuild both. The generated TypeScript mirror (`frontend/src/protocol/types.gen.ts`) is a third consumer — a wire change that only rebuilds the Go side leaves the browser decoding the old shape.
6. **Optimistic-mutation plumbing** — the TUI's pending coordinator and the web's durable outbox (`frontend/src/stores/mutationOutbox.ts`, `panes/session/composer/queue/pendingTurnsStore.ts`); same rebuild rules as 4 and 3 respectively.

Stderr probes are cheap and unambiguous. Example from the kata `wymv`
debug session — wanted to know whether the TUI's reconcile path was
reaching `applyHubNotification`:

```go
// In cmd/evener-tui/hub_model.go applyHubNotification
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
# Shut down each spawned session over the authenticated AppWire socket:
# {"id":N,"method":"thread/shutdown","params":{"ref":"local:<SID>"}}

# Kill any tmux sessions you opened.
for name in evener-test evener-test-2; do tmux kill-session -t $name 2>/dev/null; done

# Kill the hub you started, by the PID you captured — not by a
# `pkill -f evener-hub-test` pattern match, which would also kill any other
# concurrent agent's test hub (they're all named "evener hub" now that each
# one lives under its own $run dir instead of a fixed /tmp/evener-hub-test).
kill "$HUBPID" 2>/dev/null

# Remove this run's own directory — not a `rm -rf /tmp/evener-e2e-*` glob,
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
the parent's final prose. Use `evener doctor` instead of custom JSONL
parsers:

```bash
# Parent watch lifecycle, coalesced delivery counts, dropped sends, and
# self-loop verdicts.
go run ./cmd/evener doctor watches "$SID"

# Parent/delegate/observer topology. Use observer transcript refs from
# this output when you audit sidecar behavior.
go run ./cmd/evener doctor tree "$SID" --observers

# Parent and observer turns. The observer transcript should show whether
# a frame caused useful work, a no-op text response, or unwanted tool churn.
go run ./cmd/evener doctor transcript "$SID" --format outline --range last:30
go run ./cmd/evener doctor transcript "$OBSERVER_REF" --format outline --range last:30

# Structural tool counts when the scenario cares about fluency.
go run ./cmd/evener doctor transcript "$OBSERVER_REF" --count delegate_send
go run ./cmd/evener doctor transcript "$OBSERVER_REF" --count communicate
go run ./cmd/evener doctor transcript "$OBSERVER_REF" --count job_list
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
  the parent polls with `job_list`, `job_status`, or transcript
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
in `cmd/evener-tui/hub_model.go` early-returns when the composer mode
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
- **Auth token**: `$HOME/.local/state/evener/auth-token` — the isolated `$HOME` from
  the Setup checklist, never Jesse's real one.
- **Follow-up turn** (after the initial spawn prompt): send AppWire `turn/start` with `ref:"local:<SID>"`, a unique `clientMutationId`, and `input:[{"type":"text","text":"..."}]` (the spawn only starts turn 1; subsequent user turns use `turn/start`).
- **Session URL**: `/s/local:<SID>`. A bare `/s/<SID>` renders "Page not found" client-side, by design.
- **Recursion opt-in** (delegate subagents that can themselves delegate): per-spawn `launch_overrides.maxSubagentDepth:N` raises the root's own delegation allowance to N. Omitted/default is 1 (a root may delegate, but its delegates are leaves) — recursion is dark without this.
- **Per-session transcript**: `$HOME/.local/state/evener/projects/<project-id>/sessions/<SID>.transcript.jsonl`
- **Per-session meta**: same dir, `<SID>.meta.json`
- **Per-daemon log** (everything a spawned session's `evener serve` writes,
  including its `[serve]` lines): `$HOME/.local/state/evener/run/logs/daemon-<SID>.log`,
  under the hub's run dir. `$run/hub.log` holds hub lines only; each launch
  prints one `[hub] daemon session=… pid=… log=…` banner there naming this
  file. Daemon lines are stamped `[serve <RFC3339 UTC ms> session=<SID>]`,
  so they cross-reference directly against rendezvous `started_at` and the
  provider API logs.
- **TUI debug stderr** (when launched with `--debug`): redirect via `tmux new-session -d -s "$TMUX_SESSION" "$run/evener" tui --hub-addr 127.0.0.1:$PORT --debug 2>$run/tui-stderr.log"`
- **Browser console capture**: `~/.cache/superpowers/browser/<date>/<session>/<NNN>-<action>-console.txt`
- **Kata CLI**: `~/go/bin/kata` (see `kata create --help`)
- **Rate-limited provider for retry/liveness checks**: `scripts/e2e/e2e-ratelimited-provider.sh` — see "Testing against a rate-limited provider" above.
- **Browser verification ownership**: serialize the shared browser to one designated verifier, or use a genuinely distinct browser server/process owned by the run. Never call `set_profile`; see "Driving the web UI" above and `docs/developing-evener/conventions/agent-fleets.md`.
