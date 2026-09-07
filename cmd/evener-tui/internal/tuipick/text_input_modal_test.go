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

// TestTextInputModal_CapturesSpaces: bubbletea delivers a space as KeySpace,
// not KeyRunes, so a field that ignores it cannot hold a path like
// "~/Google Drive/adc.json" — or any typed value with a space in it.
func TestTextInputModal_CapturesSpaces(t *testing.T) {
	m := NewTextInputModal("path:", "")
	var updated tea.Model = m
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("~/Google")},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune("Drive/adc.json")},
	} {
		updated, _ = updated.(TextInputModal).Update(key)
	}
	_, cmd := updated.(TextInputModal).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := cmd().(TextInputResultMsg).Value; got != "~/Google Drive/adc.json" {
		t.Fatalf("Value = %q, want the space kept", got)
	}
}

// TestCredentialPasteModal_AccumulatesAnUnbracketedLineFeedPaste: a terminal
// that does not bracket its pastes sends the document as ordinary keys, and
// bubbletea maps LF to KeyCtrlJ (only CR is KeyEnter). The credential field
// keeps those newlines and their spaces, so such a paste still arrives whole
// and only the user's own Enter submits it.
func TestCredentialPasteModal_AccumulatesAnUnbracketedLineFeedPaste(t *testing.T) {
	const document = "{\n  \"type\": \"authorized_user\",\n  \"client_id\": \"a\"\n}"
	m := NewCredentialPasteModal("Credential JSON", "Paste it:", "credential-json-set:vertex")
	var updated tea.Model = m
	for _, r := range document {
		var key tea.KeyMsg
		switch r {
		case '\n':
			key = tea.KeyMsg{Type: tea.KeyCtrlJ}
		case ' ':
			key = tea.KeyMsg{Type: tea.KeySpace}
		default:
			key = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		}
		var cmd tea.Cmd
		updated, cmd = updated.(TextInputModal).Update(key)
		if cmd != nil {
			t.Fatalf("no key of an unbracketed paste may submit; %q did", string(r))
		}
	}
	_, cmd := updated.(TextInputModal).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := cmd().(TextInputResultMsg).Value; got != document {
		t.Fatalf("Value = %q, want the document intact", got)
	}
}

// TestCredentialPasteModal_NeverEchoesWhateverItIsGiven: the prompt asks for
// one thing, a credential document, so it echoes nothing at all. Whether a
// value was pasted or typed cannot be told apart on a terminal that marks no
// pastes, and guessing from its shape is what this replaces; a path goes to
// the separate file prompt, which has nothing secret to hide.
func TestCredentialPasteModal_NeverEchoesWhateverItIsGiven(t *testing.T) {
	withTestColorProfile(t)
	m := NewCredentialPasteModal("Credential JSON", "Paste it:", "t")
	for _, tt := range []struct{ name, typed string }{
		{name: "document", typed: `{"type":"authorized_user","client_id":"a"}`},
		{name: "unmarked secret", typed: "sk-ant-api03-SECRET-MATERIAL"},
		{name: "secret shaped like a path", typed: "/9j/4AAQSkZJRgABAQAA-SECRET-MATERIAL"},
		{name: "windows-shaped secret", typed: `C:\SECRET-MATERIAL`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			typed, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.typed)})
			view := ansiPattern.ReplaceAllString(typed.(TextInputModal).View(), "")
			if !strings.Contains(view, "characters") || strings.Contains(view, "SECRET-MATERIAL") || strings.Contains(view, "authorized_user") {
				t.Fatalf("the credential prompt must echo nothing: %q", view)
			}
		})
	}
}

// TestPathTextInputModal_ShowsThePath: the file prompt takes a path, which is
// not secret, so it stays visible for editing and completion on any platform.
func TestPathTextInputModal_ShowsThePath(t *testing.T) {
	withTestColorProfile(t)
	for _, path := range []string{"~/.config/gcloud/adc.json", `C:\Users\somebody\adc.json`, `\\server\share\adc.json`} {
		m := NewPathTextInputModal("path:", "t", "")
		typed, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path)})
		if view := ansiPattern.ReplaceAllString(typed.(TextInputModal).View(), ""); !strings.Contains(view, path) {
			t.Fatalf("a typed path must stay visible: %q", view)
		}
	}
}

// TestTextInputModal_DoesNotRenderControlBytes: clipboard content reaches the
// view, so an escape sequence in it must not reach the terminal.
func TestTextInputModal_DoesNotRenderControlBytes(t *testing.T) {
	withTestColorProfile(t)
	m := NewCredentialPasteModal("Credential JSON", "Paste it:", "t")
	pasted, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("adc\x1b[2J\x00.json"), Paste: true})
	view := ansiPattern.ReplaceAllString(pasted.(TextInputModal).View(), "")
	if strings.ContainsAny(view, "\x1b\x00") {
		t.Fatalf("control bytes must not be rendered: %q", view)
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
