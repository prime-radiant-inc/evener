# Compatible Host Connect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Connect to a compatible installed Evener without replacing its build, preserve explicit deployment recovery, and report truthful live identity.

**Architecture:** The SSH manager separates disk launch facts from live connection authority. A compatible initialized channel is retained unless recorded deployment/start effects still require reconciliation. Optional build metadata in the existing initialize response supplies channel-bound identity without another RPC or protocol version.

**Tech Stack:** Go, AppWire JSON-RPC, generated TypeScript, Vitest, real OpenSSH in a disposable Docker container.

**Spec:** `docs/superpowers/specs/2026-10-20-compatible-host-connect-design.md`, approved at `31f96f54d8123c6bcf2046a5255134817c37e34a`.

## Global Constraints

- Normal Connect keeps compatible installations even when a matching artifact is available.
- No new controls, settings, protocol version, compatibility shims, or widgets are proposed.
- No deployment to or restart of Jesse's running hub or m5max.
- A generic `InvalidRequest`, its message text, malformed response, EOF, or timeout does not establish protocol incompatibility.
- Keep explicit deployment's pending restart and uncertain-write reconciliation.
- Tests assert outcomes, structured fields, and side effects rather than copy.
- Keep default tests deterministic. Full repository gates run in CI; local checks target changed behavior.
- Preserve unrelated work and shared caches. Never install dependencies through symlinked `node_modules`.

## Review Focus

- A missing protocol field looks like a typed mismatch today: require no deployment for this malformed response in Task 2.
- A different on-disk protocol can hide a usable off-PATH bridge: retain discovered targets and test live/disk disagreement in Task 2.
- A same-label snapshot can carry a different commit: normal bootstrap must not acquire the controller's commit pin, including recovery in Task 3.
- An old compatible process can answer during an unfinished deployment: retain requested identity and replacement proof in Task 3.
- Clients refresh host rows after Connect: preserve actual or unknown live build through list/probe/reconnect, rather than testing only the attach response, in Tasks 1 and 4.

## File boundaries and order

1. Live build metadata and host facts: shared handshake types, server emission, hub facts and client-facing responses.
2. Compatible-first running-host connection: SSH preflight, manager decision path, protocol classification and its regression matrix.
3. Cold bootstrap and recorded recovery: retain actual installation identity for ordinary starts and strict requested identity for deployments.
4. Whole journey and documentation: real isolated SSH test, affected client tests, owning guides and full-branch evidence.

Task 1 is independently testable. Tasks 2 and 3 share SSH policy and should run sequentially in one lane. Task 4's real-process regression is written and run before the Task 2 production edit so the original failure is observed; its final checks run after all changes. Do not parallelize writers in this checkout.

---

### Task 1: Channel-bound running build identity

**Files:**
- Modify `appwire/types.go`, `internal/appserver/server.go`, `cmd/evener-hub/app_rpc.go`, `cmd/evener-hub/main.go`.
- Test `internal/appserver/server_test.go`, `cmd/evener-hub/app_rpc_test.go`, `cmd/evener-hub/main_test.go`, `cmd/evener-hub/attached_only_lazy_test.go`, `cmd/evener-hub/app_host_manage_channel_generation_test.go`.
- Regenerate `appwire-client/typescript/types.gen.ts` and `docs/appwire-protocol.md` using `go generate ./appwire`.

**Interfaces:**
- Add `BuildVersion string` with JSON `buildVersion,omitempty` to `appwire.ServerInfo` and `BuildVersion string` to `appserver.ServerConfig`.
- `Server.initialize` copies the configured field. The hub supplies `buildinfo.Version()`; existing `ServerInfo.Version` keeps its package-version meaning.
- Change `remoteHostFacts` to `func remoteHostFacts(pf sshconn.Preflight, handshake appwire.InitializeResponse, features appwire.FeatureSet) appsource.HostFacts`.
- The captured handshake owns `ProtocolVersion` and `HubVersion`. Preflight still owns disk/system facts. Existing channel/client identity checks remain in place.

- [ ] **Write failing-first identity tests.** `TestHubInitializeReportsBuildVersion` reads raw initialize JSON and checks its nonempty `serverInfo.buildVersion` against the actual build. `TestRemoteHostFactsUseLiveBuild` sets disk, package and live versions to distinct sentinel values and expects only the live build. `TestRemoteHostFactsUnknownBuildStaysUnknown` expects an empty build when metadata is absent. Retain cancellation and reconnect generation refusals.
- Assert through the unchanged `remoteHostFactsForChannel` interface for the red run; supply the new handshake field using JSON, so old code ignores it and fails behaviorally. Core assertions:

  ```go
  if got.HubVersion != "remote-build-a" { t.Fatalf("live build = %q", got.HubVersion) }
  if unknown.HubVersion != "" { t.Fatalf("invented build = %q", unknown.HubVersion) }
  if !errors.Is(canceledErr, context.Canceled) { t.Fatalf("cancellation = %v", canceledErr) }
  ```

- [ ] **Run red.** `go test ./cmd/evener-hub -run 'Test(HubInitializeReportsBuildVersion|RemoteHostFacts)' -count=1`. Before adding the field, raw JSON and serialized handshake input permit behavioral failure rather than a compilation-only red.
- [ ] **Implement metadata and projection.** Do not read `evener/host/running` for metadata: it persists fencing state. Do not use `/api/health`, disk version, package version, or controller version as a different channel's live build. Absence stays unknown and does not block attachment.
- [ ] **Generate and verify.** Run `go generate ./appwire`, then `go test ./internal/appserver ./appwire ./cmd/evener-hub -run 'Test.*(Initialize|RemoteHost|HostAttach|HostManage.*Generation|AttachedOnly)' -count=1`. Review generated changes; expect exit zero without failures.
- [ ] **Commit only the listed changed files after reading the staged diff.** Suggested message: `fix(hub): report channel-bound remote build identity`.

### Task 2: Compatible-first running-host attachment

**Files:**
- Modify `cmd/evener-hub/internal/sshconn/{manager.go,preflight.go,operations.go}`.
- Add `cmd/evener-hub/internal/sshconn/compatible_connect_test.go`.
- Extend `cmd/evener-hub/internal/sshconn/fakes_test.go`; migrate relevant assertions in `manager_test.go`, `deploy_test.go` and `version_test.go` without weakening deployment coverage.

**Interfaces:**
- Keep `ensureOnce(ctx context.Context, host hostreg.Host, explicit bool) (*Channel, error)` and `attach(ctx context.Context, host hostreg.Host, facts Preflight) (*Channel, error)`.
- Introduce `preflightResult` with `facts Preflight` and `launchErr error`, returned by `collectPreflight(ctx context.Context, host hostreg.Host) (preflightResult, error)`. The returned error is host/environment failure; launch-contract refusal stays in `launchErr`.
- Keep `preflight(ctx context.Context, host hostreg.Host) (Preflight, error)` as the explicit planning wrapper, preserving its current source-availability/error contract.
- Keep `Channel.Preflight()` disk-owned and `Channel.Handshake()` channel-owned.

- [ ] **Write failing-first running-host matrix.** Add `TestEnsureCompatibleBuildDifferenceDoesNotDeploy` for clean, dev and dirty versions, configured/unavailable/disabled sources, and a Linux/amd64 controller with Darwin/arm64 host facts. Configure valid recorders and real framed bridge fixtures; assert successful initialization, zero build/push/installer/restart effects, and identical channel on repeated Ensure.
- [ ] **Write the live/disk and error cases.** `TestEnsureCompatibleLiveHubWinsOverDiskFacts` covers different disk build/protocol, rejected or malformed launch-check, missing optional live features, and discovered installer-default target. `TestEnsureAmbiguousInitializeFailureDoesNotDeploy` covers generic InvalidRequest with protocol-looking prose, omitted/empty protocol, malformed frames, EOF, timeout and authentication/transport failures, with zero mutation and no published attachment.
- [ ] **Write genuine mismatch recovery.** `TestEnsureLiveProtocolMismatchRecovers` uses serialized structured `UpgradeRequired` and a successful nonempty mismatched protocol, then a compatible final handshake. Assert at most one recovery sequence, strict source validation, recorder ordering, and no writes when deployment is disabled or unavailable. Exercise the real server rejection path as well as scripted external transport responses.
- The runner fixtures count test-local `builds`, `pushes`, `installs` and `restarts`. The compatible matrix must satisfy:

  ```go
  if err != nil || ch == nil { t.Fatalf("attach: channel=%v err=%v", ch, err) }
  if builds+pushes+installs+restarts != 0 { t.Fatal("compatible Connect mutated the host") }
  if again != ch { t.Fatal("repeated Connect replaced the channel") }
  ```

- [ ] **Run red.** `go test ./cmd/evener-hub/internal/sshconn -run 'TestEnsure(Compatible|LiveProtocolMismatch|AmbiguousInitialize)' -count=1`. Also run Task 4's new real container regression against this pre-policy code; record the unwanted binary/process change.
- [ ] **Implement live-first policy.** Retain the discovered executable even when its launch-check rejects the protocol. Preserve environmental failure and verified-missing-executable handling. Reconcile pending effects before a compatible-live return. For a present or uncertain listener, attempt initialize and retain that channel directly on success. Generic failure cannot authorize deployment. Only positively missing installation, required cold-start contract, or positive live incompatibility reaches recovery.
- [ ] **Classify structured refusals.** Decode `WireError.Data` into `appwire.ErrorData` and match `ErrorUpgradeRequired`. A `ProtocolVersionMismatchError` with empty `Got` is not positive protocol evidence. Preserve the underlying error and existing cancellation/transport semantics.
- [ ] **Remove synchronization-only state.** Remove `devDeployed` and its helpers/callers, version-only deploy predicates and live version-only restart decisions. Retain `resolvedTargets`, pending effects, artifact/commit verification and explicit operation entrypoints.
- [ ] **Migrate old deployment fixtures.** Tests whose subject is provisioning/deployment must trigger genuine missing/incompatible conditions or explicit operations. Tests whose subject was forced synchronization become preservation tests. Do not merely remove their identity, recorder or side-effect assertions.
- [ ] **Verify and commit.** Run `go test ./cmd/evener-hub/internal/sshconn -count=1`; read every failure. Commit the exact changed SSH paths after staged diff review. Suggested message: `fix(sshconn): keep compatible running hosts on Connect`.

### Task 3: Existing-build bootstrap and strict recovery

**Files:**
- Modify `cmd/evener-hub/internal/sshconn/{manager.go,version.go}`.
- Extend `compatible_connect_test.go`, `version_test.go`, `gate_test.go`, `round12_test.go` and existing pending-start/restart tests as needed.

**Interfaces:**
- Change bootstrap to `bootstrapHub(ctx context.Context, host hostreg.Host, facts Preflight, expectedVersion, expectedGitSHA string) error`, passing the origin-specific expectation explicitly from all callers.
- Extend `pendingRestartState` with `expectedVersion` and `expectedGitSHA` strings. Capture these at start/restart recording and consume the captured pair during settlement/replay instead of deriving a new expectation from later disk facts.
- Ordinary bootstrap uses the installed version without the controller SHA pin. Actual deployment/restart keeps requested version/SHA plus replacement-process proof.

- [ ] **Write cold-start regressions.** `TestEnsureStoppedCompatibleInstallationBootstrapsWithoutDeploy` proves no listener, one start, zero deploy, and a bridge that only works after the start effect. Cover different clean/dev/dirty builds. `TestReconnectStoppedCompatibleInstallationDoesNotBootstrap` asserts no start for non-explicit reconnect.
- [ ] **Write startup-contract and identity cases.** `TestEnsureStoppedInstallationRequiresLaunchCapabilities` keeps the disk `api-log` requirement for startup. `TestEnsureSnapshotBootstrapDoesNotRequireControllerCommit` uses the same snapshot label with a different commit, including interrupted-start recovery. Explicit deployment still rejects wrong commit and old process identity.
- [ ] **Write recovery precedence.** `TestEnsureCompatibleLiveHubDoesNotSettlePendingRestart` creates the pending effect through the real manager path, then serves an old compatible process. It remains unresolved until expected identity and process replacement are proved; unknown start time remains unresolved. Use the real host operation store for journal assertions.
- The cold-start fixture counts `starts` and `deploys`; after successful explicit Connect require `starts == 1 && deploys == 0`. Its reconnect companion requires `starts == 0 && deploys == 0`. Pending-effect tests assert the retained state and typed refusal before allowing a replacement:

  ```go
  if starts != 1 || deploys != 0 { t.Fatalf("starts=%d deploys=%d", starts, deploys) }
  if !errors.Is(recoveryErr, ErrRestart) { t.Fatalf("recovery error = %v", recoveryErr) }
  ```

- [ ] **Run red.** `go test ./cmd/evener-hub/internal/sshconn -run 'Test(EnsureStopped|ReconnectStopped|EnsureSnapshotBootstrap|EnsureCompatibleLiveHubDoesNotSettle)' -count=1`.
- [ ] **Implement the origin-specific expectations.** Positively prove an empty port before bootstrap using existing listener probes; an unhealthy or unreadable health endpoint alone does not prove absence. Preserve explicit-Connect-only permission, recorded-start recovery, write-before-restart record ownership, and bounded waits. Do not clear a deployment journal entry merely because an old compatible channel connected.
- [ ] **Verify recovery and race checks.** Run the whole SSH package and focused hub lifecycle tests, then `go test -race ./cmd/evener-hub/internal/sshconn -count=1`. Expected: completed exit zero, with pending/unknown-identity and recorder-before-write tests still active.
- [ ] **Commit the exact changed files.** Suggested message: `fix(sshconn): separate bootstrap and deployment identity`.

### Task 4: Real SSH journey, both clients, and owning documentation

**Files:**
- Add `cmd/evener-hub/app_host_compatible_connect_e2e_test.go`.
- Reuse helpers from `app_host_pipeline_container_e2e_test.go`, `app_host_deploy_e2e_test.go`, `app_host_e2e_test.go` and `app_host_disposable_e2e_test.go`; extend shared build helpers only where needed to avoid duplication.
- Extend `cmd/evener-hub/frontend/src/stores/hosts.test.ts`, `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.test.tsx`, `mobile-native/src/hosts/hostsController.test.ts` and `mobile-native/src/hub/HostDetailPage.test.tsx`.
- Update `docs/evener-hub.md`, `docs/evener-hub-remote-operations.md`, `docs/product/subsystems.md`, `docs/product/friction.md`, `docs/developing-evener/testing.md`, and stale connection-policy comments in `cmd/evener-hub/internal/sshconn/doc.go`.

**Interfaces:**
- `TestHostCompatibleConnectContainerE2E` is container-only and explicitly opt-in with the existing `EVENER_SSH_E2E=1`, `EVENER_SSH_E2E_DEPLOY=1`, `EVENER_SSH_E2E_CONTAINER=1` gates. It must never honor an external-host override.
- Reuse `startHostPipelineContainer`, `installHostPipelineSSHShim`, `startDeployStack`, `forwardHostMethod` and structured host health readers. The SSH shim supplies only the test-owned SSH config; it executes real OpenSSH.
- Two real binaries use different build stamps with the same AppWire protocol. The controller starts with default deployment settings, not `--no-deploy` or a replacement deployment seam.

- [ ] **Write and run the real regression before Task 2's production change.** Docker fixture must have private filesystem/process/service namespaces, no privileged mode, host PID namespace, host bind mounts or host service sockets. Read the helper and inspect the created container's effective isolation before exercising the controller's mutating path. Never use ordinary-account localhost SSH.
- [ ] **Assert the full journey.** Seed a remote-only directory entry. Add the host, Connect, read attach/probe/list identity, forward `evener/paths/complete` and find the entry, while the controller-local read cannot find it. Repeat Connect, disconnect, reconnect and repeat the read. Compare remote binary SHA256 and hub process/start identity before and after. Leave default deployment enabled and ensure no deployment operation was created. Cleanup owns every process/container/file it removes.
- Independently observed preservation assertions, repeated after each connection:

  ```go
  if afterHash != beforeHash { t.Fatal("Connect replaced the remote executable") }
  if !afterHealth.StartedAt.Equal(beforeHealth.StartedAt) { t.Fatal("Connect restarted the remote hub") }
  if attached.HubVersion != remoteVersion { t.Fatalf("remote version = %q", attached.HubVersion) }
  ```

- [ ] **Verify both clients' existing consumers.** Use distinct controller/remote version sentinels and missing build metadata. Connect and refresh/reconnect must retain remote or unknown identity, not substitute package/controller values. Assert host state, structured fields and rendered sentinel data, never UI copy. Change production client code only if these cases expose a defect.
- [ ] **Run focused client checks.** Install only into verified non-symlink directories. From the frontend: `npm test -- src/stores/hosts.test.ts src/panes/settings/sections/hosts.test.tsx`, then `npm run typecheck`. From native: `npm test -- src/hosts/hostsController.test.ts src/hub/HostDetailPage.test.tsx`, then `npm run check`. Format touched TypeScript using each tree's pinned Biome before checking it.
- [ ] **Run the final real journey.** `EVENER_SSH_E2E=1 EVENER_SSH_E2E_DEPLOY=1 EVENER_SSH_E2E_CONTAINER=1 go test ./cmd/evener-hub -run '^TestHostCompatibleConnectContainerE2E$' -count=1 -v`. A skip is unverified, not a pass. Also run the existing container provisioning/explicit-deployment pipeline to verify preservation.
- [ ] **Update evergreen contracts.** Explain compatible-first Connect, unknown metadata, explicit deployment, and recovery ownership. Remove H05 only after its behavior is verified; do not renumber other friction cases. Document the container-only test command and its isolation.
- [ ] **Commit and hand off whole-branch review.** Report exact red/green commands, actual tests executed, generated files, and cleanup. Darwin/arm64 decision coverage is deterministic fixture evidence; Linux Docker execution is not actual Darwin verification. Run PAR, required simplify-code, shepherd-pr and CI before merging. Do not deploy the merge to live hosts.

### Recovery amendment: Retain and renew host-entry authorization

**Goal:** Automatic retries retain the original entry validation. A fresh validated
Restart may renew that validation while preserving the pending deployment's
requested build and effect state. Deduplicated requests do not renew it.

**Files:** `cmd/evener-hub/{app_host_deploy.go,app_host_manage.go}`,
`cmd/evener-hub/internal/sshconn/{operations.go,manager.go,version.go,deploy.go}`,
their focused recovery tests, and the owning host guides.

- [ ] **Review only the affected specification.** Freeze and independently review
  the host-entry authorization amendment and its lifecycle proof obligations.
  Preserve the existing failing `attached/entry-reconnect` public regression.
- [ ] **Prove renewal and automatic refusal red-first.** Extend the public journey
  through stale-entry refusal, SSH loss, automatic refusal, fresh Restart under the
  changed entry, and verified original-build recovery. Cover dedup and failed
  admission without renewal, plus another entry change after successful renewal.
- [ ] **Carry one effect-owned binding.** Pass the hub's immutable entry validation
  and original registration into explicit operations. Retain them with pending
  state, not the worker context or attempt record. Preserve the binding across
  write acknowledgement, restart promotion, command recording and replay. A fresh
  admitted Restart validates its new binding before replacing the pending binding;
  automatic attempts never replace it. A new confirmed Deploy supersedes only
  when its first validated write begins.
- [ ] **Guard shared mutation boundaries.** Make the existing pre-write callback
  able to refuse a write. Cover installation, supervised commands, bare startup,
  saved replay, and both halves of bare restart. Retain read-only settlement and
  typed refusal behavior. Release bindings with registration teardown and shutdown.
- [ ] **Verify the complete lifecycle.** Run public recovery cases with normal and
  race repetitions, the whole SSH package normally and under race, focused public
  Deploy/Plan/Restart tests, and scoped lint. Review the resulting diff. After a
  clean commit, run both disposable OpenSSH journeys without skips. Full gates
  remain in CI. No live hosts or new client controls are involved.
- [ ] **Finish one review round.** Update owning documentation, commit intended
  paths, reconcile the base, push once, and wait once for current-head CI and
  RoboRev. Preserve the existing merge and cleanup requirements.
