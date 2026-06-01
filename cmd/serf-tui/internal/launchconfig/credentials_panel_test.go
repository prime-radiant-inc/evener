package launchconfig

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
)

// --- old tests kept for regression ---

func TestCredentialsPanelShowsStatusBadges(t *testing.T) {
	withTestColorProfile(t)
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth", AuthModes: []string{"oauth"}},
		{Name: "anthropic", Type: "anthropic", ActiveSource: "env", AuthModes: []string{"apiKey"}},
		{Name: "kimi", Type: "kimi", ActiveSource: "absent", AuthModes: []string{"apiKey"}},
	}}})
	got := updated.(CredentialsPanel).View()
	plain := ansiPattern.ReplaceAllString(got, "")
	for _, want := range []string{"OAUTH", "ENV", "ABSENT"} {
		if !strings.Contains(plain, want) {
			t.Errorf("credentials panel missing badge %q in:\n%s", want, plain)
		}
	}
	if !strings.Contains(plain, "╭") {
		t.Errorf("credentials panel should use Overlay primitive: %q", plain)
	}
}

func TestCredentialsPanel_RendersList(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey", "oauth"}},
		{Name: "anthropic", Type: "anthropic", ActiveSource: "absent", AuthModes: []string{"apiKey"}},
	}}})
	view := updated.(CredentialsPanel).View()
	for _, want := range []string{"openai", "anthropic", "OAUTH", "ABSENT"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestCredentialsPanel_EnterTriggersSet(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "anthropic", Type: "anthropic", ActiveSource: "absent", AuthModes: []string{"apiKey"}},
	}}})
	_, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should produce a cmd")
	}
	msg := cmd()
	got, ok := msg.(CredentialsActionMsg)
	if !ok {
		t.Fatalf("cmd msg = %T", msg)
	}
	if got.Action != "set" || got.Instance != "anthropic" {
		t.Errorf("msg = %+v", got)
	}
}

// --- new instance-based tests ---

func TestCredentialsPanel_GroupsByType(t *testing.T) {
	withTestColorProfile(t)
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", IsDefault: true, ActiveSource: "oauth", AuthModes: []string{"oauth"}},
		{Name: "openai-compat", Type: "openai", ActiveSource: "absent", AuthModes: []string{"apiKey"}},
		{Name: "anthropic", Type: "anthropic", ActiveSource: "env", AuthModes: []string{"apiKey"}},
	}}})
	got := updated.(CredentialsPanel).View()
	plain := ansiPattern.ReplaceAllString(got, "")

	// Type group headers appear
	if !strings.Contains(plain, "openai") {
		t.Errorf("view should show openai type header:\n%s", plain)
	}
	if !strings.Contains(plain, "anthropic") {
		t.Errorf("view should show anthropic type header:\n%s", plain)
	}
	// Default instance marked with star
	if !strings.Contains(plain, "★") {
		t.Errorf("view should show ★ for default instance:\n%s", plain)
	}
	// Both instance names visible
	if !strings.Contains(plain, "openai-compat") {
		t.Errorf("view should show openai-compat instance name:\n%s", plain)
	}
	// Overlay border
	if !strings.Contains(plain, "╭") {
		t.Errorf("credentials panel should use Overlay primitive: %q", plain)
	}
}

func TestCredentialsPanel_OAuthKeyEmitsOAuth(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey", "oauth"}},
	}}})
	_, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if cmd == nil {
		t.Fatal("o key should produce a cmd for oauth instance")
	}
	msg := cmd()
	got, ok := msg.(CredentialsActionMsg)
	if !ok {
		t.Fatalf("cmd msg = %T", msg)
	}
	if got.Action != "oauth" || got.Instance != "openai" {
		t.Errorf("msg = %+v", got)
	}
}

func TestCredentialsPanel_ClearEmitsLogout(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "anthropic", Type: "anthropic", ActiveSource: "env", AuthModes: []string{"apiKey"}},
	}}})
	_, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if cmd == nil {
		t.Fatal("c key should produce a cmd")
	}
	msg := cmd()
	got, ok := msg.(CredentialsActionMsg)
	if !ok {
		t.Fatalf("cmd msg = %T", msg)
	}
	if got.Action != "logout" || got.Instance != "anthropic" {
		t.Errorf("msg = %+v", got)
	}
}

func TestCredentialsPanel_StarEmitsSetDefault(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey"}},
	}}})
	_, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("*")})
	if cmd == nil {
		t.Fatal("* key should produce a cmd")
	}
	msg := cmd()
	got, ok := msg.(InstanceSetDefaultMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want InstanceSetDefaultMsg", msg)
	}
	if got.Name != "openai" {
		t.Errorf("InstanceSetDefaultMsg.Name = %q, want openai", got.Name)
	}
}

func TestCredentialsPanel_XEmitsRemove(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey"}},
	}}})
	_, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd == nil {
		t.Fatal("x key should produce a cmd")
	}
	msg := cmd()
	got, ok := msg.(InstanceRemoveMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want InstanceRemoveMsg", msg)
	}
	if got.Name != "openai" {
		t.Errorf("InstanceRemoveMsg.Name = %q, want openai", got.Name)
	}
}

func TestCredentialsPanel_NOpensCreateForm(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey"}},
	}}})
	panel := updated.(CredentialsPanel)
	panel2, _ := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	p2 := panel2.(CredentialsPanel)
	if !p2.formOpen {
		t.Error("n key should open the create/edit form")
	}
	if p2.formEditing {
		t.Error("n key should open create form (formEditing=false), not edit")
	}
}

func TestCredentialsPanel_EOpensEditForm(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey"}},
	}}})
	panel := updated.(CredentialsPanel)
	panel2, _ := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	p2 := panel2.(CredentialsPanel)
	if !p2.formOpen {
		t.Error("e key should open the edit form")
	}
	if !p2.formEditing {
		t.Error("e key should open edit form (formEditing=true)")
	}
}

func TestCredentialsPanel_NavigationSkipsGroupHeaders(t *testing.T) {
	m := NewCredentialsPanel()
	// Two types, two instances each — group headers must be skipped by up/down.
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth"},
		{Name: "openai2", Type: "openai", ActiveSource: "absent"},
		{Name: "anthropic", Type: "anthropic", ActiveSource: "env"},
	}}})
	panel := updated.(CredentialsPanel)

	// Move down from first instance: should land on second openai instance, not the anthropic header
	panel2, _ := panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	p2 := panel2.(CredentialsPanel)
	inst := p2.selectedInstance()
	if inst == nil || inst.Name != "openai2" {
		t.Errorf("down from openai should land on openai2, got %v", inst)
	}

	// Move down again: should skip group header and land on anthropic
	panel3, _ := p2.Update(tea.KeyMsg{Type: tea.KeyDown})
	p3 := panel3.(CredentialsPanel)
	inst = p3.selectedInstance()
	if inst == nil || inst.Name != "anthropic" {
		t.Errorf("down from openai2 should land on anthropic (skipping header), got %v", inst)
	}
}

func TestCredentialsPanel_EscCloses(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey"}},
	}}})
	panel := updated.(CredentialsPanel)
	panel2, _ := panel.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p2 := panel2.(CredentialsPanel)
	if !p2.done {
		t.Error("esc should set done=true")
	}
	if !p2.cancelled {
		t.Error("esc should set cancelled=true")
	}
}

func TestCredentialsPanel_CreateFormCapturesType(t *testing.T) {
	m := NewCredentialsPanel()
	// Open the create form with "n".
	panel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	p := panel.(CredentialsPanel)
	if !p.formOpen || p.formEditing {
		t.Fatal("n key should open create form")
	}

	// Field 0 = type: type "openai".
	for _, ch := range "openai" {
		panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		p = panel.(CredentialsPanel)
	}
	if p.formType != "openai" {
		t.Fatalf("formType = %q, want openai", p.formType)
	}

	// Advance to field 1 (name).
	panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	if p.formActiveField() != "name" {
		t.Fatalf("after first Enter, active field = %q, want name", p.formActiveField())
	}

	// Type a name.
	for _, ch := range "myinst" {
		panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		p = panel.(CredentialsPanel)
	}

	// Advance to field 2 (apiStyle) — skip it.
	panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	if p.formActiveField() != "apiStyle" {
		t.Fatalf("after second Enter, active field = %q, want apiStyle", p.formActiveField())
	}

	// Advance to field 3 (baseURL) — leave empty.
	panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	if p.formActiveField() != "baseURL" {
		t.Fatalf("after third Enter, active field = %q, want baseURL", p.formActiveField())
	}

	// Submit on the last field.
	panel, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	if p.formOpen {
		t.Error("form should be closed after submit")
	}
	if cmd == nil {
		t.Fatal("submit should produce a cmd")
	}
	msg := cmd()
	got, ok := msg.(InstanceCreateSubmitMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want InstanceCreateSubmitMsg", msg)
	}
	if got.Params.Type != "openai" {
		t.Errorf("Params.Type = %q, want openai", got.Params.Type)
	}
	if got.Params.Name != "myinst" {
		t.Errorf("Params.Name = %q, want myinst", got.Params.Name)
	}
}

func TestCredentialsPanel_InstanceListResultRefreshesPanel(t *testing.T) {
	m := NewCredentialsPanel()
	// Initial load
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth"},
	}}})
	// Second load (e.g. after a mutation)
	updated2, _ := updated.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", Type: "openai", ActiveSource: "oauth"},
		{Name: "anthropic", Type: "anthropic", ActiveSource: "absent"},
	}}})
	view := updated2.(CredentialsPanel).View()
	if !strings.Contains(view, "anthropic") {
		t.Errorf("second InstanceListResultMsg should refresh panel; anthropic missing:\n%s", view)
	}
}
