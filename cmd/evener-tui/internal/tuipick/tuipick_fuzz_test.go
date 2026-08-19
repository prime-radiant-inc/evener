package tuipick

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

var fuzzCoverageUnion = func(*testing.T) {}

func FuzzPickers(f *testing.F) {
	for action := range byte(7) {
		f.Add("a", action)
	}
	f.Add("missing", byte(4))
	f.Fuzz(func(t *testing.T, filter string, action byte) {
		fuzzCoverageUnion(t)
		items := make([]ModelPickerItem, 16)
		panels := make([]PickerPanelItem, 16)
		for i := range items {
			items[i] = ModelPickerItem{ID: string(rune('a' + i)), Display: "item", Group: "group", Meta: "meta"}
			panels[i] = PickerPanelItem{ID: items[i].ID, Label: "item", Detail: "detail"}
		}
		items[0].DisabledReason = "disabled"
		panels[0].DisabledReason = "disabled"
		keyTypes := []tea.KeyType{tea.KeyRunes, tea.KeyBackspace, tea.KeyUp, tea.KeyDown, tea.KeyEnter, tea.KeyEscape, tea.KeyCtrlC}
		key := tea.KeyMsg{Type: keyTypes[int(action)%len(keyTypes)], Runes: []rune(filter)}

		model := NewModelPicker(items, "b", 0)
		_ = model.Init()
		model.filter = filter
		model.cursor = 15
		updated, _ := model.Update(key)
		model = updated.(ModelPicker)
		_ = model.View()

		panel := NewPickerPanel("", panels, 30)
		_ = panel.Init()
		panel.filter = filter
		panel.cursor = 15
		updated, _ = panel.Update(key)
		panel = updated.(PickerPanel)
		_ = panel.View()
	})
}

func TestPickerRenderBoundaryStates(t *testing.T) {
	items := make([]ModelPickerItem, 16)
	for i := range items {
		items[i] = ModelPickerItem{ID: string(rune('a' + i)), Display: "item", Group: "group", Meta: "meta"}
	}
	m := NewModelPicker(items, "", 40)
	m.title = ""
	m.emptyText = ""
	m.cursor = 15
	_ = m.View()
	m.cursor = 0
	_ = m.View()
	m.filter = "missing"
	_ = m.View()

	p := NewPickerPanel("", []PickerPanelItem{{ID: "a", Label: "a", DisabledReason: "no"}}, 40)
	p.cursor = 1
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p.cursor = 1
	p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p.filter = "x"
	p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	theme := NewThemePicker()
	theme.cursor = 1
	theme.Update(tea.KeyMsg{Type: tea.KeyUp})
}

func TestTextInputModalBoundaryStates(t *testing.T) {
	m := NewPathTextInputModal("prompt", "tag", "x")
	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	_ = m.View()
	m = NewTextInputModalWithTitle("title", "prompt", "tag")
	m.width = 0
	_ = m.View()
}

func TestCompleteLastPathSegmentNormalization(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, input := range []string{"", "~", "relative"} {
		_ = CompleteLastPathSegment(input, nil)
	}
}

func FuzzTextInputModal(f *testing.F) {
	f.Add("value", byte(0), false)
	f.Add("", byte(5), true)
	f.Fuzz(func(t *testing.T, input string, action byte, titled bool) {
		var modal TextInputModal
		if titled {
			modal = NewTextInputModalWithTitle("Title", "Prompt", "tag")
			modal.paths = true
		} else {
			modal = NewTextInputModalWithInput("Prompt", "tag", input)
		}
		_ = modal.Init()
		keys := []tea.KeyType{tea.KeyRunes, tea.KeyBackspace, tea.KeyTab, tea.KeyEnter, tea.KeyEscape, tea.KeyCtrlC}
		updated, cmd := modal.Update(tea.KeyMsg{Type: keys[int(action)%len(keys)], Runes: []rune(input)})
		modal = updated.(TextInputModal)
		_ = modal.View()
		if cmd != nil {
			if _, ok := cmd().(TextInputResultMsg); !ok {
				t.Fatal("completion command returned wrong message type")
			}
		}
	})
}

func FuzzCompleteLastPathSegment(f *testing.F) {
	f.Add("ap", true)
	f.Add("missing", false)
	f.Fuzz(func(t *testing.T, segment string, directoriesOnly bool) {
		segment = filepath.Base(strings.ReplaceAll(segment, "\x00", ""))
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "apple"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "apricot"), 0o700); err != nil {
			t.Fatal(err)
		}
		input := filepath.Join(root, segment)
		var accept func(os.DirEntry) bool
		if directoriesOnly {
			accept = DirEntry()
		}
		got := CompleteLastPathSegment(input, accept)
		if !strings.HasPrefix(got, root) {
			t.Fatalf("completion escaped temp root: %q", got)
		}
	})
}
