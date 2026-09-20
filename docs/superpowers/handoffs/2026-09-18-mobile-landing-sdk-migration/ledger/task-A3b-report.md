# Task A3b — Export from the root everything the apps actually import

Status: DONE. Branch `claude/sdk-a3b-errors-exports`, SHA `e2d4895a7` (off `27503c07d`). Not pushed.

## Measured gap

Scanned every `import ... from "<prefix>protocol/<module>"` in `cmd/evener-hub/frontend/src`
(outside `src/protocol/`), `mobile/src`, `mobile-native/src` and `mobile-native/scripts/*.mts`
— 1161 files, 17 non-`testing` modules — and diffed the imported symbols against `index.ts`.
Missing, excluding `protocol/testing/*` (which A3 moves to the `testing` specifier):

- `errors`: `ClientNotReadyError` (3 sites), `GENERIC_ERROR_MESSAGE` (1), `HUB_UNREACHABLE_MESSAGE` (1),
  `errorKind` (2), `errorText` (20), `friendlyErrorMessage` (22), `friendlyLaunchErrorMessage` (3),
  `isHubLaunchError` (1), `isStaleCursorError` (3), `mutationErrorData` (2), `sessionActionError` (17),
  `sessionActionHeadline` (3) — the twelve the plan predicted.
- `reducer.chunkViewBackingForTests` (1) and `docContent.readDocFile` (1) — deliberately excluded, per
  scope: the first is a white-box test hook and rides the `testing` specifier in A3; the second is held
  for C24 (relative URL + browser `fetch` global). `index.ts` now carries a comment for each.
- One extra finding the plan does not list: `DocPane.test.tsx:5` does `import * as docContentModule from
  "../../protocol/docContent"` and `vi.spyOn(docContentModule, "readDocFile")`. A **namespace** import of a
  single module cannot be satisfied by any root export, so A4 cannot mechanically rewrite that line — it
  needs a different seam (inject the reader, or keep that one relative import).
- No app imports any `errors` **type** (`ErrorKind`, `MutationErrorData`, `MutationOutcome`,
  `MutationRetryDisposition`), so none were exported. Note the runner's "one exported type per shipped
  module that declares any" comment (`qualify-package.mjs:120`) has never held for `errors`; unchanged here.

## RED / GREEN

RED: extended `runtimeExports` + smoke calls first, `make test-api-package` failed with 24 tsc errors —
all twelve names missing in both `esm.mts` and `commonjs.cts`. GREEN after `index.ts` exported them.

## Gates

`make test-api-package` green, `make test-web` exit 0, `make test-native` exit 0, `npx biome check --write`
on the two touched protocol source files (it re-sorted the new export list).

## Lines

49 insertions / 6 deletions across three files: `index.ts` (+21/-1), `scripts/qualify-package.mjs` (+22),
`README.md` (+6/-5, export sentence reflowed). No app file touched; nothing deleted.

## Concerns

- The runner check that would diff app-imported symbols against the installed package was **skipped**: it
  needs the app sources at qualification time, and the runner `cd`s into the package directory and packs a
  standalone tarball (after A3 the package leaves the web tree entirely). The `runtimeExports` manifest
  stays the oracle. The measurement script that produced the list above is throwaway, so this stays a
  one-shot measurement rather than a standing gate — a future app import of an unexported symbol is caught
  by `tsc` in the app after A4, not by the package gate.
- The namespace-import site above is A4's problem and should be raised before that PR starts.
