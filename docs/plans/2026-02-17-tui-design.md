# Serf TUI Design

## Overview

An interactive TUI for serf built as a daemon/client architecture. The serf agent runs as an HTTP server exposing REST+SSE, and a separate Bubble Tea terminal client connects to it.

## Goals

- Interactive coding agent experience with polished visual design
- Clean separation between agent and display via HTTP API
- Async-friendly: agent can keep working after client disconnects (configurable)
- Minimal changes to existing agent/llm packages

## Architecture

```
┌─────────────────┐         HTTP          ┌─────────────────┐
│   serf-tui      │◄──── SSE events ─────►│   serf serve    │
│  (Bubble Tea)   │──── REST input ──────►│  (HTTP server)  │
│                 │                        │                 │
│  cmd/serf-tui/  │                        │  cmd/serf/      │
└─────────────────┘                        └────────┬────────┘
                                                    │
                                           ┌────────▼────────┐
                                           │  agent.Session   │
                                           │  (existing core) │
                                           └─────────────────┘
```

Two binaries:
- `serf serve` — subcommand on the existing serf binary. Starts HTTP server, creates/manages a single session.
- `serf-tui` — separate binary. Pure display client connecting to the server.

## Server API

New package: `server/` (stdlib `net/http` only, no new dependencies).

### Endpoints

| Method | Path | Request Body | Response | Notes |
|--------|------|-------------|----------|-------|
| `POST /input` | `{"text": "..."}` | `202 Accepted` | Queues input. Returns `409 Conflict` if session is already processing. |
| `GET /events` | — | SSE stream | `Content-Type: text/event-stream`. Each event: `id:` (monotonic), `event:` (kind), `data:` (JSON SessionEvent). |
| `GET /status` | — | JSON | `{"session_id", "state", "turns", "model", "profile"}` |
| `POST /interrupt` | — | `204 No Content` | Cancels current processing via context cancellation. |

### SSE Details

- Server reads from `session.Events()` channel in a dedicated goroutine
- Fans out to all connected SSE clients
- Ring buffer (~1000 events) enables reconnecting clients to catch up via `Last-Event-ID`
- Each SSE event includes `id:` (monotonic counter) and `event:` (the SessionEvent kind)

### Input Flow

- `POST /input` puts text into a channel
- Server goroutine calls `session.ProcessInput(ctx, text)`
- If session is already processing, returns `409 Conflict`

### Session Lifecycle

- Server starts, creates a session (or resumes one via existing flags)
- Default: session persists and keeps working after client disconnects
- Optional: `--pause-on-disconnect` pauses when no SSE clients are connected
- `SIGINT`/`SIGTERM` triggers graceful shutdown: drain processing, save snapshot, close connections

## TUI Client

New binary: `cmd/serf-tui/`. Bubble Tea application.

### Dependencies

- `github.com/charmbracelet/bubbletea` — TUI framework
- `github.com/charmbracelet/lipgloss` — styling
- `github.com/charmbracelet/glamour` — markdown rendering
- `github.com/charmbracelet/bubbles` — viewport, text input components

### Layout

```
┌─────────────────────────────────────────────┐
│ serf ● connected  model: gpt-5  turns: 3    │  status bar
├─────────────────────────────────────────────┤
│                                             │
│ ▌ User                                      │
│ Fix the broken test in auth_test.go         │
│                                             │
│ ▌ Assistant                                 │
│ I'll look at the test file first.           │
│                                             │
│ ▸ shell_run  cat auth_test.go        [2.1s] │  collapsed tool
│                                             │
│ The test is failing because the mock...     │
│                                             │
│ ▾ edit_file  auth_test.go            [0.3s] │  expanded tool
│ │ -  expected := "old_value"                │
│ │ +  expected := "new_value"                │
│                                             │
│ ▸ shell_run  go test ./auth/...      [4.7s] │
│                                             │
│ All tests pass now.                         │
│                                             │
├─────────────────────────────────────────────┤
│ > _                                         │  input box
└─────────────────────────────────────────────┘
```

### Components

- **Status bar** (top): connection state, model, session ID, turn count. Updates live.
- **Message stream** (middle): scrollable viewport. User messages, streamed assistant text, tool calls.
- **Input box** (bottom): multi-line text input. Enter to send.

### Tool Call Display (Smart Collapse)

- Short output (<=5 lines): shown inline expanded
- Long output: collapsed to one line — tool name, description, duration. Expand/collapse with Tab or Enter.
- In-progress tools show a spinner until complete.

### Assistant Text

- Streamed with a cursor as `TEXT_DELTA` events arrive
- Rendered with glamour (markdown) and syntax highlighting in code blocks

### Keybindings

- `Enter` — send input
- `Ctrl+C` — quit TUI and stop server
- `Up/Down` — scroll viewport
- `Tab` — toggle expand/collapse on focused tool call

### Bubble Tea Model

- Top-level model: connection state, viewport, input component, messages list
- Messages list is append-only; new events add or update entries
- `TEXT_DELTA` events update current assistant message in-place for streaming feel
- `TOOL_CALL_START` creates a collapsible entry, `TOOL_CALL_END` finalizes it with duration

## Package Structure

```
server/
  server.go          Server struct, routes, lifecycle
  sse.go             SSE client management, ring buffer, fan-out

cmd/serf-tui/
  main.go            CLI flags (--addr), connect, run
  model.go           Top-level Bubble Tea model
  viewport.go        Message stream / scrollable area
  input.go           Text input component
  statusbar.go       Top status bar
  message.go         Message rendering (user, assistant, tool calls)
```

## What Stays Untouched

- `agent/` — no changes. Server consumes the existing Session API and Events() channel.
- `llm/` — no changes.
- `cmd/serf/` — gains a `serve` subcommand. Existing CLI behavior unchanged.

## Design Decisions

- **Autonomous tool execution** for MVP. No permission/approval gate. Architecture allows adding one later (server could hold tool execution pending a client approval response).
- **Single session** per server instance. No multi-session management.
- **Ctrl+C stops everything** — one exit behavior, simple mental model.
- **Polish-first MVP** — fewer features, high visual quality. Good colors, smooth streaming, nice typography.
- **Separate binary** for TUI client — clean separation from the agent.
