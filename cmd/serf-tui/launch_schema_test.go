package main

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

func TestSchemaRows_SettingsFiltersDefaultableLayerAndKeepsOrder(t *testing.T) {
	schema := testLaunchSchema()
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{Agent: "serf", ReasoningEffort: "high", FastCheapModel: "mini"}, launchLayerProject, launchSchemaRowsSettings)
	fields := rowFields(rows)
	want := []string{"agent", "model", "reasoning_effort", "fast_cheap_model", "system_prompt_file", "verbose"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields=%v, want %v", fields, want)
	}
}

func TestSchemaRows_OverrideFiltersPerLaunch(t *testing.T) {
	schema := testLaunchSchema()
	rows := launchSchemaRows(schema, appwire.LaunchConfigLayer{}, launchLayerLaunch, launchSchemaRowsOverride)
	fields := rowFields(rows)
	want := []string{"agent", "model", "reasoning_effort", "fast_cheap_model", "system_prompt_file", "verbose"}
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
		{Field: "app_replay_size", Label: "App replay size", Kind: "integer", DefaultableLayers: []string{"global"}, PerLaunch: false},
		{Field: "system_prompt_file", Label: "System prompt file", Kind: "path", PathKind: "file", DefaultableLayers: defaultable, PerLaunch: true},
		{Field: "verbose", Label: "Verbose event log", Kind: "boolean", DefaultableLayers: all, PerLaunch: true},
	}
}
