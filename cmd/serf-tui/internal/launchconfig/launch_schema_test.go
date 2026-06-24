package launchconfig

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestSchemaRows_SettingsFiltersDefaultableLayerAndKeepsOrder(t *testing.T) {
	schema := testLaunchSchema()
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{Agent: "serf", ReasoningEffort: "high", FastCheapModel: "mini"}, launchLayerProject, launchSchemaRowsSettings)
	fields := rowFields(rows)
	want := []string{"agent", "model", "reasoning_effort", "fast_cheap_model", "openai_responses_continuation", "system_prompt_file", "mcps", "verbose", "raw_http_logging", "export_atif_provider_handles"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields=%v, want %v", fields, want)
	}
}

func TestSchemaRows_OverrideFiltersPerLaunch(t *testing.T) {
	schema := testLaunchSchema()
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{}, launchLayerLaunch, launchSchemaRowsOverride)
	fields := rowFields(rows)
	want := []string{"agent", "model", "reasoning_effort", "fast_cheap_model", "openai_responses_continuation", "system_prompt_file", "mcps", "verbose", "raw_http_logging", "export_atif_provider_handles"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields=%v, want %v", fields, want)
	}
}

func TestSchemaRows_PathFieldsRequestCompletion(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "system_prompt_file", Label: "System prompt file", Kind: "path", PathKind: "file", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{}, launchLayerGlobal, launchSchemaRowsSettings)
	if len(rows) != 1 || !rows[0].pathCompletion {
		t.Fatalf("rows=%+v, want path completion", rows)
	}
}

func TestSchemaRows_ModelFallbacksDistinguishesUnsetAndExplicitEmpty(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "model_fallbacks", Label: "Model fallbacks", Kind: "modelList", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{}, launchLayerGlobal, launchSchemaRowsSettings)
	if len(rows) != 1 || rows[0].value != "(default)" || rows[0].editValue != "(default)" {
		t.Fatalf("nil model_fallbacks row=%+v, want default", rows)
	}
	rows = launchSchemaRows(schema, appwire.LaunchConfigLayer{ModelFallbacks: []string{}}, launchLayerGlobal, launchSchemaRowsSettings)
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
	}}, launchLayerGlobal, launchSchemaRowsSettings)
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

func TestSchemaRows_RawHTTPLoggingUsesBooleanDisplay(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "raw_http_logging", Label: "Raw HTTP logging", Kind: "boolean", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}
	rawHTTPLogging := true
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{RawHTTPLogging: &rawHTTPLogging}, launchLayerGlobal, launchSchemaRowsSettings)
	if len(rows) != 1 {
		t.Fatalf("rows=%+v, want one raw_http_logging row", rows)
	}
	if rows[0].value != "true" || rows[0].editValue != "true" {
		t.Fatalf("row=%+v, want true boolean display", rows[0])
	}
}

func TestSchemaRows_OpenAIResponsesContinuationUsesStringDisplay(t *testing.T) {
	schema := []appwire.LaunchOption{
		{Field: "openai_responses_continuation", Label: "OpenAI Responses continuation", Kind: "select", DefaultableLayers: []string{"global"}, PerLaunch: true},
	}
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{OpenAIResponsesContinuation: "auto"}, launchLayerGlobal, launchSchemaRowsSettings)
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
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{ExportATIFProviderHandles: "raw-local"}, launchLayerGlobal, launchSchemaRowsSettings)
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
		{Field: "raw_http_logging", Label: "Raw HTTP logging", Kind: "boolean", DefaultableLayers: all, PerLaunch: true},
		{Field: "export_atif_provider_handles", Label: "ATIF provider handles", Kind: "select", DefaultableLayers: all, PerLaunch: true},
	}
}
