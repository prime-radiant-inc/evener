# MCP Resilience — Design (v1)

Date: 2026-07-04
Status: Draft — awaiting Jesse's spec review
Workstream: 5 of 6 from the 2026-07-03 web-UI UX diagnostic ("Linear MCP was 400-ing" with no clarity)

## Problem — three stacked defects

1. **One bad server kills the whole session before it exists.** `mcp.NewManager` treats any per-server failure — transport build, connect (manager.go:62-65), or tool discovery — as fatal: it calls `mgr.Close()` (closing every *healthy* server already connected, manager.go:174-183) and returns nil. That error propagates `initMCP` (session_init.go:1150-1179) → `initSessionState` (:597-599) → `NewSession`, so the `*Session` is never constructed; `serf serve` exits before binding its listener (serve.go:249 precedes the Listen at :260). The hub sees "process exited before rendezvous" and forwards the MCP error buried in up to 64KB of raw subprocess stderr as an HTTP 500 (spawn.go rendezvous race; web_spawn.go:60). One 30s context budget covers *all* servers combined (session_init.go:1163); connects are serial, attempted exactly once, with no per-server timeout.
2. **Server-reported tool errors render as success.** MCP failures split into two channels in `tool.Registry.ExecuteCall` (registry.go:446-534). Channel A (transport error): `err != nil` → `IsError: true`, raw Go error text. Channel B (the server itself reports `CallToolResult.IsError` — exactly how a well-behaved Linear server wraps an upstream 400): `mcpResultToString` (manager.go:219-244) folds it into a plain `"[MCP Error] …"` string, which falls to the registry's final branch where **`IsError` is hardcoded `false`** (registry.go:532-533). Downstream everything keys on IsError: the web renders MCP calls with the default tool renderer (no body), so a Channel-B failure shows a collapsed green "ok" with the error text invisible; the TUI likewise. A failing Linear call is visually indistinguishable from a working one.
3. **Health exists nowhere honest.** A dead server's tools stay registered for the session's life — no reconnect, no retry, the SDK client gets no notification handlers (manager.go:43-46), and `Servers()` replays the startup snapshot forever (manager.go:158-171). The settings probe (`mcpstatus.ProbeMCPStatus`, mcp_status.go:29-81) is orphaned twice over: it's computed on every settings render and discarded (templates/partials/settings/mcp.html has no status reference), and for http/sse it calls any response "available" without reading the status code (mcp_status.go:57-61) — a 400 passes. Its documented vocabulary never matched its return values (web_types.go:64). The wire has a natural home — `SerfMCPServerInfo{Name, Tools}` on the diagnostics sidecar (appwire/types.go:267-270), rendered in two TUI spots (details_drawer.go:108-138, hub_status.go:88-130) — but it carries no health field.

## Decisions (Jesse, 2026-07-04)

1. **Minimal mid-session reconnect is in scope**: lazy, call-driven, one attempt per server per 30s, no background loops, no tool-list re-discovery.
2. Carried: OAuth for MCP servers is out (static `headers` + env expansion — config.go:146-191, manager.go:319-330 — is the documented Linear workaround; full MCP OAuth is a future workstream).

## Design

### Non-fatal, parallel, per-server startup

`NewManager` always returns a manager plus per-server results: `[]ServerStartResult{Name, Status: connected|failed, Stage: transport|connect|discover|register, Err}`. Connects run **in parallel**, each under its own timeout (new constant, 10s), replacing the shared 30s batch budget — startup gets faster *and* isolated. A failure records its result and moves on; healthy servers are never torn down on a sibling's account. Tool-name collisions with core tools (today fatal via RegisterTools, manager.go:129-131) become that server's `failed(register)` result. `initMCP` emits one `EventWarning` per failed server — "MCP server \"linear\" unavailable: connect: …" — riding the existing warning path into web and TUI transcripts as a visible system line at session start. CLI `--mcp-config` parse failures and malformed `--mcp` inline specs stay fatal (mcpconfig.Discover semantics unchanged — explicit user input fails fast); a missing global/project `.mcp.json` stays non-fatal as today. Config layering, env expansion, and subagent MCP-zeroing (subagents.go:364-365) are untouched.

With this, an MCP failure can no longer kill a spawn — the buried-stderr HTTP 500 path simply stops being reachable for MCP causes. (The generic spawn-stderr blob for other causes is WS6 material.)

### Severity fix (bug, TDD)

The MCP Exec closure returns Channel B through the error path: `if result.IsError { return nil, errors.New(mcpResultToString(result)) }`. The registry's existing error branch then marks `IsError: true`, the web gets its red ✕ and `data-attention="error"`, and the "[MCP Error] …" text becomes visible as the error body instead of hidden behind a bodyless default renderer. Regression tests pin both channels, plus a jstest asserting an MCP-namespaced tool call with an error renders the error marker.

### Lazy reconnect

Each connection gains a mutex-guarded `{session, broken bool, lastAttempt time}`. A Channel-A failure marks the connection broken. The next call to any of that server's tools, if ≥30s since the last attempt, rebuilds the transport and reconnects once: success swaps the session in (tool set stays as discovered at startup — no re-ListTools); failure errors as today and stamps the backoff clock. Parallel tool calls are safe under the per-connection mutex. Reconnect success/failure updates the per-server status (below) and emits a warning-path line on recovery ("MCP server \"linear\" reconnected").

### Honest health, rendered

- **Wire**: `SerfMCPServerInfo` gains `Status string` — `connected` (live), `degraded` (broken mid-session, awaiting its next reconnect window), `failed` (never connected at startup) — and `Error string` (last failure, empty when healthy), populated from the manager's live per-server state via `DetailedStatus` (status.go:76-78). Both TUI diagnostics renderers grow one status token per server line.
- **Probe**: replaced by a real handshake. New small public package `agent/mcpprobe` (the machinery lives in `agent/internal/mcp`, unreachable from the root module — a thin public wrapper is required): for http/sse, build the transport and run an actual MCP initialize (+ tool count) under a 3s timeout, classifying `available (N tools)` / `unreachable: <err>` — a 400 now fails honestly; for stdio, keep the command-present check but label it truthfully (`command found`, not `available` — settings renders must not spawn arbitrary processes). The hub's `mcpstatus` package dies; `discoverMCPsForSettings` (web_settings.go:493) calls the probe; the `web_types.go:64` comment drift is fixed to the real vocabulary.
- **Settings**: Settings → MCP servers gains a server list with the status column that `settingsData.Mcps` always fed and the template never rendered — name, transport, source layer, probe status.

## Error handling

Session start with *zero* healthy servers is still a healthy session (tool-less MCP, warnings say why). Reconnect never blocks a tool call beyond the single attempt; a reconnect racing session close aborts cleanly (manager Close marks all conns closed; closures check before dialing). Probe timeouts classify as unreachable, never hang a settings render (3s cap). Warning emission failures are non-fatal as elsewhere.

## Contract changes (tests that deliberately flip)

`TestIntg_InitMCP_ConnectError` / `DiscoverError` / `RegisterToolsError` (agent/cov_intg_mcp_test.go:125-177) and `TestIntgMCP_NewManager_TransportBuildError` / `ConnectError` / `ListToolsError` (agent/internal/mcp/cov_intg_manager_test.go:27-90) currently pin fatal startup; they flip to pin graceful degradation (session constructs, failed server reported, healthy siblings live — the previously-unpinned "healthy servers survive a sibling's failure" gets an explicit spy test).

## Out of scope

MCP OAuth (headers workaround documented); `tools/list_changed` subscription and mid-session tool-list refresh; background health polling; per-server enable/disable UI; subagent MCP inheritance; the generic spawn-stderr clarity cleanup (WS6).

## Testing

- Manager: parallel-connect result matrix (N servers × {ok, transport-fail, connect-fail, discover-fail, register-collision}); healthy-survive-sibling-failure spy; per-server timeout isolation (one hung server doesn't consume siblings' budget); reconnect (broken→backoff→success swap→status transitions; racing close).
- Severity: Channel-A and Channel-B both `IsError: true` end-to-end (registry → session_tools endData.Error), Channel-B carries the "[MCP Error]" body; jstest error-marker render for namespaced tools.
- Probe: http 200 → available; http 400 → unreachable (the exact regression); connection refused → unreachable; stdio present/missing labeling.
- Surfaces: EventWarning per failed server at start; SerfMCPServerInfo status/error over the wire; TUI renderer lines; settings status column template test.
- e2e card: session with one good + one 400-ing server → starts, warns, good server's tools work, failing tool call renders red, settings shows unreachable; kill + restart the good server mid-session → next call reconnects.

## Estimate

~600–900 loc including tests: manager rework (parallel, per-server results, non-fatal) ~150–220; severity fix ~30–60; reconnect ~80–120; probe + settings column ~120–180; diagnostics fields + TUI renderers ~60–90; warnings ~40–60; test flips + new coverage ~200–300 (overlaps ranges above).
