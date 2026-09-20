# Task C0 — subpath-aware qualification manifest

Status: done. Branch `claude/sdk-c0-subpath-manifest`, SHA `b087560dd054ff318592e0d2ed2f3350480e4ad1` (not pushed).
Files: `cmd/evener-hub/frontend/src/protocol/scripts/qualify-package.mjs`, `.../protocol/README.md`. 110 insertions, 65 deletions.

## Shape
`packageExports` maps specifier -> `{ values, types, esmTypeUses?, cjsTypeUses?, smoke? }`. Two name lists, not one, because the
declaration consumers need value and type names separately. The root's three long literals stay hoisted consts referenced by the
map (`rootValues`, `rootTypes`, `rootSmokeCalls`); inlining them would have cost ~180 diff lines of pure re-indentation.
Per specifier the runner now emits `esm-<slug>.mts`, `commonjs-<slug>.cts`, `runtime-<slug>.mjs`, `runtime-<slug>.cjs`.
Reachability is per specifier too: each specifier's declaration target is read from `exports[specifier].types` in the installed
tree, and a shipped module qualifies by being some specifier's entry or re-exported from one.

## RED
1. Manifest gains `"./testing"`, cross-check not yet written: tsc dump, `esm-testing.mts(3,8): error TS2307: Cannot find module
   '@evener/appwire-client/testing'` — and the reverse direction was not caught at all.
2. With the cross-check: `AssertionError: qualification manifest names ./testing, which package.json does not export`.
3. Reverse direction (exports gains `./docContent`, manifest does not): `AssertionError: published specifier is not qualified: no
   manifest entry for ./docContent`.

## Throwaway subpath (reverted, not committed)
Published `"./docContent"` in `exports`, gave it a manifest entry, AND deleted docContent's two re-export lines from `index.ts`
plus its names from the root entry — so the module was reachable only through its own subpath.
- Gate green. Generated set: `esm-root.mts commonjs-root.cts esm-docContent.mts commonjs-docContent.cts runtime-root.mjs
  runtime-root.cjs runtime-docContent.mjs runtime-docContent.cjs`; the subpath consumers import from
  `@evener/appwire-client/docContent` (including `readDocFile`, which the root does not export).
- Declaration checks bite: a bogus `readDocFileTypo` in the subpath's values failed both `esm-docContent.mts` (TS2724) and
  `commonjs-docContent.cts` (TS2551).
- Reachability bites: removing the subpath from `exports` and the manifest, with docContent still out of `index.ts`, failed with
  `shipped module unreachable from every published specifier: docContent`.
- Reverted `index.ts`, `package.json` and the throwaway manifest entry; gate green again with only `"."`.

## Notes
- An in-repo-only alias such as the plan's non-shipped `testing` mapping cannot be listed: the manifest accepts only specifiers
  present in `exports`, and nothing in the tarball backs it. That alias is validated by the apps' typecheck, not by this gate.
- Gates: `make test-api-package` green, `make test-web` green (typecheck/test/lint), `make test-native` green (777 tests +
  tsc), `npx biome check` on the touched script clean.
- Housekeeping: the failed RED runs left ~13 `evener-appwire-package-*` fixture directories in TMPDIR (the runner retains them on
  failure by design). Recursive delete is blocked for me; they need a manual sweep.
