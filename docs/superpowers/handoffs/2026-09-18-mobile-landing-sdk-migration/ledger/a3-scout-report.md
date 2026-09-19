# A3 execution checklist — `git mv cmd/evener-hub/frontend/src/protocol` → `appwire-client/typescript/`

Scouted read-only against `origin/main` @ `31a5a4370` ("refactor(protocol): move
composerInput into the package (#1229)") on 2026-09-12. Plan text read from
`refs/pull/1182/head:docs/superpowers/plans/2026-09-12-sdk-migration.md`
(rows A3/A4, "Temporary seams", decision 3).

Everything below is a literal verified at that commit. Line numbers are
`git show origin/main:<path>` line numbers.

---

## 0. Summary of counts

| Category | Count |
| --- | --- |
| Build/tool config files needing an edit (Vite, vitest, tsconfig, Metro, biome, guard configs) | 9 |
| Gate/Make files | 2 (`make/testing.mk`, `make/linting.mk`) |
| CI workflow files | 2 (`ci.yml` ×2 lines, `ios-testflight.yml` ×1) |
| Go functional (generator + tests + audit) | 5 files / 6 lines |
| Go comment-only mentions | 6 files / 6 lines |
| Scenario cards with **audit-enforced** citations | **8 cards / 12 citations** (plan names 2 cards / 5 citations) |
| Active docs | 6 files (2 executable, 4 prose) |
| Package-internal files that move but need no edit | `scripts/qualify-package.mjs`, `scripts/discovery-contracts.mjs`, `.gitignore`, `tsconfig.build.json`, `package.json`, `README.md` |
| Source import sites that must keep compiling | **847** (689 web + 130 mobile-native `.ts/.tsx` + 22 `mobile/src` + 6 `mobile-native/scripts/*.mts`) |

---

## 1. Every non-source file that hard-codes the path

### 1a. Build / tool configuration

Note up front: **nothing in the tree resolves `@evener/appwire-client` by name
today.** There is no `paths` block in the web tsconfig, no `resolve.alias` in
any Vite/vitest config, no Metro alias, and no `@evener/appwire-client`
dependency in any `package.json` (verified: the only in-tree uses of the name
are the package's own `package.json`, `README.md`, `examples/connection.mjs`,
and two `node_modules/@evener/appwire-client/...` strings inside the
qualification runner, which are paths in the *packed consumer*, not the repo).
So A3's config work is mostly **additive**, not a rewrite of existing aliases.

| # | File | Line(s) | Literal today | Must become |
| --- | --- | --- | --- | --- |
| 1 | `cmd/evener-hub/frontend/vite.config.ts` | 57 | `allow: [searchForWorkspaceRoot(__dirname), fs.realpathSync(path.join(__dirname, "node_modules"))]` | Add the package directory (or the repo root). **Empirically verified**: `searchForWorkspaceRoot("<repo>/cmd/evener-hub/frontend")` returns the frontend directory itself (run against the checked-in Vite 8 build in a sibling worktree) — there is no root `package.json`, no `pnpm-workspace.yaml`/`lerna.json`/`rush.json`/`workspace.json`, and Vite's `ROOT_FILES` no longer includes `.git`. After the move the package is **outside** `fs.allow`, so every `/@fs/` request for it 403s in `npm run dev` and in all five browser guards. See Risk R1. |
| 2 | `cmd/evener-hub/frontend/vite.config.ts` | 112 | `include: ["src/**/*.{ts,tsx}"]` (coverage) | Add `"../../../../appwire-client/typescript/**/*.ts"` or accept the coverage denominator change (see Risk R4 / `coverage-floors.txt`). |
| 3 | `cmd/evener-hub/frontend/vite.config.ts` | 119-120 | `"src/protocol/fixtures/**"`, `"src/protocol/testing/**"` (coverage exclude) | Repoint to the new directory; as written both become dead entries. |
| 4 | `cmd/evener-hub/frontend/vite.config.ts` | 71-139 (`test:`) | no `include`, no `alias` | Must **add** `resolve.alias` for `@evener/appwire-client`, `@evener/appwire-client/docContent`, `@evener/appwire-client/testing/*`, **and** a `test.include`/`test.projects` entry that still collects the package's **25 `*.test.ts(x)` files** (plus `tokenFlood.bench.ts`). Vitest's root is the config's directory (the frontend), and the default include glob is root-relative, so the move silently stops running all 25. See Risk R2. |
| 5 | `cmd/evener-hub/frontend/tsconfig.json` | 16 (`"include": ["src"]`) — and **no `paths` key exists** | `{"include": ["src"]}` with `compilerOptions` lacking `baseUrl`/`paths` | Add `"baseUrl": "."` + `"paths": {"@evener/appwire-client": ["../../../../appwire-client/typescript/index.ts"], "@evener/appwire-client/docContent": [".../docContent.ts"], "@evener/appwire-client/testing/*": [".../testing/*"]}` and extend `include` to cover the moved directory (otherwise the package's own test files leave `tsc --noEmit`'s file set — they are not imported by app code). |
| 6 | `cmd/evener-hub/frontend/biome.jsonc` | 18, 22 | `"!!src/protocol/__snapshots__"`, `"!!src/protocol/types.gen.ts"` | Both become dead paths. Either drop them and add a Biome config/scope covering `appwire-client/typescript` (with the same two ignores repointed), or the generated `types.gen.ts` and the snapshot dir become lint/format targets somewhere new. **Related:** `cmd/evener-hub/frontend/package.json:13-15` runs `biome ci src` / `biome format --write src` / `biome check --write src` — after the move the package is **entirely unlinted and unformatted by CI**. See Risk R5. |
| 7 | `mobile-native/metro.config.js` | 11-14 | `context.originModulePath.startsWith(path.join(root, "cmd", "evener-hub", "frontend", "src"))` | Add a third branch for `path.join(root, "appwire-client", "typescript")`. `watchFolders = [root]` (line 5) already covers the new location, so no watch change is needed. Also add name resolution (`config.resolver.extraNodeModules["@evener/appwire-client"] = …`, or a `resolveRequest` prefix map) if A3 is expected to make the alias work for Metro — A4 depends on it. |
| 8 | `mobile-native/tsconfig.check.json` | 4-11 (`paths`) | `anser`/`react`/`tinykeys`/`vitest`/`zustand` only | Add the three `@evener/appwire-client*` entries. `include` (line 13) is `["**/*", "../mobile/src"]`, so the package is only reached transitively — fine. |
| 9 | `mobile-native/tsconfig.json` | — | no `paths` at all | Leave alone. This is exactly why A4 carves out `mobile-native/scripts/*.mts` (`tsx` never reads `tsconfig.check.json`). |
| 10 | `mobile-native/vitest.config.mts` | 7-17 (`resolve.alias`) | `zustand`, `anser`, `tinykeys` | Add the `@evener/appwire-client*` aliases so `npm test` and `npm run test:shared` resolve the name. |
| 11 | `cmd/evener-hub/frontend/scripts/editorial-preview.vite.config.mjs` | 14, 34 | `config.server.fs.allow = [frontend];` and `fs: { strict: true, allow: [frontend], … }` | Must include the package directory. This config **overwrites** the base `fs.allow` twice, so fixing `vite.config.ts` alone does not fix the editorial preview. |
| 12 | `cmd/evener-hub/frontend/scripts/browserguard.vite.config.mjs` | 21-30 | `mergeConfig(baseConfig, {server:{watch:null,hmr:false}})` | No literal to change — it inherits `vite.config.ts`. This is the resolver for **all five** guards (`layoutguard`, `overflowguard`, `shellguard`, `spawnguard`, `transcriptscrollguard` — enumerated at `scripts/web/test-web-browser.sh:40`, all launched through `browserGuardProcess.mjs:990` → `scripts/browserguard-vite.mjs:14`). Verify the alias and `fs.allow` survive the merge. |

`cmd/evener-hub/frontend/scripts/browserguard-vite.mjs:23-25` contains a
containment check (`resolved.startsWith(frontendRoot)`) that applies only to the
*Vite config file argument*, not to served modules — **no edit needed**, but do
not "fix" it in passing.

### 1b. Make gates

| # | File | Line | Literal | Must become |
| --- | --- | --- | --- | --- |
| 13 | `make/testing.mk` | 58 | `@cd cmd/evener-hub/frontend/src/protocol && NODE_DISABLE_COMPILE_CACHE=1 npm run qualification` | `@cd appwire-client/typescript && …` (`test-api-package`) |
| 14 | `make/linting.mk` | 190 | `outputs='docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/developing-evener/…'` | `appwire-client/typescript/types.gen.ts` (`lint-generated`) |

There is **no** `test-web-browser` target in `make/*.mk` carrying the path — it
delegates to `scripts/web/test-web-browser.sh`, which `cd`s to
`cmd/evener-hub/frontend` (line 12) and needs no change. Likewise
`scripts/web/test-web.sh:17`, `scripts/web/web-preflight.sh:35`,
`scripts/gate/run-module-tests.sh:69`, `scripts/gate/test-timing-budget.sh:86`,
`scripts/coverage/coverage-floor.sh:191`, `scripts/coverage/e2e-cover.sh:95,97`,
`scripts/ops/deploy-hub.sh:160`, `make/building.mk:84` and
`.github/actions/setup-toolchain/action.yml:21` all name
`cmd/evener-hub/frontend` (the app, which is not moving) — **verified, no edit**.

### 1c. CI workflows

| # | File | Line | Literal | Must become |
| --- | --- | --- | --- | --- |
| 15 | `.github/workflows/ci.yml` | 57 | `cache-dependency-path: cmd/evener-hub/frontend/src/protocol/package-lock.json` | `appwire-client/typescript/package-lock.json` |
| 16 | `.github/workflows/ci.yml` | 60 | `run: npm ci --prefix cmd/evener-hub/frontend/src/protocol` | `--prefix appwire-client/typescript` |
| 17 | `.github/workflows/ios-testflight.yml` | 74 | `npm ci --prefix cmd/evener-hub/frontend/src/protocol` | `--prefix appwire-client/typescript` |

`.github/dependabot.yml` has no npm entries — verified, no edit.

### 1d. Generator + Go (functional)

| # | File | Line | Literal | Must become |
| --- | --- | --- | --- | --- |
| 18 | `appwire/doc.go` | 29 | `//go:generate go run primeradiant.com/evener/internal/appwirets -out ../cmd/evener-hub/frontend/src/protocol/types.gen.ts` | `-out ../appwire-client/typescript/types.gen.ts` — **this is the producer**; miss it and `make generate` writes into a deleted directory |
| 19 | `appwire/protocol_test.go` | 335, 360 | `os.ReadFile("../cmd/evener-hub/frontend/src/protocol/types.gen.ts")` | `"../appwire-client/typescript/types.gen.ts"` |
| 20 | `internal/appwirets/emit_test.go` | 658 | `os.ReadFile("../../cmd/evener-hub/frontend/src/protocol/types.gen.ts")` | `"../../appwire-client/typescript/types.gen.ts"` (drift test `TestGeneratedFileCurrent`) |
| 21 | `makefiletargets_audit_test.go` | 1073 | `"cmd/evener-hub/frontend/src/protocol/types.gen.ts"` in the `generated` fixture list for `TestLintGeneratedRejectsOutputDeletedFromHEAD` | new path; must match `make/linting.mk:190` exactly |

`internal/appwirets/main.go` takes `-out` as a required flag with no default
(line 31, 35-36) — no hard-coded path in the tool itself.

### 1e. Go comment-only mentions (accuracy; nothing breaks)

`appwire/doc.go:8`, `internal/appwirets/main.go:2`, `cmd/evener-tui/hub_model.go:215`,
`cmd/evener-hub/e2e_control_invariant_test.go:276` (`frontend/src/protocol/reducer.ts`),
`server/appwire_runtime_test.go:487`, `server/appwire_turns.go:530`,
`agent/session_events.go:120`.

### 1f. Scenario-card citations — **AUDIT-ENFORCED, and the plan undercounts**

`scenariosourcecite_audit_test.go` (`TestScenarioSourceCitationsResolve`, root
Go module) resolves a backticked path **by suffix** against tracked
`.go/.ts/.tsx/.js/.jsx` files (`scenarioResolveCitedPath`, lines 708-716:
`candidate == cited || strings.HasSuffix(candidate, "/"+cited)`). `protocol/` is
a path segment in every one of these citations, so **none of them survives the
move** — `appwire-client/typescript/reducer.ts` does not end in
`/protocol/reducer.ts`. The card set it scans is `test/scenarios/*.md` **plus**
`docs/developing-evener/agentic-testing.md` (line 213).

**12 citations across 8 cards** (the A3 row names only the first two files):

| File:line | Literal | Must become |
| --- | --- | --- |
| `test/scenarios/ask-two-clients.md:78` | `` `cmd/evener-hub/frontend/src/protocol/askAnswers.ts:92-100` `` | `` `appwire-client/typescript/askAnswers.ts:92-100` `` |
| `test/scenarios/ask-two-clients.md:168` | `` `cmd/evener-hub/frontend/src/protocol/deriveAskQuestions.ts:62-97` `` | new path |
| `test/scenarios/ask-two-clients.md:171` | `` `cmd/evener-hub/frontend/src/protocol/askShared.ts:118-150` `` | new path |
| `test/scenarios/ask-web-answer.md:219` | `` `cmd/evener-hub/frontend/src/protocol/askShared.ts:118-150` `` | new path |
| `test/scenarios/ask-web-answer.md:225` | `` `cmd/evener-hub/frontend/src/protocol/askAnswers.ts:92-100` `` | new path |
| `test/scenarios/attention-needs-you-end-to-end.md:279` | `` `protocol/submitRouting.ts:18-23` `` | `` `appwire-client/typescript/submitRouting.ts:18-23` `` |
| `test/scenarios/tui-effort-command.md:92` | `` `protocol/reducer.ts:702-705` `` | new path |
| `test/scenarios/web-model-switch-mid-session.md:99` | `` `protocol/reducer.ts:685-700` `` | new path |
| `test/scenarios/web-model-switch-mid-session.md:111` | `` `protocol/errors.ts:63-67` `` | new path |
| `test/scenarios/web-queue-then-drain-as-steer.md:15` | `` `protocol/submitRouting.ts:33-39` `` | new path |
| `test/scenarios/web-steer-live-turn.md:13` | `` `protocol/submitRouting.ts:33-39` `` | new path |
| `test/scenarios/workspace-title-bar-actions.md:93` | `` `protocol/submitRouting.ts:19-23` `` | new path |

A shortened form such as `` `typescript/reducer.ts:702-705` `` also resolves
(suffix match), if the executor prefers the cards to read well.

`docs/developing-evener/agentic-testing.md:913` names
`` `frontend/src/protocol/types.gen.ts` `` with **no anchor**, so it is a
"mention" and not audited — update for accuracy only.

### 1g. Active documentation

| File:line | Literal | Must become |
| --- | --- | --- |
| `docs/design/mobile/ios-build-distribution.md:9` | `npm ci --prefix cmd/evener-hub/frontend/src/protocol` | **executable** — new prefix |
| `docs/developing-evener/conventions/go-workspace.md:211` | `` (`cmd/evener-hub/frontend/src/protocol/types.gen.ts`) `` | new path |
| `docs/developing-evener/agentic-testing.md:913` | `` `frontend/src/protocol/types.gen.ts` `` | new path |
| `docs/web-ui/specs/2026-09-03-keybinding-system-survey.md:76` | `` `src/protocol/types.gen.ts:2118-2120, 2295-2297` `` | new path (not audited — only `test/scenarios/` + `agentic-testing.md` are) |
| `docs/web-ui/specs/2026-09-03-keybinding-system-survey.md:91` | `` `protocol/testing/fakeClient.ts` `` | new path |
| `docs/web-ui/parity/parity-m6-surfaces.md:166` | `` `frontend/src/protocol/attachmentMarkers.ts` `` | new path |
| `docs/web-ui/decisions.md:345, 474, 533` | `protocol/toolCallText.ts:tailFold`, `protocol/client.ts:request`, `protocol/reducer.ts:imagesToStrings` | new path |
| `AGENTS.md:35-44` | "Biome's enforced scope is `src/` only (the gate runs `biome ci src`…)" | Must be rewritten if A3 adds a second Biome scope, or must explicitly say the package is out of the gate (which would be a regression — see R5). |
| `docs/developing-evener/{README,building,testing,linting,fuzzing,coverage}.md` | generated from the `##` doc comments in `make/*.mk` | regenerate via `make generate` and commit (editing `make/testing.mk:52-56` prose is optional but the target body change alone does not alter these) |
| `cmd/evener-hub/frontend/src/protocol/README.md` | `node node_modules/@evener/appwire-client/examples/discovery.mjs` (line 74) | **no change** — consumer-side path, not repo-side |

**Not swept** (dated records, per the plan): everything under
`docs/superpowers/{plans,specs}/`.

### 1h. Scripts reading the package via `__dirname` / `import.meta`

Exhaustively checked. Only two, both **self-relative and safe under `git mv`**:

- `cmd/evener-hub/frontend/src/protocol/scripts/qualify-package.mjs:12` —
  `const packageDir = resolve(dirname(fileURLToPath(import.meta.url)), "..")`;
  also `:412` `resolve(packageDir, "node_modules/.bin/tsc")`. It never reaches
  above the package, so it moves cleanly — but it **requires
  `npm ci --prefix <new path>` to have run** (items 16, 17, and the docs line).
- `cmd/evener-hub/frontend/src/protocol/scripts/discovery-contracts.mjs:21` —
  resolves inside the temporary packed consumer only.

`cmd/evener-hub/frontend/scripts/*` contains **no** reference to the package
(the three `protocol` hits there are `location.protocol` / `endpoint.protocol` /
`socket.protocol` — verified false positives).

---

## 2. Source-import sites that must keep compiling — and the seam choice

Counted at `origin/main` by grep over import lines (not files):

| Tree | Sites | Files | Depths present |
| --- | --- | --- | --- |
| `cmd/evener-hub/frontend/src` (relative `…/protocol/x`) | **689** | 387 | 5 distinct: `./protocol/` ×3, `../protocol/` ×150, `../../protocol/` ×164, `../../../protocol/` ×176, `../../../../protocol/` ×196 |
| `mobile-native` `.ts`/`.tsx` | **130** | 106 | **1**: `../../cmd/evener-hub/frontend/src/protocol/` ×130 |
| `mobile/src` | **22** | 14 | **1**: `../../../cmd/evener-hub/frontend/src/protocol/` ×22 |
| `mobile-native/scripts/*.mts` (A4 carve-out) | **6** | 5 | `../../cmd/evener-hub/frontend/src/protocol/` |
| **Total** | **847** | 512 | |

(The plan's A4 row cites 652 web / 116 native / 22 mobile at its baseline; the
web and native numbers have grown with the landed C rows. Re-measure at the A3
merge base.)

Related facts that change the calculus:

- **`vi.mock` on a protocol path: 0.** Verified across all three trees — there
  is not a single `vi.mock`, `vi.doMock`, `importActual` or `importMock`
  pointing into `protocol/`.
- **Namespace imports of a protocol module: exactly 1** —
  `cmd/evener-hub/frontend/src/panes/doc/DocPane.test.tsx:5`
  (`import * as docContentModule from "../../protocol/docContent"`, spied at
  `:44`). This is the one site a re-export stub is *not* guaranteed to satisfy:
  `vi.spyOn` on a binding that a stub re-exported from another module patches
  the stub's binding, not the original's. See R7.
- **Distinct specifiers web imports: 30** (25 top-level modules +
  `testing/{fakeClient,fakeSocket,hubWireFixtures,notifications,tokenFlood}`).
  The union across all trees is 31 (`activityList` is native-only).
- The package is **76 tracked files**, of which **25 are `*.test.ts(x)`** plus
  `tokenFlood.bench.ts`, `__snapshots__/reducer.test.ts.snap`, 4 `fixtures/*.jsonl`
  and 6 `examples/*.mjs`.

### Recommendation: **stubs, not a relative-path rewrite.**

Reasoning, per tree and per depth:

1. **Web is where the cost lives, and rewriting it makes the paths longer.**
   `cmd/evener-hub/frontend/src/` sits four levels below the repo root, so every
   one of the 689 web lines would gain exactly four `../` segments
   (`../../protocol/x` → `../../../../../../appwire-client/typescript/x`), across
   five distinct depth buckets. That is a 689-line diff that A4 then rewrites
   again three PRs later — pure churn, and A4's own "the non-import diff is
   empty" review technique gets applied twice to the same lines.
2. **A stub directory is ~30 one-line files** at
   `cmd/evener-hub/frontend/src/protocol/<module>.ts`, each
   `export * from "../../../../appwire-client/typescript/<module>";` (plus 5
   under `testing/`). A4 deletes the directory wholesale — the plan already
   names that as seam 1.
3. **Stubs at the old path also cover both mobile trees for free.** Every
   mobile-native (130), `mobile/src` (22) and `mobile-native/scripts` (6) import
   is spelled `…/cmd/evener-hub/frontend/src/protocol/<module>`, which resolves
   straight through the stub. Metro's existing `resolveRequest` origin check
   (`metro.config.js:11-14`) matches the stub path unchanged, so A3 need not
   touch Metro's shared-source branch at all for the *old* imports.
4. **The cheap-rewrite half is real but small.** Both mobile trees are at a
   *single* uniform depth, so each is one `sed`:
   `../../cmd/evener-hub/frontend/src/protocol/` → `../../appwire-client/typescript/`
   (130 + 6 lines) and `../../../cmd/…/protocol/` → `../../../appwire-client/typescript/`
   (22 lines). Those rewrites also *shorten* the specifier and are exactly what
   the A4 row says A3 should do for the `.mts` carve-out anyway.

**So: rewrite the 158 mobile lines (3 mechanical seds, no depth variation,
paths get shorter), stub the 689 web lines (30-31 stub files, deleted by A4).**
That is ~190 changed lines + ~31 new files instead of ~847 rewritten lines, and
it leaves A4 exactly the work its row describes.

**Files both trees import** (would need a stub either way, and are the ones to
smoke-check first if stubs are used): `activityData`, `activityRows`,
`askAnswers`, `attachmentMarkers`, `catalogCommands`, `client`, `composerInput`,
`deriveAskQuestions`, `displayFormat`, `docContent`, `errors`, `itemFailure`,
`jobOutput`, `sessionErrors`, `stableDelegate`, `submitRouting`, `transport`,
`types.gen` — **18 modules**. `transport` and `types.gen` are additionally
imported by the five `.mts` scripts, and `types.gen` is the generated file, so a
hand-written stub named `types.gen.ts` will sit at a path the generator no
longer writes: keep it out of `make/linting.mk:190`'s `outputs` list and out of
`makefiletargets_audit_test.go:1073`, both of which must already point at the
new location.

---

## 3. Does anything resolve `@evener/appwire-client` by name today? **No.**

Verified facts:

- **No root `package.json`** at all (`git cat-file -e origin/main:package.json`
  fails). Therefore no npm workspaces anywhere in the repo.
- `cmd/evener-hub/frontend/package.json` — `dependencies` (lines 23-38) and
  `devDependencies` (39-55) contain **no** `@evener/appwire-client`, no `file:`
  link, no `link:`.
- `mobile-native/package.json` — same; the A4 row already states this.
- `cmd/evener-hub/frontend/tsconfig.json` has **no `paths` and no `baseUrl`**.
- `mobile-native/tsconfig.check.json:4-11` has `paths`, but only for
  `anser`/`react`/`tinykeys`/`vitest`/`zustand`.
- `mobile-native/vitest.config.mts:7-17` aliases only those three runtime deps.
- `mobile-native/metro.config.js` has no alias/`extraNodeModules` at all.
- The only in-tree occurrences of the string `@evener/appwire-client` are the
  package's own `package.json:2`, `README.md:1,74`,
  `examples/connection.mjs:1`, and two packed-consumer `node_modules/...` paths
  inside the qualification scripts.

**What A3 must add so the name resolves for web, mobile-native, vitest and Metro
without publishing** (all four are independent resolvers — a green `tsc` proves
none of the others):

1. **Web tsc** — `paths` in `cmd/evener-hub/frontend/tsconfig.json` (needs
   `baseUrl` or `paths` relative to the config with `moduleResolution: "bundler"`),
   three entries: root, `/docContent`, `/testing/*`. Point at **source `.ts`**,
   not `dist/` — `dist/` is gitignored (`protocol/.gitignore:1`) and never built
   in a dev/test flow.
2. **Vite / vitest (web)** — `resolve.alias` in `vite.config.ts`, inherited by
   `browserguard.vite.config.mjs` and `editorial-preview.vite.config.mjs`.
   `paths` in tsconfig is **not** honoured by Vite; the alias is mandatory.
3. **Vite `server.fs.allow`** — item 1 above. Alias without `fs.allow` gives a
   403 at dev-server runtime with a green typecheck.
4. **mobile-native vitest** — `resolve.alias` in `mobile-native/vitest.config.mts`
   (used by both `npm test`, root = `mobile-native`, and `npm run test:shared`,
   root = repo root).
5. **mobile-native tsc** — `paths` in `tsconfig.check.json` only
   (`tsconfig.json` has none by design; `tsx` reads neither).
6. **Metro** — `config.resolver.extraNodeModules["@evener/appwire-client"]`
   (and the two subpaths, which `extraNodeModules` does **not** cover — a
   `resolveRequest` prefix branch is the reliable form), plus the origin-path
   branch at `metro.config.js:11-14`.
7. **The package's own `exports` map stays unchanged.** The in-repo `testing`
   specifier is deliberately absent from `package.json` `exports` (plan, "Where
   test support lives") and must stay absent, or A3c's manifest/`exports`
   equality assertion in `qualify-package.mjs` fails.

A root `package.json` with `workspaces` would fix `searchForWorkspaceRoot` and
give real `node_modules/@evener/appwire-client` symlinks in one move, but it
changes npm topology for three independent lockfiles
(`cmd/evener-hub/frontend`, `mobile-native`, the package) and for CI's three
separate `npm ci --prefix` steps. **Not recommended inside A3**; explicit
aliases plus an explicit `fs.allow` entry are the smaller change.

---

## 4. Gates an A3 executor must run

The plan's "all four gates plus `make generate` and `make lint`" expands to:

| Gate | Command | Needs a path edit in this PR? |
| --- | --- | --- |
| Web unit + typecheck + Biome | `make test-web` | **Yes** — vite/vitest/tsconfig/biome (items 1-6) |
| Browser guards (all five) | `make test-web-browser` | **Yes** — `fs.allow` in `vite.config.ts` **and** `editorial-preview.vite.config.mjs`. Chrome-capable host required; CI runs it in the `web` job (`.github/workflows/ci.yml:29`). |
| Native | `make test-native` (= `npm test && npm run test:shared && npm run check` in `mobile-native`) | **Yes** — metro/vitest/tsconfig.check (items 7-10) |
| Package qualification | `make test-api-package` | **Yes** — `make/testing.mk:58`; and `npm ci --prefix <new path>` must have run first |
| Generated-output freshness | `make generate && make lint` (`lint-generated`, `make/linting.mk:189-190`) | **Yes** — `appwire/doc.go:29` + `make/linting.mk:190` + `makefiletargets_audit_test.go:1073` must agree |
| Root Go tests | `ROOT_FULL=1 make test` | **Yes** — `scenariosourcecite_audit_test.go` (12 citations), `makefiletargets_audit_test.go:1073`, `appwire/protocol_test.go:335,360`, `internal/appwirets/emit_test.go:658` |
| Build | `make build` / `make build-web` | No path edit, but `vite build` is the one surface `fs.allow` does **not** gate, so a green build proves nothing about the guards |
| Coverage floor (separate gate) | `scripts/coverage/coverage-floor.sh` | **Probably** — see R4 |

Everything above is `make merge-approval-gate` (`make/testing.mk:98-102`) plus
`make test-web-browser`, which that target explicitly does *not* run
(`make/testing.mk:94-95`).

---

## 5. Risks the A3 row does not mention

**R1 — Vite `server.fs.allow` excludes the new location; `npm run dev` and all
five browser guards 403.** *(Highest.)* `vite.config.ts:57` sets
`allow: [searchForWorkspaceRoot(__dirname), realpath(node_modules)]`. I ran
Vite 8's `searchForWorkspaceRoot` against this checkout's frontend directory: it
returns **the frontend directory itself**, because there is no root
`package.json`, no `pnpm-workspace.yaml`/`lerna.json`/`rush.json`/`workspace.json`,
and `.git` is not in Vite's `ROOT_FILES`. Moving the package to a repo-root
sibling puts every package module outside the allow list, so the dev server and
every guard serve a 403 for it — while `tsc --noEmit`, `vite build` (Rollup, no
fs gate) and `vitest` (SSR transform, no fs gate) all stay green. The plan's A3
risk cell warns that "a green `make test-web` does not prove
`make test-web-browser` resolves the alias"; this is the concrete mechanism, and
it needs **two** fixes (base config **and** `editorial-preview.vite.config.mjs`,
which overwrites `fs.allow` at lines 14 and 34).

**R2 — the package's 25 test files silently stop running.** Vitest's root is the
config file's directory (`cmd/evener-hub/frontend`) and the default `include`
glob is root-relative. After the move, `appwire-client/typescript/*.test.ts` is
outside the collection set: `make test-web` goes green having run 25 fewer files
(`activityData`, `activityList` ×2, `activityMerge`, `activityRows`,
`askAnswers`, `askShared`, `attachmentMarkers`, `client`, `deriveAskQuestions`,
`displayFormat`, `docContent`, `errors`, `itemFailure`, `jobOutput`,
`publicExports`, `reconnect`, `reducer` ×2, `sendQueueAvailability`,
`submitRouting`, `testing/fakeClient`, `tokenFlood`, `toolCallText`, and the
`?raw` fixture-driven `reducer.test.ts`). Nothing in the tree asserts a floor on
the web suite's file count. A3 must add an explicit `test.include` /
`test.projects` entry (or a dedicated vitest project for the package) **and**
check the before/after test count.

**R3 — the package's test files import bare specifiers that will no longer
resolve.** `tokenFlood.test.tsx` imports `react`, `@testing-library/react` and
`vitest`; other test files import `vitest`. Those resolve today by walking up
from `cmd/evener-hub/frontend/src/protocol/` into
`cmd/evener-hub/frontend/node_modules`. From `appwire-client/typescript/` the
walk reaches the repo root, which has **no `node_modules`**. Whatever config
change fixes R2 must also give those bare imports a resolution root (a vitest
project rooted at the frontend with an added `include`, an alias set, or a
`node_modules` link) — and `reducer.test.ts:5-8` uses Vite-only `?raw` imports,
so the package's tests cannot simply be run by plain `tsc`/`node` instead.

**R4 — the web coverage floor moves.** `vite.config.ts:112` puts all of `src/`
in the coverage denominator and `:119-120` excludes only
`src/protocol/{fixtures,testing}`. `scripts/coverage/coverage-floors.txt:34`
pins `web 96.1`. Removing ~45 heavily-tested package source files from the
denominator (and, per R2, their tests from the numerator) shifts that number in
an unpredictable direction. Either repoint `coverage.include` at the new
directory or re-bless the floor in the same PR, with the delta stated.

**R5 — the package becomes unlinted and unformatted.** `biome ci src` /
`biome format --write src` / `biome check --write src`
(`cmd/evener-hub/frontend/package.json:13-15`) are scoped to `src`, and
`AGENTS.md:36-37` states that scope as policy. After the move nothing in CI runs
Biome over the package's ~45 source files, and the two `biome.jsonc:18,22`
ignore entries (`src/protocol/__snapshots__`, `src/protocol/types.gen.ts`)
become dead — so if a new scope is added without repointing them, the 2.9 MB
generated `types.gen.ts` and the reducer snapshot enter the lint set.
`mobile-native` also ships its own `@biomejs/biome` devDependency but no biome
config at `mobile-native/`, so it is not an alternative home.

**R6 — seven scenario-card citations beyond the two files the plan names.**
Section 1f. `TestScenarioSourceCitationsResolve` is a root-module Go test, so it
fails under `make test`, not under any frontend gate — an executor who runs only
the "four gates" for the frontend will not see it until `ROOT_FULL=1 make test`.

**R7 — `DocPane.test.tsx:5`'s namespace import + `vi.spyOn` under a re-export
stub.** It is the only namespace import of a protocol module in the tree, and
`:44` does `vi.spyOn(docContentModule, "readDocFile")`. If A3 leaves a
`export * from "…"` stub at `protocol/docContent.ts`, the spy patches the
*stub's* re-exported binding while `DocPane.tsx` imports the same stub — which
happens to work, but only because both go through the stub. Prove it rather than
assume it, or rewrite that one file's two lines onto
`@evener/appwire-client/docContent` in A3 (A3d already published that subpath).

**R8 — web `tsc --noEmit` stops covering the package's own test files.**
`cmd/evener-hub/frontend/tsconfig.json:16` is `"include": ["src"]`. App code
pulls the package's *source* modules in transitively, so those stay
typechecked — but the 25 test files and `tokenFlood.bench.ts` are imported by
nothing, so they leave the program entirely. Combined with R2 that means a type
error in a package test is invisible to every gate.

**R9 — `types.gen.ts` at two paths during the seam.** If A3 uses stubs and names
one `types.gen.ts`, the repo carries a hand-written `types.gen.ts` at the old
path and the generated one at the new path. `make/linting.mk:190` and
`makefiletargets_audit_test.go:1073` must name only the new one, and
`biome.jsonc:22`'s ignore must not accidentally keep matching the stub. Consider
naming the stub file differently (`types.gen.ts` re-exporting is unavoidable if
web imports `…/protocol/types.gen`, which it does — 689-line set includes it).

**R10 — the A3-row Metro claim is thinner than it reads.** The row says "Metro
config" as if it were a path swap. `metro.config.js` today has **no alias at
all**; A3 has to *introduce* name resolution there, and Metro ignores both
tsconfig `paths` and `package.json` `exports` subpaths unless configured. Since
no gate bundles the native app (CI runs `vitest` + `tsc`, not Metro), a broken
Metro alias ships green — the same class of failure as R1, with no guard at all.

**R11 — sequencing, as the plan says: A3 must not land while any phase-C
relocation PR is open.** At `31a5a4370` the plan's status section lists C5
(#1229, now merged), C9 (#1234) and C12 (#1233) in flight. Confirm all are
merged or closed before starting.
