//go:build serffuzz

package launchconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/appwire/appwiretest"
)

func init() {
	fuzzApplyEditCoverage = exerciseLaunchConfigCoverage
}

// exerciseLaunchConfigCoverage replays the package's deterministic behavioral
// tests from FuzzApplyEdit's registered seed corpus. Fuzzing behavior remains
// in FuzzApplyEdit; this helper unions the UI, schema, rendering, and RPC state
// machines into that target's corpus replay.
func exerciseLaunchConfigCoverage(t *testing.T) {
	exerciseClientCommands(t)
	exerciseRemainingStates(t)
	TestLaunchSettingsPanelUsesOverlay(t)
	TestLaunchSettingsPanel_TabSwitch(t)
	TestLaunchSettingsPanel_LoadsGlobalFirst(t)
	TestLaunchSettingsPanel_EditEmitsModalRequest(t)
	TestLaunchSettingsPanel_UsesSchemaRowsWhenAvailable(t)
	TestLaunchSettingsPanel_ProjectSchemaRowsExcludeGlobalOnly(t)
	TestLayerRows_IncludesFastCheapModel(t)
	TestPanel_SurfacesResolveDiagnostics(t)
	TestLayerRows_ListFieldsExposeEditableValues(t)
	TestLaunchSettingsPanel_ApplyEditMCPs(t)
	TestApplyEdit_FastCheapModel(t)
	TestApplyEdit_SandboxValidatesMode(t)
	TestApplyEdit_RejectsMissingSkillDir(t)
	TestApplyEdit_AcceptsExistingMCPConfigFile(t)
	TestApplyEdit_NewSchemaFields(t)
	TestApplyEdit_ModelFallbacksUnsetExplicitEmptyAndReplacement(t)
	TestApplyEdit_OutputFileRejectsMissingParent(t)
	TestValidateLocalLaunchPath_OutputFileRejectsExistingDirectory(t)
	TestApplyEdit_MCPsParsesRowsAndPreservesArgs(t)
	TestApplyEdit_MCPsPreservesSerializedRows(t)
	TestMCPsEditValueRoundTripsArgsWithSpaces(t)
	TestApplyEdit_MCPsRejectsInvalidRows(t)

	TestPluginsPanelUsesOverlay(t)
	TestPluginsPanel_TabSwitch(t)
	TestPluginsPanel_LoadingStates(t)
	TestPluginsPanel_ErrorStates(t)
	TestPluginsPanel_MarketplacesTab_RendersList(t)
	TestPluginsPanel_MarketplacesTab_EmptyState(t)
	TestPluginsPanel_NOpensAddForm(t)
	TestPluginsPanel_AddForm_EscClosesFormOnly(t)
	TestPluginsPanel_AddForm_CyclesKindAndSubmits(t)
	TestPluginsPanel_RefreshEmitsMarketplaceRefreshMsg(t)
	TestPluginsPanel_RemoveEmitsMarketplaceRemoveMsg(t)
	TestPluginsPanel_MarketplaceListResultRefreshesPanel(t)
	TestPluginsPanel_NavigationClampsAtBounds(t)
	TestPluginsPanel_CursorClampsWhenListShrinks(t)
	TestPluginsPanel_EscClosesPanelOnMarketplacesTab(t)
	TestPluginsPanel_BrowseTab_PickerRendersMarketplaceNames(t)
	TestPluginsPanel_BrowseTab_EnterEmitsBrowseRequest(t)
	TestPluginsPanel_BrowseTab_ResultPopulatesCatalog(t)
	TestPluginsPanel_BrowseTab_StaleResultIgnored(t)
	TestPluginsPanel_BrowseTab_InstallEmitsPluginAction(t)
	TestPluginsPanel_BrowseTab_AlreadyInstalledSkipsInstall(t)
	TestPluginsPanel_BrowseTab_EscGoesBackToPickerNotClose(t)
	TestPluginsPanel_InstalledTab_RendersBadges(t)
	TestPluginsPanel_InstalledTab_EmptyState(t)
	TestPluginsPanel_InstalledTab_EnterTogglesEnable(t)
	TestPluginsPanel_InstalledTab_AEmitsAutoUpgradeToggle(t)
	TestPluginsPanel_InstalledTab_UEmitsUpgrade(t)
	TestPluginsPanel_InstalledTab_XEmitsRemove(t)
	TestPluginsPanel_PluginListResultRefreshesPanel(t)
	TestMarketplaceSourceLabel(t)
	TestVersionOrUnknown(t)
	TestNextMarketplaceKind(t)
	TestPluginsPanel_FormSource(t)

	TestCredentialsPanelShowsStatusBadges(t)
	TestCredentialsPanel_RendersList(t)
	TestCredentialsPanel_EnterTriggersSet(t)
	TestCredentialsPanel_GroupsByType(t)
	TestCredentialsPanel_OAuthKeyEmitsOAuth(t)
	TestCredentialsPanel_ClearEmitsLogout(t)
	TestCredentialsPanel_StarEmitsSetDefault(t)
	TestCredentialsPanel_XEmitsRemove(t)
	TestCredentialsPanel_NOpensCreateForm(t)
	TestCredentialsPanel_EOpensEditForm(t)
	TestCredentialsPanel_NavigationSkipsGroupHeaders(t)
	TestCredentialsPanel_EscCloses(t)
	TestCredentialsPanel_CreateFormCapturesType(t)
	TestCredentialsPanel_InstanceListResultRefreshesPanel(t)

	TestLaunchOverridesModalUsesOverlay(t)
	TestLaunchOverridesModal_AddsField(t)
	TestLaunchOverridesModal_ProducesOverrideOnSubmit(t)
	TestLaunchOverridesModal_EscapeCancels(t)
	TestLaunchOverridesModal_UsesSchemaRows(t)
	TestLaunchOverridesModal_SchemaPathFieldRequestsCompletion(t)
	TestLaunchOverridesModal_MCPsRowRequestsEdit(t)
	TestLaunchOverridesModal_ApplyEditMCPs(t)

	TestSchemaRows_SettingsFiltersDefaultableLayerAndKeepsOrder(t)
	TestSchemaRows_OverrideFiltersPerLaunch(t)
	TestSchemaRows_PathFieldsRequestCompletion(t)
	TestSchemaRows_ModelFallbacksDistinguishesUnsetAndExplicitEmpty(t)
	TestSchemaRows_MCPsExposeEditableRows(t)
	TestSchemaRows_OpenAIResponsesContinuationUsesStringDisplay(t)
	TestSchemaRows_ExportATIFProviderHandlesUsesStringDisplay(t)

	TestLaunchOptionValue_AllFields(t)
	TestLaunchOptionValue_Defaults(t)
	TestLaunchOptionValue_ModelFallbacksVariants(t)
	TestMultilineSummary(t)
	TestEnvEditValue(t)
	TestLaunchOptionUsesPathCompletion(t)
	TestLaunchSettingsFieldUsesPathCompletion(t)
	TestLaunchOptionDefaultableInLayer(t)
	TestFormActiveField(t)
	TestToggleAPIStyle(t)
	TestFormAppendAndDeleteChar(t)
	TestApiStyleDisplay(t)
	TestSourceBadgeColor(t)
	TestParseOptionalBool(t)
	TestParseOptionalInt(t)
	TestParseEnvMap(t)
	TestParseModelFallbacks(t)
	TestApplyEdit_ScalarAndNumericFields(t)
	TestApplyEdit_SystemPromptDefaults(t)
	TestApplyEdit_UnsupportedField(t)
	TestApplyEdit_EnvField(t)
	TestMcpEditValue(t)
	TestParseMCPs_Forms(t)
	TestValidateMCPs_Errors(t)
	TestValidateLocalLaunchPath(t)
	TestSplitTrim(t)
}

func exerciseClientCommands(t *testing.T) {
	t.Helper()
	tr := appwiretest.NewScriptedTransport()
	client := appwire.NewClient(tr)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client.Start(ctx)
	go func() {
		for i := 0; i < 30; i++ {
			req := <-tr.Sent()
			tr.DeliverResponse(req.Request.ID, map[string]any{})
		}
	}()
	commands := []tea.Cmd{
		CmdResolveLaunch(client, "/tmp", nil),
		CmdGetLayer(client, "/tmp", "global"),
		CmdSetLayer(client, "/tmp", "global", appwire.LaunchConfigLayer{}),
		CmdTrustRepo(client, "/tmp", "hash"), CmdLaunchSchema(client),
		CmdAuthApiKeySet(client, "openai", "secret"), CmdAuthLogout(client, "openai"),
		CmdAuthLoginStart(client, "openai"), CmdAuthLoginComplete(client, "openai", "flow", "http://local"),
		CmdInstanceList(client), CmdInstanceCreate(client, appwire.InstanceCreateParams{}),
		CmdInstanceEdit(client, appwire.InstanceEditParams{}), CmdInstanceRemove(client, "x"),
		CmdInstanceSetDefault(client, "x"), CmdMarketplaceList(client),
		CmdMarketplaceAdd(client, appwire.MarketplaceAddParams{}), CmdMarketplaceRemove(client, "m"),
		CmdMarketplaceRefresh(client, "m"), CmdMarketplaceBrowse(client, "m"), CmdPluginList(client),
		CmdPluginInstall(client, "p", "m"), CmdPluginUpgrade(client, "p", "m"),
		CmdPluginRemove(client, "p", "m"), CmdPluginEnable(client, "p", "m"),
		CmdPluginDisable(client, "p", "m"), CmdPluginSetAutoUpgrade(client, "p", "m", true),
		NewLaunchSettingsPanel(client, "/tmp").InitialCmd(),
	}
	for _, cmd := range commands {
		if cmd() == nil {
			t.Fatal("client command returned nil message")
		}
	}
}

func exerciseRemainingStates(t *testing.T) {
	t.Helper()
	_ = NewCredentialsPanel().Init()
	_ = NewCredentialsPanel().Done()
	_, _ = (CredentialsPanel{}).Update(struct{}{})
	_ = (CredentialsPanel{formOpen: true}).View()
	_ = (CredentialsPanel{formOpen: true, formEditing: true}).View()
	_ = (CredentialsPanel{loading: false, err: errors.New("x")}).View()
	_ = firstSelectableRow([]panelRow{{header: true}})
	_ = nextSelectableRow(nil, 0, 1)
	_ = (CredentialsPanel{rows: []panelRow{{header: true}}, cursor: 0}).selectedInstance()
	_ = (CredentialsPanel{rows: []panelRow{{}}, cursor: -1}).selectedInstance()
	_ = (CredentialsPanel{}).selectedInstance()
	cred := CredentialsPanel{rows: []panelRow{{header: true}}, cursor: 0}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyUp}, {Type: tea.KeyDown}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune("c")}, {Type: tea.KeyRunes, Runes: []rune("o")}, {Type: tea.KeyRunes, Runes: []rune("*")}, {Type: tea.KeyRunes, Runes: []rune("x")}, {Type: tea.KeyRunes, Runes: []rune("e")}, {Type: tea.KeyRunes, Runes: []rune("?")}} {
		_, _ = cred.updateList(key)
	}
	cred = CredentialsPanel{rows: []panelRow{{entry: &appwire.InstanceEntry{Name: "o", AuthModes: []string{"oauth"}}}}}
	_, cmd := cred.updateList(tea.KeyMsg{Type: tea.KeyEnter})
	_ = cmd()
	cred = CredentialsPanel{rows: []panelRow{{entry: &appwire.InstanceEntry{}}, {entry: &appwire.InstanceEntry{}}}, cursor: 1}
	_, _ = cred.updateList(tea.KeyMsg{Type: tea.KeyUp})
	for _, editing := range []bool{false, true} {
		max := 3
		if editing {
			max = 1
		}
		form := CredentialsPanel{formOpen: true, formEditing: editing}
		_, _ = form.updateForm(tea.KeyMsg{Type: tea.KeyEsc})
		_, _ = form.updateForm(tea.KeyMsg{Type: tea.KeyBackspace})
		for field := 0; field <= max; field++ {
			form.formField = field
			_, _ = form.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
			_, _ = form.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
			model, submit := form.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
			_ = model
			if submit != nil {
				_ = submit()
			}
		}
	}
	blank := CredentialsPanel{}
	blank.formField = 0
	blank.formType = "x"
	blank.formDeleteChar()
	blank.formField = 3
	blank.formDeleteChar()
	blank.formField = 1
	blank.formName = "x"
	blank.formDeleteChar()
	_ = (CredentialsPanel{loading: true}).View()
	_, _ = (CredentialsPanel{instances: nil, cursor: 3}).Update(InstanceListResultMsg{})
	_ = (CredentialsPanel{loading: false}).View()
	_ = (CredentialsPanel{loading: false, rows: []panelRow{{entry: &appwire.InstanceEntry{Name: "x", IsDefault: true, APIStyle: "responses", BaseURL: "url", ActiveSource: "env"}}}}).View()

	modal := NewLaunchOverridesModal()
	_ = modal.Init()
	_ = modal.Done()
	_, _ = modal.Update(struct{}{})
	_, _ = modal.Update(LaunchSchemaResultMsg{Err: errors.New("x")})
	_, _ = modal.Update(LaunchSchemaResultMsg{Schema: appwire.LaunchOptionSchemaResponse{Options: []appwire.LaunchOption{{Field: "model", PerLaunch: true}}}})
	_, _ = modal.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, _ = modal.Update(tea.KeyMsg{Type: tea.KeyDown})
	modal.cursor = 1
	_, _ = modal.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, save := modal.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	_ = save()
	modal.cursor = 99
	_, _ = modal.Update(tea.KeyMsg{Type: tea.KeyEnter})
	modal = NewLaunchOverridesModalWithSchema(appwire.LaunchConfigLayer{}, []appwire.LaunchOption{{Field: "mcps", PerLaunch: true}})
	_, _ = modal.Update(tea.KeyMsg{Type: tea.KeyEnter})
	modal = NewLaunchOverridesModalWithSchema(appwire.LaunchConfigLayer{}, []appwire.LaunchOption{{Field: "sandbox", PerLaunch: true, Kind: "select"}})
	_, _ = modal.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, err := modal.ApplyEdit("unknown", "x"); err == nil {
		t.Fatal("expected invalid modal edit")
	}

	panel := NewLaunchSettingsPanel(nil, "/tmp")
	_ = panel.Init()
	_ = panel.Done()
	_ = panel.CWD()
	_ = panel.InitialCmd()()
	_, _ = panel.Update(struct{}{})
	for _, msg := range []tea.Msg{
		LaunchLayerResultMsg{Err: errors.New("x")}, LaunchLayerResultMsg{Layer: "project"},
		LaunchSchemaResultMsg{Err: errors.New("x")}, LaunchResolveResultMsg{Err: errors.New("x")},
		LaunchSetLayerResultMsg{Err: errors.New("x")}, LaunchSetLayerResultMsg{Layer: "global", Resolved: appwire.LaunchConfigResolved{Diagnostics: []appwire.LaunchConfigDiagnostic{{Message: "d"}}}},
		LaunchTrustResultMsg{Err: errors.New("x")}, LaunchTrustResultMsg{Resolved: appwire.LaunchConfigResolved{Diagnostics: []appwire.LaunchConfigDiagnostic{{Field: "f", Message: "d"}}}},
	} {
		_, _ = panel.Update(msg)
	}
	for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyLeft, tea.KeyRight, tea.KeyUp, tea.KeyDown} {
		_, _ = panel.Update(tea.KeyMsg{Type: key})
	}
	panel.tab = launchTabProject
	panel.cursor = 1
	_, _ = panel.Update(tea.KeyMsg{Type: tea.KeyLeft})
	panel.cursor = 1
	_, _ = panel.Update(tea.KeyMsg{Type: tea.KeyUp})
	panel.tab = launchTabRepo
	_ = panel.renderActiveTab()
	_ = renderRepoView(nil)
	_ = renderRepoView(&appwire.RepoLaunchConfigStatus{Path: "p", Trust: "untrusted", Hash: "h", Preview: "v"})
	_ = renderRepoView(&appwire.RepoLaunchConfigStatus{Trust: "trusted"})
	panel.tab = launchTabProject
	_ = panel.renderActiveTab()
	_, _, _ = panel.ApplyEdit("model", "x")
	panel.tab = launchTabRepo
	panel.resolved.Repo = nil
	_, _ = panel.editCurrent()
	panel.resolved.Repo = &appwire.RepoLaunchConfigStatus{Hash: "h", Trust: "trusted"}
	_, _ = panel.editCurrent()
	panel.resolved.Repo.Trust = "changed"
	_, _ = panel.editCurrent()
	panel.tab = launchTabGlobal
	panel.cursor = 999
	_, _ = panel.editCurrent()
	panel.cursor = 0
	panel.schema = []appwire.LaunchOption{{Field: "sandbox", Label: "", DefaultableLayers: []string{"global"}}}
	originalReadOnly := launchSettingsFieldReadOnly
	launchSettingsFieldReadOnly = func(string) bool { return true }
	_, _ = panel.editCurrent()
	_, _ = modal.Update(tea.KeyMsg{Type: tea.KeyEnter})
	launchSettingsFieldReadOnly = originalReadOnly
	_, _ = panel.editCurrent()
	_, _, _ = panel.ApplyEdit("unknown", "x")
	panel.tab = launchTabProject
	_ = panel.tabName()
	_ = panel.currentLayer()
	_ = launchSchemaRows(nil, appwire.LaunchConfigLayer{}, "global", launchSchemaRowsSettings)
	_ = layerRowForOption(appwire.LaunchOption{Field: "model"}, appwire.LaunchConfigLayer{})
	b := true
	_, _ = launchOptionValue(appwire.LaunchOption{Field: "sandbox_net"}, appwire.LaunchConfigLayer{SandboxNet: &b})
	_, _ = launchOptionValue(appwire.LaunchOption{Field: "sandbox"}, appwire.LaunchConfigLayer{Sandbox: "x"})
	_, _ = launchOptionValue(appwire.LaunchOption{Field: "mcps"}, appwire.LaunchConfigLayer{})
	exerciseApplyEditBranches(t)

	plugins := NewPluginsPanel()
	_ = plugins.Init()
	_ = plugins.Done()
	_, _ = plugins.Update(struct{}{})
	_ = clampCursor(-1, 2)
	plugins.tab = pluginsTab(99)
	_ = plugins.maxCursor()
	_, _ = plugins.updateKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	plugins.formOpen = true
	_ = plugins.View()
	plugins.formKind = marketplaceKindGitHub
	_ = plugins.formView()
	plugins.formKind = marketplaceKindDirectory
	_ = plugins.formView()
	for _, tab := range []pluginsTab{pluginsTabMarketplaces, pluginsTabBrowse, pluginsTabInstalled} {
		p := PluginsPanel{tab: tab, cursor: 1, marketplaces: []appwire.MarketplaceEntry{{}, {}}, plugins: []appwire.PluginEntry{{}, {}}}
		for _, key := range []tea.KeyMsg{{Type: tea.KeyUp}, {Type: tea.KeyDown}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune("r")}, {Type: tea.KeyRunes, Runes: []rune("x")}, {Type: tea.KeyRunes, Runes: []rune("a")}, {Type: tea.KeyRunes, Runes: []rune("u")}, {Type: tea.KeyRunes, Runes: []rune("i")}, {Type: tea.KeyRunes, Runes: []rune("?")}} {
			_, _ = p.updateKeys(key)
		}
	}
	plugins = PluginsPanel{formOpen: true, formField: 1, formValue: "x"}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyBackspace}, {Type: tea.KeyRunes, Runes: []rune("x")}, {Type: tea.KeyTab}, {Type: tea.KeySpace}} {
		_, _ = plugins.updateForm(key)
	}
	plugins.formField = 0
	_, _ = plugins.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	_, _ = plugins.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	_, _ = (PluginsPanel{tab: pluginsTabBrowse}).installSelectedCatalogPlugin()
	_, _ = (PluginsPanel{tab: pluginsTabInstalled}).handleEnter()
	_, _ = (PluginsPanel{}).updateKeys(tea.KeyMsg{Type: tea.KeyTab})
	_, _ = (PluginsPanel{tab: pluginsTabBrowse}).handleEnter()
	_, _ = (PluginsPanel{}).handleRune("r")
	_, _ = (PluginsPanel{}).handleRune("x")
	_, _ = (PluginsPanel{tab: pluginsTabInstalled}).handleRune("a")
	_, _ = (PluginsPanel{cursor: -1}).selectedMarketplaceName()
	_ = (PluginsPanel{tab: pluginsTabBrowse, browseMarketplace: "m", browseLoading: false, browseErr: errors.New("x")}).renderBrowseTab()
	_ = (PluginsPanel{tab: pluginsTabBrowse, browseMarketplace: "m"}).renderBrowseTab()
	_ = (PluginsPanel{tab: pluginsTabBrowse, browseMarketplace: "m", browseLoading: true}).renderBrowseTab()
	_ = (PluginsPanel{tab: pluginsTabBrowse, browseMarketplace: "m", browseCatalog: appwire.MarketplaceBrowseResponse{Plugins: []appwire.MarketplaceCatalogPlugin{{Name: "p"}}}}).renderBrowseTab()
}

func exerciseApplyEditBranches(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ field, value string }{
		{"agent", "x"}, {"context_strategy", "x"}, {"openai_responses_continuation", "x"},
		{"export_atif_provider_handles", "x"}, {"max_subagent_depth", "bad"}, {"app_replay_size", "bad"},
		{"max_subagent_depth", "2"}, {"app_replay_size", "2"},
		{"no_project_prompts", "bad"}, {"no_project_prompts", "true"}, {"sandbox_net", "bad"}, {"sandbox_net", "true"},
		{"system_prompt_mode", "x"}, {"system_prompt_file", file}, {"system_prompt_append_mode", "x"},
		{"system_prompt_append_file", file}, {"system_prompt_append_file", "(default)"}, {"system_prompt_append_text", "(default)"},
		{"trace_file", filepath.Join(dir, "trace")}, {"cpu_profile", filepath.Join(dir, "cpu")},
		{"export_atif_path", filepath.Join(dir, "atif")}, {"plugin_dirs", dir}, {"mcp_configs", file},
		{"system_prompt_append", "a,b"},
	}
	for _, tc := range cases {
		_, _ = applyEdit(appwire.LaunchConfigLayer{}, tc.field, tc.value)
	}
	i := 1
	tr := true
	_ = layerRows(appwire.LaunchConfigLayer{MaxRounds: &i, NoProjectPrompts: &tr})
	originalMarshal := marshalMCPEditSpecs
	marshalMCPEditSpecs = func([]mcpEditSpec) ([]byte, error) { return nil, errors.New("x") }
	_ = mcpEditValue([]appwire.MCPServerSpec{{Name: "x"}})
	marshalMCPEditSpecs = originalMarshal
	parentFile := filepath.Join(dir, "parent")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = validateLocalLaunchPath(filepath.Join(parentFile, "out"), "outputFile")
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	_ = validateLocalLaunchPath(filepath.Join(locked, "out"), "outputFile")
	_ = validateLocalLaunchPath(dir, "command")
	t.Setenv("HOME", dir)
	_ = validateLocalLaunchPath("~/f", "file")
	_, _ = applyEdit(appwire.LaunchConfigLayer{}, "system_prompt_append_file", filepath.Join(dir, "missing"))
}
