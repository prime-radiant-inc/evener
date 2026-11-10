# Round 12 — reconciliation and disposition

Reviewed at commit `37cff6e2ec` (the round-11 revision pass), code base
`2be411cd8f`. Seats — the same three families, a fifth pass, each
auditing closure of its OWN round-11 findings before fresh attack, all
leaves, high effort, all three ran the reds uncached with `-v` (every
red failed for exactly the documented reason, zero disputed, all four
characterizations passing):

- **R** `codex-jesse-fsck.com/gpt-5.6`
  (dlg_034tW6qbVFT2dX4PwGpnT8), run 15:13–15:26: 4 significant +
  1 minor; R11-1, R11-2 and R11-3 all PARTIAL (each fix closed its
  finding's core and stopped one hop short of the wire).
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034tW6scH7WogPStmCTKl8), run 15:13–15:24: 2 significant +
  1 minor; all four round-11 closures (S-N1, S-N2, S-N3, the
  empty-link minor) CLOSED at design level.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034tW6tyCyBRSKew9paXE0), run 15:13–15:24: 1 significant +
  7 minor; 6 of 9 round-11 closures CLOSED, T-S1, T-S3 and T-M1
  PARTIAL.

Tally: **5 distinct significant + 7 distinct minors**, with the round's
heaviest convergence yet on two axes: all three seats independently
found the frame's pageability reason has no carrier
(R12-R-5 = T-N1 = S12-3), and two seats independently found the pager
leaves the whole unbounded `PendingAsk.Calls` array on the row every
metadata read returns (R12-R-4 = S12-2 — S measured it: 316,555 bytes
at 4,000 fresh-id calls, already over the 262,144-byte cap, 260,555 at
`turn_m<N>` ids). Every load-bearing claim was re-verified in-tree by
me at the exact hop named.

## The honest shape of this round

Every significant sits in the round-11 pass's own new text, and four of
the five are the SAME defect at the type layer: the pass named
concepts — the label, the reason, the page — but never named their
WIRE CARRIERS, so each fix closed its finding's semantics and stopped
one hop short of an implementable field. Round 10's pager fix stopped
one hop short of a callable tool; round 11's fixes stopped one hop
short of the types. The fifth is a genuine completeness discovery: the
frontier refresh set still misses a third entry mode.

- The frame's `pageable`/`unavailable` reason was wired to "the same
  row the fence's common prepare reads" — but the frame builder
  serializes `AttentionEntry` fields only (`agent/job_watch_hub.go:213-225`),
  `AttentionEntry` carries no capability
  (`appwire/attention.go:10-30`), the broker derives it from a
  probe-cached `LiveEntry` while DROPPING the capabilities it holds
  (`cmd/evener-hub/internal/hubcore/attention.go:87-113`;
  `cmd/evener-hub/internal/hubcore/roster.go:56-64`), and the fence's
  prepare reads a
  LIVE row (`cmd/evener-hub/app_relay_read.go:164-212`). Three seats;
  the Manager-side edge has no executable data path. My own round-11
  text.
- The pager rode `ThreadReadResponse` — whose `Thread` is MANDATORY
  (`appwire/types_turn.go:367-369`) and whose row clones the complete
  `PendingAsk.Calls` slice unconditionally
  (`server/appwire_thread_projection.go:117`,
  `appwire/types_pending_ask.go:19-25`), riding every metadata read
  regardless of `IncludeTurns` (`server/appwire_read_handlers.go:204-216`)
  under the 256KiB whole-response cap
  (`cmd/evener-hub/session_tools.go:396-403`). So my round-10 claim
  "the paged read kills the whole-array path" is FALSE at the layer
  where the array actually lives: the row. A 4,000-call page request
  dies at the same cap before the bounded page helps. Two seats plus
  one independent measurement.
- The truncation label, the elision cursor and the page response were
  all described as content with no field names: `ThreadItem` carries
  no truncated flag, byte count or `fetchCursor`
  (`appwire/types_turn.go:181-267`), `ThreadReadResponse` carries no
  page field, and no tool-result projection for a page exists.
- The traversal cap's value was "derived" from a quantity that does
  not exist (the widest window's turn span; `{latest: N}` is
  unbounded, and displacement is caused by unbounded NEW arrivals,
  not the original window's span).
- And S12-1: goal continuations are a THIRD execution-opening entry
  mode my round-11 row set missed — `acceptContinuationInput` emits
  `EventGoalContinuation` at `agent/session_lifecycle.go:2415` BEFORE
  `recordTurn` persists the execution's steering entry at `:2416-2419`,
  emits no `SteeringInjected` when no pending steering exists, and the
  facet map's `EventGoalContinuation` row serves goal/queue/ask
  facets only (`server/thread_envelope.go:300`). A clean goal
  continuation held at its first provider response leaves the metadata
  frontier naming the PREVIOUS execution through the whole wait —
  the exact disagreement the spec exists to remove. Verified by me
  at the hops; the emission order is real and the journey's
  hold seam already exists
  (`agent/session_drain_as_steer_turn_boundary_test.go:48-55`).

Against that: every round-11 fix's SEMANTICS closed — R, S and T
verified the label/cursor rule, the traversal's stopping rule and
precedence, the one accounting, the frontier's steering/notification
rows, the pager's request route, the call-less count's computability
(T: the accessor already walks the questions, so the count is free
under the same lock — the atomicity claim held), the empty-link edge
(S traced the full delivery path: nil observations are skipped before
communicate synthesis, so the polarity is right), the drain MAX
argument (S attacked the containment claim and found no reachable
disjoint population), and the fixture basis. The fence core remains
converged: no seat found a fence defect, and every seat re-ran the
reds clean. Spec 1 is READY from all three seats for the third
consecutive round.

## The findings, verified

- **The frame's pageability reason has no carrier (R12-R-5 = T-N1 =
  S12-3, all three seats; T and R grade it significant, S minor with
  the hub-side refusal as mitigation).** The Manager-side degradation
  edge of R11-3's fix is unimplementable as written; the fix names the
  carrier chain and the cached-hint/fresh-gate semantics (disposition
  1).
- **Page mode still ships the whole array it was introduced to avoid
  (R12-R-4 = S12-2, two seats + measurement).** The row's
  `PendingAsk.Calls` is unbounded on the mandatory `Thread`; the
  pager never disposes of it (disposition 2).
- **The truncation label has no wire carrier (R12-R-1).** The
  never-silent contract names label CONTENT but no field on the
  closed generated `ThreadItem` (disposition 3).
- **Goal-continuation executions have no frontier refresh (S12-1).**
  A third entry mode, with the emit-before-persist ordering named as
  the code fact the design must change (disposition 4).
- **The page has no response field or tool-result projection
  (R12-R-3, folded into disposition 2).** `ThreadReadResponse` is
  closed; the "existing tool-result projection" a page would ride
  does not exist.

Minors: the frame's K measurement is circular (its fixture needs K to
build) and the marker/reason are undeclared fields, with contract 4's
payload list omitting the list/marker/count (T-N3); the frame's
call-less count has no carrier to the frame — `PendingAskState`
carries no count field (T-N7); spec 2's fetch names no
`SessionOperationRequest` member (T-N2); the envelope-ceiling sentence
still derives the envelope from content lines the one accounting just
made content (T-N4 = T-S3's PARTIAL); the traversal cap's value is not
computable (T-N5 = R12-R-2 = R11-2's PARTIAL); the fetch-time
`PageBefore` source and skew are unnamed, and the remote rebuild's
unconditional masking makes journey 10's success leg local-only
(T-N6 — R's correction: the bit is hub-owned from the serving
arrangement, `cmd/evener-hub/app_threadread.go:892-934`, NOT the
probe-cached daemon bit; the fetch still needs its source named);
README rule 4's exception list predates spec 4's new typed page
refusal (T-N8); and spec 4's "used today by that metadata read alone"
is false — `agent/job_watch_hub.go:958` also submits
`SessionOperationRequest.Read` for held-snapshot/answer-continuation
lookups (T-S1's residue; additive page keys affect it not).

## What survived

The fence: zero new defects across all three seats, every round-11
fence-adjacent closure verified, all five reds reproducing exactly for
the documented reasons and all four characterizations passing, run
uncached with `-v` by every seat. The one accounting is now
authoritative (S hunted residue and found none beyond T-N4's one
sentence). The traversal's stopping rule and precedence held (R
verified the lexicographic position order at
`internal/appitempaging/page.go:196-214`; the 40-arrival success
journey is correct). The empty-link edge held with its full delivery
path traced. The drain MAX's nested-population argument held under a
dedicated attack. The call-less count's computability held (the
questions walk is already inside the accessor's one lock). Spec 1
READY from all three seats, third consecutive round.

## Disposition — one revision pass, then round 13

Decided fixes:

1. **Carry the pageability fact to the frame (the three-seat
   significant).** `AttentionEntry` gains an additive ask-pageable
   capability fact, derived at `DeriveAttentionFromRoots`
   (`cmd/evener-hub/internal/hubcore/attention.go:87-113`) from the `LiveEntry` the
   broker already reads — the probe-cached capabilities the entry
   derivation currently drops (`cmd/evener-hub/internal/hubcore/roster.go:56-64`) —
   and the frame payload declares the reason field beside its
   `truncated-list` marker. The semantics are stated honestly: the
   frame's reason is a HINT from the probe cache that can trail the
   daemon's real capability; the hub-side typed refusal is the
   authority. A `pageable` hint against a bit-less daemon fails the
   typed refusal (fail-closed); an `unavailable` hint sends no page
   (fail-closed). The false "reads the same row the fence's common
   prepare reads" claim is corrected: the prepare reads a live row
   (`app_relay_read.go`), the frame reads the cached derivation —
   same originating row type, different moments, the staleness rule
   stated.
2. **Bound the row's calls array in the same change the page lands
   (the two-seat + measured significant).** The wire row's
   `PendingAsk.Calls` is CLAMPED to the frame's K composites with the
   set's total and the truncated-list marker — everywhere the row
   serves, not just page mode, because the array rides every
   `ThreadReadResponse` (the held-snapshot consumer included) and
   the pager is the whole-set read. The page-mode response gains its
   additive field: `ThreadReadResponse.pendingAskPage` carrying
   `{composites, total, nextOffset, calllessCount}` (owned wire
   work, regenerated types and validators per rule 5). The
   tool-result projection is traced end to end:
   `SessionOperationResponse.Read` returns the response raw
   (`agent/session_tools_hub.go:751-755`), the hub preserves the row
   and assigns the read (`cmd/evener-hub/session_tools.go:246-252`),
   `o.read` passes the source response through (`:557`), so the
   additive page field reaches the Manager's tool output — and the
   "used today by that metadata read alone" falsehood is corrected
   (`agent/job_watch_hub.go:958` is the second
   `SessionOperationRequest.Read` submitter; it sends no page keys
   and gains page fields only when requested). The max-page fixture
   measures the WHOLE Manager result with a large underlying set
   (the clamped row + the page under the 256KiB cap), with S's
   measurements (316,555 bytes at 4,000 fresh-id calls; 260,555 at
   `turn_m<N>`) named as the evidence of the unbounded today-shape.
   "The paged read kills the whole-array path" becomes true when the
   row stops shipping it.
3. **Name the truncation label's wire fields (R12-R-1).** The label is
   owned additive wire work ON `ThreadItem`: `truncated` (bool),
   `truncatedBytes` (the untruncated body length), `fetchCursor`
   (string) — the same item-level fields serving shape (a)'s
   on-item label and shape (b)'s elision line (an elided item rides
   as a `ThreadItem` carrying identity + label fields and no body),
   with the generated-types and appwire-client validator landing
   zones named. The never-silent contract becomes implementable
   against the closed generated types.
4. **Give goal continuations their frontier refresh (S12-1).** The
   design states the `EventGoalContinuation` emission MOVES to after
   `recordTurn`'s persist — write-then-emit, the rule every other
   frontier row follows; today's emit-before-record order
   (`agent/session_lifecycle.go:2415-2419`) is named as the code
   fact being changed, the marker-only text is preserved (the
   reorder moves timing, not content), the facet map's existing row
   gains the frontier facet, and the goal-mode blocked-carrier
   journey is added: a clean goal continuation (no pending
   steering), its first provider response held at the existing
   `heldLegAdapter` seam, a metadata read during the hold must name
   the new execution as the frontier.
5. **Make the traversal cap a named constant (T-N5 = R12-R-2).** The
   cap bounds COST, not termination (the transcript is finite;
   exhaustion fails typed with the named recovery). Its value is a
   named server-side constant whose default is CHOSEN — a
   round-trip cost budget against the peer — with journey 10's
   cap-exceeding leg deriving its own fixture from the chosen
   value at cap+1 pages and asserting the typed failure and the
   recovery at exactly that bound. (The pass corrected this
   disposition's first wording, which said the value was measured
   on a fixture derived from the value — the same circularity
   round 12 had just caught in the frame K.) The false "widest
   window's turn span" derivation is dropped. Displacement is
   caused by unbounded new arrivals; the cap is a round-trip
   budget, stated as such.
6. **Resolve the frame K's circularity (T-N3 = T-M1's PARTIAL).** The
   frame's K gets a NAMED starting value (10, the established
   idiom); the journey fixture builds K+1 composites (buildable
   now); the fixture MEASURES the max-case frame payload at that K
   and asserts it fits the watch delivery rail (a recording
   obligation, not a derivation — the obligation's direction is
   fixed: K is chosen, the payload is measured). The marker and its
   reason get declared field names on the frame payload; contract
   4's field list gains the composite list, the marker, the reason
   and the call-less count. The pager's limit maximum names its
   binding bound: the 256KB `managerReadWithinCap`
   (`cmd/evener-hub/session_reads.go:18`, applied at
   `cmd/evener-hub/session_tools.go:399-403`).
7. **Carry the call-less count's wire carrier (T-N7).**
   `PendingAskState` gains an additive `CalllessCount` minted inside
   the atomic accessor beside the pair (where the questions are
   walked), cloned with the state, riding the §6 chain (row
   projection → probe row copy → `LiveEntry` → `AttentionEntry` →
   frame) — the chain enumeration gains it beside the composite and
   the pair.
8. **Name the fetch's operation member (T-N2).** `SessionOperationRequest`
   gains an additive `Fetch` member carrying the decoded cursor; the
   `read_transcript` branch's new `fetchCursor` argument decodes into
   it, and scope 4's grammar-completeness claim names it.
9. **Fix the envelope-ceiling residue (T-N4 = T-S3's PARTIAL).** The
   K bound is attributed to what it bounds — the floor's funding
   story (a turn fragment carries at most K cursor-bearing lines and
   one record, which is what makes the floor sufficient by
   construction) — while the envelope's own content remains the
   window-level trimming report and nothing else. The ceiling
   sentence stops deriving the envelope from content lines.
10. **Name the fetch gate's source and skew (T-N6).** The fetch reads
    the same `PageBefore` bit the serving arrangement stamps —
    hub-owned for local and saved threads
    (`cmd/evener-hub/app_threadread.go:892-934`), not the probe-cached
    daemon bit windowed reads use — and the fetch-time rule is the
    uniform contract: a peer whose serving arrangement lacks the bit
    takes the newest-page re-derivation path and the same typed
    failure. Journey 10's success leg is scoped to a LOCAL compat
    peer, stated (the remote rebuild masks the bit,
    `cmd/evener-hub/internal/appsource/remote_hub_refs.go:134-158`).
11. **README rule 4's exceptions (T-N8).** The standing-exceptions
    list gains spec 4's typed `ask-fence-unsupported-on-daemon` page
    refusal — a fail-closed capability gate in spec 2's
    `window-unsupported-on-daemon` class, named so the spec does not
    violate the rule's letter.

README: round-12 trail. fence-spike: a round-12 note (the §6 chain's
enumeration extends to the count and the pageability fact — spec 4's
own additions, the fence core untouched). Round 13: the same three
seats — the convergence rate keeps climbing (three seats on one
finding, two on another), and the seats hold the complete map of
their own findings.

## Readiness after this round

**Spec 1 READY (all three seats, third consecutive round).** The fence
stays converged. Specs 2, 3 and 4 carry the five significants above:
four are carrier-name completions in the round-11 pass's own text
(the label's fields, the reason's chain, the page's response field,
the row's clamp) and one is a genuine new completeness discovery
(the goal-continuation entry mode). None touches the fence's settled
core, spec 1, or any round-11 semantics — the semantics all closed;
the types did not.
