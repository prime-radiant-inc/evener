# First-run onboarding implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make terminal startup, provider setup and first-session launch clear while preserving existing ownership and user work.

**Architecture:** Refine the existing CLI help, provider dialog, model-picker owner and Spawn view. Keep readiness, credentials, catalogue loading, launch resolution and drafts with their current owners. Use a real hub and scripted external provider for end-to-end evidence.

**Tech Stack:** Go, React/TypeScript, existing Evener widgets, Vitest, Chrome browser guards.

**Spec:** `docs/superpowers/specs/2026-10-18-onboarding-design.md`

## Global constraints

- No automatic browser opening, session creation, global-default mutation or new onboarding completion flag.
- Preserve configured-user welcome routing and all existing remote/custom/OAuth boundaries.
- Configuration readiness is not verification. No mandatory successful probe or new launch restriction.
- Checks list models only. No paid generation probe.
- Production UI uses existing widget APIs and tokens. Ask Jesse before adding a widget or widget prop.
- Tests assert behavior, structured data and focus, not prose. Follow `docs/developing-evener/testing.md`.
- Use this existing isolated worktree. Leave unrelated main-checkout files alone.
- Each task uses red/green tests and a focused commit; review staged paths before committing. Never bypass hooks.
- No merge before Jesse reviews the implemented-flow screencast.

## Review focus

1. Piped input and explicit flags must retain routing and exit semantics: Task 1 pins these branches.
2. Saved access followed by failed verification must remain usable: Tasks 2–3 pin dismissal and explicit selection without resaving.
3. Host/instance changes during catalogue loading must reject stale results: Task 3 retains and extends generation-fencing tests.
4. Advanced sandbox overrides must win over the access selector: Task 4 pins effective values, unknown values and resolution failure.
5. Blank Start must create an idle session rather than claim work: Tasks 4–5 verify empty input and visible composer through the real stack.

## Task 1: Terminal-to-browser handoff

**Files:** `cmd/evener/main.go`, `cmd/evener/main_test.go`, `cmd/evener-hub/main.go`, `cmd/evener-hub/main_test.go`, `cmd/evener-hub/main_ephemeral_port_test.go`.

**Interfaces:** Keep `printRunCommands(w io.Writer)` as the shared complete command roster. Add `printStartupUsage(w io.Writer)` for concise bare-interactive guidance. Existing `runMain` dependencies retain dispatch, stdin mode and exit ownership. Hub handoff continues using `hubedge.AuthURLFor` and `advertisedHubHost`.

- [ ] Add `TestMainBareInteractiveStartup`: with no arguments and character-device stdin, assert no agent invocation, no subcommand dispatch side effects, existing exit status 1, output on stderr, and the machine command tokens from all 11 dispatch branches. Extend routing cases for piped prompt, empty pipe, explicit flags, help and resume; assert their existing paths and exit codes rather than copy.
- [ ] Run `go test ./cmd/evener -run 'Test(Main|DispatchCLICommand)' -count=1`; confirm the new concise-routing case fails before implementation. Pin compact versus full usage structurally through CLI flag tokens, not human wording.
- [ ] In the empty-prompt branch, use concise startup help only for zero arguments plus character-device stdin. Reuse the command roster; retain full flag/environment help for explicit help and invalid/scripted invocation. Describe hub as interactive web UI.
- [ ] Refine the hub's existing final startup output into browser address/private sign-in handoff and terminal-lifetime instructions. Preserve a single private auth URL, advertised/ephemeral address correctness, token-file guidance and errors. Extend existing URL tests to assert parsed address/query and exactly one credential-bearing URL, not prose.
- [ ] Run `go test ./cmd/evener -run 'Test(Main|DispatchCLICommand)' -count=1` and `go test ./cmd/evener-hub -run 'TestRunMain|TestMain' -count=1`, inspect complete output, then commit only Task 1 paths.

## Task 2: Provider discovery and saved dismissal

**Files:** `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/ProviderConnection.tsx` and `ProviderConnection.test.tsx`.

**Interfaces:** Preserve `ProviderConnectionProps { onClose(): void; onConnected(name?: string): void; onOpenSettings(): void }`. Use existing `ListRow.meta` for resolved auth capabilities. Keep the existing `saved`, `configured`, operation-generation and keyed SelectedConnection owners.

- [ ] Add behavior cases that choose providers through their authentication metadata, retain resolved auth-mode precedence, and exercise save → failed check → dismiss/reopen and unsupported continuation. Assert actual instance identity, one save request, retained stored access and no cross-provider draft transfer. Preserve endpoint-assertion and OAuth cases.
- [ ] Run targeted `ProviderConnection.test.tsx` and confirm new behavior assertions fail where behavior changes. Pure copy changes receive diff review rather than invented copy tests.
- [ ] Annotate provider discovery using catalogue auth modes, keeping Show all providers/search and Manage existing connections. Rename successful continuation for model choice and use truthful saved-dismissal labels; retain existing retry/no-resave logic and server-authoritative OAuth. Do not introduce persistent check state.
- [ ] Rerun the provider test file and frontend typecheck. Commit only the two provider files.

## Task 3: Model catalogue recovery

**Files:** `cmd/evener-hub/frontend/src/panes/session/chrome/ModelSwitchTrigger.tsx`, `ModelSwitchTrigger.test.tsx`, and its Spawn call site in `cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx`.

**Interfaces:** Keep `loadCatalog: (refresh?: boolean) => Promise<ModelCatalog>`, `onPick: (entry: ModelCatalogEntry) => void`, and `connectionRequest?: { name?: string }`. Add optional `onOpenConnection?: () => void` to this pane-level component, not to any widget. Existing `startLoad(generation, loader, refresh?, inPlace)` remains the only read owner.

- [ ] Add tests for scoped load success with empty data, initial failure, retry to success and connection repair. Assert retry calls the existing loader, never credential save; stale results after host/loader replacement do not publish; Show all models exposes other instances. Assert close/pick focus and callback identities.
- [ ] Run targeted `ModelSwitchTrigger.test.tsx` and observe new recovery tests fail.
- [ ] Compose existing Button controls around ModelCatalogPanel: one retry action and one connection-settings action for relevant failure/empty states. Keep Show all models and instance-name filtering. Wire local Spawn repair to its existing connection dialog; do not offer a local credential editor for remote hosts. Keep desktop and Sheet focus paths.
- [ ] Run model-picker tests plus Spawn handoff tests and typecheck. Commit only Task 3 paths.

## Task 4: First-session hierarchy and effective access

**Files:** `cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx`, `Spawn.test.tsx`, `accessMode.ts`, `accessMode.test.ts`, and existing Spawn stylesheet only if layout requires it.

**Interfaces:** Keep `useProviderSetup(submittedSource)`, `providerConnected`, `modelHandoff` and existing launch resolver/draft/submit owners. Add a pure known-mode consequence helper in `accessMode.ts`, `accessModeDescription(sandbox: string): string | undefined`, returning no invented description for unknown values. Read effective sandbox from resolved configuration, respecting explicit advanced overrides.

- [ ] Add tests asserting missing-provider action precedes composer in DOM order, retained task/directory through setup, no duplicate missing-model action while provider is missing, loading/error distinct from missing, and local/remote callback ownership. Extend saved-failed-dismissal and explicit-model-before-Start cases without requiring verification.
- [ ] Add effective-access cases for all four modes, advanced override winning, inherited/custom values, resolution pending/failure and unchanged launch eligibility. Assert resolved machine values/control targets rather than prose. Preserve blank/whitespace Start tests and keyboard focus after handoff/dismissal.
- [ ] Run `Spawn.test.tsx` and `accessMode.test.ts` to observe the new structural failures.
- [ ] Move prerequisite presentation before the composer; keep the task editable. Avoid a large healthy-success notice. Add a compact effective-access summary near Start with a Change action targeting the existing controlling Advanced setting. Describe blank launch plainly. Do not move ownership, change sandbox defaults or add a launch gate.
- [ ] Rerun Spawn/access/provider/picker tests, typecheck and the existing spawnguard fixture. Commit only Task 4 paths.

## Task 5: Real-stack evidence, guides and screencast

**Files:** Create `cmd/evener-hub/onboarding_browser_test.go` and `cmd/evener-hub/frontend/scripts/onboardingguard/run.mjs`; reuse patterns from `cmd/evener-hub/skill_composer_browser_test.go` and `frontend/scripts/skillguard/run.mjs`. Update `docs/getting-started.md`, `docs/connecting-a-provider.md`, `docs/product/subsystems.md` and affected current web-guide sections. Create `docs/design/onboarding/verification.md` as the evidence index. Record video outside source under a retained session artifact directory and link it from that index.

**Interfaces:** New `TestOnboardingBrowser` uses the existing browserguard build tag and real hub/daemon/provider-adapter boundary. The Node driver receives only its harness-provided address and fixtures, never personal credentials. Read the existing harness before copying its integration contract; share helpers where duplication would otherwise result. The harness must fail on incorrect routing, missing transcript evidence or lost drafts.

- [ ] Add a browser scenario for fresh isolated config, actual connection UI, model-list request, explicit model selection and Start, real producer reply and durable transcript. Include failed model-list/retry and blank-session composer. Assert no generation request during credential checking and no session before Start. Run against pre-change behavior to establish the missing-prerequisite ordering regression where feasible, without reverting other people's work.
- [ ] Build frontend using existing preflight/build targets, then run `go test -tags browserguard ./cmd/evener-hub -run '^TestOnboardingBrowser$' -count=1`. Fix causes until it passes with the real stack. Keep fixture-only spawnguard evidence separately labeled.
- [ ] Update current guides with one in-context setup path and own-project first task. Keep the disposable tutorial as optional and distinguish task instructions from permissions. Update subsystem ownership only where changed; remove friction entries only if fully implemented and verified.
- [ ] Format touched frontend files using its pinned Biome. Run `make test-web` then `make test-web-browser` sequentially after inspecting target scripts and `make help`. Run targeted Go checks. Use CI for full repository gates; do not merge.
- [ ] Run simplify-code, independent whole-branch review and address findings red-test-first. Review diffs and commit exact remaining paths.
- [ ] Record the actual implemented flow using the isolated real hub and scripted provider. Label the provider as scripted, show actual production UI and generated-by-fixture transcript, exclude auth links/tokens and personal credentials. Verify the video opens and shows the full sequence; leave no unnecessary services or scratch files.
- [ ] Deliver video address, code/guide paths, commands and results to Jesse. Wait for screencast review before any merge.

## Verification commands and setup

From the repository root, inspect `Makefile` and relevant preflight scripts, then run `make help` and `make hooks`. Ensure dependencies exist without running npm ci through a symlink. Use the project's preflight owner rather than modifying a shared install.

From `cmd/evener-hub/frontend`, targeted suites use:

```sh
npm test -- src/panes/settings/sections/credentials/ProviderConnection.test.tsx src/panes/session/chrome/ModelSwitchTrigger.test.tsx src/panes/spawn/Spawn.test.tsx src/panes/spawn/accessMode.test.ts
npm run typecheck
npm run spawnguard
```

The npm test wrapper also runs package-test-file checks and script tests; it is not a bare three-file Vitest run. Read the wrapper before reporting results. Heavy gates run sequentially. Capture red/green evidence in the verification index; temporary logs are removed after results are recorded.

## Self-review

The five tasks cover startup, discovery/auth, catalogue handoff/recovery, launch/access/drafts, evergreen docs and the implemented screencast. Existing welcome routing, host scope, credential assertions and OAuth are preservation checks, not replacement implementations. No new widget API, credential store, recommendation engine or global model default is planned. The code-writing and recording steps remain pending.
