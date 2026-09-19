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
	state, applied = classifyMarketplaceCloneRemains(marketplaceCloneRemainsError(map[string]any{
		"evenerErrorInfo": "other-error",
		"applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": "kept"}},
		},
	}))
	if state != marketplaceCloneRemainsNotTyped || applied.Marketplaces != nil {
		t.Fatalf("unmarked JSON outcome = %v/%+v, want ordinary error", state, applied)
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
	if list.Err != nil || list.ListGeneration != after.marketplaceListGeneration || len(list.List.Marketplaces) != 1 || list.List.Marketplaces[0].Name != confirmed.Name {
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

func marketplaceListResultFromBatch(t *testing.T, cmd tea.Cmd) launchconfig.MarketplaceListResultMsg {
	t.Helper()
	message := cmd()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		t.Fatalf("refresh command returned %T, want tea.BatchMsg", message)
	}
	for _, command := range batch {
		if result, ok := command().(launchconfig.MarketplaceListResultMsg); ok {
			return result
		}
	}
	t.Fatal("refresh batch did not contain a marketplace list result")
	return launchconfig.MarketplaceListResultMsg{}
}

func TestMarketplacePriorRefreshIsDroppedAfterUnavailableRemoval(t *testing.T) {
	stale := appwire.MarketplaceEntry{Name: "removed"}
	confirmed := appwire.MarketplaceEntry{Name: "kept"}
	listCalls := 0
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			listCalls++
			if listCalls == 1 {
				return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{stale}}, nil
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
	priorRefresh := m.refreshPluginsPanel()
	got, reconcileCmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err:            marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains, AppliedUnavailable: true}),
		ListGeneration: m.marketplaceListGeneration,
		Action:         "remove",
		Name:           stale.Name,
	})
	after := got.(hubModel)
	if reconcileCmd == nil || after.marketplaceListGeneration != 2 {
		t.Fatalf("unavailable removal state = generation %d, cmd=%v; want tagged reconciliation generation 2", after.marketplaceListGeneration, reconcileCmd != nil)
	}

	oldList := marketplaceListResultFromBatch(t, priorRefresh)
	if oldList.ListGeneration != 1 {
		t.Fatalf("prior refresh generation = %d, want 1", oldList.ListGeneration)
	}
	got, _ = after.handleMarketplaceListResult(oldList)
	after = got.(hubModel)
	if after.marketplaceRemovePending != stale.Name || !after.marketplaceReconcilePending {
		t.Fatalf("old refresh settled pending state = %q/%v, want fence preserved", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}

	fresh := reconcileCmd().(launchconfig.MarketplaceListResultMsg)
	if fresh.Err != nil || fresh.ListGeneration != 2 || len(fresh.List.Marketplaces) != 1 || fresh.List.Marketplaces[0].Name != confirmed.Name {
		t.Fatalf("fresh reconciliation result = %+v, want confirmed generation 2 list", fresh)
	}
	got, _ = after.handleMarketplaceListResult(fresh)
	after = got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("fresh reconciliation left pending state = %q/%v, want cleared", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	if listCalls != 2 {
		t.Fatalf("marketplace list calls = %d, want delayed refresh plus fresh reconciliation", listCalls)
	}
	got, nextRemove := after.handleMarketplaceRemove(launchconfig.MarketplaceRemoveMsg{Name: confirmed.Name})
	if nextRemove == nil || got.(hubModel).marketplaceRemovePending != confirmed.Name {
		t.Fatal("confirmed reconciliation did not release the next removal")
	}
}

func TestMarketplaceCommandTagsListGeneration(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginList, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{}, nil
		})
	})
	defer cleanup()

	m := hubModel{client: client}
	definition, ok := hubCommandByName("plugins")
	if !ok {
		t.Fatal("plugins command is not registered")
	}
	batch, ok := definition.Run(&m, "")().(tea.BatchMsg)
	if !ok {
		t.Fatal("plugins command did not return a batch")
	}
	for _, command := range batch {
		if result, ok := command().(launchconfig.MarketplaceListResultMsg); ok {
			if result.ListGeneration == 0 || result.ListGeneration != m.marketplaceListGeneration {
				t.Fatalf("plugins command list generation = %d, model generation = %d", result.ListGeneration, m.marketplaceListGeneration)
			}
			return
		}
	}
	t.Fatal("plugins command batch did not contain a marketplace list result")
}

func TestMarketplaceMutationDuringReconciliationRequestsFreshList(t *testing.T) {
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
	got, initialReconcile := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err:    marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains, AppliedUnavailable: true}),
		Action: "remove",
		Name:   stale.Name,
	})
	after := got.(hubModel)
	if initialReconcile == nil || !after.marketplaceReconcilePending {
		t.Fatal("unavailable removal did not start reconciliation")
	}
	got, replacement := after.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:           appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{stale}},
		ListGeneration: after.marketplaceListGeneration,
		Action:         "refresh",
		Name:           confirmed.Name,
	})
	after = got.(hubModel)
	if replacement == nil || after.marketplaceRemovePending != stale.Name || !after.marketplaceReconcilePending || after.err == nil {
		t.Fatalf("successful stale mutation changed pending state = %q/%v warning=%v, want fence and warning preserved", after.marketplaceRemovePending, after.marketplaceReconcilePending, after.err)
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale mutation snapshot discarded the pending marketplace row")
	}
	remove := panelCmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != stale.Name {
		t.Fatalf("stale mutation snapshot selected %q, want %q", remove.Name, stale.Name)
	}

	fresh := replacement().(launchconfig.MarketplaceListResultMsg)
	if fresh.Err != nil || fresh.ListGeneration != after.marketplaceListGeneration || fresh.List.Marketplaces[0].Name != confirmed.Name {
		t.Fatalf("replacement list = %+v, want current confirmed snapshot", fresh)
	}
	got, _ = after.handleMarketplaceListResult(fresh)
	after = got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("replacement list left pending state = %q/%v, want cleared", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
}

func TestMarketplaceMutationSnapshotInvalidatesOlderList(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept"}
	removed := appwire.MarketplaceEntry{Name: "removed"}
	m := hubModel{
		pluginsPanel:              marketplacePanelWithEntries(t, kept),
		marketplaceListGeneration: 1,
	}
	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:           appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
		ListGeneration: m.marketplaceListGeneration,
		Action:         "refresh",
		Name:           kept.Name,
	})
	after := got.(hubModel)
	if after.marketplaceListGeneration != 2 {
		t.Fatalf("successful mutation generation = %d, want 2", after.marketplaceListGeneration)
	}
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:           appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed}},
		ListGeneration: 1,
	})
	after = got.(hubModel)
	updated, cmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("old list discarded the current mutation snapshot")
	}
	remove := cmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != kept.Name {
		t.Fatalf("old list changed selected marketplace to %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceDelayedMutationAfterAppliedSettlementIsDropped(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed"}
	kept := appwire.MarketplaceEntry{Name: "kept"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceRefresh, func(context.Context, appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}}, nil
		})
	})
	defer cleanup()

	m := hubModel{
		client:                    client,
		pluginsPanel:              marketplacePanelWithEntries(t, removed, kept),
		marketplaceRemovePending:  removed.Name,
		marketplaceListGeneration: 1,
	}
	_, delayedRefresh := m.handleMarketplaceRefresh(launchconfig.MarketplaceRefreshMsg{Name: kept.Name})
	if delayedRefresh == nil {
		t.Fatal("refresh did not create a delayed mutation command")
	}

	applied := appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}
	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err:            marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains, Applied: applied}),
		ListGeneration: 1,
		Action:         "remove",
		Name:           removed.Name,
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending || after.marketplaceListGeneration != 2 {
		t.Fatalf("applied settlement state = %q/%v generation=%d, want cleared fence at generation 2", after.marketplaceRemovePending, after.marketplaceReconcilePending, after.marketplaceListGeneration)
	}

	delayed := delayedRefresh().(launchconfig.MarketplaceMutateResultMsg)
	if delayed.ListGeneration != 1 {
		t.Fatalf("delayed refresh generation = %d, want request generation 1", delayed.ListGeneration)
	}
	got, _ = after.handleMarketplaceMutateResult(delayed)
	after = got.(hubModel)
	if after.err == nil {
		t.Fatal("stale mutation result cleared the applied warning")
	}
	updated, cmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale mutation result replaced the applied marketplace panel")
	}
	remove := cmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != kept.Name {
		t.Fatalf("stale mutation result changed selected marketplace to %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceDelayedMutationAfterFreshReconciliationIsDropped(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed"}
	kept := appwire.MarketplaceEntry{Name: "kept"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceRefresh, func(context.Context, appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}}, nil
		})
	})
	defer cleanup()

	m := hubModel{
		client:                    client,
		pluginsPanel:              marketplacePanelWithEntries(t, removed),
		marketplaceRemovePending:  removed.Name,
		marketplaceListGeneration: 1,
	}
	_, delayedRefresh := m.handleMarketplaceRefresh(launchconfig.MarketplaceRefreshMsg{Name: kept.Name})
	if delayedRefresh == nil {
		t.Fatal("refresh did not create a delayed mutation command")
	}

	got, reconcile := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err:            marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains, AppliedUnavailable: true}),
		ListGeneration: 1,
		Action:         "remove",
		Name:           removed.Name,
	})
	after := got.(hubModel)
	if reconcile == nil || !after.marketplaceReconcilePending || after.marketplaceListGeneration != 2 {
		t.Fatalf("unavailable removal state = pending=%v generation=%d, want pending generation 2", after.marketplaceReconcilePending, after.marketplaceListGeneration)
	}
	fresh := reconcile().(launchconfig.MarketplaceListResultMsg)
	got, _ = after.handleMarketplaceListResult(fresh)
	after = got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending || after.marketplaceListGeneration != 3 {
		t.Fatalf("fresh reconciliation state = %q/%v generation=%d, want cleared fence at generation 3", after.marketplaceRemovePending, after.marketplaceReconcilePending, after.marketplaceListGeneration)
	}

	delayed := delayedRefresh().(launchconfig.MarketplaceMutateResultMsg)
	if delayed.ListGeneration != 1 {
		t.Fatalf("delayed refresh generation = %d, want request generation 1", delayed.ListGeneration)
	}
	got, _ = after.handleMarketplaceMutateResult(delayed)
	after = got.(hubModel)
	if after.err == nil {
		t.Fatal("stale mutation result cleared the unavailable warning")
	}
	updated, cmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale mutation result replaced the reconciled marketplace panel")
	}
	remove := cmd().(launchconfig.MarketplaceRemoveMsg)
	if remove.Name != kept.Name {
		t.Fatalf("stale mutation result resurrected %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceNotificationBeforeRemoveSuccessCompletesAndReconciles(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed"}
	kept := appwire.MarketplaceEntry{Name: "kept"}
	listCalls := 0
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceRemove, func(context.Context, appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			listCalls++
			if listCalls == 1 {
				return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}}, nil
			}
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginList, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{}, nil
		})
	})
	defer cleanup()

	m := hubModel{client: client, pluginsPanel: marketplacePanelWithEntries(t, removed)}
	got, removeCmd := m.handleMarketplaceRemove(launchconfig.MarketplaceRemoveMsg{Name: removed.Name})
	m = got.(hubModel)
	notification := marketplaceListResultFromBatch(t, m.refreshPluginsPanel())
	if notification.ListGeneration != 1 {
		t.Fatalf("notification list generation = %d, want 1", notification.ListGeneration)
	}
	got, _ = m.handleMarketplaceListResult(notification)
	m = got.(hubModel)
	if m.marketplaceRemovePending != removed.Name {
		t.Fatalf("notification cleared remove pending = %q, want %q", m.marketplaceRemovePending, removed.Name)
	}

	remove := removeCmd().(launchconfig.MarketplaceMutateResultMsg)
	if remove.ListGeneration != 0 {
		t.Fatalf("remove result generation = %d, want request generation 0", remove.ListGeneration)
	}
	got, reconcile := m.handleMarketplaceMutateResult(remove)
	m = got.(hubModel)
	if reconcile == nil || m.marketplaceRemovePending != removed.Name || !m.marketplaceReconcilePending {
		t.Fatalf("stale remove completion state = %q/%v cmd=%v, want reconciliation fence", m.marketplaceRemovePending, m.marketplaceReconcilePending, reconcile != nil)
	}
	fresh := reconcile().(launchconfig.MarketplaceListResultMsg)
	got, _ = m.handleMarketplaceListResult(fresh)
	m = got.(hubModel)
	if m.marketplaceRemovePending != "" || m.marketplaceReconcilePending || listCalls != 2 {
		t.Fatalf("fresh reconciliation state = %q/%v calls=%d, want completed removal after two reads", m.marketplaceRemovePending, m.marketplaceReconcilePending, listCalls)
	}
	updated, cmd := m.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("fresh reconciliation did not leave the surviving marketplace removable")
	}
	if remove := cmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("fresh reconciliation selected %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceNotificationBeforeTypedCloneFailurePreservesWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed"}
	kept := appwire.MarketplaceEntry{Name: "kept"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceRemove, func(context.Context, appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{}, marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains, AppliedUnavailable: true})
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginList, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{}, nil
		})
	})
	defer cleanup()

	m := hubModel{client: client, pluginsPanel: marketplacePanelWithEntries(t, removed)}
	got, removeCmd := m.handleMarketplaceRemove(launchconfig.MarketplaceRemoveMsg{Name: removed.Name})
	m = got.(hubModel)
	notification := marketplaceListResultFromBatch(t, m.refreshPluginsPanel())
	got, _ = m.handleMarketplaceListResult(notification)
	m = got.(hubModel)

	remove := removeCmd().(launchconfig.MarketplaceMutateResultMsg)
	got, reconcile := m.handleMarketplaceMutateResult(remove)
	m = got.(hubModel)
	if reconcile == nil || m.marketplaceRemovePending != removed.Name || !m.marketplaceReconcilePending || m.err == nil {
		t.Fatalf("stale typed failure state = %q/%v warning=%v cmd=%v, want fenced warning and reconciliation", m.marketplaceRemovePending, m.marketplaceReconcilePending, m.err, reconcile != nil)
	}
	fresh := reconcile().(launchconfig.MarketplaceListResultMsg)
	got, _ = m.handleMarketplaceListResult(fresh)
	m = got.(hubModel)
	if m.marketplaceRemovePending != "" || m.marketplaceReconcilePending || m.err == nil {
		t.Fatalf("typed failure reconciliation state = %q/%v warning=%v, want cleared fence with warning", m.marketplaceRemovePending, m.marketplaceReconcilePending, m.err)
	}
	updated, cmd := m.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("typed failure reconciliation did not preserve the surviving marketplace")
	}
	if remove := cmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("typed failure reconciliation selected %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplacePassiveListBeforeRemoveOutcomeReconcilesStaleSnapshot(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed"}
	kept := appwire.MarketplaceEntry{Name: "kept"}
	listCalls := 0
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceRemove, func(context.Context, appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			listCalls++
			if listCalls == 1 {
				return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}}, nil
			}
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanup()

	m := hubModel{client: client, pluginsPanel: marketplacePanelWithEntries(t, removed)}
	got, removeCmd := m.handleMarketplaceRemove(launchconfig.MarketplaceRemoveMsg{Name: removed.Name})
	m = got.(hubModel)
	listGeneration := m.nextMarketplaceListGeneration()
	passive := launchconfig.CmdMarketplaceListWithGeneration(client, listGeneration)().(launchconfig.MarketplaceListResultMsg)
	got, _ = m.handleMarketplaceListResult(passive)
	m = got.(hubModel)

	remove := removeCmd().(launchconfig.MarketplaceMutateResultMsg)
	got, reconcile := m.handleMarketplaceMutateResult(remove)
	m = got.(hubModel)
	if reconcile == nil || m.marketplaceRemovePending != removed.Name || !m.marketplaceReconcilePending {
		t.Fatalf("passive-list/remove state = %q/%v cmd=%v, want reconciliation fence", m.marketplaceRemovePending, m.marketplaceReconcilePending, reconcile != nil)
	}
	fresh := reconcile().(launchconfig.MarketplaceListResultMsg)
	got, _ = m.handleMarketplaceListResult(fresh)
	m = got.(hubModel)
	if m.marketplaceRemovePending != "" || m.marketplaceReconcilePending || listCalls != 2 {
		t.Fatalf("passive-list reconciliation state = %q/%v calls=%d, want completed removal after two lists", m.marketplaceRemovePending, m.marketplaceReconcilePending, listCalls)
	}
}

func TestMarketplaceFailedReconciliationRecoversOnLaterRefresh(t *testing.T) {
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
	got, reconcileCmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err:    marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains, AppliedUnavailable: true}),
		Action: "remove",
		Name:   stale.Name,
	})
	after := got.(hubModel)
	failed := reconcileCmd().(launchconfig.MarketplaceListResultMsg)
	if failed.Err == nil || failed.ListGeneration != after.marketplaceListGeneration {
		t.Fatalf("failed reconciliation result = %+v, want current generation error", failed)
	}
	got, _ = after.handleMarketplaceListResult(failed)
	after = got.(hubModel)
	if after.marketplaceRemovePending != stale.Name || !after.marketplaceReconcilePending {
		t.Fatalf("failed reconciliation cleared pending state = %q/%v", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}

	recovery := marketplaceListResultFromBatch(t, after.refreshPluginsPanel())
	if recovery.Err != nil || recovery.ListGeneration != after.marketplaceListGeneration || recovery.List.Marketplaces[0].Name != confirmed.Name {
		t.Fatalf("later refresh result = %+v, want confirmed current list", recovery)
	}
	got, _ = after.handleMarketplaceListResult(recovery)
	after = got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("later refresh left pending state = %q/%v, want cleared", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
}
