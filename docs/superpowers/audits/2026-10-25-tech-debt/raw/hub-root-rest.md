# Tech-debt audit: hub-root-rest

Slice: `cmd/evener-hub/*.go` at package root that do not start with `app_` (non-test), plus a judgment on whether the
`cmd/evener-hub` main package (`package hub`) should be split.
Worktree: `.worktrees/debt-audit` (origin/main). Read-only audit; no tests were run. Line counts and reference counts
come from `wc`, `grep`, and two throwaway Go/AST scripts kept in the session scratchpad (a name-based "referenced from
production?" scan and a `go/types` undefined-identifier scan used for the split analysis).

## Overview and inventory

| Metric | Value |
| --- | --- |
| Non-test files in slice | 60 files, 17,360 lines (21% comment lines: 3,642) |
| Whole `package hub` non-test | 137 files, 56,197 lines (slice 17,360 + `app_*` 38,837) |
| Whole `package hub` test code | 192,767 lines (463 `_test.go` files); non-`app_` tests 70,696 lines in 242 files |
| `internal/` sibling packages | 14, 39.6k lines (`sshconn` 11.4k, `hubcore` 13.5k, `hostops` 9.8k, `appsource` 9.4k, `launchconfig` 1.9k, ...) |
| Importers of `package hub` | exactly one production caller: `cmd/evener/main.go:20,512` (`hubcmd.Run`) |
| Functions over 100 lines in slice | 14. Worst: `runMain` 775 (`main.go:253-1028`), `buildSnapshot` 170, `projectDelete` 166, `refreshRemoteThreadSnapshot` 159, `navigationSnapshotInputs` 154, `decodeConfig` 143, `fitNavigationV3Snapshot` 137 |
| Package-level mutable seams (`var x = os.Y` / `func(){}`) in slice | ~44 (list in finding 12) |
| `cov_*_test.go` coverage files | 40 files, 6,031 lines (finding 11) |
| Production functions referenced only from tests (slice) | 11 funcs + 16 methods (finding 5, 16) |
| Comment-narration markers in slice | 11 `kata xxxx`, 10 issue numbers, 69 spec/component/08b citations, 27 history phrases (finding 19) |

Largest files: `navigation_projection.go` 1,956; `navigation_service.go` 1,731; `web_api_tree.go` 1,528; `main.go` 1,419;
`spawn.go` 1,305; `doc_serve.go` 908; `project_delete.go` 811; `config.go` 787.
The navigation_* group is 5.4k source lines with 10.1k test lines.

The AppWire handlers (`app_*`, covered by the other two hub audits) are where most churn lives. This slice is the
composition root (`main.go`, `web*.go`, `config.go`), the launch path (`spawn*.go`), the navigation engine, CLI
subcommands, and image/doc serving.

Product-model notes used for fix plans: AGENTS.md says recovery over lockdown, evergreen docs updated in the same
change, shared client owns shared derivations. None of the findings below remove a recovery path. Where a fix would
change a user-visible promise or drop a config key, it is marked "needs Jesse's decision".

---

## Findings

### hub-root-rest-1: `runMain` is a 775-line composition root with late-bound closures and dead wiring (severity: high)

Locations: `main.go:253-1028`; `main.go:470-471` vs `main.go:828-829`; `main.go:568-580`; `main.go:671-760`; `main.go:840`.

Evidence:
- One function builds config, stores, SSH manager, WebServer, invalidation hooks, 9 background goroutines, endpoint
  record and banner. 44 `_, _ = fmt.Fprintf(stderr, "[hub] ...` sites inside it (46 in the file).
- Dead wiring: `archive.SetOnChange(bump)` / `favorite.SetOnChange(bump)` (`main.go:470-471`) are replaced wholesale by
  `archive.SetOnChange(func(){ bump(); ...Invalidate(...) })` at `main.go:828-829`. `SetOnChange` is a plain
  assignment (`internal/hubcore/archive.go:106`, `favorite.go:33`), so the first calls (and the comment above them,
  "Wire archive/favorite's content-delta-gated onChange hook (Task 10)") do nothing.
- Six hand-copied `bump(); web.navigation.Invalidate(navigationChangeHint{...})` closures (`main.go:823-845`,
  `main.go:1418`). `if pinSections != nil` at `main.go:840` guards a store `NewPinSectionStore` always returns (the other
  four stores are not guarded).
- Three late-bound `var ... func` locals (`sshStateInvalidatedNavigation`, `hostAttachedWakeup`, `hostManageEvents`,
  `main.go:568,573,578`) exist only because the SSH manager is built before the WebServer; each is nil-checked in the
  event callback.
- The `hubcore.WebConfig` literal (`main.go:671-760`) copies 17 `cfg.Host*` fields one by one.
- Deferred teardown order is load-bearing and expressed only by the order of `defer` statements scattered across 600
  lines (`main.go:337,346,641,790,802,860`).

Impact: any startup change needs a full read of the function; the dead SetOnChange shows the wiring has already drifted.

Fix plan (behavior-preserving; existing `main_test.go` 835 lines + `cov_main_*` guard it):
1. Delete `main.go:470-471` and the `pinSections != nil` check.
2. Add `type hubRuntime struct{...}` with phase methods in the same file: `openStores`, `newSSHManager`, `webConfig()`,
   `wireInvalidation(web)`, `startBackground(ctx, web)`, `publishEndpoint()`. Each phase returns a cleanup that
   `runMain` defers in one visible list.
3. Replace the six closures with `onInputsChanged := func(h navigationChangeHint){ bump(); web.navigation.Invalidate(h) }`.
4. Replace the 44 inline Fprintf with one `logf` closure (also fixes finding 21).
5. Construct the SSH `OnEvent` after the WebServer using a small struct holding `*WebServer` so the three late-bound
   closures become field reads.
Docs: none (internal). Size: net -120 lines, ~700 moved. Risk: medium (shutdown ordering); keep the existing
shutdown-order tests untouched and run them in CI.

---

### hub-root-rest-2: hub.toml is both the operator's config and a machine-managed database (severity: high; architecture, needs Jesse's decision)

Locations: `config.go:62-130` (ten reserved sections), `config.go:449-592` (`decodeConfig`, 143 lines of `validate*` calls plus the
reserved-record refusal loop at `:580`), `config.go:76-83`, `app_host_manage.go:727-755` (`writeHubTOMLHosts*`).

Evidence:
- `Config` carries ten machine-managed maps beside the operator's `[[hosts]]`: `host_records`, `generations`,
  `mutation_receipts`, `tombstones`, `pruned_receipts`, `staged_receipts`, `teardown_remnants`, `teardown_attempts`,
  `pending_store_sync`, `mirror_commits`. `decodeConfig` runs a dedicated validator for each and then refuses any
  undecoded key under those prefixes.
- The comments describe a two-file two-phase commit: `PendingStoreSync` is "the exact store rows a commit's swap is
  deleting... The mutation's atomic write carries it, the store's purge applies it after the swap, and a follow-up
  atomic write clears it" (`config.go:117-123`), `MirrorCommits` is the pair that reconciles hub.toml generation against
  the `hostops` store mirror at boot (`config.go:124-128`). That protocol exists only because one logical record set is
  split across `hub.toml` and `hostops.Store` (9.8k lines, already durable, already quarantine-aware).
- The host-management code that lives on top is 14,242 non-test lines (`app_host_*.go`) with 32,682 lines of tests.
  `hub-app-host-1/2/6` already document the symptoms (nine near-identical preserve functions, ~38 sites per record kind,
  legacy sidecar migration).
- The operator-facing consequence: a hand-edited `hub.toml` is rewritten in place by the hub with a banner, so the file
  the user owns is also state the hub owns.

Impact: the largest single maintenance drag in the hub. Every record kind costs struct + validator + preserve function +
pruning policy + migration.

Fix plan (this is a consequential design choice; discuss before any code):
1. Decide the end state with Jesse: operator-owned `hub.toml` holds only `[[hosts]]` plus scalar config; every
   machine record lives in the `hostops` store (or one new `hostrecords` SQLite/JSON file under `hub_state_root`).
2. Migration needs a one-shot reader for existing files that already carry these sections. That is backward-compat
   code and needs Jesse's explicit approval per his rules; alternative is to let a hub with such sections refuse to
   rewrite and tell the operator, which conflicts with recovery-over-lockdown. Raise it, do not choose.
3. After the move, delete `PendingStoreSync`, `MirrorCommits`, the reserved-key loop and ten validators, and the
   `writeHubTOMLHosts*` rewrite machinery shrinks to rewriting `[[hosts]]` only.
Docs: `docs/product/subsystems.md` S07 host rows, `docs/evener-hub.md`. Size: -1,500 to -2,500 lines across
`config.go` and `app_host_*` (the other audit owns the host-side half). Risk: high (data migration, crash recovery);
needs the restart-recovery tests (`app_host_*_recovery_test.go`, 2,348 lines) kept green throughout.

---

### hub-root-rest-3: 30 undocumented host retention knobs plumbed through 6 layers each (severity: medium)

Locations: `config.go:154-239` (fields), `config.go:243-326` (23 `Default*` constants), `config.go:335-370`
(`DefaultConfig`), `config.go:698-787` (`applyConfigDefaults`), `main.go:671-760` (`WebConfig` copy),
`internal/hubcore/config.go:200-240` (WebConfig fields), `main.go:1098-1110` (`hostOperationRetention`),
`app_host_record_pruning.go:109-133` (second default fallback).

Evidence:
- `HostTombstoneMaxRowBytes` alone appears in 8 places (`config.go:184,262,347,723-724`; `main.go:729`;
  `internal/hubcore/config.go:210`; `app_host_record_pruning.go:109,133`). `app_host_record_pruning.go:133` re-applies
  the default that `applyConfigDefaults` already applied.
- `docs/evener-hub.md:280-294` documents 6 of the ~34 host knobs (`host_probe_timeout`, `host_min_free_space_bytes`,
  `host_tombstone_retention` among them). Receipt/remnant/operation/audit/tombstone-count caps are described only as
  "documented beside the host registry in the subsystem map"; `grep host_keyless docs/` finds nothing.
- The same doc admits the result: "The loader does not validate every duration field the same way, so assume a typo
  can take effect" (`docs/evener-hub.md:297-304`). Only five of the duration knobs get the bare-integer refusal loop
  (`config.go:496-506`).
- The knobs encode spec bounds ("64 tombstones and 16 MiB", "500 rows") that spec 08 §15 names as defaults, not as
  operator tuning needs. YAGNI.

Impact: ~110 lines of knob plumbing, 23 constants, inconsistent validation, and a test matrix for values nobody sets.

Fix plan: keep `host_probe_timeout`, `host_min_free_space_bytes`, `host_tombstone_retention` (documented, operationally
meaningful). Turn the other ~30 into one unexported `hostRetentionDefaults` value in `app_host_record_pruning.go` that
`hostOperationRetention` and the pruning policy both read. Delete the config fields, `Default*` constants (except the
three kept), `applyConfigDefaults` branches, the `WebConfig` fields and the `main.go` copy lines. `toml.Decode` is not
strict (`decodeConfig` only refuses reserved prefixes), so an old file that still names a removed key keeps loading.
Needs Jesse's decision only if he wants those keys to stay tunable. Tests: existing retention tests set the
`WebConfig` fields; point them at the single policy value. Docs: `docs/evener-hub.md` already omits them.
Size: -450 lines. Risk: low.

---

### hub-root-rest-4: Config defaults are written 3-5 times, two config keys are dead, and Settings shows a hardcoded "30s" spawn timeout (severity: medium)

Locations: `config.go:65,336-342,703-711` (`StatusPollInterval`, `PastIndexRebuild`, `SpawnTimeout`, `PastResultsPerPage`);
`spawn.go:167-170,226-229`; `web_settings.go:20-24`; `app_rpc_settings_overview.go:56`; `config.go:20-25,69`;
`docs/evener-hub.md:293`.

Evidence:
- `SpawnTimeout` defaults to 30s in `DefaultConfig()` (`config.go:338`), again in `applyConfigDefaults`
  (`config.go:705-706`), and again in `HubSpawner.Spawn` and `.Resume` (`spawn.go:167-170`, `226-229`:
  `if timeout == 0 { timeout = 30 * time.Second }`). `decodeConfig` starts from `DefaultConfig()` so the middle copy only
  matters for an explicit zero.
- Real bug: `web_settings.go:20-24` says "there is no configurable spawn timeout today" and hardcodes
  `settingsSpawnTimeoutDisplay = "30s"`, but `Config.SpawnTimeout` is a `spawn_timeout` hub.toml key that `HubSpawner`
  uses. An operator who sets `spawn_timeout = "90s"` sees "30s" on Settings -> Hub.
- `StatusPollInterval` (`status_poll_interval`) is never read by production code; only `config.go:65,336` and tests
  mention it, yet `docs/evener-hub.md:293` documents it as "How often the hub polls session state".
- `Config.Providers []ProviderConfig` (`providers`, `config.go:20-25,69`) is never read outside tests
  (`cov_main_bootstrap_pass6_fuzz_test.go:39`); provider config moved to `providers.toml`.
- `const Version = "0.1.0"` (`main.go:50`) is printed in the startup banner (`main.go:1009`) while `buildinfo.Version()`
  is what `/api/health`, `--version` and the dirty-build check use.

Fix plan:
1. Red test first: build a `Config` with `SpawnTimeout = 90s`, call the settings overview, assert the reported value is
   the configured duration (assert on the structured value, not copy). Then make `hubSettingsOverview` read
   `cfg.SpawnTimeout` (pass it through `WebConfig` or read `Spawner`), delete `settingsSpawnTimeoutDisplay`.
2. Delete `HubSpawner`'s two zero-timeout fallbacks (config guarantees non-zero) or keep exactly one in a
   `spawnTimeout()` method.
3. Delete `StatusPollInterval`, `Providers`, `ProviderConfig` and their `docs/evener-hub.md` row; delete `Version` and
   print `buildinfo.Version()` in the banner.
4. Collapse `applyConfigDefaults` to the cases where an explicit zero/negative is meaningful (`DaemonIdleTimeout` is
   deliberately excluded already).
Size: -90 lines. Risk: low. Docs: `docs/evener-hub.md`.

---

### hub-root-rest-5: Memoized tree path and its remote-fetch fallback are production-dead, kept alive by tests (severity: medium)

Locations: `web_api_tree.go:220-256` (`memoTree`, `memoTreeWithAuthority`), `web.go:54-56,82-85,195`
(`remoteFetchGeneration`, `treeCache`), `web_api_tree.go:624-640` (`remoteThreadFetch` synchronous fallback),
`web_api_tree.go:667-682` (`remoteTreeThreads`, `refreshRemoteThreads`), `web_api_tree.go:856-860`
(`listThreadsWithFallback`), `web_api_tree.go:613-616` (`navigationTreeInputs`), `web_api_tree.go:1514`
(`rowRenameable`), `web_api_tree.go:1523` (`liveEntry`), `web_api_tree.go:40` (`hubTreeAttentionRank`),
`internal/hubcore/treecache.go`.

Evidence (name-based production-reference scan; zero non-test references):
- `memoTree`, `remoteTreeThreads`, `refreshRemoteThreads`, `listThreadsWithFallback`, `navigationTreeInputs`,
  `rowRenameable`, `liveEntry` all have no non-test caller. `memoTree` is the sole caller of `memoTreeWithAuthority`,
  the sole user of `s.treeCache` (`hubcore.TreeCache`, imported in no other production file) and of
  `remoteFetchGeneration`.
- `navigation_service.go:1628` itself calls it "the legacy 30-second TreeCache".
- `remoteThreadFetch` (`web_api_tree.go:624-640`) falls back to a synchronous network walk "for tests that construct a
  WebServer without a cache" (comment at `web_api_tree.go:620-623`); `main.go:507,707` always sets `RemoteThreadCache`.
- `hubTreeAttentionRank = hubapi.AttentionRank` plus `var _ = hubTreeAttentionRank` (`web_api_tree.go:34-40`) is a
  placeholder; the only other use overrides it to a stub in `cov_exact_lifecycle_tree_fuzz_test.go:135`.
- Tests that touch them: `cov_exact_tails_fuzz_test.go`, `cov_web_tree_session_fuzz_test.go`,
  `cov_session_residue_pass5_fuzz_test.go`, `internal/hubcore/treecache_test.go`.

Impact: ~260 lines of production code plus a synchronous-network-walk path that no real deployment can reach; two
sources of truth for "the tree" in a codebase whose navigation is supposed to be owned by `NavigationService`.

Fix plan: delete the listed functions, `treeCache`, `remoteFetchGeneration`, `hubTreeAttentionRank`,
`hubcore.TreeCache` and its test. Make `RemoteThreadCache` mandatory in `newWebServer` (create one when `nil`, as it
already does for `ResumeLocks`/`PluginManager`) so tests exercise the real cache path. Repoint the four cov tests at
`navigationSnapshot`. Docs: `docs/product/subsystems.md` row for the tree read path if it mentions the memo.
Size: -260 prod, -150 test. Risk: low. Do this first; it shrinks later findings.

---

### hub-root-rest-6: `Spawn`/`Resume` and the two launch-model listers repeat ~40 lines each; three optional-interface tiers serve one implementation (severity: medium)

Locations: `spawn.go:166-220` vs `spawn.go:223-301`; `spawn.go:103-125` vs `spawn.go:127-163`;
`spawn.go:83-93`; `app_models.go:417-470`; `spawn.go:435-445,556-611` (wrappers).

Evidence:
- `Spawn` and `Resume` share, line for line: timeout default, state-dir resolution (`if req.Project.ID != ""`),
  `prepareResolvedForSpawn`, the idle-timeout assignment (the same 4-line comment appears twice), `applyHubAPILogDefault`,
  `RunDir`, `AppReplaySize`, `prepareChildProviderConfig`, `cleanupUnowned`, the 9-field `launchconfig.EnvInputs` literal.
  Only the credential gate (`Provider != ""` and model-blind contract check) and tracing differ.
- `ListLaunchModelContract` and `ListLaunchModelContractForWorkingDir` repeat the child-config and 9-field
  `EnvInputs` build; the second differs only by the "directory does not exist yet" fallback.
- `EvenerLaunchModelLister`, `...ContractLister`, `...ContractWorkingDirLister` (`spawn.go:83-93`) are three
  progressively richer optional interfaces. `selectEvenerLaunchModelListers` (`app_models.go:453-470`) type-asserts all
  three and `evenerLaunchModelList` (`app_models.go:417-440`) walks a priority cascade down to a "legacy" tier. The only
  production implementer, `HubSpawner`, implements all three; the lower tiers exist for test fakes.
- Wrapper chains: `SpawnDaemon` -> `spawnDaemon` -> `spawnDaemonWithProviderConfig`, `ResumeDaemon` -> `resumeDaemon` ->
  `resumeDaemonWithProviderConfig`. `SpawnDaemon` and `ResumeDaemon` have no production caller (tests only).

Fix plan:
1. Extract `func (h *HubSpawner) prepareLaunch(req *launchRequestCommon) (cleanup func(), childConfig *childProviderConfig, err error)`
   holding the shared 40 lines (a small struct embedding the fields both request types share, or pass pointers to the
   shared fields). Add `h.envInputs(...)` for the `EnvInputs` literal.
2. Make `ListLaunchModelContract(ctx)` call `ListLaunchModelContractForWorkingDir(ctx, "")`.
3. Collapse the three interfaces to the single `ListLaunchModelContractForWorkingDir` method on `hubcore.Spawner` (or one
   small `LaunchModelLister`) and delete the selector cascade; update the 9 test fakes.
4. Delete `SpawnDaemon`, `ResumeDaemon`; rename `spawnDaemonWithProviderConfig` -> `spawnDaemon` taking the hub log and
   provider config as parameters.
Tests: existing `spawn_test.go` (1,661), `resume_lifetime_test.go`, `spawn_daemonlog_test.go`. No behavior change.
Size: -220 lines. Risk: low-medium (resume ownership ordering).

---

### hub-root-rest-7: Launch-check subprocess handling and credential-gate branches are copy-pasted (severity: medium)

Locations: `spawn.go:1152-1189` vs `spawn.go:1191-1247`; `spawn.go:932-960` vs `spawn.go:962-1015`; `app_models.go:450-480`.

Evidence:
- `validateEvenerLaunchContract` and `listEvenerLaunchModelContract` repeat default-binary, timeout, `exec.CommandContext`,
  `runLaunchCheck`, `launchCheckWaitError`, secret redaction + `errLaunchCheckFailed` wrapping, JSON decode and the
  protocol-version check (~25 lines each).
- `listEvenerLaunchModelContract` trims/filters descriptors and diagnostics (`spawn.go:1219-1246`), and its caller
  `evenerLaunchModelList` immediately runs `sanitizeModelListResponse` (`app_models.go:455-465`) which does the same trim
  and drop; the normalization exists twice.
- `validateProviderCredentials` has an instance branch and an implicit-provider branch that run the same
  `resolveGateCredentialForSpawn` call, the same `switch res.Transport.Auth { case AuthNone, AuthOptionalBearer }` and the
  same `res.Credential.Source != "none"` test (`spawn.go:940-958` vs `spawn.go:976-1000`); only the remedy selection
  differs.

Fix plan: add `runLaunchCheckJSON(ctx, binary, env, extraArgs, out any) error` that owns timeout, redaction, decode and
protocol check; both callers become ~8 lines. Drop the trimming in `listEvenerLaunchModelContract` (keep
`sanitizeModelListResponse` as the one normalizer). In `validateProviderCredentials` resolve once, return nil when
satisfied, then choose the remedy by instance vs implicit. Tests: `cov_spawn_credentials_pass4_fuzz_test.go`,
`spawn_test.go` credential cases. Size: -90 lines. Risk: low.

---

### hub-root-rest-8: Doc and image routes resolve paths in three divergent ways; remote image-by-path misses the artifacts root (severity: medium)

Locations: `doc_serve.go:57-110` (`resolveLocalDocTarget`), `doc_serve.go:423-487` (`sessionDocumentFromHub`),
`image_serve.go:281-309` (`sessionImageByPath`), `output_images.go:360-398` (`readOutputImageFile`,
`readOutputImageInRoot`), `image_proxy.go:80-90` vs `doc_proxy.go:52-72`, `doc_serve.go:503-540` (`handleDocImage`).

Evidence:
- The "cwd first, then the retained artifacts directory for an absolute path, honoring the deletion fence and the
  contained-miss rule" decision is implemented twice: once with HTTP statuses (`resolveLocalDocTarget`) and once with
  wire errors (`sessionDocumentFromHub`). Each carries a ~25-line explanation of the same rule in comments.
- Drift: `sessionImageByPath` (the host-side of the proxied `/doc/image`) resolves only against `sessionCWD` with
  `fspaths.ResolveInRoot` (`image_serve.go:281-293`) and never tries the artifacts root. The local `/doc/image` route
  goes through `resolveLocalDocTarget` and does, and the remote document method does too. An absolute artifacts-image
  path therefore serves for a local session and 404s for a remote one (unless that omission is intentional, in which
  case it is undocumented; I found no comment saying so).
- Two image file readers with different guarantees: `readOutputImageFile` (`output_images.go:360-368`) does
  `os.Stat` then `os.ReadFile` on the path; `readOutputImageInRoot` opens through the root so a symlink swapped after
  the containment check cannot escape. `resolveOutputImageFile` (thumbnail metadata) uses the weaker one.
- `sessionImageProxyStatus` (`image_proxy.go:80`) and `sessionDocumentProxyStatus` (`doc_proxy.go:52`) are two wire-error
  -> HTTP-status tables over the same `statusForWireError` base.
- `imageShaRegexp` (named for images) validates document revisions in `proxyableSessionDocument` (`doc_proxy.go:88`).

Fix plan (red-test-first for the drift):
1. Write a test that serves an absolute artifacts image over `evener/session/image` by path and expect success (today
   fails). Fix by extracting `resolveSessionFile(ctx, cfg, sources, session, rel) (root, abs string, err error)` returning
   typed errors (`errPathEscapes`, `errNotFound`, deletion error); `resolveLocalDocTarget`, `sessionDocumentFromHub` and
   `sessionImageByPath` become thin mappers from that error to status/wire error.
2. Delete `readOutputImageFile`; have `resolveOutputImageFile` use `readOutputImageInRoot`.
3. Merge the two proxy-status functions into one table with an `allowNotImplemented` flag, rename the sha regexp
   `sha256HexPattern`.
Docs: none, unless the artifacts-root behavior for remote images is declared intentional. Size: -140 lines.
Risk: medium (security-relevant containment code; keep all existing escape tests).

---

### hub-root-rest-9: Navigation layer works around an upstream grouping defect and re-validates its own output (severity: medium)

Locations: `navigation_projection.go:315-630` (`navigationMergeProject*`, ~280 lines),
`navigation_delta.go:15-82`, `navigation_history.go:36-45`, `navigation_normalize.go:159`,
`navigation_schema.go:78-190`, `navigation_projection.go:688-800`, `navigation_service.go:1183-1185`,
`navigation_projection.go:155-161`, `navigation_projection.go:277`, `navigation_delta.go:84-120`.

Evidence:
- `navigationMergeProjectBucketsContext` exists because "the tree groups sessions by canonical project identity, but its
  Key is the canonical identifier.Project.ID only when the working directory resolved; sessions that resolve to no
  project all present the shared 'no-project' key while keeping their own grouping path" (doc comment,
  `navigation_projection.go:335-360`). `internal/hubcore/tree.go:213,910,1013` is where that Key is minted. The
  projector merges duplicate Keys after the fact (five functions, 280 lines) instead of the tree producing one group per
  wire Key.
- A snapshot built by the hub is validated by `validateNavigationResourceSnapshot` (which itself calls
  `hubapi.NavigationSnapshot.Validate`, `navigation_schema.go:85`) at: `finish()` (`navigation_normalize.go:159`),
  `diffNavigationSnapshots` base and current (`navigation_delta.go:16,19`), after applying the delta
  (`navigation_delta.go:73`), plus `delta.Validate` (line 66) and a full reconstruction equality check
  (`applyNavigationDelta` + `navigationSnapshotsEqual`), and `Remember` (`navigation_history.go:42`). A delta read does
  4 snapshot validations, 1 delta validation and 1 apply. `applyNavigationDelta` returns an `error` that is always nil
  (`navigation_delta.go:84-120`).
- Plus `validateNavigationInputsContext` / `validateNavigationNodesContext` / `validateNavigationPageProgress` on the
  projection side. Self-checks on trusted in-process data.
- `X`/`XContext` pairs: `buildNavigationProjection` (53 test callers, no production caller) wraps
  `buildNavigationProjectionContext`; `cloneNavigationInputs` wraps its `Context` form (test-only);
  `navigationLogicalFingerprintsContext` is a pure pass-through of `navigationLogicalFingerprintsWithContext`
  (`navigation_service.go:1183-1185`). 19 `...Context` functions and 68 `ctx.Err()` checks across the two big files.
- `readV3`/`navigation_v3_budget.go`/`navigationV3ResponseLimit` (26 "v3" mentions) with no v1 or v2 in the repo.
- `validNavigationTimestamp` (`navigation_projection.go:1804`) and `cloneNavigationSnapshot`
  (`navigation_normalize.go:166`) have no production caller.

Fix plan:
1. Fix grouping at the source: make `hubcore.BuildTreeWithProjects` emit one `TreeProject` per wire Key (merge no-project
   groups inside the tree), with a hubcore test carrying the "one attached host with unresolvable directories" scenario
   from the comment. Then delete `navigationMergeProject*` (about -280 lines) and the duplicate-key `Validate` special-casing.
2. Validate once where the snapshot is produced (`finish`) and once where an externally supplied one enters (history
   `Remember` only if it can receive foreign data; it cannot). Remove validation from `diffNavigationSnapshots`/history,
   keep the reconstruction equality check as a test-only assertion.
3. Make every function that can be long-running take `ctx` and delete the non-`Context` twins; rename to drop the
   `Context` suffix. Rename `V3` identifiers to plain names.
Docs: none. Size: -450 lines. Risk: medium (hubcore tree ordering is relied on by the sidebar); guard with the existing
`navigation_projection_test.go` (1,287) and `navigation_service_test.go` (2,964).

---

### hub-root-rest-10: Should the 56k-line main package be split? Yes, along three seams, after deleting dead code (severity: high; architecture)

Locations: whole `cmd/evener-hub` root; seams below.

Evidence for splitting:
- 56,197 non-test lines and 192,767 test lines compile into one test binary; any edit to any of 463 test files rebuilds
  and relinks all of it, and every `_test.go` can reach every unexported symbol, which is how the `cov_*` helpers got
  so entangled (finding 11).
- The package already depends on 14 `internal/` packages, so the repo knows how to split; the root is the part that
  never was.
- Dependency scan (go/types, undefined-identifier scan of a file group compiled alone):

| Group | Non-test lines | Test lines | Identifiers it needs from the rest of the root |
| --- | --- | --- | --- |
| `navigation_*.go` (10 files) | 5,400 | 10,086 | 17: `WebServer` (2 adapter methods, `navigation_service.go:1592,1630`), tree/pin/favorite helpers in `web_api_tree.go` (`classifySessionPins`, `canonicalPinAssignments`, `dropSubagentPins`, `navigationBuildInputsFromTreeSnapshot`, `projectFavorite*`, `referencedSessionIDs`, `navigationProjectBucket*`), `isLocalRouteID`, `prettifyModelDisplayName`, `parseNavigationCatalog` |
| `app_host_*.go` (16 files) | 14,242 | 32,682 | 38, almost all `Config`/`HostConfig`, 22 `DefaultHost*` constants, `decodeConfig`/`configReadFile`, `hostRegistryEntries`, `guardControllerLocalHosts`/`wireRefusal`, `NavigationService` and `navigationChangeHint` |
| `config.go` + `endpoint_config.go` | 934 | ~1,900 | none of `WebServer` |
| `spawn.go` + `child_provider_config.go` + `spawn_daemonlog.go` + `thread_lifecycle_logging.go` | ~1,900 | ~3,700 | 5: `Config`, `wireRefusal`, `withThreadLifecycleLog`, `childProviderConfig` family |
| `project_delete.go` | 811 | 2,051 | 17, nearly all `WebServer` methods (not separable without `WebServer` surgery) |

- Outside the navigation group only 15 non-navigation files mention navigation identifiers (`app_navigation.go` 100
  references, `web_api_tree.go` 37, `main.go` 16, `app_rpc.go` 12).

Evidence against a big-bang split: `WebServer` (84 methods in 18 files) and `hubcore.WebConfig` (84 exported fields, 18 of
them funcs, 11 test hooks via `RelayLifecycleHooks`) are the hub for the `app_*` handlers. Splitting the handlers
requires dismantling that object, which is a redesign, not a move.

Fix plan (each step is a pure move guarded by the existing tests, one PR each):
1. Prerequisite: findings 5, 16 (delete test-only code) and 9.1 (so fewer files cross the line).
2. `internal/hubconfig`: `Config`, `HostConfig`, `DefaultConfig`, `LoadConfig*`, `EndpointConfig`, resolve functions.
   Export the 20 symbols `app_host_*` and `main.go` use. Pure types plus TOML, no dependency on `WebServer`. ~930 lines.
3. `internal/navigation`: `navigation_*.go` with a `Source` interface (already exists: `navigationSource`) and the 17
   helpers moved with it; `webNavigationSource` and `navigationSubagentParent` stay in the root as the adapter. ~5.4k
   source + 10k tests move.
4. `internal/hostmanage` (after finding 2 decides what stays in hub.toml): `app_host_*` once it depends on `hubconfig`
   rather than `Config`. 14k + 32k move.
5. Leave `app_*` handlers, `WebServer` and `main.go` in `package hub` (about 30k lines, the composition layer).
Docs: `docs/architecture.md` (module table at line 44 and the `web.go` link at line 53), `docs/product/subsystems.md` S06/S07
source links, `docs/developing-evener/testing.md` if package-specific `-run` names change (`cmd/evener-dev/webbrowser.go`
runs `go test ./cmd/evener-hub -run ...` by name; those argv lists need the new package path where tests move).
Size: ~21k source and ~43k test lines moved, ~300 lines of export/adapters added. Risk: medium; mostly mechanical, but
`cmd/evener-dev/webbrowser.go:66-117` and `cmd/evener-fuzzcov` reference the package path.

---

### hub-root-rest-11: 40 `cov_*_fuzz_test.go` files (6,031 lines) chase coverage through the production API (severity: high; test-debt)

Locations: `cmd/evener-hub/cov_*_test.go` (40 files), `web_covtest_test.go` (608), `*_coverage_test.go`,
`cov_small_faults_pass5_fuzz_test.go:50-57,158-215`.

Evidence:
- File names carry history, not intent: `cov_*_pass3/4/5/6`, `cov_exact_tails`, `cov_final_smalltails`,
  `cov_small_tails_pass6`, `cov_*_residue_*`. 46 `Fuzz*` functions, most documented as "closes residual ... branches"
  (e.g. `cov_session_tree_pass6_fuzz_test.go:13`).
- `web_covtest_test.go:20-60` names its tests "covers the path where ResumeLocks is already initialized ... (web.go:96)":
  the test oracle is a line number.
- Handlers are called directly with synthetic requests purely to execute lines: `web.handleThreadDocument(...)`,
  `web.handleIndex(...)`, `web.handleSettings(...)`, `web.handleCredentials(...)` appear only in `cov_*` files
  (`cov_provider_render_pass4_fuzz_test.go:63-67`, `cov_web_api_residue_pass5_fuzz_test.go:26-28`). Their bodies are one
  line (`serveSPAIndex(w, r, distFS())`).
- `cov_small_faults_pass5_fuzz_test.go:50-57,158-215` swaps nine package-level seams (`outputImageMarshal`,
  `outputImageStat`, `outputImageRel`, `hubTokenRead`, ...) to force error returns that cannot occur in production
  (`json.Marshal` of a map; `rand.Read`).
- AGENTS.md's testing rules require behavior over coverage and "no test may check ... copy"; these files assert neither.

Impact: 6k lines of tests that fail when code is refactored but protect no stated behavior, and the reason for the
seam variables in finding 12. Changing the production code under them (every finding here) means editing them.

Fix plan:
1. For each cov file, list which production lines only it executes (use the existing `make coverage` tooling on a branch;
   do not run locally on the shared box, per standing rules; use CI).
2. Where the executed code is real behavior, replace with a behavior test at the nearest boundary; where it is an
   unreachable error branch, delete the branch and its seam.
3. Delete the rest. Reducing coverage numbers is a policy call: coverage is worse than failing tests in Jesse's rules, so
   get his approval for deleting tests whose only claim is a line number. Raise it, do not decide.
Docs: `docs/developing-evener/coverage.md` if it names these files. Size: -6,000 test lines, ~-150 prod lines of
seams. Risk: low per file, but needs the approval above.

---

### hub-root-rest-12: ~44 package-level mutable seams exist to inject faults that cannot happen (severity: medium)

Locations (non-exhaustive, all in the slice): `output_images.go:16-23` (6), `main_background.go:14-35` (6),
`navigation_service.go:40-60` (8), `spawn.go:45-48,65,528` (6), `project_delete.go:60-82` (4), `main.go:62-78` (5),
`config.go:18-19`, `doc_serve.go:30-31`, `http_recorder.go:21-22`, `web.go:95`, `token.go:9`, `navigation_projection.go:961`,
`webnext.go:14`, `web_api_tree.go:34-38` (4).

Evidence:
- `var outputImageMarshal = json.Marshal`, `manifestMarshal = json.Marshal`, `navigationEnvelopeMarshal = json.Marshal`,
  `httpRecorderMarshal = json.Marshal`, `hubTokenRead = rand.Read`, `outputImageRel = filepath.Rel`,
  `spawnMkdirAll = os.MkdirAll`: each is replaced only in `cov_*` tests to force a branch (`cov_small_faults_pass5_fuzz_test.go`).
  `docOpen`/`docStat` comment: "Both are variables so coverage tests can fail them" (`doc_serve.go:28-31`).
- `main_background.go:14-35`: `hubSeedDefaults`, `hubPluginGC`, `hubStartUpgradeDaemon`, `hubRosterWatch` are lambdas that
  call the one method they name; `hubStartUpgrade` forwards to `hubStartUpgradeDaemon`.
- `navigation_service.go:40-60`: seven empty-function hooks ("Production leaves it nil / a no-op") for ordering tests.
- Global mutable seams block `t.Parallel()` and make a forgotten restore a cross-test leak (the cov tests save and
  restore nine at a time).
- `app_*` files carry 13 more of the same shape; `hub-app-rest-8` covers them.

Fix plan: three classes. (a) Seams on infallible calls (Marshal of fixed types, `rand.Read`, `Rel` on resolved paths):
delete the seam and the unreachable error branch together (the branch then needs no test). (b) Seams on real
boundaries (`docOpen`, exec factories, `distFS`): keep, but move into a small struct field on `WebServer`/`HubSpawner`
so tests set per-instance state. (c) Concurrency-ordering hooks (`navigation_service.go`): move into
`navigationServiceConfig` (already holds `NewTimer`, `Now`) instead of globals. Do it with finding 11. Size: -200 lines.
Risk: low-medium.

---

### hub-root-rest-13: Tests assert on copy; five deploy remedy strings are tested by identity (severity: medium; test-debt)

Locations: `deploy_flags_test.go:28,46,126,144,149,201,241-244,488-497,528-532,558-581,617,850-851,965,1005,1044,1175`;
`deploy_flags.go:74-95,186-195`; repo-wide 96 `strings.Contains(err.Error(), ...)` in `cmd/evener-hub/*_test.go`.

Evidence:
- `docs/developing-evener/testing.md` "Copy Is Not a Test Oracle" forbids exactly this: `help != hubDeployHelpDefaultedDirtySource`
  (`deploy_flags_test.go:488`), `strings.Contains(help, "without "+noDeployFlag)` (`:561`), `strings.Contains(err.Error(), "execut")` (`:244`).
- `deploy_flags_test.go` has 23 of its 25 `Contains` calls on error/help text; `main_test.go` 12 of 16.
- Product side: there are five remedy constants (`hubDeployHelp`, `...Unwired`, `...NoSource`, `...DefaultedSource`,
  `...DefaultedDirtySource`, 400+ chars each) selected by a 4-way state machine (`deployHelp`, `deploy_flags.go:212-232`),
  `deployDefaultFailure` enum, `deployDefault` flag, `defaultFailure` field. The comments are 60% of the file.

Fix plan:
1. Replace string assertions with the typed state: have `deployHelp()` return a `deployState` enum (or the existing
   `deployDefaultFailure` plus two bools) and assert it; render text in one place. Use `errors.Is(err, errArtifactNotEvener)`
   and flag-name fields on a typed `DeployFlagError{Flag string}` for the "names the flag" assertions.
2. Collapse the five strings into one template with the clause list chosen by state, ~25 lines.
3. Sweep the other 96 `err.Error()` Contains sites as each file is touched (sentinel + `errors.Is`).
Size: -350 lines (deploy_flags 515 -> ~250, tests -250). Risk: low.

---

### hub-root-rest-14: Five CLI subcommands re-implement flag parsing, config selection, and token reading (severity: medium)

Locations: `main.go:1176-1225` (`parseHubOptions`), `attach.go:63-97`, `endpoint.go:29-60,64-82`, `login_url.go:26-47`,
`restart.go:27-52`, `attach.go:264-284` (`readAuthToken`), `login_url.go:50`, `main.go:253-266`, `endpoint.go:64-82`.

Evidence:
- `attachOptions`, `endpointOptions`, `restartOptions`, `hubOptions` each re-declare `configPath`, `configExplicit`
  (and `addr`/`addrPresent`/`urlPresent`), and each runs the same `fs.Visit(func(f){ switch f.Name { case "config": configExplicit = true ...` block
  (5 copies: `main.go:1205`, `attach.go:82`, `endpoint.go:42`, `login_url.go:36`, `restart.go:45`).
- `login_url.go:50-54` reads the token file with its own `os.ReadFile` + `TrimSpace`; `readAuthToken` (`attach.go:270`) is
  the canonical helper with the same logic and better errors, used by `attach` and `restart`. A third reader is
  `hubedge.LoadOrCreateAuthToken`.
- `restart.go:49-52`, `attach.go:150`, `restart.go:60-62`: every command also repeats `stdout := deps.stdout; if stdout == nil { stdout = os.Stdout }`
  (`attach.go:147-154`, `restart.go:115-118`), while `mainDeps` defaults already fill them (`defaultMainDeps`, `main.go:217-236`).
- `resolveHubEndpoint` (`endpoint.go:64-82`) branches on `deps.loadConfig == nil` to choose between two ways to load and
  resolve config that `main.go:290-297` also inlines a third time.
- `runMain` dispatches subcommands with four `if len(args) > 0 && args[0] == ...` blocks (`main.go:263-272`) while the
  usage text lists them by hand (`main.go:1190-1195`).
- `--version` is detected by scanning all args (`main.go:254-258`), so `evener hub endpoint --json --version` prints
  version info.

Fix plan: one `commonFlags` struct with `bind(fs)` (config path + explicit tracking via a custom `flag.Value` that sets
a bool on `Set`, which removes every `fs.Visit`) embedded in all option structs. One `subcommands` table
(`name -> func`) used for dispatch and usage. Make `login_url` call `readAuthToken`. Resolve `deps.stdout/stdin` once in
`Run`. Collapse `resolveHubEndpoint`'s two load paths by making `deps.loadConfig` always non-nil
(`defaultMainDeps` already sets it). Tests: `endpoint_test.go` (604), `restart_test.go` (1,042), `attach_*_test.go`.
Size: -170 lines. Risk: low.

---

### hub-root-rest-15: Four hand-rolled "run once, then tick or poke" loops (severity: low)

Locations: `main_background.go:49-90` (`watchHubAttention`), `main_background.go:97-120` (`refreshHubMessageSearch`),
`main_background.go:122-135` (`watchHubNotices`), `main_background.go:171-206` (`refreshHubRemoteThreads`),
`main.go:893-905` (inline past-rebuild ticker), `scratch_reconcile.go:62-88`, `main.go:487-502` (poke channels).

Evidence: each repeats `if ctx.Err() != nil { return }`, `ticks, stop := hubTicker(...)`, `defer stop()`, `run := func(){...}`,
`run()`, and a `for { select { ctx.Done / ticks / poke } }`. The poke channel pattern (`select { case ch <- struct{}{}: default: }`)
appears in `main.go:494,498,615`, `scratch_reconcile.go:73`, `navigation_service.go:304,958`, `app_host_admin.go:600`.

Fix plan: `runPeriodic(ctx, interval, poke <-chan struct{}, fn func(context.Context))` and a tiny `type kicker chan struct{}`
with `Kick()`; each watcher shrinks to its body. Collapses `hubTicker` and the three function-valued seams in
`main_background.go` (finding 12). Size: -90 lines. Risk: low.

---

### hub-root-rest-16: Pass-through wrappers, dead helpers and an error that is always nil (severity: low)

Locations and evidence (name-based scan found no non-test reference unless noted):
- `spawn.go:1072` `envLookup` (no production caller, 9 test references); `spawn.go:815-817` `resolveEvenerStateDir` and
  `:852-855` `resolveEvenerStateDirWithStateHome` (production only reaches the `...WithProject` form; the 6-function
  family at `spawn.go:815-883` collapses to `resolveStateDir(workDir, override, stateHome) (Project, string, error)` and
  `resolveStateDirForProject`).
- `web.go:138` `NewWebServer` (285 test uses, 0 production; wraps `newWebServer(cfg, nil)`).
- `web.go:440` `splitProviderModel` (5 test refs only); `web_api.go:23` `writeAPIWireError` (4 test refs only).
- `host_routing_origin.go` `withHostRoutingOrigin` / `hostRoutingOrigin`: one-line forwards to `appsource.WithHostRoutingOrigin`
  / `HostRoutingOrigin` (`:19-25`).
- `openai_state_dir.go`: two layers forwarding to `cmdutil.StateRootFromLookup`; named "OpenAI" but resolves the general
  state root; its own comment narrates that it "used to resolve the chain itself and had drifted".
- `image_serve.go:83-85` `findImageInTranscript` forwards to `scanTranscriptForImage(..., 0)`; `:199` `var imageSha = events.ImageSHA`.
- `output_images.go:326` `supportedOutputImageMedia(data []byte, _ string)`: second parameter unused; callers pass
  `""` or `filepath.Base(abs)` (`image_proxy.go:58`, `image_serve.go:263,297`, `doc_serve.go:529`, `output_images.go:329`).
- `navigation_delta.go:84` `applyNavigationDelta` returns `(snapshot, error)` and only ever returns nil.
- `main_background.go:36-38` `refreshPastOnStatus` wraps a one-line closure; `transcript_limits.go` is a 3-line file
  holding one constant used in two files.

Fix plan: delete the zero-production-caller functions and repoint tests; inline the forwards; drop the `_ string`
parameter; make `applyNavigationDelta` return only the snapshot. Size: -130 lines. Risk: low.

---

### hub-root-rest-17: Two `Ref` types and a dozen route-id helpers for the same identifier (severity: medium)

Locations: `hubapi/refs.go:13-30` (`Ref{HostID, SessionID}`) vs `appwire/refs.go:22-32` (`Ref{SourceID, ThreadID}`);
helpers `web.go:411-440` (`localAppRef`, `appRefFromRouteID`, `isLocalRouteID`, `canonicalRouteID`),
`image_proxy.go:67` (`hostQualifiedRouteRef`), `web_api_tree.go:1287,1299,1377,1516`
(`canonicalSessionDecisionID`, `favoriteSessionAliases`, `favoriteRemoteThreadRef`, `hubRefFromTreeNodeID`),
`navigation_projection.go:779-802` (`navigationRef`, `navigationNodeRef`), `app_pin_section.go:189`.

Evidence: `web_api_tree.go` mixes `hubapi.ParseRef` (`ref.HostID == "local"`, `canonicalSessionDecisionID`) and
`appwire.ParseRef` (`ref.SourceID != ref.SourceID`, `favoriteSessionSourceQuality`) within 100 lines; the same "local:"
prefix rule is spelled in `favoriteSessionAliases` (`"local:"+id`), `localAppRef`, `canonicalRouteID`, `isLocalRouteID`
and `hubRefFromTreeNodeID`. 54 `appwire.ParseRef` and 12 `hubapi.ParseRef` calls in production root files. A
`"local"` source-id literal is the discriminator everywhere.

Fix plan (AGENTS: shared client owns shared derivations): converge on one type in `appwire` (hubapi is the public
HTTP/REST contract mirror; keep `hubapi.Ref` only if the generated TS types need it) and add `Ref.IsLocal()`,
`appwire.LocalRef(id)`, `Ref.Canonical()` plus a single `RouteID` helper family in `appwire/refs.go`. Replace the root
helpers. Tests: `hubapi/refs_test.go`, `refs_fuzz_test.go` migrate with it. Needs a check that the TS client's generated
`Ref` shape is unaffected. Size: -120 lines, wide touch (~25 files). Risk: medium.

---

### hub-root-rest-18: Redundant SPA shell handlers and misnamed small files (severity: low)

Locations: `web.go:297-348` (route table), `web.go:376-379` (`handleIndex`), `web_settings.go:26-29`,
`web_launchconfig.go:7-10` (`handleCredentials`), `web_workspace.go:36-38` (`handleThreadDocument`), `webnext.go`,
`web_types.go`, `web_workspace.go`, `web_settings.go`.

Evidence: `handleIndex`, `handleSettings`, `handleCredentials`, `handleThreadDocument` and the `sub == ""` arm of
`handleSession` all call `serveSPAIndex(w, r, distFS())`. `mux.Handle("/", ...)` already catches `/new`, `/settings`,
`/settings/`, `/credentials`, `/thread/`; the explicit registrations (`web.go:322-337`) route to identical bodies. File
names do not match contents: `web_launchconfig.go` (11 lines) holds the credentials page handler; `webnext.go` is the
SPA/asset server (a "next" rewrite long since the only web UI); `web_types.go` holds model caches; `web_workspace.go`
routes `/s/` and `/thread/`; `web_settings.go` holds file-size/age helpers consumed only by
`app_rpc_settings_overview.go`. The `Handler` doc comment (`web.go:270`) sits above `validAssetPath`, not above `Handler` (`web.go:287`).

Fix plan: keep `/` (SPA), `/s/` (images), `/popout.html`, `/manifest.webmanifest`; delete the three one-line handlers and
their mux lines; rename `webnext.go` -> `web_spa.go`; move the helpers in `web_settings.go` next to their one caller;
fold `web_launchconfig.go` away. Check the `hubedge` auth-exempt list first, since auth runs in the guard wrapper for
all routes, not per mux entry. Size: -60 lines, 4 files fewer. Risk: low.

---

### hub-root-rest-19: History, spec and review-round narration is 21% of the slice (severity: medium; slop)

Locations: slice-wide; examples `main.go:84-88`, `main.go:449-460`, `main_background.go:175-190`,
`output_images.go:34-50`, `spawn.go:1250`, `config.go:243-326`, `openai_state_dir.go:7-17`, `web_api_tree.go:612-625`.

Evidence: 3,642 comment lines of 17,360; 11 `kata xxxx` references, 10 issue numbers, 69 spec/component/08b/"registry spec"
citations, 27 history phrases ("used to", "previously", "pre-kata behaviour", "round-10 finding", "F5 round 6", "was
deleted, having twice drifted"). `config.go:243-326` spends 85 lines explaining default values by spec section. Comments
state what a test "used to" do (`web_api_tree.go:620`, "Tests that construct a WebServer without a cache fall back to the
old synchronous behavior"). `docs/superpowers/audits/.../hub-app-host.md` finding 17 reports the same in the host slice.
Jesse's rules: history belongs in the commit message.

Fix plan: do it opportunistically in each PR above (do not make a standalone mega-diff): when a function is touched,
delete narrative/history comments and keep the one-paragraph "why". Move spec citations to the owning doc once
(`docs/product/subsystems.md` S07 already carries host retention). Size: -1,200 comment lines over the sequence.
Risk: none.

---

### hub-root-rest-20: Session on-disk layout and transcript path are re-spelled in the hub (and ~25 other places) (severity: medium; cross-cutting)

Locations: `image_serve.go:60`, `app_threadread.go:1490-1492` (`pastTranscriptPath`), `project_delete.go:668-679,
726-770`, `project_delete.go:21,60-82`; `app_session_delete.go:28-105`; elsewhere `agent/transcript_lookup.go:438`,
`agent/session_lifecycle.go:738`, `agent/session_init.go:601,990,1403`, `agent/delegate_tree_*.go`,
`agent/jobs_activity_past.go:324`, `tools/tool-fluency/cmd/evener-fluency/main.go:1392`.

Evidence: `grep -rn '\.transcript\.jsonl"' --include='*.go'` finds 29 non-test spellings; no canonical `schema`/`agent`
helper exists (`agent.transcriptPath` is unexported, `agent/transcript_lookup.go:437`). `removeProjectDeletionArtifacts`
(`project_delete.go:657-697`) hard-codes the per-session sidecar list (`sessions/<id>.*`, `<id>/` dir, `mutations/`,
`queues/`, `tasks/`, `<id>.api.jsonl`, scratch tree) while `agent/session_queue_persist.go:21` owns the `queues`
constant: a new per-session sidecar added in `agent` is silently not deleted by the hub. Session deletion
(`app_session_delete.go`) reuses functions named `projectDeletion*`, `removeProject*`, and `projectDeleteSkip` for
single-session work.

Fix plan: add `schema.SessionPaths{StateDir, ID}` with `Transcript()`, `Meta()`, `APILog()`, `Dir()`, `Sidecars() []string`
(the delete list) in `agent/schema`; migrate the 29 sites (mechanical); have `agent` own `Sidecars()` so adding a file
means editing one function. Rename the hub deletion helpers to `sessionDeletion*`. Test: a schema test enumerating every
per-session path the agent writes (fails when a new sidecar is added without being listed). Docs: none. Size: -80 lines,
~35 call sites touched. Risk: low-medium (deletion is destructive; keep the ordering tests
`force_stop_deletion_ordering_test.go` 603 lines and `project_delete_test.go` 2,051).

---

### hub-root-rest-21: 82 hand-rolled `[hub]` stderr writes, half of them to a hardcoded `os.Stderr` (severity: low)

Locations: `main.go` (46), `restart.go` (12), `main_background.go` (6), `login_url.go` (4), `attach.go` (2), `project_delete.go:46,56,640`,
`app_plugin_autoupgrade.go` (3), `thread_lifecycle_logging.go:126` (own format).

Evidence: functions that receive `stderr` use `fmt.Fprintf(stderr, "[hub] ...")` (`main.go`, `restart.go`); background
and deletion code writes to `os.Stderr` directly (`main_background.go:51,108,155-160`, `project_delete.go:46,56,640`),
so tests cannot capture it (AGENTS: "test output MUST BE PRISTINE"). Each site discards the write error with
`_, _ =`. No level, no sink injection.

Fix plan: one `type hubLog struct{ w io.Writer }` with `Printf`, built in `Run` from the `stderr` argument, stored on
`WebServer` and passed to `startBackground` helpers; replaces both styles. Do with finding 1. Size: -100 lines
(boilerplate), no behavior change. Risk: low.

---

### hub-root-rest-22: HTTP recorder writes credentials to disk; three separate "which origin do clients use" selectors (severity: low)

Locations: `http_recorder.go:30-83`; `main.go:1319-1329` (`advertisedHubHost`), `login_url.go:62-81`
(`browserLoginOrigin`), `mobile_origin.go:15-70` (`mobilePairingOrigin`), `app_mobile.go:37-75` (`safeMobileOrigin`),
`device_pairing.go:39-75`.

Evidence:
- `newHTTPRequestRecorder` records `Headers: r.Header` (`http_recorder.go:62`) before the auth guard
  (`web.go:348`: `record(auth(...))`), so `Authorization` and `Cookie` (the hub's master token) land in
  `hub-http.jsonl`. It is opt-in (`EVENER_RECORD_HTTP`) and mode 0600, documented in `docs/developing-evener/environment.md`,
  but its purpose is fuzz corpus harvesting, which does not need credentials.
- Three functions answer "what URL do I show the user", each with different loopback/unspecified/proxy rules; the
  mobile one adds interface ranking. `evener/mobile/pairing` (`app_mobile.go:19-31`, used by
  `frontend/src/panes/settings/sections/mobile.tsx:178`) hands the long-lived token in a URL, while the newer device
  pairing flow (`device_pairing.go`) uses an encrypted envelope; both are live in the UI.

Fix plan: redact `Authorization`/`Cookie`/`Set-Cookie` headers in the recorder (red test: record a request with a bearer
header, assert the JSONL has no token). For origins: write down the intended single policy in
`docs/product/subsystems.md` before consolidating; ask Jesse whether `evener/mobile/pairing` is superseded by device
pairing (if so, delete it and `mobile_origin.go`'s ranking, -250 lines). Not a code decision I can make from the audit.
Risk: low.

---

## Recommended fix sequence

1. **Delete what nothing runs** (low risk, shrinks everything after): finding 5 (memo tree chain, `TreeCache`,
   sync remote fallback), finding 16 (zero-production-caller functions and wrappers), finding 4's dead keys
   (`status_poll_interval`, `providers`, `Version`).
2. **Fix the two drift bugs red-test-first**: finding 4 (Settings shows 30s regardless of `spawn_timeout`) and finding
   8.1 (remote `/doc/image` by path ignores the artifacts root). Add the redaction fix from finding 22.
3. **Test debt, with Jesse's approval on coverage policy**: findings 11, 12, 13. Replace line-number tests with behavior
   tests or delete the unreachable branches and their seams together; sweep copy assertions.
4. **De-duplicate the launch path and file serving**: findings 6, 7, 8, then 14 and 15 (CLI + loops), 17 (Ref types) and
   20 (session layout) as separate mechanical PRs.
5. **Decompose `runMain`** (finding 1) together with the logger (finding 21) once the dead code is gone.
6. **Config**: finding 3 (knobs to constants) now; finding 2 (hub.toml as database) is a design discussion with Jesse
   first, then a migration PR.
7. **Navigation**: fix grouping at the source and drop the merge and re-validation (finding 9).
8. **Split the package** (finding 10) in this order: `hubconfig`, `navigation`, `hostmanage` (the last only after
   finding 2 decides what stays in hub.toml).
9. **Comment sweep** (finding 19) rides along with every PR above; no standalone PR.

Docs to update along the way: `docs/evener-hub.md` (config table), `docs/product/subsystems.md` (S06/S07 rows and source
links), `docs/architecture.md` (module table and `web.go` link), `docs/developing-evener/coverage.md` and `testing.md`
(finding 11), and `cmd/evener-dev/webbrowser.go` (package path for tests that move).
