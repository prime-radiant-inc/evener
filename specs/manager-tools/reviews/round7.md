# Round 7 — reconciliation and disposition

Reviewed at commit `7be9e8da56` (round-6 revision), code base `2be411cd8f`
(the specs' stated base; origin/main has since moved to `c4ac1c66b2` — drift is
reconciled when implementer lanes branch fresh main). Seats, all never-seated
models, all leaves, high effort:

- **T** `lunaroute/muse-spark-open-background` (dlg_034tPSvzwMeV2Ai4iGy8fC),
  run 10:42–10:45: 3 significant + 2 minor.
- **R** `codex-jesse-fsck.com/gpt-5.6-luna` (dlg_034tPSnpd0EjibwHMinhs3),
  run 10:42–10:51: 3 significant + 0 minor.
- **S** `codex-jesse-fsck.com/gpt-5.6-sol` (dlg_034tPSsxJZGMsgER9VC4A5),
  run 10:42–11:27: 7 significant + 2 minor.

Seat notes: two gpt-5.6-family seats again (luna + sol, distinct models — same
overlap shape as round 6, noted there; every alternative was worse: the author's
glm-5.3 family was excluded, deepseek-4.1 family was seated twice already,
codex-auto-review lacks `supportsTools` in the catalog while every working
codex seat lists it). S ran 45 minutes and found the round's deepest findings;
seat speed correlated inversely with depth. All three seats used `git show`/
`git grep` at `2be411cd8f` — the method is now uniform.

Every load-bearing claim below was re-verified in-tree by me at the exact hop
named (the round-6 grounding lesson). Nothing below is a seat's word alone.

## Findings, verified

### Spec 4 — the fence (8 distinct findings; the escalation fires)

- **T1 (confirmed)** — the boot-generation carrier's storage is unnamed. The
  spec says "the session is stamped at boot … minted beside `Revision` in
  `PendingAskCalls()`", but `agent.Session` holds only `askPendingRevision`
  (`agent/session.go:753`) and `PendingAskCalls()` builds the state from that
  field alone (`agent/session_tools_ask.go:101-108`): no boot source exists in
  the agent session. Two alternatives compile (a session field wired through
  the constructors, or a server-side overlay at projection), with different
  failure modes; the spec names neither.
- **T2 (confirmed)** — the empty-set branch is undefined. `processOneInput`
  clears unconditionally after the durable user turn; the resolving steer
  checks `steeringAnswersAsk` only. Neither conditions on the set being empty,
  and the spec states no contract for a fenced arrival against an
  already-cleared set (the interrupt race): refuse-as-mismatch or
  admit-as-plain-input is the implementer's choice. Two correct-looking
  implementations diverge on whether a stale answer is consumed.
- **T3 (confirmed, evidence corrected)** — the pair's named carry is the wrong
  record shape. T's story ("steers are not journaled") is FALSE:
  `AcceptClientMutationSteer` durably journals in `clientMutations`
  (`agent/session_client_mutation_queue.go:749-756`) and the steering carrier
  is that journal's claim/drain. But the spec names `clientMutationQueueEntry`
  — the queue-method entry — as the carry, and a steer's journal record is a
  different shape. Pair fields added to queue entries only leave steer records
  unfenced: the crash window T describes opens anyway.
- **R1 (confirmed)** — check-to-clear is not atomic. `clearAskPending` takes
  `s.mu` itself; live appends take the same lock; the spec's "immediately
  before `clearAskPending`" is two lock acquisitions with a preemptible gap.
  Reachable at the steer site, where the model goroutine is live mid-turn and
  can append between check and clear — the mirror image of round-6's two-call
  sampling gap.
- **R2 = S9 (confirmed, two seats independently)** — the composite identity is
  missing. The declared payload lists (frame, event contract) name only "the
  displayed first question's call id"; the fetch clause presupposes "the
  excerpt carries an ask turn id"; the journeys and red sections reference the
  ask turn id. `PendingAskCall` carries `TurnID` and `CallID` as separate
  fields (`appwire/types_pending_ask.go:13-17`); no call-id-to-turn-id resolver
  is named. An implementer building from the payload lists ships a frame that
  cannot address its own exact-turn fetch.
- **S6 (confirmed — the deepest finding of the round)** — the fence's specified
  check placement is on the wrong side of the durable append. Both clear sites
  run AFTER the answer is durable: the resolving steer sets
  `AnswerToTurnIDs`, durably appends the turn (`agent/session_queue.go:1005-1070`),
  and clears later; the queue site clears after "the claimed user turn is now
  durable" (`agent/session_lifecycle.go:2342-2352`). On restore,
  `turnResolvesAskBoundary` returns true for ANY durable `TurnUserInput`
  (`agent/session_tools_ask.go:597-608`). A mismatch checked where the spec
  puts it can refuse the clear but cannot undo the durable answer; a crash
  then restores the ask as resolved. "Never admitted as an answer" is
  unachievable from that placement.
- **S7 (confirmed, contingent on the placement)** — no late-rejection
  delivery path. Queue/steer acceptance returns an applied receipt immediately
  (`AcceptClientMutationQueue`, `:113-184`); the hub forwards it. A typed
  mismatch discovered at consumption reaches no one: the spec defines no wait,
  no correlated rejection outcome, no watch event, no re-read trigger for the
  Manager that already holds a success receipt.
- **S8 (confirmed — breaks the third revision's central mechanism)** — the
  hub-side `AskFence` gate is not on the paths it exists for.
  `hubRelay.startTurn` reads the row in `prepareRelay` and checks capabilities
  there (`cmd/evener-hub/app_relay.go:71-95`); Queue and Steer call
  `ensureSkillInputSupported`, whose first line is
  `if !containsSkillInputItem(input) { return nil }`
  (`cmd/evener-hub/appwire_validation.go:55-69`) — ordinary text input never
  reads the row. Start is refused mid-turn, so the append window is answerable
  only via queue/steer: precisely the modes the gate does not cover. An older
  daemon receives fence fields permissively on those modes — the fail-open
  case the revision claims to close.

Round-6 grounding repairs were verified correct by all three seats (T: "the
three grounding defects from round 6 are correctly repaired", "no false
citations underpin the spec text"). Round 7's findings are design-placement
breaks, not grounding errors: the check is after the durable answer, the gate
is on the wrong paths, the rejection cannot reach the caller, and the carried
identity cannot address the fetch.

### Spec 1 — first significant finding of the process

- **S1 (confirmed)** — filtered discovery's source-state schema is undefined.
  `discover` returns every configured source unconditionally
  (`cmd/evener-hub/session_reads.go:22-53`: local + all catalog hosts + all
  `sources.All()`), while the fan-out's `allowed` set excludes caller-filtered
  sources and unattached remotes (`app_threadlist.go:81-101`). The spec
  requires a state on "each `Sources` entry" and simultaneously says "the
  states describe the implicit fan-out only" — so an explicit filtered read
  carries nonselected entries with no defined state, and deriving state from
  `allowed` mislabels a caller-filtered source as `skipped-unattached`.
- T and R judged spec 1 clean; S's deeper pass found what they missed.

### Spec 2

- **S2 (confirmed)** — the `fetchCursor` byte-range continuation names a wire
  contract that does not exist. `ThreadItem` is heterogeneous (Text,
  ArgumentsJSON, Output, Error, Raw, images, metadata —
  `appwire/types_turn.go:181-267`); today's cursor carries projection and
  position only (`internal/appitempaging/cursor.go:25-31`). The spec defines
  no ranged byte domain, no offset unit, no partial-item response schema, no
  boundary or reassembly rules. "Equal to the unfiltered read's bytes" is
  untestable for multi-field items. Round 4 named the form; no seat opened
  the content until S.
- **S3 (minor, confirmed)** — acceptance omits `docs/product/history-content.md`,
  the owning guide for content selection and paging.
- **TM1 (minor, confirmed)** — the floor invariant needs one concrete
  max-envelope bound to be checkable.

### Spec 3

- **R3 (confirmed)** — `frontierTurnId` is promised on the metadata-shaped read
  with no source. The metadata hub read is `ThreadReadParams{Ref}` without
  `IncludeTurns` (`agent/session_tools_hub.go:742-746`);
  `LatestExecutionTurnID` is assigned only inside the `IncludeTurns` branch
  (`server/appwire_read_handlers.go:233-242`), and the thread projection has
  no such field. "Live metadata reads compute it from the projection" is
  unimplementable from the cited path: name the source or make the shape
  absent-when.
- **S4 (confirmed)** — the `Authoritative` migration names the redesign but
  not the outcomes. Today's client deletes held items only when
  `authoritative` (`transcriptHistory.ts:819-845,1061-1084`); the pinned
  daemonless test requires deletion of missing items
  (`reducer.history.test.ts:533-567`). Under the new contract a normal
  daemonless serve and a live-read fallback both say `servedFrom:
  saved-index`, distinguished only by `fallbackReason`. Always-retain strands;
  always-delete can erase newer held live items on a stale fallback merged
  under live identity. The replacement truth table is genuinely missing.
- **S5 (minor, confirmed)** — acceptance omits the Manager prompt
  (`agent/testdata/messagingprompt/manager-on.md`), required by the spec-set
  contract for changed Manager tool surfaces.
- **TM2 (minor, confirmed)** — the remote landing-zone parenthetical names the
  capability translation (`remote_hub_refs.go:134-203`) where the serving
  facts ride the read-response translation, a different function in the same
  file.

## Tally

13 significant (12 distinct + R2=S9) + 6 minor. All verified. No inflation:
no seat re-reported a resolved finding; S's spec-1 catch and S8's gate trace
are new classes.

## Readiness

No spec is ready. This round's verdicts overturned round-6's on specs 1 and 2 —
S's deeper pass found what the faster seats and all of round 6 missed. The
pattern across rounds 2→7 (36 → 33 → 15 → 8 → 12 → 13) is not convergence;
the fence's serial revisions (r4 single revision → r5 pair → r6 hub gate →
r7 placement break) each closed the named class and exposed the next.

## Disposition — the escalation fires

Round6.md's rule: another fence-level significant sends the fence to its own
red-test spike before re-entering the spec set. Eight distinct verified
fence findings across three seats fire it decisively. A fourth paper revision
would redesign the admission protocol from imagination again — the method
that produced three structurally broken revisions.

1. **The relay fence, ask-identity carry, and sufficiency-relay machinery
   leave spec 4** for a red-test spike (`specs/manager-tools/fence-spike.md`
   carries the design on return). The spike writes RED tests first, against
   the real seams at fresh origin/main, one per verified failure mode:
   publication sampling (Q1 excerpt + Q2 revision), append-between-check-and-
   clear (R1), mismatch-after-durable-append restoring as resolved (S6),
   fenced arrival on an empty set (T2), steer journal carry across restart
   (T3), ordinary-text queue/steer admitted on a daemon lacking the gate (S8),
   late mismatch invisible to the success-holding caller (S7), composite
   identity fetch (R2/S9), and the carrier-storage alternatives (T1). Each red
   must fail for the stated reason in harness before any design is trusted;
   the fence is then designed from the failing tests. The shape the reds point
   at: the check moves INSIDE the admission transaction — comparing the pair
   under the same lock that commits the journal/append, before the answer is
   durable, so acceptance itself is refused synchronously (the receipt IS the
   refusal, no late-rejection channel needed) — plus one common prepare
   operation that all three modes route through, reading the row
   unconditionally.
2. **Spec 4 keeps its non-fence content** (attention derivation, discovery
   enrichment, excerpts/flags, the needs_you shape) with the fence scopes
   replaced by the spike pointer.
3. **Specs 1, 2, 3 take one revision pass**: S1 (explicit-filtered-read
   population and states), S2+S3+TM1 (byte-range wire contract + owning doc +
   envelope bound), R3+S4+S5+TM2 (frontier source + replacement truth table +
   prompt + landing-zone correction).
4. **Round 8** reviews the revised specs 1–3 plus the spiked fence design
   once the spike lands; implementer lanes follow a clean round. No issue
   filing (Jesse's standing hold).
