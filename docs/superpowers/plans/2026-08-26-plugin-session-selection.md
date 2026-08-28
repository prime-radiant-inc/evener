# Session Plugin Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let web, mobile, TUI, and direct CLI users inspect the plugins a new session would load and restrict that session to an exact manifest-name allow-list without changing global plugin state.

**Architecture:** A single `internal/plugins.Manager.ResolveForLaunch` function becomes the source of truth for candidate enumeration, validation, manifest-name deduplication, diagnostics, and allow-list selection. A presence-sensitive `enabledPlugins` launch field preserves omitted versus explicit-empty semantics through appwire, launch merging, argv, direct CLI, and `serve`; the hub exposes the same resolver through `evener/plugin/preview` and revalidates before spawn. The UIs keep default and explicit selection modes separate, then persist only the resolver's selected directories through the existing session snapshot.

**Tech Stack:** Go 1.26, standard `flag` package, appwire JSON-RPC/code generation, Bubble Tea/Lip Gloss, React 19, TypeScript 6, Zustand, CSS modules, Vitest/Testing Library, Biome.

**Spec:** `docs/superpowers/specs/2026-08-26-plugin-session-toggle-design.md`

## Global Constraints

- Read `docs/developing-evener/testing.md` before changing tests.
- Default tests must not use provider credentials, network access, quota, live model behavior, sleeps, or ambient developer state.
- Keep plugin identity keyed by validated manifest name; marketplace refs and paths are display metadata only.
- Omitted `enabledPlugins` means all otherwise-loadable plugins; explicit `[]` means none; a non-empty value means exactly those names.
- Globally disabled plugins remain unavailable; explicit plugin directories retain first-wins precedence over registry entries.
- Apply selection before sandbox infrastructure roots, session IDs, transcripts, hooks, MCP startup, or any durable state.
- Persist selected directories through the existing `SessionConfig.PluginDirs` / `schema.ConfigSnapshot.PluginDirs`; do not add a snapshot field or migration.
- Keep `plugin_dirs` append semantics, global plugin mutation RPCs, and `evener/command/list`'s global wire contract unchanged.
- Preview may parse plugin files but must not execute hooks, start MCP processes, register tools, or create session state.
- Before frontend gates, run `npx biome check --write` on every touched file under `frontend/src/`.
- Stage named paths only for every commit; never use `git add .` or `git add -A`.
- Run each command block from the repository root unless that block begins with an explicit `cd`; command blocks are independent and do not inherit a prior block's directory.

## File and interface map

### New focused files

- `internal/plugins/resolve.go` — shared launch candidate/result types and `Manager.ResolveForLaunch`.
- `internal/plugins/resolve_test.go` — resolver ordering, diagnostics, nil/empty/named selection, and strict errors.
- `cmd/evener/plugin_selection_flag.go` — presence-aware comma-separated CLI flag parsing.
- `cmd/evener/plugin_selection_flag_test.go` — omitted/empty/named/malformed parsing.
- `cmd/evener-hub/app_plugin_preview_test.go` — preview mapping, no-side-effect, stale-selection, and pre-spawn tests.
- `agent/plugin_selection_snapshot_test.go` — existing snapshot field across resume/fork/aside/subagent/delegate paths.
- `cmd/evener-hub/frontend/src/panes/spawn/pluginSelectionState.ts` — pure default/explicit selection reconciliation and launch-override merge.
- `cmd/evener-hub/frontend/src/panes/spawn/pluginSelectionState.test.ts` — pure state contract.
- `cmd/evener-hub/frontend/src/panes/spawn/usePluginPreview.ts` — debounced, stale-safe preview orchestration.
- `cmd/evener-hub/frontend/src/panes/spawn/usePluginPreview.test.tsx` — request key, retry, notification revision, and stale response tests.
- `cmd/evener-hub/frontend/src/panes/spawn/PluginSelectionPanel.tsx` — shared candidate list, filter, switches, All/None, diagnostics, and selection errors.
- `cmd/evener-hub/frontend/src/panes/spawn/PluginSelectionPanel.test.tsx` — panel behavior and accessibility.
- `cmd/evener-hub/frontend/src/panes/spawn/pluginSelection.module.css` — desktop disclosure and mobile sheet layout.
- `cmd/evener-tui/internal/launchconfig/plugins_for_launch_panel.go` — filterable multi-select launch overlay.
- `cmd/evener-tui/internal/launchconfig/plugins_for_launch_panel_test.go` — overlay keys, filtering, apply/cancel, scrolling, and width.
- `cmd/evener-tui/hub_spawn_plugins_test.go` — spawn field, preview refresh, payload, and reset integration.

### Existing files changed by responsibility

- `internal/plugins/enabled.go`, `internal/plugins/enabled_test.go` — compatibility wrapper over the shared resolver.
- `appwire/types.go`, `appwire/protocol.go`, `appwire/client.go`, `appwire/types_test.go`, `appwire/wiretypes_fuzz_test.go` — presence-sensitive launch field and preview protocol.
- `cmd/evener-hub/internal/launchconfig/{types,wire,merge,args,schema,resolver}.go` and `wire_test.go`, `merge_test.go`, `args_test.go`, `schema_test.go`, `resolver_test.go` — internal pointer representation, replacement merge, provenance, argv, custom schema kind, and repository-field rejection.
- `cmd/evener-hub/app_launch.go`, `cmd/evener-hub/app_launch_test.go` — reject persistence outside the launch layer.
- `cmd/evener/{main,run,serve,plugincmd}.go`, `plugin_selection_flag.go`, `plugin_selection_flag_test.go`, `main_test.go`, `run_test.go`, `serve_test.go`, and `plugincmd_test.go` — direct run/serve selection plus `plugin list --effective`.
- `cmd/evener-hub/app_plugins.go`, `app_plugin_preview_test.go`, `app_rpc.go`, `app_rpc_test.go`, `app_threadlifecycle.go`, and `app_threadlifecycle_plugin_test.go` — preview RPC, pre-spawn validation, and default command-catalog resolution.
- `cmd/evener-hub/frontend/src/{protocol/types.gen.ts,stores/extensions.ts,stores/extensions.test.ts}` — generated wire types and plugin notification revision.
- `cmd/evener-hub/frontend/src/panes/spawn/{Spawn.tsx,Spawn.test.tsx,MobileSettingRows.tsx,MobileSettingRows.test.tsx,schema.ts,schema.test.ts}` — launch orchestration and responsive placement.
- `cmd/evener-hub/frontend/src/shell/palette/{commands.ts,commands.edge.test.ts,CommandPalette.tsx,CommandPalette.test.tsx}` — active-session plugin-command filtering.
- `cmd/evener-tui/internal/launchconfig/plugins_client.go`, `plugins_client_covtest_test.go`, `launch_schema.go`, `launch_schema_test.go`, `plugins_for_launch_panel.go`, and `plugins_for_launch_panel_test.go` — preview client, custom-schema exclusion, and multi-select overlay.
- `cmd/evener-tui/hub_model.go`, `hub_spawn.go`, `hub_update.go`, `hub_update_config.go`, `hub_notifications.go`, `hub_spawn_plugins_test.go`, and `tmux_e2e_test.go` — TUI preview state, focus, overlay dispatch, notification refresh, one-shot payload, and live rendering coverage.
- `README.md`, `docs/evener-hub.md`, `docs/appwire-protocol.md` — user guidance and generated protocol reference.

## Spec coverage map

| Approved spec area | Implemented and proved by |
|---|---|
| Candidate sources, manifest identity, validation, deduplication, diagnostics | Task 1 |
| Omitted/empty/named presence, launch-only merge, provenance, argv, persistence rejection | Task 2 |
| Direct CLI, `serve`, effective listing, resume conflicts, default fail-soft compatibility | Task 3 |
| Preview protocol, launch-layer resolution, authoritative pre-spawn revalidation, global command catalog | Task 4 |
| No preview/startup side effects, excluded contributions, sandbox roots, snapshot/resume/fork/aside/child/delegate behavior | Task 5 |
| Web default/explicit state, debounce, stale requests, retry, notification reconciliation, one-shot reset | Task 6 |
| Desktop disclosure, mobile sheet, accessibility, diagnostics, geometry | Task 7 |
| Active-session command suggestion filtering without changing the global RPC | Task 8 |
| TUI field, overlay, filtering, keys, refresh, explicit empty payload, width | Task 9 |
| User documentation, generated output, formatting, unit, browser, lint, vet, and full tests | Task 10 |

---

### Task 1: Build the shared plugin launch resolver

**Files:**
- Create: `internal/plugins/resolve.go`
- Create: `internal/plugins/resolve_test.go`
- Modify: `internal/plugins/enabled.go`
- Modify: `internal/plugins/enabled_test.go`

**Interfaces:**
- Consumes: `agent/plugin.Load`, `Manager.List`, explicit directory order, and installed registry metadata.
- Produces:

```go
type LaunchPluginSource string

const (
    LaunchPluginSourceDirectory LaunchPluginSource = "directory"
    LaunchPluginSourceInstalled LaunchPluginSource = "installed"
)

type LaunchPluginCandidate struct {
    Name, Version, Description string
    Source                     LaunchPluginSource
    Marketplace, Path          string
    Selected                   bool
    SkillCount, AgentCount     int
    CommandCount, HookCount    int
    MCPCount                   int
}

type LaunchPluginDiagnostic struct {
    Name    string             `json:"name,omitempty"`
    Path    string             `json:"path,omitempty"`
    Message string             `json:"message"`
    Source  LaunchPluginSource `json:"source,omitempty"`
}

type PluginSelectionError struct {
    Name   string `json:"name"`
    Reason string `json:"reason"`
}

type LaunchPluginResolution struct {
    Candidates      []LaunchPluginCandidate
    SelectedDirs    []string
    Diagnostics     []LaunchPluginDiagnostic
    SelectionErrors []PluginSelectionError
}

func (m *Manager) ResolveForLaunch(explicitDirs []string, enabledNames *[]string) (LaunchPluginResolution, error)
func (r LaunchPluginResolution) ValidateSelection() error
```

On registry/infrastructure failure, `ResolveForLaunch` returns the explicit-dir
portion of `LaunchPluginResolution` together with the error. Preview always
reports that error because it cannot claim a complete inventory. Startup and
the global command catalog preserve existing fail-soft behavior only when
`enabledNames == nil`; an explicit allow-list makes the same error fatal.

- [ ] **Step 1: Write failing resolver contract tests**

Create table-driven tests covering: explicit-before-registry order, explicit winner over a same-name registry entry, deterministic registry order, globally disabled omission, globally enabled broken diagnostics, duplicate losers, omitted selection, explicit empty selection, named selection without load-order changes, unknown selected names, newly invalid selected names, source metadata, component counts, and a registry failure that returns explicit candidates plus an error.

```go
func TestResolveForLaunch_SelectionPresence(t *testing.T) {
    root := t.TempDir()
    explicitA := filepath.Join(root, "alpha")
    explicitB := filepath.Join(root, "beta")
    writePlugin(t, explicitA, "alpha", nil)
    writePlugin(t, explicitB, "beta", nil)
    mgr := NewManager(filepath.Join(root, "store"))

    all, err := mgr.ResolveForLaunch([]string{explicitA, explicitB}, nil)
    if err != nil { t.Fatal(err) }
    gotNames := []string{all.Candidates[0].Name, all.Candidates[1].Name}
    if diff := cmp.Diff([]string{"alpha", "beta"}, gotNames); diff != "" { t.Fatal(diff) }
    if diff := cmp.Diff([]string{explicitA, explicitB}, all.SelectedDirs); diff != "" { t.Fatal(diff) }

    none := []string{}
    empty, err := mgr.ResolveForLaunch([]string{explicitA, explicitB}, &none)
    if err != nil { t.Fatal(err) }
    if len(empty.SelectedDirs) != 0 { t.Fatalf("selected = %v", empty.SelectedDirs) }
    if err := empty.ValidateSelection(); err != nil { t.Fatal(err) }

    names := []string{"beta"}
    one, err := mgr.ResolveForLaunch([]string{explicitA, explicitB}, &names)
    if err != nil { t.Fatal(err) }
    if diff := cmp.Diff([]string{explicitB}, one.SelectedDirs); diff != "" { t.Fatal(diff) }
}
```

- [ ] **Step 2: Run the resolver tests and verify red**

Run:

```bash
go test ./internal/plugins -run 'TestResolveForLaunch|TestEnabledPluginDirs' -count=1
```

Expected: compile failure because `ResolveForLaunch` and the result types do not exist.

- [ ] **Step 3: Implement enumeration, metadata, selection, and validation**

Load explicit directories first, then globally enabled registry entries. Count hooks by summing each `Instance.Hooks` slice. Keep one candidate per manifest name, emit structured diagnostics for invalid candidates and duplicate losers, and construct `SelectedDirs` from candidate order. Sort requested names only when rendering a deterministic aggregate selection error; do not reorder selected directories.

```go
func (r LaunchPluginResolution) ValidateSelection() error {
    if len(r.SelectionErrors) == 0 { return nil }
    errs := append([]PluginSelectionError(nil), r.SelectionErrors...)
    slices.SortFunc(errs, func(a, b PluginSelectionError) int { return cmp.Compare(a.Name, b.Name) })
    parts := make([]string, 0, len(errs))
    for _, item := range errs { parts = append(parts, fmt.Sprintf("%s: %s", item.Name, item.Reason)) }
    return fmt.Errorf("enabled plugin selection is unavailable: %s", strings.Join(parts, "; "))
}
```

Reserve the returned `error` for registry/infrastructure failures. Candidate failures stay structured.

- [ ] **Step 4: Convert `EnabledPluginDirs` into a compatibility wrapper**

Call `ResolveForLaunch(explicit, nil)`, print the resolver diagnostics through `m.stderr()` with the existing warning wording, return `SelectedDirs`, and preserve existing fail-soft behavior. Do not let the wrapper duplicate ordering or plugin loading.

- [ ] **Step 5: Run package tests and verify green**

Run:

```bash
go test ./internal/plugins -count=1
```

Expected: PASS, including all existing install/list/enabled tests.

- [ ] **Step 6: Commit the resolver**

```bash
git add internal/plugins/resolve.go internal/plugins/resolve_test.go internal/plugins/enabled.go internal/plugins/enabled_test.go
git commit -m "feat: resolve plugins for session launch"
```

### Task 2: Preserve `enabledPlugins` through launch config and appwire

**Files:**
- Modify: `appwire/types.go`
- Modify: `appwire/types_test.go`
- Modify: `appwire/wiretypes_fuzz_test.go`
- Modify: `cmd/evener-hub/internal/launchconfig/types.go`
- Modify: `cmd/evener-hub/internal/launchconfig/wire.go`
- Modify: `cmd/evener-hub/internal/launchconfig/wire_test.go`
- Modify: `cmd/evener-hub/internal/launchconfig/merge.go`
- Modify: `cmd/evener-hub/internal/launchconfig/merge_test.go`
- Modify: `cmd/evener-hub/internal/launchconfig/args.go`
- Modify: `cmd/evener-hub/internal/launchconfig/args_test.go`
- Modify: `cmd/evener-hub/internal/launchconfig/schema.go`
- Modify: `cmd/evener-hub/internal/launchconfig/schema_test.go`
- Modify: `cmd/evener-hub/internal/launchconfig/resolver.go`
- Modify: `cmd/evener-hub/internal/launchconfig/resolver_test.go`
- Modify: `cmd/evener-hub/app_launch.go`
- Modify: `cmd/evener-hub/app_launch_test.go`

**Interfaces:**
- Consumes: Task 1's manifest-name selection semantics.
- Produces: `appwire.LaunchConfigLayer.EnabledPlugins *[]string`, `launchconfig.Layer.EnabledPlugins *[]string`, schema kind `pluginSelection`, and `--enabled-plugins=<csv>` argv.

- [ ] **Step 1: Write failing nil/empty/non-empty wire tests**

Assert raw JSON, wire conversion, deep copies, merge replacement, empty-value provenance, argv, schema metadata, and persistence rejection.

```go
func TestLaunchConfigEnabledPluginsJSONPresence(t *testing.T) {
    nilRaw, _ := json.Marshal(LaunchConfigLayer{})
    if bytes.Contains(nilRaw, []byte(`"enabledPlugins"`)) { t.Fatalf("nil encoded: %s", nilRaw) }

    empty := []string{}
    emptyRaw, _ := json.Marshal(LaunchConfigLayer{EnabledPlugins: &empty})
    if !bytes.Contains(emptyRaw, []byte(`"enabledPlugins":[]`)) { t.Fatalf("empty lost: %s", emptyRaw) }

    var roundTrip LaunchConfigLayer
    if err := json.Unmarshal(emptyRaw, &roundTrip); err != nil { t.Fatal(err) }
    if roundTrip.EnabledPlugins == nil || len(*roundTrip.EnabledPlugins) != 0 { t.Fatalf("round trip = %#v", roundTrip.EnabledPlugins) }
}
```

- [ ] **Step 2: Run targeted tests and verify red**

```bash
go test ./appwire ./cmd/evener-hub/internal/launchconfig ./cmd/evener-hub -run 'Test.*EnabledPlugins' -count=1
```

Expected: compile failure because the new fields and schema kind do not exist.

- [ ] **Step 3: Add the presence-sensitive fields and converters**

Use pointers on both appwire and internal layers. Add a helper that deep-copies `*[]string`. Ensure `LaunchConfigLayer.MarshalJSON`'s existing `modelFallbacks` handling carries `EnabledPlugins` through its alias value.

```go
func cloneStringSlicePtr(in *[]string) *[]string {
    if in == nil { return nil }
    out := append([]string{}, (*in)...)
    return &out
}
```

Set the internal TOML tag to `toml:"-"`.

- [ ] **Step 4: Add replacement merge, provenance, schema, and argv**

Mirror the existing `ModelFallbacks` replacement branch. A non-nil empty value must mark the launch layer as contributing and set provenance to `launch`.

```go
if l.EnabledPlugins != nil {
    eff.EnabledPlugins = cloneStringSlicePtr(l.EnabledPlugins)
    prov["enabled_plugins"] = name
    nonEmpty = true
}
```

Emit one argv token so empty remains observable:

```go
if e.EnabledPlugins != nil {
    args = append(args, "--enabled-plugins="+strings.Join(*e.EnabledPlugins, ","))
}
```

Add `LaunchControlPluginSelection`, with `PerLaunch: true`, no `DefaultableLayers`, and evener-only driver support.

- [ ] **Step 5: Reject persistent layer writes and repository TOML**

In `hubLaunchController.SetLayer`, reject non-nil `params.Config.EnabledPlugins` before `SaveLayer`. In `decodeTrustedRepoLayer`, retain `toml.MetaData`, inspect `Undecoded()`, and return a `LayerRepo` diagnostic with field `enabled_plugins` when that key appears; return a layer with `EnabledPlugins == nil` so repository TOML cannot masquerade as a launch selection.

```go
if params.Config.EnabledPlugins != nil {
    return appwire.LaunchConfigResolved{}, appwire.InvalidParams("enabledPlugins is per-launch only")
}
```

- [ ] **Step 6: Run affected Go tests and verify green**

```bash
go test ./appwire ./cmd/evener-hub/internal/launchconfig ./cmd/evener-hub -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit launch presence semantics**

```bash
git add appwire/types.go appwire/types_test.go appwire/wiretypes_fuzz_test.go cmd/evener-hub/internal/launchconfig/types.go cmd/evener-hub/internal/launchconfig/wire.go cmd/evener-hub/internal/launchconfig/wire_test.go cmd/evener-hub/internal/launchconfig/merge.go cmd/evener-hub/internal/launchconfig/merge_test.go cmd/evener-hub/internal/launchconfig/args.go cmd/evener-hub/internal/launchconfig/args_test.go cmd/evener-hub/internal/launchconfig/schema.go cmd/evener-hub/internal/launchconfig/schema_test.go cmd/evener-hub/internal/launchconfig/resolver.go cmd/evener-hub/internal/launchconfig/resolver_test.go cmd/evener-hub/app_launch.go cmd/evener-hub/app_launch_test.go
git commit -m "feat: add plugin allow-list launch field"
```

### Task 3: Add direct CLI, `serve`, and effective listing

**Files:**
- Create: `cmd/evener/plugin_selection_flag.go`
- Create: `cmd/evener/plugin_selection_flag_test.go`
- Modify: `cmd/evener/main.go`
- Modify: `cmd/evener/main_test.go`
- Modify: `cmd/evener/run.go`
- Modify: `cmd/evener/run_test.go`
- Modify: `cmd/evener/serve.go`
- Modify: `cmd/evener/serve_test.go`
- Modify: `cmd/evener/plugincmd.go`
- Modify: `cmd/evener/plugincmd_test.go`

**Interfaces:**
- Consumes: `plugins.Manager.ResolveForLaunch`, `LaunchPluginResolution.ValidateSelection`, and Task 2's argv format.
- Produces: presence-aware `pluginSelectionFlag`, `runConfig.enabledPlugins`, `--enabled-plugins`, and `evener plugin list --effective`.

```go
type pluginSelectionFlag struct {
    set   bool
    names []string
}

func (f *pluginSelectionFlag) Set(raw string) error
func (f *pluginSelectionFlag) String() string
func (f *pluginSelectionFlag) Value() *[]string

type effectivePluginListJSON struct {
    Plugins     []effectivePluginJSON            `json:"plugins"`
    Diagnostics []plugins.LaunchPluginDiagnostic `json:"diagnostics,omitempty"`
}

type effectivePluginJSON struct {
    Name         string                     `json:"name"`
    Version      string                     `json:"version,omitempty"`
    Description  string                     `json:"description,omitempty"`
    Source       plugins.LaunchPluginSource `json:"source"`
    Marketplace  string                     `json:"marketplace,omitempty"`
    Path         string                     `json:"path,omitempty"`
    SkillCount   int                        `json:"skillCount"`
    AgentCount   int                        `json:"agentCount"`
    CommandCount int                        `json:"commandCount"`
    HookCount    int                        `json:"hookCount"`
    MCPCount     int                        `json:"mcpCount"`
}
```

The CLI envelope is intentionally separate from appwire but maps the same resolver result.

- [ ] **Step 1: Write failing flag and conflict tests**

Cover omission, `--enabled-plugins=`, trimmed comma-separated names, duplicate names, empty interior elements, invalid kebab-case names, `--resume`/`--resume-last` rejection, and `--resume-with` acceptance.

```go
func TestPluginSelectionFlagPresence(t *testing.T) {
    var f pluginSelectionFlag
    if f.Value() != nil { t.Fatal("zero value must be omitted") }
    if err := f.Set(""); err != nil { t.Fatal(err) }
    if got := f.Value(); got == nil || len(*got) != 0 { t.Fatalf("empty = %#v", got) }
    if err := f.Set("alpha,beta"); err != nil { t.Fatal(err) }
    if diff := cmp.Diff([]string{"alpha", "beta"}, *f.Value()); diff != "" { t.Fatal(diff) }
}
```

- [ ] **Step 2: Write failing run/serve/effective-list tests**

Use temp plugin roots and process/session seams. Assert strict selected-name failure occurs before any session metadata or transcript appears. Assert `serve` receives the same selected directories. Assert `plugin list --effective --json` includes explicit and registry candidates with source/count fields and excludes globally disabled entries.

- [ ] **Step 3: Run targeted CLI tests and verify red**

```bash
go test ./cmd/evener -run 'Test.*(PluginSelection|EnabledPlugins|PluginListEffective)' -count=1
```

Expected: compile or unknown-flag failures.

- [ ] **Step 4: Implement the presence-aware flag and fresh-session conflicts**

`pluginSelectionFlag` stores a `set` bit and normalized names. Its `Value()` returns nil until `Set` is called and a non-nil copy afterward. Register it in direct run and `serve`. Reject it with resume and resume-last before loading a provider or creating state; allow resume-with because it creates a new session.

- [ ] **Step 5: Route direct run and `serve` through the shared resolver**

Resolve before constructing `SessionConfig`, render diagnostics to stderr, call `ValidateSelection`, and pass only `SelectedDirs` to `SessionConfig.PluginDirs`. Do not persist enabled names.

```go
resolved, err := plugins.NewManager("").ResolveForLaunch(cfg.pluginDirs, cfg.enabledPlugins)
if err != nil && cfg.enabledPlugins != nil { return fmt.Errorf("resolve plugins: %w", err) }
if err != nil { fmt.Fprintf(cfg.stderr, "warning: listing installed plugins: %v\n", err) }
renderLaunchPluginDiagnostics(cfg.stderr, resolved.Diagnostics)
if err := resolved.ValidateSelection(); err != nil { return err }
sessionCfg.PluginDirs = resolved.SelectedDirs
```

- [ ] **Step 6: Implement `plugin list --effective`**

Add list-local repeatable `--plugin-dir`, `--effective`, and existing `--json`. Effective mode calls `ResolveForLaunch(dirs, nil)` and renders candidates plus diagnostics; ordinary list output stays byte-for-byte behaviorally unchanged. JSON uses a stable envelope with `plugins` and optional `diagnostics`.

- [ ] **Step 7: Run CLI tests and verify green**

```bash
go test ./cmd/evener -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit CLI behavior**

```bash
git add cmd/evener/plugin_selection_flag.go cmd/evener/plugin_selection_flag_test.go cmd/evener/main.go cmd/evener/main_test.go cmd/evener/run.go cmd/evener/run_test.go cmd/evener/serve.go cmd/evener/serve_test.go cmd/evener/plugincmd.go cmd/evener/plugincmd_test.go
git commit -m "feat: add session plugin selection CLI"
```

### Task 4: Add preview RPC and hub pre-spawn validation

**Files:**
- Modify: `appwire/types.go`
- Modify: `appwire/protocol.go`
- Modify: `appwire/client.go`
- Modify: `appwire/types_test.go`
- Modify: `appwire/protocol_test.go`
- Modify: `appwire/cov_rhub_appwire_test.go`
- Modify: `cmd/evener-hub/app_plugins.go`
- Create: `cmd/evener-hub/app_plugin_preview_test.go`
- Modify: `cmd/evener-hub/app_rpc.go`
- Modify: `cmd/evener-hub/app_rpc_test.go`
- Modify: `cmd/evener-hub/app_threadlifecycle.go`
- Create: `cmd/evener-hub/app_threadlifecycle_plugin_test.go`
- Generated: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`
- Generated: `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: `MethodEvenerPluginPreview`, `PluginPreviewParams`, `PluginPreviewResponse`, appwire client method, and hub route.

```go
type PluginPreviewParams struct {
    CWD             string             `json:"cwd"`
    LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
}

type PluginPreviewResponse struct {
    Plugins         []PluginLaunchCandidate `json:"plugins"`
    Diagnostics     []PluginDiagnostic      `json:"diagnostics,omitempty"`
    SelectionErrors []PluginSelectionError  `json:"selectionErrors,omitempty"`
}

type PluginLaunchCandidate struct {
    Name         string `json:"name"`
    Version      string `json:"version,omitempty"`
    Description  string `json:"description,omitempty"`
    Source       string `json:"source"`
    Marketplace  string `json:"marketplace,omitempty"`
    Path         string `json:"path,omitempty"`
    Selected     bool   `json:"selected"`
    SkillCount   int    `json:"skillCount"`
    AgentCount   int    `json:"agentCount"`
    CommandCount int    `json:"commandCount"`
    HookCount    int    `json:"hookCount"`
    MCPCount     int    `json:"mcpCount"`
}

type PluginDiagnostic struct {
    Name    string `json:"name,omitempty"`
    Path    string `json:"path,omitempty"`
    Source  string `json:"source,omitempty"`
    Message string `json:"message"`
}

type PluginSelectionError struct {
    Name   string `json:"name"`
    Reason string `json:"reason"`
}
```

- [ ] **Step 1: Write failing appwire and controller tests**

Assert method catalog scope is hub, client request shape preserves explicit empty selection, preview maps every candidate field, selection errors remain structured, invalid cwd is rejected, the controller uses resolved launch `pluginDirs` plus `enabledPlugins`, and `ThreadResumeParams` remains selection-free because resume reattaches to the frozen session.

- [ ] **Step 2: Write failing no-side-effect and race tests**

Create a fixture plugin whose startup hook and MCP command would write marker files if executed. Call Preview and assert neither marker exists and no session metadata was created. Then remove a selected plugin between Preview and Thread Start and assert Thread Start returns invalid params before `Spawner.Spawn` is called.

- [ ] **Step 3: Run targeted tests and verify red**

```bash
go test ./appwire ./cmd/evener-hub -run 'Test.*(PluginPreview|PluginSelectionBeforeSpawn|HubCommandList)' -count=1
```

Expected: compile failure for missing method and wire types.

- [ ] **Step 4: Implement wire types, controller mapping, and route**

Extend `hubPluginsController` with both plugin root and launch config root. Preview canonicalizes cwd, resolves launch layers, calls `ResolveForLaunch`, treats any infrastructure error as an RPC failure because a partial inventory is not truthful, and maps internal types to appwire. Change construction to:

```go
pluginsController := newHubPluginsController(cfg.PluginRoot, hubLaunchConfigRoot(cfg))
```

Register `evener/plugin/preview` beside the other plugin handlers. Keep it hub-scoped; do not add a daemon route.

- [ ] **Step 5: Revalidate in `hubThreadStart` before spawn**

After launch resolution and before `cfg.Spawner.Spawn`, call the shared resolver with `spawnResolved.Effective.PluginDirs` and `EnabledPlugins`. An infrastructure error is fatal when `EnabledPlugins` is present; with omission, preserve current fail-soft startup and continue with the partial explicit result. Convert `ValidateSelection` failures to invalid params. Leave `serve` revalidation in place as the second authority.

- [ ] **Step 6: Move the global command catalog onto the shared resolver**

Call `ResolveForLaunch(cfg.PluginDirs, nil)`, warn and continue with the partial result on registry failure, load commands only from `SelectedDirs`, and keep `evener/command/list` params and response unchanged.

- [ ] **Step 7: Generate protocol outputs**

```bash
make generate
```

Expected: updates to `docs/appwire-protocol.md` and `frontend/src/protocol/types.gen.ts`; generated checks pass.

- [ ] **Step 8: Run appwire and hub tests**

```bash
go test ./appwire ./cmd/evener-hub -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit preview and hub validation**

```bash
git add appwire/types.go appwire/protocol.go appwire/client.go appwire/types_test.go appwire/protocol_test.go appwire/cov_rhub_appwire_test.go cmd/evener-hub/app_plugins.go cmd/evener-hub/app_plugin_preview_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_rpc_test.go cmd/evener-hub/app_threadlifecycle.go cmd/evener-hub/app_threadlifecycle_plugin_test.go cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md
git commit -m "feat: preview plugins for session launch"
```

### Task 5: Pin snapshot and contribution boundaries with integration tests

**Files:**
- Create: `agent/plugin_selection_snapshot_test.go`
- Modify: `agent/session_config_test.go`
- Modify: `agent/fork_test.go`
- Modify: `agent/aside_test.go`
- Modify: `agent/subagents_test.go`
- Modify: `agent/delegate_resource_runtime_test.go`
- Modify: `cmd/evener/run_test.go`
- Modify: `cmd/evener-hub/app_plugin_preview_test.go`

**Interfaces:**
- Consumes: selected concrete directories from Tasks 1–4 and existing `ConfigSnapshot.PluginDirs`.
- Produces: executable proof that no new snapshot schema is needed and excluded contributions never initialize.

- [ ] **Step 1: Add snapshot round-trip and historical snapshot tests**

Prove `SessionConfig.PluginDirs` survives `toSnapshot`/`configFromSnapshot`, including empty and ordered lists. Decode a pre-feature fixture without a new field and assert it preserves its historical `PluginDirs` without consulting the current registry.

```go
func TestPluginSelectionSnapshotRoundTrip(t *testing.T) {
    want := []string{"/plugins/alpha", "/plugins/beta"}
    snapshot := (SessionConfig{PluginDirs: want}).toSnapshot()
    got := configFromSnapshot(snapshot).PluginDirs
    if diff := cmp.Diff(want, got); diff != "" { t.Fatal(diff) }
}
```

- [ ] **Step 2: Add lifecycle-specific inheritance tests**

Use distinct selected and globally added plugin dirs. In `cmd/evener/run_test.go`, prove resume and resume-last restore the recorded dirs and reject a new flag, while resume-with constructs a new snapshot from its fresh selection. In agent tests, cover ordinary restore, fork, aside, direct child/subagent, and durable delegate descriptor restoration. `thread/resume` reattaches to an existing daemon and has no launch-selection field; pin that unchanged request shape in Task 4's appwire test instead of pretending it rebuilds a snapshot. Assert every config structurally; do not match prompt prose.

- [ ] **Step 3: Add excluded-contribution integration test**

Build two fixture plugins. Give the excluded one a skill, agent, command, startup hook marker, and MCP marker; select only its sibling through `ResolveForLaunch`; construct a real session below a scripted LLM boundary; assert diagnostics contain only the sibling and that excluded skill, agent, command, hook marker, MCP marker, tool, and sandbox infrastructure path are absent.

- [ ] **Step 4: Run focused lifecycle tests**

```bash
go test ./agent ./cmd/evener ./cmd/evener-hub -run 'TestPluginSelection' -count=1
```

Expected: PASS without network or provider credentials.

- [ ] **Step 5: Run full agent package tests**

```bash
go test ./agent -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit lifecycle proof**

```bash
git add agent/plugin_selection_snapshot_test.go agent/session_config_test.go agent/fork_test.go agent/aside_test.go agent/subagents_test.go agent/delegate_resource_runtime_test.go cmd/evener/run_test.go cmd/evener-hub/app_plugin_preview_test.go
git commit -m "test: pin session plugin selection inheritance"
```

### Task 6: Implement web selection state and preview orchestration

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/spawn/pluginSelectionState.ts`
- Create: `cmd/evener-hub/frontend/src/panes/spawn/pluginSelectionState.test.ts`
- Create: `cmd/evener-hub/frontend/src/panes/spawn/usePluginPreview.ts`
- Create: `cmd/evener-hub/frontend/src/panes/spawn/usePluginPreview.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/schema.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/schema.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/extensions.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/extensions.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx`

**Interfaces:**
- Consumes: generated `PluginPreviewResponse` and `LaunchConfigLayer.enabledPlugins`.
- Produces:

```ts
export type PluginSelectionState =
  | { mode: "default" }
  | { mode: "explicit"; names: string[] };

export function withPluginSelection(
  overrides: LaunchConfigLayer,
  selection: PluginSelectionState,
): LaunchConfigLayer;

export function reconcilePluginSelection(
  selection: PluginSelectionState,
  preview: PluginPreviewResponse,
): PluginSelectionState;

export function setPluginSelected(
  selection: PluginSelectionState,
  preview: PluginPreviewResponse,
  name: string,
  selected: boolean,
): PluginSelectionState;

export function selectAllPlugins(preview: PluginPreviewResponse): PluginSelectionState;
export function selectNoPlugins(): PluginSelectionState;

export type PluginPreviewLoadState =
  | { status: "loading" }
  | { status: "ready"; response: PluginPreviewResponse }
  | { status: "error"; message: string };

export interface UsePluginPreviewArgs {
  client: AppwireClientLike;
  cwd: string;
  launchOverrides: LaunchConfigLayer;
  pluginRevision: number;
}

export function usePluginPreview(args: UsePluginPreviewArgs): {
  state: PluginPreviewLoadState;
  retry(): void;
};
```

- [ ] **Step 1: Write failing pure state tests**

Cover untouched omission, first toggle materialization, explicit None as `[]`, All, candidate refresh in default mode, surviving explicit names, newly appearing unselected names, stale names retained for selection errors, stable candidate order, and reset after success.

```ts
test("explicit none stays present on the wire", () => {
  expect(withPluginSelection({ sandbox: "off" }, { mode: "explicit", names: [] })).toEqual({
    sandbox: "off",
    enabledPlugins: [],
  });
});
```

- [ ] **Step 2: Write failing hook and notification tests**

Use fake timers only to advance the declared debounce, then await the request promise. Prove one request per settled cwd/override key, stale response rejection, retry, plugin revision refresh, no guessed count while loading, and explicit selection included in Preview.

- [ ] **Step 3: Run targeted web tests and verify red**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/panes/spawn/pluginSelectionState.test.ts src/panes/spawn/usePluginPreview.test.tsx src/stores/extensions.test.ts --maxWorkers=4
```

Expected: missing module/type failures.

- [ ] **Step 4: Implement pure state and custom schema filtering**

Filter `kind === "pluginSelection"` out of `perLaunchEvenerOptions` so Advanced options does not render a duplicate fallback text field. Keep the field in the server schema.

- [ ] **Step 5: Implement notification revision and preview hook**

Add `pluginRevision: number` to the extensions store and increment it synchronously on `evener/plugin/updated` before scheduling the existing plugin-list refetch. `usePluginPreview` keys requests by cwd plus serialized relevant overrides, debounces cwd changes, stores the latest key in a ref, and ignores late mismatches.

- [ ] **Step 6: Integrate state into Spawn submission**

Keep advanced overrides and plugin selection as separate state. Derive one combined override for Preview, launch resolve, and Thread Start. On successful Start, reset selection to default; on failure, preserve it. Do not let a later Advanced-options update erase `enabledPlugins`.

- [ ] **Step 7: Format and run targeted tests**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/panes/spawn/pluginSelectionState.ts src/panes/spawn/pluginSelectionState.test.ts src/panes/spawn/usePluginPreview.ts src/panes/spawn/usePluginPreview.test.tsx src/panes/spawn/schema.ts src/panes/spawn/schema.test.ts src/stores/extensions.ts src/stores/extensions.test.ts src/panes/spawn/Spawn.tsx src/panes/spawn/Spawn.test.tsx
npx vitest run src/panes/spawn/pluginSelectionState.test.ts src/panes/spawn/usePluginPreview.test.tsx src/panes/spawn/schema.test.ts src/stores/extensions.test.ts src/panes/spawn/Spawn.test.tsx --maxWorkers=4
```

Expected: PASS.

- [ ] **Step 8: Commit web state plumbing**

```bash
git add cmd/evener-hub/frontend/src/panes/spawn/pluginSelectionState.ts cmd/evener-hub/frontend/src/panes/spawn/pluginSelectionState.test.ts cmd/evener-hub/frontend/src/panes/spawn/usePluginPreview.ts cmd/evener-hub/frontend/src/panes/spawn/usePluginPreview.test.tsx cmd/evener-hub/frontend/src/panes/spawn/schema.ts cmd/evener-hub/frontend/src/panes/spawn/schema.test.ts cmd/evener-hub/frontend/src/stores/extensions.ts cmd/evener-hub/frontend/src/stores/extensions.test.ts cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx
git commit -m "feat(web): resolve plugin selection at launch"
```

### Task 7: Add desktop and mobile plugin selection surfaces

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/spawn/PluginSelectionPanel.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/spawn/PluginSelectionPanel.test.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/spawn/pluginSelection.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/MobileSettingRows.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/spawn/MobileSettingRows.test.tsx`
- Modify: `cmd/evener-hub/frontend/scripts/spawnguard/run.mjs`
- Modify: `cmd/evener-hub/frontend/src/dev/spawnguard-entry.tsx`

**Interfaces:**
- Consumes: Task 6 state and `usePluginPreview` result.
- Produces: approved summary/disclosure, shared list panel, and mobile sheet.

```ts
export interface PluginSelectionPanelProps {
  preview: PluginPreviewResponse;
  selection: PluginSelectionState;
  onSelectionChange(next: PluginSelectionState): void;
  onRetry(): void;
}

export function PluginSelectionPanel(props: PluginSelectionPanelProps): React.ReactElement;
```

- [ ] **Step 1: Write failing panel accessibility and behavior tests**

Assert visible and accessible switch names, `aria-checked`, keyboard toggle, filter by name/source/description, All/None, selected count, installed/directory metadata, component counts, nonblocking diagnostics, blocking selection errors, retry, and no color-only status.

- [ ] **Step 2: Write failing Spawn and mobile placement tests**

Assert the desktop summary sits between config and Advanced options, mobile exposes `Plugins — N of M`, sheet stays open across toggles, Done applies, Cancel restores prior selection, close restores focus, pending reads `Inspecting plugins…`, and failed Preview never shows zero.

- [ ] **Step 3: Run targeted tests and verify red**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/panes/spawn/PluginSelectionPanel.test.tsx src/panes/spawn/Spawn.test.tsx src/panes/spawn/MobileSettingRows.test.tsx --maxWorkers=4
```

Expected: missing component and missing Plugins row failures.

- [ ] **Step 4: Implement the shared panel and desktop disclosure**

Use the existing `Switch`, `Disclosure`, and button primitives. Render source and counts from Preview. Disable Start only for `selectionErrors`, not for ordinary diagnostics. Keep Start available while default-mode Preview is pending or unavailable.

- [ ] **Step 5: Implement the mobile row and non-auto-closing Sheet**

Pass summary, panel content, apply, and cancel callbacks into `MobileSettingRows`. Use `aria-haspopup="dialog"`, `aria-expanded`, a complete label, minimum tap targets, and focus restoration.

- [ ] **Step 6: Add geometry coverage**

Extend spawnguard for desktop, narrow docked pane, and phone widths. Assert zero horizontal overflow, visible summary, usable sheet, and no overlap with the prompt Start action.

- [ ] **Step 7: Format and run web surface gates**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/panes/spawn/PluginSelectionPanel.tsx src/panes/spawn/PluginSelectionPanel.test.tsx src/panes/spawn/pluginSelection.module.css src/panes/spawn/Spawn.tsx src/panes/spawn/Spawn.test.tsx src/panes/spawn/MobileSettingRows.tsx src/panes/spawn/MobileSettingRows.test.tsx src/dev/spawnguard-entry.tsx
npx vitest run src/panes/spawn/PluginSelectionPanel.test.tsx src/panes/spawn/Spawn.test.tsx src/panes/spawn/MobileSettingRows.test.tsx --maxWorkers=4
npm run spawnguard
```

Expected: PASS.

- [ ] **Step 8: Commit responsive surfaces**

```bash
git add cmd/evener-hub/frontend/src/panes/spawn/PluginSelectionPanel.tsx cmd/evener-hub/frontend/src/panes/spawn/PluginSelectionPanel.test.tsx cmd/evener-hub/frontend/src/panes/spawn/pluginSelection.module.css cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx cmd/evener-hub/frontend/src/panes/spawn/MobileSettingRows.tsx cmd/evener-hub/frontend/src/panes/spawn/MobileSettingRows.test.tsx cmd/evener-hub/frontend/scripts/spawnguard/run.mjs cmd/evener-hub/frontend/src/dev/spawnguard-entry.tsx
git commit -m "feat(web): add session plugin selector"
```

### Task 8: Scope plugin command suggestions to the active session

**Files:**
- Modify: `cmd/evener-hub/frontend/src/shell/palette/commands.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/palette/commands.edge.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/palette/CommandPalette.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/palette/CommandPalette.test.tsx`

**Interfaces:**
- Consumes: global `CommandDescriptor[]` and active `Thread.Diagnostics.Plugins`.
- Produces:

```ts
export function visibleCatalogCommands(
  commands: CommandDescriptor[],
  activePluginNames: ReadonlySet<string> | null | undefined,
): CommandDescriptor[];
```

`undefined` means no active local session and keeps the global catalog. `null` means an active session whose diagnostics are unavailable and drops plugin commands. A Set filters plugin commands to loaded names. Non-plugin commands always remain.

- [ ] **Step 1: Write failing pure filter tests**

Cover no active session, loaded plugin subset, diagnostics unavailable, built-in/user-global preservation, duplicate command names, and immutable input.

- [ ] **Step 2: Write failing palette integration test**

Hydrate the global catalog with one enabled-plugin command, one excluded-plugin command, and one user command. Open an active thread with diagnostics naming only the enabled plugin. Assert only enabled and user commands render. Remove diagnostics and assert user remains while all plugin commands fail closed.

- [ ] **Step 3: Run targeted tests and verify red**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/shell/palette/commands.edge.test.ts src/shell/palette/CommandPalette.test.tsx --maxWorkers=4
```

Expected: missing `visibleCatalogCommands` failure.

- [ ] **Step 4: Implement the pure filter and call it at the palette view boundary**

Do not mutate the global command store or change `evener/command/list`. Derive active names from thread diagnostics immediately before catalog commands become results.

- [ ] **Step 5: Format, test, and commit**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/shell/palette/commands.ts src/shell/palette/commands.edge.test.ts src/shell/palette/CommandPalette.tsx src/shell/palette/CommandPalette.test.tsx
npx vitest run src/shell/palette/commands.edge.test.ts src/shell/palette/CommandPalette.test.tsx --maxWorkers=4
cd ../../..
git add cmd/evener-hub/frontend/src/shell/palette/commands.ts cmd/evener-hub/frontend/src/shell/palette/commands.edge.test.ts cmd/evener-hub/frontend/src/shell/palette/CommandPalette.tsx cmd/evener-hub/frontend/src/shell/palette/CommandPalette.test.tsx
git commit -m "fix(web): scope plugin commands to active session"
```

### Task 9: Add the TUI new-session plugin selector

**Files:**
- Create: `cmd/evener-tui/internal/launchconfig/plugins_for_launch_panel.go`
- Create: `cmd/evener-tui/internal/launchconfig/plugins_for_launch_panel_test.go`
- Modify: `cmd/evener-tui/internal/launchconfig/plugins_client.go`
- Modify: `cmd/evener-tui/internal/launchconfig/plugins_client_covtest_test.go`
- Modify: `cmd/evener-tui/internal/launchconfig/launch_schema.go`
- Modify: `cmd/evener-tui/internal/launchconfig/launch_schema_test.go`
- Modify: `cmd/evener-tui/hub_model.go`
- Modify: `cmd/evener-tui/hub_spawn.go`
- Modify: `cmd/evener-tui/hub_update.go`
- Modify: `cmd/evener-tui/hub_update_config.go`
- Modify: `cmd/evener-tui/hub_notifications.go`
- Create: `cmd/evener-tui/hub_spawn_plugins_test.go`
- Modify: `cmd/evener-tui/tmux_e2e_test.go`

**Interfaces:**
- Consumes: appwire Preview client and `LaunchConfigLayer.EnabledPlugins`.
- Produces: `PluginsForLaunchPanel`, preview/result messages, focusable spawn field, and explicit one-shot payload.

```go
type PluginsForLaunchResult struct {
    Applied        bool
    Cancelled      bool
    EnabledPlugins *[]string
}

func NewPluginsForLaunchPanel(preview appwire.PluginPreviewResponse, initial *[]string, width int) PluginsForLaunchPanel
func (p PluginsForLaunchPanel) Update(tea.Msg) (tea.Model, tea.Cmd)
func (p PluginsForLaunchPanel) View() string
func (p PluginsForLaunchPanel) Done() bool
func (p PluginsForLaunchPanel) Result() PluginsForLaunchResult

type PluginPreviewRequestMsg struct {
    Params appwire.PluginPreviewParams
    Key    string
}

type PluginPreviewResultMsg struct {
    Response appwire.PluginPreviewResponse
    Key      string
    Err      error
}
```

- [ ] **Step 1: Write failing panel model tests**

Cover initial candidates, selected markers, Up/Down, typed filter, Backspace, Space, All, None, Enter apply, Escape cancel, blocking selection errors, nonblocking diagnostics, 15-row scrolling window, 80-column render, and narrow render.

```go
func TestPluginsForLaunchPanel_NoneAppliesExplicitEmpty(t *testing.T) {
    preview := appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{
        {Name: "alpha", Selected: true},
        {Name: "beta", Selected: true},
    }}
    p := NewPluginsForLaunchPanel(preview, nil, 80)
    updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
    p = updated.(PluginsForLaunchPanel)
    updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
    p = updated.(PluginsForLaunchPanel)
    got := p.Result()
    if got.EnabledPlugins == nil || len(*got.EnabledPlugins) != 0 { t.Fatalf("result = %#v", got) }
}
```

- [ ] **Step 2: Write failing spawn integration tests**

Assert Plugins follows Dir in focus order, preview loads on form open, leaving the Dir field or applying launch overrides refreshes Preview without issuing one request per typed rune, plugin-updated notifications refresh, stale keyed results drop, Enter opens the overlay, cancel restores selection, apply updates the summary, Thread Start carries explicit empty/names, successful Start clears selection, and failed Start preserves it.

- [ ] **Step 3: Run targeted TUI tests and verify red**

```bash
go test ./cmd/evener-tui/... -run 'Test.*(PluginsForLaunch|SpawnPlugins)' -count=1
```

Expected: missing panel/message/field failures.

- [ ] **Step 4: Implement the preview client and custom schema exclusion**

Add `PluginPreview(ctx, params)` to the TUI client wrapper. In generic launch schema rows, skip `opt.Kind == "pluginSelection"` because the dedicated spawn field owns it.

- [ ] **Step 5: Implement panel model and spawn wiring**

Use explicit `[x]`/`[ ]` text plus theme color. Keep a bounded filtered window; `a` selects all filtered rows and `n` selects none across the full candidate set. Add `hubSpawnFieldPlugins`, preview status/revision/request-key fields, and overlay dispatch. Merge explicit selection into `spawnLaunchOverrides` without erasing other overrides.

- [ ] **Step 6: Add live tmux coverage**

Extend the existing deterministic fake-hub tmux scenario: open New Session, focus Plugins, toggle one, apply, capture `Plugins: 1/2 enabled`, submit, and assert the fake hub received `launchOverrides.enabledPlugins` with the selected name.

- [ ] **Step 7: Run TUI package tests**

```bash
go test ./cmd/evener-tui/... -count=1
```

Expected: PASS; environment-gated tmux cases may skip only through their existing capability probe.

- [ ] **Step 8: Commit TUI selection**

```bash
git add cmd/evener-tui/internal/launchconfig/plugins_for_launch_panel.go cmd/evener-tui/internal/launchconfig/plugins_for_launch_panel_test.go cmd/evener-tui/internal/launchconfig/plugins_client.go cmd/evener-tui/internal/launchconfig/plugins_client_covtest_test.go cmd/evener-tui/internal/launchconfig/launch_schema.go cmd/evener-tui/internal/launchconfig/launch_schema_test.go cmd/evener-tui/hub_model.go cmd/evener-tui/hub_spawn.go cmd/evener-tui/hub_update.go cmd/evener-tui/hub_update_config.go cmd/evener-tui/hub_notifications.go cmd/evener-tui/hub_spawn_plugins_test.go cmd/evener-tui/tmux_e2e_test.go
git commit -m "feat(tui): select plugins for new sessions"
```

### Task 10: Update user documentation and run repository gates

**Files:**
- Modify: `README.md`
- Modify: `docs/evener-hub.md`
- Verify generated: `docs/appwire-protocol.md`
- Verify generated: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`

**Interfaces:**
- Consumes: every completed feature task.
- Produces: user guidance and final verification evidence.

- [ ] **Step 1: Document CLI and UI behavior**

In `README.md`, document:

```text
evener plugin list --effective --json
evener --enabled-plugins=alpha,beta "task"
evener --enabled-plugins= "task"
```

State that omission uses current defaults, the flag is new-session-only, and global `evener plugin enable/disable` remains persistent. In `docs/evener-hub.md`, document the desktop summary/disclosure, mobile sheet, TUI field, preview diagnostics, and session-only reset/inheritance behavior.

- [ ] **Step 2: Regenerate and verify generated files are stable**

```bash
make generate
git diff --exit-code -- docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts
```

Expected: no diff if Task 4 committed current generated output. If generation changes either file, inspect the change, stage those named files with this task, and rerun until stable.

- [ ] **Step 3: Run frontend formatting and unit gate**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/panes/spawn/pluginSelectionState.ts src/panes/spawn/pluginSelectionState.test.ts src/panes/spawn/usePluginPreview.ts src/panes/spawn/usePluginPreview.test.tsx src/panes/spawn/PluginSelectionPanel.tsx src/panes/spawn/PluginSelectionPanel.test.tsx src/panes/spawn/pluginSelection.module.css src/panes/spawn/Spawn.tsx src/panes/spawn/Spawn.test.tsx src/panes/spawn/MobileSettingRows.tsx src/panes/spawn/MobileSettingRows.test.tsx src/panes/spawn/schema.ts src/panes/spawn/schema.test.ts src/shell/palette/commands.ts src/shell/palette/commands.edge.test.ts src/shell/palette/CommandPalette.tsx src/shell/palette/CommandPalette.test.tsx src/stores/extensions.ts src/stores/extensions.test.ts src/dev/spawnguard-entry.tsx
cd ../../..
make test-web
```

Expected: PASS.

- [ ] **Step 4: Run browser geometry gate**

```bash
make test-web-browser
```

Expected: PASS on a Chrome-capable host. A missing/blocked Chrome environment is incomplete verification, not a pass.

- [ ] **Step 5: Run Go and repository gates**

```bash
make lint
make vet
make test
```

Expected: every command exits zero. Read and fix every warning or failure; do not mute or weaken tests.

- [ ] **Step 6: Commit documentation and any required generated refresh**

```bash
git add README.md docs/evener-hub.md docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts
git commit -m "docs: explain session plugin selection"
```

The generated paths are named deliberately: staging unchanged files is harmless, while a required regeneration cannot be omitted from the commit.

- [ ] **Step 7: Verify final repository state**

```bash
git status --short
git log --oneline -10
```

Expected: no uncommitted files created by this plan, and one focused commit for each completed task.
