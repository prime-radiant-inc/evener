# Marketplace publication signal restack

## Current base and head

- Actual current main (includes merged #1954): `5c407b152cac4fa6b6a39debf8647a114f4f7ddc` (`sdk: fence reconciliation of applied marketplace removals (#1954)`).
- Composite parent recorded in the live PR body: `9328b79b9098575ae8496bb358dc90784f8f8b56`.
- Owned source commit: `37fb037a9f1dcaccfef105dd32d1a2b6fd592ffe`.
- Restacked branch: `codex/marketplace-publication-restack`.
- Restacked head: `f947bba6b6d368fa6caea023cabdce3a2ebfd49d`.
- The branch is one commit ahead of actual main; #1954 is inherited, not duplicated as an owned change.

## Range-diff and semantics

The exact owned source diff `9328b79b9098575ae8496bb358dc90784f8f8b56..37fb037a9f1dcaccfef105dd32d1a2b6fd592ffe` touches only:

```text
appwire-client/typescript/state/extensions/marketplaces.test.ts
appwire-client/typescript/state/extensions/marketplaces.ts
appwire-client/typescript/state/extensions/storeLifecycle.ts
```

It is 117 insertions and 7 deletions. The current-base-to-head diff is 117 insertions and 9 deletions because the current main version includes the accepted-cache retirement correction from #1954; those two deletion differences are the reconciliation of `publishMarketplaceSnapshot` with `retireCatalogsAbsentFrom`, not a second cache change.

Publication semantics are limited to the SDK marketplace state: `marketplacesPublicationVersion` advances only in accepted normal list writers, accepted normal mutation writers, and accepted typed applied-failure writers; failed, malformed, unavailable, fenced, reset, and disposed responses do not advance it. Reset preserves the last version through `storeLifecycle.resetState`. The inherited cache retirement remains present in all accepted writers, including the typed applied failure path.

No web or native consumer edits are included.

## Gates

- Full extension suite (`vitest run appwire-client/typescript/state/extensions`): PASS, 158/158 across 5 files.
- `npm --prefix appwire-client/typescript run build`: PASS.
- `npm --prefix cmd/evener-hub/frontend run typecheck`: PASS.
- Frontend `check-package-tests`: PASS; collected all 103 AppWire package test files and the activity-data edge test passed.
- `npx biome check --write` on touched SDK files: PASS.
- Frontend Biome CI scope: PASS.
- `make lint-package-imports`: PASS.
- `make test-api-package`: PASS; package qualification installed imports, declarations, and the read-only example.
- Dependency manifests matched the supplied parent worktree before using its existing `appwire-client/typescript/node_modules` and `cmd/evener-hub/frontend/node_modules` symlinks; no npm install was run.
- No push performed.

## Raw RoboRev receipt

Command:

```text
roborev review --branch --wait --base 5c407b152cac4fa6b6a39debf8647a114f4f7ddc
```

Output:

```text
Reviewing branch "codex/marketplace-publication-restack": 1 commits since 5c407b152cac4fa6b6a39debf8647a114f4f7ddc
Enqueued job 2687 for 5c407b152cac4fa6b (agent: codex)
Waiting for review to complete...
 done!

Review (by codex)
------------------------------------------------------------
No issues found.

Summary: Adds a marketplace snapshot publication version that advances on accepted list publications, survives resets, and remains unchanged for failed or fenced responses.
```
