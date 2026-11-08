# Round 15 — reconciliation and disposition

Reviewed at commit `663c7b7386` (the round-14 revision pass), code base
`2be411cd8f`. Seats — the same three families, an eighth pass, each
auditing closure of its OWN round-14 findings before fresh attack, all
leaves, high effort; all three ran the reds uncached with `-count=1 -v`
(every red failed for exactly the documented reason across all three
seats, zero disputed, all four characterizations passing — three
independent confirmations, the ninth consecutive clean red audit):

- **R** `codex-jesse-fsck.com/gpt-5.6`
  (dlg_034tbFXmfH2rBb7rSspb1R), run 18:42-19:13: 3 significant + 1
  minor; R14-R-1..R-4 CLOSED, R14-R-5 **PARTIAL** (the test
  comparisons).
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034tbFZbqDtM7gTc9VJJib), run 18:42-18:55: 3 significant;
  S14-1, S14-2, S14-3, the S13-2 residual and the S13-1 lock note all
  **CLOSED** — but each closure opened a new adjacent hole (below);
  three scratch dynamic proofs.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034tbFaxBmBWuAIFzkDAnz), run 18:42-18:57: 1 significant + 5
  minor; N14-T-1/T-3..T-7 CLOSED, N14-T-2 **PARTIAL** (the truncation
  half: marker carrier and recovery).

Tally: **7 distinct significant + 5 distinct minor. Spec 1 READY from
all three seats — the sixth consecutive round. The fence converged
through its SEVENTH re-audit with zero findings.**

Every load-bearing claim was re-verified in-tree by me at the exact
hop named, including both of R's dynamic-adjacent claims re-derived
from the seats' evidence.

## The honest shape of this round

**All seven significants are the round-14 revision pass's own text,
and the count went UP for the second consecutive round: 5 to 7.**
The round-14 record ordered exactly this — "the first target of the
fresh attack is MY round-15 pass text" — and the seats executed. The
pattern across the two rounds is one lesson, now stated as a pass
rule: **a contract is not closed at its design site; it is closed at
every site that writes to the same surface.** Round 14's pass named
the roster segment's bound without walking the merger that coalesces
segments (`agent/watch_send_rail.go:703-705` →
`agent/job_watch_hub.go:41-77`); named the frame's carrier by
misreading a whitelist builder as a marshaller (`rosterChange`,
`agent/job_watch_hub.go:213-224`, a seven-field local struct filled
by a positional literal — my "the frame builder marshals entry
fields only" was false); claimed a recovery pointer the code does not
deliver (`job_watch_hub.go:35` re-registers the watch, which
re-truncates the same segment); "named" the fetch's remote leg with a
byte-for-byte pass-through that cannot work in either direction (the
cursor's identity fence is an exact match on ThreadRef, incarnation
and projection version, `internal/appitempaging/cursor.go:59-64`,
and the ordinary remote read translates seven kinds of typed content
a byte fragment cannot carry,
`cmd/evener-hub/internal/appsource/remote_hub_refs.go:407-455`);
stated the signal's emission population as two named sites when the
producer set includes three more writers (the free resolution helper
discarding `writer.Record`'s return,
`agent/session_attention.go:1924-1943`; the stop stabilization write
`:1891-1893`; the resident resolver `:1743-1784`, whose own comment
says stop goroutines reach the running execution "at any time", with
the delegate-tree stop cleanup as the live trigger,
`agent/delegate_tree_restore.go:719-755`); and centralized on a
one-shot fact that the attention site's own failed-read-back path
consumes before the emission point, with the successful retry
structurally unable to re-record (`agent/session_attention.go:621`
records, `:630-637` fails verification and returns, the retry takes
the existing-content branch at `:600-614` and records nothing).

## The findings, verified

- **S15-1** (significant, spec 4): the roster rail coalesces
  individually bounded segments into an over-bound segment — the
  pending-send merger unions entries by ref with no budget pass and
  loses the previous segment's truncation marker (proved
  dynamically: two 24-entry payloads merge to 48 entries, 6,001 runes
  against a 4,096 witness bound). Verified by me at both hops.
  Disposition 1.
- **N15-T-1** (significant, spec 4): the frame's roster payload is
  a seven-field whitelist — `rosterChange` at
  `agent/job_watch_hub.go:213-224` — so a new `AttentionEntry` field
  never reaches the frame payload, and my carrier claim ("the frame
  builder marshals entry fields only") was false at the last hop.
  Verified by me. Disposition 1.
- **R15-R-3 = the recovery half of N14-T-2's PARTIAL** (significant,
  spec 4; three seats, three faces of one contract): the truncation
  marker has no declared carrier, and the frame's actual recovery
  instruction (`agent/job_watch_hub.go:35`) re-registers the roster
  watch — which re-delivers the SAME truncated segment, so the
  omitted lanes are permanently invisible. Spec 1's `job_list`
  fleet read is the recovery, and nothing wires it. Verified by me.
  Disposition 1.
- **S15-2** (significant, spec 2): remote fetch fragments cannot
  match the controller's unfiltered encoding — the ordinary remote
  read translates actors, image routes, authorship and nested refs
  (`remote_hub_refs.go:407-455`), a base64 fragment cannot receive
  those transformations, and the measured encodings differ (987 vs
  1004 bytes, image routes in the wrong namespace). Verified by me.
  Disposition 2.
- **R15-R-1** (significant, spec 2): the remote fetch cursor has no
  valid two-hub round trip — cursor validation is an exact
  ThreadRef/incarnation/version match (`cursor.go:59-64`); the inner
  hub's cursor names `local:` and carries no outer identity, and a
  reminted outer cursor fails the inner hub's validation. The
  existing remote ITEM paging already shows the answer the fetch
  must mirror: the controller mints (or continues) a controller-owned
  identity and retains the inner cursor behind it
  (`remote_hub_source.go:743-753`, :750-797). Verified by me at all
  hops. Disposition 2.
- **S15-3** (significant, spec 3): the one-shot fresh fact is
  consumed by a failed post-append read-back before the emission
  point; the successful retry takes the existing-content branch and
  never re-records, so no signal is ever emitted for that execution
  (proved dynamically through the existing
  `delegateAttentionReadFold` seam and the pinned persistence-failure
  test, `agent/session_assistant_persistence_test.go:51-91`).
  Verified by me at `agent/session_attention.go:600-642`. Disposition
  3.
- **R15-R-2** (significant, spec 3): the direct attention-resolution
  writers escape the signal's emission population — the free helper
  discards `Record`'s return (`:1924-1943`), the stop stabilization
  write does the same (`:1891-1893`), and the resident resolver's
  own comment says delivery and stop goroutines reach the running
  execution "at any time" (`:1781-1782`), with the delegate-tree
  stop cleanup as the live trigger
  (`agent/delegate_tree_restore.go:719-755`). T traced the same
  sites without a schedule; R supplied the trigger. Verified by me
  at every hop. Disposition 3.
- **R15-R-4 = N15-T-2** (minor, spec 4; two seats): the equality
  migration omits the test comparisons — eight comparisons in three
  roster test files break under the slice field, because
  `RosterEvent` embeds `AttentionEntry` (`appwire/roster.go:5-8`).
  Disposition 4.
- **N15-T-4** (minor, spec 2): the spec contradicts itself about who
  builds and slices the canonical bytes (the hub-mints-and-slices
  text at `2-cheap-filtered-reads.md:277-280` versus the
  serving-side-returns-the-range text the round-14 pass added).
  Disposition 2.
- **N15-T-5** (minor, spec 2): the fetch member's landing zone is
  cited one layer off — `local_daemon.go:648-659` is the hub's
  client call; the daemon handler that builds `ThreadReadResponse`
  and must assign the fetch member is
  `server/appwire_read_handlers.go:98`. Disposition 2.
- **N15-T-6** (minor, spec 4): "all three travel the chain" through
  `AttentionEntry` contradicts `CallsTruncated` being row-side only.
  Disposition 5.

Also verified and NOT findings: the choke-point amendment matches the
code and the spec text (all three seats, independently — the
entry-population closure argument held under attack; T's note that
the layer PERMITS the violation without enforcing it is recorded
below as a wording hardening, not a defect); the emit-to-record-gap
barrier is expressible at existing seams (two seats: the blocked-writer
pattern the existing tests use, `agent/session_tool_round_test.go`);
`ClonePendingAsk` copies every field so the mint-site split is sound
(two seats); the voice migration is reachable (T: the voice bridge
holds an `*appwire.Client` that already issues `ThreadRead`);
`Record` already returns `(transcript.Record, error)` so the
outcome-surfacing is expressible (S and T independently), and the
error-only wrapper `recordTranscriptLocked` at
`agent/session.go:2744-2768` can consume the outcome without
changing outer signatures (S); the compaction events DO have
configured facet rows, just none with the frontier facet — the
held-compaction journey's absence claim is about frontier refresh,
which is correct as scoped (S); the cursor encoding can carry an
additive range offset coherently (all three seats); the remote clamp
rollout and the mint-split soundness (two seats each); no fence
finding from any seat.

## Disposition — one revision pass, then round 16

1. **The roster segment contract completed end to end (S15-1 +
   N15-T-1 + R15-R-3 + the N14-T-2 residual).** The marker gains a
   DECLARED frame field (`rosterDropped`, a count) — IN-PASS
   CORRECTION to this record's first draft, which placed it "in
   the bounded frame body": the landing is a frame LINE beside
   `roster_events` (`agent/watch_send_rail.go:52-53`), appended
   after the cap exactly as `roster_initial` is, because the
   coalescing merger's surgery is line-oriented
   (`rosterFrameField`, `agent/job_watch_hub.go:80-87`) and a
   marker inside the capped body would be unreachable to it. The merger hop is OWNED: `mergeHubRosterFrames`
   (`agent/watch_send_rail.go:703-705` calling
   `agent/job_watch_hub.go:41-77`) re-applies the segment bound to
   the MERGED payload at whole-entry granularity and recomputes the
   dropped count as the union of the inputs' dropped counts plus any
   newly dropped entries — the marker survives coalescing; the
   current marker-preservation arms (`:72-75`) gain the dropped
   count beside resync/initial. The builder WHITELIST is the last
   carrier hop and is named: `rosterChange` (`:213-224`) gains the
   declared fields anything on `AttentionEntry` needs in the frame
   payload — the composite list, the composite total, the pair, the
   count fields — and the spec's false "the frame builder marshals
   entry fields only" claim is replaced by the struct-and-literal
   truth. The RECOVERY is corrected: the truncated frame's
   instruction names spec 1's `job_list` fleet snapshot read as the
   recovery for dropped entries (the re-register instruction
   re-delivers the same truncated segment and is no longer claimed
   as recovery for the dropped entries); the `rosterRecoveryFrame`
   text at `:35` is named as owned work. The boundary fixtures gain
   S's case: two pending batches plus an already-truncated
   predecessor, the merged segment asserted within the bound with
   the marker preserved, and the payload asserted to CARRY the new
   fields.
2. **The remote fetch leg designed, not named (S15-2 + R15-R-1 +
   N15-T-4 + N15-T-5).** The byte-for-byte remote pass-through is
   WITHDRAWN. The identity follows the pattern the existing remote
   item paging already proves (`remote_hub_source.go:743-753`): the
   fetch's cursor for remote-origin items is a CONTROLLER-OWNED
   identity with the remote state retained behind it, exactly as
   `remoteItemPagingKey` retains the inner cursor — the outer hub
   resolves its cursor to the retained remote state before
   forwarding. The encoding namespace follows S's correction: the
   canonical encoding for a remote-origin fetch is the CONTROLLER's
   unfiltered encoding — TRANSLATION PRECEDES ENCODING AND SLICING
   (the ordinary translated read is the source; the fetch reuses
   its already-translated typed item, so journey 7's byte equality
   holds against the same encoding the Manager would read), and the
   remote-origin leg resolves HUB-SIDE; the serving-daemon leg
   (same namespace, no translation) keeps the daemon-side range
   serve — the WHO-SLICES contradiction resolves by arrangement:
   the serving DAEMON forms the canonical encoding and slices the
   addressed range (the handler at
   `server/appwire_read_handlers.go:98` is the assigning site —
   N15-T-5's correction), the hub relays; the hub-compat and
   saved-index legs construct hub-side as already stated; the
   hub-mints-and-slices sentence at `2-cheap-filtered-reads.md:277-280`
   is corrected to the per-arrangement truth. Items a remote-origin
   fetch cannot carry through the inter-hub read fail a TYPED
   refusal naming the degradation — never a silent path.
3. **The signal's emission population and one-shot survival
   (R15-R-2 + S15-3).** The emission is centralized at the Record
   call, not at two named sites: the session-level outcome handler
   reads the placement outcome at EVERY record site, and the sites
   that discard `Record`'s return today — the free resolution helper
   (`agent/session_attention.go:1924-1943`), the stop stabilization
   write (`:1891-1893`), beside the delivery site (`:616-642`) and
   the `recordTranscriptLocked` wrapper (`agent/session.go:2744-2768`)
   — gain the outcome read as owned work, each named. The one-shot
   fact's failure path is designed (S15-3): the outcome is consumed
   and HELD as an owed notification when the post-append
   verification fails; the successful retry's existing-content
   branch reconciles the owed notification on its verified read-back
   (once-per-execution dedup), so a committed-but-unverified first
   entry still produces the signal exactly once — the journey gains
   the failed-read-back schedule as a red case (the existing
   persistence-failure seam and pinned test are its evidence).
4. **The equality migration completed (R15-R-4 = N15-T-2).** The
   helper's migration names the eight test comparisons in the three
   roster test files (RosterEvent embeds AttentionEntry,
   `appwire/roster.go:5-8`) — the "no other comparable context"
   claim is replaced by the full site list: production, then the
   named test sites, all migrating to the helper.
5. **The two text contradictions fixed in place (N15-T-4 + N15-T-6).**
   Spec 2's who-slices sentences take the per-arrangement truth
   (disposition 2); spec 4's chain sentence is corrected to
   "CalllessCount and CompositeTotal travel the §6 chain;
   `CallsTruncated` is row-side, stamped at the projection and never
   on `AttentionEntry`" — the two statements reconcile.

Wording hardening (T's non-finding note, adopted): the choke-point
argument's "the join is the only stamp" is precise as scoped (the
running turn's id and kind), but the spec should state it as a
caller-survey-plus-layer-contract, not an enforced invariant — the
layer permits an unstamped first entry through the named-turn
placement, and the population closure holds because no production
caller reaches it, which the seats re-verified.

README: round-15 trail (fifteen; the honest shape, the count up 5 to
7, the producer-sweep lesson). fence-spike: a seventh-re-audit note
(zero findings) plus the §6 chain gaining the `rosterChange`
whitelist hop (the chain's last leg is AttentionEntry → rosterChange
→ frame, not AttentionEntry → frame). Round 16: the same three
seats, closure audits of their own round-15 findings plus fresh
attack on the new text — my round-15 pass text first again.

## Readiness after this round

Spec 1 READY from all three seats (sixth consecutive). Specs 2, 3
and 4 NOT READY. The count went 7, 5, 4, 5, 7 across rounds 11-15:
the seats are now auditing the passes, and the passes are the
design's own newest text — the attack surface follows the pen.
The fence holds at zero findings across seven re-audits.
