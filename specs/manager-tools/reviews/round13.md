# Round 13 — reconciliation and disposition

Reviewed at commit `47ce7bbaa6` (the round-12 revision pass
`4c90a2f562` plus two addenda), code base `2be411cd8f`. Seats — the
same three families, a sixth pass, each auditing closure of its OWN
round-12 findings before fresh attack, all leaves, high effort, all
three ran the reds uncached with `-count=1 -v` (every red failed for
exactly the documented reason, zero disputed, all four
characterizations passing):

- **R** `codex-jesse-fsck.com/gpt-5.6`
  (dlg_034tX3mVu9PPoMGdl1YxhP), run 15:51–16:04: 2 significant, 0
  minor; all five of its round-12 closures (R12-R-1..R-5) CLOSED.
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034tX3oF8bBeOgmdNTFdr4), run 15:51–16:13: 1 significant + 1
  minor; all three of its round-12 closures (S12-1..S12-3) CLOSED;
  two scratch-only dynamic proofs, both removed afterward.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034tX3pcKpCz098he2r9gY), run 15:51–16:01: 1 significant + 4
  minor; six of its eight round-12 closures CLOSED, T-N3 and T-N4
  PARTIAL.

Tally: **4 distinct significant + 5 distinct minors.** No spec is
clean across all three seats except spec 1 (READY from all three,
fourth consecutive round; the fence stays converged — zero fence
defects again, and S's dedicated attack on the pageability bit's
change-detection interaction came back clean: the pair, not the bit,
sits in the comparison keys, and a daemon upgrade changes the boot
component, which supplies the delivery trigger).

Every load-bearing claim was re-verified in-tree by me at the exact
hop named, including two dynamic claims re-derived from S's
scratch-proof descriptions.

## The honest shape of this round

Three of the four significants are mine to own, plainly:

- **R13-R-2 = S13-2 (two seats, one dynamically proved): the
  goal-continuation journey I wrote in round 12 asserts the mirror
  image of the real order.** The hold happens AFTER the persist —
  `acceptContinuationInput` records the steering entry
  (`agent/session_lifecycle.go:2416-2419`) before the provider call
  (`:1814-1829`) — so the full-history read already names the NEW
  execution during the hold (the first execution entry advances the
  index locator, `internal/transcriptindex/build.go:483-490`; history
  derives its anchor from that index). The real pre-fix disagreement
  is METADATA PREVIOUS / HISTORY NEW; after the reorder both shapes
  agree. My journey had the two shapes swapped. S proved it with a
  scratch-only held-continuation test:
  `fullHistoryFrontier="turn_m1"` during the hold. R walked the same
  chain statically. Verified by me at the hops.
- **T-N1 (round 13's new numbering): the row clamp I added in round
  12 named its own metadata without naming its fields.** The fix
  text says the clamped row is served "with the set's composite total
  and the row's own truncated-list marker beside the clamped slice" —
  but `PendingAskState` was named to gain only `CalllessCount`, the
  page's `total` lives on the page response alone, and the question
  count is expressly disclaimed as the truncation signal. So the
  frame's declared `truncatedList` has no computable input (the chain
  carries the pair, the count and the pageable bit — no total), and
  journey 5's paged-read trigger cannot fire. The same defect class
  round 12 caught in my round-11 pass, now one layer deeper in my
  own clamp fix.
- **T-N5 (minor): my round-12 addenda named the wrong Validate
  mechanism.** `SessionOperationRequest.Validate` rejects content
  filters via a field-level branch on `Turns`
  (`appwire/session_tools.go:126-128`); exclusivity is the
  exactly-one-operation count over the member slice (`:105-113`). A
  `Fetch` member added without joining that slice leaves
  `Fetch`+`Turns` at count 1 — Validate passes and the dispatch
  silently builds the `Turns` arm, dropping the cursor. Verified by
  me directly.

The fourth significant and the round's deepest finding is S's:

- **S13-1: asynchronous attention can supply a reminder-less
  notification execution's first persisted entry with no
  frontier-refresh event.** Verified by me at every hop: a
  root-attention-only wake is admitted with no job notifications and
  no steering (`agent/session_lifecycle.go:2459-2484`), the reminder
  is written only when job notifications exist — the code's own
  comment: "the steering-only and attention-only wakes emit no
  reminder at all" (`:2509-2523`), so the notification execution
  holds no persisted entry when its provider call is held; a second
  delegate completing during the hold persists its attention through
  `writer.Record(..., PlaceAsync)` with NO session event
  (`agent/session_attention.go:616-642`); `PlaceAsync` joins the
  RUNNING execution (`agent/transcript/placement.go:117-121`), making
  that attention turn the execution's FIRST persisted entry; and the
  first execution entry advances the history anchor
  (`build.go:483-490`) while the metadata frontier — keyed on rows
  that never fire for this path — stays on the previous execution. S
  reproduced it end to end with a scratch overlay: second attention
  during the held provider, history frontier `turn_m1`, session
  events `[WARNING]` (its own drain barrier) only. The refresh set's
  population claim is falsified: a fourth first-persist path exists
  inside the existing notification mode, and the set misses it.

And one significant is a gap five rounds old:

- **R13-R-1: the byte-range fetch has had a complete request grammar
  since round 9 and no response carrier anywhere.** The partial
  response payload is described as content — "the identity fields,
  totalLength, offset, data, continuation cursor" — but
  `SessionOperationResponse` is closed with no fetch member
  (`appwire/session_tools.go:202-218`, verified by me), the hub
  dispatcher has no FETCH arm, and the 256KB Manager read-cap gate
  that spec 2 promises bounds every fragment has no FETCH arm
  either. The hub can resolve the cursor as designed and has no
  declared route to return the bytes. Nobody asked where the fetched
  bytes land until R did.

## The findings, verified

- **S13-1** (significant, spec 3): the async-attention first-persist
  path (above). Disposition 3.
- **R13-R-1** (significant, spec 2): the fetch response carrier
  (above). Disposition 1.
- **R13-R-2 = S13-2** (significant/minor, two-seat, spec 3): the
  goal journey's reversed history expectation (above), plus S's
  bridge-synchronization note — reaching the provider hold does not
  establish when the bridge sampled the pre-persist marker
  (`agent/session_events.go:438-440` enqueues; the journey must
  drain the authoritative event consumer before asserting the
  metadata shape). Disposition 4.
- **T-N1** (significant, spec 4): the row clamp's total and
  truncation fact have no wire carriers; the frame's `truncatedList`
  has no computable input (above). Disposition 2.
- **T-N4** (minor, spec 4): the frame K's recording obligation
  asserts against an unnamed "watch delivery rail's working budget";
  the real bound is `watchFrameMaxChars = 4096`
  (`agent/watch_types.go:37`, applied at `agent/watch_frame.go:107`),
  and the roster payload — the frame's own carrier — is appended
  AFTER that cap (`agent/watch_send_rail.go:52-53`), so no existing
  bound covers the payload the fixture measures and the obligation
  cannot fail as written. Disposition 5.
- **T-N5** (minor, spec 2): the Validate mechanism (above).
  Disposition 1.
- **T-N2** (minor, spec 2): the envelope accounting is still stated
  two ways — the floor sentence says the floor "always covers the
  response envelope" (envelope bytes inside the floor) while the new
  rule says the envelope's content is the trimming report and
  nothing else, and the budget sentence says it is "measured on the
  encoded envelope" against the scope's own "budget bounds the
  window CONTENT". The residue of T-N4 from round 12. Disposition 6.
- **T-N3** (minor, spec 2): the floor's funding head-count counts "at
  most K cursor-bearing per-item lines plus one aggregate record",
  but the per-turn-cap-maximum leg carries 40 served item lines (all
  truncated and labeled, K of them bearing cursors) — the line bound
  is the per-turn item cap (itemLimit max = 40, scope 6), which the
  funding story never names; "sufficient by construction" is false
  as a head-count. Disposition 7.

Also verified and NOT findings (the attack surface clearing): the
held-snapshot and answer-continuation lookups use snapshot identity,
not `PendingAsk.Calls` (R); the frame's composite list derives from
the same probe row the projection clamps, at the same K (R); unknown
capabilities fail closed to `unavailable` and every probe sets
`CapabilitiesKnown` (R, T); the pageability bit does not participate
in change detection beyond the pair (S); the clamp at the projection
starves neither the pager nor the ask facet (S, T); the moved
`EventGoalContinuation` emission still precedes the provider call
and no consumer depends on its old timing (R, S); the K+1 fixture is
buildable at both id forms (R).

## Disposition — one revision pass, then round 14

1. **Name the fetch's response side end to end (R13-R-1 + T-N5).**
   `SessionOperationResponse` gains an additive `Fetch` member — a
   named fetch response type carrying the partial-response payload
   scope 4 already describes (`{position, version, totalLength,
   offset, data, continuation}`), owned wire work with the
   generated types and validators per rule 5. The hub dispatcher
   gains the FETCH arm (decode the cursor, resolve by origin, assign
   the response), and the 256KB Manager read-cap gate gains the
   FETCH arm — the fragment is bounded by the same whole-read cap,
   the promise scope 4 already makes. And the exclusivity is
   corrected to the real mechanism: the `Fetch` member joins the
   exactly-one-operation count slice
   (`appwire/session_tools.go:105-113`) — that count is THE
   exclusivity rule — while the content-filter branch (`:126-128`)
   is cited as what it is, a `Turns` field-level rule, not an
   exclusivity mechanism.
2. **Carry the clamp's metadata (T-N1).** `PendingAskState` gains
   two more additive fields beside `CalllessCount`: the set's
   composite total and the row's truncation fact, minted inside the
   atomic accessor under the same lock, cloned with the state. The
   §6 chain carries the total to `AttentionEntry` in a declared
   field, so the frame's `truncatedList` is computable from the
   entry itself (the set is larger than the list) and journey 5's
   paged-read trigger fires. The row's "marker beside the clamped
   slice" becomes these named fields.
3. **Cover the execution-first-record population (S13-1).** The
   refresh set's claim is corrected from "four named rows cover
   every execution-opening persist" to the honest rule: the frontier
   refreshes on rows that follow an execution's FIRST PERSISTED
   ENTRY, and the set must cover every path that can supply that
   first entry. The async-attention persist site
   (`agent/session_attention.go:616-642`) gains a post-persist
   emission — a new event row in the facet map with the frontier
   facet, owned additive work stated with its landing zone,
   write-then-emit after the verified read-back, the same rule every
   frontier row follows — so the fifth first-persist path
   (attention-only notification, second delivery mid-hold) refreshes
   the frontier like the other four. Journey 8 gains S's held case:
   the first attention's provider hold, the second delivery during
   it, the metadata read naming the previous execution before the
   fix and the new execution after — the history anchor already
   names the new execution, verified by S's proof.
4. **Fix the goal journey's polarity (R13-R-2 = S13-2).** The
   journey's disagreement is corrected to METADATA PREVIOUS /
   HISTORY NEW before the reorder (the persist precedes the hold;
   history has advanced; the metadata frontier lags on rows that do
   not exist for this entry mode), both shapes naming the new
   execution after. The journey synchronizes the bridge's
   observation before asserting the metadata shape (drain the
   authoritative event consumer — the emission is enqueued at
   `agent/session_events.go:438-440` — the barrier S's proof used).
5. **Name the frame's bound and fold the payload inside it
   (T-N4).** The frame K's recording obligation asserts against the
   NAMED `watchFrameMaxChars = 4096` (`agent/watch_types.go:37`,
   applied at `agent/watch_frame.go:107`), and the design states the
   roster payload is folded INSIDE that cap — today it appends after
   it (`agent/watch_send_rail.go:52-53`), and the fold is owned
   work — so the fixture measures the whole delivered frame against
   the named bound and the obligation can fail.
6. **Finish the one accounting (T-N2).** The envelope's CONTENT is
   the window-level trimming report (the fields it carries); the
   envelope's BYTES sit inside the floor's coverage (the floor funds
   one turn fragment, the envelope, and one continuation cursor) and
   inside the cap's outer bound; the budget sentence is reworded to
   what it means — the budget measures the ENCODED BYTES of the
   packed content, so every field of the content and its JSON
   escaping count by construction.
7. **Fix the funding head-count (T-N3).** The line bound is the
   per-turn item cap: a turn fragment carries at most itemLimit-max
   (40) item lines — bounded bodies, labeled-truncated lines, or
   elision lines — of which at most K bear cursors, plus its turn's
   ONE aggregate record. K bounds the cursor-bearing subset, never
   the line count; that is what makes the floor sufficient by
   construction, with the max-case legs (40-item and K-elision)
   named as the measurements that pin it.

README: round-13 trail (thirteen rounds). fence-spike: a round-13
sentence — the fence stayed converged through its fourth consecutive
re-audit; no fence finding in either direction. Round 14: the same
three seats, closure audits of their own round-13 findings plus
fresh attack on the new text.

## Readiness after this round

Spec 1 READY from all three seats (fourth consecutive). Spec 4:
READY from R and S, NOT from T (its clamp-metadata significant and
the unnamed frame bound). Spec 2: READY from S, NOT from R (the
fetch response) or T (three accounting/mechanism minors). Spec 3:
READY from T, NOT from R (the journey) or S (the async-attention
path). The trend is real — 7 significant (round 11), 5 (round 12),
4 (round 13) — and the seats verify closures at 5/5, 3/3 and 6/8.
None of the four significants touches the fence or spec 1; one is a
five-round-old gap, one a genuine completeness discovery, and two
are errors in my own round-12 text, owned above.
