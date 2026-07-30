# AppWire Authoritative Rejoin Design

**Status:** Approved

**Date:** 2026-07-29

**Scope:** Serf daemon AppWire snapshot authority, atomic subscription
hydration, frontend retry ownership, retry-safe mutation projection, session
identity replacement, and the remaining reviewed RelaySession/Codex cut fixes.

**Supersedes:**
`2026-07-29-appwire-canonical-transcript-projection-design.md`.

## Decision

Keep the existing AppWire v2 protocol, transcript format, event projector, and
durable mutation IDs. Fix the observed stale-session failures at the two
authority boundaries that already exist:

1. The daemon owns one fully materialized in-memory turn snapshot. It seeds
   that snapshot once from the current session transcript before the event
   bridge starts, then advances it with the same committed notifications sent
   to subscribers.
2. The frontend owns hydration for as long as a pane, watcher, or durable
   mutation record owns a ref. A failed `thread/read(subscribe:true)` is
   retried while that ownership remains, including when the current WebSocket
   never leaves `ready`.

Notification retention is transport replay only. It does not define current
turn state. A subscribing read captures a clone of the daemon snapshot and its
notification cut under the existing `appserver.CommitProjection` boundary and
does no transcript I/O.

Mutation receipts describe whether accepted input is already visible:

| Mutation | Receipt projection state at acceptance |
| --- | --- |
| `turn/start` | `pending` |
| `turn/steer` | `pending` |
| `turn/queue` | `pending` |
| `turn/drainAsSteer` | `pending` |
| `turn/promoteQueuedAsSteer` | `pending` |
| `turn/interrupt` | `reflected` |
| `turn/cancelQueued` | `removed` |

This is a flag-day behavioral change. There is no compatibility path, dual
implementation, transcript migration, or negotiation.

## Why this is smaller than the superseded design

The rejected design made every transcript entry carry durable presentation
IDs, introduced transcript format v3, grouped transcript paging around those
IDs, and made the session own a new projection allocator.

That machinery is not needed for the reachable lifecycle:

- restored `SessionStart` already seeds the live projector's turn counter
  above the persisted transcript entry count;
- the daemon can seed its current projection before it starts consuming live
  events;
- clear happens while the old session is idle and can install a prepared new
  projection before bridging it; and
- existing generation and session-ID checks fence late old-session events.

The replacement therefore keeps projector-local turn/item IDs and solves the
authority problem directly. It accepts the existing transcript paging shape
and holds the projected transcript in memory for the life of the daemon.

## Root causes

The failures reported from a moving cellular connection are not one timeout:

1. `Session.tsx` marks its initial hydrate as started before awaiting
   `ensureThread`. If `thread/read` times out while the WebSocket remains
   ready, `ensureThread` rolls back its claim, the component swallows the
   rejection, and nothing retries until remount or a later ready transition.
2. `handleReady` also treats a rejoin failure as best effort and waits for a
   future reconnect, focus event, or remount. A healthy-enough socket can
   therefore remain `ready` while the transcript is permanently stale.
3. `connectionStore.connect(B)` leaves client A's state-change listener
   installed. A later A transition can overwrite the global state after B is
   authoritative.
4. `appTurnSnapshot` is a bounded notifier replay cache. Eviction can delete
   current turns, and subscribing reads can choose between that partial cache
   and a transcript that advanced independently.
5. A subscribing read can parse the transcript while holding the projection
   cut. Transcript persistence can be ahead of live event delivery, so the
   response and post-cut notification can represent the same semantic output
   under different projector identities.
6. Successful input receipts currently claim `reflected` before the
   corresponding user/steering/queue item is necessarily present. The browser
   then removes its optimistic copy and can show no evidence of accepted
   input.
7. `BridgeWithObserver` records `SessionStart` before updating thread
   metadata, allowing a notification to cross the cut before its corresponding
   snapshot fields.
8. Clear swaps the live session and AppWire identity before rendezvous update,
   then tries to roll both back on failure. A successful swap does not
   explicitly terminate the old subscription stream.

Two final reviewed Hub races are adjacent and must be carried forward:

- a RelaySession read cut must wait behind notifications accepted just before
  capture even when capture itself buffered no frame; and
- EOF observed during connection installation must prevent that dead client
  from becoming canonical.

The reviewed Codex source has one matching authority gap: a committed cache is
not authoritative while a newer dirty generation is still refreshing.

## Invariants

1. **One daemon snapshot authority.** `thread/read`, latest-window reads, and
   older-page reads all derive from the installed materialized snapshot.
2. **Seed before live delivery.** The current transcript is projected exactly
   once before the session event bridge starts.
3. **One committed transition.** Event projection, notifier sequence
   allocation, materialized-state reduction, and subscriber routing happen
   inside one `CommitProjection` transition.
4. **Retention is not state.** Notifier eviction cannot remove a turn, item, or
   paging cursor from the daemon snapshot.
5. **No read-time transcript I/O.** A subscribing or paging read only clones
   or windows memory. It never opens the transcript.
6. **Snapshot/cut agreement.** State included in a subscribing response is not
   replayed after that response; later committed notifications are delivered
   once in producer order.
7. **Owned hydration converges.** While any live owner retains a ref, a
   transient read failure schedules another read even if the socket stays
   `ready`.
8. **Retry pacing is not protocol truth.** Backoff controls load only. No
   elapsed duration decides whether a message was accepted, reflected, lost,
   or requires a reload.
9. **Stale is better than blank.** A failed refresh preserves the last
   published model until a newer authoritative response replaces it.
10. **Newest client wins.** Old-client state, read responses, errors,
    notifications, and retry callbacks cannot mutate state owned by a newer
    client generation.
11. **Release is terminal for that owner generation.** A late response or
    scheduled retry cannot resurrect a released pane or watcher.
12. **Receipt state is visible truth.** Accepted input remains optimistic while
    its receipt is `pending` and is removed only after identity-bearing state
    or a receipt-only terminal effect makes that correct.
13. **Identity replacement is prepared, then committed.** Preparation failure
    or rendezvous failure leaves the old session and stream intact. Successful
    replacement atomically installs the new authority and closes the old
    stream.
14. **Metadata does not lag its event.** Session metadata is updated before the
    notification transition that advertises it.

## Canonical daemon materialization

`appTurnSnapshot` becomes state storage rather than replay history:

```go
type appTurnSnapshot struct {
	mu           sync.Mutex
	threadID     string
	activeTurnID string
	turns        []appwire.Turn
	turnIndex    map[string]int
}

func (s *appTurnSnapshot) Seed(turns []appwire.Turn)
func (s *appTurnSnapshot) Apply(records []appserver.SequencedNotification)
func (s *appTurnSnapshot) Snapshot() []appwire.Turn
```

`Seed` deep-clones the projected transcript and rebuilds the turn index.
`Apply` ignores notification retention and reduces records in commit order.
The reducer keeps all currently handled lifecycle messages and adds the two
missing state-bearing cases:

- `item/agentMessage/reset` removes the named partial assistant item; and
- `serf/steering/injected` appends the steering item to the active turn using
  the same per-turn count-based identity and fields as the frontend reducer.

Warnings remain ephemeral because they have no transcript representation.
Thread-level status, queue, and metadata continue to come from `appThread`;
they do not become transcript items.

The following replay-cache fields and methods are removed:

- `limit`
- `cursor`
- `retainedLower`
- `records`
- `Cursor`
- `ReconcileAndSnapshot`

`appAllTurns`, `appLatestTurns`, and `appPageTurns` clone/window/page
`appTurnSnapshot`. They no longer compare transcript and notifier authorities.
`Notifier.RetainedWindow` may remain for transport users, but daemon snapshot
code does not call it.

The cost is deliberate: one daemon keeps one full projected transcript in
memory. That is simpler and safer than reparsing a mutable file inside a
subscription cut. A future memory optimization must preserve the same single
authority and cannot reintroduce independent read-time projection.

## Prepared identity lifecycle

Transcript loading happens before publication through an opaque prepared
value:

```go
type PreparedAppIdentity struct {
	// package-owned validated identity and seeded projection
}

func PrepareAppIdentity(sourceID, threadID, transcriptPath string) (PreparedAppIdentity, error)
func (s *Server) ReplaceAppIdentity(prepared PreparedAppIdentity, activate func())
```

`PrepareAppIdentity` validates that a non-empty transcript header belongs to
`threadID`, projects the full transcript, and builds a seeded snapshot and
fresh `AppEventProjector`. It performs no server mutation.

`ReplaceAppIdentity` is infallible after successful preparation. Under
`CommitProjection` it:

1. invokes the optional infallible `activate` closure;
2. updates the server's source/thread identity and status session ID;
3. advances the identity generation;
4. installs the prepared projector and snapshot;
5. clears active/reserved/failure-stamp state; and
6. when replacing a different non-empty identity, records one
   `thread/closed` notification targeted at the old thread/ref.

At startup, serve prepares and installs the current session before
`bridgeSession(sess)`.

For HTTP clear, serve creates the new environment/session and prepares its
identity without touching the old one. It then updates rendezvous. Only after
both steps succeed does it call `ReplaceAppIdentity`, passing an activation
closure that swaps `currentSess/currentEnv`. It then closes the old session
and starts the new bridge. Preparation or rendezvous failure closes the new
session and leaves the old authority untouched.

`SetTranscriptPathFunc` and read-time transcript callbacks leave the
production interface. Test fixtures prepare an identity from a real temporary
transcript instead of teaching `thread/read` to open files.

## Event and response ordering

`RecordAppEvent` retains the current sequence:

1. validate the event against the installed session identity;
2. project it;
3. stamp target and failure-count fields;
4. allocate notifier records;
5. apply those exact records to the installed snapshot; and
6. return the records for subscriber routing.

All six steps stay in one `CommitProjection` callback. There is no background
reconciliation loop.

`BridgeWithObserver` applies metadata/state effects before calling
`RecordAppEvent`. A snapshot may therefore see newer metadata just before its
redundant notification, but a delivered notification can never lead the
snapshot field it announces.

`CaptureSubscription` remains the response-cut primitive. Its snapshot
callback now clones memory, so it is bounded by projection size and performs
no filesystem work.

## Frontend owned hydration

The threads store owns one single-flight hydration lifecycle per
`(ref, owner-kind, owner-generation)`. Owner kinds remain separate for real
panes and watched child threads so their refcounts and rich/lean read
requirements do not interfere.

On a read rejection:

1. keep the existing model and ownership claim;
2. discard only that attempt's pending response-cut buffer;
3. if the client or ready epoch changed, join the newest attempt;
4. if the current client is still ready, schedule a retry with capped backoff;
5. if it is not ready, wait for that client generation's next ready event; and
6. before starting or publishing, recheck refcount, owner generation, client
   identity, and ready epoch.

Tests inject the retry scheduler and advance it explicitly. Production may use
capped exponential backoff with jitter, but delay values are load policy, not
correctness constants.

`Session.tsx` still claims once and releases once. The store no longer rejects
and rolls back that claim for a transient hydrate failure, so the component's
one-shot `started` flag cannot strand the pane. A terminal release cancels
scheduled work and fences the unresolved request by generation.

Pinned outbox refs use the same owned retry trigger. Mutation replay remains
closed until an authoritative read succeeds.

`connectionStore` retains and invokes the unsubscribe returned by the current
client's `onStateChange` before wiring a replacement. The callback also checks
that its captured client is still current before mutating the store.

## Mutation receipt projection

The durable mutation record remains the source of receipt state. Initial
successful application sets both `record.ProjectionState` and the serialized
receipt to the method-specific value in the decision table.

Start, steer, queue, drain, and promote remain `pending` until existing
incorporation/projection code advances them to `reflected` or `removed`.
Interrupt is already a receipt-only reflected effect. Cancel is already a
removed tombstone.

No response-before-notification ordering is required. The response may win the
race because `pending` instructs the frontend to keep its durable optimistic
item. Existing identity reconciliation removes that item once snapshot or live
state carries its client mutation ID.

## Hub carry-forward fixes

The implementation includes the already-reviewed RED/GREEN commit pairs:

- `619b894fae3b2ee36c910d14b287e174f0f170f4`
- `f0c3770e59f5d9f229392d2c02d8961a87300a6e`
- `291342018561ae8254f6d584a5ace2a203f1c137`
- `2969c4233c7f0beaf863bc1919f31c284a341829`

They preserve RelaySession FIFO response barriers, fence EOF during connection
installation, and reject a dirty Codex cache as authoritative. They add no new
wire fields or timers.

## Explicit non-goals

- transcript format v3
- durable presentation IDs on every transcript entry
- grouped logical-turn transcript paging
- a public stream epoch or sequence field
- an application-level mutation status RPC
- response-before-notification delivery as a correctness requirement
- treating request timeout as evidence that a mutation failed
- hot handoff between simultaneously active old and new daemon sessions
- silently dropped frame detection while one WebSocket stays ready

The last limitation is conscious. The browser trusts ordered WebSocket
delivery plus explicit `serf/thread/resync`. Current production paths either
deliver, disconnect a slow subscriber, or issue resync; review found no path
that silently omits a frame while keeping the socket ready and omitting
resync. Adding public epochs without a proven producer gap would expand the
protocol without fixing the observed one-shot hydration failure.

## Verification

Tests must prove behavior through controlled seams, not sleeps or rendered
warning text:

- notifier eviction leaves the seeded snapshot and paging results unchanged;
- transcript persistence ahead of event delivery cannot duplicate an answer;
- idle seeded history followed by a live turn cannot reuse a prior turn ID;
- subscribing `thread/read` performs no transcript read;
- assistant reset and steering reduce identically in live and rehydrated state;
- bridge metadata is updated before its notification crosses the cut;
- failed identity preparation and failed rendezvous leave the old stream
  intact;
- successful replacement closes the old stream once and installs only the new
  state;
- a timed-out initial or reconnect read retries on the same ready client;
- stale content remains visible between failed and successful refreshes;
- release cancels retry ownership and late responses cannot resurrect it;
- swapping A for B detaches A's state callback and fences every A completion;
- each retry-safe mutation method returns the projection state in the decision
  table, including replay after restart;
- pending receipts retain optimistic input until identity reconciliation;
- the four carry-forward RelaySession/Codex regressions stay green under
  repeated and race-enabled runs.

Default tests use fake clients, scripted transports, temporary transcripts,
and injected schedulers. They make no provider request and do not depend on
wall-clock races.
