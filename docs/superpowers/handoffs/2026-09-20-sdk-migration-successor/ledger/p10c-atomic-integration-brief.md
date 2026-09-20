# P10c atomic integration brief

Status: preparation only. The P10c source remains frozen at
`c660aee9999ef0b0e80dcf53eac9d88e62a2a108` in
`/Users/jesse/.codex/worktrees/transcript-display-patch-preview/evener`.
Do not restack or edit P10c until P10b lands.

## Inputs and boundary

P10b's current head is
`7d7fa7292e33529341e84bd2d88558dfc9a0beaf` in
`/Users/jesse/.codex/worktrees/transcript-display-read-generation-p10b-reentrancy-fix/evener`.
Its `applyHubDefaults` implementation fixes the real synchronous reentrancy
bug by computing both layout acceptances, calling `fence.firstPayloadApplied()`,
and publishing `hub`, `loaded`, and `hubLoading` in one `setState`.

P10c cannot cherry-pick that function unchanged. P10c's direct-write preview
contract lives in `applyHubDefault`: every authoritative layout payload must
evaluate `previewBases` and `drafts`, and must clear a contradicted preview when
the preview base belongs to an older generation or the incoming revision is
higher than the preview base. A lower or equal revision in a new generation
still clears a contradiction because the generation changed. Calling
`applyHubDefault` twice from P10b's first GET would restore the reentrancy bug
and expose a partially applied hub before `loaded` is published.

## Smallest implementation

After P10b lands, extract only the pure per-layout calculation from
`applyHubDefault`; do not add a new store abstraction or duplicate the
algorithm. The calculation should receive:

- the current confirmed layout value (from a local `hub` copy, not live store
  reads);
- the incoming confirmed value;
- the active fence state needed for monotonic acceptance and first-payload
  restart semantics;
- the layout's preview base and draft.

It should return whether the value is accepted, the resulting layout value,
and whether that layout's preview must be removed. It must have no `setState`,
fence mutation, or subscriber-visible side effect.

Then use that calculation in both paths:

1. `applyHubDefault` calculates one layout and publishes its hub/preview/error
   change as it does today. Preserve its write-token and `stillMine()` checks
   around PATCH settlement and reentrant subscriber paths.
2. `applyHubDefaults` copies `hub` and `drafts` locally, calculates desktop and
   mobile from the same pre-publication snapshot, removes only the layouts
   whose previews are contradicted, calls `fence.firstPayloadApplied()`, and
   performs exactly one `setState` containing the resulting `hub`, `drafts`,
   `loaded: true`, and `hubLoading: false`. Delete corresponding
   `previewBases` entries only after the calculation has decided they are
   contradicted and before that one publish.

The first-read ordering must stay:

1. validate and calculate both layouts without publishing;
2. mark the fence's first payload applied;
3. publish both confirmed layouts, preview removals, `loaded`, and loading state
   together.

Do not call the single-layout helper from `applyHubDefaults`. Do not move
`successfulHubReads`, shared reconciliation reads, or the generation-scoped
reconciliation promise into the generic settings-hub module; those remain
P10c's write/recovery behavior. A successful authoritative GET must still be
counted only after the atomic publication, so internal PATCH recovery cannot
mistake cached fallback state for a committed write.

## Required preservation checks

Keep all 47 P10c tests meaningful, especially:

- newer and lower/equal-revision new-generation reads that contradict a
  stranded preview;
- a newer payload matching a stranded preview, which leaves it for the host;
- support flap, detach/dispose fencing, missed notification refresh, and
  current-value settlement for superseded PATCH replies;
- internal PATCH failure recovery through one shared authoritative GET,
  including the revision-advance requirement;
- subscriber retirement before PATCH dispatch and conflict settlement
  reentrancy.

Retain all P10b reentrancy regressions from the predecessor head:

- a first read publishes desktop and mobile atomically;
- a subscriber cannot observe `loaded` with only one layout updated;
- a relayed lower revision cannot roll back the first read;
- a refresh started synchronously during the loaded publication cannot replace
  the first read with stale data;
- retirement or a generation change during publication leaves the payload
  retired.

Add one integration regression if the existing combined tests do not already
cover it: start a stranded P10c preview, let the first authoritative GET carry
one contradictory layout and one matching layout, and assert one subscriber
publication clears only the contradictory draft while `loaded` is true and
both layouts are fully confirmed. This proves the extracted calculation is
used by the atomic publisher rather than bypassed.

## Scope and verification

The frozen P10c production delta is 178 additions and 4 deletions in
`appwire-client/typescript/transcriptDisplayStore.ts`; its test delta is 320
additions in the matching test file. The integration should remain a small
follow-up to P10b, with no new public API, compatibility layer, native adapter,
or server protocol change.

After P10b lands and the integration is applied, run the combined 47-test
P10c/P10b store suite plus the predecessor reentrancy regressions, Biome,
`make test-api-package`, and `make test-web`. Request a fresh exact-base
branch review after the resulting head; the old P10c receipt is not evidence
for the restacked combined head.
