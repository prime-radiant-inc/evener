//go:build serffuzz

package main

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

// FuzzRootTUIMessageProgram drives every external result-message family through
// the real hub update switch. Models have no client, so commands remain inert
// and no RPC, terminal, or network boundary can be reached.
func FuzzRootTUIMessageProgram(f *testing.F) {
	for selector := byte(0); selector < 45; selector++ {
		f.Add(selector, false)
		f.Add(selector, true)
	}
	f.Fuzz(func(t *testing.T, selector byte, failed bool) {
		err := error(nil)
		if failed {
			err = errors.New("fixture failure")
		}
		messages := []tea.Msg{
			launchconfig.AuthListResultMsg{},
			launchconfig.InstanceListResultMsg{},
			launchconfig.InstanceMutateResultMsg{Err: err},
			launchconfig.InstanceSetDefaultMsg{},
			launchconfig.InstanceRemoveMsg{},
			launchconfig.InstanceCreateSubmitMsg{},
			launchconfig.InstanceEditSubmitMsg{},
			launchconfig.CredentialsActionMsg{Action: "set", Instance: "fixture"},
			launchconfig.CredentialsActionMsg{Action: "logout"},
			launchconfig.CredentialsActionMsg{Action: "oauth"},
			launchconfig.CredentialsActionMsg{Action: "unknown"},
			launchconfig.LaunchOverridesOpenMsg{},
			launchconfig.LaunchOverridesResultMsg{Cancelled: failed},
			launchconfig.LaunchSettingsEditRequestMsg{Layer: "launch", Field: "mcps"},
			launchconfig.LaunchSettingsEditRequestMsg{Layer: "profile", Field: "sandbox"},
			tuipick.TextInputResultMsg{Tag: "credential-set:fixture", Value: "value", Cancelled: failed},
			tuipick.TextInputResultMsg{Tag: "oauth-redirect:provider:flow", Value: "redirect", Cancelled: failed},
			tuipick.TextInputResultMsg{Tag: "launch-override:model", Value: "model", Cancelled: failed},
			tuipick.TextInputResultMsg{Tag: "settings-edit:profile:model", Value: "model", Cancelled: failed},
			tuipick.TextInputResultMsg{Tag: "unknown"},
			launchconfig.AuthApiKeySetResultMsg{Err: err},
			launchconfig.AuthLoginStartResultMsg{Err: err},
			launchconfig.AuthLoginCompleteResultMsg{Err: err},
			launchconfig.LaunchSetLayerResultMsg{Err: err},
			launchconfig.LaunchLayerResultMsg{},
			launchconfig.LaunchResolveResultMsg{},
			launchconfig.LaunchTrustResultMsg{},
			launchconfig.LaunchSchemaResultMsg{},
			launchconfig.MarketplaceListResultMsg{},
			launchconfig.MarketplaceMutateResultMsg{Err: err},
			launchconfig.MarketplaceBrowseResultMsg{},
			launchconfig.MarketplaceAddSubmitMsg{},
			launchconfig.MarketplaceRemoveMsg{},
			launchconfig.MarketplaceRefreshMsg{},
			launchconfig.MarketplaceBrowseRequestMsg{},
			launchconfig.PluginListResultMsg{},
			launchconfig.PluginMutateResultMsg{Err: err},
			launchconfig.PluginActionMsg{Action: "install"},
			launchconfig.PluginActionMsg{Action: "upgrade"},
			launchconfig.PluginActionMsg{Action: "remove"},
			launchconfig.PluginActionMsg{Action: "enable"},
			launchconfig.PluginActionMsg{Action: "disable"},
			launchconfig.PluginActionMsg{Action: "unknown"},
			launchconfig.PluginSetAutoUpgradeMsg{},
		}
		m := newHubModel(nil, "http://hub.invalid")
		_, _ = m.updateImpl(messages[int(selector)%len(messages)])
	})
}
