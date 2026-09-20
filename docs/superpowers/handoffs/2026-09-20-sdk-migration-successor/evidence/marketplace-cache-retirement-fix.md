# Marketplace accepted-cache retirement

## Disposition

Luna22647 Medium is real and reachable at parent `f3d4435207bc7429785542504559550b210751b4`.
The accepted publication successor `37fb037a9f1dcaccfef105dd32d1a2b6fd592ffe` adds publication-version signaling and web refetch/guard behavior, but does not retire SDK browse caches omitted by accepted snapshots. The SDK correction remains required.

## Change

Branch: `codex/marketplace-accepted-cache-retirement`

Commit: `623d1d3015c57f2d0e5be9ff286f99fd66826ccb`

The exact full SHA above is the reviewed commit. Accepted whole-list writers (list reads, successful mutations, and typed applied clone-litter failures) now retire cached catalog names absent from the accepted marketplace array. Existing named retirement and list-revision/generation fences remain intact.

Regression: an older `removeMarketplace("acme")` rejects with accepted applied `[local]`, a newer unrelated removal accepts `[]`, and the final list is empty with `acme` absent from `browseCatalogs`. The current parent fails this assertion; the branch passes it.

## Proof

- `npx vitest run appwire-client/typescript/state/extensions/marketplaces.test.ts --reporter=dot`: PASS, 31/31.
- `npm --prefix appwire-client/typescript run build`: PASS.
- `npx biome check --write appwire-client/typescript/state/extensions/marketplaces.ts appwire-client/typescript/state/extensions/marketplaces.test.ts`: PASS.
- `npx biome ci src ../../../appwire-client/typescript` from `cmd/evener-hub/frontend`: PASS.
- `make lint-package-imports`: PASS.
- `git diff --check`: PASS.
- Full extension suite (`vitest run appwire-client/typescript/state/extensions`): PASS, 154/154 across 5 files.
- Frontend `npm run typecheck`: PASS.
- Frontend `npm run --silent check-package-tests`: PASS; collected all 102 AppWire package test files and the activity-data edge test passed.
- `make test-api-package`: PASS; package qualification installed imports, declarations, and the read-only example.
- The earlier fresh-worktree package-test block was resolved only by symlinking the matching parent worktree dependency directories after manifest hashes matched; no package installation or source change was made.

Diffstat against exact parent: 2 files changed, 47 insertions(+), 3 deletions(-).

## Raw RoboRev receipt

Command:

```text
git rev-parse --verify f3d4435207bc7429785542504559550b210751b4
roborev review --branch --wait --base f3d4435207bc7429785542504559550b210751b4
```

Output:

```text
Reviewing branch "codex/marketplace-accepted-cache-retirement": 1 commits since f3d4435207bc7429785542504559550b210751b4
Enqueued job 2684 for f3d4435207bc74297 (agent: codex)
Waiting for review to complete...
 done!

Review (by codex)
------------------------------------------------------------
No issues found.

Summary: Accepted marketplace lists now retire omitted catalogs and invalidate their pending browse responses, with a regression test covering an older applied failure superseded by a newer successful removal.
```
