package launchconfig

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

// --- Init ---

func TestCovLaunchOverridesInit(t *testing.T) {
	m := NewLaunchOverridesModal()
	if m.Init() != nil {
		t.Fatal("Init should return nil")
	}
}

// --- Done ---

func TestCovLaunchOverridesDone(t *testing.T) {
	m := LaunchOverridesModal{done: true}
	if !m.Done() {
		t.Fatal("Done should be true")
	}
	m2 := NewLaunchOverridesModal()
	if m2.Done() {
		t.Fatal("Done should be false on new modal")
	}
}

// --- Update: CtrlC ---

func TestCovLaunchOverridesCtrlC(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m2 := updated.(LaunchOverridesModal)
	if !m2.done || !m2.cancelled {
		t.Fatal("CtrlC should set done and cancelled")
	}
	if cmd == nil {
		t.Fatal("CtrlC should produce a cmd")
	}
	res := cmd().(LaunchOverridesResultMsg)
	if !res.Cancelled {
		t.Fatal("result should be cancelled")
	}
}

// --- Update: Down navigation ---

func TestCovLaunchOverridesDown(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m2 := updated.(LaunchOverridesModal)
	// cursor should move down (layerRows has many rows)
	if m2.cursor != 1 {
		t.Fatalf("Down should move cursor to 1, got %d", m2.cursor)
	}
}

// --- Update: Down at bottom is no-op ---

func TestCovLaunchOverridesDownAtBottom(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	// Move cursor way down
	for i := 0; i < 30; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(LaunchOverridesModal)
	}
	// cursor should be clamped at len(rows)-1
	rows := m.rows()
	if m.cursor != len(rows)-1 {
		t.Fatalf("cursor = %d, want %d", m.cursor, len(rows)-1)
	}
}

// --- Update: Up ---

func TestCovLaunchOverridesUp(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	// Move down first
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m2 := updated.(LaunchOverridesModal)
	// Move up
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyUp})
	m3 := updated.(LaunchOverridesModal)
	if m3.cursor != 0 {
		t.Fatalf("Up should move cursor to 0, got %d", m3.cursor)
	}
}

// --- Update: Up at top is no-op ---

func TestCovLaunchOverridesUpAtTop(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m2 := updated.(LaunchOverridesModal)
	if m2.cursor != 0 {
		t.Fatalf("Up at top should stay 0, got %d", m2.cursor)
	}
}

// --- Update: Enter on read-only field ---

func TestCovLaunchOverridesEnterReadOnly(t *testing.T) {
	orig := launchSettingsFieldReadOnly
	launchSettingsFieldReadOnly = func(field string) bool { return field == "model" }
	defer func() { launchSettingsFieldReadOnly = orig }()

	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter on read-only field should return nil cmd")
	}
}

// --- Update: Enter with cursor out of range ---

func TestCovLaunchOverridesEnterOutOfRange(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	m.cursor = 999
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter with cursor out of range should return nil cmd")
	}
}

// --- Update: LaunchSchemaResultMsg ---

func TestCovLaunchOverridesSchemaResult(t *testing.T) {
	m := NewLaunchOverridesModal()
	updated, _ := m.Update(LaunchSchemaResultMsg{Schema: appwire.LaunchOptionSchemaResponse{Options: testLaunchSchema()}})
	m2 := updated.(LaunchOverridesModal)
	if len(m2.schema) == 0 {
		t.Fatal("schema should be set")
	}
}

// --- Update: LaunchSchemaResultMsg error ---

func TestCovLaunchOverridesSchemaResultError(t *testing.T) {
	m := NewLaunchOverridesModal()
	updated, _ := m.Update(LaunchSchemaResultMsg{Err: errOOM})
	m2 := updated.(LaunchOverridesModal)
	if len(m2.schema) != 0 {
		t.Fatal("schema should stay empty on error")
	}
}

// --- Update: unknown message ---

func TestCovLaunchOverridesUnknownMsg(t *testing.T) {
	m := NewLaunchOverridesModal()
	updated, cmd := m.Update("unknown")
	if cmd != nil {
		t.Fatal("unknown msg should return nil cmd")
	}
	_ = updated
}

// --- ApplyEdit error ---

func TestCovLaunchOverridesApplyEditError(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	_, err := m.ApplyEdit("nonexistent", "x")
	if err == nil {
		t.Fatal("unknown field should return error")
	}
}

// --- Current ---

func TestCovLaunchOverridesCurrent(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{Model: "x"})
	cur := m.Current()
	if cur.Model != "x" {
		t.Fatalf("Current model = %q", cur.Model)
	}
}

// --- View ---

func TestCovLaunchOverridesView(t *testing.T) {
	withTestColorProfile(t)
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{Model: "x"})
	v := m.View()
	if v == "" {
		t.Fatal("View should not be empty")
	}
}

var errOOM = errorNew("out of memory")

func errorNew(s string) error { return &strErr{s} }

type strErr struct{ s string }

func (e *strErr) Error() string { return e.s }
