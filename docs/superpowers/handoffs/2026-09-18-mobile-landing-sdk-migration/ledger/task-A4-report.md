# Task A4 report — import the AppWire package by name, delete the seam

**Status: done.** PR #1272 (not draft), branch `claude/sdk-a4-package-imports`, head `08cb33369`, off `origin/main` `57509ffd5` (A3).

Commits: `21f2dca56` rewriter + `testing/reducerHooks.ts` · `38643431f` duplicate-import merge pass · `342ec9e29` web rewrite · `0b95cd929` native rewrite · `df7d142ba` seam deletion · `adcc94a15` `lint-package-imports` · `564a853ab` `resolve-check.mjs` · `61cc0658c` AGENTS.md + shim comment · `acff341f7` lint-gate audit row · `08cb33369` metro.config.js comment.

Final state, after the merge pass collapsed duplicates: web 489 root / 3 `docContent` / 124 `testing/*` (fakeClient 113, notifications 6, tokenFlood 3, fakeSocket 1, reducerHooks 1); mobile-native 120 / 2 / —; mobile/src 20 / — / —. **758 statements across 515 files.** The sweep rewrote 870 + 1 hand edit; merging brought 871 to 758. The six `mobile-native/scripts/*.mts` relative imports are untouched and name no `src/protocol`.

Gates, all green: `make test-api-package` (incl. the new resolve-check), `make test-web` (typecheck/test/lint; `check-package-tests` still collects all 27 package test files), `make test-web-browser` (all five guards), `make test-native` (740 native + 778 shared + `tsc`), `make lint` incl. `lint-generated` (ten targets), `WEB=0 make test` (every Go module, both audits), `make build-web`. **`make test-native-bundle`: PASS, 7s, 1592 modules** — run by hand from #1245's `scripts/native/test-native-bundle.sh`, dropped in uncommitted and removed; nothing from #1245 is committed here and it must be re-run on main before merge.

## The one judgment call

The rewrite found exactly one symbol with no published home: `chunkViewBackingForTests` at `reasoningFormat.test.ts:3`. The A4 row already names the fix — a `testing/reducerHooks.ts` split — and **assigns it to A3, which shipped without it**. Rather than stop, I closed it: a one-line re-export in the package's non-shipped `testing/` directory (not in `tsconfig.build.json` `files`, so the tarball, `exports` map and qualification manifest are untouched), plus a two-line hand split of that test's import. Reverting means reverting one file and two lines. Everything else needed no judgment.

## Where A3's report and the brief were wrong or short

- **A3 did not ship `testing/reducerHooks.ts`**, which the plan's A4 row states as an A3 deliverable and which A4's "no carve-outs" acceptance depends on. Above.
- **The brief's item 3 is empty work.** No biome, vitest or tsconfig entry named `src/protocol/` — A3 left none behind (verified by a repo-wide grep). The seam deletion is the deletion alone.
- **The brief's item 6 overstates the docs.** No `docs/web-ui` active citation spells `src/protocol/` (the one hit is `codex-rs/protocol/src/protocol.rs`), and no README sentence describes deep-path imports — the package README is about the published package, so I left it alone. Real finds: `node-fs-shim.d.ts:2` and a now-false comment in `mobile-native/metro.config.js` saying nothing imports the package by name yet.
- **`make lint` needed a second edit the brief does not mention:** `makefiletargets_audit_test.go`'s `lintGateCommands` map, or `TestMakeLintRunsEveryGateInLintTargets` fails. CI's `lint-repository` job names targets one by one, so the new gate had to be added there too — `make lint` locally is not what CI runs.
- The rewrite creates duplicate statements (three deep paths collapsing to one specifier): 89 files. Biome merges them for web; nothing formats `mobile-native`, so the script does it for both.

## Concerns

1. **#1244/#1245 is still the load-bearing unknown.** This is the first change that makes Metro's `@evener/appwire-client` branch live, and the only gate that reads it is on an unmerged branch being rewritten. I falsified it (disabling the branch fails at `HubSettingsScreen.tsx:20`), so the PASS is real — but until #1245 lands, nothing on main would catch a Metro resolver break.
2. The acceptance grep matches quoted specifiers, so a run-time-assembled specifier would slip past. Nothing in the tree does that.
3. `scripts/sdk/rewrite-package-imports.mjs` is a one-shot migration tool that will have no callers after A4. It stays because the C and D rows relocate more modules into the package and will want it; if the coordinator disagrees, deleting it costs nothing (only `package-import-paths-check.sh` is wired into a gate).


## Round 1 (2026-09-14)

Merged `origin/main` `ea6649c17` (`a9250c123`, Go-only). Head **`d68f6af4c`**, PR comment posted.

- **F1** `d68f6af4c` — rewriter staged writes in memory; writes only after a clean sweep. `--root` added for fixtures. Four tests at `cmd/evener-hub/frontend/scripts/rewrite-package-imports.test.mjs` (the only runner with the `node_modules` the rewriter parses with); the refusal case fails against the old code and passes now.
- **F2** `4fcc502c4` — **broadened the grep, did not switch to the AST `--check`**: CI's `lint-repository` job installs no dependency tree, so that path costs an `npm ci` on a sub-second lane. Either quote; any call taking the path as first arg (covers `vi.mock`/`importActual`/`require`/`import(`); no trailing slash needed. Forced two narrowings: match `/protocol/` not `protocol` (the app has a `"protocol"` literal and imports `@modelcontextprotocol`), and skip `*.config.*` (`mobile-native/vitest.config.mts` names the path in three `new URL` calls — that mapping is the point). `packageimportpaths_audit_test.go` at root holds it against fixtures; exactly the four bypasses fail against the old pattern.
- **F3, F4** `4fcc502c4` — metro comment cites #1244; `lint`'s `## proves:` gained the import check, `make generate` re-rendered `linting.md`.
- Unrequested: `errorlint` rejected a `*exec.ExitError` type assertion in the new audit test; uses `errors.As`.

Gates all green on `d68f6af4c`, including `make test-native-bundle` PASS (7s, 1592 modules), again run by hand from #1245 and removed — still must be re-run on main before merge.

## Round 2 (2026-09-14)

`origin/main` already merged (nothing new). Head **`1177bc70e`**, PR comment posted. All gates green, incl. `make test-native-bundle` PASS (11s, 1592 modules) run by hand from #1245 and removed.

- **M1** `94ef2bc6e` — merged members keyed by local binding, not rendered text. Same export spelled `{ type Thing }` + `{ Thing }` collapses to the value form; two exports behind one alias is refused. Forced the merge pass to become pure and stage its output beside the sweep's. Both cases are tests; exactly those two fail against the old code.
- **M2** `4e54c29f0` — the finding is right and the example is real: `panes/spawn/usePluginPreview.ts:2` value-imports `errorText`, Metro bundles `cmd/evener-hub/frontend/src` as shared source, and the fixture never asked for it. Scan now covers all three trees minus `*.test.*` and `testing/`; **27→81 root, 2→4 docContent**. Removed the duplicate list instead of growing it: `resolve-check.mjs`'s import clauses **are** the list (Node throws on a missing named export), the runner parses them, and an object naming every binding keeps them load-bearing via Biome's unused-import rule. Renamed to `consumer-value-imports.mjs`.
- **L1, L2** `1177bc70e` — all three trees must exist (exit 2); grep status 1 vs >1 classified, inline rather than in a helper because `exit` inside a command substitution leaves only the subshell; seam pattern takes a closing quote so `"../protocol"` is caught.
- **Found while testing L1:** `--root ""` passed the arg check and `cd ""` is a no-op, so the gate swept the caller's directory — I watched it report on a different worktree. Now exits 2, with a test.

## Round 3 (2026-09-14)

Merged `origin/main` `38da64a63` (`06ba3f05c`). Head **`85ac874fe`**. All gates green, incl. `make test-native-bundle` PASS (7s, 1592 modules) by hand from #1245.

- **M1 — the carve-out's premise was false; it is gone.** Measured: `npx tsx scripts/check-hub.mts` on the old head died with `Cannot find module '@evener/appwire-client'` from `mobile-native/src/connection.ts` — the documented scripts *were* broken by this PR. But the failing stack runs through tsx's own `resolveTsPaths`: **tsx reads `paths`, from `tsconfig.json`, which simply had none** (`tsconfig.check.json` is passed explicitly to `tsc` only). With the three entries mirrored in, the script reaches its own usage throw. So: paths added (`d0772fb2c`), six `.mts` imports rewritten (`ca2b3096d`), carve-out deleted from the gate, the deriver — which removes the relative/absolute `CARVE_OUT` compare, the round-2 Low — and AGENTS.md (`85ac874fe`).
- **Smoke check** `mobile-native/scripts/check-script-imports.mts`, run as `npm run check:scripts` at the end of `make test-native`. It **resolves rather than imports**: every one of these scripts opens a socket or starts a server the moment its body runs (`demo-hub.mts` listens forever), and `import.meta.main` guards would mean restructuring five files including a 416-line one. It asks Node's resolver through the `createRequire` path tsx hooks and walks each graph to the first `node_modules` boundary. RED without the paths (4 unresolvable), green with (5 tools, 39 first-party modules).
- **Metro unchanged** — `resolveRequest` stays the mechanism; nothing measured shows Expo's tsconfig-paths support makes it redundant.
- **M2 — grep option order.** Measured: BSD grep 2.6.0-FreeBSD accepts options after operands by permutation, so it was not a live defect; under `POSIXLY_CORRECT=1` the same call gives `grep: --include=*.ts: No such file or directory`, exit 2, filter silently dropped. Moved them first. The two sweeps collapsed into one — the old-path grep was re-reporting a subset of the first's lines — with the two verdicts split out of one result.
- **M3** declined upstream; no change.

## Round 4 (2026-09-14)

`origin/main` `fccf109bf` already an ancestor. Head **`4d92e3e0c`**. All gates green, incl. `make test-native-bundle` PASS (8s, 1592 modules) by hand from #1245.

- **1** `4d92e3e0c` — grep measurement in the script header (macOS 26, BSD grep 2.6.0-FreeBSD, all three flags accepted, dated).
- **2** `0e87c794d` — `.mts` in the extension filter with its `ScriptKind`, and `ts.isExportDeclaration` nodes collected alongside imports. **Neither hole changes the list today**: `.mts` contributes only `WireError`, the re-export only `graftContinuationTree`, both already imported elsewhere, so it stays 81/4. `WireError` is not `.mts`-only, so removing it proves nothing; verified the mechanism by giving `demo-hub.mts` a value import of `APPWIRE_PROTOCOL_VERSION` (nothing else takes it) and watching the runner fail on the drift, then reverting.
- **3** `0e87c794d` — regex replaced by the shared AST reader `packageValuesIn`, used for both readings. Six tests at `appwire-client/typescript/scripts/consumer-value-imports.test.mjs` (package vitest include; count gate now 28): two statements on one specifier, either quote, `import type` split, re-export, `X as Y` → `X`, unpublished specifiers ignored.
- **4** `4d92e3e0c` — `make/linting.mk` annotation, regenerated `linting.md` row, and the audit-test header describe three trees swept whole with only named resolver configs exempt; no carve-out narration.
- **5** `4d92e3e0c` — exemption by filename (`vite.config.*`, `vitest.config.*`, `metro.config.*`). Only `mobile-native/vitest.config.mts` is inside the swept trees; the frontend's vite and browser-guard configs sit beside `src/`, not in it. Fixture `src/panes/spawn/launch.config.ts` fails now and passed under `*.config.*`.

## Round 5 (2026-09-14) — CI was RED, my break

Merged `origin/main` (`43a3f71db`). Head **`8ab672ee4`**. CI run 34806898972 failed collecting the round-4 test file with `Failed to resolve import "typescript"`.

- **Cause** `6a8de5dd6` — Vite resolves a bare specifier from the importer, which is inside the package. `appwire-client/typescript/node_modules` exists on my machine because step 0 of the brief and `make test-api-package` require `npm ci --prefix appwire-client/typescript` (it installs `typescript` and `ws`), so the package's own install answered. CI's web job installs the frontend alone. **Not a stray to delete** — the api-package lane needs it. Fix: `typescript` added to `vite.config.ts`'s `resolve.alias`, the same seam A3 used for `react`/`@testing-library/react`. Nothing added to the package; `qualify-package.mjs` still resolves `typescript` from the package's own devDependency under plain node, which is what the api-package lane installs.
- **Guard** `6a8de5dd6` — `check-package-tests` now walks the package's test graph and holds every bare specifier against the keys of `vite.config.ts`'s `resolve.alias` (AST-read), with `vitest` the one exception since the runner resolves it. Removing the alias makes it fail by name locally, with the package's node_modules present. Five unit tests. This is the invariant the alias block already asserted in prose and could not enforce.
- **Verified both directions** by moving the package's node_modules aside: without the alias, CI's exact error; with it, green.
- **3** `8ab672ee4` — AGENTS.md: "exempts nothing" → "exempts only the resolver configs, named one by one".
- **`make test-web` and `make build-web` were re-run with `appwire-client/typescript/node_modules` out of the tree** (CI parity), then it was restored for the rest. All other gates green, incl. Metro by hand (8s, 1592 modules).

## Round 6 (2026-09-14)

Head **`f7acc5e1f`**. Merge up to `1d5e12100` was a no-op (already an ancestor); main stays red at `c09997369`, not ours.

- `f7acc5e1f` — `existsSync` is true for a directory, so `import "./helpers"` returned the directory, the `index.ts` candidate never ran, and the walk read a directory as a file (EISDIR): a crash, not a failure. Test written first (real dir + `index.ts` reachable from a package test, asserted on both the resolver's answer and the walk getting through it); RED against the old rule.
- Shared in **`scripts/sdk/resolve-source.mjs`**, used by the rewriter and the package-test gate. TypeScript was not the obstacle (the gate already imports it; the frontend already reaches into `scripts/lib/`); the rewriter's own resolver could not be imported because that module ends in `process.exit(main())`. Extension list is a parameter — the rewriter resolves TypeScript only, the gate also reaches `.mjs`.
- Gates: `make test-web`, `make lint` (ten targets), `WEB=0 make test` (both audits) green; `node --test` 34 + 6.

## Rounds 7-9 (2026-09-14)

Merge of main at `42be47c90` (#1168's `canonicalSkillNames` reached through the deleted seam: took main's content, restored the seam, re-ran the rewriter, deleted it again; **`canonicalSkillNames` became a root export** because it had none once the seam went). Heads: r7 `cc0a2b61c`, r8 `e64fcb689`, r9 **`b514e64ff`**.

- **r7** — `ts.isStringLiteralLike` in all four AST readers (backtick specifiers); `linting.md`'s hand-written LINT_TARGETS list.
- **r8** — Metro resolver unit test + a bogus subpath now falls through instead of naming a missing file; `index.<ext>` probing for every extension; longest-alias-prefix matching; stopped skipping app dirs named `testing` (lists unchanged); next-line specifier caught by the grep without `-P`/`-z`/awk.
- **r9** — **closed the class**: `scripts/sdk/module-specifiers.mjs`, one AST reader (ts injected as a parameter) consumed by the rewriter, the deriver, the package-test walker and the native `.mts` check. Loud refusals for side-effect/namespace/default imports of the package. The grep stays a grep, held to the reader by `module-specifier-forms.json` — 14 forms, both suites read it; three fail against the round-8 pattern. Exemption by exact resolver filename.
- **#1300 filed**: `TestSkillComposerBrowser` fails on macOS (`/private/var` vs `/var`), untouched by this branch, green on ubuntu.

## Round 10 (2026-09-14)

Head **`f8af7d59b`** on `origin/main` `f9e1f5459`. `packageValuesIn` inverted from a denylist to an allowlist: only `import-named` and `export-from` are accountable; every other kind is reported with file and kind. That also picks up `export-star-from`, which the runtime-site filter had been skipping a layer earlier and which the review's list did not name. One test per refused kind (7); five fail against the previous version. **Lists unchanged at 82 root / 4 docContent** — 82 rather than the earlier 81 because `canonicalSkillNames` joined at the main merge — so `resolve-check.mjs` is untouched. All gates green including `make test-web-browser` (six guards; #1301 fixed the macOS path comparison, closing my #1300) and the Metro gate by hand (6s, 1592 modules).

## Round 11 (2026-09-14)

Head **`db388c9dd`**. Merged `bcb8406d9` (#1249) at `596a5c582` — same seam-conflict recipe, 7 statements in 2 files, nothing new to publish.

- **Measured first:** inside mobile-native's vitest, `require` is the runner's injected CommonJS trio (`require`/`module`/`__filename` all present). Switched the metro test to `createRequire(import.meta.url)`.
- **`isRuntimeSite` was answering two questions** — value qualification (star re-exports name nothing) and graph traversal (star re-exports are loaded). Now `isLoadedAtRuntime`; the round-10 allowlist keeps star exports out of qualification. `walkScriptImports` exported + entry-guarded with an injectable resolver (tsx's patch is not installed under vitest); three cases, two fail against the old predicate.
- **`import X = require()`** added as `require-equals`: refused by the rewriter, in the shared form list (15), RED without it.
- All gates green incl. six browser guards and the Metro gate by hand (7s, 1592 modules).

## Round 12 (2026-09-14)

Head **`a82e36e99`**; `origin/main` already merged. Both `.../appwire-client/typescript` and `.../typescript/index` resolve to `index.ts` → module id `index`, which the whole-module branch was refusing as an unpublished subpath — of the root, which *is* published. `index` now maps to `@evener/appwire-client` for namespace / dynamic-import / require / star-re-export / require-equals sites; `./docContent` stays the one subpath and every other module id stays refused. Two fixtures (both spellings rewrite; `.../typescript/errors` still refused by name); the first is RED against the previous version. Round 10's deriver allowlist untouched — a non-test namespace import of the root now rewrites cleanly *and* is still reported by `make test-api-package`, which is correct: spelling and value-derivability are different questions. All gates green incl. six browser guards and the Metro gate by hand (7s, 1592 modules).

## Round 13 (2026-09-14)

Head **`ea24e102c`**; `origin/main` already merged. `import { type Foo } from "x"` is erased exactly as `import type { Foo }` is, but the statement carries no type-only flag — only the members do, and `isLoadedAtRuntime` read only the statement, so the native script walker followed erased dependencies. The binding shapes decide it now, **with emptiness checked before the `some()`**: a site naming no binding (star re-export, side-effect import, bare require) is loaded regardless, and an `every()` over no members would have called each of those erased. Cases: all-inline-type → not loaded, mixed → loaded, `export { type X } from` → not loaded, the three no-binding forms → loaded, plus two walker fixtures. One reader case and one walker case are RED against the previous predicate. All gates green incl. six browser guards and the Metro gate by hand (7s, 1592 modules).
