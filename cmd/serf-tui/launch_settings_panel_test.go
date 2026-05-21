package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

func TestLaunchSettingsPanel_TabSwitch(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(launchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{Model: "openai/gpt-5"}})
	p = updated.(launchSettingsPanel)
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyRight})
	v := updated.(launchSettingsPanel).View()
	if !strings.Contains(v, "Project") {
		t.Errorf("view should show Project tab after Right:\n%s", v)
	}
}

func TestLaunchSettingsPanel_LoadsGlobalFirst(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	cmd := p.initialCmd()
	if cmd == nil {
		t.Fatal("initialCmd nil")
	}
	if !p.loadingGlobal {
		t.Errorf("expected loadingGlobal")
	}
}

func TestLaunchSettingsPanel_EditEmitsModalRequest(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(launchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{Model: "openai/gpt-5"}})
	// cursor starts at 0, which is "model"
	_, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should produce a cmd requesting a modal")
	}
	msg := cmd()
	req, ok := msg.(launchSettingsEditRequestMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if req.Layer != "global" || req.Field != "model" {
		t.Errorf("req = %+v", req)
	}
	if req.CurrentValue != "openai/gpt-5" {
		t.Errorf("req.CurrentValue = %q", req.CurrentValue)
	}
}

func TestLaunchSettingsPanel_UsesSchemaRowsWhenAvailable(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(launchSchemaResultMsg{Schema: appwire.LaunchOptionSchemaResponse{Options: testLaunchSchema()}})
	p = updated.(launchSettingsPanel)
	updated, _ = p.Update(launchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{Agent: "serf"}})
	view := updated.(launchSettingsPanel).View()
	if !strings.Contains(view, "Agent") {
		t.Fatalf("view should use schema labels:\n%s", view)
	}
	if !strings.Contains(view, "App replay size") {
		t.Fatalf("global schema view should include global-only defaultable field:\n%s", view)
	}
}

func TestLaunchSettingsPanel_ProjectSchemaRowsExcludeGlobalOnly(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	p.tab = launchTabProject
	updated, _ := p.Update(launchSchemaResultMsg{Schema: appwire.LaunchOptionSchemaResponse{Options: testLaunchSchema()}})
	p = updated.(launchSettingsPanel)
	updated, _ = p.Update(launchLayerResultMsg{Layer: "project", Data: appwire.LaunchConfigLayer{Agent: "serf"}})
	view := updated.(launchSettingsPanel).View()
	if strings.Contains(view, "App replay size") {
		t.Fatalf("project schema view should exclude global-only field:\n%s", view)
	}
}

func TestLayerRows_IncludesFastCheapModel(t *testing.T) {
	rows := layerRows(appwire.LaunchConfigLayer{FastCheapModel: "openai/gpt-5-mini"})
	for _, row := range rows {
		if row.field == "fast_cheap_model" {
			if row.value != "openai/gpt-5-mini" {
				t.Fatalf("fast_cheap_model value = %q, want openai/gpt-5-mini", row.value)
			}
			return
		}
	}
	t.Fatalf("layerRows missing fast_cheap_model row: %#v", rows)
}

func TestApplyEdit_FastCheapModel(t *testing.T) {
	got, err := applyEdit(appwire.LaunchConfigLayer{}, "fast_cheap_model", " openai/gpt-5-mini ")
	if err != nil {
		t.Fatalf("applyEdit: %v", err)
	}
	if got.FastCheapModel != "openai/gpt-5-mini" {
		t.Fatalf("FastCheapModel = %q, want openai/gpt-5-mini", got.FastCheapModel)
	}
}

func TestLayerRows_ListFieldsExposeEditableValues(t *testing.T) {
	rows := layerRows(appwire.LaunchConfigLayer{SkillsDirs: []string{"/one", "/two"}})
	for _, row := range rows {
		if row.field == "skills_dirs" {
			if row.value != "2 entries" {
				t.Fatalf("row.value=%q, want entry count", row.value)
			}
			if row.editValue != "/one, /two" {
				t.Fatalf("row.editValue=%q, want joined paths", row.editValue)
			}
			return
		}
	}
	t.Fatal("missing skills_dirs row")
}

func TestApplyEdit_RejectsMissingSkillDir(t *testing.T) {
	_, err := applyEdit(appwire.LaunchConfigLayer{}, "skills_dirs", filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("expected missing directory error")
	}
}

func TestApplyEdit_AcceptsExistingMCPConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := applyEdit(appwire.LaunchConfigLayer{}, "mcp_configs", path)
	if err != nil {
		t.Fatalf("applyEdit: %v", err)
	}
	if len(got.MCPConfigs) != 1 || got.MCPConfigs[0] != path {
		t.Fatalf("MCPConfigs=%v", got.MCPConfigs)
	}
}

func TestApplyEdit_NewSchemaFields(t *testing.T) {
	dir := t.TempDir()
	prompt := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(prompt, []byte("prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(dir, "trace.jsonl")
	got, err := applyEdit(appwire.LaunchConfigLayer{}, "system_prompt_file", prompt)
	if err != nil {
		t.Fatalf("system_prompt_file: %v", err)
	}
	got, err = applyEdit(got, "system_prompt_text", "inline prompt")
	if err != nil {
		t.Fatalf("system_prompt_text: %v", err)
	}
	got, err = applyEdit(got, "model_fallbacks", "openai/gpt-5-mini, openai/gpt-5-nano")
	if err != nil {
		t.Fatalf("model_fallbacks: %v", err)
	}
	got, err = applyEdit(got, "verbose", "true")
	if err != nil {
		t.Fatalf("verbose: %v", err)
	}
	got, err = applyEdit(got, "trace_file", trace)
	if err != nil {
		t.Fatalf("trace_file: %v", err)
	}
	got, err = applyEdit(got, "cpu_profile", filepath.Join(dir, "cpu.pprof"))
	if err != nil {
		t.Fatalf("cpu_profile: %v", err)
	}
	got, err = applyEdit(got, "export_atif_path", filepath.Join(dir, "out.atif.json"))
	if err != nil {
		t.Fatalf("export_atif_path: %v", err)
	}
	if got.SystemPromptFile != prompt || got.SystemPromptText != "inline prompt" || len(got.ModelFallbacks) != 2 || got.Verbose == nil || !*got.Verbose || got.TraceFile != trace || got.CPUProfile == "" || got.ExportATIFPath == "" {
		t.Fatalf("updated layer=%+v", got)
	}
}

func TestApplyEdit_ModelFallbacksUnsetExplicitEmptyAndReplacement(t *testing.T) {
	got, err := applyEdit(appwire.LaunchConfigLayer{}, "model_fallbacks", "")
	if err != nil {
		t.Fatalf("blank model_fallbacks: %v", err)
	}
	if got.ModelFallbacks != nil {
		t.Fatalf("blank ModelFallbacks=%#v, want nil", got.ModelFallbacks)
	}
	got, err = applyEdit(appwire.LaunchConfigLayer{ModelFallbacks: []string{"openai/gpt-5-mini"}}, "model_fallbacks", "(default)")
	if err != nil {
		t.Fatalf("default model_fallbacks: %v", err)
	}
	if got.ModelFallbacks != nil {
		t.Fatalf("default ModelFallbacks=%#v, want nil", got.ModelFallbacks)
	}
	got, err = applyEdit(appwire.LaunchConfigLayer{}, "model_fallbacks", "[]")
	if err != nil {
		t.Fatalf("empty model_fallbacks: %v", err)
	}
	if got.ModelFallbacks == nil || len(got.ModelFallbacks) != 0 {
		t.Fatalf("empty ModelFallbacks=%#v, want explicit empty slice", got.ModelFallbacks)
	}
	got, err = applyEdit(appwire.LaunchConfigLayer{}, "model_fallbacks", "openai/gpt-5-mini, openai/gpt-5-nano")
	if err != nil {
		t.Fatalf("replacement model_fallbacks: %v", err)
	}
	if len(got.ModelFallbacks) != 2 || got.ModelFallbacks[0] != "openai/gpt-5-mini" || got.ModelFallbacks[1] != "openai/gpt-5-nano" {
		t.Fatalf("replacement ModelFallbacks=%#v", got.ModelFallbacks)
	}
}

func TestApplyEdit_OutputFileRejectsMissingParent(t *testing.T) {
	_, err := applyEdit(appwire.LaunchConfigLayer{}, "trace_file", filepath.Join(t.TempDir(), "missing", "trace.jsonl"))
	if err == nil {
		t.Fatal("expected missing parent error")
	}
}

func TestValidateLocalLaunchPath_OutputFileRejectsExistingDirectory(t *testing.T) {
	err := validateLocalLaunchPath(t.TempDir(), "outputFile")
	if err == nil || !strings.Contains(err.Error(), "path is a directory") {
		t.Fatalf("err=%v, want existing directory rejection", err)
	}
}

func TestValidateLocalLaunchPath_OutputFileRejectsNonWritableParent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	err := validateLocalLaunchPath(filepath.Join(dir, "trace.jsonl"), "outputFile")
	if err == nil || !strings.Contains(err.Error(), "parent directory is not writable") {
		t.Fatalf("err=%v, want non-writable parent rejection", err)
	}
}

func TestApplyEdit_MCPsParsesRowsAndPreservesArgs(t *testing.T) {
	got, err := applyEdit(appwire.LaunchConfigLayer{}, "mcps", "docs:sh -c docs; files:sh")
	if err != nil {
		t.Fatalf("applyEdit: %v", err)
	}
	if len(got.MCPs) != 2 {
		t.Fatalf("MCPs=%+v, want 2 entries", got.MCPs)
	}
	if got.MCPs[0].Name != "docs" || got.MCPs[0].Command != "sh" || strings.Join(got.MCPs[0].Args, " ") != "-c docs" {
		t.Fatalf("first MCP=%+v", got.MCPs[0])
	}
	if got.MCPs[1].Name != "files" || got.MCPs[1].Command != "sh" || len(got.MCPs[1].Args) != 0 {
		t.Fatalf("second MCP=%+v", got.MCPs[1])
	}
}

func TestLaunchSettingsPanel_ApplyEditMCPs(t *testing.T) {
	p := newLaunchSettingsPanel(nil, "/cwd")
	p.global = appwire.LaunchConfigLayer{MCPs: []appwire.MCPServerSpec{{Name: "old", Command: "sh"}}}
	gotPanel, updated, err := p.ApplyEdit("mcps", mcpEditValue(p.global.MCPs))
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	if len(updated.MCPs) != 1 || updated.MCPs[0].Name != "old" || updated.MCPs[0].Command != "sh" {
		t.Fatalf("updated MCPs=%+v", updated.MCPs)
	}
	if len(gotPanel.global.MCPs) != 1 || gotPanel.global.MCPs[0].Name != "old" {
		t.Fatalf("panel MCPs=%+v", gotPanel.global.MCPs)
	}
}

func TestApplyEdit_MCPsPreservesSerializedRows(t *testing.T) {
	layer := appwire.LaunchConfigLayer{MCPs: []appwire.MCPServerSpec{{Name: "docs", Command: "docs-mcp"}}}
	value := mcpEditValue([]appwire.MCPServerSpec{{Name: "docs", Command: "sh", Args: []string{"-c", "docs"}}})
	got, err := applyEdit(layer, "mcps", value)
	if err != nil {
		t.Fatalf("applyEdit: %v", err)
	}
	if len(got.MCPs) != 1 || got.MCPs[0].Name != "docs" || got.MCPs[0].Command != "sh" || strings.Join(got.MCPs[0].Args, " ") != "-c docs" {
		t.Fatalf("MCPs=%+v, want serialized row preserved", got.MCPs)
	}
}

func TestMCPsEditValueRoundTripsArgsWithSpaces(t *testing.T) {
	want := []appwire.MCPServerSpec{
		{Name: "docs", Command: "sh", Args: []string{"-c", "echo hi"}},
		{Name: "empty", Command: "sh", Args: []string{}},
		{Name: "nil", Command: "sh"},
	}
	got, err := parseMCPs(mcpEditValue(want))
	if err != nil {
		t.Fatalf("parseMCPs: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip MCPs=%#v, want %#v", got, want)
	}
}

func TestApplyEdit_MCPsRejectsInvalidRows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "missing name", value: ":sh", want: "name:command"},
		{name: "missing command", value: "docs:", want: "missing command"},
		{name: "invalid command", value: "docs:definitely-not-installed-xyz123", want: "executable file not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := applyEdit(appwire.LaunchConfigLayer{}, "mcps", tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}
