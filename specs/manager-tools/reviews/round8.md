# Round 8 — reconciliation and disposition

Reviewed at commit `6116dbe979` (the post-spike re-entry), code base
`2be411cd8f`. Seats, all leaves, high effort, all three ran the spike's
red tests themselves:

- **R** `codex-jesse-fsck.com/gpt-5.6` (dlg_034tRj2jpN901xw2FdUSXE),
  run 12:14–13:04 (50 min, 34M cumulative tokens — the deepest single
  review of the process): 12 significant + 1 minor.
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m` (dlg_034tRj4wypYAMcgoBsBkto),
  run 12:14–12:23: 7 significant + 1 minor.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034tRj6OYxzg9UflpdJDws), run 12:14–12:28: 9 significant + 3
  minor.

Seat notes: the never-seated pool was exhausted after seven rounds, so
round 8 deliberately re-tread three families that each broke the fence
in a prior round — they were chosen as the sharpest judges of whether
their own findings are now closed, with the anti-re-report bar raised
in the brief. That judgment paid: every seat ran the reds and confirmed
all five reproduce for exactly the documented reasons, zero disputed,
and T independently verified the worktree code is byte-identical to
the base. The spike's evidence and core design survived all three
seats. No seat found a re-report worth filing — every finding is new.

Tally: **28 significant + 5 minor**, no spec ready. Every load-bearing
claim below was re-verified in-tree by me at the exact hop named.

## The convergence: the fence's evidence story (three seats)

Three seats independently found the same class: the settled design's
RESTORE half cannot be expressed by the evidence the system writes
today.

- **Durable granularity + sampling point (S1 = T1 = R8).** The
  captured identity is per call `(TurnID, CallID)`, but the durable
  answer names only executions: `AnswerToTurnIDs []string` "names the
  pending question executions" (`agent/schema/turn.go:429-431`);
  every ask in one turn stamps the same `TurnID`
  (`agent/session_tools_ask.go:156-159`; pinned by
  `TestAskUser_MultipleAsksOneRound`). Worse, the durable link is
  sampled at CONSUMPTION (`turn.AnswerToTurnIDs = s.pendingAskTurnIDs()`
  at `session_lifecycle.go:2331`), not at admission — an ask appended
  between admission and consumption is in the durable names but not in
  the captured clear list. Live and restore disagree on exactly the
  append case the fence exists for.
- **The clear-site inventory is incomplete (T2 ⊂ R9).** The design
  named two answer sites; the code has more whole-set clears reachable
  from answers: the steering-carrier entry clear
  (`session_lifecycle.go:1530–1575`, named by R), the
  already-incorporated replay branch (`:2218–2222`), and the recovered
  pre-append-failure branch (`:2303–2313`). Only `:864/:873` (accepted
  interrupt, close) are the stated exceptions.
- **Restore boundary kinds (S2 = R11).** `deriveRestoredAskPending`'s
  shortcut boundaries (assistant final, generic completion,
  round-resolves-everything) are round-granular, not captured-name-
  aware; and `TurnFailure` with `SteeringCarrier` is a third resolving
  kind whose writers (`session_events.go:276–292`,
  `session_queue.go:1141–1214`) record no answer names.
- **Constructor wiring is impossible as stated, and the named
  constructor does not exist (S3 = T5 = R12).**
  `NextBootGeneration` runs "after it owns the session"
  (`agent/schema/boot_generation.go:22–34`) — fresh/restore construct
  at `serve.go:725–765`, the mint at `:776–788`; Clear constructs at
  `:1104`, mints at `:1124` — so a config field cannot carry the
  minted value into construction. And `RestoreSessionFromMeta` is a
  test helper (`agent/session_queue_entrypoints_test.go:300`); the
  production entry is `RestoreSessionFromMetaWithConfig`
  (`agent/session_init.go:888`).
- **Queue transformations replace the record that owns the captured
  names (R10, new).** `AcceptClientMutationDrainAsSteerAs` creates a
  new drain record and marks each original queued record terminal,
  clearing its payload (`removeQueuedMutationSource`, `:903/:1028`);
  promote has the same replacement shape. The fence's captured set must
  be copied or combined into the replacement record explicitly.

## Seat-unique findings, all verified

- **R1 (spec 1):** `liveEntries` is not "the routable set every live
  route already reads" — `SessionToolsThread` iterates raw
  `s.entries()` for binding lookup and `RunSessionTools` publishes
  routes from raw entries
  (`cmd/evener-hub/internal/appsource/local_daemon.go:218–228,280–286`).
  A crashed entry stays routable for session-tool binding and route
  publication after the proposed single filter. Spec 1's "no per-route
  checks to forget" claim is false.
- **R2 (spec 1):** explicit reads FAIL WHOLE today when a named source
  errors (`app_threadlist.go:183–190` returns the error). The
  spec's `attempted-failed`-for-explicit-sources silently migrates a
  fail-whole contract to partial success; the mixed named-source case
  is undefined.
- **R3 (spec 2):** user-authored steering has emitted type `steering`
  with `Source == "user"` and IS conversation text
  (`appwire/history_content.go:65–73`); the exact-type predicate
  `messages = agentMessage + userMessage` drops it while promising
  "visible user text".
- **R4 (spec 2):** the fetch's REQUEST grammar is absent — which
  Manager argument submits a `fetchCursor`, how it composes with
  `turnWindow`/filters/bounds, which AppWire params receive it, and
  the validation errors. The closed tool edge
  (`managerReadDefinition`, `invokeHubRead`) maps only today's shapes.
- **R5 (spec 2) = T9, deeper:** overlay items mutate in place
  (`internal/appoverlay/overlay.go:489–506`) with `Version` an entry
  ordinal and `0` meaning "no history version" — a cursor `(position,
  0)` stays valid while bytes change, including between ranges.
- **R6 (spec 3):** the older-client/new-hub direction is NOT safe for
  cached deferred pages: the deployed client's validator is exact-key
  (`hasOnlyKeys`, `transcriptValidators.ts:291–293`) with
  `ThreadTurnsListResponse` allowlisted to exactly
  `data,nextCursor,bootGeneration,epoch,snapshot,authoritative`
  (`types.gen.ts:5774`); new serving-fact fields fail
  `validDeferredPage`, and `decodeCurrentTranscriptState` rejects the
  whole cached state. A new hub poisons an already-deployed old
  client's cache. The migration needs an actual delivery/gating
  strategy.
- **S4 (spec 4):** pending asks accumulate ACROSS rounds — a
  human-note carrier's round can post its own ask without answering an
  earlier one (`deriveRestoredAskPending`'s own doc comment; pinned by
  `TestAskUser_RestoreAccumulatesPendingQuestionsAcrossNonResolvingRounds`,
  run by S). The sufficiency rule's single-turn fetch cannot surface a
  multi-execution set.
- **S5 (spec 3):** the retain-on-fallback row is unreachable without
  touching the identity machinery the spec freezes: `compareBootGeneration`
  REPLACES on numeric↔daemonless switches (`bootGeneration.ts:17–36`)
  and `identityDisposition` applies it before any authoritative
  consideration (`transcriptHistory.ts:770–779`) — the failed-live
  fallback (BootGeneration daemonless) replaces a held live history
  regardless of the flag.
- **S6 (spec 3) = R7:** the truth table has no row for a new client
  against an OLD hub (no `servedFrom` exists on old responses); the
  product compatibility direction requires it.
- **S7 (spec 2):** our round-7 envelope ceiling is arithmetically
  false — scope 6 requires a per-item `fetchCursor` for every
  selected-but-elided item; 40 × ~164 base64 bytes = 6,560 > the
  asserted 4KiB. The one-cursor fixture cannot prove the guarantee.
- **T3 (spec 4):** our re-entry overclaimed — spec 4 says the broker
  trigger "is DESIGNED, RED-TESTED AND SETTLED by the fence spike"
  while `fence-spike.md:293` says "unchanged by this spike". The
  trigger needs its own design, landing zones, and red.
- **T4 (README/spec 4):** stale conditionals — rule 6 and spec 4's
  acceptance still describe the pre-re-entry state; the fence
  machinery is now spec 4's own scope and its assertions are
  unconditional.
- **T7 (spec 3):** our R3-fix named "the session's in-memory history"
  as the frontier source — the projection's own doc forbids exactly
  that ("every live-session value comes from the materialized
  envelope... It must stay that way"; direct reads block the response
  cut). The source must be an owned ENVELOPE field stamped by the
  bridge.
- **T8 (spec 1):** `EvenerThread` already carries
  `activeTurnId`/`activeTurnStartedAt` (`types_thread.go:91,161`);
  `threadFromEntry` simply does not populate them. Minting
  `currentTurnId`/`turnStartedAt` duplicates keys for one fact and
  violates the spec's own no-duplication rule. Populate the existing
  fields, spec-3-style.
- **T6 (spec 4) = S8, graded significant:** canonical restores keep
  both ids (`questionsFromAskCalls`; installed via
  `setAskPendingLocked`); only LEGACY sets are call-less. The recovery
  sentence would suppress the composite identity after every restart.
- **T10/T11 (fence-spike, minor):** the capture-placement sentence
  contradicts itself (only "before entering the store transaction" is
  safe — the serializer never waits on `s.mu`); "one common prepare"
  is three call sites, and its read must be scoped to PAIR-BEARING
  requests only.
- **R13 (spec 4, minor):** "no new durable state" in the recovery
  section contradicts the spike's durable
  `clientMutationRecord.Preconditions` additions — an implementer
  could skip the journal schema/replay compatibility work.

## What survived (all three seats)

The spike's five reds reproduce exactly; the four characterizations
pass as pinned. The settled design's core is sound in all three
verdicts: the shared `acceptClientMutation` admission transaction, the
synchronous typed refusal through `rejectClientMutation` (T traced it
end to end: "the receipt is the refusal" is substantively true), the
empty-set rule (defensive — `setAskPendingLocked` bumps the revision
on clear, so a matching pair cannot name an empty set), the scoped
clear's benign capture-vs-commit append behavior, the common-prepare
direction, the composite identity. What broke is the evidence and
inventory AROUND the core — the parts the spike's own "did not verify"
section predicted.

## Disposition — one revision pass, no second spike

The round-6 escalation rule fired on serial DESIGN breaks; round 8's
findings are a different class: verified static facts (type shapes,
validator key lists, call-site inventories, record transformations,
mint ordering) with named fixes, plus errors in our own round-7/8
revision text. A red-test spike cannot adjudicate these better than
the three seats' exact citations already have. The core the spike
red-tested survived all three seats. One careful pass, then round 9.

The fence's evidence redesign, decided by the convergence:

1. **The admission-time captured composites are the SOLE resolution
   evidence**, riding TWO durable carriers written from the same
   capture: the journal record's preconditions (for the scoped clear)
   and a new additive `schema.Turn` field naming the resolved calls
   (for restore) — written by the consumption path from the record's
   captured list, never re-sampled from live state. The failure
   boundary writers (`TurnFailure` + `SteeringCarrier`) write the same
   field from their records. This one mechanism closes granularity,
   the sampling point, and the failure boundaries.
2. **Every answer-reachable clear routes through the scoped
   primitive**: the two designed sites plus the carrier-entry clear,
   the incorporated-replay branch, and the recovered-failure branch;
   only accepted-interrupt and close keep the whole-set clear.
3. **Drain and promote copy/combine the captured set and pair
   provenance into the replacement record** (entries admitted against
   different sets union their captured lists).
4. **Restore's shortcut boundaries become scope-aware**: a boundary
   fires only when the captured names are exhausted; rounds that found
   asks but did not resolve them keep scanning (the existing
   accumulate-across-rounds behavior, preserved).
5. **The boot-generation carrier lands as a post-construction
   initialization step** on the owned session (before the first
   admission or projection), covering fresh, restore
   (`RestoreSessionFromMetaWithConfig`), Clear, and delegate-restore
   paths — the mint's ownership prerequisite respected, the constructor
   name corrected.
6. **The broker's pending-set trigger is designed in spec 4 itself**:
   the pair on `AttentionEntry` and the frame, the comparison keys
   extended, with its own red; fence-spike.md's "unchanged by this
   spike" line stays true and spec 4 owns the design.
7. **The sufficiency rule gains the full-set fetch**: the frame
   carries the composite list (bounded, with the count), and a
   count-mismatch directs a named continuation until the fetched
   questions cover the frame's set.

Per-spec fix lists:

- **Spec 1:** name ONE eligibility function consumed by list RPCs,
  session-tool binding, and route publication (R1); preserve
  explicit-read fail-whole and scope `attempted-failed` to the implicit
  fan-out, with the mixed named-source case defined (R2); populate the
  EXISTING `activeTurnId`/`activeTurnStartedAt` wire fields and drop
  the minted names (T8).
- **Spec 2:** `messages` becomes source-aware — `agentMessage`,
  `userMessage`, and user-source `steering` (R3); the fetch REQUEST
  grammar specified end to end (R4); overlay/version-0 items are not
  fetchable until settled — no `fetchCursor` while `Version` is 0
  (R5/T9); the envelope ceiling re-proven on the max-case fixture (40
  elided items with cursors) with the clamp margin measured on it
  (S7).
- **Spec 3:** the frontier's source is an owned ENVELOPE field
  stamped by the projection bridge (T7); the serving facts ride the
  wire only for requests that opt in (an additive params flag the new
  clients send) so deployed strict validators never see unknown keys —
  old hubs then answer without the facts and the table gains its
  new-client/old-hub row: absent facts, today's authoritative-keyed
  behavior (R6+S6/R7); the retain-on-fallback row reconciled with the
  identity machinery — the non-goal lifts for fallback-marked
  responses (the retained-fallback serve is exempt from
  identity-level replacement) or the row narrows to the succeeded-empty
  seam (S5) — decided in the pass with the client code open.
- **Spec 4 + fence-spike:** the evidence redesign above; the trigger
  design (T3); conditionals unstaled — the fence machinery is spec 4's
  own scope, one lane, unconditional assertions (T4); the recovery
  section rewritten (legacy-only call-less restore; the new durable
  preconditions named beside the derived ask state) (T6+R13);
  capture-placement and prepare-scoping cleanups (T10/T11); the
  red-test inheritance list extended with a red per round-8 fence
  finding (same-turn composite restore, post-admission append,
  carrier/replay/failure clears, drain/promote copying, failure-writer
  names, the trigger, the wiring initialization).

README: round-8 trail; rule 6 and the spike section unstaled.

## Readiness after this round

No spec is ready. Spec 1 is closest (two grounding/compatibility fixes
plus the field-naming repoint). The fence's core is settled; its
evidence story is now designed once, properly, from the three seats'
convergence rather than from imagination.
