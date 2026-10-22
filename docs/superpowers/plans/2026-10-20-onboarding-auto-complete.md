# Automatic Provider Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Check newly acquired provider access automatically, then return to the preserved regular UI without a completion button or forced model picker.

**Architecture:** Keep the existing guided connector, credential store and launch-draft owners. Reconcile checks against the selected instance rather than global listing churn. Carry the registry's usable instance default through existing model discovery and adopt it only in an untouched originating draft.

**Tech Stack:** Go, AppWire, React/TypeScript, Zustand, Vitest, production-browser Go guard with scripted external HTTP services.

**Spec:** `docs/superpowers/specs/2026-10-20-onboarding-auto-complete-design.md`

## Global Constraints

- "Jesse chose provider-access verification only: no generation probe, no user-task submission and no claim that the check proves generation permission, billing status or remaining quota."
- "Do not create or start a session, send the user's task, or change global provider or model defaults. Start remains the explicit launch action."
- "Preserve every explicit model choice, including a choice made while checking. Do not silently replace an explicit choice that is unavailable."
- "Keep credentials scoped to their selected host and provider instance. Do not transfer secrets between providers or discard saved access on check failure."
- "Use the current ProviderConnection, OAuth dialogs, credentials store, useProviderSetup, Spawn draft owner, catalog and launch resolver. Do not add a second onboarding state machine, credential store or persistent completed flag."
- "Use the existing dialog and status components, with no new widget API."
- "Tests assert state, ownership, requests, focus and side effects, not exact copy. Default tests use scripted external boundaries and no personal credentials."
- Scope is desktop/mobile web and its shared guided connector. Keep Settings diagnostic controls and remote-host ownership. Native onboarding redesign is excluded.
- Read `docs/developing-evener/testing.md` before tests and `docs/developing-evener/design-system.md` before UI edits. Use named AppWire package imports and existing widget exports.
- Run focused checks locally; full suites run in CI. Never install through a symlinked `node_modules`, skip hooks or change unrelated files.

## Review Focus

1. An unchanged selected instance survives another provider's refresh, while a changed credential revision or endpoint retires the old evidence. Task 1 pins both cases.
2. A disconnect after a successful credential write must recover without writing again; dismissal or changed intent must retire every retry. Task 3 pins these lifetimes.
3. A static fallback catalog can contain the configured default even when live enumeration is empty or unsupported. Task 2 must leave those rows unmarked.
4. A same-value or explicit-empty model choice, remount, or A-to-B-to-A navigation can defeat string comparisons. Task 4 pins choice revisions and operation generations.
5. Automatic completion can close the wrong dialog or steal focus after the person moves away. Tasks 3, 4 and 5 pin dismissal, origin scope and actual focus.

## Execution and files

Use one fresh `lunaroute/deepseek-4.1-flash` implementer per task, sequentially, without child delegation. A separate reviewer checks specification compliance and code quality after each task. Read the report and review the full task commit range before proceeding. Stop the review loop after two rejected attempts at the same finding and report the evidence, following the repository's stricter rule.

Use the subagent-driven-development skill's plan-scoped ledger and brief/review-package scripts. Copy Global Constraints into every extracted brief. Record the task base before dispatch. The ledger owns progress; do not maintain a duplicate progress narrative in this plan.

| Files | Responsibility | Tasks |
|---|---|---|
| `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/ProviderConnection.tsx` and tests | Selected access identity, check lifecycle and guided completion | 1, 3 |
| `oauthDialogs.tsx`, `reconcileListing.ts` and their tests in that credentials directory | Existing OAuth acquisition and post-write refresh | 1, 3, only if reproduction requires changes |
| `appwire-client/typescript/state/credentials/instances.ts` and tests | Existing listing provenance and stale-connection refusal | 1, only if the proven cause lives here |
| `appwire/types.go`, launch-check producer, hub subprocess decoder | Usable configured-default discovery fact | 2 |
| `Spawn.tsx`, `spawnDrafts.ts` and tests in `frontend/src/panes/spawn` | Draft-local model adoption and origin ownership | 4 |
| Shared connector callers and `ModelSwitchTrigger.tsx` | Remove manual completion and forced picker plumbing | 3, 4 |
| `cmd/evener-hub/onboarding_browser_test.go`, `frontend/scripts/onboardingguard` | Real-stack acceptance and visual/focus evidence | 5 |

### Task 1: Preserve checks across unrelated listing refreshes

**Files:**
- Modify/test: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/ProviderConnection.tsx`, `ProviderConnection.test.tsx`.
- If required by the failing trace, modify/test `oauthDialogs.tsx`, `oauthDialogs.test.tsx`, `reconcileListing.ts`, `reconcileListing.test.ts` in that directory, or `appwire-client/typescript/state/credentials/instances.ts`, `instances.test.ts`.

**Interfaces:**
- Consume `InstanceEntry.endpointFingerprint`, `configRevision`, `activeSource`; existing `fetchSelf(): Promise<boolean>` and `testCredentials(provider: string, expectedEndpointFingerprint?: string): Promise<AuthTestResponse>`.
- Keep public connector props unchanged in this task. Add a local `accessIdentity(row: InstanceEntry | undefined): string` beside `destination` to compare effective selected access, excluding loading/error flags, warnings and other providers.
- Produce selected-target reconciliation for Task 3. Do not weaken `foreignListingChange` globally or remove store stale-listing refusals to make this view pass.

- [ ] **Step 1: Reproduce invalidation before changing production code.** Extend the real component/store test with a pending `evener/auth/test`; issue an unrelated-provider notification through `FakeClient`, let the real store refresh an unchanged target, then resolve the check. Record the observed state transition and the source that caused it. This proves that mechanism, not the exact historical screenshot event.

```ts
// Test: unchanged target survives another provider's refresh during its check
expect(reasonShown("changed")).toBe(false);
await checkConfirmed();
expect(client.calls.filter(c => c.method === "evener/auth/test")).toHaveLength(1);
```

Also cover delayed device and redirect acquisition refreshes, a foreign target `configRevision` change with unchanged `activeSource`, and a query-only endpoint fingerprint change. Old target results must never confirm the new target. Preserve all assertions that no credential is sent after an endpoint refusal.

- [ ] **Step 2: Run the red test.** Prepare this lane's dependencies with `make help`, inspect `scripts/web/web-preflight.sh`, then `make web-preflight`. From `cmd/evener-hub/frontend`, run `./node_modules/.bin/vitest run src/panes/settings/sections/credentials/ProviderConnection.test.tsx --maxWorkers=2 -t 'unchanged target survives'`. Expect a behavioral assertion failure, not missing dependencies or a timeout.
- [ ] **Step 3: Fix the confirmed ownership error.** Compare selected destination/access identity on applied listing transitions. Ignore unrelated row replacement and read-status changes. An acquisition's own confirmed revision becomes the check's anchor; an external revision change retires stale evidence. Retain endpoint assertions and explicit destination review. Change mutation refresh attribution only if its failing test requires it. Do not introduce a second store.
- [ ] **Step 4: Run green and regressions.** Run the complete `ProviderConnection.test.tsx`, `oauthDialogs.test.tsx` and `reconcileListing.test.ts` files with the real Vitest binary, `--maxWorkers=2` and no `-t` filter, plus `../../../appwire-client/typescript/state/credentials/instances.test.ts` if touched. Assert real request counts/arguments and late-result behavior, not copy. Replace the broad any-refresh-invalidates expectation with unchanged-target preservation and actual-target-change refusal cases. Keep manual success continuation until Task 3 changes that contract.
- [ ] **Step 5: Format and commit only this task's files.** Run touched-file Biome from the frontend directory and `npm run typecheck`. Inspect the staged diff; commit as `fix(onboarding): preserve checks across unrelated refreshes`. Report red/green commands, observed invalidation cause and commit range.

### Task 2: Expose the usable configured instance default

**Files:**
- Modify: `appwire/types.go`, `cmd/evener/internal/launchcheck/launchcheck.go`, `cmd/evener-hub/spawn.go`.
- Test: `cmd/evener/internal/launchcheck/launchcheck_test.go`, `launchcheck_disabled_test.go` in that directory; `cmd/evener-hub/spawn_test.go`, `app_models_test.go`, `app_models_refresh_test.go`.
- Generate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`.

**Interfaces:**
- Add `InstanceDefault bool` with JSON tag `json:"instanceDefault,omitempty"` to `appwire.ModelDescriptor` and the private `launchCheckModel`.
- Consume `registry.Instance.DefaultModel` from the listing producer's own `client.Registry().Instances()` and `llm.ModelListing.Usable`.
- Produce `ModelDescriptor.instanceDefault?: boolean` through the existing `model/list` RPC. True means this row is the configured default in this instance's usable current listing, not the global default or proof of generation access. Absence authorizes no automatic selection.
- Keep `ModelListParams`, `ModelListResponse`, spawner interfaces and cache scope unchanged.

- [ ] **Step 1: Add failing producer and propagation tests.** Use configured registry plus scripted HTTP provider. Name the main case `TestLaunchCheckModelsMarksUsableInstanceDefault`: two instances share IDs `first` and `chosen`. Instance `team` defaults to the non-first `chosen` row; the globally selected instance `other` defaults to `first`. Assert each instance's own default independently of global selection.

```go
marked := map[string]bool{}
for _, row := range rows {
    if row.InstanceDefault {
        marked[row.Provider+"/"+row.Model] = true
    }
}
want := map[string]bool{"team/chosen": true, "other/first": true}
if !reflect.DeepEqual(marked, want) {
    t.Fatalf("instance defaults = %v, want %v", marked, want)
}
```

Cover user override versus inherited default, aliases/namespaced IDs, absent/unavailable/disabled/hidden default, and empty/unsupported/failed listing. Unsupported static fallback and the mint-free command-credential branch stay unmarked; no credential command runs. Add `TestListEvenerLaunchModelContractCarriesInstanceDefault` and a real `model/list` routing/cache-refresh assertion to catch the decoder dropping the field.

- [ ] **Step 2: Observe red.** Add the field declarations first to let the tests compile, without populating them. Run `go test ./cmd/evener/internal/launchcheck -run 'TestLaunchCheckModelsMarksUsableInstanceDefault' -count=1` and the named decoder test. Expect missing-marker assertions.
- [ ] **Step 3: Populate and preserve the fact.** In `launchCheckModels() ([]launchCheckModel, []appwire.ModelListDiagnostic, error)`, mark only an emitted row whose model identity matches a nonempty `inst.DefaultModel` and whose listing is usable. Retain all existing row visibility and disabled-model filters. Carry the field through `listEvenerLaunchModelContract(ctx context.Context, evenerBinary string, env []string) (appwire.ModelListResponse, error)`; avoid a second default resolver or controller-registry lookup.
- [ ] **Step 4: Regenerate and verify.** Run `go generate ./appwire/...`, then:

```sh
go test ./cmd/evener/internal/launchcheck -run 'Test(LaunchCheck|RunLaunchCheck|ValidateLaunchCheck)' -count=1
go test ./cmd/evener-hub -run 'Test(HubSpawnerListsModelsFromEvenerLaunchContract|ListEvenerLaunchModelContract|FetchLaunchModels|StoreLaunchModels|EnrichModelListResponse|ModelListExplicitRefresh)' -count=1
go test ./appwire ./internal/appwirets ./internal/appwiredoc
```

From the frontend directory run `npm run typecheck`. Every command must exit zero with no failed tests. Verify the generated field and docs match the Go source; do not hand-edit generated output.
- [ ] **Step 5: Format and commit the named sources, tests and generated files.** Use gofmt on touched Go files and touched-file Biome on generated TS. Commit as `feat(models): expose usable instance defaults for onboarding`.

### Task 3: Complete guided setup automatically and recover reads

**Files:**
- Modify/test: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/ProviderConnection.tsx`, `ProviderConnection.test.tsx`, `ConnectProviderDialog.tsx`, `ConnectProviderDialog.test.tsx`.
- Modify callers: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.tsx`, `cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx`; adjust affected caller tests only for automatic connector completion here.
- OAuth files from Task 1 only if acquisition-to-check recovery requires changes.

**Interfaces:**
- Keep `onConnected(name?: string): void` and `onClose(): void`; remove `completionLabel` from connector props and every caller/test.
- Reuse `SelectedConnection`'s operation token, `useEditorLifetime`, `refreshAndCheck(token: number, expectedSource?: string)` and `check(token: number, target: InstanceEntry)`.
- Add one owned retry timer, not another flow store. Implementation timing: delays 1, 2, 4, 8, then 15 seconds, capped at 15 while open. One check at a time. Manual retry cancels the scheduled one.

- [ ] **Step 1: Add failing automatic completion and recovery cases.** Drive both OAuth variants, API key, credential JSON, existing host access and keyless paths through the real component/store. Replace the manual success-button click with callback evidence; wrap the connector in an owner that actually unmounts it on completion.

```ts
// Test: current successful access check completes exactly once without a click
await waitFor(() => expect(connected).toHaveBeenCalledExactlyOnceWith("openai-codex"));
expect(screen.queryByRole("dialog")).toBeNull();
expect(client.calls.filter(c => c.method === "thread/start")).toHaveLength(0);
```

Use controlled time and deferred wire responses for temporary `endpoint_failure` and listing failure followed by success. Assert one credential write/sign-in, paced nonoverlapping retries, no generation call, and dismissal always available. During a check expose one progress state rather than Sign in plus a competing check action. Cover unmount, provider switch, cancelled OAuth, reconnect, same-origin replacement client and newer dialog while old reads settle. `auth_rejected`, `missing`, `configuration_failure`, endpoint conflict and unavailable fingerprints stop retry; `unsupported` retains explicit unverified continuation.

- [ ] **Step 2: Observe red.** Run the changed `ProviderConnection.test.tsx` and `ConnectProviderDialog.test.tsx` cases. The pre-change success callback is absent and temporary failures do not recover automatically.
- [ ] **Step 3: Implement completion and recovery in the existing owner.** On current success retire the operation before invoking `onConnected` once. Keep endpoint guards from Task 1. Retry only transient checks/reads, never a credential mutation. On transport recovery refresh and re-anchor on the same browser controller and destination before a new check; obsolete results cannot win. All production web clients use `rpcURLFromLocation(window.location)`; do not invent a new host-selection API. Keep saved access status for OAuth as well as stored keys. Clear retry ownership on leaving, unmount, changed target or terminal outcome. Never abort a shared store request for another consumer.
- [ ] **Step 4: Verify connector and callers.** Run complete `ProviderConnection.test.tsx`, `ConnectProviderDialog.test.tsx`, `oauthDialogs.test.tsx` and `Spawn.test.tsx` with `--maxWorkers=2`; retain existing Settings diagnostic tests. Task 4 will replace Spawn's remaining picker handoff, so this task's Spawn expectations should only remove the extra completion click. Run typecheck and touched-file Biome.
- [ ] **Step 5: Commit this task's files** as `feat(onboarding): finish successful checks and retry temporary failures`. Report timer cleanup, callback count and no-repeat-write evidence.

### Task 4: Return to the originating draft with a safe model choice

**Files:**
- Modify/test: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx`, `Spawn.test.tsx`, `spawnDrafts.ts`.
- Create: `cmd/evener-hub/frontend/src/panes/spawn/spawnDrafts.test.ts` for explicit-choice provenance and remount ownership.
- Modify/test: `cmd/evener-hub/frontend/src/panes/session/chrome/ModelSwitchTrigger.tsx`, `ModelSwitchTrigger.test.tsx`.
- Test propagation if needed: `cmd/evener-hub/frontend/src/widgets/modelCatalog/catalogClient.test.ts`.

**Interfaces:**
- Consume Task 3's existing `onConnected(name?: string)` and Task 2's `ModelDescriptor.instanceDefault` via `loadModelList(refresh = false): Promise<ModelListResponse>`.
- Add draft-owned `modelChoiceRevision: number`, initialized to 1 for a nonempty saved model and 0 otherwise. Add `markModelChoice(draft: SpawnDraft): void`, incrementing for each explicit choice, even a same-value or empty choice. Automatic discovery/fallback must not call it.
- Replace mount-local `providerChoiceScopes` with draft field `providerConnectionScope: { source: string; harness: string; cwd: string } | null`, initially null. It carries the existing suppression of first-row fallback for the draft's connection flow, scoped to the same source/harness/cwd. It survives remount but is never persisted as an onboarding-completed flag.
- Remove `modelHandoff` and its `connectionRequest` plumbing once unused. Preserve normal user-opened pickers and their in-place refresh/retry behavior.

- [ ] **Step 1: Add failing integration cases in Spawn.** Replace manual handoff tests with successful automatic return. Feed the real model loader two rows, mark the connected instance's non-first row and keep another provider as global default. Assert draft-local adoption and no picker.

```ts
await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
expect(screen.queryByRole("listbox")).toBeNull();
expect(screen.getByTestId("spawn-model-value").textContent).toBe("team-local/chosen");
expect(client.calls.filter(c => c.method === "thread/start")).toEqual([]);
expect(client.calls.filter(c => c.method === "evener/instance/setDefault")).toEqual([]);
```

Assert the selected model value, not a label phrase. Preserve prompt, attachments, cwd, source, harness and access settings. Cover explicit chip/Advanced/slash/saved/unavailable/empty choices, including a choice made while the catalog is pending. Cover remount and A-to-B-to-A draft/host/harness navigation; the previous completion must not adopt or steal focus. Cover absent instance name/default/marker, failed/empty/unsupported catalog and a late answer from a retired credential generation. Preserve useful displayed catalog rows but do not use them as fresh adoption evidence.

- [ ] **Step 2: Observe red** with `./node_modules/.bin/vitest run src/panes/spawn/Spawn.test.tsx --maxWorkers=2 -t 'automatic connection completion'` from the frontend directory. Name the new tests with that prefix. Expect missing adoption/forced-picker assertions.
- [ ] **Step 3: Implement the draft-owned handoff.** Capture original draft, source/harness/cwd, model-choice revision and monotonically increasing completion generation when opening setup. On current success close setup first, refresh setup/catalog through existing owners, then adopt exactly one marked row for `name` only if that originating draft remains untouched and current. Unknown or ambiguous defaults leave inline model recovery. Do not call `handleModelChange` or `saveDefaults` for automatic adoption: those are explicit-choice/persistence paths. Fence the existing stale-model sweep so this completion cannot erase a preserved explicit choice or rewrite saved defaults. Ordinary configured-user behavior stays unchanged.
- [ ] **Step 4: Verify the full affected frontend surface.** Run `Spawn.test.tsx`, `spawnDrafts.test.ts`, `spawnDefaults.test.ts`, `ModelSwitchTrigger.test.tsx` and `catalogClient.test.ts` with the real Vitest binary and `--maxWorkers=2`. Test actual Start after adoption as well as zero launches before it. Verify inline recovery can reach a manual model choice and then Start. Run typecheck and touched-file Biome.
- [ ] **Step 5: Commit named files** as `feat(onboarding): restore the draft without a forced model picker`. Report preserved choices, stale-response fences and configured-user regression evidence.

### Task 5: Prove the production journey and update the guides

**Files:**
- Modify: `cmd/evener-hub/onboarding_browser_test.go`, `cmd/evener-hub/frontend/scripts/onboardingguard/run.mjs`, `README.md` in that guard directory.
- Create if needed: `cmd/evener-hub/onboarding_oauth_browser_test.go` with `//go:build browserguard`, sharing the existing guard setup rather than copying it.
- Modify: `cmd/evener-dev/webbrowser.go`, `webbrowser_test.go` only if the added test name needs inclusion in the existing browser CI lane.
- Update: `docs/connecting-a-provider.md`, `docs/getting-started.md`, `docs/llm-provider-config-and-launch.md`, `docs/product/subsystems.md`.

**Interfaces:**
- Preserve `TestOnboardingBrowser`, the existing driver arguments and the Go checkpoint's sessions/generations/launches checks. Extend the production-browser harness with device and redirect subtests, invoked by the same tagged lane.
- Use `hubAuthControllerSetup` only to direct `cfg.IssuerBaseURL` and HTTP client traffic to a scripted OAuth issuer. Keep real OAuth functions, persistence, registry reload, AppWire, browser UI and daemon execution. Script the real device-disabled response to exercise redirect fallback. Do not stub auth completion, model RPCs or frontend stores.

- [ ] **Step 1: Extend behavioral guard assertions.** Remove manual Retry/Continue/Choose-model actions after successful acquisition. Require automatic retry of the fixture's first model-list failure, automatic return with draft/attachment preserved, and adoption of the fixture's configured non-first default. Seed provider configuration only; never seed credentials, browser model choices or session state. Assert zero generations, sessions and process-launch attempts before explicit Start; after Start verify the actual persisted task/attachment and visible reply.
- [ ] **Step 2: Demonstrate sensitivity.** With the new guard, temporarily reintroduce one task-owned defect at a time: suppress automatic completion, restore forced picker, or disable transient retry. Rebuild the frontend for each mutation and after restoration; stale-dist refusal is not behavioral evidence. Each run must fail on the corresponding behavioral checkpoint. Save an exact copy of each touched file in owned scratch first and restore only that file afterward. Inspect the restored diff before continuing; never restore the entire tree.
- [ ] **Step 3: Add both OAuth paths and dismissal/recovery.** Exercise real issuer HTTP/device polling/token exchange and saved OAuth records. Verify automatic access checks after acquisition, unchanged destination under background refresh, endpoint-change refusal, and cancellation with no late completion. At least one key path must Start a real session and show its reply; OAuth paths must prove real persistence and model-list access without any pre-Start generation. All fixture secrets and profiles stay isolated and disposable.
- [ ] **Step 4: Build and run the focused production guard.** Read `scripts/web/web-preflight.sh` and `cmd/evener-hub/frontend/scripts/clean-dist.mjs` before the build. From repository root:

```sh
make help
make build-web
go test -tags browserguard ./cmd/evener-hub -run '^TestOnboarding(Browser|OAuthBrowser|Evidence)' -count=1 -v
```

Run device/redirect fixtures through real production DOM at desktop 1440×1000 and mobile 390×844, light and dark. Extend the guard's existing viewport/theme capture mechanism so checks are not recording-only. Assert dismissal, no forced overlay, focus return while still owned, and no focus theft after intent changes. Inspect representative screenshots. Optional recordings must be labeled as scripted-provider evidence and contain no personal links or credentials.
- [ ] **Step 5: Update evergreen documentation.** Describe automatic checks/completion, non-generating meaning, saved-access recovery, unresolved model handling and draft-local configured-default selection. Update S01/S15/S16 responsibilities and source-of-truth links. Preserve remote onboarding deferrals and unresolved friction cases. Update the guard README with actual coverage and commands; report anything not exercised.
- [ ] **Step 6: Commit test and documentation files** as `test(onboarding): prove automatic completion through the real stack`. Run focused tests for any changed driver helpers and browser-lane selection. Supply exact commands, red/mutation failures, green output and retained screenshot paths in the task report.

## Branch completion

After all five task reviews pass, run PAR with two independent reviewers on identical immutable base/head, approved spec, plan and behavior evidence. Resolve legitimate findings with failing-then-passing tests. Run simplify-code, review its changes and rerun affected checks. Use shepherd-pr for one push/CI/review wait per round; inspect review bodies and own per-commit findings. Merge only the checked reviewed head under repository policy, verify GitHub's merged SHA/content, then remove this plan's owned workspace, disposable scratch and merged worktree. Keep evidence explicitly linked in the report. Do not deploy.
