# Codex Agent Integration Removal

## Status

The removal boundary is approved. This written specification awaits review before
implementation planning begins.

## Decision

Remove Evener's integration with external Codex app-server agents end to end:
the remote session source, managed launcher, configuration, runtime registration,
lifecycle dispatch, settings exposure, frontend and TUI affordances, test
fixtures, fuzz metadata, environment marker, scenarios, and active user
instructions.

This change does **not** remove Codex-family models from Evener's OpenAI provider
or remove generic AppWire compatibility with Codex-shaped messages. Those
features serve Evener sessions independently of the external Codex agent
integration.

There will be no dormant compatibility adapter or hidden launch path. An old
`hub.toml` that defines `codex_sources` or `codex_launches` will fail to load with
an actionable removal error instead of silently ignoring the obsolete section.

## Terms

- **Codex agent integration** means a Codex app-server process that Evener Hub
  connects to as a session source or starts as a managed harness.
- **Codex-family provider support** means OpenAI authentication and model
  selection used by Evener's own agent runtime, including `openai-codex` model
  registrations.
- **Codex-shaped AppWire compatibility** means generic wire types and decoders
  that accept protocol fixtures also emitted by Codex app-server. It is not a
  source registration or process launcher.

## Goals

- Evener Hub cannot configure, connect to, register, advertise, start, resume,
  fork, or shut down an external Codex agent.
- Hub settings, generated clients, the web UI, and the TUI expose no Codex launch
  control or Codex harness.
- Obsolete Codex source and launch configuration fails clearly at startup.
- Source-independent AppWire paging, local-daemon transport, and Evener harness
  behavior remain unchanged.
- OpenAI login, token storage, Codex-family model registration, and generic wire
  compatibility remain available.
- Active documentation and repository metadata describe the resulting product
  accurately.

## Non-goals

- Do not remove `openai-codex` models, OpenAI device authorization, provider
  token handling, or the `evener login`, `status`, and `logout` flows.
- Do not redesign the generic `appsource.Registry`, `appsource.Source`,
  `HarnessDescriptor`, or AppWire method catalog.
- Do not remove the generic `RemoteSources` health field solely because Codex was
  its current producer. Stop producing Codex entries; preserve the field's API
  shape for other sources.
- Do not remove local-daemon transcript paging or shared item-paging state.
- Do not remove generic Codex-shaped protocol fixtures, fuzz coverage, or
  `CodexErrorInfo`.
- Do not delete historical design documents, plans, or proofs. They remain
  architectural archaeology rather than current product guidance.
- Do not delete users' external Codex state or rewrite historical transcripts.
  Evener simply stops launching or connecting to those processes.
- Do not add tests whose sole purpose is to assert that a removed Codex symbol,
  route, field, harness, or settings section is absent.

## Resulting architecture

### Configuration and startup

`cmd/evener-hub/config.go` will no longer import `appsource` or `codexlaunch`, and
`Config` will no longer contain `CodexSources` or `CodexLaunches`. The matching
fields will also leave `internal/hubcore.WebConfig` and every constructor or
runtime options struct that currently propagates them.

The TOML decoder currently ignores unknown fields. Removing the Go fields alone
would therefore make an obsolete configuration appear accepted. `LoadConfig`
will decode once while retaining TOML metadata, detect any definition of the
top-level `codex_sources` or `codex_launches` key, and return a specific error
that tells the operator to remove that section because Codex agent integration
has been removed. This check applies regardless of whether the section is empty
or whether TOML expresses it as a table or array of tables. Other unknown-key
behavior does not change, and the error never echoes configured endpoints,
tokens, commands, or environment values.

`main.go` and `web.go` will stop constructing, injecting, supervising, and
shutting down a `codexlaunch.CodexLauncher`. The `codexShutdowner` seam and
launcher-specific shutdown tests will disappear. A normal hub shutdown will
continue to stop the components it still owns.

### Source registry and lifecycle

Hub startup will stop converting configured Codex endpoints into
`appsource.CodexSource` instances. Model and thread-list paths will stop lazily
activating managed Codex sources. The source lookup, thread lifecycle, tree, and
RPC paths will lose branches that recognize a Codex source or ask the launcher
to ensure one exists.

No surviving code will publish `HarnessDescriptor{Kind: "codex"}`. The generic
harness descriptor and source registry stay because Evener and local-daemon
paths use them. A request containing a stale Codex source or harness identifier
will follow the ordinary unknown-source or unknown-harness error path; there is
no Codex-specific migration alias.

The affected mixed hub files are:

- `cmd/evener-hub/app_rpc.go`
- `cmd/evener-hub/app_models.go`
- `cmd/evener-hub/app_threadlist.go`
- `cmd/evener-hub/app_sources.go`
- `cmd/evener-hub/app_threadlifecycle.go`
- `cmd/evener-hub/web_api_tree.go`
- `cmd/evener-hub/web_api.go`
- `cmd/evener-hub/web.go`
- `cmd/evener-hub/main.go`
- `cmd/evener-hub/internal/hubcore/config.go`

Their mixed tests will be edited narrowly. Tests for Evener sources, local
sources, generic routing, lifecycle errors, and shutdown will remain.

### Appsource package

Delete the external adapter implementation:

- `cmd/evener-hub/internal/appsource/codex_cache.go`
- `cmd/evener-hub/internal/appsource/codex_input.go`
- `cmd/evener-hub/internal/appsource/codex_item_paging.go`
- `cmd/evener-hub/internal/appsource/codex_live_thread.go`
- `cmd/evener-hub/internal/appsource/codex_mapping.go`
- `cmd/evener-hub/internal/appsource/codex_source.go`
- `cmd/evener-hub/internal/appsource/codex_wire_types.go`

Delete adapter-only tests:

- `codex_cache_coverage_test.go`
- `codex_mapping_fuzz_test.go`
- `codex_source_test.go`
- `cov_rp_codex_errors_test.go`

`codex_source.go` presently owns four transport helpers that
`local_daemon.go` also uses: `appwireDialFunc`, `defaultAppwireDial`,
`hubStderr`, and `hubConnectionLogf`. Move those helpers, with their existing
logging and race-safety behavior, to a source-neutral `transport.go` before
removing the Codex file.

In `source.go`, delete only methods with a `*CodexSource` receiver and
Codex-specific comments. Preserve `Source`, `ItemCandidateSource`,
`ItemReadCandidateSource`, `CombinedItemReadSource`, their result types, and the
local-daemon implementations.

`codex_item_paging_test.go` contains both Codex and source-independent/local
coverage. Delete its Codex cases, move the surviving tests to a neutral filename
such as `local_daemon_item_paging_test.go`, and then delete the Codex-named file.
Likewise, edit `transport_seams_test.go` and hub item-paging tests by case rather
than deleting their local-daemon coverage.

Keep:

- `cmd/evener-hub/internal/appsource/item_paging_state.go`
- `cmd/evener-hub/internal/appitempaging/`
- local-daemon source, relay, cursor, registry, ref, cache, and navigation code
- `github.com/coder/websocket`, which the surviving AppWire/local-daemon
  transport still requires

### Managed launcher

Delete `cmd/evener-hub/internal/codexlaunch/` in full, including its unit tests,
program fuzz tests, seed corpus, and test helpers. Delete the two hub-level
launcher suites:

- `cmd/evener-hub/codex_launch_test.go`
- `cmd/evener-hub/codex_launch_real_test.go`

No generic process launcher will be introduced as a substitute. Evener's own
harness launch path remains the only supported managed agent launch path.

### Settings and AppWire

Remove `SettingsOverviewResponse.CodexLaunches` and
`SettingsCodexLaunchEntry` from `appwire/types.go`. Remove the corresponding
projection in `cmd/evener-hub/app_rpc_settings_overview.go` and its
Codex-specific test cases. Do not leave an always-empty `codexLaunches` JSON
field.

Preserve the rest of `SettingsOverviewResponse`, the settings RPC, generic
`RemoteSources`, `HarnessDescriptor`, `CodexErrorInfo`, and the AppWire method
catalog. Regenerate, rather than hand-edit:

- `docs/appwire-protocol.md`
- `cmd/evener-hub/frontend/src/protocol/types.gen.ts`

The authoritative command is `make generate`; `make lint-generated` proves the
checked-in outputs match their Go inputs.

### Web UI and TUI

Delete the Codex launch settings section:

- `cmd/evener-hub/frontend/src/panes/settings/sections/launchCodex.tsx`
- `cmd/evener-hub/frontend/src/panes/settings/sections/launchCodex.test.tsx`
- `cmd/evener-hub/frontend/src/panes/settings/sections/launchCodex.module.css`

Remove its imports, route/section registration, and data plumbing from
`Settings.tsx`, `sections.ts`, and affected settings tests. Preserve the other
settings sections and their navigation behavior.

In `cmd/evener-tui/hub_spawn.go`, remove the `kind != "codex"` model-catalog
special case. Surviving Evener harnesses continue to use Evener models, and
plugin support remains governed by the existing Evener-kind capability. Remove
Codex launch/detail samples from the TUI sample corpus. Where a sample exercises
generic read-only source rendering rather than Codex behavior, rename and retain
it as a neutral local-daemon/source sample; delete Codex-only spawn coverage.

### Environment, active docs, scenarios, and gate metadata

Remove `EVENER_HUB_SPAWNED_CODEX` from `envvars/envvars.go` and all generated or
handwritten environment references. The surviving `EVENER_HUB_SPAWNED` marker
continues to identify hub-spawned Evener daemons.

Remove obsolete Codex-agent instructions from:

- `docs/evener-hub.md`
- `docs/evener-hub-remote-operations.md`
- `docs/developing-evener/environment.md`

Delete:

- `test/scenarios/codex-sidebar-drive.md`
- `test/scenarios/codex-sidebar-open.md`

Update `scripts/fuzz/fuzz-targets.txt` by removing
`FuzzMapCodexTurn`, `FuzzParseCodexEndpoint`, and
`FuzzCodexLaunchBehaviorProgram`. Retain `FuzzCodexItemDecode`, which protects
the generic wire decoder. Remove only the
`cmd/evener-hub/internal/codexlaunch` row from `testing-budget.json`; retain the
`appsource` budget.

Historical material under `docs/superpowers/specs/`, plans, and proof artifacts
will not be rewritten. Agent `.codex` instruction/profile support and
`.codex-plugin` ecosystem compatibility also remain because they do not launch
or source external Codex sessions.

## Retained Codex-family boundaries

The following are explicitly outside the removal:

- `llm/providers/tokenauth/codex.go`
- OpenAI device OAuth and provider credential flows
- `openai-codex` model registrations, generated catalogs, and model goldens
- `cmd/evener` OpenAI login/status/logout behavior
- `appwire/codex_compat_test.go`
- `appwire/item_fuzz_test.go`, `FuzzCodexItemDecode`, and its corpus/golden
- `CodexErrorInfo` and generic compatible JSON tags in `appwire/types.go`
- agent `.codex` and `.codex-plugin` compatibility
- historical specs, plans, and proofs

Every remaining textual `codex` match after implementation must be classified as
one of these retained uses, a retired-config error tombstone, or historical
documentation. A surviving match that can configure, launch, connect to,
advertise, or operate an external Codex session is a defect.

## Error and migration behavior

This is deliberate removal, not a deprecation window:

1. Existing valid non-Codex hub configuration loads as before.
2. A config that defines `codex_sources` fails with a removal message naming
   that section.
3. A config that defines `codex_launches` fails with a removal message naming
   that section.
4. The hub publishes no Codex source, harness, or launcher settings entry.
5. Old clients tolerate the omitted optional settings field under the existing
   JSON compatibility rules; Evener will not serve a placeholder field or route.
6. Stale source/harness IDs receive the ordinary unsupported identifier error.

No migration reads old tokens or endpoints, starts a replacement process, or
discards external files. Operators remove the obsolete sections and restart the
hub.

## Test strategy

The test strategy follows the explicit decision not to add absence tests.
Obsolete implementation tests and fixtures will be deleted. No new test will
search a registry, JSON object, route table, generated type, or settings list
only to prove that a Codex entry is missing.

Add behavioral coverage only for the new configuration contract: defining each
retired top-level section must make `LoadConfig` return its actionable error,
while ordinary configuration still loads. These are startup error-contract
tests, not repository absence assertions.

Use existing positive tests to protect surviving behavior:

- local-daemon connection, item paging, continuation, cancellation, and transport
  logging;
- generic source lookup and unknown-source errors;
- Evener harness listing, model selection, plugin support, spawn, and shutdown;
- non-Codex settings sections and navigation;
- `TestCodexAppServerCoreFixtureCompatibility`, `TestCodexItemDecodeGolden`, and
  `TestMethodCatalogWellFormed` for retained wire compatibility.

Before production edits, the retired-config tests must fail for the expected
reason. For deletion-only surfaces, use the compiler, generator, frontend type
checker, fuzz registry checker, and an uncommitted classified grep as the
red/green dependency oracle. Do not encode that grep as a permanent negative
test.

## Implementation sequence

1. Add and run the retired-config behavior tests to establish RED.
2. Move shared transport helpers to `appsource/transport.go`; run local-daemon
   transport and race coverage before deleting their old owner.
3. Remove launcher and source configuration from config, hubcore, startup,
   shutdown, and web construction; make the config tests GREEN.
4. Delete the launcher and source adapters; remove Codex registration,
   activation, routing, and lifecycle branches while preserving generic/local
   code and tests.
5. Remove the settings DTO/projection, web section, TUI special case, fixtures,
   environment variable, fuzz rows, timing row, scenarios, and active docs.
6. Run `gofmt` on touched Go files and Biome with `--write` on touched frontend
   files under `src/`.
7. Run `make generate`, inspect the generated diff, and run
   `make lint-generated`.
8. Run focused positive tests, review every remaining `codex` match against the
   retained boundary, then run all repository gates.

Implementation may be split into non-overlapping backend/generated/TUI,
frontend, and documentation lanes. The backend lane owns both generated outputs
so no two writers update the same file. Each lane must commit its scoped work and
report its diff and verification before integration.

## Verification

Focused checks:

```sh
go test ./cmd/evener-hub ./cmd/evener-hub/internal/appsource ./appwire ./cmd/evener-tui -count=1
go test -race ./cmd/evener-hub/internal/appsource ./cmd/evener-hub -count=1
make test-web
make fuzz-registry-check
make fuzz-seeds
make lint-generated
```

Run a repository-wide case-insensitive `git grep` for `codex` and classify every
remaining match. Also inspect the formerly operative names
`codex_sources`, `codex_launches`, `launch-codex`,
`EVENER_HUB_SPAWNED_CODEX`, `CodexSource`, `CodexLauncher`, and
`Kind: "codex"`. The only acceptable retired configuration matches are the
loader's explicit error contract and its tests; historical specs and the
retained provider/wire surfaces above are acceptable. This is a review command,
not a checked-in absence test.

Full post-integration gates:

```sh
make merge-approval-gate
make test-race
make test-web-browser
make fuzz
```

A gate counts only if it runs to completion and exits zero.

## Acceptance criteria

- The Codex source adapter and managed-launch package are deleted.
- Hub configuration contains no live Codex fields, and either obsolete section
  fails with an actionable, non-secret-bearing error.
- Startup, shutdown, source registry, harness listing, thread listing,
  lifecycle, RPC, tree, and settings paths contain no operative Codex branch.
- AppWire and generated TypeScript expose no Codex launcher settings DTO.
- The web settings route/section and Codex-specific TUI behavior are removed.
- The Codex-spawn environment marker, active docs, scenarios, fuzz targets, and
  timing budget entry are removed.
- Local-daemon paging and transport behavior remain covered and pass under the
  race detector.
- OpenAI authentication, Codex-family model selection, and generic Codex-shaped
  AppWire compatibility remain covered and pass.
- Generated outputs are current, every remaining `codex` match is classified,
  the worktree is clean after commits, and every focused and full gate above
  exits zero.
