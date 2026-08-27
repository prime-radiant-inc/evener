package launchconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

func TestPluginsForLaunchPanel_NoneAppliesExplicitEmpty(t *testing.T) {
	preview := appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{
		{Name: "alpha", Selected: true}, {Name: "beta", Selected: true},
	}}
	p := NewPluginsForLaunchPanel(preview, nil, 80)
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	p = updated.(PluginsForLaunchPanel)
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	got := p.Result()
	if got.EnabledPlugins == nil || len(*got.EnabledPlugins) != 0 {
		t.Fatalf("result = %#v", got)
	}
}

func TestPluginsForLaunchPanel_SelectionAndFiltering(t *testing.T) {
	preview := appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{
		{Name: "alpha", Source: "project", Description: "database tools", Selected: true},
		{Name: "beta", Source: "marketplace", Description: "shell tools"},
	}}
	p := NewPluginsForLaunchPanel(preview, nil, 80)
	view := p.View()
	if !strings.Contains(view, "[x] alpha") || !strings.Contains(view, "[ ] beta") {
		t.Fatalf("initial view = %q", view)
	}
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if strings.Contains(p.View(), "alpha") || !strings.Contains(p.View(), "beta") {
		t.Fatalf("filtered view = %q", p.View())
	}
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyBackspace})
	if !strings.Contains(p.View(), "alpha") || !strings.Contains(p.View(), "beta") {
		t.Fatalf("backspace view = %q", p.View())
	}
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyDown})
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeySpace})
	if !strings.Contains(p.View(), "[x] beta") {
		t.Fatalf("toggle view = %q", p.View())
	}
}

func TestPluginsForLaunchPanel_AllNoneAndCancel(t *testing.T) {
	preview := appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{
		{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"},
	}}
	p := NewPluginsForLaunchPanel(preview, nil, 80)
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	if !strings.Contains(p.View(), "[x] alpha") || !strings.Contains(p.View(), "[x] gamma") {
		t.Fatalf("all view = %q", p.View())
	}
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyEscape})
	if !p.Done() || p.Result().Applied || strings.Contains(p.View(), "[x]") {
		t.Fatalf("cancelled panel = done=%v result=%#v view=%q", p.Done(), p.Result(), p.View())
	}
}

func TestPluginsForLaunchPanel_EnterBlocksOnlySelectionErrors(t *testing.T) {
	preview := appwire.PluginPreviewResponse{
		Plugins:         []appwire.PluginLaunchCandidate{{Name: "broken", Selected: true}, {Name: "ok"}},
		SelectionErrors: []appwire.PluginSelectionError{{Name: "broken", Reason: "missing manifest"}},
		Diagnostics:     []appwire.PluginDiagnostic{{Name: "ok", Message: "optional metadata unavailable"}},
	}
	p := NewPluginsForLaunchPanel(preview, nil, 80)
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	if p.Done() || !strings.Contains(p.View(), "missing manifest") {
		t.Fatalf("blocking result: done=%v view=%q", p.Done(), p.View())
	}
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	if !p.Done() || p.Result().EnabledPlugins == nil {
		t.Fatalf("diagnostic-only apply: done=%v result=%#v", p.Done(), p.Result())
	}
}

func TestPluginsForLaunchPanel_AbsentSelectedErrorIsVisibleAndClearable(t *testing.T) {
	initial := []string{"ghost"}
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{
		Plugins:         []appwire.PluginLaunchCandidate{{Name: "alpha"}},
		SelectionErrors: []appwire.PluginSelectionError{{Name: "ghost", Reason: "no valid plugin candidate"}},
	}, &initial, 80)
	view := p.View()
	if !strings.Contains(view, "[x] ghost") || !strings.Contains(view, "no valid plugin candidate") {
		t.Fatalf("absent selected error view = %q", view)
	}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	if p.Done() {
		t.Fatalf("absent selected error should block apply: result=%#v", p.Result())
	}
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	result := p.Result()
	if !p.Done() || result.EnabledPlugins == nil || len(*result.EnabledPlugins) != 0 {
		t.Fatalf("cleared absent selection result=%#v done=%v", result, p.Done())
	}
}

func TestPluginsForLaunchPanel_AllPrunesAbsentSelectionErrors(t *testing.T) {
	initial := []string{"ghost"}
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{
		Plugins:         []appwire.PluginLaunchCandidate{{Name: "alpha"}},
		SelectionErrors: []appwire.PluginSelectionError{{Name: "ghost", Reason: "no valid plugin candidate"}},
	}, &initial, 80)
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	result := p.Result()
	if !p.Done() || result.EnabledPlugins == nil || !reflect.DeepEqual(*result.EnabledPlugins, []string{"alpha"}) {
		t.Fatalf("all result=%#v done=%v", result, p.Done())
	}
}

func TestPluginsForLaunchPanel_LowercaseAFiltersAndUppercaseASelectsVisible(t *testing.T) {
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{
		{Name: "alpha"},
		{Name: "ember"},
	}}, nil, 80)
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	view := p.View()
	if p.filter != "a" || !strings.Contains(view, "alpha") || strings.Contains(view, "ember") {
		t.Fatalf("lowercase a filter=%q view=%q", p.filter, view)
	}
	if p.dirty {
		t.Fatal("lowercase a filter marked selection dirty")
	}
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	if !p.selected["alpha"] || p.selected["ember"] {
		t.Fatalf("uppercase A selected=%v, want only filtered alpha selected", p.selected)
	}
}

func TestPluginsForLaunchPanel_LowercaseNFiltersAndUppercaseNClears(t *testing.T) {
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{
		{Name: "alpha", Selected: true},
		{Name: "none"},
		{Name: "ember"},
	}}, nil, 80)
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if p.filter != "n" || !strings.Contains(p.View(), "none") || strings.Contains(p.View(), "ember") {
		t.Fatalf("lowercase n filter=%q view=%q", p.filter, p.View())
	}
	if p.dirty || !p.selected["alpha"] {
		t.Fatalf("lowercase n changed selection: dirty=%v selected=%v", p.dirty, p.selected)
	}
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if len(p.selected) != 0 || !p.dirty || p.filter != "n" {
		t.Fatalf("uppercase N result: filter=%q dirty=%v selected=%v", p.filter, p.dirty, p.selected)
	}
}

func TestPluginsForLaunchPanel_FooterDescribesFilterScopedAllAction(t *testing.T) {
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{
		{Name: "alpha"},
	}}, nil, 80)
	view := p.View()
	if !strings.Contains(view, "A all matching") || !strings.Contains(view, "N none") || strings.Contains(view, "n none") || strings.Contains(view, "all available") {
		t.Fatalf("all action footer = %q", view)
	}
}

func TestPluginsForLaunchPanel_ScrollingAndNarrowRender(t *testing.T) {
	plugins := make([]appwire.PluginLaunchCandidate, 20)
	for i := range plugins {
		plugins[i] = appwire.PluginLaunchCandidate{Name: "plugin-" + string(rune('a'+i))}
	}
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{Plugins: plugins}, nil, 80)
	for range 19 {
		p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyDown})
	}
	if strings.Contains(p.View(), "plugin-a") || !strings.Contains(p.View(), "plugin-t") {
		t.Fatalf("scrolled view = %q", p.View())
	}
	if got := strings.Count(p.View(), "plugin-"); got > 15 {
		t.Fatalf("visible rows=%d, want <=15", got)
	}
	if got := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{Plugins: plugins}, nil, 12).View(); got == "" {
		t.Fatal("narrow view is empty")
	}
}

func TestPluginsForLaunchPanel_RejectsErrorOnlyOnEnter(t *testing.T) {
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "alpha"}}}, nil, 80)
	updated, _ := p.Update(PluginPreviewResultMsg{Key: "current", Err: errors.New("hub unavailable")})
	p = updated.(PluginsForLaunchPanel)
	if p.Done() || !strings.Contains(p.View(), "Couldn't inspect plugins") || !strings.Contains(p.View(), "Press Enter to retry") {
		t.Fatalf("failure state: done=%v view=%q", p.Done(), p.View())
	}
}

func TestPluginsForLaunchPanel_FailedRefreshCannotEditStaleSelection(t *testing.T) {
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "stale", Selected: true}}}, nil, 80)
	updated, _ := p.Update(PluginPreviewResultMsg{Err: errors.New("temporary failure")})
	p = updated.(PluginsForLaunchPanel)
	if !strings.Contains(p.View(), "enter retry") || strings.Contains(p.View(), "enter apply") {
		t.Fatalf("failure footer = %q", p.View())
	}
	before := map[string]bool{"stale": true}
	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeySpace},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}},
		tea.KeyMsg{Type: tea.KeyDown},
	} {
		updated, _ = p.Update(msg)
		p = updated.(PluginsForLaunchPanel)
		if !reflect.DeepEqual(p.selected, before) || p.dirty {
			t.Fatalf("failed refresh accepted edit %T: selected=%v dirty=%v", msg, p.selected, p.dirty)
		}
	}
	updated, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	if cmd == nil || cmd().(PluginsForLaunchResultMsg).Retry != true || p.Done() {
		t.Fatalf("failed refresh Enter = done=%v cmd=%v", p.Done(), cmd != nil)
	}
	updated, _ = p.Update(PluginPreviewResultMsg{Response: appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "fresh", Selected: true}}}})
	p = updated.(PluginsForLaunchPanel)
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	result := p.Result()
	if result.EnabledPlugins == nil || !reflect.DeepEqual(*result.EnabledPlugins, []string{"fresh"}) {
		t.Fatalf("retry result=%#v, want only fresh selection", result)
	}
}

func TestPluginsForLaunchPanel_PreviewErrorNoneAppliesExplicitEmpty(t *testing.T) {
	initial := []string{"stale"}
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{
		Plugins:         []appwire.PluginLaunchCandidate{{Name: "fresh"}},
		SelectionErrors: []appwire.PluginSelectionError{{Name: "stale", Reason: "no valid plugin candidate"}},
	}, &initial, 80)
	updated, _ := p.Update(PluginPreviewResultMsg{Err: errors.New("temporary failure")})
	p = updated.(PluginsForLaunchPanel)
	if !strings.Contains(p.View(), "enter retry") || !strings.Contains(p.View(), "N none") {
		t.Fatalf("preview error footer = %q", p.View())
	}

	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if len(p.selected) != 0 {
		t.Fatalf("cleared selection = %v, want empty", p.selected)
	}
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeySpace})
	p = updated.(PluginsForLaunchPanel)
	if len(p.selected) != 0 {
		t.Fatalf("preview-error stale candidate became editable: %v", p.selected)
	}
	if view := p.View(); !strings.Contains(view, "enter apply") || strings.Contains(view, "enter retry") {
		t.Fatalf("cleared preview error footer = %q", view)
	}

	updated, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	if cmd == nil || !p.Done() {
		t.Fatalf("cleared preview error apply = done=%v cmd=%v", p.Done(), cmd != nil)
	}
	result, ok := cmd().(PluginsForLaunchResultMsg)
	if !ok || !result.Applied || result.EnabledPlugins == nil || len(*result.EnabledPlugins) != 0 {
		t.Fatalf("cleared preview error result = %#v", result)
	}
}

func TestPluginsForLaunchPanel_PreviewErrorNoneShortcutIgnoresNonShortcutText(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.KeyMsg
	}{
		{
			name: "pasted exact N",
			msg:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}, Paste: true},
		},
		{
			name: "lowercase n",
			msg:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}},
		},
		{
			name: "ordinary text containing N",
			msg:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("None")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := []string{"stale"}
			p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{
				Plugins: []appwire.PluginLaunchCandidate{{Name: "fresh"}},
			}, &initial, 80)
			updated, _ := p.Update(PluginPreviewResultMsg{Err: errors.New("temporary failure")})
			p = updated.(PluginsForLaunchPanel)

			p = updatePluginsPanel(p, tc.msg)
			if got := p.selectedValues(); got == nil || !reflect.DeepEqual(*got, []string{"stale"}) {
				t.Fatalf("selected after %s = %#v, want stale retained", tc.name, got)
			}
			if p.previewErrorSelectionCleared {
				t.Fatalf("%s marked preview error selection cleared", tc.name)
			}

			updated, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
			p = updated.(PluginsForLaunchPanel)
			if cmd == nil || !cmd().(PluginsForLaunchResultMsg).Retry || p.Done() {
				t.Fatalf("Enter after %s = done=%v cmd=%v, want retry", tc.name, p.Done(), cmd != nil)
			}
		})
	}
}

func TestPluginsForLaunchPanel_PastedShortcutsAreFilterText(t *testing.T) {
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{
		{Name: "alpha", Selected: true},
		{Name: "beta"},
	}}, nil, 80)
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A/n"), Paste: true})

	if p.filter != "A/n" {
		t.Fatalf("pasted filter=%q, want %q", p.filter, "A/n")
	}
	if p.dirty || !p.selected["alpha"] || p.selected["beta"] {
		t.Fatalf("pasted shortcuts changed selection: dirty=%v selected=%v", p.dirty, p.selected)
	}
}

func TestPluginsForLaunchPanel_ExplicitSelectionStaysBlockedAcrossFailedRefreshRetry(t *testing.T) {
	initial := []string{"stale"}
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{
		Plugins: []appwire.PluginLaunchCandidate{{Name: "stale", Selected: true}},
	}, &initial, 80)
	updated, _ := p.Update(PluginPreviewResultMsg{Err: errors.New("temporary failure")})
	p = updated.(PluginsForLaunchPanel)
	updated, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	if cmd == nil || !cmd().(PluginsForLaunchResultMsg).Retry || p.Done() {
		t.Fatalf("failed refresh Enter = done=%v cmd=%v", p.Done(), cmd != nil)
	}
	updated, _ = p.Update(PluginPreviewResultMsg{Response: appwire.PluginPreviewResponse{
		Plugins:         []appwire.PluginLaunchCandidate{{Name: "fresh", Selected: true}},
		SelectionErrors: []appwire.PluginSelectionError{{Name: "stale", Reason: "no valid plugin candidate"}},
	}})
	p = updated.(PluginsForLaunchPanel)
	if got := p.selectedValues(); got == nil || !reflect.DeepEqual(*got, []string{"stale"}) {
		t.Fatalf("selected after retry = %#v, want stale retained", got)
	}
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	if p.Done() {
		t.Fatalf("stale retained selection should keep Apply blocked: result=%#v", p.Result())
	}
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	result := p.Result()
	if !p.Done() || result.EnabledPlugins == nil || len(*result.EnabledPlugins) != 0 {
		t.Fatalf("cleared stale selection result=%#v done=%v", result, p.Done())
	}
}

func updatePluginsPanel(p PluginsForLaunchPanel, msg tea.Msg) PluginsForLaunchPanel {
	updated, _ := p.Update(msg)
	return updated.(PluginsForLaunchPanel)
}
