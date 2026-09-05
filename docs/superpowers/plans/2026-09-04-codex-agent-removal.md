# Codex Agent Integration Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove every operative external Codex app-server source and managed-launch path while preserving Evener's Codex-family provider support, generic AppWire compatibility, and local-daemon behavior.

**Architecture:** Retire the configuration contract first with a precise startup error, then extract transport primitives that the retained local-daemon source needs. Remove settings and TUI consumers before deleting launcher/runtime producers, then delete the now-unreferenced source adapter and its fuzz estate. Finish by updating active documentation and metadata, regenerating authoritative outputs, classifying all remaining Codex references, and running the complete gate set.

**Tech Stack:** Go 1.26, BurntSushi/toml, AppWire JSON-RPC, React/TypeScript, Biome, Vitest, repository Make targets, Go race detector and fuzz harnesses.

**Spec:** `docs/superpowers/specs/2026-09-04-codex-agent-removal-design.md`

## Global Constraints

- There will be no dormant compatibility adapter or hidden launch path.
- An old `hub.toml` that defines `codex_sources` or `codex_launches` will fail to load with an actionable removal error instead of silently ignoring the obsolete section.
- This check applies regardless of whether the section is empty or whether TOML expresses it as a table or array of tables.
- Other unknown-key behavior does not change, and the error never echoes configured endpoints, tokens, commands, or environment values.
- Do not remove `openai-codex` models, OpenAI device authorization, provider token handling, or the `evener login`, `status`, and `logout` flows.
- Do not redesign the generic `appsource.Registry`, `appsource.Source`, `HarnessDescriptor`, or AppWire method catalog.
- Do not remove the generic `RemoteSources` health field solely because Codex was its current producer. Stop producing Codex entries; preserve the field's API shape for other sources.
- Do not remove local-daemon transcript paging or shared item-paging state.
- Do not remove generic Codex-shaped protocol fixtures, fuzz coverage, or `CodexErrorInfo`.
- Do not delete historical design documents, plans, or proofs. They remain architectural archaeology rather than current product guidance.
- Do not delete users' external Codex state or rewrite historical transcripts.
- Do not add tests whose sole purpose is to assert that a removed Codex symbol, route, field, harness, or settings section is absent.
- No generic process launcher will be introduced as a substitute. Evener's own harness launch path remains the only supported managed agent launch path.
- The authoritative generator command is `make generate`; `make lint-generated` proves the checked-in outputs match their Go inputs.
- Default tests must be deterministic and must not depend on provider credentials, network access, quota, current model behavior, or ambient developer machine state.
- Run `gofmt` on touched Go files and Biome with `--write` on touched frontend files under `src/`.
- A gate counts only if it runs to completion and exits zero.

---

### Task 1: Enforce the Retired Configuration Contract

**Files:**
- Modify: `cmd/evener-hub/config.go:13-15,29-45,110-128`
- Modify: `cmd/evener-hub/config_test.go:3-8,197-274`
- Read before test edits: `docs/developing-evener/testing.md`

**Interfaces:**
- Consumes: `toml.Decode(data string, v any) (toml.MetaData, error)` and `toml.MetaData.IsDefined(key ...string) bool` from `github.com/BurntSushi/toml`.
- Produces: `LoadConfig(path string) (Config, error)` rejects either retired top-level key with `config section %q is no longer supported because Codex agent integration has been removed; remove it from hub.toml` before returning any TOML type error.
- Produces temporarily: `Config.CodexSources` and `Config.CodexLaunches` remain until Task 5 so the repository stays buildable while the new error contract lands.

- [ ] **Step 1: Read the repository testing policy**

Run:

```sh
sed -n '1,55p' docs/developing-evener/testing.md
```

Expected: the deterministic-test, external-boundary, and no-sleep rules are visible before editing tests.

- [ ] **Step 2: Replace successful Codex parsing coverage with the removal contract**

Add `strings` to the imports in `config_test.go`. Remove the `[[codex_sources]]`, `[[codex_launches]]`, and `[codex_launches.env]` blocks and their assertions from `TestLoadConfig_ParsesFile`; retain its provider and ordinary hub-config assertions, and add `future_option = "ignored"` to that valid fixture so the test positively preserves unrelated unknown-key behavior. Add this behavioral test:

```go
func TestLoadConfig_RejectsRemovedCodexSections(t *testing.T) {
	tests := []struct {
		name    string
		section string
		body    string
	}{
		{name: "sources empty value", section: "codex_sources", body: "codex_sources = []"},
		{name: "sources table", section: "codex_sources", body: "[codex_sources]\nendpoint = \"wss://secret.example/session\""},
		{name: "sources array of tables", section: "codex_sources", body: "[[codex_sources]]\nendpoint = \"wss://secret.example/session\"\nbearer_token = \"secret-value\""},
		{name: "launches empty value", section: "codex_launches", body: "codex_launches = []"},
		{name: "launches table", section: "codex_launches", body: "[codex_launches]\nbinary = \"/secret/codex\""},
		{name: "launches array of tables", section: "codex_launches", body: "[[codex_launches]]\nbinary = \"/secret/codex\"\n[codex_launches.env]\nTOKEN = \"secret-value\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hub.toml")
			if err := os.WriteFile(path, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatalf("LoadConfig accepted removed %s configuration", tt.section)
			}
			message := err.Error()
			for _, want := range []string{tt.section, "Codex agent integration has been removed", "remove it from hub.toml"} {
				if !strings.Contains(message, want) {
					t.Errorf("error %q does not contain %q", message, want)
				}
			}
			for _, secret := range []string{"secret.example", "secret-value", "/secret/codex"} {
				if strings.Contains(message, secret) {
					t.Errorf("error exposed configured value %q: %q", secret, message)
				}
			}
		})
	}
}
```

- [ ] **Step 3: Run the focused test and record RED**

Run:

```sh
go test ./cmd/evener-hub -run 'TestLoadConfig_(RejectsRemovedCodexSections|ParsesFile)$' -count=1
```

Expected: `TestLoadConfig_RejectsRemovedCodexSections` fails because current decoding accepts arrays and returns a generic type error for table syntax rather than the removal contract. `TestLoadConfig_ParsesFile` remains green.

- [ ] **Step 4: Decode once, inspect metadata before surfacing decode errors**

Replace the current `toml.Unmarshal` block in `LoadConfig` with:

```go
	metadata, decodeErr := toml.Decode(string(data), &cfg)
	for _, key := range []string{"codex_sources", "codex_launches"} {
		if metadata.IsDefined(key) {
			return cfg, fmt.Errorf("config section %q is no longer supported because Codex agent integration has been removed; remove it from hub.toml", key)
		}
	}
	if decodeErr != nil {
		return cfg, fmt.Errorf("parse config: %w", decodeErr)
	}
```

The metadata check deliberately precedes `decodeErr`: BurntSushi/toml supplies metadata for `[codex_sources]` and `[codex_launches]` even though those table shapes do not decode into the temporary slice fields.

- [ ] **Step 5: Verify GREEN and unchanged ordinary config behavior**

Run:

```sh
gofmt -w cmd/evener-hub/config.go cmd/evener-hub/config_test.go
go test ./cmd/evener-hub -run '^TestLoadConfig_' -count=1
git diff --check
```

Expected: all three commands exit zero; missing files, ordinary fields, provider arrays, unrelated unknown keys, validation errors, and both retired-key errors behave as before or as newly specified.

- [ ] **Step 6: Commit the config contract**

```sh
git add cmd/evener-hub/config.go cmd/evener-hub/config_test.go
git diff --cached --check
git commit -m "test(config): reject removed Codex sections"
```

Expected: one commit containing only the loader and its behavioral tests.

---

### Task 2: Extract Shared AppWire Transport Helpers

**Files:**
- Create: `cmd/evener-hub/internal/appsource/transport.go`
- Modify: `cmd/evener-hub/internal/appsource/codex_source.go:3-20,45-64`
- Test: `cmd/evener-hub/internal/appsource/transport_seams_test.go`
- Test: `cmd/evener-hub/internal/appsource/local_daemon_test.go`

**Interfaces:**
- Consumes: `appwire.DialWebSocketWithHeaders(context.Context, string, *http.Client, http.Header) (appwire.Transport, error)`.
- Produces: package-private `type appwireDialFunc func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error)`, `defaultAppwireDial`, `hubStderr`, and `hubConnectionLogf`, unchanged for `local_daemon.go`.
- Produces: `codex_source.go` still compiles in this task but no longer owns shared local-daemon dependencies.

- [ ] **Step 1: Create the source-neutral owner**

Create `transport.go` with the exact implementation below:

```go
package appsource

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"primeradiant.com/evener/appwire"
)

type appwireDialFunc func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error)

func defaultAppwireDial(ctx context.Context, endpoint string, client *http.Client, header http.Header) (appwire.Transport, error) {
	return appwire.DialWebSocketWithHeaders(ctx, endpoint, client, header)
}

// hubStderr is captured once because runClientKeepalive outlives the test
// that started it (connect uses context.WithoutCancel), so reading the
// mutable os.Stderr global from that goroutine races any test that swaps
// and restores os.Stderr (issue #837).
var hubStderr = os.Stderr

// hubConnectionLogf is the appwire.Client connection-lifecycle sink (see
// appwire.Client.SetLogf) for all hub-dialed source connections. The hub is a
// plain daemon, never a TUI rendering over an interactive terminal, so its own
// stderr — labelled like every other hub diagnostic — is a safe destination,
// unlike the TUI's stderr (issue #783).
func hubConnectionLogf(format string, args ...any) {
	_, _ = fmt.Fprintf(hubStderr, "[hub] "+format+"\n", args...)
}
```

- [ ] **Step 2: Remove duplicate ownership from the Codex adapter**

Delete the four definitions from `codex_source.go` and remove only the now-unused `fmt` and `os` imports. Keep its `context`, `net/http`, `appwire`, and other adapter imports until Task 6 deletes the file.

- [ ] **Step 3: Prove the retained local-daemon transport path**

Run:

```sh
gofmt -w cmd/evener-hub/internal/appsource/transport.go cmd/evener-hub/internal/appsource/codex_source.go
go test ./cmd/evener-hub/internal/appsource -run 'LocalDaemon|ConnectionLog|CallerCancellation|Transport' -count=1
go test -race ./cmd/evener-hub/internal/appsource -run 'LocalDaemon|ConnectionLog|CallerCancellation|Transport' -count=1
git diff --check
```

Expected: all commands exit zero, including connection logging and cancellation coverage under the race detector.

- [ ] **Step 4: Commit the extraction**

```sh
git add cmd/evener-hub/internal/appsource/transport.go cmd/evener-hub/internal/appsource/codex_source.go
git diff --cached --check
git commit -m "refactor(appsource): isolate shared transport helpers"
```

Expected: one behavior-preserving refactor commit with no adapter deletion.

---

### Task 3: Remove Launcher Settings and Web Controls

**Files:**
- Modify: `appwire/types.go:3018-3041,3129-3156`
- Modify: `appwire/protocol.go` method-catalog entry for `MethodEvenerSettingsOverview`
- Modify: `cmd/evener-hub/app_rpc_settings_overview.go:3-31,75-102`
- Modify: `cmd/evener-hub/app_rpc_settings_overview_test.go`
- Delete: `cmd/evener-hub/frontend/src/panes/settings/sections/launchCodex.tsx`
- Delete: `cmd/evener-hub/frontend/src/panes/settings/sections/launchCodex.test.tsx`
- Delete: `cmd/evener-hub/frontend/src/panes/settings/sections/launchCodex.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/Settings.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/SettingsNav.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/agents.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/overviewSeam.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/project.tsx`
- Generated: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`
- Generated: `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: Task 1's still-present `hubcore.WebConfig.CodexLaunches`; this task stops consuming it before Task 5 removes it.
- Produces: `SettingsOverviewResponse` with exactly `Hub`, `Storage`, `Agents`, and `McpDiscovered`; the `evener/settings/overview` method and every other settings type remain.
- Produces: 15 legacy settings sections and 17 total registered frontend sections, with no `launch-codex` route.
- Preserves: `RemoteSources`, `HarnessDescriptor`, `CodexErrorInfo`, `appwire/codex_compat_test.go`, and `FuzzCodexItemDecode`.

- [ ] **Step 1: Remove the settings wire field and projection**

Make the response type exactly:

```go
type SettingsOverviewResponse struct {
	Hub           *SettingsHubOverview     `json:"hub,omitempty"`
	Storage       *SettingsStorageOverview `json:"storage,omitempty"`
	Agents        []SettingsAgentEntry     `json:"agents,omitempty"`
	McpDiscovered *SettingsMCPOverview     `json:"mcpDiscovered,omitempty"`
}
```

Delete `SettingsCodexLaunchEntry` and its documentation. Rewrite the response comment to describe five sections: General, Hub, Storage, Agents, and probed MCP. Keep the method constant and change only its catalog description to:

```go
{MethodEvenerSettingsOverview, EmptyParams{}, SettingsOverviewResponse{}, ScopeHub, "Returns the settings overview field bag: hub/runtime, storage, agent roster, and probed MCP servers — the five template-only settings sections' data."},
```

In `app_rpc_settings_overview.go`, remove the `codexlaunch` import, the `CodexLaunches` composite-literal field, and `settingsCodexLaunchEntries`. Keep `sort` for MCP rows. The result is:

```go
	return appwire.SettingsOverviewResponse{
		Hub:           settingsHubOverview(cfg),
		Storage:       &appwire.SettingsStorageOverview{StateDir: cfg.StateDir},
		Agents:        settingsAgentRoster(),
		McpDiscovered: settingsMCPOverview(ctx, cfg),
	}, nil
```

- [ ] **Step 2: Narrow the settings RPC tests to surviving behavior**

Delete `TestHubRPCSettingsOverview_CodexLaunches` and imports used only by it: `reflect`, `strings`, `time`, and `internal/codexlaunch`. Change `requestSettingsOverview` to return only `appwire.SettingsOverviewResponse`, then change the four surviving `resp, _ := requestSettingsOverview(...)` assignments to `resp := requestSettingsOverview(...)`.

Retain these positive suites:

```text
TestHubRPCSettingsOverview_HubAndStorage
TestHubRPCSettingsOverview_NilPastIndexWhenNotConfigured
TestHubRPCSettingsOverview_Agents
TestHubRPCSettingsOverview_MCPDiscovered
TestHubRPCSettingsOverview_MCPDiscoveredEmptyWhenConfigMissing
```

Run:

```sh
gofmt -w appwire/types.go appwire/protocol.go cmd/evener-hub/app_rpc_settings_overview.go cmd/evener-hub/app_rpc_settings_overview_test.go
go test ./cmd/evener-hub -run '^TestHubRPCSettingsOverview_' -count=1
go test ./appwire -run '^(TestCodexAppServerCoreFixtureCompatibility|TestCodexItemDecodeGolden|TestMethodCatalogWellFormed)$' -count=1
```

Expected: both commands exit zero; retained settings and generic wire compatibility remain covered.

- [ ] **Step 3: Delete the frontend section and route registration**

Delete the three `launchCodex` files. Remove these exact lines from their owners:

```ts
import { CodexLaunchSection } from "./sections/launchCodex";
```

```ts
"launch-codex": CodexLaunchSection,
```

```ts
{ id: "launch-codex", label: "Codex launch", cluster: "agents-models" },
```

Change the project-settings copy to:

```tsx
Layered on top of the global Evener launch settings. Only fields set here override the global defaults.
```

In `overviewSeam.ts`, make the overview-fed consumer singular (`agents.tsx - the overview-fed section`, `one call site`, and `the consumer`). In `agents.test.tsx`, rename the arbitrary `{ name: "codex" }` roster fixture to `{ name: "explorer" }` and update its positive rendered-name expectation.

- [ ] **Step 4: Update positive section/navigation expectations**

In `sections.test.ts`, update total sections `18` to `17`, legacy sections `16` to `15`, the Agents & models cluster count `5` to `4`, and remove `Codex launch` from its labels. Update contiguous cluster slices to `5:9`, `9:13`, and `13:17`. In `SettingsNav.test.tsx`, rename the 16-section test to 15 and remove the deleted label while retaining ordering, filtering, navigation, uniqueness, and dispatch assertions.

- [ ] **Step 5: Format frontend sources and regenerate the protocol outputs**

Run from the repository root:

```sh
cd cmd/evener-hub/frontend
npx biome check --write \
  src/panes/settings/Settings.tsx \
  src/panes/settings/SettingsNav.test.tsx \
  src/panes/settings/sections.ts \
  src/panes/settings/sections.test.ts \
  src/panes/settings/sections/agents.test.tsx \
  src/panes/settings/sections/overviewSeam.ts \
  src/panes/settings/sections/project.tsx
cd ../../../
make generate
git diff -- docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts
make lint-generated
```

Expected generated changes: `SettingsCodexLaunchEntry` and `codexLaunches?: SettingsCodexLaunchEntry[]` disappear from `types.gen.ts`; the `codexLaunches` row disappears from `docs/appwire-protocol.md`; the preserved method description names five sections. No method or route constant disappears. `make lint-generated` exits zero.

- [ ] **Step 6: Run the frontend positive gate**

Run:

```sh
make test-web
```

Expected: frontend unit tests, typecheck, and Biome gate all exit zero with the remaining settings navigation intact.

- [ ] **Step 7: Commit the settings removal**

```sh
git add appwire/types.go appwire/protocol.go \
  cmd/evener-hub/app_rpc_settings_overview.go \
  cmd/evener-hub/app_rpc_settings_overview_test.go \
  cmd/evener-hub/frontend/src/panes/settings/Settings.tsx \
  cmd/evener-hub/frontend/src/panes/settings/SettingsNav.test.tsx \
  cmd/evener-hub/frontend/src/panes/settings/sections.ts \
  cmd/evener-hub/frontend/src/panes/settings/sections.test.ts \
  cmd/evener-hub/frontend/src/panes/settings/sections/agents.test.tsx \
  cmd/evener-hub/frontend/src/panes/settings/sections/overviewSeam.ts \
  cmd/evener-hub/frontend/src/panes/settings/sections/project.tsx \
  cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md
git add -u cmd/evener-hub/frontend/src/panes/settings/sections/launchCodex.tsx \
  cmd/evener-hub/frontend/src/panes/settings/sections/launchCodex.test.tsx \
  cmd/evener-hub/frontend/src/panes/settings/sections/launchCodex.module.css
git diff --cached --check
git commit -m "refactor(settings): remove Codex launch controls"
```

Expected: one settings/API/frontend commit; no provider login or model files are staged.

---

### Task 4: Remove TUI Codex Harness Behavior

**Files:**
- Modify: `cmd/evener-tui/hub_spawn.go`
- Modify: `cmd/evener-tui/hub_model.go`
- Modify: `cmd/evener-tui/hub_commands.go`
- Modify: `cmd/evener-tui/hub_update.go`
- Modify: `cmd/evener-tui/hub_commands_covtest_test.go`
- Modify: `cmd/evener-tui/hub_spawn_covtest_test.go`
- Modify: `cmd/evener-tui/hub_spawn_plugins_test.go`
- Modify: `cmd/evener-tui/hub_update_fuzz_test.go`
- Modify: `cmd/evener-tui/command_registry_fuzz_test.go`
- Modify: `cmd/evener-tui/spawn_views_fuzz_test.go`
- Modify: `cmd/evener-tui/hub_model_test.go`
- Modify: `cmd/evener-tui/tmux_e2e_test.go`
- Modify: `cmd/evener-tui/details_drawer_test.go`
- Modify: `cmd/evener-tui/hub_agents_test.go`
- Modify: `cmd/evener-tui/hub_notice_test.go`
- Modify: `cmd/evener-tui/hub_status_test.go`
- Modify: `cmd/evener-tui/hub_transcript_widgets_test.go`
- Modify: `cmd/evener-tui/hub_types.go`
- Modify: `cmd/evener-tui/notice_panel_covtest_test.go`
- Modify: `cmd/evener-tui/question_overlay_test.go`
- Modify: `cmd/evener-tui/queue_send.go`
- Modify: `cmd/evener-tui/root_factories_fuzz_test.go`
- Modify: `cmd/evener-tui/internal/tuipick/picker_panel_test.go`
- Modify: `cmd/evener-tui/tui_samples.go`
- Modify: `cmd/evener-tui/tui_samples_test.go`
- Modify: `cmd/evener-tui/tui_samples_covtest_test.go`

**Interfaces:**
- Consumes: the Task 3-generated `HarnessDescriptor` type unchanged; production now publishes only the Evener descriptor after Task 5.
- Produces: spawn model selection always uses `hubModel.spawnModels`; the dead per-harness `hubModelsMsg` request/cache chain no longer exists.
- Preserves: `spawnHarnessSupportsPlugins() bool` remains `m.spawnHarnessKind() == "evener"`; generic non-Evener capability tests use kind `external` and prove plugins are omitted.
- Preserves: read-only source rendering through neutral `local-daemon` sample data.

- [ ] **Step 1: Collapse spawn model selection to the Evener catalog**

Remove the per-harness branches from `activateSpawnModelField`, `submitSpawnForm`, field hints, `spawnSelectableModels`, `syncSpawnModelWithHarness`, picker title, and `spawnView`. Delete `spawnHarnessUsesEvenerModels` and `spawnHarnessModelDisplay`. The retained production helpers must read:

```go
func (m hubModel) activateSpawnModelField() (tea.Model, tea.Cmd) {
	models := m.spawnSelectableModels()
	if len(models) == 0 {
		m.err = errors.New("no models available")
		return m, nil
	}
	m.openSpawnModelPicker(models)
	return m, nil
}

func (m hubModel) spawnSelectableModels() []tuipick.ModelPickerItem {
	return m.spawnModels
}

func (m *hubModel) syncSpawnModelWithHarness() {
	if strings.TrimSpace(m.spawnModel) == "" {
		if model, ok := firstEnabledModel(m.spawnSelectableModels()); ok {
			m.spawnModel = model.ID
		}
	}
}

func (m hubModel) spawnModelPickerTitle() string {
	return "Select model"
}

func (m hubModel) spawnHarnessSupportsPlugins() bool {
	return m.spawnHarnessKind() == "evener"
}
```

Require a selected model on submit for every surviving harness. Keep `fetchHubSpawnOptions`, its global `ModelListParams{CWD: workingDir}`, `fetchHubSessionModels`, and the generic descriptor type.

- [ ] **Step 2: Delete the dead per-harness model message chain**

Delete `hubModel.spawnHarnessModels` from `hub_model.go`, `hubModelsMsg` and `fetchHubModelsForHarness` from `hub_commands.go`, and the `hubModelsMsg` plus per-harness conditions from `hub_update.go`. Delete `TestCovFetchHubModelsForHarness`, its fuzz-registry invocation, and synthetic `hubModelsMsg` fuzz cases. Do not alter the session-model request path.

- [ ] **Step 3: Narrow spawn and plugin tests to positive surviving behavior**

In `hub_spawn_covtest_test.go`, delete the non-Evener model-fetch/default cases and `TestCovSpawnHarnessUsesEvenerModels`; retain no-model error, populated picker, first-enabled default, existing selection, harness cycling, kind, and empty-task coverage. Keep only the global catalog assertion in `TestCovSpawnSelectableModels`.

In `hub_spawn_plugins_test.go`, rename `TestSpawnPlugins_CodexIgnoresStaleSelectionForLaunch` to `TestSpawnPlugins_NonEvenerIgnoresStaleSelectionForLaunch`; use harness ID/kind `external` and retain assertions that the plugin field is hidden and `EnabledPlugins` is omitted while unrelated overrides survive. Apply the same neutral `external` fixture to generic non-Evener cases in the fuzz files.

In `hub_model_test.go`, delete `TestHubModelCodexSpawnSurvivesModelListFailure` and `TestHubModelCodexSpawnOpensHarnessModelPicker`. Rewrite `TestHubModelSpawnCyclesConfiguredHarnesses` with ID/kind `external`: after cycling, it must retain the global `openai/gpt-5` selection and send that model with `Harness: "external"`. Rewrite `TestHubModelSpawnFormFocusControlsHarnessAndModel` so switching to `external` keeps the global model and activating Model opens the already-populated picker without another harness-specific RPC. In `TestHubSpawnSendsHarnessSeparatelyFromModel`, rename only the arbitrary harness/ref to `external`; keep the separate model assertion. Delete `TestTUITmuxE2E_CodexSpawnUsesHarnessModelPicker` from `tmux_e2e_test.go`, but retain the `openai-codex` OAuth tmux flow. Remove the two deleted test registrations from `root_factories_fuzz_test.go`. Preserve provider model IDs such as `gpt-5.3-codex` when a retained test is specifically about provider model display.

- [ ] **Step 4: Neutralize generic source-rendering fixtures and comments**

Keep generic non-local source rendering and routing coverage, but rename its arbitrary Codex identities to `remote` or `local-daemon`. Rename `TestHubModelSessionHeaderShowsCodexMetadata` to `TestHubModelSessionHeaderShowsSourceMetadata` and `TestHubModelAgentsPickerShowsCodexSourceAndLiveSubagent` to `TestHubModelAgentsPickerShowsRemoteSourceAndLiveSubagent`; update the latter's registration in `root_factories_fuzz_test.go`. Apply the same source-neutral data to the dashboard, provider-fallback, status-line, truncation, notices, transcript widgets, and picker-panel cases in the files listed above. Comments about missing usage/capabilities must say “source-backed thread” or “source that omits the field”; `queue_send.go` must say “reference clients.” Preserve generic capability, source-label, remote-ref, notice, and tool-shape behavior; do not change `openai-codex` OAuth assertions or the transcript reducer's intentionally Codex-shaped AppWire tool test.

- [ ] **Step 5: Retain read-only source samples under neutral names**

In `tui_samples.go`, rename `codexLive` to `daemonLive`, `codex-readonly` to `source-readonly`, and use these exact values:

```go
Ref:         "local-daemon:01READONLY"
SessionID:   "01READONLY"
SourceLabel: "local-daemon"
Title:       "Local daemon read-only session"
Project:     "daemon-src"
Model:       "openai/gpt-5.5"
WorkingDir:  "/repo/daemon"
```

Use `local-daemon:01BUSY`, `local-daemon`, `Local daemon busy read-only sample`, and `daemon-src` for the busy fixture. Rename the interaction to `unsupported-source-actions-hidden-or-disabled` and use:

```go
Summary:    "Clear is not available for this source"
Cause:      "local-daemon did not advertise thread/clear"
NextAction: "open /help to see source-supported actions"
Source:     "local-daemon"
```

Keep the existing positive capability flags and read-only/queue rendering checks. Delete only `sampleSpawnOptions` row `codex-local`, `sampleRenders` row `spawn-codex`, its `sampleRenderFromRealWidget` case, and Codex harness/model-map initialization. Change `spawn-auth-required` indexing from `[2]` to `[1]`.

- [ ] **Step 6: Update sample tests without adding an absence assertion**

In `tui_samples_test.go`, require sources `evener` and `local-daemon`, require `source-readonly`, assert its `SourceLabel == "local-daemon"` and unsupported actions, and require `unsupported-source-actions-hidden-or-disabled`. Remove `spawn-codex` from the positive render roster. In `tui_samples_covtest_test.go`, delete its launch row and update dashboard, diagnostic, app-shell, and spawn negative-control strings to the neutral values while retaining non-empty real-widget assertions.

- [ ] **Step 7: Format and run the TUI suite**

Run:

```sh
git diff --name-only --diff-filter=ACM -- 'cmd/evener-tui/*.go' | sort -u | xargs gofmt -w
go test ./cmd/evener-tui -count=1
git diff --check
```

Expected: TUI tests exit zero; Evener model selection/plugin behavior and source-neutral read-only rendering remain covered.

- [ ] **Step 8: Commit the TUI removal**

```sh
git add cmd/evener-tui/hub_spawn.go cmd/evener-tui/hub_model.go \
  cmd/evener-tui/hub_commands.go cmd/evener-tui/hub_update.go \
  cmd/evener-tui/hub_commands_covtest_test.go \
  cmd/evener-tui/hub_spawn_covtest_test.go \
  cmd/evener-tui/hub_spawn_plugins_test.go \
  cmd/evener-tui/hub_update_fuzz_test.go \
  cmd/evener-tui/command_registry_fuzz_test.go \
  cmd/evener-tui/spawn_views_fuzz_test.go \
  cmd/evener-tui/hub_model_test.go cmd/evener-tui/tmux_e2e_test.go \
  cmd/evener-tui/details_drawer_test.go cmd/evener-tui/hub_agents_test.go \
  cmd/evener-tui/hub_notice_test.go cmd/evener-tui/hub_status_test.go \
  cmd/evener-tui/hub_transcript_widgets_test.go cmd/evener-tui/hub_types.go \
  cmd/evener-tui/notice_panel_covtest_test.go \
  cmd/evener-tui/question_overlay_test.go cmd/evener-tui/queue_send.go \
  cmd/evener-tui/root_factories_fuzz_test.go \
  cmd/evener-tui/internal/tuipick/picker_panel_test.go \
  cmd/evener-tui/tui_samples.go cmd/evener-tui/tui_samples_test.go \
  cmd/evener-tui/tui_samples_covtest_test.go
git diff --cached --check
git commit -m "refactor(tui): remove Codex harness affordances"
```

Expected: one TUI-only commit; no generated or provider files are staged.

---

### Task 5: Remove Hub Launcher and Source Runtime Branches

**Files:**
- Modify: `cmd/evener-hub/config.go`
- Modify: `cmd/evener-hub/internal/hubcore/config.go`
- Modify: `cmd/evener-hub/main.go`
- Modify: `cmd/evener-hub/web.go`
- Modify: `cmd/evener-hub/app_rpc.go`
- Modify: `cmd/evener-hub/app_models.go`
- Modify: `cmd/evener-hub/app_threadlist.go`
- Modify: `cmd/evener-hub/app_sources.go`
- Modify: `cmd/evener-hub/app_threadlifecycle.go`
- Modify: `cmd/evener-hub/app_jobs.go`
- Modify: `cmd/evener-hub/app_jobs_test.go`
- Modify: `cmd/evener-hub/app_transcripts.go`
- Modify: `cmd/evener-hub/app_compact.go`
- Modify: `cmd/evener-hub/app_vision_model.go`
- Modify: `cmd/evener-hub/app_rename.go`
- Modify: `cmd/evener-hub/app_relay.go`
- Modify: `cmd/evener-hub/app_session_resume.go`
- Modify: `cmd/evener-hub/web_api_tree.go`
- Modify: `cmd/evener-hub/web_api.go`
- Modify: `cmd/evener-hub/web_types.go`
- Modify: `cmd/evener-hub/main_test.go`
- Modify: `cmd/evener-hub/main_ephemeral_port_test.go`
- Modify: `cmd/evener-hub/main_hublock_test.go`
- Modify: `cmd/evener-hub/cov_main_bootstrap_pass6_fuzz_test.go`
- Modify: `cmd/evener-hub/cov_main_listener_fuzz_test.go`
- Modify: `cmd/evener-hub/cov_final_main_fuzz_test.go`
- Modify: `cmd/evener-hub/app_rpc_test.go`
- Modify: `cmd/evener-hub/app_rpc_item_paging_test.go`
- Modify: `cmd/evener-hub/app_aside_test.go`
- Modify: `cmd/evener-hub/app_session_delete_test.go`
- Modify: `cmd/evener-hub/app_threadread_test.go`
- Modify: `cmd/evener-hub/web_api_tree_lastgood_test.go`
- Modify: `cmd/evener-hub/web_test.go`
- Modify: `cmd/evener-hub/testmain_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/favorite_authority_test.go`
- Modify: `cmd/evener-hub/app_launch_test.go`
- Modify narrowly: `cmd/evener-hub/cov_exact_app_rpc_fuzz_test.go`, `cov_exact_lifecycle_tree_fuzz_test.go`, `cov_exact_tails_fuzz_test.go`, `cov_launch_models_plugins_fuzz_test.go`, `cov_plugins_fs_pass3_fuzz_test.go`, `cov_rpc_relay_pass3_fuzz_test.go`, `cov_rpc_sources_pass6_fuzz_test.go`, `cov_rpc_threads_helpers_fuzz_test.go`, `cov_threadlife_list_pass6_fuzz_test.go`, `cov_web_tree_session_fuzz_test.go`, `web_mutating_fuzz_test.go`
- Modify: `envvars/envvars.go`
- Modify: `testing-budget.json`
- Delete: `cmd/evener-hub/main_shutdowner_test.go`
- Delete: `cmd/evener-hub/codex_launch_test.go`
- Delete: `cmd/evener-hub/codex_launch_real_test.go`
- Delete tree: `cmd/evener-hub/internal/codexlaunch/`

**Interfaces:**
- Consumes: Task 1's removal-error metadata check and Task 3's settings code that no longer reads launcher fields.
- Produces: `Config` and `hubcore.WebConfig` with no `CodexSources`, `CodexLaunches`, or `CodexLauncher` fields.
- Produces: `mainDeps.serve func(context.Context, hubHTTPServer) error` and `serveHub(context.Context, hubHTTPServer) error`; shutdown still waits for HTTP-server shutdown.
- Produces: `sourceForThreadWithDeletionFence(cfg hubcore.WebConfig, sources *appsource.Registry, ref, threadID string) (appsource.Source, error)` and direct `sourceForThread` calls inside already-owned deletion locks.
- Produces: `sourceForModelHarness(sources *appsource.Registry, harness string) (appsource.Source, error)` and `launchHarnessDescriptors() []appwire.HarnessDescriptor` returning only Evener.
- Preserves: ordinary unsupported-source/harness errors, local spawning/resume, generic registry lookup, remote thread cache, deletion fences, and `RemoteSources` response shape.

- [ ] **Step 1: Remove configuration and construction fields**

Delete the `appsource` and `codexlaunch` imports and the two Codex fields from `Config`; retain Task 1's metadata tombstones as literal key strings. In `hubcore.WebConfig`, delete the two imports and three fields. In `main.go` and `web.go`, remove launcher construction and the three WebConfig assignments. Do not add a replacement launcher.

- [ ] **Step 2: Reduce shutdown ownership to the HTTP server**

Delete `hubShutdowner`, `codexShutdowner`, and `main_shutdowner_test.go`. Change the dependency and implementation signatures to:

```go
serve func(context.Context, hubHTTPServer) error
```

```go
func serveHub(ctx context.Context, srv hubHTTPServer) error {
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err := srv.ListenAndServe()
	if ctx.Err() != nil {
		<-shutdownDone
	}
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
```

Call `deps.serve(ctx, srv)`. Update every main/fuzz test seam listed above from three parameters to two. In `cov_main_listener_fuzz_test.go`, remove its companion fake and keep positive HTTP shutdown, server-error, and nil-error cases.

- [ ] **Step 3: Stop source registration and harness publication**

In `newHubSourceRegistry`, retain only the local-daemon source registration and delete the configured-source loop. Make:

```go
func launchHarnessDescriptors() []appwire.HarnessDescriptor {
	return []appwire.HarnessDescriptor{{ID: "evener", Label: "evener", Kind: "evener"}}
}
```

Change `sourceForModelHarness` to accept only the registry and harness, look up `sources.Source(harness)`, and return the existing ordinary `appwire.Unavailable("model list source is not available: " + harness)` error. Update its caller and every `launchHarnessDescriptors(cfg)` caller to the new signatures.

- [ ] **Step 4: Remove lazy launch from thread list, lifecycle, and tree**

Delete `ensureManagedCodexSourcesForList`, its test seam/call, `hubEnsureSource`, and all launcher branches. For non-local `thread/start`, use:

```go
		source, ok := sources.Source(sourceID)
		if !ok || source == nil {
			return appwire.ThreadStartResponse{}, appwire.Unavailable("spawn source is not available: " + sourceID)
		}
		return source.StartThread(ctx, params)
```

For resume/fork/read/tree paths, use ordinary source lookup and retain the current unsupported/unavailable error contracts. Remove `WebServer.ensureManagedCodexSources`, its refresh call, and every `CodexLauncher.EnsureSource` branch. Preserve generic iteration over `sources.All()` and the remote cache.

- [ ] **Step 5: Preserve deletion fences while removing managed-launch helpers**

Replace the outer helper with:

```go
func sourceForThreadWithDeletionFence(cfg hubcore.WebConfig, sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	return withDeletionTargetOwnership(cfg, ref, threadID, "", func() (appsource.Source, error) {
		return sourceForThread(sources, ref, threadID)
	})
}
```

Use it from unlocked read/list/job/transcript/tree callsites in `app_rpc.go`, `app_jobs.go`, `app_transcripts.go`, and `web_api_tree.go`. Inside closures that already called `withDeletionTargetOwnership`, replace `sourceForThreadWithManagedLaunchUnlocked` with direct `sourceForThread` in `app_rpc.go`, `app_compact.go`, `app_vision_model.go`, and `app_rename.go`. Delete `managedLaunchSourceIDForRef`. Make `hubKnowsRef` consult only `pastThreadForRead` and rewrite its comment to describe the local past-index retry gate.

Change the turn-start seam to the neutral lookup signature:

```go
var resolveTurnStartSource = sourceForThread
```

and update its production call and test replacements to `(sources, params.Ref, params.ThreadID)`.

- [ ] **Step 6: Stop producing Codex health and test-environment state**

In `web_api.go`, omit the `RemoteSources` assignment from the health response; do not delete the `appwire.HealthResponse.RemoteSources` field. Remove `EVENERHubSpawnedCodex` and its `allVars` entry from `envvars/envvars.go`. In `testmain_test.go`, delete the fake app-server re-execution branch, `TestReExecutedHelperLeavesNoThrowawayRoot`, and launcher-only comments. Retain the `codex` temp subdirectory and `CODEX_HOME` assignment because retained OpenAI OAuth tests use that hermetic external-state root. Remove only the `primeradiant.com/evener/cmd/evener-hub/internal/codexlaunch` entry from `testing-budget.json`.

- [ ] **Step 7: Delete launcher-only tests and narrow mixed hub suites**

Before deleting `codex_launch_test.go`, move `assertHubLaunchError` unchanged into new neutral `app_launch_test.go`; it remains shared by `app_threadlifecycle_plugin_test.go`, `app_rpc_test.go`, and `spawn_test.go`. Delete the launcher tree and the three launcher-only hub test files plus helpers `fakeCodexLaunchConfig`, launcher shutdown/running/wait functions, and launcher-only re-execution code. In mixed suites, delete tests/helpers that construct `CodexSource`, `CodexLauncher`, `CodexLaunchConfig`, or `CodexSources/CodexLaunches` fields. In `app_rpc_item_paging_test.go`, delete only the real Codex family from `TestAppRPCAtomicItemPagingEndToEnd`, `newRealCodexPagingSource`, and `codexPagingItems`; preserve live-daemon, ended-local, positioned-source, cursor-before-lookup, and generic native-success/legacy-identity coverage.

Rename arbitrary foreign-source IDs and refs from `codex`/`codex-local` to `remote` in `app_aside_test.go`, `app_jobs_test.go`, `app_session_delete_test.go`, `app_threadread_test.go`, `web_api_tree_lastgood_test.go`, `internal/hubcore/favorite_authority_test.go`, `cov_web_tree_session_fuzz_test.go`, and the generic cases in `app_rpc_item_paging_test.go`. Use `external` for arbitrary non-Evener harness values in `cov_rpc_threads_helpers_fuzz_test.go` and `web_mutating_fuzz_test.go`. Rewrite the stale bridge/source comments in `app_relay.go`, `app_session_resume.go`, and `web_types.go` in terms of non-local or source-backed threads. These are fixture/comment neutralizations only: retain generic registry routing, stale/unknown source errors, Evener harness listing, local lifecycle, deletion fences, tree refresh, HTTP shutdown, server-error propagation, and all associated assertions. Remove now-unused imports and retired scenarios from every mixed fuzz file listed under **Files**, including `cov_rpc_relay_pass3_fuzz_test.go`; do not replace them with negative-search tests.

- [ ] **Step 8: Format and run the hub/backend positive suite**

Run:

```sh
git diff --name-only --diff-filter=ACM -- 'cmd/evener-hub/*.go' 'cmd/evener-hub/internal/hubcore/*.go' 'envvars/*.go' | sort -u | xargs gofmt -w
go test ./cmd/evener-hub -count=1
go test ./envvars -count=1
go test -race ./cmd/evener-hub -run 'Local|Source|Harness|Thread|Tree|Shutdown|Serve|Config' -count=1
git diff --check
```

Expected: all commands exit zero. The hub package compiler is the deletion dependency oracle; ordinary unknown-source/harness tests prove stale identifiers use the generic error path.

- [ ] **Step 9: Commit the runtime and launcher removal**

```sh
git add cmd/evener-hub/config.go cmd/evener-hub/internal/hubcore/config.go \
  cmd/evener-hub/main.go cmd/evener-hub/web.go cmd/evener-hub/app_rpc.go \
  cmd/evener-hub/app_models.go cmd/evener-hub/app_threadlist.go \
  cmd/evener-hub/app_sources.go cmd/evener-hub/app_threadlifecycle.go \
  cmd/evener-hub/app_jobs.go cmd/evener-hub/app_jobs_test.go \
  cmd/evener-hub/app_transcripts.go \
  cmd/evener-hub/app_compact.go cmd/evener-hub/app_vision_model.go \
  cmd/evener-hub/app_rename.go cmd/evener-hub/app_relay.go \
  cmd/evener-hub/app_session_resume.go cmd/evener-hub/web_api_tree.go \
  cmd/evener-hub/web_api.go cmd/evener-hub/web_types.go \
  cmd/evener-hub/main_test.go \
  cmd/evener-hub/main_ephemeral_port_test.go cmd/evener-hub/main_hublock_test.go \
  cmd/evener-hub/cov_main_bootstrap_pass6_fuzz_test.go \
  cmd/evener-hub/cov_main_listener_fuzz_test.go \
  cmd/evener-hub/cov_final_main_fuzz_test.go \
  cmd/evener-hub/app_rpc_test.go cmd/evener-hub/app_rpc_item_paging_test.go \
  cmd/evener-hub/app_aside_test.go cmd/evener-hub/app_session_delete_test.go \
  cmd/evener-hub/app_threadread_test.go \
  cmd/evener-hub/web_api_tree_lastgood_test.go \
  cmd/evener-hub/web_test.go cmd/evener-hub/testmain_test.go \
  cmd/evener-hub/internal/hubcore/favorite_authority_test.go \
  cmd/evener-hub/app_launch_test.go \
  cmd/evener-hub/cov_exact_app_rpc_fuzz_test.go \
  cmd/evener-hub/cov_exact_lifecycle_tree_fuzz_test.go \
  cmd/evener-hub/cov_exact_tails_fuzz_test.go \
  cmd/evener-hub/cov_launch_models_plugins_fuzz_test.go \
  cmd/evener-hub/cov_plugins_fs_pass3_fuzz_test.go \
  cmd/evener-hub/cov_rpc_relay_pass3_fuzz_test.go \
  cmd/evener-hub/cov_rpc_sources_pass6_fuzz_test.go \
  cmd/evener-hub/cov_rpc_threads_helpers_fuzz_test.go \
  cmd/evener-hub/cov_threadlife_list_pass6_fuzz_test.go \
  cmd/evener-hub/cov_web_tree_session_fuzz_test.go \
  cmd/evener-hub/web_mutating_fuzz_test.go \
  envvars/envvars.go testing-budget.json
git add -u cmd/evener-hub/main_shutdowner_test.go \
  cmd/evener-hub/codex_launch_test.go cmd/evener-hub/codex_launch_real_test.go \
  cmd/evener-hub/internal/codexlaunch
git diff --cached --check
git commit -m "refactor(hub): remove Codex source and launcher runtime"
```

Expected: one buildable backend commit. The appsource adapter remains only until Task 6 removes its package-local implementation and coverage.

---

### Task 6: Delete the Codex Appsource Adapter and Fuzz Estate

**Files:**
- Delete: `cmd/evener-hub/internal/appsource/codex_cache.go`
- Delete: `cmd/evener-hub/internal/appsource/codex_input.go`
- Delete: `cmd/evener-hub/internal/appsource/codex_item_paging.go`
- Delete: `cmd/evener-hub/internal/appsource/codex_live_thread.go`
- Delete: `cmd/evener-hub/internal/appsource/codex_mapping.go`
- Delete: `cmd/evener-hub/internal/appsource/codex_source.go`
- Delete: `cmd/evener-hub/internal/appsource/codex_wire_types.go`
- Delete: `cmd/evener-hub/internal/appsource/codex_cache_coverage_test.go`
- Delete: `cmd/evener-hub/internal/appsource/codex_mapping_fuzz_test.go`
- Delete: `cmd/evener-hub/internal/appsource/codex_source_test.go`
- Delete: `cmd/evener-hub/internal/appsource/cov_rp_codex_errors_test.go`
- Replace: `cmd/evener-hub/internal/appsource/codex_item_paging_test.go`
- Create: `cmd/evener-hub/internal/appsource/local_daemon_item_paging_test.go`
- Create: `cmd/evener-hub/internal/appsource/item_paging_state_test.go`
- Modify: `cmd/evener-hub/internal/appsource/source.go`
- Modify: `cmd/evener-hub/internal/appsource/transport_seams_test.go`
- Modify: `cmd/evener-hub/internal/appsource/coverage_completion_test.go`
- Modify: `cmd/evener-hub/internal/appsource/appsource_program_fuzz_test.go`
- Modify: `cmd/evener-hub/internal/appsource/registry_test.go`
- Delete: `cmd/evener-hub/internal/appsource/cov_rhub_appsource_test.go`
- Delete corpus: `cmd/evener-hub/internal/appsource/testdata/fuzz/FuzzMapCodexTurn/`
- Modify: `scripts/fuzz/fuzz-targets.txt`

**Interfaces:**
- Consumes: Task 2's `transport.go`, which now owns every symbol needed by `local_daemon.go`.
- Produces: no `CodexSource` type or adapter; generic `Source`, `ItemCandidateSource`, `ItemReadCandidateSource`, `CombinedItemReadSource`, registry/cache/navigation, and local-daemon receiver methods remain unchanged.
- Preserves: `itemSnapshotStateCache`, local-daemon paging identity/continuation/cancellation tests, `FuzzAppSourceProgram`, and `github.com/coder/websocket`.
- Preserves outside this package: `FuzzCodexItemDecode` and its AppWire corpus/golden.

- [ ] **Step 1: Move source-neutral paging tests before deleting their old file**

Create `local_daemon_item_paging_test.go` with these existing functions copied intact from `codex_item_paging_test.go`:

```text
TestLocalItemPagingPreservesCursorAcrossSlidingNewestWindow
TestLocalItemPagingPreservesCursorAcrossDisjointAppendedNewestWindow
TestLocalItemPagingPreservesCursorAcrossUnchangedBoundedToCompleteRead
TestLocalItemPagingIdentityBindsEveryItemPosition
TestLocalItemPagingRejectsNonIncreasingCandidatesBeforeStateMutation
newLocalItemReadConversionSource
positionedItemReadResponse
positionedItemReadResponseFor
```

Create `item_paging_state_test.go` with these source-neutral state tests and helper copied intact:

```text
TestItemSnapshotStateFingerprintTailIsFixedAndBounded
TestItemSnapshotStateTypeContainsNoTranscriptPayloadTypes
typeContains
retainedPagingStateCount
```

Keep their package declaration, imports, assertions, and current local-daemon fixtures. Do not weaken cursor identity, ordering, state-mutation, or bounded-memory assertions.

- [ ] **Step 2: Remove only Codex receivers and neutralize shared comments**

In `source.go`, delete the three methods whose receiver is `*CodexSource`; retain all generic interfaces/result types and every `*LocalDaemonSource` method. Rewrite the positioned-candidate comment to describe a private source contract and the stream comment to describe the retained local-daemon upstream stream. Do not change `itemSnapshotStateCache` behavior added by the paging repairs.

- [ ] **Step 3: Narrow transport and program-fuzz coverage**

In `transport_seams_test.go`, rename `fuzzScenarioSourceDialSeamsPreserveCallerCancellation` to `fuzzScenarioLocalDaemonDialSeamPreservesCallerCancellation`, remove its Codex half, and retain both local `withClient` and `SubscribeThread` cancellation checks, scripted transport helpers, `rendezvousEntry`, and every local-daemon case. Delete `fuzzScenarioCodexConnectHandshakeFailures`, `fuzzScenarioCodexRPCFailureAndValidationBranches`, and `fuzzScenarioCodexRPCResponseErrors`. From `fuzzScenarioCodexInitialAndResumedTurnFailures`, move the `localDaemonDialError(context.DeadlineExceeded)` assertion into `fuzzScenarioLocalDaemonRemainingTransportBranches`, then delete the rest of the Codex scenario.

In `coverage_completion_test.go`, keep local-daemon scenarios and `emptyHandler`; delete its four Codex scenarios. Move `fuzzScenarioRegistryRemove` from `cov_rhub_appsource_test.go` into `registry_test.go`, change the arbitrary source ID in `fuzzScenarioRegistryAllReturnsSourcesInIDOrder` from `codex` to `daemon`, then delete the former file. In `appsource_program_fuzz_test.go`, retain the renamed local dial scenario and all local-daemon/registry/helper entries; remove every Codex or mapping-only entry. Keep `FuzzAppSourceProgram`.

- [ ] **Step 4: Delete adapter implementation, adapter-only tests, and mixed Codex cases**

Delete the seven production files and four adapter-only tests listed above. Delete the remainder of `codex_item_paging_test.go` after the neutral tests have moved. In hub item-paging tests already narrowed in Task 5, ensure `newRealCodexPagingSource` and only its real-adapter cases are gone; local paging cases remain.

- [ ] **Step 5: Remove obsolete fuzz targets and corpus**

Delete these exact rows from `scripts/fuzz/fuzz-targets.txt`:

```text
FuzzMapCodexTurn
FuzzParseCodexEndpoint
FuzzCodexLaunchBehaviorProgram
```

Delete all eight tracked seeds under `internal/appsource/testdata/fuzz/FuzzMapCodexTurn/`. The launcher corpus was deleted with its package in Task 5. Do not remove `FuzzCodexItemDecode` or any seed under `appwire/testdata/fuzz/FuzzCodexItemDecode/`.

- [ ] **Step 6: Format and prove retained appsource behavior**

Run:

```sh
git diff --name-only --diff-filter=ACM -- 'cmd/evener-hub/internal/appsource/*.go' | sort -u | xargs gofmt -w
go test ./cmd/evener-hub/internal/appsource -count=1
go test -race ./cmd/evener-hub/internal/appsource -count=1
make fuzz-registry-check
make fuzz-seeds
git diff --check
```

Expected: all commands exit zero. Local-daemon connection, transport logging, caller cancellation, snapshot identity, continuation, cursor-cycle, bounded paging, registry, and navigation tests remain green.

- [ ] **Step 7: Commit the adapter deletion**

```sh
git add cmd/evener-hub/internal/appsource/transport_seams_test.go \
  cmd/evener-hub/internal/appsource/coverage_completion_test.go \
  cmd/evener-hub/internal/appsource/appsource_program_fuzz_test.go \
  cmd/evener-hub/internal/appsource/registry_test.go \
  cmd/evener-hub/internal/appsource/source.go \
  cmd/evener-hub/internal/appsource/local_daemon_item_paging_test.go \
  cmd/evener-hub/internal/appsource/item_paging_state_test.go \
  scripts/fuzz/fuzz-targets.txt
git add -u cmd/evener-hub/internal/appsource/codex_cache.go \
  cmd/evener-hub/internal/appsource/codex_input.go \
  cmd/evener-hub/internal/appsource/codex_item_paging.go \
  cmd/evener-hub/internal/appsource/codex_live_thread.go \
  cmd/evener-hub/internal/appsource/codex_mapping.go \
  cmd/evener-hub/internal/appsource/codex_source.go \
  cmd/evener-hub/internal/appsource/codex_wire_types.go \
  cmd/evener-hub/internal/appsource/codex_cache_coverage_test.go \
  cmd/evener-hub/internal/appsource/codex_mapping_fuzz_test.go \
  cmd/evener-hub/internal/appsource/codex_source_test.go \
  cmd/evener-hub/internal/appsource/cov_rp_codex_errors_test.go \
  cmd/evener-hub/internal/appsource/codex_item_paging_test.go \
  cmd/evener-hub/internal/appsource/cov_rhub_appsource_test.go \
  cmd/evener-hub/internal/appsource/testdata/fuzz/FuzzMapCodexTurn
git diff --cached --check
git commit -m "refactor(appsource): remove Codex agent adapter"
```

Expected: one appsource/fuzz commit; shared transport and local paging files remain.

---

### Task 7: Update Active Documentation and Operative Copy

**Files:**
- Modify: `docs/evener-hub.md`
- Modify: `docs/evener-hub-remote-operations.md`
- Modify: `docs/developing-evener/environment.md`
- Modify: `docs/developing-evener/testing.md`
- Modify: `docs/developing-evener/conventions/naming.md`
- Modify: `appwire/client.go`
- Modify: `appwire/client_test.go`
- Modify: `appwire/doc.go`
- Modify: `appwire/types.go`
- Modify: `appwire/types_test.go`
- Modify: `server/server.go`
- Modify: `server/appwire_failure_count_test.go`
- Modify: `cmd/evener-hub/doc_serve.go`
- Modify: `cmd/evener-hub/doc_serve_test.go`
- Modify: `test/scenarios/INDEX.md`
- Modify: `test/scenarios/README.md`
- Modify: `test/scenarios/local-sidebar-url-stability.md`
- Modify: `test/scenarios/sidebar-rename-live-and-ended.md`
- Modify: `test/scenarios/tui-interrupt-live-turn.md`
- Modify: `test/scenarios/web-model-switch-mid-session.md`
- Modify: `test/scenarios/worktree-dispose/README.md`
- Modify: `test/scenarios/written-image-inline-after-reload.md`
- Modify: `test/scenarios/reconnect-auto-resume.md`
- Modify: `test/scenarios/web-goal-set-and-complete.md`
- Delete: `test/scenarios/codex-sidebar-drive.md`
- Delete: `test/scenarios/codex-sidebar-open.md`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/TasksPanel.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/sessionErrors.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/taskData.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/taskData.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.liveCapabilities.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/harnessModels.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/harnessModels.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/MobileSettingRows.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/schema.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/protocol/model.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/reducer.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/reducer.test.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/errors.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/errors.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/RailRow.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/threads.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/settingsOverview.ts`
- Modify: `cmd/evener-hub/frontend/src/widgets/modelCatalog/catalogClient.test.ts`

**Interfaces:**
- Consumes: Tasks 3-6's resulting product surface.
- Produces: current user/developer docs describe only Evener and local-daemon operation; source-neutral comments describe generic non-local AppWire data without claiming a Codex bridge.
- Preserves: every historical design/spec/plan/proof, all provider/OAuth/model documentation, `.codex/instructions.md` profile behavior, and `.codex-plugin` ecosystem documentation.

- [ ] **Step 1: Remove active hub configuration and operating instructions**

In `docs/evener-hub.md`, make the overview describe local `evener serve` daemons, delete the `[[codex_sources]]` and `[[codex_launches]]` configuration blocks, remove the manual Codex launch step and launcher-shutdown claim, and keep the OpenAI Codex provider/OAuth/model sections. In `docs/evener-hub-remote-operations.md`, delete the complete “Codex App-Server Sources” section, its manual verification step, and the managed-process shutdown sentence. Change the graceful-shutdown statement to say the hub stops active connections and the HTTP server.

- [ ] **Step 2: Remove environment and scenario guidance**

Delete only the `EVENER_HUB_SPAWNED_CODEX` row from `docs/developing-evener/environment.md`; retain `OPENAI_CODEX_BASE_URL`. In `docs/developing-evener/testing.md`, remove `codexlaunch` from the deleted package list while leaving the OpenAI Codex provider E2E section intact.

Delete the two Codex-session scenario files and their index entries. In `written-image-inline-after-reload.md`, replace the obsolete `remote/codex` example with a generic remote source. In `reconnect-auto-resume.md`, delete the four-line Codex managed-launch follow-up note. In `web-goal-set-and-complete.md`, describe the negative case as any source that does not advertise `goal/set`. In `local-sidebar-url-stability.md`, remove the historical Codex-sidebar spec citation and compare the qualified local URL only with generic source-qualified refs. In `sidebar-rename-live-and-ended.md`, say “non-local source-backed rows.” In `web-model-switch-mid-session.md`, retain only the fact that Evener daemons do not populate `ActiveFlags`. In `tui-interrupt-live-turn.md`, say the behavior matches other coding-agent interfaces. In both scenario READMEs, say “an AI agent” without naming executors. Do not delete provider-login scenarios or change model examples whose names happen to contain “Codex.”

- [ ] **Step 3: Make active naming and source comments neutral**

In `docs/developing-evener/conventions/naming.md`, remove the `CodexSource` example and describe AppWire implementations generically while retaining camelCase compatibility guidance. Apply these wording contracts to code comments without changing types or runtime behavior:

```text
AppWire connects Evener clients, hubs, and session sources.
Non-local/source-backed threads may omit capabilities their source does not advertise.
The hub preserves source-provided compatible fields without claiming a Codex bridge.
```

In `appwire/doc.go`, say AppWire connects Evener clients, hubs, and session sources. In `appwire/client.go` and its test, describe a large initial-turn replay without naming its former producer. In `appwire/types.go`, `appwire/types_test.go`, `server/server.go`, and `server/appwire_failure_count_test.go`, replace comparisons to Codex threads with “source-backed threads that omit the field”; retain `CodexErrorInfo`, Codex item/vocabulary tests, camelCase interoperability comments, and the `thread/turns/items/list` catalog description because those protect generic AppWire compatibility.

In `doc_serve.go`, describe `/doc/image` as local-session-only rather than contrasting it with Codex. In `doc_serve_test.go`, rename the arbitrary remote source fixture and source-qualified ref from `codex` to `remote` while preserving the test's non-local rejection behavior.

- [ ] **Step 4: Neutralize frontend source fixtures and comments**

Replace Codex-source examples in TasksPanel, task-data, live-capability, protocol model/reducer, rail-row, and threads-store tests/comments with `remote`, `source-backed`, or “a source that omits the capability.” Preserve every unsupported-action, omitted-field merge, source-label, capability, and error-propagation assertion. In the generic spawn tests, use harness ID/kind `external` instead of `codex-cli`/`codex`; keep the generic `HarnessDescriptor` behavior and model/plugin assertions. In `schema.test.ts`, use `driverSupport: { external: true }`. In `errors.test.ts`, use an arbitrary external launch error while preserving the `hubLaunch` mapping. In `catalogClient.test.ts`, use harness `external`. Remove the dead `internal/codexlaunch` path from `errors.ts`. In `settingsOverview.ts`, describe the remaining five overview-backed sections. Preserve provider/OAuth fixtures containing `openai-codex`, JSON names such as `codexErrorInfo`, and tests that intentionally document Codex-shaped wire compatibility.

- [ ] **Step 5: Format edited code and inspect documentation diff**

Run:

```sh
gofmt -w appwire/client.go appwire/client_test.go appwire/doc.go \
  appwire/types.go appwire/types_test.go server/server.go \
  server/appwire_failure_count_test.go cmd/evener-hub/doc_serve.go \
  cmd/evener-hub/doc_serve_test.go
cd cmd/evener-hub/frontend
npx biome check --write \
  src/panes/session/chrome/TasksPanel.tsx src/panes/session/chrome/TasksPanel.test.tsx \
  src/panes/session/chrome/sessionErrors.ts \
  src/panes/session/chrome/taskData.ts src/panes/session/chrome/taskData.test.ts \
  src/panes/session/composer/Composer.liveCapabilities.test.tsx \
  src/panes/spawn/harnessModels.ts src/panes/spawn/harnessModels.test.ts \
  src/panes/spawn/MobileSettingRows.test.tsx src/panes/spawn/schema.test.ts \
  src/panes/spawn/Spawn.test.tsx \
  src/protocol/model.ts src/protocol/reducer.ts src/protocol/reducer.test.ts \
  src/protocol/errors.ts src/protocol/errors.test.ts \
  src/shell/rail/RailRow.test.tsx \
  src/stores/threads.ts src/stores/threads.test.ts src/stores/settingsOverview.ts \
  src/widgets/modelCatalog/catalogClient.test.ts
cd ../../../
git diff --check
git diff -- docs/evener-hub.md docs/evener-hub-remote-operations.md \
  docs/developing-evener/environment.md docs/developing-evener/testing.md \
  docs/developing-evener/conventions/naming.md \
  cmd/evener-hub/doc_serve.go cmd/evener-hub/doc_serve_test.go \
  test/scenarios/INDEX.md test/scenarios/README.md \
  test/scenarios/local-sidebar-url-stability.md \
  test/scenarios/sidebar-rename-live-and-ended.md \
  test/scenarios/tui-interrupt-live-turn.md \
  test/scenarios/web-model-switch-mid-session.md \
  test/scenarios/worktree-dispose/README.md \
  test/scenarios/written-image-inline-after-reload.md \
  test/scenarios/reconnect-auto-resume.md test/scenarios/web-goal-set-and-complete.md
```

Expected: no whitespace errors; the diff changes active instructions only and leaves historical documents and provider/model guidance untouched.

- [ ] **Step 6: Commit active documentation and copy**

```sh
git add docs/evener-hub.md docs/evener-hub-remote-operations.md \
  docs/developing-evener/environment.md docs/developing-evener/testing.md \
  docs/developing-evener/conventions/naming.md \
  cmd/evener-hub/doc_serve.go cmd/evener-hub/doc_serve_test.go \
  test/scenarios/INDEX.md test/scenarios/README.md \
  test/scenarios/local-sidebar-url-stability.md \
  test/scenarios/sidebar-rename-live-and-ended.md \
  test/scenarios/tui-interrupt-live-turn.md \
  test/scenarios/web-model-switch-mid-session.md \
  test/scenarios/worktree-dispose/README.md \
  test/scenarios/written-image-inline-after-reload.md \
  test/scenarios/reconnect-auto-resume.md test/scenarios/web-goal-set-and-complete.md \
  appwire/client.go appwire/client_test.go appwire/doc.go appwire/types.go \
  appwire/types_test.go server/server.go server/appwire_failure_count_test.go \
  cmd/evener-hub/frontend/src/panes/session/chrome/TasksPanel.tsx \
  cmd/evener-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx \
  cmd/evener-hub/frontend/src/panes/session/chrome/sessionErrors.ts \
  cmd/evener-hub/frontend/src/panes/session/chrome/taskData.ts \
  cmd/evener-hub/frontend/src/panes/session/chrome/taskData.test.ts \
  cmd/evener-hub/frontend/src/panes/session/composer/Composer.liveCapabilities.test.tsx \
  cmd/evener-hub/frontend/src/panes/spawn/harnessModels.ts \
  cmd/evener-hub/frontend/src/panes/spawn/harnessModels.test.ts \
  cmd/evener-hub/frontend/src/panes/spawn/MobileSettingRows.test.tsx \
  cmd/evener-hub/frontend/src/panes/spawn/schema.test.ts \
  cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx \
  cmd/evener-hub/frontend/src/protocol/model.ts \
  cmd/evener-hub/frontend/src/protocol/reducer.ts \
  cmd/evener-hub/frontend/src/protocol/reducer.test.ts \
  cmd/evener-hub/frontend/src/protocol/errors.ts \
  cmd/evener-hub/frontend/src/protocol/errors.test.ts \
  cmd/evener-hub/frontend/src/shell/rail/RailRow.test.tsx \
  cmd/evener-hub/frontend/src/stores/threads.ts \
  cmd/evener-hub/frontend/src/stores/threads.test.ts \
  cmd/evener-hub/frontend/src/stores/settingsOverview.ts
git add cmd/evener-hub/frontend/src/widgets/modelCatalog/catalogClient.test.ts
git add -u test/scenarios/codex-sidebar-drive.md test/scenarios/codex-sidebar-open.md
git diff --cached --check
git commit -m "docs: remove external Codex agent guidance"
```

Expected: one documentation/copy commit with no historical specs/plans/proofs staged.

---

### Task 8: Regenerate, Classify, Review, and Run Every Gate

**Files:**
- Verify generated: `docs/appwire-protocol.md`
- Verify generated: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`
- Verify retained: `appwire/codex_compat_test.go`
- Verify retained: `appwire/item_fuzz_test.go`
- Verify retained: `appwire/testdata/fuzz/FuzzCodexItemDecode/`
- Verify retained: `llm/providers/tokenauth/codex.go`
- Verify retained: `llm/registry/data/providers_overlay.toml`
- Verify retained: agent `.codex` and `.codex-plugin` compatibility paths

**Interfaces:**
- Consumes: all prior task commits.
- Produces: classified review evidence for every remaining case-insensitive Codex match, fresh focused/full gate output, exact final commit range, and a clean worktree.
- Produces no permanent negative-search test or compatibility shim.

- [ ] **Step 1: Run formatting and generation one final time**

Run:

```sh
git diff --name-only 565f7f19411fbd284080ba21d2c9acda2659e2ef..HEAD --diff-filter=ACM -- '*.go' | sort -u | xargs gofmt -w
make generate
make lint-generated
git diff --check
```

Expected: `make lint-generated` and `git diff --check` exit zero. If formatting or generation changes tracked files, stage only the named changed files and commit them as `chore: refresh Codex removal outputs` before continuing.

- [ ] **Step 2: Classify every remaining Codex reference without committing the search**

Run:

```sh
git grep -n -i codex >"${TMPDIR:-/tmp}/evener-codex-after.txt"
git grep -n -E 'codex_sources|codex_launches|launch-codex|EVENER_HUB_SPAWNED_CODEX|CodexSource|CodexLauncher|Kind:[[:space:]]*"codex"' || true
cut -d: -f1 "${TMPDIR:-/tmp}/evener-codex-after.txt" | sort -u
```

Review every line into exactly one allowed class: provider/OAuth/model; generic AppWire compatibility; `.codex`/`.codex-plugin`; Task 1's retired-config tombstone and behavior test; or historical documentation. Any line that configures, launches, connects to, registers, advertises, starts, resumes, forks, or shuts down an external Codex session must be removed in the owning task's code and committed before gates. Do not add this grep or its output to the repository.

- [ ] **Step 3: Run the focused positive checks**

Run:

```sh
go test ./cmd/evener-hub ./cmd/evener-hub/internal/appsource ./appwire ./cmd/evener-tui -count=1
go test -race ./cmd/evener-hub/internal/appsource ./cmd/evener-hub -count=1
make test-web
make fuzz-registry-check
make fuzz-seeds
make lint-generated
```

Expected: every command runs to completion and exits zero. Confirm the output includes passing `TestCodexAppServerCoreFixtureCompatibility`, `TestCodexItemDecodeGolden`, and `TestMethodCatalogWellFormed` when running the explicit retained-wire command below:

```sh
go test ./appwire -run '^(TestCodexAppServerCoreFixtureCompatibility|TestCodexItemDecodeGolden|TestMethodCatalogWellFormed)$' -count=1 -v
```

- [ ] **Step 4: Run the canonical full gates serially**

Run:

```sh
make merge-approval-gate
make test-race
make test-web-browser
make fuzz
```

Expected: each command completes and exits zero. Treat any failure, timeout, launch error, sandbox denial, flake, or warning-backed nonzero status as a real unresolved gate; diagnose and fix its root cause rather than weakening or skipping coverage.

- [ ] **Step 5: Perform the final repository review**

Run:

```sh
git log --oneline --decorate -8
git diff --stat 565f7f19411fbd284080ba21d2c9acda2659e2ef..HEAD
git diff --check 565f7f19411fbd284080ba21d2c9acda2659e2ef..HEAD
git status --short
```

Expected: the commit list contains the scoped task commits; the diff matches the approved removal boundary; `git diff --check` exits zero; `git status --short` prints nothing.

- [ ] **Step 6: Record delivery evidence**

Report the exact commit hashes, changed/deleted paths, generated-output status, classified-grep categories, focused command results, full gate results, and clean status. Explicitly name each acceptance criterion: adapter/package deletion; config errors; runtime/settings/UI/TUI removal; metadata/docs removal; local-daemon preservation; provider/wire preservation; generated freshness; classified references; and all gates.
