# Codex App-Server Wire Compatibility

Date: 2026-05-19
Status: implementation wave
Source of truth: https://developers.openai.com/codex/app-server.md

## Summary

Evener's AppWire backend is intentionally close to Codex app-server, but several
wire-level details still use Evener-local vocabulary or compatibility shims. This
wave makes Evener's JSON-RPC app-server surface Codex-shaped where the official
Codex app-server protocol defines a core method, status, item, or notification.
Evener-only features remain allowed, but must live behind explicit `evener/*`
methods or `thread.evener` extension fields.

The internal daemon and agent may keep natural Evener state names such as
`processing`. The boundary rule is stricter: once data crosses the app-server
JSON-RPC wire, core Codex fields should use Codex names.

## Compatibility Rules

- Accept legacy Evener names at ingress during migration, but emit Codex names.
- Keep Evener extensions namespaced under `evener/*` methods or `Thread.Evener`.
- Prefer typed helper predicates over scattered string checks.
- Keep user-facing labels free to say "processing" even when wire state is
  `active`.
- Add focused tests at every boundary that translates status, input, item, or
  notification shape.

## Core Wire Targets

### Status vocabulary

Codex thread status values are `notLoaded`, `idle`, `systemError`, and
`active` with optional `activeFlags`.

Codex turn active status is `inProgress`; terminal turn statuses are
`completed`, `interrupted`, and `failed`.

Codex item statuses use `inProgress` for active work and item-specific terminal
states such as `completed`, `failed`, or `declined`.

Evener should therefore emit:

| Concept | Current Evener wire | Codex wire target |
|---|---|---|
| thread busy | `processing` | `active` |
| active turn | `running` | `inProgress` |
| active command/tool item | `running` | `inProgress` |
| unloaded or ended stored thread | `ended` | `notLoaded` |
| thread error | `error` | `systemError` |
| interrupted turn | `canceled` | `interrupted` |

### Initialization

Codex clients send `initialize`, then an `initialized` notification. The server
rejects requests before initialization and rejects repeated initialization.
Evener should accept the `initialized` notification and have tests proving that
core requests are not processed before `initialize`.

### Request shape

Core methods should accept Codex field names first:

- `threadId` for thread addressing.
- `input: [{ type: "text", text: "..." }, ...]` for `turn/start` and related
  user input.
- `expectedTurnId` for active-turn operations where Codex requires it.

Evener-specific `ref`, `prompt`, `items`, `harness`, and queue/drain extensions
can remain, but they should be documented and tested as extensions.

### Item shape

Core Codex item type names are camelCase, including `userMessage`,
`agentMessage`, `commandExecution`, `mcpToolCall`, `fileChange`,
`dynamicToolCall`, `webSearch`, and others. Evener can use snake_case internally,
but Codex-shaped wire responses and notifications should emit Codex item names.

### Notification shape

Core notifications should match Codex method names and payload shapes:

- `thread/started`
- `thread/closed`
- `thread/status/changed`
- `turn/started`
- `turn/completed`
- `item/started`
- `item/completed`
- `item/agentMessage/delta`
- tool output delta methods where Evener can map cleanly

Payloads should carry `threadId`, `turnId`, `turn`, and `item` with Codex field
names. Evener extension fields should be additive.

### Errors and feature gates

Codex error payloads preserve `code`, `message`, and optional `data`.
Turn failures carry `error.message` plus optional Codex diagnostic fields such
as `codexErrorInfo` and `additionalDetails`. Evener diagnostics may remain, but
should not replace Codex fields on core methods.

Experimental Codex methods and fields should require
`initialize.params.capabilities.experimentalApi` where Evener claims Codex parity.

### Schema guard

The Codex doc recommends generating TypeScript or JSON Schema from the Codex
CLI. Evener should keep a fixture or generated subset test for the core surface it
claims to implement, so future drift is caught by tests instead of UI bugs.

## Kata Wave

1. Codex status vocabulary on Evener AppWire.
2. Codex initialization handshake and JSON-RPC envelope compatibility.
3. Codex request parameter and input-shape compatibility.
4. Codex item type and item-payload compatibility.
5. Codex notification lifecycle compatibility.
6. Codex error payload and experimental feature-gate compatibility.
7. Codex schema/fixture compatibility guard.

Each kata should land with focused tests and a commit subject that includes the
kata id.
