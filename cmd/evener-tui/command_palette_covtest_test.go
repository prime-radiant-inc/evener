package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
)

// ---- commandPalette.Init ----------------------------------------------------

func TestCovCommandPalette_InitReturnsNil(t *testing.T) {
	p := newCommandPalette("test", nil, 80)
	if cmd := p.Init(); cmd != nil {
		t.Fatalf("Init should return nil cmd")
	}
}

// ---- ViewWithMaxHeight: with filter set -------------------------------------

func TestCovViewWithMaxHeight_WithFilter(t *testing.T) {
	withTestColorProfile(t)
	p := newCommandPalette("Test", []commandPaletteEntry{
		{Item: tuipick.PickerPanelItem{ID: "a", Label: "/alpha"}},
		{Item: tuipick.PickerPanelItem{ID: "b", Label: "/beta"}},
	}, 80)
	p.panel.SetFilter("alpha")
	got := p.ViewWithMaxHeight(20)
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "Filter: alpha") || !strings.Contains(plain, "> /alpha") {
		t.Fatalf("filtered palette lost filter text or selected match:\n%s", plain)
	}
	if strings.Contains(plain, "/beta") {
		t.Fatalf("filtered palette retained non-matching command:\n%s", plain)
	}
}

func TestCovViewWithMaxHeight_NoFilterShowsPlaceholder(t *testing.T) {
	withTestColorProfile(t)
	p := newCommandPalette("Test", []commandPaletteEntry{
		{Item: tuipick.PickerPanelItem{ID: "a", Label: "/alpha"}},
	}, 80)
	got := p.ViewWithMaxHeight(0)
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "Filter: type to filter...") || !strings.Contains(plain, "> /alpha") {
		t.Fatalf("unfiltered palette lost placeholder or selected command:\n%s", plain)
	}
}

func TestCovViewWithMaxHeight_NarrowWidth(t *testing.T) {
	withTestColorProfile(t)
	p := newCommandPalette("Test", []commandPaletteEntry{
		{Item: tuipick.PickerPanelItem{ID: "a", Label: "/alpha"}},
	}, 30)
	plain := ansiPattern.ReplaceAllString(p.ViewWithMaxHeight(0), "")
	for _, want := range []string{"Test", "Filter: type to filter...", "> /alpha", "enter run"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("narrow palette lost %q:\n%s", want, plain)
		}
	}
}

// ---- paletteItemWindow: edge cases -------------------------------------------

func TestCovPaletteItemWindow_ZeroCount(t *testing.T) {
	start, end := paletteItemWindow(0, 0, 5)
	if start != 0 || end != 0 {
		t.Fatalf("zero count = (%d,%d), want (0,0)", start, end)
	}
}

func TestCovPaletteItemWindow_MaxRowsZeroShowsAll(t *testing.T) {
	start, end := paletteItemWindow(10, 5, 0)
	if start != 0 || end != 10 {
		t.Fatalf("maxRows=0 = (%d,%d), want (0,10)", start, end)
	}
}

func TestCovPaletteItemWindow_MaxRowsGECountShowsAll(t *testing.T) {
	start, end := paletteItemWindow(5, 2, 10)
	if start != 0 || end != 5 {
		t.Fatalf("maxRows>=count = (%d,%d), want (0,5)", start, end)
	}
}

func TestCovPaletteItemWindow_NegativeCursorClamped(t *testing.T) {
	start, end := paletteItemWindow(10, -5, 3)
	if start != 0 || end != 3 {
		t.Fatalf("negative cursor = (%d,%d), want (0,3)", start, end)
	}
}

func TestCovPaletteItemWindow_CursorBeyondCount(t *testing.T) {
	start, end := paletteItemWindow(5, 100, 3)
	if start != 2 || end != 5 {
		t.Fatalf("cursor beyond count = (%d,%d), want (2,5)", start, end)
	}
}

func TestCovPaletteItemWindow_WindowNearEnd(t *testing.T) {
	start, end := paletteItemWindow(10, 9, 3)
	if start != 7 || end != 10 {
		t.Fatalf("window near end = (%d,%d), want (7,10)", start, end)
	}
}

func TestCovPaletteItemWindow_WindowNearStart(t *testing.T) {
	start, end := paletteItemWindow(10, 0, 3)
	if start != 0 || end != 3 {
		t.Fatalf("window near start = (%d,%d), want (0,3)", start, end)
	}
}

// ---- selectedEntry -----------------------------------------------------------

func TestCovSelectedEntry_NoSelectionReturnsFalse(t *testing.T) {
	p := newCommandPalette("Test", []commandPaletteEntry{
		{Item: tuipick.PickerPanelItem{ID: "a", Label: "/alpha"}},
	}, 80)
	_, ok := p.selectedEntry()
	if ok {
		t.Fatalf("no selection should return ok=false")
	}
}

func TestCovSelectedEntry_SelectionNotFound(t *testing.T) {
	p := newCommandPalette("Test", []commandPaletteEntry{
		{Item: tuipick.PickerPanelItem{ID: "a", Label: "/alpha"}},
	}, 80)
	// Set cursor to an item, but the selected ID won't match any entry
	p.panel = tuipick.NewPickerPanel("Test", []tuipick.PickerPanelItem{
		{ID: "nonexistent", Label: "X"},
	}, 80)
	_, ok := p.selectedEntry()
	if ok {
		t.Fatalf("selection not found in entries should return ok=false")
	}
}

func TestCovSelectedEntry_Found(t *testing.T) {
	entries := []commandPaletteEntry{
		{Item: tuipick.PickerPanelItem{ID: "a", Label: "/alpha"}, Command: "alpha"},
	}
	p := newCommandPalette("Test", entries, 80)
	// Move cursor to first item and select it via Enter
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(commandPalette)
	entry, ok := p.selectedEntry()
	if !ok || entry.Command != "alpha" {
		t.Fatalf("selectedEntry = %+v ok=%v, want alpha", entry, ok)
	}
}

// ---- commandPaletteEntriesForRows --------------------------------------------

func TestCovCommandPaletteEntriesForRows_SessionMode(t *testing.T) {
	entries := commandPaletteEntriesForRows(hubModeSession, hubSessionCapabilities{Send: true}, nil)
	got := commandPaletteCommandNames(entries)
	want := []string{
		"upgrade", "help", "dashboard", "project", "auth", "login", "logout",
		"tasks", "agents", "goal", "status", "details", "interrupt", "compact",
		"clear", "fork", "aside", "shutdown", "model", "effort", "theme", "quit",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("session command order = %q, want %q", got, want)
	}
}

func TestCovCommandPaletteEntriesForRows_DashboardMode(t *testing.T) {
	entries := commandPaletteEntriesForRows(hubModeDashboard, hubSessionCapabilities{}, nil)
	got := commandPaletteCommandNames(entries)
	want := []string{"new", "refresh", "upgrade", "clear", "credentials", "settings", "plugins", "quit"}
	if !slices.Equal(got, want) {
		t.Fatalf("dashboard command order = %q, want %q", got, want)
	}
}

func commandPaletteCommandNames(entries []commandPaletteEntry) []string {
	commands := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind == commandPaletteCommand {
			commands = append(commands, entry.Command)
		}
	}
	return commands
}

func TestCovCommandPaletteEntriesForRows_WithSessionRows(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowSession, ref: appwire.Ref{SourceID: "local", ThreadID: "01"}, title: "Session 1", sourceLabel: "evener", state: "idle", model: "gpt-5"},
	}
	entries := commandPaletteEntriesForRows(hubModeDashboard, hubSessionCapabilities{}, rows)
	found := false
	for _, e := range entries {
		if e.Kind == commandPaletteSession {
			found = true
		}
	}
	if !found {
		t.Fatalf("dashboard mode with session rows should include session entries")
	}
}

func TestCovCommandPaletteEntriesForRows_WithProjectRows(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowProject, projectKey: "proj1", project: "Project 1"},
	}
	entries := commandPaletteEntriesForRows(hubModeDashboard, hubSessionCapabilities{}, rows)
	found := false
	for _, e := range entries {
		if e.Kind == commandPaletteProject && e.ProjectKey == "proj1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dashboard mode with project rows should include project entries")
	}
}

func TestCovCommandPaletteEntriesForRows_DuplicateProjectKeys(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowProject, projectKey: "dup", project: "A"},
		{kind: hubRowProject, projectKey: "dup", project: "B"},
	}
	entries := commandPaletteEntriesForRows(hubModeDashboard, hubSessionCapabilities{}, rows)
	count := 0
	for _, e := range entries {
		if e.Kind == commandPaletteProject && e.ProjectKey == "dup" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate project keys should be deduped: count=%d, want 1", count)
	}
}

func TestCovCommandPaletteEntriesForRows_EmptyProjectKey(t *testing.T) {
	rows := []hubRow{
		{kind: hubRowProject, projectKey: "", project: "Empty"},
	}
	entries := commandPaletteEntriesForRows(hubModeDashboard, hubSessionCapabilities{}, rows)
	for _, e := range entries {
		if e.Kind == commandPaletteProject {
			t.Fatalf("empty projectKey should be skipped")
		}
	}
}

// ---- renderItemsWindow: no matching items -----------------------------------

func TestCovRenderItemsWindow_NoMatchingItems(t *testing.T) {
	withTestColorProfile(t)
	p := newCommandPalette("Test", []commandPaletteEntry{
		{Item: tuipick.PickerPanelItem{ID: "a", Label: "/alpha"}},
	}, 80)
	p.panel.SetFilter("zzznotfound")
	got := p.renderItemsWindow(0)
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "No matching commands") {
		t.Fatalf("no matching items should show 'No matching commands':\n%s", plain)
	}
}

// ---- renderItemsWindow: disabled items ---------------------------------------

func TestCovRenderItemsWindow_DisabledItem(t *testing.T) {
	withTestColorProfile(t)
	p := newCommandPalette("Test", []commandPaletteEntry{
		{Item: tuipick.PickerPanelItem{ID: "a", Label: "/alpha", DisabledReason: "not available"}},
	}, 80)
	got := p.renderItemsWindow(0)
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "disabled") {
		t.Fatalf("disabled item should show 'disabled':\n%s", plain)
	}
}
