# Task 4c1 report: native image-marker reservation

## Result

Implemented and committed Task4c1 as `59dfa3f6eb` (`fix(native): reserve queued draft image markers`). The work changes only the approved native marker allocator/document paths and focused tests. Draft recovery, submission, UI, runtime, storage schema, dependencies, and hooks were not changed.

## Behavior and files

- `mobile-native/src/imageSelection.ts` uses the shared `nextComposerImageMarker` allocator from the package root to reserve markers found in draft text, current images, unconfirmed images, pending selection allocations, and the existing monotonic high-water mark.
- `mobile-native/src/draftDocument.ts` rechecks a marker against the latest draft text and current/unconfirmed image references at insertion. A collision gets a fresh marker without changing the image ID or bytes. Duplicate-ID rejection and attachment limits remain in place. Mention offsets shift on marker insertion and removal using JavaScript UTF-16 string offsets; other composer metadata is spread through unchanged.
- `mobile-native/src/imageSelection.test.ts` covers literal/current reservations, unconfirmed and pending reservations, literal text typed during encode, another tile claiming a pending marker, and existing picker behavior.
- `mobile-native/src/draftDocument.imageMarkers.test.ts` uses `DraftDocument`, `DraftRepository`, and the SQLite test database to cover metadata offsets around a multi-digit marker and a UTF-16 surrogate pair, marker reallocation with ID/bytes preserved after reopen, and a failed write followed by retry.

## Test-first evidence

Before any production edit, ran from `mobile-native`:

```sh
npm test -- src/imageSelection.test.ts
```

Exit: **1**. The runner's three Node scene-lifecycle tests passed. Vitest ran 20 image-selection tests: 18 passed and the two new cases failed for the intended behavior: expected marker allocations `[2, 4]` but received `[2, 3]`, and expected `typed [image 1][image 2]` but received `typed [image 1][image 1]`.

Then ran the expanded RED suite before production edits:

```sh
npm test -- src/imageSelection.test.ts src/draftDocument.imageMarkers.test.ts
```

Exit: **1**. The new tests failed on literal reservation, late text collision, UTF-16 mention offsets, and document marker collision. This showed the expected missing behavior before implementation.

## Final checks

From `mobile-native`, the final required focused command was:

```sh
npm test -- src/imageSelection.test.ts src/draftDocument.test.ts src/draftDocument.imageMarkers.test.ts
```

Exit: **0**. The three scene-lifecycle Node tests passed. Vitest passed all 3 files and all 42 tests (22 image-selection, 17 existing draft-document, 3 focused SQLite image-marker tests).

Also passed:

- `npm run check` — exit 0; `tsc --noEmit --project tsconfig.check.json` completed.
- `./node_modules/.bin/biome ci src/imageSelection.ts src/imageSelection.test.ts src/draftDocument.ts src/draftDocument.imageMarkers.test.ts` — exit 0; checked all 4 touched TypeScript paths. Biome's `check --write` ran on these paths first and fixed formatting; the post-format test and typecheck commands passed.
- `git diff --check` — exit 0.
- The existing configured pre-commit hook was executable at `/home/jesse/git/prime-radiant-inc/evener/scripts/hooks/pre-commit`. Commit ran with hooks enabled; it reported `Formatted 4 files ... No fixes applied.`

## Review and limits

Self-review confirmed marker reallocation changes only the marker reference, preserves the incoming image ID/data, and shifts metadata for insertion/removal while retaining other metadata fields. The new behavior tests use actual SQLite-backed draft persistence; only picker/encode timing is scripted. The code imports the existing shared allocator through `@evener/appwire-client`, because `composerRecovery` is exported at the package root and not as an importable subpath in this setup.

No full native gate or other full repository gates were run, per the brief. No recovery-ownership changes were made; Task4c2 remains separate. The working tree was clean after the source/test commit. No scratch files were created.
