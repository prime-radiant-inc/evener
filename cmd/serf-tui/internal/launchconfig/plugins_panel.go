package launchconfig

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

type pluginsTab int

const (
	pluginsTabMarketplaces pluginsTab = iota
	pluginsTabBrowse
	pluginsTabInstalled
)

// Marketplace source kinds offered by the add-marketplace form. git-subdir is
// deliberately not offered here (it needs both a URL and a subdirectory path);
// use the CLI or web for that source type.
const (
	marketplaceKindURL       = "url"
	marketplaceKindGitHub    = "github"
	marketplaceKindDirectory = "directory"
)

// PluginsPanel is the overlay model for managing plugin marketplaces and
// installed plugins: Marketplaces | Browse | Installed tabs, modeled on
// LaunchSettingsPanel's tab switching and CredentialsPanel's single-key row
// actions and inline add form.
type PluginsPanel struct {
	tab    pluginsTab
	cursor int

	marketplaces        []appwire.MarketplaceEntry
	loadingMarketplaces bool
	marketplacesErr     error

	plugins        []appwire.PluginEntry
	loadingPlugins bool
	pluginsErr     error

	// Browse tab sub-state: "" means the tab shows the marketplace picker;
	// non-empty means it shows that marketplace's fetched catalog.
	browseMarketplace string
	browseCatalog     appwire.MarketplaceBrowseResponse
	browseLoading     bool
	browseErr         error

	// add-marketplace form state
	formOpen  bool
	formField int // 0 = kind, 1 = value
	formKind  string
	formValue string

	done      bool
	cancelled bool
}

func NewPluginsPanel() PluginsPanel {
	return PluginsPanel{loadingMarketplaces: true, loadingPlugins: true}
}

func (p PluginsPanel) Init() tea.Cmd { return nil }

// Done reports whether the panel has been dismissed.
func (p PluginsPanel) Done() bool { return p.done }

// BrowseMarketplace returns the marketplace name currently shown in the
// Browse tab's catalog view ("" if the picker is showing instead), so a
// live-refresh notification can also re-fetch the open catalog.
func (p PluginsPanel) BrowseMarketplace() string { return p.browseMarketplace }

func (p PluginsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case MarketplaceListResultMsg:
		p.loadingMarketplaces = false
		p.marketplacesErr = m.Err
		if m.Err == nil {
			p.marketplaces = m.List.Marketplaces
		}
		p.cursor = clampCursor(p.cursor, p.maxCursor())
		return p, nil

	case PluginListResultMsg:
		p.loadingPlugins = false
		p.pluginsErr = m.Err
		if m.Err == nil {
			p.plugins = m.List.Plugins
		}
		p.cursor = clampCursor(p.cursor, p.maxCursor())
		return p, nil

	case MarketplaceBrowseResultMsg:
		if m.Name != p.browseMarketplace {
			// A stale response for a marketplace the user has since
			// navigated away from; drop it rather than resurrect the picker.
			return p, nil
		}
		p.browseLoading = false
		p.browseErr = m.Err
		if m.Err == nil {
			p.browseCatalog = m.Response
		}
		p.cursor = clampCursor(p.cursor, p.maxCursor())
		return p, nil

	case tea.KeyMsg:
		if p.formOpen {
			return p.updateForm(m)
		}
		return p.updateKeys(m)
	}
	return p, nil
}

func clampCursor(cursor, count int) int {
	if count <= 0 {
		return 0
	}
	if cursor >= count {
		return count - 1
	}
	if cursor < 0 {
		return 0
	}
	return cursor
}

// maxCursor returns the row count of the currently visible list, whichever
// tab (and Browse sub-state) is active.
func (p PluginsPanel) maxCursor() int {
	switch p.tab {
	case pluginsTabMarketplaces:
		return len(p.marketplaces)
	case pluginsTabBrowse:
		if p.browseMarketplace == "" {
			return len(p.marketplaces)
		}
		return len(p.browseCatalog.Plugins)
	case pluginsTabInstalled:
		return len(p.plugins)
	}
	return 0
}

func (p PluginsPanel) updateKeys(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		if p.tab == pluginsTabBrowse && p.browseMarketplace != "" {
			// Back out of the catalog view to the marketplace picker rather
			// than closing the whole panel.
			p.browseMarketplace = ""
			p.browseCatalog = appwire.MarketplaceBrowseResponse{}
			p.browseErr = nil
			p.cursor = clampCursor(p.cursor, p.maxCursor())
			return p, nil
		}
		p.cancelled = true
		p.done = true
		return p, nil
	case tea.KeyLeft:
		if p.tab > pluginsTabMarketplaces {
			p.tab--
			p.cursor = 0
		}
		return p, nil
	case tea.KeyRight:
		if p.tab < pluginsTabInstalled {
			p.tab++
			p.cursor = 0
		}
		return p, nil
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		return p, nil
	case tea.KeyDown:
		if p.cursor < p.maxCursor()-1 {
			p.cursor++
		}
		return p, nil
	case tea.KeyEnter:
		return p.handleEnter()
	case tea.KeyRunes:
		return p.handleRune(string(m.Runes))
	}
	return p, nil
}

func (p PluginsPanel) handleEnter() (tea.Model, tea.Cmd) {
	switch p.tab {
	case pluginsTabBrowse:
		if p.browseMarketplace == "" {
			name, ok := p.selectedMarketplaceName()
			if !ok {
				return p, nil
			}
			p.browseMarketplace = name
			p.browseLoading = true
			p.browseErr = nil
			p.cursor = 0
			return p, func() tea.Msg { return MarketplaceBrowseRequestMsg{Name: name} }
		}
		return p.installSelectedCatalogPlugin()
	case pluginsTabInstalled:
		entry, ok := p.selectedPlugin()
		if !ok {
			return p, nil
		}
		action := "enable"
		if entry.Enabled {
			action = "disable"
		}
		return p, func() tea.Msg {
			return PluginActionMsg{Action: action, Plugin: entry.Plugin, Marketplace: entry.Marketplace}
		}
	}
	return p, nil
}

func (p PluginsPanel) handleRune(s string) (tea.Model, tea.Cmd) {
	switch p.tab {
	case pluginsTabMarketplaces:
		switch strings.ToLower(s) {
		case "n":
			p.formOpen = true
			p.formField = 0
			p.formKind = marketplaceKindURL
			p.formValue = ""
			return p, nil
		case "r":
			name, ok := p.selectedMarketplaceName()
			if !ok {
				return p, nil
			}
			return p, func() tea.Msg { return MarketplaceRefreshMsg{Name: name} }
		case "x":
			name, ok := p.selectedMarketplaceName()
			if !ok {
				return p, nil
			}
			return p, func() tea.Msg { return MarketplaceRemoveMsg{Name: name} }
		}
	case pluginsTabBrowse:
		if p.browseMarketplace != "" && strings.EqualFold(s, "i") {
			return p.installSelectedCatalogPlugin()
		}
	case pluginsTabInstalled:
		entry, ok := p.selectedPlugin()
		if !ok {
			return p, nil
		}
		switch strings.ToLower(s) {
		case "a":
			return p, func() tea.Msg {
				return PluginSetAutoUpgradeMsg{Plugin: entry.Plugin, Marketplace: entry.Marketplace, AutoUpgrade: !entry.AutoUpgrade}
			}
		case "u":
			return p, func() tea.Msg {
				return PluginActionMsg{Action: "upgrade", Plugin: entry.Plugin, Marketplace: entry.Marketplace}
			}
		case "x":
			return p, func() tea.Msg {
				return PluginActionMsg{Action: "remove", Plugin: entry.Plugin, Marketplace: entry.Marketplace}
			}
		}
	}
	return p, nil
}

func (p PluginsPanel) selectedMarketplaceName() (string, bool) {
	if p.cursor < 0 || p.cursor >= len(p.marketplaces) {
		return "", false
	}
	return p.marketplaces[p.cursor].Name, true
}

func (p PluginsPanel) selectedPlugin() (appwire.PluginEntry, bool) {
	if p.cursor < 0 || p.cursor >= len(p.plugins) {
		return appwire.PluginEntry{}, false
	}
	return p.plugins[p.cursor], true
}

func (p PluginsPanel) selectedCatalogPlugin() (appwire.MarketplaceCatalogPlugin, bool) {
	if p.cursor < 0 || p.cursor >= len(p.browseCatalog.Plugins) {
		return appwire.MarketplaceCatalogPlugin{}, false
	}
	return p.browseCatalog.Plugins[p.cursor], true
}

// isInstalled reports whether plugin@marketplace already has a registry
// entry, mirroring the web's Browse-tab installed badge (plugins-manager.html
// isInstalled).
func (p PluginsPanel) isInstalled(plugin, marketplace string) bool {
	for _, e := range p.plugins {
		if e.Plugin == plugin && e.Marketplace == marketplace {
			return true
		}
	}
	return false
}

// installSelectedCatalogPlugin emits an install action for the catalog row
// under the cursor, unless it is already installed — mirroring the web's
// Browse tab, which hides the Install button once installed.
func (p PluginsPanel) installSelectedCatalogPlugin() (tea.Model, tea.Cmd) {
	cp, ok := p.selectedCatalogPlugin()
	if !ok {
		return p, nil
	}
	if p.isInstalled(cp.Name, p.browseMarketplace) {
		return p, nil
	}
	marketplace := p.browseMarketplace
	return p, func() tea.Msg { return PluginActionMsg{Action: "install", Plugin: cp.Name, Marketplace: marketplace} }
}

// updateForm handles key input while the add-marketplace form is shown.
// Fields cycle kind(0) -> value(1) -> submit, mirroring CredentialsPanel's
// create form.
func (p PluginsPanel) updateForm(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.Type {
	case tea.KeyEsc:
		p.formOpen = false
		return p, nil
	case tea.KeyEnter:
		if p.formField < 1 {
			p.formField++
			return p, nil
		}
		p.formOpen = false
		source := p.formSource()
		return p, func() tea.Msg { return MarketplaceAddSubmitMsg{Params: appwire.MarketplaceAddParams{Source: source}} }
	case tea.KeyTab, tea.KeySpace:
		// A real Tab or Space keypress arrives as its own Type (bubbletea
		// key.go), not as KeyRunes; both cycle the kind field.
		if p.formField == 0 {
			p.formKind = nextMarketplaceKind(p.formKind)
		}
		return p, nil
	case tea.KeyBackspace:
		if p.formField == 1 && len(p.formValue) > 0 {
			p.formValue = p.formValue[:len(p.formValue)-1]
		}
		return p, nil
	case tea.KeyRunes:
		s := string(m.Runes)
		if p.formField == 0 {
			if s == " " || s == "\t" {
				p.formKind = nextMarketplaceKind(p.formKind)
			}
			return p, nil
		}
		p.formValue += s
	}
	return p, nil
}

func nextMarketplaceKind(kind string) string {
	switch kind {
	case marketplaceKindURL:
		return marketplaceKindGitHub
	case marketplaceKindGitHub:
		return marketplaceKindDirectory
	default:
		return marketplaceKindURL
	}
}

// formSource builds the wire source from the form's kind+value fields. The
// marketplace name is left empty: the server derives it from the fetched
// manifest (appwire.MarketplaceAddParams doc).
func (p PluginsPanel) formSource() appwire.MarketplaceSourceInput {
	value := strings.TrimSpace(p.formValue)
	switch p.formKind {
	case marketplaceKindGitHub:
		return appwire.MarketplaceSourceInput{Kind: "github", Repo: value}
	case marketplaceKindDirectory:
		return appwire.MarketplaceSourceInput{Kind: "directory", Path: value}
	default:
		return appwire.MarketplaceSourceInput{Kind: "url", URL: value}
	}
}

func (p PluginsPanel) View() string {
	var body strings.Builder
	body.WriteString(p.renderTabs())
	body.WriteString("\n\n")
	if p.formOpen {
		body.WriteString(p.formView())
	} else {
		switch p.tab {
		case pluginsTabMarketplaces:
			body.WriteString(p.renderMarketplacesTab())
		case pluginsTabBrowse:
			body.WriteString(p.renderBrowseTab())
		case pluginsTabInstalled:
			body.WriteString(p.renderInstalledTab())
		}
	}
	width := 80
	return tuiprim.Overlay(tuiprim.OverlayOpts{Title: "Plugins", Width: width, Body: body.String(), Footer: p.footerFor(width)})
}

func (p PluginsPanel) renderTabs() string {
	var b strings.Builder
	tabs := []string{"Marketplaces", "Browse", "Installed"}
	for i, name := range tabs {
		if pluginsTab(i) == p.tab {
			fmt.Fprintf(&b, "[%s] ", name)
		} else {
			fmt.Fprintf(&b, " %s  ", name)
		}
	}
	return b.String()
}

func (p PluginsPanel) renderMarketplacesTab() string {
	if p.loadingMarketplaces {
		return "Loading marketplaces…"
	}
	if p.marketplacesErr != nil {
		return "Error: " + p.marketplacesErr.Error()
	}
	if len(p.marketplaces) == 0 {
		return "No marketplaces registered. Press n to add one."
	}
	rows := make([]string, 0, len(p.marketplaces))
	for i, mk := range p.marketplaces {
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		rows = append(rows, fmt.Sprintf("%s%-24s %s", cursor, mk.Name, marketplaceSourceLabel(mk.Source)))
	}
	return strings.Join(rows, "\n")
}

// marketplaceSourceLabel mirrors the web's sourceLabel (plugins-manager.html).
func marketplaceSourceLabel(src appwire.MarketplaceSourceInput) string {
	switch src.Kind {
	case "github":
		return "github: " + src.Repo
	case "url":
		return src.URL
	case "directory":
		return src.Path
	case "git-subdir":
		return src.URL + " (" + src.Path + ")"
	default:
		return src.Kind
	}
}

func (p PluginsPanel) renderBrowseTab() string {
	if p.browseMarketplace == "" {
		if len(p.marketplaces) == 0 {
			return "No marketplaces registered. Add one from the Marketplaces tab."
		}
		rows := make([]string, 0, len(p.marketplaces))
		for i, mk := range p.marketplaces {
			cursor := "  "
			if i == p.cursor {
				cursor = "> "
			}
			rows = append(rows, cursor+mk.Name)
		}
		return "Choose a marketplace to browse:\n" + strings.Join(rows, "\n")
	}
	if p.browseLoading {
		return "Loading catalog for " + p.browseMarketplace + "…"
	}
	if p.browseErr != nil {
		return "Error: " + p.browseErr.Error()
	}
	if len(p.browseCatalog.Plugins) == 0 {
		return p.browseMarketplace + " has no plugins."
	}
	th := tuitheme.ActiveTheme()
	rows := make([]string, 0, len(p.browseCatalog.Plugins))
	for i, cp := range p.browseCatalog.Plugins {
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		line := fmt.Sprintf("%s%-24s %s", cursor, cp.Name, cp.Description)
		if p.isInstalled(cp.Name, p.browseMarketplace) {
			line += "  " + tuiprim.StatusBadge(th.StateIdle, "installed")
		}
		rows = append(rows, line)
	}
	return p.browseMarketplace + ":\n" + strings.Join(rows, "\n")
}

func (p PluginsPanel) renderInstalledTab() string {
	if p.loadingPlugins {
		return "Loading plugins…"
	}
	if p.pluginsErr != nil {
		return "Error: " + p.pluginsErr.Error()
	}
	if len(p.plugins) == 0 {
		return "No plugins installed yet. Install one from Browse."
	}
	th := tuitheme.ActiveTheme()
	rows := make([]string, 0, len(p.plugins))
	for i, e := range p.plugins {
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		var badges []string
		if e.Broken {
			badges = append(badges, tuiprim.StatusBadge(th.StateWarning, "broken"))
		}
		if !e.Enabled {
			badges = append(badges, tuiprim.StatusBadge(th.StateEnded, "disabled"))
		}
		if e.AutoUpgrade {
			badges = append(badges, tuiprim.StatusBadge(th.StateIdle, "auto-upgrade"))
		}
		line := fmt.Sprintf("%s%s @ %s  v%s", cursor, e.Plugin, e.Marketplace, versionOrUnknown(e.Version))
		if len(badges) > 0 {
			line += "  " + strings.Join(badges, " ")
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n")
}

func versionOrUnknown(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}

func (p PluginsPanel) formView() string {
	return strings.Join([]string{
		"Add marketplace",
		"",
		p.formFieldLine("Kind ("+p.formKind+")", 0, p.formKind),
		p.formFieldLine(p.formValueLabel(), 1, p.formValue),
	}, "\n")
}

func (p PluginsPanel) formValueLabel() string {
	switch p.formKind {
	case marketplaceKindGitHub:
		return "owner/repo"
	case marketplaceKindDirectory:
		return "Path"
	default:
		return "URL"
	}
}

func (p PluginsPanel) formFieldLine(label string, fieldIdx int, value string) string {
	th := tuitheme.ActiveTheme()
	active := p.formField == fieldIdx
	cursor := "  "
	if active {
		cursor = "> "
	}
	prompt := lipgloss.NewStyle().Foreground(th.TextDim).Render(label + ":")
	var val string
	if active {
		val = lipgloss.NewStyle().Foreground(th.Text).Render(value + "_")
	} else {
		val = lipgloss.NewStyle().Foreground(th.Text).Render(value)
	}
	return cursor + prompt + " " + val
}

func (p PluginsPanel) footerFor(width int) string {
	if p.formOpen {
		return tuiprim.ActionBarForWidth(width,
			tuiprim.KbdHint("enter", "next/submit"),
			tuiprim.KbdHint("space", "cycle kind"),
			tuiprim.KbdHint("esc", "cancel"),
		)
	}
	switch p.tab {
	case pluginsTabMarketplaces:
		return tuiprim.ActionBarForWidth(width,
			tuiprim.KbdHint("←→", "tab"),
			tuiprim.KbdHint("↑↓", "select"),
			tuiprim.KbdHint("n", "new"),
			tuiprim.KbdHint("r", "refresh"),
			tuiprim.KbdHint("x", "remove"),
			tuiprim.KbdHint("esc", "close"),
		)
	case pluginsTabBrowse:
		if p.browseMarketplace == "" {
			return tuiprim.ActionBarForWidth(width,
				tuiprim.KbdHint("←→", "tab"),
				tuiprim.KbdHint("↑↓", "select"),
				tuiprim.KbdHint("enter", "browse"),
				tuiprim.KbdHint("esc", "close"),
			)
		}
		return tuiprim.ActionBarForWidth(width,
			tuiprim.KbdHint("←→", "tab"),
			tuiprim.KbdHint("↑↓", "select"),
			tuiprim.KbdHint("enter", "install"),
			tuiprim.KbdHint("esc", "back"),
		)
	default: // pluginsTabInstalled
		return tuiprim.ActionBarForWidth(width,
			tuiprim.KbdHint("←→", "tab"),
			tuiprim.KbdHint("↑↓", "select"),
			tuiprim.KbdHint("enter", "enable/disable"),
			tuiprim.KbdHint("a", "auto-upgrade"),
			tuiprim.KbdHint("u", "upgrade"),
			tuiprim.KbdHint("x", "remove"),
			tuiprim.KbdHint("esc", "close"),
		)
	}
}
