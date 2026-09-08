# Shared Notes Design

Date: 2026-09-08. Approach A: first-class persisted fields, goal-style RPCs.

## Purpose

Each session gets two one-paragraph whiteboards (human, agent) plus an
agent-curated URL list. Both sides read everything. The human edits only the
human paragraph; the agent edits only its paragraph and the URL list; either
side removes URLs. Human whiteboard updates interrupt the agent thread like a
steer. Hub Details sidebar and TUI drawer display all three.

## Data model and ownership

Extend persisted `SessionMeta` (`agent/schema/snapshot.go`, `<id>.meta.json`)
with `humanNote: string`, `agentNote: string`, and
`sessionUrls: [{id, url, label?, addedBy: human|agent, addedAt}]`. Mirror
in-memory on `Session` (`agent/session.go`). Project onto wire `EvenerThread`
(`appwire/types.go`), hub `fetchStatus`/`daemonStatus`
(`cmd/evener-hub/web_session.go`), and frontend `ThreadModel`
(`protocol/model.ts`, `types.gen.ts`). Old sessions default to empty note and
empty list. The server enforces ownership: human-note set rejects agent
callers, agent-note set rejects the human path, url-add accepts agent only,
url-remove accepts either. A paragraph is one block: normalize whitespace, cap
at 1000 Unicode characters; the server clamps and returns the stored value.
URL entry ids are server-assigned UUIDs returned by `urls/add`.

## Wire RPCs and pushes

Four mutations plus two pushes, all following the retry-safe-mutations spec
(clientMutationId, expected-instance fencing, authoritative rejoin):

- `notes/human/set` (hub to daemon): stores, emits `evener/notes/updated`,
  and injects a top-level steering message through `AcceptClientMutationSteer`
  so the update interrupts like steer. Reuse the steering path; do not fork it.
- `notes/agent/set` (agent tool, same family as `update_goal`; handler beside
  `session_tools_goal.go`).
- `urls/add` (agent tool: url plus optional label). The server validates the
  scheme (http(s) or `file:`), resolves bare file paths against the session cwd
  per the `securepath` precedent, and dedups identical URLs.
- `urls/remove` (agent tool plus hub RPC, by entry id).

Pushes `evener/notes/updated` and `evener/urls/updated` mirror
`evener/goal/updated`. Daemon handlers sit beside `server/appwire_turns.go`
and `server/appwire_runtime.go` (immediate receipt, async loop); hub relays via
`appserver.HandleTyped` like goal and steer. New agent event kinds go in
`agent/events/events.go` as needed.

## Hub Details UI

One new "Shared notes" section in `DetailsPanelBody`
(`cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.tsx`), shared
by the session-chrome sheet and the `sessionDetails` pane. Human paragraph:
read view plus `GoalControl`-style click-to-edit popover; save dispatches
`notes/human/set` via `threads.ts` and `mutationDispatcher.ts`, with
`TasksPanel`-style refetch on `evener/notes/updated`. Agent paragraph:
read-only, updated by the same push. URL list: rows reuse `ContextCard` and
transcript-link rendering (http(s) opens a new tab; `file:` and paths use the
`docContent.ts` builders plus `OpenButton`); each row has a remove button
dispatching `urls/remove`; agent-added rows appear live via
`evener/urls/updated`. No human add-URL affordance. New components live under
`panes/session/chrome/`; Biome scope stays `src/`.

## TUI and notification rendering

The TUI mirrors hub: notes plus URL rows in `cmd/evener-tui/details_drawer.go`
(read views, human-note edit command, url-remove command) through the existing
hub-command registry; the reducer in `internal/transcript/reducer.go` handles
both pushes. Human-note updates render in-thread through the existing
`notification` steering kind (hub `SteeringItem` to `NotificationCard`, TUI
`job_notification.go`) with a new source label (e.g. `human-note`) reading
"human updated their whiteboard: ..." with the new text inline. No new
transcript card types.

## Edge cases

Empty note clears the whiteboard; the push still fires so both sides converge.
Over-length input clamps server-side; the response carries the stored value.
Same-field races resolve last-writer-wins through mutation fencing
(expected-instance and queue revision reject stale writes; clients retry like
goal and steer). Duplicate URL add returns the existing entry. Add performs no
fetch: unreachable hosts are accepted because the list holds references, which
also keeps default tests hermetic. Out-of-scope file paths fail `securepath`
validation with a typed error the tool surfaces. Rejoin carries the latest
notes and URLs in the authoritative snapshot; pushes resume after. Daemon down
at human save fails like goal-set-unavailable: the UI keeps the optimistic text
and flags it unsaved.

## Testing

Default tests stay hermetic: scripted provider at the LLM boundary, no live
fetches. Agent and daemon: `SessionMeta` extension plus migration defaulting,
ownership rejection matrix (human/agent by note and url verbs),
steering-injection-on-human-set (held, queued, and idle cases beside
`session_steering_held_test.go`), URL validation, dedup, and securepath
rejection, push emission per mutation. Wire: retry-safe-mutation conformance
(fencing, idempotent retry, rejoin snapshot carries notes and URLs). Hub:
`DetailsPanel.test.tsx`-style component tests (edit dispatches RPC, remove
dispatches RPC, push rerenders) plus `threads.ts` push tests. TUI: drawer
render and command tests beside `hub_goal_test.go`. E2E scenarios in
`test/scenarios/` for human-note-interrupts-thread and
agent-add/human-remove-URL, mirroring the goal set-and-complete scenarios.

## Non-goals

No edit history or versioning; no human URL add; no rich bookmarks (title,
description, tags); no availability check at add time; no new transcript card
types. History can layer onto this model later if wanted.
