# Round-6 PAR reconciliation record

Inputs reviewed at commit 768e71c4e9 (base origin/main 2be411cd8f):
- Reviewer O, codex-jesse-fsck.com/gpt-6.1-sol: 4 significant.
- Reviewer P2, codex-jesse-fsck.com/gpt-6-sol: 4 significant.
  (The original third seat, kimi/k3-256k, died at 10:19 on its
  provider's hard 262,144-token request limit — the spec set plus
  in-tree verification has outgrown 256k-window models; the 1M kimi
  seats of rounds 4-5 handled it. P2 was the replacement; the round
  therefore ran two codex-family models, a family overlap recorded
  here. Every alternative was worse: kimi's remaining windows are
  256k, plain glm-5.3 is the author's own family, shisa seats carry
  no tools, stepfun's subscription is dead, and the other providers
  error per catalog diagnostics.)
- Reviewer Q, lunaroute/deepseek-4.1-flash: 4 significant + 4 minor
  (volume winner; verified every citation against the base commit
  with git show/grep rather than the worktree tip).

Twelve significant findings, with the heaviest convergence of any
round: the fence broke on three independent axes, each found by two
or all three seats. The findings have left the rest of the specs —
spec 1 came back CLEAN from all three seats, the first spec to reach
the bar — and concentrated exactly where round 5's plan predicted a
spike: the fence.

## Fence-spike condition (round5.md plan): met, disposition recorded

Round 5's plan said another fence-level significant finding earns the
fence a red-test spike before further revision. The condition fired.
Disposition: the findings were verified to the same certainty a spike
provides — the permissive decoder (`internal/appserver/router.go:44-49`
`HandleTyped` → plain `json.Unmarshal`), the two-call sampling
(`server/thread_envelope.go:420-426`, each accessor taking the session
lock independently), and the missing row field (`appwire/types_thread.go:63-85`,
no `BootGeneration`) were each read directly, by three independent
reviewer passes and by me. A dynamic spike would have tested Go's
stdlib `json.Unmarshal` ignoring unknown fields. The demanded dynamic
proofs — the append-between-samples regression and the
old-daemon-admits-fence-fields test — are now REQUIRED red-first
journeys in the revision below; they are the spike, and the implementer
lane runs them.

## New code facts verified this round (in-tree, this session)

1. The daemon's direct turn/start (and queue/steer) register through
   `appserver.HandleTyped` (`server/appwire_read_handlers.go:30`),
   which decodes with plain `json.Unmarshal`
   (`internal/appserver/router.go:44-49`): unknown fields are silently
   IGNORED. `TurnStartParams`/`TurnQueueParams`/`TurnSteerParams` have
   no strict decoder. The `DisallowUnknownFields` decoder I cited in
   round 5 (`appwire/session_tools.go:142-150`) is the HUB-side
   Manager-operation decoder — the wrong boundary: the new hub
   understands the fence fields, so its strictness never protects the
   older-daemon hop. (O1 = P2-1; verified by me.)
2. `EvenerThread` carries NO boot generation (`appwire/types_thread.go:63-85`:
   `PendingAsk`, `InstanceID`, `Ref`, ...). The field I cited
   (`appwire/types_turn.go:382-386`) is `ThreadReadResponse.BootGeneration`
   — a response field, not a row field. The probe is a LIST read
   (`appClient.ThreadList`, `cmd/evener-hub/internal/hubcore/prober.go:71`);
   `ThreadListResponse` is rows and cursors only
   (`appwire/types_turn.go:316-320`); nothing in the projection stamps
   a boot generation. My round-5 grounding sentence "the same row
   stamps `EvenerThread.BootGeneration`" was MY ERROR, made by reading
   a nearby struct instead of the cited one. (O2 = Q1; verified by me.)
   The revision half IS on the row (`PendingAskState.Revision`
   travels via `EvenerThread.PendingAsk`); the boot half has no
   source on the probe path at all, for local and remote lanes alike.
3. The excerpt and the pending-set identity are sampled in TWO
   separately-locked calls: `next.PendingQuestion = src.PendingQuestion()`
   then `next.PendingAsk = ClonePendingAsk(pending.PendingAskCalls())`
   (`server/thread_envelope.go:420-426`); each accessor locks the
   session independently (`agent/session_tools_ask.go:101-126`); an
   append between them mispairs the excerpt with the revision, and
   publication installs the question unconditionally while guarding
   only the pending state's revision advance (`thread_envelope.go:568-573`).
   A row can carry Q1's count-one excerpt with Q2's revision.
   (O3 = P2-2; verified by me.)
4. `start` is refused while a turn is active — `Conflict("turn is
   already active")` (`agent/session_client_mutation.go:425-429`) — so
   the append window (Q1 posted mid-turn, Q2 appended in the same
   turn) can only be answered through `queue` or `steer`. BOTH clear
   the whole pending set: queued input consumed via `processOneInput`
   clears at `agent/session_lifecycle.go:2342-2355`; user-provenance
   steering clears via `clearAskPendingForResolvingSteer`
   (`agent/session_tools_ask.go:231-235`, via `steeringAnswersAsk:280`,
   which the spec itself cites). The round-5 fence covered only
   `start`. (Q2; the start-refusal confirmed from code I read earlier.)
5. The agent session holds no boot generation: it lives on the server
   (`s.appBootGeneration`, `server/appwire_identity.go:164`), minted
   per start (`cmd/evener/serve.go:780`, replacement `:1124`). A
   queued intent (`clientMutationQueueEntry`, `agent/session_client_mutation_queue.go:164-170`)
   carries no precondition fields. Both landing zones were unnamed.
   (Q3.)
6. The relay's prepare step ALREADY reads the thread row's capabilities:
   `app_relay.go`'s `prepareRelay` result feeds
   `ValidateSkillInputSupport(params.Input, prepared.Thread.Evener.Capabilities.SkillInput)`
   (and the command-input check) — the existing fail-closed gate shape
   the fence needs. (Verified while fixing F1.)
7. `invokeHubInput` rejects arguments outside its closed set
   (`agent/session_tools_hub.go:558-565`), and `managerSendDefinition`
   declares the same closed properties with `additionalProperties:
   false` (:443-468) — the pre-dispatch schema gate any new relay
   argument must extend. (Q8.)
8. A params decode failure surfaces as one generic `InvalidParams`
   wrapping the raw json error (`internal/appserver/router.go:44-53`;
   `ThreadReadParams.UnmarshalJSON` returns the raw error,
   `appwire/types_turn.go:518-531`): no machine-readable
   "unknown-field" identity exists to map or retry on. (Q7.)
9. `setAskPendingLocked` receives parsed questions only — the live
   append's full call JSON is "never reconstructible" (its own comment,
   `session_tools_ask.go:130-137`) — so the `complete`/`truncated`
   flags cannot be computed after restore; they are live-append facts.
   (Q5.)
10. A `ThreadTurnsListResponse` page carries NO instance identity at
    all — the metadata read's result is used for display fields and
    image enrichment only, never serialized as identity
    (`cmd/evener-hub/app_rpc.go:441-465`; `HistoryIdentity.StampPage`
    stamps BootGeneration/Epoch/Snapshot/Authoritative,
    `internal/appsource/source.go:86-92`). My round-5 framing ("a
    Clear stamps an instance that did not serve the page") was wrong:
    the defect is ABSENCE. The owned wire work stands. (Q6; corrects
    my round-5 K2#16 resolution's framing.)

## Lessons recorded

Two of the three fence breaks trace to my grounding method, not to
the reviewers' cleverness: I verified "the daemon already serves X"
claims by reading NEARBY code (a response struct instead of the row
struct; the hub's decoder instead of the daemon's). The discipline
fix, applied from this round forward: every "already exists" claim is
verified at the EXACT hop the spec names, with the struct definition
opened, before it enters a spec — the same bar the reviewers held.

## Decisions

### The fence, third revision (spec 4)

- **F1 — the older-daemon gate moves hub-side, fail-closed on a
  capability bit.** A new `ThreadCapabilities` bit (`AskFence`)
  advertises fence support. The hub refuses a fence-bearing relay
  BEFORE dispatch when the serving daemon lacks the bit — checked in
  the relay's own prepare step, which already reads the row's
  capabilities for skill/command input validation (fact 6) — with the
  typed `ask-fence-unsupported-on-daemon` error and the fallback
  guidance. No fence field is ever dispatched to a daemon that lacks
  the bit (refused, never stripped, never auto-retried).
  `RemoteHubSource`'s capability rebuild forwards the bit (the
  landing zone spec 2 already names). Removal condition: every
  supported daemon advertises the bit. Decode-error string-matching is
  banned by fact 8 (Q7): the bit is the only skew signal.
- **F2 — the boot generation becomes real owned work, carried INSIDE
  the pending state.** The session is stamped with its boot
  generation at boot (`serve.go` already computes it — fact 5); 
  `PendingAskState` gains `BootGeneration`, minted beside `Revision`
  in `PendingAskCalls()`. The pair travels atomically with the state
  the row already clones. The false "the daemon already serves it"
  grounding is corrected: today the row carries the revision only;
  the boot half is named owned work, absent on older daemons. For
  remote lanes, until the remote daemon serves the pair, the frame's
  pair is absent (flags-only degradation, the existing rule) and a
  fenced relay is refused by F1's bit check.
- **F3 — one atomic snapshot.** A single locked session accessor
  returns the excerpt text, the count, the identity pair and the
  flags together (landing zone: `agent/session_tools_ask.go`, a new
  snapshot accessor; the envelope's facetAsk calls it ONCE,
  `thread_envelope.go:420-426`). The append-between-samples mispair
  (fact 3) is the required red: a deterministic regression (sample,
  append, sample) asserting the second snapshot is internally
  coherent, red on today's two-call path.
- **F4 — omission equals legacy, honestly.** The "older callers are
  refused into re-reading" clause is dropped: the daemon cannot
  distinguish omission from informed opt-out, and the compatibility
  rule preserves existing call shapes. Omission is today's unfenced
  behavior — the legacy path and the documented fallback share the
  wire shape BY DESIGN, stated. The spec's own direct-relay rule
  REQUIRES the pair; the unfenced fallback's guidance is a fresh
  read immediately before the relay. The legacy/informed distinction
  lives in the guides, not the wire.
- **F5 — the fence covers all three modes.** The pair rides
  `TurnQueueParams` and `TurnSteerParams` as well as
  `TurnStartParams`; the queued intent carries it
  (`clientMutationQueueEntry` gains the fields — fact 5); the check
  runs at BOTH clear sites — `processOneInput`'s admission
  (`session_lifecycle.go:2342-2355`) and
  `clearAskPendingForResolvingSteer` (`session_tools_ask.go:231-235`)
  — comparing immediately before `clearAskPending`. Journeys add a
  delayed QUEED answer and a delayed STEERED answer racing an append,
  both refused on mismatch; mid-turn answering is the fence's
  primary scenario (fact 4) and is now covered.
- **F6 — the tool schema is a named landing zone.** The relay's new
  arguments extend `managerSendDefinition`'s closed property set and
  `invokeHubInput`'s argument allowlist (fact 7); the journey's red
  asserts the pre-dispatch refusal today, beside the hub gate.
- **F7 — the flags are live-append facts.** `complete`/`truncated`
  are computed on the live append only and are ABSENT after restore
  (fact 9); contract 4's list is conditional, and the restore journey
  asserts the absence (fail-to-absent, the existing rule).

### Spec 2

- **F8 — aggregate elision recovery is defined.** The aggregate
  record splits its populations: per-type excluded counts (e.g.
  `commandExecution: 3`) beside the cap-withheld remainder, whose
  continuation cursor stays INSIDE the same filtered view. Type-
  excluded items are reconstructable by a NAMED RE-READ — the same
  turn via a wider type list, a fresh view by the view-identity rule
  (deliberate, stated) — never by same-filter continuation (which
  re-excludes them) and never by cursor reuse across views (which
  the selection fence forbids). The recovery guarantee is restated:
  every withheld item is reconstructable — by continuation within the
  same view, or by the named re-read; the record always names which.
  The journey covers interleaved included/excluded items through
  exhaustion plus the wider-list re-read retrieving the excluded
  ones. (O4 = P2-4.)
- **F9 — the skew retry is bit-gated, not decode-matched.** Spec 2's
  hub-compat retry (round 4's J1) keys on the capability bit, the
  same signal F1 uses; a generic `InvalidParams` is a parameter
  error, never a skew signal (fact 8). (Q7, shared with spec 4.)

### Spec 3

- **F10 — a remote serve states whose serve it was.** A reachable
  remote ref served through `RemoteHubSource` gets its provenance
  from the REMOTE hub's serving branch, never derived from the local
  branch (which reads "live source" for a response the remote hub
  filled from its saved index — the false-provenance class contract 2
  forbids). The remote source's row/response rebuild forwards the
  remote hub's own serving facts (a named landing zone beside the
  capability rebuild); where the remote hub carries none, the facts
  are absent-when with the reason, stated. (Q4.)
- **F11 — the page-shape defect is absence.** The grounding says
  pages carry NO instance identity at all (fact 10); the metadata
  read supplies display fields only. The owned wire work (stamp from
  the serving cut) stands; the framing is corrected. (Q6.)

### README

- Rule 6's ownership list and the review trail gain round 6; the
  fence bullet moves to "capability-gated, all three modes, atomic
  snapshot" phrasing once the spec lands.

## Per-spec readiness across all three seats

- Spec 1: clean from O, P2 and Q — the first spec at the bar. The
  lane may launch once the set is ready, or wait for the set; the
  README's order rule governs.
- Spec 2: one converged significant (aggregate recovery) + one minor
  (bit-gated retry). Fix in this pass.
- Spec 3: one significant (remote serves) + one minor (framing).
  Fix in this pass.
- Spec 4: the fence, third revision. Fix in this pass.

## Round 7 plan

One revision pass lands F1-F11, then round 7 with three never-seated
models. The fence gets one more full-depth attack; if round 7
produces another fence-level significant, the fence leaves the spec
set and becomes its own design spike with a red-test harness before
re-entering. Zero significant across all three seats gates the four
implementer lanes.
