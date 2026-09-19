# Reconnect readiness fix report

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/sdk-d14-navigation-store`

Branch: `claude/sdk-d28-d2-flap-banner`

The assigned worktree was clean at successor `7d5d68b09aa9e52d3fcee389d4fcc533a05e411a` before the switch. No merge, rebase, cherry-pick, or revert operation was in progress. The worktree was switched in place to the predecessor branch.

## Heads

- BASE before the fix: `b689acb3b36bcf52d4463b46d7f47c9900480c22`
- Fix commit: `457c441e54549027f3e8cef5f825bc2d4817e07e`
- `origin/main` fetched at: `e7630003ea3d93b392384a90e6347ff4d2da73c5`
- Final local HEAD after the required `git merge --no-ff --no-edit origin/main`: `cf08ab1176ea7f0681e96426f667865352df4c76`
- No push, PR merge, or review comment was made. The local main merge is the required refresh merge; it is not a PR merge.

## Change

`useLiveReadiness(scope, client, state)` is the single render-owned seam for deferred actions. It updates a ref during render, while each returned predicate retains the hub scope and client identity from the render that created it. A callback therefore runs only when the current render still names that hub and client and the current state is `ready`.

`whenReady`, provider `act`/`confirm`, and `runGatedMutation` now evaluate that predicate at invocation time. Plugins, marketplaces, providers, hub settings, and hub upgrades all use the same predicate. Local cancel, form editing, and navigation controls remain usable while offline.

The real `HubUpgradeSection`/`createHubUpgradeController` test opens an upgrade alert while client A is ready, transitions through reconnecting to replacement client B, confirms the old alert, and observes no request and no checkpoint. A second case confirms the same alert while client A remains ready and observes the upgrade and readback requests plus the installed checkpoint. Direct hook and gate tests cover hub/client identity and live predicate evaluation.

The fix commit's non-test diff is 97 added / 44 removed lines. Test diff is 181 added / 6 removed lines across three test files. Changed files in the fix commit:

- `mobile-native/src/connectionDisplay.ts`
- `mobile-native/src/connectionDisplay.test.ts`
- `mobile-native/src/pluginMutationGate.ts`
- `mobile-native/src/pluginMutationGate.test.ts`
- `mobile-native/src/HubUpgradeSection.test.tsx`
- `mobile-native/src/HubSettingsScreen.tsx`
- `mobile-native/src/MarketplaceBrowser.tsx`
- `mobile-native/src/PluginsScreen.tsx`
- `mobile-native/src/ProvidersScreen.tsx`

## Falsification and gates

From the committed pre-fix production, after reverse-applying only the fix's production patch with `git apply -R`:

```text
npx vitest run src/pluginMutationGate.test.ts -t 'not ready refuses'
FAIL: expected "refused", received "ran"
```

The reverse patch was restored with `git diff | git apply -R`; the worktree was clean afterward.

Final targeted tests:

```text
npx vitest run src/connectionDisplay.test.ts src/pluginMutationGate.test.ts src/HubUpgradeSection.test.tsx src/ProvidersScreen.test.tsx src/hubUpgrade.test.ts
5 test files passed; 42 tests passed
```

```text
cd mobile-native && npm run check
PASS (tsc --noEmit --project tsconfig.check.json)
```

```text
make lint-package-imports
PASS lint-package-imports
```

`git diff --check` passed. The full native suite and full repository gates were intentionally not run, per the lane brief's targeted-gates requirement.

## Raw findings and successor work

- #1915 Medium store reconnect recovery and initial ready-transition reads remain the successor lane's responsibility; #1922 owns that content.
- #1915 Medium deferred confirmation readiness is fixed here. Its Low missing Plugins remove readiness guard is also fixed by checking the same live predicate before opening the alert and again through `act`.
- #1922 Medium fatal-close retry can clear the fatal wall before a replacement is ready, remounting children against the closed/connecting client. This remains held successor work.
- #1922 Medium `runGatedMutation` still maps a not-ready refusal to the existing busy copy. That copy distinction remains held successor work.
- #1922 Low Plugins remove alert reachability is covered here by the live opening guard and invocation-time gate; the successor's current-head panel should still verify its remaining findings after this predecessor moves.
- #1922 still owes a current-head panel including fatal-close remount and refused/busy behavior, plus checking the simplify tautological-test fix. No successor files were changed.

## Parked deletion disposition

The remote WIP ref `origin/claude/sdk-d28-d2a-wip-test-deletion` remains intact. Commit `613dac7f1edb120dae41f3c4615e5460670b5da1` deletes only the successor test named `useConnectionDisplay: a hub change resets everReady`. Its fixed `connecting` state never made the first hub ready, so the test could not observe the reset and was tautological. I did not apply or remove that WIP deletion.

On the successor branch, same-hub transitions are independently covered by the ready-to-reconnecting and reconnecting-to-ready tests, and the following meaningful changed-hub test proves that the next hub does not inherit the previous hub's `everReady` state. The parked deletion is therefore redundant evidence, while the real same-hub and changed-hub coverage is retained.

## Residuals

- No full suite, browser gate, or CI result was produced locally; those remain coordinator/CI work.
- The final branch contains the required local main integration merge and is 17 commits ahead of its remote predecessor branch. It was not pushed.
