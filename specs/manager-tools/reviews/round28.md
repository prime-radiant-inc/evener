# Round 28 — three-seat reconciliation (record of decision) — CLEAN

Seats: R gpt-5.6 (`dlg_034tt4RXumvvJZG3atGFZP`), S gpt-6.1-sol-1m
(`dlg_034tt6cKgePZTH0XhsafG4`, "par-r28-reviewer-s2"), T
deepseek-4.1-flash-background (`dlg_034tt6gUKBsjVUvIAtDuBt`).
Reviewed at `c50f50aee9` (the round-27 revision pass, on top of
`87f8619df6` the round-27 record) over the production-identical base
`2be411cd8f`. All three seats ran the required reds uncached
(`-count=1 -v`, bare `|`, sources inspected first): 5/5 red families
reproduced for their documented reasons, 4/4 characterizations
passing, zero disputed, worktrees clean, owned scratch removed. R
re-ran the two sampling proof tests and the compaction-replay harness
— PASS.

Spawn incidents, noted for the record's integrity: seat S was first
spawned on the wrong model with a placeholder prompt
(`dlg_034tt4TRrlNTt6C8C3rTQ5`); it was stopped within seconds,
reported "no actionable work", and did nothing — no report from it
enters this record. A late re-feed of the round-27 R report arrived
mid-round and was ignored per the standing procedure (its reviewed
commit, `76f6d5949a`, is round 27's already-reconciled target). One
probe-design relay: T's walk found the capacity reclamation loop's
test-only close hook (`agent/delegate_tree_reclaim.go:467-470`)
bypasses the funnel when set; the chair verified it and relayed it
before S built its capacity probe, so S left the hook unset and drove
the real funnel.

## Closures

- **R27-R-1 — CLOSED (all three seats), closed by construction and
  dynamically.** The pass names the pre-teardown release at the
  funnel (`teardownChildSessionWithPolicy`,
  `agent/subagents.go:205-236`, the release before
  `releaseRuntime` at `:217`) and every path reaches it. R's closure
  audit supplies the structural argument, chair-verified at every
  hop: `releaseRuntime` has exactly two production callers — the
  funnel (`agent/subagents.go:217`) and the session's own terminal
  helper `close` (`agent/session_close.go:67`, reached only from
  top-level `Session.Close`/`CloseForShutdown` at `:25`/`:32`) — so
  no child teardown can bypass the funnel and the writer set is
  closed by construction. T verified the four named paths funnel and
  found three more sites the parenthetical list omits — capacity
  reclamation's teardown (`agent/delegate_tree_reclaim.go:470`), the
  reclaimed runtime's manager children (`:771`), and
  `disposeUnadoptedSubagentSession`'s teardown
  (`agent/subagents.go:343`) — all through the same funnel, so
  "every ordinary teardown path funnels through it" holds beyond the
  listed four. The round-27 measurement is an entry-lifetime defect
  no more: the same release that measured
  `entriesAfterExactRuntimeRelease=0` now runs at the named site.
- **S27-S-1 — CLOSED (S, dynamic; the round's mechanism of
  record).** S rebuilt the round-27 transport in a fresh 20-file
  scratch overlay, added the funnel release, and drove real
  lifecycle entrypoints under `-race -p 2` (final job
  `job_034tt6cKgeaaUk7GXOhWUp_P3ps9Q7CCWtk`, exit 0, no race
  warnings; the exact replay source retained at
  `job_034tt6cKgeaaUk7GXOhWUp_db0LIcTIkbYD`, independently
  reconstructed byte-identical, files=20):
  - **Ordinary close, both identity-replacement orderings:**
    `entriesAfterRootClose=0 removals=1 releaseCalls=2
    heldCallbackInstallDelta=0 dropsDelta=1`; the no-release control
    retained the entry, and close-before-replacement reproduced the
    behavioral red (`heldCallbackInstallDelta=1 dropsDelta=0`).
  - **Double release:** both real reclamation paths and the
    constructed reverse order printed `releaseCalls=2 removals=1
    entriesAfterDoubleCall=0 dropsAfterCallback=1
    thirdReleaseNoOp=true`; capacity left the test-only close hook
    unset, so the real funnel ran.
  - **Actual worktree disposal:** the dirty-lane attempt reached the
    exact `uncommitted changes` refusal, cleared the armed gate, and
    performed no release; the successful retry reached real worktree
    destruction with `gateAtRelease=true successGateConsumed=true
    entriesAfterDispose=0 releaseCalls=1 removals=1
    callbackInstallDelta=0 dropsDelta=1`; the no-release control
    completed disposal but printed the red (`entriesAfterDispose=1
    callbackInstallDelta=1 dropsDelta=0`). Journey 8's red/green
    pins match the measurement.
  - **Retirement then restore:** the real non-terminal child-release
    method removed the entry while the durable aggregate remained
    byte-identical; a callback released before restore met a MISS; a
    genuine callback held after its sample through the cold
    constructor's replacement registration printed
    `heldIdentity=1 replacementIdentity=2 oldCallbackDrops=1
    oldCallbackInstallDelta=0 newFrontier="turn_m901"`;
    generation-present (`"1"`/`"2"`) and no-mint (`""`/`""`) cases
    both passed; A's retained publish and release closures both
    preserved B (`oldPublishPreservedB=true oldReleasePreservedB=true`).
  - **Nil closures:** the actual superseded-root factory branch
    returned zero-value hooks and the child printed
    `registryEntries=0 MISSdrops=1 releaseCalls=0` — agreeing with
    the nil rule and the generic MISS consequence.
  - **Root control:** unchanged (`rootFactoryCalls=0
    registryEntries=0 rootFrontier="turn_m1"`).
  Scope limits stated by S: the emitter retains the probe-only
  asynchronous shape, not the full managed-emitter implementation;
  restore probes use the real cold constructor, not a daemon
  reboot.
- **T27-1..T-4 — CLOSED (T's own audit, chair-verified at the
  hops).** The disarm names the flag flip at constructor success
  (spec `:832-838`, the adjacent paragraph citing the returns at
  `:721`/`:1594` one line away); the nil rule is stated with both
  consumers (`:935-939`, `server/appwire_histories.go:49-60`
  chair-verified); the funnel is named with its four paths
  (`:866-873`); the publish fence is stated (`:801-804`). The
  idempotence claims hold under the double call: removal is
  identity-gated and the runtime release is `closeOnce`-guarded
  (`agent/session_close.go:48`), so the second release no-ops on a
  MISS or a replacement's entry.
- **R's round-22 positives — ALL FIVE CONFIRMED** (dispatcher
  wiring, emitter lifecycle, sampling proof coverage,
  compaction-replay harness, doors text). R re-ran the four named
  proof tests uncached — PASS. Spec 1 READY from R for the
  nineteenth consecutive round.

## Findings — ZERO significant; three distinct minors

Round 28 is the set's first CLEAN round: no seat reported a
significant finding, and every closure the round asked for came back
verified. The three minors are one-clause text folds, all
chair-verified at the exact lines.

- **T28-T-1 — the release's landing site is stated two ways.** The
  round-26 paragraph at `:960-975` still says the release lands at
  `unhookReclaimedRuntimeRecord` — singular, unamended — while the
  round-27 paragraph at `:862-891` names the funnel as the writer
  the set was missing. A reader auditing the writer set from the
  older sentence alone sees one landing site. The mechanism is
  closed; the fold amends the older sentence in place, the same
  treatment the round-26 record's own claims received.
- **T28-T-2 — the nil-closure rule's row consequence is unstated.**
  `:935-939` says nil means no registration and no release and that
  the two consumers stay coherent, but never says what the design
  then does with a superseded root's descendants: no entry, so
  every first-record install takes the lookup MISS and drops — the
  correct behavior (their rows are dead projections, matching
  today's replaced-tree contract at
  `server/appwire_histories.go:72-82`, chair-verified: "a replaced
  tree's records go nowhere"), not a new silent loss. The same
  sentence should also state the publish closure's absent-entry arm
  is a no-op.
- **R28-R-1 — three-seat converged in fact: the "never a matching
  current one" clause is broader than the demonstrated behavior.**
  `:996-998` says a racing callback "meets a released entry (a
  MISS), never a matching current one." That is true for a callback
  that already carries A across a release, and for one that
  resolves in the emptied interval. It is false for the
  explicitly-permitted cold restore: a callback that begins route
  processing only after a replacement registers resolves the
  replacement's current entry, carries it, samples the
  replacement, and may idempotently install the replacement's own
  frontier. S measured the schedule through the real seams: an old
  callback paused before entering the descendant route (the
  reachable delay at `agent/session_events.go:501-506`,
  chair-verified: `eventsMu` releases at `:501` and the descendant
  callback fires at `:505-506` outside it), resumed after B
  registered and recorded, printed
  `oldCallbackMatchedCurrent=true installDelta=1 dropsDelta=0
  rowFrontier="turn_m902" regression=false` — a truthful B
  resample, not an A write, not a row regression. T's independent
  retirement walk reached the same behavior and judged it coherent
  without flagging the sentence; R graded the sentence minor (the
  fence's safety property holds); S declined to inflate it into a
  finding and supplied the measurement. All three gradings
  recorded; the chair folds it as a minor accuracy correction: the
  guaranteed property is that a callback already carrying a
  released runtime's entry can never install it — not that every
  late callback drops.

## Disposition — one pass, three folds

1. **THE LANDING-SITE SENTENCE (T28-T-1).** The round-26 sentence
   at `:960-975` amended in place: the release lands at the two
   sites that own a runtime's end — the reclamation unhook and the
   ordinary-teardown funnel release above — the SAME idempotent
   operation in both orders.
2. **THE NIL CONSEQUENCE (T28-T-2).** The nil-rule sentence gains
   the row consequence: a superseded root's descendants take the
   lookup MISS and drop (dead projections, today's replaced-tree
   contract), and the publish closure's absent-entry arm is a
   no-op.
3. **THE THREE-OUTCOME SENTENCE (R28-R-1 = S's before-route
   qualification).** The racing-callback clause restated with its
   three outcomes: a callback already carrying the released
   runtime's entry fails the comparison and drops; a callback
   resolving in the emptied interval meets a MISS and drops; a
   callback beginning route processing only after a replacement's
   fresh registration resolves and carries that replacement and may
   install only the replacement's own frontier. The guaranteed
   property named: a callback already carrying a released
   runtime's entry can never install it — not that every late
   callback drops. Journey 8 gains the BEFORE-ROUTE RESAMPLE case
   pinning S's measured schedule (red if the retired runtime's
   value lands or the row regresses).
4. README trail twenty-eight; fence-spike twentieth re-audit note.

## Readiness

- Spec 1 — READY from all three seats (R: nineteenth consecutive).
- Spec 2 — READY from all three seats.
- Spec 3 — READY from all three seats — the first round in which
  every seat marks spec 3 READY.
- Spec 4 — READY from all three seats.

## Verdict

**CLEAN.** Zero significant findings from any seat; every closure
the round asked for came back verified — the round-27 significant
closed by construction (the two-caller funnel argument) and
dynamically (the 20-file overlay through the real lifecycle
entrypoints, with no-release controls reproducing each red).
Significant-finding trajectory, rounds 11-28:
7, 5, 4, 5, 7, 4, 2, 2, 3, 2, 2, 1, 3, 1, 2, 2, 1, 0.

The clean-round gate is met: the implementer lanes launch next (one
lane per spec, the fence spike's red corpus as spec 4's starting
tests), per the standing plan. No issue filing.
