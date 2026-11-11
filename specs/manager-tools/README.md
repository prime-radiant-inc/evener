# Manager tool gap specs

Four specifications for the Manager session tools (the `job_list`,
`job_status`, `job_watch`, `read_transcript`, `delegate`/`delegate_send`
hub surfaces that a Manager session uses to run a fleet of coding-agent
lanes). They were designed on a live fleet: the Manager session that ran
Jesse's coding agents through a night of full-load operation, sweeping
every lane every 30 minutes. Each spec names the operational evidence
that produced it, grounds itself in what exists today, and defines a
testable contract.

Intended audience: implementer lanes (one per spec, `/implementing-features`
pipeline) and adversarial PAR reviewers. These are lane design inputs, not
product-guide pages; each implementer moves its durable rules into the
owning product docs in the same change, per the subsystem map discipline.

Shared base: `origin/main` at `2be411cd8f` (the tip that already contains
the runner-bound gate split and the transient-delivery-retry fix). The
delivery reliability PR (5662, reliable Manager hub-watch delivery:
resync, rotation, outage) and the hub-watch affordances PR (5641) may
land while these specs iterate; every spec below treats those as trusted
substrate, not as scope.

## Review trail

The specs have been through twenty-eight adversarial review rounds, each with
independent model families verifying every load-bearing citation against
the base commit:

- Round 1 (two reviewers): 20 verified findings; all four specs revised.
- Round 2 (three competing reviewers on three model families plus two
  red-test spikes): 36 significant findings and two dynamic proofs;
  reconciled in [reviews/round2.md](reviews/round2.md).
- Round 3 (three competing reviewers on three fresh model families):
  33 significant findings, heavy independent convergence on the core
  gaps, and several new mechanism facts (the succeeded-empty seam's
  live-stamped identity, the clients' reliance on `Authoritative`, the
  coarse wire categories, the operation-validation gate, the append
  semantics of pending asks, the archive-blind roster population);
  reconciled in [reviews/round3.md](reviews/round3.md).
- Round 4 (three competing reviewers on three more fresh families):
  15 significant findings — no broken premises, but naming gaps,
  two internal contradictions (elision cardinality; the locate read
  versus the item cap), one preservation hole (an unfenced relay
  answer could clear a newer question), and one grounding rewrite
  that simplifies the design (the hub already packs reads at a 1MB
  soft limit above the Manager's 256KB cap; the specs extend that
  packer); reconciled in
  [reviews/round4.md](reviews/round4.md).
- Round 5 (three competing reviewers on three more fresh families):
  8 significant findings with heavy convergence — two reviewers
  independently broke the round-4 relay fence (the revision counter
  is not persisted and repeats across a daemon restart), one found
  the fence had no row carrier, one found its enforcement point and
  older-daemon direction unspecified, and verification exposed a
  trigger flaw of our own (a single-ask restore re-mints the same
  revision value, so restart delivery fires nothing). The fence and
  the trigger now share one identity: the pair of the daemon's
  durable boot generation and the per-boot revision. Plus a
  per-field remote-row story for spec 1, the whole-set relay
  semantics stated, and `attentionAsOf`'s landing zone named;
  reconciled in [reviews/round5.md](reviews/round5.md).
- Round 6 (three competing reviewers, two never-seated models plus a
  replacement for a small-window seat that died on its provider's
  256k request limit): 12 significant findings with the heaviest
  convergence yet — all three seats broke the round-5 relay fence,
  on three axes (the older-daemon refusal cited the hub-side decoder
  while the daemon's direct mutation methods decode permissively;
  the boot generation claimed to ride the thread row does not exist
  there — a grounding error of ours, made by reading a nearby
  struct; and the excerpt and revision are sampled in two locked
  calls). The sharpest catch: the fence covered only start, which is
  refused mid-turn — the exact append window it exists for — leaving
  the unfenced queue and steer modes as the only answerable paths.
  Spec 1 came back clean from all three seats, the first at the bar;
  spec 3 gained its remote-serve provenance rule; spec 2 its
  aggregate-elision recovery. Reconciled in
  [reviews/round6.md](reviews/round6.md), which also records the
  fence-spike disposition and the grounding-method lesson.
- Round 7 (three competing reviewers on three never-seated models):
  13 significant findings with NO overlap against round 6 — and a
  different character: the grounding repairs held (no seat found a
  false citation), but the fence's DESIGN broke. The check sat after
  the answer was already durable (a mismatch can refuse the clear
  but not undo the append, and restore resolves ANY durable user
  turn); the hub-side gate covered only `startTurn`'s prepare step
  while queue and steer short-circuit without a row read for
  ordinary text (fail-open on the only mid-turn-answerable modes);
  and a consumption-time refusal had no path back to a caller already
  holding a success receipt. Spec 1 lost its first significant of the
  process (explicit filtered reads carry nonselected sources the
  states never covered), spec 2's byte-range continuation named a
  wire contract that did not exist, and spec 3's `Authoritative`
  migration named the redesign but not the replacement outcomes.
  Reconciled in [reviews/round7.md](reviews/round7.md), which fires
  the round-6 escalation: the relay fence, the pending-set identity
  carriers and the broker's pending-set trigger leave spec 4 for a
  RED-TEST SPIKE — one failing test per verified failure mode against
  the real seams, then the fence designed from the failing tests.
  Specs 1-3 took one revision pass; spec 4 keeps its visibility
  content and states its delivery contracts for the spike to pay.
- Round 8 (three competing reviewers — deliberate family re-treads,
  each having broken the fence in a prior round, each running the
  spike's red tests themselves): 28 significant + 5 minor, no spec
  ready — but the spike's core survived all three seats (every red
  reproduced for exactly the documented reason, zero disputed, the
  worktree verified byte-identical to the base), and the findings were
  a different class: verified static facts with named fixes. The
  fence's RESTORE half could not be expressed by the evidence the
  system writes (the durable answer names executions, sampled at
  consumption, not calls); the clear-site inventory missed three
  answer-reachable sites; the constructor wiring was impossible in the
  mint's own order; queue transformations replace the record that
  would carry the captured names. Around the core: spec 1's
  one-filter routing claim was false and explicit reads fail whole
  today; spec 2's `messages` dropped user steering, its fetch REQUEST
  grammar was absent, and our 4KiB envelope ceiling was arithmetically
  false; spec 3's migration would poison deployed clients' exact-key
  caches, its retain row was unreachable under identity-replace, and
  our frontier fix named a direct read the projection forbids; our
  re-entry overclaimed the trigger the spike left unchanged. Reconciled
  in [reviews/round8.md](reviews/round8.md): ONE revision pass, no
  second spike — the evidence redesign (captured composites as the
  sole resolution evidence, on the journal record and the resolving
  turn, the failure writers included), the full clear-site routing,
  post-construction wiring, the trigger designed in spec 4, the
  full-set fetch, and version-gated delivery on every client surface.
- Round 9 (the same three seats, re-tread a second time — each auditing
  closure of its OWN round-8 findings before fresh attack): 17
  significant + 9 minor, no spec ready — but **25 of the 28 round-8
  closures verified CLOSED by the seats that found them**, all five
  reds still reproducing exactly, and the fence core intact. Three of
  the round-8 pass's own claims were false, owned in the record: the
  old-client "conservatively retains" skew (the failed-live seam
  compares generation BEFORE the flag, so old clients keep today's
  erase — the legacy method is now frozen byte-for-byte with its costs
  stated), the 10KiB envelope ceiling (the real cursor encoding
  measures 10,381 bytes for 40 minimal lines — the burst is now
  bounded by construction at K lines and every number is a measurement
  obligation), and the fetch cursor's "only minted by a bit-advertising
  daemon" premise (the cursor is hub-owned and now carries its ORIGIN,
  the fetch hub-resolved on all three serving arrangements). Four
  deeper code facts surfaced: the pair has NO wire carrier end to end
  (`PendingAskState` lacks a boot generation; the probe drops even
  `Revision`/`Calls`), the steer writers LIVE-SAMPLE the pending set
  at claim and delivery (the sole-evidence claim never disposed of
  them — they now write from the record), a FOURTH evidence writer
  (`recordClientMutationFailure`) the inventory missed, and the fence
  cannot express call-less legacy questions (the capture is now
  `(calls, legacyCount)`). Reconciled in
  [reviews/round9.md](reviews/round9.md): one surgical revision pass,
  then round 10 with the same seats.
- Round 10 (the same three seats, re-tread a third time — closure
  audits of their own round-9 findings, then fresh attack): **10
  significant + 10 minor with heavy three-seat convergence, and
  spec 3 READY from all three seats — the first spec at the bar** —
  with 23 of 26 round-9 closures verified CLOSED, all five reds
  reproducing exactly, and each seat's one PARTIAL verdict becoming
  exactly its own new significant. The convergence classes: the
  K-bound roll-in had no truthful aggregate population
  (served-but-truncated markers were also unbounded — one K now
  bounds ALL cursor-bearing per-item lines, and the aggregate
  record splits four populations, each with its recovery named);
  the hub-compat fetch could not re-derive an item scrolled out of
  the newest legacy page (the fetch now pages backward within the
  window's turn span and fails a typed `item-unreachable-on-compat`
  when the traversal cannot reach it); the frame's question count
  and composite list are DIFFERENT UNITS (the list carries its own
  truncation marker and the page owns iteration to completion); and
  the round-9 steer-writer narrowing broke two runnable contracts
  (the rule now splits link evidence from call-resolution evidence,
  unfenced and legacy behavior preserved exactly). Seat-unique: the
  pending-ask pager had no callable route (now an additive page
  request on the ref-addressed thread read behind the `AskFence`
  bit, its server-normalized bound a measurement obligation) and
  `legacyCount` never reached the resolving turn (the turn field
  carries BOTH halves of the consumed capture; the drain union takes
  the max). Reconciled in
  [reviews/round10.md](reviews/round10.md): one surgical revision
  pass (this commit), then round 11 with the same seats.
- Round 11 (the same three seats, re-tread a fourth time — closure
  audits of their own round-10 findings, then fresh attack): **7
  distinct significant + 4 minor, no spec clean — but the FENCE IS
  CONVERGED**: every round-10 fence finding closed across all three
  seats, zero new fence defects, and spec 1 READY from all three for
  the second consecutive round. The heaviest three-seat convergence
  of the arc: the pager had a wire shape but no callable
  Manager-tool route (it now rides the `job_status` hub branch end
  to end, its degradation named at both edges). Most of the rest sat
  in text the round-10 pass itself added: its K fix left truncated
  items beyond K unlabeled — indistinguishable from complete in the
  same response (the label now always travels; K bounds only
  cursors); its compat traversal named no stopping rule and an
  impossible version-before-reachability ordering, while its
  scroll-out journey demanded the failure at exactly the arrival
  count one page-back recovers (the traversal now ends at the
  cursor's own target position under a page-count cap, precedence
  reachability-first, gated on the peer's `PageBefore` bit, and the
  journey's 40-arrival case now asserts success); its frontier fix
  named a row the facet map's own comment documents as NOT
  write-then-emit and missed `EventSteeringInjected` (the rows now:
  user input, steering injected, assistant text end). Older text
  carried the rest: the per-turn aggregate records were accounted
  two incompatible ways (they are fragment content now, one
  accounting), the page's composite completion could not enumerate
  call-less questions in a mixed set (the frame and every page
  response carry the call-less count), and two cited function names
  never existed. All five reds reproduced exactly, zero disputed.
  Reconciled in [reviews/round11.md](reviews/round11.md): one
  surgical revision pass (this commit), then round 12 with the
  same seats.
- Round 12 (the same three seats, re-tread a fifth time — closure
  audits of their own round-11 findings, then fresh attack): **5
  distinct significant + 7 minor, no spec clean except spec 1 —
  READY from all three seats for the third consecutive round, the
  fence still converged: zero new fence defects, and every
  round-11 semantics closure verified under fresh attack (the
  label/cursor rule, the traversal's stopping rule and precedence,
  the one accounting, the frontier's steering rows, the pager's
  request route, the call-less count's computability, the
  empty-link edge, the drain MAX argument).** Four of the five
  significants were one defect at the type layer: the round-11
  pass named concepts — the truncation label, the frame's
  pageability reason, the page — but never named their WIRE
  CARRIERS, so each fix stopped one hop short of an implementable
  field (round 10 stopped at the tool, round 11 at the types).
  All three seats independently found the frame's reason has no
  carrier (`AttentionEntry` carries no capability and the broker's
  derivation drops the `LiveEntry` capabilities it reads); two
  seats found the pager leaves the whole unbounded
  `PendingAsk.Calls` array on the mandatory thread row every
  metadata read returns — the round-12 review measured today's
  minimal response at 316,555 bytes for 4,000 composites, over
  the cap before any page field exists — so the row is now
  clamped to the frame's K beside the page; the label's
  `ThreadItem` fields, the page's `ThreadReadResponse` field,
  the traversal cap constant, the frame's declared payload
  fields with K named at 10, and the fetch's
  `SessionOperationRequest` member are all named wire work now.
  The fifth significant was a genuine completeness discovery: a
  third execution-opening entry mode — goal continuations persist
  after emitting today, so the frontier stays stale through the
  whole provider wait — now refreshed by a moved emission and a
  goal-mode journey. All five reds reproduced exactly, zero
  disputed. Reconciled in
  [reviews/round12.md](reviews/round12.md): one
  surgical revision pass (this commit), then round 13 with the
  same seats.
- Round 13 (the same three seats, sixth pass — closure audits of
  their own round-12 findings, then fresh attack): **4 distinct
  significant + 5 minor, no spec clean across all three seats
  except spec 1 — READY from all three for the fourth
  consecutive round, the fence converged again with zero fence
  findings in either direction (and the seat that attacked the
  pageability bit's change-detection interaction came back
  clean: the pair, not the bit, sits in the comparison keys).**
  Three of the four significants were this design's own text,
  owned plainly: the goal-continuation journey asserted the two
  read shapes in the mirror of their real order (two seats — the
  persist precedes the hold, so full history already names the
  new execution and the metadata frontier lags, proved
  dynamically); the round-12 row clamp named its composite total
  and truncation marker without naming their fields, so the
  frame's declared marker had no computable input; and the
  addenda named the content-filter branch as the fetch's
  exclusivity mechanism where the exactly-one-operation count
  is the mechanism. The fourth was the round's deepest finding:
  an attention-only notification wake can run a reminder-less
  execution whose first persisted entry arrives asynchronously —
  a second delegate's attention, persisted mid-hold through
  `PlaceAsync`, joins the running execution, advances the
  history anchor, and emits no event — so the refresh set gains
  the execution-first-record rule and a fifth first-persist row
  at the attention persist site. Beside it, a five-round-old gap
  closed: the byte-range fetch had a complete request grammar
  and no response carrier anywhere — `SessionOperationResponse`
  is closed, the dispatcher has no FETCH arm, the read-cap gate
  has no FETCH arm — all named now. All five reds reproduced
  exactly, zero disputed. Reconciled in
  [reviews/round13.md](reviews/round13.md): one surgical
  revision pass (this commit), then round 14 with the same
  seats.
- Round 14 (the same three seats, seventh pass — closure audits
  of their own round-13 findings, then fresh attack): **5
  distinct significant + 5 minor, no spec clean across all
  three seats except spec 1 — READY from all three for the
  fifth consecutive round, and the fence converged through its
  sixth re-audit with zero findings from any seat.** Every
  significant was the round-13 revision pass's own defect or
  miss, owned in the record, and the count went UP for the
  first time since round 11 — the seats now audit the fixes
  harder than the original design. All three converged on the
  roster fold the pass had ordered without arithmetic: the
  initial snapshot carries every lane, one delivery marshals
  every changed entry, and 32 ordinary entries already
  produce a 4,847-rune payload against the 4,096 bound — no K
  funds it, so the roster payload is now its own segment with
  a structure-preserving truncation contract and its own
  bound. The fetch chain the pass named stopped one hop short
  again — the serving daemon returns the closed
  `ThreadReadResponse`, which now gains its own fetch member
  and a named round trip, with the hub minting byte ranges so
  a valid item never reaches the read cap as a refusal. The
  frame's composite list rides `AttentionEntry`, which breaks
  the entry's value comparison at both broker baseline sites
  (a compile break, proved with an overlay — the design now
  owns a typed equality helper at both sites); the goal
  journey's drain barrier proved processing, not sample
  timing (a post-hold-gated drain reproduces the green
  observation on pre-reorder code — the barrier now sits in
  the emit-to-record gap); and the population walk missed a
  sixth first-persist path, automatic compaction during
  request preparation, whose CHECKPOINT entry joins the
  running execution with no frontier row. The closure is now
  centralized: the placement layer's own first-ness flag —
  the one that stamps `TurnKind` — emits one post-persist
  first-record signal, `EventExecutionFirstRecorded`, closing
  the population by construction at the choke point the
  placement layer already enforces (the join is the only way
  an entry lands in a running execution's turn), superseding
  the round-13 per-site attention event. All five reds
  reproduced exactly
  across all three seats, zero disputed. Reconciled in
  [reviews/round14.md](reviews/round14.md): one surgical
  revision pass (this commit), then round 15 with the same
  seats.
- Round 15 (the same three seats, eighth pass — closure audits
  of their own round-14 findings, then fresh attack on the
  round-14 revision's own text): **7 distinct significant + 5
  minor, the count UP for the second consecutive round (5 to
  7), and every significant again the previous pass's own
  text. Spec 1 READY from all three seats, sixth consecutive
  round; the fence converged through its seventh re-audit with
  zero findings.** Three seats broke the roster segment
  contract from three angles: the pending-send merger unions
  two bounded segments into one over-bound segment and loses
  the marker (proved dynamically: two 24-entry payloads merge
  to 6,001 runes against a 4,096 witness bound); the frame
  payload builder is a seven-field whitelist, not the
  marshaller the pass claimed, so the composite list's last
  carrier hop did not exist; and the recovery instruction the
  pass pointed at re-registers the same truncating watch, so
  dropped lanes stay invisible — the recovery is now the
  fleet snapshot read, the merger re-applies the bound to the
  merged segment and carries the dropped count, and the
  whitelist struct and its literal are named as owned work,
  with the per-lane ask facts landing on the payload row. The
  remote fetch's byte-for-byte cursor pass-through was
  withdrawn: cursor validation is an exact identity match no
  forwarded cursor survives, and the ordinary remote read
  translates actors, image routes and authorship a byte
  fragment cannot receive — the remote-origin fetch now
  resolves hub-side after translation, the controller-owned
  identity pattern the remote item paging already proves,
  with a typed refusal beyond the inter-hub read's reach. The
  first-record signal's emission is centralized at the Record
  call — the resolution writers that discard the outcome today
  are named — and the one-shot fresh fact is held as an owed
  notification across the attention site's failed read-back,
  reconciled by the retry, once per execution. The lesson both
  rising rounds teach, now a pass rule: a contract is not
  closed at its design site but at every site that writes the
  same surface. All reds reproduced exactly across all three
  seats, zero disputed. Reconciled in
  [reviews/round15.md](reviews/round15.md): one surgical
  revision pass (this commit), then round 16 with the same
  seats.
- Round 16 (the same three seats, ninth pass — closure audits
  of their own round-15 findings, then fresh attack on the
  round-15 revision's own text): **4 distinct significant + 4
  minor, the count DOWN 7 to 4, all four again the previous
  pass's own text, and the round's largest finding is the
  set's first THREE-seat convergence. Specs 1 and 2 READY from
  all three seats — the first time for either since round 10;
  the fence converged through its eighth re-audit with zero
  findings.** The convergence (S16-1 = 16-T-5 = R16-R-3, S
  proving it dynamically through the real ambiguous-sync
  seams): the synced door has a third outcome the owed
  notification never read — a line recorded but not made
  durable returns `*RetainedUnsyncedError` together with
  `rec.Recorded`, "and the caller adopts the record" — and
  every producer returns on the error before the owed site,
  so the fresh flip is consumed at placement and the signal
  emits ZERO times; the owed obligation now keys on the
  outcome's own field (`rec.Recorded`, with or without an
  error) at every producer, and the false blanket mismatch
  rationale is replaced by the code's two sub-cases. The
  truncation recovery line was landed on a constant whose
  text begins with the resync marker the coalescing merger
  treats as authoritative replacement, so a truncated delta
  would short-circuit the next merge discarding the pending
  payload — the recovery line is now a new `roster_recovery:`
  shape that never carries the marker. The recovery was
  nominated against spec 1's fleet read without walking that
  read's own cap boundary (one call, no pagination, over-cap
  refusal) — a fleet large enough to truncate is exactly a
  fleet that can fail the recovery whole — so the marker gains
  dropped refs and the recovery becomes a ladder, per-lane
  reads total at any fleet size, the fleet read the
  small-fleet rung, overflow stated in the frame. And the
  whitelist extension list named the ask facts but not the
  identity fields the frame-only relay's own journeys require
  — `RosterInstanceID` is journey 6's clear token, `ID` the
  fetch addressing — now added with the hubcore fill site
  named as owned work. The comparison-context list was short
  for the third consecutive round (twelve, not ten), which
  converts a habit into a rule: **enumeration claims are made
  by mechanical sweep, never assembled from seat reports.**
  All reds reproduced exactly across all three seats, zero
  disputed. Reconciled in
  [reviews/round16.md](reviews/round16.md): one surgical
  revision pass (this commit), then round 17 with the same
  seats.
- Round 17 (the same three seats, tenth pass — closure audits
  of their own round-16 findings, then fresh attack on the
  round-16 revision's own text): **2 distinct significant + 2
  minor, the count DOWN 4 to 2, and the largest finding is a
  second consecutive THREE-seat convergence. Spec 1 READY the
  eighth consecutive round, spec 2 the second; spec 4 READY on
  significants with one verified minor; the fence at zero
  findings through its ninth re-audit.** The convergence
  (S17-1 = T17-1 = R17-R-2, S proving it dynamically through
  the real pair-helper and stabilization seams): the round-16
  pass generalized the CAPTURE of the owed obligation to
  every recorded outcome but not its DISCHARGE — the pair
  helper's own documented contract deliberately converts the
  retained error to success ("the turn is adopted here...
  and the next fsync settles the debt"), so every synced
  owner never retries and never reaches the two attention
  reconcilers the pass named, and the stabilization writer's
  successful recovery runs durability and verification with
  no Record at all: zero emissions after successful recovery.
  The discharge is now stated at the durability-completion
  boundary at every producer — the wrapper's next successful
  synced outcome (the barrier settles the whole file), the
  stabilization's durability-plus-verification site, the
  attention fold sites — and the outcome gains the durability
  fact as its own field at every door, since the buffered and
  durable doors return the retained fact as a separate value
  `Record` discards today. R's own discovery past every
  seat's attack list (R17-R-1): the four synchronous frontier
  rows carry a round-11 durability premise the third outcome
  falsifies — the user-input emission's own in-tree comment
  states the false premise — so the rows' premise is restated
  once: the row fires on a retained outcome but its frontier
  facet does not advance until the owed id discharges and the
  signal's own facet row advances it. The mismatch path now
  holds the owed id too (the "fresh flip comes again" claim
  was false at the placement layer), and the merger's
  stamped-together rule now covers its own truncations with a
  presence-check dedupe and a bounded refs sum. The
  mechanical-sweep rule's first test PASSED: all three seats
  re-ran the comparison grep themselves — one compile-verified
  — and the twelve contexts held. All reds reproduced
  exactly across all three seats, zero disputed. Reconciled in
  [reviews/round17.md](reviews/round17.md): one surgical
  revision pass (this commit), then round 18 with the same
  seats.
- Round 18 (the same three seats, eleventh pass — closure audits
  of their own round-17 findings, then fresh attack on the
  round-17 revision's own text): **2 distinct significant + 2
  minor — the count HELD at 2, but the character changed: both
  significants are seat convergences attacking the SAME two
  round-17 decisions, and both are the pass's own errors.**
  Three seats converged on the discharge (R18-R-1 = S18-1 =
  T18-1's door family, S proving it dynamically through the
  real assistant durable path): keying the discharge to the
  synced door missed the doors that actually complete
  durability — the durable door (the ordinary assistant
  reply), the buffered door's interval expiry, and Close —
  so the ordinary retained-input → assistant-reply schedule
  completed durability with the named discharge never
  running: zero signals. The discharge now covers every
  result that proves the writer's whole file durable, and
  the durability model is a first-class doors table the
  rules derive from — the class-level lesson: the passes
  kept deriving rules from single code paths instead of from
  the writer's complete door model. Two seats converged on
  the frontier (S18-2 = T18-1, dynamic evidence from both):
  the retained-edge freeze overloaded the frontier with a
  second meaning — the served view's anchor semantics are
  RECORDED (the index names recorded entries; a real index
  read names the retained new execution before any fsync),
  so freezing metadata alone created the exact two-shapes
  disagreement this spec exists to eliminate, and the goal
  row's ordinary buffered write makes recorded-not-durable
  the NORMAL outcome of a designed row. The frontier's
  meaning is reverted to RECORDED — the facet advances with
  the row, both shapes agree through the retained and
  buffered windows, and durability keys the signal alone.
  The durability field is computed from the door's sync
  decision, never from the retained diagnostic (non-nil
  only on failure), and the mismatch differs-at-read-back
  hold now clears without emitting at the retry's typed
  failure. Spec 1 READY the ninth consecutive round, spec 2
  the third, spec 4 READY with the merger revision closed on
  every face; the fence at zero findings through its tenth
  re-audit. All reds reproduced exactly across all three
  seats, zero disputed. Reconciled in
  [reviews/round18.md](reviews/round18.md): one revision
  pass (this commit), then round 19 with the same seats.
- Round 19 (the same three seats, twelfth pass — closure audits
  of their own round-18 findings, then fresh attack on the
  round-18 revision's own text): **3 distinct significant + 2
  minor — the count UP 2 to 3, but every finding from all three
  seats attacks the SAME root: the first-record signal's
  post-durability gating, the design's own founding decision
  since round 14.** Seat T reported ZERO significant findings —
  the first seat ever — marking specs 1, 2, and 4 stable from
  every seat. Two seats converged on the async gap (R19-R-2 =
  S19-1, S reading a REAL index naming a retained attention
  entry with zero emitted events): the recorded revert fixed
  the four synchronous rows but left the signal — the only
  refresh on the async attention and compaction paths —
  durability-gated, so the retained window still split history
  from metadata for as long as the provider call was held. The
  "every durability result" table still missed the rollback's
  own successful fsync and the non-final stabilization Close
  (R19-R-1 + T's N1); and the any-door discharge emitted a
  mismatch-held id before its content ever verified (S19-2 =
  R19-R-3, S proving it dynamically with a real intervening
  assistant write). **The disposition is a simplification, owned
  plainly: the signal EMITS AT THE RECORDED OUTCOME — any door,
  with or without an error, once per execution — and the entire
  held-id, discharge-enumeration, and clear-without-emit
  machinery built across rounds 16-18 is withdrawn with the
  post-durability invariant that manufactured a new escape
  every round.** Durability is the writer's own documented debt
  (the pair helper's adoption contract, Close the terminal
  settlement), the read-back and mismatch paths keep today's
  content-verification behavior with no signal interaction, and
  the revision makes the spec shorter. All reds reproduced
  exactly across all three seats, zero disputed. Reconciled in
  [reviews/round19.md](reviews/round19.md): one revision pass
  (this commit), then round 20 with the same seats.
- Round 20 (the same three seats, thirteenth pass — closure audits
  of their own round-19 findings, then fresh attack on the
  round-19 revision's own text): **2 distinct significant + 3
  minor.** All six round-19 closures verified CLOSED for their
  reported schedules — S re-proved the returned-outcome fix with
  five producer probes — but the seats broke the replacement's
  own placement. Two converged on the retained synced-door
  PRE-RETURN window, S proving it dynamically (a gated recovery
  fsync on the real fixture, the real production thread/read
  serving the fresh execution with zero proposed callbacks,
  an explicit contract red): the commit fires the file's
  recorded-entry hook and the history projector while
  `recordSynced` still blocks in `EstablishDurability` before
  returning the outcome, so a producer-side outcome handler
  cannot emit during an unbounded recovery fsync while the
  index already names the new execution. T broke the sampler's
  source: the spec named two different frontier mechanisms and
  both candidates fail — history lags the record; the execution
  clears before the completion entry while `TURN_ENDED`
  resamples every facet. **The disposition relocates the
  emission and the frontier stamp to the commit itself: the
  file's recorded-entry hook hands the committed first-entry
  fact to a session listener under the hook's leaf contract,
  which stamps the frontier field and queues the refresh event
  before any durability work — the same queue-and-wake shape
  the history hook already uses at that moment — so the refresh
  is in flight before the barrier, the frontier's source is
  pinned to the stamped field, and the producer-outcome
  machinery is withdrawn (the hook sees every writer; no
  signature changes).** The doors accounting gained the two
  mid-session `EstablishDurability` barriers and the
  synced-door-with-error face; a stale ordering phrase in the
  event definition (the chair's own sweep miss, owned) is
  gone and the sweep's pattern list widened. All reds
  reproduced exactly across all three seats, zero disputed.
  Reconciled in
  [reviews/round20.md](reviews/round20.md): one revision pass
  (this commit), then round 21 with the same seats.
- Round 21 (the same three seats, fourteenth pass — closure audits
  of their own round-20 findings, then fresh attack on the
  round-20 revision's own text): **2 distinct significant (BOTH
  three-seat converged) + 3 minor.** All five round-20 closures
  verified CLOSED — S re-proved the relocation dynamically (the
  field and the real index both named the fresh execution while
  the recovery fsync was gated, the event reaching the real
  stream before the producer returned) — and the relocation
  survived every attack: no seat found a relocation-killer. The
  three seats converged on two new breaks of the replacement's
  own text: compaction's verbatim replay copies carry the
  original execution-kind stamps and satisfied the kind-only
  filter (S drove a real automatic compaction: the replay of six
  preserved first entries regressed the field to an old
  execution, 19 matches vs 13, the index unmoved — and S's
  causal control proved the `OriginalOrdinal` exclusion fixes
  it), and the commit-only field has no restore seed (an idle
  restored session serves frontierless metadata indefinitely
  while history names the completed execution). **The pass makes
  the listener's predicate the session's own bookkeeping rule
  (`noteRecordedLocked`'s exclusion+filter, already in code),
  seeds the field at writer attach with the index's anchor
  semantics serialized against racing commits, installs a
  composed dispatcher so the daemon's history hook survives the
  single-slot setter, amends the sampling rule for the field's
  leaf, and qualifies the doors' proofs on successful sync.**
  All reds reproduced exactly across all three seats, zero
  disputed. Reconciled in
  [reviews/round21.md](reviews/round21.md): one revision pass
  (this commit), then round 22 with the same seats.
- Round 22 (the same three seats, fifteenth pass — closure
  audits of their own round-21 findings, then fresh attack on
  the round-21 revision's own text): **1 significant + 4
  minor.** All eight round-21 closures verified CLOSED — R
  reported zero significant and zero minor, T zero significant
  with four implementability minors, and S re-proved the
  predicate, the seed and the dispatcher dynamically (the
  real compaction passing with the predicate and regressing
  without it; the restored-transcript probe seeded and blank;
  both dispatcher wiring orders; the rewriteTail verbatim arm
  twice with field and index agreeing). S's one new
  significant is a real spawned-child proof: descendants
  bypass the root-only frontier sampler — their
  first-record events route through RecordDescendantAppEvent,
  which never runs the facet machinery and whose child
  projector has no first-record consumer, so the child's
  cached metadata row stays frontierless while the child's
  own field and full-history read name the execution. **The
  pass adds the descendant frontier consumer — sample the
  CHILD's stamped field through the child-frontier seam
  beside SetDescendantEventFunc, install it on the cached
  child row outside the projection gate, bootstrap restored
  children at their own SessionStart row construction —
  plus four localizations: the seed's data path at the
  attach call sites, the dispatcher's install site at
  setTranscriptLocked, the two-ways exclusion claim scoped
  to the compaction replay, and the IDLE-RESTART guard
  restated as what it pins.** All reds reproduced exactly
  across all three seats, zero disputed. Reconciled in
  [reviews/round22.md](reviews/round22.md): one revision
  pass (this commit), then round 23 with the same seats.
- Round 23 (the same three seats, sixteenth pass — closure
  audits of their own round-22 findings, then fresh attack on
  the round-22 revision's own text): **3 distinct significant
  + 3 minor.** The headline closure: S re-proved the descendant
  frontier consumer DYNAMICALLY — a real spawned child, the
  second provider call held, the typed signal returned from
  RecordDescendantAppEvent, metadata == field == full-history
  under -race, with the causal red (consumer disabled)
  reproducing the blank — so the mechanism itself survived;
  every finding sat in the pass's own text, in three places.
  T proved the named install point (the ThreadStartedParams
  arm) sits INSIDE the CommitProjection closure, so the
  "outside the projection gate" claim contradicted the
  zero-calls invariant the gate pins, and the cited
  drain-goroutine sampling rule cannot cover a route that
  runs on the child's emitter goroutine; T also found the
  setter replacing the dispatcher after attach (production
  wires history after construction, so the internal listener
  is stripped at every daemon start); R found the handoff has
  no incarnation fence — idle release unhooks the old runtime
  before teardown and a replacement reuses the child session
  ID, so a delayed old callback can regress the cached row
  past the root-only fence. Three-seat convergence on the
  seed input's scope at the fresh attach site. **The pass moves
  BOTH the sample and the install outside the projection gate
  with the resolution through construction-bound sampler
  closures, fences the row write with the child's boot
  generation (a delayed stale callback is dropped), makes the
  setter recompose through the compose path, scopes the
  verbatim-caller invariant to the nil-OriginalOrdinal copy
  class, and states the two seed inputs (fresh nil, restore
  its validated entries).** All reds reproduced exactly across
  all three seats, zero disputed. Reconciled in
  [reviews/round23.md](reviews/round23.md): one revision
  pass (this commit), then round 24 with the same seats.
- Round 24 (the same three seats, seventeenth pass — closure
  audits of their own round-23 findings, then fresh attack on
  the round-23 revision's own text): **1 distinct significant
  (THREE-SEAT converged) + 3 minor.** The significant is the
  new fence's own residue: the token publication path is
  unbuildable as written — the child's boot generation is
  minted only after the constructor returns (spec 4's
  contract) while the sampler registers at attach, no
  per-child mint exists today (the only descendant token is
  root-derived, identical across a replacement), and no
  Server-owned current-incarnation value, publication, or
  lookup-miss rule is named. The mechanism itself is PROVEN:
  S drove the real replacement schedule through the actual
  reclamation seams (hold the old callback after its sample,
  real idle release, cold restore of the same child id) and
  with a per-child mint supplied the fence dropped the stale
  write exactly once under -race, with three causal controls
  isolating the consumer, the comparison, and the mint.
  **The pass replaces the value-at-registration fence with a
  Server-owned incarnation registry — the entry {SessionID,
  boot generation, sampler, registration identity}, the
  owner's post-construction mint published into the entry
  before any child work, the route carrying its resolved
  entry across the sample, the row write comparing carried
  vs current, release removing only its own registration,
  and lookup miss = drop — plus the absent-when-missing
  honesty, the after-gate sentence, the notification-less
  coherence, the emitter lifetime, and the non-goal
  scoped.** All reds reproduced exactly across all three
  seats, zero disputed. Reconciled in
  [reviews/round24.md](reviews/round24.md): one revision
  pass (this commit), then round 25 with the same seats.
- Round 25 (the same three seats, eighteenth pass — closure
  audits of their own round-24 findings, then fresh attack on
  the round-24 revision's own text): **2 significant + 5 minor,
  every defect textual while every mechanism claim came back
  green.** The significants: the absent-generation
  characterization is mutually exclusive with the identity
  guard the registry specifies (three-seat — S's strict-form
  drive dropped the no-mint replacement on identity alone, so
  the spec's unfenced proceed is impossible in code the seat
  actually drove; T named the identity's unmentioned origin and
  the conflatable MISS-vs-absent; R graded the contradiction
  unbuildable), and the attach-before-mint bridge argument is
  false on restore (the restore constructor records a real
  first execution entry through failure recovery before it
  returns — chair-verified at the hops). The minors converged
  three-seat on the notification-less cost (the facet
  machinery is root-only, so the correction clause was false
  for child rows; S measured an indefinitely stale hydrated
  subscriber) and the close-budget qualifier (the join gives
  up; S measured the dropped queued refresh), plus the release
  landing zone at the shared reclamation unhook and the
  binder-factory precedent for the registration. **The pass
  makes the registration identity THE FENCE (it holds with no
  generation; the generation is durable corroboration;
  absent-when-missing is a MISS distinction, no unfenced
  proceed), states the constructor window as current-runtime
  writes with the failed-construction release named, lands
  release at unhookReclaimedRuntimeRecord, withdraws the
  bounded-loss analogy for the honest indefinite cost, and
  qualifies the close promises as best-effort within the
  shared budget.** All reds reproduced exactly across all
  three seats, zero disputed. Reconciled in
  [reviews/round25.md](reviews/round25.md): one revision
  pass (this commit), then round 26 with the same seats.
- Round 26 (the same three seats, nineteenth pass — closure
  audits of their own round-25 findings, then fresh attack on
  the round-25 pass's own text): **2 significant, one shared
  root — the pass wrote the registry's surrounding transport
  from precedent sentences without walking the hops.** The
  registration transport is one hop short everywhere
  (three-seat: the cited binder factory returns only
  Recorded/Published, is keyed by the durable session id, and
  runs before attach, so it cannot mint or carry the
  identity; the reclamation entry has no identity field; and
  the childSessionID-lookup fallback selects the
  replacement's entry — S's extended-factory prototype, with
  register/publish/release closures, a Server-side counter
  mint, and the runtime-held release identity, drove green
  and is the shape the pass now names), and the
  failed-construction writer set is wrong as named (the
  restore caller's error branch returns nil with no child,
  the spawn caller receives nil; the release is a
  constructor-local conditional defer on the existing
  failure-defer conditions plus the discardRestoredCandidate
  and disposeUnadoptedSubagentSession funnels — and without
  it the failed runtime's queued first-record callback
  installs after the nil return, measured dynamically,
  because the failure defer never closes the session).
  All five round-25 closures CLOSED across the seats; S's
  no-mint re-probe is now GREEN against the corrected text;
  the constructor-window drive confirmed R25-R-2's
  restatement; all reds reproduced exactly, zero disputed.
  Reconciled in [reviews/round26.md](reviews/round26.md):
  one revision pass (this commit), then round 27 with the
  same seats.
- Round 27 (the same three seats, twentieth pass — closure
  audits of their own round-26 findings, then fresh attack on
  the round-26 pass's own text): **1 significant, three-seat
  converged in fact — the adopted-runtime teardown never
  releases the registry entry.** Every closure the round asked
  for came back CLOSED, including the round-24 originating
  publication-transport hole after four rounds, and S's fresh
  overlay drove the named transport green on every probe
  (replacement, no-mint, MISS, comparison-off, the
  constructor-window write, the failed-construction pair, both
  post-success funnels, the literal journey-8 failure case,
  and the root control). The one significant: the "complete"
  writer set was missing its ordinary paths — every teardown
  funnels through teardownChildSessionWithPolicy, which
  reaches only releaseRuntime, so root close, retirement,
  explicit close and manage_worktree dispose leave the entry
  current (S measured entries surviving root close and
  identity replacement while the descendant rows went to
  zero; R found the dispose path and the falsified
  token-check reliance — the check drops only stale
  identities, so a racing current-identity callback matches);
  the release closure already cleans the entry when called,
  so the pass names the funnel as the release site. Three
  minors folded: the disarm wording (the flags flip at
  constructor success, before adoption), the nil-closure
  semantics (nil = no registration, no release), and the
  publish fence (the publish closure presents its identity;
  mismatch = no-op). All reds reproduced exactly, zero
  disputed. Reconciled in [reviews/round27.md](reviews/round27.md):
  one revision pass (this commit), then round 28 with the
  same seats.
- Round 28 (the same three seats, twenty-first pass — closure
  audits of their own round-27 findings, then fresh attack on the
  round-27 pass's own text): **CLEAN — zero significant findings
  from any seat, the set's first clean round.** Every closure came
  back verified: the round-27 funnel release is closed by
  construction (`releaseRuntime`'s only production callers are the
  funnel and the session's own terminal close) and dynamically
  (S's fresh 20-file overlay drove real close, reclamation,
  disposal, retirement-to-restore, nil-closure and root-control
  entrypoints green under `-race`, with the no-release controls
  reproducing each red); all five round-22 positives confirmed.
  Three one-clause minors folded: the round-26 landing-site
  sentence amended in place (the release lands at both the
  reclamation unhook and the funnel), the nil-closure rule's row
  consequence stated (superseded-root descendants take the MISS
  and drop), and the racing-callback sentence restated with its
  three outcomes (a late callback beginning after a replacement
  registers may safely resample it; the guarantee is that a
  callback already carrying a released runtime's entry can never
  install it). All reds reproduced exactly, zero disputed.
  Reconciled in [reviews/round28.md](reviews/round28.md): one fold
  pass (this commit) — all four specs READY from every seat.

A round that comes back clean across fresh reviewers gates the
implementer lanes.

- Implementation amendment 1 (lane 3's implementation-phase PAR,
  chair-approved): two competing reviewers found — and the lane
  reproduced through the retained-runtime reclamation and
  cold-restore seams, re-verified by the chair in both generation
  modes — a same-registration ordering hole in the descendant
  frontier's install fence: a late route holding an older sample
  of the SAME registration can regress a newer installed
  frontier, because the fence compares identity and generation
  only. This commit amends spec 3: the frontier field family is a
  (turn id, stamp sequence) pair minted under the leaf, the
  registry entry records its last successful install, and the row
  write is newest-wins within a registration. Journey 8 gains
  the SAME-REGISTRATION STALE-INSTALL case; the lane's preserved
  witness is its red.

## Spike evidence (tested, not traced)

Two mechanisms were too load-bearing to leave on citations, so red-test
spikes reproduced them against the base:

1. **Spec 2's red is real and fails pre-dispatch.** A windowed
   `read_transcript` call today dies at the agent's pre-dispatch schema
   gate with a generic type refusal (the live wire never sees the
   params; round 3 added a second gate: the hub's operation validation
   rejects content filters on Manager history reads outright). The
   spike also falsified the draft's budget arithmetic with real
   constants: 40 items × 8,192 default bytes = 327,680, over both the
   proposed 192KB budget and the 256KB read cap, before metadata and
   JSON escaping — which is why spec 2 extends the EXISTING read
   packer (`packItemCandidates`, whose 1MB soft limit sits above the
   Manager cap and so never helps these reads) to measure the encoded
   envelope at Manager scale, with a floor, instead of trusting field
   arithmetic.
2. **Spec 3's silent stale serve reproduces through both fallback
   seams**, with existing test fakes and no product changes: a
   caught-up saved index served an hours-old turn as newest while the
   daemon held fresh in-memory turns — the production incident's
   mechanism. The catch-up oracle is real and runs on every
   Manager-path serving point, but its authority is the transcript
   FILE's end; a live mid-run daemon sits ahead of the file. That
   file-end-bounded gap, not a broken oracle, is the defect spec 3
   makes visible — and round 3 sharpened it: the succeeded-empty seam
   serves those saved items under the LIVE daemon's BootGeneration,
   so the response's own identity claims liveness for saved content.
3. **The fence spike (landed, round 7's escalation).** Three paper
   revisions of spec 4's relay fence each broke in adversarial
   review, and round 7's breaks were design-placement: a fence
   checked after the durable answer cannot undo the answer, a gate
   on `startTurn` cannot cover the modes that answer mid-turn, and a
   refusal at consumption cannot reach a caller holding success.
   The spike wrote one test per verified failure mode against the
   real daemon seams — five reproduced reds, four gap-pinning
   characterizations, zero refutations — and the fence was designed
   FROM the failing tests
   ([fence-spike.md](fence-spike.md)): the pair compared inside the
   shared admission transaction before anything is durable (the
   receipt is the refusal), the consumed answer clearing exactly the
   calls captured with the pair at admission, restore resolving
   exactly what the CAPTURED evidence names — round 8's amendment: the
   admission-time composites carried on the journal record and the
   resolving turn, because the durable answer names executions,
   sampled at consumption, and cannot express the subtraction — one
   common hub-side prepare on all three modes with its row read
   scoped to pair-bearing requests, and the composite (turn id, call
   id) identity end to end. The reds live on this branch, failing
   against today's code by design; they are the implementer lane's
   starting corpus. Round 8's three seats re-ran them independently —
   every one reproduced for the documented reason — and the revision
   pass amended the design sections of fence-spike.md in place
   ([reviews/round8.md](reviews/round8.md) is the reconciliation of
   record). Round 9 re-ran and re-audited with the same seats: all
   five reds reproducing exactly, 25 of 28 closures verified closed,
   and the record amended once more
   ([reviews/round9.md](reviews/round9.md)) — the pair's wire carriers
   designed end to end (the state field, the probe's row copy,
   `LiveEntry`, `AttentionEntry`, the frame, the relay params), the
   steer writers set from the record rather than live samples, the
   fourth evidence writer (`recordClientMutationFailure`)
   inventoried, the capture widened to `(calls, legacyCount)` for
   call-less legacy questions, the replacement-record capture list
   with its dominance rule, and the direct-user-input branch named as
   the third inherently-unfenced whole-set exception. Round 10
   re-audited a second time with the same seats: all five reds
   reproducing exactly, 23 of 26 closures verified closed, and the
   record amended in place once more
  ([reviews/round10.md](reviews/round10.md)) — the steer writers'
  rule split by field KIND (evidence from the capture, links from
  the capture minus the active execution, unfenced and legacy
  behavior preserved), and the capture's count half riding the
  resolving turn beside the calls. Round 11 re-audited a third
  time ([reviews/round11.md](reviews/round11.md)): every round-10
  fence closure verified, zero new fence defects, the reds
  reproducing exactly — the fence is converged at the design
  level, and the record's steer-writer citations were corrected to
  the real function names. Round 12 re-audited a fourth time
  ([reviews/round12.md](reviews/round12.md)): the convergence
  held — zero new fence defects again, every round-11 semantics
  closure verified under fresh attack — and the record's §6 chain
  enumeration gained the round-12 additions (the call-less count
  and the ask-pageable fact, spec 4's own carriers riding the
  same chain), the fence core untouched. Round 13 re-audited a
  fifth time ([reviews/round13.md](reviews/round13.md)): the
  convergence held again — zero fence findings in either
  direction — and the §6 chain gained the round-13 clamp
  metadata (the composite total and truncation fact), the fence
  core still untouched. Round 14 re-audited a sixth time
  ([reviews/round14.md](reviews/round14.md)): zero fence
  findings from any seat, all five reds reproducing exactly —
  the §6 chain gains the round-14 mint-site split (the
  truncation fact stamps at the wire projection), the fence
  core still untouched.

## Grounding summary

Every spec's current-behavior section was verified against the base by
a read-only scout pass with path:line citations, then adversarially
re-verified across three review rounds. The decisive facts:

- The discovery rows and the Manager `job_status` read ALREADY carry the
  pending-question flag and the question's own bounded text
  (`Evener.AskPending`, `Evener.PendingQuestion` via `threadFromEntry`,
  `cmd/evener-hub/internal/appsource/local_daemon.go:1488-1504`). What is
  genuinely absent from pull: the attention level, and any documentation
  that the existing fields are there at all.
- `NeedsResponse` has a tested meaning this spec set deliberately
  preserves: `awaiting && !askPending` (`hubapi/attention.go:75-78`) — a
  pending question makes it FALSE (the human is answering). No spec adds
  it to rows; specs assert the components and the guides state the rule.
- The Manager's attention roster is archive-blind and remote-inclusive
  ("Archiving clears human attention, not the Manager's live population",
  `cmd/evener-hub/main_background.go:63-72`, pinned by
  `attention_roster_archive_test.go`): rows follow that population —
  archived and remote lanes keep their attention facts, subagents never
  have them. The daemon roster behind the probe facts is local-only,
  so spec 1 states a per-field remote story: remote rows carry their
  own hub's thread-row facts and the attention join; the local probe's
  additions are absent-when for them.
- The running-turn facts exist on the daemon wire
  (`ActiveTurnID`/`ActiveTurnStartedAt`) but the hub probe drops them;
  the crashed lane's facts exist in the roster's retained record
  (roster `Status: "errored"` plus the `Crashed` flag; the row's own
  `ThreadStatus` vocabulary names the value `systemError`, which hub
  and client normalize to `errored`)
  but discovery drops the row; per-source outcomes exist inside the
  merge but never reach the response. Spec 1 closes hops, it does not
  build sources.
- The wire ALREADY carries a coarse content selection
  (`ThreadContentParams`, documented in
  `docs/product/history-content.md`) whose categories are coarser than
  item types (`agentMessage`, `userMessage` and user steering share one
  bucket; `Selection.Item` drops what it excludes), and the Manager
  path has two gates refusing it (the agent schema gate and the hub's
  operation validation). The hub ALSO already packs large reads
  (`packItemCandidates` trims to a 1MB soft limit) — but that limit
  sits above the Manager's 256KB whole-read cap, so Manager reads die
  at the cap before the packer helps: extending the existing packer to
  Manager scale is the fix, not a second packer. Spec 2 owns an
  exact-type predicate applied
  by the windowed handler, elision computed from the full item list;
  the saved-index windows are item-shaped, so turn windows are owned
  new machinery, named as such.
- The saved-index serving points are marked only by a magic constant,
  and the succeeded-empty seam merges saved items under the live
  daemon's BootGeneration (`fallbackHistoryIdentity`) while
  `Authoritative: true` is stamped against its own documented meaning —
  a flag the typed clients key held-item replacement on. Spec 3
  replaces the constant with serving facts derived from the serving
  BRANCH (`servedFrom`, `asOf` stamped at serve-time catch-up,
  `frontierTurnId` in the pinned execution-anchor semantics,
  `instanceId` from the served thread row), and migrates the
  `Authoritative` clients deliberately. Staleness stays the caller's
  computation: a hub-asserted verdict was falsified in round 2.
- A pending ask APPENDS: a second `ask_user` in the same turn grows the
  set (the flags never change), the excerpt is first-question-plus-count,
  and the ask machinery's own comment says the excerpt is insufficient
  for an external responder. Spec 4 carries the ask facts (the
  composite displayed-call identity, the count, the excerpt flags) from
  one atomic snapshot, makes the excerpt a notice whose relay contract
  requires the full ask before answering, and resolves the whole set on
  admission. The pending-set's change identity and the relay fence —
  three paper revisions of which each broke in review (rounds 5, 6, 7)
  — were redesigned by the RED-TEST SPIKE
  ([fence-spike.md](fence-spike.md), landed): five reproduced reds and
  four characterizations against the real seams drove a fence that
  compares the pair inside the shared admission transaction before
  anything is durable (the receipt is the refusal), clears exactly the
  calls captured with the pair at admission (an ask appended after
  admission survives, no window), resolves across restore exactly what
  the CAPTURED evidence names (round 8's amendment: the admission-time
  composites, carried on the journal record and the resolving turn),
  gates older daemons through one common
  hub-side prepare on all three input modes with its row read scoped
  to pair-bearing requests, and carries the composite
  identity end to end so the exact-turn fetch is addressable. The
  broker's pending-set trigger is spec 4's own design, not the spike's.
- The attention enrichment cannot live in `threadFromEntry` (an import
  cycle: `hubcore` imports `appsource`); the seam is the discover
  projection post-fan-out, where the attention broker is in scope. The
  roster and broker refresh loops are independent, so the join carries
  its derivation moment and a stated tolerance.

The night's incident is consistent with, but not proven to be, version
skew: the fleet's running hub predates the recent fixes the armed
redeploy carries, so the pull path's live behavior against the running
build is unverified and is not asserted as evidence in any spec.

The Manager-facing tool contracts live in `docs/automation.md`, and
Managers learn their tools from the Manager prompt template
(`agent/testdata/messagingprompt/manager-on.md`); every spec that changes
a tool surface updates both in the same change. Each spec's acceptance
also requires its PR description to name the version direction its
older-daemon tolerance serves and the condition that removes it, per
the compatibility policy in `docs/product/README.md`. Spec 2 adds a
new `ThreadCapabilities` bit and names it and its removal condition
explicitly.

## Cross-cutting rules every spec obeys

1. **Extend existing surfaces before inventing new ones.** The daemon's
   activity meter (`server/activity_meter.go`), the hub's roster
   aggregation (`cmd/evener-hub/app_activity.go`), the durable
   delegate/session state that `job_status` reads, the wire's content
   selection, the roster watch's fixed event set and the saved-index
   windows already exist. Each spec extends one of these; none
   introduces a parallel source of truth. Where a spec genuinely needs
   new machinery (spec 2's turn windows, exact-type predicate and
   version-fenced fetch cursors; spec 3's per-view frontier resolver;
   spec 4's pending-call identity carrying path), it names that
   machinery as owned work with its landing zones, never as existing
   behavior.
2. **Absent, not fabricated.** A field a daemon, host, or projection
   cannot supply truthfully is absent from the response with its
   reason, never filled with a stale or zero value — and volatile facts
   whose observation has gone stale go absent too (spec 1's
   `probeState: retained`), with a marker naming why. The pulse
   meter's staleness rule (`docs/product/pulse-meter.md`) is the
   precedent.
3. **Freshness is part of the answer.** Any read that serves derived or
   projected state says what moment it reflects and which view served
   it (spec 3 owns the facts: `servedFrom`, `asOf`, `frontierTurnId`;
   spec 1's `builtAt` and `attentionAsOf` name their own clocks and are
   never compared across clocks). The hub states facts; the caller
   computes staleness against the fleet picture. Two tools answering
   about the same session must agree, or the disagreement must be
   computable from the payloads (specs 1, 2 and 4 consume the facts).
4. **Read paths degrade without blocking work.** No spec adds a gate
   that refuses a read or watch delivery when a non-critical
   enrichment is missing: the row is served without the missing field.
   A read may never silently serve wrong data, but it may always
   serve less data, labeled. The standing exceptions are existing
   honest failures kept, not new gates: the fallback path's typed
   failure when catch-up cannot run (locked or rebuilding index),
   spec 2's typed `window-unsupported-on-daemon` for windows a
   legacy page cannot truthfully serve, and spec 4's typed
   `ask-fence-unsupported-on-daemon` page refusal — a fail-closed
   capability gate in that same class, refusing a page a bit-less
   daemon cannot fence rather than serving it unfenced.
5. **Manager-tool surface compatibility, and no silent schema rot.**
   New parameters are additive and opt-in; existing call shapes keep
   their current meaning. Older daemons that cannot answer a new
   field are represented by absence, per the hub/daemon skew
   direction in `docs/product/README.md#compatibility`. Where the
   wire's strict decoding makes a new parameter unsendable to an
   older daemon (spec 2), a capability bit gates it, the remote
   source forwards it, and the degradation is visible in the
   response. Every new wire field lands with regenerated AppWire
   types and updated appwire-client validators in the same change —
   the typed clients validate strictly, and the repo's
   generated-output gates enforce the regeneration.
6. **One implementer lane per spec, with owned interfaces.** Where one
   spec consumes a field another produces, the consumer treats the
   field as absent-when-missing (rule 2), so no lane blocks on
   another. Interfaces between specs are named explicitly in each
   contract, and every shared field has exactly one producing spec:
   spec 3 owns the serving facts (`servedFrom`, `fallbackReason`,
   `asOf`, `frontierTurnId`, `instanceId`); spec 4 owns
   the attention row fields (`attentionLevel` and `attentionAsOf`;
   ask-pending keeps the row's existing `Evener.AskPending`), the
   frame's and excerpt's question facts, the atomic ask snapshot, the
   pending-set identity carriers, the broker's pending-set change
   trigger, the relay fence, and the session boot-generation field's
   initialization (its producing spec; spec 3's descendant incarnation
   fence consumes it as durable corroboration and holds without it) —
   ONE lane, unconditional: the fence
   machinery re-entered spec 4 from the landed spike
   ([fence-spike.md](fence-spike.md)), whose reds live on the branch
   as that lane's starting corpus; spec 1 owns
   the running-turn facts, crash-row inclusion, per-source states,
   `probeState` and `builtAt`; spec 2 owns the capability bit, the
   turn windows (including the exact `{turn: id}` form), the
   exact-type predicate and the fetch cursors. Consumer journeys
   assert another spec's fields only in a build where those have
   landed (asserting absence otherwise), so any order builds without
   guessing.

## The evidence base (one night, one Manager)

The Manager ran six concurrent lanes for eight hours on a hub whose
daemon build predates the delivery-reliability fix. Observed, with
direct reads as proof:

- Sweeps re-read every lane's latest turn every 30 minutes because no
  single call returns the fleet's live state. Each sweep costs one
  heavyweight transcript read per lane; the responses carry full raw
  tool outputs and usage blocks that the sweep never asked for,
  dominated by multi-kilobyte `job_status` echoes and evidence-comment
  bodies.
- Locating one specific older turn (a roborev verdict relayed two turns
  back) required three backward pages through opaque projection
  cursors, each page returning items rather than turns, and each page
  dragging payloads the search did not want.
- One transcript read of a delegate served an 11-hour-stale turn while
  `job_status` for the same delegate showed a run started minutes
  earlier with fresh activity. Nothing in either payload marked the
  disagreement; the Manager had to reconcile by hand and decide which
  instrument to believe.
- A top-level lane posted `ask_user` and ended its turn waiting for the
  human. The roster's `needs_you` machinery covers this case, but the
  fleet held no watches (the delivery channel was fabricating frames
  all night, so every watch had been cleared as untrustworthy) and the
  Manager never ran a discovery read while the question was pending,
  so nothing surfaced it until a sweep happened to read that lane's
  transcript — roughly 30 minutes later. Unproxied, it would have
  stalled an approved pipeline indefinitely. At base the pull rows
  carry the question flag and its text (grounded above); what no
  surface carried to the Manager was the attention level, and what no
  document ever taught the Manager was that the fields existed at all.

## Spec index

| Spec | Gap | One line |
| --- | --- | --- |
| [1-fleet-snapshot.md](1-fleet-snapshot.md) | No fleet view | One `job_list` call returns every lane's operational state, crashed lanes included, fresh or probe-marked |
| [2-cheap-filtered-reads.md](2-cheap-filtered-reads.md) | Reads are expensive | Turn-addressed, budget-measured transcript reads with an exact item-type predicate the wire can honor |
| [3-transcript-status-consistency.md](3-transcript-status-consistency.md) | Instruments disagree | Every transcript read states which view served it, at what moment, from which branch |
| [4-question-surfacing.md](4-question-surfacing.md) | Questions are invisible | Pending questions surface to any watching Manager — and deliver when the pending set changes |
