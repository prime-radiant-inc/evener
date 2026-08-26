# Session Rail Re-land Reconciliation

**Issue:** [#338](https://github.com/prime-radiant-inc/evener/issues/338)  
**Related contracts:** [#448](https://github.com/prime-radiant-inc/evener/issues/448), [#450](https://github.com/prime-radiant-inc/evener/issues/450), [#451](https://github.com/prime-radiant-inc/evener/issues/451), [#457](https://github.com/prime-radiant-inc/evener/issues/457), and retained-history integrity in [#449](https://github.com/prime-radiant-inc/evener/issues/449)  
**Status:** approved design/spec reconciliation; no runtime implementation  
**Base audited:** `da9146ce11502077bc20370cbc739e9f0230ae1e`  
**Date:** 2026-08-26

## Scope and non-claim

This document reconciles the approved Session Rail product contract with the
validated findings from the reverted implementation and its follow-up issues.
It defines contracts and re-land gates; it does not implement them. This PR
must contain documentation only, must use `Refs #338`, and must not close the
epic or imply that Session Rail is shipped. Later runtime PRs close only their
concrete child issues. #338 remains open until the end-to-end product contract
is implemented and verified.

The `wip/session-rail` reference and the commits reverted by `8fec1b124` are
evidence of interaction intent, not authority to silently promote experimental
behavior to a decision. Where this document changes or constrains the prior
spec, the change is called out explicitly below.

## Source-to-decision map

| Source | Validated finding used here |
| --- | --- |
| #338 marked triage comment (2026-08-26) | One design/spec PR; reconcile locked decisions; define global history, unloaded prefix, completeness/degraded behavior, dependencies, and phased gates. |
| `docs/superpowers/specs/2026-08-22-session-rail-design.md` | Original locked live-faithful behavior, responsive bands, both transcript panes, desktop flag, and P0–P4 product shape. |
| `8fec1b124` and its #338 ancestry | The complete P0–P4 implementation was deliberately reverted; current base has no rail runtime, RPC, tree field, or setting. |
| #448 marked comment | Projection limits do not bound journal acquisition. Scanning needs context, raw byte/event limits, cancellation between records, one shared delegate index per root, and traversal budget before later descendants. |
| #450 marked comment | Activity results are bounded pages, not implicitly complete history. Continuations require revision identity, typed restart on mismatch, diagnostics preservation, stable dedupe, and global ordering. |
| #451 marked comment | A one-shot ref cache, swallowed errors, and stale job rows are insufficient. Reuse revision/hydration/coalescing machinery; isolate job degradation from valid turns. |
| #457 marked comment and `c55c8b589` | Full-count rows were an unloaded older prefix but were indexed as a loaded prefix and rendered as zero-height `null`; this caused the measured virtualizer loop. Use global tail indices and fixed-height placeholders. |
| #449 issue | Owner records beat forwarded copies; fallback, torn tails, lifecycle corruption, and descendant damage need observable integrity status. |

## Decision ledger

### Unchanged (locked)

These decisions remain product requirements from #338:

- The rail is a scrollbar replacement in the live and read-only transcript
  panes, with a 156px desktop/full encoding rail.
- Live-faithful rendering is causal: no event, anchor, gap, total, or END cap
  is rendered before its timestamp is knowable. Ended sessions use their end as
  `now`, never a hidden future denominator.
- The time axis is `[session start, max(now, start + 10 minutes)]`, continuously
  rescaled; turn-index mode normalizes only by turns revealed so far.
- The rail reports “so far” totals and never `current / final` denominators.
- Phones below 560px receive a functional 40px minimal strip (burn line,
  errors, anchors, and drag), not a hidden rail.
- Read-only transcript panes receive the rail in v1.
- `last_activity_at` is a real tree-node field, not an `updated_at` proxy.
- The desktop `settings.rail` flag defaults on.
- Comprehension View keeps the parent leftmost, orders descendants by recent
  activity with 60-second hysteresis, uses one aligned live clock, supports
  horizontal overflow, and can click through to an exact session and turn.
- Theme colors come from resolved design tokens; no new color literals are
  introduced.

### Revised or clarified (explicit changes to the prior spec)

1. **“Full-history rail summary” means a full-history *attempt*, not an
   unqualified complete result.** The summary may contain a bounded page,
   continuation, unavailable branch, scanner refusal, torn tail, or damaged
   descendant. A consumer must carry this state and must not call it complete.
2. **`JobActivityTree` is one bounded page.** A complete consumer follows
   continuations under one snapshot revision and bounded request budget, or
   returns an explicit incomplete/degraded result. It never infers completeness
   from an empty tail or from the absence of an error.
3. **Snapshot identity is mandatory.** A continuation is valid only for its
   captured revision (or an explicitly supplied expected revision). A revision
   mismatch returns a typed restart outcome; it is never merged by taking a
   maximum revision.
4. **Global ordering is separate from tree traversal.** Flattened job lanes are
   stable-sorted by `StartedAt`, then `JobID` (with stable identity dedupe).
   Parent-first traversal remains useful for tree structure but is not a
   chronological timeline.
5. **Resource limits apply while acquiring data.** Read-only, context-aware
   streaming scanners enforce raw-byte and record/event ceilings, check
   cancellation between records, and spend traversal budget before opening
   later descendants. The existing valid-log order and in-flight trailing
   record rule remain unchanged.
6. **Global turn history is a suffix-window model.** If `fullTurnCount = N`
   and the loaded ordered suffix has `L` turns, its global prefix is
   `N - L`. Turn `i` in the loaded suffix has global index `N - L + i`;
   indices below `N - L` are unloaded. Prepending a page decreases the prefix
   and does not renumber already-loaded turn identities.
7. **Every measured unloaded row has geometry.** An unloaded prefix row is an
   `aria-hidden` placeholder with the exact `estimateSize` (96px in the
   validated branch finding), never `null` or a zero-height measured node.
   Initial end positioning targets global index `N - 1`. Full-count inflation is
   not allowed for filtered/focused views unless their unloaded projection is
   separately defined.
8. **Job state is a separate freshness lane.** Job-only failure or incomplete
   retained history preserves valid turn summary. It produces a quiet,
   accessible degraded-job indicator and diagnostic state rather than being
   swallowed or turning the entire rail blank.

These are contract clarifications/revisions, not claims that any code has
landed.

### Deferred (not silently decided here)

- Exact wire field names and compatibility policy for opaque v1 continuation
  tokens (the implementation PR must choose and document migration/restart
  behavior).
- Exact raw-byte, record, response, and traversal budget values. They must be
  bounded and measured in #448 runtime work; this spec does not invent limits.
- Whether the summary endpoint can provide every ended-session job interval in
  one response after applying those bounds. If not, it must expose the same
  continuation/degraded contract rather than fake completeness.
- Variable-height correction for unloaded placeholders after hydration. The
  96px estimate is the safe initial geometry; the virtualizer acceptance gate
  must define how replacement preserves key identity and visual anchoring.
- A full-history representation for filtered/focused views.
- Exact visual copy and placement of the degraded indicator, subject to the
  accessibility gate.
- Whether a later performance optimization uses a persisted index. It must not
  weaken the observable limits or revision contract.

## Data contracts

### Full-history and global-index contract

The rail's turn axis and the transcript's window are different coordinate
systems. A summary may know `N`, while the transcript has only a loaded suffix.
The model must expose at least:

- `fullTurnCount` and the snapshot/revision that established it;
- ordered loaded suffix turns with stable turn identity;
- `loadedPrefixCount = fullTurnCount - loadedSuffix.length`;
- whether the prefix is unloaded, unavailable, or known empty; and
- a completeness status independent of the count.

For an unfiltered transcript, the prefix is older history. A prepend fills rows
below the prefix and reduces it; it does not append placeholders after the
loaded suffix. The rail remains authoritative for turn-index mapping, while
DOM scroll pixels remain approximate while placeholders stand in for variable
height. Anchor navigation must request older pages before scrolling when the
target is outside the loaded window.

A count alone is not proof that every row can be loaded. A consumer that cannot
obtain the prefix within its bounded request must retain the known global
indices, show the unavailable/incomplete state, and never relabel the visible
suffix as complete history.

### Job activity, continuation, and ordering contract

Each job page carries its snapshot revision, branch diagnostics, completeness
counts/status, and continuation (when more data exists). A continuation walk:

1. starts with one revision and a bounded acquisition/traversal budget;
2. follows only continuations for that revision;
3. deduplicates by stable job identity, preferring the authoritative owner
   record over forwarded copies;
4. preserves branch errors, scanner diagnostics, torn-tail state, and
   unavailable-owner fallback; and
5. stable-sorts any flattened timeline by `StartedAt`, then `JobID`.

A changing revision causes a typed restart response. It cannot combine pages,
replace the current revision with `max(current, patch)`, or overwrite
root-authoritative counts with continuation-local counts. A complete result is
permitted only when every required branch is complete and no diagnostics mark
unresolved damage. “No entries” is compatible with either complete-empty or
incomplete-unavailable; the status distinguishes them.

### Completeness and degraded-state contract

Use separate dimensions rather than one overloaded error bit:

- **complete:** all requested branches and pages were acquired at one revision,
  no relevant scanner or integrity diagnostics remain, and the result is safe
  to describe as complete;
- **incomplete:** a bounded page/continuation, cancellation, limit refusal,
  unavailable owner, torn tail, or recoverable descendant damage prevents a
  complete claim; retained rows remain usable and the cause is observable;
- **degraded:** the result can render useful data but one lane or branch is
  damaged/unavailable; the UI exposes this state; and
- **fatal:** the root/session summary cannot be trusted or returned. No
  misleading partial root result is presented as complete.

A job-only fatal/degraded result must not erase valid turn data. A fatal root
turn-summary failure may leave the transcript usable but must not fabricate rail
ink. Descendant damage is recoverable only when the root and unaffected branches
remain trustworthy; the affected branch is marked incomplete. Owner-authoritative
records replace forwarded copies; forwarded records are fallback only when the
owner is unavailable and that fallback is incomplete.

### Live refresh contract

For live sessions, job state is revisioned. Matching `jobs/treeUpdated`
notifications invalidate/refetch the job lane; reconnect also invalidates it.
Burst notifications coalesce, and requests do not overlap. Stable ended-session
results remain cached at their terminal revision. A stale response cannot
replace a newer hydration generation. No fixed polling is required by this
contract. The turn lane and job lane can succeed or degrade independently.

## Dependency and re-land matrix

| Order | Runtime child slice | Depends on | Must not claim |
| --- | --- | --- | --- |
| 1 | #448 bounded retained acquisition/scanners | Existing journal ordering and context plumbing | A bounded projection alone bounds input. |
| 2 | #450 revisioned bounded-page consumer contract | #448 limits; #449 integrity diagnostics | A page or tree traversal is complete history. |
| 3 | #451 live/degraded rail job lane | #450 contract and existing revision/hydration/coalescing machinery | One-shot cache or swallowed job errors are live fidelity. |
| 4 | #457 transcript global-prefix/placeholder and virtualizer boundary | Transcript paging model; rail full-count semantics | Zero-height/null placeholders, shared VirtualList changes, or disabled dynamic sizing are fixes. |
| 5 | #338 P0–P4 product slices | #448–#457 gates as mapped below | The spec PR or any single phase ships the epic. |

#459 heartbeat work is separate and already present at the audited base; it is
not a substitute for #448, #451, or #457. The rail re-land must keep the
heartbeat/socket boundary separate.

## Phased entry and exit gates

Every phase is a separately reviewable runtime PR. Entry means its dependencies
are merged and its acceptance fixtures are available. Exit requires the listed
focused tests plus applicable repository gates; no phase advances on a
swallowed warning or an incomplete result mislabeled green.

### P0 — foundations and contracts (no UI)

**Entry:** #448 acquisition design is accepted; revision/integrity fields from
#450/#449 are specified; fixtures include large journals, continuations,
changing revisions, torn tails, owner/forwarded conflicts, live and ended
sessions.

**Work:** bounded context-aware scanners; one shared delegate index per root;
revisioned bounded-page result; `last_activity_at`; typed usage; pure rail
model/axis/ordering. The model must represent incomplete/degraded states and
must never emit future events.

**Exit:** backend and pure-model tests prove raw/event/traversal bounds,
cancellation between records, revision mismatch restart, owner dedupe,
continuation walk, global job ordering, no-future-ink, live-faithful totals,
and parent/hysteresis ordering. No UI is mounted. `make test`, `make lint`, and
`make test-web` pass for the implementation change.

### P1 — rail scrollbar in both transcript panes

**Entry:** P0 exit; transcript paging and global suffix mapping are available;
#457's fixed-height placeholder regression is ready.

**Work:** rail mount, scroll synchronization, anchors, exact-target paging,
follow arbitration, desktop flag default-on; keep loaded turns at stable global
indices and use 96px `aria-hidden` placeholders for unloaded rows.

**Exit:** drag ratio is 1.000 in turn-index mode; anchors page and land exactly;
summary-before-page and page-before-summary both render; large ended sessions
survive automatic prepend with no maximum-depth error/error boundary; dynamic
variable-height measurement remains enabled; focused/read-only/live controls
pass; overflow and token-contract guards pass.

### P2 — responsive and accessible rail

**Entry:** P1 exit and geometry remains correct at the three container bands.

**Work:** container-query widths (>=900: 156px; 560–899: 96px; <560: 40px),
tooltips, keyboard targets, reduced motion, labels, and no horizontal overflow.

**Exit:** layout geometry at 1440/1024/768/390, tap-target, keyboard,
reduced-motion, aria, and overflow browser guards pass. The minimal phone strip
remains draggable and exposes the locked encodings.

### P3 — Comprehension View

**Entry:** P1/P2 and live/degraded job lane contract are green.

**Work:** overlay via the existing OverlayPanel pattern; refcounted parent and
subagent hydration; aligned shared clock; ordering/hysteresis/FLIP; exact
click-through.

**Exit:** overlay focus/Escape/aria-modal tests, parent-leftmost and hysteresis
unit tests, reconnect/live refresh tests, aligned now-line test, exact
session+turn navigation, and browser guards pass. Incomplete/degraded child
lanes remain visible and announced without erasing valid siblings.

### P4 — graduation

**Entry:** P0–P3 exit evidence is published and all known degraded paths have
honest UI copy.

**Work:** user-facing legend and settings copy only.

**Exit:** documentation links/style/generated checks pass; review confirms the
legend does not promise completeness where the runtime reports incomplete or
degraded. #338 may close only after a separate end-to-end release decision, not
from this spec PR.

## Design-review checklist

- [ ] Every original locked decision is marked unchanged, revised, or deferred.
- [ ] No experimental branch behavior is presented as shipped or silently made
      normative.
- [ ] Full history distinguishes global count, loaded suffix, unloaded prefix,
      unavailable prefix, and complete status.
- [ ] Job pages preserve revision, continuation, diagnostics, branch errors,
      owner-authoritative dedupe, and deterministic global ordering.
- [ ] No consumer labels a result complete while any required branch is
      incomplete.
- [ ] Job-only failure preserves valid turn summary and exposes an accessible
      degraded indicator.
- [ ] Resource limits apply during acquisition and cancellation is checked
      between records.
- [ ] Unloaded measured rows have fixed nonzero geometry; filtered/focused views
      are not inflated without a defined projection.
- [ ] #448, #450, #451, and #457 remain separate reviewable dependencies; #459
      heartbeat work is not folded into this contract.
- [ ] Later runtime PRs own their focused backend, unit, component, and browser
      evidence; this spec PR does not invent executable TDD or claim those tests
      ran.

## Verification for this documentation PR

The design PR's applicable verification is documentation/repository hygiene:

- `git diff --check` and link/style checks for the changed documentation;
- generated-document check if the changed path is covered by one;
- `make lint` and `make vet` as repository gates, with every warning/error and
  exit recorded;
- `make test` if the project gate covers documentation changes (it does not
  replace later runtime acceptance tests).

No production/runtime files, frontend components, RPCs, fixtures, or fake TDD
red/green claims belong in this PR.
