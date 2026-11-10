# Round 11 — reconciliation and disposition

Reviewed at commit `d8abec8520` (the round-10 revision pass), code base
`2be411cd8f`. Seats — the same three families, a third pass, each
auditing closure of its OWN round-10 findings before fresh attack, all
leaves, high effort, all three ran the reds themselves (every red
failed for exactly the documented reason, zero disputed, all four
characterizations passing; S and T reran theirs uncached with `-v`):

- **R** `codex-jesse-fsck.com/gpt-5.6`
  (dlg_034tVGVksqdX2kwtfxQdyt), run 14:38–14:46: 3 significant +
  0 minor; 4 of 6 round-10 closures CLOSED, R2 and R4 PARTIAL.
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034tVGaFyIamo9V5JckqZr), run 14:38–14:49: 2 significant +
  1 minor; 3 of 3 CLOSED.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034tVGbQ0h76th6GMLjOtO), run 14:38–14:50: 4 significant +
  5 minor; 9 of 11 CLOSED, T-N1 and T-N3 PARTIAL.

Tally: **7 distinct significant + 4 distinct minors, with the round's
heaviest convergence — all three seats independently found the pager
still has no callable Manager-tool route** (R11-3 = S-N1 = T-S1).
Every load-bearing claim was re-verified in-tree by me at the exact
hop named, including the seat disagreement (below).

## The honest shape of this round

Most findings sit in text the ROUND-10 PASS ITSELF added: the K-bound
(R11-1), the compat traversal and its journey (R11-2 = S-N3 = M5),
the frontier event rows (S-N2), and the pager fix stopping one hop
short of a callable tool (the three-seat convergence). T-S3 attacks
an accounting ambiguity that predates round 10 but became
load-bearing when the K claim leaned on it; T-S2 is the units fix's
own uncovered case; T-S4 is the traversal's ungated population. Four
of seven significants are the round-10 pass's own new-text defects
or incompletions — the same pattern round 9 showed, and the reason
the disposition names its fixes against the pass's own additions.

Against that: **the fence is converged.** All three seats closed
every fence finding from round 10 (R3, R6, S-N1, S-N2, T-N4, T-N6,
T-N9, T-N10, T-N11 all CLOSED), and no new fence defect was found —
the link/evidence split, the pair-carrying turn field, the drain max
union, the wiring list, and the 308KB restatement all survived
attack. The remaining defects concentrate in the read/visibility
surfaces (spec 2's packer accounting and compat fetch, spec 3's
frontier stamping, spec 4's pager and mixed-set completion) — the
newest, least-iterated machinery, exactly where residual defects
should live. Spec 1 is READY from all three seats for the second
consecutive round.

## Seat disagreement, settled by code evidence

T dismissed the frontier-row attack ("the turn frontier is not
stale — the turn's assistant entry is already persisted"), which is
correct for tool-call-bearing turns. S attacked the STEER-OPENED
path and found the real defect: a steering carrier deliberately
writes no `TurnUserInput` (`acceptSteeringCarrierInput`'s own doc,
`agent/session_lifecycle.go:2553-2558`), the queue delivery
persists `TurnSteering` and then emits `EventSteeringInjected`
(`agent/session_queue.go:1060-1074` — the same row spec 4's
ask-facet refresh keys on), and the notification path persists its
steering BEFORE `TurnStarted` fires (`:2510-2529`), so my round-10
exclusion reason ("TurnStarted fires before the first persisted
entry") is false for notification executions. I verified every hop
directly, including the facet map's own comment that `ToolCallEnd`
is "the one row where the write-then-emit ordering does NOT hold"
(`server/thread_envelope.go:271-278`) — my round-10 text called
ToolCallEnd a post-persist row, which the map's own documentation
contradicts. **S is right; T's dismissal covered a different case.**
Round 10's T-N7 fix named the wrong rows.

## The findings, verified

- **The pager has no callable Manager-tool route (R11-3 = S-N1 =
  T-S1, all three seats).** Spec 4 names AppWire fields on
  `ThreadReadParams` and stops at the wire. The Manager's
  `job_status` hub schema accepts only `ref`/`jobId`
  (`agent/session_tools_hub.go:86-94`, closed schema,
  `DisallowUnknownFields` at :644-645) and its dispatch builds only
  `ThreadReadParams{Ref}` (:731-746); `read_transcript` maps to a
  different params family (:705-730). T adds: `SessionOperationRequest.Read`
  (`appwire/session_tools.go:84-99`) is used today only by that one
  metadata read, and T-S1's degradation has no decision point — the
  `truncated-list` boolean cannot distinguish pageable from
  unavailable, and neither the Manager-side check nor the hub-side
  refusal is named. Verified at every hop by me.
- **The K overflow hides which served items are truncated
  (R11-1).** Scope 3 promises truncation is "always labeled on the
  item ... never silent," but my round-10 shape (a) leaves items
  beyond K with truncated bodies, NO label, NO byte count, NO
  cursor — indistinguishable from complete items in the same
  response. Reachable at the max itemLimit (40 legal,
  `appwire/paging.go:8-21`): a mixed 40-item turn with more than K
  truncated items, all bodies fitting the budget. The aggregate
  says HOW MANY, never WHICH. My own fix's defect.
- **The compat traversal is not executable as written, and its
  journey demands the wrong outcome (R11-2 = S-N3 = M5).** "Within
  the window's own turn span" carries no stopping rule (the cursor
  holds the target position but the spec never uses it as the
  endpoint, and no maximum page count exists); the claimed
  version-before-reachability ordering is impossible (the hub must
  locate the item to read its version); and the journey's exact-40
  arrival case actually SUCCEEDS — `Before` is exclusive
  (`appwire/types_turn.go:539-551`) and `CandidatesBefore` returns
  the newest older page (`internal/appitempaging/page.go:201-214`),
  so one page-back reaches the former page. All verified by me at
  the hops.
- **The traversal is unavailable on pre-`Before` compat peers
  (T-S4).** The peer decodes `ThreadTurnsListParams` strictly
  (:573-582) and `ValidateThreadTurnsListParams` requires the field
  (`appwire/paging.go:32-47`); the codebase carries
  `remoteCursorlessBeforeRefusal` (`remote_hub_source.go:1632-1645`,
  issue #3283) precisely because older peers reject `before`. A
  daemon lacking `before` necessarily lacks the window bit, so it
  IS in the compat population — there the fetch surfaces the peer's
  raw `InvalidParams`, not the promised typed absence. Verified.
- **The per-turn aggregate records have contradictory accounting
  (T-S3).** Scope 5 measures "the complete encoded fragment —
  bounded bodies and their elision records together" (content) while
  scope 5's trimming paragraph says "Aggregate elision records
  (shape c) ... are envelope-only and bounded" (envelope). Under the
  envelope reading, N served turns can carry N records outside the
  budget and the by-construction cap claim is unproven; under the
  content reading the second sentence is false. Load-bearing: this
  is the spec's central measured-cap guarantee. Verified as a real
  contradiction in my own text.
- **Composite completion cannot cover call-less questions
  (T-S2).** A mixed set (named calls + call-less legacy questions)
  iterates the page to `nextOffset` empty while the call-less
  question is never enumerated — `PendingAskCalls` skips questions
  without both ids (`agent/session_tools_ask.go:102-114`) — so the
  sufficiency rule ("EVERY pending ask") is unsatisfiable by the
  page, and `question count − total` cannot decompose the
  difference (one call carries many questions). Verified.
- **The frontier rows miss the steering/notification persist
  (S-N2, above).**

Minors: the frame's composite bound K is never defined and is not
spec 2's K (T-M1); two load-bearing citations name functions that do
not exist — `claimSteeringCarrier` is `claimSteeringCarrierInput`
(`:379`) and `deliverSteering` is `consumeSteeringMessage`
(`agent/session_queue.go:959`), line ranges right, names wrong
(T-M2); the two-leg fixture's cited measurement basis is the OLD
cursor form while leg 2 is the new version-and-origin-bearing
encoding and K lines, not 40 (T-M3); the empty-link case of the
link/evidence split is unstated — a fenced inline self-resolving
steer yields an empty `AnswerToTurnIDs`, suppression correctly
does not fire (no communicate delivery either), matching today's
unfenced strip behavior, but nothing says so (T-M4 ≈ S's
attack-2 answer).

## What survived

Spec 1 READY from all three seats (second consecutive round). The
fence: every round-10 fence finding closed, no new fence defect, the
link/evidence split and its runnable contracts verified by S against
the real tests run uncached. The origin-required decode survived
attack (R found no break). The units fix's computability survived
(T: `total` is computable under the one lock; two pages of one
iteration cannot disagree on `total` while both pass the pair
fence). The drain max union survived (T: pre/post-restore records
can combine, but the call-less populations are NESTED — a restore
re-derives from the same durable transcript — so MAX remains right;
the STATED reason should be the nested-population argument, which
the pass also fixes). The failing writers survived (S: every
reaching path carries the identity; predating-fields records take
the legacy rule). All five reds reproduce exactly; all four
characterizations pass.

## Disposition — one revision pass, then round 12

Decided fixes:

1. **Split the truncation LABEL from the truncation CURSOR
   (R11-1).** Every truncated item ALWAYS carries its on-item label
   — the truncated flag and its byte count — with no exception
   (scope 3's promise restored); K bounds only the CURSOR-BEARING
   lines (fetch cursors on truncated items and elision lines
   share it). Truncated items beyond K are LABELED-CURSORLESS,
   counted in the aggregate's truncated-beyond-K population, and
   recoverable by the named wider-bound re-read — the caller
   always knows WHICH items are truncated because every one is
   labeled. The fixture's truncation leg measures 40 labeled
   items with K cursors at the new encoding.
2. **Make the traversal executable, honest, and correctly
   journaled (R11-2 + S-N3 + M5).** The stopping rule uses the
   cursor's OWN target position as the endpoint (positions are
   ordinals; page backward until the target position is reached
   or passed), backed by a server-side maximum page count whose
   value is a measurement obligation. The precedence is
   REACHABILITY-FIRST, stated: reach the item by position, then
   the version fence decides (`item-changed`); not reached within
   the cap → `item-unreachable-on-compat` naming the re-read; an
   item that both changed and scrolled out reports unreachable
   (the recovery is the same). The false version-first claim is
   dropped. Journey 10 gains the corrected pair: 40 arrivals →
   one page-back reaches the target, fetch SUCCEEDS with the fence
   holding (asserted); a fixture whose arrivals exceed the
   page-count cap → the typed failure and the re-read recovery.
3. **Gate the traversal on the peer's own capability (T-S4).**
   The backward traversal runs only on peers that advertise the
   EXISTING `PageBefore` bit (`appwire/types_thread.go:618-624`,
   the bit the remote rebuild deliberately treats as off;
   `remoteCursorlessBeforeRefusal` is the named evidence the
   pre-`Before` subpopulation is real). On a pre-`Before` compat
   peer the fetch attempts only the newest-page re-derivation
   and, when the target is absent, fails the SAME typed
   `item-unreachable-on-compat` with the same recovery — the
   typed contract is uniform across the whole compat population;
   the traversal is the gated recovery path, never an
   availability dependency.
4. **Name the pager's Manager-tool route and its decision point
   (R11-3 = S-N1 = T-S1).** The page request rides the
   `job_status` hub branch — the one read whose dispatch already
   builds `ThreadReadParams` (`agent/session_tools_hub.go:731-746`
   through `SessionOperationRequest.Read`,
   `appwire/session_tools.go:84-99`): its closed schema (the
   default branch at :86-94) gains the additive page keys (pair,
   offset, limit), its fixed decoder struct gains the fields, the
   dispatch maps them into `request.Read`'s new page fields, and
   the response carries the page through the read's tool-result
   projection. The DEGRADATION decision point is named at both
   edges: the frame's `truncated-list` marker carries its reason
   (pageable when the lane's row advertises the `AskFence` bit —
   the frame builder reads the same row the fence's prepare reads;
   unavailable when it does not, falling back to the
   content-locate rule), and a page request against a bit-less
   daemon fails typed hub-side with the fallback named —
   fail-closed like the fence's own prepare. The grammar stands
   alone; journey 5 pins a page through the ACTUAL Manager tool
   edge.
5. **Carry the call-less count end to end (T-S2).** The frame and
   each page response carry the set's CALL-LESS count — the same
   count the capture represents, computed under the atomic
   accessor's one lock where the set is already walked, never
   derived by subtraction. The sufficiency rule for a mixed set
   reads: page the composites to `nextOffset` empty, then serve
   the call-less half by the content-locate fallback with the
   call-less count as the completion bound. Journey 5 gains the
   mixed-set case: composites to completion, the call-less count
   matching, and the content-locate read finding the call-less
   questions.
6. **One accounting for the aggregate records (T-S3).** The
   content reading is the rule: per-turn aggregate elision records
   (shape c) are FRAGMENT CONTENT, measured by the packer inside
   the budget exactly like bounded bodies — a fragment that does
   not fit the remaining budget is withheld with its mode's
   continuation, and the record travels with its turn. The
   ENVELOPE-ONLY fields are named as exactly the window-level
   trimming report (served and withheld counts, boundary turn
   ids, ONE continuation cursor) and nothing else; the
   contradictory "envelope-only" phrasing for shape (c) records
   is removed. The cap guarantee then holds by construction
   under one accounting, and the fixture's legs each carry their
   turn's one record inside the measured budget.
7. **Correct the frontier refresh set (S-N2).** The frontier
   facet refreshes on the write-then-emit rows that follow an
   execution-OPENING entry's persist: `EventUserInput`,
   `EventSteeringInjected` (ADDED — the steering/notification
   execution-opening persist, the same row spec 4's ask-facet
   refresh already keys on, with the carrier's no-`TurnUserInput`
   doc and the notification-before-`TurnStarted` ordering cited),
   and `EventAssistantTextEnd` (kept: covers model-response-opened
   turns, harmless within a turn). `EventToolCallEnd` is DROPPED
   — the map's own documented write-then-emit exception (its
   result persistence occurs afterward), and a turn whose model
   response carries tool calls is already covered by
   `EventAssistantTextEnd`. `EventTurnStarted`'s exclusion reason
   is CORRECTED: it is a start row, not a persist row —
   user-input executions precede the first persist, but
   notification executions ALREADY hold a persisted entry when it
   fires (the notification path's own ordering), so the frontier
   keys on persist moments only, never start moments. Journey
   gains the blocked-carrier case: steer persisted, provider wait
   blocked, the metadata read during the wait must name the new
   execution as the frontier.
8. **The minors.** The frame's composite bound is defined as its
   own K with its value a measurement obligation on the frame's
   max-case fixture, explicitly not spec 2's K (M1); the function
   names corrected everywhere — `claimSteeringCarrierInput`
   (`:379`) and `consumeSteeringMessage`
   (`agent/session_queue.go:959`) (M2); the fixture's measurement
   basis restated — the round-9 10,381-byte measurement is the
   OLD identity+position form's basis, and both legs re-measure at
   the NEW version-and-origin-bearing encoding, K lines each,
   starting values re-derived from the fixture (M3); the
   empty-link case stated with an explicit regression journey —
   a fenced inline self-resolving steer's empty link list matches
   today's unfenced strip behavior, suppression correctly silent
   because no communicate delivery fires either (M4). Plus the
   drain-max reason restated as the nested-population argument
   (the reason that covers records spanning a restore boundary,
   per T's own analysis).

README: round-11 trail. Round 12: the same three seats — the
convergence rate and closure-audit precision keep rising (three
seats on one finding this round), and the seats hold the complete
map of their own findings.

## Readiness after this round

**Spec 1 READY (all three seats, second consecutive round).** The
fence remains converged (zero new fence defects). Specs 2, 3 and 4
carry the seven significants above, all with decided shapes that
either restore promises the pass broke (the label, the accounting),
complete fixes that stopped one hop short (the tool route, the
call-less count), or correct rows/names the pass chose wrong (the
frontier set, the citations). None touches the fence's settled core
or spec 1.
