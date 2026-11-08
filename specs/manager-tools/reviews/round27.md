# Round 27 — three-seat reconciliation (record of decision)

Seats: R gpt-5.6 (`dlg_034tsMgJAr4fYqjXqwjva0`), S gpt-6.1-sol-1m
(`dlg_034tsMjunHGTLxWtH5q1MH`), T deepseek-4.1-flash-background
(`dlg_034tsMmQrK6zgRaM8yRI4J`). Reviewed at `76f6d5949a` (the
round-26 revision pass, on top of `c32452c95b` the round-26 record)
over the production-identical base `2be411cd8f`. All three seats ran
the required reds uncached (`-count=1 -v`, bare `|`, sources
inspected first): 5/5 red families reproduced for their documented
reasons, 4/4 characterizations passing, zero disputed, worktrees
clean, owned scratch removed. R re-ran the two sampling proof tests
and the compaction-replay harness — PASS.

## Closures

- **R26-R-1 — CLOSED (R; S and T concur).** The transport is now
  named as additive owned work with every hop accurate: the factory
  returns only `{Recorded, Published}` and binds `fn(s.id)`
  (`agent/session_conversation_provenance.go:209-230`), invoked at
  `agent/session_init.go:530`/`:1189` before attach `:696`/`:1427`,
  which `attachTranscript` never calls; the registration extends the
  factory's return with register/publish/release closures; the mint
  is a `Server.mu`-guarded counter at attach; the closures are
  retained on the constructing Session; every release presents the
  runtime-held identity, the `childSessionID` lookup explicitly
  forbidden. R's consumer sweep: only two in-tree constructors of
  the hooks (production `server/appwire_histories.go:54`, test
  `server/history_publication_restore_test.go:152`), all empty or
  named-field literals — additive fields break no caller. S's
  consumer sweep agrees and notes the factory stays descendant-only
  by construction (the root's hooks install directly at
  `server/appwire_histories.go:49-53`; the root's own attach
  precedes the factory's installation, so a root can never
  register — S's root control measured `rootFactoryCalls=0`,
  `registryEntries=0`).
- **R26-R-2 — CLOSED (all three seats).** The constructor-local
  ownership is accurate: the flags arm at construction start and
  flip at constructor success (fresh `initComplete` `:448` →
  `:720`; restored `restoreComplete` `:903` → `:1593`), the
  constructors' post-attach error returns are covered, and the two
  pre-adoption funnels own the post-success gap. S drove the
  literal journey-8 schedule green: recovery completes inside the
  constructor, a later step fails, the constructor returns nil, and
  the defer leaves `registrationPresent=false`,
  `postReturnInstallDelta=0`, `drops=1` — while the caller-only
  control still measures the install (`delta=1`) as the journey's
  red.
- **R24-R-1 — CLOSED (R).** The round-24 publication-transport hole
  is fully closed by the named transport: attach registers
  generation-empty, the owner completes the current entry through
  the retained publish closure, replacement installs a new
  immutable entry, release presents the runtime-held identity. The
  fence lifecycle's originating finding is settled after four
  rounds.
- **S26-1 — CLOSED (S, dynamic; T and R concur at the text).** S
  built a FRESH overlay (not the retained generator) implementing
  the named shape and drove it green under `-race -p 2`
  (`job ..._F2LE0WgXtiLy`, with an independently reconstructed
  byte-identical replay `..._Tl1telbAWkqz`, source retained at
  `..._uY04lChX8MUm`): replacement, no-mint, MISS, comparison-off,
  the current-constructor recovery write, the failed-construction
  caller-only red and defer green, BOTH post-success funnel calls,
  the fresh post-attach strategy error, and the root control.
- **T26-1..T-3 — CLOSED (T's own audit, chair-verified at the
  hops).** T explicitly concurs the corrected consequence matches
  the code and withdraws its round-26 leak-only claim.
- **R's round-22 positives — ALL FIVE CONFIRMED** (dispatcher
  wiring, emitter lifecycle, sampling proof coverage,
  compaction-replay harness, doors text).

## The round's one significant — three-seat converged in fact,
severity adjudicated on evidence

### R27-R-1 — significant (R27-R-1 = S27-S-1 = T27-3) — the
adopted-runtime teardown never releases the registry entry: the
"complete" writer set is missing its ordinary paths, and the
design's stated racing-callback rejection cannot fire there

The pass says the constructor-local defer plus the two pre-adoption
funnels are "completing the registry's writer set" and lands
healthy-runtime release only at `unhookReclaimedRuntimeRecord`
(spec `:846-857`, `:921-935`). Every ordinary teardown path
bypasses that seam, chair-verified at every hop:

- The root-close cascade: `closeOwnedDelegateRuntimeTree`
  (`agent/session_close.go:172-176`) → `closeRuntimeTree`
  (`agent/delegate_tree_stop.go:644`, calling `teardownChildSession`
  directly at `:740-750`) → `teardownChildSessionWithPolicy`
  (`agent/subagents.go:205-236`), which runs only
  `releaseRuntime` and environment cleanup — no release hop.
- `manage_worktree dispose`: `disposeStableExecute` calls
  `teardownChildSession` then `subagents.removeSession`
  (`agent/session_tools_worktree_dispose.go:227-232`), and
  `removeSession` deletes only the exact parent map entry
  (`agent/subagent_manager.go:190-196`, its own comment: "removes
  only the exact resident child").
- Root identity replacement resets the descendant ROW maps
  (`server/appwire_identity.go:176-180`) but not the Server-owned
  registry; Clear keeps the Server alive while closing the
  superseded root (`cmd/evener/serve.go:1135-1170`, both
  orderings), cascading the same unreleased teardown.

Consequences, both measured or constructed: S's dynamic probe
(`TestR27OrdinaryCloseRegistryLifetime`) printed
`entriesBefore=1 entriesAfterRootClose=1
entriesAfterIdentityReplacement=1` while `descendantRowsAfterIdentity=0`
— the sampler retains the closed Session after its tree stops being
served, and new child IDs never replace those entries; the retained
release closure DOES clean the entry when called
(`entriesAfterExactRuntimeRelease=0`), so the fix is naming the
site. And R's behavioral instance: a first-record callback already
delivered to the route before the stream close is processed with
the STILL-CURRENT identity and installs on the disposed child's
row — the spec's own sentence leans on the token check for
racing callbacks ("the token check inside the consumer respects
that contract", `:950-956`), but the check rejects only STALE
identities; a racing CURRENT-identity callback matches while the
entry is unreleased, so the stated rejection cannot fire at this
path.

Severity adjudication, owned plainly: T graded its instance minor
(leak-only — the event-stream close gates post-teardown delivery)
and S graded minor ("a process-local retention defect, not a
demonstrated wrong-row install; I measured retained entries, not
RSS growth"). R graded significant: the writer-set completeness
claim is false at a constructible path, and the design's stated
safety mechanism for racing callbacks is vacuous there. The chair
sustains R on the set's own bar — round 24's vacuous-as-written and
round 25's mutually-exclusive-rules precedents both graded exactly
this class significant — while recording that S's and T's
gradings rest on the same verified facts. The fix is one family
and cheap: the release already exists on the Session; only its
landing site is unnamed.

## Minors

- **T27-1 — the disarm clause names a point that does not exist.**
  "Disarmed only on the successful ownership handoff" — the flags
  flip at CONSTRUCTOR success (`agent/session_init.go:720`,
  `:1593`), before the caller's validation discards; the funnels
  own that gap (already stated). R read "handoff" as the
  constructor-to-caller return and declined the finding; T reports
  it; the honest fix names the flag flip, which is what the code
  does.
- **T27-2 — the nil closure set is undefined.** Two in-tree
  consumers produce it: `WireTranscriptHistory` returns the
  zero-value `TranscriptPublicationHooks{}` for a superseded root
  (`server/appwire_histories.go:56-57`), and the restore test
  wraps only `Published`
  (`server/history_publication_restore_test.go:152-168`). An
  unconditional register-at-attach would panic in that test and
  register nothing for superseded-root descendants. Fix: a
  nil/absent closure set means NO registration and NO release —
  the register step skips nil.
- **T27-4 — the publish writer is not identity-fenced the way
  release is.** A publish resolving "the current" entry after a
  replacement attach would complete the replacement's entry with
  the old generation. T could not construct a reachable schedule
  (spec 4's mint precedes the first admission; constructions
  serialize per child id), so minor, not behavioral. Fix: the
  publish closure also presents its held identity — mismatch =
  no-op.

## S's dynamic re-probe (the mechanism of record)

The fresh 19-file overlay drove the named transport end to end
under `-race -p 2`: replacement through real create, idle unhook and
cold send/restore (identities 1/2, generations 1/2, `drops=1`, B
retained, `oldReleasePreservedReplacement=true`); no-mint
replacement (empty generations, the drop on identity alone); MISS
(unregister mid-flight, `drops=1`); comparison-off (A installs,
`drops=0`); the current-constructor recovery write (one install
before return, `frontier=turn_m1`); the failed-construction pair
(caller-only `delta=1 drops=0`; defer `delta=0 drops=1`); both
post-success funnel calls (registration present after constructor
success, absent after the funnel, the delayed callback
`delta=0 drops=1`); the literal journey-8 post-recovery failure
(recovery completes, the strategy install fails, registration
absent); the fresh post-attach strategy error (caller-only entry
remained, local-defer entry removed); and the root control
(`rootFactoryCalls=0 registryEntries=0 rootFrontier=turn_m1`). A
byte-identical independent replay reconstruction was verified
before scratch removal. Scope limits stated by S: the funnel cases
invoke the funnels directly rather than through their production
callers, and the probe emitter is not the full managed-emitter
implementation.

## Disposition — one fix family plus three folds, one pass

1. **THE PRE-TEARDOWN RELEASE (R27-R-1 = S27-S-1 = T27-3).** The
   identity-conditional release lands at the ordinary-teardown
   funnel: `teardownChildSessionWithPolicy`
   (`agent/subagents.go:205-236`) — the single funnel every
   ordinary path reaches (the root-close cascade, retirement's
   non-terminal policy, explicit child close, and `manage_worktree
   dispose`'s `disposeStableExecute`) — presenting the Session's
   retained release closure IDEMPOTENTLY before the runtime
   release. The reclamation unhook and the funnel release are then
   the same idempotent operation in both orders, and
   A-release-never-removes-B is preserved. The "completing the
   registry's writer set" claim becomes true. The reclamation-path
   sentence stands as written (unhook-before-teardown makes a
   racing callback a MISS there); the ordinary-teardown paths no
   longer lean on the token check for a racing CURRENT-identity
   callback — the funnel release makes it a MISS too. Journey 8
   gains the WORKTREE-DISPOSE case: hold a first-record callback,
   dispose a stable worktree delegate, release the callback, assert
   the MISS/drop and the entry removal. The round-26 record's
   "completing the registry's writer set" claim amended in place.
2. **THE DISARM WORDING (T27-1).** The defer is disarmed when the
   flag flips at constructor success (`:720`/`:1593`); the
   pre-adoption funnels own the post-success gap, as the next
   sentence already states.
3. **THE NIL CLOSURE SET (T27-2).** A nil/absent closure set means
   NO registration and NO release; the register step skips nil, so
   the superseded-root zero-value hooks and the partial test wrap
   stay coherent.
4. **THE PUBLISH FENCE (T27-4).** The publish closure presents its
   held identity — mismatch = no-op — closing by construction a
   surface no schedule reaches today.
5. README trail twenty-seven; fence-spike nineteenth re-audit
   (zero ask-fence findings; 5/5 reds and 4/4 characterizations as
   documented in every seat).

## Readiness

- Spec 1 — READY from all three seats (R: eighteenth consecutive).
- Spec 2 — READY from all three seats.
- Spec 4 — READY from all three seats.
- Spec 3 — NOT READY (R; T's and S's minors-only judgments
  recorded — the chair sustains R: the writer-set completeness
  break is the same handoff bar rounds 24 and 25 set).

Significant trajectory r11-r27: 7, 5, 4, 5, 7, 4, 2, 2, 3, 2, 2, 1,
3, 1, 2, 2, 1. The round's shape is the set's strongest yet on
mechanism: every closure the round asked for came back CLOSED (the
round-24 originating finding among them, settled after four
rounds), S's fresh overlay drove the named transport green on
every probe, and the one significant is a single unnamed landing
site for a release S proved already works when called. The fix is
one family; the minors are one-clause folds.

## Round 28

Same three seat families, new delegates: closure audits of their
own round-27 findings (R: R27-R-1; S: S27-S-1 plus a re-probe of
the pre-teardown release through the funnels and the dispose path;
T: T27-1..T-4) plus fresh attack on the pass's new text: the funnel
release's idempotence and both-orders safety against the
reclamation unhook, the dispose gate's interplay
(`trySetDisposeGate`/`clearDisposeGate`) with a racing callback,
retirement's non-terminal policy path, the nil-closure register
skip, the publish fence, and the journey's dispose case.
