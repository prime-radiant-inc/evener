package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/internal/appserver"
)

func marketplacePanelWithEntries(t *testing.T, entries ...appwire.MarketplaceEntry) *launchconfig.PluginsPanel {
	t.Helper()
	panel := launchconfig.NewPluginsPanel()
	updated, _ := panel.Update(launchconfig.MarketplaceListResultMsg{
		List: appwire.MarketplaceListResponse{Marketplaces: entries},
	})
	got := updated.(launchconfig.PluginsPanel)
	return &got
}

func marketplaceCloneRemainsError(data any) error {
	return appwire.WireError{
		Code:    appwire.CodeConflict,
		Message: "marketplace unregistered, but its clone could not be removed",
		Data:    data,
	}
}

func marketplaceRemoveAppliedError(data any) error {
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: `marketplace "removed": removed, but the updated list could not be read`,
		Data:    data,
	}
}

func TestClassifyMarketplaceRemovalOutcomeJSONData(t *testing.T) {
	valid := map[string]any{
		"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains),
		"applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": "kept"}},
		},
	}
	state, applied := classifyMarketplaceRemovalOutcome(marketplaceCloneRemainsError(valid))
	if state != marketplaceRemovalApplied || len(applied.Marketplaces) != 1 || applied.Marketplaces[0].Name != "kept" {
		t.Fatalf("JSON applied outcome = %v/%+v, want applied kept snapshot", state, applied)
	}

	for _, data := range []map[string]any{
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "applied": nil},
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "appliedUnavailable": true},
	} {
		state, applied = classifyMarketplaceRemovalOutcome(marketplaceCloneRemainsError(data))
		if state != marketplaceRemovalUnavailable || applied.Marketplaces != nil {
			t.Fatalf("JSON uncertain outcome = %v/%+v, want unavailable zero snapshot", state, applied)
		}
	}
	state, applied = classifyMarketplaceRemovalOutcome(marketplaceCloneRemainsError(map[string]any{
		"evenerErrorInfo": "other-error",
		"applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": "kept"}},
		},
	}))
	if state != marketplaceRemovalNotMarked || applied.Marketplaces != nil {
		t.Fatalf("unmarked JSON outcome = %v/%+v, want ordinary error", state, applied)
	}
}

func TestClassifyMarketplaceRemoveAppliedMarkerData(t *testing.T) {
	typed := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: `marketplace "removed": removed, but the updated list could not be read`,
		Data: appwire.MarketplaceRemoveAppliedData{
			EvenerErrorInfo:    appwire.ErrorMarketplaceRemoveApplied,
			AppliedUnavailable: true,
		},
	}
	state, applied := classifyMarketplaceRemovalOutcome(typed)
	if state != marketplaceRemovalRemoved || applied.Marketplaces != nil {
		t.Fatalf("typed removed outcome = %v/%+v, want removed with no snapshot", state, applied)
	}

	// The hub emits the marker from one site, always with AppliedUnavailable,
	// so the marker alone is the proof: neither a missing flag nor a stray
	// snapshot the marker never carries may demote a standing removal back to
	// a retryable failure.
	for _, data := range []map[string]any{
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceRemoveApplied)},
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceRemoveApplied), "appliedUnavailable": true},
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceRemoveApplied), "applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": "kept"}},
		}},
	} {
		state, applied = classifyMarketplaceRemovalOutcome(marketplaceRemoveAppliedError(data))
		if state != marketplaceRemovalRemoved || applied.Marketplaces != nil {
			t.Fatalf("JSON removed outcome %v = %v/%+v, want removed with no snapshot", data, state, applied)
		}
	}

	// The removed-marker check must not swallow the clone-remains family: its
	// own marker still classifies, and an unknown discriminator stays
	// ordinary.
	state, _ = classifyMarketplaceRemovalOutcome(marketplaceCloneRemainsError(map[string]any{
		"evenerErrorInfo":    string(appwire.ErrorMarketplaceUnregisteredCloneRemains),
		"appliedUnavailable": true,
	}))
	if state != marketplaceRemovalUnavailable {
		t.Fatalf("clone-remains marker after removed-marker check = %v, want unavailable", state)
	}
	state, _ = classifyMarketplaceRemovalOutcome(marketplaceCloneRemainsError(map[string]any{
		"evenerErrorInfo": "other-error",
	}))
	if state != marketplaceRemovalNotMarked {
		t.Fatalf("unmarked removal outcome = %v, want ordinary", state)
	}
}

func TestClassifyMarketplaceRemovalOutcomeDiscardsPartialJSONSnapshot(t *testing.T) {
	malformed := map[string]any{
		"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains),
		"applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": 42}},
		},
	}
	state, applied := classifyMarketplaceRemovalOutcome(marketplaceCloneRemainsError(malformed))
	if state != marketplaceRemovalUnavailable || applied.Marketplaces != nil {
		t.Fatalf("malformed partial snapshot = %v/%+v, want unavailable zero snapshot", state, applied)
	}
}

func TestMarketplaceMutateResultAppliesTypedSnapshotAndKeepsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed"}
	kept := appwire.MarketplaceEntry{Name: "kept"}
	m := hubModel{
		pluginsPanel:             marketplacePanelWithEntries(t, removed, kept),
		marketplaceRemovePending: removed.Name,
	}
	err := marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
		Applied: appwire.MarketplaceListResponse{
			Marketplaces: []appwire.MarketplaceEntry{kept},
		},
	})

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err, Action: "remove", Name: removed.Name})
	after := got.(hubModel)
	if cmd != nil {
		t.Fatal("applied-with-litter result should not request another list")
	}
	if after.err == nil {
		t.Fatal("applied-with-litter result should leave a visible warning")
	}
	if _, ok := errors.AsType[appwire.WireError](after.err); !ok {
		t.Fatalf("warning = %v, want original WireError in error chain", after.err)
	}
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("pending state = %q/%v, want cleared", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	if after.pluginsPanel == nil {
		t.Fatal("applied snapshot should keep the plugins panel")
	}
	updated, cmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("applied snapshot should leave the surviving marketplace removable")
	}
	remove := cmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != kept.Name {
		t.Fatalf("panel selected marketplace after applied snapshot = %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceMutateResultUnavailableReconcilesBeforeRetry(t *testing.T) {
	stale := appwire.MarketplaceEntry{Name: "removed"}
	confirmed := appwire.MarketplaceEntry{Name: "kept"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{confirmed}}, nil
		})
	})
	defer cleanup()

	m := hubModel{
		client:                   client,
		pluginsPanel:             marketplacePanelWithEntries(t, stale),
		marketplaceRemovePending: stale.Name,
	}
	err := marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo:    appwire.ErrorMarketplaceUnregisteredCloneRemains,
		AppliedUnavailable: true,
	})

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err, Action: "remove", Name: stale.Name})
	after := got.(hubModel)
	if cmd == nil {
		t.Fatal("unavailable applied outcome should request a fresh list")
	}
	if after.marketplaceRemovePending != stale.Name || !after.marketplaceReconcilePending {
		t.Fatalf("pending state = %q/%v, want fenced until list success", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("unavailable outcome should preserve the stale marketplace row")
	}
	remove := panelCmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != stale.Name {
		t.Fatalf("panel selected marketplace after unavailable outcome = %q, want %q", remove.Name, stale.Name)
	}

	list := cmd().(launchconfig.MarketplaceListResultMsg)
	if list.Err != nil || list.ReconcileGeneration != after.marketplaceReconcileGeneration || len(list.List.Marketplaces) != 1 || list.List.Marketplaces[0].Name != confirmed.Name {
		t.Fatalf("reconcile result = %+v, want confirmed list", list)
	}
	got, _ = after.handleMarketplaceListResult(list)
	reconciled := got.(hubModel)
	if reconciled.marketplaceRemovePending != "" || reconciled.marketplaceReconcilePending {
		t.Fatalf("after reconciliation pending state = %q/%v, want cleared", reconciled.marketplaceRemovePending, reconciled.marketplaceReconcilePending)
	}
}

func TestMarketplaceListResultDoesNotSettleReconciliationWithoutItsGeneration(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept"}
	removed := appwire.MarketplaceEntry{Name: "removed"}
	m := hubModel{
		pluginsPanel:                   marketplacePanelWithEntries(t, kept),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcilePending:    true,
		marketplaceReconcileGeneration: 1,
	}

	got, _ := m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed}},
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != removed.Name || !after.marketplaceReconcilePending {
		t.Fatalf("untagged list settled pending state = %q/%v, want fence preserved", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	updated, cmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("untagged list should not discard the existing panel state")
	}
	remove := cmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != kept.Name {
		t.Fatalf("untagged list changed selected marketplace to %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceMutateResultMalformedAppliedPayloadStaysFenced(t *testing.T) {
	m := hubModel{marketplaceRemovePending: "removed"}
	err := marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
	})

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err, Action: "remove", Name: "removed"})
	after := got.(hubModel)
	if cmd != nil || after.marketplaceRemovePending != "removed" || !after.marketplaceReconcilePending {
		t.Fatalf("malformed applied state = pending %q/%v cmd=%v, want fenced reconciliation", after.marketplaceRemovePending, after.marketplaceReconcilePending, cmd != nil)
	}
}

func TestMarketplaceRemoveBlocksDuplicateWhileOutcomeUnconfirmed(t *testing.T) {
	m := hubModel{marketplaceRemovePending: "removed"}
	got, cmd := m.handleMarketplaceRemove(launchconfig.MarketplaceRemoveMsg{Name: "removed"})
	if cmd != nil {
		t.Fatal("duplicate remove should not issue a command")
	}
	if got.(hubModel).marketplaceRemovePending != "removed" {
		t.Fatal("duplicate remove changed the pending identity")
	}
}

func TestMarketplaceMutateResultRemovedOutcomeReconcilesWithoutLitterWarning(t *testing.T) {
	stale := appwire.MarketplaceEntry{Name: "removed"}
	confirmed := appwire.MarketplaceEntry{Name: "kept"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{confirmed}}, nil
		})
	})
	defer cleanup()

	m := hubModel{
		client:                   client,
		pluginsPanel:             marketplacePanelWithEntries(t, stale),
		marketplaceRemovePending: stale.Name,
	}
	err := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: `marketplace "removed": removed, but the updated list could not be read`,
		Data: appwire.MarketplaceRemoveAppliedData{
			EvenerErrorInfo:    appwire.ErrorMarketplaceRemoveApplied,
			AppliedUnavailable: true,
		},
	}

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err, Action: "remove", Name: stale.Name})
	after := got.(hubModel)
	if cmd == nil {
		t.Fatal("removed outcome should request a fresh list")
	}
	if after.marketplaceRemovePending != stale.Name || !after.marketplaceReconcilePending {
		t.Fatalf("removed outcome pending state = %q/%v, want fenced until list success", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	if after.err == nil {
		t.Fatal("removed outcome should leave a visible account")
	}
	if strings.Contains(after.err.Error(), "clone") {
		t.Fatalf("removed outcome warning = %q, want no clone-litter claim", after.err.Error())
	}
	if _, ok := errors.AsType[appwire.WireError](after.err); !ok {
		t.Fatalf("removed outcome warning = %v, want original WireError in error chain", after.err)
	}
	if _, dup := after.handleMarketplaceRemove(launchconfig.MarketplaceRemoveMsg{Name: stale.Name}); dup != nil {
		t.Fatal("duplicate remove should stay blocked while the removed outcome reconciles")
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("removed outcome should preserve the stale marketplace row")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != stale.Name {
		t.Fatalf("panel selected marketplace after removed outcome = %q, want %q", remove.Name, stale.Name)
	}

	list := cmd().(launchconfig.MarketplaceListResultMsg)
	if list.Err != nil || list.ReconcileGeneration != after.marketplaceReconcileGeneration || len(list.List.Marketplaces) != 1 || list.List.Marketplaces[0].Name != confirmed.Name {
		t.Fatalf("reconcile result = %+v, want confirmed list", list)
	}
	got, _ = after.handleMarketplaceListResult(list)
	reconciled := got.(hubModel)
	if reconciled.marketplaceRemovePending != "" || reconciled.marketplaceReconcilePending {
		t.Fatalf("after reconciliation pending state = %q/%v, want cleared", reconciled.marketplaceRemovePending, reconciled.marketplaceReconcilePending)
	}
}

func TestMarketplaceReconciliationRetriesThroughPanelReopen(t *testing.T) {
	confirmed := appwire.MarketplaceEntry{Name: "kept"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{confirmed}}, nil
		})
	})
	defer cleanup()

	// The state a marked-but-unconfirmed removal leaves behind once its first
	// reconciliation read failed: the fence stands and no read is in flight.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, appwire.MarketplaceEntry{Name: "removed"}),
		marketplaceRemovePending:       "removed",
		marketplaceReconcilePending:    true,
		marketplaceReconcileGeneration: 1,
	}
	got, _ := m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		Err:                 errors.New("list read failed"),
		ReconcileGeneration: 1,
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "removed" || !after.marketplaceReconcilePending {
		t.Fatalf("after failed reconcile pending = %q/%v, want fence standing", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}

	// Reopening /plugins must re-issue the marketplace read through the
	// tagged reconciliation path, so its result can settle the fence and
	// populate the fresh panel; the ordinary untagged read the guard
	// discards would leave both stuck forever.
	plugin, ok := hubCommandByName("plugins")
	if !ok {
		t.Fatal("registry missing /plugins")
	}
	run := plugin.Run(&after, "")
	if run == nil {
		t.Fatal("/plugins reopen should issue its initial reads")
	}
	var list launchconfig.MarketplaceListResultMsg
	seen := false
	for _, c := range run().(tea.BatchMsg) {
		if msg, ok := c().(launchconfig.MarketplaceListResultMsg); ok {
			list, seen = msg, true
		}
	}
	if !seen {
		t.Fatal("/plugins reopen did not issue a marketplace list read")
	}
	if list.Err != nil || list.ReconcileGeneration != after.marketplaceReconcileGeneration {
		t.Fatalf("/plugins reopen read = %+v, want tagged generation %d", list, after.marketplaceReconcileGeneration)
	}

	got, _ = after.handleMarketplaceListResult(list)
	recovered := got.(hubModel)
	if recovered.marketplaceRemovePending != "" || recovered.marketplaceReconcilePending {
		t.Fatalf("after recovered reconcile pending = %q/%v, want cleared", recovered.marketplaceRemovePending, recovered.marketplaceReconcilePending)
	}
	if recovered.pluginsPanel == nil {
		t.Fatal("recovered read should keep the plugins panel")
	}
	updated, panelCmd := recovered.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("recovered read should leave the surviving marketplace removable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != confirmed.Name {
		t.Fatalf("panel selected marketplace after recovery = %q, want %q", remove.Name, confirmed.Name)
	}
}

func TestMarketplaceReconciliationRetriesThroughNotificationRefresh(t *testing.T) {
	confirmed := appwire.MarketplaceEntry{Name: "kept"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{confirmed}}, nil
		})
	})
	defer cleanup()

	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, appwire.MarketplaceEntry{Name: "removed"}),
		marketplaceRemovePending:       "removed",
		marketplaceReconcilePending:    true,
		marketplaceReconcileGeneration: 4,
	}
	var list launchconfig.MarketplaceListResultMsg
	seen := false
	for _, c := range m.refreshPluginsPanel()().(tea.BatchMsg) {
		if msg, ok := c().(launchconfig.MarketplaceListResultMsg); ok {
			list, seen = msg, true
		}
	}
	if !seen {
		t.Fatal("notification refresh did not issue a marketplace list read")
	}
	if list.Err != nil || list.ReconcileGeneration != m.marketplaceReconcileGeneration {
		t.Fatalf("notification refresh read = %+v, want tagged generation %d", list, m.marketplaceReconcileGeneration)
	}

	got, _ := m.handleMarketplaceListResult(list)
	recovered := got.(hubModel)
	if recovered.marketplaceRemovePending != "" || recovered.marketplaceReconcilePending {
		t.Fatalf("after recovered reconcile pending = %q/%v, want cleared", recovered.marketplaceRemovePending, recovered.marketplaceReconcilePending)
	}
}
