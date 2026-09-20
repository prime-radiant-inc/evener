# Marketplace publication reset facade follow-up

## Disposition

The remote Low is real and reachable. The marketplace core's `resetState` correctly preserves `marketplacesPublicationVersion`, but `cmd/evener-hub/frontend/src/stores/extensions.ts` reset the combined facade with `marketplaces.getInitialState()`, overwriting that retained value with zero. A search of the available #1960 refs found no marketplace reset successor that handles this facade path.

## Base and head

- Qualified parent: `5dfd06d299a60f3919e17b4d546589c5b2c92860` (verified main after #1973).
- Branch: `codex/marketplace-publication-low` in `/tmp/evener-marketplace-publication-low`.
- Preserved backup: `backup/marketplace-publication-low-272da` at `272da2fca492da2146eadef2a3e986e6eb65cf08`.
- Head: `b384e8c041f9a4da19180a4466182afca048ffea`.
- Diff: 4 files, 22 insertions, 5 deletions.
- Ready PR: [#2054](https://github.com/prime-radiant-inc/evener/pull/2054), labels `sdk-refactor`, `sdk`, `web`; pushed, not merged.

## Change and proof

The facade reset now copies `marketplaces.getState().marketplacesPublicationVersion` after spreading the marketplace initial state. Reset docs now describe retained fields selected by `resetState` rather than claiming every field returns to its initial value. The frontend regression publishes one marketplace snapshot, calls the combined-store reset, and asserts the same publication version remains.

- Frontend `extensions.test.ts` focused test: PASS, 76/76.
- Full SDK extensions suite: PASS, 158/158 across 5 files.
- AppWire TypeScript build: PASS.
- Frontend typecheck: PASS.
- Frontend package test collection: PASS, all 103 package test files collected and activity-data edge proof passed.
- `make test-api-package`: PASS.
- Biome touched files and frontend enforced scope: PASS.
- `make lint-package-imports`: PASS.
- `git diff --check`: PASS.

## Raw local RoboRev receipt

Command:

```text
roborev review --branch --wait --base 5dfd06d299a60f3919e17b4d546589c5b2c92860
```

Output:

```text
Reviewing branch "codex/marketplace-publication-low": 1 commits since 5dfd06d299a60f3919e17b4d546589c5b2c92860
Enqueued job 2693 for 5dfd06d299a60f391 (agent: codex)
Waiting for review to complete...
 done!

Review (by codex)
------------------------------------------------------------
No issues found.

Summary: Preserves the marketplace publication version across combined-store test resets, adds regression coverage, and clarifies reset documentation.
```
