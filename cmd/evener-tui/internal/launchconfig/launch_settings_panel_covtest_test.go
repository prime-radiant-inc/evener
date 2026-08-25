package launchconfig

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

// --- Init ---

func TestCovLaunchSettingsInit(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	if p.Init() != nil {
		t.Fatal("Init should return nil")
	}
}

// --- InitialCmd with nil client ---

func TestCovLaunchSettingsInitialCmdNilClient(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	cmd := p.InitialCmd()
	if cmd == nil {
		t.Fatal("InitialCmd should not be nil even with nil client")
	}
	raw := cmd()
	msg, ok := raw.(LaunchLayerResultMsg)
	if !ok {
		t.Fatalf("InitialCmd message type = %T, want LaunchLayerResultMsg", raw)
	}
	want := LaunchLayerResultMsg{Layer: "global"}
	if !reflect.DeepEqual(msg, want) {
		t.Fatalf("InitialCmd message = %+v, want %+v", msg, want)
	}
}

// --- launchDiagnosticsStatus ---

func TestCovLaunchDiagnosticsStatusEmpty(t *testing.T) {
	if got := launchDiagnosticsStatus(appwire.LaunchConfigResolved{}); got != "" {
		t.Fatalf("empty diagnostics should return empty, got %q", got)
	}
}

func TestCovLaunchDiagnosticsStatusWithField(t *testing.T) {
	r := appwire.LaunchConfigResolved{
		Diagnostics: []appwire.LaunchConfigDiagnostic{{Field: "sandbox", Message: "bad mode"}},
	}
	got := launchDiagnosticsStatus(r)
	if got != "⚠ sandbox: bad mode" {
		t.Fatalf("diagnostics with field = %q, want %q", got, "⚠ sandbox: bad mode")
	}
}

func TestCovLaunchDiagnosticsStatusWithoutField(t *testing.T) {
	r := appwire.LaunchConfigResolved{
		Diagnostics: []appwire.LaunchConfigDiagnostic{{Message: "just a message"}},
	}
	got := launchDiagnosticsStatus(r)
	if got != "⚠ just a message" {
		t.Fatalf("diagnostics without field = %q, want %q", got, "⚠ just a message")
	}
}

// --- Update: LaunchLayerResultMsg error ---

func TestCovLaunchSettingsLoadError(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	p.global.Model = "keep-global"
	p.project.Model = "keep-project"
	updated, _ := p.Update(LaunchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{Model: "discard"}, Err: errors.New("load fail")})
	p2 := updated.(LaunchSettingsPanel)
	if p2.statusMessage != "load error: load fail" {
		t.Fatalf("statusMessage = %q, want %q", p2.statusMessage, "load error: load fail")
	}
	if p2.global.Model != "keep-global" || p2.project.Model != "keep-project" || !p2.loadingGlobal || !p2.loadingProj {
		t.Fatalf("load error changed panel state: global=%q project=%q loadingGlobal=%v loadingProj=%v", p2.global.Model, p2.project.Model, p2.loadingGlobal, p2.loadingProj)
	}
}

// --- Update: LaunchLayerResultMsg project ---

func TestCovLaunchSettingsLoadProject(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(LaunchLayerResultMsg{Layer: "project", Data: appwire.LaunchConfigLayer{Model: "x"}})
	p2 := updated.(LaunchSettingsPanel)
	if p2.loadingProj {
		t.Fatal("loadingProj should be false after project load")
	}
	if p2.project.Model != "x" {
		t.Fatalf("project model = %q", p2.project.Model)
	}
}

// --- Update: LaunchSchemaResultMsg error ---

func TestCovLaunchSettingsSchemaError(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	want := []appwire.LaunchOption{{Field: "existing", Label: "Existing", Kind: "text"}}
	p.schema = want
	updated, _ := p.Update(LaunchSchemaResultMsg{Schema: appwire.LaunchOptionSchemaResponse{Options: []appwire.LaunchOption{{Field: "discard"}}}, Err: errors.New("schema fail")})
	p2 := updated.(LaunchSettingsPanel)
	if !reflect.DeepEqual(p2.schema, want) {
		t.Fatalf("schema error replaced existing schema with %+v", p2.schema)
	}
}

// --- Update: LaunchResolveResultMsg error ---

func TestCovLaunchSettingsResolveError(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	wantResolved := appwire.LaunchConfigResolved{Effective: appwire.LaunchConfigLayer{Model: "failed-resolution"}}
	updated, _ := p.Update(LaunchResolveResultMsg{Resolved: wantResolved, Err: errors.New("resolve fail")})
	p2 := updated.(LaunchSettingsPanel)
	if p2.statusMessage != "resolve error: resolve fail" || p2.loadingResolve || !reflect.DeepEqual(p2.resolved, wantResolved) {
		t.Fatalf("resolve error state = status %q loading=%v resolved=%+v", p2.statusMessage, p2.loadingResolve, p2.resolved)
	}
}

// --- Update: LaunchResolveResultMsg with diagnostics ---

func TestCovLaunchSettingsResolveDiagnostics(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(LaunchResolveResultMsg{
		Resolved: appwire.LaunchConfigResolved{
			Diagnostics: []appwire.LaunchConfigDiagnostic{{Field: "x", Message: "warn"}},
		},
	})
	p2 := updated.(LaunchSettingsPanel)
	if p2.statusMessage != "⚠ x: warn" {
		t.Fatalf("statusMessage = %q, want %q", p2.statusMessage, "⚠ x: warn")
	}
}

// --- Update: LaunchSetLayerResultMsg success ---

func TestCovLaunchSettingsSetLayerSuccess(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(LaunchSetLayerResultMsg{Layer: "global", Resolved: appwire.LaunchConfigResolved{}})
	p2 := updated.(LaunchSettingsPanel)
	if p2.statusMessage != "saved global" {
		t.Fatalf("statusMessage = %q, want %q", p2.statusMessage, "saved global")
	}
}

// --- Update: LaunchSetLayerResultMsg success with diagnostics ---

func TestCovLaunchSettingsSetLayerSuccessWithDiagnostics(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(LaunchSetLayerResultMsg{
		Layer: "global",
		Resolved: appwire.LaunchConfigResolved{
			Diagnostics: []appwire.LaunchConfigDiagnostic{{Message: "diag"}},
		},
	})
	p2 := updated.(LaunchSettingsPanel)
	if p2.statusMessage != "saved global — ⚠ diag" {
		t.Fatalf("statusMessage = %q, want %q", p2.statusMessage, "saved global — ⚠ diag")
	}
}

// --- Update: LaunchSetLayerResultMsg error ---

func TestCovLaunchSettingsSetLayerError(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	p.resolved = appwire.LaunchConfigResolved{Effective: appwire.LaunchConfigLayer{Model: "keep"}}
	updated, _ := p.Update(LaunchSetLayerResultMsg{Layer: "global", Resolved: appwire.LaunchConfigResolved{Effective: appwire.LaunchConfigLayer{Model: "discard"}}, Err: errors.New("save fail")})
	p2 := updated.(LaunchSettingsPanel)
	if p2.statusMessage != "save error: save fail" || p2.resolved.Effective.Model != "keep" {
		t.Fatalf("save error state = status %q resolved=%+v", p2.statusMessage, p2.resolved)
	}
}

// --- Update: LaunchTrustResultMsg success with diagnostics ---

func TestCovLaunchSettingsTrustSuccessWithDiagnostics(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(LaunchTrustResultMsg{
		Resolved: appwire.LaunchConfigResolved{
			Diagnostics: []appwire.LaunchConfigDiagnostic{{Message: "trust diag"}},
		},
	})
	p2 := updated.(LaunchSettingsPanel)
	if p2.statusMessage != "trust recorded — ⚠ trust diag" {
		t.Fatalf("statusMessage = %q, want %q", p2.statusMessage, "trust recorded — ⚠ trust diag")
	}
}

// --- Update: LaunchTrustResultMsg error ---

func TestCovLaunchSettingsTrustError(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	p.resolved = appwire.LaunchConfigResolved{Effective: appwire.LaunchConfigLayer{Model: "keep"}}
	updated, _ := p.Update(LaunchTrustResultMsg{Resolved: appwire.LaunchConfigResolved{Effective: appwire.LaunchConfigLayer{Model: "discard"}}, Err: errors.New("trust fail")})
	p2 := updated.(LaunchSettingsPanel)
	if p2.statusMessage != "trust error: trust fail" || p2.resolved.Effective.Model != "keep" {
		t.Fatalf("trust error state = status %q resolved=%+v", p2.statusMessage, p2.resolved)
	}
}

// --- Update: KeyMsg Up/Down/Left/Right ---

func TestCovLaunchSettingsKeyUpDown(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(LaunchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{}})
	p = updated.(LaunchSettingsPanel)
	// Down moves cursor
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p2 := updated.(LaunchSettingsPanel)
	if p2.cursor != 1 {
		t.Fatalf("Down should move cursor to 1, got %d", p2.cursor)
	}
	// Up moves back
	updated, _ = p2.Update(tea.KeyMsg{Type: tea.KeyUp})
	p3 := updated.(LaunchSettingsPanel)
	if p3.cursor != 0 {
		t.Fatalf("Up should move cursor to 0, got %d", p3.cursor)
	}
}

func TestCovLaunchSettingsKeyLeft(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(LaunchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{}})
	p = updated.(LaunchSettingsPanel)
	// Move to project tab
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyRight})
	p2 := updated.(LaunchSettingsPanel)
	if p2.tab != launchTabProject {
		t.Fatalf("Right should move to project tab, got %d", p2.tab)
	}
	// Left moves back
	updated, _ = p2.Update(tea.KeyMsg{Type: tea.KeyLeft})
	p3 := updated.(LaunchSettingsPanel)
	if p3.tab != launchTabGlobal {
		t.Fatalf("Left should move to global tab, got %d", p3.tab)
	}
	// Left at first tab is no-op
	updated, _ = p3.Update(tea.KeyMsg{Type: tea.KeyLeft})
	p4 := updated.(LaunchSettingsPanel)
	if p4.tab != launchTabGlobal {
		t.Fatalf("Left at first tab should stay, got %d", p4.tab)
	}
}

func TestCovLaunchSettingsKeyRightAtLast(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(LaunchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{}})
	p = updated.(LaunchSettingsPanel)
	// Move to repo (last) tab
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyRight})
	updated, _ = updated.(LaunchSettingsPanel).Update(tea.KeyMsg{Type: tea.KeyRight})
	p2 := updated.(LaunchSettingsPanel)
	if p2.tab != launchTabRepo {
		t.Fatalf("2x Right should move to repo tab, got %d", p2.tab)
	}
	// Right at last tab is no-op
	updated, _ = p2.Update(tea.KeyMsg{Type: tea.KeyRight})
	p3 := updated.(LaunchSettingsPanel)
	if p3.tab != launchTabRepo {
		t.Fatalf("Right at last tab should stay, got %d", p3.tab)
	}
}

// --- Update: Esc/CtrlC ---

func TestCovLaunchSettingsEsc(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p2 := updated.(LaunchSettingsPanel)
	if !p2.done || !p2.cancelled {
		t.Fatal("Esc should set done and cancelled")
	}
}

// --- Done / CWD ---

func TestCovLaunchSettingsDone(t *testing.T) {
	p := LaunchSettingsPanel{done: true}
	if !p.Done() {
		t.Fatal("Done should be true")
	}
	p2 := NewLaunchSettingsPanel(nil, "/cwd")
	if p2.Done() {
		t.Fatal("Done should be false on new panel")
	}
}

func TestCovLaunchSettingsCWD(t *testing.T) {
	p := NewLaunchSettingsPanel(nil, "/my/cwd")
	if got := p.CWD(); got != "/my/cwd" {
		t.Fatalf("CWD = %q, want /my/cwd", got)
	}
}

// --- renderRepoView ---

func TestCovRenderRepoViewNil(t *testing.T) {
	if got := renderRepoView(nil); got != "no in-repo file" {
		t.Fatalf("nil repo view = %q", got)
	}
}

func TestCovRenderRepoViewWithPreview(t *testing.T) {
	r := &appwire.RepoLaunchConfigStatus{
		Path:    "/path/to/config",
		Trust:   "trusted",
		Hash:    "abc123",
		Preview: "preview content",
	}
	v := renderRepoView(r)
	if !strings.Contains(v, "path:  /path/to/config") {
		t.Fatalf("repo view should show path: %q", v)
	}
	if !strings.Contains(v, "preview content") {
		t.Fatalf("repo view should show preview: %q", v)
	}
}

func TestCovRenderRepoViewUntrusted(t *testing.T) {
	r := &appwire.RepoLaunchConfigStatus{
		Path:  "/path",
		Trust: "untrusted",
		Hash:  "abc",
	}
	v := renderRepoView(r)
	if !strings.Contains(v, "[T] trust this file") {
		t.Fatalf("untrusted repo should show trust prompt: %q", v)
	}
}

func TestCovRenderRepoViewChanged(t *testing.T) {
	r := &appwire.RepoLaunchConfigStatus{
		Path:  "/path",
		Trust: "changed",
		Hash:  "abc",
	}
	v := renderRepoView(r)
	if !strings.Contains(v, "[T] trust this file") {
		t.Fatalf("changed repo should show trust prompt: %q", v)
	}
}

// --- editCurrent: repo tab ---

func TestCovEditCurrentRepoNoHash(t *testing.T) {
	p := LaunchSettingsPanel{tab: launchTabRepo, resolved: appwire.LaunchConfigResolved{Repo: &appwire.RepoLaunchConfigStatus{Hash: ""}}}
	_, cmd := p.editCurrent()
	if cmd != nil {
		t.Fatal("Enter on repo with no hash should return nil cmd")
	}
}

func TestCovEditCurrentRepoTrusted(t *testing.T) {
	p := LaunchSettingsPanel{
		tab:      launchTabRepo,
		client:   nil,
		resolved: appwire.LaunchConfigResolved{Repo: &appwire.RepoLaunchConfigStatus{Trust: "trusted", Hash: "abc"}},
	}
	_, cmd := p.editCurrent()
	if cmd != nil {
		t.Fatal("Enter on trusted repo should return nil cmd")
	}
}

// --- editCurrent: cursor out of range ---

func TestCovEditCurrentCursorOutOfRange(t *testing.T) {
	p := LaunchSettingsPanel{tab: launchTabGlobal, cursor: 100}
	_, cmd := p.editCurrent()
	if cmd != nil {
		t.Fatal("Enter with cursor out of range should return nil cmd")
	}
}

// --- editCurrent: read-only field ---

func TestCovEditCurrentReadOnlyField(t *testing.T) {
	// Temporarily override the read-only predicate
	orig := launchSettingsFieldReadOnly
	launchSettingsFieldReadOnly = func(field string) bool { return field == "env" }
	defer func() { launchSettingsFieldReadOnly = orig }()

	p := LaunchSettingsPanel{tab: launchTabGlobal, cursor: 0, global: appwire.LaunchConfigLayer{}}
	// Cursor 0 with layerRows is "model" but with the override, only "env" is read-only
	// so we need cursor at the env index. Instead, set cursor to where env is.
	rows := layerRows(appwire.LaunchConfigLayer{})
	envIdx := -1
	for i, r := range rows {
		if r.field == "env" {
			envIdx = i
			break
		}
	}
	if envIdx < 0 {
		t.Fatal("env row not found")
	}
	p.cursor = envIdx
	_, cmd := p.editCurrent()
	if cmd != nil {
		t.Fatal("Enter on read-only field should return nil cmd")
	}
}

// --- tabName / currentLayer ---

func TestCovTabName(t *testing.T) {
	p := LaunchSettingsPanel{tab: launchTabProject}
	if got := p.tabName(); got != "project" {
		t.Fatalf("project tabName = %q", got)
	}
	p2 := LaunchSettingsPanel{tab: launchTabGlobal}
	if got := p2.tabName(); got != "global" {
		t.Fatalf("global tabName = %q", got)
	}
	p3 := LaunchSettingsPanel{tab: launchTabRepo}
	if got := p3.tabName(); got != "global" {
		t.Fatalf("repo tabName = %q, want global (default)", got)
	}
}

func TestCovCurrentLayer(t *testing.T) {
	p := LaunchSettingsPanel{tab: launchTabProject, project: appwire.LaunchConfigLayer{Model: "proj"}}
	if got := p.currentLayer(); got.Model != "proj" {
		t.Fatalf("project currentLayer = %q", got.Model)
	}
	p2 := LaunchSettingsPanel{tab: launchTabGlobal, global: appwire.LaunchConfigLayer{Model: "global"}}
	if got := p2.currentLayer(); got.Model != "global" {
		t.Fatalf("global currentLayer = %q", got.Model)
	}
}

// --- ApplyEdit ---

func TestCovApplyEditProject(t *testing.T) {
	p := LaunchSettingsPanel{tab: launchTabProject, project: appwire.LaunchConfigLayer{}}
	updated, _, err := p.ApplyEdit("model", "gpt-5")
	if err != nil {
		t.Fatalf("ApplyEdit error: %v", err)
	}
	if updated.project.Model != "gpt-5" {
		t.Fatalf("project model = %q", updated.project.Model)
	}
}

func TestCovApplyEditGlobal(t *testing.T) {
	p := LaunchSettingsPanel{tab: launchTabGlobal, global: appwire.LaunchConfigLayer{}}
	updated, _, err := p.ApplyEdit("model", "gpt-5")
	if err != nil {
		t.Fatalf("ApplyEdit error: %v", err)
	}
	if updated.global.Model != "gpt-5" {
		t.Fatalf("global model = %q", updated.global.Model)
	}
}

func TestCovApplyEditError(t *testing.T) {
	p := LaunchSettingsPanel{tab: launchTabGlobal, global: appwire.LaunchConfigLayer{}}
	_, _, err := p.ApplyEdit("sandbox", "invalid-mode")
	if err == nil {
		t.Fatal("invalid sandbox mode should return error")
	}
}

// --- applyEdit: various fields ---

func TestCovApplyEditSandboxValid(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "sandbox", "read-only")
	if err != nil || layer.Sandbox != "read-only" {
		t.Fatalf("sandbox = %q err=%v", layer.Sandbox, err)
	}
}

func TestCovApplyEditSandboxInherit(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "sandbox", "")
	if err != nil || layer.Sandbox != "" {
		t.Fatalf("empty sandbox = %q err=%v", layer.Sandbox, err)
	}
}

func TestCovApplyEditUnknownField(t *testing.T) {
	_, err := applyEdit(appwire.LaunchConfigLayer{}, "nonexistent", "x")
	if err == nil {
		t.Fatal("unknown field should return error")
	}
}

func TestCovApplyEditSystemPromptMode(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "system_prompt_mode", "custom")
	if err != nil || layer.SystemPromptMode != "custom" {
		t.Fatalf("system_prompt_mode = %q err=%v", layer.SystemPromptMode, err)
	}
}

func TestCovApplyEditSystemPromptAppendMode(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "system_prompt_append_mode", "append")
	if err != nil || layer.SystemPromptAppendMode != "append" {
		t.Fatalf("system_prompt_append_mode = %q err=%v", layer.SystemPromptAppendMode, err)
	}
}

func TestCovApplyEditExportATIFProviderHandles(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "export_atif_provider_handles", "raw-local")
	if err != nil || layer.ExportATIFProviderHandles != "raw-local" {
		t.Fatalf("export_atif_provider_handles = %q err=%v", layer.ExportATIFProviderHandles, err)
	}
}

func TestCovApplyEditFastCheapModel(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "fast_cheap_model", "mini")
	if err != nil || layer.FastCheapModel != "mini" {
		t.Fatalf("fast_cheap_model = %q err=%v", layer.FastCheapModel, err)
	}
}

func TestCovApplyEditContextStrategy(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "context_strategy", "compact")
	if err != nil || layer.ContextStrategy != "compact" {
		t.Fatalf("context_strategy = %q err=%v", layer.ContextStrategy, err)
	}
}

func TestCovApplyEditOpenAIResponsesContinuation(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "openai_responses_continuation", "auto")
	if err != nil || layer.OpenAIResponsesContinuation != "auto" {
		t.Fatalf("openai_responses_continuation = %q err=%v", layer.OpenAIResponsesContinuation, err)
	}
}

func TestCovApplyEditSystemPromptFileDefault(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "system_prompt_file", "(default)")
	if err != nil || layer.SystemPromptFile != "" {
		t.Fatalf("system_prompt_file (default) = %q err=%v", layer.SystemPromptFile, err)
	}
}

func TestCovApplyEditSystemPromptAppendFileDefault(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "system_prompt_append_file", "(default)")
	if err != nil || layer.SystemPromptAppendFile != "" {
		t.Fatalf("system_prompt_append_file (default) = %q err=%v", layer.SystemPromptAppendFile, err)
	}
}

func TestCovApplyEditSystemPromptText(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "system_prompt_text", "custom text")
	if err != nil || layer.SystemPromptText != "custom text" {
		t.Fatalf("system_prompt_text = %q err=%v", layer.SystemPromptText, err)
	}
}

func TestCovApplyEditSystemPromptTextDefault(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{SystemPromptText: "x"}, "system_prompt_text", "  (default)  ")
	if err != nil || layer.SystemPromptText != "" {
		t.Fatalf("system_prompt_text (default) = %q err=%v", layer.SystemPromptText, err)
	}
}

func TestCovApplyEditSystemPromptAppendText(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "system_prompt_append_text", "append text")
	if err != nil || layer.SystemPromptAppendText != "append text" {
		t.Fatalf("system_prompt_append_text = %q err=%v", layer.SystemPromptAppendText, err)
	}
}

func TestCovApplyEditSystemPromptAppendTextDefault(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{SystemPromptAppendText: "x"}, "system_prompt_append_text", "(default)")
	if err != nil || layer.SystemPromptAppendText != "" {
		t.Fatalf("system_prompt_append_text (default) = %q err=%v", layer.SystemPromptAppendText, err)
	}
}

func TestCovApplyEditModelFallbacks(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "model_fallbacks", "a, b, c")
	want := []string{"a", "b", "c"}
	if err != nil || !reflect.DeepEqual(layer.ModelFallbacks, want) {
		t.Fatalf("model_fallbacks = %v, want %v, err=%v", layer.ModelFallbacks, want, err)
	}
}

func TestCovApplyEditModelFallbacksDefault(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "model_fallbacks", "(default)")
	if err != nil || layer.ModelFallbacks != nil {
		t.Fatalf("model_fallbacks (default) = %v err=%v", layer.ModelFallbacks, err)
	}
}

func TestCovApplyEditModelFallbacksEmpty(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "model_fallbacks", "[]")
	if err != nil || !reflect.DeepEqual(layer.ModelFallbacks, []string{}) {
		t.Fatalf("model_fallbacks [] = %v err=%v", layer.ModelFallbacks, err)
	}
}

func TestCovApplyEditVerbose(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "verbose", "true")
	if err != nil || layer.Verbose == nil || !*layer.Verbose {
		t.Fatalf("verbose = %v err=%v", layer.Verbose, err)
	}
}

func TestCovApplyEditSandboxNet(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "sandbox_net", "true")
	if err != nil || layer.SandboxNet == nil || !*layer.SandboxNet {
		t.Fatalf("sandbox_net = %v err=%v", layer.SandboxNet, err)
	}
}

func TestCovApplyEditSystemPromptAppend(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "system_prompt_append", "a, b")
	want := []string{"a", "b"}
	if err != nil || !reflect.DeepEqual(layer.SystemPromptAppend, want) {
		t.Fatalf("system_prompt_append = %v, want %v, err=%v", layer.SystemPromptAppend, want, err)
	}
}

// --- mcpEditValue ---

func TestCovMCPEditValueEmpty(t *testing.T) {
	if got := mcpEditValue(nil); got != "" {
		t.Fatalf("empty mcpEditValue = %q", got)
	}
}

func TestCovMCPEditValueWithError(t *testing.T) {
	orig := marshalMCPEditSpecs
	marshalMCPEditSpecs = func(specs []mcpEditSpec) ([]byte, error) { return nil, errors.New("marshal fail") }
	defer func() { marshalMCPEditSpecs = orig }()
	if got := mcpEditValue([]appwire.MCPServerSpec{{Name: "x", Command: "c"}}); got != "" {
		t.Fatalf("marshal error should return empty, got %q", got)
	}
}

// --- parseMCPs: object form ---

func TestCovParseMCPsObjectForm(t *testing.T) {
	mcps, err := parseMCPs(`{"name":"x","command":"sh","args":["-c","echo ok"]}`)
	if err != nil {
		t.Fatalf("parseMCPs object form error: %v", err)
	}
	want := []appwire.MCPServerSpec{{Name: "x", Command: "sh", Args: []string{"-c", "echo ok"}}}
	if !reflect.DeepEqual(mcps, want) {
		t.Fatalf("mcps = %+v, want %+v", mcps, want)
	}
}

// --- parseMCPs: invalid JSON array ---

func TestCovParseMCPsInvalidArray(t *testing.T) {
	_, err := parseMCPs("[invalid")
	if err == nil {
		t.Fatal("invalid JSON array should return error")
	}
}

// --- parseMCPs: invalid JSON object ---

func TestCovParseMCPsInvalidObject(t *testing.T) {
	_, err := parseMCPs("{invalid")
	if err == nil {
		t.Fatal("invalid JSON object should return error")
	}
}

// --- parseMCPs: line form with empty command ---

func TestCovParseMCPsLineEmptyCommand(t *testing.T) {
	_, err := parseMCPs("name:")
	if err == nil {
		t.Fatal("empty command should return error")
	}
}

// --- parseMCPs: line form without colon ---

func TestCovParseMCPsLineNoColon(t *testing.T) {
	_, err := parseMCPs("nocommand")
	if err == nil {
		t.Fatal("line without colon should return error")
	}
}

// --- validateLocalLaunchPath ---

func TestCovValidateLocalLaunchPathEmpty(t *testing.T) {
	if err := validateLocalLaunchPath("", "dir"); err == nil {
		t.Fatal("empty path should return error")
	}
}

func TestCovValidateLocalLaunchPathCommandRelative(t *testing.T) {
	// A command with no separator is looked up on PATH
	// "nonexistent-cmd-xyz" should fail LookPath
	err := validateLocalLaunchPath("nonexistent-cmd-xyz", "command")
	if err == nil {
		t.Fatal("nonexistent command should return error")
	}
}

func TestCovValidateLocalLaunchPathNotAbs(t *testing.T) {
	if err := validateLocalLaunchPath("relative/path", "dir"); err == nil {
		t.Fatal("relative path should return error")
	}
}

func TestCovValidateLocalLaunchPathOutputFileDir(t *testing.T) {
	dir := t.TempDir()
	if err := validateLocalLaunchPath(dir, "outputFile"); err == nil {
		t.Fatal("directory as outputFile should return error")
	}
}

func TestCovValidateLocalLaunchPathOutputFileParentMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nonexistent-parent", "output.txt")
	if err := validateLocalLaunchPath(missing, "outputFile"); err == nil {
		t.Fatal("missing parent should return error")
	}
}

// --- layerRows ---

func TestCovLayerRowsWithNilPointers(t *testing.T) {
	rows := layerRows(appwire.LaunchConfigLayer{})
	byField := make(map[string]layerRow, len(rows))
	for _, row := range rows {
		byField[row.field] = row
	}
	for _, field := range []string{"max_rounds", "max_subagent_depth", "max_concurrent_delegate_turns", "max_retained_terminal", "no_project_prompts"} {
		row, ok := byField[field]
		if !ok {
			t.Errorf("layerRows omitted %q", field)
			continue
		}
		if row.value != "(default)" || row.editValue != "(default)" {
			t.Errorf("nil pointer row %q = value %q editValue %q, want (default)/(default)", field, row.value, row.editValue)
		}
	}
}

func TestCovLayerRowsWithSetPointers(t *testing.T) {
	n := 5
	b := true
	rows := layerRows(appwire.LaunchConfigLayer{MaxRounds: &n, NoProjectPrompts: &b})
	byField := make(map[string]layerRow, len(rows))
	for _, row := range rows {
		byField[row.field] = row
	}
	for field, want := range map[string]string{"max_rounds": "5", "no_project_prompts": "true"} {
		row, ok := byField[field]
		if !ok {
			t.Errorf("layerRows omitted %q", field)
			continue
		}
		if row.value != want || row.editValue != want {
			t.Errorf("row %q = value %q editValue %q, want %q/%q", field, row.value, row.editValue, want, want)
		}
	}
}

// --- renderActiveTab: repo tab ---

func TestCovRenderActiveTabRepo(t *testing.T) {
	withTestColorProfile(t)
	p := LaunchSettingsPanel{
		tab:      launchTabRepo,
		resolved: appwire.LaunchConfigResolved{Repo: &appwire.RepoLaunchConfigStatus{Path: "/p", Trust: "trusted", Hash: "h"}},
	}
	v := p.renderActiveTab()
	if !strings.Contains(v, "path:") {
		t.Fatalf("repo tab should show path: %q", v)
	}
}

// --- renderActiveTab: project tab ---

func TestCovRenderActiveTabProject(t *testing.T) {
	withTestColorProfile(t)
	p := LaunchSettingsPanel{
		tab:     launchTabProject,
		cwd:     "/my/cwd",
		project: appwire.LaunchConfigLayer{Model: "x"},
	}
	v := p.renderActiveTab()
	if !strings.Contains(v, "cwd: /my/cwd") {
		t.Fatalf("project tab should show cwd: %q", v)
	}
}

// --- View ---

func TestCovLaunchSettingsView(t *testing.T) {
	withTestColorProfile(t)
	p := NewLaunchSettingsPanel(nil, "/cwd")
	updated, _ := p.Update(LaunchLayerResultMsg{Layer: "global", Data: appwire.LaunchConfigLayer{Model: "x"}})
	v := updated.(LaunchSettingsPanel).View()
	if !strings.Contains(v, "Launch settings") {
		t.Fatalf("view should show title: %q", v)
	}
}

// --- parseEnvMap ---

func TestCovParseEnvMap(t *testing.T) {
	env, err := parseEnvMap("FOO=bar,BAZ=qux")
	if err != nil {
		t.Fatalf("parseEnvMap error: %v", err)
	}
	want := map[string]string{"FOO": "bar", "BAZ": "qux"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %+v, want %+v", env, want)
	}
}

func TestCovParseEnvMapEmpty(t *testing.T) {
	env, err := parseEnvMap("")
	if err != nil || env != nil {
		t.Fatalf("empty env = %#v, want nil; err=%v", env, err)
	}
}

func TestCovParseEnvMapInvalid(t *testing.T) {
	_, err := parseEnvMap("NOEQUALS")
	if err == nil {
		t.Fatal("missing = should return error")
	}
}

func TestCovParseEnvMapWhitespace(t *testing.T) {
	env, err := parseEnvMap("  ")
	if err != nil || env != nil {
		t.Fatalf("whitespace env = %#v, want nil; err=%v", env, err)
	}
}

// --- LaunchSettingsFieldUsesPathCompletion ---

func TestCovLaunchSettingsFieldUsesPathCompletion(t *testing.T) {
	for _, f := range []string{"skills_dirs", "plugin_dirs", "mcp_configs", "system_prompt_file", "system_prompt_append_file", "trace_file", "cpu_profile", "export_atif_path"} {
		if !LaunchSettingsFieldUsesPathCompletion(f) {
			t.Errorf("field %q should use path completion", f)
		}
	}
	if LaunchSettingsFieldUsesPathCompletion("model") {
		t.Error("model should not use path completion")
	}
}

// --- parseModelFallbacks ---

func TestCovParseModelFallbacks(t *testing.T) {
	if got := parseModelFallbacks(""); got != nil {
		t.Fatalf("empty = %v, want nil", got)
	}
	if got := parseModelFallbacks("(default)"); got != nil {
		t.Fatalf("(default) = %v, want nil", got)
	}
	if got := parseModelFallbacks("[]"); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("[] = %#v, want non-nil empty slice", got)
	}
	if got, want := parseModelFallbacks("a, b"), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a, b = %v, want %v", got, want)
	}
}

// --- validateLocalLaunchPath with home ---

func TestCovValidateLocalLaunchPathHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Create a subdirectory so the stat check passes
	subDir := filepath.Join(home, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// ~/sub should be expanded to home/sub and pass for "dir" kind
	if err := validateLocalLaunchPath("~/sub", "dir"); err != nil {
		t.Fatalf("~/sub dir should pass after expansion: %v", err)
	}
}

// --- applyEdit: trace_file, cpu_profile, export_atif_path ---

func TestCovApplyEditTraceFileDefault(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{TraceFile: "old"}, "trace_file", "(default)")
	if err != nil || layer.TraceFile != "" {
		t.Fatalf("trace_file (default) = %q err=%v", layer.TraceFile, err)
	}
}

func TestCovApplyEditCPUProfileDefault(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{CPUProfile: "old"}, "cpu_profile", "(default)")
	if err != nil || layer.CPUProfile != "" {
		t.Fatalf("cpu_profile (default) = %q err=%v", layer.CPUProfile, err)
	}
}

func TestCovApplyEditExportATIFPathDefault(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{ExportATIFPath: "old"}, "export_atif_path", "(default)")
	if err != nil || layer.ExportATIFPath != "" {
		t.Fatalf("export_atif_path (default) = %q err=%v", layer.ExportATIFPath, err)
	}
}

func TestCovApplyEditTraceFileEmpty(t *testing.T) {
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "trace_file", "")
	if err != nil || layer.TraceFile != "" {
		t.Fatalf("trace_file empty = %q err=%v", layer.TraceFile, err)
	}
}

// --- applyEdit: outputFile with valid path ---

func TestCovApplyEditOutputFileValid(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "output.txt")
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "trace_file", validPath)
	if err != nil || layer.TraceFile != validPath {
		t.Fatalf("trace_file valid = %q err=%v", layer.TraceFile, err)
	}
}

// --- applyEdit: system_prompt_file with invalid path ---

func TestCovApplyEditSystemPromptFileInvalid(t *testing.T) {
	_, err := applyEdit(appwire.LaunchConfigLayer{}, "system_prompt_file", "relative/path")
	if err == nil {
		t.Fatal("relative path should fail for system_prompt_file")
	}
}

// --- applyEdit: skills_dirs with valid absolute ---

func TestCovApplyEditSkillsDirsValid(t *testing.T) {
	dir := t.TempDir()
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "skills_dirs", dir)
	want := []string{dir}
	if err != nil || !reflect.DeepEqual(layer.SkillsDirs, want) {
		t.Fatalf("skills_dirs = %v, want %v, err=%v", layer.SkillsDirs, want, err)
	}
}

// --- applyEdit: mcp_configs with valid ---

func TestCovApplyEditMCPConfigsValid(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	layer, err := applyEdit(appwire.LaunchConfigLayer{}, "mcp_configs", configFile)
	want := []string{configFile}
	if err != nil || !reflect.DeepEqual(layer.MCPConfigs, want) {
		t.Fatalf("mcp_configs = %v, want %v, err=%v", layer.MCPConfigs, want, err)
	}
}
