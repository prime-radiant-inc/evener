# Task 2A runtime history core report

## Result

Task 2A runtime history/routing core is implemented and committed. This is only the bounded core slice; Task 2B integration and the rest of Task 2 remain incomplete.

Commit: `129e47b48f4af41c46030d9889a579aa1e5a91c4` (`feat(web): bind attention history to retained readers`). The commit contains the four required attentionHistory/routing files and the explicitly permitted narrow validity extraction in `attentionPlacement.ts`.

## Changes and interfaces

- `cmd/evener-hub/frontend/src/shell/attentionHistory.ts` exports `openAttentionSession(ref)`, `restoreCurrentAttentionEntry()`, `currentAttentionEntryToken()`, `goBackInAttentionHistory()`, and `attentionEntryOwnsReader(paneId, sessionRef)`. It stores generation-scoped tokens in history state, retains exact `AttentionPlacementEntry` references in memory, tracks known app-history branch order, releases unreferenced parked readers after app pushes, validates an exact live placement, and requests Back only when the immediate tracked predecessor remains valid.
- `cmd/evener-hub/frontend/src/shell/routing.ts` exports `openSessionRoute(ref)` for ordinary session routing and `observeRouteHistory(observer)`. `navigate` accepts `attentionEntry?: { generation: string; id: string }`. Attention pushes can force one identical-URL history step and merge their token into existing state; ordinary identical-URL navigation remains a no-op. App-push observation runs through this existing routing authority.
- `cmd/evener-hub/frontend/src/shell/attentionPlacement.ts` now exports `attentionPlacementIsValid(entry)`. It checks exact record membership, binding revision, live lifetime, requested ref, and live read-view identity without moving focus. `restoreAttentionPlacement` uses the same predicate before its existing focus/seat behavior. No seat, placement, or disposal semantics were redesigned.
- `attentionHistory.test.ts` uses real workspace records and jsdom history/events. It covers A→B→C Back/Back/Forward/Forward, exact secondary X restoration after focus-only navigation, a cascade-leaf retype, foreign-generation state, same-URL distinct readers with exactly one real `pushState`, close plus pane-ID reuse, and Forward-branch pruning after ordinary and attention pushes.
- `routing.test.ts` proves ordinary session routing creates no attention state and repeated identical routes do not add another entry.

Task 2B should call `restoreCurrentAttentionEntry()` during popstate reconciliation before generic route placement, and use `goBackInAttentionHistory()` only when it returns true. `attentionEntryOwnsReader` is an inspection-only predicate and does not focus. Ordinary live session changes use `openSessionRoute(ref)`. The route observer sees pushes made through `navigate`; direct history writes outside that route path are not observed.

## RED/GREEN evidence

- The first new-history run had 3 failures and 1 pass. Review showed the test had not made A an actual browser-history entry, so I corrected the fixture before using later failures as behavior evidence.
- The new `goBackInAttentionHistory` assertion failed before its correction: the test reported `expected false to be true` at `attentionHistory.test.ts:72`. The old lookup compared the predecessor binding with the current URL. It now validates against the predecessor entry's saved URL; the full A→B→C test passes with actual browser events.
- Mutation proof for restoration: I saved `attentionHistory.ts`, changed only `restoreCurrentAttentionEntry()` to return false, and ran the A→B→C test. It failed at `attentionHistory.test.ts:63` (`expected false to be true`). I restored the saved file and verified it byte-for-byte with `cmp`.
- Mutation proof for branch release: I saved `attentionHistory.ts`, disabled the observer's `retainBranchEntries()` call, and ran the ordinary-push-after-Back test. It failed at `attentionHistory.test.ts:218` because the discarded B reader remained in workspace panes. I restored that file byte-for-byte with `cmp`.
- Final focused command, from `cmd/evener-hub/frontend`, sourced `../../../scripts/lib/gate-budgets.sh`, sourced `../../../scripts/lib/load-aware-workers.sh`, and ran:
  `npx vitest run $(vitest_run_args) src/shell/attentionHistory.test.ts src/shell/routing.test.ts src/shell/workspace.test.ts src/shell/paneLifetime.test.ts src/panes/zoom/inspectionOrigin.test.ts`
  Output: **5 files passed, 172 tests passed**.
- Pinned Biome `2.5.5` checked all five touched TypeScript files: `Checked 5 files ... No fixes applied.`
- `npm run typecheck` completed (`tsc --noEmit --incremental false`) with exit 0.
- `git diff --check` completed with exit 0.

## Limits and integration caveats

The checked core uses runtime-only bindings; it does not restore state across reloads. Browser Back/Forward reaches core restoration only when the application integration invokes the exported restoration function; that host/editor route reconciliation is Task 2B. This slice does not prove pending attachment preservation, actual host/editor mounting, mobile/Dockview behavior, or the full Next-needs-you interaction. I did not run browser, server, install, full gates, or any unrelated integration tests.

The existing partial changes in `AppShell.tsx`, `StackHost.tsx`, `Composer.tsx`, `needsYouCycle.ts`, and the two preservation contracts remain unstaged and unmodified by this slice. The worktree still has those Task 2B files dirty. No claim of whole-feature readiness is made.

## Fix round 1: mixed ordinary history and identity proofs

This round starts at `129e47b48f4af41c46030d9889a579aa1e5a91c4`; its findings and commit do not complete Task 2B or the whole feature.

- `attentionHistory.ts` now associates each observed branch entry with its route-entry ID as well as URL and attention token. When an ordinary route entry is the current branch cursor, the next attention departure stamps that entry with its token instead of resetting the known branch. Route-entry identity also makes repeated ordinary URLs unambiguous. `routing.ts` exports `routeHistoryEntryId(state)` and tags every accepted app-generated navigation entry with a fresh `__evenerRouteEntry` UUID. This does not create attention ownership for ordinary routes.
- The mixed ordinary→attention regression follows A → attention B → ordinary route X, with a real ordinary workspace placement for X → attention C, then Back through X, B, A. Its first RED failed at `attentionHistory.test.ts:248` because parked A had been disposed. After the cursor-stamping fix, the focused test passed; the test checks A's exact lifetime/read-view and restoration. B may be disposed by ordinary destructive replacement, so the test does not require B to survive.
- The repeated-URL mixed-chain test showed that URL-only matching can prune an earlier parked attention reader. Its RED failed at `attentionHistory.test.ts:298` with parked C missing after starting a new route from the second `/s/local%3Ax` occurrence. After per-entry route identity was added, the repeated-URL test passed, including C's live lifetime/read-view assertions. The first weaker repeated-URL fixture was unreliable and timed out; it was replaced by the deterministic second-occurrence branch test before accepting evidence.
- Distinct same-URL readers now actually restore on Back and Forward, not merely count pushes. A deliberate mutation removing the attention same-URL exception failed at `attentionHistory.test.ts:234` (`pushState` expected once, received zero). Restored code passes and restores the exact A and B lifetimes/read-view IDs.
- Shared-entry retention is tested with two branch references to the same parked reader. A deliberate mutation retaining only the forward suffix failed at `attentionHistory.test.ts:287` (`restoreCurrentAttentionEntry()` returned false), showing that the surviving shared reference was lost. The restored implementation passes and keeps A's exact lifetime/read-view alive.
- The rebound-current-reader case captures the disposed old lifetime/read-view and the replacement's different IDs, then proves ownership and restoration return false without changing focus. A deliberate mutation making `attentionPlacementIsValid` return true failed at `attentionHistory.test.ts:320` (`attentionEntryOwnsReader` returned true). A less complete mutation was ineffective and was discarded; no production change was based on it.
- Focus-only and cascade-leaf cases assert exact reader and lifetime/read-view identity and exactly two `pushState` calls. Suppressing an existing-token placement update made the focus-only test fail because X was no longer retained; the same mutation made cascade restoration fail at `attentionHistory.test.ts:197`. The unmutated implementation passes both.

Final checks from `cmd/evener-hub/frontend`:

```sh
. ../../../scripts/lib/gate-budgets.sh
gate_source_helper ../../../scripts/lib/load-aware-workers.sh
npx vitest run $(vitest_run_args) src/shell/attentionHistory.test.ts src/shell/routing.test.ts
```

Output: **2 files passed, 62 tests passed**. Pinned Biome `2.5.5` checked all four owned TypeScript files with no fixes. `npm run typecheck` completed `tsc --noEmit --incremental false` with exit 0. `git diff --check` exited 0. The first typecheck caught a strict possibly-undefined branch-cursor access in the new guard; it was narrowed through a local entry variable, then all final checks passed. Biome initially flagged import ordering in the new test; the import order was fixed and the final check is clean.

Task 2B should use `routeHistoryEntryId(state)` only as the core's opaque in-memory identity for app-created route entries; it is not an attention token or a persistence guarantee. The observer still sees only accepted pushes through `navigate`. No server, browser, host, full gate, install, or integration test was run. Task 2B files and contracts remain untouched and unstaged. Whole-feature readiness is not claimed.
