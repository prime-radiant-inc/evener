# Round 21 — three-seat reconciliation (record of decision)

Seats: R gpt-5.6 (`dlg_034tkKDkEsueOqPb7E274c`), S gpt-6.1-sol-1m
(`dlg_034tkKFwc2GlM12eWJ1gCA`), T deepseek-4.1-flash-background
(`dlg_034tkKHFEBihwa3WiWjllo`). Reviewed at `efea258dff` (the round-20
revision pass, on top of `4ff2ced491` the round-20 record) over the
production-identical base `2be411cd8f`. All three seats ran the required
reds uncached (`-count=1 -v`, sources inspected first): 4/4 red for the
documented reasons, 4/4 characterizations passing, zero disputed,
worktrees clean.

## Round-20 closures — all five CLOSED

- **R20-R-1 / S20-1** (the retained synced-door pre-return window) —
  CLOSED. S re-proved the relocation dynamically through the real
  fixture: while the recovery `Sync` was gated, the listener-stamped
  field AND the real index both named the fresh execution and the
  refresh event reached the real session stream before the producer
  returned (`field=turn_m1 real-index=turn_m1 first-record-count=1
  producer-returned=false`). R verified the placement's code ordering
  end to end (the hook fires inside `appendBatch`; the barrier follows
  at `record.go:73-95`; the listener's own-mutex queue satisfies the
  leaf rule; the enqueue happens on the emitter goroutine after the
  hook returns). The relocation survives: no seat found a
  relocation-killer.
- **R20-R-2** (the stale event-definition phrase) — CLOSED; R re-ran
  the sweep, zero matches.
- **T20-1 / T20-2 / T20-3** — CLOSED (T's audit, verified): the pinned
  source at every hop; the TURN-ENDED resample matches the index's
  non-copy anchor semantics (`build.go:483-490`); the doors correct;
  the free helper's records reach the hook through the reopened
  writer's shared append tail.

All three seats corrected the same error in the chair's ROUND-21
BRIEF, owned here: the brief said the wrapper `recordTranscriptLocked`
holds `s.mu` across `Record` — false (the readiness lock ends at
`session.go:2749`, the call is at `:2757`, the next lock at `:2762`).
No production record site holds `s.mu` at the hook; the spec text
never made the claim, and the leaf-only listener holds at every
producer.

## New findings — 2 distinct significant (both three-seat converged),
3 minor; every load-bearing claim verified in-tree by the chair

### S21-1 = T21-1 = R21-R-1 — significant, three-seat, dynamically
proved with a passing causal control

**Compaction's verbatim replay copies satisfy the kind-only filter and
regress the frontier.** The round-20 text identifies the first entry
"from the record alone" (`TurnKind == TurnSpanExecution`) and excludes
the replay with "whose ids cannot be a fresh turn's" — an argument
that belonged to the WITHDRAWN producer-outcome mechanism (it read the
fresh flip from the outcome). The relocation replaced that
discriminator with the kind value, which copies carry:

- `agent/session_compaction.go:417-476` builds each replay turn from
  the file entry and only adds `OriginalOrdinal`, preserving the old
  `TurnID` and `TurnKind`; `:369-395` records the copies through
  `PlaceVerbatim`; `agent/transcript/placement.go:102-103` stamps
  nothing; the hook fires for every copy
  (`agent/transcript/transcript.go:854-865`).
- The index skips the same copies
  (`internal/transcriptindex/build.go:154-161`) and its anchor moves
  only on non-copy execution entries (`:463-490`) — so the field and
  the index DISAGREE after a compaction, until another fresh commit.

S's dynamic proof: twelve real completed executions, `turn_m13` opened,
real automatic compaction at pressure 0.85 — the checkpoint stamped
`turn_m13`, then the replay of six preserved first entries regressed
the field to `turn_m12` (19 matches vs 13, the index stayed
`turn_m13`). S's CAUSAL CONTROL: excluding `rec.Turn.OriginalOrdinal !=
nil` from the stamp and the signal produced `field=turn_m13
index=turn_m13 matches=13 replay-matches=0` — the exclusion is
sufficient on the real schedule. T adds: the copies also break the
exactly-once red (a copy is a second recorded entry for the same
execution). The layer already knows the rule —
`agent/transcript/turn_status.go:5-12` ("counting it as a fresh start
would resurrect a turn its own completion... already closed"),
`ExecutionTurns` skips `OriginalOrdinal != nil` at `:17`, pinned by
`agent/transcript/turn_status_test.go:46-63`; the session's own
`noteRecordedLocked` (`agent/session_execution.go:129-139`) applies
exactly the exclusion+filter the listener needs.

### S21-2 = T21-2 = R21-R-2 — significant, three-seat, dynamically
proved

**The commit-only field has no restore seed.** The mechanism writes the
field only from the commit listener; a session restored from a prior
process has recorded executions but no in-process commit, the reopen
entry carries no execution kind
(`agent/session_execution.go:76-79`), crashed completions write empty
`TurnKind` (`:346-353`), and the initial envelope sample runs before
the bridge (`cmd/evener/serve.go:1199-1204`) — before any commit could
fire. S's dynamic proof: a real transcript holding a completed
execution, listener installed before attach — `field="" matches=0
real-index=turn_m1`. An idle restored session may never record another
first entry: the metadata frontier stays empty INDEFINITELY while
history names the completed execution. `SessionStart` merely resamples
the blank field (R).

### S21-3 = T21-3 — minor, two-seat, characterized

**The single-slot hook needs a named composition.** `OnRecorded` is a
replacing setter (`record.go:109-125`); `SetTranscriptRecordedFunc`
replaces (`session_execution.go:241-257`); the daemon owns the slot
today (`WireTranscriptHistory` → `SetTranscriptRecordedFunc(hooks.Recorded)`,
`server/appwire_histories.go:49-59`); descendants' slot at
`agent/session_init.go:505`. S characterized both registration orders
discarding one consumer.

### T21-4 — minor, verified at the hop

**The sampling rule must be amended, not merely cited; the emitter must
release the leaf before enqueueing.** The rule verbatim: "A method on
this interface may take Session.mu. It may take no other lock that
lives in package agent" (`agent/session_envelope_sampling.go:22-26`) —
the spec's "then the field's own leaf lock" contradicts it as written,
and the interface's proof test needs the new method plus the
leaf-safety statement. And `s.emit` takes `s.mu` (via
`activeCausalProvenance`, `agent/session_provenance.go:8-12`), so the
emitter must release the leaf before enqueueing — the history drain's
release-before-project is the precedent
(`server/thread_history.go:525-546`).

### R21-R-3 — minor, verified at the hop

**The doors paragraph still calls attempted syncs durability proofs.**
`DOORDURABLE` "proves it (the write's own fsync)" and `DOORBUFFERED`
"proves it IFF the interval elapsed or is zero" both lack the
successful-sync qualifier: an elapsed or zero interval causes a sync
ATTEMPT, not proof; a failed sync on a whole line returns nil with the
line recorded as dirty debt ("recorded-or-error is their whole
contract", `agent/transcript/transcript.go:589-603`; the diagnostic
goes through DrainWarnings, not the returned error). The two mid-session
barriers' behavior-change claims are otherwise correct (R verified
`session_tool_round.go:359-393`, `session_retirement_prepare.go:218-245`).

## Disposition — five fix families, one pass

1. **THE FILTER BECOMES noteRecordedLocked's OWN PREDICATE.** The
   listener stamps and signals only on
   `rec.Recorded && rec.Turn.OriginalOrdinal == nil && rec.Turn.TurnKind
   == TurnSpanExecution` — fold copies excluded from BOTH the stamp and
   the signal, with the session's own bookkeeping
   (`session_execution.go:129-139`) named as the in-code precedent and
   S's causal control as the dynamic proof. The replay's exclusion
   argument is restated on the record's own facts (copies cannot flip
   the fresh fact — they never join — but their COPIED KIND satisfies a
   kind-only filter, hence the `OriginalOrdinal` predicate), retiring
   the inherited "whose ids cannot be a fresh turn's" reasoning.
   Journey 8 gains the PRESERVED-TAIL case (a real compaction with
   retained first entries: red if the field regresses or the signal
   re-fires).
2. **THE FIELD GAINS A BOOTSTRAP.** At writer attach (the
   `attachTranscript` seam, `agent/session.go:2873-2895`), the session
   seeds the field from the validated existing view with the INDEX's
   anchor semantics — the newest execution among existing entries,
   with the same `OriginalOrdinal` exclusion — BEFORE the initial
   envelope sample (`serve.go:1199-1204`). The seed is serialized with
   the listener installation under the listener's mutex and cannot
   overwrite a concurrent commit's stamp (S's correction: the seed
   applies only when no commit fact has stamped or queued; a racing
   commit wins). Journey 8 gains the IDLE-RESTART case (a restored
   session with a completed execution and no new execution: the
   metadata read names the completed execution — red if blank).
3. **THE COMPOSED DISPATCHER.** The session installs ONE dispatcher
   into the file's hook slot that fans out to the internal frontier
   listener and the externally-set callback;
   `SetTranscriptRecordedFunc` keeps its single-slot external semantics
   by setting the dispatcher's external half, so the history callback
   survives every setter call, attachment, reopen, and descendant
   wiring. The emitter goroutine's lifecycle is named (started at
   dispatcher install, stopped at session close — the history drain's
   goroutine shape).
4. **THE RULE IS AMENDED, AND THE EMITTER'S DISCIPLINE STATED.** The
   sampling rule's text gains the frontier field's leaf as the second
   allowed lock, with the no-cycle argument in the rule's own terms
   (the listener takes only the leaf, never `Session.mu`), and the
   interface's proof test gains the method plus the leaf-safety
   statement. The emitter RELEASES the leaf before enqueueing (s.emit
   takes `s.mu`; the history drain's release-before-project is the
   precedent).
5. **THE DOORS ACCOUNTING QUALIFIES ON SUCCESS.** DOORDURABLE proves
   the whole file when its own fsync SUCCEEDS and DOORBUFFERED when the
   interval's attempt SUCCEEDS; both doors' retained-unsynced face is
   named (the line recorded as dirty debt, nil returned, the
   diagnostic through DrainWarnings).

## Readiness

- Spec 1 — READY from all three seats (twelfth consecutive round).
- Spec 2 — READY from all three seats (sixth consecutive round).
- Spec 3 — NOT READY: the copy regression and the missing seed break
  the same-frontier contract until the pass lands.
- Spec 4 — READY from all three seats; the fence at zero findings.

Significant trajectory r11-r21: 7, 5, 4, 5, 7, 4, 2, 2, 3, 2, 2.

## Round 22

Same three seat families, new delegates: closure audits of their own
round-21 findings plus fresh attack on the pass's new text — the
filter's predicate against every replay/copy/reopen path, the
bootstrap's serialization and the attach seam, the dispatcher's
composition against the daemon's wiring order, the amended rule's
proof test, the doors' qualified claims, and the three new journey
cases.
