package launchconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

// --- Init / Done ---

func TestCovPluginsPanelInit(t *testing.T) {
	p := NewPluginsPanel()
	if p.Init() != nil {
		t.Fatal("Init should return nil")
	}
}

func TestCovPluginsPanelDone(t *testing.T) {
	p := PluginsPanel{done: true}
	if !p.Done() {
		t.Fatal("Done should be true")
	}
	p2 := NewPluginsPanel()
	if p2.Done() {
		t.Fatal("Done should be false on new panel")
	}
}

// --- clampCursor ---

func TestCovClampCursor(t *testing.T) {
	if got := clampCursor(5, 0); got != 0 {
		t.Errorf("clampCursor(5,0) = %d, want 0", got)
	}
	if got := clampCursor(-1, 3); got != 0 {
		t.Errorf("clampCursor(-1,3) = %d, want 0", got)
	}
	if got := clampCursor(10, 3); got != 2 {
		t.Errorf("clampCursor(10,3) = %d, want 2", got)
	}
	if got := clampCursor(1, 3); got != 1 {
		t.Errorf("clampCursor(1,3) = %d, want 1", got)
	}
}

// --- maxCursor ---

func TestCovMaxCursor(t *testing.T) {
	p := PluginsPanel{marketplaces: []appwire.MarketplaceEntry{{}, {}}}
	if got := p.maxCursor(); got != 2 {
		t.Errorf("marketplaces maxCursor = %d, want 2", got)
	}
	p = PluginsPanel{tab: pluginsTabInstalled, plugins: []appwire.PluginEntry{{}, {}, {}}}
	if got := p.maxCursor(); got != 3 {
		t.Errorf("plugins maxCursor = %d, want 3", got)
	}
	// Browse tab with picker showing
	p = PluginsPanel{tab: pluginsTabBrowse, marketplaces: []appwire.MarketplaceEntry{{}, {}, {}, {}}}
	if got := p.maxCursor(); got != 4 {
		t.Errorf("browse picker maxCursor = %d, want 4", got)
	}
	// Browse tab with catalog showing
	p = PluginsPanel{tab: pluginsTabBrowse, browseMarketplace: "x", browseCatalog: appwire.MarketplaceBrowseResponse{Plugins: []appwire.MarketplaceCatalogPlugin{{}, {}}}}
	if got := p.maxCursor(); got != 2 {
		t.Errorf("browse catalog maxCursor = %d, want 2", got)
	}
}

// --- selectedMarketplaceName / selectedPlugin / selectedCatalogPlugin ---

func TestCovSelectedMarketplaceName(t *testing.T) {
	p := PluginsPanel{marketplaces: []appwire.MarketplaceEntry{{Name: "a"}, {Name: "b"}}}
	name, ok := p.selectedMarketplaceName()
	if !ok || name != "a" {
		t.Fatalf("selectedMarketplaceName = %q %v, want a true", name, ok)
	}
	p2 := PluginsPanel{cursor: -1}
	_, ok2 := p2.selectedMarketplaceName()
	if ok2 {
		t.Fatal("selectedMarketplaceName with negative cursor should be false")
	}
	p3 := PluginsPanel{cursor: 5, marketplaces: []appwire.MarketplaceEntry{{Name: "a"}}}
	_, ok3 := p3.selectedMarketplaceName()
	if ok3 {
		t.Fatal("selectedMarketplaceName with cursor >= len should be false")
	}
}

func TestCovSelectedPlugin(t *testing.T) {
	p := PluginsPanel{plugins: []appwire.PluginEntry{{Plugin: "x", Marketplace: "m"}}}
	entry, ok := p.selectedPlugin()
	if !ok || entry.Plugin != "x" {
		t.Fatalf("selectedPlugin = %+v ok=%v", entry, ok)
	}
	p2 := PluginsPanel{cursor: 5}
	_, ok2 := p2.selectedPlugin()
	if ok2 {
		t.Fatal("selectedPlugin with cursor out of range should be false")
	}
}

func TestCovSelectedCatalogPlugin(t *testing.T) {
	p := PluginsPanel{browseCatalog: appwire.MarketplaceBrowseResponse{Plugins: []appwire.MarketplaceCatalogPlugin{{Name: "cat"}}}}
	cp, ok := p.selectedCatalogPlugin()
	if !ok || cp.Name != "cat" {
		t.Fatalf("selectedCatalogPlugin = %+v ok=%v", cp, ok)
	}
	p2 := PluginsPanel{cursor: -1}
	_, ok2 := p2.selectedCatalogPlugin()
	if ok2 {
		t.Fatal("selectedCatalogPlugin with negative cursor should be false")
	}
}

// --- installSelectedCatalogPlugin ---

func TestCovInstallSelectedCatalogPluginAlreadyInstalled(t *testing.T) {
	p := PluginsPanel{
		browseMarketplace: "mp",
		browseCatalog:     appwire.MarketplaceBrowseResponse{Plugins: []appwire.MarketplaceCatalogPlugin{{Name: "dup"}}},
		plugins:           []appwire.PluginEntry{{Plugin: "dup", Marketplace: "mp"}},
	}
	_, cmd := p.installSelectedCatalogPlugin()
	if cmd != nil {
		t.Fatal("install of already-installed plugin should return nil cmd")
	}
}

func TestCovInstallSelectedCatalogPluginNoSelection(t *testing.T) {
	p := PluginsPanel{cursor: 10}
	_, cmd := p.installSelectedCatalogPlugin()
	if cmd != nil {
		t.Fatal("install with no selection should return nil cmd")
	}
}

// --- handleEnter ---

func TestCovHandleEnterMarketplacesTab(t *testing.T) {
	// Enter on marketplaces tab does nothing
	p := PluginsPanel{tab: pluginsTabMarketplaces}
	_, cmd := p.handleEnter()
	if cmd != nil {
		t.Fatal("Enter on marketplaces tab should return nil cmd")
	}
}

func TestCovHandleEnterBrowsePickerNoSelection(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabBrowse, cursor: 5}
	_, cmd := p.handleEnter()
	if cmd != nil {
		t.Fatal("Enter on browse picker with no selection should return nil cmd")
	}
}

func TestCovHandleEnterInstalledNoSelection(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabInstalled, cursor: 5}
	_, cmd := p.handleEnter()
	if cmd != nil {
		t.Fatal("Enter on installed with no selection should return nil cmd")
	}
}

// --- handleRune ---

func TestCovHandleRuneMarketplaceRemove(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabMarketplaces, marketplaces: []appwire.MarketplaceEntry{{Name: "toRemove"}}}
	_, cmd := p.handleRune("x")
	if cmd == nil {
		t.Fatal("x on marketplaces should produce a remove cmd")
	}
	msg := cmd()
	if rm, ok := msg.(MarketplaceRemoveMsg); !ok || rm.Name != "toRemove" {
		t.Fatalf("msg = %+v", msg)
	}
}

func TestCovHandleRuneMarketplaceRemoveNoSelection(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabMarketplaces, cursor: 5}
	_, cmd := p.handleRune("x")
	if cmd != nil {
		t.Fatal("x with no selection should return nil cmd")
	}
}

func TestCovHandleRuneMarketplaceRefreshNoSelection(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabMarketplaces, cursor: 5}
	_, cmd := p.handleRune("r")
	if cmd != nil {
		t.Fatal("r with no selection should return nil cmd")
	}
}

func TestCovHandleRuneInstalledNoSelection(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabInstalled, cursor: 5}
	_, cmd := p.handleRune("a")
	if cmd != nil {
		t.Fatal("a with no selection should return nil cmd")
	}
}

func TestCovHandleRuneUnknown(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabMarketplaces, marketplaces: []appwire.MarketplaceEntry{{Name: "x"}}}
	updated, cmd := p.handleRune("z")
	if cmd != nil {
		t.Fatal("unknown rune should return nil cmd")
	}
	if !reflect.DeepEqual(updated, p) {
		t.Fatalf("unknown rune changed panel to %+v, want %+v", updated, p)
	}
}

// --- updateForm ---

func TestCovUpdateFormTabCyclesKind(t *testing.T) {
	p := PluginsPanel{formOpen: true, formField: 0, formKind: marketplaceKindURL}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyTab})
	p2 := updated.(PluginsPanel)
	if p2.formKind != marketplaceKindGitHub {
		t.Fatalf("Tab should cycle kind to github, got %q", p2.formKind)
	}
}

func TestCovUpdateFormSpaceKeyCyclesKind(t *testing.T) {
	p := PluginsPanel{formOpen: true, formField: 0, formKind: marketplaceKindGitHub}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeySpace})
	p2 := updated.(PluginsPanel)
	if p2.formKind != marketplaceKindDirectory {
		t.Fatalf("Space should cycle kind to directory, got %q", p2.formKind)
	}
}

func TestCovUpdateFormBackspaceOnValueField(t *testing.T) {
	p := PluginsPanel{formOpen: true, formField: 1, formValue: "abc"}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	p2 := updated.(PluginsPanel)
	if p2.formValue != "ab" {
		t.Fatalf("backspace should delete last char, got %q", p2.formValue)
	}
}

func TestCovUpdateFormBackspaceOnKindField(t *testing.T) {
	p := PluginsPanel{formOpen: true, formField: 0, formKind: marketplaceKindURL}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	p2 := updated.(PluginsPanel)
	if p2.formKind != marketplaceKindURL {
		t.Fatalf("backspace on kind field should not change kind, got %q", p2.formKind)
	}
}

func TestCovUpdateFormRunesOnKindFieldSpace(t *testing.T) {
	p := PluginsPanel{formOpen: true, formField: 0, formKind: marketplaceKindDirectory}
	updated, _ := p.Update(runeKey(" "))
	p2 := updated.(PluginsPanel)
	if p2.formKind != marketplaceKindURL {
		t.Fatalf("space rune on kind should cycle to url, got %q", p2.formKind)
	}
}

func TestCovUpdateFormRunesOnValueField(t *testing.T) {
	p := PluginsPanel{formOpen: true, formField: 1, formValue: ""}
	updated, _ := p.Update(runeKey("h"))
	p2 := updated.(PluginsPanel)
	if p2.formValue != "h" {
		t.Fatalf("rune on value field should append, got %q", p2.formValue)
	}
}

func TestCovUpdateFormEnterAdvanceField(t *testing.T) {
	p := PluginsPanel{formOpen: true, formField: 0, formKind: marketplaceKindURL}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p2 := updated.(PluginsPanel)
	if p2.formField != 1 {
		t.Fatalf("Enter should advance formField to 1, got %d", p2.formField)
	}
}

// --- nextMarketplaceKind ---

func TestCovNextMarketplaceKind(t *testing.T) {
	if got := nextMarketplaceKind(marketplaceKindURL); got != marketplaceKindGitHub {
		t.Fatalf("url -> %q, want github", got)
	}
	if got := nextMarketplaceKind(marketplaceKindGitHub); got != marketplaceKindDirectory {
		t.Fatalf("github -> %q, want directory", got)
	}
	if got := nextMarketplaceKind(marketplaceKindDirectory); got != marketplaceKindURL {
		t.Fatalf("directory -> %q, want url", got)
	}
	if got := nextMarketplaceKind("unknown"); got != marketplaceKindURL {
		t.Fatalf("unknown -> %q, want url", got)
	}
}

// --- formSource ---

func TestCovFormSource(t *testing.T) {
	p := PluginsPanel{formKind: marketplaceKindURL, formValue: "  https://x  "}
	src := p.formSource()
	if want := (appwire.MarketplaceSourceInput{Kind: "url", URL: "https://x"}); src != want {
		t.Fatalf("url formSource = %+v, want %+v", src, want)
	}
	p2 := PluginsPanel{formKind: marketplaceKindGitHub, formValue: "owner/repo"}
	src2 := p2.formSource()
	if want := (appwire.MarketplaceSourceInput{Kind: "github", Repo: "owner/repo"}); src2 != want {
		t.Fatalf("github formSource = %+v, want %+v", src2, want)
	}
	p3 := PluginsPanel{formKind: marketplaceKindDirectory, formValue: "/path"}
	src3 := p3.formSource()
	if want := (appwire.MarketplaceSourceInput{Kind: "directory", Path: "/path"}); src3 != want {
		t.Fatalf("directory formSource = %+v, want %+v", src3, want)
	}
}

// --- formView / formValueLabel / formFieldLine ---

func TestCovFormView(t *testing.T) {
	p := PluginsPanel{formOpen: true, formKind: marketplaceKindURL, formValue: "x"}
	v := p.formView()
	if !strings.Contains(v, "Add marketplace") {
		t.Fatalf("formView missing title: %q", v)
	}
	if !strings.Contains(v, "URL") {
		t.Fatalf("formView missing URL label: %q", v)
	}
}

func TestCovFormValueLabel(t *testing.T) {
	p := PluginsPanel{formKind: marketplaceKindGitHub}
	if got := p.formValueLabel(); got != "owner/repo" {
		t.Fatalf("github label = %q, want owner/repo", got)
	}
	p2 := PluginsPanel{formKind: marketplaceKindDirectory}
	if got := p2.formValueLabel(); got != "Path" {
		t.Fatalf("directory label = %q, want Path", got)
	}
	p3 := PluginsPanel{formKind: marketplaceKindURL}
	if got := p3.formValueLabel(); got != "URL" {
		t.Fatalf("url label = %q, want URL", got)
	}
}

func TestCovFormFieldLine(t *testing.T) {
	withTestColorProfile(t)
	p := PluginsPanel{formOpen: true, formField: 1}
	line := p.formFieldLine("test", 0, "val")
	if !strings.Contains(line, "test") {
		t.Fatalf("formFieldLine missing label: %q", line)
	}
}

// --- footerFor ---

func TestCovFooterFor(t *testing.T) {
	p := PluginsPanel{formOpen: true}
	f := p.footerFor(80)
	if !strings.Contains(f, "submit") {
		t.Fatalf("form footer missing submit: %q", f)
	}

	p2 := PluginsPanel{tab: pluginsTabBrowse, browseMarketplace: "x"}
	f2 := p2.footerFor(80)
	if !strings.Contains(f2, "install") {
		t.Fatalf("browse catalog footer missing install: %q", f2)
	}

	p3 := PluginsPanel{tab: pluginsTabInstalled}
	f3 := p3.footerFor(80)
	if !strings.Contains(f3, "auto-upgrade") {
		t.Fatalf("installed footer missing auto-upgrade: %q", f3)
	}
}

// --- View with form open ---

func TestCovPluginsPanelViewWithForm(t *testing.T) {
	withTestColorProfile(t)
	p := PluginsPanel{formOpen: true, formKind: marketplaceKindURL, formValue: "test"}
	v := p.View()
	if !strings.Contains(v, "Add marketplace") {
		t.Fatalf("View with form open should show form: %q", v)
	}
}

// --- Update: MarketplaceBrowseResultMsg stale ---

func TestCovPluginsPanelStaleBrowseResult(t *testing.T) {
	wantCatalog := appwire.MarketplaceBrowseResponse{Name: "current", Plugins: []appwire.MarketplaceCatalogPlugin{{Name: "keep"}}}
	wantErr := errors.New("keep error")
	p := PluginsPanel{browseMarketplace: "current", browseLoading: true, browseCatalog: wantCatalog, browseErr: wantErr, cursor: 3}
	updated, _ := p.Update(MarketplaceBrowseResultMsg{
		Name:     "stale",
		Response: appwire.MarketplaceBrowseResponse{Name: "stale", Plugins: []appwire.MarketplaceCatalogPlugin{{Name: "discard"}}},
		Err:      errors.New("discard error"),
	})
	p2 := updated.(PluginsPanel)
	if p2.browseMarketplace != "current" || !p2.browseLoading || !reflect.DeepEqual(p2.browseCatalog, wantCatalog) || p2.browseErr != wantErr || p2.cursor != 3 {
		t.Fatalf("stale browse result changed state: marketplace=%q loading=%v catalog=%+v err=%v cursor=%d", p2.browseMarketplace, p2.browseLoading, p2.browseCatalog, p2.browseErr, p2.cursor)
	}
}

// --- Update: browse result with error ---

func TestCovPluginsPanelBrowseError(t *testing.T) {
	wantCatalog := appwire.MarketplaceBrowseResponse{Name: "old", Plugins: []appwire.MarketplaceCatalogPlugin{{Name: "keep"}}}
	wantErr := errors.New("fail")
	p := PluginsPanel{browseMarketplace: "mp", browseLoading: true, browseCatalog: wantCatalog}
	updated, _ := p.Update(MarketplaceBrowseResultMsg{Name: "mp", Response: appwire.MarketplaceBrowseResponse{Name: "discard"}, Err: wantErr})
	p2 := updated.(PluginsPanel)
	if p2.browseErr != wantErr || p2.browseLoading || !reflect.DeepEqual(p2.browseCatalog, wantCatalog) {
		t.Fatalf("browse error state = err %v loading=%v catalog=%+v", p2.browseErr, p2.browseLoading, p2.browseCatalog)
	}
}

// --- Update: browse result success ---

func TestCovPluginsPanelBrowseSuccess(t *testing.T) {
	p := PluginsPanel{browseMarketplace: "mp", browseLoading: true}
	resp := appwire.MarketplaceBrowseResponse{Plugins: []appwire.MarketplaceCatalogPlugin{{Name: "p1"}}}
	updated, _ := p.Update(MarketplaceBrowseResultMsg{Name: "mp", Response: resp})
	p2 := updated.(PluginsPanel)
	if p2.browseLoading {
		t.Fatal("browseLoading should be false after success")
	}
	if p2.browseErr != nil || !reflect.DeepEqual(p2.browseCatalog, resp) {
		t.Fatalf("browse success state = err %v catalog=%+v, want %+v", p2.browseErr, p2.browseCatalog, resp)
	}
}

// --- updateKeys: Esc from catalog goes back to picker ---

func TestCovPluginsPanelEscFromBrowseCatalog(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabBrowse, browseMarketplace: "mp", browseCatalog: appwire.MarketplaceBrowseResponse{Plugins: []appwire.MarketplaceCatalogPlugin{{}}}}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p2 := updated.(PluginsPanel)
	if p2.browseMarketplace != "" {
		t.Fatal("Esc from catalog should clear browseMarketplace")
	}
	if p2.done {
		t.Fatal("Esc from catalog should NOT close panel")
	}
}

// --- updateKeys: Up/Down navigation ---

func TestCovPluginsPanelUpDown(t *testing.T) {
	p := loadedMarketplaces(appwire.MarketplaceEntry{Name: "a"}, appwire.MarketplaceEntry{Name: "b"})
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p2 := updated.(PluginsPanel)
	if p2.cursor != 1 {
		t.Fatalf("Down should move cursor to 1, got %d", p2.cursor)
	}
	updated, _ = p2.Update(tea.KeyMsg{Type: tea.KeyUp})
	p3 := updated.(PluginsPanel)
	if p3.cursor != 0 {
		t.Fatalf("Up should move cursor to 0, got %d", p3.cursor)
	}
	// Up at 0 is no-op
	updated, _ = p3.Update(tea.KeyMsg{Type: tea.KeyUp})
	p4 := updated.(PluginsPanel)
	if p4.cursor != 0 {
		t.Fatalf("Up at 0 should stay 0, got %d", p4.cursor)
	}
}

// --- handleRune: installed tab actions ---

func TestCovHandleRuneInstalledAutoUpgrade(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabInstalled, plugins: []appwire.PluginEntry{{Plugin: "p", Marketplace: "mp", AutoUpgrade: false}}}
	_, cmd := p.handleRune("a")
	if cmd == nil {
		t.Fatal("a should produce a cmd")
	}
	msg := cmd().(PluginSetAutoUpgradeMsg)
	want := PluginSetAutoUpgradeMsg{Plugin: "p", Marketplace: "mp", AutoUpgrade: true}
	if msg != want {
		t.Fatalf("message = %+v, want %+v", msg, want)
	}
}

func TestCovHandleRuneInstalledUpgrade(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabInstalled, plugins: []appwire.PluginEntry{{Plugin: "p", Marketplace: "mp"}}}
	_, cmd := p.handleRune("u")
	if cmd == nil {
		t.Fatal("u should produce a cmd")
	}
	msg := cmd().(PluginActionMsg)
	want := PluginActionMsg{Action: "upgrade", Plugin: "p", Marketplace: "mp"}
	if msg != want {
		t.Fatalf("message = %+v, want %+v", msg, want)
	}
}

func TestCovHandleRuneInstalledRemove(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabInstalled, plugins: []appwire.PluginEntry{{Plugin: "p", Marketplace: "mp"}}}
	_, cmd := p.handleRune("x")
	if cmd == nil {
		t.Fatal("x should produce a cmd")
	}
	msg := cmd().(PluginActionMsg)
	want := PluginActionMsg{Action: "remove", Plugin: "p", Marketplace: "mp"}
	if msg != want {
		t.Fatalf("message = %+v, want %+v", msg, want)
	}
}

// --- handleEnter: installed enable/disable ---

func TestCovHandleEnterInstalledDisable(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabInstalled, plugins: []appwire.PluginEntry{{Plugin: "p", Marketplace: "mp", Enabled: true}}}
	_, cmd := p.handleEnter()
	if cmd == nil {
		t.Fatal("Enter on enabled plugin should produce a cmd")
	}
	msg := cmd().(PluginActionMsg)
	want := PluginActionMsg{Action: "disable", Plugin: "p", Marketplace: "mp"}
	if msg != want {
		t.Fatalf("message = %+v, want %+v", msg, want)
	}
}

func TestCovHandleEnterInstalledEnable(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabInstalled, plugins: []appwire.PluginEntry{{Plugin: "p", Marketplace: "mp", Enabled: false}}}
	_, cmd := p.handleEnter()
	if cmd == nil {
		t.Fatal("Enter on disabled plugin should produce a cmd")
	}
	msg := cmd().(PluginActionMsg)
	want := PluginActionMsg{Action: "enable", Plugin: "p", Marketplace: "mp"}
	if msg != want {
		t.Fatalf("message = %+v, want %+v", msg, want)
	}
}

// --- renderBrowseTab: empty catalog ---

func TestCovRenderBrowseTabEmptyCatalog(t *testing.T) {
	p := PluginsPanel{browseMarketplace: "mp", browseCatalog: appwire.MarketplaceBrowseResponse{}}
	v := p.renderBrowseTab()
	if !strings.Contains(v, "no plugins") {
		t.Fatalf("empty catalog should show 'no plugins': %q", v)
	}
}

// --- renderBrowseTab: catalog with installed badge ---

func TestCovRenderBrowseTabInstalledBadge(t *testing.T) {
	withTestColorProfile(t)
	p := PluginsPanel{
		browseMarketplace: "mp",
		browseCatalog:     appwire.MarketplaceBrowseResponse{Plugins: []appwire.MarketplaceCatalogPlugin{{Name: "formatter", Description: "d"}}},
		plugins:           []appwire.PluginEntry{{Plugin: "formatter", Marketplace: "mp"}},
	}
	v := p.renderBrowseTab()
	if !strings.Contains(v, "formatter") || !strings.Contains(v, "INSTALLED") {
		t.Fatalf("browse tab should show the plugin and independent installed badge: %q", v)
	}
}

// --- renderBrowseTab: browse error ---

func TestCovRenderBrowseTabError(t *testing.T) {
	p := PluginsPanel{browseMarketplace: "mp", browseErr: errors.New("fetch failed")}
	v := p.renderBrowseTab()
	if !strings.Contains(v, "Error: fetch failed") {
		t.Fatalf("browse tab should show error: %q", v)
	}
}

// --- renderBrowseTab: loading ---

func TestCovRenderBrowseTabLoading(t *testing.T) {
	p := PluginsPanel{browseMarketplace: "mp", browseLoading: true}
	v := p.renderBrowseTab()
	if !strings.Contains(v, "Loading catalog") {
		t.Fatalf("browse tab should show loading: %q", v)
	}
}

// --- renderBrowseTab: no marketplaces ---

func TestCovRenderBrowseTabNoMarketplaces(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabBrowse}
	v := p.renderBrowseTab()
	if !strings.Contains(v, "No marketplaces registered") {
		t.Fatalf("browse tab with no marketplaces should show empty state: %q", v)
	}
}

// --- renderInstalledTab: empty/error ---

func TestCovRenderInstalledTabEmpty(t *testing.T) {
	p := PluginsPanel{}
	v := p.renderInstalledTab()
	if !strings.Contains(v, "No plugins installed") {
		t.Fatalf("empty installed tab: %q", v)
	}
}

func TestCovRenderInstalledTabError(t *testing.T) {
	p := PluginsPanel{pluginsErr: errors.New("load fail")}
	v := p.renderInstalledTab()
	if !strings.Contains(v, "Error: load fail") {
		t.Fatalf("error installed tab: %q", v)
	}
}

// --- renderInstalledTab: badges ---

func TestCovRenderInstalledTabBadges(t *testing.T) {
	withTestColorProfile(t)
	p := PluginsPanel{plugins: []appwire.PluginEntry{
		{Plugin: "alpha", Marketplace: "mp", Broken: true, Enabled: true, Version: "1.0"},
		{Plugin: "beta", Marketplace: "mp", Enabled: false, Version: ""},
		{Plugin: "gamma", Marketplace: "mp", Enabled: true, AutoUpgrade: true, Version: "2.0"},
	}}
	v := p.renderInstalledTab()
	for _, want := range []string{"alpha", "beta", "gamma", "BROKEN", "DISABLED", "AUTO-UPGRADE", "unknown"} {
		if !strings.Contains(v, want) {
			t.Fatalf("installed rows missing %q: %q", want, v)
		}
	}
}

// --- View for each tab ---

func TestCovPluginsPanelViewAllTabs(t *testing.T) {
	withTestColorProfile(t)
	// Browse tab view
	p := loadedMarketplaces(appwire.MarketplaceEntry{Name: "mp"})
	p.tab = pluginsTabBrowse
	v := p.View()
	if !strings.Contains(v, "Browse") {
		t.Fatalf("browse tab view should show Browse: %q", v)
	}

	// Installed tab view
	p2 := loadedPlugins(appwire.PluginEntry{Plugin: "p", Marketplace: "mp"})
	v2 := p2.View()
	if !strings.Contains(v2, "Installed") {
		t.Fatalf("installed tab view should show Installed: %q", v2)
	}
}

// --- Update: PluginListResultMsg with error ---

func TestCovPluginsPanelPluginListError(t *testing.T) {
	wantPlugins := []appwire.PluginEntry{{Plugin: "keep", Marketplace: "mp"}}
	wantErr := errors.New("plugin load error")
	p := PluginsPanel{plugins: wantPlugins, loadingPlugins: true}
	updated, _ := p.Update(PluginListResultMsg{List: appwire.PluginListResponse{Plugins: []appwire.PluginEntry{{Plugin: "discard"}}}, Err: wantErr})
	p2 := updated.(PluginsPanel)
	if p2.pluginsErr != wantErr || p2.loadingPlugins || !reflect.DeepEqual(p2.plugins, wantPlugins) {
		t.Fatalf("plugin list error state = err %v loading=%v plugins=%+v", p2.pluginsErr, p2.loadingPlugins, p2.plugins)
	}
}

// --- handleRune: browse tab install via 'i' ---

func TestCovHandleRuneBrowseInstall(t *testing.T) {
	p := PluginsPanel{
		tab:               pluginsTabBrowse,
		browseMarketplace: "mp",
		browseCatalog:     appwire.MarketplaceBrowseResponse{Plugins: []appwire.MarketplaceCatalogPlugin{{Name: "new"}}},
	}
	_, cmd := p.handleRune("i")
	if cmd == nil {
		t.Fatal("i on browse catalog should produce install cmd")
	}
	msg := cmd().(PluginActionMsg)
	want := PluginActionMsg{Action: "install", Plugin: "new", Marketplace: "mp"}
	if msg != want {
		t.Fatalf("msg = %+v, want %+v", msg, want)
	}
}

// --- handleRune: browse picker (no marketplace selected) 'i' does nothing ---

func TestCovHandleRuneBrowsePickerNoInstall(t *testing.T) {
	p := PluginsPanel{tab: pluginsTabBrowse, browseMarketplace: ""}
	_, cmd := p.handleRune("i")
	if cmd != nil {
		t.Fatal("i on browse picker should return nil cmd")
	}
}

// --- Update: MarketplaceListResultMsg with error ---

func TestCovPluginsPanelMarketplaceListError(t *testing.T) {
	wantMarketplaces := []appwire.MarketplaceEntry{{Name: "keep"}}
	wantErr := errors.New("marketplace error")
	p := PluginsPanel{marketplaces: wantMarketplaces, loadingMarketplaces: true}
	updated, _ := p.Update(MarketplaceListResultMsg{List: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{{Name: "discard"}}}, Err: wantErr})
	p2 := updated.(PluginsPanel)
	if p2.marketplacesErr != wantErr || p2.loadingMarketplaces || !reflect.DeepEqual(p2.marketplaces, wantMarketplaces) {
		t.Fatalf("marketplace list error state = err %v loading=%v marketplaces=%+v", p2.marketplacesErr, p2.loadingMarketplaces, p2.marketplaces)
	}
}

// --- marketplaceSourceLabel ---

func TestCovMarketplaceSourceLabel(t *testing.T) {
	if got := marketplaceSourceLabel(appwire.MarketplaceSourceInput{Kind: "github", Repo: "o/r"}); got != "github: o/r" {
		t.Fatalf("github label = %q", got)
	}
	if got := marketplaceSourceLabel(appwire.MarketplaceSourceInput{Kind: "url", URL: "https://x"}); got != "https://x" {
		t.Fatalf("url label = %q", got)
	}
	if got := marketplaceSourceLabel(appwire.MarketplaceSourceInput{Kind: "directory", Path: "/p"}); got != "/p" {
		t.Fatalf("directory label = %q", got)
	}
	if got := marketplaceSourceLabel(appwire.MarketplaceSourceInput{Kind: "git-subdir", URL: "https://x", Path: "/sub"}); got != "https://x (/sub)" {
		t.Fatalf("git-subdir label = %q", got)
	}
	if got := marketplaceSourceLabel(appwire.MarketplaceSourceInput{Kind: "unknown"}); got != "unknown" {
		t.Fatalf("unknown label = %q", got)
	}
}

// --- versionOrUnknown ---

func TestCovVersionOrUnknown(t *testing.T) {
	if got := versionOrUnknown(""); got != "unknown" {
		t.Fatalf("empty version = %q, want unknown", got)
	}
	if got := versionOrUnknown("  "); got != "unknown" {
		t.Fatalf("whitespace version = %q, want unknown", got)
	}
	if got := versionOrUnknown("1.2.3"); got != "1.2.3" {
		t.Fatalf("version = %q, want 1.2.3", got)
	}
}
