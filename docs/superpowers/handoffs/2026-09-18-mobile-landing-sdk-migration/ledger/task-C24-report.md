# C24 — docContent injected base origin, browser and native adapters

**Status:** done, committed, not pushed.
**SHA:** `272a3cd9b` on `claude/sdk-c24-doccontent-origin` (off `f39aa2c83`).

## Port shape
`DocPort = { origin: string; fetch: DocFetch }`, `readDocFile(session, path, port)`.
Chosen over an origin-bearing sibling to `DocFetch` because it is strictly
smaller: `DocFetch` keeps its meaning and its single implementation shape,
`readDocFile` keeps arity 3, and the adapters are the only new code. A sibling
port type would have to re-derive the URL itself (duplicating `docFileRawURL`)
or carry the origin redundantly.

Builders are origin-first: `docFileRawURL(origin, session, path)` and
`docImageURL(origin, session, path)`. `docImageURL` still returns a string.
A private `docBase()` drops a trailing slash so a hand-typed
`https://hub.test/` composes one well-formed href.

## Adapters
- Web: `cmd/evener-hub/frontend/src/panes/doc/browserDocPort.ts` (renamed from
  `browserDocFetch.ts` — it exports a port now), `origin: ""`, same-origin
  credentials unchanged. Web URLs are byte-identical to today's.
- Native: `mobile-native/src/nativeDocPort.ts` + `nativeDocPort.test.ts`.
  That is where the hub origin and bearer token live (`connection.ts`
  `createHubClient(origin, token, …)`), and it sits beside the existing
  `transcriptImageSource.ts` image-source precedent. No screen wired.

## REDs
1. Manifest first: `packageExports["./docContent"]` updated to `DocPort` →
   `make test-api-package` failed with TS2305/TS2694 "no exported member
   'DocPort'" in both the ESM and CJS declaration consumers.
2. `nativeDocPort.test.ts` failed to resolve `./nativeDocPort` before the
   adapter existed. Protocol unit tests were extended in the same RED pass
   (origin composition, absolute base, trailing slash) and went green with the
   implementation.

## Gates
`make test-api-package` PASS · `make test-web` PASS (typecheck/test/lint) ·
`make test-native` PASS (79 files/740 tests, shared 7/777, `tsc` check) ·
`make test-web-browser` PASS (all five guards; plan rule 1 requires it for
package-touching PRs). Biome run on the 8 touched web/protocol files only.

## Lines
+272 / −97 across 14 files — within the ~180 estimate once the two renamed
files (counted as add+delete) and the test extensions are separated out.

## Concerns
- `AgentMarkdown.tsx` now imports `browserDocPort` from `panes/doc/`. That
  cross-pane direction already exists (`fileOpenBeside.tsx` imports
  `../../doc/openDoc`), but if a third consumer appears the port belongs
  somewhere more neutral than the doc pane.
- `nativeDocImageSource` always sets `headers` (`{}` when there is no token),
  where the sibling `transcriptImageSource` omits the key entirely. One helper
  for both fetch and image source was worth the small inconsistency; say so if
  you disagree.
