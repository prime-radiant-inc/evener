package launchconfig

import (
	"reflect"
	"strings"
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
	for range 30 {
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
	// Build the oracle after Update so neither the top-level slice nor the
	// nested DefaultableLayers slices share backing storage with the input.
	want := testLaunchSchema()
	if !reflect.DeepEqual(m2.schema, want) {
		t.Fatalf("schema = %+v, want %+v", m2.schema, want)
	}
}

// --- Update: LaunchSchemaResultMsg error ---

func TestCovLaunchOverridesSchemaResultError(t *testing.T) {
	seed := []appwire.LaunchOption{{Field: "existing", Label: "Existing", Kind: "text", DefaultableLayers: []string{"global"}}}
	m := NewLaunchOverridesModalWithSchema(appwire.LaunchConfigLayer{}, seed)
	updated, _ := m.Update(LaunchSchemaResultMsg{Err: errOOM})
	m2 := updated.(LaunchOverridesModal)
	if len(m2.schema) != 1 || m2.schema[0].Field != "existing" || m2.schema[0].Label != "Existing" || m2.schema[0].Kind != "text" ||
		len(m2.schema[0].DefaultableLayers) != 1 || m2.schema[0].DefaultableLayers[0] != "global" {
		t.Fatalf("schema error replaced existing schema with %+v", m2.schema)
	}
}

// --- Update: unknown message ---

func TestCovLaunchOverridesUnknownMsg(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{Model: "keep"})
	m.cursor = 3
	updated, cmd := m.Update("unknown")
	if cmd != nil {
		t.Fatal("unknown msg should return nil cmd")
	}
	got := updated.(LaunchOverridesModal)
	if got.cursor != 3 || got.cur.Model != "keep" || got.done || got.cancelled {
		t.Fatalf("unknown msg changed modal to %+v", got)
	}
}

// --- ApplyEdit error ---

func TestCovLaunchOverridesApplyEditError(t *testing.T) {
	m := NewLaunchOverridesModalWith(appwire.LaunchConfigLayer{})
	_, err := m.ApplyEdit("nonexistent", "x")
	if err == nil || err.Error() != `editing "nonexistent" in TUI not yet supported; use the web UI` {
		t.Fatalf("ApplyEdit error = %v", err)
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
	if !strings.Contains(v, "Launch overrides") || !strings.Contains(v, "x") {
		t.Fatalf("View did not render title and model override: %q", v)
	}
}

var errOOM = errorNew("out of memory")

func errorNew(s string) error { return &strError{s} }

type strError struct{ s string }

func (e *strError) Error() string { return e.s }
