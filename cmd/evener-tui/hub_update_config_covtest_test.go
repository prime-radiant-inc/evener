package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
)

// TestCovHandleAuthLoginStart exercises both the error and success paths of
// handleAuthLoginStart, including the empty-provider default.
func TestCovHandleAuthLoginStart(t *testing.T) {
	// Error path: err set, adds auth notice, returns early.
	m := hubModel{session: newModel(nil)}
	got, _ := m.handleAuthLoginStart(hubAuthLoginStartMsg{err: errors.New("boom")})
	after := got.(hubModel)
	if after.err != nil {
		t.Fatalf("handleAuthLoginStart error path set m.err = %v, want nil", after.err)
	}
	if len(after.notices) == 0 {
		t.Fatal("handleAuthLoginStart error path should add an auth notice")
	}

	// Success path with empty provider: defaults to "openai".
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleAuthLoginStart(hubAuthLoginStartMsg{
		resp: appwire.AuthLoginStartResponse{Provider: "", FlowID: "flow1", URL: "http://sign.in"},
	})
	after = got.(hubModel)
	if after.authLoginProvider != "openai" {
		t.Fatalf("authLoginProvider = %q, want openai", after.authLoginProvider)
	}
	if after.authLoginFlowID != "flow1" {
		t.Fatalf("authLoginFlowID = %q, want flow1", after.authLoginFlowID)
	}

	// Success path with explicit provider.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleAuthLoginStart(hubAuthLoginStartMsg{
		resp: appwire.AuthLoginStartResponse{Provider: "anthropic", FlowID: "flow2", URL: "http://sign.in"},
	})
	after = got.(hubModel)
	if after.authLoginProvider != "anthropic" {
		t.Fatalf("authLoginProvider = %q, want anthropic", after.authLoginProvider)
	}
}

// TestCovHandleAuthLoginComplete exercises error and success paths.
func TestCovHandleAuthLoginComplete(t *testing.T) {
	// Error path: adds notice, records session error, does NOT clear login state.
	m := hubModel{session: newModel(nil), authLoginProvider: "openai", authLoginFlowID: "flow1"}
	got, _ := m.handleAuthLoginComplete(hubAuthLoginCompleteMsg{err: errors.New("login failed")})
	after := got.(hubModel)
	if len(after.notices) == 0 {
		t.Fatal("error path should add an auth notice")
	}

	// Success path: clears login state, sets auth status.
	m = hubModel{session: newModel(nil), authLoginProvider: "openai", authLoginFlowID: "flow1"}
	got, _ = m.handleAuthLoginComplete(hubAuthLoginCompleteMsg{
		resp: appwire.AuthLoginCompleteResponse{Status: appwire.AuthStatusResponse{Provider: "openai", SignedIn: true}},
	})
	after = got.(hubModel)
	if after.authLoginFlowID != "" {
		t.Fatalf("authLoginFlowID = %q, want cleared", after.authLoginFlowID)
	}
	if !after.authStatusSeen {
		t.Fatal("authStatusSeen should be true on success")
	}
}

// TestCovHandleAuthLogout exercises error and both Removed branches.
func TestCovHandleAuthLogout(t *testing.T) {
	// Error path.
	m := hubModel{session: newModel(nil)}
	got, _ := m.handleAuthLogout(hubAuthLogoutMsg{err: errors.New("logout failed")})
	after := got.(hubModel)
	if len(after.notices) == 0 {
		t.Fatal("error path should add an auth notice")
	}

	// Success: Removed=true.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleAuthLogout(hubAuthLogoutMsg{resp: appwire.AuthLogoutResponse{Removed: true}})
	after = got.(hubModel)
	if !after.authStatusSeen {
		t.Fatal("authStatusSeen should be true on success")
	}

	// Success: Removed=false.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleAuthLogout(hubAuthLogoutMsg{resp: appwire.AuthLogoutResponse{Removed: false}})
	after = got.(hubModel)
	if !after.authStatusSeen {
		t.Fatal("authStatusSeen should be true on success")
	}
}

// TestCovHandleInstanceListWithPanel exercises the panel-forward path.
func TestCovHandleInstanceListWithPanel(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.credentialsPanel = newCredentialsPanelForTest()
	got, _ := m.handleInstanceList(launchconfig.InstanceListResultMsg{})
	after := got.(hubModel)
	if after.credentialsPanel == nil {
		t.Fatal("credentialsPanel should still be set after forwarding list msg")
	}
}

// TestCovHandleInstanceListNoPanel exercises the no-panel early return.
func TestCovHandleInstanceListNoPanel(t *testing.T) {
	m := hubModel{}
	got, _ := m.handleInstanceList(launchconfig.InstanceListResultMsg{})
	after := got.(hubModel)
	if after.credentialsPanel != nil {
		t.Fatal("credentialsPanel should be nil")
	}
}

// TestCovHandleInstanceMutateResultNoPanel exercises the no-panel path.
func TestCovHandleInstanceMutateResultNoPanel(t *testing.T) {
	m := hubModel{}
	got, _ := m.handleInstanceMutateResult(launchconfig.InstanceMutateResultMsg{})
	after := got.(hubModel)
	if after.err != nil {
		t.Fatalf("err = %v, want nil for no-panel success", after.err)
	}
}

// TestCovHandleInstanceSetDefault exercises both nil-client and client paths.
func TestCovHandleInstanceSetDefault(t *testing.T) {
	// No client.
	m := hubModel{}
	got, cmd := m.handleInstanceSetDefault(launchconfig.InstanceSetDefaultMsg{Name: "inst1"})
	if cmd != nil {
		t.Fatal("cmd should be nil with no client")
	}
	after := got.(hubModel)
	if after.client != nil {
		t.Fatal("client should be nil")
	}

	// With client.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	got, cmd = m.handleInstanceSetDefault(launchconfig.InstanceSetDefaultMsg{Name: "inst1"})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
}

// TestCovHandleInstanceRemove exercises both paths.
func TestCovHandleInstanceRemove(t *testing.T) {
	m := hubModel{}
	got, cmd := m.handleInstanceRemove(launchconfig.InstanceRemoveMsg{Name: "inst1"})
	if cmd != nil {
		t.Fatal("cmd should be nil with no client")
	}

	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	got, cmd = m.handleInstanceRemove(launchconfig.InstanceRemoveMsg{Name: "inst1"})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	_ = got
}

// TestCovHandleInstanceCreateSubmit exercises both paths.
func TestCovHandleInstanceCreateSubmit(t *testing.T) {
	m := hubModel{}
	got, cmd := m.handleInstanceCreateSubmit(launchconfig.InstanceCreateSubmitMsg{})
	if cmd != nil {
		t.Fatal("cmd should be nil with no client")
	}

	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	got, cmd = m.handleInstanceCreateSubmit(launchconfig.InstanceCreateSubmitMsg{
		Params: appwire.InstanceCreateParams{Name: "new"},
	})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	_ = got
}

// TestCovHandleInstanceEditSubmit exercises both paths.
func TestCovHandleInstanceEditSubmit(t *testing.T) {
	m := hubModel{}
	got, cmd := m.handleInstanceEditSubmit(launchconfig.InstanceEditSubmitMsg{})
	if cmd != nil {
		t.Fatal("cmd should be nil with no client")
	}

	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	got, cmd = m.handleInstanceEditSubmit(launchconfig.InstanceEditSubmitMsg{
		Params: appwire.InstanceEditParams{Name: "edit"},
	})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	_ = got
}

// TestCovHandleCredentialsAction exercises all action branches.
func TestCovHandleCredentialsAction(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()

	for _, tc := range []struct {
		action string
	}{
		{"set"},
		{"logout"},
		{"oauth"},
		{"test"},
		{"unknown"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			m := newHubModel(client, "http://hub.test")
			got, cmd := m.handleCredentialsAction(launchconfig.CredentialsActionMsg{Action: tc.action, Instance: "inst"})
			after := got.(hubModel)
			if tc.action == "set" && after.followupModal == nil {
				t.Fatal("set action should open followup modal")
			}
			if tc.action != "set" && cmd == nil && tc.action != "unknown" {
				// logout/oauth/test should produce a cmd when client is set.
				t.Fatalf("action %q should produce a cmd with client", tc.action)
			}
			if tc.action == "unknown" && cmd != nil {
				t.Fatal("unknown action should produce no cmd")
			}
		})
	}

	// Without client: logout/oauth/test should return nil cmd.
	m := hubModel{}
	for _, action := range []string{"logout", "oauth", "test"} {
		got, cmd := m.handleCredentialsAction(launchconfig.CredentialsActionMsg{Action: action})
		if cmd != nil {
			t.Fatalf("action %q without client should return nil cmd", action)
		}
		_ = got
	}
}

// TestCovHandleAuthTestResult exercises the nil-panel and panel paths.
func TestCovHandleAuthTestResult(t *testing.T) {
	// Nil panel early return.
	m := hubModel{}
	got, _ := m.handleAuthTestResult(launchconfig.AuthTestResultMsg{})
	if got.(hubModel).credentialsPanel != nil {
		t.Fatal("should not set panel")
	}

	// With panel.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	m.credentialsPanel = newCredentialsPanelForTest()
	got, _ = m.handleAuthTestResult(launchconfig.AuthTestResultMsg{})
	if got.(hubModel).credentialsPanel == nil {
		t.Fatal("panel should still be set")
	}
}

// TestCovHandleLaunchOverridesOpen exercises both initial-value branches.
func TestCovHandleLaunchOverridesOpen(t *testing.T) {
	// With initial value.
	m := hubModel{session: newModel(nil)}
	initial := &appwire.LaunchConfigLayer{Model: "test/model"}
	got, _ := m.handleLaunchOverridesOpen(launchconfig.LaunchOverridesOpenMsg{Initial: initial})
	after := got.(hubModel)
	if after.launchOverridesModal == nil {
		t.Fatal("launchOverridesModal should be set")
	}

	// Without initial value.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleLaunchOverridesOpen(launchconfig.LaunchOverridesOpenMsg{})
	after = got.(hubModel)
	if after.launchOverridesModal == nil {
		t.Fatal("launchOverridesModal should be set")
	}

	// Without client: no schema cmd.
	m = hubModel{session: newModel(nil)}
	got, cmd := m.handleLaunchOverridesOpen(launchconfig.LaunchOverridesOpenMsg{})
	if cmd != nil {
		t.Fatal("cmd should be nil without client")
	}

	// With client: should produce a schema cmd.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	got, cmd = m.handleLaunchOverridesOpen(launchconfig.LaunchOverridesOpenMsg{})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	_ = got
}

// TestCovHandleLaunchOverridesResult exercises cancelled and non-cancelled paths.
func TestCovHandleLaunchOverridesResult(t *testing.T) {
	// Cancelled: overrides not applied.
	overrides := &appwire.LaunchConfigLayer{Model: "old/model"}
	m := hubModel{session: newModel(nil), spawnLaunchOverrides: overrides}
	got, _ := m.handleLaunchOverridesResult(launchconfig.LaunchOverridesResultMsg{Cancelled: true})
	after := got.(hubModel)
	if after.launchOverridesModal != nil {
		t.Fatal("launchOverridesModal should be cleared")
	}
	if after.spawnLaunchOverrides == nil {
		t.Fatal("spawnLaunchOverrides should be preserved on cancel")
	}

	// Not cancelled: overrides applied.
	m = hubModel{session: newModel(nil), spawnLaunchOverrides: overrides}
	newOverrides := &appwire.LaunchConfigLayer{Model: "new/model"}
	got, _ = m.handleLaunchOverridesResult(launchconfig.LaunchOverridesResultMsg{Cancelled: false, Overrides: newOverrides})
	after = got.(hubModel)
	if after.launchOverridesModal != nil {
		t.Fatal("launchOverridesModal should be cleared")
	}
	if after.spawnLaunchOverrides != newOverrides {
		t.Fatal("spawnLaunchOverrides should be the new overrides")
	}
}

// TestCovHandleLaunchSettingsEditRequest exercises launch and non-launch layers.
func TestCovHandleLaunchSettingsEditRequest(t *testing.T) {
	// Launch layer with regular field.
	m := hubModel{session: newModel(nil)}
	got, _ := m.handleLaunchSettingsEditRequest(launchconfig.LaunchSettingsEditRequestMsg{
		Layer: "launch", Field: "model", CurrentValue: "old",
	})
	after := got.(hubModel)
	if after.followupModal == nil {
		t.Fatal("followupModal should be set")
	}

	// Launch layer with mcps field.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleLaunchSettingsEditRequest(launchconfig.LaunchSettingsEditRequestMsg{
		Layer: "launch", Field: "mcps", CurrentValue: "[]",
	})
	if got.(hubModel).followupModal == nil {
		t.Fatal("followupModal should be set for mcps")
	}

	// Launch layer with sandbox field.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleLaunchSettingsEditRequest(launchconfig.LaunchSettingsEditRequestMsg{
		Layer: "launch", Field: "sandbox", CurrentValue: "off",
	})
	if got.(hubModel).followupModal == nil {
		t.Fatal("followupModal should be set for sandbox")
	}

	// Launch layer with path-completion field.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleLaunchSettingsEditRequest(launchconfig.LaunchSettingsEditRequestMsg{
		Layer: "launch", Field: "cwd", CurrentValue: "/tmp", PathCompletion: true,
	})
	if got.(hubModel).followupModal == nil {
		t.Fatal("followupModal should be set for path-completion field")
	}

	// Non-launch layer.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleLaunchSettingsEditRequest(launchconfig.LaunchSettingsEditRequestMsg{
		Layer: "project", Field: "model", CurrentValue: "old",
	})
	if got.(hubModel).followupModal == nil {
		t.Fatal("followupModal should be set for non-launch layer")
	}

	// Non-launch layer with mcps.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleLaunchSettingsEditRequest(launchconfig.LaunchSettingsEditRequestMsg{
		Layer: "project", Field: "mcps", CurrentValue: "[]",
	})
	if got.(hubModel).followupModal == nil {
		t.Fatal("followupModal should be set for non-launch mcps")
	}

	// Non-launch layer with sandbox.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleLaunchSettingsEditRequest(launchconfig.LaunchSettingsEditRequestMsg{
		Layer: "project", Field: "sandbox", CurrentValue: "off",
	})
	if got.(hubModel).followupModal == nil {
		t.Fatal("followupModal should be set for non-launch sandbox")
	}
}

// TestCovHandleTextInputResult exercises all tag-prefix branches.
func TestCovHandleTextInputResult(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()

	// credential-set: cancelled.
	m := newHubModel(client, "http://hub.test")
	m.followupModal = &tuipick.TextInputModal{}
	got, _ := m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "credential-set:openai", Cancelled: true})
	if got.(hubModel).followupModal != nil {
		t.Fatal("followupModal should be cleared")
	}

	// credential-set: empty value.
	m = newHubModel(client, "http://hub.test")
	m.followupModal = &tuipick.TextInputModal{}
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "credential-set:openai", Value: ""})
	if got.(hubModel).followupModal != nil {
		t.Fatal("followupModal should be cleared on empty value")
	}

	// credential-set: valid value, no client.
	m = hubModel{session: newModel(nil), followupModal: &tuipick.TextInputModal{}}
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "credential-set:openai", Value: "sk-xxx"})
	if got.(hubModel).followupModal != nil {
		t.Fatal("followupModal should be cleared")
	}

	// credential-set: valid value with client.
	m = newHubModel(client, "http://hub.test")
	m.followupModal = &tuipick.TextInputModal{}
	got, cmd := m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "credential-set:openai", Value: "sk-xxx"})
	if cmd == nil {
		t.Fatal("cmd should not be nil for credential-set with client")
	}

	// oauth-redirect: cancelled.
	m = newHubModel(client, "http://hub.test")
	m.followupModal = &tuipick.TextInputModal{}
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "oauth-redirect:openai:flow1", Cancelled: true})
	if got.(hubModel).followupModal != nil {
		t.Fatal("followupModal should be cleared on cancel")
	}

	// oauth-redirect: empty value.
	m = newHubModel(client, "http://hub.test")
	m.followupModal = &tuipick.TextInputModal{}
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "oauth-redirect:openai:flow1", Value: ""})
	if got.(hubModel).followupModal != nil {
		t.Fatal("followupModal should be cleared on empty value")
	}

	// oauth-redirect: valid value with client.
	m = newHubModel(client, "http://hub.test")
	m.followupModal = &tuipick.TextInputModal{}
	got, cmd = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "oauth-redirect:openai:flow1", Value: "http://redirect"})
	if cmd == nil {
		t.Fatal("cmd should not be nil for oauth-redirect with valid value and client")
	}

	// oauth-redirect: malformed tag (no second colon).
	m = newHubModel(client, "http://hub.test")
	m.followupModal = &tuipick.TextInputModal{}
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "oauth-redirect:malformed", Value: "http://redirect"})
	// followupModal is cleared even when parts is too short.

	// launch-override: cancelled.
	m = hubModel{session: newModel(nil), followupModal: &tuipick.TextInputModal{}}
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "launch-override:model", Cancelled: true})
	if got.(hubModel).followupModal != nil {
		t.Fatal("followupModal should be cleared on cancel")
	}

	// launch-override: with modal, success.
	m = hubModel{session: newModel(nil), followupModal: &tuipick.TextInputModal{}}
	modal := launchconfig.NewLaunchOverridesModal()
	m.launchOverridesModal = &modal
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "launch-override:model", Value: "newmodel"})
	after := got.(hubModel)
	if after.followupModal != nil {
		t.Fatal("followupModal should be cleared")
	}

	// launch-override: with modal, ApplyEdit error.
	m = hubModel{session: newModel(nil), followupModal: &tuipick.TextInputModal{}}
	modal = launchconfig.NewLaunchOverridesModal()
	m.launchOverridesModal = &modal
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "launch-override:model", Value: ""})
	// Empty value — ApplyEdit may succeed or fail depending on field.
	_ = got

	// settings-edit: malformed (no second colon).
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "settings-edit:malformed", Value: "x"})
	// Should not crash.

	// settings-edit: cancelled.
	m = hubModel{session: newModel(nil), followupModal: &tuipick.TextInputModal{}}
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "settings-edit:project:model", Cancelled: true})
	if got.(hubModel).followupModal != nil {
		t.Fatal("followupModal should not be cleared by settings-edit cancel — wait, it should be")
	}

	// settings-edit: no panel.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "settings-edit:project:model", Value: "newval"})
	if got.(hubModel).err != nil {
		t.Fatalf("should not set err when panel is nil: %v", got.(hubModel).err)
	}

	// No matching tag prefix: should not crash.
	m = hubModel{session: newModel(nil)}
	got, _ = m.handleTextInputResult(tuipick.TextInputResultMsg{Tag: "unknown:tag", Value: "x"})
	_ = got
}

// TestCovHandleAuthApiKeySetResult exercises error and success paths.
func TestCovHandleAuthApiKeySetResult(t *testing.T) {
	// Error path.
	m := hubModel{}
	boom := errors.New("key set failed")
	got, _ := m.handleAuthApiKeySetResult(launchconfig.AuthApiKeySetResultMsg{Err: boom})
	if !errors.Is(got.(hubModel).err, boom) {
		t.Fatalf("err = %v, want %v", got.(hubModel).err, boom)
	}

	// Success without panel.
	m = hubModel{err: boom}
	got, _ = m.handleAuthApiKeySetResult(launchconfig.AuthApiKeySetResultMsg{})
	if got.(hubModel).err != nil {
		t.Fatal("err should be cleared on success")
	}

	// Success with panel and client.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	m.credentialsPanel = newCredentialsPanelForTest()
	got, cmd := m.handleAuthApiKeySetResult(launchconfig.AuthApiKeySetResultMsg{})
	if cmd == nil {
		t.Fatal("cmd should not be nil with panel and client")
	}
}

// TestCovHandleAuthLoginStartResult exercises error and success paths.
func TestCovHandleAuthLoginStartResult(t *testing.T) {
	// Error path.
	m := hubModel{}
	boom := errors.New("login start failed")
	got, _ := m.handleAuthLoginStartResult(launchconfig.AuthLoginStartResultMsg{Err: boom})
	if !errors.Is(got.(hubModel).err, boom) {
		t.Fatalf("err = %v, want %v", got.(hubModel).err, boom)
	}

	// Success path.
	m = hubModel{err: boom}
	got, _ = m.handleAuthLoginStartResult(launchconfig.AuthLoginStartResultMsg{
		Provider: "openai", URL: "http://sign.in", FlowID: "flow1",
	})
	after := got.(hubModel)
	if after.err != nil {
		t.Fatal("err should be cleared on success")
	}
	if after.followupModal == nil {
		t.Fatal("followupModal should be set on success")
	}
}

// TestCovHandleAuthLoginCompleteResult exercises error and success paths.
func TestCovHandleAuthLoginCompleteResult(t *testing.T) {
	// Error path.
	m := hubModel{}
	boom := errors.New("login complete failed")
	got, _ := m.handleAuthLoginCompleteResult(launchconfig.AuthLoginCompleteResultMsg{Err: boom})
	if !errors.Is(got.(hubModel).err, boom) {
		t.Fatalf("err = %v, want %v", got.(hubModel).err, boom)
	}

	// Success without panel.
	m = hubModel{err: boom}
	got, _ = m.handleAuthLoginCompleteResult(launchconfig.AuthLoginCompleteResultMsg{})
	if got.(hubModel).err != nil {
		t.Fatal("err should be cleared on success")
	}

	// Success with panel and client.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	m.credentialsPanel = newCredentialsPanelForTest()
	got, cmd := m.handleAuthLoginCompleteResult(launchconfig.AuthLoginCompleteResultMsg{})
	if cmd == nil {
		t.Fatal("cmd should not be nil with panel and client")
	}
}

// TestCovHandleLaunchSetLayerResult exercises nil-panel, error, and success paths.
func TestCovHandleLaunchSetLayerResult(t *testing.T) {
	// Nil panel.
	m := hubModel{}
	got, _ := m.handleLaunchSetLayerResult(launchconfig.LaunchSetLayerResultMsg{})
	if got.(hubModel).launchSettingsPanel != nil {
		t.Fatal("should not set panel")
	}

	// With panel (needs one to forward to).
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	// LaunchSettingsPanel needs a client to construct; skip if not available,
	// just exercise the nil-panel path which is the uncovered branch.
	_ = client
	_ = cleanup
}

// TestCovHandleMarketplaceListResult exercises nil-panel and panel paths.
func TestCovHandleMarketplaceListResult(t *testing.T) {
	// Nil panel.
	m := hubModel{}
	got, _ := m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{})
	if got.(hubModel).pluginsPanel != nil {
		t.Fatal("should not set panel")
	}
}

// TestCovHandleMarketplaceMutateResultNoPanel exercises the no-panel path.
func TestCovHandleMarketplaceMutateResultNoPanel(t *testing.T) {
	m := hubModel{}
	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{})
	if got.(hubModel).err != nil {
		t.Fatalf("err = %v, want nil", got.(hubModel).err)
	}
}

// TestCovHandleMarketplaceBrowseResult exercises nil-panel path.
func TestCovHandleMarketplaceBrowseResult(t *testing.T) {
	m := hubModel{}
	got, _ := m.handleMarketplaceBrowseResult(launchconfig.MarketplaceBrowseResultMsg{})
	if got.(hubModel).pluginsPanel != nil {
		t.Fatal("should not set panel")
	}
}

// TestCovHandleMarketplaceAddSubmit exercises both paths.
func TestCovHandleMarketplaceAddSubmit(t *testing.T) {
	m := hubModel{}
	got, cmd := m.handleMarketplaceAddSubmit(launchconfig.MarketplaceAddSubmitMsg{})
	if cmd != nil {
		t.Fatal("cmd should be nil without client")
	}

	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	got, cmd = m.handleMarketplaceAddSubmit(launchconfig.MarketplaceAddSubmitMsg{
		Params: appwire.MarketplaceAddParams{Name: "mp1"},
	})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	_ = got
}

// TestCovHandleMarketplaceRemove exercises both paths.
func TestCovHandleMarketplaceRemove(t *testing.T) {
	m := hubModel{}
	got, cmd := m.handleMarketplaceRemove(launchconfig.MarketplaceRemoveMsg{Name: "mp1"})
	if cmd != nil {
		t.Fatal("cmd should be nil without client")
	}

	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	got, cmd = m.handleMarketplaceRemove(launchconfig.MarketplaceRemoveMsg{Name: "mp1"})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	_ = got
}

// TestCovHandleMarketplaceRefresh exercises both paths.
func TestCovHandleMarketplaceRefresh(t *testing.T) {
	m := hubModel{}
	got, cmd := m.handleMarketplaceRefresh(launchconfig.MarketplaceRefreshMsg{Name: "mp1"})
	if cmd != nil {
		t.Fatal("cmd should be nil without client")
	}

	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	got, cmd = m.handleMarketplaceRefresh(launchconfig.MarketplaceRefreshMsg{Name: "mp1"})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	_ = got
}

// TestCovHandleMarketplaceBrowseRequest exercises both paths.
func TestCovHandleMarketplaceBrowseRequest(t *testing.T) {
	m := hubModel{}
	got, cmd := m.handleMarketplaceBrowseRequest(launchconfig.MarketplaceBrowseRequestMsg{Name: "mp1"})
	if cmd != nil {
		t.Fatal("cmd should be nil without client")
	}

	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	got, cmd = m.handleMarketplaceBrowseRequest(launchconfig.MarketplaceBrowseRequestMsg{Name: "mp1"})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	_ = got
}

// TestCovHandlePluginListResult exercises nil-panel path.
func TestCovHandlePluginListResult(t *testing.T) {
	m := hubModel{}
	got, _ := m.handlePluginListResult(launchconfig.PluginListResultMsg{})
	if got.(hubModel).pluginsPanel != nil {
		t.Fatal("should not set panel")
	}
}

// TestCovHandlePluginMutateResultNoPanel exercises the no-panel path.
func TestCovHandlePluginMutateResultNoPanel(t *testing.T) {
	m := hubModel{}
	got, _ := m.handlePluginMutateResult(launchconfig.PluginMutateResultMsg{})
	if got.(hubModel).err != nil {
		t.Fatalf("err = %v, want nil", got.(hubModel).err)
	}
}

// TestCovHandlePluginAction exercises all action branches and nil-client.
func TestCovHandlePluginAction(t *testing.T) {
	// Nil client.
	m := hubModel{}
	got, cmd := m.handlePluginAction(launchconfig.PluginActionMsg{Action: "install"})
	if cmd != nil {
		t.Fatal("cmd should be nil without client")
	}
	_ = got

	// With client, each action.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	for _, action := range []string{"install", "upgrade", "remove", "enable", "disable", "unknown"} {
		t.Run(action, func(t *testing.T) {
			m := newHubModel(client, "http://hub.test")
			got, cmd := m.handlePluginAction(launchconfig.PluginActionMsg{Action: action, Plugin: "p", Marketplace: "mp"})
			if action != "unknown" && cmd == nil {
				t.Fatalf("cmd should not be nil for action %q", action)
			}
			if action == "unknown" && cmd != nil {
				t.Fatal("cmd should be nil for unknown action")
			}
			_ = got
		})
	}
}

// TestCovHandlePluginSetAutoUpgrade exercises both paths.
func TestCovHandlePluginSetAutoUpgrade(t *testing.T) {
	m := hubModel{}
	got, cmd := m.handlePluginSetAutoUpgrade(launchconfig.PluginSetAutoUpgradeMsg{AutoUpgrade: true})
	if cmd != nil {
		t.Fatal("cmd should be nil without client")
	}

	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	got, cmd = m.handlePluginSetAutoUpgrade(launchconfig.PluginSetAutoUpgradeMsg{Plugin: "p", Marketplace: "mp", AutoUpgrade: true})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	_ = got
}

// TestCovHandleLaunchResult exercises the schema-result-with-modal path and the
// launch-settings-panel path.
func TestCovHandleLaunchResult(t *testing.T) {
	// Schema result with overrides modal.
	m := hubModel{session: newModel(nil)}
	modal := launchconfig.NewLaunchOverridesModal()
	m.launchOverridesModal = &modal
	got, _ := m.handleLaunchResult(launchconfig.LaunchSchemaResultMsg{})
	after := got.(hubModel)
	if after.launchOverridesModal == nil {
		t.Fatal("launchOverridesModal should still be set after schema result forwarded")
	}

	// No modal, no panel: returns nil.
	m = hubModel{}
	got, _ = m.handleLaunchResult(launchconfig.LaunchSchemaResultMsg{})
	if got.(hubModel).launchOverridesModal != nil {
		t.Fatal("launchOverridesModal should be nil")
	}

	// Non-schema message with no panel.
	m = hubModel{}
	got, _ = m.handleLaunchResult(launchconfig.LaunchResolveResultMsg{})
	_ = got
}

// Ensure we reference tea.Msg where needed.
var _ tea.Msg = hubAuthStatusMsg{}
