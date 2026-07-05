package launchconfig

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

func rightKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRight} }
func leftKey() tea.KeyMsg  { return tea.KeyMsg{Type: tea.KeyLeft} }
func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func loadedMarketplaces(entries ...appwire.MarketplaceEntry) PluginsPanel {
	p := NewPluginsPanel()
	updated, _ := p.Update(MarketplaceListResultMsg{List: appwire.MarketplaceListResponse{Marketplaces: entries}})
	return updated.(PluginsPanel)
}

func loadedPlugins(entries ...appwire.PluginEntry) PluginsPanel {
	p := NewPluginsPanel()
	updated, _ := p.Update(PluginListResultMsg{List: appwire.PluginListResponse{Plugins: entries}})
	return updated.(PluginsPanel)
}

// --- overlay / tabs ---

func TestPluginsPanelUsesOverlay(t *testing.T) {
	withTestColorProfile(t)
	p := NewPluginsPanel()
	got := p.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "╭") {
		t.Errorf("plugins panel should use Overlay primitive: %q", plain)
	}
	if !strings.Contains(plain, "Plugins") {
		t.Errorf("plugins panel should show title: %q", plain)
	}
}

func TestPluginsPanel_TabSwitch(t *testing.T) {
	p := NewPluginsPanel()
	updated, _ := p.Update(rightKey())
	v := updated.(PluginsPanel).View()
	if !strings.Contains(v, "[Browse]") {
		t.Errorf("view should show Browse tab active after Right:\n%s", v)
	}
	updated, _ = updated.(PluginsPanel).Update(rightKey())
	v = updated.(PluginsPanel).View()
	if !strings.Contains(v, "[Installed]") {
		t.Errorf("view should show Installed tab active after 2x Right:\n%s", v)
	}
	// Right at the last tab is a no-op.
	updated, _ = updated.(PluginsPanel).Update(rightKey())
	v = updated.(PluginsPanel).View()
	if !strings.Contains(v, "[Installed]") {
		t.Errorf("Right at last tab should stay on Installed:\n%s", v)
	}
	// Left walks back.
	updated, _ = updated.(PluginsPanel).Update(leftKey())
	v = updated.(PluginsPanel).View()
	if !strings.Contains(v, "[Browse]") {
		t.Errorf("view should show Browse tab active after Left from Installed:\n%s", v)
	}
}

func TestPluginsPanel_LoadingStates(t *testing.T) {
	p := NewPluginsPanel()
	v := p.View()
	if !strings.Contains(v, "Loading marketplaces") {
		t.Errorf("Marketplaces tab should show loading state initially:\n%s", v)
	}
	updated, _ := p.Update(rightKey())
	updated, _ = updated.(PluginsPanel).Update(rightKey())
	v = updated.(PluginsPanel).View()
	if !strings.Contains(v, "Loading plugins") {
		t.Errorf("Installed tab should show loading state initially:\n%s", v)
	}
}

func TestPluginsPanel_ErrorStates(t *testing.T) {
	p := NewPluginsPanel()
	updated, _ := p.Update(MarketplaceListResultMsg{Err: errors.New("boom")})
	v := updated.(PluginsPanel).View()
	if !strings.Contains(v, "Error: boom") {
		t.Errorf("Marketplaces tab should show error:\n%s", v)
	}

	p2 := NewPluginsPanel()
	updated2, _ := p2.Update(PluginListResultMsg{Err: errors.New("kaboom")})
	updated2, _ = updated2.(PluginsPanel).Update(rightKey())
	updated2, _ = updated2.(PluginsPanel).Update(rightKey())
	v2 := updated2.(PluginsPanel).View()
	if !strings.Contains(v2, "Error: kaboom") {
		t.Errorf("Installed tab should show error:\n%s", v2)
	}
}

// --- Marketplaces tab ---

func TestPluginsPanel_MarketplacesTab_RendersList(t *testing.T) {
	p := loadedMarketplaces(
		appwire.MarketplaceEntry{Name: "official", Source: appwire.MarketplaceSourceInput{Kind: "github", Repo: "anthropics/claude-plugins-official"}},
		appwire.MarketplaceEntry{Name: "local", Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: "/srv/plugins"}},
	)
	v := p.View()
	for _, want := range []string{"official", "github: anthropics/claude-plugins-official", "local", "/srv/plugins"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

func TestPluginsPanel_MarketplacesTab_EmptyState(t *testing.T) {
	p := loadedMarketplaces()
	v := p.View()
	if !strings.Contains(v, "No marketplaces registered") {
		t.Errorf("expected empty-state message:\n%s", v)
	}
}

func TestPluginsPanel_NOpensAddForm(t *testing.T) {
	p := loadedMarketplaces(appwire.MarketplaceEntry{Name: "official"})
	updated, _ := p.Update(runeKey("n"))
	p2 := updated.(PluginsPanel)
	if !p2.formOpen {
		t.Fatal("n key should open the add-marketplace form")
	}
	if p2.formKind != marketplaceKindURL {
		t.Errorf("form should default to url kind, got %q", p2.formKind)
	}
}

func TestPluginsPanel_AddForm_EscClosesFormOnly(t *testing.T) {
	p := loadedMarketplaces()
	updated, _ := p.Update(runeKey("n"))
	p2 := updated.(PluginsPanel)
	updated2, _ := p2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p3 := updated2.(PluginsPanel)
	if p3.formOpen {
		t.Error("esc should close the form")
	}
	if p3.done {
		t.Error("esc should NOT close the whole panel while only the form was open")
	}
}

func TestPluginsPanel_AddForm_CyclesKindAndSubmits(t *testing.T) {
	p := loadedMarketplaces()
	updated, _ := p.Update(runeKey("n"))
	p2 := updated.(PluginsPanel)

	// Cycle kind: url -> github -> directory -> url.
	updated, _ = p2.Update(tea.KeyMsg{Type: tea.KeySpace})
	p2 = updated.(PluginsPanel)
	if p2.formKind != marketplaceKindGitHub {
		t.Fatalf("after one space, kind = %q, want github", p2.formKind)
	}
	updated, _ = p2.Update(tea.KeyMsg{Type: tea.KeySpace})
	p2 = updated.(PluginsPanel)
	if p2.formKind != marketplaceKindDirectory {
		t.Fatalf("after two spaces, kind = %q, want directory", p2.formKind)
	}

	// Advance to the value field and type a path.
	updated, _ = p2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p2 = updated.(PluginsPanel)
	if p2.formField != 1 {
		t.Fatalf("Enter should advance to the value field, formField = %d", p2.formField)
	}
	for _, ch := range "/srv/plugins" {
		updated, _ = p2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		p2 = updated.(PluginsPanel)
	}

	// Submit.
	updated, cmd := p2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p2 = updated.(PluginsPanel)
	if p2.formOpen {
		t.Error("form should be closed after submit")
	}
	if cmd == nil {
		t.Fatal("submit should produce a cmd")
	}
	msg := cmd()
	got, ok := msg.(MarketplaceAddSubmitMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want MarketplaceAddSubmitMsg", msg)
	}
	want := appwire.MarketplaceSourceInput{Kind: "directory", Path: "/srv/plugins"}
	if got.Params.Source != want {
		t.Errorf("Params.Source = %+v, want %+v", got.Params.Source, want)
	}
	if got.Params.Name != "" {
		t.Errorf("Params.Name = %q, want empty (server derives it)", got.Params.Name)
	}
}

func TestPluginsPanel_RefreshEmitsMarketplaceRefreshMsg(t *testing.T) {
	p := loadedMarketplaces(appwire.MarketplaceEntry{Name: "official"})
	_, cmd := p.Update(runeKey("r"))
	if cmd == nil {
		t.Fatal("r key should produce a cmd")
	}
	got, ok := cmd().(MarketplaceRefreshMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want MarketplaceRefreshMsg", cmd())
	}
	if got.Name != "official" {
		t.Errorf("MarketplaceRefreshMsg.Name = %q, want official", got.Name)
	}
}

func TestPluginsPanel_RemoveEmitsMarketplaceRemoveMsg(t *testing.T) {
	p := loadedMarketplaces(appwire.MarketplaceEntry{Name: "official"})
	_, cmd := p.Update(runeKey("x"))
	if cmd == nil {
		t.Fatal("x key should produce a cmd")
	}
	got, ok := cmd().(MarketplaceRemoveMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want MarketplaceRemoveMsg", cmd())
	}
	if got.Name != "official" {
		t.Errorf("MarketplaceRemoveMsg.Name = %q, want official", got.Name)
	}
}

func TestPluginsPanel_MarketplaceListResultRefreshesPanel(t *testing.T) {
	p := loadedMarketplaces(appwire.MarketplaceEntry{Name: "official"})
	updated, _ := p.Update(MarketplaceListResultMsg{List: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{
		{Name: "official"}, {Name: "extra"},
	}}})
	v := updated.(PluginsPanel).View()
	if !strings.Contains(v, "extra") {
		t.Errorf("second MarketplaceListResultMsg should refresh panel; extra missing:\n%s", v)
	}
}

// --- navigation bounds ---

func TestPluginsPanel_NavigationClampsAtBounds(t *testing.T) {
	p := loadedMarketplaces(appwire.MarketplaceEntry{Name: "a"}, appwire.MarketplaceEntry{Name: "b"})
	// Up at 0 stays at 0.
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyUp})
	if updated.(PluginsPanel).cursor != 0 {
		t.Errorf("cursor after Up at 0 = %d, want 0", updated.(PluginsPanel).cursor)
	}
	// Down moves to 1, then clamps at the last row.
	updated, _ = updated.(PluginsPanel).Update(tea.KeyMsg{Type: tea.KeyDown})
	if updated.(PluginsPanel).cursor != 1 {
		t.Fatalf("cursor after Down = %d, want 1", updated.(PluginsPanel).cursor)
	}
	updated, _ = updated.(PluginsPanel).Update(tea.KeyMsg{Type: tea.KeyDown})
	if updated.(PluginsPanel).cursor != 1 {
		t.Errorf("cursor after Down past the end = %d, want clamped at 1", updated.(PluginsPanel).cursor)
	}
}

func TestPluginsPanel_CursorClampsWhenListShrinks(t *testing.T) {
	p := loadedMarketplaces(appwire.MarketplaceEntry{Name: "a"}, appwire.MarketplaceEntry{Name: "b"})
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p2 := updated.(PluginsPanel)
	if p2.cursor != 1 {
		t.Fatalf("setup: cursor = %d, want 1", p2.cursor)
	}
	updated, _ = p2.Update(MarketplaceListResultMsg{List: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{{Name: "a"}}}})
	p3 := updated.(PluginsPanel)
	if p3.cursor != 0 {
		t.Errorf("cursor should clamp to 0 when the list shrinks to 1 row, got %d", p3.cursor)
	}
}

// --- esc / close semantics ---

func TestPluginsPanel_EscClosesPanelOnMarketplacesTab(t *testing.T) {
	p := loadedMarketplaces()
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p2 := updated.(PluginsPanel)
	if !p2.done || !p2.cancelled {
		t.Errorf("esc on Marketplaces tab should close the panel: done=%v cancelled=%v", p2.done, p2.cancelled)
	}
}

// --- Browse tab ---

func browsePanel(marketplaces []appwire.MarketplaceEntry, installed []appwire.PluginEntry) PluginsPanel {
	p := NewPluginsPanel()
	updated, _ := p.Update(MarketplaceListResultMsg{List: appwire.MarketplaceListResponse{Marketplaces: marketplaces}})
	updated, _ = updated.(PluginsPanel).Update(PluginListResultMsg{List: appwire.PluginListResponse{Plugins: installed}})
	updated, _ = updated.(PluginsPanel).Update(rightKey()) // Marketplaces -> Browse
	return updated.(PluginsPanel)
}

func TestPluginsPanel_BrowseTab_PickerRendersMarketplaceNames(t *testing.T) {
	p := browsePanel([]appwire.MarketplaceEntry{{Name: "official"}, {Name: "local"}}, nil)
	v := p.View()
	if !strings.Contains(v, "official") || !strings.Contains(v, "local") {
		t.Errorf("Browse picker should list marketplace names:\n%s", v)
	}
	if p.BrowseMarketplace() != "" {
		t.Errorf("BrowseMarketplace() = %q, want empty while picking", p.BrowseMarketplace())
	}
}

func TestPluginsPanel_BrowseTab_EnterEmitsBrowseRequest(t *testing.T) {
	p := browsePanel([]appwire.MarketplaceEntry{{Name: "official"}}, nil)
	updated, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a marketplace row should produce a cmd")
	}
	got, ok := cmd().(MarketplaceBrowseRequestMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want MarketplaceBrowseRequestMsg", cmd())
	}
	if got.Name != "official" {
		t.Errorf("MarketplaceBrowseRequestMsg.Name = %q, want official", got.Name)
	}
	if updated.(PluginsPanel).BrowseMarketplace() != "official" {
		t.Errorf("BrowseMarketplace() = %q, want official (optimistic set before the result arrives)", updated.(PluginsPanel).BrowseMarketplace())
	}
}

func TestPluginsPanel_BrowseTab_ResultPopulatesCatalog(t *testing.T) {
	p := browsePanel([]appwire.MarketplaceEntry{{Name: "official"}}, nil)
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(PluginsPanel).Update(MarketplaceBrowseResultMsg{
		Name: "official",
		Response: appwire.MarketplaceBrowseResponse{Name: "official", Plugins: []appwire.MarketplaceCatalogPlugin{
			{Name: "hello-plugin", Description: "says hello"},
		}},
	})
	v := updated.(PluginsPanel).View()
	if !strings.Contains(v, "hello-plugin") || !strings.Contains(v, "says hello") {
		t.Errorf("catalog view should show fetched plugins:\n%s", v)
	}
}

func TestPluginsPanel_BrowseTab_StaleResultIgnored(t *testing.T) {
	p := browsePanel([]appwire.MarketplaceEntry{{Name: "official"}, {Name: "other"}}, nil)
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // browse "official"
	p2 := updated.(PluginsPanel)
	// Back out to the picker (simulating the user navigating away) before the
	// "official" response arrives.
	updated, _ = p2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p3 := updated.(PluginsPanel)
	if p3.BrowseMarketplace() != "" {
		t.Fatalf("setup: expected picker state, BrowseMarketplace() = %q", p3.BrowseMarketplace())
	}
	// The stale "official" response now arrives; it must not resurrect the catalog view.
	updated, _ = p3.Update(MarketplaceBrowseResultMsg{Name: "official", Response: appwire.MarketplaceBrowseResponse{Name: "official"}})
	p4 := updated.(PluginsPanel)
	if p4.BrowseMarketplace() != "" {
		t.Errorf("stale browse result should not reopen the catalog view, BrowseMarketplace() = %q", p4.BrowseMarketplace())
	}
}

func TestPluginsPanel_BrowseTab_InstallEmitsPluginAction(t *testing.T) {
	p := browsePanel([]appwire.MarketplaceEntry{{Name: "official"}}, nil)
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(PluginsPanel).Update(MarketplaceBrowseResultMsg{
		Name: "official",
		Response: appwire.MarketplaceBrowseResponse{Plugins: []appwire.MarketplaceCatalogPlugin{
			{Name: "hello-plugin"},
		}},
	})
	p2 := updated.(PluginsPanel)

	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, runeKey("i")} {
		_, cmd := p2.Update(key)
		if cmd == nil {
			t.Fatalf("%v should produce an install cmd", key)
		}
		got, ok := cmd().(PluginActionMsg)
		if !ok {
			t.Fatalf("cmd msg = %T, want PluginActionMsg", cmd())
		}
		if got.Action != "install" || got.Plugin != "hello-plugin" || got.Marketplace != "official" {
			t.Errorf("PluginActionMsg = %+v, want install hello-plugin@official", got)
		}
	}
}

func TestPluginsPanel_BrowseTab_AlreadyInstalledSkipsInstall(t *testing.T) {
	p := browsePanel(
		[]appwire.MarketplaceEntry{{Name: "official"}},
		[]appwire.PluginEntry{{Plugin: "hello-plugin", Marketplace: "official", Enabled: true}},
	)
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(PluginsPanel).Update(MarketplaceBrowseResultMsg{
		Name: "official",
		Response: appwire.MarketplaceBrowseResponse{Plugins: []appwire.MarketplaceCatalogPlugin{
			{Name: "hello-plugin"},
		}},
	})
	p2 := updated.(PluginsPanel)
	v := p2.View()
	// StatusBadge uppercases its label, so the badge renders as "INSTALLED".
	if !strings.Contains(v, "INSTALLED") {
		t.Errorf("catalog view should badge an already-installed plugin:\n%s", v)
	}
	_, cmd := p2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("enter on an already-installed catalog row should be a no-op")
	}
}

func TestPluginsPanel_BrowseTab_EscGoesBackToPickerNotClose(t *testing.T) {
	p := browsePanel([]appwire.MarketplaceEntry{{Name: "official"}}, nil)
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p2 := updated.(PluginsPanel)
	if p2.BrowseMarketplace() != "official" {
		t.Fatalf("setup: expected catalog state")
	}
	updated, _ = p2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p3 := updated.(PluginsPanel)
	if p3.done {
		t.Error("esc from the catalog view should not close the panel")
	}
	if p3.BrowseMarketplace() != "" {
		t.Errorf("esc from the catalog view should return to the picker, BrowseMarketplace() = %q", p3.BrowseMarketplace())
	}
	// A second esc, now from the picker, closes the panel.
	updated, _ = p3.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !updated.(PluginsPanel).done {
		t.Error("esc from the picker should close the panel")
	}
}

// --- Installed tab ---

func installedPanel(entries ...appwire.PluginEntry) PluginsPanel {
	p := loadedPlugins(entries...)
	updated, _ := p.Update(rightKey())
	updated, _ = updated.(PluginsPanel).Update(rightKey())
	return updated.(PluginsPanel)
}

func TestPluginsPanel_InstalledTab_RendersBadges(t *testing.T) {
	p := installedPanel(
		appwire.PluginEntry{Plugin: "broken-one", Marketplace: "mkt", Broken: true, Enabled: true},
		appwire.PluginEntry{Plugin: "disabled-one", Marketplace: "mkt", Enabled: false},
		appwire.PluginEntry{Plugin: "auto-one", Marketplace: "mkt", Enabled: true, AutoUpgrade: true, Version: "1.2.0"},
	)
	v := p.View()
	// StatusBadge uppercases its label; check for the badge text distinctly
	// from the plugin's own (lowercase) name so a name substring can't make
	// the assertion pass vacuously.
	for _, want := range []string{"broken-one", "BROKEN", "disabled-one", "DISABLED", "auto-one", "AUTO-UPGRADE", "v1.2.0"} {
		if !strings.Contains(v, want) {
			t.Errorf("installed view missing %q:\n%s", want, v)
		}
	}
}

func TestPluginsPanel_InstalledTab_EmptyState(t *testing.T) {
	p := installedPanel()
	v := p.View()
	if !strings.Contains(v, "No plugins installed") {
		t.Errorf("expected empty-state message:\n%s", v)
	}
}

func TestPluginsPanel_InstalledTab_EnterTogglesEnable(t *testing.T) {
	enabled := installedPanel(appwire.PluginEntry{Plugin: "p", Marketplace: "mkt", Enabled: true})
	_, cmd := enabled.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got, ok := cmd().(PluginActionMsg)
	if !ok || got.Action != "disable" {
		t.Errorf("enter on an enabled plugin should emit disable, got %+v (ok=%v)", got, ok)
	}

	disabled := installedPanel(appwire.PluginEntry{Plugin: "p", Marketplace: "mkt", Enabled: false})
	_, cmd = disabled.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got, ok = cmd().(PluginActionMsg)
	if !ok || got.Action != "enable" {
		t.Errorf("enter on a disabled plugin should emit enable, got %+v (ok=%v)", got, ok)
	}
}

func TestPluginsPanel_InstalledTab_AEmitsAutoUpgradeToggle(t *testing.T) {
	p := installedPanel(appwire.PluginEntry{Plugin: "p", Marketplace: "mkt", AutoUpgrade: false})
	_, cmd := p.Update(runeKey("a"))
	got, ok := cmd().(PluginSetAutoUpgradeMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want PluginSetAutoUpgradeMsg", cmd())
	}
	if !got.AutoUpgrade || got.Plugin != "p" || got.Marketplace != "mkt" {
		t.Errorf("PluginSetAutoUpgradeMsg = %+v, want AutoUpgrade=true for p@mkt", got)
	}
}

func TestPluginsPanel_InstalledTab_UEmitsUpgrade(t *testing.T) {
	p := installedPanel(appwire.PluginEntry{Plugin: "p", Marketplace: "mkt"})
	_, cmd := p.Update(runeKey("u"))
	got, ok := cmd().(PluginActionMsg)
	if !ok || got.Action != "upgrade" || got.Plugin != "p" || got.Marketplace != "mkt" {
		t.Errorf("u should emit upgrade for p@mkt, got %+v (ok=%v)", got, ok)
	}
}

func TestPluginsPanel_InstalledTab_XEmitsRemove(t *testing.T) {
	p := installedPanel(appwire.PluginEntry{Plugin: "p", Marketplace: "mkt"})
	_, cmd := p.Update(runeKey("x"))
	got, ok := cmd().(PluginActionMsg)
	if !ok || got.Action != "remove" || got.Plugin != "p" || got.Marketplace != "mkt" {
		t.Errorf("x should emit remove for p@mkt, got %+v (ok=%v)", got, ok)
	}
}

func TestPluginsPanel_PluginListResultRefreshesPanel(t *testing.T) {
	p := installedPanel(appwire.PluginEntry{Plugin: "p", Marketplace: "mkt"})
	updated, _ := p.Update(PluginListResultMsg{List: appwire.PluginListResponse{Plugins: []appwire.PluginEntry{
		{Plugin: "p", Marketplace: "mkt"}, {Plugin: "q", Marketplace: "mkt"},
	}}})
	v := updated.(PluginsPanel).View()
	if !strings.Contains(v, "q @ mkt") {
		t.Errorf("second PluginListResultMsg should refresh panel; q missing:\n%s", v)
	}
}

// --- small helpers ---

func TestMarketplaceSourceLabel(t *testing.T) {
	tests := []struct {
		name string
		src  appwire.MarketplaceSourceInput
		want string
	}{
		{"github", appwire.MarketplaceSourceInput{Kind: "github", Repo: "owner/repo"}, "github: owner/repo"},
		{"url", appwire.MarketplaceSourceInput{Kind: "url", URL: "https://example.com/mkt.git"}, "https://example.com/mkt.git"},
		{"directory", appwire.MarketplaceSourceInput{Kind: "directory", Path: "/srv/mkt"}, "/srv/mkt"},
		{"git-subdir", appwire.MarketplaceSourceInput{Kind: "git-subdir", URL: "https://x.git", Path: "sub"}, "https://x.git (sub)"},
		{"unknown", appwire.MarketplaceSourceInput{Kind: "npm"}, "npm"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := marketplaceSourceLabel(tc.src); got != tc.want {
				t.Errorf("marketplaceSourceLabel(%+v) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

func TestVersionOrUnknown(t *testing.T) {
	if got := versionOrUnknown(""); got != "unknown" {
		t.Errorf("versionOrUnknown(\"\") = %q, want unknown", got)
	}
	if got := versionOrUnknown("   "); got != "unknown" {
		t.Errorf("versionOrUnknown(whitespace) = %q, want unknown", got)
	}
	if got := versionOrUnknown("1.0.0"); got != "1.0.0" {
		t.Errorf("versionOrUnknown(1.0.0) = %q, want 1.0.0", got)
	}
}

func TestNextMarketplaceKind(t *testing.T) {
	seq := []string{marketplaceKindURL, marketplaceKindGitHub, marketplaceKindDirectory, marketplaceKindURL}
	for i := 0; i < len(seq)-1; i++ {
		if got := nextMarketplaceKind(seq[i]); got != seq[i+1] {
			t.Errorf("nextMarketplaceKind(%q) = %q, want %q", seq[i], got, seq[i+1])
		}
	}
	if got := nextMarketplaceKind("bogus"); got != marketplaceKindURL {
		t.Errorf("nextMarketplaceKind(bogus) = %q, want url (default)", got)
	}
}

func TestPluginsPanel_FormSource(t *testing.T) {
	tests := []struct {
		kind  string
		value string
		want  appwire.MarketplaceSourceInput
	}{
		{marketplaceKindURL, " https://x.git ", appwire.MarketplaceSourceInput{Kind: "url", URL: "https://x.git"}},
		{marketplaceKindGitHub, "owner/repo", appwire.MarketplaceSourceInput{Kind: "github", Repo: "owner/repo"}},
		{marketplaceKindDirectory, "/srv/mkt", appwire.MarketplaceSourceInput{Kind: "directory", Path: "/srv/mkt"}},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			p := PluginsPanel{formKind: tc.kind, formValue: tc.value}
			if got := p.formSource(); got != tc.want {
				t.Errorf("formSource() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
