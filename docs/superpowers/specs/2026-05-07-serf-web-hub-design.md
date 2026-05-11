# Serf Web Hub — Design Spec

Date: 2026-05-07
Status: Draft (rev 2, post adversarial review)

## Summary

A multi-session web interface for serf that lets a user browse, drive, search, and resume serf sessions from a browser. v1 manages only local-host daemons.

The hub is a small htmx-based shell that:

- Lists live `serf serve` daemons via filesystem rendezvous.
- Spawns new daemons.
- Browses and searches past saved sessions across the user's serf state.
- **Reverse-proxies** REST and SSE between the browser and each daemon. The browser only ever speaks to the hub origin.

The hub does not transform the daemon's SSE events. It proxies the raw event stream and a small client-side `renderer.js` parses it. Server-side HTML-fragment rendering is explicitly avoided: htmx-sse's per-event-swap semantics fight delta streaming, and a JS renderer can mirror `serf-tui`'s coalescing logic directly.

## Goals

- Drive live serf sessions interactively from a browser: input, steer, interrupt, compact, clear, switch model, watch streaming events.
- Spawn new serf sessions from the browser with a chosen working dir, model, agent persona.
- Browse and search past saved sessions across the user's serf state.
- Read past session transcripts and resume into a new live session.
- Support many concurrent daemons on one host without manual port management.
- Surface and shut down orphaned daemons.

## Non-goals (v1)

- Remote daemons (other hosts, Tailscale-aggregated peers). Loopback only.
- Multi-user sharing or link-out URLs.
- A new JSON-RPC or wire-format overhaul. Today's REST+SSE is the contract.
- Mobile layout, themes, keyboard-shortcut palette.
- Mid-session migration of a daemon between hub and standalone modes.

## Daemon prerequisites

These are real changes to `serf serve` and `agent/`. Each is a `serf` issue in its own right, surfaced by the adversarial review. They ship before or alongside the hub. The hub depends on them; calling out separately so they can be tracked.

### D-1. Persist the original task on `SessionMeta`

`agent.SessionMeta` (`agent/snapshot.go`) currently has `{ID, ProfileID, Model, Config, EnvInfo, CreatedAt, UpdatedAt, TurnCount, LastInputTokens}`. Past-session search needs the first user input to be searchable without cracking the transcript JSONL on every rebuild.

Add `OriginalTask string` to `SessionMeta`. Populate it on the first `ProcessInput` when empty (the existing `extractOriginalTask` logic in `agent/session.go` knows how). Persist on the same save path as the rest of the meta.

### D-2. Fix `/clear` vs in-flight input race

`cmd/serf/serve.go:225-261`. `SetClearFunc` swaps the session pointer under `currentMu` while the input goroutine may be mid-`ProcessInput` against the old session. Behavior under contention is undefined.

Fix: `/clear` returns 409 Conflict while `processing == true` (mirrors `/input`'s 409 in `server/server.go:434`). Document that the UI must interrupt before clearing if a turn is running.

### D-3. `POST /shutdown` daemon endpoint

Today the only shutdown path is SIGINT. The hub needs a button-driven way to terminate orphaned daemons.

Add `POST /shutdown` to `server.Server`. Triggers the same path as SIGINT (cancel context, close http server, close session, remove rendezvous file). Returns 202 immediately.

### D-4. Rendezvous-file lifecycle

`serf serve` writes its rendezvous file when the listener binds and removes it on graceful shutdown only. Non-graceful exits (panic, OOM, kernel kill, SIGKILL, SIGTERM) leak files. Pid-liveness alone is not enough because pids get reused.

Daemon-side:

- Rendezvous file does NOT carry `session_id` (which can change on `/clear`). It carries process and address only — see "Filesystem layout" below.
- Removed via `defer` in `cmd/serf/serve.go` plus a SIGTERM handler.

Hub-side liveness check:

- TCP probe to the rendezvous'd `address`.
- `GET /status` to confirm the daemon answers and to discover the current `session_id` (which is the canonical key the hub routes by — see "Routing" below).

### D-5. Add `working_dir` to `/status`

`agent.Session` already tracks `envInfo.WorkingDir`. Thread it through `server.StatusInfo`. Roster row labeling depends on this.

## Architecture

```
                 ┌───────────────────────────┐
                 │ browser                   │
                 │  • htmx shell from hub    │
                 │  • renderer.js (client)   │
                 └───────────┬───────────────┘
                             │
                  ALL traffic via hub
                             ▼
        ┌────────────────────────────────────┐
        │ serf-hub          (127.0.0.1:9180) │
        │   • roster (lazy scan + fsnotify)  │
        │   • REST proxy   (httputil)        │
        │   • SSE passthrough (flush:-1)     │
        │   • spawner                        │
        │   • past-session index             │
        │   • static asset embed             │
        │   • same-origin guard              │
        └───────────┬────────────────────────┘
                    │
       proxied REST+SSE       reads/spawns
                    ▼                ▼
   ┌─────────────────────┐  ┌────────────────────────────────┐
   │ serf serve daemon 1 │  │ ~/.serf/run/<pid>.json         │
   │ serf serve daemon 2 │  │ $XDG_STATE_HOME/serf/projects/ │
   │ ... (loopback only) │  │   */sessions/*.meta.json       │
   └─────────────────────┘  └────────────────────────────────┘
```

Daemons bind loopback only and have no browser-facing surface. The browser only knows the hub origin.

## Routing

The browser's drive URL is keyed by **session_id**, not pid. PIDs reuse on long-running boxes. Hub's roster maintains `session_id → {pid, address}`. The daemon's session_id can change on `/clear`; the hub re-keys the roster entry when `GET /status` reports a new id.

`session_id` is canonical wherever the user might refer to a session: live URLs, past URLs, and any links the hub renders.

## Components

### Browser

- htmx for navigation and form submission of hub-served pages.
- A small client-side `renderer.js` (vendored, no build pipeline) that:
  - Opens `EventSource('/live/<session_id>/events')` against the hub.
  - Maintains the same coalescing state machines `serf-tui` already implements (`cmd/serf-tui/model.go:735-820`): per-message text delta append, per-call_id tool stream tracking, subagent grouping, communicate-tool elision, context-pressure derivation from `ASSISTANT_TEXT_END.usage`.
  - Renders into the transcript pane via direct DOM ops.
  - Sends user input via `fetch('/live/<session_id>/input', ...)` against the hub.
  - Renders markdown using a small client-side library (`marked.js` or `markdown-it`, vendored).
- Status bar polled every 2s via htmx against `/live/<session_id>/status` on the hub.

The browser never learns the daemon address. All browser ↔ daemon traffic flows through the hub.

### Hub (`serf-hub`, new sibling binary at `cmd/serf-hub/`)

Following the existing pattern in the repo (`serf-tui`, `serfeval`, `llmcall` are all sibling binaries; the `serf` binary stays focused on agent operations). The hub spawns `serf serve` subprocesses; it does not import the agent loop itself.

Default bind: `127.0.0.1:9180` (chosen to avoid the daemon's `:9131` neighborhood).

Five subsystems:

| Package | File | Job |
|---|---|---|
| `hub/roster` | `roster.go` | Live-daemon roster: lazy scan of `~/.serf/run/` on each `/` or `/live` request, plus fsnotify for cheap incremental updates. TCP-probe + `/status` liveness verification. Maps `session_id → {pid, addr, working_dir, model, ...}`. |
| `hub/proxy` | `proxy.go` | REST reverse-proxy via `httputil.ReverseProxy` (one per request). SSE passthrough that opens an upstream `GET /events` per browser viewer, forwards `Last-Event-ID` request header, sets `FlushInterval = -1`, and streams bytes through unmodified. One upstream connection per browser viewer (the daemon's `Broadcaster` already supports many subscribers — no need for hub-side fanout). |
| `hub/past` | `past.go` | Loads `agent.SessionMeta` by globbing `$XDG_STATE_HOME/serf/projects/*/sessions/*.meta.json`. In-memory index, rebuilt every 60s. Substring search over `OriginalTask`, `ID`, `EnvInfo.WorkingDir`. Recency-paginated. |
| `hub/spawn` | `spawn.go` | Forks `serf serve` subprocesses from named templates. Sets `SERF_HUB_SPAWNED=1`. Always passes `--addr 127.0.0.1:0` (does NOT change daemon default). |
| `hub/security` | `security.go` | Same-origin / Host validation middleware (hub edge only). |
| `hub/web` | `web.go` | HTTP mux, static-asset embed (htmx, renderer.js, markdown lib, base CSS), HTML templates, route handlers. |

### Daemon (`serf serve`)

Beyond the prerequisites in D-1..D-5: only the rendezvous-file write and the spawn-by-hub path. Today's REST+SSE surface is consumed unchanged.

## Filesystem layout

`~/.serf/` (user home) is a new shared directory used by the hub and by every daemon on the host. Distinct from per-project `.serf/` directories.

| Path | Owner | Purpose |
|---|---|---|
| `~/.serf/run/<pid>.json` | daemon (write+remove), hub (read) | Live-daemon rendezvous |
| `~/.serf/hub.lock` | hub | flock to prevent two hubs |
| `~/.serf/hub.toml` | user | Hub config |
| `$XDG_STATE_HOME/serf/projects/<sha256>/sessions/*.meta.json` | daemon | Per-project saved sessions (existing) |
| `$XDG_STATE_HOME/serf/projects/<sha256>/sessions/*.transcript.jsonl` | daemon | Per-project saved transcripts (existing) |

### Rendezvous file shape

```json
{
  "pid": 48211,
  "address": "127.0.0.1:54321",
  "working_dir": "/Users/jesse/git/foo",
  "state_dir": "/Users/jesse/.local/state/serf/projects/abc12345",
  "agent": "default",
  "model": "gpt-5.2",
  "provider": "openai",
  "started_at": "2026-05-07T14:32:11Z",
  "spawned_by": "user"
}
```

`session_id` deliberately omitted — fetched from `/status` on every roster refresh, since it can change under `/clear`.

`spawned_by` is `"user"` for manual launches, `"hub"` when env `SERF_HUB_SPAWNED=1` is present.

## Security

Loopback is not a trust boundary. Any browser tab on the local machine — including from a hostile site — can issue cross-origin `fetch` requests against `127.0.0.1`. DNS rebinding is real. v1 must defend.

### Hub edge

Same-origin guard applied to all hub mutating routes (`POST`, `DELETE`):

- Reject if `Origin` header is present and not `http://127.0.0.1:9180`.
- Reject if `Host` header is not `127.0.0.1:9180` or `localhost:9180`.

GET routes that return HTML or JSON: the same `Host` check applies, to defend DNS rebinding.

### Daemon edge

Daemons remain loopback-only and have no browser-facing surface. The hub is the sole client. No CORS work, no Origin validation needed on daemons in v1.

A spawned-by-hub token (random per-hub session, embedded in the rendezvous file as `hub_token`, shared via `SERF_HUB_TOKEN` env to the spawned daemon, enforced by the daemon on every request) is listed in v2 to harden against the case where a different local user runs a daemon the hub then surfaces. v1 trusts every loopback daemon.

### Hub flock

`flock` on `~/.serf/hub.lock` at startup. Refuse to start if held. Eliminates two-hubs races on the rendezvous dir and the bind port.

## Data flow

### Live drive (browser ↔ hub ↔ daemon)

1. User clicks a roster row → browser navigates to `GET /live/<session_id>` on the hub.
2. Hub looks up `session_id → {addr}` from the roster. Renders the drive page (HTML shell only). If lookup fails, redirect to `/`.
3. Page bootstraps `renderer.js`. It opens `EventSource('/live/<session_id>/events')` and `setInterval` polls `/live/<session_id>/status` every 2 s — both against the hub.
4. Hub's SSE handler resolves `session_id → daemon addr`, opens `GET <addr>/events` upstream, forwards request `Last-Event-ID` header, streams bytes through to the browser unchanged with `FlushInterval = -1`.
5. Hub's REST handler resolves and reverse-proxies `POST /live/<session_id>/{input,steer,interrupt,compact,clear,model,shutdown}` and `GET /live/<session_id>/{status,models,tasks}` to the daemon's matching endpoint via `httputil.ReverseProxy`.
6. The browser parses SSE events keyed by name (`SESSION_START`, `ASSISTANT_TEXT_DELTA`, `TOOL_CALL_START`, etc.). `renderer.js` maintains state per current message, per `call_id`, per subagent, and updates the DOM.

When the daemon emits a `SESSION_END` followed by a `SESSION_START` with a new `session_id` (the `/clear` case), the renderer:

- Resets in-progress per-message and per-call_id state.
- Clears the transcript pane.
- Updates the page URL via `history.replaceState` to `/live/<new_session_id>`. The hub roster has already re-keyed because its `/status` poll observed the change; the next polled status request resolves under the new id.
- Continues on the same SSE connection (the proxy holds it open for the daemon's lifetime).

### Past-session browse (browser ↔ hub)

1. Hub starts → indexer globs `$XDG_STATE_HOME/serf/projects/*/sessions/*.meta.json`, parses metas (cheap; meta is small).
2. Indexer rebuilds every 60 s in a goroutine; fsnotify can be added later if the rebuild becomes hot.
3. User searches → `GET /past?q=...` → in-memory substring match across `OriginalTask`, `ID`, `EnvInfo.WorkingDir`. Results sorted by `UpdatedAt` desc, paginated 50/page.
4. Click result → `GET /past/<session_id>`. Hub reads the saved transcript JSONL from disk and renders the transcript read-only via the same client-side `renderer.js` — server-streams the events as one synthetic SSE replay over a `text/event-stream` response keyed off the JSONL contents (no live daemon involved).
5. Click resume → `POST /past/<session_id>/resume` → spawner forks `serf serve --resume <session_id>` with `--addr 127.0.0.1:0`. Hub waits up to 30 s for rendezvous (configurable; covers cold-start and large-session restore). On success, redirects to `/live/<new_session_id>` (resume creates a new session_id; this is the canonical behavior).
6. Concurrent resume requests for the same `session_id` are serialized at the hub via a per-session lock. Second request gets a 202 + redirect to the in-flight resume.

### Spawn new (browser ↔ hub ↔ subprocess)

1. `GET /live/new` → form: spawn-template picker (from `hub.toml`), working-dir override.
2. `POST /live/new` → spawner picks the template, applies the override, forks `serf serve --addr 127.0.0.1:0 --provider … --model … --dir … --agent … --state-dir …` with `SERF_HUB_SPAWNED=1`.
3. Hub waits up to 30 s for the rendezvous file. On success, redirects to `/live/<session_id>`.

The form does **not** expose serf's full ~25-flag CLI surface. Templates are named presets in `hub.toml`:

```toml
[[spawn_template]]
name = "code, gpt-5.2"
provider = "openai"
model = "gpt-5.2"
agent = "default"
reasoning_effort = "medium"

[[spawn_template]]
name = "review, claude-opus"
provider = "anthropic"
model = "claude-opus-4-7"
agent = "reviewer"
```

## HTTP surface

### Hub (browser-facing — the only surface the browser ever sees)

| Route | Method | Purpose |
|---|---|---|
| `GET /` | | Landing: live roster + recent past, all server-rendered |
| `GET /live` | | Live roster fragment (htmx-pollable) |
| `GET /live/new` | | New-session form |
| `POST /live/new` | | Spawn daemon, redirect |
| `GET /live/<session_id>` | | Drive page (HTML shell; data flows via the proxied routes below) |
| `GET /live/<session_id>/events` | SSE | **Proxied** to daemon `/events` |
| `GET /live/<session_id>/status` | | **Proxied** to daemon `/status` |
| `GET /live/<session_id>/{models,tasks}` | | **Proxied** |
| `POST /live/<session_id>/{input,steer,interrupt,compact,clear,model,shutdown}` | | **Proxied** |
| `GET /past` | | Past-session search |
| `GET /past/<session_id>` | | Read-only transcript viewer |
| `POST /past/<session_id>/resume` | | Spawn resuming daemon, redirect |
| `GET /past/<session_id>/replay` | SSE | Synthetic replay of a saved transcript |
| `GET /assets/*` | | embed.FS-served htmx, renderer.js, markdown lib, css |

### Daemon (loopback only, never browser-facing)

The hub depends on today's daemon surface plus the prereqs in D-1..D-5. The browser never speaks to these directly.

| Route | Method | Notes |
|---|---|---|
| `GET /events` | SSE | Live event stream |
| `GET /status` | | Now includes `working_dir` (D-5) |
| `POST /input` | | |
| `POST /steer` | | |
| `POST /interrupt` | | |
| `POST /compact` | | |
| `POST /clear` | | Now 409 while processing (D-2) |
| `POST /model` | | |
| `GET /models` | | |
| `GET /tasks` | | (Surfaced as a side panel — see UI) |
| `POST /shutdown` | | New (D-3) |

## Page layouts

### Landing (`/`)

Two-column. Left: live roster (one row per active session: working dir, model, turns, ctx %, started). Right: past-session search box plus the most recent N saved sessions.

### Drive (`/live/<session_id>`)

```
┌──────────────────────────────────────────────────────┐
│ status: model · profile · turns · ctx % · ●          │  ← polled 2s via hub
├──────────────────────────────────────────────────────┤
│ transcript                              [tasks ▶]    │
│   • user → "fix the bug"                             │
│   • assistant → markdown                             │
│   • tool_call header (collapsed)                     │
│   • subagent block (nested)                          │
│   • thinking (expandable)                            │
├──────────────────────────────────────────────────────┤
│ [textarea]                                  [send]   │
│ [steer] [interrupt] [compact] [clear] [model ▾] [⏻]  │
└──────────────────────────────────────────────────────┘
```

`[⏻]` is the shutdown button (D-3). Confirmation modal first. `[tasks ▶]` is a slide-out panel that polls `/tasks`.

### Past (`/past/<session_id>`)

Same transcript layout, no input footer, with a `[resume]` button in the header.

## Configuration

`~/.serf/hub.toml`:

```toml
addr = "127.0.0.1:9180"
state_glob = "${XDG_STATE_HOME}/serf/projects/*/sessions"   # default
run_dir = "~/.serf/run"                                     # default
status_poll_interval = "2s"
past_index_rebuild_interval = "60s"
spawn_timeout = "30s"
past_results_per_page = 50

[[spawn_template]]
name = "..."
# ... see Spawn flow
```

## Failure handling

- **Daemon crashes ungracefully**: rendezvous file lingers → hub TCP probe fails → roster prunes the entry on the next refresh → drive page's SSE drops → renderer shows "session ended."
- **Pid reuse**: `kill -0 <pid>` succeeds for an unrelated process. TCP probe fails (or `/status.session_id` mismatches the roster's expected) → entry pruned.
- **Daemon unresponsive**: two consecutive `/status` failures → marked `unreachable` in the UI; user can shutdown via button (which probably also fails) or `kill -9`. Hub does not auto-kill.
- **`/clear` mid-stream**: see "Live drive" data flow. Renderer resets state in place, no redirect.
- **Browser ↔ hub or hub ↔ daemon network blip**: `EventSource` auto-reconnects with the last received event id. The hub's SSE passthrough forwards `Last-Event-ID` to the daemon, so the daemon's broadcaster ring (`server/broadcaster.go`, default 1000 events) replays missed events. For long sessions that overflow the ring, late or reconnecting viewers see partial history; we accept this in v1 and document. Bumping the ring is a v2 knob.
- **Hub ↔ daemon connection drops mid-stream**: hub closes the browser SSE; browser reconnects; hub re-opens upstream. `Last-Event-ID` carries through. No state lost provided the ring hasn't rolled.
- **Spawn timeout**: 30 s default; configurable. On timeout, hub kills the subprocess, returns error to the form.
- **Concurrent resume**: per-session lock at the hub serializes; second request gets the in-flight result.
- **Two hubs**: flock blocks the second.

## Testing

- **Unit**: rendezvous round-trip; pid-reuse-with-tcp-probe pruning; past-index search ranking; spawn argument construction; security middleware (rejects bad Origin/Host); per-event-name parser stubs against `agent/events.go` payload structs.
- **Integration**: real `serf serve` with a stub LLM provider; verify rendezvous appears; hub roster reflects it; spawn-and-redirect flow; resume flow; clear-mid-session does not break the renderer's state (assert via headless browser).
- **Browser**: end-to-end with Playwright or chromedp — landing → drive → input → events → clear → events. One smoke test per UI flow.
- **Daemon prereqs** (D-1..D-5) ship with their own unit + integration tests in `agent/` and `server/`.

## Out of scope (v2+)

These are deferred but the v1 design does not preclude them.

- **Remote daemons**: roster grows a Tailscale-peer enumerator alongside fsnotify; rendezvous lives in a shared-known location or daemons self-register. The proxy already abstracts daemon address — extending to non-loopback addresses is local to the roster and proxy.
- **Auth & per-user trust tokens**: bearer at the hub edge; `hub_token` shared via env to spawned daemons; daemons enforce. Same-origin guard from v1 stays.
- **sqlite FTS5 past-index**: graduate from in-memory substring search when N gets uncomfortable. Define the threshold when we hit it.
- **Codex app-server protocol adoption**: still possible later as a separate spec. Daemons could grow a JSON-RPC channel alongside REST+SSE without disturbing the hub.
- **Bumped broadcaster ring** for late-joiner replay on long sessions.
- **Mid-session migration** of a daemon between standalone and hub modes.
