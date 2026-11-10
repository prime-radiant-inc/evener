# Round-4 PAR reconciliation record

Inputs reviewed at commit 0ef0d6b8b2 (base origin/main 2be411cd8f):
- Reviewer I, codex-jesse-fsck.com/gpt-5.6-terra: 2 significant.
- Reviewer J, kimi/k3: 1 significant + 3 minor (~40 citations verified,
  zero false; confirmed the round-3 mechanisms all trace).
- Reviewer K2, lunaroute/deepseek-4.1-flash-abliterated: 12 significant
  + 11 minor. Most legitimate significant findings; round winner. (The
  original third seat, stepfun/step-3.5-flash, died instantly on a dead
  provider subscription and was replaced.)

Every load-bearing new code claim below was re-verified in-tree before
acceptance. The round's findings are narrower than round 3's: no broken
premises, but real naming gaps (machinery the specs require without
naming), two internal contradictions, one preservation hole, and one
grounding rewrite that simplifies the design.

## New code facts verified this round (in-tree, this session)

1. The hub ALREADY packs transcript reads: `transcriptRPCResultSoftLimit
   = 1 << 20` (1MB) and `packItemCandidates`
   (`cmd/evener-hub/app_item_page_fit.go:14,87,107,122`) trim the oldest
   items until the encoded turns fit the soft limit, on both Manager
   read shapes (cursorless at `session_tools.go:609`, pages at
   `app_rpc.go:445`, saved at `app_threadread.go:230,287`), documented
   in `docs/product/history-content.md:32-37`. The defect is that the
   soft limit (1MB) sits ABOVE the Manager's 256KB whole-read cap
   (`session_reads.go:244-252` via `session_tools.go:399-401`), so
   Manager reads die at the cap before the packer helps.
2. Cursor views are selection-fenced: `contentCursor{Selection,
   Snapshot, Epoch, Before}` and `apphistory/paging.go`'s `Snapshot`
   binds "an opaque incarnation to its selected representation" — a
   selection-scoped view identity exists; the cursor encoding carries
   only a `before` bound (`internal/appitempaging/cursor.go:25-31`).
3. The turns/list page path fills thread identity from a SEPARATE
   `ReadThread` metadata call
   (`cmd/evener-hub/app_rpc.go:441-451`), not from the serving cut.
4. The missed-probe outcomes: `missReused` publishes `prev` with only
   `LifecycleFresh=false` (`hubcore/roster.go:855-866`); `missRetained`
   publishes no entry (no case in the publish switch at :564-579); a
   healthy daemon whose lifecycle sub-probe fails also gets
   `LifecycleFresh=false` (`prober.go:146-147,258-262`). `LifecycleFresh`
   therefore mislabels fresh rows and is not a probe-state source.
5. The pending set has a version: `askPendingRevision` increments on
   both restore (`setAskPendingLocked`,
   `agent/session_tools_ask.go:143-149`, via `agent/session_init.go`)
   and append (:163).
6. Legacy restored asks carry no call identity — text-only, pinned by
   `TestVoicePendingAsk_LegacyRestoreStaysTextOnly`
   (`agent/session_voice_pending_ask_test.go:336`) — on current
   daemons, not an older-daemon degradation.
7. The `ThreadStatus` vocabulary is idle/active/awaiting/warning/
   closed/notLoaded/systemError/restartRequired
   (`appwire/types_protocol.go:365-371`); `errored` is the hub's
   normalized state (`hubcore/tree.go:566-567`) and a client attention
   state, not a row `status.type`. `localDaemonThreadStatus` maps
   unknown values to idle
   (`cmd/evener-hub/internal/appsource/local_daemon.go:1659-1679`).
8. Two workspace-ref builders diverge on the empty-`WorkspaceRef`
   fallback: `localDaemonWorkspaceRef`
   (`internal/appsource/local_daemon.go:1637`, threadID-first) vs
   `localSpawnWorkspaceRef` (`app_threadlifecycle.go:395`,
   sessionID-first, used by the broker's identities at
   `main_background.go:98`).
9. The local source already separates display from routable entries:
   `ListThreads` reads `listedEntries` while `liveEntries` filters the
   routable set (`local_daemon.go:1432-1465`) — the existing seam for a
   crashed-entry routing filter.
10. Page candidates serve newest-first BY SELECTION but in
    chronological order (`SelectCandidates`,
    `internal/appitempaging/page.go:93-113`;
    `RegroupTurnFragments` preserves it; stated at
    `app_item_page_fit.go:179`).
11. `LocalDaemonEntry` has no `Activity` field; the roster→entry copy
    (`app_rpc.go:128-148`) does not carry it.
12. The merge loop enriches rows (`mergePastMetadataForList`,
    `applyHubCapabilities`, `app_threadlist.go:200-204`), so "rows pass
    unmodified" is false as written.

## Decisions

### Spec 1 (fleet snapshot)

- K2#1 ACCEPTED: the scope names the owned work explicitly —
  `hubThreadListWithSourceTimeout` RETURNS per-source outcomes to
  `discover` (the signature change is named work), and the fan-out's
  `allowed` set defines `skipped-unattached`.
- K2#2 ACCEPTED: the crashed row projects the EXISTING
  `status.type: systemError` (which hub and client normalize to
  `errored`) beside the `crashed` discriminator. No new ThreadStatus
  value; the roster's `errored` stays the roster's normalized state.
- K2#3 ACCEPTED: `probeState` is a roster-owned field set on the fresh
  path (`liveEntryFromProbe`) and on `missReused`/`missCrashed`
  (`retained`); `missRetained` entries are not listed; `LifecycleFresh`
  is explicitly not the source.
- K2#4 ACCEPTED: `Failure` is absent-when-none; journey 4 asserts
  `Failure` ABSENT for a mid-run kill (a mid-run lane's last row is
  `active` with no `Failure`; the crashed roster entry is the prior
  record with only status and the crashed flag set).
- K2#6 ACCEPTED (simplification): crashed entries are excluded from
  routing through the EXISTING routable/display split —
  `LocalDaemonEntry` gains the `Crashed` flag and `liveEntries` filters
  it; no per-route checks.
- K2#7 ACCEPTED: `LocalDaemonEntry` gains `Activity` and the roster→
  entry copy, named as landing zones.
- K2#8 ACCEPTED: "rows pass unmodified" corrected — the merge's
  metadata/capability enrichment applies (harmless to the ask facts).
- K2#5 ACCEPTED: citation fix (`session_tools.go:237-242` is the
  dispatch loop; the size check is `managerReadWithinCap` at
  `session_tools.go:399-401`).
- J4 ACCEPTED: contract 1 reads "working via `status: active`
  (`quietForMs` present only when no subagent runs, per the meter's
  own rule)" — `quietForMs` is not the working signal for
  subagent-running lanes.

### Spec 2 (cheap filtered reads)

- K2#9 ACCEPTED, grounding rewrite: the problem statement grounds on
  the EXISTING packer — the hub already trims reads to a 1MB soft
  limit, but that limit sits above the Manager's 256KB cap, so Manager
  reads fail whole before the packer helps. The owned work EXTENDS
  `packItemCandidates` (`app_item_page_fit.go`) with the window and
  Manager-budget semantics; the spec builds no second packer.
- I1 ACCEPTED, one elision rule table: served-but-truncated → per-item
  truncation marker + `fetchCursor`; selected-but-budget-elided →
  per-item elision line + `fetchCursor` (scope 5 unchanged);
  type-excluded or over-cap → the ONE aggregate intra-turn elision
  record (count + a continuation cursor positioned at the first
  withheld position, version-fenced like every fetch cursor). The
  aggregate record is explicitly exempt from the per-item rule.
- K2#10 ACCEPTED: the per-turn cap applies to `{turn: id}` reads too;
  "whole" is corrected to "bounded like any windowed read, with its
  continuation" — the locate case is about addressing, not wholeness.
- K2#11 ACCEPTED, one filter: in windowed mode the exact-type predicate
  is the ONLY filter; coarse content flags are translated into it at
  the tool edge (sugar), and `Selection.Items` is bypassed for
  filtering (its projection forms, like intent-only, remain). Mixed
  coarse+itemTypes requests need no precedence rule because there is
  one mechanism.
- K2#12 ACCEPTED: the windowed view identity (window + types + bounds)
  extends the existing selection-scoped snapshot incarnation
  (`apphistory/paging.go`); change-a-filter-mints-a-fresh-view carries
  over, and the extension is named owned work.
- K2#13 ACCEPTED: owned cursor forms named — an after-bound (forward)
  variant for `since` continuations, and the version-bearing
  single-item fence for fetches; both extend the existing
  identity-fenced encoding.
- K2#14 ACCEPTED: order stays chronological (today's behavior);
  "newest-first" claims are removed; the trim rule (withhold the
  oldest) is unchanged and the response says which end was trimmed.
- K2#15 ACCEPTED: the budget bounds window content; the 256KB cap
  bounds the OUTER response (envelope + follow-ups) and keeps today's
  refusal; acceptance asserts both (packed content ≤ budget; encoded
  outer response ≤ cap).
- J1 ACCEPTED: the capability gate's source is the probe-cached
  thread-row `Capabilities` per lane
  (`localDaemonEntriesFromRoster` carries them), with the staleness
  rule stated; the skew window (cache says the bit, daemon lacks it →
  strict-decode rejection) triggers ONE automatic hub-compat retry,
  visibly marked; the reverse skew is harmless hub-compat. Journey 10
  gains the probe-stale case.

### Spec 3 (transcript-status consistency)

- K2#16 ACCEPTED: every page shape stamps `instanceId` from the
  serving cut (owned wire work: the serving source carries it; the
  separate metadata read stays for display fields, not identity); the
  Clear race is tested on a cursor page.
- K2#17 ACCEPTED: `asOf` is the moment of the file stat that bounded
  this serve's catch-up (the covered-length moment), not the
  completion moment — entries appended during catch-up are not
  covered.
- J3 ACCEPTED: one sentence in the `asOf` clause and the
  `docs/automation.md` contract: `asOf` bounds file-completeness at
  serve time; content recency is judged by the frontier against the
  fleet picture, never by `asOf` freshness.
- K2#19 ACCEPTED: the wire reuses the existing field name
  (`latestExecutionTurnID`) on every shape; `frontierTurnId` is the
  spec's concept term, and the spec says so.
- K2#18 ACCEPTED: citation fix — the live turns handler is
  `handleAppThreadTurnsList` at `server/appwire_read_handlers.go:406`
  (`appwire_thread_turns.go` does not exist).

### Spec 4 (question surfacing)

- I2 ACCEPTED, the relay fence: the pending set's `Revision` is the
  relay precondition. The frame and row carry it (the broker already
  derives from the state that owns it); the relay request gains an
  optional `expectedAskRevision` through `invokeHubInput` (new wire
  param, additive); on mismatch the relay is REFUSED typed, naming the
  current pending set — never admitted as an answer that would clear a
  newer question. Required for direct relay AND fetch-then-relay
  (one parameter covers both, same call path). The race journey proves
  a delayed Q1 answer cannot clear a later Q2 on the same instance.
  `PendingAskState`'s own doc ("presentation hints, not a conditional
  answer API") stays true: the precondition refuses, it never
  conditionally reinterprets.
- K2#20 ACCEPTED: the delivery trigger gains `Revision` — it changes on
  restore and on append, so both deliver; the frame carries it (dual
  use: trigger and relay fence).
- K2#21 ACCEPTED: the ask turn id is absent-when the ask is call-less
  (legacy restored, pinned test); the fetch rule covers call-less asks
  via newest-turn reads; `Revision` (universal) is the relay fence,
  the ask turn id is for fetching.
- K2#23 ACCEPTED: the trigger is stated precisely: `Revision` change +
  the existing flag/level set; the frame's count is the question
  count.
- K2#24 ACCEPTED: the enrichment join uses ONE ref function — the
  roster's own `rosterIdentities` map (which already builds refs for
  local and remote), not a re-derivation through a second builder;
  unifying the builders is named owned work.
- K2#22 ACCEPTED: journey 10's `builtAt` comparison is conditional per
  README rule 6 (or asserts against the read's own moment).
- J2 ACCEPTED: `complete`/`truncated` are computed at the ask-posting
  site (`appendAskPendingLocked`/`setAskPendingLocked`), where the
  full schema content exists — named as the landing zone, not the
  excerpt machinery.
- K2#25 ACCEPTED: citation fix (the root-only gate is
  `isSubagentSession` at `session_tools_ask.go:52-63`).

### README

- Grounding summary updated: the existing packer and the
  cap-above-soft-limit defect; `systemError` row vocabulary; the
  `Revision` trigger and relay fence; the one-join-function rule;
  chronological order kept. Review trail gains round 4.

## Round 5 plan

The fix pass lands the above, then ONE more competing round with three
fresh models (never-seated: candidates gpt-6-luna, kimi-for-coding,
glm-5.3-flash — chosen at spawn time from live models) gates the four
implementer lanes: a round with no significant findings across all
three reviewers is Jesse's bar. If findings survive, iterate again.
