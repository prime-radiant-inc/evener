# Spec 4: Pending-question visibility for Managers

A lane holding a pending question is fully visible to a Manager through
pull reads, the roster event carries what is actually asked and delivers
when the pending set CHANGES, and the relay path that answers it is a
documented contract rather than a tool description footnote.

## Problem

Tonight, a top-level lane posted `ask_user` and ended its turn waiting for
the human. Nothing surfaced that question to the Manager for roughly 30
minutes, until a sweep happened to read the lane's transcript. Grounding
corrected the naive reading of that incident in an important way: the
pending-question fact and the question's own text already reach the
Manager's pull surfaces at base — the discovery row and the `job_status`
thread read carry `AskPending` and the bounded `PendingQuestion` (grounded
below). What the night actually exposed, and what this spec fixes:

1. **The attention level is absent from pull.** The Manager's attention
   derivation exists beside the rows (`AttentionEntry`), and the Manager
   roster consumes it, but the discovery row carries no attention level,
   so a pull-based Manager — the only kind that survives a night of
   untrusted push — cannot see the one-bit summary of "this row needs
   you" that the roster already computes.
2. **The event says that something is asked, not what — and not when it
   changes.** The roster `needs_you` frame carries flags only
   (`agent/job_watch_hub.go:213-224`): `{ref, title, level, kind,
   askPending, approvalPending, needsResponse}`. The question's own
   bounded text sits on `LiveEntry.PendingQuestion` and never reaches
   the frame. Worse: the roster's change detection keys on attention
   level, `AskPending`, `ApprovalPending` and `NeedsResponse`
   (`cmd/evener-hub/attention_subscriptions.go:123-130`), none of which
   change when the pending set grows — a second `ask_user` in the same
   turn appends (grounding below), the flags stay put, and a Manager
   holding a working watch is never told.
3. **Nothing teaches the Manager what already exists.** Neither
   `docs/automation.md`'s discovery contract nor the Manager prompt
   template mentions the row's ask fields. The author of this spec ran a
   fleet all night and did not know the fields were there — which is its
   own evidence: an untaught surface is an unusable surface.
4. **The relay is mechanics, not contract.** The answer path exists and
   works (grounded below), including the warning "A notification does
   not answer a pending question" in the tool text, but no product guide
   states the guarantee: what counts as an answer, what clearing is owed,
   how much of a question a frame carries before the Manager must go
   read the ask turn, and what identity the relay requires — and the ask
   machinery's own comment says the bounded excerpt is insufficient for
   an external responder (`agent/session_tools_ask.go:447-450`), which
   the contract must agree with rather than paper over.

## Current behavior (grounded at origin/main 2be411cd8f)

- **The row already carries the question.** `threadFromEntry`
  (`cmd/evener-hub/internal/appsource/local_daemon.go:1488-1504`) sets
  `Evener.AskPending` and `Evener.PendingQuestion` (the question's own
  text, options and count, bounded to `MaxQuestionTextRunes = 200`,
  `appwire/excerpt.go`) on every local discovery row, with the design
  intent documented in the type's own doc (`local_daemon.go:84-89`):
  a controller hub listing this hub's sessions names the question as a
  local probe would. Pinned by
  `cmd/evener-hub/internal/appsource/local_daemon_question_test.go:14-28`
  and `server/thread_envelope_question_test.go:16-26`; through the
  Manager path at `cmd/evener-hub/e2e_manager_session_test.go:150`.
  Rows pass the hub merge unmodified.
- **`NeedsResponse` has a tested meaning this spec set preserves.**
  `NeedsResponse(state, askPending)` is `awaiting && !askPending`
  (`hubapi/attention.go:75-78`), pinned by
  `cmd/evener-hub/internal/hubcore/attention_test.go` and
  `hubapi`'s own tests: a pending question makes it FALSE (the human is
  answering, not the Manager); `appwire/attention.go:25-29` documents
  the distinction. A question-pending row reads `attentionLevel:
  needs_you` with `needsResponse: false` — and that is correct, not a
  defect.
- **Pending asks APPEND; a pending question outlives the turn.**
  `agent/session_tools_ask.go`: `setAskPendingLocked`/
  `appendAskPendingLocked` (:143-165) — a second `ask_user` in the same
  turn appends a call to the pending set, it does not replace;
  `PendingAskCalls` returns `PendingAskState{Version, InstanceID,
  Revision, Calls[{TurnID, CallID}]}` (:99-116) — the exact per-call
  identity; `PendingQuestion` is first-question-plus-count (:114-126);
  `clearAskPending` on user input (:187); `steeringAnswersAsk` (:280)
  makes a plain reply or user-provenance steering an answer; answers
  link via `AnswerToTurnIDs` (`agent/schema/turn.go:429-431`). A plain
  admitted reply clears the ENTIRE pending set — multiple pending calls
  resolve with ONE admitted answer — at the queued input's consumption
  (`agent/session_lifecycle.go:2342-2352`), and user-provenance
  steering clears it too (`clearAskPendingForResolvingSteer`,
  `agent/session_tools_ask.go:231-235`, via `steeringAnswersAsk`).
  The queued intent (`clientMutationQueueEntry`,
  `agent/session_client_mutation_queue.go:164-170`) carries no
  precondition fields today. Autonomous wakes are refused while
  pending (`session_lifecycle.go:674-676`), so no later turn can
  supersede a pending question: the real second-question case is an
  append. `ask_user` is root-only (`isSubagentSession`,
  `agent/session_tools_ask.go:52-63`); subagents cannot hold
  a pending ask. A legacy RESTORED ask is text-only:
  `PendingAskCalls` includes only questions with both ids, so a
  restored pending set can carry a question with NO calls (pinned by
  `TestVoicePendingAsk_LegacyRestoreStaysTextOnly`,
  `agent/session_voice_pending_ask_test.go:336-348`) — on current
  daemons, not an older-daemon degradation. The revision counter is
  NOT durable: the `askPending` block's own comment says it "is not
  itself part of persisted SessionMeta" (`agent/session.go:743-753`),
  and restore re-derives into a FRESH session whose counter starts at
  zero, so a single-ask restore re-mints revision 1 — the same value
  the pre-restart live append minted. While pending, session
  state is `SessionAwaiting`
  (`agent/session_state.go:25-45`), the daemon-truth source for the
  hub's needs-you attention state. An accepted interrupt marker clears
  pending and finishes idle (`session_lifecycle.go:858-865`); the close
  branch clears (:868-873).
- **The daemon wire carries the pending-call identity; the probe drops
  it — and the row carries no boot generation.** The thread row's
  projection clones the full `PendingAskState` — per-call
  `TurnID`/`CallID` and the `Revision` (`appwire/types_pending_ask.go:6-17`)
  — onto `EvenerThread.PendingAsk`
  (`server/appwire_thread_projection.go:117`). `EvenerThread` has NO
  boot-generation field (`appwire/types_thread.go:63-85`): the boot
  generation appears only on read RESPONSES
  (`ThreadReadResponse.BootGeneration`, `appwire/types_turn.go:382-386`),
  and the probe is a LIST read whose response is rows and cursors
  only (`appwire/types_turn.go:316-320`). The durable per-boot counter
  is real (`schema.NextBootGeneration`,
  `agent/schema/boot_generation.go:31-37`: fsynced before the boot
  serves, never reused across boots) and lives on the server, minted
  per start (`server/appwire_identity.go:164`,
  `cmd/evener/serve.go:780`) — but it has NO carrier on the row path
  today and the agent session holds no copy; carrying it is owned
  work (scope 2). But
  `ProbeResultFromThread` fills only `PendingAsk: bool` and the bounded
  `PendingQuestion` text (`cmd/evener-hub/internal/hubcore/prober.go:175,179`),
  so no ask turn id crosses into `LiveEntry`
  (`cmd/evener-hub/internal/hubcore/roster.go:24-50`), `LocalDaemonEntry`
  (`local_daemon.go:64-90`), `AttentionEntry` (`appwire/attention.go:10-30`)
  or the frame (`agent/job_watch_hub.go:213-224`).
- **The row's question and identity are sampled in two locked calls.**
  The envelope's ask facet samples `src.PendingQuestion()` and then
  `PendingAskCalls()` separately (`server/thread_envelope.go:420-426`),
  each accessor taking the session lock independently
  (`agent/session_tools_ask.go:101-126`); an append between the calls
  mispairs the excerpt with the revision, and publication installs the
  question unconditionally while guarding only the pending state's
  revision advance (`thread_envelope.go:568-573`). A published row can
  carry Q1's count-one excerpt with Q2's revision — the mispair the
  atomic snapshot (scope 2) removes.
- **The roster event fires flags-only, and misses pending-set changes.**
  Awaiting normalizes to the `needs_you` attention level
  (`cmd/evener-hub/internal/hubcore/tree.go:548-567`,
  `cmd/evener-hub/internal/hubcore/attention.go:10-21`); the broker assigns the event kind on
  ask/needsResponse changes (`attention_subscriptions.go:123-131`);
  `AttentionEntry` carries no question text today
  `cmd/evener-hub/internal/hubcore/attention.go:100-106`) — and no
  derivation moment either (`appwire/attention.go:10-30` carries no
  timestamp; the broker publishes on a 5s tick,
  `main_background.go:52-60`, recording no per-entry moment). The frame payload is flags-only
  (`job_watch_hub.go:213-224`). The per-target watch has a selectable
  `question` event (:376-387). Documented at
  `docs/automation.md:132-142`.
- **The Manager roster is archive-blind and remote-inclusive.**
  `watchHubAttention` derives the Manager roster from the same
  remote-inclusive navigation snapshot the tree uses, passing nil
  archive decisions — "Archiving clears human attention, not the
  Manager's live population" (`cmd/evener-hub/main_background.go:63-72`),
  pinned by `cmd/evener-hub/attention_roster_archive_test.go:14-90`:
  an archived, question-pending live session stays in the Manager
  roster with `needs_you`. The derivation excludes non-top-level
  sessions (subagents); it excludes neither archived nor remote lanes.
  The roster refresh loop and the attention broker's publish loop run
  independently (`cmd/evener-hub/internal/hubcore/roster.go:1180-1203`;
  `main_background.go:59-75`), so a discovery-time join can pair a
  fresh row with attention facts up to a broker cadence stale.
- **The enrichment seam.** Rows are built by `threadFromEntry` in
  `cmd/evener-hub/internal/appsource`, which cannot reach the hub's attention
  derivation: `hubcore` imports `appsource`
  (`cmd/evener-hub/internal/hubcore/config.go:11`), so the call would
  be an import cycle. The attention facts are reachable one layer up:
  `sessionOperations.discover`
  (`cmd/evener-hub/session_reads.go:22-53`) runs after the fan-out with
  `attentionBroker.latest` in scope
  (`attention_subscriptions.go:24-27`, published from
  `watchHubAttention` via `main_background.go:52-83`). The
  roster-to-discovery adapter `localDaemonEntriesFromRoster`
  (`cmd/evener-hub/app_rpc.go:121-150`) carries no attention field
  today; the fields land at the discover projection, keyed by the
  roster's OWN identity map — `rosterIdentities`, which retains the
  workspace ref across thread/clear and names the instance id
  (`main_background.go:97-107`). Two other workspace-ref builders
  exist (`localDaemonWorkspaceRef`, thread-id-first,
  `cmd/evener-hub/internal/appsource/local_daemon.go:1637`;
  `localSpawnWorkspaceRef`, session-id-first, used by the broker,
  `app_threadlifecycle.go:395`) and their empty-ref fallbacks DISAGREE,
  so re-deriving the join key through either would silently miss
  attention facts; the join uses `rosterIdentities` itself, and
  unifying the builders is named owned work.
- **The relay path.** `delegate_send`'s Manager branch
  (`agent/session_tools_hub.go:558-613`): `input_mode` start with a
  plain reply is the answer path; the receipt is a human input receipt;
  the tool text warns that a notification alone does not answer; the
  request REQUIRES `expected_instance_id` (:458-464, forwarded
  :593-600) — an identity today's flags-only frame does not carry,
  and one whose check (`requireRootMutationIdentity`,
  `server/appwire_thread_settings.go:168-185`) compares the DURABLE
  thread id: its own comment scopes the fence to Clear/replacement,
  so it does not fence a restart. The daemon's direct mutation
  methods decode PERMISSIVELY — `HandleTyped` uses plain
  `json.Unmarshal` (`internal/appserver/router.go:44-49`) — so an
  older daemon silently IGNORES unknown request fields; the strict
  decoder on the Manager-operation envelope
  (`appwire/session_tools.go:142-150`) guards the hub's own input,
  never the older-daemon hop. A params decode failure surfaces as one
  generic `InvalidParams` (`internal/appserver/router.go:44-53`) with
  no machine-readable unknown-field identity — decode errors cannot
  be a capability signal. The admission-check idiom already exists:
  the turn params carry `ExpectedQueueRevision`
  (`appwire/types_turn.go:886`), compared at admission — and `start`
  is REFUSED while a turn is active
  (`agent/session_client_mutation.go:425-429`), so the append window
  is answerable only through `queue` or `steer`, both of which clear
  the whole set (grounding above). The relay's prepare step already
  reads the thread row's capabilities for skill and command input
  validation (`cmd/evener-hub/app_relay.go:87-95`) — the existing
  fail-closed gate shape the fence needs.
- **Overlaps.** PR 5641 owns watch-argument surface and
  `docs/automation.md`; PR 5662 owns delivery reliability and
  `docs/job-control.md`; both rebase under, neither re-litigated. Issue
  #5666 (status-event loss) predates this and covers the delivery side.

## Scope

1. **Attention facts on pull rows, wired at the discover seam.**
   Discovery rows for sessions the Manager roster covers gain
   `attentionLevel`, sourced from the same derivation the roster
   consumes, enriched in `sessionOperations.discover`'s projection
   post-fan-out (the seam grounded above), keyed by the roster's own
   identity map — one join function, never a re-derivation (the
   two-builder divergence is grounded above; unifying the builders is
   owned work). The population is the
   roster's own: archived and remote lanes INCLUDED (the pinned
   archive test is the contract), subagents excluded. Absent when spec
   4 has not landed, for subagent rows, and for rows with no roster
   entry (disk-index rows); never zero-filled. Ask-pending keeps the
   row's existing `Evener.AskPending` as its one fact; approval-pending
   keeps the existing escalation facts; this spec adds the attention row
   fields — `attentionLevel` and its `attentionAsOf` moment —
   `needsResponse` is deliberately NOT a row field
   (its tested meaning is the opposite of "the Manager should look
   here"; the row carries the components and the relay rule below is
   the contract). The attention facts carry their derivation moment:
   `AttentionEntry` gains `publishedAt`, stamped at the derivation
   site and carried to the row as `attentionAsOf` — named owned work
   (`appwire/attention.go` and the `watchHubAttention` derivation),
   the one landing zone for a moment no existing structure carries
   (grounding above) — so a reader can see the join's tolerance.
2. **The pending set's ask facts travel, and changes deliver.** The
   excerpt, the count and the flags (scope 3) travel on the frame and
   the row's excerpt additively (the same field set, two consumers),
   sampled ATOMICALLY: one locked session accessor returns the
   excerpt, the count, the flags and the pending set's identity
   together (a new snapshot accessor in `agent/session_tools_ask.go`;
   the envelope's ask facet calls it ONCE, replacing today's two-call
   sampling — grounding above); the append-between-samples mispair is
   the required red, this spec's own publication seam. THE PENDING
   SET'S IDENTITY — its change-identity carriers end to end (row,
   frame and relay) and the displayed call's COMPOSITE identity (the
   ask turn id beside the call id: two distinct ids the machinery
   carries separately, grounding above, and the fetch needs the turn
   id) — is DESIGNED AND RED-TESTED by the fence spike
   (`specs/manager-tools/fence-spike.md`, the round-7 escalation's
   record): five reproduced reds and four gap-pinning
   characterizations against the real seams, on this branch. The
   settled shape, this spec's contract: the identity is the PAIR of
   the session's boot generation and the per-boot revision; the pair
   is COMPARED INSIDE
   the shared admission transaction (`acceptClientMutation`, which all
   three input modes already funnel through), BEFORE anything is
   durable, so the receipt IS the refusal and a mismatch — including
   an empty pending set, typed — can never become a durable answer,
   which is what makes "never admitted" hold across crash and restore
   by construction. The boot-generation carrier is a
   post-construction initialization on the OWNED session, not a
   constructor argument — the mint requires the session to exist
   (`schema.NextBootGeneration` runs after it owns the session;
   fresh and restore construct at `cmd/evener/serve.go:725-765` and
   mint at `:776-788`, Clear constructs at `:1104` and mints at
   `:1124`): the hub sets the field immediately after construction —
   fresh (`NewSession`, on EVERY caller — the serve paths above plus
   `agent/subagents.go:890` and `cmd/evener/run.go:469`: subagents
   cannot hold a pending ask (root-only, grounding above) and the
   CLI single-session path fences the same way, so neither is
   exempt), restore (the production entry
   `RestoreSessionFromMetaWithConfig`, `agent/session_init.go:888`),
   Clear's replacement, and the delegate-restore paths alike — and
   BEFORE the first admission or projection touches the session.
   The pair's WIRE carriers are designed end to end — the grounding's
   owned work, hop by hop: `PendingAskState` gains `BootGeneration`
   (additive, minted beside `Revision` inside `PendingAskCalls` under
   the same lock, `agent/session_tools_ask.go:101-116`), so the state
   the daemon's row projection already clones
   (`server/thread_envelope.go:423-425`) carries the pair atomically
   with the calls; the probe's row copy (`ProbeResultFromThread`,
   which today drops even `Revision`/`Calls`, grounding above) gains
   the pair; `LiveEntry`, `AttentionEntry` and the `needs_you` frame
   carry it (the broker's trigger keys and the relay's fence both read
   it); and the relay params forward it — on the Manager tool edge as
   the additive keys `expected_ask_revision` and
   `expected_ask_boot_generation` beside the existing
   `expected_instance_id` (the named landing zones:
   `managerSendDefinition`'s property set and `invokeHubInput`'s key
   allowlist, `agent/session_tools_hub.go:559-566`, forwarded into
   the params construction at `:588-596`), and on the three input
   modes' params as the pair fields (below). AppWire regeneration
   and the appwire-client
   validators for every new field land in the same change.
   The consumed answer clears EXACTLY the calls captured with the pair
   at admission (the scoped clear: an ask appended after admission is
   not in the captured list and survives deterministically — no window
   at all). The capture records `(calls, legacyCount)`: the set's
   accessor skips questions without both ids (grounding above), so a
   call-less legacy question present at admission is COUNTED, not
   named — the scoped clear removes the named calls AND that many
   call-less questions, the durable evidence carries the same pair,
   and restore subtracts likewise; an all-zero capture against a
   nonempty set is UNREACHABLE in a well-formed admission (the
   capture is exhaustive: every pending question contributes a
   named call or increments the count), so the typed refusal stays
   as the MALFORMED-STATE invariant guard — a capture that cannot
   express the set it names is a mismatch, never a silent
   whole-set clear (journey 6 injects it directly, labeled as such);
   the mixed case resolves naturally. EVERY
   answer-reachable clear routes through the scoped primitive: the
   two designed answer sites plus the steering-carrier entry clear
   (`agent/session_lifecycle.go:1530-1575`), the
   already-incorporated replay branch (`:2218-2222`) and the recovered
   pre-append-failure branch (`:2303-2313`). THREE named exception
   classes keep the whole-set clear, each inherently unfenced: the
   accepted-interrupt branch (`:864`), the close branch (`:873`), and
   the direct-user-input branch (`:2352`) — a user input that arrives
   through no client-mutation record has no journal record to carry a
   pair, so no fence can bind it; today all six sites clear
   whole-set (fence-spike §2's inventory). Queue transformations
   carry the captures forward as a LIST: `drain` and `promote` build
   REPLACEMENT records (the originals are marked terminal and their
   payloads cleared,
   `agent/session_client_mutation_queue.go:859-920,1250-1260`), and a
   replacement can combine entries admitted under DIFFERENT
   contracts, so its precondition shape is a LIST of captures, one
   per replaced record, under a dominance rule: all-fenced sources →
   the union of their captured sets, scoped clear — the union's CALLS
   dedupe by composite, and its `legacyCount` takes the MAX across the
   list, never the sum: call-less questions are unnamed (two records
   capturing the same legacy set must clear it once), and the
   call-less populations any two captures represent are NESTED,
   never disjoint — a restore re-derives the call-less set from the
   same durable transcript, so a capture taken across a restore
   boundary represents a population that CONTAINS every later one
   (and a live append is call-bearing, adding none) — which is what
   makes MAX correct even for a drain combining records admitted
   across a restore boundary, where summing would over-clear; the
   reason is
   stated because the rule is load-bearing;
   ANY unfenced
   source → whole-set clear, today's semantics — the fence never
   weakens an unfenced input's contract, so the combined record
   resolves at least what its unfenced source alone would resolve. Restore resolves
   exactly the calls the DURABLE evidence names, and the evidence is
   the admission-time captured composites, never a consumption-time
   sample: today's durable answer names EXECUTIONS —
   `AnswerToTurnIDs` "names the pending question executions"
   (`agent/schema/turn.go:429-431`, every ask in one turn stamps the
   same id, and the field is sampled from live state at consumption,
   `session_lifecycle.go:2331`) — so it cannot express a call-scoped
   subtraction; the redesign gives the captured composites TWO
   durable carriers written from the one capture: the journal
   record's preconditions (for the scoped clear) and a new additive
   `schema.Turn` field carrying the capture the resolving turn
   CONSUMED, both halves together — the resolved calls AND the
   `legacyCount` (one field, both halves: restore subtracts the named
   calls and that many call-less questions from ONE durable carrier) —
   written by the consumption path from the record's captured list
   and never re-sampled from live state — and the boundary writers
   write the same field from their records: the two failure writers
   (`TurnFailure` with `SteeringCarrier`,
   `agent/session_events.go:276-292`,
   `agent/session_queue.go:1141-1214`), the recovered
   pre-append-failure writer — `recordClientMutationFailure` persists
   its own resolving `TurnUserInput` directly
   (`agent/session_client_mutation_queue.go:1694-1715`, written before
   the normal consumption assignment), the one resolution-evidence
   writer the round-8 inventory missed, and it copies the capture from
   its journal record onto the recovered user input so the crash-cut
   restore subtracts the same names — and the STEER writers
   (`claimSteeringCarrierInput`, `agent/session_client_mutation_queue.go:379-418`,
   and `consumeSteeringMessage`, `agent/session_queue.go:959-1019`). Round
   10 split their rule by the two fields' KINDS, because the
   unconditional narrowing broke two runnable contracts. The
   captured-calls field is call-resolution EVIDENCE: AUTHORITATIVE
   for resolution, written at every FENCED writer from the RECORD's
   captured list, never live state — and it MAY name the active
   execution's calls. `AnswerToTurnIDs` is a LINK field and keeps the
   existing active-execution exclusion in EVERY regime — "cannot be
   its own predecessor": the continuation validator rejects
   non-earlier edges
   (`internal/transcriptindex/answer_continuation.go:103-117`), and
   today's `consumeSteeringMessage` already strips the active execution from
   its live sample. A FENCED resolving turn writes its links from the
   capture MINUS the active execution; its evidence field keeps the
   full capture — and the split's EMPTY edge is stated: for a fenced
   INLINE self-resolving steer (a steer resolving its own turn's
   ask), the capture minus the active execution is EMPTY, the
   resolving turn's `AnswerToTurnIDs` is empty — exactly today's
   unfenced inline strip result — and both consequences match today
   on purpose: `job_watch_hub.go:418-421`'s suppression key
   (`len(AnswerToTurnIDs) != 0`) correctly does not fire, and no
   answer-observation metadata is written
   (`internal/apptranscript/apptranscript.go:902-911` returns nil
   for an empty list), because no communicate delivery fires for
   the self-edge either — correct, not a regression; journey 6 pins
   it as the regression case. UNFENCED and legacy records keep TODAY's behavior
   exactly — the carrier claim records live answer names on the
   journal record (`claimSteeringCarrierInput`,
   `agent/session_client_mutation_queue.go:401-419`), and delivery
   unions live and recorded names with the active execution stripped —
   so the runnable answer-link contracts (the inline, carrier,
   restore-retry and journal-copy tests,
   `agent/session_ask_answer_link_test.go:203-295`) hold unchanged.
   `job_watch_hub.go:420`'s suppression key keeps working on the link
   field; turns predating the
   fields keep whatever they carry, named legacy. With every writer
   sourced from the one capture, restore subtracts exactly the
   captured names at every resolving boundary, and its shortcut
   boundaries (assistant-final, generic completion,
   round-resolves-everything) fire only when the captured names are
   exhausted — rounds that found asks but resolved none keep
   scanning, preserving the existing accumulate-across-rounds
   behavior. The composite
   identity travels the row, the frame and the relay so the exact-turn
   fetch is addressable, and the frame carries the bounded composite
   LIST — at most K composites, the FRAME's own K with a NAMED
   starting value of 10 (explicitly NOT spec 2's cursor-line K, a
   different bound for a different payload) — in declared fields
   ON THE ASKING LANE'S ROSTER PAYLOAD ROW:
   `compositeList` for the list, `truncatedList` for its OWN
   truncation marker, and `listReason` for the marker's reason
   (scope 5's vocabulary) when the set is larger, beside the
   question text, the ask turn id, the count, the identity and
   the pair the same row carries — the payload row is the
   frame's per-lane surface, and a delta with two askers
   carries each asker's own row, which one frame body cannot;
   the frame body keeps its today shape and its own
   `watchFrameMaxChars` bound (`agent/watch_types.go:37`,
   applied at `agent/watch_frame.go:107`), and no ask fact is
   added to it. The frame's
   existing QUESTION count is a fact about the set in a DIFFERENT
   unit from the composite list (questions vs calls) and is never the
   truncation signal — scope 5's paged read addresses the whole
   set. K's value is a RECORDING OBLIGATION with its direction
   fixed, never a derivation: K is chosen first (10), the
   max-case fixture then builds K+1 composites at the real
   composite encoding (both id forms) on ONE asking lane's row
   and MEASURES the whole ROSTER SEGMENT — that row beside the
   segment's other real populations, the multi-lane initial and
   the 256-event delta (the segment's own-bound obligation,
   below) — against the SEGMENT's chosen bound and asserts it
   fits — if it does not, K lowers before landing, never the
   assertion. The
   frame's bound and the roster payload's are SEGMENT BUDGETS,
   never one fold: the arithmetic forbids the fold. The initial
   roster update carries EVERY entry in one payload
   (`cmd/evener-hub/attention_subscriptions.go:72-82`, about a
   kilobyte per lane at the real field set), a delta batch
   carries up to 256 events before resync (`:11`), and one
   delivery marshals every changed entry into one payload
   (`agent/job_watch_hub.go:222-235`) — 32 ordinary entries
   with NO composite identities already produce one
   4,847-rune payload against the 4,096 bound today (the
   round-14 review measured it on the real journal path), so
   no K can fund the fold. The roster segment gets its OWN
   chosen bound with a STRUCTURE-PRESERVING truncation
   contract, closed at every site that writes the surface:
   whole entries, never a cut inside the payload's
   JSON; a DECLARED marker — the frame field
   `roster_dropped`, a count of dropped entries on its own
   frame line beside `roster_events`
   (`agent/watch_send_rail.go:52-53`), the appended-line
   shape `roster_initial` already uses, because the
   coalescing merger below reads line fields and a marker
   inside the capped body would be unreachable to it; and
   the recovery — a LADDER, each rung with its own reach,
   below — named in the frame's recovery line when the
   marker engages, as owned work on a NEW recovery-line
   shape `roster_recovery:` that NEVER carries
   `roster_resync: true`: the coalescing merger returns
   the current frame immediately on that marker
   (`agent/job_watch_hub.go:42-44`), so a recovery line
   that began with it would short-circuit the next merge
   and silently discard the pending predecessor payload
   this contract promises to merge, while asserting a
   resync that is not happening. Today's
   `rosterRecoveryFrame` (`:35`) is the line SHAPE's
   precedent only — its text begins `roster_resync: true`
   — and splitting the shape from the marker is part of
   the same owned work. Re-registering the roster
   watch is NOT the recovery for dropped entries: a fresh
   initial re-delivers the same bounded segment, so the
   same lanes truncate again. The LADDER, rung by rung:
   the marker funds the first — `roster_dropped` gains the
   dropped REFS, the refs of the dropped entries as a
   bounded list whose bound is a named constant (a
   recording obligation against the real fleet shapes,
   like the segment bound), with the count naming the
   TOTAL when the refs overflow it — and per-lane reads
   recover exactly the named refs (the Manager's
   lane-scoped `job_status` / `read_transcript` reads,
   each call bounded by that lane's own read, total at
   ANY fleet size). The spec-1 `job_list` fleet read,
   whose rows carry the ask flags, attention level and
   pending question text the dropped entries would have
   (grounded above), is the SMALL-FLEET rung — never
   claimed as total recovery, because that read's own
   contract is one call, no new pagination,
   whole-response refusal above the 256KB cap ("an
   over-cap response keeps today's refusal",
   `1-fleet-snapshot.md:279-283`): a fleet large enough
   to truncate the segment is exactly a fleet that can
   fail that read whole. And when the dropped refs
   overflow the marker's own bound the frame SAYS SO —
   the count still names the total, so the remainder
   beyond the listed refs is computable from the frame
   itself (total minus listed), and the ladder names its
   rungs, never a silent loss. Today
   the roster payload appends after the capped frame body on
   the same rail (`agent/watch_send_rail.go:52-53`); the
   design keeps the
   append, bounds the segment at the payload construction
   (`agent/job_watch_hub.go:222-231`, where the entries are
   marshaled), and stamps the marker line beside the payload
   when truncation engages. The COALESCING HOP is owned too,
   because the bound is only half the surface: a pending
   roster send the Manager has not consumed merges with the
   next delivery through `mergeHubRosterFrames`
   (`agent/watch_send_rail.go:703-705` →
   `agent/job_watch_hub.go:41-77`), which unions entries by
   ref with NO budget pass today and preserves only the
   resync/initial markers (`:72-75`). The merger re-applies
   the segment bound to the MERGED payload at whole-entry
   granularity and recomputes the marker: the merged
   `roster_dropped` count is the SUM of both inputs' counts
   plus any entries the merged bound itself drops, and the
   refs list the same sum BOUNDED by the marker's own
   ref-list bound — the count names the total when the summed
   refs overflow it, the overflow rule stated above — the
   merger is pure string
   surgery with no dropped-ref knowledge, so a lane dropped
   by both segments counts twice; the over-count is the
   SAFE direction (never an under-count), and a duplicated
   ref costs one redundant lane read, nothing more. The
   marker-preservation arms (`:72-75`) gain the
   dropped count beside resync and initial, so a truncated
   segment never coalesces into an unmarked one — with ONE
   exception, stated: a CURRENT resync or initial REPLACES
   the pending segment authoritatively (the merger's early
   return at `:42-44`), discarding the previous frame's
   marker BY DESIGN, because the replacement computes its
   own fresh count and refs from its own bounded snapshot.
   And the recovery line needs its own named survival,
   because the merger's surgery drops every previous-frame
   line it has no arm for: the merge builds on the CURRENT
   frame's body, unions only the `roster_events` payload
   into it (`agent/job_watch_hub.go:46-68`), and re-appends
   only the marker lines the arms name — so the arm that
   carries the dropped count from the previous frame
   re-appends the `roster_recovery:` line with it, stamped
   together, carried together, one coalescing rule. And the
   STAMPED-TOGETHER rule holds at every site that writes the
   surface, the merger included: when the merger's own bound
   drops entries from the MERGED payload, it stamps the
   recovery line beside its own dropped count exactly as the
   construction site does — a merged frame never carries a
   dropped count without the line; and the carry arm appends
   the previous frame's line only when the current frame
   does not already carry one — identical text, presence
   check, never a duplicate. The
   segment's bound is a
   RECORDING OBLIGATION like K's, with its direction fixed:
   chosen first against the real fleet shapes — a multi-lane
   initial snapshot AND a 256-event delta at the real
   encoding, entry COUNT part of the measurement, because
   per-entry size alone bounds nothing — and the boundary
   fixture pins the truncation marker at the chosen bound,
   including the coalescing case: two pending batches plus
   an already-truncated predecessor merge to a segment
   asserted WITHIN the bound with the marker preserved; if
   the real fleet exceeds the chosen bound the marker
   engages, never a silent loss. The self-influence notice
   and the job-read annotation keep their by-design behavior
   — both append after the cap by their own contracts
   (`agent/watch_frame.go:110-112`: never truncated away) —
   and no text of this spec claims the whole delivery fits
   one bound.
   The set's CALL-LESS count and its clamp metadata ride named
   wire carriers the whole way: `PendingAskState`
   (`appwire/types_pending_ask.go:3-17`, closed today with no count
   field) gains three additive fields — `CalllessCount` (the same
   count the capture represents), `CompositeTotal` (the set's
   composite count) and `CallsTruncated` (the row's own
   truncation fact: the clamped slice dropped composites). The
   mint sites SPLIT with the facts: `CalllessCount` and
   `CompositeTotal` are whole-set facts, minted inside the
   atomic accessor beside the pair where the questions are
   already walked, one lock, and cloned with the state
   (`ClonePendingAsk`); `CallsTruncated` is the ROW's fact about
   the clamped slice, so it mints at the WIRE PROJECTION where
   the clamp lands and K is known — stamped on the projected
   copy after the clamp — never inside the accessor, which
   holds the whole set under its one lock and takes no limit
   (`agent/session_tools_ask.go:101-112`). TWO of the three ride
   the spike's §6
   chain beside the composite and the pair — `CalllessCount`
   and `CompositeTotal` — and the chain's last leg is TWO
   hops, named: row projection → probe row copy → `LiveEntry`
   → `AttentionEntry` → the `rosterChange` payload whitelist
   (`agent/job_watch_hub.go:213-224`) → the frame's roster
   payload row. `CallsTruncated` never travels the chain: it
   is the ROW-SIDE fact the wire projection stamps on the
   clamped slice after the clamp, never a field on
   `AttentionEntry` — the two claims agree because only the
   two whole-set facts ride the chain.
   `CalllessCount` lands in the payload row's declared
   `calllessCount` field (scope 5's mixed-set completion
   bound); `CompositeTotal`
   lands on `AttentionEntry` in a declared field, so the row's
   `truncatedList` is COMPUTABLE from the entry itself —
   truncated iff the carried total exceeds the composite list's
   length — and `CallsTruncated` is the row-side fact the wire
   projection asserts beside the clamped slice (scope 5), giving
   the marker its input: no marker in this design names a fact
   no declared field carries. The frame's composite LIST rides
   `AttentionEntry` as its declared carrier, and the LAST HOP
   into the frame is a WHITELIST, not a marshaller — the
   round-14 text claimed "the frame builder marshals entry
   fields only" and was false: the payload builder declares a
   local `rosterChange` struct of exactly seven fields (Ref,
   Title, Level, Kind, AskPending, ApprovalPending,
   NeedsResponse) filled by a positional literal
   (`agent/job_watch_hub.go:213-224`), so an `AttentionEntry`
   fact reaches a payload row ONLY by joining that struct and
   its literal. Every entry fact this design names — the
   composite list, the composite total, the call-less count,
   the pair, the question text, the ask turn id, the entry's
   own identity fields `RosterInstanceID` and `ID`, the
   excerpt `complete`/`truncated` flags, and `listReason` —
   GAINS its
   declared field and literal slot there as owned work, and a
   row built from today's seven fields ships without them,
   which is exactly what journey 1's red and journey 2's
   green assert at the frame's edge. The two identity
   fields are the frame-only relay's own requirements, not
   new asks: `RosterInstanceID`
   (`appwire/attention.go:12-14`, stamped at
   `cmd/evener-hub/attention_subscriptions.go:98-110`) is
   the token journey 6 must carry into
   `expected_instance_id` to clear safely, and `ID` (the
   thread id, `appwire/attention.go:15`) is the fetch
   addressing — a frame that carries neither cannot decide
   the clear nor address the ask turn's read. The
   INTERMEDIATE hop is named with them: the hubcore builder
   that fills `AttentionEntry` field by field — ID, Level,
   the three flags, Title, Project
   (`cmd/evener-hub/internal/hubcore/attention.go:100-113`)
   — has no slot for any of the new entry fields today, and
   gains the fill for every one of them as the same owned
   work, the way `publishedAt`'s fill site is named on its
   own hop. A slice field ends the
   entry's value comparison at
   both baseline sites: the broker's changed-entry test
   (`agent/job_watch_hub.go:173`, `old != event.AttentionEntry`)
   and the subscription baseline
   (`cmd/evener-hub/attention_subscriptions.go:150`,
   `maps.Equal`). Both become a typed equality helper —
   field-wise, the list compared with `slices.Equal`, a nil
   list equal to an empty one — the map site as
   `maps.EqualFunc` over the helper; the helper is owned
   additive work named with both landing zones. The entry's
   `comparable` contexts are named IN FULL by MECHANICAL
   SWEEP — the enumeration is a grep of the tree for
   `RosterEvent` and `AttentionEntry` comparisons, never an
   assembly of review reports — production and
   test: the two sites above, plus TEN test comparisons the
   slice breaks because `RosterEvent` embeds `AttentionEntry`
   (`appwire/roster.go:5-8`) —
   `agent/job_watch_hub_roster_test.go:116,119,155,160,232,323,361`,
   `agent/job_watch_hub_roster_clear_identity_test.go:71`,
   `agent/job_watch_hub_roster_reconnect_test.go:27,51` —
   all TWELVE migrating to the helper with the production
   sites. The sweep's exclusions are named with them:
   `agent/job_watch_hub_roster_test.go:1145` compares `.Ref`
   alone (a field comparison, unbroken by a slice field),
   and the `RosterEvent` references under `cmd/evener-hub`
   (`cmd/evener-hub/attention_roster_reconnect_test.go`,
   `cmd/evener-hub/e2e_manager_roster_test.go`) construct
   and decode without ever comparing whole entries. The broker's pending-set change trigger is DESIGNED HERE,
   this spec's own scope — the spike's record says it left the
   trigger unchanged — and this spec owes its red: the pair lands on
   `AttentionEntry` and the frame beside the existing flags, the
   broker's change detection gains the pair to its comparison keys
   (`cmd/evener-hub/attention_subscriptions.go:123-131`), and
   journey 2's append case and journey 3's restart case are its
   reds. The
   contracts this pays: an APPEND (a second `ask_user` in the same
   turn: count 1→2, flags and level unchanged) delivers; a restart's
   re-derivation of a stable set delivers exactly once — on the boot
   component, the part the revision alone cannot provide (grounding
   above: the revision is not persisted, and a single-ask restore
   re-mints the same value); a published row's excerpt, count and
   identity all describe ONE set; and the frame's identity is enough
   to DECIDE and to address the fetch (the composite identity, never
   the call id alone). No text-hash fallback: identity is exact, and
   older daemons that do not carry it degrade to flags-only per the
   existing rule — absent-when, never fabricated. No new event kind, no
   resolution event in
   v1 (pull confirms; push resolution optimizes the channel whose
   trust PR 5662 is rebuilding).
3. **The excerpt is a notice, not an answer key.** The bounded excerpt
   gains two daemon-side flags, computed at the LIVE append only —
   `appendAskPendingLocked` receives the call's full arguments, while
   `setAskPendingLocked` sees parsed questions only and a restored
   set cannot reconstruct the calls' JSON (grounding above; the
   landing zone is `agent/session_tools_ask.go:143-165`) — so both
   flags are ABSENT after a restore, fail-to-absent per the existing
   rule: `truncated` (the bounded text or labels were cut) and
   `complete` (true iff the full ask's answer-relevant content —
   question, option labels AND details, recommended markers,
   multi_select, `why`, `if_unanswered` (`agent/schema/ask_user.go:11-26`)
   — fits the bounded form; false whenever the details the excerpt
   structurally drops were present). Both travel with the excerpt on
   row and frame. The ask machinery's own comment says the excerpt is
   insufficient for an external responder; the contract agrees.
4. **Teach what exists.** `docs/automation.md`'s discovery contract names
   the row's existing ask fields (`AskPending`, `PendingQuestion`) and
   the new attention facts; the Manager prompt template
   (`agent/testdata/messagingprompt/manager-on.md`) teaches the same, so
   a Manager knows the fleet read answers "who needs me" without
   transcript archaeology.
5. **The relay contract, in the guides.** `docs/automation.md` and the
   session-activity guide state the guarantee in product terms:
   - While a session is question-pending, input submitted through the
     queued start path with user provenance is admitted as the answer;
     the pending fact clears when the session consumes it; a
     notification alone never answers.
   - A plain admitted reply clears the ENTIRE pending set — one
     admitted answer resolves every pending call — so a relaying
     Manager sends exactly one.
   - Cancellation is not answering: an accepted interrupt marker clears
     pending and finishes idle, and the close branch clears — the two
     existing exceptions, named so no reader mistakes them for loss.
   - **Sufficiency rule:** the frame or row excerpt lets the Manager
     DECIDE and triage; ANSWERING requires the full ask — EVERY
     pending ask, not only the displayed one: pending asks accumulate
     across rounds (grounding above), a set can span several turns,
     and one admitted reply resolves them all, so the relay must see
     the whole set first. Direct relay
     without a further read is allowed only when the excerpt is
     `complete`, untruncated, and the question count is one — a
     single pending call, or a call-less legacy ask — AND the
     pending-set identity the fence lands is sent and accepted
     (omission is the fallback path, never the direct relay) — which
     the ask machinery's own design makes rare; every other case
     REQUIRES fetching the ask turns first. The frame carries the
     pending set's composite LIST — every call's `(turnId, callId)`
     pair, bounded at the frame's own K (scope 2's definition), with
     the list's OWN truncation
     marker (`truncated-list`) when the set is larger — and the
     set's CALL-LESS count, so the fetch
     is addressable for the whole set; when the marker says the list
     is truncated, the fetch continues by a NAMED PAGED READ, never
     by shipping the whole array. For a MIXED set — named calls plus
     call-less legacy questions, the case the capture's
     `legacyCount` exists for (grounding above) — completion is TWO
     halves, because the page enumerates only composites
     (`PendingAskCalls` skips questions without both ids): page the
     composites to `nextOffset` empty, then serve the call-less
     half by the content-locate fallback (below) with the
     call-less count as the completion bound; the count is never
     derived by subtraction — the frame's question count minus the
     page's composite total is ambiguous (one call can carry many
     questions). Recorded evidence, both today's shape and the
     landed one. Today's unbounded row, measured by the round-12
     review at the
     real encoding: 4,000 composites in one turn encode a minimal
     existing `SessionOperationResponse` at 316,555 bytes with fresh
     24-character turn ids — over the 262,144-byte whole-read cap
     before any page field exists — and at 260,555 bytes with the
     client-mutation `turn_m<N>` ids a Manager-relayed ask actually
     carries, 1,589 bytes from the cap before any remaining
     metadata. The design's conclusion is by construction, not by
     the arithmetic: the row's `PendingAsk.Calls` is CLAMPED to the
     frame's K composites everywhere the row serves — the mandatory
     `Thread` on `ThreadReadResponse`
     (`appwire/types_turn.go:367-369`), whose projection clones the
     complete slice unconditionally today
     (`server/appwire_thread_projection.go:117`), rides every read
     regardless of `IncludeTurns`
     (`server/appwire_read_handlers.go:204-216`) — with the set's
     composite total and the row's truncation fact in their named
     carriers (`PendingAskState.CompositeTotal`, minted with the
     pair inside the atomic accessor, and
     `PendingAskState.CallsTruncated`, stamped at this
     projection where the clamp lands — scope 2's mint-site
     split) beside
     the clamped slice, so the pager is the whole-set read; the
     paged read kills the whole-array path whatever the id form,
     because the row stops shipping it. The clamp lands at the WIRE
     PROJECTION — the site that clones the slice today
     (`server/appwire_thread_projection.go:117`) — never inside the
     atomic accessor: the accessor keeps the whole set under its
     one lock, because the page server and the ask facet's excerpt
     both sample it (below). The clamp's REACH is the serving
     daemon's own projection: a remote thread's row is clamped
     only by the remote daemon's version, because the hub
     forwards the remote response byte-for-byte by design
     (`cmd/evener-hub/internal/appsource/remote_hub_refs.go:530-541`, the
     `ThreadReadResponse` case at :687 — the translator rewrites
     ref and capability fields only). A pre-clamp remote daemon
     therefore still ships the whole array, and the hub's read
     cap refuses the Manager read TYPED — the degradation is a
     typed refusal, never a silent path; remote hubs pick up
     the clamp on upgrade, and until then the typed refusal is
     the lane's behavior. The page request is named
     owned wire work, wire to
     tool. WIRE: an
     ADDITIVE page request on the ref-addressed thread read — NOT
     the probe's fleet-shaped `ThreadList`
     (`cmd/evener-hub/internal/hubcore/prober.go:71`, whose
     `ThreadListParams` (`appwire/types_turn.go:300-314`) carries no
     ref/pair/offset/limit), but the existing ref-addressed read
     shape (`ThreadReadParams`, `appwire/types_turn.go:322-342`)
     extended additively with the pair, offset and limit — gated by
     the SAME `AskFence` capability bit that gates the fence's row
     machinery: the daemon's strict decoder accepts the fields on
     new daemons, and the hub never sends them to an old one. TOOL,
     end to end: the page keys ride the `job_status` hub branch —
     the one Manager read whose dispatch already builds
     `ThreadReadParams` (`agent/session_tools_hub.go:731-746`
     through `SessionOperationRequest.Read`,
     `appwire/session_tools.go:84-99`; two submitter families
     build that request today — this metadata read, and the
     held-snapshot and answer-continuation lookups at
     `agent/job_watch_hub.go:958`, which send no page keys and
     gain page fields only when requested; `read_transcript`'s
     hub branch is a different
     params family, `:705-730`, untouched): the branch's closed
     schema (the default branch at
     `agent/session_tools_hub.go:86-94`, `ref`/`jobId` only,
     unknown keys refused) gains the additive page keys (the pair —
     `expected_ask_revision` and `expected_ask_boot_generation`,
     the same keys the relay sends — plus `offset` and `limit`),
     its fixed decoder struct gains the fields, the dispatch maps
     them into `request.Read`'s new page fields, and the response
     lands in a named additive field: `ThreadReadResponse`
     (`appwire/types_turn.go:365-402`, closed today with `Thread`
     mandatory and no page field) gains `pendingAskPage` carrying
     `{composites, total, nextOffset, calllessCount}` — and the
     page reaches the Manager's tool output through the existing
     projection, traced end to end:
     `SessionOperationResponse.Read` returns the response raw
     (`agent/session_tools_hub.go:751-755`), the hub preserves
     the row and assigns the complete read
     (`cmd/evener-hub/session_tools.go:246-252`), and `o.read`
     passes the source response through (`:557`, only the thread
     body is rewritten), so the additive field rides the same
     response path the metadata read's fields reach today. The
     grammar stands alone: a request carrying a
     page key is a PAGE, `ref` required, and the page keys are
     refused in combination with the job-branch fields exactly as
     spec 2's fetch grammar refuses its conflicts. Served
     by the state accessor the row projection already samples
     (`PendingAskCalls`, one `s.mu` hold per page): `{ref, pair,
     offset, limit}` → `{composites, total, nextOffset,
     calllessCount}`, PAIR-FENCED
     (a page against a changed set fails the fence typed). The
     page's `total` is a COMPOSITE count, and the page OWNS iteration
     to completion — `nextOffset` empty is the end; the frame's
     question count never drives completion (its unit is questions,
     not calls); each page response carries the set's CALL-LESS
     count beside `total` (the mixed-set completion bound, above).
     The caller-supplied `limit` is normalized
     server-side to a maximum whose value is a MEASUREMENT OBLIGATION
     with its binding bound named: the 256KB whole-response cap
     (`managerReadWithinCap`,
     `cmd/evener-hub/session_reads.go:18`, applied to this read at
     `cmd/evener-hub/session_tools.go:399-403`) — the max-page
     fixture (below) measures the WHOLE Manager result with a large
     underlying set, the clamped row plus the page together, and
     the normalized maximum is the largest limit whose whole result
     fits under that cap at both id forms. DEGRADATION has a
     named decision point at BOTH edges. Manager-side: the frame's
     `truncated-list` marker carries its REASON in the declared
     `listReason` field — `pageable` when the entry the frame
     builder serializes carries the ask-pageable bit, `unavailable`
     when it does not — and the bit rides a named carrier chain,
     because the frame builder (`agent/job_watch_hub.go:213-225`)
     serializes only `AttentionEntry`'s fields and that type is
     closed today with no capability
     (`appwire/attention.go:10-30`): `AttentionEntry` gains an
     additive ask-pageable fact, derived at the broker's own
     derivation site (`DeriveAttentionFromRoots`,
     `cmd/evener-hub/internal/hubcore/attention.go:87-113`) from
     the probe-cached `LiveEntry` it already reads — the same
     entry whose `Capabilities` the derivation currently drops
     (`cmd/evener-hub/internal/hubcore/roster.go:56-64`) — so `pageable` routes
     the sufficiency rule to the page and `unavailable` routes it
     to the content-locate fallback. The semantics are honest
     about the two read moments this creates: the fence's common
     prepare reads a LIVE row
     (`cmd/evener-hub/app_relay_read.go:164-212`, entered from
     `cmd/evener-hub/app_relay.go:71-96`), while the frame's reason is a HINT
     from the probe cache that can trail the daemon's real
     capability — the hub-side typed refusal below is the
     authority, and both skew directions fail closed: a `pageable`
     hint against a bit-less daemon fails that refusal, and an
     `unavailable` hint sends no page at all. Hub-side: a page
     request against a bit-less daemon
     fails the typed `ask-fence-unsupported-on-daemon` hub-side
     with the fallback
     named — fail-closed like the fence's own prepare — so the
     frame's K-composite list is then all a Manager gets
     (absent-when, never fabricated), and the
     relay falls back to reading the lane's newest turns per the
     call-less rule below. The fetch itself is
     defined in
     both landing orders and for the call-less case: with spec 2
     landed and the excerpt carries the composite ask-turn identity,
     the exact `{turn: askTurnId}` window read per DISTINCT ask turn
     (a set spanning N turns fetches N turns); without spec 2 — or
     when the ask is call-less and carries no id (grounding above) —
     read the lane's newest turns through the existing shape and
     locate each ask turn by its content (the ask turns are recent).
     No lane-order dependency.
   - **The relay fence is settled by the fence spike**
     (`specs/manager-tools/fence-spike.md`): the pair comparison sits
     INSIDE the shared admission transaction (`acceptClientMutation`)
     on all three input modes, before anything is durable — the
     receipt IS the refusal, so no late-rejection channel exists or
     is needed; the consumed answer clears exactly the calls captured
     with the pair at admission, and restore resolves exactly what
     the CAPTURED evidence names — the composites captured at
     admission, carried on the journal record and the resolving turn
     (scope 2), never a consumption-time sample (the scoped
     semantics, live and restore in agreement); a fenced arrival against an empty pending set is a
     typed refusal (a pair naming a set that no longer exists is a
     mismatch); older daemons are gated HUB-SIDE, fail-closed, by the
     `AskFence` capability bit checked in ONE common prepare step at
     the three mode entry points, its row read scoped to PAIR-BEARING
     requests only — an ordinary text input never pays a row read —
     the
     spike's pin proved today's gate misses ordinary-text queue/steer
     entirely; and omission equals today's unfenced legacy behavior
     (the unfenced record keeps the whole-set clear), stated honestly
     — the legacy/informed distinction lives in the guides. Three
     rounds of paper revision each broke in review (rounds 5, 6, 7);
     the spike's reds (on this branch, failing against today's code
     by design) are the implementer lane's starting corpus, and
     `fence-spike.md`'s inheritance list states which reds retire as
     placement refutations, which characterizations flip, and which
     new journeys the lane owes. The fence names the SET, never a
     call: an admitted relay resolves EVERY pending call it was
     admitted against — the existing whole-set admission semantics
     scoped to the captured set — so a Manager with more than one
     pending question relays everything the human has answered and
     re-reads after. `PendingAskState`'s own doc stays true —
     presentation hints, not a conditional answer API: the
     precondition refuses, it never conditionally reinterprets.
   - The relay request REQUIRES the target's instance identity; the
     frame carries it (the roster identity that retains the ref across
     Clear, `main_background.go:97-107`), so a Manager discovering an
     unknown lane from a frame can answer without a preliminary read —
     and never guesses across a Clear.
   - The per-target `question` event keeps its existing semantics.

## Non-goals

- No new row field for the question's text: the row already has
  `PendingQuestion`; this spec does not rename, duplicate or shadow it.
- No `needsResponse` on rows: its tested meaning is the opposite of
  "needs the Manager"; adding it as a row field would bake the
  confusion the roster already resolved.
- No per-session activity-summary change: the summary resource keeps its
  counts/context shape; single-lane attention reads use the discovery row.
- No automatic answering, no LLM-generated answers, no cross-session
  routing; the human (or a Manager relaying the human's explicit answer)
  always supplies the reply.
  - No change to `ask_user` round semantics, no new notification channel or
    retry machinery, no client BEHAVIOR change (the web and phone badges
    read the same derivation and keep working; new wire fields land with
    regenerated AppWire types and updated appwire-client validators) —
    with ONE named migration: the VOICE question surface resolves a
    just-asked question through the row's clamped list today
    (`cmd/evener-hub/voice_questions.go:20`, fed the row at
    `cmd/evener-hub/voice_speech.go:105`), and a still-pending composite beyond K is
    absent from the clamped slice — that lookup resolves through the
    same paged pending-ask read (scope 5) instead, so the voice
    surface migrates with the clamp and never falls back to empty
    spoken text for a beyond-K composite.

## Behavior contract

1. **Detection without luck, in pull.** A Manager's next discovery read
   after a lane posts `ask_user` shows the lane with its existing
   `AskPending` and `PendingQuestion` text plus `attentionLevel:
   needs_you` — for an archived lane too (the roster's pinned
   population) and a remote lane the roster covers — without reading
   that lane's transcript. The red test asserts exactly the new
   field's absence today (the existing fields are asserted as the
   anti-regression half).
2. **One derivation, with its moment.** The row's attention fact, the
   frame's flags and the frame's question text come from the attention
   derivation the Manager roster already consumes; the excerpt is the
   same bounded `PendingQuestion` the row carries, now carrying the
   ask facts — the displayed first question's composite identity, the
   count and the spike-owned pending-set identity — plus the two
   flags; `attentionAsOf` is the
   entry's `publishedAt` (scope 1's named landing zone). The roster
   and broker refresh loops are independent, so the
   join's tolerance is stated: attention may trail the row's own facts
   by up to the broker's cadence, and the disagreement rule is that
   the row's own `AskPending` is the fresher fact for ask-detection —
   the mandatory fetch before answering re-reads current state anyway.
   (Deriving from the same snapshot as the rows is the implementer's
   option if it proves cheaper; the contract is the tolerance either
   way.) A second copy of any fact is a design failure.
3. **The fact is truthful, and changes deliver.** The attention facts
   exist exactly while the probed session state says the row needs the
   human; they clear when admitted input consumes it. A pending-set
   change delivers even when every flag and the level are unchanged:
   an append (count 1→2) delivers, and a restart's re-derivation of a
   stable set delivers once — on an identity component that survives
   restarts, the machinery the fence spike lands (scope 2; the
   single-ask restore that re-mints the revision value is the red). A
   crashed
   question-pending session stays question-pending through restart per
   the existing `SessionAwaiting` restore; the DERIVED visibility
   facts add no state of their own — the one new durable state is the
   fence's own (the journal preconditions and the resolving turn's
   captured names, named in the recovery section, additive beside the
   durable ask markers), so the durability is inherited.
4. **The event carries enough to act, or says it does not.** A
   `needs_you` delivery carries the bounded question, its flags
   (absent after a restore — they are live-append facts, scope 3),
   the displayed first question's composite identity (absent when the
   DISPLAYED first question is call-less), the question count, the
   bounded composite LIST in its declared `compositeList` field with
   the `truncatedList` marker and its `listReason` (scope 2's
   declared fields), the set's CALL-LESS count in its declared
   `calllessCount` field (the mixed-set completion bound, scope 5),
   the pending set's identity (the spike's carriers), and the target's
   instance identity — enough
   to DECIDE and to address the fetch, the relay and the relay
   fence. Answering follows the sufficiency rule: complete,
   untruncated, count-one excerpts with the identity accepted may be
   relayed directly (rare by
   the ask machinery's own design); everything else names the ask
   turn to fetch first, or the newest turns when no id travels. An
   admitted relay resolves the whole set (scope 5); an older
   daemon's frame degrades to today's flags, never to silence.
5. **Push degrades to pull.** With every watch delivery dropped — the
   fleet's nightly reality — the discovery row still answers
   detection. The authoritative fact is the pull one; the event is
   convenience.
6. **Population honesty.** The attention fields are absent for rows
   outside the roster's population — subagent rows, and rows with no
   roster entry (disk-index) — and a subagent never holds a pending
   ask (root-only). Archived and remote lanes KEEP their facts (the
   pinned roster behavior flowing to rows). A delegate needing
   attention projects `awaiting` into the parent's delegate
   diagnostics without changing the parent's own attention kind — the
   pinned existing rule, unchanged by this spec.

## Recovery and preservation

A pending question is user work in waiting; nothing here may lose it.
The DERIVED ask state stays exactly as durable as today: the markers
derive from the durable `PendingAskState`/`SessionAwaiting` facts and
the probe that already carries them, so crash, daemon restart and hub
restart inherit existing guarantees. The fence names its OWN new
durable state beside them: the journal record's preconditions (the
captured composites and pair, read at consumption and at restore) and
the new additive `schema.Turn` field carrying the capture a
resolving turn consumed — the calls AND the `legacyCount`, both
halves of the capture — both additive to their records, written from the
capture admission already holds. The lane owes legacy-record
compatibility: a journal record or resolving turn predating the
fields carries no captured names and takes today's unfenced
whole-set behavior, stated; and a replay-compatibility test covers
both fields. Clearing is conservative and existing: only admitted
input clears — plus the named whole-set exception classes (the
accepted interrupt marker and the close branch, user actions, not
losses; and the inherently unfenced direct-user-input branch, which
has no mutation record to carry a pair — scope 2), stated so no
reader mistakes any for loss. A failed relay
attempt (the empty-receipt mode the fleet observed) and a REFUSED
relay (the spike's fence naming a changed pending set) both leave the
question visible — the safe direction. Only a LEGACY restored set
serves call-less (grounding above: canonical restores keep both ids,
and the spike's characterizations pin it) — the excerpt names no call
identity exactly when the content-locate fallback applies (scope 5)
— and the re-entered fence identity is restart-safe by contract
(scope 2). The
new derivations — the frame's text,
the composite call identity, the two excerpt flags — fail to absent, never to
a placeholder. A delivery that arrives late duplicates nothing: the
frame names the ask turn, and pull remains the confirmation path.

## Journey test plan

1. **Red-first, the genuinely absent facts.** A top-level session posts
   `ask_user` and ends its turn. Red: the discovery row carries
   `AskPending` and `PendingQuestion` (existing behavior, asserted) but
   no `attentionLevel` (today's absence), and the excerpt carries no
   ask facts (no composite call identity, no pending-set identity, no
   `complete`, no `truncated`); a held roster watch's
   `needs_you` frame carries flags but no question text, no ask turn
   id, no instance identity; and the relay's identity arguments are
   refused pre-dispatch today by the tool's closed schema
   (`managerSendDefinition`'s property set and `invokeHubInput`'s
   allowlist — grounding above), the same gate class spec 2
   documents. Green: all present, from one derivation.
2. **Append delivers (the real second-question case).** While Q1 is
   pending, the session posts a second `ask_user` in the same turn:
   the pending set grows (count 1→2, a new identity), `AskPending`
   and the level are unchanged. Red: no frame delivers (the broker's
   trigger set misses the change). Green: a frame delivers on the
   pending-set identity change (the spike's trigger machinery),
   carrying the first question's text, the first call's composite ask
   identity, count 2 and the new identity — and the
   published row's excerpt, count and identity all describe ONE set:
   the atomic snapshot's red is today's two-call sampling pairing
   Q1's excerpt with Q2's revision (grounding above), asserted by a
   deterministic sample-append-sample regression.
3. **Restart re-derivation, including the revision repeat.** Kill
   and restart a question-pending session's daemon:
   `SessionAwaiting` re-derives the same call set, and — the red — a
   SINGLE-ask restore re-mints the same revision value the
   pre-restart live append minted (grounding above), so the revision
   alone delivers nothing. Green: the next delivery fires on the
   spike's restart-surviving identity component and carries the same
   set identity once
   — delivered on re-derivation, not duplicated per text.
4. **Relay, on the real path.** The waiting session receives queued
   input carrying the human's answer; its next turn consumes it; the
   ask facts clear on the next pull read; the journey asserts the real
   queue/start path end to end, no mocked machinery — and, with two
   calls pending, asserts ONE admitted reply clears both.
5. **Sufficiency rule.** A frame with a complete, untruncated,
   single-call excerpt is relayed without a further read (asserted by
   contract test, with the fixture the ask machinery's comment says
   is rare); a truncated or detail-bearing excerpt (incomplete) names
   the ask turn id, and fetching that turn — via spec 2's exact
   `{turn: id}` window when landed, via a newest-turns read otherwise
   — yields the full ask before the relay. The multi-turn set case:
   two pending asks in TWO turns require fetching both — the frame's
   composite list addresses each; the truncated-list case (a set
   larger than the frame's K-composite bound, named by the list's
   own `truncated-list` marker — computed from the entry's
   carried `CompositeTotal` against the list's length, never by
   the count) drives the
   paged pending-ask read THROUGH THE MANAGER TOOL EDGE — a
   `job_status` call carrying the page keys, the grammar's schema,
   decoder and dispatch hops asserted (scope 5's route) — page by
   page, pair-fenced, iterated
   until the page's `nextOffset` is empty (`total`, a composite
   count, names the set size for visibility; the frame's question
   count never drives completion — different units). The MIXED-set
   case: composites paged to `nextOffset` empty, the call-less
   count matching on the frame and every page response, and the
   call-less half found by the content-locate fallback with the
   count as its completion bound. The MAX-PAGE fixture measures
   the WHOLE Manager result, not the page alone: a large
   underlying set (at least the 4,000-composite population), the
   CLAMPED row and the page together, measured under the 256KB
   whole-response cap at both id forms — asserting the clamped
   row's `Calls` never carries more than the frame's K composites
   and the normalized limit's whole result fits the cap — and the
   frame's own max-case fixture builds K+1 composites at the real
   encoding and records the measured whole ROSTER SEGMENT against
   the segment's chosen bound — the asking lane's
   K+1-composite row beside the segment's other populations,
   with the frame body and the roster segment measured
   separately, never folded — while the segment's
   boundary fixture pins the whole-entry truncation marker at
   its chosen bound, including the COALESCING case: two pending
   batches plus an already-truncated predecessor merge to a
   segment asserted within the bound, the merged
   `roster_dropped` count the SUM of the inputs' counts plus
   the merge's own drops — and the payload asserted to CARRY
   the new entry fields (the composite list, the total, the
   count, the pair, `RosterInstanceID`, `ID`), which today's
   seven-field whitelist
   drops. And a page
   whose pair no longer matches the set fails the
   fence typed (the set changed mid-fetch; the caller re-reads the
   frame); against a daemon without the capability bit, the
    frame's truncated-list marker carries `unavailable` in its
    declared `listReason` field, a page
   request through the tool fails the typed hub-side refusal with
   the fallback named, and no page is served. Both landing orders are
   exercised.
6. **Relay identity.** A Manager holding only a frame (a
   lane it never read) relays an answer using the frame's instance
   identity through `delegate_send`'s `expected_instance_id`; a Clear
   racing the relay fails the identity check rather than answering a
   stranger. The fence journeys are inherited from the spike
   (`specs/manager-tools/fence-spike.md`'s red-test list, on this
   branch): the admission-refusal red (a stale-pair queue/steer
   returns the typed refusal receipt, the set untouched, nothing
   durable); the scoped-survival red (a fenced answer admitted
   against Q1 with Q2 appended mid-turn resolves Q1 and leaves Q2
   pending); the named-subtraction restore red (a resolving turn's
   CAPTURED names naming a subset of earlier pending calls leaves
   the unnamed asks pending across restore), with the existing
   `TestAskRestoreKeepsAsksPostedAfterADurableAnswer` pin as the
   anti-regression; the empty-set refusal; the steer-journal carry
   across a store reopen; the gate flip (a fence-bearing queue/steer
   passes the common prepare — today's gate misses it — while an
   ordinary text-only input never pays a row read, per the
   pair-bearing scope); and the older-daemon pre-dispatch refusal on
   all three
   modes. Plus the new journeys the lane owes: the delayed queued
   and steered answers racing an append (refused at acceptance, never
   at consumption), the restart collision (refused on the boot
   component), the append-delivers trigger, and the end-to-end relay
   on the real queue path. The round-8 evidence reds, one per fence
   finding: the same-turn composite restore (two asks in ONE turn —
   today's execution-granular answer cannot distinguish them; the
   captured names can); the post-admission append (an ask appended
   between admission and consumption is in NEITHER the captured list
   nor the durable names — live and restore agree, where today's
   consumption-time sample puts it in the durable names only); the
   three newly-routed clear sites (an append racing the
   steering-carrier, incorporated-replay and recovered-failure
   branches survives each); the drain/promote carry (a queued ask
   admitted before a drain transformation keeps its captured set
   through the REPLACEMENT record); the failure-writer names (a
   `TurnFailure`-with-`SteeringCarrier` resolving turn carries the
   captured names); and the wiring initialization (a session's boot
   generation is set before its first admission — a freshly
   constructed session admits against a real pair, never a zero
   value). Plus the round-9 owed journeys: the legacy-calls capture
   (a fenced answer against a call-less legacy question resolves it —
   the `legacyCount` half of the capture — live and across restore,
   the restore subtraction reading the resolving turn's field, which
   carries BOTH halves of the consumed capture;
   a mixed set resolves both halves; an all-zero capture against a
   nonempty set is the malformed-state invariant guard, INJECTED
   directly as an invariant test — the exhaustive capture makes it
   unreachable in a well-formed admission); the crash-cut
   writer (a scoped-survivor ask admitted before a pre-append store
   failure survives the recovery's `recordClientMutationFailure`
   resolve — the fourth evidence writer, its capture copied from the
   journal record onto the recovered user input); the steer-writer
   link/evidence split (a fenced steer's durable turn carries its
   capture as EVIDENCE — a post-admission append stays absent,
   where today's live sampling unions it in — while its
   `AnswerToTurnIDs` LINK excludes the active execution, the
   self-edge rule — with its EMPTY edge pinned: a fenced INLINE
   self-resolving steer's link list is empty, matching today's
   unfenced strip exactly, the suppression key correctly silent
   because no communicate delivery fires for the self-edge either;
   an UNFENCED steer keeps today's link behavior
   exactly — live names at claim, live-and-recorded union at
   delivery, active execution stripped — and the
   accepted-before-upgrade-delivered-after replay of a legacy steer
   record keeps today's link behavior too, named legacy); and the
   drain dominance (a drain combining a fenced and an
   unfenced entry resolves at the unfenced source's whole-set
   semantics, never weakening it).
7. **Crash honesty.** Kill the daemon of a question-pending session; the
   restart re-derives `SessionAwaiting`; the next discovery read still
   reports the ask facts. The derived visibility facts were never
   state; the fence's own durable state (journal preconditions, the
   resolving turn's captured names) is additive beside the ask
   markers and is covered by the replay-compatibility test
   (recovery section).
8. **Event-drop honesty.** With watch delivery suppressed, the discovery
   row still reports the ask facts and the question's text.
9. **Population boundary.** An archived, question-pending live session
   RETAINS its attention facts on rows (the pinned roster behavior
   flowing to rows); a remote lane covered by the roster carries them;
   a nested subagent row and a disk-index row carry none (absent,
   never zero); a child delegate needing attention shows `awaiting` in
   the parent's delegate diagnostics while the parent's own attention
   kind is unchanged (the pinned rule, asserted). An idle session, a
   working session and a tool-escalation-pending session each do NOT
   report `AskPending`.
10. **Skew tolerance.** With the broker's publish loop paused, a
    discovery read serves the row's own `AskPending: true` with an
    attention fact stamped `attentionAsOf` behind the read's own serve
    moment (and behind `builtAt` in a build where spec 1 has landed —
    conditional per README rule 6); the disagreement is visible and
    the row's own fact wins for detection.
11. **Older-daemon degradation.** Against a daemon predating the
    frame's new fields, the frame degrades to flags-only, never
    silence.

## Dependencies and interfaces

- Owns `attentionLevel` on discovery rows; the question text, the
  displayed first question's composite identity, count,
  `complete`/`truncated` flags and instance
  identity on the `needs_you` frame and the row's excerpt; the
  `attentionAsOf` moment (`AttentionEntry.publishedAt`); and the
  atomic ask snapshot accessor (the publication seam — one locked
  call, the mispair red). The pending-set identity carriers (row,
  frame and relay) and the
  relay fence are DESIGNED AND RED-TESTED by the fence spike
  (`specs/manager-tools/fence-spike.md`, landed on this branch): the
  pair fields on the `start`, `queue` and `steer` params (the
  `ExpectedQueueRevision` idiom, `appwire/types_turn.go:886`), the
  pair's wire carriers end to end (`PendingAskState.BootGeneration`
  minted beside `Revision`, the probe's row copy, `LiveEntry`,
  `AttentionEntry` and the frame — scope 2), the
  admission-transaction comparison and the captured-name scoped clear
  (`acceptClientMutation`, routed through EVERY answer-reachable
  clear site — scope 2's inventory, with the three unfenced
  exception classes named), the journal-record carry
  (`clientMutationPreconditions` — the record, never the queue entry)
  and the drain/promote inheritance through replacement records (the
  capture LIST and its dominance rule), the resolving turn's
  captured-names field (the new additive
  `schema.Turn` evidence, the failure writers and
  `recordClientMutationFailure` included, with the steer writers set
  from the record rather than live samples — the evidence field from
  the capture, the link field from the capture minus the active
  execution), the frame's K-bounded
  composite list with its own truncation marker and call-less count,
  and the paged
  pending-ask read (an additive page request on the ref-addressed
  thread read behind the `AskFence` bit, riding the `job_status` hub
  branch to the Manager tool; a server-normalized page
  bound held to a measurement obligation; its degradation named at
  both edges — the frame's `truncated-list` marker carrying its
  reason, and the typed hub-side refusal on a bit-less daemon), the session
  boot-generation field set by post-construction initialization, the
  `AskFence` capability bit behind one
  common prepare on all three modes, forwarded by `RemoteHubSource`'s
  rebuild, and the tool-schema extension (`managerSendDefinition`
  beside `invokeHubInput`); the spike's red-test list plus the
  round-8 and round-9 evidence reds (journey 6) are the
  implementer lane's starting corpus. The broker's pending-set change
  trigger is this spec's own design (scope 2), not the spike's.
  Spec 1 consumes the row field
  absent-when-not-landed.
- Consumes spec 1's `builtAt` only in the conditional skew journey
  (README rule 6); consumes spec 3's serving facts for the pull reads
  that report it;
  the sufficiency rule's ask-turn fetch uses spec 2's exact-turn read
  when that spec has landed (ordinary newest-turns reads otherwise) —
  asserted conditionally in both orders, never blocking.
- Rebases under 5641 and 5662 wherever they touch the same files and
  follows #5666's resolution if it changes event vocabulary.
- `docs/automation.md`'s roster (:132-142) and discovery contract lines
  are extended in this change, not replaced. New wire fields land with
  regenerated AppWire types and updated appwire-client validators in
  the same change.

## Acceptance criteria

- The journey is red pre-change on exactly the new row field, the
  frame's new payload, the append trigger and the excerpt
  flags, green
  post-change, with detection in one pull read, the question's own line
  in that read and in the event, the sufficiency rule stated in the
  guides and exercised in both landing orders, and the relay answerable
  through the documented path with an end-to-end behavior test on the
  real queue path.
- The re-entered fence machinery is implemented per scopes 2 and 5:
  the identity carriers, the trigger, the evidence field and its
  failure writers, the clear-site routing, the drain/promote
  inheritance and the wiring initialization, with the inherited
  corpus — the spike's red-test list plus the round-8 evidence reds
  (journey 6) — green on the lane, and `fence-spike.md`'s inheritance
  list followed (which reds retire as placement refutations, which
  characterizations flip, which new journeys the lane owed).
- The relay contract text lands in `docs/automation.md`, the
  session-activity guide and the Manager prompt template in the same
  change.
- The web and phone attention badges continue to work unchanged.
- The PR description names the older-daemon direction (flags-only frame
  degradation; absent row fields) and its removal condition.

## Alternatives considered

- **Watch every lane's transcript from the Manager.** Rejected: tonight's
  O(N) archaeology in formalized form.
- **A dedicated inter-session question queue.** Rejected: the durable
  pending-ask state already exists; a second queue would create two
  sources of truth for one fact.
- **A resolution event.** Deferred: pull confirms; push resolution
  optimizes the channel whose trust PR 5662 is rebuilding.
- **Relying on the existing needs_you event alone.** Rejected after
  grounding: flags-only payloads force a second read per question, a
  dropped event currently leaves pull unable to say who needs the
  human, and the change-detection gap means even a working watch
  misses an appended question. The row's attention facts close the
  first two; the call-set trigger closes the third.
- **Adding `needsResponse` or a `questionSummary` row field.** Rejected
  after grounding: the row already carries `PendingQuestion` at the
  same 200-rune bound (a second spelling violates
  one-owner-per-fact), and `needsResponse`'s TESTED meaning — false
  exactly when a question is pending — is the opposite of what a
  relaying Manager needs; the row carries the components and the
  guides state the decision rule instead.
- **Excluding archived or remote lanes from the row population.**
  Rejected in round 3: the Manager's roster deliberately includes them
  ("Archiving clears human attention, not the Manager's live
  population", pinned by test); a narrower row population would be a
  silent product decision contradicting the pinned behavior.
- **A text-hash fallback for question identity.** Rejected in round 3:
  the daemon already serves the exact per-call identity; a hash would
  miss detail-only changes and collide on repeated text, and older
  daemons already have a defined degradation path.
- **Direct relay on any untruncated excerpt.** Rejected in round 3:
  the excerpt structurally drops option details, recommendations,
  `why` and `if_unanswered`, and the ask machinery's own comment says
  it is insufficient for an external responder; `complete` is computed
  where the original exists, and the default is fetch-before-answer.
