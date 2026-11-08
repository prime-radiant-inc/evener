# Round 17 — reconciliation and disposition

Reviewed at commit `5f6198d6b1` (the round-16 revision pass `8997482176`
plus its addendum), code base `2be411cd8f`. Seats — the same three
families, a tenth pass, each auditing closure of its OWN round-16
findings before fresh attack on the round-16 revision's own text, all
leaves, high effort; all three ran the reds uncached with `-count=1 -v`
(every red failed for exactly the documented reason across all three
seats, zero disputed, all four characterizations passing — three
independent confirmations, the eleventh consecutive clean red audit):

- **R** `codex-jesse-fsck.com/gpt-5.6`
  (dlg_034tgTU7h31ahPmcnjjna6, run 22:15-22:40): 2 significant, no
  minors; R16-R-1/R-2/R-4 CLOSED, R16-R-3 **PARTIAL** (its
  stabilization face — R17-R-2 is the reason, not a second count).
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034tgTckNHoe54rZp1gvIi, run 22:15-22:26): 1 significant, no
  minors; S16-1 CLOSED for its reported schedule, with dynamic proofs
  through the real content, resolution, wrapper-synced, parent-steer
  and stabilization seams — the round's decisive evidence again.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034tgTi2ym13rhU0MgZCPF, run 22:15-22:27): 1 significant + 2
  minors; 16-T-1..T-5 ALL CLOSED, including its own compile-verified
  re-run of the mechanical comparison sweep (the twelve contexts and
  their exclusions held exactly).

Tally: **2 distinct significant + 2 minor. The count went DOWN again,
4 to 2, and the round's largest finding is a second consecutive
THREE-seat convergence** (S17-1 = T17-1 = R17-R-2). Every load-bearing
claim was re-verified in-tree by me at the exact hop named, including
the pair helper's adoption contract (its own comment, verbatim: "the
turn is adopted here (never re-appended, which would duplicate it on
restart) and the next fsync settles the debt; Returning nil for the
adopted case is what keeps an owner from discarding its state"),
the buffered/durable doors' retained-value discard, the placement
freshness consumption, the user-input emission's false in-code
durability comment, and the stabilization's no-Record recovery.

## The honest shape of this round

Both significants are again the previous pass's own text, each one hop
past the fix that closed round 16 — and they are the SAME hop from two
directions: the pass generalized the CAPTURE of the owed obligation to
every recorded outcome, but not its DISCHARGE. Round 16 taught that
`Record` has a third outcome (recorded-but-unsynced, "and the caller
adopts the record"); round 17 taught what "the caller adopts" means
downstream: the pair helper's adoption contract DELIBERATELY converts
the retained error to success (by its own documented reasoning —
re-appending would duplicate the line on restart), so the wrapper's
producers never retry and never reach the two attention reconcilers the
pass named; the stabilization writer's own successful recovery runs
EstablishDurability and verification with no Record at all; and the
outer mode rows still carry a round-11 durability premise ("already
durable when the row fires") that the third outcome falsifies — the
user-input emission site's own code comment states the false premise
in-tree. The population closure from rounds 14-16 HELD: all three
seats re-walked the producer list and the caller survey with no
escape. The mechanical-sweep rule HELD: all three seats re-ran the
comparison grep themselves, T compile-verified it with a scratch slice
field, and the twelve contexts plus named exclusions survived every
sweep. No seat found a double-emission schedule.

## The findings, verified

- **S17-1 = T17-1 = R17-R-2** (significant, spec 3, three-seat
  convergence with S's dynamic proof through the real pair-helper and
  stabilization methods): the owed obligation is captured at every
  producer but discharged at only two sites, both attention fold
  paths — the content retry's existing-content branch
  (`agent/session_attention.go:604-614`) and the resolver's durable
  reopen (`:1771-1795`). The wrapper's producers never reach either:
  every synced owner — user input
  (`agent/session_lifecycle.go:2153-2159`), daemon steering
  (`agent/session_queue.go:1262-1270`), delegate steering
  (`agent/delegate_tree_steer.go:403-412`), the skill carrier, the
  client-mutation recovery, the delegate seed — writes through the
  pair helper `appendTurnAfterTranscriptWriteLocked`
  (`agent/session.go:2567-2588`), whose own contract treats
  `ErrRetainedUnsynced` as recorded-and-adopted and returns nil, so
  no retry ever arrives; a later successful synced write settles the
  file's debt but is not fresh (`agent/transcript/placement.go:73-77`
  consumes the flip at the first record,
  `agent/transcript/transcript.go:854-855` commits the tail) and is
  not a named discharge site — the signal emits ZERO times after
  successful recovery. The stabilization writer's successful retry
  (`agent/session_attention.go:1894-1903`) runs
  `EstablishDurability` and verification with no Record, never
  reaching the resolver reopen; the pinned
  `TestDelegateAttention_StabilizationRetryPreservesFailedBarrier`
  (`agent/session_attention_test.go:909`) proves the schedule
  executable. Verified by me at every hop, including the helper's
  comment and the tail commit. T's secondary, also verified: at the
  buffered/durable doors the retained fact returns as a SEPARATE
  value (`agent/transcript/transcript.go:796`) that `Record`
  discards (`agent/transcript/record.go:61-63`), so at those doors a
  retained line reports `Recorded=true` with nil — the pass's "with
  OR without an error" cannot be evaluated from the outcome there.
  Disposition 1.
- **R17-R-1** (significant, spec 3, R's own walk past every seat's
  attack list): the four synchronous frontier rows carry a durability
  premise the third outcome falsifies — spec text at
  `3-transcript-status-consistency.md:197-199` ("the user-input
  execution's own turn is already durable when the row fires") and
  the in-code comment at `agent/session_lifecycle.go:2350-2351`
  ("The direct user turn is durable (or was durably preseeded) at
  this point") are both false when the synced door returns
  recorded-but-retained: the pair helper adopts, returns nil, and
  `EventUserInput` fires at `:2352-2361` with the frontier sampling
  an entry that is not yet durable — violating the write-then-emit
  rule the row set itself states. The wrapper's new outcome handler
  can hold the first-record signal but cannot suppress the outer
  row. The same premise serves `EventSteeringInjected`'s two paths
  and `EventAssistantTextEnd`; the goal row was already moved to
  write-then-emit by an earlier pass. Verified by me at the emission
  site, the premise text, and the door. Disposition 2.
- **T17-2** (minor, spec 3): the mismatch-path replacement rationale
  "content-MISSING retries the record and the fresh flip comes again"
  is false at the placement layer — the first record already consumed
  freshness, and because the FILE keeps the entry the retry's fold
  takes the existing-content branch rather than re-recording, so the
  mismatch sub-case loses the signal too. T rates it minor because
  the mismatch path is defensive/test-seam reachable. Verified by me
  at `agent/transcript/transcript.go:854-855` and the fold branch.
  Disposition 3.
- **T17-3** (minor, spec 4): the addendum's coalescing survival
  covers only the PREVIOUS frame's recovery line — three faces, all
  verified against the merger I read myself: when the previous frame
  was clean and the MERGER's own bound drops entries, the merged
  frame carries a dropped count with NO recovery line, contradicting
  "stamped together"; when both segments truncated at construction
  the arm's append duplicates the identical instruction line; and
  "the refs list the same sum" cannot hold past the marker's own
  bound, so the sum needs the bound qualifier. Disposition 4.

Also verified and NOT findings: the roster coalescing survival held
under R's and S's independent walks (both recovery lines CAN coexist
coherently — a pending genuine-resync frame's marker arm and a
current truncation line serve different true conditions; unknown
lines do not break `rosterFrameField`); the recovery ladder held at
every seat (the truncation site has the changed entries and refs in
hand at `agent/job_watch_hub.go:154-231`, the per-lane rung is bounded
at any fleet size, spec 1's cap boundary is quoted faithfully); the
merger SUM is computable through the line surgery, over-count safe;
the resync replacement frame itself carries no payload
(`agent/job_watch_hub.go:212`), so its fresh count comes from the
subsequent initial — no loss beyond the bounded re-truncation already
named; the whitelist enumeration held under all three seats' own
greps, T's compile verification included; no double-emission schedule
exists in the specified paths (the flip is consumed once, the
attention retries return before the normal emission); the content
writer can implement the outcome rule (`rec` in scope at its error
exit); the resolver reopen is a real reconciliation site for
resolution facts (it verifies disposition and resume generation, the
right fact for resolution turns — content equality is the content
path's fact).

## Disposition — one revision pass, then round 18

1. **The discharge generalized to the durability-completion boundary
   at every producer (S17-1 = T17-1 = R17-R-2).** Capture without
   discharge is a held id that never fires. The outcome gains the
   DURABILITY FACT as its own field at every door — today the
   buffered/durable doors return the retained fact as a separate
   value `Record` discards, and the handler must never key on the
   error's presence — so every producer's handler reads recorded,
   durable-or-retained, from the outcome itself. The owed id
   discharges where durability COMPLETES for the held entry: the
   wrapper's next successful synced outcome through the same writer
   (the synced barrier settles the WHOLE file, so a successful synced
   Record proves every earlier line durable — the pair helper's own
   "the next fsync settles the debt" made this schedule visible);
   the stabilization retry's `EstablishDurability` plus verification
   site; the attention fold sites as already named. Journey 8 gains
   the wrapper sibling (a direct-input retained first entry: no
   premature signal, exactly one at the next successful synced write,
   no duplicate) and the stabilization sibling (the pinned
   barrier-retry test named as the existing seam evidence).
2. **The synchronous rows' durability premise restated for the third
   outcome (R17-R-1).** The rows keep write-then-emit; the premise
   becomes "already durable when the row fires, OR recorded-and-adopted
   on the retained outcome" — the row still fires (the input is
   recorded and visible; the client echo is not frozen by a rare
   fsync failure) but its FRONTIER FACET does not advance on a
   retained outcome: the envelope keeps naming the durable frontier,
   the event payload carries the outcome's durability fact, and the
   facet keys on it. The discharge then emits the first-record
   signal, whose own facet row carries the frontier facet and
   advances the frontier to the now-durable entry — the signal is
   the closure backstop including for the rows' retained edge, the
   role the row set already assigns it. All four mode rows are
   restated at once (the steering paths and the assistant row share
   the premise); the goal row's moved emission needed no change.
   The in-code false comment is named as the premise's artifact.
3. **The mismatch path holds the owed id too (T17-2).** The
   read-back mismatch is a recorded-but-unverified outcome like the
   failed read-back: the owed id is held, and the retry's
   existing-content branch reconciles it after durable verification.
   The content-DIFFERS sub-case still fails typed BEFORE any record
   and holds nothing. The "fresh flip comes again" claim is withdrawn
   with its placement evidence (the flip is consumed at the first
   record; the file keeps the entry, so the retry takes the
   existing-content branch, never re-recording).
4. **The stamped-together rule at every site that writes the surface
   (T17-3).** The merger stamps the recovery line whenever IT drops
   entries — its own truncation, not only the carry from a previous
   frame; the carry arm appends the previous frame's line only when
   the current frame does not already carry one (identical text,
   presence check, no duplicate); and the refs list carries "the
   same sum, bounded by the marker's own bound," with the count
   naming the total when the sum overflows it — the bound was already
   the rule for the count, and the refs list now says so.

README: round-17 trail (seventeen; the count 4 to 2; the second
consecutive three-seat convergence; R's row-premise find; spec 1
READY the eighth consecutive round, spec 2 the second; the
mechanical-sweep rule's first test passed — all three seats re-ran
the grep and the list held). fence-spike: a ninth re-audit note (zero
fence findings from any seat). Round 18: the same three seats,
closure audits of their own round-17 findings plus fresh attack on
the new pass text — first target again.

## Readiness after this round

Spec 1 READY (all three seats, eighth consecutive). Spec 2 READY (all
three, second consecutive). Spec 3 NOT READY (all three seats: the
discharge hole and the row premise). Spec 4 READY on significants
from all three seats, with T17-3 the one verified minor, fixed in
this pass. Counts across rounds 11-17: 7, 5, 4, 5, 7, 4, 2. The
fence holds at zero findings across nine re-audits.
