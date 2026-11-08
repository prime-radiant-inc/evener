# Round 22 — three-seat reconciliation (record of decision)

Seats: R gpt-5.6 (`dlg_034tlEXc9BYjnSdu3db1KM`), S gpt-6.1-sol-1m
(`dlg_034tlEZT4i04okFlJ8eTvq`), T deepseek-4.1-flash-background
(`dlg_034tlEb1Hv7HRcIo6RRCCO`). Reviewed at `8e45a05bb9` (the round-21
revision pass, on top of `531d18c96a` the round-21 record) over the
production-identical base `2be411cd8f`. All three seats ran the required
reds uncached (`-count=1 -v`, sources inspected first): 4/4 red
families reproduced for their documented reasons (five individual
reds), 4/4 characterizations passing, zero disputed, worktrees clean.

## Round-21 closures — all eight CLOSED

- **R21-R-1** — CLOSED by all three seats. The predicate
  (`rec.Recorded && OriginalOrdinal == nil && TurnKind ==
  TurnSpanExecution`) is the session's own rule
  (`agent/session_execution.go:129-139`), airtight both ways: R ruled
  out every false positive and false negative branch by branch (T
  independently, adding the delivery kind `TurnSpanDelivery` at
  `agent/transcript/placement.go:148-155`); S re-ran the REAL
  automatic compaction — PASS with the predicate
  (`field=turn_m13 index=turn_m13 first-record-count=13
  preserved-first-copies=6`), FAIL without it (the regression
  reproduced: `field=turn_m12 ... first-record-count=19`), the oracle a
  real `transcriptindex.Open(...).Latest(1)`; S also triggered the
  conditional `rewriteTail` verbatim arm twice
  (`agent/session_compaction.go:386-393`, the ordinal set at
  `agent/session_compaction_ask.go:20-36`) — field and index agreed
  both times. The PRESERVED-TAIL journey's real seam is the
  compaction-replay harness
  (`agent/session_compaction_replay_transcript_test.go:48-170`).
- **R21-R-2 / S21-2 / T21-2** — CLOSED in mechanism. S's restored
  probe: with the seed `field="turn_m1" index="turn_m1"`, without it
  the field blank; both seed-policy interleavings finished
  `field=index=turn_m2` with REAL commits; reopening the older
  `turn_m1` after `turn_m2` preserved `field=index=turn_m2` (the
  reopen creates no new anchor). T verified no losing interleaving
  exists: the only commits at attach are the held-turn flushes, which
  join the PRELUDE
  (`agent/transcript/append_tail.go:118-125`) and can never carry an
  execution kind. The seed's data path is the T22-1 minor below.
- **R21-R-3** — CLOSED (R and T): the doors match the writer
  (`agent/transcript/transcript.go:589-603`, `:781-788`, `:812-846`,
  `:955-979`); no new door finding.
- **S21-1** — CLOSED (S, dynamic, the causal-control shape again).
- **S21-3 / T21-3** — CLOSED: S drove both external-before-attach and
  external-after-attach orders through the real setter and
  `reopenAttentionTranscriptDurably` (`nil external retains internal`,
  PASS), and real `WireTranscriptHistory` probes in both reattach
  orders; the composition-gap residue is the T22-2 minor below.
- **T21-4** — CLOSED (T): the rule's proof-test obligations are
  concrete — the permitted-lock table
  (`agent/session_envelope_sampling_test.go:159-165`), the
  classification test (`:201-249`) and the reflection-driven
  method-coverage test (`:285-294`) cover the new method
  automatically once the leaf's permitted entry lands; S ran both
  existing sampling proof tests with the prototype accessor added —
  passing.

## New findings — 1 significant, 4 minors; every load-bearing claim
verified in-tree by the chair

### S22-1 — significant, single seat, dynamically proved with a real
delegate spawn and a passing causal control

**Descendants bypass the frontier sampler: the child's metadata stays
blank after the signal is processed.** The child's field is correct
and its signal arrives, but the child's CACHED ROW never receives the
frontier:

1. `agent/session_events.go:505-506` forwards child events through
   `descendantEvent`;
2. production wiring sends them to `RecordDescendantAppEvent`, not
   `BridgeEvent` (`cmd/evener/serve_bridge.go:191-204` — descendants
   "do not have their own daemon or authoritative event consumer");
3. that route's own doc states it "deliberately does not sample or
   stamp the root session envelope... refreshFacets samples and
   installs only the root envelope"
   (`server/appwire_record_events.go:365-373`) — the facet machinery
   never runs for descendant events;
4. the child projector returns nil for unknown kinds
   (`internal/appprojector/appwire_projection.go:435-436`) — it has no
   first-record consumer — and its `SessionStart` arm constructs a
   separate cached child row (`:87-123`), installed at
   `appwire_record_events.go:428-445`;
5. metadata reads copy that cached row
   (`server/appwire_read_handlers.go:350-364`) while full-history
   reads independently obtain the anchor (`:233-242`).

S's dynamic proof (scratch-only overlays of the prescribed mechanism,
a REAL `delegate` tool call spawning the child, the child's first
assistant entry committed, the second provider call held, the test
waiting until the typed first-record event had RETURNED from
`RecordDescendantAppEvent`):

```text
listener-field="t_034tm9glZ..." count=1
full-history-frontier="t_034tm9glZ..."
metadata-frontier=""
root-control-field="t_034tm9gb..." root-metadata="t_034tm9gb..."
FAIL: descendant's committed frontier never reaches metadata
```

The passing causal control: adding ONLY the child-field-to-cached-row
handoff — sampling outside the projection gate, then committing the
field on the child row — made the unchanged assertions pass, including
under `-race -p 2`. This is an addressable Manager path
(`agent/session_tools_hub.go:731-746`,
`cmd/evener-hub/internal/appsource/local_daemon.go:1386-1415`), not an
invented child API. The chair verified every hop. The chair's own
gap, owned: the consumer-disposition text named job-watch fanout and
the bridge projector but never the `RecordDescendantAppEvent` route —
the descendant population is the fleet.

S's trigger corrections are NOT findings: an initial fixture holding
the child's first provider call had no execution entry yet (its
environment and preseeded user input in `turn_system`, correctly
excluded by the predicate; the preseed precedes launch at
`agent/delegate_create.go:258-687`). Honest reporting, recorded.

### T22-1 — minor — the seed's cited seam cannot read the validated view

`attachTranscript` receives only the writer
(`agent/session.go:2873-2895`); the Writer exposes no decoded-entries
accessor (its public surface is the append doors, `Header`,
`TrackFailures` as a seed input, `FailedToolCalls`,
`EstablishDurability`, `Poisoned`/`Closed`/`WhileHealthy`,
`DrainWarnings`, `Close`). The validated entries are the CALLER's
local (`agent/session_init.go:1015`, `:1023`), in scope at both attach
call sites (`:696`, `:1427`). The fix names the data path: the seed
receives the validated entries (attach gains the parameter) or runs at
the two attach call sites with the same only-if-empty guard.

### T22-2 — minor — the dispatcher's install site must be named, and
the field's direct writers composed

`s.transcriptRecorded` is written directly, never through the setter,
at `agent/session_init.go:505`, `:1144` and
`agent/session_conversation_provenance.go:226-230`, besides
`SetTranscriptRecordedFunc` (`agent/session_execution.go:241-246`).
The fix: `setTranscriptLocked` (`:248-258`) is the install site — it
installs the DISPATCHER, reading the field's current value as the
external half at that moment — so the direct writers compose too; a
dispatcher composed only by the setter would leave a descendant's
history hook in place but never install the internal listener.

### T22-3 — minor — the "excluded TWO ways" claim is scoped to the
compaction replay

The fork's prefix copy records parent entries through `PlaceVerbatim`
(`agent/fork.go:267-272`) with `OriginalOrdinal` nil and kinds
preserved — those copies WOULD satisfy the predicate; they are inert
today only because `fork.go` uses a bare, unhooked child writer
(`:249-293`). The caller survey stays load-bearing for that
nil-`OriginalOrdinal` verbatim class: a future `PlaceVerbatim` caller
under a live hook would regress the frontier. The spec's claim is
scoped to the compaction replay (whose copies carry `OriginalOrdinal`).

### T22-4 — minor — the IDLE-RESTART race clause is not
real-schedule constructible

No commit at attach can carry an execution kind (the held-turn flush
joins the prelude, `agent/session.go:2882-2886`,
`agent/transcript/append_tail.go:118-125`; `BeginExecution` is a
nil-writer no-op before attach). The guard is correct; the journey
pins it by pre-stamping the field or unit-calling the seed, and says
so.

## Disposition — one fix family plus four localizations, one pass

1. **THE DESCENDANT FRONTIER CONSUMER (S22-1).** The first-record
   kind gains a consumer on the `RecordDescendantAppEvent` route: on
   the typed kind, the handler samples the CHILD session's stamped
   field — under the amended sampling rule's lock order, through the
   child-frontier sampling seam installed at the same wiring point as
   `SetDescendantEventFunc`
   (`cmd/evener/serve_bridge.go:191-197`) — and installs the value on
   the cached child row (`projection.thread`, the same install point
   the `ThreadStartedParams` arm uses), OUTSIDE the projection gate as
   S's causal control proved. The route's own doc invariant is
   amended in step: the child-row install samples the CHILD's field,
   never the root envelope, so the root's no-race argument is
   untouched. The RESTORED-CHILD bootstrap rides the child's
   `SessionStart` row construction
   (`internal/appprojector/appwire_projection.go:87-123`): the
   child's attach-seeded value rides the `SessionStart`
   payload as an additive field and the row construction
   installs it from the payload (ADDENDUM, the correction
   named: the pass's first wording said the construction
   "samples the child's attach-seeded field", but the
   projector is a pure event consumer reading `event.Data`
   and cannot read the child session's field — the chair's
   own pass rule caught the missing carrier one hop short),
   because a root `facetAll` resample does not touch
   descendants. Journey 8
   gains the ADDRESSED-CHILD case (metadata and full-history compared
   after the signal is processed on the descendant route — red while
   the consumer is missing) and the IDLE RESTORED-CHILD case.
2. **THE FOUR LOCALIZATIONS (T22-1..T22-4)**: the seed's data path
   (the validated entries passed to the seed at the attach call
   sites); the dispatcher's install site
   (`setTranscriptLocked`, the external half read from the field at
   install); the two-ways claim scoped to the compaction replay with
   the caller survey load-bearing for the fork's nil-`OriginalOrdinal`
   verbatim class; the IDLE-RESTART clause restated as what it pins.

## Readiness

- Spec 1 — READY from all three seats (thirteenth consecutive round).
- Spec 2 — READY from all three seats (seventh consecutive round).
- Spec 3 — NOT READY: S22-1 leaves addressed descendant metadata
  frontierless.
- Spec 4 — READY from all three seats; the fence at zero findings.

Significant trajectory r11-r22: 7, 5, 4, 5, 7, 4, 2, 2, 3, 2, 2, 1.

## Scratch incident, recorded

Seat S's `s22/` probe directory vanished from the shared session
scratch mid-review. It was not the chair (the chair's only scratch
deletion was its own `r20pass.py`, removed before round-22 seat S was
spawned); S rebuilt its probe and removed its own scratch. If another
seat's scratch disappears, the incident gets root-caused before
anything else.

## Round 23

Same three seat families, new delegates: closure audits of their own
round-22 findings plus fresh attack on the pass's new text — the
descendant consumer against the projection gate and the child-row
install, the restored-child bootstrap at `SessionStart`, the seed's
data path at the attach call sites, the dispatcher's install site
against the direct field writers, the scoped two-ways claim, and the
new journey cases.
