package launchconfig

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

const (
	// pluginsQuickTimeout bounds unlocked, purely local reads (the list calls).
	pluginsQuickTimeout = 5 * time.Second
	// pluginsSlowTimeout bounds every mutation, and the preview that readies
	// the bundled store alongside them. internal/plugins.Manager takes a flock
	// budgeted up to 30s under contention before add/refresh/install/upgrade
	// even start their own git clone/pull, so this must clear that floor with
	// headroom for real network I/O.
	pluginsSlowTimeout = 60 * time.Second
)

// MarketplaceListResultMsg carries the result of a passive evener/marketplace/list
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

// MarketplaceBrowseResultMsg carries the result of a evener/marketplace/browse
// fetch for the panel's Browse tab.
type MarketplaceBrowseResultMsg struct {
	Name     string
	Response appwire.MarketplaceBrowseResponse
	Err      error
}

// MarketplaceAddSubmitMsg triggers evener/marketplace/add, emitted when the
// panel's add-marketplace form is submitted.
type MarketplaceAddSubmitMsg struct {
	Params appwire.MarketplaceAddParams
}

// MarketplaceRemoveMsg triggers evener/marketplace/remove for Name.
type MarketplaceRemoveMsg struct {
	Name string
}

// MarketplaceRefreshMsg triggers evener/marketplace/refresh for Name.
type MarketplaceRefreshMsg struct {
	Name string
}

// MarketplaceBrowseRequestMsg triggers evener/marketplace/browse for Name,
// emitted when the panel's Browse tab picks a marketplace to view.
type MarketplaceBrowseRequestMsg struct {
	Name string
}

// PluginListResultMsg carries the result of a passive evener/plugin/list fetch
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

// PluginPreviewRequestMsg asks the hub model to run a plugin preview. Preview
// starts no session and runs no plugin code; for a requested bundled plugin it
// readies the store a launch publishes into, staging and removing a marked
// copy. Key identifies the request's directory/override revision so a late
// response cannot replace a newer preview.
type PluginPreviewRequestMsg struct {
	Params appwire.PluginPreviewParams
	Key    string
}

// PluginPreviewResultMsg carries one keyed plugin preview result.
type PluginPreviewResultMsg struct {
	Response appwire.PluginPreviewResponse
	Key      string
	Err      error
}

// PluginActionMsg carries a plugin mutation sharing PluginRefParams' shape:
// install, upgrade, remove, enable, or disable.
type PluginActionMsg struct {
	Action      string // "install" | "upgrade" | "remove" | "enable" | "disable"
	Plugin      string
	Marketplace string
}

// PluginSetAutoUpgradeMsg triggers evener/plugin/setAutoUpgrade.
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

func CmdPluginPreview(client *appwire.Client, params appwire.PluginPreviewParams, key string) tea.Cmd {
	return func() tea.Msg {
		// Not a quick read: previewing a bundled plugin readies the store the
		// way a launch does, which waits on the bundled cache's flock for the
		// same budget a publish gets. A client deadline under that wait would
		// report a failure the hub is still working through.
		ctx, cancel := context.WithTimeout(context.Background(), pluginsSlowTimeout)
		defer cancel()
		resp, err := client.PluginPreview(ctx, params)
		return PluginPreviewResultMsg{Response: resp, Key: key, Err: err}
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
