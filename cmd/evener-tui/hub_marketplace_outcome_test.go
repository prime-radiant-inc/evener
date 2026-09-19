package tui

import (
	"context"
	"errors"
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

func TestClassifyMarketplaceCloneRemainsJSONData(t *testing.T) {
	valid := map[string]any{
		"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains),
		"applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": "kept"}},
		},
	}
	state, applied := classifyMarketplaceCloneRemains(marketplaceCloneRemainsError(valid))
	if state != marketplaceCloneRemainsApplied || len(applied.Marketplaces) != 1 || applied.Marketplaces[0].Name != "kept" {
		t.Fatalf("JSON applied outcome = %v/%+v, want applied kept snapshot", state, applied)
	}

	for _, data := range []map[string]any{
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "applied": nil},
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "appliedUnavailable": true},
	} {
		state, applied = classifyMarketplaceCloneRemains(marketplaceCloneRemainsError(data))
		if state != marketplaceCloneRemainsUnavailable || applied.Marketplaces != nil {
			t.Fatalf("JSON uncertain outcome = %v/%+v, want unavailable zero snapshot", state, applied)
		}
	}
}

func TestClassifyMarketplaceCloneRemainsDiscardsPartialJSONSnapshot(t *testing.T) {
	malformed := map[string]any{
		"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains),
		"applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": 42}},
		},
	}
	state, applied := classifyMarketplaceCloneRemains(marketplaceCloneRemainsError(malformed))
	if state != marketplaceCloneRemainsUnavailable || applied.Marketplaces != nil {
		t.Fatalf("malformed partial snapshot = %v/%+v, want unavailable zero snapshot", state, applied)
	}
}

func TestMarketplaceMutateResultAppliesTypedSnapshotAndKeepsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed"}
	kept := appwire.MarketplaceEntry{Name: "kept"}
	m := hubModel{
		pluginsPanel:              marketplacePanelWithEntries(t, removed, kept),
		marketplaceRemovePending:  removed.Name,
		marketplaceListGeneration: 1,
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
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:           appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed}},
		ListGeneration: 1,
	})
	afterStale := got.(hubModel)
	if afterStale.marketplaceRemovePending != "" || afterStale.marketplaceReconcilePending {
		t.Fatalf("stale list after applied snapshot changed pending state = %q/%v", afterStale.marketplaceRemovePending, afterStale.marketplaceReconcilePending)
	}
	updated, cmd = afterStale.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale list after applied snapshot should preserve the surviving marketplace")
	}
	remove = cmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != kept.Name {
		t.Fatalf("stale list after applied snapshot changed selected marketplace to %q, want %q", remove.Name, kept.Name)
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
	if list.Err != nil || list.ListGeneration != after.marketplaceListGeneration || len(list.List.Marketplaces) != 1 || list.List.Marketplaces[0].Name != confirmed.Name {
		t.Fatalf("reconcile result = %+v, want confirmed list", list)
	}
	got, _ = after.handleMarketplaceListResult(list)
	reconciled := got.(hubModel)
	if reconciled.marketplaceRemovePending != "" || reconciled.marketplaceReconcilePending {
		t.Fatalf("after reconciliation pending state = %q/%v, want cleared", reconciled.marketplaceRemovePending, reconciled.marketplaceReconcilePending)
	}
	got, _ = reconciled.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:           appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{stale}},
		ListGeneration: list.ListGeneration,
	})
	afterStale := got.(hubModel)
	updated, cmd = afterStale.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("old reconciliation reply should preserve the confirmed marketplace")
	}
	remove = cmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != confirmed.Name {
		t.Fatalf("old reconciliation reply changed selected marketplace to %q, want %q", remove.Name, confirmed.Name)
	}
}

func TestMarketplaceMutationSnapshotSettlesPendingReconciliation(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed"}
	kept := appwire.MarketplaceEntry{Name: "kept"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {})
	defer cleanup()

	m := hubModel{
		client:                      client,
		pluginsPanel:                marketplacePanelWithEntries(t, removed),
		marketplaceRemovePending:    removed.Name,
		marketplaceReconcilePending: true,
		marketplaceListGeneration:   1,
	}
	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Action: "refresh",
		Name:   kept.Name,
		List:   appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
	})
	if cmd != nil {
		t.Fatal("authoritative successful mutation should not need a replacement read")
	}
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("successful mutation left pending state = %q/%v, want cleared", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	if after.marketplaceListGeneration != 2 {
		t.Fatalf("successful mutation generation = %d, want 2", after.marketplaceListGeneration)
	}

	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:           appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed}},
		ListGeneration: 1,
	})
	after = got.(hubModel)
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("old reconciliation reply should not discard the mutation snapshot")
	}
	remove := panelCmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != kept.Name {
		t.Fatalf("old reconciliation reply changed selected marketplace to %q, want %q", remove.Name, kept.Name)
	}

	got, cmd = after.handleMarketplaceRemove(launchconfig.MarketplaceRemoveMsg{Name: kept.Name})
	if cmd == nil || got.(hubModel).marketplaceRemovePending != kept.Name {
		t.Fatal("successful mutation should release the fence for a subsequent remove")
	}
}

func TestMarketplaceListResultDoesNotSettleUnrelatedReconciliation(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept"}
	removed := appwire.MarketplaceEntry{Name: "removed"}
	m := hubModel{
		pluginsPanel:                marketplacePanelWithEntries(t, kept),
		marketplaceRemovePending:    removed.Name,
		marketplaceReconcilePending: true,
		marketplaceListGeneration:   1,
	}

	got, _ := m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed}},
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != removed.Name || !after.marketplaceReconcilePending {
		t.Fatalf("stale list settled pending state = %q/%v, want fence preserved", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	updated, cmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale list should not discard the existing panel state")
	}
	remove := cmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != kept.Name {
		t.Fatalf("stale list changed selected marketplace to %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceReconciliationFailureRecoversOnLaterPanelRefresh(t *testing.T) {
	stale := appwire.MarketplaceEntry{Name: "removed"}
	confirmed := appwire.MarketplaceEntry{Name: "kept"}
	listCalls := 0
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			listCalls++
			if listCalls == 1 {
				return appwire.MarketplaceListResponse{}, errors.New("temporary list failure")
			}
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{confirmed}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginList, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{}, nil
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
	first := cmd().(launchconfig.MarketplaceListResultMsg)
	if first.Err == nil || first.ListGeneration != after.marketplaceListGeneration {
		t.Fatalf("failed reconciliation result = %+v, model generation = %d", first, after.marketplaceListGeneration)
	}
	got, _ = after.handleMarketplaceListResult(first)
	after = got.(hubModel)
	if after.marketplaceRemovePending != stale.Name || !after.marketplaceReconcilePending {
		t.Fatalf("after failed reconciliation pending state = %q/%v, want fenced", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}

	batch := after.refreshPluginsPanel()().(tea.BatchMsg)
	var recovery launchconfig.MarketplaceListResultMsg
	for _, command := range batch {
		if result, ok := command().(launchconfig.MarketplaceListResultMsg); ok {
			recovery = result
		}
	}
	if recovery.Err != nil || recovery.ListGeneration != after.marketplaceListGeneration {
		t.Fatalf("recovery result = %+v, model generation = %d", recovery, after.marketplaceListGeneration)
	}
	got, _ = after.handleMarketplaceListResult(recovery)
	recovered := got.(hubModel)
	if recovered.marketplaceRemovePending != "" || recovered.marketplaceReconcilePending {
		t.Fatalf("after later successful refresh pending state = %q/%v, want cleared", recovered.marketplaceRemovePending, recovered.marketplaceReconcilePending)
	}
	if listCalls != 2 {
		t.Fatalf("marketplace list calls = %d, want failed reconciliation plus one recovery", listCalls)
	}
}

func TestMarketplaceCloneRemainsWrongShapedDataStaysFenced(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{}, nil
		})
	})
	defer cleanup()

	m := hubModel{client: client, marketplaceRemovePending: "removed"}
	err := marketplaceCloneRemainsError(map[string]any{
		"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains),
		"applied":         "not-a-list",
	})
	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err: err, Action: "remove", Name: "removed",
	})
	after := got.(hubModel)
	if cmd == nil || after.marketplaceRemovePending != "removed" || !after.marketplaceReconcilePending {
		t.Fatalf("wrong-shaped marked data = pending %q/%v cmd=%v, want fenced reconciliation", after.marketplaceRemovePending, after.marketplaceReconcilePending, cmd != nil)
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
