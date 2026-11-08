# Round-3 PAR reconciliation record

Inputs reviewed at commit 8e836f5d26 (base origin/main 2be411cd8f):
- Reviewer F, lunaroute/muse-spark-open: 3 significant + 1 minor.
- Reviewer G, codex-jesse-fsck.com/gpt-6-astra: 11 significant + 2 minor.
- Reviewer H, kimi/k3.1-pro-highspeed: 19 significant. Most legitimate
  significant findings; round winner.

No inflation detected; H's 19 include seven findings that converge with
F/G from independent models, which strengthens them. Every load-bearing
new code claim below was re-verified in-tree before acceptance. One
reliability note for the record: round-2's winning reviewer (C) cited
several paths that do not exist (`cmd/evener-hub/server/...`,
`cmd/evener-hub/internal/hubapi/...`) while being right about the code
facts; the real paths are flat under `cmd/evener-hub/` and
root-level `hubapi/`. G13's audit stands and the citation fixes below
apply to the specs and to `reviews/round2.md` alike.

## New code facts verified this round (in-tree, this session)

1. `fallbackHistoryIdentity` (`cmd/evener-hub/app_rpc.go:355-369`, used
   by the succeeded-empty swap at `session_tools.go:594-600`) merges
   saved-index items under the LIVE daemon's BootGeneration whenever the
   live read named one: "the response merges under the daemon's
   generation and epoch rather than replacing as a daemonless read
   would." The failed-live seam serves daemonless-stamped (spike), the
   succeeded-empty seam serves LIVE-stamped saved content. A
   `servedFrom` derived from the response's BootGeneration is therefore
   false on exactly one of the two fallback seams. Only the serving
   branch knows the truth.
2. `Authoritative`'s documented meaning is "true when this response can
   settle uncertain state (a live daemon read), false for a
   saved-transcript read that cannot" (`appwire/types_turn.go:395-398`);
   saved serves stamp true (spike-observed). Clients key held-item
   replacement on the flag (`appwire-client/typescript/transcriptHistory.ts:830-833,1081-1084`,
   daemonless behavior pinned in `reducer.history.test.ts:533-567`).
   A bare flip changes client behavior; the fix is a designed saved-view
   replacement contract keyed on `servedFrom`, migrated deliberately.
3. `SessionOperationRequest.Validate` rejects Manager history reads
   carrying any nonzero `ThreadContentParams` ("content filters are
   unsupported", `appwire/session_tools.go:126-127`) — a second gate
   behind the agent schema gate.
4. `RemoteHubSource` rebuilds capabilities field by field
   (`cmd/evener-hub/internal/appsource/remote_hub_refs.go:134-139,185-203`):
   any new capability bit is false on remote reads until the rebuild
   names it.
5. `LatestExecutionTurnID` populates only under `IncludeTurns`
   (`server/appwire_read_handlers.go:233-242`); the Manager `job_status`
   hub branch issues `ThreadReadParams{Ref}` with no IncludeTurns
   (`agent/session_tools_hub.go:742-746`); `Before` windows compute no
   anchor (`server/history_read.go:75-100`,
   `internal/transcriptindex/window.go:232-251`). Also
   `agent/session_execution.go:54-76`: a new execution has
   `ActiveTurnID` before its first persisted entry, so a row's
   running-turn id can legitimately name a turn the persisted view does
   not show yet.
6. `identityReasoning` serves each thinking part's `Text`, falling back
   to `Summary` only when text is empty
   (`internal/apptranscript/apptranscript.go:1011-1040`); client
   summaries derive from that projected text. There is no servable
   summary wire form.
7. Follow-up suggestions emit only `{ref, cursor, itemLimit}`
   (`cmd/evener-hub/session_tools.go:253-260`); `SessionStatusTarget`
   (`appwire/session_tools.go:393-406`) has no window/filter/bound
   fields. Following one today silently returns to an unfiltered read.
8. The typed clients validate wire shapes strictly: the generated item
   allowlist has no fetch/elision fields
   (`appwire-client/typescript/types.gen.ts:5752-5757`),
   `validWireItem` rejects unknown keys
   (`transcriptValidators.ts:295-300`), `ThreadTurnsListResponse`
   allows only six fields, and deferred-page validation rejects others
   (:652-670). Every new wire field obligates regenerated types and
   validator updates.
9. The crash vocabulary is `Status: "errored"` plus `LiveEntry.Crashed`
   (`cmd/evener-hub/internal/hubcore/roster.go:907-916`); no "crashed"
   status exists. `localDaemonThreadStatus` maps unknown values to idle
   (`cmd/evener-hub/internal/appsource/local_daemon.go:1659-1679`); removing the
   discovery adapter's crashed skip without a routable discriminator
   would make crashed entries live-RPC-routable.
10. `ask_user` APPENDS to the pending set
    (`agent/session_tools_ask.go:151-164`); `PendingQuestion` is
    first-question-plus-count (:114-126); autonomous wakes are refused
    while pending (`agent/session_lifecycle.go:674-676`), so a later
    turn cannot replace a pending question. The real second-question
    case is an append (count 1→2), not a replacement with unchanged
    flags. The ask machinery's own comment says the bounded excerpt is
    insufficient for an external responder (:447-450).
11. The roster's refresh loop and the attention broker's publish loop
    run independently (`cmd/evener-hub/internal/hubcore/roster.go:1180-1203`;
    `main_background.go:59-75`); discovery joining the broker's cached
    map can pair a fresh row with attention facts up to a cadence stale.
12. The Manager roster is archive-blind and remote-inclusive
    (`cmd/evener-hub/main_background.go:63-72`: "Archiving clears human
    attention, not the Manager's live population"; pinned by
    `attention_roster_archive_test.go:14-90`). G2/H18's population
    finding is confirmed.
13. The wire's content selection is coarser than item types:
    `Selection.Item` groups `agentMessage`+`userMessage`+user-steering
    into conversation and everything unlisted into system
    (`appwire/history_content.go:65-97`); `Items` drops excluded items
    outright (:100-110). `input_image` is nested content, not a
    projected item type.
14. The probe fills `PendingAsk` as a bool and `PendingQuestion` as
    bounded text only (`cmd/evener-hub/internal/hubcore/prober.go:175,179`) while the thread
    row carries the full `PendingAskState` with per-call `TurnID`
    (`appwire/types_pending_ask.go:6-17`, cloned at
    `server/appwire_thread_projection.go:117`). The thread row also
    carries its instance identity at the serving cut (`:119`).
15. `classifyMissedProbe` retains the previous record on a failed probe
    of a still-live daemon (`cmd/evener-hub/internal/hubcore/roster.go:851-868`), so a
    successfully-answering discovery can serve per-session facts that
    are one-or-more probes stale with nothing marking them.
16. Path audit (mechanical, all five files): `cmd/evener-hub/server/*`
   does not exist (files are flat under `cmd/evener-hub/`); the short
   `cmd/evener-hub/internal/hubcore/...` and `cmd/evener-hub/internal/appsource/...` forms need their full
   `cmd/evener-hub/internal/...` paths; `hubapi/attention.go` is
   root-level (`NeedsResponse` at :75-78).

## Spec 1 (fleet snapshot) decisions

- F1/G1/H1 ACCEPTED: `needsResponse` is REMOVED from spec 1 (table,
  contract 2, journeys 1 and 5, dependencies). Spec 4 owns the attention
  row facts and deliberately adds only `attentionLevel`; ask-pending
  keeps the row's existing `Evener.AskPending` as its one fact (no new
  `askPending` field anywhere — G1's duplicate demand resolved by
  declaration).
- H13 ACCEPTED: the crash scope rewrites to the existing vocabulary.
  Crashed roster entries (`LiveEntry.Crashed`, status `errored`)
  become discovery rows carrying the existing status, the retained
  `Failure`, and a projected `crashed` discriminator; the discriminator
  excludes the row from every live RPC route (a named routing check),
  and the status projection must not fall through
  `localDaemonThreadStatus`'s unknown-to-idle mapping. Journeys assert
  the discriminator and non-routability. Defining a new status value
  everywhere is rejected; the existing vocabulary is the contract.
- G3/H15-fact ACCEPTED: probe-freshness. The volatile facts
  (`currentTurnId`, `turnStartedAt`, `quietForMs`) are present only
  when the row's record was refreshed by a successful probe in this
  read's cycle; a retained record (the `classifyMissedProbe` class)
  serves the durable facts with the volatile ones absent and a
  `probeState: fresh | retained` marker (the roster's own
  classification, one owner). New journey: live daemon, injected probe
  failure, volatile facts absent, durable facts present, marker
  `retained`.
- H5 ACCEPTED (in-flight window): the agreement clause states that the
  row's `currentTurnId` names the RUNNING turn, which may be ahead of
  the persisted view; the read's frontier names the newest PERSISTED
  execution turn; agreement is asserted when the running turn has
  persisted entries, and the labeled divergence inside that window is
  honest on both sides.
- G2/H18 ACCEPTED: population. Attention fields on rows are absent only
  when spec 4 has not landed, the row is a subagent's, or no roster
  entry exists (disk-index rows). Archived and remote lanes KEEP
  attention facts — the Manager roster is archive-blind and
  remote-inclusive (fact 12). Contract 1's eligibility wording follows
  the roster's own population.
- G12/H5 ACCEPTED: the witness claim narrows precisely: the
  owned-delegate `job_status` result has no turn id, and the hub-branch
  `job_status` is a `ThreadReadParams{Ref}` without `IncludeTurns`, so
  it carries no frontier either. Turn identity is witnessed by a
  transcript read and the roster facts only.
- F4 ACCEPTED: the response-level stamp is renamed `builtAt` (owned by
  spec 1; never compared with spec 3's per-serve `asOf` — different
  clocks, and the docs say so).
- Path citations corrected throughout (fact 16).

## Spec 2 (cheap filtered reads) decisions

- G4/H8 ACCEPTED: `itemTypes` is honest about the wire. The existing
  categories are coarse (fact 13) and `Items` drops what it excludes,
  so the windowed handler must NOT filter through `Selection.Item`.
  Owned new machinery: an additive exact-type allow-list predicate
  layered onto the selection, applied by the windowed handler AFTER
  computing elision metadata from the full turn item list; the type
  list enumerates real projected types only (no `input_image` as a
  standalone type; it is nested message content, bounded by the
  message bounds); the `messages` alias selects `agentMessage` +
  `userMessage` through the new predicate, not the conversation flag.
- H9 ACCEPTED by removal: `summarizeReasoning` is removed. There is no
  servable summary wire form (fact 6); callers exclude reasoning or
  bound it by bytes, and the projection's summary fallback stays
  internal.
- H6 ACCEPTED: the hub's operation-validation gate (fact 3) is a named
  landing zone — the windowed Manager read's validation branch changes
  to accept the selection and window params, and the documented
  contract line in `docs/automation.md` changes in the same edit. The
  journey drives the full agent-schema → operation-validation path.
- H7 ACCEPTED: remote capability forwarding is a named landing zone
  (fact 4): `RemoteHubSource`'s rebuild forwards the new bit, with the
  raw-status and masking policy updated and exhaustively tested;
  remote reads then behave like local bit-gated reads. The alternative
  (define remote as always-hub-compat) is rejected: it would hide
  capable remote daemons behind a permanent compat path.
- H11 ACCEPTED: follow-up suggestions preserve the window, filter and
  bound parameters (`SessionStatusTarget` and the emission site are
  landing zones, fact 7). Journey: a continued messages-only read stays
  messages-only, asserted.
- H12 ACCEPTED: the wire-schema obligation is named in dependencies and
  the non-goal is reworded — no client BEHAVIOR change, but every new
  wire field lands with regenerated AppWire types and updated
  appwire-client validators (fact 8), which the repo's generated-output
  gates enforce.
- G5 ACCEPTED, narrowed: hub-compat serves `{latest: N}` windows only,
  from a single legacy page (the legacy read is one 40-item window;
  fact 5's cousin); a window the page cannot cover is labeled partial
  with a continuation; `since`, `before` and `turn` against a daemon
  without the bit are a typed `window-unsupported-on-daemon` error
  naming the capability fix. No multi-page traversal promises.
- G6/H10 ACCEPTED: budget floor and bounded metadata. Effective budget
  = clamp(caller value, floor, cap-margin) with the floor (16KiB)
  covering the envelope, one elision record and a continuation;
  withheld-turn metadata is bounded (served count, withheld count,
  boundary ids, continuation — not an exhaustive per-turn list; naming
  is capped and then aggregates). The response states the effective
  budget it enforced.
- G7 ACCEPTED: the `fetchCursor` fence gains the item's version (items
  mutate in place: `internal/transcriptindex/build.go:974-997`,
  `internal/apptranscript/logical_turn.go:243-305`); a fetch against a
  changed version fails typed (`item-changed`, naming the new version)
  instead of serving different bytes silently; byte-range continuations
  carry the version. Journey: a continuation across item completion.
- G10/H17 ACCEPTED: `turnWindow` gains the exact form `{turn: turnId}`
  (inclusive, single turn), owned here — it serves the locate-a-turn
  evidence case and spec 4's ask-turn fetch.
- Path citations corrected.

## Spec 3 (transcript-status consistency) decisions

- H2 ACCEPTED, design-shaping: `servedFrom` derives from the SERVING
  BRANCH the hub took (live read served vs past page swapped in), never
  from the response's BootGeneration (fact 1: the succeeded-empty seam
  merges saved items under the live generation). The spec also names
  the identity-merge itself as part of the defect surface: the response
  identity fields describe the client-merge machinery (which tracks the
  still-streaming daemon), while `servedFrom` describes content
  provenance. Both facts, one response, both true. The journey asserts
  `servedFrom: saved-index` + `fallbackReason: live-read-empty` on a
  response whose BootGeneration remains live-stamped — today's red.
- H3 ACCEPTED, redesigned: the `Authoritative` resolution is a
  designed migration, not a bare flip (fact 2). Order: `servedFrom`
  lands as the serving-point signal; the client's saved-view
  replacement contract is redesigned around it and the pinned reducer
  test is updated deliberately; doc-conformant stamping (false on saved
  serves) lands WITH the client change in the same release; the skew
  direction is named (an old client against a new hub sees
  false-on-saved and conservatively retains held items — the implementer
  verifies with the reducer test that this is the safe direction
  before landing).
- H4 ACCEPTED: saved-serve `asOf` sources from serve-time catch-up:
  `withPastTranscriptIndex` catches up before serving on every
  Manager-path point, so the moment this serve's catch-up completed IS
  the moment through which the view is file-complete — the hub stamps
  it at serve time (its own clock, no index-format change, no stored
  state). Never absent on a served view.
- H5 ACCEPTED: frontier availability is owned machinery, honestly
  scoped: the anchor computes per served view — `thread/read` populates
  it under `IncludeTurns` today; the owned work adds it to
  `turns/list`, cursor pages and the metadata-shaped read (the
  `job_status` hub read) from the same view the items came from (the
  index stores the anchor per snapshot for saved views; live
  metadata reads compute it from the projection). The spec's
  running-vs-persisted note cross-references spec 1's in-flight rule.
- G8 ACCEPTED: `instanceId` sources from the served thread row's own
  `InstanceID` (fact 14 — captured at the serving cut), not the
  independently-sampled roster identity; the Clear-before-roster-refresh
  race then resolves naturally, and the replaced-instance journey gains
  the pre-refresh case.
- G12/H5: witness wording as spec 1.
- Path citations corrected.

## Spec 4 (question surfacing) decisions

- F2/H14 ACCEPTED: the ask-turn identity carrying path is named as
  owned machinery end to end: the thread row already carries
  `PendingAskState` (per-call `TurnID`, fact 14); the probe gains the
  pending-call identity (ProbeResult → LiveEntry → the roster-to-row
  projection), selecting the call that owns the displayed first
  question; `AttentionEntry` and the frame carry it; the bounded
  excerpt carries it additively. Multiple pending calls: the frame
  names the first question's call id and the count. The text-hash
  fallback is DROPPED — identity is exact; older daemons degrade
  flags-only per the existing rule.
- H15 ACCEPTED: the Q1-replaced-by-Q2 journey is replaced by the real
  cases (fact 10): (a) a second `ask_user` in the same turn APPENDS
  (count 1→2, flags and level unchanged) and the identity trigger
  delivers because the pending-call set changed; (b) a restart
  re-derives a stable pending set with unchanged flags and the trigger
  fires on re-derivation, not on the set. Contract 3 states content
  delivery as "the pending-call set changed".
- H16/G9/F3 ACCEPTED, redesigned: the excerpt is a notice, not an
  answer key. The bounded excerpt gains `complete` (true iff the full
  ask's answer-relevant content — question, option labels and details,
  recommended, multi_select, why, if_unanswered — fits the bounded
  form, computed at ask-build where the details exist) and `truncated`
  (computed daemon-side, where the original length is known, carried
  additively). The direct-answer fast path requires complete AND
  untruncated AND a single pending call — which the ask machinery's own
  comment expects to be rare; every other relay REQUIRES fetching the
  full ask turn first. The spec now agrees with the code's stated
  judgment (fact 10, :447-450).
- H17/G10 ACCEPTED: the fetch is defined in both landing orders: with
  spec 2, the exact `{turn: askTurnId}` window; without spec 2, read
  the lane's newest turns through the existing shape and locate the ask
  turn by its id (the ask turn is recent). No hard dependency; the
  journeys assert both paths.
- G11 ACCEPTED: the frame carries the session's instance identity
  (`rosterIdentity` retains the ref across Clear,
  `main_background.go:97-107`), and the relay contract requires it on
  `delegate_send` (`expected_instance_id`). Journeys: relaying for a
  newly discovered lane, and the Clear race.
- G2/H18 ACCEPTED: rows follow the Manager roster's population (fact
  12): attention facts are absent only when spec 4 has not landed, the
  row is a subagent's, or no roster entry exists. The journey fixture
  flips: an archived live question-pending session RETAINS its
  attention facts (the pinned roster behavior flowing to rows), and
  remote rows carry them when the roster holds the entry.
- H19 ACCEPTED: the enrichment's attention facts carry their
  derivation moment (`attentionAsOf`, the broker's publish moment), and
  the contract states the tolerance: attention may trail the row's own
  facts by up to the broker's refresh cadence; the disagreement rule is
  that the row's own `AskPending` is the fresher fact for ask-detection,
  and the mandatory fetch before answering re-reads current state
  anyway. (Deriving from the same snapshot as the rows is named as the
  implementer's option if it proves cheaper.)
- F4/G13: path citations corrected; `builtAt` cross-referenced.

## README updates

- Owner map: spec 4 owns `attentionLevel` on rows (the only new
  attention row field) plus the frame's question facts; spec 1 owns the
  running-turn facts, crashed-row inclusion, per-source states and
  `builtAt`; spec 2 owns the capability bit, windows, the exact-type
  predicate, fetch cursors and the exact-turn form; spec 3 owns the
  serving facts with branch-derived provenance.
- Grounding summary gains: the archive-blind remote-inclusive roster
  population; the coarse wire categories; probe-state freshness; append
  (not replace) as the second-question case; the client-validator
  obligation on every new wire field; the two-loop attention skew and
  its tolerance.
- Cross-cutting rule 5 gains the wire-schema obligation sentence, and
  the review trail gains round 3.
- Path citations corrected.

## Round 4 plan

Three fresh competing reviewers (no model repeats a round-3 seat),
same brief at the new revision commit. A clean round across all three
gates the four implementer lanes. Candidates at spawn time from the
model catalog: gpt-5.6-terra (codex-jesse-fsck.com), plus two more
supportsTools models chosen from `model_list` that have not sat a
round-2 or round-3 seat.
