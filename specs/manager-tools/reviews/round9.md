# Round 9 — reconciliation and disposition

Reviewed at commit `3fe6e35a04` (the round-8 revision pass), code base
`2be411cd8f`. Seats — the same three round-8 families re-tread with
closure audits of their own findings plus a fresh-attack competition, all
leaves, high effort, all three ran the spike's red tests themselves
(every red failed for exactly the documented reason, zero disputed):

- **R** `codex-jesse-fsck.com/gpt-5.6` (dlg_034tTOFQJyrVwPXbFPJxIv),
  run 13:22–13:37: 6 significant + 2 minor; 11 of its 13 round-8
  closures CLOSED, 2 PARTIAL.
- **S** `codex-jesse-fsck.com/gpt-6.1-sol-1m`
  (dlg_034tTOHL9TkdfoaH0udAZG), run 13:22–13:38: 6 significant;
  5 of 8 CLOSED, 3 PARTIAL.
- **T** `lunaroute/deepseek-4.1-flash-background`
  (dlg_034tTOIe9w1MSEzqE1T9Sj), run 13:22–13:44: 5 significant +
  7 minor; 9 of 11 CLOSED, 2 PARTIAL.

Tally: **17 significant + 9 minor**, no spec ready. Every load-bearing
claim was re-verified in-tree by me at the exact hop named. The closure
audits held well across the board: **25 of the 28 round-8 findings are
verified CLOSED by the seats that found them** — the fence evidence
redesign is confirmed expressible in the real structures
(`deriveRestoredAskPending` can subtract composites;
`roundEntryResolvesAskBoundary` can read captured names), the clear-site
inventory is confirmed reachable with `queuedIdentity` in scope at every
site, the post-construction wiring is confirmed window-free on the
fresh/restore/Clear paths (the constructor returns before any
admission), and the serializer-never-waits fact was re-confirmed at four
code sites. What broke is concentrated in the NEW text the round-8 pass
added, plus four deeper code facts no prior round had surfaced.

## The convergence classes

- **The old-client migration claim is false on the primary seam
  (R4 = S5 = T2 — all three seats).** "An older client against the new
  hub sees false-on-saved and conservatively retains" cannot happen on
  the failed-live seam: `identityDisposition` compares boot generation
  BEFORE any `authoritative` consultation
  (`transcriptHistory.ts:770-779`), a daemonless fallback page against a
  held numeric generation is REPLACE (`bootGeneration.ts:29-36`), so the
  newer held live history is erased regardless of the stamp. And the
  stamp flip breaks the normal daemonless row in the opposite direction
  (same-identity merge + `authoritative: false` RETAINS in-window held
  items where the pinned daemonless test requires replace). My
  round-8-pass error: I verified the "safe direction" against the
  daemonless-pinned test only, not the failed-live replace path.
- **The fetch cursor's owner is contradictory and hub-served elisions
  are unfetchable (R3 = S6 = T1 — all three).** The cursor is
  hub-minted by encoding (hub-owned identity,
  `internal/appitempaging/cursor.go:19-65`) while the grammar routes it
  to the daemon's `ThreadReadParams` behind the capability bit on the
  premise "only ever minted by a bit-advertising daemon" — false: the
  same packer runs hub-side for `appliedBy: hub-compat` (an old daemon
  that strict-decodes no such field) and `appliedBy: saved-index` (no
  daemon at all). Two of the three serving arrangements mint recovery
  instructions their own request contract cannot fulfill.
- **The routing/display split (R1 = T3).** `ListThreads` iterates
  `listedEntries` — "preserves confirmed incompatible owners FOR
  DISPLAY, without admitting them to the live RPC routes selected by
  `liveEntries`" (`local_daemon.go:1443-1444`). My revision wrongly
  routed the display list through the route predicate, which would
  DELETE the crashed row the spec exists to add. `liveEntries`' real
  callers are `localEntryForRefMode` (`:1410`, with the
  read-only-alias parameterization) and `ListModels` (`:1022`); binding
  and publication iterate raw entries.
- **The full-set continuation is neither bounded nor defined
  (R5 ≈ S2).** "The row's own `PendingAsk` state" is one unpaginated
  array the probe drops today, and at the boundary it cannot arrive at
  all: 4,000 composites with the real JSON tags ≈ 308KB against the
  262,144-byte whole-read cap. No request, cursor, fence, bound, or
  landing zone is specified.

## Seat-unique significant findings, all verified

- **R2 (spec 2):** the fetch grammar still does not decide `ref`
  (required vs cursor-derived), the legacy history `cursor`, or the job
  branch fields (`jobId`, `maxBytes`, `beforeBytes`); the Manager
  schema has mutually exclusive history/job branches
  (`session_tools_hub.go:73-89`) and dispatch parses `ref` before
  routing (`:705-730`). An implementer cannot build the strict anyOf.
  (T12's minor converges: the existing `cursor` is in no refusal list.)
- **R6 (spec 4):** a drain transformation can combine a fenced and an
  unfenced queued entry into ONE replacement record
  (`session_client_mutation_queue.go:879-920`); marking the result
  fenced or unfenced violates one source's contract, and the specified
  precondition shape is singular.
- **S1 (spec 4):** the evidence-writer inventory misses
  `recordClientMutationFailure` — the recovered pre-append-failure
  branch persists its own resolving `TurnUserInput` directly
  (`session_client_mutation_queue.go:1694-1715`, written before the
  normal consumption assignment at `session_lifecycle.go:2331`), so a
  scoped live clear can sit beside a resolving turn with no scoped
  evidence — the exact live/restore disagreement the redesign exists
  to eliminate.
- **S3 (spec 2):** my 10KiB ceiling is false AGAIN, against the real
  cursor encoding this time: 40 minimal elision lines using the existing
  identity+position encoding measure 10,381 bytes (`internal/
  appitempaging/cursor.go:25-53`), and that is before serving facts,
  aggregates, or continuations. Second arithmetic error on this
  ceiling (round 7: 4KiB; round 9: 10KiB).
- **S4 (spec 4):** the fence cannot express a supported legacy
  call-less question: `PendingAskCalls` skips questions without both
  ids (`session_tools_ask.go:101-110`, pinned by
  `TestVoicePendingAsk_LegacyRestoreStaysTextOnly`), so a fenced
  admission against a call-less (or mixed) set captures nothing for the
  legacy question — the scoped clear is a no-op and the question stays
  pending after its answer is durable, or an empty-capture degenerates
  to whole-set and reintroduces the append-loss defect.
- **T4 (spec 4):** the pending-set PAIR has no carrier end to end:
  `PendingAskState` carries `Version/InstanceID/Revision/Calls` — no
  boot generation (`appwire/types_pending_ask.go:6-17`); the probe
  drops even `Revision`/`Calls` (`prober.go:171-196`);
  `AttentionEntry` and the `needs_you` frame are flags-only. The
  trigger's comparison keys and the relay's pair both need what this
  path cannot deliver; the restart-delivery contract is unimplementable
  as written.
- **T5 (spec 4 / fence-spike):** "sole resolution evidence" does not
  dispose of the EXISTING live-sampled `AnswerToTurnIDs` writers:
  `claimSteeringCarrier` samples `pendingAskTurnIDs()` at claim
  (`session_client_mutation_queue.go:400-418`) and
  `deliverSteering` re-samples at delivery, unioning into the durable
  turn (`session_queue.go:1005-1019`) — so a fenced steer turn still
  durably names post-admission appends through the old field, and
  `job_watch_hub.go:420` keys its suppression on it.

## Verified minors

- **R7:** contract 3 (`:509-510`) and journey 7 (`:666-667`) still say
  "no new state" — stale beside the rewritten recovery section.
- **R8:** fence-spike §4's heading still says "reading the row
  unconditionally" while its body scopes to pair-bearing requests.
- **T6 (= T9 partial):** spec 2's scope 6 "exactly three shapes, one
  rule" still attaches `fetchCursor` to truncated/budget-elided items
  with no settled carve-out — the version-0 hole survives in the rule
  text while contract 5 has the carve-out.
- **T7:** the `Sources` per-source `state` is missing from spec 1's
  wire-schema obligation and the README's field list.
- **T8:** fence-spike §2's "today all but the first two wipe the whole
  set" is inaccurate (all five named sites call whole-set clears
  today), and the direct-user-input branch at
  `session_lifecycle.go:2352` is a SIXTH answer-reachable whole-set
  clear — inherently unfenced (no mutation record exists to carry a
  pair) and therefore needing its own named exception class.
- **T9:** the envelope frontier's facet/event mapping is not named —
  `facetsByEvent` has no execution row and `ThreadEnvelopeSource` has
  no frontier method; both are the landing zone.
- **T10:** the scoped primitive cannot be called at the carrier-entry
  site as written — `s.mu` is already held there
  (`session_lifecycle.go:1543-1574`); the primitive needs a
  `...Locked` sibling.
- **T11:** `attentionAsOf` is a second new attention row field,
  contradicting README rule 6's "the only new attention row field" and
  spec 1's "spec 4 adds only `attentionLevel`".
- **T12:** a fetch carrying the existing paging `cursor` is neither
  refused nor defined.

## What survived (all three seats)

The closure audits are the round's strongest result: 25 of 28 round-8
findings verified CLOSED by their own finders, including every fence
core item — the evidence redesign (T1: "expressible in the real
structure"), the clear-site routing (T2: "each site has queuedIdentity
in scope"), the post-construction wiring (T5: "no unavoidable
root-session exposure window"), the capture placement (T10: confirmed
at four code sites), the prepare scoping (T11), the conditionals (T4),
the frontier envelope sourcing (T7), and spec 1's field repoint (T8).
S marks spec 1 READY in its fresh state (its two findings are the
eligibility conflation R found too). The spike's reds remain undisputed
across all nine rounds.

## Disposition — one surgical revision pass, then round 10

Round 9's findings split into (a) errors in my own round-8 revision text
and (b) four deeper code facts the process had not yet surfaced (the
pair carrier, the steer `AnswerToTurnIDs` writers, the fourth failure
writer, the legacy-calls hole). Every one has a decided fix. The pass
rule that addresses the round's sharpest lesson — three of my own
revision-pass claims were false — is: **every new numeric claim is
measured or marked as a measurement obligation, never asserted; every
new "X uses Y" claim is opened at the hop before it is written.**

Decided fixes:

1. **The old-method freeze (three-seat convergence).** The legacy
   method keeps TODAY's stamping and behavior byte-for-byte:
   true-on-saved stays (the pinned daemonless reducer test stays green;
   old clients' failed-live replace and daemonless replace are TODAY's
   behavior, stated honestly as the known cost, with the removal
   condition tied to the old method's retirement). The corrected
   stamping, the fallback-marked identity exemption, and the serving
   facts live ONLY on the new method. The "conservatively retains"
   claim is withdrawn; the migration states plainly which old-client
   behavior remains on each seam.
2. **The fetch cursor is hub-owned, and the fetch is hub-resolved.**
   Delete the "only ever minted by a bit-advertising daemon" premise.
   The cursor carries its ORIGIN (serving mode); the hub decodes its
   own cursor and routes: a serving-daemon item fetches through a
   bit-advertised single-item read; a hub-compat item resolves
   hub-side from the held page; a saved-index item resolves from the
   index. All three `appliedBy` arrangements gain a defined fetch
   path, and the compatibility journey covers a settled old-daemon
   truncation end to end.
3. **The display/routing split named.** `ListThreads` keeps
   `listedEntries` (display, crashed row included via the projection);
   the ONE routing predicate is consumed by `localEntryForRefMode`,
   `ListModels`, `SessionToolsThread`, and `RunSessionTools`, with the
   read-only-alias purpose as its parameter.
4. **A bounded pending-set continuation.** The frame's composite list
   is bounded (K composites); the overflow path is a paged pending-ask
   read on the row's own source — request {ref, pair, offset, limit},
   response {composites, total, nextOffset} — pair-fenced, bounded per
   page, with the landing zone named (the thread-row read the probe
   already serves). The 308KB whole-array path is gone.
5. **The envelope burst bounded by construction, then measured.** No
   asserted ceiling: per-item elision lines are capped at K per
   response; the overflow rolls into the existing aggregate record
   with the same-view continuation (every item still reconstructable).
   The max-case fixture measures the K-line burst with the REAL cursor
   encoding, and the acceptance states floor + envelope from that
   measurement — the number is an obligation, not a claim.
6. **The pair's carrier designed end to end.** `PendingAskState` gains
   `BootGeneration` (additive, minted beside `Revision` in
   `PendingAskCalls` under the same lock); the projection clones it
   with the state it already clones; `ProbeResultFromThread`,
   `LiveEntry`, `AttentionEntry`, and the `needs_you` frame carry the
   pair; the relay params forward it. AppWire regeneration and
   validator obligations named.
7. **The old field's disposition stated.** The new captured-calls field
   is authoritative for resolution. The steer writers
   (`claimSteeringCarrier`, `deliverSteering`) set both fields from the
   record's captured list, never live samples; `AnswerToTurnIDs` on
   resolving turns is narrowed to the captured executions
   (job_watch_hub's suppression key keeps working on it); legacy turns
   keep whatever they carry, named.
8. **The fourth writer and the sixth clear inventoried.**
   `recordClientMutationFailure` copies the capture from its journal
   record onto the recovered user input (scoped-survivor restore red at
   the crash cut); the direct-user-input branch (`:2352`) is named as
   the third whole-set exception class — inherently unfenced (no
   mutation record exists to carry a pair) — beside interrupt and
   close, and the fence-spike inventory sentence states today's code
   accurately (all five sites whole-set today).
9. **The legacy-calls representation.** The capture records
   `(calls, legacyCount)` — call-less questions present at admission
   are counted; the scoped clear removes the named calls AND that many
   call-less questions; the durable evidence carries the same pair, and
   restore subtracts likewise. An all-zero capture against a nonempty
   set is the typed empty-set refusal. The mixed case resolves
   naturally; legacy-only and mixed-set live/restore tests owed.
10. **The multi-capture shape and the dominance rule.** Replacement
    records carry a LIST of captures; the rule: all-fenced sources →
    union captured sets, scoped clear; ANY unfenced source → whole-set
    clear (today's semantics — the fence never weakens an unfenced
    input's contract). The singular precondition shape becomes a list.
11. **The grammar completed.** The fetch branch: `ref` required,
    `fetchCursor` routed FIRST (before history/job), and the refusal
    list gains the legacy fields (`cursor`, `jobId`, `maxBytes`,
    `beforeBytes`) beside the windowed vocabulary — no combination
    undefined.
12. **The minors.** R7's stale sentences; R8's heading; the scope-6
    settled carve-out in shapes (a)/(b); the `Sources` state in the
    wire obligation and README list; the frontier facet/event and
    source method named; the `...Locked` sibling for the carrier-entry
    site; `attentionAsOf` added to the owner lists (it IS a row field,
    spec 4 owns it beside `attentionLevel`); the frontier landing zone
    and the R5-continuation both stated as measurements or owned work.

README: round-9 trail; the owner list corrected. Round 10: the same
three seats, closure audit plus fresh attack — the pattern is working
(their closure audits found my errors faster than fresh eyes did).

## Readiness after this round

No spec ready. Spec 1 is closest: one grounding fix (the display/routing
split) plus the wire-obligation omission, both decided. The fence core
remains settled; the round's fence findings are carriers, inventory,
and field dispositions — named work with decided shapes, not design
breaks.
