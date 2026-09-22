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
			"marketplaces": []any{map[string]any{"name": "kept", "lastUpdated": 1, "source": map[string]any{"kind": "url"}}},
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
	for _, malformed := range []map[string]any{
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": 42}},
		}},
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "applied": map[string]any{
			"marketplaces": []any{nil},
		}},
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "applied": map[string]any{
			"marketplaces": []any{map[string]any{}},
		}},
		// A member missing a REQUIRED field: decoding into the struct
		// erases presence, so lastUpdated unmarshals into a zero and the
		// row looks whole. The SDK's wire-shape re-check rejects it.
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": "kept", "source": map[string]any{"kind": "url"}}},
		}},
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "applied": map[string]any{
			"marketplaces": []any{map[string]any{"lastUpdated": 1, "source": map[string]any{"kind": "url"}}},
		}},
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": "kept", "lastUpdated": 1}},
		}},
		// Optional fields that are PRESENT but null: each decodes into a
		// zero string, so the typed row passes the non-empty checks while
		// the wire shape the hub guarantees says they were never strings.
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": "kept", "lastUpdated": 1, "installLocation": nil, "source": map[string]any{"kind": "url"}}},
		}},
		{"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains), "applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": "kept", "lastUpdated": 1, "source": map[string]any{"kind": "url", "repo": nil, "ref": nil}}},
		}},
	} {
		state, applied := classifyMarketplaceRemovalOutcome(marketplaceCloneRemainsError(malformed))
		if state != marketplaceRemovalUnavailable || applied.Marketplaces != nil {
			t.Fatalf("malformed partial snapshot %v = %v/%+v, want unavailable zero snapshot", malformed, state, applied)
		}
	}
}

func TestMarketplaceMutateResultAppliesTypedSnapshotAndKeepsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed"}
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
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

	// The snapshot also arms the read boundary: a straggler read issued
	// before the removal landed cannot land afterward and revert it.
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}},
	})
	straggler := got.(hubModel)
	updated, cmd = straggler.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("straggler read should leave the surviving marketplace selectable")
	}
	if remove := cmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("straggler read resurrected marketplace %q; the applied snapshot must survive", remove.Name)
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
		marketplaceListReadsOrdered:    true,
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
		marketplaceListReadsOrdered:    true,
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
		marketplaceListReadsOrdered:    true,
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

func TestMarketplaceListResultDiscardsStaleReadsAfterRemovalSettles(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept"}
	removed := appwire.MarketplaceEntry{Name: "removed"}
	// The model state after a marked-but-unconfirmed removal settled: the
	// fence is cleared, the panel shows the post-removal list, and the read
	// that settled it was issued after the removal landed.
	m := hubModel{
		pluginsPanel:                   marketplacePanelWithEntries(t, kept),
		marketplaceRemovePending:       "",
		marketplaceReconcilePending:    false,
		marketplaceReconcileGeneration: 1,
		marketplaceListReadsOrdered:    true,
	}

	// A straggler read issued BEFORE the removal landed - the kind every
	// ordinary list read is - cannot be authoritative anymore, however late
	// it arrives: its snapshot still carries the removed marketplace, and
	// accepting it would resurrect the row the settlement already dropped.
	got, _ := m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}},
	})
	after := got.(hubModel)
	updated, cmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale read should leave the surviving marketplace selectable")
	}
	if remove := cmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("stale read resurrected marketplace %q; the settled list must survive", remove.Name)
	}
}

func TestMarketplaceMutateResultSuccessAlsoRejectsLaterStaleReads(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept"}
	removed := appwire.MarketplaceEntry{Name: "removed"}
	m := hubModel{
		pluginsPanel:             marketplacePanelWithEntries(t, removed),
		marketplaceRemovePending: removed.Name,
	}

	// A successful remove answers with the post-removal list itself; the
	// same straggler race applies to the reads issued before it landed.
	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:   appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
		Action: "remove",
		Name:   removed.Name,
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" {
		t.Fatalf("successful remove left the fence at %q, want cleared", after.marketplaceRemovePending)
	}
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}},
	})
	recovered := got.(hubModel)
	updated, cmd := recovered.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale read should leave the surviving marketplace selectable")
	}
	if remove := cmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("stale read resurrected marketplace %q; the successful removal must survive", remove.Name)
	}
}

func TestMarketplaceListResultDiscardsTaggedReadsOlderThanTheLatestRemoval(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept"}
	latest := appwire.MarketplaceEntry{Name: "latest"}
	// Two removals have landed and settled; the model's read sequence
	// stands at 3, so the read tagged 2 was issued before the latest
	// removal landed - it still carries that marketplace.
	m := hubModel{
		pluginsPanel:                   marketplacePanelWithEntries(t, kept),
		marketplaceRemovePending:       "",
		marketplaceReconcilePending:    false,
		marketplaceReconcileGeneration: 3,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           2,
	}

	got, _ := m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:                appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{latest, kept}},
		ReconcileGeneration: 2,
	})
	after := got.(hubModel)
	updated, cmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("delayed read should leave the surviving marketplace selectable")
	}
	if remove := cmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("delayed pre-removal read resurrected marketplace %q; the latest removal must survive", remove.Name)
	}
}

func TestMarketplaceMutateResultSuccessAdvancesTheRemovalBoundary(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	removing := appwire.MarketplaceEntry{Name: "removing"}
	// A removal is in flight on a model whose reads are already ordered:
	// the read tagged 2 was issued before the removal landed.
	m := hubModel{
		pluginsPanel:                   marketplacePanelWithEntries(t, removing),
		marketplaceRemovePending:       removing.Name,
		marketplaceReconcileGeneration: 2,
		marketplaceListReadsOrdered:    true,
	}

	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:   appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
		Action: "remove",
		Name:   removing.Name,
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" {
		t.Fatalf("successful remove left the fence at %q, want cleared", after.marketplaceRemovePending)
	}

	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:                appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removing, kept}},
		ReconcileGeneration: 2,
	})
	recovered := got.(hubModel)
	updated, cmd := recovered.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("delayed read should leave the surviving marketplace selectable")
	}
	if remove := cmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("delayed pre-removal read resurrected marketplace %q; the successful removal must survive", remove.Name)
	}
}

func TestMarketplaceListResultOverlappingReadsSettleDespiteNewerFailure(t *testing.T) {
	confirmed := appwire.MarketplaceEntry{Name: "kept"}
	stale := appwire.MarketplaceEntry{Name: "removed"}
	// A marked-but-unconfirmed removal issued its settle read (generation
	// 1, above the floor) and a notification refetch has since issued a
	// newer read (generation 2); both are in flight, and the panel still
	// shows the stale row.
	m := hubModel{
		pluginsPanel:                   marketplacePanelWithEntries(t, stale),
		marketplaceRemovePending:       stale.Name,
		marketplaceReconcilePending:    true,
		marketplaceReconcileGeneration: 2,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           0,
	}

	// The older reconciliation read succeeds: it was issued after the
	// removal landed, so it confirms the post-removal state whichever
	// refresh was issued last.
	got, _ := m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:                appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{confirmed}},
		ReconcileGeneration: 1,
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("older successful read left the fence at %q/%v, want settled", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("older successful read should settle the panel on its list")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != confirmed.Name {
		t.Fatalf("panel row after older successful read = %q, want %q", remove.Name, confirmed.Name)
	}

	// The newer notification read then fails: the settlement must stand.
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		Err:                 errors.New("list read failed"),
		ReconcileGeneration: 2,
	})
	settled := got.(hubModel)
	if settled.marketplaceRemovePending != "" || settled.marketplaceReconcilePending {
		t.Fatalf("newer failed read rearmed the fence to %q/%v, want the settlement kept", settled.marketplaceRemovePending, settled.marketplaceReconcilePending)
	}
	updated, panelCmd = settled.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("newer failed read should keep the settled marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != confirmed.Name {
		t.Fatalf("panel row after newer failed read = %q, want the settled %q", remove.Name, confirmed.Name)
	}
}

func TestMarketplaceMutateResultMalformedMemberSnapshotStaysFenced(t *testing.T) {
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
	// The clone-remains marker carrying a snapshot whose members are not
	// real rows: json.Unmarshal succeeds, but a null or empty-object member
	// is not a marketplace.
	err := marketplaceCloneRemainsError(map[string]any{
		"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains),
		"applied":         map[string]any{"marketplaces": []any{nil}},
	})

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err, Action: "remove", Name: stale.Name})
	after := got.(hubModel)
	if cmd == nil {
		t.Fatal("malformed-member snapshot should request a fresh list")
	}
	if after.marketplaceRemovePending != stale.Name || !after.marketplaceReconcilePending {
		t.Fatalf("malformed-member snapshot pending = %q/%v, want fenced until a fresh list", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("malformed-member snapshot should preserve the stale marketplace row")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != stale.Name {
		t.Fatalf("panel row after malformed-member snapshot = %q, want the stale %q", remove.Name, stale.Name)
	}

	list := cmd().(launchconfig.MarketplaceListResultMsg)
	if list.Err != nil || len(list.List.Marketplaces) != 1 || list.List.Marketplaces[0].Name != confirmed.Name {
		t.Fatalf("reconcile result = %+v, want confirmed list", list)
	}
	got, _ = after.handleMarketplaceListResult(list)
	reconciled := got.(hubModel)
	if reconciled.marketplaceRemovePending != "" || reconciled.marketplaceReconcilePending {
		t.Fatalf("after reconciliation pending state = %q/%v, want cleared", reconciled.marketplaceRemovePending, reconciled.marketplaceReconcilePending)
	}
}

func TestMarketplaceListResultRejectsSuccessOlderThanAnAppliedRead(t *testing.T) {
	newest := appwire.MarketplaceEntry{Name: "newest", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	older := appwire.MarketplaceEntry{Name: "older", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	// Two reads are in flight on a model whose reads are ordered: the
	// older one (generation 1) captured the list before another client's
	// removal, the newer one (generation 2) after it.
	m := hubModel{
		pluginsPanel:                   marketplacePanelWithEntries(t, older, newest),
		marketplaceReconcileGeneration: 2,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           0,
	}

	// The newer read's response lands first...
	got, _ := m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:                appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{newest}},
		ReconcileGeneration: 2,
	})
	after := got.(hubModel)
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("newer read should leave the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != newest.Name {
		t.Fatalf("panel row after newer read = %q, want %q", remove.Name, newest.Name)
	}

	// ...and the older one arrives afterward: it was issued before the
	// applied read and would restore the snapshot that one superseded.
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:                appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{older, newest}},
		ReconcileGeneration: 1,
	})
	rejected := got.(hubModel)
	updated, panelCmd = rejected.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("older read should leave the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != newest.Name {
		t.Fatalf("older read restored marketplace %q; the applied newer list must survive", remove.Name)
	}
}

func TestMarketplaceMutateResultStaleRefreshCannotResurrect(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanup()

	// A refresh of "removed" was issued before the removal landed; the
	// removal landed, its reconciliation read has been applied, and the
	// fence is settled.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, kept),
		marketplaceReconcileGeneration: 2,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           1,
		marketplaceListApplied:         2,
	}

	// The delayed refresh result still carries the removed marketplace.
	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}},
		Action:     "refresh",
		Name:       removed.Name,
		Generation: 1,
	})
	after := got.(hubModel)
	if cmd == nil {
		t.Fatal("stale refresh snapshot should schedule a replacement read")
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale refresh should leave the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("stale refresh resurrected marketplace %q; the settled list must survive", remove.Name)
	}

	// The replacement read converges the panel on the post-removal truth.
	list := cmd().(launchconfig.MarketplaceListResultMsg)
	if list.Err != nil || len(list.List.Marketplaces) != 1 || list.List.Marketplaces[0].Name != kept.Name {
		t.Fatalf("replacement read = %+v, want the settled list", list)
	}
	got, _ = after.handleMarketplaceListResult(list)
	recovered := got.(hubModel)
	updated, panelCmd = recovered.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("replacement read should leave the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("panel row after replacement = %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceMutateResultFreshSnapshotSettlesPendingReconciliation(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanup()

	// A marked-but-unconfirmed removal issued its settle read (generation
	// 2, still outstanding) while a refresh issued after the removal landed
	// (generation 3) succeeds first.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removed),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcilePending:    true,
		marketplaceReconcileGeneration: 2,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           0,
	}

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
		Action:     "refresh",
		Name:       removed.Name,
		Generation: 3,
	})
	after := got.(hubModel)
	if cmd != nil {
		t.Fatal("fresh refresh snapshot needs no replacement read")
	}
	// The refresh's post-removal snapshot confirms the removal: the
	// pending reconciliation must settle on it.
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("fresh refresh snapshot left the fence at %q/%v, want settled", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("fresh refresh snapshot should settle the panel on its list")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("panel row after fresh refresh = %q, want %q", remove.Name, kept.Name)
	}

	// The outstanding settle read (generation 2) arrives after: it is
	// superseded by the applied refresh, and the settlement must stand.
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:                appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
		ReconcileGeneration: 2,
	})
	superseded := got.(hubModel)
	if superseded.marketplaceRemovePending != "" || superseded.marketplaceReconcilePending {
		t.Fatalf("superseded settle read rearmed the fence to %q/%v, want the settlement kept", superseded.marketplaceRemovePending, superseded.marketplaceReconcilePending)
	}

	// Even a later failed notification refetch keeps the fence clear.
	got, _ = superseded.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		Err:                 errors.New("list read failed"),
		ReconcileGeneration: 4,
	})
	settled := got.(hubModel)
	if settled.marketplaceRemovePending != "" || settled.marketplaceReconcilePending {
		t.Fatalf("failed later refetch rearmed the fence to %q/%v, want the settlement kept", settled.marketplaceRemovePending, settled.marketplaceReconcilePending)
	}
	updated, panelCmd = settled.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("settled panel should keep the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("panel row after failed refetch = %q, want the settled %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceListResultNewestReadFailureReachesThePanel(t *testing.T) {
	// During a pending reconciliation, /plugins was reopened (issuing the
	// list read, generation 2) and an add was submitted after it (bumping
	// the ordering counter to 3); both requests then fail. The fresh panel
	// is still showing its loading state.
	panel := launchconfig.NewPluginsPanel()
	m := hubModel{
		pluginsPanel:                   &panel,
		marketplaceRemovePending:       "removed",
		marketplaceReconcilePending:    true,
		marketplaceReconcileGeneration: 3,
		marketplaceListReadIssued:      2,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           1,
	}

	// The newest list read's failure must reach the panel and clear its
	// loading state, however many mutations were issued after it: an add's
	// issuance never outranks a list read's failure.
	got, _ := m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		Err:                 errors.New("list read failed"),
		ReconcileGeneration: 2,
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "removed" || !after.marketplaceReconcilePending {
		t.Fatalf("failed read settled the fence to %q/%v, want the fence standing", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	view := after.pluginsPanel.View()
	if strings.Contains(view, "Loading marketplaces…") {
		t.Fatal("the newest read's failure must clear the panel's loading state")
	}
	if !strings.Contains(view, "Error: list read failed") {
		t.Fatalf("panel view = %q, want the read failure surfaced", view)
	}
}

func TestMarketplaceMutateResultSuccessRefetchesPastTheAdvancingFloor(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	removing := appwire.MarketplaceEntry{Name: "removing"}
	added := appwire.MarketplaceEntry{Name: "added"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{added, kept}}, nil
		})
	})
	defer cleanup()

	// A remove is in flight on an ordered model; a notification read the
	// hub answered after the removal landed - carrying a NEWER change than
	// the removal's own snapshot - was issued as generation 2.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removing, kept),
		marketplaceRemovePending:       removing.Name,
		marketplaceReconcileGeneration: 2,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           1,
	}

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:   appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
		Action: "remove",
		Name:   removing.Name,
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" {
		t.Fatalf("successful remove left the fence at %q, want cleared", after.marketplaceRemovePending)
	}
	// The snapshot settles the panel, and the branch must schedule the
	// replacement read for the reads its floor advance just invalidated.
	if cmd == nil {
		t.Fatal("successful remove should schedule the replacement read past the advancing floor")
	}

	// The invalidated notification read is discarded, snapshot and all.
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:                appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{added, kept}},
		ReconcileGeneration: 2,
	})
	discarded := got.(hubModel)
	updated, panelCmd := discarded.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("invalidated read should leave the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("panel row after invalidated read = %q, want the snapshot's %q", remove.Name, kept.Name)
	}

	// The scheduled replacement read carries a post-floor generation and
	// settles the panel on the newer truth.
	list := cmd().(launchconfig.MarketplaceListResultMsg)
	if list.Err != nil || list.ReconcileGeneration <= after.marketplaceListFloor {
		t.Fatalf("replacement read = %+v, want a post-floor generation", list)
	}
	got, _ = after.handleMarketplaceListResult(list)
	recovered := got.(hubModel)
	updated, panelCmd = recovered.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("replacement read should leave the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != added.Name {
		t.Fatalf("panel row after replacement read = %q, want the newer %q", remove.Name, added.Name)
	}
}

func TestMarketplaceMutateResultAppliedSnapshotRefetchesPastTheAdvancingFloor(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	removing := appwire.MarketplaceEntry{Name: "removing"}
	added := appwire.MarketplaceEntry{Name: "added"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{added, kept}}, nil
		})
	})
	defer cleanup()

	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removing, kept),
		marketplaceRemovePending:       removing.Name,
		marketplaceReconcileGeneration: 2,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           1,
	}
	err := marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
		Applied: appwire.MarketplaceListResponse{
			Marketplaces: []appwire.MarketplaceEntry{kept},
		},
	})

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err, Action: "remove", Name: removing.Name})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" {
		t.Fatalf("applied-with-litter outcome left the fence at %q, want cleared", after.marketplaceRemovePending)
	}
	if cmd == nil {
		t.Fatal("applied snapshot should schedule the replacement read past the advancing floor")
	}

	// The invalidated notification read is discarded in favor of the
	// applied snapshot, and the scheduled replacement settles the panel
	// on the newer truth the snapshot lacks.
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:                appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{added, kept}},
		ReconcileGeneration: 2,
	})
	discarded := got.(hubModel)
	updated, panelCmd := discarded.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("invalidated read should leave the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("panel row after invalidated read = %q, want the snapshot's %q", remove.Name, kept.Name)
	}

	list := cmd().(launchconfig.MarketplaceListResultMsg)
	if list.Err != nil || list.ReconcileGeneration <= after.marketplaceListFloor {
		t.Fatalf("replacement read = %+v, want a post-floor generation", list)
	}
	got, _ = after.handleMarketplaceListResult(list)
	recovered := got.(hubModel)
	updated, panelCmd = recovered.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("replacement read should leave the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != added.Name {
		t.Fatalf("panel row after replacement read = %q, want the newer %q", remove.Name, added.Name)
	}
}
