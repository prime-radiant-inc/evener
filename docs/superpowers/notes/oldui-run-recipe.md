# Running Build B (old server-rendered htmx UI) for the webui usability study

This is the recipe for participants 2-5. Build A (current SPA) auth "just works" the
same way described here — this doc exists because Build B's auth is NOT `/?token=...`
and is NOT a Bearer header on page navigation, and that trips people up.

## 1. Build (or reuse) the binary

Build B source = main checkout (`/Users/jesse/prime-radiant/toil-suite/serf`, branch `main`).
A binary is already at `/tmp/serf-hub-oldui`. To rebuild:

```
cd /Users/jesse/prime-radiant/toil-suite/serf
go build -o /tmp/serf-hub-oldui ./cmd/serf-hub
```

Build A source = `.claude/worktrees/webui-workspace-shell`. Build with:

```
cd /Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/webui-workspace-shell
go build -o /tmp/serf-hub-newui ./cmd/serf-hub
```

## 2. Isolated HOME + credentials

Never touch port 9180, `~/.serf/`, or `~/.local/state/serf/` — that's the user's real
daemon. Use a scratch HOME per build:

```
export HOME=$(mktemp -d)
mkdir -p "$HOME/myrepo" && cd "$HOME/myrepo" && git init -q && git commit --allow-empty -q -m init
```

The hub needs real provider credentials to spawn real sessions. A fresh HOME's
materialized `~/.serf/providers.toml` only has `[instances.ollama]` (and ollama is
usually not running locally, so every model in that list errors with connection
refused). Fix: source the repo's `.env` for API keys into the environment BEFORE
starting the hub, AND add a real provider instance to providers.toml — the materialized
file doesn't auto-add one just because the env var is present:

```
cd /Users/jesse/prime-radiant/toil-suite/serf
set -a; . "$PWD/.env"; set +a   # exports ANTHROPIC_API_KEY etc. (zsh: the `set -a` matters)

cat > "$HOME/.serf/providers.toml" <<'EOF'
schema = 1
default = "anthropic"

[instances.anthropic]
type = "anthropic"

[instances.ollama]
type = "ollama"
EOF
```

(`.env` must already exist at repo root with `ANTHROPIC_API_KEY=...` etc. — it does in
this repo.)

## 3. Start the hub

```
/tmp/serf-hub-oldui -addr 127.0.0.1:0 > "$HOME/hub.log" 2>&1 &
sleep 2
cat "$HOME/hub.log"
```

**Known bug (filed as kata w20h):** Build B's log line and printed auth URL both echo
the LITERAL `-addr` you passed, not the actual bound port:

```
[hub] serf-hub 0.1.0 listening on 127.0.0.1:0 (run_dir=...)
[hub] auth URL (visit once per browser): http://127.0.0.1:0/auth?token=<TOKEN>
```

`127.0.0.1:0` is not a real port. Get the real one with:

```
lsof -iTCP -sTCP:LISTEN -P -n | grep serf-hub
```

Build A's hub does NOT have this bug — its own log line shows the real port correctly.

## 4. Auth — the actual working recipe

**This is the part Jesse got `unauthorized` on.** Do NOT use `/?token=...` (wrong path)
and do NOT use a Bearer header for browser navigation (that's for scripted/API clients
only, per the hub's own log line: "use as Authorization: Bearer ... for scripted
clients"). The real mechanism, identical in both builds (shared `hubedge` package,
`cmd/serf-hub/internal/hubedge/auth_token.go`):

Navigate the browser to:

```
http://127.0.0.1:<REAL_PORT>/auth?token=<TOKEN>&next=/
```

This sets an httpOnly, SameSite=Lax cookie (named per-hub, so multiple hubs in the same
browser don't collide) and redirects to `next` (default `/`). After that one visit, the
browser's cookie jar is authorized for that hub's origin — no token needed on
subsequent navigations from the same browser profile.

Both `<TOKEN>` and the auth URL are printed in the hub's own startup log
(`[hub] auth URL (visit once per browser): ...`); just remember to substitute the REAL
port per the bug above.

## 5. Verify you're on the right one

Both builds run on `127.0.0.1:<random-port>` and look similar at a glance. Always
`location.port` (or check the URL bar) before trusting any observation, especially if
sharing a browser/MCP session with other concurrent agents — tabs get silently stolen
by other agents driving the same shared browser. If a tab's port doesn't match what you
expect, don't trust what's on screen; re-navigate and re-check.
