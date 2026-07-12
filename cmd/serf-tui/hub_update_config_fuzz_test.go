//go:build serffuzz

package main

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

// FuzzHubUpdateConfig replays the complete config-message branch matrix. The
// non-nil client is backed by the in-process appwire server used by the regular
// TUI tests; returned commands are deliberately not run because this target is
// the reducer's routing and state transition behavior.
func FuzzHubUpdateConfig(f *testing.F) {
	f.Add("fixture")
	f.Fuzz(func(t *testing.T, value string) {
		client, closeClient := newTestHubClient(t, nil)
		defer closeClient()
		failure := errors.New("fixture failure")
		status := appwire.AuthStatusResponse{Provider: "openai", Supported: true, SignedIn: true}

		apply := func(m hubModel, msg tea.Msg) hubModel {
			updated, _ := m.updateImpl(msg)
			return updated.(hubModel)
		}
		base := newHubModel(nil, "http://hub.invalid")
		for _, msg := range []tea.Msg{
			hubAuthStatusMsg{err: failure}, hubAuthStatusMsg{status: status},
			hubAuthLoginStartMsg{err: failure}, hubAuthLoginStartMsg{resp: appwire.AuthLoginStartResponse{Provider: "  ", URL: "u"}}, hubAuthLoginStartMsg{resp: appwire.AuthLoginStartResponse{Provider: "anthropic", URL: "u"}},
			hubAuthLoginCompleteMsg{err: failure}, hubAuthLoginCompleteMsg{resp: appwire.AuthLoginCompleteResponse{Status: status}},
			hubAuthLogoutMsg{err: failure}, hubAuthLogoutMsg{resp: appwire.AuthLogoutResponse{Removed: true, Status: status}}, hubAuthLogoutMsg{resp: appwire.AuthLogoutResponse{Status: status}},
			launchconfig.InstanceListResultMsg{}, launchconfig.InstanceMutateResultMsg{Err: failure}, launchconfig.InstanceMutateResultMsg{},
			launchconfig.InstanceSetDefaultMsg{}, launchconfig.InstanceRemoveMsg{}, launchconfig.InstanceCreateSubmitMsg{}, launchconfig.InstanceEditSubmitMsg{},
			launchconfig.CredentialsActionMsg{Action: "set", Instance: value}, launchconfig.CredentialsActionMsg{Action: "logout"}, launchconfig.CredentialsActionMsg{Action: "oauth"}, launchconfig.CredentialsActionMsg{Action: "other"},
			launchconfig.LaunchOverridesOpenMsg{}, launchconfig.LaunchOverridesOpenMsg{Initial: &appwire.LaunchConfigLayer{}},
			launchconfig.LaunchOverridesResultMsg{Cancelled: true}, launchconfig.LaunchOverridesResultMsg{Overrides: &appwire.LaunchConfigLayer{}},
			launchconfig.LaunchSettingsEditRequestMsg{Layer: "launch", Field: "mcps"}, launchconfig.LaunchSettingsEditRequestMsg{Layer: "launch", Field: "sandbox"}, launchconfig.LaunchSettingsEditRequestMsg{Layer: "launch", Field: "cwd", PathCompletion: true}, launchconfig.LaunchSettingsEditRequestMsg{Layer: "launch", Field: "model"},
			launchconfig.LaunchSettingsEditRequestMsg{Layer: "global", Field: "mcps"}, launchconfig.LaunchSettingsEditRequestMsg{Layer: "global", Field: "sandbox"}, launchconfig.LaunchSettingsEditRequestMsg{Layer: "global", Field: "cwd", PathCompletion: true}, launchconfig.LaunchSettingsEditRequestMsg{Layer: "global", Field: "model"},
			tuipick.TextInputResultMsg{Tag: "credential-set:p", Cancelled: true}, tuipick.TextInputResultMsg{Tag: "credential-set:p"}, tuipick.TextInputResultMsg{Tag: "credential-set:p", Value: value},
			tuipick.TextInputResultMsg{Tag: "oauth-redirect:p:f", Cancelled: true}, tuipick.TextInputResultMsg{Tag: "oauth-redirect:p:f"}, tuipick.TextInputResultMsg{Tag: "oauth-redirect:bad", Value: value}, tuipick.TextInputResultMsg{Tag: "oauth-redirect:p:f", Value: value},
			tuipick.TextInputResultMsg{Tag: "launch-override:model", Cancelled: true}, tuipick.TextInputResultMsg{Tag: "launch-override:model", Value: value},
			tuipick.TextInputResultMsg{Tag: "settings-edit:bad", Value: value}, tuipick.TextInputResultMsg{Tag: "settings-edit:global:model", Cancelled: true}, tuipick.TextInputResultMsg{Tag: "settings-edit:global:model", Value: value},
			tuipick.TextInputResultMsg{Tag: "other"},
			launchconfig.AuthApiKeySetResultMsg{Err: failure}, launchconfig.AuthApiKeySetResultMsg{}, launchconfig.AuthLoginStartResultMsg{Err: failure}, launchconfig.AuthLoginStartResultMsg{Provider: "p", FlowID: "f", URL: "u"}, launchconfig.AuthLoginCompleteResultMsg{Err: failure}, launchconfig.AuthLoginCompleteResultMsg{},
			launchconfig.LaunchSetLayerResultMsg{}, launchconfig.MarketplaceListResultMsg{}, launchconfig.MarketplaceMutateResultMsg{Err: failure}, launchconfig.MarketplaceMutateResultMsg{}, launchconfig.MarketplaceBrowseResultMsg{},
			launchconfig.MarketplaceAddSubmitMsg{}, launchconfig.MarketplaceRemoveMsg{}, launchconfig.MarketplaceRefreshMsg{}, launchconfig.MarketplaceBrowseRequestMsg{},
			launchconfig.PluginListResultMsg{}, launchconfig.PluginMutateResultMsg{Err: failure}, launchconfig.PluginMutateResultMsg{},
			launchconfig.PluginActionMsg{Action: "install"}, launchconfig.PluginActionMsg{Action: "upgrade"}, launchconfig.PluginActionMsg{Action: "remove"}, launchconfig.PluginActionMsg{Action: "enable"}, launchconfig.PluginActionMsg{Action: "disable"}, launchconfig.PluginActionMsg{Action: "other"}, launchconfig.PluginSetAutoUpgradeMsg{},
			launchconfig.LaunchLayerResultMsg{}, launchconfig.LaunchResolveResultMsg{}, launchconfig.LaunchTrustResultMsg{}, launchconfig.LaunchSchemaResultMsg{},
		} {
			_ = apply(base, msg)
		}

		credentials := launchconfig.NewCredentialsPanel()
		settings := launchconfig.NewLaunchSettingsPanel(client, "/tmp")
		plugins := launchconfig.NewPluginsPanel()
		overrides := launchconfig.NewLaunchOverridesModal()
		full := base
		full.client = client
		full.credentialsPanel = &credentials
		full.launchSettingsPanel = &settings
		full.pluginsPanel = &plugins
		full.launchOverridesModal = &overrides
		for _, msg := range []tea.Msg{
			launchconfig.InstanceListResultMsg{}, launchconfig.InstanceMutateResultMsg{},
			launchconfig.InstanceSetDefaultMsg{}, launchconfig.InstanceRemoveMsg{}, launchconfig.InstanceCreateSubmitMsg{}, launchconfig.InstanceEditSubmitMsg{},
			launchconfig.CredentialsActionMsg{Action: "logout"}, launchconfig.CredentialsActionMsg{Action: "oauth"},
			launchconfig.LaunchOverridesOpenMsg{},
			tuipick.TextInputResultMsg{Tag: "credential-set:p", Value: value}, tuipick.TextInputResultMsg{Tag: "oauth-redirect:p:f", Value: value},
			tuipick.TextInputResultMsg{Tag: "launch-override:model", Value: value}, tuipick.TextInputResultMsg{Tag: "launch-override:mcps", Value: "{"},
			tuipick.TextInputResultMsg{Tag: "settings-edit:global:model", Value: value}, tuipick.TextInputResultMsg{Tag: "settings-edit:global:mcps", Value: "{"},
			launchconfig.AuthApiKeySetResultMsg{}, launchconfig.AuthLoginCompleteResultMsg{},
			launchconfig.LaunchSetLayerResultMsg{}, launchconfig.LaunchSetLayerResultMsg{Err: failure},
			launchconfig.MarketplaceListResultMsg{}, launchconfig.MarketplaceMutateResultMsg{}, launchconfig.MarketplaceBrowseResultMsg{},
			launchconfig.MarketplaceAddSubmitMsg{}, launchconfig.MarketplaceRemoveMsg{}, launchconfig.MarketplaceRefreshMsg{}, launchconfig.MarketplaceBrowseRequestMsg{},
			launchconfig.PluginListResultMsg{}, launchconfig.PluginMutateResultMsg{},
			launchconfig.PluginActionMsg{Action: "install"}, launchconfig.PluginActionMsg{Action: "upgrade"}, launchconfig.PluginActionMsg{Action: "remove"}, launchconfig.PluginActionMsg{Action: "enable"}, launchconfig.PluginActionMsg{Action: "disable"}, launchconfig.PluginActionMsg{Action: "other"}, launchconfig.PluginSetAutoUpgradeMsg{},
			launchconfig.LaunchLayerResultMsg{}, launchconfig.LaunchResolveResultMsg{}, launchconfig.LaunchTrustResultMsg{}, launchconfig.LaunchSchemaResultMsg{},
		} {
			_ = apply(full, msg)
		}
	})
}
