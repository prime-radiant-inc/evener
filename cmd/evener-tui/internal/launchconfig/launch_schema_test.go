package launchconfig

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestSchemaRows_SettingsFiltersDefaultableLayerAndKeepsOrder(t *testing.T) {
	schema := testLaunchSchema()

	// Layer "project": app_replay_size is excluded because it is only defaultable in "global".
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{Agent: "evener", ReasoningEffort: "high", FastCheapModel: "mini"}, launchLayerProject, launchSchemaRowsSettings, appwire.LaunchConfigLayer{})
	fields := rowFields(rows)
	want := []string{"agent", "model", "reasoning_effort", "fast_cheap_model", "openai_responses_continuation", "system_prompt_file", "mcps", "verbose", "export_atif_provider_handles"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("project-layer fields=%v, want %v", fields, want)
	}

	// Layer "global": app_replay_size IS included because it is defaultable in "global" but
	// PerLaunch=false. This diverges from override mode (which filters by PerLaunch): settings
	// mode must use DefaultableLayers, not PerLaunch, to include this field.
	rows = launchSchemaRows(schema, appwire.LaunchConfigLayer{}, launchLayerGlobal, launchSchemaRowsSettings, appwire.LaunchConfigLayer{})
	fields = rowFields(rows)
	wantGlobal := []string{"agent", "model", "reasoning_effort", "fast_cheap_model", "openai_responses_continuation", "app_replay_size", "system_prompt_file", "mcps", "verbose", "export_atif_provider_handles"}
	if !reflect.DeepEqual(fields, wantGlobal) {
		t.Fatalf("global-layer fields=%v, want %v", fields, wantGlobal)
	}
}

func TestSchemaRows_OverrideFiltersPerLaunch(t *testing.T) {
	schema := testLaunchSchema()
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{}, launchLayerLaunch, launchSchemaRowsOverride, appwire.LaunchConfigLayer{})
	fields := rowFields(rows)
	want := []string{"agent", "model", "reasoning_effort", "fast_cheap_model", "openai_responses_continuation", "system_prompt_file", "mcps", "verbose", "export_atif_provider_handles"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields=%v, want %v", fields, want)
	}
}

func TestSchemaRows_ExcludesDedicatedPluginSelection(t *testing.T) {
	rows := launchSchemaRows([]appwire.LaunchOption{
		{Field: "agent", Label: "Agent", DefaultableLayers: []string{"global"}, PerLaunch: true},
		{Field: "enabled_plugins", Label: "Plugins", Kind: "pluginSelection", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}, appwire.LaunchConfigLayer{}, launchLayerLaunch, launchSchemaRowsOverride, appwire.LaunchConfigLayer{})
	if fields := rowFields(rows); !reflect.DeepEqual(fields, []string{"agent"}) {
		t.Fatalf("fields=%v, want only generic agent row", fields)
	}
}

func TestSchemaRows_PathFieldsRequestCompletion(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "system_prompt_file", Label: "System prompt file", Kind: "path", PathKind: "file", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{}, launchLayerGlobal, launchSchemaRowsSettings, appwire.LaunchConfigLayer{})
	if len(rows) != 1 || !rows[0].pathCompletion {
		t.Fatalf("rows=%+v, want path completion", rows)
	}
}

func TestSchemaRows_ModelFallbacksDistinguishesUnsetAndExplicitEmpty(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "model_fallbacks", Label: "Model fallbacks", Kind: "modelList", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{}, launchLayerGlobal, launchSchemaRowsSettings, appwire.LaunchConfigLayer{})
	if len(rows) != 1 || rows[0].value != "(default)" || rows[0].editValue != "(default)" {
		t.Fatalf("nil model_fallbacks row=%+v, want default", rows)
	}
	rows = launchSchemaRows(schema, appwire.LaunchConfigLayer{ModelFallbacks: []string{}}, launchLayerGlobal, launchSchemaRowsSettings, appwire.LaunchConfigLayer{})
	if len(rows) != 1 || rows[0].value != "0 entries (explicit)" || rows[0].editValue != "[]" {
		t.Fatalf("empty model_fallbacks row=%+v, want explicit empty", rows)
	}
}

func TestSchemaRows_MCPsExposeEditableRows(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "mcps", Label: "MCP servers", Kind: "mcpServerList", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{MCPs: []appwire.MCPServerSpec{
		{Name: "docs", Command: "sh", Args: []string{"-c", "docs"}},
		{Name: "files", Command: "/bin/sh"},
	}}, launchLayerGlobal, launchSchemaRowsSettings, appwire.LaunchConfigLayer{})
	if len(rows) != 1 {
		t.Fatalf("rows=%+v, want one mcps row", rows)
	}
	if rows[0].value != "2 entries" {
		t.Fatalf("row.value=%q, want entry count", rows[0].value)
	}
	if rows[0].editValue != `[{"name":"docs","command":"sh","args":["-c","docs"]},{"name":"files","command":"/bin/sh","args":null}]` {
		t.Fatalf("row.editValue=%q, want serialized MCP rows", rows[0].editValue)
	}
}

func TestSchemaRows_OpenAIResponsesContinuationUsesStringDisplay(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "openai_responses_continuation", Label: "OpenAI Responses continuation", Kind: "select", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{OpenAIResponsesContinuation: "auto"}, launchLayerGlobal, launchSchemaRowsSettings, appwire.LaunchConfigLayer{})
	if len(rows) != 1 {
		t.Fatalf("rows=%+v, want one openai_responses_continuation row", rows)
	}
	if rows[0].value != "auto" || rows[0].editValue != "auto" {
		t.Fatalf("row=%+v, want auto display", rows[0])
	}
}

func TestSchemaRows_ExportATIFProviderHandlesUsesStringDisplay(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "export_atif_provider_handles", Label: "ATIF provider handles", Kind: "select", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{ExportATIFProviderHandles: "raw-local"}, launchLayerGlobal, launchSchemaRowsSettings, appwire.LaunchConfigLayer{})
	if len(rows) != 1 {
		t.Fatalf("rows=%+v, want one export_atif_provider_handles row", rows)
	}
	if rows[0].value != "raw-local" || rows[0].editValue != "raw-local" {
		t.Fatalf("row=%+v, want raw-local display", rows[0])
	}
}

func rowFields(rows []layerRow) []string {
	fields := make([]string, 0, len(rows))
	for _, row := range rows {
		fields = append(fields, row.field)
	}
	return fields
}

// TestLaunchOptionValue_ResolvedDefaultLabels: a field unset in the displayed
// layer but set in the resolved effective layer renders the resolved value
// with a " (default)" suffix, formatted exactly like a set value. The edit
// value keeps the unset placeholder so editing still starts blank.
func TestLaunchOptionValue_ResolvedDefaultLabels(t *testing.T) {
	tru := true
	twoHundred := 200
	effective := appwire.LaunchConfigLayer{
		Model:            "anthropic/claude-sonnet-4",
		ReasoningEffort:  "high",
		MaxRounds:        &twoHundred,
		Verbose:          &tru,
		SystemPromptText: "hello\nworld",
		ModelFallbacks:   []string{"openai/gpt-5-mini", "openai/gpt-5-nano"},
	}
	tests := []struct {
		field         string
		wantValue     string
		wantEditValue string
	}{
		{"model", "anthropic/claude-sonnet-4 (default)", ""},
		{"reasoning_effort", "high (default)", ""},
		{"max_rounds", "200 (default)", "(default)"},
		{"verbose", "true (default)", "(default)"},
		{"system_prompt_text", "11 chars, 2 lines (default)", ""},
		{"model_fallbacks", "2 entries (default)", "(default)"},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			value, editValue := launchOptionValue(appwire.LaunchOption{Field: tc.field}, appwire.LaunchConfigLayer{}, effective)
			if value != tc.wantValue {
				t.Errorf("value = %q, want %q", value, tc.wantValue)
			}
			if editValue != tc.wantEditValue {
				t.Errorf("editValue = %q, want %q", editValue, tc.wantEditValue)
			}
		})
	}
}

// TestLaunchOptionValue_ResolvedDefaultOnlyWhenUnset: a value set in the
// displayed layer always wins over the resolved default; a field unset in
// both layers keeps the bare "(default)" label; a field whose unset display
// is not "(default)" (entry-count lists) never gains the suffix.
func TestLaunchOptionValue_ResolvedDefaultOnlyWhenUnset(t *testing.T) {
	effective := appwire.LaunchConfigLayer{Model: "resolved/model", SkillsDirs: []string{"/a", "/b"}}
	if v, _ := launchOptionValue(appwire.LaunchOption{Field: "model"}, appwire.LaunchConfigLayer{Model: "set/model"}, effective); v != "set/model" {
		t.Fatalf("set value = %q, want set/model", v)
	}
	if v, _ := launchOptionValue(appwire.LaunchOption{Field: "model"}, appwire.LaunchConfigLayer{}, appwire.LaunchConfigLayer{}); v != "(default)" {
		t.Fatalf("unset-everywhere value = %q, want (default)", v)
	}
	if v, _ := launchOptionValue(appwire.LaunchOption{Field: "skills_dirs"}, appwire.LaunchConfigLayer{}, effective); v != "0 entries" {
		t.Fatalf("skills_dirs = %q, want 0 entries (no resolved-default suffix)", v)
	}
}

// TestSchemaRows_ResolvedDefaultFlowsThrough: launchSchemaRows threads the
// resolved effective layer into each row's display value.
func TestSchemaRows_ResolvedDefaultFlowsThrough(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "reasoning_effort", Label: "Reasoning effort", Kind: "select", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{}, launchLayerGlobal, launchSchemaRowsSettings, appwire.LaunchConfigLayer{ReasoningEffort: "high"})
	if len(rows) != 1 || rows[0].value != "high (default)" {
		t.Fatalf("rows=%+v, want resolved default label", rows)
	}
}

func testLaunchSchema() []appwire.LaunchOption {
	defaultable := []string{"global", "project"}
	all := []string{"global", "project", "launch"}
	return []appwire.LaunchOption{
		{Field: "agent", Label: "Agent", Kind: "text", DefaultableLayers: defaultable, PerLaunch: true},
		{Field: "model", Label: "Model", Kind: "modelPicker", DefaultableLayers: defaultable, PerLaunch: true},
		{Field: "reasoning_effort", Label: "Reasoning effort", Kind: "select", DefaultableLayers: defaultable, PerLaunch: true},
		{Field: "fast_cheap_model", Label: "Fast cheap model", Kind: "modelPicker", DefaultableLayers: defaultable, PerLaunch: true},
		{Field: "openai_responses_continuation", Label: "OpenAI Responses continuation", Kind: "select", DefaultableLayers: defaultable, PerLaunch: true},
		{Field: "app_replay_size", Label: "App replay size", Kind: "integer", DefaultableLayers: []string{"global"}, PerLaunch: false},
		{Field: "system_prompt_file", Label: "System prompt file", Kind: "path", PathKind: "file", DefaultableLayers: defaultable, PerLaunch: true},
		{Field: "mcps", Label: "MCP servers", Kind: "mcpServerList", DefaultableLayers: defaultable, PerLaunch: true},
		{Field: "verbose", Label: "Verbose event log", Kind: "boolean", DefaultableLayers: all, PerLaunch: true},
		{Field: "export_atif_provider_handles", Label: "ATIF provider handles", Kind: "select", DefaultableLayers: all, PerLaunch: true},
	}
}
