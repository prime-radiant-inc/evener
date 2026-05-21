# Launch Options Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the full launch-configurable Serf session surface in web and TUI per-launch Advanced controls and default settings, using one shared schema.

**Architecture:** `internal/launchconfig` becomes the source of truth for option metadata, validation classes, layer support, and launch argument serialization. Hub exposes the schema over AppWire and the web/TUI render native controls from that schema while preserving the current visual style and interaction patterns. Inline system prompt text is stored in launch config but materialized to private prompt files only when hub spawns `serf serve`, keeping prompt bodies out of argv and logs.

**Tech Stack:** Go, AppWire JSON-RPC, Go `testing`, HTMX/templates, browser JavaScript, Bubble Tea TUI, existing Serf path/model picker helpers.

---

## File Structure

- Create `internal/launchconfig/schema.go`: shared option schema, control kinds, group names, layer support, and schema coverage helpers.
- Create `internal/launchconfig/schema_test.go`: proves schema covers intended layer fields and categorizes serve-only exclusions.
- Modify `internal/launchconfig/types.go`: add system prompt source fields and Debug Logging fields to `Layer`.
- Modify `internal/launchconfig/merge.go`: merge new fields with correct precedence and diagnostics for legacy append list compatibility.
- Modify `internal/launchconfig/wire.go`: convert new fields between internal and AppWire types.
- Modify `internal/launchconfig/args.go` and `internal/launchconfig/args_test.go`: serialize file-backed prompt fields and debug flags to `serf serve`.
- Modify `internal/launchconfig/resolver.go`: expand/check path fields for system prompt and debug outputs.
- Modify `internal/appwire/types.go`: add `serf/launch/schema` method constant and wire schema structs.
- Modify `cmd/serf-hub/app_launch.go`: add schema RPC handler and backend validation helper for schema-backed writes.
- Modify `cmd/serf-hub/app_rpc.go`: register the schema method.
- Modify `cmd/serf-hub/spawn.go` and `cmd/serf-hub/spawn_test.go`: materialize inline prompt text into private launch files before building argv.
- Modify `cmd/serf-hub/assets/launchconfig.js`: add `launchconfig.schema()`.
- Modify `cmd/serf-hub/templates/partials/spawn.html`: replace hard-coded Advanced fieldsets with schema render roots that retain current spawn page structure.
- Modify `cmd/serf-hub/assets/spawn.js`: render schema-backed Advanced controls, collect overrides, validate add-time path values, and show non-secret env fallback values for per-launch only.
- Modify `cmd/serf-hub/templates/partials/settings/launch-serf.html`: render global defaults from schema.
- Modify `cmd/serf-hub/templates/partials/settings/project.html`: render project defaults from schema while keeping the project picker and repo trust behavior unchanged.
- Modify `cmd/serf-hub/assets/settings-pickers.js`: attach model/path pickers to schema-rendered controls when needed.
- Modify `cmd/serf-hub/web_test.go` and AppWire tests under `cmd/serf-hub/*_test.go`: cover schema RPC, spawn overrides, and rendered HTML hooks.
- Create `cmd/serf-tui/launch_schema.go`: TUI adapter from AppWire schema fields to rows/edit metadata.
- Modify `cmd/serf-tui/launchconfig_client.go`: fetch schema.
- Modify `cmd/serf-tui/launch_settings_panel.go`: render/edit settings rows from schema.
- Modify `cmd/serf-tui/launch_overrides_modal.go`: render/edit per-launch override rows from schema.
- Modify `cmd/serf-tui/launch_settings_panel_test.go` and `cmd/serf-tui/launch_overrides_modal_test.go`: cover schema-backed rows, grouping order, validation, and prompt modes.

## Task 1: Add Shared Launch Option Schema

**Files:**
- Create: `internal/launchconfig/schema.go`
- Create: `internal/launchconfig/schema_test.go`
- Modify: `internal/appwire/types.go`
- Modify: `cmd/serf-hub/app_launch.go`
- Modify: `cmd/serf-hub/app_rpc.go`
- Modify: `cmd/serf-hub/app_launch_test.go`
- Modify: `cmd/serf-hub/assets/launchconfig.js`

- [ ] **Step 1: Write failing schema tests**

Add `internal/launchconfig/schema_test.go`:

```go
package launchconfig

import "testing"

func TestLaunchOptionSchema_FieldCoverage(t *testing.T) {
	got := map[string]bool{}
	for _, opt := range LaunchOptionSchema() {
		got[opt.Field] = true
	}
	want := []string{
		"agent", "model", "reasoning_effort", "fast_cheap_model",
		"context_strategy", "max_rounds", "max_subagent_depth",
		"no_project_prompts", "app_replay_size",
		"system_prompt_mode", "system_prompt_file", "system_prompt_text",
		"system_prompt_append_mode", "system_prompt_append_file", "system_prompt_append_text",
		"skills_dirs", "plugin_dirs", "mcp_configs", "mcps",
		"model_fallbacks", "env",
		"verbose", "trace_file", "cpu_profile", "export_atif_path",
	}
	for _, field := range want {
		if !got[field] {
			t.Fatalf("schema missing field %q", field)
		}
	}
}

func TestLaunchOptionSchema_GroupOrder(t *testing.T) {
	opts := LaunchOptionSchema()
	if len(opts) == 0 {
		t.Fatal("empty schema")
	}
	if opts[0].Group != LaunchGroupAgent || opts[0].Field != "agent" {
		t.Fatalf("first option = %s/%s, want Agent/agent", opts[0].Group, opts[0].Field)
	}
	modelIndex := indexOption(opts, "model")
	reasoningIndex := indexOption(opts, "reasoning_effort")
	fastIndex := indexOption(opts, "fast_cheap_model")
	if modelIndex < 0 || reasoningIndex < 0 || fastIndex < 0 {
		t.Fatalf("missing model group fields")
	}
	if opts[reasoningIndex].Group != LaunchGroupModel {
		t.Fatalf("reasoning_effort group = %q, want %q", opts[reasoningIndex].Group, LaunchGroupModel)
	}
	if reasoningIndex > fastIndex {
		t.Fatalf("reasoning_effort should appear with primary model before fast_cheap_model")
	}
}

func TestLaunchOptionSchema_ExclusionsAreExplicit(t *testing.T) {
	excluded := LaunchOptionExclusions()
	for _, flag := range []string{"addr", "run_dir", "resume", "resume_last", "state_dir", "system_prompt_as_user", "output_schema", "result_tool_name", "share_task_store"} {
		if excluded[flag] == "" {
			t.Fatalf("missing exclusion reason for %q", flag)
		}
	}
}

func indexOption(opts []LaunchOption, field string) int {
	for i, opt := range opts {
		if opt.Field == field {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/launchconfig -run 'TestLaunchOptionSchema' -count=1
```

Expected: fails because `LaunchOptionSchema`, `LaunchOption`, `LaunchGroupAgent`, `LaunchGroupModel`, and `LaunchOptionExclusions` do not exist.

- [ ] **Step 3: Implement schema types and option list**

Create `internal/launchconfig/schema.go`:

```go
package launchconfig

type LaunchControlKind string
type LaunchPathKind string
type LaunchGroup string
type LaunchLayerSupport string

const (
	LaunchControlModelPicker LaunchControlKind = "modelPicker"
	LaunchControlText        LaunchControlKind = "text"
	LaunchControlMultiline   LaunchControlKind = "multilineText"
	LaunchControlInteger     LaunchControlKind = "integer"
	LaunchControlBoolean     LaunchControlKind = "boolean"
	LaunchControlSelect      LaunchControlKind = "select"
	LaunchControlRadio       LaunchControlKind = "radio"
	LaunchControlPath        LaunchControlKind = "path"
	LaunchControlPathList    LaunchControlKind = "pathList"
	LaunchControlModelList   LaunchControlKind = "modelList"
	LaunchControlMCPList     LaunchControlKind = "mcpServerList"
	LaunchControlEnvMap      LaunchControlKind = "envMap"
)

const (
	LaunchPathNone       LaunchPathKind = ""
	LaunchPathDir        LaunchPathKind = "dir"
	LaunchPathFile       LaunchPathKind = "file"
	LaunchPathOutputFile LaunchPathKind = "outputFile"
	LaunchPathCommand    LaunchPathKind = "command"
)

const (
	LaunchGroupAgent        LaunchGroup = "Agent"
	LaunchGroupModel        LaunchGroup = "Model"
	LaunchGroupLimits       LaunchGroup = "Limits"
	LaunchGroupSystemPrompt LaunchGroup = "System Prompt"
	LaunchGroupResources    LaunchGroup = "Resources"
	LaunchGroupEnvironment  LaunchGroup = "Environment"
	LaunchGroupDebugLogging LaunchGroup = "Debug Logging"
)

const (
	LaunchLayerGlobal  LaunchLayerSupport = "global"
	LaunchLayerProject LaunchLayerSupport = "project"
	LaunchLayerLaunch  LaunchLayerSupport = "launch"
)

type LaunchOptionChoice struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type LaunchOptionEnvFallback struct {
	Name   string `json:"name"`
	Secret bool   `json:"secret,omitempty"`
}

type LaunchOption struct {
	Field             string                    `json:"field"`
	WireField         string                    `json:"wireField"`
	Label             string                    `json:"label"`
	Group             LaunchGroup               `json:"group"`
	Kind              LaunchControlKind         `json:"kind"`
	PathKind          LaunchPathKind            `json:"pathKind,omitempty"`
	Repeatable        bool                      `json:"repeatable,omitempty"`
	DefaultableLayers []LaunchLayerSupport      `json:"defaultableLayers,omitempty"`
	PerLaunch         bool                      `json:"perLaunch"`
	DebugOnly         bool                      `json:"debugOnly,omitempty"`
	EnvFallback       *LaunchOptionEnvFallback  `json:"envFallback,omitempty"`
	Choices           []LaunchOptionChoice      `json:"choices,omitempty"`
	DriverSupport     map[string]bool           `json:"driverSupport,omitempty"`
}

func LaunchOptionSchema() []LaunchOption {
	defaultLayers := []LaunchLayerSupport{LaunchLayerGlobal, LaunchLayerProject}
	allLayers := []LaunchLayerSupport{LaunchLayerGlobal, LaunchLayerProject, LaunchLayerLaunch}
	serfOnly := map[string]bool{"serf": true}
	return []LaunchOption{
		{Field: "agent", WireField: "agent", Label: "Agent", Group: LaunchGroupAgent, Kind: LaunchControlText, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "model", WireField: "model", Label: "Model", Group: LaunchGroupModel, Kind: LaunchControlModelPicker, DefaultableLayers: defaultLayers, PerLaunch: true, EnvFallback: &LaunchOptionEnvFallback{Name: "SERF_MODEL"}, DriverSupport: serfOnly},
		{Field: "reasoning_effort", WireField: "reasoningEffort", Label: "Reasoning effort", Group: LaunchGroupModel, Kind: LaunchControlSelect, DefaultableLayers: defaultLayers, PerLaunch: true, EnvFallback: &LaunchOptionEnvFallback{Name: "SERF_REASONING_EFFORT"}, Choices: reasoningChoices(), DriverSupport: serfOnly},
		{Field: "fast_cheap_model", WireField: "fastCheapModel", Label: "Fast cheap model", Group: LaunchGroupModel, Kind: LaunchControlModelPicker, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "context_strategy", WireField: "contextStrategy", Label: "Context strategy", Group: LaunchGroupLimits, Kind: LaunchControlSelect, DefaultableLayers: defaultLayers, PerLaunch: true, Choices: contextChoices(), DriverSupport: serfOnly},
		{Field: "max_rounds", WireField: "maxRounds", Label: "Max rounds", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "max_subagent_depth", WireField: "maxSubagentDepth", Label: "Max subagent depth", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "no_project_prompts", WireField: "noProjectPrompts", Label: "Suppress .serf/prompts loading", Group: LaunchGroupLimits, Kind: LaunchControlBoolean, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "app_replay_size", WireField: "appReplaySize", Label: "App replay size", Group: LaunchGroupLimits, Kind: LaunchControlInteger, DefaultableLayers: []LaunchLayerSupport{LaunchLayerGlobal}, PerLaunch: false, DriverSupport: serfOnly},
		{Field: "system_prompt_mode", WireField: "systemPromptMode", Label: "System prompt", Group: LaunchGroupSystemPrompt, Kind: LaunchControlRadio, DefaultableLayers: defaultLayers, PerLaunch: true, Choices: systemPromptModeChoices(), DriverSupport: serfOnly},
		{Field: "system_prompt_file", WireField: "systemPromptFile", Label: "System prompt file", Group: LaunchGroupSystemPrompt, Kind: LaunchControlPath, PathKind: LaunchPathFile, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "system_prompt_text", WireField: "systemPromptText", Label: "System prompt text", Group: LaunchGroupSystemPrompt, Kind: LaunchControlMultiline, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "system_prompt_append_mode", WireField: "systemPromptAppendMode", Label: "Append to system prompt", Group: LaunchGroupSystemPrompt, Kind: LaunchControlRadio, DefaultableLayers: defaultLayers, PerLaunch: true, Choices: appendModeChoices(), DriverSupport: serfOnly},
		{Field: "system_prompt_append_file", WireField: "systemPromptAppendFile", Label: "Append file", Group: LaunchGroupSystemPrompt, Kind: LaunchControlPath, PathKind: LaunchPathFile, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "system_prompt_append_text", WireField: "systemPromptAppendText", Label: "Append text", Group: LaunchGroupSystemPrompt, Kind: LaunchControlMultiline, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "skills_dirs", WireField: "skillsDirs", Label: "Skill directories", Group: LaunchGroupResources, Kind: LaunchControlPathList, PathKind: LaunchPathDir, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "plugin_dirs", WireField: "pluginDirs", Label: "Plugin directories", Group: LaunchGroupResources, Kind: LaunchControlPathList, PathKind: LaunchPathDir, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "mcp_configs", WireField: "mcpConfigs", Label: "MCP config files", Group: LaunchGroupResources, Kind: LaunchControlPathList, PathKind: LaunchPathFile, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "mcps", WireField: "mcps", Label: "MCP servers", Group: LaunchGroupResources, Kind: LaunchControlMCPList, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "model_fallbacks", WireField: "modelFallbacks", Label: "Model fallbacks", Group: LaunchGroupResources, Kind: LaunchControlModelList, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "env", WireField: "env", Label: "Environment variables", Group: LaunchGroupEnvironment, Kind: LaunchControlEnvMap, Repeatable: true, DefaultableLayers: defaultLayers, PerLaunch: true, DriverSupport: serfOnly},
		{Field: "verbose", WireField: "verbose", Label: "Verbose event log", Group: LaunchGroupDebugLogging, Kind: LaunchControlBoolean, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: serfOnly},
		{Field: "trace_file", WireField: "traceFile", Label: "Trace file", Group: LaunchGroupDebugLogging, Kind: LaunchControlPath, PathKind: LaunchPathOutputFile, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: serfOnly},
		{Field: "cpu_profile", WireField: "cpuProfile", Label: "CPU profile", Group: LaunchGroupDebugLogging, Kind: LaunchControlPath, PathKind: LaunchPathOutputFile, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: serfOnly},
		{Field: "export_atif_path", WireField: "exportATIFPath", Label: "Export ATIF path", Group: LaunchGroupDebugLogging, Kind: LaunchControlPath, PathKind: LaunchPathOutputFile, DefaultableLayers: allLayers, PerLaunch: true, DebugOnly: true, DriverSupport: serfOnly},
	}
}
```

Add helper functions in the same file:

```go
func reasoningChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "(default)"}, {Value: "low", Label: "low"}, {Value: "medium", Label: "medium"}, {Value: "high", Label: "high"}, {Value: "xhigh", Label: "xhigh"}, {Value: "none", Label: "none"}}
}

func contextChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "(default)"}, {Value: "compact", Label: "compact"}, {Value: "recall", Label: "recall"}, {Value: "session-log", Label: "session-log"}, {Value: "ooda", Label: "ooda"}}
}

func systemPromptModeChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "Serf default"}, {Value: "file", Label: "Pick a file"}, {Value: "inline", Label: "Fill in text"}}
}

func appendModeChoices() []LaunchOptionChoice {
	return []LaunchOptionChoice{{Value: "", Label: "Do not append anything"}, {Value: "file", Label: "Pick a file"}, {Value: "inline", Label: "Fill in text"}}
}

func LaunchOptionExclusions() map[string]string {
	return map[string]string{
		"addr":                  "hub-owned process binding",
		"run_dir":               "hub-owned process state",
		"resume":                "hub-owned lifecycle control",
		"resume_last":           "hub-owned lifecycle control",
		"state_dir":             "hub-owned process state",
		"system_prompt_as_user":  "CLI-only behavior flag excluded from this UI pass",
		"output_schema":         "CLI-only eval/result behavior excluded from this UI pass",
		"result_tool_name":      "CLI-only eval/result behavior excluded from this UI pass",
		"share_task_store":      "CLI-only task behavior excluded from this UI pass",
	}
}
```

- [ ] **Step 4: Add AppWire schema types and method**

In `internal/appwire/types.go`, add:

```go
const MethodSerfLaunchSchema = "serf/launch/schema"

type LaunchOptionChoice struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type LaunchOptionEnvFallback struct {
	Name   string `json:"name"`
	Secret bool   `json:"secret,omitempty"`
}

type LaunchOption struct {
	Field             string                    `json:"field"`
	WireField         string                    `json:"wireField"`
	Label             string                    `json:"label"`
	Group             string                    `json:"group"`
	Kind              string                    `json:"kind"`
	PathKind          string                    `json:"pathKind,omitempty"`
	Repeatable        bool                      `json:"repeatable,omitempty"`
	DefaultableLayers []string                  `json:"defaultableLayers,omitempty"`
	PerLaunch         bool                      `json:"perLaunch"`
	DebugOnly         bool                      `json:"debugOnly,omitempty"`
	EnvFallback       *LaunchOptionEnvFallback  `json:"envFallback,omitempty"`
	Choices           []LaunchOptionChoice      `json:"choices,omitempty"`
	DriverSupport     map[string]bool           `json:"driverSupport,omitempty"`
}

type LaunchOptionSchemaResponse struct {
	Options []LaunchOption    `json:"options"`
	Excluded map[string]string `json:"excluded,omitempty"`
}
```

Keep the method constant with the other `MethodSerfLaunch*` constants.

- [ ] **Step 5: Add backend conversion and schema RPC**

In `cmd/serf-hub/app_launch.go`, add:

```go
func (c *hubLaunchController) Schema(ctx context.Context, params appwire.EmptyParams) (appwire.LaunchOptionSchemaResponse, error) {
	opts := launchconfig.LaunchOptionSchema()
	out := appwire.LaunchOptionSchemaResponse{
		Options:  make([]appwire.LaunchOption, 0, len(opts)),
		Excluded: launchconfig.LaunchOptionExclusions(),
	}
	for _, opt := range opts {
		wire := appwire.LaunchOption{
			Field:       opt.Field,
			WireField:   opt.WireField,
			Label:       opt.Label,
			Group:       string(opt.Group),
			Kind:        string(opt.Kind),
			PathKind:    string(opt.PathKind),
			Repeatable:  opt.Repeatable,
			PerLaunch:   opt.PerLaunch,
			DebugOnly:   opt.DebugOnly,
			DriverSupport: opt.DriverSupport,
		}
		for _, layer := range opt.DefaultableLayers {
			wire.DefaultableLayers = append(wire.DefaultableLayers, string(layer))
		}
		if opt.EnvFallback != nil {
			wire.EnvFallback = &appwire.LaunchOptionEnvFallback{Name: opt.EnvFallback.Name, Secret: opt.EnvFallback.Secret}
		}
		for _, choice := range opt.Choices {
			wire.Choices = append(wire.Choices, appwire.LaunchOptionChoice{Value: choice.Value, Label: choice.Label, Disabled: choice.Disabled, Hint: choice.Hint})
		}
		out.Options = append(out.Options, wire)
	}
	return out, nil
}
```

In `cmd/serf-hub/app_rpc.go`, register the handler beside the existing launch RPCs:

```go
appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchSchema, func(ctx context.Context, params appwire.EmptyParams) (appwire.LaunchOptionSchemaResponse, error) {
	return launchController.Schema(ctx, params)
})
```

In `cmd/serf-hub/assets/launchconfig.js`, add:

```js
schema: () => request("serf/launch/schema", {}),
```

- [ ] **Step 6: Add schema RPC test**

In `cmd/serf-hub/app_launch_test.go`, add:

```go
func TestHubLaunchControllerSchema(t *testing.T) {
	c := newHubLaunchController(t.TempDir())
	got, err := c.Schema(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if len(got.Options) == 0 {
		t.Fatal("expected schema options")
	}
	if got.Excluded["state_dir"] == "" {
		t.Fatalf("expected state_dir exclusion, got %#v", got.Excluded)
	}
	if got.Options[0].Field != "agent" {
		t.Fatalf("first schema field = %q, want agent", got.Options[0].Field)
	}
}
```

Add `context` to the `cmd/serf-hub/app_launch_test.go` import block. If the file currently has a single import string, convert it to a grouped import block.

- [ ] **Step 7: Run schema tests**

Run:

```bash
go test ./internal/launchconfig ./cmd/serf-hub -run 'TestLaunchOptionSchema|TestHubLaunchControllerSchema' -count=1
```

Expected: all selected tests pass.

- [ ] **Step 8: Commit schema slice**

```bash
git add internal/launchconfig/schema.go internal/launchconfig/schema_test.go internal/appwire/types.go cmd/serf-hub/app_launch.go cmd/serf-hub/app_rpc.go cmd/serf-hub/app_launch_test.go cmd/serf-hub/assets/launchconfig.js
git commit -m "feat: expose launch option schema"
```

## Task 2: Extend Launch Config Data Model

**Files:**
- Modify: `internal/launchconfig/types.go`
- Modify: `internal/launchconfig/merge.go`
- Modify: `internal/launchconfig/wire.go`
- Modify: `internal/launchconfig/args.go`
- Modify: `internal/launchconfig/resolver.go`
- Modify: `internal/launchconfig/args_test.go`
- Modify: `internal/launchconfig/merge_test.go`
- Modify: `internal/launchconfig/wire_test.go`
- Modify: `internal/appwire/types.go`

- [ ] **Step 1: Write failing data-model tests**

In `internal/launchconfig/args_test.go`, extend `TestToArgs_AllFields` by adding these fields to the `Layer` literal:

```go
SystemPromptMode:       "file",
SystemPromptFile:       "/system.md",
SystemPromptAppendMode: "file",
SystemPromptAppendFile: "/append.md",
Verbose:                ptrBool(true),
TraceFile:              "/tmp/trace.out",
CPUProfile:             "/tmp/cpu.pprof",
ExportATIFPath:         "/tmp/session.atif.json",
```

Add these expected args before list fields:

```go
"--system-prompt", "/system.md",
"--system-prompt-append", "/append.md",
"--verbose",
"--trace", "/tmp/trace.out",
"--cpu-profile", "/tmp/cpu.pprof",
"--export-atif", "/tmp/session.atif.json",
```

Add a focused inline prompt args test:

```go
func TestToArgs_InlinePromptTextDoesNotEmitArgv(t *testing.T) {
	got := ToArgs(Resolved{Effective: Layer{
		SystemPromptMode:       "inline",
		SystemPromptText:       "do not leak me",
		SystemPromptAppendMode: "inline",
		SystemPromptAppendText: "also secret-ish",
	}})
	for _, arg := range got {
		if strings.Contains(arg, "do not leak") || strings.Contains(arg, "also secret") {
			t.Fatalf("ToArgs leaked inline prompt text in argv: %#v", got)
		}
	}
}
```

In `internal/launchconfig/merge_test.go`, add:

```go
func TestMergeLayers_SystemPromptModesOverrideByLayer(t *testing.T) {
	resolved, _ := mergeLayers(map[LayerName]Layer{
		LayerGlobal:  {SystemPromptMode: "file", SystemPromptFile: "/global.md", SystemPromptAppendMode: "file", SystemPromptAppendFile: "/global-append.md"},
		LayerProject: {SystemPromptMode: "inline", SystemPromptText: "project", SystemPromptAppendMode: "inline", SystemPromptAppendText: "project append"},
	})
	if resolved.Effective.SystemPromptMode != "inline" || resolved.Effective.SystemPromptText != "project" {
		t.Fatalf("effective system prompt = %#v", resolved.Effective)
	}
	if resolved.Effective.SystemPromptAppendMode != "inline" || resolved.Effective.SystemPromptAppendText != "project append" {
		t.Fatalf("effective append prompt = %#v", resolved.Effective)
	}
}
```

In `internal/launchconfig/wire_test.go`, add:

```go
func TestWire_SystemPromptAndDebugFieldsRoundTrip(t *testing.T) {
	verbose := true
	in := Layer{
		SystemPromptMode: "inline", SystemPromptText: "base",
		SystemPromptAppendMode: "file", SystemPromptAppendFile: "/append.md",
		Verbose: &verbose, TraceFile: "/trace", CPUProfile: "/cpu", ExportATIFPath: "/atif",
	}
	got := FromWire(ToWire(in))
	if got.SystemPromptMode != in.SystemPromptMode || got.SystemPromptText != in.SystemPromptText {
		t.Fatalf("system prompt round trip = %#v", got)
	}
	if got.Verbose == nil || *got.Verbose != true || got.TraceFile != "/trace" || got.CPUProfile != "/cpu" || got.ExportATIFPath != "/atif" {
		t.Fatalf("debug round trip = %#v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/launchconfig -run 'TestToArgs|TestMergeLayers_SystemPrompt|TestWire_SystemPrompt' -count=1
```

Expected: compile fails because the new fields are not present.

- [ ] **Step 3: Add fields to internal and wire layer types**

In `internal/launchconfig/types.go`, add these fields near `SystemPromptAppend` and debug/resource fields:

```go
SystemPromptMode       string `toml:"system_prompt_mode,omitempty"`
SystemPromptFile       string `toml:"system_prompt_file,omitempty"`
SystemPromptText       string `toml:"system_prompt_text,omitempty"`
SystemPromptAppendMode string `toml:"system_prompt_append_mode,omitempty"`
SystemPromptAppendFile string `toml:"system_prompt_append_file,omitempty"`
SystemPromptAppendText string `toml:"system_prompt_append_text,omitempty"`
Verbose                *bool  `toml:"verbose,omitempty"`
TraceFile              string `toml:"trace_file,omitempty"`
CPUProfile             string `toml:"cpu_profile,omitempty"`
ExportATIFPath         string `toml:"export_atif_path,omitempty"`
```

In `internal/appwire/types.go`, add matching JSON fields to `LaunchConfigLayer`:

```go
SystemPromptMode       string `json:"systemPromptMode,omitempty"`
SystemPromptFile       string `json:"systemPromptFile,omitempty"`
SystemPromptText       string `json:"systemPromptText,omitempty"`
SystemPromptAppendMode string `json:"systemPromptAppendMode,omitempty"`
SystemPromptAppendFile string `json:"systemPromptAppendFile,omitempty"`
SystemPromptAppendText string `json:"systemPromptAppendText,omitempty"`
Verbose                *bool  `json:"verbose,omitempty"`
TraceFile              string `json:"traceFile,omitempty"`
CPUProfile             string `json:"cpuProfile,omitempty"`
ExportATIFPath         string `json:"exportATIFPath,omitempty"`
```

- [ ] **Step 4: Implement merge behavior**

In `internal/launchconfig/merge.go`, after context/limit scalar handling, add:

```go
if l.SystemPromptMode != "" {
	eff.SystemPromptMode = l.SystemPromptMode
	eff.SystemPromptFile = ""
	eff.SystemPromptText = ""
	if l.SystemPromptMode == "file" {
		eff.SystemPromptFile = l.SystemPromptFile
	}
	if l.SystemPromptMode == "inline" {
		eff.SystemPromptText = l.SystemPromptText
	}
	prov["system_prompt_mode"] = name
	nonEmpty = true
}
if l.SystemPromptAppendMode != "" {
	eff.SystemPromptAppendMode = l.SystemPromptAppendMode
	eff.SystemPromptAppendFile = ""
	eff.SystemPromptAppendText = ""
	eff.SystemPromptAppend = nil
	if l.SystemPromptAppendMode == "file" {
		eff.SystemPromptAppendFile = l.SystemPromptAppendFile
	}
	if l.SystemPromptAppendMode == "inline" {
		eff.SystemPromptAppendText = l.SystemPromptAppendText
	}
	prov["system_prompt_append_mode"] = name
	nonEmpty = true
}
if l.Verbose != nil {
	v := *l.Verbose
	eff.Verbose = &v
	prov["verbose"] = name
	nonEmpty = true
}
if l.TraceFile != "" {
	eff.TraceFile = l.TraceFile
	prov["trace_file"] = name
	nonEmpty = true
}
if l.CPUProfile != "" {
	eff.CPUProfile = l.CPUProfile
	prov["cpu_profile"] = name
	nonEmpty = true
}
if l.ExportATIFPath != "" {
	eff.ExportATIFPath = l.ExportATIFPath
	prov["export_atif_path"] = name
	nonEmpty = true
}
```

Change legacy `SystemPromptAppend` list merge to only apply when the new append mode is not already set at that layer:

```go
if len(l.SystemPromptAppend) > 0 && l.SystemPromptAppendMode == "" {
	eff.SystemPromptAppend = append(eff.SystemPromptAppend, l.SystemPromptAppend...)
	prov["system_prompt_append"] = name
	nonEmpty = true
}
```

- [ ] **Step 5: Implement wire conversion**

In `internal/launchconfig/wire.go`, add the new fields in both `FromWire` and `ToWire`:

```go
SystemPromptMode:       in.SystemPromptMode,
SystemPromptFile:       in.SystemPromptFile,
SystemPromptText:       in.SystemPromptText,
SystemPromptAppendMode: in.SystemPromptAppendMode,
SystemPromptAppendFile: in.SystemPromptAppendFile,
SystemPromptAppendText: in.SystemPromptAppendText,
Verbose:                copyBoolPtr(in.Verbose),
TraceFile:              in.TraceFile,
CPUProfile:             in.CPUProfile,
ExportATIFPath:         in.ExportATIFPath,
```

- [ ] **Step 6: Implement args serialization**

In `internal/launchconfig/args.go`, before list fields:

```go
if e.SystemPromptMode == "file" && e.SystemPromptFile != "" {
	add("--system-prompt", e.SystemPromptFile)
}
if e.SystemPromptAppendMode == "file" && e.SystemPromptAppendFile != "" {
	add("--system-prompt-append", e.SystemPromptAppendFile)
}
if e.Verbose != nil && *e.Verbose {
	out = append(out, "--verbose")
}
if e.TraceFile != "" {
	add("--trace", e.TraceFile)
}
if e.CPUProfile != "" {
	add("--cpu-profile", e.CPUProfile)
}
if e.ExportATIFPath != "" {
	add("--export-atif", e.ExportATIFPath)
}
```

Keep the existing legacy loop:

```go
for _, d := range e.SystemPromptAppend {
	add("--system-prompt-append", d)
}
```

This keeps old config files working while new UI emits the single append source.

- [ ] **Step 7: Expand and check new path fields**

In `internal/launchconfig/resolver.go`, extend existing path expansion/checking logic with:

```go
in.SystemPromptFile = expandOne("system_prompt_file", in.SystemPromptFile)
in.SystemPromptAppendFile = expandOne("system_prompt_append_file", in.SystemPromptAppendFile)
in.TraceFile = expandOne("trace_file", in.TraceFile)
in.CPUProfile = expandOne("cpu_profile", in.CPUProfile)
in.ExportATIFPath = expandOne("export_atif_path", in.ExportATIFPath)
```

If `expandOne` does not exist, add a local helper beside the existing slice expansion:

```go
expandOne := func(field, value string) string {
	if value == "" {
		return ""
	}
	expanded, err := expandPath(value)
	if err != nil {
		diags = append(diags, Diagnostic{Layer: layerName, Field: field, Message: err.Error()})
		return value
	}
	return expanded
}
```

- [ ] **Step 8: Run launchconfig tests**

Run:

```bash
go test ./internal/launchconfig -count=1
```

Expected: all launchconfig tests pass.

- [ ] **Step 9: Commit data-model slice**

```bash
git add internal/launchconfig internal/appwire/types.go
git commit -m "feat: add launch prompt and debug fields"
```

## Task 3: Materialize Inline System Prompt Text Safely

**Files:**
- Modify: `cmd/serf-hub/spawn.go`
- Modify: `cmd/serf-hub/spawn_test.go`

- [ ] **Step 1: Write failing spawn materialization tests**

In `cmd/serf-hub/spawn_test.go`, add:

```go
func TestPrepareResolvedForSpawn_MaterializesInlineSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	r := launchconfig.Resolved{Effective: launchconfig.Layer{
		SystemPromptMode:       "inline",
		SystemPromptText:       "base prompt",
		SystemPromptAppendMode: "inline",
		SystemPromptAppendText: "append prompt",
	}}
	got, cleanup, err := prepareResolvedForSpawn(dir, r)
	if err != nil {
		t.Fatalf("prepareResolvedForSpawn: %v", err)
	}
	defer cleanup()
	args := launchconfig.ToArgs(got)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "base prompt") || strings.Contains(joined, "append prompt") {
		t.Fatalf("inline text leaked into args: %v", args)
	}
	if got.Effective.SystemPromptMode != "file" || got.Effective.SystemPromptFile == "" {
		t.Fatalf("base prompt not materialized: %#v", got.Effective)
	}
	if got.Effective.SystemPromptAppendMode != "file" || got.Effective.SystemPromptAppendFile == "" {
		t.Fatalf("append prompt not materialized: %#v", got.Effective)
	}
	base, err := os.ReadFile(got.Effective.SystemPromptFile)
	if err != nil || string(base) != "base prompt" {
		t.Fatalf("base file = %q, err=%v", string(base), err)
	}
}
```

Add `os` and `strings` to the `cmd/serf-hub/spawn_test.go` import block:

```go
import (
	"os"
	"strings"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./cmd/serf-hub -run TestPrepareResolvedForSpawn_MaterializesInlineSystemPrompt -count=1
```

Expected: fails because `prepareResolvedForSpawn` is undefined.

- [ ] **Step 3: Implement prompt materialization helper**

In `cmd/serf-hub/spawn.go`, add:

```go
func prepareResolvedForSpawn(stateDir string, resolved launchconfig.Resolved) (launchconfig.Resolved, func(), error) {
	cleanup := func() {}
	if resolved.Effective.SystemPromptMode != "inline" && resolved.Effective.SystemPromptAppendMode != "inline" {
		return resolved, cleanup, nil
	}
	if stateDir == "" {
		return resolved, cleanup, fmt.Errorf("state dir required for inline system prompt")
	}
	promptDir, err := os.MkdirTemp(stateDir, "launch-prompts-")
	if err != nil {
		return resolved, cleanup, fmt.Errorf("create launch prompt dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(promptDir) }
	writePrompt := func(name, body string) (string, error) {
		path := promptDir + string(os.PathSeparator) + name
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return "", err
		}
		return path, nil
	}
	if resolved.Effective.SystemPromptMode == "inline" {
		path, err := writePrompt("system-prompt.md", resolved.Effective.SystemPromptText)
		if err != nil {
			cleanup()
			return resolved, func() {}, fmt.Errorf("write system prompt: %w", err)
		}
		resolved.Effective.SystemPromptMode = "file"
		resolved.Effective.SystemPromptFile = path
		resolved.Effective.SystemPromptText = ""
	}
	if resolved.Effective.SystemPromptAppendMode == "inline" {
		path, err := writePrompt("system-prompt-append.md", resolved.Effective.SystemPromptAppendText)
		if err != nil {
			cleanup()
			return resolved, func() {}, fmt.Errorf("write system prompt append: %w", err)
		}
		resolved.Effective.SystemPromptAppendMode = "file"
		resolved.Effective.SystemPromptAppendFile = path
		resolved.Effective.SystemPromptAppendText = ""
	}
	return resolved, cleanup, nil
}
```

- [ ] **Step 4: Call helper from spawn and resume**

In `HubSpawner.Spawn`, after `req.StateDir` is resolved and before `ToEnv`:

```go
prepared, cleanup, err := prepareResolvedForSpawn(req.StateDir, req.Resolved)
if err != nil {
	return rendezvous.Entry{}, err
}
defer cleanup()
req.Resolved = prepared
```

Repeat the same block in `HubSpawner.Resume`.

- [ ] **Step 5: Run spawn tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestPrepareResolvedForSpawn|TestBuildSpawnArgs' -count=1
```

Expected: selected tests pass.

- [ ] **Step 6: Commit materialization slice**

```bash
git add cmd/serf-hub/spawn.go cmd/serf-hub/spawn_test.go
git commit -m "feat: materialize inline launch prompts"
```

## Task 4: Render Schema-Backed Web Spawn Advanced

**Files:**
- Modify: `cmd/serf-hub/templates/partials/spawn.html`
- Modify: `cmd/serf-hub/assets/spawn.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Modify: `cmd/serf-hub/web_test.go`

- [ ] **Step 1: Write failing web tests for Advanced hooks**

In `cmd/serf-hub/web_test.go`, add a render test near other spawn template tests:

```go
func TestSpawnTemplate_HasSchemaAdvancedRoot(t *testing.T) {
	body := renderSpawnPartialForTest(t, WebData{})
	for _, want := range []string{
		`data-launch-advanced-root`,
		`data-launch-schema-loading`,
		`data-launch-env-fallbacks`,
		`Launch defaults`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("spawn partial missing %q:\n%s", want, body)
		}
	}
}
```

Use the same helper that existing spawn partial tests in `cmd/serf-hub/web_test.go` use to render the `spawn` template. Do not add a second generic template renderer in this task.

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./cmd/serf-hub -run TestSpawnTemplate_HasSchemaAdvancedRoot -count=1
```

Expected: fails because the schema roots are absent.

- [ ] **Step 3: Replace hard-coded Advanced body with schema roots**

In `cmd/serf-hub/templates/partials/spawn.html`, keep the surrounding `<details class="spawn-advanced">` and replace the hard-coded fieldsets with:

```html
<div class="spawn-advanced-body" data-launch-advanced-root>
  <p class="settings-help spawn-advanced-note">
    Per-launch overrides for this thread only. Defaults come from
    <a href="/settings/launch-serf">Launch defaults</a> and any per-project layers.
  </p>
  <div class="settings-help" data-launch-schema-loading>Loading advanced options…</div>
  <div data-launch-env-fallbacks></div>
  <div data-launch-advanced-groups></div>
  <div class="spawn-advanced-actions">
    <button type="button" id="ovr-show-resolved">show resolved config</button>
  </div>
  <pre id="ovr-resolved-out" class="settings-code"></pre>
</div>
```

- [ ] **Step 4: Implement schema renderer helpers in spawn.js**

In `cmd/serf-hub/assets/spawn.js`, add helpers above `collectAdvancedOverrides`:

```js
let launchSchema = null;

function launchFieldName(opt) {
  return "launch-" + opt.wireField;
}

function optionAppliesToLaunch(opt) {
  return !!opt.perLaunch && (!opt.driverSupport || opt.driverSupport.serf);
}

function renderLaunchOption(opt, current) {
  const row = document.createElement("div");
  row.className = "spawn-advanced-row";
  row.dataset.launchField = opt.wireField;
  const label = document.createElement("label");
  label.className = "spawn-advanced-label";
  label.textContent = opt.label;
  row.appendChild(label);
  const control = renderLaunchControl(opt, current);
  row.appendChild(control);
  const msg = document.createElement("div");
  msg.className = "spawn-advanced-validation";
  msg.dataset.validationFor = opt.wireField;
  row.appendChild(msg);
  return row;
}
```

Add `renderLaunchControl` with branches for the control kinds used in the schema:

```js
function renderLaunchControl(opt, current) {
  if (opt.kind === "boolean") {
    const input = document.createElement("input");
    input.type = "checkbox";
    input.name = launchFieldName(opt);
    input.checked = !!current;
    return input;
  }
  if (opt.kind === "select") {
    const select = document.createElement("select");
    select.name = launchFieldName(opt);
    (opt.choices || []).forEach(choice => {
      const option = document.createElement("option");
      option.value = choice.value;
      option.textContent = choice.label;
      if ((current || "") === choice.value) option.selected = true;
      select.appendChild(option);
    });
    return select;
  }
  if (opt.kind === "radio") {
    const wrap = document.createElement("div");
    wrap.className = "spawn-radio-group";
    (opt.choices || []).forEach(choice => {
      const label = document.createElement("label");
      label.className = "spawn-radio-option";
      const input = document.createElement("input");
      input.type = "radio";
      input.name = launchFieldName(opt);
      input.value = choice.value;
      input.checked = (current || "") === choice.value;
      label.appendChild(input);
      label.appendChild(document.createTextNode(choice.label));
      wrap.appendChild(label);
    });
    return wrap;
  }
  if (opt.kind === "multilineText") {
    const textarea = document.createElement("textarea");
    textarea.name = launchFieldName(opt);
    textarea.className = "spawn-advanced-textarea";
    textarea.value = current || "";
    return textarea;
  }
  if (opt.kind === "pathList" || opt.kind === "modelList" || opt.kind === "mcpServerList" || opt.kind === "envMap") {
    return renderLaunchListControl(opt, current);
  }
  const input = document.createElement("input");
  input.name = launchFieldName(opt);
  input.type = opt.kind === "integer" ? "number" : "text";
  input.value = current || "";
  if (opt.pathKind) input.dataset.settingsDirInput = "true";
  if (opt.kind === "modelPicker") input.dataset.launchModelInput = "true";
  return input;
}
```

Implement `renderLaunchListControl` as a vertical list with the add row after existing values:

```js
function renderLaunchListControl(opt, current) {
  const wrap = document.createElement("div");
  wrap.className = "spawn-list-control";
  const list = document.createElement("ul");
  list.className = "settings-items-list";
  list.dataset.listFor = opt.wireField;
  const values = Array.isArray(current) ? current : [];
  values.forEach(value => appendLaunchListItem(list, value));
  wrap.appendChild(list);
  const add = document.createElement("div");
  add.className = "settings-add-row";
  const input = document.createElement("input");
  input.type = "text";
  input.dataset.addFor = opt.wireField;
  if (opt.pathKind) input.dataset.settingsDirInput = "true";
  add.appendChild(input);
  const button = document.createElement("button");
  button.type = "button";
  button.textContent = "Add";
  button.addEventListener("click", () => addLaunchListValue(opt, input, list));
  add.appendChild(button);
  wrap.appendChild(add);
  return wrap;
}
```

Implement list item add/remove:

```js
function appendLaunchListItem(list, value) {
  const li = document.createElement("li");
  li.dataset.value = typeof value === "string" ? value : JSON.stringify(value);
  const code = document.createElement("code");
  code.textContent = li.dataset.value;
  li.appendChild(code);
  const remove = document.createElement("button");
  remove.type = "button";
  remove.className = "danger";
  remove.textContent = "remove";
  remove.addEventListener("click", () => li.remove());
  li.appendChild(remove);
  list.appendChild(li);
}
```

- [ ] **Step 5: Add add-time path validation**

In `cmd/serf-hub/assets/spawn.js`, add:

```js
async function addLaunchListValue(opt, input, list) {
  const value = input.value.trim();
  if (!value) return;
  const message = list.closest(".spawn-advanced-row").querySelector(".spawn-advanced-validation");
  if (opt.pathKind) {
    const kind = opt.pathKind === "outputFile" ? "output-file" : opt.pathKind;
    const result = await launchconfig.validatePath(value, kind);
    if (result && result.ok === false) {
      message.textContent = result.error || "Path does not exist";
      return;
    }
  }
  message.textContent = "";
  appendLaunchListItem(list, value);
  input.value = "";
}
```

- [ ] **Step 6: Render schema on Advanced open**

In `init()`, replace `attachListAdd("ovr-skill"...` calls with:

```js
initLaunchAdvanced(form);
```

Add:

```js
async function initLaunchAdvanced(form) {
  const root = form.querySelector("[data-launch-advanced-root]");
  if (!root) return;
  try {
    const schema = await launchconfig.schema();
    launchSchema = schema.options || [];
    renderLaunchAdvanced(form, launchSchema);
  } catch (err) {
    const loading = root.querySelector("[data-launch-schema-loading]");
    if (loading) loading.textContent = "Failed to load advanced options: " + (err && err.message ? err.message : err);
  }
}

function renderLaunchAdvanced(form, schema) {
  const loading = form.querySelector("[data-launch-schema-loading]");
  if (loading) loading.remove();
  const groupsRoot = form.querySelector("[data-launch-advanced-groups]");
  groupsRoot.replaceChildren();
  const byGroup = new Map();
  schema.filter(optionAppliesToLaunch).forEach(opt => {
    if (!byGroup.has(opt.group)) byGroup.set(opt.group, []);
    byGroup.get(opt.group).push(opt);
  });
  byGroup.forEach((opts, group) => {
    const fieldset = document.createElement("fieldset");
    fieldset.className = "spawn-advanced-group";
    const legend = document.createElement("legend");
    legend.textContent = group;
    fieldset.appendChild(legend);
    opts.forEach(opt => fieldset.appendChild(renderLaunchOption(opt, undefined)));
    groupsRoot.appendChild(fieldset);
  });
  if (window.SettingsPickers) window.SettingsPickers.init(groupsRoot);
  renderEnvFallbacks(form, schema);
}
```

- [ ] **Step 7: Show non-secret env fallback values on per-launch only**

Add:

```js
function renderEnvFallbacks(form, schema) {
  const root = form.querySelector("[data-launch-env-fallbacks]");
  if (!root) return;
  root.replaceChildren();
  const fallbacks = schema.filter(opt => opt.envFallback && !opt.envFallback.secret);
  if (fallbacks.length === 0) return;
  const box = document.createElement("div");
  box.className = "spawn-env-fallbacks";
  fallbacks.forEach(opt => {
    const value = window.SERF_ENV && Object.prototype.hasOwnProperty.call(window.SERF_ENV, opt.envFallback.name)
      ? window.SERF_ENV[opt.envFallback.name]
      : "";
    const row = document.createElement("div");
    row.className = "settings-help";
    row.textContent = value ? (opt.envFallback.name + " is set: " + value) : (opt.envFallback.name + " is not set");
    box.appendChild(row);
  });
  root.appendChild(box);
}
```

Add a safe non-secret environment map to spawn page data before this renderer is enabled. The map must include only the environment variables named by non-secret schema fallbacks, currently `SERF_MODEL` and `SERF_REASONING_EFFORT`. Render it before `spawn.js` runs:

```html
<script>
  window.SERF_ENV = {
    SERF_MODEL: {{printf "%q" (index .SafeEnv "SERF_MODEL")}},
    SERF_REASONING_EFFORT: {{printf "%q" (index .SafeEnv "SERF_REASONING_EFFORT")}}
  };
</script>
```

Add `SafeEnv map[string]string` to the spawn template data and populate it from `os.LookupEnv` with an allowlist:

```go
func safeLaunchEnvForTemplate() map[string]string {
	out := map[string]string{}
	for _, key := range []string{"SERF_MODEL", "SERF_REASONING_EFFORT"} {
		if value, ok := os.LookupEnv(key); ok {
			out[key] = value
		}
	}
	return out
}
```

- [ ] **Step 8: Update override collection**

Replace `collectAdvancedOverrides()` with schema-based collection:

```js
function collectAdvancedOverrides() {
  if (!launchSchema) return undefined;
  const overrides = {};
  launchSchema.filter(optionAppliesToLaunch).forEach(opt => {
    const value = readLaunchControlValue(opt);
    if (value === undefined) return;
    overrides[opt.wireField] = value;
  });
  return Object.keys(overrides).length ? overrides : undefined;
}

function readLaunchControlValue(opt) {
  const name = launchFieldName(opt);
  if (opt.kind === "boolean") {
    const el = document.querySelector(`[name="${name}"]`);
    return el && el.checked ? true : undefined;
  }
  if (opt.kind === "integer") {
    const el = document.querySelector(`[name="${name}"]`);
    return el && el.value !== "" ? +el.value : undefined;
  }
  if (opt.kind === "radio") {
    const el = document.querySelector(`[name="${name}"]:checked`);
    return el && el.value !== "" ? el.value : undefined;
  }
  if (opt.kind === "pathList" || opt.kind === "modelList") {
    const list = document.querySelector(`[data-list-for="${opt.wireField}"]`);
    const values = list ? Array.from(list.querySelectorAll("li")).map(li => li.dataset.value).filter(Boolean) : [];
    return values.length ? values : undefined;
  }
  const el = document.querySelector(`[name="${name}"]`);
  if (!el) return undefined;
  const value = el.value.trim();
  return value === "" ? undefined : value;
}
```

- [ ] **Step 9: Add CSS for vertical rows**

In `cmd/serf-hub/assets/style.css`, add styles that match current settings/spawn styling:

```css
.spawn-advanced-row {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 10px 0;
}
.spawn-advanced-label {
  color: var(--text-muted);
  font-size: 12px;
}
.spawn-radio-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.spawn-radio-option {
  display: flex;
  align-items: center;
  gap: 8px;
}
.spawn-advanced-textarea {
  min-height: 120px;
  resize: vertical;
}
.spawn-advanced-validation {
  color: var(--danger);
  font-size: 12px;
}
```

- [ ] **Step 10: Run web spawn tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestSpawnTemplate_HasSchemaAdvancedRoot|TestThreadStart_LaunchOverridesApplied' -count=1
```

Expected: selected tests pass.

- [ ] **Step 11: Commit web spawn slice**

```bash
git add cmd/serf-hub/templates/partials/spawn.html cmd/serf-hub/assets/spawn.js cmd/serf-hub/assets/style.css cmd/serf-hub/web_test.go
git commit -m "feat: render schema-backed spawn advanced"
```

## Task 5: Render Schema-Backed Web Launch Defaults

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/launch-serf.html`
- Modify: `cmd/serf-hub/templates/partials/settings/project.html`
- Modify: `cmd/serf-hub/assets/settings-pickers.js`
- Modify: `cmd/serf-hub/web.go`
- Modify: `cmd/serf-hub/web_test.go`

- [ ] **Step 1: Write failing settings render tests**

In `cmd/serf-hub/web_test.go`, add:

```go
func TestLaunchSerfSettings_UsesSchemaRoot(t *testing.T) {
	body := renderSettingsPartialForTest(t, "launch-serf", nil)
	for _, want := range []string{`data-launch-settings-root`, `data-launch-settings-layer="global"`, `data-launch-settings-groups`} {
		if !strings.Contains(body, want) {
			t.Fatalf("launch settings missing %q:\n%s", want, body)
		}
	}
}
```

Use the same helper that existing settings partial tests in `cmd/serf-hub/web_test.go` use to render settings templates. Do not add a second generic template renderer in this task.

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./cmd/serf-hub -run TestLaunchSerfSettings_UsesSchemaRoot -count=1
```

Expected: fails because the schema root is not present.

- [ ] **Step 3: Replace global launch settings form with schema root**

In `cmd/serf-hub/templates/partials/settings/launch-serf.html`, keep the title/help text and replace the form body with:

```html
<div id="launch-form" class="settings-launch-form" data-launch-settings-root data-launch-settings-layer="global" data-cwd="/">
  <div class="settings-help" data-launch-settings-loading>Loading launch defaults…</div>
  <div data-launch-settings-groups></div>
  <div class="form-actions">
    <button type="button" data-launch-settings-save>Save launch defaults</button>
    <p id="launch-form-status" class="settings-help" style="margin:0"></p>
  </div>
</div>
<script>
  window.LaunchSettings && window.LaunchSettings.init(document.getElementById("launch-form"));
</script>
```

- [ ] **Step 4: Add reusable LaunchSettings JS**

Put reusable launch settings rendering in `cmd/serf-hub/assets/launchconfig.js`, because `app.html` already loads that asset before `spawn.js` and `settings-pickers.js`. Expose:

```js
window.LaunchSettings = window.LaunchSettings || {
  async init(root) {
    if (!root) return;
    const cwd = root.dataset.cwd || "/";
    const layer = root.dataset.launchSettingsLayer || "global";
    const [schema, current] = await Promise.all([
      launchconfig.schema(),
      launchconfig.getLayer(cwd, layer),
    ]);
    root.__launchSchema = (schema.options || []).filter(opt => (opt.defaultableLayers || []).includes(layer));
    root.__launchCurrent = current || {};
    renderLaunchSettings(root);
  }
};
```

Move the shared vertical row/control functions from Task 4 into `cmd/serf-hub/assets/launchconfig.js` under `window.LaunchConfigControls`:

```js
window.LaunchConfigControls = {
  renderOption,
  readValue,
  validateAndAppendListValue,
};
```

The shared renderer must take a mode parameter:

```js
renderOption(opt, currentValue, { mode: "settings" })
```

Settings mode must not render env fallback choices or environment fallback values.

- [ ] **Step 5: Render project defaults from the same schema**

In `cmd/serf-hub/templates/partials/settings/project.html`, keep the project picker and top-level `<div id="project-settings-root"...>`. Inside `render()`, replace the hard-coded Launch defaults section with:

```js
root.innerHTML = `
  <h3 class="settings-h3">Launch defaults</h3>
  <div id="proj-launch-form" class="settings-launch-form" data-launch-settings-root data-launch-settings-layer="project" data-cwd="${escapeHtml(cwd)}">
    <div data-launch-settings-groups></div>
    <div class="form-actions">
      <button type="button" id="proj-launch-save" data-launch-settings-save>Save launch defaults</button>
      <p id="proj-status" class="settings-help" style="margin:0"></p>
    </div>
  </div>
`;
```

After injecting the root, initialize:

```js
const launchRoot = root.querySelector("[data-launch-settings-root]");
launchRoot.__launchSchema = cachedLaunchSchema.filter(opt => (opt.defaultableLayers || []).includes("project"));
launchRoot.__launchCurrent = current;
window.LaunchSettings.render(launchRoot);
```

Keep existing project list sections for plugins, skills, MCPs, prompt append, and env only until the shared list controls cover them. Once shared list controls are active, remove duplicate hard-coded sections so each field appears once.

- [ ] **Step 6: Add validation before settings save**

In the shared settings save function, validate path fields before calling `setLayer`:

```js
async function validateLaunchSettings(root, schema, draft) {
  for (const opt of schema) {
    if (!opt.pathKind) continue;
    const value = draft[opt.wireField];
    const values = Array.isArray(value) ? value : (value ? [value] : []);
    for (const item of values) {
      const kind = opt.pathKind === "outputFile" ? "output-file" : opt.pathKind;
      const result = await launchconfig.validatePath(item, kind);
      if (result && result.ok === false) {
        throw new Error(opt.label + ": " + (result.error || "invalid path"));
      }
    }
  }
}
```

For `env`, rely on `serf/launch/setLayer` credential-key rejection and show the returned error inline.

- [ ] **Step 7: Run web settings tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestLaunchSerfSettings_UsesSchemaRoot|TestHubLaunchController|TestThreadStart_LaunchOverridesApplied' -count=1
```

Expected: selected tests pass.

- [ ] **Step 8: Commit web settings slice**

```bash
git add cmd/serf-hub/templates/partials/settings/launch-serf.html cmd/serf-hub/templates/partials/settings/project.html cmd/serf-hub/assets/launchconfig.js cmd/serf-hub/assets/settings-pickers.js cmd/serf-hub/web.go cmd/serf-hub/web_test.go
git commit -m "feat: render schema-backed launch settings"
```

## Task 6: Render Schema-Backed TUI Settings and Overrides

**Files:**
- Create: `cmd/serf-tui/launch_schema.go`
- Modify: `cmd/serf-tui/launchconfig_client.go`
- Modify: `cmd/serf-tui/launch_settings_panel.go`
- Modify: `cmd/serf-tui/launch_overrides_modal.go`
- Modify: `cmd/serf-tui/hub_model.go`
- Modify: `cmd/serf-tui/launch_settings_panel_test.go`
- Modify: `cmd/serf-tui/launch_overrides_modal_test.go`

- [ ] **Step 1: Write failing TUI schema adapter tests**

Create `cmd/serf-tui/launch_schema_test.go`:

```go
package main

import (
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

func TestSchemaRows_PutsAgentFirstAndReasoningWithModel(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "model", WireField: "model", Label: "Model", Group: "Model", Kind: "modelPicker", PerLaunch: true, DefaultableLayers: []string{"global"}},
		{Field: "agent", WireField: "agent", Label: "Agent", Group: "Agent", Kind: "text", PerLaunch: true, DefaultableLayers: []string{"global"}},
		{Field: "fast_cheap_model", WireField: "fastCheapModel", Label: "Fast cheap model", Group: "Model", Kind: "modelPicker", PerLaunch: true, DefaultableLayers: []string{"global"}},
		{Field: "reasoning_effort", WireField: "reasoningEffort", Label: "Reasoning effort", Group: "Model", Kind: "select", PerLaunch: true, DefaultableLayers: []string{"global"}},
	}
	rows := schemaRows(schema, appwire.LaunchConfigLayer{ReasoningEffort: "high"}, schemaRowModeSettings, "global")
	if rows[0].field != "agent" {
		t.Fatalf("first row = %q, want agent", rows[0].field)
	}
	if rows[2].field != "reasoning_effort" {
		t.Fatalf("row 2 = %q, want reasoning_effort grouped with model", rows[2].field)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./cmd/serf-tui -run TestSchemaRows_PutsAgentFirstAndReasoningWithModel -count=1
```

Expected: fails because `schemaRows`, `schemaRowModeSettings`, and adapter types do not exist.

- [ ] **Step 3: Implement TUI schema adapter**

Create `cmd/serf-tui/launch_schema.go`:

```go
package main

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/internal/appwire"
)

type schemaRowMode string

const (
	schemaRowModeSettings schemaRowMode = "settings"
	schemaRowModeLaunch   schemaRowMode = "launch"
)

func schemaRows(schema []appwire.LaunchOption, layer appwire.LaunchConfigLayer, mode schemaRowMode, settingsLayer string) []layerRow {
	var rows []layerRow
	for _, opt := range schema {
		if mode == schemaRowModeLaunch && !opt.PerLaunch {
			continue
		}
		if mode == schemaRowModeSettings && !stringIn(opt.DefaultableLayers, settingsLayer) {
			continue
		}
		rows = append(rows, layerRow{
			field:     opt.Field,
			label:     opt.Label,
			value:     schemaValue(opt, layer),
			editValue: schemaEditValue(opt, layer),
		})
	}
	return rows
}

func stringIn(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
```

Add value helpers for all schema fields:

```go
func schemaValue(opt appwire.LaunchOption, l appwire.LaunchConfigLayer) string {
	v := schemaEditValue(opt, l)
	if v == "" {
		return "(default)"
	}
	if opt.Kind == "pathList" || opt.Kind == "modelList" || opt.Kind == "mcpServerList" {
		return fmt.Sprintf("%d entries", len(splitTrim(v, ",")))
	}
	return v
}

func schemaEditValue(opt appwire.LaunchOption, l appwire.LaunchConfigLayer) string {
	switch opt.Field {
	case "agent":
		return l.Agent
	case "model":
		return l.Model
	case "reasoning_effort":
		return l.ReasoningEffort
	case "fast_cheap_model":
		return l.FastCheapModel
	case "context_strategy":
		return l.ContextStrategy
	case "skills_dirs":
		return strings.Join(l.SkillsDirs, ", ")
	case "plugin_dirs":
		return strings.Join(l.PluginDirs, ", ")
	case "mcp_configs":
		return strings.Join(l.MCPConfigs, ", ")
	case "model_fallbacks":
		return strings.Join(l.ModelFallbacks, ", ")
	case "system_prompt_mode":
		return l.SystemPromptMode
	case "system_prompt_file":
		return l.SystemPromptFile
	case "system_prompt_text":
		return l.SystemPromptText
	case "system_prompt_append_mode":
		return l.SystemPromptAppendMode
	case "system_prompt_append_file":
		return l.SystemPromptAppendFile
	case "system_prompt_append_text":
		return l.SystemPromptAppendText
	case "trace_file":
		return l.TraceFile
	case "cpu_profile":
		return l.CPUProfile
	case "export_atif_path":
		return l.ExportATIFPath
	}
	return legacyLayerEditValue(opt.Field, l)
}
```

Move current `layerRows` scalar value logic into `legacyLayerEditValue` to keep `max_rounds`, `max_subagent_depth`, `no_project_prompts`, `app_replay_size`, `mcps`, and `env` working during the transition.

- [ ] **Step 4: Fetch schema in TUI client**

In `cmd/serf-tui/launchconfig_client.go`, add message and command:

```go
type launchSchemaResultMsg struct {
	Schema []appwire.LaunchOption
	Err    error
}

func cmdLaunchSchema(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		var resp appwire.LaunchOptionSchemaResponse
		err := client.Request(context.Background(), appwire.MethodSerfLaunchSchema, appwire.EmptyParams{}, &resp)
		return launchSchemaResultMsg{Schema: resp.Options, Err: err}
	}
}
```

Add `context` and `tea "github.com/charmbracelet/bubbletea"` to `cmd/serf-tui/launchconfig_client.go` imports.

- [ ] **Step 5: Store schema in settings panel and overrides modal**

In `launchSettingsPanel`, add:

```go
schema        []appwire.LaunchOption
loadingSchema bool
```

Initialize `loadingSchema: true` in `newLaunchSettingsPanel`, add `cmdLaunchSchema(p.client)` to `initialCmd`, and handle:

```go
case launchSchemaResultMsg:
	p.loadingSchema = false
	if m.Err != nil {
		p.statusMessage = "schema error: " + m.Err.Error()
	} else {
		p.schema = m.Schema
	}
```

Change `renderLayerView` signature:

```go
func renderLayerView(label string, l appwire.LaunchConfigLayer, cursor int, schema []appwire.LaunchOption) string
```

Use:

```go
rows := schemaRows(schema, l, schemaRowModeSettings, label)
```

When `schema` is empty, fall back to the existing hard-coded `layerRows(l)` so offline tests and older hubs still render.

- [ ] **Step 6: Use schema rows in per-launch modal**

Add `schema []appwire.LaunchOption` to `launchOverridesModal`. Change constructors:

```go
func newLaunchOverridesModalWith(initial appwire.LaunchConfigLayer, schema []appwire.LaunchOption) launchOverridesModal {
	return launchOverridesModal{cur: initial, schema: schema}
}
```

Keep a compatibility helper for existing tests:

```go
func newLaunchOverridesModalWithLegacy(initial appwire.LaunchConfigLayer) launchOverridesModal {
	return launchOverridesModal{cur: initial}
}
```

In modal `Update` and `View`, choose rows with:

```go
rows := layerRows(m.cur)
if len(m.schema) > 0 {
	rows = schemaRows(m.schema, m.cur, schemaRowModeLaunch, "")
}
```

Update existing tests to pass `nil` or call the legacy helper.

- [ ] **Step 7: Apply schema-backed edits**

Extend `applyEdit` in `launch_settings_panel.go` for new fields:

```go
case "system_prompt_mode":
	layer.SystemPromptMode = strings.TrimSpace(value)
case "system_prompt_file":
	if strings.TrimSpace(value) != "" {
		if err := validateLocalLaunchPath(value, "file"); err != nil {
			return layer, err
		}
	}
	layer.SystemPromptFile = strings.TrimSpace(value)
case "system_prompt_text":
	layer.SystemPromptText = value
case "system_prompt_append_mode":
	layer.SystemPromptAppendMode = strings.TrimSpace(value)
case "system_prompt_append_file":
	if strings.TrimSpace(value) != "" {
		if err := validateLocalLaunchPath(value, "file"); err != nil {
			return layer, err
		}
	}
	layer.SystemPromptAppendFile = strings.TrimSpace(value)
case "system_prompt_append_text":
	layer.SystemPromptAppendText = value
case "model_fallbacks":
	layer.ModelFallbacks = splitTrim(value, ",")
	layer.ModelFallbacksSet = true
case "verbose":
	layer.Verbose, err = parseOptionalBool(value)
	if err != nil {
		return layer, err
	}
case "trace_file", "cpu_profile", "export_atif_path":
	if strings.TrimSpace(value) != "" {
		if err := validateOutputFileParent(value); err != nil {
			return layer, err
		}
	}
	switch field {
	case "trace_file":
		layer.TraceFile = strings.TrimSpace(value)
	case "cpu_profile":
		layer.CPUProfile = strings.TrimSpace(value)
	case "export_atif_path":
		layer.ExportATIFPath = strings.TrimSpace(value)
	}
```

Add:

```go
func parseOptionalBool(value string) (*bool, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "(default)":
		return nil, nil
	case "true", "yes", "1":
		v := true
		return &v, nil
	case "false", "no", "0":
		v := false
		return &v, nil
	default:
		return nil, fmt.Errorf("bool required, got %q", value)
	}
}

func validateOutputFileParent(path string) error {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") || path == "~" {
		path = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(path, "~"))
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute path required")
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("parent is not a directory")
	}
	return nil
}
```

- [ ] **Step 8: Path completion for schema path fields**

Change `launchSettingsFieldUsesPathCompletion` to consult schema when available. Add a helper:

```go
func schemaFieldUsesPathCompletion(schema []appwire.LaunchOption, field string) bool {
	for _, opt := range schema {
		if opt.Field == field {
			return opt.PathKind == "dir" || opt.PathKind == "file" || opt.PathKind == "outputFile" || opt.PathKind == "command"
		}
	}
	return launchSettingsFieldUsesPathCompletion(field)
}
```

Thread path completion through `launchSettingsEditRequestMsg` by adding:

```go
PathCompletion bool
```

Set it in `editCurrent` and `launchOverridesModal.Update`:

```go
PathCompletion: schemaFieldUsesPathCompletion(p.schema, row.field),
```

and:

```go
PathCompletion: schemaFieldUsesPathCompletion(m.schema, row.field),
```

In `hub_model.go`, when handling `launchSettingsEditRequestMsg`, pass `PathCompletion` to the existing text input modal configuration. If the modal currently calls `launchSettingsFieldUsesPathCompletion(req.Field)`, replace that call with `req.PathCompletion || launchSettingsFieldUsesPathCompletion(req.Field)`.

- [ ] **Step 9: Run TUI tests**

Run:

```bash
go test ./cmd/serf-tui -run 'TestSchemaRows|TestLaunchSettingsPanel|TestLaunchOverridesModal|TestApplyEdit' -count=1
```

Expected: selected tests pass.

- [ ] **Step 10: Commit TUI slice**

```bash
git add cmd/serf-tui/launch_schema.go cmd/serf-tui/launch_schema_test.go cmd/serf-tui/launchconfig_client.go cmd/serf-tui/launch_settings_panel.go cmd/serf-tui/launch_overrides_modal.go cmd/serf-tui/hub_model.go cmd/serf-tui/launch_settings_panel_test.go cmd/serf-tui/launch_overrides_modal_test.go
git commit -m "feat: render schema-backed launch settings in tui"
```

## Task 7: Final Integration Verification

**Files:**
- Modify only files that previous tasks touched if verification exposes issues.

- [ ] **Step 1: Run focused package tests**

Run:

```bash
go test ./internal/launchconfig ./cmd/serf-hub ./cmd/serf-tui -count=1
```

Expected: all three packages pass.

- [ ] **Step 2: Run full Go test suite**

Run:

```bash
go test ./... -count=1
```

Expected: full suite passes. If a known slow/live test fails due to missing external credentials, record the exact failing test and rerun the focused non-live packages from Step 1.

- [ ] **Step 3: Manual web verification**

Start hub bound for remote access:

```bash
go run ./cmd/serf-hub serve --addr 0.0.0.0:7777
```

Open `/new` and verify:

- Advanced opens below the existing prompt and chips.
- Agent is the first Advanced group.
- Model group contains Model, Reasoning effort, and Fast cheap model in that order.
- System Prompt uses a radio group for Serf default, Pick a file, and Fill in text.
- Append to system prompt uses a separate radio group for Do not append anything, Pick a file, and Fill in text.
- Skill/plugin/MCP/model fallback lists render vertically, with Add controls on a new line below existing values.
- Non-secret env fallback values appear on per-launch Advanced only.
- No API token or secret-looking environment value is displayed.
- Settings pages do not show environment fallback choices.

- [ ] **Step 4: Manual TUI verification**

Run:

```bash
go run ./cmd/serf-tui
```

Verify:

- Launch settings load schema-backed global/project rows.
- Repo tab stays read-only/trust-gated.
- Per-launch overrides show Agent first and Reasoning effort in the Model group.
- Prompt file fields, append file fields, and debug output path fields open the same path-completion flow used by existing path fields.
- Missing plugin/skill/MCP prompt paths are rejected before saving.

- [ ] **Step 5: Final commit for verification fixes**

If Step 1-4 required fixes, commit the exact modified files shown by `git status --short`:

```bash
git status --short
git commit -m "fix: polish launch options parity"
```

Between the two commands, run `git add` with the exact modified files printed by `git status --short`. Do not use `git add .`.

If no fixes were needed, do not create an empty commit.
