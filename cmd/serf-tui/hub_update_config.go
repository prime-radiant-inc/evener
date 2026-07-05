package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

// Handlers for the auth / credentials / instance / launch-config domain of
// updateImpl. Each mirrors a single (or, for the launch-result group, a
// multi-type) case of the central type switch and is invoked from there.

func (m hubModel) handleAuthStatus(msg hubAuthStatusMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.addAuthErrorNotice("Auth error", msg.err)
		m.recordSessionError("Auth status failed: " + msg.err.Error())
		return m, nil
	}
	m.clearNoticesByCategory("auth")
	m.authStatus = authStatusFromAppWire(msg.status)
	m.authStatusSeen = true
	m.clearSessionError()
	m.addSessionSystem(formatAuthStatusSummary(m.authStatus))
	return m, nil
}

func (m hubModel) handleAuthLoginStart(msg hubAuthLoginStartMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.addAuthErrorNotice("Auth error", msg.err)
		return m, nil
	}
	m.authLoginProvider = strings.TrimSpace(msg.resp.Provider)
	if m.authLoginProvider == "" {
		m.authLoginProvider = "openai"
	}
	m.authLoginFlowID = msg.resp.FlowID
	m.addSessionSystem("OpenAI sign-in URL:\n" + msg.resp.URL + "\nPaste the full OpenAI redirect URL and press enter.")
	return m, nil
}

func (m hubModel) handleAuthLoginComplete(msg hubAuthLoginCompleteMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.addAuthErrorNotice("Auth error", msg.err)
		m.recordSessionError("Login failed: " + msg.err.Error())
		return m, nil
	}
	m.clearNoticesByCategory("auth")
	m.authLoginProvider = ""
	m.authLoginFlowID = ""
	m.authStatus = authStatusFromAppWire(msg.resp.Status)
	m.authStatusSeen = true
	m.clearSessionError()
	m.addSessionSystem("OpenAI login complete. " + formatAuthStatusSummary(m.authStatus))
	return m, nil
}

func (m hubModel) handleAuthLogout(msg hubAuthLogoutMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.addAuthErrorNotice("Auth error", msg.err)
		m.recordSessionError("Logout failed: " + msg.err.Error())
		return m, nil
	}
	m.clearNoticesByCategory("auth")
	m.authStatus = authStatusFromAppWire(msg.resp.Status)
	m.authStatusSeen = true
	m.clearSessionError()
	if msg.resp.Removed {
		m.addSessionSystem("OpenAI sign-out complete. " + formatAuthStatusSummary(m.authStatus))
	} else {
		m.addSessionSystem("OpenAI auth was already signed out. " + formatAuthStatusSummary(m.authStatus))
	}
	return m, nil
}

func (m hubModel) handleInstanceList(msg launchconfig.InstanceListResultMsg) (tea.Model, tea.Cmd) {
	if m.credentialsPanel != nil {
		updated, cmd := m.credentialsPanel.Update(msg)
		panel := updated.(launchconfig.CredentialsPanel)
		m.credentialsPanel = &panel
		return m, cmd
	}
	return m, nil
}

func (m hubModel) handleInstanceMutateResult(msg launchconfig.InstanceMutateResultMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.err = msg.Err
		return m, nil
	}
	m.err = nil
	// Refresh the panel with the updated list returned by the mutation.
	if m.credentialsPanel != nil {
		updated, cmd := m.credentialsPanel.Update(launchconfig.InstanceListResultMsg{List: msg.List})
		panel := updated.(launchconfig.CredentialsPanel)
		m.credentialsPanel = &panel
		return m, cmd
	}
	return m, nil
}

func (m hubModel) handleInstanceSetDefault(msg launchconfig.InstanceSetDefaultMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdInstanceSetDefault(m.client, msg.Name)
	}
	return m, nil
}

func (m hubModel) handleInstanceRemove(msg launchconfig.InstanceRemoveMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdInstanceRemove(m.client, msg.Name)
	}
	return m, nil
}

func (m hubModel) handleInstanceCreateSubmit(msg launchconfig.InstanceCreateSubmitMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdInstanceCreate(m.client, msg.Params)
	}
	return m, nil
}

func (m hubModel) handleInstanceEditSubmit(msg launchconfig.InstanceEditSubmitMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdInstanceEdit(m.client, msg.Params)
	}
	return m, nil
}

func (m hubModel) handleCredentialsAction(msg launchconfig.CredentialsActionMsg) (tea.Model, tea.Cmd) {
	switch msg.Action {
	case "set":
		modal := tuipick.NewTextInputModalMasked(fmt.Sprintf("API key for %s:", msg.Instance), "credential-set:"+msg.Instance)
		m.followupModal = &modal
		return m, nil
	case "logout":
		if m.client != nil {
			return m, launchconfig.CmdAuthLogout(m.client, msg.Instance)
		}
		return m, nil
	case "oauth":
		if m.client != nil {
			return m, launchconfig.CmdAuthLoginStart(m.client, msg.Instance)
		}
		return m, nil
	}
	return m, nil
}

func (m hubModel) handleLaunchOverridesOpen(msg launchconfig.LaunchOverridesOpenMsg) (tea.Model, tea.Cmd) {
	var modal launchconfig.LaunchOverridesModal
	if msg.Initial != nil {
		modal = launchconfig.NewLaunchOverridesModalWith(*msg.Initial)
	} else {
		modal = launchconfig.NewLaunchOverridesModal()
	}
	m.launchOverridesModal = &modal
	if m.client != nil {
		return m, launchconfig.CmdLaunchSchema(m.client)
	}
	return m, nil
}

func (m hubModel) handleLaunchOverridesResult(msg launchconfig.LaunchOverridesResultMsg) (tea.Model, tea.Cmd) {
	m.launchOverridesModal = nil
	if !msg.Cancelled {
		m.spawnLaunchOverrides = msg.Overrides
	}
	return m, nil
}

func (m hubModel) handleLaunchSettingsEditRequest(msg launchconfig.LaunchSettingsEditRequestMsg) (tea.Model, tea.Cmd) {
	if msg.Layer == "launch" {
		prompt := fmt.Sprintf("Edit %s (current: %s):", msg.Field, msg.CurrentValue)
		if msg.Field == "mcps" {
			prompt = fmt.Sprintf("Edit %s as JSON array, or name:command args... (current: %s):", msg.Field, msg.CurrentValue)
		}
		tag := "launch-override:" + msg.Field
		var modal tuipick.TextInputModal
		if msg.PathCompletion || launchconfig.LaunchSettingsFieldUsesPathCompletion(msg.Field) {
			modal = tuipick.NewPathTextInputModal(prompt, tag, msg.CurrentValue)
		} else {
			modal = tuipick.NewTextInputModalWithInput(prompt, tag, msg.CurrentValue)
		}
		m.followupModal = &modal
		return m, nil
	}
	prompt := fmt.Sprintf("Edit %s.%s (current: %s):", msg.Layer, msg.Field, msg.CurrentValue)
	if msg.Field == "mcps" {
		prompt = fmt.Sprintf("Edit %s.%s as JSON array, or name:command args... (current: %s):", msg.Layer, msg.Field, msg.CurrentValue)
	}
	tag := fmt.Sprintf("settings-edit:%s:%s", msg.Layer, msg.Field)
	var modal tuipick.TextInputModal
	if msg.PathCompletion || launchconfig.LaunchSettingsFieldUsesPathCompletion(msg.Field) {
		modal = tuipick.NewPathTextInputModal(prompt, tag, msg.CurrentValue)
	} else {
		modal = tuipick.NewTextInputModalWithInput(prompt, tag, msg.CurrentValue)
	}
	m.followupModal = &modal
	return m, nil
}

func (m hubModel) handleTextInputResult(msg tuipick.TextInputResultMsg) (tea.Model, tea.Cmd) {
	if strings.HasPrefix(msg.Tag, "credential-set:") {
		provider := strings.TrimPrefix(msg.Tag, "credential-set:")
		m.followupModal = nil
		if msg.Cancelled || msg.Value == "" {
			return m, nil
		}
		if m.client != nil {
			return m, launchconfig.CmdAuthApiKeySet(m.client, provider, msg.Value)
		}
		return m, nil
	}
	if strings.HasPrefix(msg.Tag, "oauth-redirect:") {
		parts := strings.SplitN(strings.TrimPrefix(msg.Tag, "oauth-redirect:"), ":", 2)
		m.followupModal = nil
		if msg.Cancelled || msg.Value == "" {
			return m, nil
		}
		if len(parts) == 2 && m.client != nil {
			return m, launchconfig.CmdAuthLoginComplete(m.client, parts[0], parts[1], msg.Value)
		}
		return m, nil
	}
	if strings.HasPrefix(msg.Tag, "launch-override:") {
		field := strings.TrimPrefix(msg.Tag, "launch-override:")
		m.followupModal = nil
		if msg.Cancelled {
			return m, nil
		}
		if m.launchOverridesModal != nil {
			updated, err := m.launchOverridesModal.ApplyEdit(field, msg.Value)
			if err != nil {
				m.err = err
				return m, nil
			}
			m.launchOverridesModal = &updated
		}
		return m, nil
	}
	if strings.HasPrefix(msg.Tag, "settings-edit:") {
		parts := strings.SplitN(strings.TrimPrefix(msg.Tag, "settings-edit:"), ":", 2)
		if len(parts) != 2 {
			return m, nil
		}
		layer, field := parts[0], parts[1]
		m.followupModal = nil
		if msg.Cancelled {
			return m, nil
		}
		if m.launchSettingsPanel == nil {
			return m, nil
		}
		panel, updatedLayer, err := m.launchSettingsPanel.ApplyEdit(field, msg.Value)
		if err != nil {
			m.err = err
			return m, nil
		}
		m.launchSettingsPanel = &panel
		return m, launchconfig.CmdSetLayer(m.client, panel.CWD(), layer, updatedLayer)
	}
	return m, nil
}

func (m hubModel) handleAuthApiKeySetResult(msg launchconfig.AuthApiKeySetResultMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.err = msg.Err
		return m, nil
	}
	m.err = nil
	if m.credentialsPanel != nil && m.client != nil {
		return m, launchconfig.CmdInstanceList(m.client)
	}
	return m, nil
}

func (m hubModel) handleAuthLoginStartResult(msg launchconfig.AuthLoginStartResultMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.err = msg.Err
		return m, nil
	}
	m.err = nil
	modal := tuipick.NewTextInputModal("Paste full redirect URL after sign-in:\n"+msg.URL, "oauth-redirect:"+msg.Provider+":"+msg.FlowID)
	m.followupModal = &modal
	return m, nil
}

func (m hubModel) handleAuthLoginCompleteResult(msg launchconfig.AuthLoginCompleteResultMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.err = msg.Err
		return m, nil
	}
	m.err = nil
	if m.credentialsPanel != nil && m.client != nil {
		return m, launchconfig.CmdInstanceList(m.client)
	}
	return m, nil
}

func (m hubModel) handleLaunchSetLayerResult(msg launchconfig.LaunchSetLayerResultMsg) (tea.Model, tea.Cmd) {
	if m.launchSettingsPanel != nil {
		updated, cmd := m.launchSettingsPanel.Update(msg)
		p := updated.(launchconfig.LaunchSettingsPanel)
		m.launchSettingsPanel = &p
		if msg.Err == nil && m.client != nil {
			// Refresh the just-saved layer from disk.
			return m, tea.Batch(cmd, launchconfig.CmdGetLayer(m.client, msg.CWD, msg.Layer))
		}
		return m, cmd
	}
	return m, nil
}

func (m hubModel) handleMarketplaceListResult(msg launchconfig.MarketplaceListResultMsg) (tea.Model, tea.Cmd) {
	if m.pluginsPanel != nil {
		updated, cmd := m.pluginsPanel.Update(msg)
		panel := updated.(launchconfig.PluginsPanel)
		m.pluginsPanel = &panel
		return m, cmd
	}
	return m, nil
}

func (m hubModel) handleMarketplaceMutateResult(msg launchconfig.MarketplaceMutateResultMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.err = msg.Err
		return m, nil
	}
	m.err = nil
	if m.pluginsPanel != nil {
		updated, cmd := m.pluginsPanel.Update(launchconfig.MarketplaceListResultMsg{List: msg.List})
		panel := updated.(launchconfig.PluginsPanel)
		m.pluginsPanel = &panel
		return m, cmd
	}
	return m, nil
}

func (m hubModel) handleMarketplaceBrowseResult(msg launchconfig.MarketplaceBrowseResultMsg) (tea.Model, tea.Cmd) {
	if m.pluginsPanel != nil {
		updated, cmd := m.pluginsPanel.Update(msg)
		panel := updated.(launchconfig.PluginsPanel)
		m.pluginsPanel = &panel
		return m, cmd
	}
	return m, nil
}

func (m hubModel) handleMarketplaceAddSubmit(msg launchconfig.MarketplaceAddSubmitMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdMarketplaceAdd(m.client, msg.Params)
	}
	return m, nil
}

func (m hubModel) handleMarketplaceRemove(msg launchconfig.MarketplaceRemoveMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdMarketplaceRemove(m.client, msg.Name)
	}
	return m, nil
}

func (m hubModel) handleMarketplaceRefresh(msg launchconfig.MarketplaceRefreshMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdMarketplaceRefresh(m.client, msg.Name)
	}
	return m, nil
}

func (m hubModel) handleMarketplaceBrowseRequest(msg launchconfig.MarketplaceBrowseRequestMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdMarketplaceBrowse(m.client, msg.Name)
	}
	return m, nil
}

func (m hubModel) handlePluginListResult(msg launchconfig.PluginListResultMsg) (tea.Model, tea.Cmd) {
	if m.pluginsPanel != nil {
		updated, cmd := m.pluginsPanel.Update(msg)
		panel := updated.(launchconfig.PluginsPanel)
		m.pluginsPanel = &panel
		return m, cmd
	}
	return m, nil
}

func (m hubModel) handlePluginMutateResult(msg launchconfig.PluginMutateResultMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		m.err = msg.Err
		return m, nil
	}
	m.err = nil
	if m.pluginsPanel != nil {
		updated, cmd := m.pluginsPanel.Update(launchconfig.PluginListResultMsg{List: msg.List})
		panel := updated.(launchconfig.PluginsPanel)
		m.pluginsPanel = &panel
		return m, cmd
	}
	return m, nil
}

func (m hubModel) handlePluginAction(msg launchconfig.PluginActionMsg) (tea.Model, tea.Cmd) {
	if m.client == nil {
		return m, nil
	}
	switch msg.Action {
	case "install":
		return m, launchconfig.CmdPluginInstall(m.client, msg.Plugin, msg.Marketplace)
	case "upgrade":
		return m, launchconfig.CmdPluginUpgrade(m.client, msg.Plugin, msg.Marketplace)
	case "remove":
		return m, launchconfig.CmdPluginRemove(m.client, msg.Plugin, msg.Marketplace)
	case "enable":
		return m, launchconfig.CmdPluginEnable(m.client, msg.Plugin, msg.Marketplace)
	case "disable":
		return m, launchconfig.CmdPluginDisable(m.client, msg.Plugin, msg.Marketplace)
	}
	return m, nil
}

func (m hubModel) handlePluginSetAutoUpgrade(msg launchconfig.PluginSetAutoUpgradeMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdPluginSetAutoUpgrade(m.client, msg.Plugin, msg.Marketplace, msg.AutoUpgrade)
	}
	return m, nil
}

// handleLaunchResult covers the launch layer/resolve/trust/schema result group,
// which is dispatched as a single multi-type case, so it re-asserts the
// concrete message type to route schema results to the overrides modal.
func (m hubModel) handleLaunchResult(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(launchconfig.LaunchSchemaResultMsg); ok && m.launchOverridesModal != nil {
		updated, cmd := m.launchOverridesModal.Update(msg)
		p := updated.(launchconfig.LaunchOverridesModal)
		m.launchOverridesModal = &p
		return m, cmd
	}
	if m.launchSettingsPanel != nil {
		updated, cmd := m.launchSettingsPanel.Update(msg)
		p := updated.(launchconfig.LaunchSettingsPanel)
		m.launchSettingsPanel = &p
		return m, cmd
	}
	return m, nil
}
