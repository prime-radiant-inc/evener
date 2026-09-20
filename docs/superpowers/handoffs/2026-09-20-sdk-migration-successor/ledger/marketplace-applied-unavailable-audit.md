# Marketplace applied-unavailable consumer audit

Date: 2026-09-20

## Disposition

PR #2050 is a real, reachable producer-side contract gap. Its exact head is `06841197c4a8b0d43f7270c4a0ebdefc2fe98677` (`fix(hub): an applied marketplace removal survives a failed list read`). The success path of `hubPluginsController.RemoveMarketplace` has already completed unregister and clone cleanup, then fails while re-reading the list. The patch returns `appwire.ErrorMarketplaceRemoveApplied` (`"marketplaceRemoveApplied"`) with `MarketplaceRemoveAppliedData{AppliedUnavailable: true}` and deliberately no snapshot. The test proves the marketplace is gone after the read is restored and that retry is not-found.

The contract is currently unconsumed in the SDK on current main. `appwire-client/typescript/state/extensions/marketplaces.ts:101-110` recognizes only `marketplaceUnregisteredCloneRemains`; `removeMarketplace` passes only `cloneLitterApplied` at lines 298-300. There are no current SDK, web, or native references to `marketplaceRemoveApplied`, `ErrorMarketplaceRemoveApplied`, or `MarketplaceRemoveAppliedData`. A #2050 error therefore rejects as an ordinary mutation failure. The web currently catches that rejection at `cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/MarketplaceSheet.tsx:270-273` and emits `Remove marketplace failed`, which is false after the removal has applied; retry then targets a missing marketplace.

The pending web PR #1960 is not a consumer for this discriminator. Its exact head is `07b00b456b06224111cf0b9e17eb1f2444396913` (`codex/marketplace-removal-warning`). Its SDK helper still checks only `error.evenerErrorInfo === "marketplaceUnregisteredCloneRemains"`; its web path treats any recognized outcome as clone cleanup failure and emits the leftover-clone warning. That is correct for the existing litter discriminator, but would be incorrect for `marketplaceRemoveApplied`, where the producer explicitly proves clone cleanup completed. #1960's publication guards and refetch path are useful infrastructure for the unavailable case, but do not bind the new producer signal.

## Smallest dependency sequence

1. Land the producer contract from #2050 (already available as the exact head above).
2. Add the smallest SDK consumer binding after the accepted-list/publication work: classify `marketplaceRemoveApplied` plus `appliedUnavailable: true` as an applied-but-unconfirmed removal, preserving the current list and never treating the absent snapshot as an empty list or retryable rejection. Keep the existing generation, revision, and named-retirement guards unchanged. The SDK outcome must retain enough distinction that web does not describe this no-litter path as clone cleanup failure.
3. Apply that SDK outcome in #1960's web reconciliation path: close/guard the completed removal as appropriate, trigger the normal list reconciliation, and use a neutral applied/unavailable message. Keep the existing clone-litter warning only for `marketplaceUnregisteredCloneRemains`.

This is a focused consumer binding, not a cache framework change. No implementation, PR, merge, or branch changes were made during this audit.

## Evidence commands

- `gh pr diff 2050 --patch` (exact producer diff and contract test).
- `gh pr view 1960 --json headRefOid` (pending head `07b00b456b06224111cf0b9e17eb1f2444396913`).
- `git show 07b00b456b06224111cf0b9e17eb1f2444396913:appwire-client/typescript/state/extensions/marketplaces.ts` and the corresponding `MarketplaceSheet.tsx` (pending consumer behavior).
- `rg -n 'marketplaceRemoveApplied|ErrorMarketplaceRemoveApplied|MarketplaceRemoveAppliedData' appwire-client cmd/evener-hub/frontend mobile-native` (no current-main consumer matches).
