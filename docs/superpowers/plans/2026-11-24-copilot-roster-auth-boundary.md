# Copilot Roster/Auth Transport Boundary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Separate Copilot's connection-roster transport from per-model generation, derive endpoint trust from the actual child provider file, and distinguish steering attachments from historical images.

**Architecture:** Use the existing `listsThroughConfiguredTransport` Copilot predicate for roster/connection behavior and keep `listingTransport` for generation. Compute endpoint exclusions from the trusted provider file each child actually receives. Use `turnHistoryBaseline` to distinguish current-turn prompt/steering attachments from older history.

**Tech Stack:** Go, existing registry/launchconfig/agent request shaping, scripted `httptest` providers and child processes. No new dependency or AppWire type.

**Spec:** `docs/superpowers/specs/2026-11-24-copilot-roster-auth-boundary-design.md`

## Global Constraints

- Copilot uses its provider-head transport for roster/connection auth. Non-Copilot fallback behavior and all per-model generation transports/credentials remain unchanged.
- No new provider identities, auth modes, launch services, public request contracts, or AppWire shapes.
- No blanket project-env filtering and no message-text heuristics. Preserve unrelated project env keys, user/global/trusted/per-launch/parent settings, transcript entries, and persisted attachments.
- Production budget is 200 lines across all three tasks. Stop and report measured size and what each part buys before exceeding it.
- Every behavior change is red-first with real code and scripted providers. Do not claim a fresh live gate without new authorization; the temporary probe credentials are deleted.

## Review Focus

- Alternate trusted provider file with custom `vars_env`: project values cannot redirect launch-check, serve, or a real child registry load (Task 2).
- Default generation override to an API-key gateway: Settings still offers Copilot sign-in, connection status uses Copilot, and generation validation uses the gateway credential (Task 1).
- Two consecutive listings under that override preserve configured models URL, route, and visibility, with no static resurrection on an empty roster (Task 1).
- Steering an image after a turn starts on a text-only model preserves that new attachment and its actionable refusal while older images are omitted (Task 3).
- Continuation fallback and mid-turn skill rebuild use the same `turnHistoryBaseline` ownership, not only the original prompt (Task 3).

---

### Task 1: Roster and connection transport

**Files:**
- Modify: `llm/registry/instances.go`, `llm/registry/resolve.go`, `llm/client.go`, `cmd/evener-hub/app_auth.go`
- Test: `llm/registry/disabled_test.go`, `llm/copilot_models_test.go`, `cmd/evener-hub/app_provider_reload_test.go`, `cmd/evener-hub/app_auth_test.go`, `cmd/evener-hub/app_copilot_roster_cache_test.go`

**Interfaces:**
- Consumes: existing `listsThroughConfiguredTransport(rec *record) bool`, which selects Copilot's provider-head transport, and `listingTransport(rec *record) Transport`, which remains generation-specific.
- Produces: connection-level `Instance.Auth`, credential source/warnings, base URL, `AuthFingerprint`, and `LiveListingAuthoritative` from the roster transport. `Instance.DefaultModel`, `Instance.Protocol`, and generation credentials stay generation-specific.

**Size budget:** 80 production lines.

- [ ] **Step 1: Write failing roster/connection tests.** Use a Copilot instance whose default model generates through a separate API-key gateway. Assert Settings still returns Copilot auth modes/device sign-in, connection credential source and endpoint identity remain Copilot, and generation validation keeps the gateway credential. Add consecutive-listing coverage preserving models URL, routes, and visibility, plus an empty roster with no static resurrection. Run the new cases against the base and capture the expected generation-auth and fallback failures.

- [ ] **Step 2: Run RED.** `go test ./llm/registry ./llm ./cmd/evener-hub -run 'Copilot|LiveListingAuthoritativeFollowsConfiguredListingTransport|Roster|DeviceStart'` and confirm the new connection-auth/authority cases fail for those reasons.

- [ ] **Step 3: Implement roster/connection selection.** Reuse `listsThroughConfiguredTransport` for Copilot in `ResolveInstanceListing`/`listLive`, `LiveListingAuthoritative`, `computeInstances` connection auth/credential/base URL, and `AuthFingerprint`. Do not add another transport abstraction or change `listingTransport`, per-model generation, or non-Copilot fallback behavior.

- [ ] **Step 4: Run GREEN.** Re-run Step 2 plus `go test ./llm/... ./cmd/evener-hub -run 'Copilot|Roster|DeviceStart|Auth_'` and confirm all new and existing Copilot cases pass.

- [ ] **Step 5: Commit.** Stage exactly the listed files and commit `fix(registry): separate Copilot roster and generation auth`.

### Task 2: Effective-child endpoint exclusions

**Files:**
- Modify: `cmd/evener-hub/spawn.go`, `cmd/evener-hub/internal/launchconfig/env.go`
- Test: `cmd/evener-hub/child_provider_config_test.go`, `cmd/evener-hub/internal/launchconfig/env_test.go`

**Interfaces:**
- Consumes: `childConfig.path` as the trusted provider file for spawn and resume, and existing `registry.EndpointEnvNames()` on a registry loaded from that actual file.
- Produces: `projectEndpointEnvExclusions(providersConfigPath string) []string`, called at both spawn and resume instead of reading only `h.Registry`. Unrelated project keys and trusted/global/launch/parent values remain intact.

**Size budget:** 80 production lines.

- [ ] **Step 1: Write failing alternate-file tests.** Give the child a trusted alternate provider file with a custom `vars_env` Copilot endpoint mapping and set that variable only in project launch settings. Assert the real child env, launch-check env, and a registry load keep the trusted endpoint and an unrelated project key. Cover global, trusted-repo, per-launch, and parent overrides.

- [ ] **Step 2: Run RED.** `go test ./cmd/evener-hub ./cmd/evener-hub/internal/launchconfig -run 'Alternate|ProjectEnvExclusions|vars_env|Copilot'` and capture the checkout-selected endpoint surviving before the fix.

- [ ] **Step 3: Implement effective-file exclusions.** Change `projectEndpointEnvExclusions` to load the provider configuration named by `childConfig.path` (using the existing child-config preparation path) and derive exclusions from that registry. Keep empty/no-user-layer behavior and call it at both spawn and resume with the child's path.

- [ ] **Step 4: Run GREEN.** Re-run Step 2, then `go test ./cmd/evener-hub/internal/launchconfig` and focused spawn/launch-check tests. Confirm all allowed sources and unrelated keys still work.

- [ ] **Step 5: Commit.** Stage exactly the listed files and commit `fix(hub): derive endpoint exclusions from child provider config`.

### Task 3: Current-turn steering image ownership

**Files:**
- Modify: `agent/session_model_call.go`, `agent/session.go`, `agent/session_lifecycle.go`
- Test: `agent/session_model_switch_images_test.go`

**Interfaces:**
- Consumes: existing `turnHistoryBaseline`, which identifies the first message of the in-flight turn, and existing `targetRulesOutImageHistory(profile)` behavior.
- Produces: historical-image projection only before that baseline; prompt and steering messages at/after it retain attachments for the provider's actionable refusal. The original request, transcript turns, and persisted files are unchanged.

**Size budget:** 40 production lines.

- [ ] **Step 1: Write failing steering tests.** Start with historical image content on a text-only target, then admit a new steering image after the turn starts. Assert the old image is projected out while the new steering attachment remains for the provider's refusal. Include opening-prompt refusal, supported vision, non-Copilot controls, continuation fallback, and mid-turn skill rebuild where the existing harness supports it.

- [ ] **Step 2: Run RED.** `go test ./agent -run 'Image|Media|Vision'` and capture steering attachments being stripped by the opening-prompt-only check.

- [ ] **Step 3: Implement baseline ownership.** Replace the `turnInputCarriesImages` ownership decision with `turnHistoryBaseline` in normal request shaping and continuation fallback. Do not change public request types or copy/mutate transcript history.

- [ ] **Step 4: Run GREEN.** Re-run Step 2 and `go test ./agent -timeout 25m -count=1`, confirming current and historical cases pass.

- [ ] **Step 5: Commit.** Stage exactly the listed files and commit `fix(agent): preserve new steering images on text-only targets`.

## Final verification

- Run all affected packages: `go test ./llm/... ./cmd/evener`, `go test ./agent -timeout 25m -count=1`, and `go test ./cmd/evener-hub -timeout 25m -count=1`.
- Run `make lint` once. Fix only findings in the changed boundary, and report unrelated failures separately.
- Review the complete boundary diff with an independent reviewer. If it exceeds 200 production lines, stop for Jesse's measured-size decision before merging.
- Do not claim a fresh live Copilot gate without new authorization. Existing deterministic tests must cover every changed path.
