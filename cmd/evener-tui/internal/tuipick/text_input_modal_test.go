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

// TestCredentialPasteModal_SummarizesEveryCredentialAndShowsLongPaths: what
// must never be echoed is a credential document, and every document the hub
// accepts is a JSON object — so the rule keys on that, not on a length whose
// margin over the shortest acceptable document was two runes. A path stays
// visible however long it is.
func TestCredentialPasteModal_SummarizesEveryCredentialAndShowsLongPaths(t *testing.T) {
	withTestColorProfile(t)
	// The shortest document CheckCredentialJSON accepts: an allowed type plus
	// its three required fields at one character each.
	const shortest = `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`
	m := NewCredentialPasteModal("Credential JSON", "Paste it:", "t")
	pasted, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(shortest), Paste: true})
	if view := ansiPattern.ReplaceAllString(pasted.(TextInputModal).View(), ""); strings.Contains(view, "authorized_user") {
		t.Fatalf("the shortest acceptable credential must still be summarized: %q", view)
	}
	// Longer than any threshold a length rule would use, and still a path.
	// The overlay wraps it, so the tail is what proves it was not summarized.
	const longPath = "/Users/somebody/Library/Application Support/gcloud/application_default_credentials.json"
	typed, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(longPath)})
	view := ansiPattern.ReplaceAllString(typed.(TextInputModal).View(), "")
	if strings.Contains(view, "characters pasted") || !strings.Contains(view, "application_default_credentials.json") {
		t.Fatalf("a long path must stay visible: %q", view)
	}
}

// TestCredentialPasteModal_SummarizesAnythingPasted: the shape rule keeps a
// typed path visible, and the terminal already says which input was a paste —
// so a secret pasted into this prompt by mistake, whatever its shape or
// length, is summarized too.
func TestCredentialPasteModal_SummarizesAnythingPasted(t *testing.T) {
	withTestColorProfile(t)
	const wrongSecret = "sk-ant-api03-SECRET-MATERIAL-pasted-into-the-credential-json-prompt-by-mistake"
	m := NewCredentialPasteModal("Credential JSON", "Paste it:", "t")
	pasted, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(wrongSecret), Paste: true})
	view := ansiPattern.ReplaceAllString(pasted.(TextInputModal).View(), "")
	if strings.Contains(view, "SECRET-MATERIAL") {
		t.Fatalf("anything pasted must be summarized, whatever its shape: %q", view)
	}
	// A paste stays summarized as the user edits around it.
	trimmed, _ := pasted.(TextInputModal).Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if view := ansiPattern.ReplaceAllString(trimmed.(TextInputModal).View(), ""); strings.Contains(view, "SECRET-MATERIAL") {
		t.Fatalf("editing a pasted value must not reveal it: %q", view)
	}
}

// TestCredentialPasteModal_ShowsOnlyWhatLooksLikeAPath: a terminal without
// bracketed paste marks nothing as pasted, so the field cannot tell a pasted
// secret from a typed one. It shows what looks like a path and summarizes
// everything else, which is the side to err on in a credential prompt.
func TestCredentialPasteModal_ShowsOnlyWhatLooksLikeAPath(t *testing.T) {
	withTestColorProfile(t)
	m := NewCredentialPasteModal("Credential JSON", "Paste it:", "t")
	for _, tt := range []struct {
		name, typed string
		shown       bool
	}{
		{name: "absolute path", typed: "/home/somebody/adc.json", shown: true},
		{name: "home path", typed: "~/.config/gcloud/adc.json", shown: true},
		{name: "relative path", typed: "./adc.json", shown: true},
		{name: "unmarked secret", typed: "sk-ant-api03-SECRET-MATERIAL-typed-without-a-paste-mark", shown: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			typed, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.typed)})
			view := ansiPattern.ReplaceAllString(typed.(TextInputModal).View(), "")
			// The summary marker is the reliable signal: a long value is
			// wrapped by the overlay, so searching for it whole can miss it.
			if summarized := strings.Contains(view, "characters pasted"); summarized == tt.shown {
				t.Fatalf("summarized = %t, want shown = %t for %q: %q", summarized, tt.shown, tt.typed, view)
			}
			if tt.shown && !strings.Contains(view, tt.typed) {
				t.Fatalf("a path short enough to fit must render whole: %q", view)
			}
		})
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
