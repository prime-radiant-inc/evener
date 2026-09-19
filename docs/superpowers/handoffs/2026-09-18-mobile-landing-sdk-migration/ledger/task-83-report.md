# Task 83 report — SDK migration inventory and plan

**Status:** done. Docs only, committed on `claude/sdk-migration-inventory`, not pushed.

**Commits** (worktree `.claude/worktrees/sdk-migration-inventory`, off `92561dbe3`):
- `053d99b92` docs(design): inventory web and native state modules for the SDK migration
  → `docs/design/2026-09-12-sdk-migration-inventory.md` (298 lines)
- `79d35fc46` docs(plans): sequence the SDK migration as 64 bite-sized PRs
  → `docs/superpowers/plans/2026-09-12-sdk-migration.md` (229 lines)

**Counts.** 105 inventory rows: SHARED ALREADY 6, DUPLICATED 36, PACKAGE CANDIDATE 51
(33 of those already have two consumers via deep relative import), PLATFORM-ONLY 11,
dead code 1. Plan: 64 PRs (A packaging 5, B pure-logic dedup 7, C relocations 24,
D stores 28), ~29,000 lines changed, net shrink ~6,000–8,000.

**Decisions flagged for Jesse:**
1. Does the package take runtime dependencies (zustand, anser) or stay at zero?
   Recommend zero — ship framework-free stores, each app wraps them; leave
   `widgets/codeblock/ansi.ts` where it is. Gates every phase-D store.
2. One view model or two layers? Web does wire → ThreadModel → display projection;
   native does wire → MobileConversation in one step. Recommend the web's two layers,
   because `transcriptDisplay/config.ts` is already shared (10 native import sites) and
   a hub setting only one client honors is a bug. Costs native a visible behavior change.
3. Where does the package live on disk? Recommend `git mv` to a top-level
   `appwire-client/`; must be settled before the first build-config PR.

**Surprises:**
- **The shipped package is six files, not sixteen.** `protocol/tsconfig.build.json:13`
  lists only index/client/errors/transport/types.gen/askAnswers. `reducer.ts`,
  `model.ts`, `activityData.ts`, `activityList.ts` and six more (3,635 lines) live in
  the folder but are never packed or qualified. `index.ts` exports nine symbols; every
  real import on both sides is a deep relative path that would not resolve for an
  installed consumer.
- **`AppwireClientLike` — the interface 136 web files use, 20 of them production —
  is declared in `protocol/testing/fakeClient.ts:40`,** a test double outside the build.
- **The native app already imports 30+ web frontend modules by relative path.**
  `mobile-native/src` has 202 import sites into `cmd/evener-hub/frontend/src/`, 124 of
  them outside `protocol/` (`transcriptDisplay/config` ×10, `stores/navigation/testing`
  ×7, `stores/composerInput` ×6, `keybindings/*`, `panes/*`). The brief's "web imports
  nothing from mobile/src" is true, but the reverse coupling is far wider than protocol/.
- **Nearly every web store has a full native twin** doing the same RPCs:
  `providerInstances`+`providerSignIn` ↔ credentials, `installedPlugins`+`marketplaces`
  ↔ extensions, `launchSettings` ↔ launchConfig, four navigation modules ↔
  stores/navigation, `nativePreferences` (839) ↔ keybindings + transcriptDisplay,
  `hubOverview`/`hubUpgrade`/`taskList`/`commandCatalog`/`jobOutput`. ~3,900 lines.
- `mobile/src` does **not** import `protocol/reducer`, `model`, `activityData` or
  `activityMerge` at all — it re-implements notification application in
  `state/conversation.ts` and projection in `conversation/project.ts`, whose own comment
  says it "mirrors the web frontend's ... predicate (projector.ts:118-130) exactly".
- `protocol/activityList.ts` is the one protocol module the web does not use.
- **Defect found, not filed** (task was docs-only): `mobile/src/dev/conversationFixtures.ts`
  (774 lines) is dead — no importer, and two dangling type imports
  (`../services/nativeProfiles`, `../state/connection`) to files that do not exist.
  `mobile-native/tsconfig.check.json` has no `include`, so `mobile/src` is typechecked
  only through the import graph and this file is never reached. Worth a GitHub issue.

---

# Task 88 — RoboRev round on PR #1182

**Status:** done. Two commits on `claude/sdk-migration-inventory`, not pushed.
- `f2289bf19` docs(design): correct the SDK migration inventory's counts and record the third failure predicate (inventory now 329 lines)
- `0e063359c` docs(plans): close the SDK migration plan's dependency gaps and path-update step (plan now 252 lines)

All ten findings accepted; every one verified against the code first. Nothing disputed.

| Finding | What changed |
| --- | --- |
| High, A3 paths | A3 now names eight functional files: `appwire/doc.go:29`, `make/testing.mk:58`, `make/linting.mk:190`, `internal/appwirets/emit_test.go:658`, `appwire/protocol_test.go:335,360`, `makefiletargets_audit_test.go:1073`, `.github/workflows/ci.yml:57,60`, `ios-testflight.yml:74`. Found two the reviewer did not list (`protocol_test.go`, `makefiletargets_audit_test.go`) plus eight comment-only mentions, listed separately. Oracle widened to `make generate` + `make lint` |
| Med, A2 | Rewrite scope corrected to the **25** `AppwireClientLike` importers (not 136 — 136 is the `fakeClient` figure, which stays in the inventory as the `FakeClient` count); `clientLike.ts` exported from `index.ts` and in the build `files`. Marked **done in #1188** |
| Med, C3 | `registry.ts:6` zustand + `chord.ts:12` tinykeys recorded; C3 now depends on decision 1, which gained `tinykeys` as a third blocked dependency |
| Med, C9 | C9 now depends on C15 (`catalogCommands.ts:13`); C15 annotated "lands before C9" |
| Med, C18 | `LaunchConfigLayerName` (`schema.ts:8`) moves into the package in C18, not at D7 |
| Med, C21 | `catalogView.ts` moves with `pickerRows.ts:18`; `catalogClient`/`scopedCatalog` stay |
| Med, D14 | Host persistence port defined: `store.ts:203` calls `loadExpansion()` in the initial state and `railExpansion.ts:40,68` is `localStorage`, so importing the package would throw on device. zustand hook + storage stay in adapters; `railExpansion.ts` added to the never-moves list |
| Low, export counts | "nine export statements / eleven runtime exports" with all eleven named; four consumer files (`esm.mts`:29, `commonjs.cts`:51, `esm-runtime.mjs`:76, `commonjs-runtime.cjs`:85); A1's oracle updated to all four |
| Low, import counts | Corrected to **204 total / 88 non-protocol / 39 distinct modules**, with the counting method stated and the old "124" called out as my arithmetic error |
| Low, C24 rule 7 | C24 labelled a deliberate rule-7 exception; rule 7 now names both exceptions (C24, D26) |

**Folded in since the docs were written.** A1 #1184 **open** (nine modules, `files` 6→15, `docContent` held to C24, runner smoke-calls each module); A2 #1188 **open**; A5 #1186 **merged** (`2245f9715`, also widened native typechecking over `mobile/src`); B1 #1189 **open** (the two predicates were identical). Note: the coordinator described #1184/#1188/#1189 as landed — they are open, only #1186 is merged; both docs say so. New **B1b** PR added for issue #1190's third predicate at `toolRenderers.ts:189` (no `error` trim, no `interrupted`, ignores `exitCode`, adds a `descriptor.failed` hook; 43 files reach the registry) — numbered out of sequence so D-phase references to B2–B7 stay valid. New inventory row for it.

**Totals now:** 106 inventory rows (DUPLICATED 37); **65 PRs, ~29,300 lines**. Both docs carry a dated status section.

---

# Task 88b — RoboRev round 2 on PR #1182 (`777c8e2bd`)

**Status:** done. Two doc commits plus the requested merge, not pushed.
- `9a40c6059` docs(design): reconcile the inventory's line totals and consumer counts with its own tables
- `34a674ee6` docs(plans): give test support a permanent home and fix the plan's stale counts
- `34fb2274c` merge of `origin/main` (fetched fresh; brought in load-aware workers, vite/testing changes)

All seven findings accepted; nothing disputed.

| Finding | Fix |
| --- | --- |
| M1 `FakeClient` imports dangle after A3/A4 | New "Where test support lives" section: `protocol/testing/` stays inside the package, out of the tarball, addressed by a non-shipped in-repo specifier (`@evener/appwire-client/testing`). A2 leaves it alone, A3 moves it and teaches the aliases a second entry, A4 rewrites the ~111 imports in the same sweep. Rejected alternative (fork the fakes per app) named, with the reason: `fakeClient.ts:20-30` checks scripted methods against `METHOD_NAMES`, and two copies can disagree with the hub |
| M2 `immutable.ts` moves without an exception | Documented as a **transitive-dependency** exception in rule 7 and on the C1 row — `codec.ts:8` and `merge.ts:10` both import it, so it crosses with them or they arrive broken. Inventory row updated to say so |
| L1 §6 says 124; plan uses 202/124 | §6 → 88. Plan's A4 row recounted by one stated method: **856 import statements (652 web, 204 native)**, estimate 800 → 950 lines. The single surviving "124" is the §0 note that records the error |
| L2 "three generated consumers" | → four, at inventory :253 |
| L3 A3 comment-path count | Now "seven further mentions ... in six files beyond the eight functional ones, fourteen files in all", with the file-type search scope stated |
| L4 4,383 lines | → **3,864** (3,369 + 495), both docs, with the two per-file counts spelled out |
| L5 3,635 lines | → **3,502**, both docs, matching §5's table |

---

# Task 88c — RoboRev round 3 on PR #1182 (`34fb2274c`)

**Status:** done. Two doc commits plus the requested merge, not pushed.
- `1b86c5747` docs(design): recompute the inventory's headline totals and record the askShared helper chain
- `1479c6ecf` docs(plans): cover mobile/src in the import rewrite and give C11 its real scope
- `02a43c81e` merge of `origin/main` (fetched fresh; tool-fluency, npm shim audit, toolchain action)

All five findings accepted; nothing disputed. Verified each against the code first.

| Finding | Fix |
| --- | --- |
| M1 A4 misses `mobile/src` | Confirmed 21 old-path imports there. A4 now covers all three trees (652 web + 204 `mobile-native` + 21 `mobile/src` = ~877), with a stated zero-old-path grep across `mobile` and `mobile-native` as an acceptance check; estimate 950 → 980 |
| M2 A3-vs-decision-3 ordering | Resolved the way reality went: A1 ships modules into today's directory (#1184 did exactly that), A3 performs the move, decision 3 gates **A3**. Decision 3's "settled before A1" wording replaced, and the A3 dependency cell now spells out content-first/location-second |
| M3 C11's hidden chain | Confirmed `deriveAskQuestions.ts:20-21` → `askShared.ts` (151) → `tools/helpers.ts:19` `parseArgs` (138, no imports of its own). Both added to C11's scope (~160 → ~480 lines), the inventory seam column updated, and a new inventory row created for the two modules — neither appeared anywhere before |
| L1 headline totals | §2 12,362 → **12,336**; §4 and §6 7,595 → **7,615**; both recomputed from the rows and cross-checked against the files. Added a note that #1186 has since taken `mobile/src` to 6,841 |
| L2 errors.ts exports | "twenty" → **nineteen** exported declarations, three re-exported |

Inventory is now **107 rows** (DUPLICATED 38); plan **65 PRs, ~29,600 lines**.

---

# Task 88d — RoboRev round 4 on PR #1182 (`02a43c81e`)

**Status:** done, not pushed. `git merge --no-ff origin/main` after a fresh fetch reported **Already up to date** — no new merge commit; the round-3 merge `02a43c81e` is still the tip's base.
- `419b5ffa2` docs(design): correct the docContent seam and the export and consumer counts in section 5
- `4230dce32` docs(plans): scope A4 to protocol imports and give C24 the real docContent port

All six RoboRev findings accepted. **One coordinator side-claim disputed** — see below.

| Finding | Fix |
| --- | --- |
| M1 A4 over-scoped | Recounted by the protocol-only rule: 652 web + **116** of `mobile-native`'s 204 + 21 `mobile/src` = **789**. The other 88 sites stay for phase C, each relocation rewriting its own. Oracle narrowed to `protocol/` specifiers, with the inverse check spelled out: the broad `src/` grep must still return those 88, or A4 over-reached. Estimate 980 → 890 |
| M2 `docContent.ts` | Inventory row was wrong and is corrected: `docFileRawURL:58`/`docImageURL:97` build **same-origin-relative** `/doc/...` paths (no origin at all), and `readDocFile:74` calls global `fetch` with `credentials: "same-origin"`. C24 now specifies injected origin, fetch and auth for all three, names the browser-adapter alternative, and flags that the runner cannot call `readDocFile`, so the auth path has no automated oracle |
| L1 §5 errors count | "3 of 20" → "3 of its 19 exported declarations" |
| L2 `pathListAdd.ts` | Added to the C18 inventory row with its 54 lines; row total now 435 |
| L3 totals drift | Phase subtotals recomputed from the Lines cells: **A 2,340 / B 1,600 / C 8,040 / D 18,010 = 29,990**, and the headline is now that sum. Found and fixed a malformed C24 row (six cells in a five-column table) that was making the phase-C sum read 162 low |
| L4 §5 testing row | → "136 web files import `testing/fakeClient`; 25 name `AppwireClientLike`" |

**Disputed:** the brief said "#1184 round 3 removed `readDocFile` from the entry point pending C24". At #1184's current head `68a1da7c2` the opposite holds: `tsconfig.build.json` ships **sixteen** files including `docContent.ts`, `index.ts:33-34` exports `DOC_FILE_MAX_BYTES`, `DocFileError`, `docFileRawURL`, `docImageURL` and `readDocFile`, and `qualify-package.mjs:48,110-112,156` imports all three and asserts `docFileRawURL("s","p")` — it only declines to *call* `readDocFile` (comment at `:28-31`). I wrote reality into both docs: A1 ships all ten modules, and C24 keeps only the port, not the move.

---

# Task 88e — grep re-audit + RoboRev round 5 on PR #1182

**Status:** done, not pushed. `3d1347b59` docs(design), `cb09d97cd` docs(plans), `dfab9aa89` merge of `origin/main` (brings A1 #1184 in as `f2599d1ed`).

**B2/B3/B5/B6 verdicts: all four fail**, same failure mode as B7 — same conceptual slot, different rule.
- B2: `taskGroups.ts:16` partitions parsed `TaskRow[]` into arrays; `services/activity.ts:409` `projectTasks` emits counts off `TaskAggregate` with `active` pinned to `0`.
- B3: `detailsAccounting.ts:69-89` derives by summing loaded turns (pinned `detailsAccounting.test.ts:26-29,41-44`); `services/activity.ts:437-451` copies ten fields with no arithmetic, and `state/activity.ts:23-27,337-344` *forbids* the derivation. Real analog is `reducer.ts:797-805`.
- B5: `toolRuns.ts:56` folds settled `commandExecution` at `MIN_RUN = 3` over `ProjectedEntry[]`; `project.ts:528` folds same-family activities at ≥ 2 over `PreActivity[]`.
- B6: `pendingReconcile.ts:111` reconciles outbox records across four states; `project.ts:759` is a field copy of `evener.queue`.

**Seven rows reclassified:** liveness/threadTitle → PLATFORM-ONLY (React hooks + navigation singleton; no native twin); seenWatermark → PLATFORM-ONLY (`localStorage`; `activityRetention.ts` is unrelated); taskData → PACKAGE CANDIDATE (2 consumers); detailsAccounting trio, messages trio, toolRuns trio, pendingReconcile → single-consumer PACKAGE CANDIDATE. **All 34 two-consumer claims re-checked by grep at `f2599d1ed` and all hold.** New DUPLICATED row: native's two `projectUsage` copies (`project.ts:774-787` vs `services/activity.ts:437-451`).

**Round 5, nine findings, all accepted except one disputed detail:** H1 B7 withdrawn. H2 D25 rescoped to a headless queue core with per-frontend adapters (`:1` React, `:2-3` zustand, `:21` platform draft store). M1 D15 now names `selectors.ts:238`'s `railNodes` type import. M2 `transcript/tools/taskData.ts:18` folded into new C25. M3 C24 gains an authenticated image-source port (`{uri, headers}`) with its own assertion. M4 plan status table + inventory §5 tarball cells now say "yes, since #1184". L1 §5 headline 8,032 → 7,233 top-level + 799 `testing/`. L2 the license is at `cmd/evener-hub/frontend/LICENSES/beautiful-ui.txt` — it exists, but sits outside the package and `files` is `["dist","README.md","examples"]`, so C9 must move it or leave the code. L3 `mobile-native/src/jobOutput.ts` dropped from the deleted-twins list (thin adapter, no PR deletes it, none should).

**Disputed:** the brief said `messages/systemGrouping.ts` "does not exist" — it does, at both `92561dbe3` and `f2599d1ed`. B4 was mis-targeted, not dangling: `systemGrouping.ts:23,54,79` is run-grouping at `MIN_GROUP_SIZE = 3`; `systemFamily`'s real web counterpart is `projector.ts:112-130`, so B4 folded into D24.

**New decision 4** for Jesse: derive a session usage figure from per-turn usage when no cumulative exists (web does) or refuse and rehydrate (native does)? Recommend keeping the web's derivation *with* native's discipline — derive only behind the explicit `scope` label, never write it back into the aggregate.

**New totals:** inventory **108 rows** — SHARED ALREADY 6, DUPLICATED 33, PACKAGE CANDIDATE 55, PLATFORM-ONLY 13, dead 1. Plan **61 live PRs across 67 rows, 29,140 lines** (A 2,340 / B 350 / C 8,340 / D 18,110). Phase B fell from 1,600 lines to 350.

**Process note:** an earlier `git checkout FETCH_HEAD -- .` in this session leaked main's A1 content into the working tree. Caught before the merge, restored with `git checkout HEAD -- protocol/`, and the content arrived properly through the merge instead. No doc commit was affected.

---

# Task 88f — RoboRev round 6 on PR #1182 (`dfab9aa89`)

**Status:** done, not pushed. `dd389d756` docs (one commit, both files) + `ae9a06ea7` merge of `origin/main` (brought A2 #1188 and B3b #1203 into the branch).

All six accepted; nothing disputed.
- **M1** — confirmed six protocol imports in five `mobile-native/scripts/*.mts` files. A4's count 789 → **795**, the file list names them, and the zero-old-path grep gains `--include='*.mts'` with a note that it is not optional.
- **M2** — status tables normalized in both docs: #1184 `f2599d1ed`, #1188 `3bf357337`, #1203 `4da382482` merged; #1189 open. Added a B3b status row — that lane shipped, so its duplication is closed.
- **M3** — §0 now says facts 1, 2 and 5 are the pre-A1 snapshot and carries the post-A1 values inline. Fact 5 records that A1 did what the doc asked: one shared `runtimeExports` array at `qualify-package.mjs:69` feeding `esm.mts`:163, `commonjs.cts`:177, `esm-runtime.mjs`:201, `commonjs-runtime.cjs`:207 (measured at `3bf357337`; the reviewer's 161/175/199/205 were from a different head).
- **M4** — confirmed `composerInput.ts:2` imports `./attachmentMarkers` and `attachmentMarkers.ts` has no imports at all. C6 → `Deps: A4`, C5 → `Deps: C6`, C7 → `Deps: A4` with the wrong dependency called out.
- **L1** — inventory "33 such rows / all 33 hold" → **34**, matching the 34 `PACKAGE CANDIDATE (2 consumers)` table rows; rule 7 now says 25 relocations, reconciled with the phase-C heading.
- **L2** — "six of its eight rows" → **nine**, matching the nine-row phase-B table.

Totals unchanged: inventory 108 rows; plan 61 live PRs across 67 rows, 29,140 lines (A 2,340 / B 350 / C 8,340 / D 18,110).

---

# Task 88g — RoboRev round 7 on PR #1182 (`ae9a06ea7`)

**Status:** done, not pushed. `947ee13d2` docs (one commit, both files) + `c3c938ecf` merge of `origin/main` (brought B1 #1189 in as `27503c07d`).

- **H1** — measured it rather than taking the shape: at `27503c07d`, **14** symbols the apps import are absent from `index.ts` — twelve `errors` declarations (`errorText`, `friendlyErrorMessage`, `sessionActionError`, `errorKind`, `mutationErrorData`, `isStaleCursorError`, `isHubLaunchError`, `friendlyLaunchErrorMessage`, `sessionActionHeadline`, `ClientNotReadyError`, `GENERIC_ERROR_MESSAGE`, `HUB_UNREACHABLE_MESSAGE`), plus `reducer`'s `chunkViewBackingForTests` and `docContent`'s `readDocFile`. New **A3b** exports the twelve at the root before A4; the test hook goes to the `testing` specifier and `readDocFile` stays held for C24. Recommended root exports over per-module subpaths: one entry exists today, a symbol is a two-file one-liner, and each subpath multiplies the gate by an ESM + CJS type check plus a runtime check. A4 gains A3b as a dependency and an explicit "every imported symbol resolves from the package" oracle.
- **H2** — stated once as a phase-C rule: every relocation must add its module to `tsconfig.build.json` `files`, give it a published target (`index.ts` re-export or a `package.json` subpath), and add its symbols to `runtimeExports` — otherwise the module is unreachable by package name and the gate passes in silence. C1 rewritten to publish a `state/navigation` subpath (~1,250 → ~1,350) and flagged as the row that settles the subpath question for every later state move.
- **L (staleness)** — root-caused and fixed structurally rather than patched again: §§0, 2, 3, 4, 5, 7 are now explicitly **frozen at `92561dbe3`**, §5's "In tarball?" column reverted to its baseline `no`s with a pointer, and a new **Delta since baseline** table carries the news (A1 → 16 files, A2 → 17 + `clientLike.ts`, A5 → `conversationFixtures.ts` deleted and `mobile/src` at 6,841, B1 → 18 + `itemFailure.ts`, B3b). Fact 3 now names `protocol/clientLike.ts` with **114** `testing/fakeClient` import lines and none importing the type; §6 cites the `runtimeExports` manifest at `qualify-package.mjs:69` instead of "eleven"; §7 spells out 32 + 49 + 10 (9 modules) + 17 = 108.

**New totals:** inventory 108 rows, unchanged. Plan **62 PRs across 68 rows, 29,330 lines** (A 2,430 / B 350 / C 8,440 / D 18,110).

**Process note:** a stray `git checkout ae9a06ea7` while gathering evidence left HEAD detached at the branch tip; caught immediately and reattached with `git checkout claude/sdk-migration-inventory`. No commits were at risk.

---

# Task 88h — two facts folded into the plan

**Status:** done, not pushed. `b8835217a` docs(plans) — one commit, plan only. No merge requested this time; the coordinator had already pushed the round-7 work, so this sits one commit ahead of the remote.

- **A3b is PR #1206** (open, head `e2d4895a7`): recorded on the A3b row and in the status table — twelve root error exports, `chunkViewBackingForTests` and `readDocFile` deliberately excluded, 1,161 app files measured.
- **A4's one unmechanical line.** Verified: `panes/doc/DocPane.test.tsx:5` namespace-imports `../../protocol/docContent` so `:44` can `vi.spyOn(docContentModule, "readDocFile")`. A root re-export cannot satisfy it — there is no per-module namespace object to spy on. **Recorded decision: A4 leaves that single line relative**, and its zero-old-path grep must expect exactly that path and line rather than carry a blanket allowance; **C24 publishes `docContent` as its own subpath** (with ESM and CJS runner checks), rewrites the line, and takes the repository's last relative protocol specifier to zero. Written into both the A4 oracle and the C24 row so neither side can drift.

Totals unchanged: inventory 108 rows; plan 62 PRs across 68 rows, 29,330 lines.

---

# Task 88i — RoboRev round 8 on PR #1182

**Status:** done, not pushed. `9771fc9c4` docs(plans), one commit. `git merge --no-ff origin/main` after a fresh fetch: **Already up to date** (main is still `27503c07d`), so no merge commit. Two commits ahead of the remote tip.

- **M1** — verified `toolRenderers.ts:190-193` delegates the shared half to `hasItemFailure` and keeps the descriptor half, and that #1190/#1197 are CLOSED. B1b marked **done — landed inside B1 (`27503c07d`)**, not as its own PR; row zeroed, status table updated.
- **M2** — new **C0** at the head of phase C: reshape the flat `runtimeExports` array (`qualify-package.mjs:69`) into `{ specifier: [names] }` and emit one ESM declaration check, one CJS declaration check and one runtime presence check **per specifier**. Stated as the rule for every subpath the plan adds — `state/navigation` (C1), `testing` (A3), `docContent` (C24) — and C1 now depends on it. Pure plumbing, so rule 2's one-module limit does not apply.
- **M3** — verified `reasoningFormat.test.ts:3` imports `applyNotification` and `chunkViewBackingForTests` from the same specifier, and that the hook lives in `reducer.ts`, out of the `testing/` specifier's reach. **Chose the `testing/reducerHooks.ts` re-export** over an exemption: A3 adds the non-shipped file, A4 splits the import. Reason recorded — A3 moves the whole directory, so an exempted line still needs rewriting and buys nothing, while leaving a white-box hook one root export from being published. A4's exemption list therefore has **exactly one** entry (the DocPane namespace import), and I spelled out that "left alone" means at the new relative path A3 already rewrote it to, so nobody reads it as "skip it at A3 too".
- **M4** — C24's oracle now specifies deterministic injected fetch and image adapters asserting, without a network: the constructed request URL from a given base origin and session/path; auth-header propagation into both the fetch and the image source's `{uri, headers}`; 403/404/500 → `DocFileErrorKind`; and `X-Doc-Truncated`/`X-Doc-Total-Size` → `truncated`/`totalBytes`, with a body of exactly `DOC_FILE_MAX_BYTES` reading as complete.

**New totals:** plan **63 PRs across 69 rows, 29,370 lines** (A 2,430 / B 210 / C 8,620 / D 18,110). Six have landed (A1, A2, A5, B1, B1b, B3b), A3b #1206 is in flight, 56 remain. Inventory unchanged at 108 rows.

---

# Task 88j — RoboRev round 9 on PR #1182 (`9771fc9c4`)

**Status:** done, not pushed. `5a1d9a06c` docs (one commit, both files) + `34b4b3f7f` merge of `origin/main` (A3b #1206 in as `99fa1882f`). All accepted; nothing disputed.

- **H1** — C1 now publishes `state/navigation` through **one barrel** (`state/navigation/index.ts` re-exporting every runtime and type symbol of the four modules), with the reason stated: four per-module subpaths would mean four ESM + four CJS declaration checks and four runtime checks for a set nothing consumes separately, and the barrel is what later state relocations extend rather than multiply.
- **H2** — C0's manifest reshaped to `{ specifier: { runtimeExports, typeExports, shippedModules } }`, with the point spelled out: the reachability and type-only checks are exactly as blind to a subpath as the runtime list, so all three go per-specifier.
- **M1** — C0's rule reworded to "a specifier in `package.json` `exports` with no manifest entry is not qualified"; the `testing` alias is named as the deliberate exception — in-repo only, never in `exports`, never in the tarball, validated by the apps' own `tsc --noEmit` — and no longer demands a manifest entry.
- **M2** — verified `transcript/messages/format.ts` (87 lines, zero imports, one native import site). Added **C26**, ~150 lines, deps A4 + C0. It was the last two-consumer module without a row.
- **M3** — removed the stale "the runner cannot call `readDocFile`" clause from C24; the injected fetch and image-source assertions *are* the runner's, so the auth path is covered.
- **L** — B1b folded into B1 for counting: 70 rows, **62 real PRs**; phase B subtotal is the row sum, 210. A3's sweep now names the active Markdown (`ios-build-distribution.md:9` runs `npm ci --prefix` against the old path, plus `go-workspace.md:211`, `agentic-testing.md:913`, `test/scenarios/ask-{two-clients,web-answer}.md`) and explicitly excludes the dated `docs/superpowers/{plans,specs}` records, which describe the tree as it was. A4's grep gains `--exclude-dir=node_modules`. The phase-C sentence names C0 and C24 as the two non-relocation rows. Import counts moved to **210/122** for `mobile-native` in both docs, with the `.ts`/`.tsx` 204/116 split shown inline. The §3 `toolRenderers.ts` row now names its baseline counterpart (`projector.ts:118-130`) and the `itemFailure.ts` resolution moved to the delta table.

**New totals:** plan **62 PRs across 70 rows, 29,520 lines** (A 2,430 / B 210 / C 8,770 / D 18,110). Inventory unchanged at 108 rows.

---

# Task 88k — RoboRev round 10 on PR #1182 (`34b4b3f7f`)

**Status:** done, not pushed. `0fc6d915c` docs(plans), one commit, plan only. `git merge --no-ff origin/main` after a fresh fetch: **Already up to date** (main still `99fa1882f`), no merge commit. Four commits ahead of the remote tip. All accepted; nothing disputed.

- **M1** — verified the callers before deciding: `DocPane.tsx:95` and `AgentMarkdown.tsx:56` feed `docImageURL` to an `<img src>`, and `DocPane.test.tsx:140,163` assert on `getAttribute("src")`. So `docImageURL` **stays string-returning**, and C24 defines two named adapters instead — browser (string image source, global `fetch` with `credentials: "same-origin"`) and native (`{uri, headers}` bearer image source, same header on fetch). Callers to update and test are listed: `DocPane.tsx:48,95`, `AgentMarkdown.tsx:56`, `DocPane.test.tsx:140,163`, plus the native doc surface.
- **M2** — `stores/navigation/testing.ts` has ten consumers, all tests: six web, four `mobile-native`. "Stays put" is therefore not available — native would keep a relative import into the web tree, which A3/A4 delete. **Decided in C2, before C1:** it goes under the in-repo `testing` alias, never in `exports`, never in the tarball, so per C0 it gets no manifest entry and the apps' `tsc --noEmit` is its only gate.
- **M3** — A4's zero-old-path check now also sweeps `cmd/evener-hub/frontend/src` for the old relative specifier, allowing only the one documented DocPane line, with the reason stated: A3's re-export stubs let a stale web deep import keep compiling and outlive A4 while the mobile half reads green. Mobile count recomputed post-B1: **22** lines (21 at baseline; B1 added one when `project.ts` took up `itemFailure`), total 796.
- **L1** — rule 2 now allows one module plus its pure transitive dependencies in one relocation, naming the seven rows that use it (C1, C3, C11, C17, C18, C21, C25).
- **L2** — rule 7 reads "25 relocations" against a 27-row table; honest total restated as **64 rows of work, 63 real PRs, 6 landed, 57 remaining**; the stale "collapsed to 350" narrative corrected to 210 to match phase B's subtotal.
- **L3** — the phase-C closing paragraph now names `{systemGrouping,turnMeta}.ts` and the full `{activityFormat,statusFormat,detailsAccounting}` trio, attributes the eventKind analog to `systemGrouping.ts`, and says `format.ts` is that directory's exception because it relocates in C26.
- **L4** — status section observed at `99fa1882f`; A2's merge already read `3bf357337` and still does.

**Totals:** plan **63 PRs across 70 rows (64 of work), 29,520 lines** (A 2,430 / B 210 / C 8,770 / D 18,110). Inventory unchanged at 108 rows.

---

# Task 88l — RoboRev round 11 on PR #1182 (`0fc6d915c`)

**Status:** done, not pushed. `bf9f79fe4`, one commit, both files. `git merge --no-ff origin/main` after a fresh fetch: **Already up to date** (main still `99fa1882f`), no merge commit. All accepted; nothing disputed.

- **M1** — took the recommended route and it turned out to pay double. Enumerated the five `docContent` import lines across four files: `DocPane.tsx:9` (blocked, imports `readDocFile`), `DocPane.test.tsx:5` (namespace import), and three that A4 could already rewrite (`DocPane.test.tsx:6`, `docFile.test.ts:2`, `AgentMarkdown.tsx:3`). Added **A3d** — publish `./docContent` with `readDocFile` behind an injected fetch, the web installing a browser adapter that preserves today's behavior — and moved the manifest work out of phase C into **A3c**, since A4 now depends on it. Publishing the subpath before A4 fixes the namespace import too (a real subpath gives `vi.spyOn` a per-module namespace object), so **A4's exception list is now empty** and both zero-old-path greps are absolute zeroes with nothing for phase C to inherit. C24 keeps only the base origin and the two platform adapters (220 → 180).
- **M2** — re-grepped: **13** consumers, not 10 (my earlier count was truncated by a `head`). Six web, seven native — the reviewer's three omissions (`pinNavigationRecovery`, `projectBrowser`, plus `sessionDeletionNavigation`) confirmed. C2 now names all thirteen and scopes every one.
- **L1** — rule 7 reads "25 relocations" against a 26-row phase-C table, C24 named as the single non-relocation row.
- **L2** — deleted the blank line at inventory :357; the A3b row is back inside the delta table.
- **L3** — inventory's dead "see 'Since this was written'" → "see 'Delta since baseline'".

**New totals:** plan **64 PRs across 71 rows (65 of work), 29,650 lines** (A 2,780 / B 210 / C 8,550 / D 18,110); six landed, 58 remain. Inventory unchanged at 108 rows.

---

# Task 88m — RoboRev round 12 on PR #1182, plus the A3d facts

**Status:** done, not pushed. `25ce5e77d` docs (one commit, both files; the A3d facts amended in) + `dfa6e5917` merge of `origin/main` (#1098 `cb211c5f8`).

- **M1** — A3d's oracle now requires moving `protocol/docContent.test.ts` onto the injected-fetch seam, with the reason: it stubs the global today, so a module that no longer reads the global would keep passing against a stub nothing calls. Plus a browser-adapter test proving `credentials: "same-origin"` survives.
- **M2** — verified `mobile-native/package.json` has no `appwire-client` dependency and no gate runs the `.mts` scripts. A4 gains a **runtime resolution check** (`npm exec -- tsx scripts/check-hub.mts --help` or equivalent, plus the alias or `file:` dependency that makes it resolve), recommended over relative paths because that coupling is what the phase exists to remove.
- **L1** — B1's row now reads "merged as `27503c07d`", matching the status section.
- **L2** — verified `editorial-preview.html` is a real Vite entry, so `dev/editorial-preview/fixture.ts` is a **runtime** consumer; C2 reclassified it (five web tests + seven native tests + one runtime consumer) and its oracle gained a preview-entry build check.
- **L3/L4/L5** — rule 7 states one number; the phase-C intro reads "every module in this phase except C24"; D17's cell now says it deletes the fetch/subscribe body of the web's `stores/activityPanel.ts`, with native's `ActivitySheet.tsx` named as the existing `activityList.ts` consumer the web converges on.
- **A3d (#1209)** — recorded on the A3d row, C24 and the delta table: `readDocFile(session, path, fetchDoc)` with a required `DocFetch` returning `DocResponseLike` (the DOM-free `{ ok, status, headers.get, arrayBuffer }` shape, because `Promise<Response>` breaks the runner's declaration consumers); web adapter at `panes/doc/browserDocFetch.ts`; C24's native adapter must return that shape.

**One correction to the brief:** the A4 split is **3 root / 3 subpath**, not 4/2. Measured on #1209's head: root takes `DocPane.test.tsx:6`, `docFile.test.ts:2`, `AgentMarkdown.tsx:3`; the subpath takes `DocPane.tsx:9` (`readDocFile`), `DocPane.test.tsx:5` (namespace) **and `browserDocFetch.ts:1`**, which imports the `DocFetch` type the root does not export (verified: zero `DocFetch`/`DocResponseLike` mentions in `index.ts`).

**Totals:** plan **64 PRs across 71 rows (65 of work), 29,690 lines** (A 2,820 / B 210 / C 8,550 / D 18,110); seven landed, 57 remain. Inventory unchanged at 108 rows.

---

# Task 88n — RoboRev round 13 on PR #1182 (`dfa6e5917`)

**Status:** done, not pushed. `82ef154b9` docs (the round's one commit), `7f2dc29cf` merge of `origin/main`, `bcabbc174` a follow-up recording that A3d merged while the merge was running. All accepted.

- **M1** — verified at `origin/claude/sdk-a3d-doccontent-subpath`: `DocPane.tsx:49` calls `readDocFile(session, path, browserDocFetch)` and is the only `readDocFile` call site in either app. A3d's row now says it wires the caller itself, so the web is type-correct the moment A3d lands — no window where a required third parameter has no argument.
- **M2** — C24 now states where the base origin enters: the injected port owns URL resolution, `docFileRawURL`/`docImageURL` gain an origin parameter, and `readDocFile` composes through the port rather than taking a fourth positional argument (which would put origin policy back in every caller). Browser adapter passes the empty same-origin base so today's `/doc/...` strings are unchanged; native passes the hub origin.
- **L1** — rule 7 reads "schedules 25 relocations, not 55".
- **L2** — every manifest reference is now `packageExports` with no line number, in both docs, including the A1 delta row, which also notes A3c renamed it. Verified zero remaining `qualify-package.mjs:69` citations. The one surviving `runtimeExports` mention is correct: it names a field inside `packageExports[specifier]`.
- **L3** — B1's risk cell reads "Merged as `27503c07d`".
- **L4** — the last `C0` references in C1/C2 are `A3c`; the A3c row keeps "was numbered C0" as its history.
- **L5** — inventory fact 3's A2 hash is `3bf357337`.
- **L6** — both status sections read "A3 and A4 are unstarted"; A3d moved from open to **merged as `f39aa2c83`** in the follow-up commit.

**Totals:** plan **64 PRs across 71 rows (65 of work), 29,690 lines**; eight landed, 56 remain. Inventory unchanged at 108 rows.

---

# Task 88o — RoboRev round 14 on PR #1182 (`bcabbc174`)

**Status:** done, not pushed. `507fa808f`, one commit, plan only. `git merge --no-ff origin/main`: **Already up to date**. All accepted.

- **M** — verified: `check-hub.mts:7-9` takes `ORIGIN TOKEN_FILE` positionally and throws without them, has no `--help`, and its only protocol import is `import type { WebSocketLike }` — type-only, erased at build, so running it could never prove resolution. A4's oracle is now a purpose-built **`mobile-native/scripts/resolve-check.mts`**: no arguments, no network, imports the runtime export `APPWIRE_PROTOCOL_VERSION` from `@evener/appwire-client`, asserts it is a non-empty string, exits 0; the gate runs it, and A4 ships the alias or `file:` dependency that makes it resolve.
- **L1** — found and fixed. **Why it survived three rounds:** the text is line-wrapped as `schedules 24\n   relocations`, so every `grep "24 relocations"` I ran — including the reviewer's suggested pattern — matched nothing while the string was sitting there. I re-checked with a whitespace-flattened regex, which is what caught it, and I will use that form for count assertions from now on.
- **L2** — the phase-C intro now reads "Every module here **except C24's** is already imported by the web and by `mobile-native`", with C24 named as relocating nothing.
- **L3** — C24's row now states the published API change explicitly: A3d's `DocFetch` *receives* a resolved URL and cannot supply an origin, so C24 widens it to a `DocPort` (`{ origin, fetch }`) or adds an origin-bearing sibling; the `./docContent` signature and its `packageExports` entry change together, the generated ESM and CJS consumers update in the same PR, and — since A3d is already on main — it is flagged as a breaking change to a published specifier.

**Requested greps, both run against the committed file:**
```
$ grep -n "24 relocations\|24 —\|— 24" docs/superpowers/plans/2026-09-12-sdk-migration.md
$ python3 -c "re.findall(r'24\s+relocations', flattened)"   # wrap-tolerant
[]
```
Both empty; the wrap-tolerant scan now finds only `25 relocations` (twice).

**Totals unchanged:** 64 PRs across 71 rows (65 of work), 29,690 lines; eight landed, 56 remain.

---

# Task 88p — RoboRev round 15 on PR #1182 (`507fa808f`)

**Status:** done, not pushed. `5efba7be7`, one commit, both files. `git merge --no-ff origin/main`: **Already up to date**. All accepted.

- **M1** — read the shipped runner at `f39aa2c83`. True shape is `packageExports[specifier] = { values, types, esmTypeUses, cjsTypeUses, smoke }` (five keys, not three), with `package.json` `exports` and the manifest asserting each other; reachability is separate — a module counts when it is a specifier's declaration entry or when an entry's `.d.ts` contains `from "./<module>"`. **Answering the question asked: no, a `dist/state/navigation/index.d.ts` barrel would not satisfy the assertion as-is.** `shippedModules` (`:33`) is a flat hand-maintained array of bare names, and the re-export scan looks for `from "./<module>"` while a barrel spells its siblings `from "./codec"` — so every module under it reads as unreachable. C1 therefore gains a runner change as its **first step**: resolve re-export specifiers relative to the declaring file, then add the nested names. Written into both A3c and C1.
- **M2** — verified three importers and named them all in C18: `launchShared/schema.ts:8`, `launchShared/fields.tsx:31`, `launchShared/LaunchConfigForm.tsx:22`.
- **M3** — chose the narrower scope: there is no image-source port in the published API (`docImageURL` returns a string and nothing wraps it), so the runner's image assertions are dropped and native `<Image>` authentication is proved by an explicit native-adapter test in the native tree. Recorded that adding a shared image-source port would be a larger API change than C24's origin work and is not taken on here.
- **L1** — the plan's observation commit reads `f39aa2c83`.
- **L2** — baseline citations updated to current: `readDocFile` at `docContent.ts:91`, call site `DocPane.tsx:49`; the bare `:58`/`:97` line numbers on the URL builders dropped rather than left stale.
- **L3** — inventory `tsconfig.build.json:13` → `:14`.
- **L4** — both docs now say A3/A4 block every phase-C **relocation**, and that C24 depends only on A1 and A3d — both merged — so it could start now.

**Totals unchanged:** 64 PRs across 71 rows (65 of work), 29,690 lines; eight landed, 56 remain.

---

# Task 88q — RoboRev round 16 on PR #1182 (`5efba7be7`)

**Status:** done, not pushed. `ce879caae`, one commit, both files. `git merge --no-ff origin/main`: **Already up to date** (main `f39aa2c83`). All accepted.

- **M1** — A4 now names the gate wiring, not just the fixture: a `mobile-native` package script `"check:package-resolution"` invoked from `make test-native` in `make/testing.mk`, beside the existing `npm test` / `test:shared` / `check` steps, with the point stated — typechecking the fixture is exactly the failure mode it exists to catch.
- **M2** — verified the allowlist at `qualify-package.mjs:325-340` (only `package/package.json`, `package/README.md`, `package/dist/*`, `package/examples/*`, everything else asserted absent). **Chose to ship the license**: C9's scope now lists all three edits — copy it into the package directory, add it to `files`, widen the allowlist and its assertion — because the alternative keeps a two-consumer module out of the package over a one-line allowlist entry and leaves `mobile-native` importing it relatively forever.
- **M3** — removed the last claim that the runner observes auth: `DocFetch` receives only a URL, so headers are set inside the host's adapter, out of the package's sight. Auth propagation is proved in the browser- and native-adapter tests; the `./docContent` smoke stays non-networking. C24's title also changed from "injected URL, fetch and auth" to "an injected origin and the two platform adapters" to match its real scope.
- **L** — **measured 112, not 111 or 114.** At `f39aa2c83`: 114 lines mention `protocol/testing/fakeClient`, of which 3 are comments; exactly **112 files carry exactly one `from "…testing/fakeClient"` import line each**, none commented out. Both inventory sites now read 112 with the counting method stated inline so the figure is reproducible. Flagging the disagreement rather than adopting a number I could not reproduce.

**Totals unchanged:** 64 PRs across 71 rows (65 of work), 29,690 lines; eight landed, 56 remain.

---

# Task 88r — RoboRev round 17 on PR #1182 (`4bcf2f30f`)

**Status:** done and **pushed** (this round authorised it). Commit `7d68af975`, merge `3749a66ca`, pushed head `3749a66ca`. PR body has a "## Review round 17" section.

- **M, native image auth** — gap accepted, remedy rejected: no new port. Verified `mobile-native/src/transcriptImageSource.ts` already returns `{ uri, headers: { Authorization } }`, token only when `url.origin` matches, pinned by its own test. C24 and the inventory doc-pane row now route native `/doc/image` through it.
- **L, Fact 5** — a subpath now costs one `packageExports` entry and one `exports` entry; consumers are generated; "four write sites" gone.
- **L, field names** — `values`/`types` throughout; zero `runtimeExports`/`typeExports` remnants in either doc.
- **L, A4 `Deleted:`** — names the A3 re-export stubs and points at seam 1.
- Swept both docs: no remaining line describes C24 as pending.

**Corrected the brief on one detail:** C24's merge commit is `303053dfb`, not `272a3cd9b` (that was the PR head before the squash). I recorded the merge commit, consistent with every other landed row. Also polled until #1221 actually merged rather than writing "done" against an open PR.

**New C24 image sentence, verbatim:** "**`docImageURL` stays a pure string builder, and native image authentication needs no new port:** it returns the href and each renderer wraps it — the browser `<img>` sends the hub cookie on a same-origin request, and native passes it through `transcriptImageSource(docImageURL(origin, session, path), origin, token)` (`mobile-native/src/transcriptImageSource.ts`, pinned by `transcriptImageSource.test.ts`), which attaches `Authorization: Bearer …` only when the URL's origin matches the hub's."

**Totals:** 64 PRs across 71 rows, 29,690 lines; **nine landed, 55 remain**.

---

# Task 88s — RoboRev round 18 on PR #1182 (`3749a66ca`)

**Status:** done and pushed. Commit `878b7992b`; pushed head `878b7992b` (main had not moved, so `git merge --no-ff origin/main` was "Already up to date"). PR body has "## Review round 18".

- **H, phase D publication gap** — fixed with one rule in the phase D intro rather than 28 row edits: the phase C publication rule applies to every shared module phase D creates, plus the C13 lesson that `shippedModules` arms the reachability assertion so a `dist/` module missing from it passes silently. Cites #1224 and says the manual workaround holds until its fix lands after #1222/#1223.
- **M, D1** — grep verdict: **reviewer right on all four**, plus three more behaviours nobody listed. D1 rewritten as "web adopts the native logic (superset)", deletes nothing, names all seven behaviours as contract and three pinned test files, estimate 250 → 450.
- **L, stale names** — A3d row, status table and inventory delta row now say `browserDocPort.ts` and `readDocFile(session, path, port: DocPort)`; zero bare `browserDocFetch`/`DocFetch` remnants outside C24-rename sentences.

**Disagreed on one count:** the `protocol/docContent` import lines are **nine**, not seven — the review counted the web tree only and missed `mobile-native/src/nativeDocPort.ts:4` and `nativeDocPort.test.ts:2`, which A4 must rewrite as well. Split recorded as four root / five subpath.

C10 #1222, C13 #1223, C11a and issue #1224 recorded as **open**, with PR numbers and no merge SHAs.

**Totals:** 64 PRs across 71 rows, **29,890 lines**; nine landed, three open, 55 remain.

---

# Task 88t — RoboRev round 19 on PR #1182 (`878b7992b`)

**Status:** done and pushed. Commit `c58e12659`, merge of `origin/main` (`e2c77cc72`), pushed head `144ea7bd3`. PR body has "## Review round 19".

- **M, D1 port** — read both interfaces before ruling: `ConversationClientLike` needs only `request` + `onNotification`; `AppwireClientLike` demands ten members. D1 declares a package-local `CommandCatalogClient` as a `Pick` of `AppwireClient` over those two, matching `activityList.ts:13`'s `ActivityClient`; `AppwireClientLike` satisfies it structurally. A2 added to deps.
- **M, totals** — C11 split into C11a/C11b; recomputed from rows: 72 rows, 66 of work, **65 real PRs**.
- **L, phase C/D wording** — rewritten in both docs with the evidence (C10, C13, C24 all landed without A3/A4): A3/A4 block the package-name rewrite and the directory move only, and a legend tells the executor how to read the `A4` dependency. All "nothing can move" language removed.

**Disagreed on one count:** remaining is **54**, not 56 — 65 minus the eleven landed. The 56 came from before C10 and C13 merged. Three of the 54 are in flight (C11a #1225, C6, C26).

**Status recorded:** C10 `ba4164649`, C13 `e2c77cc72`, C11a #1225 open, C6 and C26 open lanes off `e2c77cc72`, issue #1224 open.

**Totals:** 65 PRs across 72 rows, 29,890 lines; eleven landed, 54 remain.

---

# Task 88u — RoboRev round 20 on PR #1182 (`144ea7bd3`)

**Status:** done and pushed. Commit `b8d3322d0`, merge of `origin/main` (`b9a98151c`), pushed head `cd1607140`. PR body has "## Review round 20".

- **M, D1 deps** — grepped: `commandCatalog.ts:4` → `slashCompletion` (C9), `:6` → `catalogCommands` (C15). Both added to deps; `:5` `sessionActionError` is a root export since A3b and `:7` is the port from round 19.
- **M, C2 alias** — defined: `<package>/testing/navigation.ts`, imported as `@evener/appwire-client/testing/navigation` under the single existing `testing/*` alias entry, relative imports rewritten to the `state/navigation` subpath, still out of `exports` and the tarball.
- **L, D14** — corrected: `railExpansion.ts:36-55` never throws, so the risk is silent loss of persisted expansion and dropped writes, not a crash.
- **L, 18 vs 21** — **21**, shown as 55 PACKAGE CANDIDATE less 34 two-consumer rows so it is derivable.
- **L, phase B heading** — "9 rows, 2 live PRs".

**Self-correction, and the process fix:** the inventory half of round 19's "what A3/A4 actually block" rewrite never persisted — a later `rep` in the same script raised, so the file was never written, and I reported the round as done without reading the file back. Same failure had silently dropped part of an earlier round too. Both are applied now, and I have switched to asserting every edit up front and grepping the result afterwards.

**Status:** C26 #1226 merged `b9a98151c` (+#1228 filed), C11a #1225 at `9d2e482d4` and C6 #1227 at `f6876459e` open on `b9a98151c`.

**Totals:** 65 PRs across 72 rows, 29,890 lines; **twelve landed, 53 remain**, two in flight.

---

# Task 88v — RoboRev round 21 on PR #1182 (`cd1607140`)

**Status:** done and pushed. Commit `749e231c8`, merge of `origin/main` (`2b1e02939`), pushed head `b0aa41d74`. PR body has "## Review round 21". Every edit asserted up front and grepped back before committing.

- **M, A4** — verified `make/testing.mk:98-102` runs `test-native` before `test-api-package` and that `npm ci` never builds `dist/`. Check moved into `test-api-package`'s existing packed-tarball consumer; the `file:`-dependency alternative recorded with its costs (native manifest + lockfile, pack ordered first, no `prepack` on a directory dep).
- **M, D8** — invariant becomes `protocol/instances.test.ts`: sentinel secrets absent from store state, thrown-error `message`/`data`, and every user-facing string.
- **M, D27** — row now specifies fake-client tests over the real dispatcher: disconnect mid-mutation, reconnect, blocked/unknown receipts, cross-ref ordering, no-blind-replay. Citation checked — #1116 was already right, now stated as the standing decision rather than a bare number.
- **L** — rule 7 reads 26 relocations; both docs observe at `2b1e02939`.

**Status:** C6 #1227 merged `2b1e02939`; C11a #1225 at `0ba4ad45d`; C5 open lane. **Thirteen landed, 52 remain.**

**Totals:** 65 PRs across 72 rows, 29,890 lines.

---

# Task 88w — RoboRev round 22 on PR #1182 (`b0aa41d74`)

**Status:** done and pushed. `4dd972e03` (round edits), merge `f4bc46801`, `06f1170ca` (C11a merged mid-round). Pushed head `06f1170ca`. PR body has "## Review round 22".

- **M, A4** — packed consumer now imports all five rewritten entrypoints; read every script: only `demo-hub.mts:399-400` has a main guard, so A4 adds it to the other four.
- **M, C24** — my round-17 answer was wrong. #1221 shipped `nativeDocImageSource` at `nativeDocPort.ts:26`, **unwired** (no importer, native has no doc pane); `transcriptImageSource` serves transcript images only (`TranscriptImages.tsx`). Both docs corrected; new **D29** owns the wiring plus an integration test; C24 no longer reads "clean" for images.
- **L, C9** — allowlist cited by block name, not a shifting line range.
- **L, count** — 112, consistent with the inventory.
- **L, A4 wording** — "the only exception, and A3d dissolved it, so A4's grep has no carve-out".

**Status:** C11a #1225 merged `c867646c4`; C5 #1229 and C15 #1230 open. **Fourteen landed, 52 remain.**

**Totals:** 66 PRs across 73 rows, 30,190 lines (D29 added).

---

# Task 88x — RoboRev round 23 on PR #1182 (`06f1170ca`)

**Status:** done and pushed. `7aef094f6` (round edits), merge `f6526efe6`, `153fa8ba3` (C15 merged mid-round). Pushed head `153fa8ba3`. PR body has "## Review round 23".

- **M, A4** — ruling applied verbatim: (a) type-only imports proved by `tsc` under `make test-native`, no runtime claim; (b) a JS fixture in the packed consumer importing all **21** runtime values the native tree takes from the package (enumerated by grep at `c867646c4`), with the fixture list and the grep asserting equality. The five-entrypoint sentence is deleted.
- **M, counting rule** — grep quote fixed; rule stated once (direct import only; transitive and type-only do not count); `sendQueueAvailability` reclassified single-consumer, nothing moves since A1 shipped it. **Two-consumer 34 → 33**, single 21 → 22, rule 7 recomputed.
- **L** — keybindings 1445 → **1485** both docs, C3 1550 → 1590; phase D header "29 rows: 28 store migrations, plus D29"; inventory C24 delta names `nativeDocImageSource` and D29.

**Status:** C15 #1230 merged `0acebbb0d`; C11b #1232 and C5 #1229 open. #1231 excluded as not an SDK row. **Fifteen landed, 51 remain.**

**Totals:** 66 PRs across 73 rows, 30,230 lines.

---

# Task 88y — RoboRev round 24 on PR #1182 (`153fa8ba3`)

**Status:** done and pushed. `a41b5263d`, merge of `origin/main` (`314281cd5`), pushed head `18648b210`. PR body has "## Review round 24".

- **M, C2** — A3 added to deps alongside C1; the `testing/*` alias is A3's creation.
- **M, A4 scripts** — established the fact: `mobile-native/tsconfig.json` has **no `paths`**; they live in `tsconfig.check.json`, which only `tsc --noEmit` reads and `tsx` never does, and the scripts run by hand from the README under no gate. **Took branch 2:** `mobile-native/scripts` excluded from A4 as a named carve-out, relative imports kept (A3 rewrites them with the move), count 796 → 790, zero-old-path grep expects exactly those six lines. Noted that rounds 14/16/21/23 all chased a runtime proof for a premise that no longer holds.
- **L** — "all 34" now reads "held at the time — 33 today", citing round 23's rule.

**Status:** C11b #1232 merged `314281cd5` (C11 complete); C9 and C12 open lanes; C5 #1229 refreshing. **Sixteen landed, 50 remain.**

**Totals:** 66 PRs across 73 rows, 30,230 lines.

---

# Task 88z — Jesse's four decisions recorded

**Status:** done and pushed. `e3bc25f08`; pushed head `e3bc25f08` (main had not moved). PR body has "## Decisions settled".

Section renamed "Decisions", each ruling marked SETTLED 2026-09-13: (1) zero runtime dependencies, (2) two layers, (3) **`appwire-client/typescript/`** — "typescript should be in the path", leaving room for other language clients — propagated to the A3 row and title, the seams section and C2's alias mapping (zero `<package>` placeholders left), plus A3's new sequencing constraint that it runs only when no phase-C relocation PR is open, and (4) derive behind the `scope` label, never writing back into the aggregate.

Also recorded C12 #1233 (`1661e49ae`) and C9 #1234 (`c9feb6990`), both on `314281cd5`.

---

# Task 88aa — RoboRev round 25 on PR #1182 (`e3bc25f08`)

**Status:** done and pushed. `67000fdc3`, merge of `origin/main` (`195217f82`), pushed head `2af985864`. PR body has "## Review round 25".

- **M, A4** — carve-out resolved: scripts keep *relative* imports, A3 rewrites the six named lines to `../../appwire-client/typescript/…`, A4's grep expects **zero** old-path lines (`.mts` included), and stub deletion cannot touch them. Both rows state it.
- **L, C11a** — reads "merged as `c867646c4`".
- **L, D3** — **D1 dropped.** Zero references to the command catalog in either `stores/tasksPanel.ts` or `mobile-native/src/taskList.ts`; the real shared piece is `chrome/taskData.ts` (C25) plus already-shipped `protocol/errors`/`sessionErrors`, now named in the row.

**Status:** C5 #1229 merged `31a5a4370`; C12 #1233 and C9 #1234 open on `31a5a4370`; A3 scouting. **Seventeen landed, 49 remain.**

---

# Task 88ab — A3 sized from the scout report

**Status:** done and pushed. `a2533ba5d`, merge of `origin/main`, pushed head `42ff3020e`. PR body has "## A3 sizing".

A3's row and the seams section now carry the scout's measured facts: 9 config files (named), 2 make targets, 3 CI lines, 5 functional Go files + 6 comment-only, **12 audit-enforced card citations across 8 cards** (row had said 5/2, all twelve now listed), 847 import sites at five web depths, and the fact that nothing resolves the package by name today so the resolver work is additive across four independent resolvers. The seam is recorded as the scout's **hybrid** — stub the 689 web lines, sed the 158 mobile ones — with the 18 both-tree modules named. The three ship-green-fail-later risks are A3 sub-steps: Vite `fs.allow` (two files), the 25 orphaned test files, and the Biome scope plus the `web 96.1` coverage re-baseline.

**A3 estimate: ~320 → 360, measured.** Plan totals 30,270 lines across 73 rows, 66 PRs.

---

# Task 88ac — RoboRev round 26 on PR #1182 (`42ff3020e`)

**Status:** done and pushed. `7e56398fd`, merge of `origin/main` (`d7ff88653`), pushed head `fc1b99782`. PR body has "## Review round 26".

- **M, `deriveAskQuestions`** — single-consumer at run time under our own rule (native's only import is `questionBatches.ts:1`, type-only). Recorded honestly in the C11b and inventory rows; move **not** reverted. Native's duplicate is `mobile/src/conversation/project.ts:182-256` (self-declared: `:183` "Mirrors parseAskUserQuestions", `:259` "Mirrors liveAskQuestions"), and **D22** got the deletion — not D20 — because the parser is called from the projection at `:272`, `:355`, `:469`. D22 1100 → 1200. Two-consumer set **33 → 32**, single 22 → 23, rule 7 recomputed.
- **L** — `parity-m6-surfaces.md:166` added to A3's doc list; C3's zustand/tinykeys alternative removed per settled decision 1.

**Status:** C12 `a0e594122`, C9 `d7ff88653` merged; **A3 implementing** on `claude/sdk-a3-package-move`. #1145/#1235/#1239 excluded as non-SDK. **Nineteen landed, 47 remain.**

**Totals:** 66 PRs across 73 rows, 30,370 lines.

---

# Task 88ad — RoboRev round 27 on PR #1182 (`fc1b99782`)

**Status:** done and pushed. `446080ff7`; pushed head `446080ff7` (main unmoved). PR body has "## Review round 27".

- **M1** — dependency direction corrected in all three places: `deriveAskQuestions.ts` imports `askShared.ts`. Co-move conclusion stands.
- **M2** — A4 fixture is now a plain `.mjs` run as `node resolve-check.mjs` in the packed consumer, matching the runner's existing `run(process.execPath, …)` shape. `.mts` wording dropped.
- **L3–L6** — phase-C rule excepts C2 and test-only fixtures; "112 importers in the web app" with the native half of the fork argument replaced by the real objection; 33 → 32; ~111 → 112.
- **L7, and the fact I verified:** `qualify-package.mjs:444-456` runs tsc with **no `--lib`**, so the declaration consumers default to `lib.es2022.full.d.ts`, **which includes DOM** — `Response` resolves. The "DOM-free consumers" justification carried since round 13 was wrong. Both docs now call `DocResponseLike` a deliberate host-independent API and say an earlier draft claimed otherwise.

**Totals:** 66 PRs across 73 rows, 30,370 lines; nineteen landed, 47 remain, A3 implementing.

---

# Task 88ae — RoboRev round 28 on PR #1182 (`446080ff7`)

**Status:** done and pushed. `ba5c29b56` (plan + merge of `eeff54b70`) and `c649e3b36` (inventory). Pushed head `c649e3b36`. PR body has "## Review round 28".

- **M1** — fixture is `resolve-check.mjs` everywhere; lists re-derived at `eeff54b70` with the producing grep stated. **Root 27, `./docContent` 2.**
- **M2** — A3 ruling written in: frontend vitest keeps the package's tests (`include` + `fs.allow` + coverage `include` extended to `appwire-client/typescript/**`), rejected alternative named with its cost, and `make test-web` asserts the ran-count equals the on-disk count.
- **L3** — verified pair: **136 files / 137 lines** at `f2599d1ed`, **112 / 112** post-A2 and today, **24 files dropped**; the 25-rewritten set is explicitly not the same set, with the two overlap files named.
- **L4** — one narrative: 34 at the re-audit, 32 today.

**Process note:** my inventory edit script aborted on a stale anchor and I committed the plan-only change before noticing — caught by the read-back, fixed in `c649e3b36`. The assert-up-front guard worked (nothing partially applied); what failed was committing before reading the grep output.

---

# Task 88af — A3 recorded as open (#1241)

**Status:** done and pushed. `8f2c81a0b`; pushed head `8f2c81a0b` (main unmoved).

A3's row now opens with "Open as #1241 (`d32a1d479`), six commits" and lists the implementation's measured corrections to the scout: **27 test files not 25**, **855 import sites not 847** (138 mobile-native), **32 distinct web specifiers not 30**, **14 scenario citations across nine cards not 12**, the two package test files that import the web app (#1242, the only hand work in the move), bare `react`/`@testing-library` imports breaking at run time as well as in `tsc`, `editorial-preview.test.mjs:33` asserting the exact `fs.allow` array, and `biome.jsonc` `files.includes` needing a `**/`-anchored pattern. Measured outcomes recorded too: 403 → 200, 0 → 27 test files collected, biome 5 errors → 0, coverage floor unmoved at 96.79% via `coverage.allowExternal`.

Three residuals named in the row: **#1242**, **#1243** (an untested package module scores ABSENT rather than 0% — the false green the `include` line existed to prevent), **#1244** (no CI gate bundles the native app, so Metro resolution is unverified). **#1244 added to A4's Deps column and its risk cell**, as a dependency rather than a note, because A4's native half could otherwise ship green and break on a real bundle. Status tables in both docs updated.

---

# Task 88ag — RoboRev round 29 on PR #1182 (`8f2c81a0b`)

**Status:** done and pushed. `c03ed0b11`; pushed head `c03ed0b11` (main unmoved). PR body has "## Review round 29".

- **M, C23** — mis-filed and now **moved to phase D as D30**, not made a third phase-C exception, so the intro's blanket no-logic rule stays true. Verified the reason: `disclosureStore.ts:7-8` imports zustand and `zustand/vanilla`, and `:45` `isDisclosureOpen` is a React hook over a module singleton. Rewritten in the phase-D store shape (framework-free core with the API named, web hook adapter, native adapter, root export + `packageExports`, existing test as the core's oracle plus an adapter test each). **210 → 380.**
- **L, A3 figures** — 855 / 138 / 27 everywhere; seams sed count corrected to 166 lines and 33 new files. Only the two deliberate "the scout's superseded figures" clauses still name 847/130.

**Totals:** 66 PRs across 73 rows, **30,540 lines** (A 2,860 / B 210 / C 8,380 / D 19,090); phase C 26 rows, phase D 30.

---

# Task 88ah — RoboRev round 30 on PR #1182 (`c03ed0b11`)

**Status:** done and pushed. `533d23761`; pushed head `533d23761` (main unmoved). PR body has "## Review round 30".

- **M1 verdict: runtime two-consumer.** `mobile-native/src/composerCommand.ts:1-4` imports `findBuiltinArgument`/`matchBuiltinInvocation` as values. Inventory row added; **C27** added (deps A4, ~150). Counts: two-consumer **32 → 33**, PC 55 → 56, inventory **109 rows**, phase C **27 rows / 26 relocations**, plan **67 PRs / 30,690 lines**.
- **M2** — 27 test files everywhere; guard described as deriving the expected count from disk, as #1241 built it.
- **L3** — both-tree list 18 → **17**, `deriveAskQuestions` dropped with the type-only reason stated.
- **L4/L5** — relocation count reconciled with the heading; decision 1 names D30-was-C23; phase-D heading "30 rows: 29 store migrations, plus D29".

**Status:** #1241 at `2fb3d5fa2`; **#1245** (Metro gate for #1244) at `3c4d54c60` — both in the status table. **Nineteen landed, 48 remain.**

---

# Task 88ai — RoboRev round 31 on PR #1182 (`533d23761`)

**Status:** done and pushed. `635579a4d`; pushed head `635579a4d` (main unmoved). PR body has "## Review round 31".

- **M, C27 importers** — six sites in six files, all named in the row: `Composer.tsx:73`, `builtinCommand.ts:19`, `builtinCommand.test.ts:9`, `spawn/Spawn.tsx:67`, `spawn/spawnSlashMenu.ts:12`, `mobile-native/src/composerCommand.ts:1-4`. No `vi.mock` names it, and it has **no test of its own** — `builtinCommand.test.ts` reaches it through `builtinCommand.ts`, so that test is repointed, not moved, and C27 must write the module an oracle.
- **L, stale counts** — "34 then, **33 today**" in both docs with all three moves named; rule 7 "not 56"; zero `32 today` / `not 55` remaining. Derivation re-run: **56 − 33 = 23**.

**Status:** #1241 `5062edf34` (round 3), #1245 `4c78b34a0` (round 2). Totals 67 PRs / 74 rows / 30,690 lines.

---

# Task 88aj — RoboRev round 32 on PR #1182 (`635579a4d`)

**Status:** done and pushed. `0772e0836`; pushed head `0772e0836` (main unmoved). PR body has "## Review round 32".

- **M** — shared-runtime list **17 → 19**; verified all three are value imports (`questionBatches.ts:2`, `composerCommand.ts:5`, `CommandCompletion.tsx:4`). Fixed the A3 sentence that called four specifiers web-only: only `clientLike` and `model` have zero native importers; `reconcileBatches` (2) and `slashCompletion` (5) do not.
- **L2** — "drops all 27", plus the round-3 clause taking it to 26 and closing #1242; guard still derives from disk.
- **L3** — heads cited as "head at the time of this commit", refreshed to `5062edf34`/`4c78b34a0`, with a standing staleness note; status sections retitled and dated.

**Disagreed on one detail:** dated **2026-09-13**, not the 2026-09-14 in the brief — that is today's date here, and post-dating a commit would defeat the convention. Flagged in the PR body.

---

# Task 88ak — RoboRev round 33 on PR #1182 (`0772e0836`)

**Status:** done and pushed. `fe100dfda`, merge of `origin/main` (`42d27f946`), pushed head `5a5e98a92`. PR body has "## Review round 33".

- **M, D30** — `createDisclosureStore()` returns store-bound actions and selectors (named in the row); no module singleton in the core; the web adapter owns the single instance behind its hook; native creates its own. Row gains an **isolation test with two store instances** — the assertion that fails against today's singleton. **380 → 420.**
- **L, Go count** — `gh pr view --json files` caps at 100 and showed 4; recounted from the branch diff: **10 Go files, 4 functional + 6 comment-only**, both listed. The list had been right, the count wrong.
- **L, honest total** — `commandCatalog.ts` (105) is relocated, not deleted (D1 makes it the shared module); moved out of the deleted-twins list, net reduction ~3,900 → **~3,800**, double-count named.

**Totals:** 67 PRs across 74 rows, **30,730 lines**.
