# Round 18 — reconciliation and disposition

Reviewed at commit `e84edde38d` (the round-17 revision pass), code base
`2be411cd8f`. Seats — the same three families, an eleventh pass, each
auditing closure of its OWN round-17 findings before fresh attack on the
round-17 revision's own text, all leaves, high effort; all three ran the
reds uncached with `-count=1 -v` (every red failed for exactly the
documented reason, zero disputed, all four characterizations passing —
three independent confirmations, the twelfth consecutive clean red audit):

- **R** `codex-jesse-fsck.com/gpt-5.6`
  (dlg_034thctNdk7P8TipvPcvbA, run 23:02-23:13): 1 significant, no
  minors; R17-R-1 CLOSED, R17-R-2 **PARTIAL** (the synced-only
  discharge).
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034thd16YsnxAyDPtWOSAq, run 23:02-23:13): 2 significant, no
  minors; S17-1 CLOSED for its reported schedules, with seven
  dynamic probes through the real seams — including a probe driving
  the REAL `appendAssistantTurn` durable path and a REAL
  `transcriptindex.Open(...).Latest(40)` read against a retained
  first entry.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034thd5cuRySo3GeVmJM75, run 23:02-23:09): 1 significant + 4
  minors; T17-1 CLOSED, T17-2 **PARTIAL** (the differs-at-read-back
  sub-case), T17-3 CLOSED — plus the pinned writer tests run as
  extra dynamic evidence.

Tally: **2 distinct significant + 2 distinct minor. The count HELD at
2 (r11-r18: 7, 5, 4, 5, 7, 4, 2, 2) — but the character changed: both
significants are seat convergences attacking the SAME two round-17
decisions, and both are the pass's own errors.** Every load-bearing
claim was re-verified in-tree by me at the exact hop named, including
the recorded-length publication for retained lines, the assistant
response's durable door, Close's sync contract, and the buffered
door's interval skip.

## The honest shape of this round

Round 17's pass made two one-hop errors, and the seats caught both
with dynamic evidence. First, the discharge was keyed to the SYNCED
door — but production durability completes through the DURABLE door
(the ordinary assistant reply), the BUFFERED door's interval expiry,
and Close; the synced door is the rare one. Second — the deeper
error — the retained-edge fix froze the METADATA frontier on
durability, but the served view's anchor semantics are RECORDED (the
index names recorded entries; `Recorded` means "the whole line is in
the file", `record.go:32-34`; the session publishes recorded length
for retained lines, `session.go:2765-2766`; the history read catches
the index to recorded length; the anchor names the newest recorded
execution). Freezing metadata alone created the exact two-shapes
disagreement this spec exists to eliminate — S's probe read a REAL
index naming the retained new execution while the new rule held
metadata at the previous one — and T showed the collision is not
restricted to failure paths: the goal row's ORDINARY write is
buffered (production interval one second), so recorded-but-not-durable
is the NORMAL outcome of a designed row, and the freeze either violates
its own text or breaks journey 8's goal case. The class-level lesson,
now the round-19 rule: **the pass keeps deriving rules from single
code paths instead of from the writer's complete door model — the
durability model becomes a first-class doors table in the spec, and
capture, discharge, and the frontier derive from it.**

## The findings, verified

- **R18-R-1 = S18-1 = T18-3** (significant, spec 3, three-seat
  convergence, S's dynamic proof through the real durable path): the
  synced-only discharge misses the doors that actually complete
  durability. The ordinary assistant response records through
  DoorDurable (`agent/session_model_call.go:1038-1057` →
  `agent/session.go:2981-2984` → `writeTranscriptDurableLocked`
  `:2794-2799`), which fsyncs the whole file
  (`agent/transcript/transcript.go:677-692`); a buffered record
  fsyncs when its interval expires (`:781-787`, production interval
  one second, `agent/session_init.go:676,1418`); and `Writer.Close`
  syncs dirty debt with no Record outcome at all
  (`:1033-1061`). S's probe drove a retained user input settled by
  the real `appendAssistantTurn` durable path with no later synced
  write: durability completed, the named discharge never ran —
  the signal emits ZERO times on the ordinary retained-input →
  assistant-reply schedule. Verified by me at every hop.
  Disposition 1.
- **S18-2 = T18-1 + T18-4** (significant, spec 3, two-seat
  convergence with independent dynamic evidence, plus T's checkpoint
  face): the retained-edge freeze overloaded the frontier with a
  second meaning. The served view's anchor is RECORDED — S's probe
  read a real `transcriptindex.Open(...).Latest(40)` naming the
  retained new execution before any successful fsync, while the new
  rule mandates metadata hold the previous execution: two live reads
  of the same content disagree by design. T's face: the goal row's
  first entry is written through DoorBuffered
  (`agent/session.go:2668`), where the interval skip makes
  recorded-not-durable the ordinary outcome
  (`transcript.go:781-786`, pinned by
  `TestWriter_PeriodicSync_SkipsSyncWithinInterval`), so the freeze
  either advances on non-durable entries (violating its own text) or
  makes journey 8's goal-mode green unreachable in production. T's
  checkpoint face: the envelope's `facetAll` and `EventTurnEnded`
  resample (`server/thread_envelope.go:198-200,238,352-357`) write
  the same surface and the spec never mentions them; no
  durability-qualified frontier fact is defined. Verified by me.
  Disposition 2.
- **T18-2** (minor, spec 3, noted independently by all three seats —
  S's probe observed the same shape, R's wording): the durability
  field cannot be grounded on the retained diagnostic — the
  buffered door's `retained` is non-nil only when a write or sync
  FAILED (`transcript.go:770-796`); the ordinary interval-skip
  write returns nil error, nil retained, and zero fsyncs. The field
  must be computed from the sync decision itself
  (`transcript.go:781-786`). Disposition 1.
- **T18-5** (minor, spec 3): the mismatch hold's
  content-DIFFERS-at-read-back sub-case is unreconciled — the
  read-back fails on `!exists || !reflect.DeepEqual(persisted,
  message)` (`agent/session_attention.go:630-637`), but a retry for
  differing content fails typed at the PRE-record branch
  (`:600-602`) and never reaches the existing-content verification:
  the held id is neither emitted nor cleared. Disposition 4.

Also verified and NOT findings: the mismatch content-MISSING sub-case
survival held under S's overlay probe (a later retained record in the
same execution is not fresh; the retry recovers through existing
content; no double emission — the attention lock serializes, the id
clears after emission); the facet condition is mechanically expressible
(the envelope selects the event's bitmask before sampling,
`thread_envelope.go:352-357`, so a payload can suppress a single
facet bit); the wrapper sibling IS expressible on the user-input path
(the ambiguous-sync fixture is a generic afero FS wrapper usable with
any writer); the merger revision closed all three named faces with no
new coalescing defect (all three seats walked it); spec 1 and spec 2
unchanged by the round-17 commits, no new defect; the
stabilization sibling's pinned test is seam evidence for the BARRIER
RETRY only — it builds a bare session with no running execution, so
its resolution takes the placement stamp, not a join, and no fresh
flip exists (T's verification note; the journey wording is corrected
in this pass).

## Disposition — one revision pass, then round 19

1. **The durability model becomes a first-class doors table, and the
   discharge covers every whole-file-durability result
   (R18-R-1 = S18-1 = T18-3 + T18-2).** Spec 3 scope 1 gains a named
   contract over the writer's doors: DOORSYNCED proves the whole file
   (barrier), DOORDURABLE proves the whole file (the write's own
   fsync), DOORBUFFERED proves the whole file IFF the interval
   elapsed or is zero — the outcome's durability field is computed
   from the sync decision (`transcript.go:781-786`), never from the
   retained diagnostic, which is non-nil only on failure — and
   CLOSE settles dirty debt with a successful sync and no Record
   outcome. The discharge: every result that proves the writer's
   whole file durable discharges the held id and emits the signal —
   synced, durable, buffered-that-fsynced, the stabilization
   barrier, the attention folds; Close discharges through the
   session's final event flush, and the abandonment case (no
   consumer at close) is stated as the close path's known cost.
   Journey 8's wrapper sibling is restated: the discharge is the
   next whole-file-durability result of ANY door (the ordinary
   assistant reply is the named production schedule).
2. **The frontier's meaning corrected to RECORDED — the freeze
   reverted (S18-2 = T18-1 + T18-4).** The frontier is the newest
   execution in the SERVED VIEW, and the served view is recorded:
   the index names recorded entries, both shapes agree through the
   retained window AND the ordinary buffered window, and the goal
   row's journey is green as journaled. The row premise sentences are
   corrected from "already durable" to RECORDED (the file holds the
   whole line; durability is a separate fact), the retained-edge
   paragraph rewritten: the row fires on the recorded-and-adopted
   outcome and the frontier facet ADVANCES with it, matching the
   index anchor; the durability fact keys THE SIGNAL, not the
   frontier — the signal stays post-durability with the owed id, and
   its facet row refreshes the frontier idempotently at discharge.
   The envelope's facetAll and turn-end resample key on the same
   recorded anchor — no durability-qualified frontier fact exists.
   The in-code comment naming stays (today's artifact; the spec is
   the design).
3. **The differs-at-read-back hold disposed (T18-5).** The held id
   is cleared without emitting at the retry's typed pre-record
   failure: a content that can never verify must not leak a held id
   forever, and emitting would assert a verification that never
   happened. Both read-back sub-cases now have explicit
   dispositions: content-MISSING reconciles through the
   existing-content branch; content-DIFFERS clears without emitting
   at the typed failure.
4. **The stabilization sibling's wording corrected** (T's note): the
   pinned test is seam evidence for the barrier retry only; the
   sibling needs a running execution in the fixture so the
   resolution joins it as a first entry.

README: round-18 trail (eighteen; the count held at 2; both
significants convergences on the pass's own two decisions; the
doors-table lesson). fence-spike: a tenth re-audit note (zero fence
findings; the fence untouched by this round's findings, which sit in
the signal and frontier surfaces). Round 19: the same three seats,
closure audits of their own round-18 findings plus fresh attack on
the new pass text — first target again, with the doors table and the
recorded-frontier revert named as the attack surface.

## Readiness after this round

Spec 1 READY (all three, ninth consecutive). Spec 2 READY (all
three, third consecutive). Spec 4 READY (all three; the merger
revision closed every named face). Spec 3 NOT READY (all three
seats). Counts r11-r18: 7, 5, 4, 5, 7, 4, 2, 2. The fence holds at
zero findings across ten re-audits.
