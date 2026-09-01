package launchconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuiprim"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
)

// --- old tests kept for regression ---

func TestCredentialsPanelShowsStatusBadges(t *testing.T) {
	withTestColorProfile(t)
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth", AuthModes: []string{"oauth"}},
		{Name: "anthropic", ProviderID: "anthropic", ActiveSource: "env:ANTHROPIC_API_KEY", AuthModes: []string{"apiKey"}},
		// CredentialRequired is spelled out because Go's zero value for it is
		// "optional", which is the wrong answer for a key-authenticated
		// provider: the hub always sends the field, and for kimi it sends true.
		{Name: "kimi", ProviderID: "kimi", ActiveSource: "none", CredentialRequired: true, AuthModes: []string{"apiKey"}},
	}}})
	got := updated.(CredentialsPanel).View()
	plain := ansiPattern.ReplaceAllString(got, "")
	for _, want := range []string{"OAUTH", "ENV:ANTHROPIC_API_KEY", "NONE"} {
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
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey", "oauth"}},
		{Name: "anthropic", ProviderID: "anthropic", ActiveSource: "none", CredentialRequired: true, AuthModes: []string{"apiKey"}},
	}}})
	view := updated.(CredentialsPanel).View()
	for _, want := range []string{"openai", "anthropic", "OAUTH", "NONE"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestCredentialsPanel_EnterTriggersSet(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "anthropic", ProviderID: "anthropic", ActiveSource: "none", AuthModes: []string{"apiKey"}},
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
		{Name: "openai", ProviderID: "openai", IsDefault: true, ActiveSource: "oauth", AuthModes: []string{"oauth"}},
		{Name: "openai-compat", ProviderID: "openai", ActiveSource: "none", AuthModes: []string{"apiKey"}},
		{Name: "anthropic", ProviderID: "anthropic", ActiveSource: "env:ANTHROPIC_API_KEY", AuthModes: []string{"apiKey"}},
	}}})
	got := updated.(CredentialsPanel).View()
	plain := ansiPattern.ReplaceAllString(got, "")

	// Type group headers appear as lines that contain the type name but no
	// badge dot (●). Instance rows always include a StatusBadge which emits
	// "●", so a line with the type name and no "●" uniquely identifies a
	// rendered header row.
	hasHeaderLine := func(typeName string) bool {
		for line := range strings.SplitSeq(plain, "\n") {
			if strings.Contains(line, typeName) && !strings.Contains(line, "●") {
				return true
			}
		}
		return false
	}
	if !hasHeaderLine("openai") {
		t.Errorf("view should show openai type-group header line:\n%s", plain)
	}
	if !hasHeaderLine("anthropic") {
		t.Errorf("view should show anthropic type-group header line:\n%s", plain)
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
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey", "oauth"}},
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
		{Name: "anthropic", ProviderID: "anthropic", ActiveSource: "env:ANTHROPIC_API_KEY", AuthModes: []string{"apiKey"}},
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
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey"}},
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
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey"}},
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
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey"}},
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
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey"}},
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
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth"},
		{Name: "openai2", ProviderID: "openai", ActiveSource: "none"},
		{Name: "anthropic", ProviderID: "anthropic", ActiveSource: "env:ANTHROPIC_API_KEY"},
	}}})
	panel := updated.(CredentialsPanel)

	// The panel groups by provider, so the rows read anthropic, then the two
	// openai instances by name. Move down from the first instance: it must
	// skip the openai group header and land on the first openai instance.
	if got := panel.selectedInstance(); got == nil || got.Name != "anthropic" {
		t.Fatalf("the first selectable row is %v, want anthropic", got)
	}
	panel2, _ := panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	p2 := panel2.(CredentialsPanel)
	inst := p2.selectedInstance()
	if inst == nil || inst.Name != "openai" {
		t.Errorf("down from anthropic should skip the openai header and land on openai, got %v", inst)
	}

	// Move down again: the second instance of the same group, no header between.
	panel3, _ := p2.Update(tea.KeyMsg{Type: tea.KeyDown})
	p3 := panel3.(CredentialsPanel)
	inst = p3.selectedInstance()
	if inst == nil || inst.Name != "openai2" {
		t.Errorf("down from openai should land on openai2, got %v", inst)
	}
}

func TestCredentialsPanel_EscCloses(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth", AuthModes: []string{"apiKey"}},
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
	if p.formBase != "openai" {
		t.Fatalf("formBase = %q, want openai", p.formBase)
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

	// Advance to field 2 (protocol) — skip it.
	panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	if p.formActiveField() != "protocol" {
		t.Fatalf("after second Enter, active field = %q, want protocol", p.formActiveField())
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
	if got.Params.Base != "openai" {
		t.Errorf("Params.Base = %q, want openai", got.Params.Base)
	}
	if got.Params.Name != "myinst" {
		t.Errorf("Params.Name = %q, want myinst", got.Params.Name)
	}
}

func TestCredentialsPanel_InstanceListResultRefreshesPanel(t *testing.T) {
	m := NewCredentialsPanel()
	// Initial load
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth"},
	}}})
	// Second load (e.g. after a mutation)
	updated2, _ := updated.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", ProviderID: "openai", ActiveSource: "oauth"},
		{Name: "anthropic", ProviderID: "anthropic", ActiveSource: "none"},
	}}})
	view := updated2.(CredentialsPanel).View()
	if !strings.Contains(view, "anthropic") {
		t.Errorf("second InstanceListResultMsg should refresh panel; anthropic missing:\n%s", view)
	}
}

func TestCredentialsPanel_TestCredentialsActionIsPerInstanceAndRedacted(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "custom / team-east", ProviderID: "openai", ActiveSource: "none"},
	}}})

	pendingModel, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if cmd == nil {
		t.Fatal("t should produce a credential test command")
	}
	msg, ok := cmd().(CredentialsActionMsg)
	if !ok || msg.Action != "test" || msg.Instance != "custom / team-east" {
		t.Fatalf("credential test action=%T %+v", cmd(), msg)
	}
	if !strings.Contains(pendingModel.(CredentialsPanel).View(), "Testing credentials") {
		t.Fatalf("pending view should show local progress:\n%s", pendingModel.(CredentialsPanel).View())
	}
	_, duplicate := pendingModel.(CredentialsPanel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if duplicate != nil {
		t.Fatal("duplicate test should be suppressed while the instance is pending")
	}

	secret := "provider-secret"
	resultModel, _ := pendingModel.(CredentialsPanel).Update(AuthTestResultMsg{
		Provider:   "custom / team-east",
		Generation: pendingModel.(CredentialsPanel).testGeneration,
		Response:   appwire.AuthTestResponse{Provider: "custom / team-east", Status: appwire.AuthTestStatusAuthRejected, Message: secret},
	})
	view := resultModel.(CredentialsPanel).View()
	collapsed := strings.Join(strings.Fields(strings.ReplaceAll(ansiPattern.ReplaceAllString(view, ""), "│", " ")), " ")
	if !strings.Contains(collapsed, "The provider rejected these credentials. Replace the key or sign in again.") {
		t.Fatalf("result view missing safe auth message:\n%s", view)
	}
	if strings.Contains(view, secret) {
		t.Fatalf("result view leaked secret:\n%s", view)
	}
}

func TestCredentialsPanel_RendersSafeCredentialTestStatuses(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "success", status: appwire.AuthTestStatusSuccess, want: "Credentials verified."},
		{name: "missing", status: appwire.AuthTestStatusMissing, want: "No credentials are configured for this instance."},
		{name: "rejected", status: appwire.AuthTestStatusAuthRejected, want: "The provider rejected these credentials."},
		{name: "endpoint", status: appwire.AuthTestStatusEndpointFailure, want: "The provider endpoint could not be reached."},
		{name: "configuration", status: appwire.AuthTestStatusConfigurationFailure, want: "Provider configuration could not be loaded."},
		{name: "unsupported", status: appwire.AuthTestStatusUnsupported, want: "This provider does not support harmless credential verification."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewCredentialsPanel()
			updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
				{Name: "openai", ProviderID: "openai"},
			}}})
			pendingModel, _ := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
			secret := "provider-secret-" + tt.name
			resultModel, _ := pendingModel.(CredentialsPanel).Update(AuthTestResultMsg{
				Provider:   "openai",
				Generation: pendingModel.(CredentialsPanel).testGeneration,
				Response:   appwire.AuthTestResponse{Provider: "openai", Status: tt.status, Message: secret},
			})
			view := resultModel.(CredentialsPanel).View()
			collapsed := strings.Join(strings.Fields(strings.ReplaceAll(ansiPattern.ReplaceAllString(view, ""), "│", " ")), " ")
			if !strings.Contains(collapsed, tt.want) {
				t.Fatalf("result view missing safe message %q:\n%s", tt.want, view)
			}
			if strings.Contains(view, secret) {
				t.Fatalf("result view leaked secret:\n%s", view)
			}
		})
	}
}

func TestCredentialsPanel_RedactsCredentialTestRPCError(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "openai", ProviderID: "openai"},
	}}})
	pendingModel, _ := updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	secret := "provider-secret-from-rpc-error"
	resultModel, _ := pendingModel.(CredentialsPanel).Update(AuthTestResultMsg{
		Provider:   "openai",
		Generation: pendingModel.(CredentialsPanel).testGeneration,
		Err:        errors.New(secret),
	})
	view := resultModel.(CredentialsPanel).View()
	collapsed := strings.Join(strings.Fields(strings.ReplaceAll(ansiPattern.ReplaceAllString(view, ""), "│", " ")), " ")
	if !strings.Contains(collapsed, "The provider endpoint could not be reached.") {
		t.Fatalf("RPC error should render fixed endpoint message:\n%s", view)
	}
	if strings.Contains(view, secret) {
		t.Fatalf("RPC error leaked secret:\n%s", view)
	}
}

// instanceRow returns the one rendered row naming `name`, styling intact so a
// caller can compare whole badges - tone included - rather than bare words.
func instanceRow(t *testing.T, view, name string) string {
	t.Helper()
	found := ""
	for line := range strings.SplitSeq(view, "\n") {
		if !strings.Contains(line, name) {
			continue
		}
		if found != "" {
			t.Fatalf("%q names more than one rendered row, so this test cannot tell which badge is whose:\n%s", name, ansiPattern.ReplaceAllString(view, ""))
		}
		found = line
	}
	if found == "" {
		t.Fatalf("no rendered row names %q:\n%s", name, ansiPattern.ReplaceAllString(view, ""))
	}
	return found
}

// A gateway that inherits no type-level key holds no credential and needs none:
// the hub says so on the wire (InstanceEntry.CredentialRequired), and the web
// pane calls it optional rather than unconfigured. The badge is that same claim
// in one word, so a working local gateway must not wear the ended-tone ABSENT
// badge that belongs to a provider whose key is genuinely missing.
func TestCredentialsPanel_KeylessGatewayBadgeReadsOptional(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "llama", ProviderID: "openai", BaseURL: "http://localhost:8080", ActiveSource: "none", CredentialRequired: false, AuthModes: []string{"apiKey"}},
		{Name: "claude", ProviderID: "anthropic", ActiveSource: "none", CredentialRequired: true, AuthModes: []string{"apiKey"}},
	}}})
	view := updated.(CredentialsPanel).View()

	keyless := instanceRow(t, view, "llama")
	if !strings.Contains(keyless, tuiprim.StatusBadge(th.TextDim, "optional")) {
		t.Errorf("the keyless gateway's badge is not the neutral OPTIONAL one:\n%s", ansiPattern.ReplaceAllString(keyless, ""))
	}
	if strings.Contains(keyless, tuiprim.StatusBadge(th.StateEnded, "none")) {
		t.Errorf("the keyless gateway wears the ended-tone NONE badge, which is the badge of a provider missing its key:\n%s", ansiPattern.ReplaceAllString(keyless, ""))
	}

	missing := instanceRow(t, view, "claude")
	if !strings.Contains(missing, tuiprim.StatusBadge(th.StateEnded, "none")) {
		t.Errorf("a provider whose required key is missing must keep the ended-tone NONE badge:\n%s", ansiPattern.ReplaceAllString(missing, ""))
	}
}

func TestCredentialsPanel_RefreshResetsAndRejectsLateCredentialResult(t *testing.T) {
	m := NewCredentialsPanel()
	loaded, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "custom", ProviderID: "openai", BaseURL: "https://old.example/v1"},
	}}})
	pending, actionCmd := loaded.(CredentialsPanel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if actionCmd == nil {
		t.Fatal("t should produce a credential test action")
	}

	refreshed, _ := pending.(CredentialsPanel).Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "custom", ProviderID: "openai", BaseURL: "https://new.example/v1"},
	}}})
	view := refreshed.(CredentialsPanel).View()
	if strings.Contains(view, "Testing credentials") || strings.Contains(view, "Credentials verified") {
		t.Fatalf("refresh should clear old verification state:\n%s", view)
	}

	late := refreshed.(CredentialsPanel)
	lateModel, _ := late.Update(AuthTestResultMsg{
		Provider:   "custom",
		Generation: pending.(CredentialsPanel).testGeneration,
		Response:   appwire.AuthTestResponse{Provider: "custom", Status: appwire.AuthTestStatusSuccess, Message: "Credentials verified."},
	})
	if strings.Contains(lateModel.(CredentialsPanel).View(), "Credentials verified") {
		t.Fatal("late result attached to refreshed same-name instance")
	}
}

// TestCredentialsPanelSourceBadgeTones (kata gk5r) pins the panel's whole
// source-tone vocabulary in one place.
//
// The panel spends three tones: StateIdle marks a credential that resolved,
// StateEnded marks one that is missing, and TextDim is chrome — provider
// headings, hints, and the "optional" badge. Every registry source but "none"
// names a resolved credential (spec §10), so all of them wear the configured
// tone; "none" alone is the state where nothing resolved, and whether that is
// a problem depends on the instance, which is credentialBadge's call.
func TestCredentialsPanelSourceBadgeTones(t *testing.T) {
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()
	p := CredentialsPanel{}

	for _, tc := range []struct {
		source string
		want   lipgloss.Color
		why    string
	}{
		{"oauth", th.StateIdle, "a signed-in OAuth credential is configured"},
		{"env:ANTHROPIC_API_KEY", th.StateIdle, "a credential from the environment is configured"},
		{"store", th.StateIdle, "a stored API key is configured"},
		{"api_key", th.StateIdle, "an authored api_key is configured"},
		{"credential_headers", th.StateIdle, "an authored credential header is configured"},
		{"adc", th.StateIdle, "application-default credentials are configured"},
		{"none", th.TextDim, "nothing resolved; the row's own badge says whether that is a problem"},
		{"", th.TextDim, "an unset source reports no state"},
	} {
		if got := p.sourceBadgeColor(tc.source); got != tc.want {
			t.Errorf("sourceBadgeColor(%q) = %v, want %v — %s", tc.source, got, tc.want, tc.why)
		}
	}

	// The configured sources must be told apart from the chrome tone at all,
	// which is the whole point of the mapping above.
	if th.StateIdle == th.TextDim {
		t.Fatal("StateIdle and TextDim are the same color in this theme, so the assertions above prove nothing")
	}

	// A required credential that did not resolve is the ended tone, and the
	// same "none" source on an instance that needs nothing is not.
	missing := p.credentialBadge(appwire.InstanceEntry{Name: "claude", ActiveSource: "none", CredentialRequired: true})
	if !strings.Contains(missing, tuiprim.StatusBadge(th.StateEnded, "none")) {
		t.Errorf("a missing required credential must wear the ended tone: %q", ansiPattern.ReplaceAllString(missing, ""))
	}
	optional := p.credentialBadge(appwire.InstanceEntry{Name: "llama", ActiveSource: "none"})
	if !strings.Contains(optional, tuiprim.StatusBadge(th.TextDim, "optional")) {
		t.Errorf("an instance that needs no credential must wear the optional badge: %q", ansiPattern.ReplaceAllString(optional, ""))
	}
}

// TestCredentialsPanel_GroupsEachProviderOnce: the registry ranks instances by
// default order then name, which can interleave providers, and the panel emits
// a header whenever the provider changes from one row to the next. Sorting
// first is what keeps a provider's header from appearing twice.
func TestCredentialsPanel_GroupsEachProviderOnce(t *testing.T) {
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "work", ProviderID: "anthropic", ActiveSource: "store"},
		{Name: "gpt", ProviderID: "openai", ActiveSource: "store"},
		{Name: "claude", ProviderID: "anthropic", ActiveSource: "store"},
	}}})
	panel := updated.(CredentialsPanel)

	var headers, names []string
	for _, row := range panel.rows {
		if row.header {
			headers = append(headers, row.groupName)
			continue
		}
		names = append(names, row.entry.Name)
	}
	if !reflect.DeepEqual(headers, []string{"anthropic", "openai"}) {
		t.Errorf("headers = %v, want each provider once, in order", headers)
	}
	if !reflect.DeepEqual(names, []string{"claude", "work", "gpt"}) {
		t.Errorf("rows = %v, want each provider's instances together, by name", names)
	}
}

// TestCredentialsPanelEditClearsAnAuthoredBaseURL: evener/instance/edit now
// has an additive ClearBaseURL flag (#711) alongside BaseURL's unchanged
// "empty means unchanged" (v3), so emptying a previously set field and
// submitting sends an explicit clear instead of being refused.
func TestCredentialsPanelEditClearsAnAuthoredBaseURL(t *testing.T) {
	withTestColorProfile(t)
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "work", ProviderID: "anthropic", Protocol: "anthropic", BaseURL: "https://existing/v1", CredentialRequired: true, AuthModes: []string{"apiKey"}},
	}}})
	p := updated.(CredentialsPanel)

	// Open the edit form on the selected instance.
	panel, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	p = panel.(CredentialsPanel)
	if p.formBaseURL != "https://existing/v1" {
		t.Fatalf("formBaseURL = %q, want the instance's authored URL", p.formBaseURL)
	}

	// Move to the Base URL field and erase it.
	panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	if p.formActiveField() != "baseURL" {
		t.Fatalf("active field = %q, want baseURL", p.formActiveField())
	}
	for range len("https://existing/v1") {
		panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		p = panel.(CredentialsPanel)
	}

	if note := flattenPanelText(p.View()); !strings.Contains(note, "resets the endpoint to the provider's default") {
		t.Fatalf("the form does not say emptying resets to the default endpoint:\n%s", p.View())
	}

	panel, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	if cmd == nil {
		t.Fatal("the cleared field was not submitted")
	}
	msg, ok := cmd().(InstanceEditSubmitMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want InstanceEditSubmitMsg", cmd())
	}
	if msg.Params.Name != "work" || msg.Params.BaseURL != "" || !msg.Params.ClearBaseURL {
		t.Fatalf("params = %+v, want ClearBaseURL true and BaseURL empty", msg.Params)
	}
	if p.formOpen {
		t.Fatal("the form stayed open after a submit it accepted")
	}
}

// TestCredentialsPanelEditAllowsAnEmptyBaseURLWhenNoneWasSet: an instance with
// no base_url of its own has nothing to clear, so the empty field is honest
// and submitting it untouched sends neither BaseURL nor ClearBaseURL.
func TestCredentialsPanelEditAllowsAnEmptyBaseURLWhenNoneWasSet(t *testing.T) {
	withTestColorProfile(t)
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "work", ProviderID: "anthropic", Protocol: "anthropic", CredentialRequired: true, AuthModes: []string{"apiKey"}},
	}}})
	p := updated.(CredentialsPanel)
	panel, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	p = panel.(CredentialsPanel)
	panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)

	if note := flattenPanelText(p.View()); strings.Contains(note, "resets the endpoint to the provider's default") {
		t.Fatalf("nothing was cleared, so the form must not note a reset:\n%s", p.View())
	}
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("the edit was not submitted")
	}
	msg, ok := cmd().(InstanceEditSubmitMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want InstanceEditSubmitMsg", cmd())
	}
	if msg.Params.Name != "work" || msg.Params.BaseURL != "" || msg.Params.ClearBaseURL {
		t.Fatalf("params = %+v, want neither BaseURL nor ClearBaseURL", msg.Params)
	}
}

// TestCredentialsPanelEditOfAnImplicitInstanceLeavesBaseURLUnchanged is
// #711 (roborev): the edit form pre-fills formBaseURL with the instance's
// displayed base URL, which for an implicit instance is its resolved
// registry default, not an authored override. A save that never touches
// the field must send neither BaseURL nor ClearBaseURL — sending the
// displayed default back as a literal BaseURL would author it and stop
// spec §10's credential inheritance on an instance the user only meant to
// leave alone.
func TestCredentialsPanelEditOfAnImplicitInstanceLeavesBaseURLUnchanged(t *testing.T) {
	withTestColorProfile(t)
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "groq", ProviderID: "groq", Protocol: "openai-chat", BaseURL: "https://api.groq.com/openai/v1", Implicit: true, CredentialRequired: true, AuthModes: []string{"apiKey"}},
	}}})
	p := updated.(CredentialsPanel)

	panel, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	p = panel.(CredentialsPanel)
	if p.formBaseURL != "https://api.groq.com/openai/v1" {
		t.Fatalf("formBaseURL = %q, want the implicit instance's resolved default", p.formBaseURL)
	}

	// Advance past protocol to the baseURL field and submit without typing
	// anything into either field.
	panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	panel, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	if cmd == nil {
		t.Fatal("the edit was not submitted")
	}
	msg, ok := cmd().(InstanceEditSubmitMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want InstanceEditSubmitMsg", cmd())
	}
	if msg.Params.BaseURL != "" || msg.Params.ClearBaseURL {
		t.Fatalf("params = %+v, want neither BaseURL nor ClearBaseURL: saving without touching the field must not author the displayed default as a literal override", msg.Params)
	}
}

// TestCredentialsPanelEditOfAHiddenInstanceLeavesBaseURLUnchanged is #711
// (roborev): a hidden or otherwise unresolvable authored instance can
// display this field empty while a real base_url is still authored
// underneath (InstanceEntry.Hidden: "no resolvable base URL in this
// environment"). A save that never touches the field must not read that
// display quirk as a deliberate clear.
func TestCredentialsPanelEditOfAHiddenInstanceLeavesBaseURLUnchanged(t *testing.T) {
	withTestColorProfile(t)
	m := NewCredentialsPanel()
	updated, _ := m.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "work", ProviderID: "anthropic", Protocol: "anthropic", BaseURL: "", Hidden: true, CredentialRequired: true, AuthModes: []string{"apiKey"}},
	}}})
	p := updated.(CredentialsPanel)

	panel, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	p = panel.(CredentialsPanel)
	if p.formBaseURL != "" {
		t.Fatalf("formBaseURL = %q, want empty: a hidden instance's real base_url does not display", p.formBaseURL)
	}

	panel, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	panel, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = panel.(CredentialsPanel)
	if cmd == nil {
		t.Fatal("the edit was not submitted")
	}
	msg, ok := cmd().(InstanceEditSubmitMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want InstanceEditSubmitMsg", cmd())
	}
	if msg.Params.BaseURL != "" || msg.Params.ClearBaseURL {
		t.Fatalf("params = %+v, want neither BaseURL nor ClearBaseURL: an untouched empty display must not silently clear a real authored base_url the pane could not show", msg.Params)
	}
}

// flattenPanelText strips styling and the overlay's box rules, then collapses
// whitespace, so an assertion on a sentence does not depend on where the
// panel wrapped it.
func flattenPanelText(view string) string {
	plain := ansiPattern.ReplaceAllString(view, "")
	plain = strings.Map(func(r rune) rune {
		if strings.ContainsRune("│─╭╮╰╯", r) {
			return ' '
		}
		return r
	}, plain)
	return strings.Join(strings.Fields(plain), " ")
}
