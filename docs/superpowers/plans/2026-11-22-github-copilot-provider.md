# GitHub Copilot Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add GitHub Copilot subscriptions as a first-class provider with device login, per-instance configuration, account model discovery, CLI/client setup, and first-run popular-provider placement.

**Architecture:** A provider-owned GitHub OAuth service stores scheme-bound records and refreshes rotating tokens under inter-process locks. Evener's registry and existing Chat Completions, Responses, and Anthropic adapters own model resolution and requests; the hub dispatches by auth scheme and preserves its existing AppWire contracts.

**Tech Stack:** Go 1.27, React/TypeScript, React Native, AppWire, models.dev, scripted `httptest` providers. No new third-party dependency.

**Spec:** `docs/superpowers/specs/2026-11-21-github-copilot-provider-design.md`

## Global Constraints

- Implement the selected direct provider with the explicitly approved temporary Pi-compatible OAuth identity. Do not substitute Copilot SDK/CLI, OpenCode's formally partnered identity, or another application's stored credential files.
- Jesse explicitly approved the temporary Pi-compatible path after controlled probes and partnership research. Use public Copilot client ID `Iv1.b507a08c87ecfe98`, device scope `read:user`, and Pi's `/copilot_internal/v2/token` session exchange. Never request a client secret or claim GitHub approval for this compatibility path.
- Store the GitHub access token plus the short-lived Copilot session token and session expiry. Renew the session token through the exchange five minutes early. The current identity returns a non-expiring GitHub token with no refresh token; retain support for the already-tested expiring-token path.
- Copilot records use `oauth-github-copilot` and `<state-root>/auth/github-copilot/<instance>.json`, mode 0600. Codex records and validation stay unchanged.
- Default endpoint is `https://api.githubcopilot.com`, overridden by `GITHUB_COPILOT_BASE_URL`. Do not add GHES, account-discovered hosts, `/auto`, PAT login, policy writes, or remote revocation.
- Use `default_model = "gpt-5.6-sol"`, `cheap_model = "claude-haiku-5.5"`, and default rank after `openai-codex` and before `openai`.
- Default tests are deterministic and network-free. Live tests require `EVENER_GITHUB_COPILOT_E2E=1` plus the credential/client ID. Do not log tokens or response bodies carrying them.
- Assert behavior, structured values, final URLs, and sentinels, never human-readable copy. Keep existing providers' behavior unchanged except where a shared mechanism carries an explicit Copilot opt-in.
- Work only in the `copilot-provider` worktree. Every task is TDD, commits its listed files, and uses the commit trailer supplied by the executor's own session. Report out-of-scope issues instead of absorbing them.
- Approved task-budget ceiling is 3,171 production lines after Jesse accepted Task 1's 242 lines, Task 2's 650 lines, a 600-line ceiling for Task 3, Task 5's reviewed 435 net lines, Task 6's reviewed 334 raw lines, and Task 7's reviewed 410 net lines. Each task also carries a local budget; stop and report any new overage rather than expanding scope.

## Review Focus

- Two processes refresh or logout the same rotating-token record: Task 1 locks and Task 2 refresh tests must prove no stale save, rejection, or resurrection.
- A successful but empty, unsupported, or policy-disabled Copilot roster: Task 5 tests must prove no static resurrection and no setup success.
- External CLI login, account switch, and logout while a hub runs: Task 7 tests must prove registry reload, cache invalidation, and notification without token leakage.
- Real adapter bodies, final paths, tool-result continuation, and images: Task 3 live scenarios and Task 5 deterministic URL tests must cover them.
- Account/state-root switches and subscription usage display: Tasks 3, 4, and 5 must prove credential/continuation isolation and absent dollar prices.

---

### Task 1: Scheme-Bound Record Storage and Lifecycle Lock

**Files:**
- Create: `auth/githubcopilot/record.go`, `auth/githubcopilot/storage.go`, `auth/githubcopilot/lock_unix.go`, `auth/githubcopilot/lock_other.go`
- Test: `auth/githubcopilot/record_test.go`, `auth/githubcopilot/storage_test.go`, `auth/githubcopilot/lock_process_test.go`

**Interfaces:**
- Produces: `const AuthScheme = "oauth-github-copilot"`; `AuthRecord`, `AuthFilePath(stateDir, instance string) string`, `LoadAuth`, `SaveAuth`, `DeleteAuth`, and `WithRecordLock(stateDir, instance string, fn func() error) error`.
- `AuthRecord` requires instance, scheme, version, GitHub access token, Copilot session token/expiry, token type/scope, obtained-at, and stable account ID. GitHub refresh/access-expiry fields are optional and paired for the current non-expiring identity. Email and `RefreshRejected` are optional state. Do not serialize an OAuth `source` field.

**Size budget:** 242 production lines, explicitly accepted by Jesse after the measured minimum exceeded the initial 200-line target.

- [ ] **Step 1: Write failing record and two-process lifecycle tests.** Cover valid round trips, mode 0600, wrong scheme/path, corrupt JSON, missing required fields, and cross-scheme rejection. In `lock_process_test.go`, run two actual test processes through `WithRecordLock`; prove serialized read/modify/write and that a deleted record cannot be recreated by a stale writer. Match `ErrAuthNotFound`, `ErrAuthCorrupt`, and validation errors structurally.
- [ ] **Step 2: Run the tests red.** Run `cd auth && go test ./githubcopilot`; expect missing-package/symbol failures for the new behavior, not an unrelated compile failure.
- [ ] **Step 3: Implement the record, storage, and lock.** Use `execsupport/fsatomic.Write` and `envvars/userdirs`; use the existing `O_NOFOLLOW`, regular-file, mode-0600, `flock` pattern on Unix. The non-Unix file keeps cross-compilation compiling without claiming shipped Windows support. Re-read under the lock and let callers compare the still-current record before saving.
- [ ] **Step 4: Run green and adjacent auth tests.** Run `cd auth && go test ./githubcopilot ./openai`; expect PASS and no unexpected error logs.
- [ ] **Step 5: Commit.** `git add auth/githubcopilot` and commit `feat(auth): add scheme-bound Copilot credential records`.

### Task 2: GitHub Device Login, Account Identity, and Refresh

**Files:**
- Create: `auth/githubcopilot/config.go`, `auth/githubcopilot/device.go`, `auth/githubcopilot/tokens.go`, `auth/githubcopilot/account.go`, `auth/githubcopilot/service.go`
- Modify: `auth/githubcopilot/record.go`, `auth/githubcopilot/storage.go`
- Test: `auth/githubcopilot/device_test.go`, `auth/githubcopilot/service_test.go`, `auth/githubcopilot/tokens_test.go`

**Interfaces:**
- Produces: `Config`, `DefaultConfig()`, `DeviceCode`, `TokenSet`, `Account`, `AuthStatus`, `RuntimeCredentials`, `RequestDeviceCode`, `PollDeviceOnce`, `RefreshToken`, and `FetchAccount`.
- `NewService(Config, *http.Client) *Service` exposes `LoginWithDevice`, `Status`, `Logout`, and `ResolveRuntimeCredentials`. `DeviceCode` owns device code, user code, verification URL, interval, and expiry. `PollDeviceOnce` returns authorized/pending/expired state plus the effective interval.
- `DefaultConfig()` eventually uses public Copilot client ID `Iv1.b507a08c87ecfe98`; Task 3 adapts the merged Task 2 service to the Pi-compatible session-token path.
- `RuntimeCredentials` carries bearer token, account ID, and expiry. `AuthStatus` derives source `oauth` from the scheme.

**Size budget:** 650 production lines, explicitly accepted by Jesse after review removed the concrete duplication and no easy substantial reduction remained.

- [ ] **Step 1: Write scripted GitHub tests.** Cover device start fields, pending, `slow_down` increasing the interval, denial, actual expiry, malformed responses, cancellation, account ID lookup, refresh rotation, `bad_refresh_token`, and recovery after fresh login. Use fake clocks, not sleeps. Two actual processes must race refresh and logout without stale saves or false rejection; an unchanged newer generation must win.
- [ ] **Step 2: Run red.** `cd auth && go test ./githubcopilot`; expect missing symbols or failed device/refresh behavior.
- [ ] **Step 3: Implement the service.** Use GitHub's JSON device and token endpoints, `read:user`, no client secret, and `GET /user` for stable identity. Refresh five minutes early when the GitHub token has an expiry. Hold the Task 1 inter-process lock across re-read, refresh, rejection update, and save; compare record generation before writing. A permanent rejection is keyed to the refused token and generation. Logout participates in the same lock.
- [ ] **Step 4: Run green.** `cd auth && go test ./githubcopilot ./openai`; expect PASS.
- [ ] **Step 5: Commit.** `git add auth/githubcopilot` and commit `feat(auth): add GitHub Copilot device login and refresh`.

### Task 3: Pi-Compatible Session Auth, Runtime Authenticator, and Live Probe

**Files:**
- Modify: `auth/githubcopilot/config.go`, `auth/githubcopilot/record.go`, `auth/githubcopilot/storage.go`, `auth/githubcopilot/device.go`, `auth/githubcopilot/tokens.go`, `auth/githubcopilot/service.go`
- Modify: `llm/registry/types.go`, `llm/providers/tokenauth/tokenauth.go`, `llm/auth_override.go`, `llm/client.go`, `llm/retry_util.go`, `cmdutil/registry.go`
- Create: `llm/providers/tokenauth/copilot.go`, `llm/providers/tokenauth/copilot_test.go`, `llm/copilot_live_test.go`
- Test: `auth/githubcopilot/record_test.go`, `auth/githubcopilot/storage_test.go`, `auth/githubcopilot/device_test.go`, `auth/githubcopilot/tokens_test.go`, `auth/githubcopilot/service_test.go`, `auth/githubcopilot/service_process_test.go`, `llm/auth_override_test.go`, `llm/retry_util_test.go`, `llm/providers/all/all_test.go`

**Interfaces:**
- Produces: `registry.AuthOAuthGitHubCopilot`, `tokenauth.DefaultCopilot`, `tokenauth.ScopedCopilot(stateDir string) *Copilot`.
- Extends Task 2 with the Pi-compatible public client identity, Copilot session exchange/renewal, and a record carrying both the GitHub token and Copilot session token/expiry. GitHub refresh fields remain optional for the current non-expiring identity.
- `Copilot` implements `llm.Authenticator`, `llm.AuthScopeProvider`, and `llm.RequestPreparer`. It uses Task 2 runtime credentials and never falls back to ambient GitHub tokens.
- `copilotLiveScenario` is the reusable opt-in table for this probe and Task 9's shipped-provider smoke.

**Size budget:** 600 production lines, explicitly accepted by Jesse after the measured session-auth implementation exceeded the initial 250-line target.

- [ ] **Step 1: Write authenticator and scoped-isolation tests.** Assert the bearer/account headers, state-root isolation, typed sign-in errors, build User-Agent, interaction IDs, user/agent initiator transitions, and vision header. Prove a Copilot token cannot authenticate Codex and vice versa. Add the new scheme to registration coverage. At the existing provider retry boundary, prove one 401 retry only when the on-disk record generation changed, no second 401 retry, no global 401 retry, and no replay after a stream was exposed.
- [ ] **Step 2: Run red.** `go test ./llm/providers/tokenauth ./llm/providers/all ./llm`; expect failures naming the absent Copilot scheme/authenticator.
- [ ] **Step 3: Implement session exchange, the authenticator, and rotation retry.** Adapt the merged auth service through failing tests first. Use public Copilot client ID `Iv1.b507a08c87ecfe98`, `read:user`, and the Pi-compatible exchange/editor/integration headers. Register the authenticator, bind state root/build version, and accept both record-backed OAuth schemes. Model requests use Evener's User-Agent plus the live-proven compatibility headers. Renew Copilot sessions five minutes early and preserve the record-generation 401 retry.
- [ ] **Step 4: Run the reusable live probe.** In `llm/copilot_live_test.go`, authenticate into the optional private `EVENER_GITHUB_COPILOT_PROBE_STATE_DIR`, force one session renewal without printing tokens, list models through the real Chat Completions protocol, and use real protocol streams with production-equivalent resolved rows. Require the complete tool loop for each advertised family/protocol combination. Keep both image scenarios pinned to `claude-haiku-5.5` and fail rather than silently skip a missing pin. Run `cd llm && EVENER_GITHUB_COPILOT_E2E=1 EVENER_GITHUB_COPILOT_CLIENT_ID=Iv1.b507a08c87ecfe98 EVENER_GITHUB_COPILOT_PROBE_STATE_DIR=<session-scratch> go test . -run TestGitHubCopilotLiveProbe -v`. Any enabled failure stops the plan and is reported to Jesse.
- [ ] **Step 5: Run deterministic tests and commit only after the probe passes.** Re-run all auth and LLM packages touched, expect PASS, then stage exactly the files listed above and commit `feat(llm): authenticate Copilot provider requests`.

### Task 4: Copilot Registry Instance, Configuration, and Healthy Defaults

**Files:**
- Modify: `llm/registry/schema.go`, `llm/registry/data/providers_overlay.toml`, `llm/registry/instances.go`, `llm/registry/resolve.go`, `envvars/envvars.go`
- Test: `llm/registry/overlay_test.go`, `llm/registry/instances_test.go`, `llm/registry/instance_resolve_test.go`, `llm/registry/load_test.go`

**Interfaces:**
- Consumes: Task 1 record path/validation and Task 3 auth scheme.
- Produces: an implicit `github-copilot` instance, `GITHUB_COPILOT_BASE_URL`, and a scheme-keyed automatic-credential-readiness hook used by `DefaultInstance`.

**Size budget:** 200 production lines.

- [ ] **Step 1: Write failing registry tests.** Prove no ambient GitHub variable creates the instance; a valid Copilot record does; corrupt/scheme-mismatched records do not authenticate; custom instances use separate records; endpoint override resolves; defaults rank correctly. Show dead Copilot records skip only automatic default selection, while explicit `default` remains authoritative. Assert static Copilot rows carry no monetary `Cost`.
- [ ] **Step 2: Run red.** `go test ./llm/registry`; expect failures for the absent overlay, scheme, endpoint, and readiness behavior.
- [ ] **Step 3: Implement registry/config behavior.** Add the schema value and curated overlay, preserve family surfaces, strip inherited dollar prices, and add scheme-owned readiness without changing other schemes' presence policy. Keep unhealthy records inspectable in Settings.
- [ ] **Step 4: Run green.** `go test ./llm/registry ./envvars`; expect PASS.
- [ ] **Step 5: Commit.** `git add llm/registry/schema.go llm/registry/data/providers_overlay.toml llm/registry/instances.go llm/registry/resolve.go llm/registry/overlay_test.go llm/registry/instances_test.go llm/registry/instance_resolve_test.go llm/registry/load_test.go envvars/envvars.go` and commit `feat(registry): add the Copilot subscription instance`.

### Task 5: Account Model Routing, Authoritative Listings, and Startup Validation

**Files:**
- Modify: `llm/providers/chatcompletions/models.go`, `llm/registry/resolve.go`, `llm/client.go`, `llm/responses_continuation.go`, `llm/responses_continuation_plan.go`, `cmd/evener/serve.go`, `agent/provider/profile.go`
- Modify: `cmd/evener-hub/app_live_prefetch.go`, `cmd/evener-hub/app_credentials.go`
- Test: `llm/providers/chatcompletions/models_test.go`, `llm/registry/disabled_test.go`, `llm/client_test.go`, `llm/client_continuation_test.go`, `cmd/evener/serve_test.go`, `agent/session_startup_listing_test.go`, `cmd/evener-hub/app_live_prefetch_test.go`, `cmd/evener-hub/app_credentials_credentialtest_test.go`

**Interfaces:**
- Produces: a registry live-listing snapshot carrying `Rows` and `Authoritative`, with apply/snapshot/reload methods that preserve both. Existing providers opt out of authority; Copilot opts in.
- Produces: `ResponsesEndpointFamilyGitHubCopilot`, disabled by default. Copilot always sends full history.
- Consumes: one startup `ModelListing`/`StartupModelListing` for primary and cheap decisions; no second `/models` request.

**Size budget:** 435 net production lines, explicitly accepted by Jesse after the corrected model-routing and authority diff passed re-review.

- [ ] **Step 1: Write failing discovery and launch tests.** Script `supported_endpoints`, policy-disabled rows, tools/media/reasoning facts, and empty rosters. Assert Claude selects Messages with final `/v1/messages`, Responses selects `/responses`, Chat selects `/chat/completions`, absent metadata falls back to Chat, and count tokens is unsupported for Copilot Messages. Prove user protocol/transport overrides win. Empty/all-unsupported/disabled live results return no static rows and no setup success; fetch/parse failures retain existing diagnostics. Assert no live dollar prices.
- [ ] **Step 2: Add failing startup/continuation tests.** A live-absent primary returns a typed available-models error; a live-absent curated cheap model uses the session model, while an explicit cheap override does not silently change. Both read the same primed listing. Copilot Responses never inherits public OpenAI continuation and two accounts/state roots remain isolated.
- [ ] **Step 3: Run all listed tests red.** Use package-targeted `go test` commands; confirm each failure comes from the new scenario.
- [ ] **Step 4: Implement discovery, authority, startup, and continuation behavior.** Parse account metadata at the external boundary, carry protocol plus transport through the live layer, and apply authoritative empty snapshots through hub reapplication/reload. Reuse the startup roster in the registry/profile rather than fetching again.
- [ ] **Step 5: Run green and commit.** Run every listed package, expect PASS, then stage exactly the files listed above and commit `feat(llm): route Copilot models from account capabilities`.

### Task 6: Hub Copilot Device Flow, Status, Logout, and Scoped Requests

**Files:**
- Modify: `cmd/evener-hub/app_auth.go`, `cmd/evener-hub/app_live_prefetch.go`, `cmd/evener-hub/app_credentials.go`, `cmd/evener-hub/spawn.go`, `auth/githubcopilot/service.go`
- Test: `cmd/evener-hub/app_auth_test.go`, `cmd/evener-hub/app_auth_modes_test.go`, `cmd/evener-hub/app_auth_device_binding_test.go`, `cmd/evener-hub/app_auth_device_sweep_test.go`, `cmd/evener-hub/app_auth_flow_endpoint_test.go`, `cmd/evener-hub/app_auth_flow_expiry_test.go`, `cmd/evener-hub/spawn_test.go`, `auth/githubcopilot/service_test.go`

**Interfaces:**
- Consumes: Task 2 device/service methods, Task 3 scoped authenticator, Task 5 setup usability result.
- Produces: scheme-selected device/status/logout behavior over unchanged AppWire response types. Copilot flow state owns interval/expiry; hub state adds only `NextPollAt`.

**Size budget:** 334 raw production lines (58 removals, 276 net), explicitly accepted by Jesse after shared login completion and credential-lock scope passed re-review.

- [ ] **Step 1: Write failing hub tests.** Cover both OAuth schemes in `authModesFor`; Copilot device start/poll/status/logout; no redirect fallback; actual expiry; early polls answered locally; `slow_down` changing outbound timing; endpoint restart; write locking and notifications; API-key refusal; empty-roster setup refusal. Prove scheme/state-root selection on model-list and credential probes.
- [ ] **Step 2: Run red.** `go test ./cmd/evener-hub -run 'Copilot|AuthModes|Device|Flow'`; confirm the new Copilot cases fail for the intended reason.
- [ ] **Step 3: Implement scheme dispatch.** Keep browser login Codex-only. Replace the single device-code field with provider-owned variants, enforce `NextPollAt` before outbound Copilot polls, and save records through Task 1/2 interfaces under existing credential fences. Generalize scoped auth selection by resolved scheme.
- [ ] **Step 4: Run green.** `go test ./cmd/evener-hub`; expect PASS.
- [ ] **Step 5: Commit.** Stage exactly the files listed above and commit `feat(hub): support Copilot device authentication`.

### Task 7: Instance Lifecycle and External CLI Change Recovery

**Files:**
- Modify: `cmd/evener-hub/app_instances.go`, `cmd/evener-hub/app_provider_reload.go`, `cmd/evener-hub/app_auth.go`, `cmd/evener-hub/app_rpc.go`, `llm/registry/instances.go`, `llm/registry/resolve.go`
- Test: `cmd/evener-hub/app_instances_test.go`, `cmd/evener-hub/app_provider_reload_test.go`, `llm/registry/instances_test.go`

**Interfaces:**
- Produces: Copilot-aware rename/remove/rollback/stray-record handling and external-record change signatures containing only existence, instance, scheme, account identity, corruption state, and login health.
- Produces: registry live-invalidation for an account switch; no token, obtained-at, or rotating-token value enters the signature.

**Size budget:** 410 net production lines (439 additions, 29 removals), explicitly accepted by Jesse after canonical rename locking and exact-state adoption passed re-review.

- [ ] **Step 1: Write failing lifecycle and running-hub tests.** Cover rename, destination refusal, removal rollback, corrupt records, stray-record guidance, and scheme changes without token reuse. Drive real external CLI-service login/account-switch/logout against scripted GitHub, then run the hub notice sweep; assert registry membership/default changes, account roster invalidation, and one visible notification.
- [ ] **Step 2: Run red.** `go test ./cmd/evener-hub ./llm/registry`; confirm the new lifecycle cases fail.
- [ ] **Step 3: Implement lifecycle and notice recovery.** Use the scheme-namespaced paths and record lock for mutations, preserve raw-byte rollback, and observe Copilot auth state independently of `providers.toml`. Invalidate only affected account-scoped live state.
- [ ] **Step 4: Run green and commit.** Re-run Step 2, expect PASS, stage exactly the listed files, and commit `feat(hub): recover Copilot instance auth changes`.

### Task 8: Copilot CLI Login, Status, and Logout

**Files:**
- Create: `cmd/evener/github_copilot.go`, `cmd/evener/github_copilot_login.go`, `cmd/evener/github_copilot_status.go`, `cmd/evener/github_copilot_logout.go`, `cmd/evener/github_copilot_test.go`
- Modify: `cmd/evener/main.go`, `cmd/evener/main_test.go`

**Interfaces:**
- Consumes: Task 2 service.
- Produces: `evener github-copilot login|status|logout`, each accepting `--dir`, `--state-dir`, and `--instance` defaulting to `github-copilot`. Login is device-only. Stdout uses machine-readable fields already established by OpenAI (`auth_mode`, `device_code_url`, `device_code`, status fields), never tokens.

**Size budget:** 200 production lines.

- [ ] **Step 1: Write failing command tests.** Use scripted GitHub to cover dispatch/help, device login, custom instance/state directory, local status, logout, cancellation, missing/corrupt/rejected credentials, and no secret output. Assert machine-contract fields and side effects, not prose.
- [ ] **Step 2: Run red.** `go test ./cmd/evener -run GitHubCopilot`; expect unknown-command or missing-symbol failures.
- [ ] **Step 3: Implement the sibling command group.** Reuse the shared Evener state-dir resolution and OpenAI output conventions without adding browser flags or a generic login redesign.
- [ ] **Step 4: Run green and commit.** `go test ./cmd/evener`; expect PASS. `git add cmd/evener/github_copilot.go cmd/evener/github_copilot_login.go cmd/evener/github_copilot_status.go cmd/evener/github_copilot_logout.go cmd/evener/github_copilot_test.go cmd/evener/main.go cmd/evener/main_test.go` and commit `feat(cli): add Copilot subscription login commands`.

### Task 9: Web/Native Sign-In and Shipped-Provider Live Gate

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/ProviderConnection.tsx`, `oauthFlow.ts`, `oauthDialogs.tsx`, `cmd/evener-hub/app_auth_scheme_pin_test.go`
- Rename: `cmd/evener-hub/codex_auth_scheme.txt` to `cmd/evener-hub/device_oauth_auth_schemes.txt`.
- Test: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/ProviderConnection.test.tsx`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/oauthFlow.test.ts`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/oauthDialogs.test.tsx`, `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsHostScope.test.tsx`, `mobile-native/src/providerSignIn.test.ts`, `mobile-native/src/hub/ProvidersPage.test.tsx`, `llm/copilot_live_test.go`

**Interfaces:**
- Consumes: the shipped registry/hub/CLI path and the Task 3 scenario table.
- Produces: device sign-in support for both OAuth schemes, provider-labelled device dialogs, and only after live success, Copilot in `PROVIDERS`/the popular set.

**Size budget:** 100 production lines.

- [ ] **Step 1: Write failing non-popular UI tests.** Prove both schemes support host device sign-in, dialogs use the selected provider, native accepts Copilot OAuth, cancellation/draft recovery stays intact, and no API-key action appears. Do not assert copy strings.
- [ ] **Step 2: Implement scheme/dialog generalization and run targeted tests.** Extend the cross-language pin to two schemes. Run the touched Vitest files and native tests; expect PASS.
- [ ] **Step 3: Run the shipped-provider live gate.** Reuse `copilotLiveScenario` through the real registry and shipped authenticator with `EVENER_GITHUB_COPILOT_E2E=1`; require every enabled scenario to pass. On failure, stop before adding the popular option.
- [ ] **Step 4: Add the failing popular-provider test, then the table entry.** Prove Copilot appears in initial discovery, signs in, performs the model check, preserves drafts, and cannot complete on an empty roster. Add only the `github-copilot` presentation entry and subscription-appropriate metadata.
- [ ] **Step 5: Verify and commit.** Run touched frontend/native tests and `npm run typecheck` from `cmd/evener-hub/frontend`. Stage exactly the listed files and commit `feat(ui): offer Copilot subscription sign-in during setup`.

### Task 10: Evergreen Documentation and Final Verification

**Files:**
- Modify: `docs/connecting-a-provider.md`, `docs/getting-started.md`, `docs/llm-providers.md`, `docs/llm-provider-config-and-launch.md`, `docs/product/subsystems.md`, `docs/environment-variables.md`
- Modify if generated input changes require it: `docs/developing-evener/README.md`
- Delete after execution is complete: `docs/superpowers/plans/2026-11-22-github-copilot-provider.md`
- Test: relevant documentation/link checks and all targeted packages touched by Tasks 1–9.

**Interfaces:** Documents the shipped behavior only. Keep the design spec as the historical decision record.

**Size budget:** documentation only; no production-line growth without a newly reported defect and Jesse's scope decision.

- [ ] **Step 1: Update provider docs and generated environment output.** Document device login/status/logout, instance/endpoint config, scheme-namespaced storage, refresh/logout recovery, supported github.com scope, direct-HTTP compatibility boundary, per-account protocols/policies, no dollar estimates, and model-list versus generation/quota guarantees. Update S15/S16 ownership and model discovery.
- [ ] **Step 2: Verify generated freshness and documentation.** Run `make generate` only if the environment registry requires it, inspect every changed file, and stage only intended outputs. Run the docs checks that cover changed evergreen files; the `docs/superpowers` records are excluded from `make docs-check`.
- [ ] **Step 3: Run final targeted and required gates.** Run all affected Go package tests, Biome on touched TypeScript, `make test-web`, then `make vet`, then `make lint` once each and not concurrently. Report actual commands and failures; do not claim a local full-suite run.
- [ ] **Step 4: Run simplify-code and whole-branch review.** Use `/simplify-code` on the implementation, then `par` once. Resolve legitimate in-scope findings red-test-first and file unrelated/nonblocking findings instead of growing the PR.
- [ ] **Step 5: Remove the completed plan and commit documentation.** Stage exactly the intended docs and the plan deletion, inspect the staged diff, and commit `docs(provider): document Copilot subscription setup`.

## Execution Notes

- Recommended execution is subagent-driven: these tasks share exact interfaces and carry high credential/recovery risk, so each task needs a fresh implementer and reviewer.
- Every implementation brief must quote the task's in-scope behavior, local size budget, exact owned files, red/green commands, and requirement to report everything else rather than fix it.
- Do not create a PR until the live probe and shipped-provider live gate have passed. If either fails, leave the branch unmerged and report the non-secret evidence to Jesse.
