# Provider Connection A Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make connecting a first model provider a compact provider choice, required-access form, real save/check, and first-session handoff.

**Architecture:** Keep the registry's launch-ready instance list unchanged. Add safe setup metadata to its separate provider catalogue, build a guided connector alongside the existing full editors, and reuse the existing credential, OAuth, model-catalogue and session APIs. Saving and checking remain separate operations with explicit recovery states.

**Tech Stack:** Go/appwire, React/TypeScript, Zustand, CSS modules, Vitest/Testing Library, Go httptest, existing browser guards.

**Spec:** `docs/design/provider-connection-study/README.md`, `research.md`, `instructions.md`, and Option A at `concept.html?concept=a` in that directory. Jesse selected A after the panel recommended C, then explicitly authorized implementation.

## Global Constraints

- Implement Option A only. Zero configured providers is the primary journey. Preserve the other design alternatives as historical artifacts.
- Do not broaden `Registry.Instances()` or weaken its credential/launch-ready filter.
- Preserve every provider and auth variant, including API keys, Codex OAuth/device/redirect, Google ADC and credential JSON, Azure, Bedrock, local and custom endpoints. OpenAI API and ChatGPT/Codex remain separate instance identities.
- Standard public API connections need only the actual API key. Endpoint, name, protocol, surface, environment-variable name and header references are advanced configuration; required cloud variables and custom endpoint details must remain reachable and honest.
- Keep the full existing add/editor capabilities: custom names, protocol/surface, endpoint/template variables, reset overrides, rename, source inspection, replace/clear stored credentials, logout, and explicit default management.
- Use real save, reload, active-source and destination behavior. A model-list check proves list access only. Unsupported checks allow explicit unverified continuation. No durable verification badges, fake model selections, new atomic-connect RPC, automatic default writes, or prototype simulation code.
- Secrets stay in volatile form state, never browser storage, logs, diagnostic text, catalogue metadata, or shared state. No inherited credential may silently be sent to a changed destination.
- Preserve drafts during repair, invalidate asynchronous results on dismissal/reconnect/configuration changes, expose errors inline with usable focus, and distinguish Cancel from Change provider.
- Read `docs/developing-evener/testing.md` before changing tests. Use TDD, real production code, and scripted external provider/network boundaries. Default tests never use real provider credentials or issue external provider calls.
- Keep the remote design preview on port 43127 running. Work only in the isolated `wip/provider-connection-a` worktree based on `404437f5199f04477a97410a51e4c4577cae6d9d`. No merge or push is requested.

## Task 1: Safe catalogue setup metadata

**Files:**
- Modify: `appwire/types.go`, `cmd/evener-hub/app_instances.go`.
- Test: `cmd/evener-hub/app_instances_test.go`, new `cmd/evener-hub/app_provider_onboarding_test.go`.
- Generate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`, `docs/appwire-protocol.md` only if the generator changes it.

**Interfaces:**
- Consumes: `Registry.ProviderIDs`, `Provider`, `ResolveInstance`, `Instance`; `hubInstancesController.entryFor`; `authModesFor`.
- Produces additive catalogue fields:
```go
AuthModes []string       `json:"authModes,omitempty"`
Setup     *InstanceEntry `json:"setup,omitempty"`
```
`Setup` is the current safe, resolved view for an addressable implicit provider or existing instance, including source and sanitized destination. Absence means the provider requires configuration before it has an instance. It is discovery metadata, not membership in `instances`. Use the actual authored layer when a curated ID is overridden; never invent a clean vendor destination over a resolved override. `AuthModes` is derived from the descriptor's effective auth scheme for every catalogue row.

- [ ] Write failing catalogue tests using `newInstancesFixture(t, map[string]string{})`. Find Anthropic in `AvailableProviders`; assert `Setup != nil`, `Setup.ActiveSource == "none"`, supported key mode, real destination, and no Anthropic in `Instances`. Repeat identity/mode assertions for Codex, optional-key local, ADC, and nonimplicit/custom variants. Serialize a fixture containing credential sentinels and endpoint userinfo/query/fragment and assert none crosses the wire.
```go
f := newInstancesFixture(t, map[string]string{})
list := f.ctl.List()
for _, instance := range list.Instances {
    if instance.Name == "anthropic" { t.Fatal("uncredentialed provider is launch-ready") }
}
for _, provider := range list.AvailableProviders {
    if provider.ID == "anthropic" && provider.Setup == nil { t.Fatal("missing discovery setup") }
}
```
The assertions are structured metadata/membership checks, not prompt copy tests.
- [ ] Run `go test ./cmd/evener-hub -run 'TestProviderSetup|TestZeroProviderOnboarding' -count=1` and retain the expected red result.
- [ ] Populate setup through the existing resolver and `entryFor`, converting only public fields into an `Instance`. Do not expose `Resolved.Credential`, resolved headers, or raw URL. Do not change registry filtering or add a separate credential resolver.
- [ ] Add `TestZeroProviderOnboarding_SaveCheckListAtHTTPBoundary` using the real isolated credential store, real registry reload and `httptest.Server` as the provider. Follow `TestAuthTestCredentialsUsesConfiguredBaseURLAndHeadersAtFakeHTTPBoundary`. Prove discovery before key, actual key save, changed membership, actual model-list check request and secret-free responses. Prove a changed endpoint does not reuse a curated credential, and classify provider rejection without echoing its response.
- [ ] Run `go test ./cmd/evener-hub -run 'TestProviderSetup|TestZeroProviderOnboarding|TestAuthTestCredentials|TestInstances' -count=1` and `go test ./llm/registry -run 'TestInstances_(ImplicitFromEnv|PseudoProviderNeedsBaseURL)$' -count=1`.
- [ ] Run `go generate ./appwire`, inspect generated changes and `git diff --check`, then commit named files with intent `feat(hub): expose safe provider setup metadata`.

## Task 2: Compact guided connection and recovery

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/ProviderConnection.tsx`, `ProviderConnection.module.css`, `ProviderConnection.test.tsx`, and `providerSetup.ts`/`providerSetup.test.ts` if pure presentation helpers are needed.
- Modify: sibling `ConnectProviderDialog.tsx`, `ConnectProviderDialog.test.tsx`, `instanceDialogs.tsx`, `instanceDialogs.test.tsx` only where needed for selected-provider full-editor handoff.
- Existing full instance sheet, OAuth dialogs, credential-label safety helpers and store APIs are dependencies to reuse, not implementations to replace.

**Interfaces:**
```ts
export interface ProviderConnectionProps {
  onClose(): void;
  onConnected(name?: string): void;
  onManage(): void;
}
```
`ConnectProviderDialog` opens `ProviderConnection` by default and retains its existing management view as an explicit advanced/host-access route. Its callback becomes `onConnected(name?: string): void`, with successful selected setup passing the real instance name. Management must be reachable and returnable. `AddInstanceDialog` may accept `initialBase?: string` and call `onSuccess(name: string): void` so configured variants can continue directly to their credential step; existing no-argument callbacks remain valid TypeScript assignments.

- [ ] Write failing real-component tests from an empty launch-ready list plus the complete discovery metadata. Selecting Anthropic shows exactly one required password field, a key-acquisition link and API billing explanation; advanced fields remain collapsed. Popular choices are the editorial shortlist Anthropic/OpenAI/Gemini/OpenRouter; All providers searches every returned descriptor without filtering capabilities. Codex remains a separate authentication choice.
```tsx
render(<ProviderConnection onClose={close} onConnected={connected} onManage={manage} />);
await user.click(screen.getByRole("button", { name: "Anthropic" }));
expect(screen.getByLabelText("API key")).toHaveAttribute("type", "password");
expect(screen.getByLabelText("API key")).toBeRequired();
```
Use actual component/store logic with the repository's scripted appwire network client for isolated UI tests. These are unit/integration component tests, not end-to-end provider evidence; Task 1 supplies the real backend/provider-boundary proof.
- [ ] Run `npx vitest run src/panes/settings/sections/credentials/ProviderConnection.test.tsx` from the frontend and retain the red result.
- [ ] Implement compact picker and selected-provider form using existing Dialog/Input/Button/FormRow widgets and design tokens. Keep information density, mobile wrapping, labels and focus behavior aligned with A. Provider help links live in one presentation metadata table, separate from backend auth capability truth. For public implicit key providers, save directly under their real ID without creating an unnecessary named instance.
- [ ] Implement save → refresh → inspect source/destination → model-list check → visible result. Display saved-vs-active source honestly. If the source or destination changes unexpectedly, require explicit review before any provider check. Saving settings first and failing credential save must retain the actual created connection and offer credential repair; retry must not create a duplicate. Check failures keep the form/draft; retry checks the saved connection. No synthetic successes. Explicit unsupported continuation returns the actual instance without claiming verification.
- [ ] Keep auth variants accessible using catalogue `AuthModes`/`Setup` and the existing full configuration, credential JSON, device and redirect flows. New cloud/custom setup may use the preselected full form for configuration, then continue directly to required credentials/check. Explain required template variables and local endpoint; do not require an API key for optional/no-auth endpoints. Full advanced controls are retained, not copied into a reduced substitute. Mask credential JSON by default in new guided UI. Existing-host access stays a secondary disclosure; no new key means retain whatever the host resolves, not disable authentication.
- [ ] Add red-green tests for empty key, save failure, settings-saved/key-failed repair, auth rejection, endpoint/configuration failure, unsupported continuation, source shadowing, changed destination confirmation, complete catalogue search, optional local key, OAuth/device/redirect routing, ADC JSON routing, full-editor access, cancel/change-provider/draft isolation, loading/refusal/reconnect and stale async callbacks. Use deferred promises for operation lifetime tests.
- [ ] Format named touched `src` files with `npx biome check --write`, run focused credential component tests and `make test-web` at root, inspect and commit named files with intent `feat(web): add compact provider connection flow`.

## Task 3: Entry points, first-session handoff and user instructions

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsSection.tsx` and its tests; `cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx` and its tests; `cmd/evener-hub/frontend/src/panes/session/chrome/ModelSwitchTrigger.tsx` and its tests when adding the connect-another action.
- Test/browser: use the existing real browser harness under `cmd/evener-hub/frontend/scripts`; add a focused provider onboarding browser case to its established suite (inspect harness first, no mock Evener E2E).
- Docs: `docs/llm-provider-config-and-launch.md`, new `docs/connecting-a-provider.md`, `docs/design/provider-connection-study/README.md`.

**Interfaces:**
- Consumes: `ConnectProviderDialog` callback `onConnected(name?: string): void`, real model catalogue refresh/selection APIs, existing spawn draft state and model-switch action.
- Produces: all web add/connect entry points lead to the same compact connector; choosing a connected provider leads to the real model chooser and then the existing first-session composer. The optional name selects/filters the relevant provider using actual catalogue data, never a hardcoded model or global default mutation.

- [ ] Write failing entry-point tests: Settings Connect provider opens discovery; fresh Spawn opens the connector; connection completion preserves workdir/prompt and exposes actual models from that connection; selecting one returns to the existing Start path. Configured users retain management and all existing model switching behavior. Session model chooser offers Connect another provider and refreshes after it.
- [ ] Run the named component tests with `npx vitest run` and retain the red result.
- [ ] Wire those entry points without duplicating credential flows. Preserve source/model selection on cancel, and avoid changing session/global defaults without explicit user action.
- [ ] Add a short user how-to: New session → Connect provider → choose one provider → paste actual key or use the appropriate auth route → save/check → choose model → Start. Include key-acquisition links, subscription/API billing distinction, cloud/local/custom and existing-host/advanced routes, model-list-only checks, unsupported continuation and repair. Put a link to it at the beginning of the technical provider-config reference and correct the false claim that every uncredentialed preset appears in the launch-ready instance list. Record Jesse's A selection without rewriting historical panel rankings.
- [ ] Format touched frontend files, run `make test-web` and `make test-web-browser`. Exercise actual built production UI at desktop and narrow mobile widths against an isolated hub/fixture-owned state with no real secrets. Reuse a scripted external provider for backend calls; no fake RPC end-to-end success claims. Check no horizontal overflow, focused errors, collapsed advanced validation, masks, return/cancel and model handoff. Preserve browser evidence outside source if screenshots are only verification artifacts.
- [ ] Commit named implementation/docs/tests with intent `feat(web): route provider onboarding into first sessions`.

## Final acceptance

- [ ] Independent task reviews cover spec compliance and code quality; final branch review checks the complete flow and retained capabilities.
- [ ] Run `make lint`, `make vet`, `make test`, and `make test-web-browser`. Report any skipped/live-only boundaries explicitly; do not claim a blocked or unexecuted gate passed.
- [ ] Verify source diff against base, repository cleanliness, actual artifacts and remote design-preview HTTP availability. Leave the implementation branch ready for Jesse; no merge or push.
