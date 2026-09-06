package tuipick

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTextInputModalUsesOverlay(t *testing.T) {
	withTestColorProfile(t)
	m := NewTextInputModalWithTitle("Set OpenAI API key", "Paste the key:", "")
	got := m.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "╭") {
		t.Errorf("text input modal should use Overlay primitive: %q", plain)
	}
	if !strings.Contains(plain, "Set OpenAI API key") {
		t.Errorf("text input modal should show title: %q", plain)
	}
}

func TestTextInputModal_CapturesAndSubmits(t *testing.T) {
	m := NewTextInputModal("API key for anthropic", "")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-ant-X")})
	m = updated.(TextInputModal)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should produce a cmd")
	}
	msg := cmd()
	res, ok := msg.(TextInputResultMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want TextInputResultMsg", msg)
	}
	if res.Cancelled {
		t.Errorf("should not be cancelled")
	}
	if res.Value != "sk-ant-X" {
		t.Errorf("Value = %q", res.Value)
	}
}

// TestCredentialPasteModal_TakesAWholeBracketedPaste: a terminal sends a
// bracketed paste as ONE KeyRunes message carrying every rune, newlines
// included (bubbletea's detectBracketedPaste), so a pretty-printed credential
// JSON arrives whole and its newlines never reach the Enter branch.
func TestCredentialPasteModal_TakesAWholeBracketedPaste(t *testing.T) {
	const json = "{\n  \"type\": \"authorized_user\",\n  \"client_id\": \"a\"\n}"
	m := NewCredentialPasteModal("Credential JSON", "Paste it:", "credential-json-set:vertex")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(json), Paste: true})
	if cmd != nil {
		t.Fatalf("a paste must not submit or cancel; got %+v", cmd())
	}
	_, cmd = updated.(TextInputModal).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter after the paste should submit")
	}
	res, ok := cmd().(TextInputResultMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want TextInputResultMsg", cmd())
	}
	if res.Value != json || res.Cancelled {
		t.Fatalf("Value = %q cancelled=%t, want the paste verbatim", res.Value, res.Cancelled)
	}
}

// TestCredentialPasteModal_SummarizesASecretAndShowsAPath: the field never
// echoes a pasted credential, but a typed path stays visible so it can be
// read and Tab-completed.
func TestCredentialPasteModal_SummarizesASecretAndShowsAPath(t *testing.T) {
	withTestColorProfile(t)
	const json = "{\n  \"type\": \"service_account\",\n  \"private_key\": \"SECRET-MATERIAL\"\n}"
	m := NewCredentialPasteModal("Credential JSON", "Paste it:", "credential-json-set:vertex")
	pasted, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(json), Paste: true})
	view := ansiPattern.ReplaceAllString(pasted.(TextInputModal).View(), "")
	if strings.Contains(view, "SECRET-MATERIAL") || strings.Contains(view, "service_account") {
		t.Fatalf("the pasted credential must never be echoed: %q", view)
	}
	if !strings.Contains(view, "characters") {
		t.Fatalf("a pasted credential should render as a character count: %q", view)
	}
	if strings.Count(view, "\n") > strings.Count(ansiPattern.ReplaceAllString(m.View(), ""), "\n") {
		t.Fatalf("a multi-line paste must not grow the overlay: %q", view)
	}

	const path = "~/.config/gcloud/application_default_credentials.json"
	typed, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path)})
	if got := ansiPattern.ReplaceAllString(typed.(TextInputModal).View(), ""); !strings.Contains(got, path) {
		t.Fatalf("a typed path must stay visible for editing and completion: %q", got)
	}
}

func TestTextInputModal_EscapeCancels(t *testing.T) {
	m := NewTextInputModal("x", "")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	msg := cmd()
	res := msg.(TextInputResultMsg)
	if !res.Cancelled {
		t.Errorf("Esc should cancel")
	}
}
