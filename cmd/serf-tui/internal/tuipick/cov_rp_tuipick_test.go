package tuipick

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func TestThemePicker_UpdateSelectAndCancel(t *testing.T) {
	tuitheme.SetTheme("dark")

	// Enter selects the item under the cursor and marks the picker done.
	p := NewThemePicker()
	moved, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	moved, _ = moved.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !moved.Done() {
		t.Fatal("expected Done after Enter")
	}
	if moved.Selected() == "" {
		t.Fatal("expected a non-empty Selected after Enter")
	}
	if moved.Selected() != ThemePickerItems[moved.cursor] {
		t.Fatalf("Selected = %q, want %q", moved.Selected(), ThemePickerItems[moved.cursor])
	}

	// Escape cancels: done but no selection.
	c := NewThemePicker()
	cancelled, _ := c.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if !cancelled.Done() {
		t.Fatal("expected Done after Escape")
	}
	if cancelled.Selected() != "" {
		t.Fatalf("Selected after cancel = %q, want empty", cancelled.Selected())
	}
}

func TestThemePicker_UpdateCursorBounds(t *testing.T) {
	p := NewThemePicker()
	// Up at the top row is a no-op.
	p.cursor = 0
	up, _ := p.Update(tea.KeyMsg{Type: tea.KeyUp})
	if up.cursor != 0 {
		t.Fatalf("cursor after Up at top = %d, want 0", up.cursor)
	}
	// Down past the last row is clamped.
	p.cursor = len(ThemePickerItems) - 1
	down, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	if down.cursor != len(ThemePickerItems)-1 {
		t.Fatalf("cursor after Down at bottom = %d, want %d", down.cursor, len(ThemePickerItems)-1)
	}
}

func TestTextInputModalConstructors(t *testing.T) {
	withInput := NewTextInputModalWithInput("prompt", "tag", "seed")
	if withInput.input != "seed" || withInput.mask || withInput.paths {
		t.Fatalf("NewTextInputModalWithInput = %+v", withInput)
	}

	pathModal := NewPathTextInputModal("prompt", "tag", "/etc")
	if !pathModal.paths || pathModal.input != "/etc" {
		t.Fatalf("NewPathTextInputModal = %+v", pathModal)
	}

	masked := NewTextInputModalMasked("prompt", "tag")
	if !masked.mask {
		t.Fatalf("NewTextInputModalMasked should set mask: %+v", masked)
	}
	// A masked input view renders bullets, never the raw text.
	masked.input = "secret"
	view := masked.inputView()
	if strings.Contains(view, "secret") {
		t.Fatalf("masked inputView leaked the raw value: %q", view)
	}
	if !strings.Contains(view, "•") {
		t.Fatalf("masked inputView should render bullets: %q", view)
	}
	// A freshly-constructed modal is not yet done.
	if masked.Done() {
		t.Fatal("a new modal should not report Done")
	}
}
