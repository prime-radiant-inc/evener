# Round 10 — reconciliation and disposition

Reviewed at commit `2912b81b54` (the round-9 revision pass), code base
`2be411cd8f`. Seats — the same three round-9 families re-tread a second
time, each auditing closure of its OWN round-9 findings before fresh
attack, all leaves, high effort, all three ran the spike's red tests
themselves (every red failed for exactly the documented reason, zero
disputed, all four characterizations passing):

- **R** `codex-jesse-fsck.com/gpt-5.6`
  (dlg_034tUX28q4JnutgyNFgKls), run 14:08–14:26: 4 significant +
  2 minor; 7 of 8 round-9 closures CLOSED, R5 PARTIAL.
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034tUX5mnNLzCSOGKTq3R0), run 14:08–14:21: 2 significant +
  1 minor; 5 of 6 CLOSED, S3 PARTIAL.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034tUX7fNjNdq5oip9obuw), run 14:08–14:20: 4 significant +
  7 minor; 11 of 12 CLOSED, T3 PARTIAL.

Tally: **10 significant + 10 minor, with heavy three-seat
convergence; spec 3 is READY from all three seats — the first spec at
the bar.** Every load-bearing claim was re-verified in-tree by me at
the exact hop named (liveEntries' protocol/restart filters, the
per-call vs per-question units, ThreadReadParams' field set, the
turn-id forms, deliverSteering's active-execution strip, the carrier
claim's journal names, the continuation validator's non-earlier rule,
the unfenced answer-link tests, ThreadList's request shape, the two
extra NewSession callers). Each seat's one PARTIAL verdict is exactly
its own new significant — the closure audits are self-consistent.

## The convergence classes

- **The K-bound's roll-in has no truthful home (R1 = T-N4 ≈ S-N3,
  graded minor by S).** Shape (c) splits only type-excluded and
  cap-withheld populations, so a selected budget-elided item beyond
  the Kth line fits neither; S adds that served-but-truncated
  per-item markers are also unbounded (itemLimit max 40 legal), so
  the "at most K per-item elision lines" claim does not bound the
  cursor-bearing line classes the response can legally carry.
- **The hub-compat fetch cannot re-derive an item that has scrolled
  out of the newest legacy page (R2 = T-N3).** `ThreadReadParams`
  has no position selector; the page is bounded; purity does not
  freeze a latest-page result. Forty newer items between window and
  fetch make the cursor's target unreachable with no typed failure
  and no recovery path — on exactly the arrangement the round-9 fix
  added. (R also attacked the adjacent origin-tamper angle and found
  the identity fence holds: the resolver's independently supplied
  identity rejects a tampered cursor stale.)
- **The frame's counts are in different units (T-N1).** Composites
  are per-CALL (the state dedupes by (TurnID, CallID); one call
  carries many questions) while the frame's existing count is
  per-QUESTION (`len(s.askPending)`): "the count says truncation"
  falsely fires, and the paged read's completion rule cannot be
  built from the two numbers as specified.
- **The steer-writer narrowing breaks two runnable contracts
  (S-N1 + S-N2 ≈ T-N6).** Today `deliverSteering` deliberately
  strips the active execution from `AnswerToTurnIDs` ("cannot be its
  own predecessor"; the continuation validator rejects non-earlier
  edges and a test pins it) and `claimSteeringCarrier` stores answer
  names on the journal record, with tests requiring unfenced steers
  to retain the pending execution link inline, via carrier, and
  across restore. The unconditional "never live samples, exactly the
  captured executions" rule writes a self-edge for an inline fenced
  steer and leaves unfenced/legacy steers with no answer links at
  all.
- **`legacyCount` never reaches the resolving turn (R3).** The
  additive `schema.Turn` field is specified to name resolved CALLS
  only; restore reconstructs from the turn, so the call-less
  count has no durable carrier there, and the drain's capture-list
  union has no count semantics (two records capturing the same
  legacy set would sum and over-clear).

## Seat-unique significant findings, all verified

- **R4 (spec 4):** the pending-ask pager has no callable route —
  the probe's read is fleet-shaped `ThreadList`
  (`hubcore/prober.go:71`, `ThreadListParams` carries none of
  ref/pair/offset/limit), `ThreadReadParams` is a different method
  and also lacks the page fields — and no page bound: a caller-set
  `limit: 4000` recreates the whole-array response the pager exists
  to kill.
- **T-N2 (spec 1, graded minor by R, verified significant-graded by
  T):** the "ONE shared eligibility predicate" cannot preserve all
  four consumers' current populations — `liveEntries` adds protocol
  and restart-required filters (`local_daemon.go:1432-1440`) that
  the raw-iterating session-tools consumers never applied. R's
  framing is the disposition: routing session-tools through
  live-route eligibility is a sensible intended correction, but the
  spec must NAME and PIN the behavior change instead of describing
  the populations as already uniform.

## Verified minors

- **T-N5:** the relay pair's argument names are absent from the
  Manager tool edge — `invokeHubInput`'s key allowlist
  (`session_tools_hub.go:559-566`) and params construction
  (`:588-596`) are the landing zones and are unnamed work.
- **T-N7:** the frontier facet's event row is not named, and the
  obvious row (`EventTurnStarted: facetWork`) stamps BEFORE the
  first persisted entry (spec 1's own grounding), so it would name
  the previous frontier mid-turn; the post-persist rows
  (`EventUserInput`, `EventToolCallEnd`, `EventAssistantTextEnd`)
  are the ones that can carry it.
- **T-N8:** the origin field is not stated as required and
  validated — a paging cursor presented as `fetchCursor` (or a
  pre-upgrade cursor) decodes with an empty origin and has no
  routing; the shared encoding validates version and identity only.
- **T-N10:** the 308KB figure is id-form arithmetic: with the
  client-mutation `turn_m<N>` ids that a Manager-relayed ask always
  carries, 4,000 composites measure ~254KiB, UNDER the cap; the
  308KB number holds only for fresh long ids. The pass rule's third
  casualty (after 4KiB and 10KiB) — the figure must be restated with
  both forms or made an obligation.
- **T-N11:** the boot-generation wiring list omits two `NewSession`
  callers outside serve.go (`agent/subagents.go:890`,
  `cmd/evener/run.go:469`), so "set on every path" and the
  journey's "never a zero value" are false as written for those
  paths (harm low: subagents are root-only-ask-excluded).
- **R6 = T-N6-adjacent = S's S4 note:** the all-zero-capture-against-
  nonempty refusal is unreachable under an exhaustive capture (every
  pending question contributes a call or increments legacyCount);
  the branch is a malformed-state guard, not a normal admission
  case, and the journey as written is dead.

## What survived (all three seats)

Spec 3 is READY from all three seats: the frozen legacy method, both
skew directions, the cached old-decoder reload, the new method's
ownership, and the frontier landing zones all verified at their hops,
with only T-N7's event-row naming left. Spec 1's display/routing
split is implementable and verified at every consumer; the remaining
work is naming the predicate's scope and the behavior change. The
fence's core (admission-transaction comparison, scoped clear, receipt
-is-refusal, common prepare) survived untouched; round 10's fence
findings are all in the NEW round-9 text (units, steer writers,
count carriers, pager route) — the same shape as round 9. All five
reds still reproduce exactly; all four characterizations pass; the
closure audits confirm 23 of 26 round-9 findings CLOSED by their own
finders.

## Disposition — one revision pass, then round 11

Decided fixes:

1. **One K for all cursor-bearing per-item lines, and a truthful
   aggregate for every overflow (R1 + T-N4 + S-N3).** The response
   carries at most K per-item cursor-bearing lines TOTAL —
   truncation markers and budget-elision lines share the bound.
   Overflow of EITHER class rolls into the turn's ONE aggregate
   record, which gains the populations it needs to stay truthful:
   per-type excluded counts, the cap-withheld remainder, a
   truncated-beyond-K count, and an elided-beyond-K count. Recovery
   per class, named: type-excluded → the named re-read;
   cap-withheld and elided-beyond-K → the same-view continuation;
   truncated-beyond-K → the named re-read at a wider byte bound (a
   fresh view re-mints the cursors). The max-case fixture grows a
   max-itemLimit truncation leg (40 served-truncated items at K,
   measuring the real version/origin-bearing cursor encoding), and
   the floor obligation covers both legs.
2. **The hub-compat fetch's bounded recipe and its typed absence
   (R2 + T-N3).** The hub-compat fetch re-derives through the same
   legacy read, paging backward with the EXISTING exclusive `Before`
   boundary (`ThreadTurnsListParams:540-551`) within the window's
   own turn span, a bounded traversal. If the target is not reached,
   the fetch fails the typed `item-unreachable-on-compat` naming
   the recovery (re-read the window, which re-derives fresh state
   and mints fresh cursors); a changed version still fails
   `item-changed` first. The journey gains the scroll-out case: 40
   newer items, fetch fails typed, re-read recovers. No held-page
   cache is introduced (no new hub state).
3. **The counts keep their units and the page owns completion
   (T-N1).** The frame's question count stays what it is — a fact
   about the set, never a truncation signal and never a completion
   rule. The frame's composite list carries its own truncation
   marker (a `truncated-list` flag or `nextCompositeOffset`); the
   paged read's `{composites, total, nextOffset}` — `total` a
   COMPOSITE count — owns iteration to completion
   (`nextOffset` empty). No second question-count copy exists
   (contract 2's one-owner rule holds).
4. **The steer writers scoped, and the link/evidence split named
   (S-N1 + S-N2 + T-N6).** The captured-calls field is
   call-resolution EVIDENCE and may name the active execution's
   calls. `AnswerToTurnIDs` is a LINK field and keeps the existing
   active-execution exclusion in every regime: a fenced resolving
   turn writes its links from the capture MINUS the active
   execution. UNFENCED and legacy records keep TODAY's behavior
   exactly — claim records live names at carrier claim, delivery
   unions live and recorded names, active execution stripped — so
   the runnable answer-link contracts (inline, carrier,
   restore-retry, journal-copy isolation) hold unchanged. The
   narrowing journey gains the inline fenced-steer case (self-edge
   excluded, evidence intact) and the
   accepted-before-upgrade-delivered-after replay case (today's
   link behavior preserved).
5. **`legacyCount` rides the resolving turn, and the union takes the
   max (R3).** The additive `schema.Turn` field carries the capture
   it consumed — `(calls, legacyCount)` — so restore subtracts the
   named calls AND that many call-less questions from one durable
   carrier; the failure writers and `recordClientMutationFailure`
   copy the same pair. The drain's capture-list union: calls union
   with dedupe; `legacyCount` takes the MAX across the list, never
   the sum — call-less questions are unnamed, two records capturing
   the same legacy set must clear it once, and the reason is stated
   (the call-less population is restore-derived and static between
   restores; summing over-clears after an intervening restore).
6. **The pager gains its route, its bound, and its degradation
   (R4 + T-N9).** The paged pending-ask read is named owned wire
   work: an additive page request on the ref-addressed thread read
   (behind the same capability bit that gates the fence's row
   machinery; the strict decoder accepts it on new daemons; the hub
   never sends it to an old one), served by the state accessor the
   row projection already samples — one `s.mu` hold per page. The
   response carries `{composites, total, nextOffset}` additively.
   The caller-supplied `limit` is normalized server-side to a
   maximum; the maximum's value is a MEASUREMENT OBLIGATION on the
   max-page fixture (real composite encoding, both id forms),
   never an asserted number — the same rule as spec 2's floor.
   Against a daemon without the bit: the frame's K-list is all a
   Manager gets, the truncation marker names the unavailability,
   and no page request is sent (absent-when, never fabricated).
7. **The predicate's scope and its one behavior change named
   (T-N2 = R's minor 5).** The ONE predicate owns the CRASHED and
   read-only-alias discriminators — one gate for the concern the
   spec is about. `liveEntries`' protocol and restart-required
   filters are a SEPARATE existing concern, kept where they are,
   named. SessionToolsThread and RunSessionTools adopt the
   predicate; for non-alias entries this changes nothing (today's
   inline alias skips become the predicate's parameter), and the
   one behavior change is exactly the spec's purpose: crashed
   entries become unroutable on those two consumers, stated as a
   deliberate change from today's raw-iteration behavior, pinned by
   a journey case (a crashed non-alias entry: displayable, refused
   on all four routes).
8. **The frontier's facet rows named (T-N7).** The frontier facet
   refreshes on the post-persist event rows — `EventUserInput`,
   `EventToolCallEnd`, `EventAssistantTextEnd`, the rows that
   follow a persisted entry — and NOT on `EventTurnStarted`
   (which fires before the first persisted entry and would stamp
   the previous frontier mid-turn; spec 1's own in-flight-window
   grounding is the reason, stated).
9. **The minors.** The relay pair's argument keys named beside
   `expected_instance_id` (`expected_ask_revision`,
   `expected_ask_boot_generation`) with the `invokeHubInput`
   allowlist and params construction as the named landing zones
   (T-N5); the origin REQUIRED and validated on fetch decode, an
   empty-origin cursor failing typed — closing the paging-cursor-
   as-fetchCursor hole (T-N8); the 308KB figure restated with both
   id forms' arithmetic (the fresh-id worst case over, the
   client-mutation common form at the cap's edge before the
   envelope) and the design's conclusion independent of either
   number — the whole-array path is killed by construction, not by
   the arithmetic (T-N10); the wiring list names the two extra
   `NewSession` callers and scopes the journey's zero-value claim
   to the paths that can hold a pending ask (T-N11); the all-zero
   refusal restated as a malformed-state invariant guard with the
   journey relabeled an injected-invariant test, not a normal
   admission case (R6).

README: round-10 trail. Round 11: the same three seats — the
closure-audit pattern has now verified 23+25 of the last two rounds'
fixes and every PARTIAL became a named significant, so the seats
hold the complete map of their own findings.

## Readiness after this round

**Spec 3 READY (all three seats).** Spec 1 one fix (7) from ready.
Specs 2 and 4 carry the four convergence classes and two seat-unique
significants, all with decided shapes that extend mechanisms the
rounds have already verified (the aggregate record's populations,
the legacy Before-boundary, the existing link exclusion, the
accessor's one-lock sampling, the capability-bit gating) — none
touches the fence's settled core.
