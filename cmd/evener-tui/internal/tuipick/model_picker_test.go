package tuipick

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"primeradiant.com/evener/cmd/evener-tui/internal/tuiprim"
)

func TestModelPicker_FilterAndSelect(t *testing.T) {
	items := []ModelPickerItem{
		{ID: "gpt-4o", Display: "gpt-4o"},
		{ID: "gpt-4o-mini", Display: "gpt-4o-mini"},
		{ID: "o3", Display: "o3"},
	}
	p := NewModelPicker(items, "gpt-4o", 80)

	// Type "mini" to filter.
	var tm tea.Model = p
	for _, ch := range "mini" {
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	mp := tm.(ModelPicker)
	filtered := mp.filtered()
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered result, got %d", len(filtered))
	}
	if filtered[0].ID != "gpt-4o-mini" {
		t.Errorf("filtered[0] = %q, want gpt-4o-mini", filtered[0].ID)
	}

	// Press enter to select.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mp = tm.(ModelPicker)
	if mp.selected != "gpt-4o-mini" {
		t.Errorf("selected = %q, want gpt-4o-mini", mp.selected)
	}
	if !mp.done {
		t.Error("expected done=true after enter")
	}
}

func TestModelPicker_Escape(t *testing.T) {
	items := []ModelPickerItem{{ID: "m1", Display: "m1"}}
	p := NewModelPicker(items, "", 80)

	result, _ := p.Update(tea.KeyMsg{Type: tea.KeyEscape})
	mp := result.(ModelPicker)
	if !mp.cancelled {
		t.Error("expected cancelled=true on escape")
	}
	if !mp.done {
		t.Error("expected done=true on escape")
	}
}

func TestModelPickerEmptyIDSelectionIsDistinctFromCancellation(t *testing.T) {
	items := []ModelPickerItem{{ID: "", Display: "Current model"}}

	selectedModel, _ := NewModelPicker(items, "", 80).Update(tea.KeyMsg{Type: tea.KeyEnter})
	selected := selectedModel.(ModelPicker)
	if !selected.Done() || selected.Cancelled() || selected.Selected() != "" {
		t.Fatalf("empty-ID selection = done:%v cancelled:%v selected:%q, want selected empty ID without cancellation", selected.Done(), selected.Cancelled(), selected.Selected())
	}

	for _, key := range []tea.KeyType{tea.KeyEscape, tea.KeyCtrlC} {
		cancelledModel, _ := NewModelPicker(items, "", 80).Update(tea.KeyMsg{Type: key})
		cancelled := cancelledModel.(ModelPicker)
		if !cancelled.Done() || !cancelled.Cancelled() || cancelled.Selected() != "" {
			t.Fatalf("%v = done:%v cancelled:%v selected:%q, want cancellation without selection", key, cancelled.Done(), cancelled.Cancelled(), cancelled.Selected())
		}
	}
}

func TestModelPicker_ActiveHighlight(t *testing.T) {
	items := []ModelPickerItem{
		{ID: "gpt-4o", Display: "gpt-4o"},
		{ID: "gpt-4o-mini", Display: "gpt-4o-mini"},
	}
	p := NewModelPicker(items, "gpt-4o-mini", 80)

	plain := ansiPattern.ReplaceAllString(p.View(), "")
	lines := strings.Split(plain, "\n")

	var miniLine, gpt4oLine string
	for _, line := range lines {
		if strings.Contains(line, "gpt-4o-mini") {
			miniLine = line
		} else if strings.Contains(line, "gpt-4o") {
			gpt4oLine = line
		}
	}
	if miniLine == "" {
		t.Fatal("no line containing gpt-4o-mini found in view")
	}
	if !strings.Contains(miniLine, "(active)") {
		t.Errorf("line with gpt-4o-mini should contain (active), got: %q", miniLine)
	}
	if gpt4oLine == "" {
		t.Fatal("no line containing gpt-4o found in view")
	}
	if strings.Contains(gpt4oLine, "(active)") {
		t.Errorf("line with gpt-4o should NOT contain (active), got: %q", gpt4oLine)
	}
}

// TestModelPicker_ActiveHighlightNormalizesProviderQualifiedID covers the
// /model picker's real bug: item IDs are "provider/model" (hub_commands.go
// buildModelPickerItems) but the session's active model is the bare model
// name (hub_types.go hubDetailFromThread: Model: thread.ModelProvider, which
// itself is set from the bare status.Model). Without normalizing, the
// currently active model never renders "(active)".
func TestModelPicker_ActiveHighlightNormalizesProviderQualifiedID(t *testing.T) {
	items := []ModelPickerItem{
		{ID: "openai/gpt-5", Display: "gpt-5"},
		{ID: "openai/gpt-5-mini", Display: "gpt-5-mini"},
	}
	p := NewModelPicker(items, "gpt-5-mini", 80)

	plain := ansiPattern.ReplaceAllString(p.View(), "")
	lines := strings.Split(plain, "\n")

	var miniLine, gptLine string
	for _, line := range lines {
		if strings.Contains(line, "gpt-5-mini") {
			miniLine = line
		} else if strings.Contains(line, "gpt-5") {
			gptLine = line
		}
	}
	if miniLine == "" {
		t.Fatal("no line containing gpt-5-mini found in view")
	}
	if !strings.Contains(miniLine, "(active)") {
		t.Errorf("line with gpt-5-mini should contain (active) once the provider-qualified id is normalized against the bare active model, got: %q", miniLine)
	}
	if gptLine == "" {
		t.Fatal("no line containing gpt-5 found in view")
	}
	if strings.Contains(gptLine, "(active)") {
		t.Errorf("line with gpt-5 should NOT contain (active), got: %q", gptLine)
	}
}

func TestModelPicker_DisabledItemRendersReasonAndCannotSelect(t *testing.T) {
	items := []ModelPickerItem{
		{ID: "openai/gpt-5", Display: "openai/gpt-5", DisabledReason: "login required: run /auth openai"},
		{ID: "ollama/llama3", Display: "ollama/llama3"},
	}
	p := NewModelPicker(items, "", 80)

	if view := p.View(); !strings.Contains(view, "disabled: login required") || !strings.Contains(view, "/auth openai") {
		t.Fatalf("picker did not render disabled reason:\n%s", view)
	}

	tm, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("disabled selection returned unexpected command")
	}
	mp := tm.(ModelPicker)
	if mp.done || mp.selected != "" {
		t.Fatalf("disabled row should keep picker open without selection: done=%v selected=%q", mp.done, mp.selected)
	}
	tm, _ = mp.Update(tea.KeyMsg{Type: tea.KeyDown})
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mp = tm.(ModelPicker)
	if !mp.done || mp.selected != "ollama/llama3" {
		t.Fatalf("enabled row selection done=%v selected=%q, want ollama/llama3", mp.done, mp.selected)
	}
}

// A registry warning (e.g. a global-only model under a regional Vertex
// location) renders as a dim note under the row while the row stays
// selectable: it is information, not a disabled or removed option.
func TestModelPicker_WarningRendersUnderRowAndStaysSelectable(t *testing.T) {
	const note = `regional location cannot serve this model`
	items := []ModelPickerItem{
		{ID: "vertex/gemini-3.8-flash", Display: "gemini-3.8-flash", Warnings: []string{note}},
	}
	p := NewModelPicker(items, "", 80)

	plain := ansiPattern.ReplaceAllString(p.View(), "")
	lines := strings.Split(plain, "\n")
	var row, warningLine string
	for _, line := range lines {
		if strings.Contains(line, "gemini-3.8-flash") {
			row = line
		}
		if strings.Contains(line, note) {
			warningLine = line
		}
	}
	if row == "" {
		t.Fatal("no row for the model in view")
	}
	if strings.Contains(row, note) {
		t.Fatalf("the warning must render on its own line, not on the row: %q", row)
	}
	if warningLine == "" {
		t.Fatalf("picker did not render the warning as its own line:\n%s", plain)
	}

	selectedModel, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	selected := selectedModel.(ModelPicker)
	if !selected.Done() || selected.Selected() != "vertex/gemini-3.8-flash" {
		t.Fatalf("a warned row must stay selectable: done:%v selected:%q", selected.Done(), selected.Selected())
	}
}

// windowLines is renderBody's model window as the overlay frame renders it:
// the body wrapped to the frame's content width, minus the filter line and its
// blank separator and the optional "N items total" trailer. A warning longer
// than the frame is wide occupies the terminal lines it really occupies.
func windowLines(t *testing.T, m ModelPicker) (lines []string, trailer bool) {
	t.Helper()
	body := strings.TrimRight(ansi.Wrap(m.renderBody(), tuiprim.OverlayContentWidth(m.overlayWidth()), ""), "\n")
	lines = strings.Split(body, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "Filter:") || lines[1] != "" {
		t.Fatalf("unexpected body preamble:\n%s", body)
	}
	lines = lines[2:]
	if n := len(lines); n > 0 && strings.Contains(lines[n-1], "items total") {
		trailer = true
		lines = lines[:n-1]
	}
	return lines, trailer
}

// A warned row is taller than a plain one, so the window is budgeted in
// rendered lines: a list of warned models must not push the rows and the
// footer past the overlay's intended height.
func TestModelPicker_WarnedRowsStayWithinTheBodyBudget(t *testing.T) {
	items := make([]ModelPickerItem, 20)
	for i := range items {
		items[i] = ModelPickerItem{
			ID:       fmt.Sprintf("m%d", i),
			Display:  fmt.Sprintf("m%d", i),
			Warnings: []string{"regional location cannot serve this model"},
		}
	}
	p := NewModelPicker(items, "", 80)
	p.cursor = 10

	lines, trailer := windowLines(t, p)
	if len(lines) > maxVisibleLines {
		t.Fatalf("body window = %d rendered lines, want <= %d:\n%s", len(lines), maxVisibleLines, p.renderBody())
	}
	if !trailer {
		t.Fatalf("a windowed list must still say how many items it has:\n%s", p.renderBody())
	}
	if !strings.Contains(strings.Join(lines, "\n"), "> m10") {
		t.Fatalf("the cursor's row must stay in the window:\n%s", p.renderBody())
	}
}

// Group headers are rendered lines too: the budget counts them, not just rows.
func TestModelPicker_GroupHeadersCountTowardTheBodyBudget(t *testing.T) {
	items := make([]ModelPickerItem, 20)
	for i := range items {
		items[i] = ModelPickerItem{ID: fmt.Sprintf("m%d", i), Display: fmt.Sprintf("m%d", i), Group: fmt.Sprintf("g%d", i)}
	}
	p := NewModelPicker(items, "", 80)
	p.cursor = 10

	lines, _ := windowLines(t, p)
	if len(lines) > maxVisibleLines {
		t.Fatalf("body window = %d rendered lines, want <= %d:\n%s", len(lines), maxVisibleLines, p.renderBody())
	}
}

// The warning a regional Vertex location actually emits is longer than the
// overlay is wide, and the frame wraps it. The budget must count the terminal
// lines an item really occupies, or a few warned rows still push the rows and
// the footer off the screen.
func TestModelPicker_WrappedWarningsStayWithinTheBodyBudget(t *testing.T) {
	const note = `regional Vertex location "us-central1" does not serve Gemini 3 or later; use global, us, or eu for gemini-3.8-flash`
	items := make([]ModelPickerItem, 20)
	for i := range items {
		items[i] = ModelPickerItem{
			ID:       fmt.Sprintf("m%d", i),
			Display:  fmt.Sprintf("m%d", i),
			Warnings: []string{note},
		}
	}
	p := NewModelPicker(items, "", 80)
	p.cursor = 10

	lines, _ := windowLines(t, p)
	if len(lines) > maxVisibleLines {
		t.Fatalf("body window = %d rendered lines, want <= %d:\n%s", len(lines), maxVisibleLines, p.renderBody())
	}
	if !strings.Contains(strings.Join(lines, "\n"), "> m10") {
		t.Fatalf("the cursor's row must stay in the window:\n%s", p.renderBody())
	}
}

// A cursor can outlive the list it indexes: a filter narrows the list under a
// cursor that was set while the list was longer. The window clamps it instead
// of indexing past the end.
func TestModelPicker_StaleCursorStillRendersABoundedWindow(t *testing.T) {
	items := make([]ModelPickerItem, 20)
	for i := range items {
		items[i] = ModelPickerItem{
			ID:       fmt.Sprintf("m%d", i),
			Display:  fmt.Sprintf("m%d", i),
			Warnings: []string{"regional location cannot serve this model"},
		}
	}
	p := NewModelPicker(items, "", 80)
	p.filter = "m1" // matches m1 and m10..m19: 11 items for a cursor at 15
	p.cursor = 15

	lines, _ := windowLines(t, p)
	if len(lines) > maxVisibleLines {
		t.Fatalf("body window = %d rendered lines, want <= %d:\n%s", len(lines), maxVisibleLines, p.renderBody())
	}
}

func TestModelPicker_Navigation(t *testing.T) {
	items := []ModelPickerItem{
		{ID: "a", Display: "a"},
		{ID: "b", Display: "b"},
		{ID: "c", Display: "c"},
	}
	p := NewModelPicker(items, "", 80)
	if p.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", p.cursor)
	}

	// Down arrow.
	tm, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	mp := tm.(ModelPicker)
	if mp.cursor != 1 {
		t.Errorf("after down: cursor = %d, want 1", mp.cursor)
	}

	// Down again.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mp = tm.(ModelPicker)
	if mp.cursor != 2 {
		t.Errorf("after 2x down: cursor = %d, want 2", mp.cursor)
	}

	// Down at bottom should stay.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mp = tm.(ModelPicker)
	if mp.cursor != 2 {
		t.Errorf("at bottom: cursor = %d, want 2", mp.cursor)
	}

	// Up arrow.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyUp})
	mp = tm.(ModelPicker)
	if mp.cursor != 1 {
		t.Errorf("after up: cursor = %d, want 1", mp.cursor)
	}
}

func TestModelPicker_Backspace(t *testing.T) {
	items := []ModelPickerItem{
		{ID: "gpt-4o", Display: "gpt-4o"},
		{ID: "gpt-4o-mini", Display: "gpt-4o-mini"},
	}
	p := NewModelPicker(items, "", 80)

	// Type "xyz" -- should filter to 0 results.
	var tm tea.Model = p
	for _, ch := range "xyz" {
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	mp := tm.(ModelPicker)
	if len(mp.filtered()) != 0 {
		t.Fatalf("expected 0 filtered after 'xyz', got %d", len(mp.filtered()))
	}

	// Backspace 3 times to clear filter.
	for range 3 {
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	mp = tm.(ModelPicker)
	if mp.filter != "" {
		t.Errorf("filter should be empty after 3 backspaces, got %q", mp.filter)
	}
	if len(mp.filtered()) != 2 {
		t.Errorf("expected 2 filtered after clearing, got %d", len(mp.filtered()))
	}
}

func TestModelPickerRendersAsPopupPane(t *testing.T) {
	withTestColorProfile(t)
	p := NewModelPicker([]ModelPickerItem{{ID: "openai/gpt-5", Display: "openai/gpt-5"}}, "", 80)

	view := p.View()
	if !strings.Contains(view, "\x1b[") {
		t.Fatalf("model picker popup should render terminal styling:\n%s", view)
	}
	plain := ansiPattern.ReplaceAllString(view, "")
	if !strings.Contains(plain, "Select model") {
		t.Fatalf("model picker popup should have title:\n%s", plain)
	}
}

func TestModelPickerUsesOverlayBorder(t *testing.T) {
	withTestColorProfile(t)
	p := NewModelPicker([]ModelPickerItem{{ID: "x", Display: "openai/gpt-5.5"}}, "x", 80)
	got := p.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╯") {
		t.Errorf("model picker should use rounded border (Overlay primitive): %q", plain)
	}
	if !strings.Contains(plain, "Select model") {
		t.Errorf("title should be in border: %q", plain)
	}
}

func TestModelPicker_RendersGroupHeadersOnTransition(t *testing.T) {
	withTestColorProfile(t)
	items := []ModelPickerItem{
		{ID: "anthropic/claude-opus-4-6", Display: "Claude Opus 4 6", Group: "Recent"},
		{ID: "anthropic/claude-opus-4-6", Display: "Claude Opus 4 6", Group: "anthropic"},
		{ID: "openai/gpt-5.2", Display: "Gpt 5.2", Group: "openai"},
	}
	p := NewModelPicker(items, "", 80)
	plain := ansiPattern.ReplaceAllString(p.View(), "")
	lines := strings.Split(plain, "\n")

	recentIdx, anthropicIdx, openaiIdx := -1, -1, -1
	for i, line := range lines {
		// Overlay borders every body line with "│"; strip that framing before
		// comparing bare header text.
		trimmed := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│"))
		if trimmed == "RECENT" {
			recentIdx = i
		}
		if trimmed == "ANTHROPIC" {
			anthropicIdx = i
		}
		if trimmed == "OPENAI" {
			openaiIdx = i
		}
	}
	if recentIdx == -1 || anthropicIdx == -1 || openaiIdx == -1 {
		t.Fatalf("expected RECENT, ANTHROPIC, and OPENAI group headers, view:\n%s", plain)
	}
	if recentIdx >= anthropicIdx || anthropicIdx >= openaiIdx {
		t.Fatalf("group headers out of order: recent=%d anthropic=%d openai=%d", recentIdx, anthropicIdx, openaiIdx)
	}
}

func TestModelPicker_RendersMetaTail(t *testing.T) {
	withTestColorProfile(t)
	items := []ModelPickerItem{
		{ID: "anthropic/claude-opus-4-6", Display: "Claude Opus 4 6", Meta: "1M ctx · $5.00/$25.00 · tools,vision"},
	}
	// Wide enough (clamped to the Overlay's 96-col max) that the row isn't
	// word-wrapped across lines by the popup frame.
	p := NewModelPicker(items, "", 96)
	plain := ansiPattern.ReplaceAllString(p.View(), "")
	if !strings.Contains(plain, "1M ctx · $5.00/$25.00 · tools,vision") {
		t.Fatalf("expected meta tail in view:\n%s", plain)
	}
}

func TestModelPicker_ZeroValueGroupMetaUnchangedForActionPicker(t *testing.T) {
	// NewActionPicker/NewTranscriptPicker items never set Group/Meta; their
	// rendering must be byte-for-byte unaffected by this change.
	items := []ModelPickerItem{{ID: "restart", Display: "Restart session"}}
	p := NewActionPicker("Actions", "enter select", items, 80)
	plain := ansiPattern.ReplaceAllString(p.View(), "")
	if strings.Contains(plain, "\n\n\n") {
		t.Fatalf("zero-value Group must not introduce a spurious blank header line:\n%s", plain)
	}
	if !strings.Contains(plain, "Restart session") {
		t.Fatalf("action item should still render:\n%s", plain)
	}
}
