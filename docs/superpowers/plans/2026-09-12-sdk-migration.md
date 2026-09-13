# Plan: migrate both frontends onto the shared AppWire SDK package

Written 2026-09-12 against `92561dbe3`. Reads on
`docs/design/2026-09-12-sdk-migration-inventory.md`, which holds the evidence
for every claim about what a module owns and who imports it.

Someone else executes this one PR at a time. Nothing here is approved yet;
issue #1116 item 7 records that "no backward-compatibility layer or large
state-store extraction has been approved by this landing task."

## Rules the sequence obeys

1. Every PR leaves both frontends working and `make test-web`,
   `make test-native` and `make test-api-package` green. A PR that touches the
   package also re-runs `make test-web-browser`, because the five browser guards
   build their own entry bundles and resolve imports independently.
2. A PR migrates at most one store/module — **plus that module's pure
   transitive dependencies, when they cannot compile apart from it.** Splitting
   those out does not make the change smaller, it makes an intermediate commit
   that does not build. The rows using this allowance say so: C1
   (`immutable.ts` under `codec`/`merge`), C3 (the keybindings group), C11
   (`askShared.ts` and `tools/helpers.ts`), C17 (the spawn trio), C18 (the
   launchShared trio plus `LaunchConfigLayerName`), C21 (`catalogView.ts` under
   `pickerRows`) and C25 (the task trio). The other exception is a pure
   packaging/plumbing PR — phase A, A3c included — which may touch many files as
   long as no behavior changes.
3. Duplication is removed in the same PR that introduces the shared version.
   A PR that adds a shared module and leaves both copies in place is not done.
4. The web store's existing public interface and its existing test file stay as
   the contract; the store's implementation becomes a thin adapter over the
   shared module. The test file is edited only where it reaches inside the
   implementation.
5. No backward-compatibility layers. Two temporary seams are listed explicitly
   below, each with the PR that removes it.
6. `types.gen.ts` changes only via `make generate`. No PR here edits it.
7. **A PACKAGE CANDIDATE with one consumer does not move.** Relocating a module
   into the package "for later" adds a boundary and buys nothing. The 18
   single-consumer candidates in the inventory stay where they are until a
   second consumer arrives in the same PR. This is why phase C schedules 25
   relocations, not 55. The phase-C table carries 27 rows: those 26 — C11 is two
   PRs — plus C24,
   which is not a relocation but the doc adapters for a module #1184 already
   shipped and A3d already published. Three deliberate exceptions are labelled where they
   occur: `secureUUID.ts` (moves inside D26), `docContent.ts` (C24), and
   `stores/navigation/immutable.ts` (C1). The third is a different kind:
   a **transitive dependency**. `codec.ts:8` and `merge.ts:10` both import it,
   so it crosses the boundary with them or they arrive broken. Rule 7 is about
   not creating a boundary for its own sake; carrying a 25-line dependency
   across one already being drawn does not create one.

## Rows that stay put, permanently

These never move, and a PR that proposes moving one is wrong:
`stores/mutationOutboxIndexedDB.ts`, `stores/prefs.ts`,
`stores/panelStoreEviction.ts`, `stores/testing/stalledIndexedDB.ts`,
`keybindings/dispatcher.ts`, `transcriptDisplay/renderContext.tsx`,
`shell/{workspace,routing,paneRegistry,chromeStore,deletedSessionPanes,sessionCycle}.ts`,
`notifications/{channels,favicon,leader,title}.ts`, `widgets/toast/store.ts`,
`panes/session/composer/draft.ts`, `shell/rail/railExpansion.ts`, `auth.ts`,
and on the native side
`draftRepository.ts`, `nativeLocation.ts`, `nativeImagePicker.ts`. They are
browser or device storage, DOM event routing, dockview, `window.location`, or
`expo-*`. `widgets/codeblock/ansi.ts` also stays put — see decision 1.

## Phase A — packaging and plumbing (8 PRs)

| # | Title | Scope | Oracle | Lines | Deps | Risk |
| --- | --- | --- | --- | --- | --- | --- |
| A1 | Ship the unpacked protocol modules | In: `protocol/tsconfig.build.json` (`files` goes 6 → 16; as executed this includes `docContent.ts`, see C24), `package.json` exports, `index.ts`, `scripts/qualify-package.mjs`. Out: every app file. Deleted: nothing | `make test-api-package`, extended so **all four** generated consumer programs (`esm.mts`:29, `commonjs.cts`:51, `esm-runtime.mjs`:76, `commonjs-runtime.cjs`:85) exercise each newly shipped module under ESM and CJS. The pre-A1 baseline is nine export statements / **eleven** runtime exports — scope off symbols, not statements, and derive the list from one shared manifest rather than retyping it four times | ~200 | — | A module that compiles under `lib: ["ES2022","DOM"]` but needs a browser global at runtime ships silently if the runner only imports it; **#1184 answers this by smoke-calling every shipped module**. `docContent.ts` was planned as held back whole for the same reason; #1184 split it at the entry point instead, which is the better line: the module ships and its pure URL builders are exported and asserted (`qualify-package.mjs:110-111,155`), while `readDocFile` is deliberately left out of `index.ts` because it hardcodes a relative URL and the browser `fetch` global. C24 keeps the port and the re-export |
| A2 | Move `AppwireClientLike` out of `protocol/testing/` | In: new `protocol/clientLike.ts`, **exported from `index.ts` and added to the build `files`** so a package-name import can resolve it; the **25** files that import the type rewritten. Out: the ~111 files that import `FakeClient` — those keep their `testing/fakeClient` import. Deleted: the declaration at `testing/fakeClient.ts:40`. `protocol/testing/` stays where it is and keeps its ~111 `FakeClient` importers untouched — A3 moves it and A4 renames those imports, see below | `tsc --noEmit` in both apps; `make test-api-package` for the new export; `make test-web` + `make test-native` otherwise unchanged | ~150 | A1 | Rewriting all 136 `fakeClient` importers would break every test that constructs a `FakeClient`. The type is structural, so a partial rewrite still compiles; grep for the old path in the same PR |
| A3 | Relocate the package to a top-level directory and alias it | In: `git mv` of `protocol/`, plus **every hard-coded reference to the old path**, which is eight functional files in four categories — generator: `appwire/doc.go:29` (the `go:generate` `-out`); gates: `make/testing.mk:58` (`test-api-package`) and `make/linting.mk:190` (the generated-output freshness list); Go tests: `internal/appwirets/emit_test.go:658`, `appwire/protocol_test.go:335,360`, `makefiletargets_audit_test.go:1073`; CI: `.github/workflows/ci.yml:57,60` and `.github/workflows/ios-testflight.yml:74`. Then `tsconfig.paths`, `vite.config`, `vitest.config`, `mobile-native/tsconfig*.json`, Metro config, the five browser-guard runners. **Active Markdown too:** `docs/design/mobile/ios-build-distribution.md:9` runs `npm ci --prefix cmd/evener-hub/frontend/src/protocol`, and `docs/developing-evener/conventions/go-workspace.md:211`, `docs/developing-evener/agentic-testing.md:913` and `test/scenarios/ask-{two-clients,web-answer}.md` cite the path as current. The dated records under `docs/superpowers/{plans,specs}/` name it too and are **not** swept — they describe the tree as it was. **`protocol/testing/` moves with the directory** and stays out of the tarball; the aliases resolve a second in-repo specifier for it (see "Where test support lives"). Deleted: nothing | all four gates plus `make generate` and `make lint` — the freshness gate in `make/linting.mk:190` is the one that catches a missed generator path | ~320 | A1 (content first, location second — A1 ships modules into today's directory, A3 relocates whatever is there), decision 3 | **The reviewer's High finding: without these, the next `make generate` writes `types.gen.ts` to the deleted location and the package gate `cd`s into a directory that no longer exists.** Seven further mentions are comments — `appwire/doc.go:8` (a second line in a file A3 already edits), `internal/appwirets/main.go:2`, `cmd/evener-tui/hub_model.go:215`, `cmd/evener-hub/e2e_control_invariant_test.go:276`, `server/appwire_runtime_test.go:487`, `server/appwire_turns.go:530`, `agent/session_events.go:120` — sitting in six files beyond the eight functional ones, so fourteen files name the path in all (searching `*.go`, `*.mk`, `*.yml`, `*.yaml`, `*.sh`, `Makefile`). Update them for accuracy, but nothing breaks if one is missed. Metro and the guard bundlers resolve independently of `tsc`; a green `make test-web` does not prove `make test-web-browser` resolves the alias |
| A3b | Export from the root everything the apps actually import | In: `protocol/index.ts` and the runner's export manifest (`packageExports`). Measured at `27503c07d`, **14 symbols the apps import are not exported from `index.ts`**: twelve from `errors` — `ClientNotReadyError`, `GENERIC_ERROR_MESSAGE`, `HUB_UNREACHABLE_MESSAGE`, `errorKind`, `errorText`, `friendlyErrorMessage`, `friendlyLaunchErrorMessage`, `isHubLaunchError`, `isStaleCursorError`, `mutationErrorData`, `sessionActionError`, `sessionActionHeadline` — plus `reducer`'s `chunkViewBackingForTests` and `docContent`'s `readDocFile`. The twelve error declarations go to the root here; `chunkViewBackingForTests` is a white-box test hook and must not join the published surface, but it lives in `reducer.ts`, which the `testing` specifier (mapping only to `protocol/testing/`) cannot reach — **so A3 adds a non-shipped `testing/reducerHooks.ts` re-exporting it**, and A4 splits `reasoningFormat.test.ts:3` into two imports: `applyNotification` from the root, the hook from the testing specifier. Chosen over exempting that line, because A3 moves the whole directory and an exempted line would still need rewriting to a new relative path — an exemption buys nothing and leaves a white-box hook one root export away from being published; `readDocFile` stays held for C24. Deleted: nothing | `make test-api-package`; the runner's manifest is the oracle, so a symbol added to `index.ts` and not to `packageExports` is invisible — add both | ~90 | A1 | **Without this, A4 cannot compile.** `package.json` `exports` is `{".": …}` only, so a rewritten `import { errorText } from "@evener/appwire-client"` resolves to nothing unless the root exports it. Recommended over publishing one subpath per module: the package has exactly one entry today, adding a symbol is a one-line change in two files, and every extra subpath multiplies the qualification surface by an ESM and a CJS type check plus a runtime check. **Merged as `99fa1882f`** (#1206), which exports the twelve error helpers from the root and deliberately excludes `chunkViewBackingForTests` and `readDocFile`, over the 1,161 app files it measured |
| A3c | Make the qualification manifest subpath-aware | In: `qualify-package.mjs`. **As shipped by #1207**, `packageExports[specifier]` is `{ values, types, esmTypeUses, cjsTypeUses, smoke }`, emitting per specifier one ESM declaration check, one CJS declaration check and one runtime smoke block; `package.json` `exports` and the manifest must name exactly the same specifiers, each asserting the other. Reachability is separate: `shippedModules` is a flat, hand-maintained array of bare module names, and a module counts as reachable when it is some specifier's declaration entry (`./dist/<module>.d.ts`) or when an entry's `.d.ts` contains `from "./<module>"`. Out: every app file | `make test-api-package` | ~180 | A3b | **The rule for every subpath this plan adds** — `docContent` (A3d), `state/navigation` (C1): a specifier in `package.json` `exports` with no manifest entry is not qualified, whatever the gate says. The `testing` alias is the deliberate exception and needs no entry: in-repo only, never in `exports`, never in the tarball, validated by the apps' own `tsc --noEmit`. Pure plumbing, so rule 2's one-module limit does not apply. Was numbered C0; it moved into phase A once A4 turned out to need it |
| A3d | Publish `./docContent` as a subpath, with `readDocFile` behind an injected fetch | In: `package.json` `exports` gains `./docContent`, A3c's `packageExports` gains its entry with the runner's ESM and CJS checks, and `readDocFile` takes the transport as a **required third parameter**: `readDocFile(session, path, fetchDoc: DocFetch)` as A3d shipped it, where `DocFetch = (url: string) => Promise<DocResponseLike>` and `DocResponseLike` is a new published type — the minimal `{ ok, status, headers.get, arrayBuffer }` surface a real `Response` satisfies structurally. That indirection is not ceremony: a `Promise<Response>` in the `.d.ts` fails the runner's DOM-free declaration consumers. The web adapter lived at `panes/doc/browserDocFetch.ts` with its own test and still sent `credentials: "same-origin"` — C24 (#1221) has since renamed it `panes/doc/browserDocPort.ts` and widened the seam to `DocPort`, so `readDocFile(session, path, port: DocPort)` is today's signature — **and A3d wires the caller itself**: `DocPane.tsx:49` is the only `readDocFile` call site in either app, so it passes `browserDocFetch` here and the web is type-correct the moment A3d lands — no window in which a required third parameter has no argument. The root keeps the pure helpers (`DOC_FILE_MAX_BYTES`, `DocFileError`, `docFileRawURL`, `docImageURL`, and the two `DocFile*` types); `readDocFile`, `DocFetch` and `DocResponseLike` are published only at `./docContent`. Out: the base-origin and bearer/image work, which stays in C24 | `make test-api-package` for the new specifier; `DocPane.test.tsx` and `docFile.test.ts` unchanged. **Plus two the seam makes mandatory:** `protocol/docContent.test.ts` moves onto the injected-fetch seam — it stubs the global today, and a module that no longer reads the global would keep passing against a stub nothing calls — and the browser adapter gets a test proving it still sends `credentials: "same-origin"`, the behavior this PR promises not to change and which nothing else would catch losing | ~200 | A3c | **A4 cannot finish without this.** `DocPane.tsx:9` imports `readDocFile`, which is deliberately not a root export, so that line has no package-name form until `docContent` is published; and `DocPane.test.tsx:5` namespace-imports the module for `vi.spyOn`, which a root re-export can never satisfy but a real subpath does. The split tells A4 exactly where each import goes. **Re-measured at `303053dfb`, after C24: nine lines, not six** — four to the root and five to `@evener/appwire-client/docContent`. Root: `DocPane.test.tsx:6`, `browserDocPort.test.ts:2`, `docFile.test.ts:2`, `AgentMarkdown.tsx:3`, all of which name only `DOC_FILE_MAX_BYTES`, `DocFileError`, `docFileRawURL`, `docImageURL` or the two `DocFile*` types. Subpath: `DocPane.tsx:9` (`readDocFile`), `DocPane.test.tsx:5` (the namespace import), `browserDocPort.ts:1` (`DocPort`), and — outside the web tree — `mobile-native/src/nativeDocPort.ts:4` and `nativeDocPort.test.ts:2`. The review counted seven, which is the web tree alone; the two native lines are A4's to rewrite as well. Doing it here rather than at C24 is what lets **A4 leave zero exceptions behind** — the alternative, deferring every `docContent` import with exact-line carve-outs, means five lines across four files kept on relative paths through the whole of phase C, each an invitation to drift |
| A4 | Rewrite deep relative imports to the package name (test support included) | In: **the protocol imports only** — ~796 statements across all three trees: 652 web; **122** of `mobile-native`'s **210** — that is 116 of the 204 `.ts`/`.tsx` sites plus 6 of the 6 in `mobile-native/scripts/*.mts` (`auth-harness-smoke`, `check-hub`, `demo-hub` ×2, `launch-settings-smoke`, `plugin-fixture`); and **22** `mobile/src` (21 at the baseline; B1 added one when `project.ts` took up `itemFailure`), counted as lines matching `from "<any prefix>protocol/<module>"`, tests included. Out: `mobile-native`'s other **88** imports (all of them `.ts`/`.tsx`), which point at `transcriptDisplay/config`, `stores/navigation/codec` and friends — those modules are still in the web tree until phase C, so rewriting them here would produce specifiers that resolve to nothing. Each phase-C relocation rewrites its own non-protocol sites as part of that move. `mobile/src` is easy to forget because it is neither app; all 21 of its imports are protocol ones. Deleted: **the A3 re-export stubs at the old `protocol/` path** — seam 1 in "Temporary seams" below, and the reason this PR's own risk cell warns about stale deep imports outliving it | all four gates, plus a **zero-old-protocol-path check** scoped to what this PR actually moves: `grep -rn 'cmd/evener-hub/frontend/src/protocol/' mobile mobile-native --include='*.ts' --include='*.tsx' --include='*.mts' --exclude-dir=node_modules` returns nothing, **and the same sweep over `cmd/evener-hub/frontend/src` for the old relative specifier also returns nothing**. The web half is not optional: A3 leaves re-export stubs at the old path, so a stale deep import there keeps compiling and would outlive A4 in silence while the mobile half reads green — the `.mts` include is not optional, or those six script imports break silently. **And a grep is not enough for them:** `mobile-native/package.json` declares no dependency on `@evener/appwire-client` and no gate executes those scripts, so a rewritten import would typecheck against the repo alias and still fail to resolve at run time, in a file nothing runs. A4 adds a **runtime resolution check**, and it needs a purpose-built fixture rather than an existing entry: `check-hub.mts:7-9` takes `ORIGIN TOKEN_FILE` positionally and throws without them, has no `--help`, and imports only `import type { WebSocketLike }` — a type-only import, erased at build, so running it could not prove resolution even if it had arguments. The fixture is a new **`mobile-native/scripts/resolve-check.mts`**: no arguments, no network, importing a *runtime* export (`APPWIRE_PROTOCOL_VERSION`) from `@evener/appwire-client`, asserting it is a non-empty string and exiting 0. **A4 must also wire it into a gate that executes it**, or it proves nothing: a `mobile-native` package script `"check:package-resolution"`, invoked from `make test-native` in `make/testing.mk` beside the existing `npm test` / `test:shared` / `check` steps. Typechecking the fixture is exactly the failure mode it exists to catch, so "tsc covers it" is not an answer. A4 also ships whatever alias or `file:` dependency makes it resolve. Recommended over keeping those six on relative paths: a relative path into the package directory is the coupling this phase exists to remove, and "it is only a script" is how one unresolvable import survives to surprise someone later. Separately, and the same for the old relative protocol specifier inside the web tree. The broader `src/` grep must still return the 88 phase-C sites — a zero there would mean A4 over-reached. And an explicit resolution check: **every symbol the rewritten imports name resolves from the package**, which is what A3b exists to guarantee — `tsc --noEmit` in both apps is that oracle. **One documented exception, and only one:** `panes/doc/DocPane.test.tsx:5` does `import * as docContentModule from "../../protocol/docContent"` so that `:44` can `vi.spyOn(docContentModule, "readDocFile")`. A namespace import of one module cannot be satisfied by a root re-export — there is no per-module namespace object to spy on — and A3d exists to give it one: with `./docContent` published, that line becomes `import * as docContentModule from "@evener/appwire-client/docContent"` and the spy still has a real per-module namespace object. **So A4 leaves no exceptions at all** — not the DocPane namespace import (A3d), not `DocPane.tsx:9`'s `readDocFile` (A3d), not `reasoningFormat.test.ts:3`'s `chunkViewBackingForTests` (the `testing/reducerHooks.ts` split in A3). Both zero-old-path greps below are therefore absolute zeroes, with no carve-out to remember and nothing for phase C to inherit. No test assertion changes, only test imports | ~900 (import lines only) | A3, A3b, **A3c, A3d** | Large mechanical diff hides a semantic edit. Review with `--stat` plus a check that the non-import diff is empty. This PR is also where the ~111 `FakeClient` imports get their permanent specifier; leaving them on a relative path into a moved directory is the failure mode the reviewer caught |
| A5 | Delete the dead native conversation fixtures | Deleted: `mobile/src/dev/conversationFixtures.ts` (774 lines). Also widen `mobile-native` typechecking to every file under `mobile/src`, closing the gap that hid it | `make test-native` | ~780 (deletion) | — | **Merged as #1186 (`2245f9715`).** None; it had no importer and two dangling type imports |

A5 is independent and can land first if it is convenient.

## Phase B — lowest-risk duplicated logic (9 rows, 3 live PRs)

**Six of these rows were withdrawn after B7 stopped as NEEDS_CONTEXT** and every
DUPLICATED row was re-verified by grep at `f2599d1ed` (the rule is in the
inventory header). B1, B1b and B3b survive — all three have now landed; B2 became the C25 relocation and
B4 folded into D24. Withdrawn rows keep their numbers so the D-phase citations
below stay readable.

Each collapses one rule that is provably written twice — three times, for the
failure predicate. All pure functions; no wire calls, no storage, no React.
B1b is numbered out of sequence so the later D-phase references to B2-B7 stay
valid; it runs immediately after B1.

| # | Title | Scope | Oracle | Lines | Deps | Risk |
| --- | --- | --- | --- | --- | --- | --- |
| B1 | One settled-item failure predicate (**merged as `27503c07d`**) | In: new `itemFailure.ts` in the package. Deleted: the predicate in `transcriptDisplay/projector.ts:118-130` and `toolCallFailed`/`isInProgressStatus` in `mobile/src/conversation/project.ts:114-133` | `transcriptDisplay/projector.test.ts` and `mobile/src/conversation/project.test.ts` both keep their assertions and both point at the shared module | ~120 | A1 | **Merged as `27503c07d`.** The two predicates turned out identical in behavior. It also surfaced a third — see B1b, which landed in the same PR |
| B1b | ~~Reconcile the renderer registry's own failure predicate~~ — **done** | In: `panes/session/transcript/toolRenderers.ts:189` `toolCallFailed`, rewritten to call `hasItemFailure` with the per-tool `descriptor.failed` hook layered on top — or, if the renderer is meant to classify differently, the difference named in a comment and pinned by a test. Deleted: whichever of the two rules is wrong | `toolRenderers.test.ts`, `toolCallItem` tests, and `protocol/itemFailure.test.ts`; `make test-web-browser`'s transcript guard for the rendered result | 0 | — | **Landed inside B1 (#1189, `27503c07d`)** rather than as its own PR: `toolRenderers.ts:190-193` now delegates the shared half to `hasItemFailure` and keeps the descriptor half, so a call cannot read as failed in one surface and clean in another. Issues #1190 and #1197 are closed |
| B2 | ~~One task row parser and grouping~~ — withdrawn | **WITHDRAWN.** Not a twin: `taskGroups.ts:16` partitions parsed `TaskRow[]` into three arrays (`inProgress`/`open`/`settled`); `services/activity.ts:409` `projectTasks` emits three *counts* off `TaskAggregate` with `active` pinned to literal `0`. Different input, output and vocabulary. The three chrome task modules are a 2-consumer relocation instead — C25 — which also rewrites `transcript/tools/taskData.ts:18`, a consumer this row would have broken | n/a | 0 | — | Row kept so the B-numbers other rows cite stay stable |
| B3 | ~~One usage and cost accounting~~ — withdrawn | **WITHDRAWN.** Not a twin: `detailsAccounting.ts:69-89` `sessionTokens` derives a session figure by summing loaded turns when no cumulative exists (pinned by `detailsAccounting.test.ts:26-29,41-44`); `services/activity.ts:437-451` `projectUsage` copies ten optional fields with no arithmetic, and `state/activity.ts:23-27,337-344` *forbids* the derivation by returning `"rehydrate"`. `projectUsage`'s real analog is `protocol/reducer.ts:797-805`, already shipped — a D-phase reducer question. Replaced by B3b; see decision 4 | n/a | 0 | — | Row kept so the B-numbers other rows cite stay stable |
| B3b | Deduplicate native's two `projectUsage` copies | In: `mobile/src/conversation/project.ts:774-787` and `mobile/src/services/activity.ts:437-451`, which are the same ten-field copy off `Thread["evener"]` written twice. One survives; the other calls it. Out: the package — this never crosses a boundary. Deleted: the losing copy | `project.test.ts` and `services/activity.test.ts`, both unchanged | ~90 | — | Low. Native-internal, no frontend boundary, no package change. The two differ only in output type name (`MobileUsage` vs `UsageSummary`) and in `project.ts` omitting `durationMs`; check that before collapsing |
| B4 | ~~One system-notice family classification~~ — withdrawn | **WITHDRAWN.** Mis-targeted: `messages/systemGrouping.ts` exists (`:23,54,79`) but groups runs of consecutive system messages at `MIN_GROUP_SIZE = 3` — it is not `project.ts:67` `systemFamily`, which classifies one `eventKind` into a `NoticeFamily`. `systemFamily`'s real web counterpart is the `eventKind` set matching inside `transcriptDisplay/projector.ts:112-130`, which **D24 adopts wholesale**. Folded into D24 | n/a | 0 | — | Row kept so the B-numbers other rows cite stay stable |
| B5 | ~~One tool-run folding rule~~ — withdrawn | **WITHDRAWN.** Not a twin: `toolRuns.ts:56` `foldToolRuns` folds settled, error-free `commandExecution` entries at `MIN_RUN = 3` (`:43`) over `ProjectedEntry[]` through the renderer registry; `project.ts:528` `clusterActivities` folds consecutive same-family activity items at run length ≥ 2 over `PreActivity[]` | n/a | 0 | — | Row kept so the B-numbers other rows cite stay stable |
| B6 | ~~One queue reconciliation~~ — withdrawn | **WITHDRAWN.** Not a twin: `pendingReconcile.ts:111` `reconcilePendingEntries` matches outbox records against a `ThreadModel` across four states; `project.ts:759` `projectQueue` is a field copy of `Thread["evener"]["queue"]`. The native queue state that would be its twin is inside `state/conversation.ts` — D25's problem | n/a | 0 | — | Row kept so the B-numbers other rows cite stay stable |
| B7 | ~~One liveness line and thread title~~ — withdrawn | **WITHDRAWN.** No native twin at all: `project.ts:715` passes `thread.status.type` through (pinned by `project.test.ts:1483,1493`) and `roster.ts:48-53` `classifyAttention` uses a different vocabulary. Web-side, `liveness.ts` exports React hooks and `threadTitle.ts:21-23` reads the navigation zustand singleton, so both are PLATFORM-ONLY and rule 7 keeps them put. Stopped mid-execution as NEEDS_CONTEXT — the finding that triggered the re-audit | n/a | 0 | — | Row kept so the B-numbers other rows cite stay stable |

## Phase C — relocate modules both apps already import (27 rows: 26 relocations, plus C24)

Every module here **except C24's** is already imported by the web and by
`mobile-native` through a deep relative path (inventory §0); C24 relocates
nothing and is listed here only because its work follows the relocations. These PRs move the file into the package
and rewrite both sides' imports. No logic changes; the existing test file moves
with the module and stays the oracle. Sizes are move + import-rewrite lines.

**Rule for every module in this phase except C24, stated once instead of per
row:** a relocation is not done when the file has moved. Each PR must also (a) add the
module to `protocol/tsconfig.build.json` `files`, (b) give it a published import
target — a re-export from `index.ts`, or a new subpath declared in
`package.json` `exports` — and (c) add its symbols to the runner's
manifest under the right specifier (`packageExports`, made subpath-aware by
A3c), which is what `make test-api-package` actually checks. A module inside the package with no
entry in those three places is unreachable by package name, and the gate stays
green while saying nothing about it. The Lines estimates below include that
plumbing.

| # | Module moved | Lines | Deps | Risk |
| --- | --- | --- | --- | --- |
| C1 | `stores/navigation/{codec,merge,types,immutable}.ts` (1151), published under a new `state/navigation` subpath. **The subpath resolves to a barrel** — one `state/navigation/index.ts` re-exporting every runtime and type symbol of the four modules — not one qualified subpath per module: `package.json` `exports` gains a single entry, `tsconfig.build.json` the five files, and A3c's per-specifier manifest one row. **The barrel does not satisfy the runner as it stands, so a runner change is C1's first step, not an afterthought:** `shippedModules` (`qualify-package.mjs:33`) is a flat array of bare names, so the five nested modules have to be added in their `state/navigation/...` form; and the re-export scan looks for `from "./<module>"` in the entry declarations, which a barrel written beside its siblings spells `from "./codec"`, never `from "./state/navigation/codec"` — so every module under the barrel would read as unreachable and fail the assertion. Make the scan resolve re-export specifiers relative to the declaring file before adding the modules. Chosen because four subpaths mean four ESM plus four CJS declaration checks and four runtime checks for a set nothing consumes separately, and because the barrel is the seam later state relocations extend rather than multiply | ~1350 | A4, A3b, A3c | The package root exports no navigation API today, so without the subpath the moved code is unreachable by package name and `make test-api-package` would pass in silence. This row also settles the subpath question for every later state relocation, so do it deliberately. `immutable.ts` (25) has one consumer and moves only as a transitive dependency of `codec.ts:8` and `merge.ts:10` — the rule-7 exception is deliberate, see rule 7. `codec.ts` deep-freezes; a bundler that strips `Object.freeze` in production changes behavior no test sees |
| C2 | `stores/navigation/testing.ts` (294) | ~340 | C1 | **Decided here, before C1 lands:** it goes under the in-repo `testing` alias — never in `package.json` `exports`, never in the tarball. **Thirteen** consumers, and — correcting an earlier reading of this row — not all are tests: five web tests (`App.test.tsx`, `AppShell.test.tsx`, `DockRegion.test.tsx`, `RailHost.test.tsx`, `notifications/index.test.ts`), seven `mobile-native` tests (`navigationPages`, `navigationReveal`, `organizationNavigation`, `pinNavigation`, `pinNavigationRecovery`, `projectBrowser`, `sessionDeletionNavigation`), and one runtime consumer: `dev/editorial-preview/fixture.ts` is reached from the standalone `editorial-preview.html` Vite entry, so it is bundled, not merely typechecked — this row's oracle therefore includes building that preview entry and confirming it still resolves. This PR scopes all thirteen, so "app-side fixture that stays put" is not open to it — native would keep a relative import into the web tree, which is what A3/A4 delete. Consequence, per A3c: an alias is not in `exports`, so it gets no manifest entry and no tarball qualification; the apps' `tsc --noEmit` is its only gate, and that is accepted |
| C3 | `keybindings/{actions,chord,defaults,display,overrides,registry,validation}.ts` (1445) | ~1550 | A4, **decision 1** | `registry.ts:6` imports `zustand/vanilla` and `chord.ts:12` imports `tinykeys`; a zero-dependency package cannot pack them. Either hide both behind app adapters (the store shape from decision 1, and a `parseKeybinding` port) or add and qualify both as package dependencies. `registry.ts` is also a module-level singleton; the move must turn it into a factory or two apps share one registry in a test process |
| C4 | `transcriptDisplay/config.ts` (603) | ~700 | A4 | Encoding is a pinned localStorage contract (`prefs.ts` comment on commit 932eeddca); do not touch `encodeLocalConfig` |
| C5 | `stores/composerInput.ts` (27) | ~90 | **C6** | `:2` imports `translateAttachmentMarkers` from `./attachmentMarkers`, so C6 lands first or this leaves a cross-boundary relative import |
| C6 | `stores/attachmentMarkers.ts` (48) | ~110 | **A4** | None — the file has no imports at all, which is why it goes first. The earlier C5→C6 ordering was backwards |
| C7 | `composer/attachments/textareaMarkers.ts` (60) | ~120 | A4 | None — no import dependency on C6; the earlier `C6` here was wrong |
| C8 | `composer/attachments/limits.ts` (29) | ~90 | A4 | None |
| C9 | `composer/slashCompletion.ts` (334) | ~400 | A4, **C15** | It imports `slashCommandInvocation` from `shell/palette/catalogCommands.ts:13`, which C15 moves; a package module cannot keep that relative import. Either take the C15 dependency (as ordered here) or co-move the one helper. Also carries a third-party port attributed to `cmd/evener-hub/frontend/LICENSES/beautiful-ui.txt` — which sits in the **web app's** tree, outside the package directory, and `package.json` `files` is `["dist", "README.md", "examples"]`, so a naive move publishes the code and strands the attribution. Shipping it means three edits in this same PR, not one: copy the license into the package directory, add it to `package.json` `files`, **and widen the qualification runner's tarball allowlist** (`qualify-package.mjs:325-340`), which today permits only `package/package.json`, `package/README.md`, `package/dist/*` and `package/examples/*` and asserts anything else is absent — so an unlisted license file fails the gate. **Decision: ship it.** The alternative, leaving `slashCompletion.ts` in the web tree, keeps a two-consumer module out of the package for a reason that is a one-line allowlist entry, and would leave `mobile-native` importing it relatively forever |
| C10 | `composer/submitRouting.ts` (50) | ~110 | A1 (needs `sendQueueAvailability` shipped) | None |
| C11a | `panes/session/transcript/tools/helpers.ts` (138) → `protocol/toolCallText.ts`, the pure half `askShared.ts` depends on | ~200 | A1 | Split out of C11 because the two halves have different risk: this one has no imports at all. **Open as #1225.** |
| C11b | `composer/askDock/deriveAskQuestions.ts` (95) and `panes/session/askShared.ts` (151) | ~280 | C11a | `deriveAskQuestions.ts:20-21` imports `askShared.ts`, which at `:19` imports `parseArgs` from the module C11a moved, so these two travel together once C11a lands. `askShared.ts` reads `ItemModel`/`ThreadModel`, which A1 ships |
| C12 | `composer/askDock/reconcileBatches.ts` (76) | ~140 | C11 | None. C11 having grown does not change this dependency |
| C13 | `panes/session/chrome/activityRows.ts` (208) | ~270 | A1 | None |
| C14 | `shell/rail/sessionState.ts` (41) | ~100 | A4 | None |
| C15 | `shell/palette/catalogCommands.ts` (37) | ~100 | A4 | None. **Lands before C9**, which imports it — the C-numbers are identifiers, not a strict running order |
| C16 | `shell/reasoningEffort.ts` (30) | ~100 | A4 | None |
| C17 | `panes/spawn/{schema,pluginSelectionState,harnessModels}.ts` (192) | ~270 | A4 | None |
| C18 | `settings/launchShared/{schema,inherited,pathListAdd}.ts` (~400), plus `LaunchConfigLayerName` | ~520 | A4 | `LaunchConfigLayerName` is imported from the web-only `stores/launchConfig.ts` by **three** files, all of which this PR updates to the package export: `launchShared/schema.ts:8`, `launchShared/fields.tsx:31` and `launchShared/LaunchConfigForm.tsx:22`. D7, which moves that store, comes much later, so the type moves into the package here and `stores/launchConfig.ts` re-imports it — a type move, not a compatibility shim |
| C19 | `settings/credentials/credentialLabels.ts` (157) | ~220 | A4 | None |
| C20 | `settings/marketplacesPlugins/sourceLabel.ts` (20) | ~80 | A4 | None |
| C21 | `widgets/modelCatalog/{pickerRows,catalogView,types}.ts` (~270) | ~350 | A4 | `pickerRows.ts:18` is built on `catalogView.ts` helpers, so `catalogView.ts` moves with it; leaving it behind strands a relative import across the boundary. `catalogClient.ts` and `scopedCatalog.ts` stay (they hold the fetch) |
| C22 | `widgets/pathfield/pathRows.ts` (156) | ~220 | A4 | None |
| C23 | `widgets/disclosure/disclosureStore.ts` (142) | ~210 | decision 1 | zustand; blocked on the store-shape ruling |
| C24 | ~~`docContent.ts` gains an injected origin and the two platform adapters~~ — **done, shipped as #1221 (`303053dfb`)**. `DocPort = { origin, fetch }` (`docContent.ts:72`) is the whole doc seam a host installs; `docFileRawURL(origin, session, path)` (`:97`) and `docImageURL(origin, session, path)` (`:135`) take the origin, and `readDocFile(session, path, port)` (`:113`) composes through the port rather than a fourth positional argument. Adapters: `panes/doc/browserDocPort.ts` and `mobile-native/src/nativeDocPort.ts`, each with its own test. **`docImageURL` stays a pure string builder, and native image authentication needs no new port:** it returns the href and each renderer wraps it — the browser `<img>` sends the hub cookie on a same-origin request, and native passes it through `transcriptImageSource(docImageURL(origin, session, path), origin, token)` (`mobile-native/src/transcriptImageSource.ts`, pinned by `transcriptImageSource.test.ts`), which attaches `Authorization: Bearer …` only when the URL's origin matches the hub's. | 180 | A1, A3d | **Shipped clean.** The one design question this row carried for five rounds — how a bare URL string carries a bearer header to a native `<Image>` — was already answered in the tree by `transcriptImageSource`, which nobody had cited. Worth remembering: before specifying a new port, grep the native tree for the adapter that already does it Oracle, for the record: the two adapter tests, `protocol/docContent.test.ts`, and `make test-api-package` for the changed `./docContent` signature and its `packageExports` `values`/`types` |
| C25 | `panes/session/chrome/{taskData,taskGroups,taskTime}.ts` (154) — all three imported by native (2/1/2 sites) — plus rewriting `panes/session/transcript/tools/taskData.ts:18`, which imports `parseTaskListData` and `TaskRow` from the chrome module | ~300 | A4 | Absorbs withdrawn B2. `TaskListResponse.data` is `unknown` on the wire, so the parser is hand-written and its `null` (no data, old daemon) must stay distinct from `[]` (zero tasks). The `transcript/tools/taskData.ts` consumer is the one a naive move would break |
| C26 | `panes/session/transcript/messages/format.ts` (87) — imported by native (1 site), and the last two-consumer module with no row of its own | ~150 | A4 | None. The file has no imports at all, so it is the cheapest relocation left |

Some modules that look like relocations are deliberately not here.
`stores/secureUUID.ts` has one consumer today and moves inside D26, where the
outbox core becomes its second. The accounting trio
`panes/session/chrome/{activityFormat,statusFormat,detailsAccounting}.ts` is
single-consumer — see decision 4 and D18 — and so are
`transcript/messages/{systemGrouping,turnMeta}.ts`. `systemGrouping.ts` is where
native's `systemFamily` eventKind analog actually belongs, and D24 owns
reconciling the two. `format.ts` is that directory's exception: two consumers,
so it relocates in C26. The task trio moved to C25.

## Phase D — the stores (28 PRs)

Smallest and least coupled first. Each replaces a web store's body with an
adapter over a shared module and deletes the native twin in the same PR (rule 3).
The web store's exported shape and its `.test.ts` are the contract (rule 4).

**The phase-C publication rule applies to every shared module phase D creates,
and is stated here because it is easier to forget when the PR feels like a
store migration rather than a relocation:** a module inside the package that is
not in `tsconfig.build.json` `files`, not reachable from `index.ts` or a
declared subpath, and not named in `packageExports` is unreachable by package
name, and the gate stays green while saying nothing about it. C13 taught the
sharpest half of that — **the `shippedModules` entry is what arms the
reachability assertion, so a module emitted into `dist/` but missing from that
array passes silently**, which is exactly the hole the assertion exists to
close. Filed as **#1224**; its fix lands after #1222 and #1223, and until then
every phase-C and phase-D row must add the `shippedModules` entry by hand.

| # | Store migrated | Native twin deleted | Lines | Deps | Risk |
| --- | --- | --- | --- | --- | --- |
| D1 | Command catalog: **the web adopts the native logic**, which is the superset — not a twin deletion | nothing; `mobile-native/src/commandCatalog.ts` becomes the shared module and keeps its behavior | ~450 | A4, **A2** | **Re-audited by grep at `303053dfb`, and the reviewer is right on all four counts.** `stores/commandCatalog.ts` (26 lines) issues one `evener/command/list` and stores the result. `mobile-native/src/commandCatalog.ts` (105) does four more things, every one of which is the contract this row must preserve: (a) reads session diagnostics — `thread/read` with `includeTurns: false`, then `evener.diagnostics`; (b) filters plugin commands the session cannot run, via `visibleCatalogCommands(commands, new Set(diagnostics.plugins.map(p => p.name)))`; (c) merges skills, via `mergeSlashCommands([], commands, diagnostics?.skills ?? [])`; (d) refreshes on `evener/plugin/updated` and on a ref-matched `evener/thread/resync`. Three more the review did not list and the row must also keep: per-ref scoping with a "Catalog belongs to another session" guard on a mismatched `evener.ref`, in-flight coalescing through a `dirty` re-run loop, and `Promise.allSettled` so one failed request does not discard the other. Pinned by `mobile-native/src/commandCatalog.test.ts` (the superset behaviour) and `stores/commandCatalog.test.ts` (the web store's surface, which becomes the adapter's contract per rule 4); `slashCompletion.test.ts` covers `mergeSlashCommands`. Estimate raised from ~250 to ~450: the web gains behaviour rather than losing code, and the palette needs rewiring to a per-ref catalog **Package-boundary port, decided after reading both interfaces:** the native module imports `ConversationClientLike` from `mobile/src/services/conversation`, which a package module cannot reach. The shared module takes a **narrow package-local interface**, not `AppwireClientLike`: the catalog calls exactly two members (`onNotification` at `commandCatalog.ts:39`, `request` at `:64,65`), while `AppwireClientLike` demands ten — `connect`, `onReady`, `onStateChange`, `retryNow`, `state` and `terminalReason` among them — none of which this module touches. The package already has the pattern: `activityList.ts:13` declares `ActivityClient` as a `Pick` of `AppwireClient` over just `request` and `onNotification`, so D1 declares `CommandCatalogClient` the same way. `AppwireClientLike` (A2, merged) satisfies it structurally, so both apps pass what they already hold and no adapter is needed; A2 is listed as a dependency because that is what makes the web side true. |
| D2 | `stores/settingsOverview.ts` (122) | `hubOverview.ts` (71) | ~280 | A4 | Low. The store shape is pinned across three streams; the adapter keeps it |
| D3 | `stores/tasksPanel.ts` (228) | `taskList.ts` (123) | ~400 | C25, D1 | Low-medium. Unsupported vs daemon-gone vs empty are three distinct states; collapsing any two is invisible to a shape-only test |
| D4 | `stores/hubUpdate.ts` (244) | `hubUpgrade.ts` (263) | ~480 | A4 | Medium. The `/api/health` poll and `location.reload()` stay web-side, `expo` restart stays native-side; only check/apply move. Runtime risk: the socket drops mid-apply, which no unit test reproduces |
| D5 | `keybindings` overrides store (852) | `nativePreferences.ts` keybinding half | ~900 | C3 | Medium-high. Reconciliation is a delta against a live registry; a full rebind tears down in-flight dispatcher state |
| D6 | `stores/transcriptDisplay.ts` hub half (707) | `nativePreferences.ts` display half | ~800 | C4, D5 | Medium. The local-storage half and `shell/useIsMobile` stay web-side; the split point is the PR's whole design |
| D7 | `stores/launchConfig.ts` (114) | `launchSettings.ts` (311) | ~450 | C18 | Low-medium. The schema cache is read-your-writes sensitive for everything except `schema()` |
| D8 | `stores/credentials.ts` instance half (322) | `providerInstances.ts` (176) | ~500 | C19 | Medium. The never-echo-a-secret invariant is a review item, not a test |
| D9 | `stores/credentials.ts` auth half | `providerSignIn.ts` (386) | ~550 | D8 | Medium-high. Device and OAuth flows differ by platform (popup vs system browser); only the RPC sequencing is shared |
| D10 | `stores/extensions.ts` marketplaces | `marketplaces.ts` (144) | ~400 | C20 | Medium. Fetches never throw, mutations reject — the two conventions must survive the move |
| D11 | `stores/extensions.ts` plugins | `installedPlugins.ts` (102) | ~350 | D10 | Medium |
| D12 | `stores/extensions.ts` dirs and MCP | — | ~300 | D11 | Low |
| D13 | `stores/navigation/revalidator.ts` (467) | invalidation logic in `navigationPages.ts` | ~600 | C1 | Medium. Needs an injected scheduler; a wrong one produces refetch storms under load that no unit test shows |
| D14 | `stores/navigation/store.ts` (867) | `navigationPages.ts` (394), `navigationReveal.ts` (131) | ~1450 | D13, decision 1 | High. Two dependencies do not cross the boundary as-is: `store.ts:1-2` imports `zustand`/`zustand/vanilla`, and `store.ts:203` calls `loadExpansion()` from `shell/rail/railExpansion.ts`, which reads `localStorage` at lines 40/68 — at store-creation, i.e. module init, so merely importing the package would throw on a device. This PR **defines an explicit host persistence port** (`readExpansion`/`writeExpansion` supplied by the host) and keeps both the zustand hook and the `localStorage` implementation in platform adapters; native supplies an `expo` one. Getting this wrong either crashes native consumers at import or silently drops web rail-expansion persistence. Pagination, attention and base invalidation interact; `NavigationBaseInvalidError` recovery is the other sharp edge |
| D15 | `stores/navigation/selectors.ts` (431) | `navigationTree.ts`, `rosterSearch.ts` (165) | ~700 | D14 | Medium-high. Two problems, not one: selectors read `navigationStore` directly and must take state as an argument, **and** `:238` imports `IsExpanded`, `RailSession` and `SessionRailNode` from `shell/rail/railNodes.ts`, a web UI module that never enters the package. Either extract package-neutral navigation result types those rail types are built from, or leave the rail-shaped selectors in a web adapter and move only the graph-shaped ones. Deciding which is the PR |
| D16 | `mobile/src/services/roster.ts` (114) folded onto navigation reads | `navigationActions.ts` pin/archive half (302) | ~450 | D15 | Medium. `thread/list` and `evener/navigation/read` answer the same question differently; this PR picks one |
| D17 | Activity tree state: web adopts `activityList.ts` | the fetch/subscribe body of `stores/activityPanel.ts` (371), replaced by `activityList.ts` — this row deletes a web implementation, not a native twin; native already consumes `activityList.ts` from `ActivitySheet.tsx`, which is what the web converges on | ~600 | A1, C13 | High. Continuation grafting (`activityMerge.ts`) and retained-tree behavior across a thread replacement is the bug #1096 already fixed once |
| D18 | Activity counts and summary | `mobile/src/state/activity.ts` (495), `services/activity.ts` remainder | ~800 | D17, C25, decision 4 | High. Native's generation/identity fencing and relocation-to-rehydrate rules are stricter than the web's; keep native's, do not average them |
| D19 | `subagentModuleStore.ts` (138) | `delegateDetails.ts` (116) | ~300 | A1 | Low |
| D20 | `askDockStore.ts` (440) | `questionBatches.ts` (63) | ~550 | C11, C12 | Medium. Subscribes to `threadsStore` at module load; the shared version needs an explicit wiring call |
| D21 | Native adopts `ItemModel`/`TurnModel` | `mobile/src/conversation/model.ts` (227) | ~900 | A1, B1 | High. Two view models collapse into one; every native screen reads the new shape |
| D22 | Native adopts `reducer.hydrateThread` | the hydrate half of `project.ts` (787) | ~1100 | D21 | High. Cold attach, older-page merge and truncation caps (`MAX_ITEM_BYTES`, `RETAINED_ITEM_CAP`) must agree |
| D23 | Native adopts `reducer.applyNotification` | the notification appliers in `state/conversation.ts` (3369) | ~1800 | D22 | Highest in the plan. Two independent appliers over the same notification set; generation fencing lives on the native side and must be lifted into the shared reducer or kept as a wrapper |
| D24 | Native adopts `transcriptDisplay/projector.ts` | native timeline clustering | ~900 | D23, C4 (absorbs withdrawn B4: the `eventKind` classification in `projector.ts:112-130` is what native's `project.ts:67` `systemFamily` becomes) | High. Native gains config-driven content levels it does not have today; that is a visible product change, flag it |
| D25 | Extract a headless queue/reconciliation core from `pendingTurnsStore.ts` (474); the React, storage and thread-store adapters stay per frontend | the queue half of `state/conversation.ts` | ~700 | D23 | High. The store cannot move as a unit: `:1` imports React (`act`, `useEffect`, `useMemo`), `:2-3` zustand, `:21` the platform-only `../draft` (localStorage on web, expo-sqlite on native), and it reads the web thread stores. Only the reconciliation over outbox records plus `pendingReconcile.ts:111` is portable; everything else is an adapter. Scoping this as "move the store" is how the PR fails |
| D26 | Outbox record core + a storage port, `secureUUID.ts` moved in | `ConversationMutationState` in `state/conversation.ts` | ~700 | D23 | Medium. Defines the port; no storage moves. `secureUUID` defaults to `globalThis.crypto`, so native must keep passing `expo-crypto` |
| D27 | `mutationDispatcher.ts` (251) over the port | native's in-memory retry path | ~500 | D26 | High. Serialized per-ref dispatch, blocked/unknown receipts, and "never blindly replay a write after reconnect" (#1116). Connection loss during a mutation is exactly what tests do not cover |
| D28 | `stores/connection.ts` (107) lifecycle | `ConnectionProvider.tsx` | ~400 | D27, A2 | High. Reconnect rewiring, heartbeat and terminal-protocol handling; a wrong handler set leaves a live app silently stale |

## Where test support lives

`protocol/testing/` (`fakeClient.ts`, `fakeSocket.ts`, `hubWireFixtures.ts`,
`notifications.ts`, `tokenFlood.ts`) has ~111 importers across both apps and is
deliberately **not** in the tarball — `tsconfig.build.json` has never listed it,
and the qualification runner asserts the tarball carries no source files.

The decision: **test support stays inside the package directory, under
`testing/`, and is addressed by a non-shipped in-repo specifier.** A2 does not
touch it. A3 moves it along with everything else and teaches the same aliases a
second entry (`@evener/appwire-client/testing` → `<package>/testing`, resolved by
`tsconfig.paths`, Vite, vitest, Metro and the guard runners, and absent from
`package.json` `exports`). A4 rewrites the ~111 imports onto that specifier in
the same sweep as the runtime ones.

The rejected alternative is hoisting the fakes into each app's own test tree.
That forks `FakeClient` — whose whole value is checking scripted methods against
`METHOD_NAMES` from the generated catalog (`fakeClient.ts:20-30`) — into two
copies that can disagree with each other and with the hub. If a future consumer
outside this repo needs the fakes, the answer is a published `testing` subpath,
which is a separate decision and not needed by either app.

## Temporary seams

Exactly two, each with its removal PR named.

1. **A3 keeps the old `protocol/` path as a directory of re-export stubs** so
   A3 and A4 can be reviewed separately. A4 deletes the stubs. If A3 and A4 land
   together, this seam does not exist.
2. **D21–D24 leave `mobile/src/conversation/project.ts` in place as a shrinking
   shim** while its four halves migrate. D24 deletes the file and the
   `mobile/src/conversation/` directory. No PR after D24 may import it.

Nothing else gets a compatibility layer. In particular, no PR adds a
`legacy`/`v1` export alongside a new one.

## Decisions needed before the first code PR

**1. Does the package take runtime dependencies, or stay at zero?**
It has none today (`protocol/package.json`), and the qualification runner
installs the tarball with `--offline --ignore-scripts`, so every dependency has
to resolve in a temp directory. Three dependencies are in the way, not one:
`zustand` (`stores/navigation/store.ts:1-2`, `keybindings/registry.ts:6`,
`widgets/disclosure/disclosureStore.ts`), `tinykeys` (`keybindings/chord.ts:12`)
and `anser` (`widgets/codeblock/ansi.ts`).
*Recommendation: stay at zero.* Ship framework-free stores (a
`getState`/`subscribe`/`setState` triple — which is what `zustand/vanilla`'s
`createStore` already gives the web, so the web adapters are near-trivial) and
let each app wrap them. Take `tinykeys`' `parseKeybinding` as an injected port
in C3 rather than a dependency. Leave `ansi.ts` where it is; native already
imports it by relative path and moving it buys one import site for one
dependency. This decision gates C3, C23 and every phase-D store.

**2. One view model or two layers?**
The web goes wire → `ThreadModel` (`reducer.ts` + `model.ts`) → display entries
(`transcriptDisplay/projector.ts`, filtered by a hub-backed config). Native goes
wire → `MobileConversation` in one step (`conversation/project.ts`), with
classification and clustering baked in.
*Recommendation: adopt the web's two layers.* The display config is already
shared — `transcriptDisplay/config.ts` has 10 native import sites — so native is
already half-committed, and a hub setting that only one client honors is a bug
waiting to be filed. The cost is real and should be stated to users: D24 gives
native content levels it does not have today. This decision gates D21–D24, which
is roughly 4,700 of the plan's lines.

**3. Where does the package live on disk, and what is the directory called?**
It is `cmd/evener-hub/frontend/src/protocol` today — a subdirectory of the web
app that both mobile trees reach with `../../../`. **This gates A3, not A1.**
An earlier draft said it had to be settled before A1, on the theory that
build-config edits should land in the final directory; A1 was executed in place
instead (#1184), which is the better order — A1 changes what the package
contains and A3 changes where it lives, and neither needs the other's answer.
So A1 targets today's directory unconditionally and A3 is the move.
*Recommendation: `git mv` it to a top-level `appwire-client/`* in A3, so neither
app owns it and the `../../../` chains disappear. The cost is a large mechanical
diff plus Metro, Vite, vitest and five browser-guard resolvers to update, all in
one PR. The alternative — leave it in place — is free and keeps a package the
web app nominally owns; I do not recommend it, but it is defensible if the move
churn is judged worse than the asymmetry.

**4. When no cumulative usage is present, does a client derive a session figure
from per-turn usage, or refuse and rehydrate?**
The two frontends already answer differently, and neither is obviously wrong.
The web derives: `detailsAccounting.ts:69-89` `sessionTokens` prefers
`ThreadModel.usage` and otherwise sums the loaded turns, labelling the result
`scope: "session"` or `"loaded"` depending on whether `olderCursor` says the
window was truncated — pinned by `detailsAccounting.test.ts:26-29,41-44`, and
motivated by real fork children, whose meta carries no cumulative usage at all.
Native refuses: `state/activity.ts:23-27,337-344` returns `"rehydrate"` on
`turn/completed` rather than fold per-turn usage into the aggregate, on the
grounds that the protocol offers no authoritative cumulative projection there.
*Recommendation: keep the web's derivation and give it the native discipline* —
derive, but only behind the explicit `scope` label the web already carries, and
never write a derived figure back into the aggregate the way native fears. That
makes `scope` a package-level concept and gives native a number where it
currently shows nothing. The cheaper answer is to standardise on refusal, which
costs the web a panel row on every fork child. This gates the reducer half of
D18 and D22, and it is why B3 was withdrawn rather than executed.

A fifth question is settled by the inventory rather than needing a ruling:
`mobile/src` should not move into the package as a unit. Its 3,864 lines of
zustand stores are one app's state model, and the largest already-shared body of
code is in the web tree, not in `mobile/src`. It dissolves module by module
through D21–D24 and the directory is deleted at the end of D24.

## Honest total

**65 PRs. Roughly 29,900 lines changed** — 29,890, summed from the Lines cell of
every row above, so the headline and the phases cannot drift apart again. The
tables carry 72 rows, six of them withdrawn phase-B rows that contribute
nothing — so **66 rows of work**, and **65 real PRs**, because B1b landed inside
B1 rather than on its own. **Eleven have landed** (A1, A2, A3b, A3c, A3d, A5,
B1 carrying B1b, B3b, C10, C13, C24), so **54 remain**, three of them in flight
(C11a #1225, C6, C26).

Phase A 8 PRs / 2,820 lines. Phase B 210, the sum of its three live rows of 9
(all three landed). Phase C 27 / 8,550. Phase D 28 / 18,310. Each subtotal is its own rows added up, not an estimate.
Phase B collapsed from 1,600 lines to 210 because the re-audit found six of its
nine rows were not duplication at all, and B1b landed inside B1.

Net, the tree should shrink by roughly 6,000–8,000 lines: about 3,900 lines of
native twins deleted outright (`providerInstances`, `providerSignIn`,
`installedPlugins`, `marketplaces`, `launchSettings`, the four navigation
modules, `nativePreferences`, `hubOverview`, `hubUpgrade`, `taskList`,
`commandCatalog`, `delegateDetails`), 774 from A5, and maybe half
of `mobile/src`'s 6,600 lines of projection and conversation state — the other
half is behavior the shared version has to absorb, not delete.

Treat these numbers as ±40%. Phase D's four transcript PRs (D21–D24, ~4,700
lines) and the three mutation PRs (D26–D28, ~1,500) are where the estimate is
weakest, because both are cases where two implementations disagree today and
nobody has yet written down which one is right.

`mobile-native/src/jobOutput.ts` (135) was previously counted among the deleted
twins and has been removed from that list: it is a thin adapter over the
already-shared `protocol/jobOutput.ts` parser plus the `evener/jobs/output` RPC,
no phase-D PR deletes it, and on the evidence none should.

## Status as of 2026-09-12

Observed at `e2c77cc72`, not assumed; re-query before acting.

| Plan PR | GitHub | State |
| --- | --- | --- |
| A1 | #1184 | **merged** to main as `f2599d1ed` — all **ten** unpacked modules shipped (`files` 6 → 16), `docContent.ts` included, with only `readDocFile` kept off `index.ts` until C24. Runner smoke-calls every shipped module |
| A2 | #1188 | **merged** as `3bf357337` — `clientLike.ts` shipped and exported from `index.ts`, 25 importers rewritten |
| A5 | #1186 | merged as `2245f9715` |
| B1 | #1189 | **merged** as `27503c07d` — the two predicates were identical; surfaced B1b |
| A3b | #1206 | **merged** as `99fa1882f` — twelve root error exports; `chunkViewBackingForTests` and `readDocFile` deliberately excluded, which is why A3c/A3d exist |
| A3c | #1207 | **merged** as `2a8163eb0` — the qualification manifest is per specifier, not only at the root |
| C24 | #1221 | **merged** as `303053dfb` — `DocPort`, both adapters, `docImageURL` still a string builder |
| C10 | #1222 | **merged** as `ba4164649` — `submitRouting` → `protocol/submitRouting.ts` |
| C13 | #1223 | **merged** as `e2c77cc72` — `activityRows` → `protocol/activityRows.ts` |
| C11a | #1225 | open — `tools/helpers.ts` → `protocol/toolCallText.ts`; round 1 found stale comments and a main conflict |
| C6 | — | open lane — `stores/attachmentMarkers.ts` → `protocol/attachmentMarkers.ts`, dispatched off `e2c77cc72` |
| C26 | — | open lane — `messages/format.ts` → `protocol/displayFormat.ts`, dispatched off `e2c77cc72` |
| — | #1224 | open issue — `shippedModules` arms the reachability assertion, so a `dist/` module missing from it passes silently |
| A3d | #1209 | **merged** as `f39aa2c83` — `./docContent` published; `readDocFile` took a required `DocFetch`, since widened by C24 to a `DocPort`; browser adapter at `panes/doc/browserDocPort.ts` (named `browserDocFetch.ts` until C24 renamed it), wired at `DocPane.tsx:49` |
| B3b | #1203 | **merged** as `4da382482` — native's two `projectUsage` copies collapsed to one |
| B1b | #1190, #1197 | **done** — landed inside B1 (`27503c07d`); both issues closed |

**What A3 and A4 actually block — corrected, because three relocations have now
landed without them.** A3 moves the package directory and A4 rewrites imports to
the package name; together they block the *package-name import rewrite* and the
*directory move*, nothing else. A relocation into `protocol/` lands today with
its consumers still on deep relative paths, which is exactly how C10 (#1222),
C13 (#1223) and C24 (#1221) landed. **Read every phase-C `A4` in the Deps column
as "consumers get the package-name import when A4 lands", not "cannot start".**
A3b, A3c and A3d landed ahead of A3 for the same reason.

B7 was executed and stopped as NEEDS_CONTEXT; that triggered a grep re-audit of
every DUPLICATED row and every 2-consumer claim at `f2599d1ed`. Six phase-B rows
were withdrawn, seven inventory rows were reclassified, and all 34 2-consumer
claims held. B3b is the rescoped survivor of that lane.
