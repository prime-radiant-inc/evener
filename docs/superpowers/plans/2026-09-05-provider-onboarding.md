# In-place provider onboarding implementation plan

> **For agentic workers:** Use superpowers:subagent-driven-development or inline execution for tightly coupled integration. Execute through PR creation as authorized by Jesse.

**Goal:** Make provider and first-project setup accessible from the composer whenever credentials are missing.

**Architecture:** A shared availability hook reads the existing credentials store. A focused provider connection component reuses existing credential editors; Spawn owns its visibility and invalidates model caches after provider updates. Welcome redirects only when setup is confirmed necessary.

**Tech Stack:** React, TypeScript, Zustand, AppWire, Vitest, existing Evener widgets.

**Spec:** docs/superpowers/specs/2026-09-05-provider-onboarding-design.md

## Global constraints

- No first-run flag or automatically opened credential dialog.
- Preserve prompt and directory. Status-request failures are unknown, not missing.
- Keep tests deterministic at the external/AppWire boundary; no real credentials in artifacts.
- Reuse existing auth and path flows and current widget styles.

### Task 1: Provider connection dialog

**Files:** Create `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/ConnectProviderDialog.tsx` and colocated tests/CSS only as needed. Reuse `instanceDialogs.tsx`, `oauthDialogs.tsx`, `credentialLabels.ts`, and `stores/credentials.ts`.

**Interface:** `ConnectProviderDialog({ onClose, onConnected }: { onClose(): void; onConnected(): void })`. Mounted only while open. `onConnected` follows an explicit successful auth test. Registry instances/authModes determine allowed actions. Use `ApiKeyDialog`, `DeviceCodeDialog`, `OAuthRedirectDialog`, and `AddInstanceDialog`; render one overlay at a time. Support cancel/retry, registry errors, writesRefused, and keyless instances. Reuse existing OAuth orchestration if extractable without broad changes; do not duplicate substantial auth logic.

- [ ] Add tests rendering the component with real stores/widgets and FakeClient at the RPC boundary. Exercise API-key save then auth test, device authorization or redirect fallback, test failure/retry, cancellation and secret clearing. Assert selected provider arguments at the RPC boundary and completion only on success.
- [ ] Run `npx vitest run src/panes/settings/sections/credentials/ConnectProviderDialog.test.tsx --maxWorkers=2` and observe the missing component failure.
- [ ] Implement the interface above. Match existing auth mode checks and safe test messages. No credential testing on mount.
- [ ] Run the new tests and existing credential tests. Format touched files using Biome; commit the implementation with intent and verification.

### Task 2: Availability and composer integration

**Files:** `stores/credentials.ts`, new `panes/spawn/useProviderSetup.ts`, `panes/spawn/Spawn.tsx`, `panes/spawn/spawn.module.css`, `panes/welcome/Welcome.tsx`, and behavioral tests.

- [ ] Add tests for no credentials, keyless provider, configured provider, failed status request, auth removal while mounted, and reconnection. Readiness derives from a successfully loaded instance list; `hidden` entries do not satisfy setup. `activeSource !== "none"` or `!credentialRequired` satisfies credential configuration.
- [ ] Add Spawn tests preserving prompt/directory while opening/canceling/completing setup, and reloading `model/list` after auth changes instead of reusing the previously cached empty list.
- [ ] Run tests red, implement the smallest hook and inline action, rerun green. Loading and errors do not disable a previously configured session or produce false onboarding. Failed checks offer retry.
- [ ] Route an otherwise empty Welcome surface to `/new` when no configured provider is confirmed. Do not redirect a session or force-open a modal.
- [ ] Use existing `PathField`, `preflightDir`, and create-directory confirmation for project setup. Add an inline directory hint if needed; no separate folder manager.
- [ ] Format and commit.

### Task 3: Verification and PR

- [ ] Run `make test-web`, `make test-web-browser`, `make merge-approval-gate`, and `make vet`; fix root causes of failures.
- [ ] Build a fresh runtime and start a disposable hub with isolated state and process environment, preserving the machine build cache. Exercise the real UI using a scripted provider at the external HTTP boundary; verify saved credential, model refresh, project creation, session start, and returning to missing-credential setup.
- [ ] Exercise an available real provider if credentials are safely available; explicitly document any unavailable external-provider coverage.
- [ ] Review the entire branch for correctness, spec coverage, and maintainability. Resolve findings, rerun affected checks, push `codex/provider-onboarding`, create a PR with source and test evidence, and verify its exact head.
