# Round-5 PAR reconciliation record

Inputs reviewed at commit 29da1c6046 (base origin/main 2be411cd8f):
- Reviewer L, codex-jesse-fsck.com/gpt-6-luna: 1 significant.
- Reviewer M, kimi/kimi-for-coding: 3 significant + 3 minor.
- Reviewer N, lunaroute/glm-5.3-flash: 4 significant + 5 minor
  (~70 citations verified; round winner on count; converged with L on
  the round's central finding).

Every load-bearing claim was re-verified in-tree before acceptance,
including the two prior rounds' resolutions N spot-checked. The
findings are design-level, not citation-level: the grounding survived
three independent verification passes. The round's center of mass is
the round-4 relay fence — three of the eight significant findings are
against it, and one more (the trigger) is mine, found while verifying.

## New code facts verified this round (in-tree, this session)

1. `askPendingRevision` is a plain in-memory field, NOT persisted: the
   `askPending` block's own comment says "it is not itself part of
   persisted SessionMeta" (`agent/session.go:743-753`); restore
   re-derives the set via `deriveRestoredAskPending` →
   `setAskPendingLocked`, which increments a FRESH session's zero
   counter (`agent/session_tools_ask.go:143-149`), and each append
   increments again (:156-163).
2. The instance fence does not close the restart window:
   `PendingAskState.InstanceID` is `s.id` — the durable session id
   (`session_tools_ask.go:103`) — and the relay's
   `expected_instance_id` is compared against `s.appThreadID`, also
   durable, by `requireRootMutationIdentity`
   (`server/appwire_thread_settings.go:168-185`), whose own comment
   scopes it to Clear/replacement, not restart.
3. The single-ask restore REPEATS the revision value: a pre-restart
   live append mints revision 1; a post-restart restore of that set
   mints 1 again on the fresh counter. So round 4's K2#20 resolution
   ("a restart's re-derivation delivers once, on its new Revision")
   is false for restore-only restarts — the trigger as specified
   fires nothing (my addendum, found while verifying L1).
4. A durable per-boot counter already exists and already rides the
   wire: `schema.NextBootGeneration` (`agent/schema/boot_generation.go:31-37`)
   is fsynced before the boot serves ("a crash that loses buffered
   transcript entries can never be followed by a start that reuses a
   generation clients already hold"), is stamped on every history
   message as `appBootGeneration`, sits on the thread row beside
   `PendingAskState` (`server/appwire_thread_projection.go:117-119` —
   `EvenerThread.BootGeneration` is documented at
   `appwire/types_turn.go:382-386`), and is already a field of the
   content cursor (`internal/apphistory/paging.go` `contentCursor.BootGeneration`).
5. `SessionOperationRequest.UnmarshalJSON` decodes with
   `DisallowUnknownFields` (`appwire/session_tools.go:142-150`): a
   new request field is refused typed by an older daemon, never
   silently dropped.
6. The admission-check idiom exists: `ExpectedQueueRevision` on the
   turn params (`appwire/types_turn.go:886`), checked in
   `agent/session_client_mutation.go` — a durable revision compared at
   the admission site, exactly the shape the ask fence needs.
7. `AttentionEntry` carries no timestamp and no ask identity
   (`appwire/attention.go:10-30`); the broker's `latest` is a plain
   map published on a 5s tick (`cmd/evener-hub/attention_subscriptions.go:24-27`,
   `main_background.go:52-60`). `attentionAsOf` names machinery that
   does not exist.
8. The two rosters differ in population: the Manager ATTENTION roster
   is remote-inclusive via the navigation snapshot ("sessions on other
   hosts reach the hub only through the remote-thread cache",
   `main_background.go:63-66`), while the DAEMON roster
   (`roster.List()`, the source of the probe facts, `missCrashed` and
   `probeState`) is local-probe-only. `localSessionActivity` returns
   nothing when `entry.Activity == nil` (`app_activity.go:79-86`) —
   a fact remote-cache entries are not established to carry.
9. `localDaemonThreadStatus`'s diagnostics block is served only when
   non-empty (`local_daemon.go:1519-1533`).
10. The selection key is a hex STRING — `Key()` renders the coarse
    category bitmask (`appwire/history_content.go:47-51`) composed
    into the incarnation prefix (`internal/apphistory/paging.go:21,41`:
    `"content1:" + selection.Key() + ":"`) — not a bare uint8.
11. `PendingQuestion` displays `askPending[0]` while `PendingAskCalls`
    appends only identified calls in set order (`session_tools_ask.go:99-116`):
    with a call-less restored Q1 first and a live Q2, the excerpt shows
    Q1 while `Calls[0]` is Q2 — the frame's "first call's ask turn id"
    names the wrong question as scoped.

## Decisions

### The relay fence (spec 4; L1 + N-S1 + N-S2 + N-S3 + M1 + the trigger addendum)

- **D1 — the fence identity is the PAIR (durable boot generation,
  revision).** Within a boot the revision is monotonic; across boots
  the boot generation differs and is never reused (fact 4). The pair
  is unique per pending-set version per boot. One identity, two
  consumers, as round 4 intended for the revision alone:
  - The BROKER TRIGGER fires on the pair changing (with the existing
    flag/level set). This also repairs the trigger addendum: a
    restore-only restart delivers because the boot component changed,
    even when the revision value repeats (fact 3) — the single-ask
    restart becomes a red journey.
  - The RELAY FENCE compares the pair. N's alternative (compare the
    first call's ask turn id) is rejected: a stable set re-derives the
    SAME call ids across restore (round-4 verified), and call-less
    sets carry none.
- **D2 — the pair rides on frame AND row** (N-S2): the frame's ask
  facts and the row's `PendingQuestion` excerpt both carry the pair
  additively beside the ask turn id — same fields, two consumers, per
  the existing ask-turn-id pattern. Pull-discovered relays are fenced
  identically to frame-discovered ones.
- **D3 — enforcement is daemon-side, at the admission that clears the
  set** (N-S3): the only place the CURRENT revision is authoritative;
  a hub-side check against the probe cache false-accepts the append
  race (the cache trails by up to a probe cycle). The wire fields
  follow the `ExpectedQueueRevision` idiom (fact 6): additive
  `ExpectedAskRevision` + `ExpectedAskBootGeneration` on the
  queue/start params, compared at the admission site that runs
  `clearAskPending`. Older-daemon direction (fact 5): a fence-bearing
  request is refused TYPED by an older daemon's strict decode; the
  hub maps that to a named `ask-fence-unsupported-on-daemon` error
  whose guidance is an explicit, chosen unfenced relay after a fresh
  read (today's behavior, never silently stripped, never automatic).
  Removal condition: every supported daemon decodes the fields. The
  PR names the direction and the condition.
- **D4 — the fence names the SET, never a call** (M1): an admitted
  relay resolves EVERY pending call (the existing whole-set admission
  semantics; no per-call selection exists by design and changing it
  is a non-goal). The relay contract states this, and the guides teach
  the duty: with more than one pending question, relay everything the
  human has answered and re-read after. The sufficiency rule's
  count-one direct-relay condition stands.

### Spec 4, remaining

- **D5 — `attentionAsOf` names its machinery** (M2): `AttentionEntry`
  gains `publishedAt` stamped at the derivation site (`appwire/attention.go`
  + the `watchHubAttention` derivation are the named owned work); the
  derivation moment is the publish moment the join tolerance describes.
- **D6 — `AttentionEntry` carries the ask facts the broker compares**
  (N-M1): the bounded question, the count, and the PAIR, beside the
  existing flags.
- **D7 — the frame names the call that OWNS the displayed first
  question** (N-M2): the absent-when is "the displayed first question
  is call-less" (a call-less Q1 with a live Q2 names no id, not
  Q2's); the count guard still forces a fetch for multi-question sets.
- **D8 — the restored identity loss is stated** (M6): a set that HAD
  call identity serves call-less after restore, so the excerpt names
  no ask turn id exactly when the content-locate fallback applies;
  the fence pair is unaffected (the boot component changed).

### Spec 1

- **D9 — per-field remote behavior** (N-S4): remote rows APPEAR (the
  attention roster's population) and carry their own row facts
  (`status.type`, `Failure`, `AskPending`, `PendingQuestion`,
  `LastTurnEndedAt` — the thread row the remote hub serves); the
  local hub's probe-derived additions (`currentTurnId`,
  `turnStartedAt`, `quietForMs`, `probeState`, the crashed
  discriminator) are ABSENT on remote lanes — a dead remote host is a
  source error (`attempted-failed`), never a local crashed row;
  `attentionLevel` IS present (the roster join is remote-inclusive).
  The remote source's field-by-field row rebuild names the thread-row
  facts it forwards (the same landing zone as spec 2's capability
  bit). Contract 1's classification promise is scoped: the full
  operational classification is for locally probed lanes; a remote
  lane's working/failed classification comes from its own row, and
  its crashed/quiet story is its owning hub's. The journey seeds a
  remote lane asserting the per-field absences.
- **D10 — the awaiting rest is classifiable** (M3): contract 1 gains
  "needs-response rest via `status: awaiting` with `AskPending:
  false` and no escalations" (`NeedsResponse = awaiting &&
  !askPending`, `hubapi/attention.go:75-78`; the row already carries
  the components; no new field).
- **D11 — diagnostics qualifier** (M4): "plus a jobs/watches
  diagnostics block when non-empty."

### Spec 2

- **D12 — the selection-key sentence is corrected** (M5/N): the key is
  the hex-string `Key()` of the coarse category bitmask composed into
  the incarnation prefix (fact 10); the window/types/bounds vocabulary
  is an owned EXTENSION of the same key scheme, not a rider on a
  uint8. The owned-work conclusion is unchanged.
- **D13 — the coarse translation is bounded** (N-M3): the tool edge
  translates the coarse flags that have an exact-type equivalent (the
  `messages` shape) and REFUSES the rest typed at the tool edge,
  naming the exact-type form to use; no undefined combos remain.
- **D14 — combination gaps closed** (N-M4): (a) `itemTypes`, byte
  bounds and budget ride ONLY with a `turnWindow` — without one they
  are refused typed at the tool edge and today's read shape is
  unchanged byte-for-byte (no filter-only mode exists to define);
  (b) `appliedBy` gains `saved-index` for the hub's own index applying
  the window daemonlessly (three values: serving-daemon, hub-compat,
  saved-index).

### Spec 3

- **D15 — contract 1 reconciled with contract 6** (N-M5): every
  served read carries the facts "on paths that can state them;
  absent-when per contract 6, never fabricated."

### README

- The spec-4 grounding bullet and rule 6 ownership list move from
  Revision to the pair; the spec-1 population bullet gains the
  per-field remote qualifier; the review trail gains round 5.

## Round 6 plan

The fix pass lands D1-D15, then one more competing round with three
never-seated models gates the implementer lanes: a round with no
significant findings across all three reviewers is Jesse's bar. The
fence is the load-bearing change of this pass; if round 6 produces
another fence-level significant finding, the fence gets a red-test
spike before any further revision.
