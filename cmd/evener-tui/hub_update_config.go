package tui

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
	"primeradiant.com/evener/envvars"
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
	// The hub echoes the instance it normalized the request to, so the
	// paste-back step targets whichever instance the flow was started for.
	m.authLoginProvider = strings.TrimSpace(msg.resp.Provider)
	m.authLoginFlowID = msg.resp.FlowID
	name := authStatusInstanceName(authStatus{Provider: msg.resp.Provider})
	m.addSessionSystem("Sign-in URL for " + name + ":\n" + msg.resp.URL + "\nPaste the full redirect URL and press enter.")
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
	m.addSessionSystem("Sign-in complete for " + authStatusInstanceName(m.authStatus) + ". " + formatAuthStatusSummary(m.authStatus))
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
	// The hub removes whatever that instance actually holds — an OAuth
	// record for the Codex transport, the stored credential (an API key, or
	// a credential JSON for gcp-adc) for everything else — and Removed says
	// whether there was one. Naming the act "sign-out of
	// OpenAI OAuth" is what let /logout delete an API key and report an
	// OAuth sign-out.
	name := authStatusInstanceName(m.authStatus)
	if msg.resp.Removed {
		m.addSessionSystem("Removed the stored credential for " + name + ". " + formatAuthStatusSummary(m.authStatus))
	} else {
		m.addSessionSystem("No stored credential to remove for " + name + ". " + formatAuthStatusSummary(m.authStatus))
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
		switch instanceAppliedErrorInfo(msg.Err) {
		case appwire.ErrorInstanceRemoveApplied:
			// The removal stands: the hub deleted the instance's credential
			// (or its config entry) before a later step failed, and answered
			// with no listing. Reconcile it - refresh the list the mutation
			// could not answer with, let the panel clamp its selection as a
			// removal does, and warn - rather than report a failed remove
			// whose retry targets a missing instance while waiting on the
			// passive evener/auth/updated notification.
			m.err = nil
			m.addInstanceWriteAppliedNotice(
				"Instance removal applied",
				"The instance was removed on the hub before a later step failed; the removal stands.",
				msg.Err,
			)
			return m, m.refreshInstanceListAfterMutation()
		case appwire.ErrorInstanceRenamePersisted:
			// providers.toml carries the new name, so the save is not a
			// failure. Follow the instance to the name the edit submitted -
			// the hub's refreshed registry can still omit the new row, so the
			// follow must not be gated on one listing - and warn.
			m.err = nil
			newName := strings.TrimSpace(msg.RenameTo)
			if m.credentialsPanel != nil && newName != "" {
				m.credentialsPanel.FollowInstance(newName)
			}
			summary := "The instance was renamed on the hub before a later step failed; the rename stands."
			if newName != "" {
				summary = fmt.Sprintf("The instance is now %q on the hub; a later step failed, but the rename stands.", newName)
			}
			m.addInstanceWriteAppliedNotice("Instance rename applied", summary, msg.Err)
			return m, m.refreshInstanceListAfterMutation()
		}
		m.err = msg.Err
		if instanceEndpointConflict(msg.Err) {
			// The hub refused the asserted destination: the name no longer
			// resolves where the row the form was opened on pointed, so the
			// write did not happen. The refusal is the hub's own clear account
			// of it; re-read the listing as well, so the retry asserts the
			// destination now on screen instead of repeating the stale
			// assertion. Without the re-read this is a plain failure whose
			// retry cannot succeed.
			return m, m.refreshInstanceListAfterMutation()
		}
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

// wireErrorInfo returns the appwire.ErrorInfo discriminator an error carries,
// or "" when it is not a wire error or carries none. A wire error decoded by
// the client holds its ErrorData as a map, while one built in-process holds the
// typed struct, so both shapes are read - the same classification
// isQueuedDrainPartial performs for its own discriminator.
func wireErrorInfo(err error) appwire.ErrorInfo {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return ""
	}
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		return data.EvenerErrorInfo
	case map[string]any:
		if raw, ok := data["evenerErrorInfo"].(string); ok {
			return appwire.ErrorInfo(raw)
		}
	}
	return ""
}

// instanceAppliedErrorInfo returns the provider-instance applied-write
// discriminator err carries (ErrorInstanceRemoveApplied or
// ErrorInstanceRenamePersisted), or "" for an ordinary failure.
func instanceAppliedErrorInfo(err error) appwire.ErrorInfo {
	info := wireErrorInfo(err)
	switch info {
	case appwire.ErrorInstanceRemoveApplied, appwire.ErrorInstanceRenamePersisted:
		return info
	}
	return ""
}

// instanceEndpointConflict reports whether err is the hub's refusal of an
// asserted endpoint (appwire.Conflict, evenerErrorInfo "endpointConflict"): the
// name no longer resolves to the destination the client showed the user. The
// discriminator is the wire string, never the code - siblings share
// CodeConflict, and a genuine conflict (a rename onto an occupied name) must
// keep its own refusal rather than be reconciled as a moved endpoint.
func instanceEndpointConflict(err error) bool {
	return wireErrorInfo(err) == appwire.ErrorEndpointConflict
}

// refreshInstanceListAfterMutation re-reads the instance list after a mutation
// the hub could not answer with a listing: an applied write answered with a
// discriminator, or an endpoint-conflict refusal - both leave the panel's rows
// describing the destination the write was decided against. The panel owns the
// rows, so a model without one has nothing to refresh.
func (m hubModel) refreshInstanceListAfterMutation() tea.Cmd {
	if m.credentialsPanel != nil && m.client != nil {
		return launchconfig.CmdInstanceList(m.client)
	}
	return nil
}

// addInstanceWriteAppliedNotice reports a provider-instance write that stood
// before a later step failed. The hub answers such a write with an applied
// discriminator, so it is not a failure the user can retry; the notice wears
// the warning tone rather than the error line's plain failure, and repeats the
// hub's own account of what was left behind.
func (m *hubModel) addInstanceWriteAppliedNotice(title, summary string, err error) {
	m.addNotice(noticePanel{
		Title:      title,
		Category:   "instance",
		Summary:    summary,
		Source:     m.sourceLabelForNotice(),
		Reason:     err.Error(),
		NextAction: "The provider list was refreshed; reopen it to check the standing write.",
		State:      "warning",
	})
}

func (m hubModel) handleInstanceSetDefault(msg launchconfig.InstanceSetDefaultMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdInstanceSetDefault(m.client, msg.Name)
	}
	return m, nil
}

func (m hubModel) handleInstanceRemove(msg launchconfig.InstanceRemoveMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		return m, launchconfig.CmdInstanceRemove(m.client, msg.Name, msg.EndpointFingerprint)
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
	case "setCredentialJson":
		modal := tuipick.NewCredentialPasteModal(
			"Credential JSON for "+msg.Instance,
			"Paste a service-account key or application_default_credentials.json.\nTo read it from a file instead, cancel and press f.",
			"credential-json-set:"+msg.Instance)
		m.followupModal = &modal
		return m, nil
	case "loadCredentialJson":
		modal := tuipick.NewPathTextInputModal(
			"Path to the credential JSON for "+msg.Instance+":",
			"credential-json-file:"+msg.Instance, "")
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
	case "test":
		if m.client != nil {
			return m, launchconfig.CmdAuthTest(m.client, msg.Instance, msg.Generation)
		}
		return m, nil
	}
	return m, nil
}

func (m hubModel) handleAuthTestResult(msg launchconfig.AuthTestResultMsg) (tea.Model, tea.Cmd) {
	if m.credentialsPanel == nil {
		return m, nil
	}
	updated, cmd := m.credentialsPanel.Update(msg)
	panel := updated.(launchconfig.CredentialsPanel)
	m.credentialsPanel = &panel
	return m, cmd
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
		// The resolve supplies the modal's "(default)" labels: unset
		// overrides render the effective value a session started now would
		// inherit for this working directory.
		return m, tea.Batch(
			launchconfig.CmdLaunchSchema(m.client),
			launchconfig.CmdResolveLaunch(m.client, m.launchOverridesCWD(), nil),
		)
	}
	return m, nil
}

// launchOverridesCWD is the working directory a session started now would
// inherit — the spawn form's Dir when set, else the selected dashboard
// project's directory — which is what the overrides modal resolves its
// "(default)" labels against.
func (m hubModel) launchOverridesCWD() string {
	if dir := strings.TrimSpace(m.spawnDir); dir != "" {
		return dir
	}
	return m.spawnWorkingDir()
}

func (m hubModel) handleLaunchOverridesResult(msg launchconfig.LaunchOverridesResultMsg) (tea.Model, tea.Cmd) {
	m.launchOverridesModal = nil
	if !msg.Cancelled {
		m.spawnLaunchOverrides = msg.Overrides
		cmd := m.requestSpawnPluginPreview()
		return m, cmd
	}
	return m, nil
}

func (m hubModel) handlePluginPreviewResult(msg launchconfig.PluginPreviewResultMsg) (tea.Model, tea.Cmd) {
	if m.mode != hubModeSpawn || !m.spawnHarnessSupportsPlugins() || msg.Key != m.spawnPluginPreviewRequestKey {
		return m, nil
	}
	m.spawnPluginPreviewLoading = false
	if msg.Err != nil {
		if m.spawnPluginPreviewParamsDigest != m.spawnPluginPreviewLastSuccess {
			m.spawnPluginPreview = appwire.PluginPreviewResponse{}
			m.spawnPluginPreviewLoaded = false
		}
		m.spawnPluginPreviewErr = msg.Err
		return m.forwardSpawnPluginPreviewToPanel(msg)
	}
	m.spawnPluginPreviewErr = nil
	m.spawnPluginPreviewLoaded = true
	m.spawnPluginPreview = msg.Response
	m.spawnPluginPreviewLastSuccess = m.spawnPluginPreviewParamsDigest
	return m.forwardSpawnPluginPreviewToPanel(msg)
}

func (m hubModel) handlePluginsForLaunchResult(msg launchconfig.PluginsForLaunchResultMsg) (tea.Model, tea.Cmd) {
	if !m.spawnHarnessSupportsPlugins() {
		m.spawnPluginsPanel = nil
		return m, nil
	}
	if msg.Retry {
		cmd := m.requestSpawnPluginPreview()
		return m, cmd
	}
	m.spawnPluginsPanel = nil
	if msg.Cancelled || !msg.Applied || msg.EnabledPlugins == nil {
		return m, nil
	}
	updated := appwire.LaunchConfigLayer{}
	if m.spawnLaunchOverrides != nil {
		updated = *m.spawnLaunchOverrides
	}
	values := append([]string(nil), (*msg.EnabledPlugins)...)
	updated.EnabledPlugins = &values
	m.spawnLaunchOverrides = &updated
	cmd := m.requestSpawnPluginPreview()
	return m, cmd
}

func (m hubModel) forwardSpawnPluginPreviewToPanel(msg launchconfig.PluginPreviewResultMsg) (tea.Model, tea.Cmd) {
	if m.spawnPluginsPanel == nil {
		return m, nil
	}
	updated, cmd := m.spawnPluginsPanel.Update(msg)
	panel := updated.(launchconfig.PluginsForLaunchPanel)
	m.spawnPluginsPanel = &panel
	return m, cmd
}

func (m hubModel) handleLaunchSettingsEditRequest(msg launchconfig.LaunchSettingsEditRequestMsg) (tea.Model, tea.Cmd) {
	if msg.Layer == "launch" {
		prompt := fmt.Sprintf("Edit %s (current: %s):", msg.Field, msg.CurrentValue)
		if msg.Field == "mcps" {
			prompt = fmt.Sprintf("Edit %s as JSON array, or name:command args... (current: %s):", msg.Field, msg.CurrentValue)
		}
		if msg.Field == "sandbox" {
			prompt = fmt.Sprintf("Edit %s (off, read-only, workspace-write, restricted, or blank to inherit) (current: %s):", msg.Field, msg.CurrentValue)
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
	if msg.Field == "sandbox" {
		prompt = fmt.Sprintf("Edit %s.%s (off, read-only, workspace-write, restricted, or blank to inherit) (current: %s):", msg.Layer, msg.Field, msg.CurrentValue)
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
	if provider, ok := strings.CutPrefix(msg.Tag, "credential-set:"); ok {
		m.followupModal = nil
		if msg.Cancelled || msg.Value == "" {
			return m, nil
		}
		if m.client != nil {
			return m, launchconfig.CmdAuthApiKeySet(m.client, provider, msg.Value)
		}
		return m, nil
	}
	if provider, ok := strings.CutPrefix(msg.Tag, "credential-json-set:"); ok {
		m.followupModal = nil
		value := strings.TrimSpace(msg.Value)
		if msg.Cancelled || value == "" || m.client == nil {
			return m, nil
		}
		m.err = nil
		return m, launchconfig.CmdAuthCredentialJsonSet(m.client, provider, value)
	}
	if provider, ok := strings.CutPrefix(msg.Tag, "credential-json-file:"); ok {
		m.followupModal = nil
		path := strings.TrimSpace(msg.Value)
		if msg.Cancelled || path == "" || m.client == nil {
			return m, nil
		}
		m.err = nil
		client := m.client
		// The read happens inside the command, off the update loop, so a slow
		// or unreadable path cannot hold up the interface. Its failure takes
		// the same route as the hub's own, so both reach the error line.
		return m, func() tea.Msg {
			document, err := readCredentialFile(path)
			if err != nil {
				return launchconfig.AuthApiKeySetResultMsg{Err: err}
			}
			return launchconfig.CmdAuthCredentialJsonSet(client, provider, document)()
		}
	}
	if rest, ok := strings.CutPrefix(msg.Tag, "oauth-redirect:"); ok {
		parts := strings.SplitN(rest, ":", 2)
		m.followupModal = nil
		if msg.Cancelled || msg.Value == "" {
			return m, nil
		}
		if len(parts) == 2 && m.client != nil {
			return m, launchconfig.CmdAuthLoginComplete(m.client, parts[0], parts[1], msg.Value)
		}
		return m, nil
	}
	if field, ok := strings.CutPrefix(msg.Tag, "launch-override:"); ok {
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
	if rest, ok := strings.CutPrefix(msg.Tag, "settings-edit:"); ok {
		parts := strings.SplitN(rest, ":", 2)
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

// readCredentialFile reads a credential document from a path the user gave,
// on the machine they typed it on rather than the hub's. Its caller runs it
// inside a command, off the update loop, so a slow filesystem cannot hold up
// the interface; the file is still opened without blocking, so a path that
// names a pipe cannot leave that command waiting forever either. What the
// open descriptor actually is decides whether it is read, and the read is
// bounded — checking the path and then opening it by name again would leave
// a window for it to become something else. The hub validates the document,
// so no parsing happens here.
func readCredentialFile(path string) (string, error) {
	if strings.HasPrefix(path, "~/") || path == "~" {
		path = filepath.Join(envvars.Home.Getenv(), strings.TrimPrefix(path, "~"))
	}
	f, err := os.OpenFile(path, os.O_RDONLY|nonblockingOpen, 0)
	if err != nil {
		return "", credentialPathError(err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", credentialPathError(err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("credential JSON: the path it was read as is not a regular file")
	}
	// One byte past the bound, so a file at exactly the bound still reads and
	// anything longer is refused however it grew.
	data, err := io.ReadAll(io.LimitReader(f, maxCredentialFileBytes+1))
	if err != nil {
		return "", credentialPathError(err)
	}
	if len(data) > maxCredentialFileBytes {
		return "", fmt.Errorf("credential JSON: the file it was read as is too large (over %d bytes)", maxCredentialFileBytes)
	}
	return string(data), nil
}

// maxCredentialFileBytes bounds the file the prompt will read. The largest
// real credential document is a service-account key of a few kilobytes.
const maxCredentialFileBytes = 1 << 20

// credentialPathError reports why a submitted value could not be read as a
// path without repeating the value: it is either a mistyped path or a secret
// pasted into the wrong prompt, and this error is rendered and outlives the
// panel. A PathError's own message names the path, so only its reason is kept.
func credentialPathError(err error) error {
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		err = pathErr.Err
	}
	return fmt.Errorf("credential JSON: it does not start with %q, and the path it was read as could not be opened: %w", "{", err)
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
	// Once a removal has landed, a read issued before that landing - a
	// generation at or below the floor - is stale however late it arrives:
	// its snapshot predates the removal, so accepting it, even after the
	// fence has settled, would resurrect the removed marketplace's row.
	// The boundary outlives the fence, and every removal outcome advances
	// it, so only reads issued after the latest removal can ever land.
	if m.marketplaceListReadsOrdered && msg.ReconcileGeneration <= m.marketplaceListFloor {
		return m, nil
	}
	if m.marketplaceListApplied != 0 && msg.ReconcileGeneration <= m.marketplaceListApplied {
		// A read at or below the applied generation was issued before the
		// newest applied one, and its snapshot was superseded - landing it
		// late would overwrite the newer list with an older one. The
		// rejection is not bound to the removal boundary: before the first
		// removal ever lands, add and refresh responses advance the
		// applied generation the same way, so a delayed read from that
		// window cannot clobber the marketplace a newer response just
		// landed. A zero applied generation is nothing applied yet, not an
		// ordering decision, so it rejects nothing - least of all the
		// first read.
		return m, nil
	}
	// A successful read above the floor was issued after the removal
	// landed, so it confirms the post-removal state whichever refresh
	// issued it: an outstanding reconciliation must not be invalidated by
	// a newer refresh merely having been issued, and discarding its
	// success would leave the fence standing when the newer read fails.
	if msg.Err == nil && m.marketplaceReconcilePending {
		// The confirming read also settles the account its outcome left
		// standing: the removed outcome's "being refreshed" warning is
		// entirely stale now that the refreshed list has landed, so it
		// clears; the unavailable outcome's "could not be confirmed"
		// uncertainty went with it, while the clone files that remain on
		// disk are still the truth and keep their account without the
		// stale suffix. Stale reads never reach here - the guards above
		// rejected them without touching the warning, exactly like the
		// discarded add or refresh responses below.
		switch state, _ := classifyMarketplaceRemovalOutcome(m.marketplaceOutcomeWarning); state {
		case marketplaceRemovalRemoved:
			m.marketplaceOutcomeWarning = nil
		case marketplaceRemovalUnavailable:
			if wire, ok := errors.AsType[appwire.WireError](m.marketplaceOutcomeWarning); ok {
				m.marketplaceOutcomeWarning = marketplaceCloneRemainsWarning(wire, false)
			}
		}
		m.marketplaceRemovePending = ""
		m.marketplaceReconcilePending = false
	}
	// While the fence is still standing, hold back failed reads older than
	// the newest LIST READ issued - the newest read's failure is the news,
	// and it is the panel's only way out of its loading state - while an
	// add or refresh issuance, which shares the ordering counter, never
	// outranks it. A successful read reaches the panel: it has already
	// settled the fence above.
	if m.marketplaceReconcilePending && msg.ReconcileGeneration != m.marketplaceListReadIssued {
		return m, nil
	}
	if msg.Err == nil {
		m.marketplaceListApplied = msg.ReconcileGeneration
		m.marketplaceListAppliedRows = msg.List.Marketplaces
	}
	if m.pluginsPanel != nil {
		updated, cmd := m.pluginsPanel.Update(msg)
		panel := updated.(launchconfig.PluginsPanel)
		m.pluginsPanel = &panel
		return m, cmd
	}
	return m, nil
}

// marketplaceListRead returns the marketplace-list read this model should
// issue for a user- or notification-driven refetch. Every read carries the
// next reconciliation generation from the start: once a removal has landed,
// the read boundary rejects every read issued before the latest landing,
// and an older read could then neither recover a failed reconciliation,
// nor settle the fence, nor populate a reopened panel; and before the
// first removal lands, the applied-generation guard still rejects a read
// issued before the newest applied response, so a delayed pre-removal read
// cannot clobber the marketplace a newer add or refresh just landed.
// Tagging every read is also what keeps a fresh refetch newer than
// everything applied, so it always lands. The duplicate-remove fence is
// untouched here; only a successful read clears it.
func (m *hubModel) marketplaceListRead() tea.Cmd {
	m.marketplaceReconcileGeneration++
	m.marketplaceListReadIssued = m.marketplaceReconcileGeneration
	return launchconfig.CmdMarketplaceReconcileList(m.client, m.marketplaceReconcileGeneration)
}

// batchMarketplaceCmds batches the panel command a marketplace response
// produced with the replacement read it scheduled; either may be nil.
func batchMarketplaceCmds(first, second tea.Cmd) tea.Cmd {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	return tea.Batch(first, second)
}

// applyRemovalSnapshot applies a landed removal's own list snapshot,
// ordered against the read boundary exactly like any other list-bearing
// response, and returns the panel's command for the caller to batch. A
// snapshot whose generation predates the latest landed removal - armed
// and at or below the floor - is stale like an add or refresh of that
// vintage: discard it; the caller's replacement read converges the panel.
// One issued after that landing but at or below the newest applied
// generation would resurrect rows the applied read already dropped, so
// the panel keeps the applied rows minus the removed marketplace
// instead. One fresher than everything applied - nothing applied yet
// counts as freshest - lands wholesale and becomes the newest applied
// state.
func (m *hubModel) applyRemovalSnapshot(removed string, snapshot appwire.MarketplaceListResponse, generation uint64) tea.Cmd {
	if m.pluginsPanel == nil {
		return nil
	}
	list := snapshot
	switch {
	case m.marketplaceListReadsOrdered && generation <= m.marketplaceListFloor:
		return nil
	case m.marketplaceListApplied != 0 && generation <= m.marketplaceListApplied:
		merged := make([]appwire.MarketplaceEntry, 0, len(m.marketplaceListAppliedRows))
		for _, entry := range m.marketplaceListAppliedRows {
			if entry.Name != removed {
				merged = append(merged, entry)
			}
		}
		m.marketplaceListAppliedRows = merged
		list = appwire.MarketplaceListResponse{Marketplaces: merged}
	default:
		m.marketplaceListApplied = generation
		m.marketplaceListAppliedRows = snapshot.Marketplaces
	}
	updated, cmd := m.pluginsPanel.Update(launchconfig.MarketplaceListResultMsg{List: list})
	panel := updated.(launchconfig.PluginsPanel)
	m.pluginsPanel = &panel
	return cmd
}

func (m hubModel) handleMarketplaceMutateResult(msg launchconfig.MarketplaceMutateResultMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		if msg.Action == "remove" && msg.Name == m.marketplaceRemovePending {
			switch state, applied := classifyMarketplaceRemovalOutcome(msg.Err); state {
			case marketplaceRemovalApplied:
				m.marketplaceOutcomeWarning = marketplaceCloneRemainsWarning(msg.Err, false)
				m.marketplaceRemovePending = ""
				m.marketplaceReconcilePending = false
				// Order the removal's own snapshot against the read boundary
				// exactly like any other list-bearing response - judged against
				// the floor as it stood when this response was issued - and arm
				// the boundary only after, so the response's own generation is
				// never misread as stale by its own arm.
				panelCmd := m.applyRemovalSnapshot(msg.Name, applied, msg.Generation)
				m.marketplaceListReadsOrdered = true
				m.marketplaceListFloor = m.marketplaceReconcileGeneration
				// Reads issued after the hub landed this removal but
				// before its result was handled - a notification refetch
				// is typical - are stale to the floor this branch just
				// set, although they can be newer than the snapshot:
				// another client's change can have landed in between.
				// Schedule the replacement read that settles the panel
				// on it.
				var replacement tea.Cmd
				if m.client != nil {
					m.marketplaceReconcileGeneration++
					m.marketplaceListReadIssued = m.marketplaceReconcileGeneration
					replacement = launchconfig.CmdMarketplaceReconcileList(m.client, m.marketplaceReconcileGeneration)
				}
				return m, batchMarketplaceCmds(panelCmd, replacement)
			case marketplaceRemovalUnavailable:
				m.marketplaceOutcomeWarning = marketplaceCloneRemainsWarning(msg.Err, true)
				m.marketplaceListReadsOrdered = true
				m.marketplaceListFloor = m.marketplaceReconcileGeneration
				m.marketplaceReconcilePending = true
				if m.client != nil {
					m.marketplaceReconcileGeneration++
					m.marketplaceListReadIssued = m.marketplaceReconcileGeneration
					return m, launchconfig.CmdMarketplaceReconcileList(m.client, m.marketplaceReconcileGeneration)
				}
				return m, nil
			case marketplaceRemovalRemoved:
				// The removal and its clone cleanup both landed; only the
				// fresh list read failed, and the marker carries no
				// snapshot (appwire.MarketplaceRemoveAppliedData), so
				// reconcile from a fresh read rather than retrying, and
				// never claim litter - nothing was left on disk.
				m.marketplaceOutcomeWarning = marketplaceRemovedWarning(msg.Err)
				m.marketplaceListReadsOrdered = true
				m.marketplaceListFloor = m.marketplaceReconcileGeneration
				m.marketplaceReconcilePending = true
				if m.client != nil {
					m.marketplaceReconcileGeneration++
					m.marketplaceListReadIssued = m.marketplaceReconcileGeneration
					return m, launchconfig.CmdMarketplaceReconcileList(m.client, m.marketplaceReconcileGeneration)
				}
				return m, nil
			}
		}
		m.err = msg.Err
		if msg.Action == "remove" && msg.Name == m.marketplaceRemovePending {
			m.marketplaceRemovePending = ""
			m.marketplaceReconcilePending = false
		}
		return m, nil
	}
	if msg.Action == "remove" {
		m.err = nil
		// Every landed remove response - the success this client issued,
		// a settled duplicate the fence no longer knows, or an out-of-band
		// removal another client performed - converges the panel through
		// one sequence: clear the fence if this is the pending name, order
		// the response's own snapshot against the read boundary as that
		// boundary stood when the removal was issued, then arm the
		// boundary at the latest landing and schedule the replacement read
		// that settles every read the arm just invalidated.
		if msg.Name == m.marketplaceRemovePending {
			m.marketplaceRemovePending = ""
			m.marketplaceReconcilePending = false
		}
		panelCmd := m.applyRemovalSnapshot(msg.Name, msg.List, msg.Generation)
		m.marketplaceListReadsOrdered = true
		m.marketplaceListFloor = m.marketplaceReconcileGeneration
		var replacement tea.Cmd
		if m.client != nil {
			// The same replacement the applied-snapshot branch schedules:
			// the floor this branch just set invalidates reads issued
			// after the hub landed the removal but before this response
			// was handled, and they can be newer than the response's own
			// snapshot.
			m.marketplaceReconcileGeneration++
			m.marketplaceListReadIssued = m.marketplaceReconcileGeneration
			replacement = launchconfig.CmdMarketplaceReconcileList(m.client, m.marketplaceReconcileGeneration)
		}
		return m, batchMarketplaceCmds(panelCmd, replacement)
	}
	if (m.marketplaceListReadsOrdered && msg.Generation <= m.marketplaceListFloor) || (m.marketplaceListApplied != 0 && msg.Generation <= m.marketplaceListApplied) {
		// An add or refresh issued before the latest removal landed - the
		// floor half, armed once any removal landed - or before the newest
		// applied read - this half holds before the first removal too, for
		// the same reason the read guard above gives - carries a list that
		// predates state the panel already holds, so applying it would
		// resurrect what the settled list dropped or clobber the
		// marketplace a newer response just landed. Discard it and
		// schedule the replacement read that lands the mutation's own
		// effect. A discarded response is not this model's news either:
		// like a list read, it must not erase the prominent warning a
		// landed removal outcome left standing - the clone-remains or
		// removed-outcome notice the user still has to act on.
		if m.client != nil {
			m.marketplaceReconcileGeneration++
			m.marketplaceListReadIssued = m.marketplaceReconcileGeneration
			return m, launchconfig.CmdMarketplaceReconcileList(m.client, m.marketplaceReconcileGeneration)
		}
		return m, nil
	}
	// A fresh add or refresh snapshot is a post-removal list read like any
	// other: route it through the list-result logic so it settles a
	// pending reconciliation and advances the applied generation under the
	// same guards that order every other list response. The transient
	// clear is the only account a fresh response supersedes on its own:
	// the ordinary failure the mutation renders stale. The standing
	// outcome account is never this response's to erase - the settle the
	// response is about to route through is the one place that rewrites
	// or retires it, exactly like a confirming read, and the guards above
	// already guaranteed this response reaches that settle.
	m.err = nil
	return m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:                msg.List,
		ReconcileGeneration: msg.Generation,
	})
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
		m.marketplaceReconcileGeneration++
		return m, launchconfig.CmdMarketplaceAdd(m.client, msg.Params, m.marketplaceReconcileGeneration)
	}
	return m, nil
}

func (m hubModel) handleMarketplaceRemove(msg launchconfig.MarketplaceRemoveMsg) (tea.Model, tea.Cmd) {
	if m.marketplaceRemovePending != "" {
		if msg.Name != m.marketplaceRemovePending {
			// A different marketplace's remove while one is still
			// unconfirmed: surface feedback instead of silently dropping
			// the request - the fence tracks one removal at a time.
			m.err = fmt.Errorf("marketplace %q is still being removed; try again once that removal is confirmed", m.marketplaceRemovePending)
		}
		return m, nil
	}
	if m.client != nil {
		m.marketplaceReconcileGeneration++
		m.marketplaceRemovePending = msg.Name
		return m, launchconfig.CmdMarketplaceRemove(m.client, msg.Name, m.marketplaceReconcileGeneration)
	}
	return m, nil
}

func (m hubModel) handleMarketplaceRefresh(msg launchconfig.MarketplaceRefreshMsg) (tea.Model, tea.Cmd) {
	if m.client != nil {
		m.marketplaceReconcileGeneration++
		return m, launchconfig.CmdMarketplaceRefresh(m.client, msg.Name, m.marketplaceReconcileGeneration)
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
	var modalCmd tea.Cmd
	if _, ok := msg.(launchconfig.LaunchResolveResultMsg); ok && m.launchOverridesModal != nil {
		updated, cmd := m.launchOverridesModal.Update(msg)
		p := updated.(launchconfig.LaunchOverridesModal)
		m.launchOverridesModal = &p
		modalCmd = cmd
	}
	if m.launchSettingsPanel != nil {
		updated, cmd := m.launchSettingsPanel.Update(msg)
		p := updated.(launchconfig.LaunchSettingsPanel)
		m.launchSettingsPanel = &p
		return m, tea.Batch(modalCmd, cmd)
	}
	return m, modalCmd
}
