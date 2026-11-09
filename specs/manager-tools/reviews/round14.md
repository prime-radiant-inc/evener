# Round 14 — reconciliation and disposition

Reviewed at commit `7564e98b4a` (the round-13 revision pass), code base
`2be411cd8f`. Seats — the same three families, a seventh pass, each
auditing closure of its OWN round-13 findings before fresh attack, all
leaves, high effort; all three ran the reds uncached with `-count=1 -v`
(every red failed for exactly the documented reason across all three
seats, zero disputed, all four characterizations passing — three
independent confirmations):

- **R** `codex-jesse-fsck.com/gpt-5.6`
  (dlg_034tYBy05nmDysjip3vJJS), run 16:37-16:52: 2 significant + 3
  minor; R13-R-1 CLOSED, R13-R-2 **PARTIAL** (the barrier insufficient).
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034tYBzvJSCPZLLfwEU50I), run 16:37-16:54: 3 significant;
  S13-1 CLOSED (with a sampler-lock note), S13-2 **PARTIAL** (the same
  barrier flaw, PROVED DYNAMICALLY); three scratch dynamic proofs.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034tYC3epNCZgsquL89H7I), run 16:38-16:50: 3 significant + 4
  minor; T-N1..T-N5 all CLOSED (but the T-N1 fix's carriers
  introduced a new significant).

Tally: **5 distinct significant + 5 minor. No spec clean except spec 1
— READY from all three seats, fifth consecutive round. The fence
stayed converged: zero fence findings from any seat.**

Every load-bearing claim was re-verified in-tree by me at the exact
hop named, including two dynamic claims re-derived from the seats'
proof descriptions.

## The honest shape of this round

**All five significants are the round-13 pass's own defects or
misses. The significant count went UP, 4 to 5, for the first time
since round 11.** Named plainly:

- **The roster fold I ordered in disposition 5 is defective from
  three angles** — all three seats converged on it. T proved the
  initial roster snapshot is every lane (about 1KB per lane at the
  real field set), so multi-lane snapshots cannot fit inside
  `watchFrameMaxChars` and `limitWatchText` would cut the
  `roster_events` JSON mid-object; R proved no preservation order or
  combined budget is named (the base frame can already consume all
  4096, and a rune-cut prefix can contradict the frame's own
  `truncatedList`); S proved the cardinality the K fixture never
  bounds — 32 ordinary entries with ZERO composite identities produce
  one 4,847-rune frame against the 4,096 bound today, so lowering K
  cannot help (`attention_subscriptions.go:72-82` puts every entry
  in one update, `job_watch_hub.go:222-235` marshals every changed
  entry into one payload). I ordered a fold without doing the
  arithmetic. Verified by me at every hop.
- **The fetch response carrier I named in disposition 1 stopped one
  hop short — again.** S found the serving-daemon leg: the fetch
  rides `ThreadReadParams` to the daemon, whose response is
  `ThreadReadResponse` (`appwire/client.go:559-562`,
  `local_daemon.go:648-659`) — a closed type with no member for
  partial bytes (`types_turn.go:367-402`). The outer
  `SessionOperationResponse.Fetch` member cannot receive bytes with
  no carrier across the daemon-to-hub hop. The same class round 12
  caught (concepts without carriers), now one layer deeper in my own
  fix. Verified by me.
- **The composite-list carrier my disposition 2 left implicit breaks
  compilation.** T proved it dynamically with a scratch overlay: the
  frame builder serializes only `AttentionEntry` fields
  (`job_watch_hub.go:212-233`), so the frame's list needs a slice on
  the entry — and `AttentionEntry` is compared as a value at
  `job_watch_hub.go:173` (`old != event.AttentionEntry`) and in
  `maps.Equal(sub.baseline, current)` at
  `attention_subscriptions.go:150`; both break
  (`struct containing []appwire.PendingAskCall cannot be compared` /
  `does not satisfy comparable`). Verified by me at all three hops:
  the entry is scalar-only today, and both comparison sites are real.
- **The journey barrier my disposition 4 added is insufficient.** R
  found it statically (emit returns on ENQUEUE at
  `session_events.go:438-440` into a 256-entry buffer,
  `session_init.go:519`; the bridge samples at DEQUEUE,
  `thread_envelope.go:352-357` — a post-hold drain proves
  processing, not sample timing); S PROVED it dynamically: gating the
  consumer's `EventGoalContinuation` callback until the provider
  hold, then draining, produced the GREEN-shaped observation
  (marker-sampled frontier = the new execution) on PRE-reorder code.
  The red can pass falsely. Verified by me at the buffer and the
  dequeue-time sampling.
- **The population walk my disposition 3 claimed is still
  incomplete.** S found the sixth first-persist path and proved it
  dynamically: an attention-only notification execution (no
  reminder) whose request PREPARATION triggers automatic compaction
  (`session_model_call.go:476-500`) persists CHECKPOINT/summary turns
  via `writeTranscriptLocked` → `PlaceSession`
  (`session_compaction.go:932-946`, `session.go:2732-2734`), which
  JOINS the running execution as its first entry
  (`placement.go:125-128`); the compaction events fire afterward
  (`session_compaction.go:972-978`) but their facet rows carry
  context/meta only (`thread_envelope.go:288-289`). History names
  `turn_m1`; no configured row refreshes the metadata frontier.
  Verified by me at every hop.

Round 12's lesson was "name the wire carriers." Round 14's is harder:
**name the whole round trip.** My pass rules demanded end-to-end
carrier chains and I stopped the fetch chain at the hub; I ordered a
fold without arithmetic; the population rule was stated but the walk
was a survey of entry modes, not of the writer set. The seats are now
auditing my fixes as sharply as the original design, and this round
they found more in the fixes than in the design they closed.

## The findings, verified

- **S14-1** (significant, spec 3): the compaction first-persist path
  (above). Disposition 5.
- **S14-2** (significant, spec 2): the daemon-to-hub fetch carrier
  (above). Disposition 1.
- **S14-3 = R14-R-4 = N14-T-2** (significant, spec 4; three seats,
  three angles): the roster segment's fold (above). Disposition 2.
- **R14-R-1 = N14-T-3** (significant, spec 2; two seats): the fetch
  has no range-size rule — the grammar refuses `maxBytes`, the gate
  (`managerReadWithinCap`, `session_reads.go:244-252`) marshals the
  built response and REFUSES over-cap, so a valid item near the cap
  dead-ends instead of streaming. The response half of the gap
  R13-R-1 closed. Disposition 1.
- **N14-T-1 = R14-R-5** (significant by T's dynamic compile proof, minor
  by R's sizing; reconciled SIGNIFICANT — a compile break in the
  design's own carrier, proved at both sites): the list carrier and
  comparability (above). Disposition 3.
- **R14-R-2 = S's S13-2 residual** (minor, spec 3; R static, S
  dynamic): the goal journey's red barrier (above). Disposition 4.
  R13-R-2 and S13-2 stay PARTIAL until the barrier lands.
- **R14-R-3 = N14-T-6** (minor, spec 3; two seats): the new event has
  no payload or registration contract — `EventData` is a sealed set
  (`eventdata.go:5-24`, the unexported `eventKind` marker; the kind
  derives from the payload), the stream is public to projectors, CLI
  renderers and the bridge (`events.go:1-5`). Disposition 6.
- **N14-T-4** (minor, spec 4): the clamp drops composites the VOICE
  question surface looks up — `slices.Contains(pending.Calls, ...)`
  at `voice_questions.go:20`, called with the row at
  `voice_speech.go:105`; a still-pending composite beyond K gets
  empty spoken text, against the "no client behavior change"
  non-goal. Verified by me. Disposition 7.
- **N14-T-5** (minor, spec 4): `CallsTruncated` is minted in the
  accessor, which is forbidden from clamping and takes no limit
  (`session_tools_ask.go:101-112`) — the mint site contradicts the
  clamp-at-projection rule two sentences later. Disposition 8.
- **N14-T-7** (minor, spec 4): the clamp's "everywhere the row
  serves" is false across the remote hop — `translateThreadRaw`
  passes everything but ref/capabilities fields byte-for-byte
  (`remote_hub_refs.go:530-541`, the `ThreadReadResponse` case at
  :687), so a pre-clamp REMOTE daemon still ships the whole array and
  the hub's read-cap refuses the Manager read. Verified by me.
  Disposition 9.

Also verified and NOT findings: the count-slice exclusivity and every
decoder arm (R, T); the funding head-count — itemLimit max really is
40 (`appwire/paging.go:8-22`), the elision leg is smaller than the
bound (T); the clamp metadata's total input closes (S); the
held-attention construction is feasible (S); the first-record
emission is placeable after the read-back with `Session.mu` not held,
though the sampler must obey `session_envelope_sampling.go:22-43`
(Session.mu only, never `attentionMu`) — folded into disposition 6;
the event's other consumers default-ignore harmlessly (S: wildcard
watches accept only their subset, the activity meter whitelists
kinds); the `CompositeTotal` chain is complete end to end (R); no new
fence finding from any seat.

## Disposition — one revision pass, then round 15

1. **The fetch's whole round trip, named (S14-2 + R14-R-1 +
   N14-T-3).** The serving-daemon leg: `ThreadReadResponse`
   (`appwire/types_turn.go:367-402`) gains an additive fetch member
   REUSING the named fetch-response payload type (position, version,
   totalLength, offset, data, continuation); the daemon's read arm
   assigns it, the hub's FETCH arm reads it and assigns the outer
   `SessionOperationResponse.Fetch` member, and the remote
   translation passes the new field through byte-for-byte by its own
   preserve-unknown design — stated. The chain is named at every
   hop: cursor in (`fetchCursor` on `ThreadReadParams`) → daemon arm
   → `ThreadReadResponse`'s member → hub arm → outer member →
   Manager. And the range-size rule: the hub mints ranges of a
   CHOSEN byte length (a named constant), sized so the ENCODED fetch
   response — identity fields, `totalLength`, `offset`, base64 data
   at 4/3 expansion, and the origin-bearing continuation cursor —
   fits the 256KB whole-read cap by construction; the margin is a
   measurement obligation on a near-cap fixture (longest actor ids,
   longest cursor encoding at the new form); the continuation cursor
   advances past each range so any item streams in range-sized
   pieces; the cap gate stays as the backstop, and a VALID item never
   reaches it as a refusal.
2. **Segment budgets replace the fold (S14-3 = R14-R-4 = N14-T-2).**
   The whole-delivery-fits-4096 claim is withdrawn. The frame BODY
   keeps `watchFrameMaxChars` unchanged. The roster payload is its
   own segment with its own chosen bound and a STRUCTURE-PRESERVING
   truncation contract: whole-entry granularity (never cut JSON),
   a marker naming the dropped count, and the recovery — the
   Manager's own roster read (spec 1's fleet snapshot), stated. The
   bound's value is a measurement obligation against the real fleet
   shapes (a multi-lane initial snapshot AND a 256-event delta at
   the real encoding — entry COUNT is part of the measurement, S's
   32-entry case is the floor already exceeded today at 4,847
   runes). The self-influence notice and the job-read annotation
   keep their by-design post-cap behavior (their own comments say
   so). The K fixture stays for the frame's own composite-list
   segment; a new fixture pins the roster segment's entry-count
   boundary.
3. **The list carrier and the comparison it breaks (N14-T-1 =
   R14-R-5).** The frame's composite list rides `AttentionEntry` as
   its declared carrier, and the design owns the comparison
   replacement: a typed equality helper (field-wise, the list
   compared with `slices.Equal`, nil-list equal to empty-list)
   replacing the value comparison at `job_watch_hub.go:173` and the
   `maps.Equal` at `attention_subscriptions.go:150` (as
   `maps.EqualFunc`). Both landing zones named; the helper is owned
   work.
4. **The goal journey's barrier moves into the emit-to-record gap
   (R14-R-2 = S13-2 residual).** The red's construction: block the
   transcript record between the emission and the persist, drain the
   authoritative consumer (the only schedule that forces the
   marker's pre-persist sample), release the record, then hold the
   provider — metadata previous / history new, deterministically. S's
   proof (the green-shaped observation on pre-reorder code under a
   post-hold-gated consumer) is the evidence the barrier is required.
   The signal journeys (below) are absence-deterministic and need no
   barrier; the asymmetry is stated.
5. **Centralize the first-record signal (S14-1; supersedes the
   round-13 attention event).** The placement layer already computes
   first-ness: `openTurn.fresh` exists to stamp `TurnKind` on the
   first recorded entry (`placement.go:73-77`), and every Place mode
   routes through the same join (`:117-128`) — the choke point. *[Amended
   in place during the round-14 pass: the pass's first draft said
   "every Place mode routes through the same join", an overclaim caught
   by the pass's own choke-point rule — `placeVerbatim` and
   `PlaceInTurn` never join a running execution (`:102-103`,
   `:112-116`), `placeCompletion` skips while the running turn is
   fresh (`:104-108`); the closure is over the ENTRY POPULATION (an
   entry reaching the running turn outside the join would already
   violate the layer's own `TurnKind` contract), and the signal keys
   on the RUNNING turn's flip, not a prelude's or a gap's
   (`:129-134`).]* The
   design: the Record path surfaces the fresh-join fact additively,
   the session emits ONE post-persist signal when a running
   execution's first entry lands — write-then-emit at the record
   sites — and the facet map gains ONE row for it carrying the
   frontier facet. The population claim becomes closed BY
   CONSTRUCTION over the choke point, not by survey: all six named
   paths (user input, steering, goal continuation, assistant text,
   async attention, compaction) and any future mode funnel through
   it. `EventDelegateAttentionDelivered` is superseded by this
   signal; the held-attention journey stays as the attention-path
   red asserting the signal, and journey 8 gains S's held-COMPACTION
   sibling as the closure red (first entry kind CHECKPOINT during
   the held provider; metadata previous before the signal, new
   after; history new throughout). The four existing mode rows keep
   their frontier facets (verified correct; the double refresh is
   an idempotent sample of the same source).
6. **The signal's payload and registration (R14-R-3 = N14-T-6).**
   Payload: the first entry's stable turn id — provenance only; the
   frontier is resolved by the SAMPLER at sample time from the
   source, never guessed from the payload (S's warning: the placed
   attention may be a delivery turn; the signal's id is the placed
   entry's own). Registration is owned work named against the sealed
   set: the payload type with its `eventKind()` marker, the kind
   constant, the payload→kind table entry, the compile-time
   assertion (`eventdata.go:5-24`, `events.go:15-180`). The consumer
   disposition is stated (S's verification: wildcard watches accept
   only their supported subset, the activity meter's whitelist
   ignores other kinds — default-ignore; job-watch fanout and the
   bridge see it). The sampler obeys
   `session_envelope_sampling.go:22-43` (Session.mu only, never
   `attentionMu`).
7. **The voice consumer migrates with the clamp (N14-T-4).** The
   clamp's consumer list names the voice question surface; its
   lookup (`voice_questions.go:20`, called from
   `voice_speech.go:105`) resolves beyond-K composites through the
   same paged pending-ask read, and the "no client behavior change"
   non-goal is amended to name this migration.
8. **`CallsTruncated` mints at the projection (N14-T-5).** The
   clamp fact is minted at the wire projection where the clamp lands
   and K is known; `CompositeTotal` and `CalllessCount` stay
   accessor-minted (whole-set facts under the one lock). The two
   sentences reconcile; fence-spike's §6 chain text is amended to
   match.
9. **The remote hop's rollout honesty (N14-T-7).** The clamp lands
   on the serving daemon's projection; a remote thread's row is
   clamped only by the remote daemon's version. The byte-for-byte
   forward is by design (`remote_hub_refs.go:530-541`); a pre-clamp
   remote daemon ships the whole array and the hub's read-cap
   refuses the Manager read TYPED. The spec states the degradation
   and the rollout: remote hubs pick up the clamp on upgrade, and
   until then the typed refusal — never a silent path — is the
   lane's behavior.

README: round-14 trail (fourteen rounds; the honest shape above).
fence-spike: a round-14 note (the fence converged through its sixth
re-audit, zero findings from all three seats) plus the §6 amendment
for disposition 8. Round 15: the same three seats, closure audits of
their own round-14 findings plus fresh attack on the new text — with
one added instruction: the first target of the fresh attack is MY
round-15 pass text, applied with the same end-to-end-walk discipline
the record now states.

## Readiness after this round

Spec 1 READY from all three seats (fifth consecutive). Specs 2, 3
and 4 NOT READY from all three seats — the round-13 pass broke
ground three seats had judged ready. The count went 7, 5, 4, 5. The
convergence rate is the highest of any round (the fold drew all
three seats; the fetch, the event contract and the comparability
drew two each). The fence holds at zero findings across six
re-audits.
