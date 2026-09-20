# Marketplace applied-unavailable SDK implementation receipt

Date: 2026-09-20

## Exact refs

- Base: `origin/main` at `d55475199dbaa93a15ae4a720a7408f29e688126` (producer PR2050 merged)
- Full SDK head: `b240b03aea19d9b686f400bbf0ca6eeb95cf2746` before restack; `5e3233c156226bb40df8c15ebb46651382f83fa5` after restack
- Current branch: `codex/marketplace-applied-unavailable`
- Pre-producer backup: `backup/marketplace-applied-unavailable-pre-producer` at `b240b03aea19d9b686f400bbf0ca6eeb95cf2746`

## Behavior

The SDK exports `marketplaceRemovalOutcome`. The existing
`marketplaceUnregisteredCloneRemains` contract retains its authoritative
`applied` and unavailable outcomes. The merged producer's
`marketplaceRemoveApplied` with `appliedUnavailable: true` is classified as
`{ kind: "removed" }`, preserving the current marketplace list and browse
cache while the mutation remains rejected. This gives the host enough
information to suppress a retry and reconcile through its canonical refresh;
the host-side reconciliation and neutral message remain the web follow-up's
responsibility.

## Evidence

- Focused Vitest: `38 passed` for `state/extensions/marketplaces.test.ts`.
- TypeScript package build: passed.
- Package qualification: passed (`installed imports, declarations and read-only example`).
- Biome check/write: passed for both touched SDK files.
- `git diff --check`: passed.
- Fresh RoboRev branch review job `2702` against `origin/main` `d55475199d`: `No issues found`.

## Transfer note

The queued PR1960 web restack at `bbae95362b9860e78657236b43c1c776fa752f4f`
already owns `MarketplaceRemovalOutcome` and `marketplaceRemovalOutcome`.
The branch above is therefore a producer-qualified candidate and review
receipt, not an additional helper to land. Transfer only these deltas onto
the final PR1960 SDK files: add `{ kind: "removed" }` to the existing outcome
union; recognize `marketplaceRemoveApplied` only when
`appliedUnavailable === true`; retain ordinary and malformed errors as
undefined; and add the applied-unavailable classifier/store tests. Keep the
existing host canonical-refresh reconciliation and neutral web message in the
PR1960 follow-up.
