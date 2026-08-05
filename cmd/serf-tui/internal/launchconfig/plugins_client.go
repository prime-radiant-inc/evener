package launchconfig

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

const (
	// pluginsQuickTimeout bounds unlocked, purely local reads (the list calls).
	pluginsQuickTimeout = 5 * time.Second
	// pluginsSlowTimeout bounds every mutation. internal/plugins.Manager takes
	// a flock budgeted up to 30s under contention before add/refresh/install/
	// upgrade even start their own git clone/pull, so this must clear that
	// floor with headroom for real network I/O.
	pluginsSlowTimeout = 60 * time.Second
)

// MarketplaceListResultMsg carries the result of a passive serf/marketplace/list
// fetch (including one forwarded from a successful mutation). The panel
// understands only this type for populating its marketplace list.
type MarketplaceListResultMsg struct {
	List appwire.MarketplaceListResponse
	Err  error
}

// MarketplaceMutateResultMsg carries the result of a marketplace mutation
// (add/remove/refresh). Kept distinct from MarketplaceListResultMsg so the hub
// model can surface a mutation failure as a prominent error (mirroring
// InstanceMutateResultMsg) while a passive list-load error stays local to the
// panel.
type MarketplaceMutateResultMsg struct {
	List appwire.MarketplaceListResponse
	Err  error
}

// MarketplaceBrowseResultMsg carries the result of a serf/marketplace/browse
// fetch for the panel's Browse tab.
type MarketplaceBrowseResultMsg struct {
	Name     string
	Response appwire.MarketplaceBrowseResponse
	Err      error
}

// MarketplaceAddSubmitMsg triggers serf/marketplace/add, emitted when the
// panel's add-marketplace form is submitted.
type MarketplaceAddSubmitMsg struct {
	Params appwire.MarketplaceAddParams
}

// MarketplaceRemoveMsg triggers serf/marketplace/remove for Name.
type MarketplaceRemoveMsg struct {
	Name string
}

// MarketplaceRefreshMsg triggers serf/marketplace/refresh for Name.
type MarketplaceRefreshMsg struct {
	Name string
}

// MarketplaceBrowseRequestMsg triggers serf/marketplace/browse for Name,
// emitted when the panel's Browse tab picks a marketplace to view.
type MarketplaceBrowseRequestMsg struct {
	Name string
}

// PluginListResultMsg carries the result of a passive serf/plugin/list fetch
// (including one forwarded from a successful mutation).
type PluginListResultMsg struct {
	List appwire.PluginListResponse
	Err  error
}

// PluginMutateResultMsg carries the result of a plugin mutation
// (install/upgrade/remove/enable/disable/setAutoUpgrade). Kept distinct from
// PluginListResultMsg for the same reason as MarketplaceMutateResultMsg.
type PluginMutateResultMsg struct {
	List appwire.PluginListResponse
	Err  error
}

// PluginActionMsg carries a plugin mutation sharing PluginRefParams' shape:
// install, upgrade, remove, enable, or disable.
type PluginActionMsg struct {
	Action      string // "install" | "upgrade" | "remove" | "enable" | "disable"
	Plugin      string
	Marketplace string
}

// PluginSetAutoUpgradeMsg triggers serf/plugin/setAutoUpgrade.
type PluginSetAutoUpgradeMsg struct {
	Plugin      string
	Marketplace string
	AutoUpgrade bool
}

func CmdMarketplaceList(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsQuickTimeout)
		defer cancel()
		resp, err := client.MarketplaceList(ctx)
		return MarketplaceListResultMsg{List: resp, Err: err}
	}
}

func CmdMarketplaceAdd(client *appwire.Client, params appwire.MarketplaceAddParams) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.MarketplaceAdd(ctx, params)
		return MarketplaceMutateResultMsg{List: resp, Err: err}
	}
}

func CmdMarketplaceRemove(client *appwire.Client, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.MarketplaceRemove(ctx, appwire.MarketplaceNameParams{Name: name})
		return MarketplaceMutateResultMsg{List: resp, Err: err}
	}
}

func CmdMarketplaceRefresh(client *appwire.Client, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.MarketplaceRefresh(ctx, appwire.MarketplaceNameParams{Name: name})
		return MarketplaceMutateResultMsg{List: resp, Err: err}
	}
}

func CmdMarketplaceBrowse(client *appwire.Client, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.MarketplaceBrowse(ctx, appwire.MarketplaceBrowseParams{Name: name})
		return MarketplaceBrowseResultMsg{Name: name, Response: resp, Err: err}
	}
}

func CmdPluginList(client *appwire.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsQuickTimeout)
		defer cancel()
		resp, err := client.PluginList(ctx)
		return PluginListResultMsg{List: resp, Err: err}
	}
}

func CmdPluginInstall(client *appwire.Client, plugin, marketplace string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.PluginInstall(ctx, appwire.PluginRefParams{Plugin: plugin, Marketplace: marketplace})
		return PluginMutateResultMsg{List: resp, Err: err}
	}
}

func CmdPluginUpgrade(client *appwire.Client, plugin, marketplace string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.PluginUpgrade(ctx, appwire.PluginRefParams{Plugin: plugin, Marketplace: marketplace})
		return PluginMutateResultMsg{List: resp, Err: err}
	}
}

func CmdPluginRemove(client *appwire.Client, plugin, marketplace string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.PluginRemove(ctx, appwire.PluginRefParams{Plugin: plugin, Marketplace: marketplace})
		return PluginMutateResultMsg{List: resp, Err: err}
	}
}

func CmdPluginEnable(client *appwire.Client, plugin, marketplace string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.PluginEnable(ctx, appwire.PluginRefParams{Plugin: plugin, Marketplace: marketplace})
		return PluginMutateResultMsg{List: resp, Err: err}
	}
}

func CmdPluginDisable(client *appwire.Client, plugin, marketplace string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.PluginDisable(ctx, appwire.PluginRefParams{Plugin: plugin, Marketplace: marketplace})
		return PluginMutateResultMsg{List: resp, Err: err}
	}
}

func CmdPluginSetAutoUpgrade(client *appwire.Client, plugin, marketplace string, autoUpgrade bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.PluginSetAutoUpgrade(ctx, appwire.PluginSetAutoUpgradeParams{Plugin: plugin, Marketplace: marketplace, AutoUpgrade: autoUpgrade})
		return PluginMutateResultMsg{List: resp, Err: err}
	}
}
