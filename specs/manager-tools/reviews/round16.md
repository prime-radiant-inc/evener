# Round 16 — reconciliation and disposition

Reviewed at commit `9158488f4a` (the round-15 revision pass), code base
`2be411cd8f`. Seats — the same three families, a ninth pass, each
auditing closure of its OWN round-15 findings before fresh attack on
the round-15 pass text, all leaves, high effort; all three ran the
reds uncached with `-count=1 -v` (every red failed for exactly the
documented reason across all three seats, zero disputed, all four
characterizations passing — three independent confirmations, the
tenth consecutive clean red audit):

- **R** `codex-jesse-fsck.com/gpt-5.6`
  (dlg_034te08Pgse37wVUkAE3aq, run 20:34-21:17): 3 significant + 1
  minor; R15-R-1..R-3 CLOSED, R15-R-4 **PARTIAL** (the comparison
  list again).
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034te0AXm3D7yb9s9BDrfc, run 20:34-20:43): 1 significant, no
  minors; S15-1..S-3 all CLOSED, S15-3 closed for its reported
  schedule with the adjacent retained-error hole as the new finding;
  one scratch dynamic proof through the real ambiguous-sync seams.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034te0Bs9J3Pn2sGH2ZcYJ, run 20:34-20:41): 2 significant + 3
  minor; N15-T-1/T-3..T-6 CLOSED, N15-T-2 **PARTIAL** (the same two
  omitted comparisons R found independently).

Tally: **4 distinct significant + 4 distinct minor. Specs 1 and 2
READY from all three seats — the first time either has been READY
from all three since round 10. The fence converged through its EIGHTH
re-audit with zero findings from any seat.**

Every load-bearing claim was re-verified in-tree by me at the exact
hop named, including record.go's synced-door contract, the resolver's
existing-resolution path, the AttentionEntry identity fields, spec
1's no-pagination boundary, and both omitted test comparisons.

## The honest shape of this round

**All four significants are again the previous pass's own text, but
the count went DOWN, 7 to 4, and the surface is visibly shrinking:
the round's largest finding is the set's first THREE-seat
convergence** (S16-1 = 16-T-5 = R16-R-3), S proved it dynamically
through the existing ambiguous-sync seams, and the population
closure from rounds 14-15 HELD under attack — both S and T re-walked
all six producer-survey sites and found no escape. The new holes are
all in the round-15 pass's newest mechanisms, each one hop past the
text that closed the last round: the owed-notification design
covered the failed read-back but not the writer's own
recorded-but-unsynced outcome (`record.go:52-57`: "Record then
reports Recorded with the error, and the caller adopts the record"
— the pass never read the door's full contract); the truncation
recovery line was landed on a constant whose text begins with the
resync marker the merger treats as authoritative replacement; the
recovery was nominated against spec 1's fleet read without walking
that read's own cap boundary (spec 1:279-283: "no new pagination...
an over-cap response keeps today's refusal" — the recovery fails at
exactly the fleet size that causes truncation); and the whitelist
extension list named the ask facts but not the two identity fields
the frame-only relay's own journeys require. The comparison list was
short for the THIRD consecutive round (now 12 contexts, not 10),
which converts a habit into a rule: **enumeration claims are made by
mechanical sweep, never assembled from seat reports** — the seats
grep, the pass must too.

## The findings, verified

- **S16-1 = 16-T-5(a) = R16-R-3** (significant, spec 3, three-seat
  convergence with S's dynamic proof): the synced door's third
  outcome — `rec.Recorded` returned together with
  `*RetainedUnsyncedError` (`agent/transcript/record.go:49-57`,
  `:83-95`) — commits the first entry and consumes the fresh flip
  while every named producer returns on the error before the owed
  site: the content writer at `agent/session_attention.go:621-624`,
  the free resolution helper at `:1938-1940`, the stop stabilization
  write. The retry then finds the content in the file, takes the
  existing-content branch (`:600-614`) — or the resolver reopens
  durably around the already-present resolution
  (`:1771-1795`) — and no second outcome ever comes: the signal
  emits ZERO times. S proved the schedule through the real
  `newAttentionAmbiguousSyncFS` fixtures
  (`agent/session_attention_test.go:1177-1237`); the pinned
  injected-sync tests at `:1177-1228` and `:1240-1275` prove both
  retry shapes executable. Verified by me at every hop.
  Disposition 1.
- **16-T-3** (significant, spec 4): the truncation recovery line was
  landed on `rosterRecoveryFrame` (`agent/job_watch_hub.go:35`),
  whose text BEGINS `roster_resync: true` — and the merger returns
  the current frame immediately on that marker
  (`agent/job_watch_hub.go:42-44`), so a truncated delta carrying the
  recovery line would short-circuit the next coalescing merge and
  silently discard the pending predecessor payload the same
  paragraph promises to merge, while asserting a resync that is not
  happening. Verified by me at all three hops. Disposition 2.
- **R16-R-1 + 16-T-2(a)** (significant, spec 4): the whitelist
  extension list omits the identity fields the design's own
  consumers require: `RosterInstanceID`
  (`appwire/attention.go:12-14`, stamped at
  `cmd/evener-hub/attention_subscriptions.go:98-110`) is the token
  journey 6's frame-only relay must carry into `expected_instance_id`
  (`4-question-surfacing.md:1112-1116`, the dependency stated at
  :199-200), and `ID` (the thread id, `appwire/attention.go:15`) is
  the fetch addressing — `rosterChange` has a slot for neither. Also
  absent: the excerpt `complete`/`truncated` flags and `listReason`.
  Verified by me. Disposition 4.
- **R16-R-2** (significant, spec 4): the nominated recovery — one
  spec-1 `job_list` fleet read — is bounded by that read's own
  contract: one call, no pagination, whole-response refusal above
  the 256KB cap (`1-fleet-snapshot.md:279-283`;
  `agent/session_reads.go:244-250`). A fleet large enough to
  truncate the roster segment is exactly a fleet that can fail the
  recovery whole: permanent invisibility moved from
  re-registration to the cap. Verified by me in spec 1's own text.
  Disposition 3.
- **16-T-5(b)** (the second face of the retained-error family, spec
  3): the mismatch-path rationale is false at the code —
  `removeUnverifiedDelegateAttentionTurn`
  (`agent/session_attention.go:1604-1625`) deletes only from
  `s.history` (its own doc comment: "A deletion, not an append"),
  never from the file, so the retry re-records only in the
  content-MISSING sub-case; the content-DIFFERS sub-case fails typed
  ("conflicting content", `:601-602`) and never records. The
  blanket "removes the turn, so the retry re-records" is withdrawn.
  Verified by me. Disposition 1.
- **16-T-1 = R's R15-R-4 PARTIAL** (minor, spec 4, two-seat
  convergence): the comparable-context list is TWELVE, not ten —
  `agent/job_watch_hub_roster_test.go:232` and `:323` are direct
  `RosterEvent` comparisons (`entries[0] != a || entries[1] != b`)
  the round-15 list omitted. Verified by me by grep. Third
  consecutive short list. Disposition 4's mechanical-sweep rule.
- **16-T-2(b)** (minor, spec 4): the chain names a carrier with no
  writer at one hop — `cmd/evener-hub/internal/hubcore/attention.go:100-113`
  builds `AttentionEntry` field by field (ID, Level, the three
  flags, Title, Project) with no slot or owned-work naming for the
  pair, question text, composite list or total, while `publishedAt`'s
  fill site IS named. Verified by me. Disposition 4.
- **16-T-4 + R16-R-4** (minor, spec 4, two faces of the merger's
  marker arithmetic): "the union of both inputs' dropped counts" is
  not computable — `mergeHubRosterFrames` is pure string surgery
  with no dropped-ref knowledge; only a SUM is computable, which
  over-counts a lane dropped by both segments (never under-counts —
  the safe direction, hence minor); and the resync/initial
  early-return at `:42-44` discards the PREVIOUS frame's count
  entirely — a truncated pending delta coalesced with a current
  resync loses its marker, though the resync frame is itself an
  authoritative replacement that computes its own fresh count.
  Verified by me at the merger. Disposition 2.

Also verified and NOT findings: the producer sweep's six named
exclusion sites hold under both S's and T's independent re-walks
(fork's new-child transcript, compaction's replay, the abandoned
execution completion behind the idle gate, the two pre-execution
sites) with no new escape; `AppendBatch` is `PlaceVerbatim`'s only
other caller and has no production caller today; `AttentionWatcher.Tick`
compares fields, so there is no third production equality site; the
genuine resync-replacement semantics are deliberate (S's judgment,
and compatible with 16-T-3: the replacement is by design, the
recovery line must simply not carry the marker); the remote-source
fetch is grounded at every seam all three seats walked (cursor
extension coherence, the paging pattern, translation-before-encoding,
the typed unreachable refusal); the payload-row landing is internally
consistent with journeys 1, 2, 5 and scope 5's clamp story (S);
spec 1's rows carry the ask facts spec 4's recovery relies on,
including remote rows (`fromRemoteThread` passes `Evener` through
wholesale); no fence finding from any seat — the eighth consecutive
clean re-audit.

## Disposition — one revision pass, then round 17

1. **The owed obligation generalized to every recorded outcome
   (S16-1 = 16-T-5 = R16-R-3 + 16-T-5(b)).** The rule keys on the
   outcome's own field, not on which exit the caller took: the owed
   obligation attaches whenever `Record` reports a recorded first
   entry that cannot yet be emitted — `rec.Recorded == true`, with
   OR without an error — at every producer the round-15 pass named
   (the content site's error exit `:621-624` beside its failed
   read-back; the free helper's error exit `:1938-1940`; the
   stabilization write; the wrapper), held on the attention state's
   field family (`:650-659`) and reconciled only after durable
   recovery — the content site's existing-content branch after its
   durable verification (`:604-614`), the resolver's durable reopen
   (`:1771-1795`). Emitting on the first outcome is forbidden (the
   post-durability requirement stands); the owed id is what makes
   deferral lossless. The mismatch-path text is corrected in place:
   both sub-cases stated — content-MISSING re-records (the fresh
   flip comes again), content-DIFFERS fails typed ("conflicting
   content", `:601-602`) — and the false blanket rationale
   withdrawn. Journey 8 gains the retained-sync-failure sibling
   (red: no premature signal on the recorded-but-unsynced attempt;
   green: exactly one after durable recovery), with the pinned
   ambiguous-sync tests (`session_attention_test.go:1177-1237`,
   `:1240-1275`) named as the existing seam evidence.
2. **The truncation recovery line and the merger's marker
   arithmetic restated (16-T-3 + 16-T-4 + R16-R-4).** The recovery
   line is a NEW shape — `roster_recovery:` with the fleet-read
   instruction — that NEVER carries `roster_resync: true`, because
   the merger's authoritative-return at `:42-44` treats that marker
   as replacement; `rosterRecoveryFrame` is cited only as the line
   SHAPE's precedent, with the marker split named as owned work. The
   merged count is stated as the SUM it is (both inputs' counts plus
   the merge's own drops), with the over-count named as the safe
   direction — never under-counts. The resync/initial exception is
   stated: a current resync or initial REPLACES the pending segment
   authoritatively, discards the previous count by design, and
   computes its own fresh count from its own bounded snapshot — the
   "never coalesces into an unmarked one" promise carries its
   replacement exception.
3. **The recovery's reach made honest at any fleet size
   (R16-R-2).** The marker gains the dropped REFS — a bounded list
   (a named constant, measurement obligation against the real fleet
   shapes) with the count naming the total when the refs overflow
   the bound. The recovery instruction becomes a ladder, each rung
   stated with its own reach: per-lane reads for the named refs
   (each lane's read bounded, total at any fleet size); the spec-1
   fleet read for the small-fleet case, never claimed as total
   recovery (its one-call, no-pagination, over-cap-refusal boundary
   is quoted, not contradicted); and when the dropped refs overflow
   the marker's own bound, the frame says so — the count names the
   unrecoverable-by-refs remainder and the ladder names its rungs,
   never a silent loss.
4. **The whitelist enumeration completed, mechanically (R16-R-1 +
   16-T-1 + 16-T-2).** The extension list gains EVERY field the
   consumers require: `RosterInstanceID` (journey 6's
   `expected_instance_id` token), `ID` (the thread id, the fetch
   addressing), the pair, the composite list, the composite total,
   the call-less count, the question text, the ask turn id, the
   excerpt `complete`/`truncated` flags, and `listReason`. The
   intermediate fill site is named as owned work: hubcore's
   `AttentionEntry` builder
   (`cmd/evener-hub/internal/hubcore/attention.go:100-113`) gains
   the fill for every new entry field, the way `publishedAt`'s fill
   site is named. And the pass rule this record adds: **enumeration
   claims are made by mechanical sweep** — the comparison
   migration lists all TWELVE contexts (the two production sites
   plus the ten test sites, `roster_test.go:116,119,155,160,232,323,361`,
   `roster_clear_identity_test.go:71`, `roster_reconnect_test.go:27,51`),
   produced by grepping the tree for `RosterEvent` and
   `AttentionEntry` comparisons, not by assembling seat reports.

README: round-16 trail (sixteen; the count down 7 to 4; the
three-seat convergence; specs 1 and 2 first-time all-three READY;
the mechanical-sweep rule). fence-spike: an eighth-re-audit note
(zero findings), with the chain enumeration gaining the two identity
fields on the whitelist hop. Round 17: the same three seats,
closure audits of their own round-16 findings plus fresh attack on
the new pass text — first target again.

## Readiness after this round

Spec 1 READY (all three seats, seventh consecutive). Spec 2 READY
(all three, the first time — the remote-source fetch held under
every seat's walk). Specs 3 and 4 NOT READY. Counts across rounds
11-16: 7, 5, 4, 5, 7, 4. The fence holds at zero findings across
eight re-audits.
