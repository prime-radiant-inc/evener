# Task A1 — Ship the nine unpacked protocol modules

**Status:** done, committed, not pushed. **SHA:** `30b8c585e` on
`claude/sdk-a1-ship-protocol-modules` (off `92561dbe3`), worktree
`.claude/worktrees/sdk-a1-ship-modules`.

**Shipped (nine):** `activityData`, `activityList`, `activityMerge`,
`jobOutput`, `model`, `reducer`, `sendQueueAvailability`, `sessionErrors`,
`stableDelegate` — added to `tsconfig.build.json` `files` and re-exported from
`index.ts` (types as `export type`). Tarball now carries `.js` + `.d.ts` for
all fifteen modules; zero runtime dependencies preserved.

**Held back:** `docContent.ts`, per the brief. Proving line `docContent.ts:77`:
`const res = await fetch(docFileRawURL(session, path), { credentials: "same-origin" });`
No other module has a DOM/browser dependency — a grep for `window|document|
localStorage|navigator|fetch|location|Blob|crypto|...` over the nine hit only
comment prose. `reducer.ts:271 chunkViewBackingForTests` is also unexported
from `index.ts`: it is a white-box test hook, not public API.

**RED:** final runner against the old build config — exit 2, 170 `error TS`
lines, first `esm.mts(13,3): error TS2305: Module '"@evener/appwire-client"'
has no exported member 'isFailedJobOutcome'.`
**GREEN:** `qualified @evener/appwire-client@0.1.0: installed imports,
declarations and read-only example`
**Falsification:** flipping one smoke assertion gave
`AssertionError [ERR_ASSERTION] ... actual: 'turn_system'`, so the new
per-module call block really runs inside the installed consumer.

**Gates:** `make test-api-package` pass (baseline was pass); `make test-web`
pass (typecheck/test/lint); `make test-native` pass (78/737 and 7/777);
`npx biome check --write` clean. This worktree had no `node_modules`, so I ran
`npm ci` in `cmd/evener-hub/frontend`, `mobile-native` and `protocol`.

**Lines:** 212 insertions / 36 deletions, 4 files. Within the ~200 estimate.

**Concerns:**
1. `package.json` needed no edit — the single `"."` export already reaches
   every module through the barrel. Subpath exports would widen the public
   surface for nothing; a deep-import decision belongs to A3/phase C.
2. `ActivityNodeLike` is in `activityNodeID`'s public signature but is not
   exported from `activityData.ts`, so a consumer cannot name it. Declaration
   emit is fine (inlined). Left alone — out of this PR's stated scope.
3. `protocol/README.md` got two sentences updated; it enumerates the exports
   and what the runner proves, both of which were about to be stale.
4. `make test-web-browser` was not run (plan rule 1 wants it on package PRs).

## Review round 2 — docContent shipped (PR #1184)

The Low finding was right and the ruling is applied. `docContent.ts` now ships:
`tsconfig.build.json` files list, and `index.ts` exports `DocFileContent` /
`DocFileErrorKind` as `export type` plus `DOC_FILE_MAX_BYTES`, `DocFileError`,
`docFileRawURL`, `docImageURL`, `readDocFile`. Nothing is held back now, so the
runner's absence assertion and its `heldBackModules` list are gone, replaced by
presence in `shippedModules`. Smoke call added:
`assert.equal(client.docFileRawURL("s", "p"), "/doc/file?format=raw&session=s&path=p");`
`readDocFile` is exported but never called by the runner.

**All sixteen modules now ship.** The PR body's module list should read: index,
client, errors, transport, types.gen, askAnswers, activityData, activityList,
activityMerge, jobOutput, model, reducer, sendQueueAvailability, sessionErrors,
stableDelegate, docContent.

**SHA:** `a04f1d97d` (merge of `origin/main`), on top of `a50c06875`
`feat(protocol): ship docContent's pure helpers` and `30b8c585e`.
**RED:** `esm.mts(43,3): error TS2305: Module '"@evener/appwire-client"' has no
exported member 'docFileRawURL'.` **GREEN:** `qualified
@evener/appwire-client@0.1.0: installed imports, declarations and read-only example`

**Gates:** `make test-api-package` pass (again after the merge), `make test-web`
pass, `make test-native` pass (78/737, 7/777), biome clean on touched files.

**Note:** `tsconfig.build.json`'s `files` list is documentation, not a boundary
— tsc emits any module `index.ts` imports whether or not it is listed. The real
packing boundary is `index.ts`. I kept the list complete so it does not mislead.

## Review round 3 — the unreachable-module hole (PR #1184)

The Low was right: `shippedModules` only fed the tarball listing, and the
presence loop checks identifier names drawn from `runtimeExports`, so a module
added to `files` and to `shippedModules` but never re-exported from `index.ts`
qualified clean. The comment claimed otherwise.

Fix, the smaller of the two suggested shapes: derive reachability from the
built artifact rather than a hand-maintained manifest. The runner reads
`dist/index.d.ts` out of the *installed* package and requires a
`from "./<module>"` re-export specifier for every shipped module but `index`.
Six lines, no per-module attribution to keep in sync, and it cannot drift from
what the barrel actually does. The overreaching comment on `runtimeExports` was
corrected to say what that list does prove.

**SHA:** `68a1da7c2` (merge of `origin/main`), on top of `73387e330`
`test(protocol): fail the package gate on a shipped module nothing exports`.
**RED:** with a fake `"notReExported"` entry in `shippedModules` —
`AssertionError [ERR_ASSERTION]: shipped module unreachable from the entry point: notReExported`
**GREEN** (fake entry reverted): `qualified @evener/appwire-client@0.1.0:
installed imports, declarations and read-only example`

**Gates:** `make test-api-package` pass (again after the merge), `make test-web`
pass, `make test-native` pass (78/737, 7/777), biome clean on the touched file.
