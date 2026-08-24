package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
)

// TestCovUpdateSpawnKeyEsc exercises the esc key path that closes the spawn form.
func TestCovUpdateSpawnKeyEsc(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEscape})
	after := got.(hubModel)
	if after.mode != hubModeDashboard {
		t.Fatalf("mode = %v, want hubModeDashboard after esc in spawn", after.mode)
	}
}

// TestCovUpdateSpawnKeyTab exercises tab cycling through fields and recent dirs.
func TestCovUpdateSpawnKeyTab(t *testing.T) {
	// Tab from Dir field with recent dirs.
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.spawnRecentDirs = []string{"/tmp/proj1", "/tmp/proj2"}
	m.setSpawnFocus(hubSpawnFieldDir)
	m.spawnDirInput.SetValue("")
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyTab})
	after := got.(hubModel)
	if after.spawnDir != "/tmp/proj1" {
		t.Fatalf("spawnDir = %q, want /tmp/proj1", after.spawnDir)
	}

	// Tab cycling to next recent dir.
	got, _ = after.updateSpawnKey(tea.KeyMsg{Type: tea.KeyTab})
	after = got.(hubModel)
	if after.spawnDir != "/tmp/proj2" {
		t.Fatalf("spawnDir = %q, want /tmp/proj2 after second tab", after.spawnDir)
	}

	// Tab from Dir with custom path (path completion, not recent).
	m = newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldDir)
	m.spawnDirInput.SetValue("/tmp/nonexistent")
	got, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyTab})
	after = got.(hubModel)
	// Should advance focus since path doesn't match a recent dir.
	_ = after

	// Tab advances focus from non-dir fields.
	m = newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldPrompt)
	got, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyTab})
	after = got.(hubModel)
	if after.spawnFocus == hubSpawnFieldPrompt {
		t.Fatal("tab should advance focus from prompt")
	}
}

// TestCovUpdateSpawnKeyShiftTab exercises shift+tab.
func TestCovUpdateSpawnKeyShiftTab(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldDir)
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	after := got.(hubModel)
	if after.spawnFocus == hubSpawnFieldDir {
		t.Fatal("shift+tab should move focus backward from dir")
	}
}

// TestCovUpdateSpawnKeyEnter exercises enter on each field.
func TestCovUpdateSpawnKeyEnter(t *testing.T) {
	// Enter on harness: cycles harness.
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.spawnHarnesses = []string{"evener", "codex"}
	m.setSpawnFocus(hubSpawnFieldHarness)
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEnter})
	after := got.(hubModel)
	if after.spawnHarness == "evener" {
		t.Fatal("enter on harness should cycle")
	}

	// Enter on model field.
	m = newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldModel)
	got, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEnter})
	_ = got

	// Enter on dir: advances focus.
	m = newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldDir)
	got, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEnter})
	after = got.(hubModel)
	if after.spawnFocus == hubSpawnFieldDir {
		t.Fatal("enter on dir should advance focus")
	}

	// Enter on prompt: submits (needs client + model set for evener harness).
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.spawnModel = "openai/gpt-5"
	m.spawnModels = []tuipick.ModelPickerItem{{ID: "openai/gpt-5", Display: "GPT 5"}}
	m.setSpawnFocus(hubSpawnFieldPrompt)
	m.session.setInputValue("do something")
	got, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEnter})
	after = got.(hubModel)
	if !after.spawnSubmitting {
		t.Fatal("enter on prompt with text should submit")
	}
}

// TestCovUpdateSpawnKeySpace exercises space on harness and model fields.
func TestCovUpdateSpawnKeySpace(t *testing.T) {
	// Space on harness.
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.spawnHarnesses = []string{"evener", "codex"}
	m.setSpawnFocus(hubSpawnFieldHarness)
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	after := got.(hubModel)
	if after.spawnHarness == "evener" {
		t.Fatal("space on harness should cycle")
	}

	// Space on model field.
	m = newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldModel)
	got, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	_ = got
}

// TestCovUpdateSpawnKeyCtrlL exercises ctrl+l opening launch overrides.
func TestCovUpdateSpawnKeyCtrlL(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	got, cmd := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyCtrlL})
	if cmd == nil {
		t.Fatal("ctrl+l should produce a command")
	}
	_ = got
}

// TestCovUpdateSpawnKeyCtrlLWithOverrides exercises ctrl+l with existing overrides.
func TestCovUpdateSpawnKeyCtrlLWithOverrides(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{Model: "test/model"}
	got, cmd := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyCtrlL})
	if cmd == nil {
		t.Fatal("ctrl+l should produce a command")
	}
	_ = got
}

// TestCovUpdateSpawnKeyCtrlU exercises ctrl+u clearing dir field.
func TestCovUpdateSpawnKeyCtrlU(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldDir)
	m.spawnDirInput.SetValue("/some/path")
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	after := got.(hubModel)
	if after.spawnDir != "" {
		t.Fatalf("spawnDir = %q, want empty after ctrl+u", after.spawnDir)
	}
}

// TestCovUpdateSpawnKeyPromptTyping exercises typing in the prompt field.
func TestCovUpdateSpawnKeyPromptTyping(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldPrompt)
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	after := got.(hubModel)
	if after.session.input.Value() != "x" {
		t.Fatalf("input = %q, want x", after.session.input.Value())
	}
}

// TestCovUpdateSpawnKeyPromptNewline exercises ctrl+j / alt+enter newline.
func TestCovUpdateSpawnKeyPromptNewline(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldPrompt)
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyCtrlJ})
	after := got.(hubModel)
	if after.session.input.Value() != "\n" {
		t.Fatalf("input = %q, want newline", after.session.input.Value())
	}

	// Alt+enter.
	m = newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldPrompt)
	got, _ = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	after = got.(hubModel)
	if after.session.input.Value() != "\n" {
		t.Fatalf("input = %q, want newline from alt+enter", after.session.input.Value())
	}
}

// TestCovUpdateSpawnKeyLaunchOverridesModal exercises the launch overrides modal path.
func TestCovUpdateSpawnKeyLaunchOverridesModal(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	modal := launchconfig.NewLaunchOverridesModal()
	m.launchOverridesModal = &modal
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEsc})
	// Esc with launchOverridesModal set — the modal may handle esc; the test
	// exercises the modal-routing path. Just ensure no panic.
	_ = got
}

// TestCovUpdateSpawnKeyNonPromptNoOp exercises keys on non-prompt non-dir fields.
func TestCovUpdateSpawnKeyNonPromptNoOp(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldHarness)
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	// Should be a no-op (returns model unchanged).
	_ = got
}

// TestCovActivateSpawnModelField exercises the model activation paths.
func TestCovActivateSpawnModelField(t *testing.T) {
	// No models, not evener harness, with client: fetches models.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.spawnHarness = "codex"
	m.spawnHarnessKinds = map[string]string{"codex": "codex"}
	m.spawnModels = nil
	m.spawnHarnessModels = nil
	got, cmd := m.activateSpawnModelField()
	if cmd == nil {
		t.Fatal("should produce a fetch cmd for non-evener harness with client")
	}
	_ = got

	// No models, not evener harness, no client: sets error.
	m = newHubModel(nil, "http://hub.test")
	m.spawnHarness = "codex"
	m.spawnHarnessKinds = map[string]string{"codex": "codex"}
	got, _ = m.activateSpawnModelField()
	after := got.(hubModel)
	if after.err == nil {
		t.Fatal("should set err for no models and no client")
	}

	// No models, evener harness: sets error.
	m = newHubModel(nil, "http://hub.test")
	m.spawnHarness = "evener"
	m.spawnHarnessKinds = map[string]string{"evener": "evener"}
	got, _ = m.activateSpawnModelField()
	after = got.(hubModel)
	if after.err == nil {
		t.Fatal("should set err for no evener models")
	}

	// Has models: opens picker.
	m = newHubModel(nil, "http://hub.test")
	m.spawnHarness = "evener"
	m.spawnHarnessKinds = map[string]string{"evener": "evener"}
	m.spawnModels = []tuipick.ModelPickerItem{{ID: "openai/gpt-5", Display: "GPT 5"}}
	got, _ = m.activateSpawnModelField()
	after = got.(hubModel)
	if after.spawnModelPicker == nil {
		t.Fatal("should open model picker")
	}
}

// TestCovSubmitSpawnForm exercises validation paths.
func TestCovSubmitSpawnForm(t *testing.T) {
	// No client: no-op.
	m := hubModel{}
	got, _ := m.submitSpawnForm()
	after := got.(hubModel)
	if after.spawnSubmitting {
		t.Fatal("should not be submitting without client")
	}

	// Already submitting: no-op.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	m.spawnSubmitting = true
	got, _ = m.submitSpawnForm()
	if got.(hubModel).spawnSubmitting != true {
		t.Fatal("should remain submitting")
	}

	// Empty prompt with unsupported reason.
	m = newHubModel(client, "http://hub.test")
	m.spawnHarness = "codex"
	m.spawnHarnessKinds = map[string]string{"codex": "codex"}
	m.spawnEmptyTaskReasons = map[string]string{"codex": "codex requires a prompt"}
	m.spawnEmptyTaskNext = map[string]string{"codex": "provide a prompt"}
	got, _ = m.submitSpawnForm()
	after = got.(hubModel)
	if after.err == nil {
		t.Fatal("should set err for empty task unsupported")
	}

	// Evener harness, empty model: error.
	m = newHubModel(client, "http://hub.test")
	m.spawnHarness = "evener"
	m.spawnHarnessKinds = map[string]string{"evener": "evener"}
	m.spawnModel = ""
	got, _ = m.submitSpawnForm()
	after = got.(hubModel)
	if after.err == nil {
		t.Fatal("should set err for empty model with evener harness")
	}

	// Disabled model: error.
	m = newHubModel(client, "http://hub.test")
	m.spawnHarness = "evener"
	m.spawnHarnessKinds = map[string]string{"evener": "evener"}
	m.spawnModel = "openai/gpt-5"
	m.spawnModels = []tuipick.ModelPickerItem{{ID: "openai/gpt-5", Display: "GPT 5", DisabledReason: "unavailable"}}
	got, _ = m.submitSpawnForm()
	after = got.(hubModel)
	if after.err == nil {
		t.Fatal("should set err for disabled model")
	}

	// Valid submit.
	m = newHubModel(client, "http://hub.test")
	m.spawnHarness = "evener"
	m.spawnHarnessKinds = map[string]string{"evener": "evener"}
	m.spawnModel = "openai/gpt-5"
	m.spawnModels = []tuipick.ModelPickerItem{{ID: "openai/gpt-5", Display: "GPT 5"}}
	m.session.setInputValue("do something")
	got, cmd := m.submitSpawnForm()
	after = got.(hubModel)
	if !after.spawnSubmitting {
		t.Fatal("should be submitting")
	}
	if after.spawnLaunchOverrides != nil {
		t.Fatal("spawnLaunchOverrides should be cleared (one-shot)")
	}
	if cmd == nil {
		t.Fatal("should produce a spawn command")
	}
}

// TestCovSpawnFieldPrefix exercises prefix rendering.
func TestCovSpawnFieldPrefix(t *testing.T) {
	m := hubModel{spawnFocus: hubSpawnFieldModel}
	if got := m.spawnFieldPrefix(hubSpawnFieldModel); got != ">" {
		t.Fatalf("prefix for focused field = %q, want >", got)
	}
	if got := m.spawnFieldPrefix(hubSpawnFieldPrompt); got != " " {
		t.Fatalf("prefix for non-focused field = %q, want space", got)
	}
}

// TestCovSpawnFieldHint exercises hint text for each field.
func TestCovSpawnFieldHint(t *testing.T) {
	m := hubModel{spawnFocus: hubSpawnFieldHarness}
	if got := m.spawnFieldHint(); got == "" {
		t.Fatal("harness hint should not be empty")
	}

	m = hubModel{spawnFocus: hubSpawnFieldModel, spawnHarness: "evener", spawnHarnessKinds: map[string]string{"evener": "evener"}}
	if got := m.spawnFieldHint(); got == "" {
		t.Fatal("model hint should not be empty")
	}

	// Model hint with no models and non-evener harness.
	m = hubModel{spawnFocus: hubSpawnFieldModel, spawnHarness: "codex", spawnHarnessKinds: map[string]string{"codex": "codex"}}
	if got := m.spawnFieldHint(); got == "" {
		t.Fatal("model hint should not be empty for codex")
	}

	m = hubModel{spawnFocus: hubSpawnFieldDir}
	if got := m.spawnFieldHint(); got == "" {
		t.Fatal("dir hint should not be empty")
	}

	m = hubModel{spawnFocus: hubSpawnFieldPrompt}
	if got := m.spawnFieldHint(); got == "" {
		t.Fatal("prompt hint should not be empty")
	}
}

// TestCovCloseSpawnForm exercises closing the spawn form.
func TestCovCloseSpawnForm(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.closeSpawnForm()
	if m.mode != hubModeDashboard {
		t.Fatalf("mode = %v, want hubModeDashboard", m.mode)
	}
}

// TestCovCycleSpawnHarness exercises harness cycling.
func TestCovCycleSpawnHarness(t *testing.T) {
	m := hubModel{spawnHarness: "evener", spawnHarnesses: []string{"evener", "codex"}, spawnHarnessKinds: map[string]string{"evener": "evener", "codex": "codex"}}
	m.cycleSpawnHarness()
	if m.spawnHarness != "codex" {
		t.Fatalf("harness = %q, want codex", m.spawnHarness)
	}

	// Wrap around.
	m.cycleSpawnHarness()
	if m.spawnHarness != "evener" {
		t.Fatalf("harness = %q, want evener (wrap)", m.spawnHarness)
	}

	// Harness not in list: falls to first.
	m = hubModel{spawnHarness: "unknown", spawnHarnesses: []string{"evener", "codex"}, spawnHarnessKinds: map[string]string{"evener": "evener", "codex": "codex"}}
	m.cycleSpawnHarness()
	if m.spawnHarness != "evener" {
		t.Fatalf("harness = %q, want evener (first)", m.spawnHarness)
	}

	// Empty harnesses: defaults to evener.
	m = hubModel{spawnHarness: "unknown", spawnHarnessKinds: map[string]string{}}
	m.cycleSpawnHarness()
	if m.spawnHarness != "evener" {
		t.Fatalf("harness = %q, want evener (default)", m.spawnHarness)
	}
}

// TestCovSpawnHarnessKind exercises the harness kind lookup.
func TestCovSpawnHarnessKind(t *testing.T) {
	m := hubModel{spawnHarness: "codex", spawnHarnessKinds: map[string]string{"codex": "codex"}}
	if got := m.spawnHarnessKind(); got != "codex" {
		t.Fatalf("kind = %q, want codex", got)
	}

	// Default to evener.
	m = hubModel{spawnHarness: "unknown", spawnHarnessKinds: map[string]string{}}
	if got := m.spawnHarnessKind(); got != "evener" {
		t.Fatalf("kind = %q, want evener (default)", got)
	}
}

// TestCovSpawnHarnessModelDisplay exercises the harness model display.
func TestCovSpawnHarnessModelDisplay(t *testing.T) {
	// Empty model.
	m := hubModel{spawnHarness: "codex", spawnModel: ""}
	if got := m.spawnHarnessModelDisplay(); got != "" {
		t.Fatalf("display = %q, want empty", got)
	}

	// Model with slash (evener-style): returned as-is.
	m = hubModel{spawnHarness: "codex", spawnModel: "openai/gpt-5"}
	if got := m.spawnHarnessModelDisplay(); got != "openai/gpt-5" {
		t.Fatalf("display = %q, want openai/gpt-5", got)
	}

	// Model without slash (harness-style): prefixed with harness.
	m = hubModel{spawnHarness: "codex", spawnModel: "claude-4"}
	if got := m.spawnHarnessModelDisplay(); got != "codex/claude-4" {
		t.Fatalf("display = %q, want codex/claude-4", got)
	}
}

// TestCovSpawnWorkingDir exercises working dir resolution.
func TestCovSpawnWorkingDir(t *testing.T) {
	// No selected row.
	m := hubModel{}
	if got := m.spawnWorkingDir(); got != "" {
		t.Fatalf("workingDir = %q, want empty", got)
	}
}

// TestCovSpawnView exercises rendering the spawn view.
func TestCovSpawnView(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.spawnHarnesses = []string{"evener", "codex"}
	m.spawnHarnessKinds = map[string]string{"evener": "evener", "codex": "codex"}
	m.spawnModels = []tuipick.ModelPickerItem{{ID: "openai/gpt-5", Display: "GPT 5"}}
	m.width = 100
	m.height = 40
	got := m.spawnView()
	if got == "" {
		t.Fatal("spawnView should not be empty")
	}

	// With model picker overlay.
	picker := tuipick.NewModelPicker(m.spawnModels, "", 100)
	m.spawnModelPicker = &picker
	got = m.spawnView()
	if got == "" {
		t.Fatal("spawnView with picker should not be empty")
	}

	// With launch overrides modal.
	m.spawnModelPicker = nil
	modal := launchconfig.NewLaunchOverridesModal()
	m.launchOverridesModal = &modal
	got = m.spawnView()
	if got == "" {
		t.Fatal("spawnView with overrides modal should not be empty")
	}

	// With codex harness and no model (harness default).
	m.launchOverridesModal = nil
	m.spawnHarness = "codex"
	m.spawnHarnessKinds = map[string]string{"codex": "codex"}
	m.spawnModel = ""
	m.spawnHarnessModels = map[string][]tuipick.ModelPickerItem{}
	got = m.spawnView()
	if got == "" {
		t.Fatal("spawnView with codex harness should not be empty")
	}
}

// TestCovSpawnDirView exercises dir view rendering.
func TestCovSpawnDirView(t *testing.T) {
	m := hubModel{spawnDir: "/tmp/proj"}
	if got := m.spawnDirView(); got != "/tmp/proj" {
		t.Fatalf("dirView = %q, want /tmp/proj", got)
	}

	// Empty dir: shows hub default.
	m = hubModel{spawnDir: ""}
	if got := m.spawnDirView(); got != "(hub default)" {
		t.Fatalf("dirView = %q, want (hub default)", got)
	}

	// Focused dir field: shows input view.
	m = hubModel{spawnDir: "/tmp/proj", spawnFocus: hubSpawnFieldDir, spawnDirInput: newSpawnDirInput()}
	m.spawnDirInput.SetValue("/tmp/proj")
	if got := m.spawnDirView(); got == "" {
		t.Fatal("dirView with focus should not be empty")
	}
}

// TestCovFirstEnabledModel exercises firstEnabledModel.
func TestCovFirstEnabledModel(t *testing.T) {
	// All disabled.
	items := []tuipick.ModelPickerItem{
		{ID: "a", DisabledReason: "unavailable"},
		{ID: "b", DisabledReason: "unavailable"},
	}
	if _, ok := firstEnabledModel(items); ok {
		t.Fatal("should not find enabled model when all disabled")
	}

	// One enabled.
	items = []tuipick.ModelPickerItem{
		{ID: "a", DisabledReason: "unavailable"},
		{ID: "b"},
	}
	model, ok := firstEnabledModel(items)
	if !ok || model.ID != "b" {
		t.Fatalf("model = %+v, ok = %v, want b", model, ok)
	}

	// Empty.
	if _, ok := firstEnabledModel(nil); ok {
		t.Fatal("should not find enabled model in empty list")
	}
}

// TestCovSpawnModelDisabledReason exercises disabled reason lookup.
func TestCovSpawnModelDisabledReason(t *testing.T) {
	m := hubModel{spawnModels: []tuipick.ModelPickerItem{
		{ID: "openai/gpt-5", Display: "GPT 5", DisabledReason: "rate limited"},
		{ID: "anthropic/claude", Display: "Claude"},
	}}
	if got := m.spawnModelDisabledReason("openai/gpt-5"); got != "rate limited" {
		t.Fatalf("reason = %q, want 'rate limited'", got)
	}

	// Match by display name.
	if got := m.spawnModelDisabledReason("GPT 5"); got != "rate limited" {
		t.Fatalf("reason by display = %q, want 'rate limited'", got)
	}

	// Empty model.
	if got := m.spawnModelDisabledReason(""); got != "" {
		t.Fatalf("reason = %q, want empty", got)
	}

	// Model not in list.
	if got := m.spawnModelDisabledReason("unknown/model"); got != "" {
		t.Fatalf("reason = %q, want empty for unknown", got)
	}
}

// TestCovSpawnEmptyTaskUnsupported exercises empty task reasons.
func TestCovSpawnEmptyTaskUnsupportedReason(t *testing.T) {
	m := hubModel{spawnHarness: "codex", spawnEmptyTaskReasons: map[string]string{"codex": "requires prompt"}}
	if got := m.spawnEmptyTaskUnsupportedReason(); got != "requires prompt" {
		t.Fatalf("reason = %q, want 'requires prompt'", got)
	}

	// Nil map.
	m = hubModel{spawnHarness: "evener"}
	if got := m.spawnEmptyTaskUnsupportedReason(); got != "" {
		t.Fatalf("reason = %q, want empty", got)
	}
}

func TestCovSpawnEmptyTaskUnsupportedNextAction(t *testing.T) {
	m := hubModel{spawnHarness: "codex", spawnEmptyTaskNext: map[string]string{"codex": "type a prompt"}}
	if got := m.spawnEmptyTaskUnsupportedNextAction(); got != "type a prompt" {
		t.Fatalf("next = %q, want 'type a prompt'", got)
	}

	// Nil map.
	m = hubModel{spawnHarness: "evener"}
	if got := m.spawnEmptyTaskUnsupportedNextAction(); got != "" {
		t.Fatalf("next = %q, want empty", got)
	}
}

// TestCovSetSpawnFocus exercises focus clamping and field transitions.
func TestCovSetSpawnFocus(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.openSpawnForm()

	// Focus prompt.
	m.setSpawnFocus(hubSpawnFieldPrompt)
	if m.spawnFocus != hubSpawnFieldPrompt {
		t.Fatal("should focus prompt")
	}

	// Focus dir.
	m.setSpawnFocus(hubSpawnFieldDir)
	if m.spawnFocus != hubSpawnFieldDir {
		t.Fatal("should focus dir")
	}

	// Out of range: clamped to prompt.
	m.setSpawnFocus(hubSpawnField(99))
	if m.spawnFocus != hubSpawnFieldPrompt {
		t.Fatal("should clamp to prompt")
	}

	// Negative: clamped to prompt.
	m.setSpawnFocus(hubSpawnField(-1))
	if m.spawnFocus != hubSpawnFieldPrompt {
		t.Fatal("should clamp negative to prompt")
	}
}

// TestCovAdvanceSpawnFocus exercises focus advancement and wrapping.
func TestCovAdvanceSpawnFocus(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.openSpawnForm()
	m.setSpawnFocus(hubSpawnFieldPrompt)
	m.advanceSpawnFocus(1)
	if m.spawnFocus == hubSpawnFieldPrompt {
		t.Fatal("should advance from prompt")
	}

	// Advance backward (wraps).
	m.advanceSpawnFocus(-1)
	// After advancing forward once then backward once, should be back at prompt.
	if m.spawnFocus != hubSpawnFieldPrompt {
		t.Fatalf("spawnFocus = %v, want hubSpawnFieldPrompt", m.spawnFocus)
	}
}

// TestCovResizeSpawnInput exercises resize.
func TestCovResizeSpawnInput(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.openSpawnForm()
	// Should not panic.
	m.resizeSpawnInput()
}

// TestCovResizeSpawnInputFrom exercises resize from prevHeight.
func TestCovResizeSpawnInputFrom(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.openSpawnForm()
	// No change in height: no-op.
	m.resizeSpawnInputFrom(m.session.input.Height())
}

// TestCovNextSpawnRecentDir exercises recent dir cycling.
func TestCovNextSpawnRecentDir(t *testing.T) {
	// Empty recents.
	m := hubModel{}
	if _, ok := m.nextSpawnRecentDir(""); ok {
		t.Fatal("should return false for empty recents")
	}

	// From empty: returns first.
	m = hubModel{spawnRecentDirs: []string{"/a", "/b"}}
	next, ok := m.nextSpawnRecentDir("")
	if !ok || next != "/a" {
		t.Fatalf("next = %q, ok = %v, want /a", next, ok)
	}

	// From prefill untouched: returns first.
	m = hubModel{spawnRecentDirs: []string{"/a", "/b"}, spawnDirPrefillUntouched: true}
	next, ok = m.nextSpawnRecentDir("/prefill")
	if !ok || next != "/a" {
		t.Fatalf("next = %q, ok = %v, want /a from prefill", next, ok)
	}

	// From current recent: advances.
	m = hubModel{spawnRecentDirs: []string{"/a", "/b"}, spawnRecentIdx: 0}
	next, ok = m.nextSpawnRecentDir("/a")
	if !ok || next != "/b" {
		t.Fatalf("next = %q, ok = %v, want /b", next, ok)
	}

	// Custom path: returns false.
	m = hubModel{spawnRecentDirs: []string{"/a", "/b"}, spawnRecentIdx: 0}
	_, ok = m.nextSpawnRecentDir("/custom")
	if ok {
		t.Fatal("should return false for custom path")
	}
}

// TestCovSpawnRecentDirsVisible exercises visibility logic.
func TestCovSpawnRecentDirsVisible(t *testing.T) {
	// No recents.
	m := hubModel{}
	if m.spawnRecentDirsVisible() {
		t.Fatal("should be invisible with no recents")
	}

	// Prefill untouched: visible.
	m = hubModel{spawnRecentDirs: []string{"/a"}, spawnDirPrefillUntouched: true}
	if !m.spawnRecentDirsVisible() {
		t.Fatal("should be visible with prefill untouched")
	}

	// Empty field: visible.
	m = hubModel{spawnRecentDirs: []string{"/a"}, spawnDirInput: newSpawnDirInput()}
	if !m.spawnRecentDirsVisible() {
		t.Fatal("should be visible with empty field")
	}

	// Field shows a recent: visible.
	m = hubModel{spawnRecentDirs: []string{"/a"}, spawnDirInput: newSpawnDirInput()}
	m.spawnDirInput.SetValue("/a")
	if !m.spawnRecentDirsVisible() {
		t.Fatal("should be visible when field shows a recent")
	}

	// Custom path: invisible.
	m.spawnDirInput.SetValue("/custom")
	if m.spawnRecentDirsVisible() {
		t.Fatal("should be invisible with custom path")
	}
}

// TestCovSpawnModelPickerTitle exercises picker title.
func TestCovSpawnModelPickerTitle(t *testing.T) {
	m := hubModel{spawnHarness: "evener", spawnHarnessKinds: map[string]string{"evener": "evener"}}
	if got := m.spawnModelPickerTitle(); got != "Select model" {
		t.Fatalf("title = %q, want 'Select model'", got)
	}

	m = hubModel{spawnHarness: "codex", spawnHarnessKinds: map[string]string{"codex": "codex"}}
	if got := m.spawnModelPickerTitle(); got != "Select codex model" {
		t.Fatalf("title = %q, want 'Select codex model'", got)
	}
}

// TestCovSpawnSelectableModels exercises model selection.
func TestCovSpawnSelectableModels(t *testing.T) {
	// Evener harness.
	m := hubModel{
		spawnHarness:       "evener",
		spawnHarnessKinds:  map[string]string{"evener": "evener"},
		spawnModels:        []tuipick.ModelPickerItem{{ID: "openai/gpt-5"}},
		spawnHarnessModels: map[string][]tuipick.ModelPickerItem{"codex": {{ID: "codex/model"}}},
	}
	if got := m.spawnSelectableModels(); len(got) != 1 || got[0].ID != "openai/gpt-5" {
		t.Fatalf("selectable models = %+v, want openai/gpt-5", got)
	}

	// Codex harness.
	m.spawnHarness = "codex"
	m.spawnHarnessKinds = map[string]string{"codex": "codex"}
	if got := m.spawnSelectableModels(); len(got) != 1 || got[0].ID != "codex/model" {
		t.Fatalf("selectable models = %+v, want codex/model", got)
	}
}

// TestCovSyncSpawnModelWithHarness exercises model syncing.
func TestCovSyncSpawnModelWithHarness(t *testing.T) {
	// Codex harness with slash model: clears.
	m := hubModel{spawnHarness: "codex", spawnHarnessKinds: map[string]string{"codex": "codex"}, spawnModel: "openai/gpt-5"}
	m.syncSpawnModelWithHarness()
	if m.spawnModel != "" {
		t.Fatalf("model = %q, want empty (cleared for codex)", m.spawnModel)
	}

	// Codex harness without slash model: keeps.
	m = hubModel{spawnHarness: "codex", spawnHarnessKinds: map[string]string{"codex": "codex"}, spawnModel: "claude-4"}
	m.syncSpawnModelWithHarness()
	if m.spawnModel != "claude-4" {
		t.Fatalf("model = %q, want claude-4 (kept)", m.spawnModel)
	}

	// Evener harness, empty model, has enabled models: picks first.
	m = hubModel{
		spawnHarness:      "evener",
		spawnHarnessKinds: map[string]string{"evener": "evener"},
		spawnModel:        "",
		spawnModels:       []tuipick.ModelPickerItem{{ID: "openai/gpt-5"}},
	}
	m.syncSpawnModelWithHarness()
	if m.spawnModel != "openai/gpt-5" {
		t.Fatalf("model = %q, want openai/gpt-5 (first enabled)", m.spawnModel)
	}

	// Evener harness, already has model: keeps.
	m = hubModel{
		spawnHarness:      "evener",
		spawnHarnessKinds: map[string]string{"evener": "evener"},
		spawnModel:        "anthropic/claude",
		spawnModels:       []tuipick.ModelPickerItem{{ID: "openai/gpt-5"}},
	}
	m.syncSpawnModelWithHarness()
	if m.spawnModel != "anthropic/claude" {
		t.Fatalf("model = %q, want anthropic/claude (kept)", m.spawnModel)
	}
}

// TestCovSpawnProjectName exercises project name resolution.
func TestCovSpawnProjectName(t *testing.T) {
	// No selected row.
	m := hubModel{}
	if got := m.spawnProjectName(); got != "" {
		t.Fatalf("projectName = %q, want empty", got)
	}
}

// TestCovResetSpawnForm exercises form reset, including env model override.
func TestCovResetSpawnForm(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.openSpawnForm()
	m.spawnHarness = "codex"
	m.spawnModel = "test"
	m.resetSpawnForm()
	if m.spawnHarness != "evener" {
		t.Fatalf("harness = %q, want evener", m.spawnHarness)
	}
	if m.spawnModel != "" {
		t.Fatalf("model = %q, want empty", m.spawnModel)
	}
}

// TestCovStringInSlice exercises the helper.
func TestCovStringInSlice(t *testing.T) {
	if !stringInSlice("a", []string{"a", "b"}) {
		t.Fatal("should find 'a'")
	}
	if stringInSlice("c", []string{"a", "b"}) {
		t.Fatal("should not find 'c'")
	}
	if stringInSlice("a", nil) {
		t.Fatal("should not find in nil slice")
	}
}

// TestCovOpenSpawnModelPicker exercises opening the picker.
func TestCovOpenSpawnModelPicker(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.openSpawnForm()
	m.openSpawnModelPicker([]tuipick.ModelPickerItem{{ID: "openai/gpt-5", Display: "GPT 5"}})
	if m.spawnModelPicker == nil {
		t.Fatal("spawnModelPicker should be set")
	}
	if m.err != nil {
		t.Fatalf("err should be cleared, got %v", m.err)
	}
}

// TestCovSpawnHarnessUsesEvenerModels exercises the harness check.
func TestCovSpawnHarnessUsesEvenerModels(t *testing.T) {
	m := hubModel{spawnHarness: "evener", spawnHarnessKinds: map[string]string{"evener": "evener"}}
	if !m.spawnHarnessUsesEvenerModels() {
		t.Fatal("evener should use evener models")
	}

	m = hubModel{spawnHarness: "codex", spawnHarnessKinds: map[string]string{"codex": "codex"}}
	if m.spawnHarnessUsesEvenerModels() {
		t.Fatal("codex should not use evener models")
	}
}

// TestCovUpdateSpawnKeyFollowupAndOverrides exercises the followup+overrides modal path.
func TestCovUpdateSpawnKeyFollowupAndOverrides(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	// Set both followupModal and launchOverridesModal.
	modal := tuipick.NewTextInputModal("test", "test")
	m.followupModal = &modal
	overridesModal := launchconfig.NewLaunchOverridesModal()
	m.launchOverridesModal = &overridesModal
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEsc})
	// Should route to followup modal first.
	_ = got
}

// TestCovUpdateSpawnKeyModelPicker exercises the model picker path.
func TestCovUpdateSpawnKeyModelPicker(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	picker := tuipick.NewModelPicker([]tuipick.ModelPickerItem{{ID: "openai/gpt-5", Display: "GPT 5"}}, "", 100)
	m.spawnModelPicker = &picker
	got, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEsc})
	after := got.(hubModel)
	if after.spawnModelPicker != nil {
		t.Fatal("Esc on picker should close it")
	}
}

// ensure errors import is used
var _ = errors.New
