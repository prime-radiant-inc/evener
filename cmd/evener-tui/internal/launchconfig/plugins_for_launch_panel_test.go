package launchconfig

import (
	"errors"
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
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
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
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !strings.Contains(p.View(), "[x] alpha") || !strings.Contains(p.View(), "[x] gamma") {
		t.Fatalf("all view = %q", p.View())
	}
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
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
	p = updatePluginsPanel(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	updated, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = updated.(PluginsForLaunchPanel)
	if !p.Done() || p.Result().EnabledPlugins == nil {
		t.Fatalf("diagnostic-only apply: done=%v result=%#v", p.Done(), p.Result())
	}
}

func TestPluginsForLaunchPanel_ScrollingAndNarrowRender(t *testing.T) {
	plugins := make([]appwire.PluginLaunchCandidate, 20)
	for i := range plugins {
		plugins[i] = appwire.PluginLaunchCandidate{Name: "plugin-" + string(rune('a'+i))}
	}
	p := NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{Plugins: plugins}, nil, 80)
	for i := 0; i < 19; i++ {
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
	if p.Done() || !strings.Contains(p.View(), "Preview failed") {
		t.Fatalf("failure state: done=%v view=%q", p.Done(), p.View())
	}
}

func updatePluginsPanel(p PluginsForLaunchPanel, msg tea.Msg) PluginsForLaunchPanel {
	updated, _ := p.Update(msg)
	return updated.(PluginsForLaunchPanel)
}
