# Serf Hub — pre-compact handoff

Date: 2026-05-07
Author: Bot, mid-session before context compaction

This note captures the state of the serf-hub feature work so a fresh session (post-compact) can pick up cleanly. If anything here disagrees with the code, trust the code.

## What was built

Two stacked branches off `main` (tip `d3114c9` at session start; advanced to `d3114c9` per `git log`).

- **`serf-daemon-prereqs`** (10 commits) — Phase A: daemon-side groundwork.
  - D-1: `OriginalTask` field on `agent.SessionMeta`, populated from first `ProcessInput`.
  - D-2: `POST /clear` returns 409 while session is processing input.
  - D-3: `POST /shutdown` endpoint on `server.Server` wired to the daemon's signal-cancel.
  - D-4: New top-level `rendezvous` package; daemon writes `~/.serf/run/<pid>.json` on bind, removes on graceful shutdown. SIGTERM now triggers the cleanup path.
  - D-5: `working_dir` exposed on `/status`.
  - All D items have unit tests; an integration test under `cmd/serf/serve_test.go` (skip-by-default, requires `SERF_TEST_PROVIDER`/`SERF_TEST_MODEL` + API key) walks the rendezvous lifecycle end-to-end.

- **`serf-hub`** (30 commits, branched off Phase A) — Phase B: the `cmd/serf-hub` sibling binary.
  - 22 implementation commits per the plan in `docs/superpowers/plans/2026-05-07-serf-hub.md`, plus 1 review-fix commit (C1/C2/C3) and 7 follow-up review-fix commits (C4, I1, I2, I3, I5, two minor refactors).
  - Subsystems: `config` (TOML loader), `flock` (~/.serf/hub.lock), `security` (Origin/Host guard), `roster` (fsnotify + StatusProber + two-strikes pruning), `proxy` (per-daemon-cached `httputil.ReverseProxy` + SSE passthrough with `Last-Event-ID` forwarding), `past` (`agent.SessionMeta` glob index + substring search + replay), `spawn` (template loader + subprocess fork + `WaitForRendezvous` + Resume helper), `web` (per-page template sets, embed.FS for htmx/marked/css), `assets/renderer.js` (client-side SSE coalescing).
  - Vendored: `htmx.min.js@2.0.4` and `marked.min.js@12.0.0` (via `embed.FS`).
  - New deps: `github.com/BurntSushi/toml`, `github.com/fsnotify/fsnotify`. No other surface changes to existing packages.

## Worktrees

```
.worktrees/serf-daemon-prereqs   ← branch: serf-daemon-prereqs
.worktrees/serf-hub              ← branch: serf-hub  (depends on serf-daemon-prereqs as ancestor)
```

Spec and plans copied into both worktrees as untracked `docs/superpowers/{specs,plans}` files (not committed). The canonical originals live in the main worktree at `/Users/jesse/Documents/GitHub/prime-radiant-inc/serf/docs/superpowers/{specs,plans}`.

Build artifacts (`./serf`, `./serf-hub`) live in the `serf-hub` worktree from the demo build at 18:46 PT.

## Verification

- `go test ./... -short` clean (modulo a known load-sensitive flake in `agent/TestExecCommand_AddsVenvBinToPATH_WhenPresent`; passes in isolation).
- `go vet ./...` clean.
- `make build`, `make build-hub` produce working binaries.
- End-to-end browser demo executed via `superpowers-chrome:browsing`. GIF saved to `docs/serf-hub-demo.gif` (4 frames). Demo flow:
  1. Hub landing page lists a real running daemon
  2. Drive page resolves by `session_id`, status bar polls the daemon's `/status` through the hub
  3. User input → real OpenAI call (`gpt-5-mini-2025-08-07`) → SSE events → `renderer.js` coalesces → DOM updates
  4. `POST /shutdown` → daemon exits gracefully → rendezvous file removed
- No mocks of code-under-test anywhere. All tests use `httptest`, real subprocesses (in `e2e_test.go`, skip-by-default), or real disk I/O via `t.TempDir`.

## Architecture as built (one-screen summary)

```
browser  ──HTTPS/SSE──▶  serf-hub :9180  ──HTTPS/SSE──▶  serf serve daemon (loopback ephemeral port)
                              │                              │ writes
                              ▼                              ▼
                       ~/.serf/run/<pid>.json (rendezvous protocol — Phase A)
                       ~/.serf/hub.toml (config)
                       ~/.serf/hub.lock (flock)
                       $XDG_STATE_HOME/serf/projects/<sha>/sessions/* (existing per-project state)
```

Daemons are loopback-only. Browser sees only the hub origin. CSRF is defended via Origin + Host checks; loopback is *not* the trust boundary.

## Outstanding (deferred) items

These are documented in the spec/plan but not implemented in this branch. Consider for a follow-up.

| Item | Where | Notes |
|---|---|---|
| Same-origin guard on **daemons** as well | spec, "Daemon edge" subsection in the v1 design | The design pre-specified hub-only guard; the spec's daemon-side guard wording was for the v2 token-auth path. v1 hub→daemon is loopback-only, daemon never browser-facing. Fine as-is. |
| `hub_token` per-spawned-daemon | spec v2 section | Defended in v2 against malicious local users running daemons. v1 trusts every loopback daemon. |
| sqlite FTS5 past-index | spec v2 | In-memory substring search ships now; cut over when N hurts. |
| Codex app-server protocol adoption | spec | Explicitly deferred. Hub design does not preclude. |
| Bumped broadcaster ring for long sessions | spec | Default 1000 events; long sessions overflow; late joiners may see partial history. |
| Mid-session migration of a daemon between hub-spawned and standalone modes | spec | Out of scope. |

The reviewer's adversarial pass also noted these informational items (left as-is, no fix planned for v1):

- `WaitForRendezvous` could match a stale rendezvous file from a prior daemon if the kernel reused the PID. Mitigation in production: rare, and the hub flock + start-time hub clear-old-rendezvous-on-startup would handle it. v2 can add a `started_after` timestamp filter if it bites.
- `httputil.NewSingleHostReverseProxy` works at the request level and ignores HTTP/2; fine for loopback.
- fsnotify error channel is drained but not logged. A persistent watcher failure (e.g., directory deleted) is silently absorbed. Acceptable; if it bites, add `log.Printf` in roster.go.
- No CSP header on hub responses. Same-origin enforcement covers the hostile-tab attack surface; CSP would be belt-and-suspenders.
- Renderer.js does `marked.parse(textBuf)` on every delta, O(n²) for long messages. Acceptable for v1. If we render long doc-style outputs, switch to incremental.

## Demo replay (how to bring it back up from a fresh session)

```bash
# From the serf-hub worktree:
cd /Users/jesse/Documents/GitHub/prime-radiant-inc/serf/.worktrees/serf-hub

# (Re)build:
go build -o serf ./cmd/serf
go build -o serf-hub ./cmd/serf-hub

# Optional: tidy hub config (or use the pre-existing ~/.serf/hub.toml from the demo)
cat > ~/.serf/hub.toml <<'EOF'
addr = "127.0.0.1:9180"
spawn_timeout = "30s"
past_results_per_page = 50

[[spawn_template]]
name = "openai gpt-5-mini"
provider = "openai"
model = "gpt-5-mini-2025-08-07"
agent = "default"
EOF

# Load API keys from .env (file lives in the main repo root):
set -a; source /Users/jesse/Documents/GitHub/prime-radiant-inc/serf/.env; set +a

# Start a daemon in some working dir:
DEMO_DIR=$(mktemp -d) && (cd "$DEMO_DIR" && git init -q && echo "# demo" > README.md && git add README.md && git -c user.email=demo@example.com -c user.name=demo commit -q -m init)
./serf serve --provider openai --model gpt-5-mini-2025-08-07 --addr 127.0.0.1:0 --dir "$DEMO_DIR" &

# Start the hub:
./serf-hub --serf $PWD/serf &

# Open: http://127.0.0.1:9180
```

Cleanup: `pkill -f "serf-hub"; pkill -f "/serf serve"`. Daemons remove their rendezvous file on graceful shutdown. SIGKILL leaves stale files; the hub's two-strike pruning will pick them up on next refresh after a probe failure.

## Where things live

| Artifact | Path |
|---|---|
| Spec | `docs/superpowers/specs/2026-05-07-serf-web-hub-design.md` |
| Phase A plan | `docs/superpowers/plans/2026-05-07-serf-daemon-prereqs.md` |
| Phase B plan | `docs/superpowers/plans/2026-05-07-serf-hub.md` |
| Demo GIF | `.worktrees/serf-hub/docs/serf-hub-demo.gif` |
| This handoff note | `docs/superpowers/notes/2026-05-07-serf-hub-handoff.md` |
| Phase A worktree | `.worktrees/serf-daemon-prereqs` |
| Phase B worktree | `.worktrees/serf-hub` |
| Demo screenshots | `/Users/jesse/Library/Caches/superpowers/browser/2026-05-07/session-1778185453065/` |
| Frames staged for the GIF | `/tmp/serf-hub-demo-frames/` |

## What's next: UX pass (immediate)

User asked for a UX pass after this note. From the demo screenshots, things to look at:

1. **Live roster row layout** — long working-dir paths overflow; the model/pid/spawned-by metadata wraps awkwardly into a vertical column. Need a flex layout that truncates the wd path with ellipsis and keeps the meta on one line.
2. **No empty state** for the past-search results when no results returned (just shows "no matches", which is fine, but the form should feel more responsive — htmx-driven instead of full page reload).
3. **Drive page transcript styling** — the COMMUNICATE tool block renders raw JSON args + tool_state in a scary-looking pre-formatted block. The TUI hides this; the hub renders it verbatim. Either elide the args header or render as a one-liner.
4. **Status bar placement and color** — IDLE/PROCESSING pill is functional but the visual weight is wrong; no animation when state changes.
5. **No focus-on-textarea on drive page load** — typing `tab` into the page should land in the message input.
6. **Buttons cut off** below the textarea on standard viewport heights — need padding at the bottom of `<main>`.
7. **No keyboard shortcut visibility** — Cmd-Enter sends, but nothing tells the user.
8. **Communicate tool elision** — TUI elides the `communicate` tool because it's noise to the user; hub renders it. Either elide entirely or present as a chat message ("agent says…").
9. **Past viewer header** lacks visual hierarchy; the resume button is right next to the metadata with no separation.
10. **No favicon** — small thing but the tab title looks unbranded.

The screenshots in `/Users/jesse/Library/Caches/superpowers/browser/2026-05-07/session-1778185453065/` are the source of truth for the visual baseline; the GIF captures the full flow.

## Branch protection / merge plan

Not yet attempted. Suggested order when ready:
1. Land Phase A (`serf-daemon-prereqs`) into main as a series of focused PRs (or one squashed) — the daemon prereqs are independently valuable.
2. Rebase Phase B (`serf-hub`) onto the post-merge main.
3. Land Phase B as one big PR (it's all hub-package code, no cross-package ripples).
4. Update the README to mention `serf-hub`.

## Things I'm uncertain about

- **The `--clear` mid-session URL rebind** in `renderer.js` calls `history.replaceState`. I haven't tested it through the browser; only via the unit-tested handler. If it breaks, the renderer's session_id field gets stale.
- **fsnotify on macOS** is FSEvents-backed and aggregates events with up to 100ms latency. The roster's 5s belt-and-suspenders tick papers over this, so practically fine, but new daemons may take up to ~5s to appear in the UI in pathological cases.
- **Resume creates a new session_id** intentionally — the user goes from `/past/<old_id>` to `/live/<new_id>`. We never told them this in the UI; it might surprise them. The UX pass should add a "(forked from <old_id>)" annotation.
- **Spawn template form** has no validation on working_dir — if the user types `/path/to/repo` literal placeholder text, the daemon will fail to start in that nonexistent dir and rendezvous will time out (30s). Worth a client-side "must be absolute path that exists" check or at least a clearer error after timeout.
