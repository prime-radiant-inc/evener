# Round 26 — three-seat reconciliation (record of decision)

Seats: R gpt-5.6 (`dlg_034trJoupwTvPvTyRuimZa`), S gpt-6.1-sol-1m
(`dlg_034trJrplGAuhPu5pS9Rtq`), T deepseek-4.1-flash-background
(`dlg_034trJtWA8ro5LSBNIqYLn`). Reviewed at `9bbd3f315e` (the
round-25 revision pass, on top of `2ba2c7debf` the round-25 record)
over the production-identical base `2be411cd8f`. All three seats ran
the required reds uncached (`-count=1 -v`, bare `|` alternation,
sources inspected first): 5/5 red families reproduced for their
documented reasons, 4/4 characterizations passing, zero disputed,
worktrees clean, owned scratch removed (S replayed its own harness
from a retained source job and verified all 17 overlay files
byte-for-byte before removal; R created no scratch; T removed its
own). R additionally re-ran the two sampling proof tests and the
compaction-replay harness — PASS, all three named tests.

## Closures

- **S25-1 — CLOSED (S, dynamic; R and T concur at the text).** The
  corrected text now matches the drive: the identity is the fence,
  carried-vs-current mismatch drops unconditionally, MISS is
  distinguished from the absent build, no unfenced proceed. S's
  no-mint re-probe is now GREEN against the corrected text
  (identities 1/2, generations equal-empty, `drops=1`, B
  retained) — the round-25 contradiction is gone.
- **S25-2 — CLOSED (S, re-measured).** The honest indefinite cost
  re-probed: `subscriberHydrated=""`, `newFrontierNotifications=0`,
  a subsequent routed status notification carried no frontier;
  recovery = a full metadata read or the child's next
  `ThreadStarted`.
- **S25-3 — CLOSED (S, re-measured).** The close-budget cost
  re-probed: budget expired, `closeReturnedBeforeEmitter=true`,
  `queuedRefreshDelivered=0`; journey 8 holds the callback after
  its sample and never rides the drain.
- **N25-T-1..T25-T-5 — CLOSED (T's own audit, chair-verified at
  the hops).** The bounded-loss withdrawal, the close-budget
  qualifier, the release landing seam, the MISS-vs-absent
  distinction, and the both-builds journey pins all landed as T
  named them.
- **R25-R-1 — CLOSED (all three seats).** The identity-first
  comparison text matches the code and the drives.
- **R25-R-2 — PARTIAL (R).** The constructor-window restatement is
  correct and dynamically confirmed (S's constructor-window drive:
  the recovery's actual first write inside construction installs
  through its own current identity, exactly one install before
  return). The FAILED-CONSTRUCTION release half is false as named —
  R26-R-2 below.
- **R25-R-3 — PARTIAL (R).** The landing seam is correct and shared
  (`:466` capacity, `:656-674` idle). The identity CARRIER at that
  seam is still unnamed — R26-R-1 below.
- **R25-R-4, R25-R-5 — CLOSED (R's audit).** The honest-cost and
  close-budget statements match the implementation; no remaining
  overstatement.
- **R24-R-1 — PARTIAL (R).** The identity-first logic is sound and
  dynamically closed; the token-publication TRANSPORT is still one
  hop before a constructible registration path — R26-R-1.
- **R's round-22 positives — ALL FIVE CONFIRMED**, including the
  emitter-lifecycle positive, which RECOVERED with the round-25
  budget qualifier exactly as the pass predicted.

## The round's two significant — every load-bearing claim
verified in-tree by the chair at the exact hop

### R26-R-1 — significant (three-seat factual convergence:
R26-R-1 = T26-1 = T26-2, with S's prototype as the feasibility
evidence) — the registration transport is one hop short everywhere:
the named factory cannot perform the registry protocol, and the
release has no identity carrier

The pass says the registration "is a fresh binding from the same
factory shape the existing per-descendant binder uses
(`agent/session_conversation_provenance.go:217-231`, invoked at
`agent/session_init.go:530`/`:1189` …); attach (`:696`/`:1427`) is
the timing" (`3-transcript-status-consistency.md:843-852`), and the
round-25 record says "the factory is the registration mechanism"
(`reviews/round25.md` disposition 1). The cited factory cannot
perform it, chair-verified at every hop:

- `TranscriptPublicationHooks` contains only `Recorded` and
  `Published` callbacks (`agent/session_conversation_provenance.go:209-215`);
  `bindDescendantTranscriptPublication` calls `fn(s.id)` and
  assigns only those two fields (`:225-230`) — keyed by the
  DURABLE session id, identical across a replacement. No sampler
  enters the Server, and no registration identity, generation
  publisher, or release handle returns to the child.
- The factory is invoked at `agent/session_init.go:530`/`:1189`,
  BEFORE attach at `:696`/`:1427` (the factory's own comment:
  "invoked once per new or restored descendant, before its writer
  attaches"); `attachTranscript` (`agent/session.go:2867-2901`)
  installs the writer, flushes held turns, and never invokes the
  factory. "The factory is the registration mechanism" and "attach
  is the timing" cannot both describe the cited code.
- Normal reclamation carries no identity:
  `delegateRuntimeReclamationEntry` holds exactly `{delegateID,
  childSessionID, runtime, ownerRuntime, depth}`
  (`agent/delegate_tree_reclaim.go:29-40`); the claim builder adds
  nothing (`:308-320`); `unhookReclaimedRuntimeRecord` only calls
  `removeSession(childSessionID, runtime)` (`:435-443`). The spec's
  release-by-identity names no carrier at the hop it lands on —
  only the round-25 record's disposition 3 asserts the runtime
  holds the identity; the spec body never does — and the
  implementer's fallback, a lookup by `childSessionID`, selects the
  REPLACEMENT's entry (attach replaces the map entry), the exact
  failure the property forbids.

R grades significant (the central new mechanism's transport is
unconstructible as written); T grades the same facts as two minors
(T26-1 the carrier, T26-2 the mint point — "the mechanism is
proven feasible; one clarifying sentence each"); S's prototype is
both the feasibility evidence and the fix direction: the factory
EXTENDED with register/publish/release closures (additive owned
work), the identity minted Server-side (a `Server.mu`-guarded
counter at attach), the closures retained on the constructing
Session, the released runtime presenting its own identity through
its retained closure — S's drive proved A's release did not remove
B (`oldReleasePreservedReplacement=true` in both the replacement
and no-mint drives). The chair grades SIGNIFICANT on the set's
handoff bar (round-24 precedent R24-R-1: a mechanism one hop
before constructible is not ready): an implementer reading "the
same factory shape" against the factory's actual code cannot build
the registration. The mint's FORM stays deliberately opaque —
uniqueness per registration by construction is the contract (R and
S both decline counter-vs-guid as a finding).

### R26-R-2 — significant (three-seat factual convergence:
R26-R-2 = T26-3 = S26-1, with the consequence dispute settled by
S's drive) — the failed-construction writer set is wrong as named,
and without the constructor-local release the failed runtime's
queued callback installs

The pass says a FAILED construction — "the restore-discard path
after `agent/delegate_restore_idle.go:161` and the spawn error
path" — releases the incomplete entry by its registration identity
(`3-transcript-status-consistency.md:826-833`). Chair-verified:
neither named site can release.

- `agent/delegate_restore_idle.go:161-164` is the constructor CALL;
  its error branch returns `nil, false, err` with NO child to
  discard. The real discards are post-construction validation
  paths (`:167-215`) that funnel through
  `discardRestoredCandidate` (`agent/session_lifecycle.go:369`),
  also used by the owed-bootstrap caller
  (`agent/delegate_owed_bootstrap.go:194`).
- `agent/subagents.go:887-903` is the fault/NewSession-error
  branch: `subSess == nil`, only environment scratch is disposed.
  The real create-side teardown is `disposeUnadoptedSubagentSession`
  (`agent/subagents.go:322-350`, its own comment: "the create-path
  twin of `discardRestoredCandidate`"), reached via
  `disposeUnadopted` (`:900-926`) on grant/seed/adoption failures.
- BOTH constructors have post-attach error returns that orphan the
  registered entry: fresh `agent/session_init.go:697-709`
  (`flushPendingDelegateDeliveries`, `rearmRootDelegateAttention`,
  `installContextStrategy`), restored `:1448-1522` (the
  `refreshFromDisk` arms, `recoverClientMutationFailures` at
  `:1472-1474`, `recoverClientMutationInterrupt`, the attention
  rearm, the restore-side-effect retries, `installContextStrategy`)
  — all after attach (`:696`/`:1427`), all before the return at
  `:1594`.

THE CONSEQUENCE IS BEHAVIORAL, not a leak. The constructors'
failure defer (`agent/session_init.go:466-474`) cancels the session
context and closes the artifact store but never closes the session,
so `open` stays true (`agent/session_events.go:505-506` gates only
post-CLOSE events) and the queued first-record callback still
delivers after the nil return. S proved it dynamically (final job
`..._nbktLpgUICc4`, `TestR26ConstructorRecovery`, one actual
first-record callback through the real `after_user` recovery fault
seam): the caller-only control — release wired exactly at the
pass's named caller sites — printed `returnedChildNil=true`,
`registrationPresent=true`, `postReturnInstallDelta=1`, `drops=0`
(the failed runtime's delayed callback installed after the
constructor returned nil); adding the conditional release defer
INSIDE the constructor turned it to `postReturnInstallDelta=0`,
`drops=1`, `registrationPresent=false`. T's contrary leak-only
claim (the open gate stops delivery) is refuted by the same drive:
the gate applies to closed sessions, and a failed construction
never closes. S also reached a real fresh-constructor post-attach
error (an invalid injected strategy) with the same result.

R grades significant; T minor (with the wrong consequence); S
minor ("the caller-only control deliberately lacks the missing
release hop; the amendment localizes an already-required cleanup").
The chair grades SIGNIFICANT: the spec's own contract — "a failed
construction releases the incomplete entry" — is unimplemented by
every site it names, and an implementer wiring release at the named
sites ships the measured install hole. The fix is S's amendment
plus R's writer set, both below.

## Minors

None beyond the two significants' constituent facts. T's three
minors (T26-1 carrier, T26-2 mint point, T26-3 writer set) and S's
one (S26-1 the nil-return callers) are the two lifecycle gaps
above, graded conservatively by their finders; recorded here as
folded. T26-3's consequence claim (leak-only) is refuted and noted
above.

## S's dynamic re-probe (the mechanism of record; final job
`job_034trJrplGGvQw9KbaSfv2_nbktLpgUICc4`, reds job
`..._Xes7l63CJujo`)

Through the real seams under `-race -p 2`: REPLACEMENT green
(`carriedIdentity=1 currentIdentity=2`, A dropped once, B retained,
`oldReleasePreservedReplacement=true`); the NO-MINT drive now
GREEN against the corrected text (generations equal-empty, the
drop on identity alone) — S25-1's closure evidence; MISS green (a
real mid-flight unregister, `currentIdentity=0`, the carried
install dropped exactly once); comparison-off the causal control
(identities and generations distinct, A installed, `drops=0`);
CONSTRUCTOR-WINDOW green (a real crash-cut journal fixture through
the real start-acceptance/claim/`beginClientMutationFailure`
primitives, the recovery's actual first write inside construction,
exactly one install before the return, `frontier="turn_m1"`,
`generation=""`) — R25-R-2's restatement dynamically confirmed;
the notification-less and close-budget costs re-measured as above.
The registry mechanism — identity fence, completion, replacement,
MISS, constructor window — remains dynamically closed; this
round's defects are the transport and writer-set sentences around
it.

## Disposition — one pass, two fix families

1. **THE REGISTRATION TRANSPORT NAMED AS OWNED WORK (R26-R-1 =
   T26-1 = T26-2).** The "same factory shape" sentence WITHDRAWN
   as a mechanism claim and restated: the existing factory
   (`agent/session_conversation_provenance.go:209-231`) is the
   PRECEDENT for the per-construction fresh-binding discipline
   (its own comment: "Each invocation binds fresh hooks, so a
   retired producer cannot publish into its replacement"), cited
   accurately — it returns only `{Recorded, Published}`, is keyed
   by the durable session id, and is invoked at
   `agent/session_init.go:530`/`:1189` BEFORE attach;
   `attachTranscript` (`agent/session.go:2867-2901`) never invokes
   it. The registration is ADDITIVE OWNED WORK on that factory's
   shape: the factory's return gains register/publish/release
   closures; the Server mints the identity at attach (a
   `Server.mu`-guarded counter, unique per registration by
   construction — the mint's form otherwise opaque); the closures
   come back to the constructing Session and are RETAINED on it;
   the release presents the RUNTIME-HELD identity (the reclamation
   entry carries the runtime pointer,
   `agent/delegate_tree_reclaim.go:29-40` — never a `childSessionID`
   lookup, which selects the replacement's entry); S's drive is
   the feasibility evidence. The round-25 record's "the factory is
   the registration mechanism" amended in place.
2. **THE FAILED-CONSTRUCTION WRITER SET (R26-R-2 = T26-3 =
   S26-1).** The "restore-discard path after `:161` and the spawn
   error path" sentence WITHDRAWN and replaced: the release is
   CONSTRUCTOR-LOCAL — each constructor arms a conditional release
   defer AT REGISTRATION (attach), disarmed only on the successful
   ownership handoff (the existing `!initComplete`/`!restoreComplete`
   failure-defer condition at `agent/session_init.go:466-474` is
   the home — that defer already owns the incomplete session), so
   the constructors' own post-attach failures (fresh `:697-709`,
   restored `:1448-1522`) release the entry. The pre-adoption
   funnels carry the SAME conditional release:
   `discardRestoredCandidate` (`agent/session_lifecycle.go:369`,
   the restore-side funnel for the validation discards at
   `agent/delegate_restore_idle.go:167-215` and the owed-bootstrap
   caller `:194`) and `disposeUnadoptedSubagentSession`
   (`agent/subagents.go:322-350`, the create-side twin, via
   `disposeUnadopted` `:900-926`). The consequence stated
   honestly: without the constructor-local release the failed
   runtime's queued first-record callback installs after the nil
   return (S's caller-only control: `postReturnInstallDelta=1`,
   `drops=0`; the defer: `delta=0`, `drops=1`) — bounded by any
   later replacement (identity still fences) but real until then;
   the failure defer never closes the session, so the open gate
   does not stop it. Journey 8 gains the FAILED-CONSTRUCTION case
   (the caller-only red and the defer's green as the paired corpus
   items).
3. README trail twenty-six; fence-spike eighteenth re-audit
   (zero ask-fence findings; 5/5 reds and 4/4 characterizations as
   documented in every seat).

## Readiness

- Spec 1 — READY from all three seats (R: seventeenth consecutive).
- Spec 2 — READY from all three seats.
- Spec 4 — READY from all three seats.
- Spec 3 — NOT READY (R; T's and S's READY-with-minors judgments
  recorded — the chair sustains R: the two significants leave the
  registration transport and the failed-construction writer set
  unconstructible as written, the same handoff bar round 24 set).

Significant trajectory r11-r26: 7, 5, 4, 5, 7, 4, 2, 2, 3, 2, 2, 1,
3, 1, 2, 2. Both significants share one root, owned plainly: the
round-25 pass wrote the registry's surrounding transport from
precedent sentences without walking the hops — it cited the binder
factory as the registration mechanism (the factory binds two
transcript callbacks and runs before attach) and named
failed-construction release sites from the R25-R-2 fix shape
without checking that those callers receive nil. The mechanism
itself stayed green under every drive again — including this
round's no-mint, MISS, constructor-window, and failed-construction
probes — so the fix is textual: name the transport S already
prototyped and the writer set S already drove.

## Round 27

Same three seat families, new delegates: closure audits of their
own round-26 findings (R: R26-R-1/R-2; S: S26-1 plus a re-probe
against the named transport; T: T26-1..T-3) plus fresh attack on
the pass's new text: the extended factory's coherence (does the
register/publish/release closure set stay descendant-only — the
root session's own frontier rides the dispatcher and hook, not the
registry; does the extended return break any existing factory
consumer?), the constructor-local defer's arm/disarm window (armed
at attach, disarmed at the ownership handoff — is the disarm
ordering airtight against the owner's post-return mint in both
constructors?), the funnels' completeness (any other discard or
dispose path — fork children, Clear — that can orphan an entry?),
the journey's failed-construction pins, and the mint-opaque
contract.
