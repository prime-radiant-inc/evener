# Shared Notes Design

Date: 2026-09-08. Approach A: first-class persisted fields, goal-style RPCs.
Revision 2 folds in two adversarial review rounds (10 unique significant
findings plus minors; neither reviewer disqualified).

## Purpose

Each session gets two one-paragraph whiteboards (human, agent) plus an
agent-curated URL list. Both sides read everything. The human edits only the
human paragraph; the agent edits only its paragraph and the URL list; either
side removes URLs. Human whiteboard updates interrupt the agent thread like a
steer. Hub Details sidebar and TUI drawer display all three.

## Data model and ownership

Extend persisted `SessionMeta` (`agent/schema/snapshot.go`, `<id>.meta.json`)
with `humanNote: string`, `agentNote: string`, and
`sessionUrls: [{id, url, label?, addedBy: agent, addedAt}]`. Mirror
in-memory on `Session` (`agent/session.go`). Project onto wire `EvenerThread`
(`appwire/types.go`), hub `fetchStatus`/`daemonStatus`
(`cmd/evener-hub/web_session.go`), and frontend `ThreadModel`
(`protocol/model.ts`, `types.gen.ts`). Old sessions default to empty note and
empty list. URL entry ids are server-assigned UUIDs returned by `urls/add`.
The URL list caps at 50 entries; beyond that the server rejects with a typed
error. A paragraph is one block: collapse every run of whitespace (including
newlines) to a single space, cap at 1000 Unicode characters; the server clamps
and returns the stored value, and every downstream consumer (including the
steered text below) uses the post-clamp value. No-op detection
clamp-then-compares: the server normalizes and clamps the incoming text
first, then compares against the stored note, so re-saving identical
over-length text stays a no-op.

Ownership is enforced by channel, not by a caller-role flag (neither the tool
`Exec(ctx, env, args)` args nor the hub mutation envelope carries a role).
Each verb exists on exactly one invocation channel, and the other channel has
no route for it:

| verb | hub-to-daemon RPC | agent tool |
| ---- | ----------------- | ---------- |
| `notes/human/set` | yes | no |
| `notes/agent/set` | no | yes |
| `urls/add` | no | yes |
| `urls/remove` | yes | yes |

Cross-channel invocation fails by construction (unknown method, unregistered
tool). Ownership tests exercise the wrong channel and assert rejection.

## Wire RPCs and pushes

Only hub-to-daemon mutations (`notes/human/set`, hub-side `urls/remove`)
follow the retry-safe-mutations spec (clientMutationId, expected-instance
fencing, authoritative rejoin). Agent tools carry no mutation envelope, so they
get at-least-once semantics with server-side convergence instead: `urls/add`
dedups on the canonical URL key (below), and `notes/agent/set` is idempotent
by value (repeat sets converge; last writer wins). Wire conformance tests
(fencing, idempotent retry) cover the hub RPCs only.

- `notes/human/set` (hub to daemon): stores the clamped note, appends a
  session event, emits via the projector path (below), and injects a
  top-level steering message through `AcceptClientMutationSteer` so the update
  interrupts like steer. Reuse the steering path; do not fork it. The inner
  steer derives its mutation id deterministically from the outer
  clientMutationId (`outer + "/note-steer"`), and the daemon dedupes on it, so
a hub retry of the outer RPC never double-interrupts. A save whose text
equals the stored note is a no-op: return the current value with no push and
no steer — including an empty save on an already-empty note. Clearing means
a non-empty-to-empty transition, which notifies with a
"(whiteboard cleared)" marker instead of empty inline text.
- Idle sessions follow `turn/steer` semantics exactly: the injected steering
  wakes the session via a steering-carrier turn, which costs a model turn.
  The Details UI names this when the session is idle ("saving will wake the
  agent").
- `notes/agent/set` (agent tool, same family as `update_goal`; handler beside
  `session_tools_goal.go`).
- `urls/add` (agent tool: url plus optional label). The server validates the
scheme (http(s) or `file:`), resolves bare file paths against the session cwd
per the `securepath` precedent, and `securepath`-checks absolute
`file:///` URLs the same way. The dedup key is the canonical resolved URL:
bare paths resolve against the session cwd first (so `docs/x.md` and
`./docs/x.md` collide, and a bare path collides with its `file:///` absolute
form); http(s) URLs normalize by lowercasing scheme and host, dropping
default ports, collapsing a trailing slash on an empty path, and dropping
the fragment. Re-adding an existing URL with a
different label updates the label and returns the existing entry (id,
addedBy, addedAt unchanged). URL and label lengths cap at 2048 and 280
characters; over-length adds fail with a typed error the tool surfaces.
- `urls/remove` (agent tool plus hub RPC, by entry id).

Pushes `evener/notes/updated` and `evener/urls/updated` mirror
`evener/goal/updated` exactly: the daemon appends `EventNotesUpdated` /
`EventUrlsUpdated` session events (`agent/events/events.go`) and
`internal/appprojector` derives the pushes from them
(`EventGoalUpdated` to `NotifyEvenerGoalUpdated` precedent). Daemon handlers
never emit pushes directly, so there is exactly one emission path and no
double-push. Daemon RPC handlers sit beside `server/appwire_turns.go` and
`server/appwire_runtime.go` (immediate receipt, async loop); hub relays via
`appserver.HandleTyped` like goal and steer. Frontend handles both pushes in
`protocol/reducer.ts` and in `threads.ts` dispatch (thread plus watched
models, fallback invalidation, same as the goal path). Wire checklist for
all four RPCs plus both pushes: `appwire/protocol.go` catalog entries,
`appwire/client.go` methods, `docs/appwire-protocol.md`, `make generate` for
`types.gen.ts` (never hand-edited), and deep-copy support in `appwire/clone.go`
(the URL slice must not alias across cloned snapshots). Capability: a
`SharedNotes bool` on `ThreadCapabilities` (beside `Goal`), true for live
evener sessions whose daemon wires the four verbs. Display rule, in order:
(1) capability unset → the section hides entirely (old daemon, unknown
source); (2) capability set but session not live → the section shows
read-only with no edit trigger and no remove buttons; (3) capability set
and live → full section with edit and remove. The two hub RPCs reject when
the capability is unset — so an old daemon with a new hub fails closed
instead of dropping writes. Hub reject is a pre-flight gate like `goal/set`
(`TestHubRPCGoalSetGatedByCapability` precedent: the hub reads the
capabilities from its own thread/read and returns structured Unavailable
before the call reaches the source), named in the relay beside the goal
gate. TUI plumbing is explicit because the TUI never reads
`appwire.ThreadCapabilities` directly: add `SharedNotes` to
`hubSessionCapabilities` (`cmd/evener-tui/hub_types.go`), copy it in
`hubDetailFromThread` field-by-field like every other bit, and leave it out
of the non-live zeroing block (notes stay readable on ended sessions; only
the edit/remove commands gate on it). The capability gates the daemon verbs;
session liveness gates the affordances. Hub and TUI hide the edit affordance
and remove buttons whenever the session is not live, regardless of the
capability bit — the same status-blind-capability plus live-check layering
the composer already uses. Past sessions: `pastThreadCapabilities`
(`cmd/evener-hub/app_threadread.go`) sets `SharedNotes: true`, and
`pastEntryThread` projects stored notes and URLs into the thread snapshot,
so ended sessions display all three read-only with no edit trigger (rule 2:
capability set, not live — the live-check hides the trigger). Notes RPCs on an ended session behave like `goal/set`: the hub resumes the
session first (qp94 auto-resume) and the write lands once it runs — a
human save on an ended session therefore wakes it, exactly like an idle
session. The close frame (`stampClosedThreadCapabilities`) carries the same
set. Tests: appCapabilities wiring test, hub reject-when-unset test for both
hub RPCs, hub and TUI gating tests (capability-unset hides the section
entirely; ended-with-capability shows the section read-only with no
trigger), TUI capability-mapping test.

## Agent read path

"Both sides read everything" holds for the agent through two mechanisms.
First, the daemon injects the current notes plus URL list into agent context
at turn start and on resume (beside the goal continuation-prompt rendering),
and the session loop refreshes that context from `EventNotesUpdated` /
`EventUrlsUpdated` before the next round, so human URL removals reach the
agent even though the wire pushes travel hub-ward. Second, a `notes/read`
agent tool returns the current human note, agent note, and URL list for
mid-turn polling. The human note's one-shot steer injection is delivery, not
storage: the persisted note is the source of truth the agent re-reads.

## Hub Details UI

One new "Shared notes" section in `DetailsPanelBody`
(`cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.tsx`), shared
by the session-chrome sheet and the `sessionDetails` pane. Unlike other
Details rows, this section renders whenever the capability is set: the empty
state shows an explicit
affordance ("Add a note"), because empty is the default for every old session
and an omit-when-absent rule would leave no trigger to click. Human
paragraph: read view plus `GoalControl`-style click-to-edit popover; save
dispatches `notes/human/set` via `threads.ts` and `mutationDispatcher.ts`,
with `TasksPanel`-style refetch on `evener/notes/updated`. Save commits on
success with a generation guard (same as `setGoal`); on failure it shows an
error toast and keeps the draft in the popover. There is no optimistic
unsaved-flag state anywhere in the hub precedent, so this design adds none.
Agent paragraph: read-only, updated by the same push. URL list: rows reuse
`ContextCard` and transcript-link rendering (http(s) opens a new tab; `file:`
and paths use the `docContent.ts` builders plus `OpenButton`); each row has a
remove button dispatching `urls/remove`; agent-added rows appear live via
`evener/urls/updated`. No human add-URL affordance. New components live under
`panes/session/chrome/`; Biome scope stays `src/`.

## TUI and notification rendering

The TUI mirrors hub: notes plus URL rows in `cmd/evener-tui/details_drawer.go`
(read views, human-note edit command, url-remove command) through the existing
hub-command registry; the reducer in
`cmd/evener-tui/internal/transcript/reducer.go` handles both pushes, with a
dispatch case (or explicit ignore) for each new notification to satisfy
`TestEveryWireNotificationIsHandledOrExplicitlyIgnored`.

Human-note updates render in-thread as a new steering kind `human-note`
(Go `events` steering kind, generated union, `KIND_LABELS` entry "Human
note"): a labeled steering divider carrying the full post-clamp text
("human updated their whiteboard: ..."), collapsed by default like other
dividers, with the same TUI label treatment. The exhaustive `KIND_LABELS`
record fails the build until the new kind gets its label, which keeps the
frontend's kinds in lockstep. Non-goal clarification: a new steering kind
plus label is in scope; no new transcript card components
(`NotificationCard` untouched).

## Edge cases

Clearing (a non-empty-to-empty transition) fires the push so both sides
converge. Over-length input clamps server-side; the response carries the
stored value. Same-field races on hub RPCs resolve last-writer-wins through
mutation fencing (expected-instance and queue revision reject stale writes;
clients retry like goal and steer). Duplicate URL add returns the existing
entry with its label updated. Add performs no fetch: unreachable hosts are
accepted because the list holds references, which also keeps default tests
hermetic. Out-of-scope file paths (bare or `file:///`) fail `securepath`
validation with a typed error the tool surfaces. Rejoin carries the latest
notes and URLs in the authoritative snapshot; pushes resume after. Daemon
down at human save behaves exactly like goal-set failure: nothing commits,
an error toast shows, the draft stays in the popover.

## Testing

Default tests stay hermetic: scripted provider at the LLM boundary, no live
fetches. Agent and daemon: `SessionMeta` extension plus migration defaulting,
ownership tests via wrong-channel invocation, steering-injection-on-human-set
(held, queued, and idle-carrier cases beside
`session_steering_held_test.go`), retry of one outer mutation id producing a
single steer, URL validation, canonical dedup, label-update-on-re-add,
securepath rejection, 50-entry cap, URL/label over-length rejection, single
push emission per mutation from
the projector path, agent context injection containing current notes and
URLs, `notes/read` returning current values. Wire: retry-safe-mutation
conformance for the two hub RPCs (fencing, idempotent retry, rejoin snapshot
carries notes and URLs), hub pre-flight reject-when-unset for both hub RPCs
(`TestHubRPCGoalSetGatedByCapability` pattern), `pastThreadCapabilities`
carrying `SharedNotes: true` with past-thread projection of stored notes
and URLs, notes RPC on an ended session resuming first (goal/set-resumes-past
pattern). Hub: `DetailsPanel.test.tsx`-style component tests
(empty state renders affordance, edit dispatches RPC, remove dispatches
RPC, push rerenders, failure toasts with draft kept, section hidden when
capability unset, ended-with-capability read-only with no trigger) plus `threads.ts` and
`reducer.ts` push tests. TUI: drawer render and command tests beside
`hub_goal_test.go`, plus the notification-coverage gate, plus a
`hubDetailFromThread` capability-mapping test covering `SharedNotes`. E2E scenarios in
`test/scenarios/` for human-note-interrupts-thread and
agent-add/human-remove-URL, mirroring the goal set-and-complete scenarios.
Also: appending `human-note` to `events.AllSteeringKinds` (the generation
source for the `STEERING_KINDS` catalog in `internal/appwirets/emit.go`) with
its producer-coverage test updated, and `make generate` golden output
refreshed.

## Non-goals

No edit history or versioning; no human URL add; no rich bookmarks (title,
description, tags); no availability check at add time; no new transcript card
components. History can layer onto this model later if wanted.
