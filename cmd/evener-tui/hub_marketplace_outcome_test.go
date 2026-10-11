package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitext"
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

// hubWireErrorThroughJSONRPC carries a hub-built WireError across the AppWire
// JSON-RPC serialization boundary the way production does: the hub's server
// wraps a handler error in appwire.ErrorMessage (internal/appserver), the frame
// marshals through Message.MarshalJSON onto the wire, and a client's receive
// loop decodes it with Message.UnmarshalJSON before Client.request returns the
// decoded WireError. After the crossing, Data is the map a JSON-RPC client
// sees - not the hub's typed struct - which is exactly the input
// classifyMarketplaceRemovalOutcome's map paths exist to read.
func hubWireErrorThroughJSONRPC(t *testing.T, hubBuilt appwire.WireError) error {
	t.Helper()
	frame := appwire.ErrorMessage(appwire.NewIntID(1), hubBuilt)
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal the hub's JSON-RPC error frame: %v", err)
	}
	var decoded appwire.Message
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode the hub's JSON-RPC error frame: %v", err)
	}
	if decoded.Error == nil {
		t.Fatal("decoded frame is not an error response")
	}
	wire := decoded.Error.Error
	if _, ok := wire.Data.(map[string]any); !ok {
		t.Fatalf("decoded Data = %#v (%T), want the map a JSON-RPC client decodes", wire.Data, wire.Data)
	}
	return wire
}

// TestClassifyMarketplaceRemovalOutcomeAcrossTheJSONRPCBoundary covers the
// #1944 wire-level Low: the #1940 hub tests pin
// MarketplaceUnregisteredCloneRemainsData and MarketplaceRemoveAppliedData as
// typed Go error values, and the classifier's own tests build their maps by
// hand - neither crosses the actual AppWire JSON-RPC serialization boundary.
// This drives the hub's exact construction sites (app_plugins.go's litter
// branch and success path) through the real codec and inspects decoded
// behavior only, for both post-apply outcomes: an available authoritative
// marketplace array - including an empty one, the shape listMarketplaces
// builds when the removed marketplace was the last - and AppliedUnavailable
// with no authoritative list, plus the success path's own
// marketplaceRemoveApplied marker. The existing discriminator and Go
// omitempty wire shapes are preserved untouched: whatever the boundary does to
// them shows up as decoded behavior here.
func TestClassifyMarketplaceRemovalOutcomeAcrossTheJSONRPCBoundary(t *testing.T) {
	// The hub's available-list shape (the litter branch's Data.Applied built
	// from a successful listMarketplaces read): a real array is authoritative,
	// with its members decoded back whole.
	hubLitterWithKeptEntries := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "marketplace \"acme\": removed, but its clone could not be cleaned up",
		Data: appwire.MarketplaceUnregisteredCloneRemainsData{
			EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
			Applied: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{
				{Name: "kept", LastUpdated: 1, Source: appwire.MarketplaceSourceInput{Kind: "url", URL: "https://example.invalid/kept.git"}},
			}},
		},
	}
	state, applied := classifyMarketplaceRemovalOutcome(hubWireErrorThroughJSONRPC(t, hubLitterWithKeptEntries))
	if state != marketplaceRemovalApplied || len(applied.Marketplaces) != 1 ||
		applied.Marketplaces[0].Name != "kept" || applied.Marketplaces[0].Source.Kind != "url" {
		t.Fatalf("boundary crossing with a kept-entry snapshot = %v/%+v, want applied with the decoded row", state, applied)
	}

	// An authoritative EMPTY array: listMarketplaces allocates its slice with
	// make([]MarketplaceEntry, 0, len(names)), so the removal of the last
	// marketplace sends a real [] on the wire. The client fixture must accept
	// that array as applied-empty - a marketplace list with nothing left is a
	// list, not an absent one.
	hubLitterWithEmptyList := hubLitterWithKeptEntries
	hubLitterWithEmptyList.Data = appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
		Applied:         appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{}},
	}
	state, applied = classifyMarketplaceRemovalOutcome(hubWireErrorThroughJSONRPC(t, hubLitterWithEmptyList))
	if state != marketplaceRemovalApplied || applied.Marketplaces == nil || len(applied.Marketplaces) != 0 {
		t.Fatalf("boundary crossing with an authoritative empty array = %v/%+v, want applied with a real empty snapshot", state, applied)
	}

	// AppliedUnavailable with no authoritative list (the litter branch's
	// reconcile-read failure): the zero Applied marshals with a null
	// marketplaces member, and the marker's flag says why. Never an
	// authoritative empty list.
	hubLitterUnavailable := hubLitterWithKeptEntries
	hubLitterUnavailable.Data = appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo:    appwire.ErrorMarketplaceUnregisteredCloneRemains,
		AppliedUnavailable: true,
	}
	state, applied = classifyMarketplaceRemovalOutcome(hubWireErrorThroughJSONRPC(t, hubLitterUnavailable))
	if state != marketplaceRemovalUnavailable || applied.Marketplaces != nil {
		t.Fatalf("boundary crossing with an unavailable list = %v/%+v, want unavailable with no snapshot", state, applied)
	}

	// Null data without the unavailable flag - a payload whose applied member
	// carries JSON null, which the hub never builds on these paths but a
	// malformed or future peer could - must not read as an authoritative
	// empty list either: the client fixture treats a null array as no array.
	hubLitterNullApplied := hubLitterWithKeptEntries
	hubLitterNullApplied.Data = appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
	}
	state, applied = classifyMarketplaceRemovalOutcome(hubWireErrorThroughJSONRPC(t, hubLitterNullApplied))
	if state != marketplaceRemovalUnavailable || applied.Marketplaces != nil {
		t.Fatalf("boundary crossing with null applied data = %v/%+v, want unavailable, never applied-empty", state, applied)
	}

	// The success path's own failed re-list (the marketplaceRemoveApplied
	// discriminator): the marker alone is the proof, and it survives the
	// boundary as the removed state - no litter, no snapshot to reconcile.
	hubRemovedWithoutList := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "marketplace \"acme\": removed, but the updated list could not be read",
		Data: appwire.MarketplaceRemoveAppliedData{
			EvenerErrorInfo:    appwire.ErrorMarketplaceRemoveApplied,
			AppliedUnavailable: true,
		},
	}
	state, applied = classifyMarketplaceRemovalOutcome(hubWireErrorThroughJSONRPC(t, hubRemovedWithoutList))
	if state != marketplaceRemovalRemoved || applied.Marketplaces != nil {
		t.Fatalf("boundary crossing with the remove-applied marker = %v/%+v, want removed with no snapshot", state, applied)
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

func TestClassifyMarketplaceRemovalOutcomeZeroLastUpdatedByDecodingPath(t *testing.T) {
	// On the JSON path presence is still observable, and a present zero is
	// a timestamp the hub really sends - hubcore.UnixSeconds maps a zero
	// time.Time to 0, and the SDK's member validation deliberately accepts
	// it (marketplaces.test.ts: member validation must not reject payloads
	// the hub really sends). The zero must therefore classify applied
	// exactly like any other present timestamp; the JSON member that omits
	// lastUpdated stays unavailable via the wire-shape check.
	explicitZero := map[string]any{
		"evenerErrorInfo": string(appwire.ErrorMarketplaceUnregisteredCloneRemains),
		"applied": map[string]any{
			"marketplaces": []any{map[string]any{"name": "kept", "lastUpdated": 0, "source": map[string]any{"kind": "url"}}},
		},
	}
	state, applied := classifyMarketplaceRemovalOutcome(marketplaceCloneRemainsError(explicitZero))
	if state != marketplaceRemovalApplied || len(applied.Marketplaces) != 1 || applied.Marketplaces[0].Name != "kept" || applied.Marketplaces[0].LastUpdated != 0 {
		t.Fatalf("JSON present-zero lastUpdated snapshot = %v/%+v, want applied kept snapshot", state, applied)
	}

	// On the typed path decoding has already erased presence: a zero
	// lastUpdated is indistinguishable from the omitted field the JSON
	// path rejects, so the snapshot degrades to unavailable rather than
	// let a possibly-truncated member look authoritative.
	zeroLastUpdated := appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
		Applied: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{{
			Name:   "kept",
			Source: appwire.MarketplaceSourceInput{Kind: "url"},
		}}},
	}
	state, applied = classifyMarketplaceRemovalOutcome(marketplaceCloneRemainsError(zeroLastUpdated))
	if state != marketplaceRemovalUnavailable || applied.Marketplaces != nil {
		t.Fatalf("typed zero-lastUpdated snapshot = %v/%+v, want unavailable zero snapshot", state, applied)
	}

	// A typed member whose lastUpdated is present - the wire shape the hub
	// guarantees - stays authoritative, so the typed rule is a presence
	// compensation, not a freshness claim.
	presentLastUpdated := appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
		Applied: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{{
			Name:        "kept",
			LastUpdated: 1,
			Source:      appwire.MarketplaceSourceInput{Kind: "url"},
		}}},
	}
	state, applied = classifyMarketplaceRemovalOutcome(marketplaceCloneRemainsError(presentLastUpdated))
	if state != marketplaceRemovalApplied || len(applied.Marketplaces) != 1 || applied.Marketplaces[0].Name != "kept" {
		t.Fatalf("typed wire-valid snapshot = %v/%+v, want applied kept snapshot", state, applied)
	}
}

func TestMarketplaceMutateResultAppliesTypedSnapshotAndKeepsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed"}
	kept := appwire.MarketplaceEntry{Name: "kept", LastUpdated: 1, Source: appwire.MarketplaceSourceInput{Kind: "url"}}
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
	if after.marketplaceOutcomeWarning == nil {
		t.Fatal("applied-with-litter result should leave a visible warning")
	}
	if _, ok := errors.AsType[appwire.WireError](after.marketplaceOutcomeWarning); !ok {
		t.Fatalf("warning = %v, want original WireError in error chain", after.marketplaceOutcomeWarning)
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
	// The confirming read settles the reconciliation, so the account it
	// confirmed must not keep claiming the list "could not be confirmed":
	// the clone files that remain on disk are still the truth and keep
	// standing, without the stale uncertainty.
	if reconciled.marketplaceOutcomeWarning == nil {
		t.Fatal("confirming read should keep the clone-remains account standing")
	}
	if strings.Contains(reconciled.marketplaceOutcomeWarning.Error(), "could not be confirmed") {
		t.Fatalf("confirming read left the stale uncertainty in the warning: %v", reconciled.marketplaceOutcomeWarning)
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
	if after.marketplaceOutcomeWarning == nil {
		t.Fatal("removed outcome should leave a visible account")
	}
	if strings.Contains(after.marketplaceOutcomeWarning.Error(), "clone") {
		t.Fatalf("removed outcome warning = %q, want no clone-litter claim", after.marketplaceOutcomeWarning.Error())
	}
	if _, ok := errors.AsType[appwire.WireError](after.marketplaceOutcomeWarning); !ok {
		t.Fatalf("removed outcome warning = %v, want original WireError in error chain", after.marketplaceOutcomeWarning)
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
	// The confirming read settles the refresh the removed outcome promised:
	// nothing was left on disk and the list is current, so no account of the
	// refresh may keep standing.
	if reconciled.marketplaceOutcomeWarning != nil {
		t.Fatalf("confirming read left the removed outcome's account standing: %v", reconciled.marketplaceOutcomeWarning)
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
	// the removal was issued as generation 3, after a read tagged 2 that
	// was issued before the removal landed.
	m := hubModel{
		pluginsPanel:                   marketplacePanelWithEntries(t, removing),
		marketplaceRemovePending:       removing.Name,
		marketplaceReconcileGeneration: 3,
		marketplaceListReadsOrdered:    true,
	}

	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
		Action:     "remove",
		Name:       removing.Name,
		Generation: 3,
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

func TestMarketplaceListResultPreRemovalDelayedReadCannotClobberNewerAddition(t *testing.T) {
	existing := appwire.MarketplaceEntry{Name: "existing", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	added := appwire.MarketplaceEntry{Name: "added", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	// Before any removal lands, the panel's list read (generation 0, the
	// pre-ordering ordinary read) is still in flight when the user adds a
	// marketplace (generation 1) and the add's response lands first.
	m := hubModel{
		pluginsPanel:                   marketplacePanelWithEntries(t, existing),
		marketplaceReconcileGeneration: 1,
	}
	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Action:     "add",
		Generation: 1,
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{existing, added}},
	})
	after := got.(hubModel)
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("add response should leave the marketplace list selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != existing.Name {
		t.Fatalf("panel row after add = %q, want %q", remove.Name, existing.Name)
	}

	// The delayed read - captured before the add, so its list lacks the
	// added marketplace - lands late. It is superseded by the add's
	// applied response, so it must be discarded: landing it would
	// transiently drop the just-added row until the next read healed it.
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List: appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{existing}},
	})
	rejected := got.(hubModel)
	if rejected.marketplaceListApplied != after.marketplaceListApplied {
		t.Fatalf("delayed pre-removal read moved the applied generation to %d, want it kept at %d", rejected.marketplaceListApplied, after.marketplaceListApplied)
	}
	down, _ := rejected.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, panelCmd = down.(launchconfig.PluginsPanel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("delayed pre-removal read should leave the marketplace list selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != added.Name {
		t.Fatalf("delayed pre-removal read clobbered the just-added marketplace; panel row = %q, want %q", remove.Name, added.Name)
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

func TestMarketplaceMutateResultPreRemovalStaleRefreshCannotClobberNewerAddition(t *testing.T) {
	existing := appwire.MarketplaceEntry{Name: "existing", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	added := appwire.MarketplaceEntry{Name: "added", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{existing, added}}, nil
		})
	})
	defer cleanup()

	// Before any removal lands: a refresh of "existing" (generation 1) is
	// in flight when the user adds a marketplace (generation 2) and the
	// add's response lands first.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, existing),
		marketplaceReconcileGeneration: 2,
	}
	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Action:     "add",
		Generation: 2,
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{existing, added}},
	})
	after := got.(hubModel)

	// The delayed refresh response (generation 1, captured before the
	// add) lands late: it is superseded by the add's applied response, so
	// it must be discarded wholesale with the replacement read that
	// lands its effect - not applied over the marketplace the add just
	// added.
	got, cmd := after.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Action:     "refresh",
		Name:       existing.Name,
		Generation: 1,
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{existing}},
	})
	discarded := got.(hubModel)
	if cmd == nil {
		t.Fatal("discarded pre-removal refresh should schedule the replacement read that lands its effect")
	}
	down, _ := discarded.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, panelCmd := down.(launchconfig.PluginsPanel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("discarded pre-removal refresh should leave the marketplace list selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != added.Name {
		t.Fatalf("discarded pre-removal refresh clobbered the just-added marketplace; panel row = %q, want %q", remove.Name, added.Name)
	}

	// The replacement read converges the panel on the current truth.
	list := cmd().(launchconfig.MarketplaceListResultMsg)
	if list.Err != nil || list.ReconcileGeneration != discarded.marketplaceReconcileGeneration {
		t.Fatalf("replacement read = %+v, want the newest tagged generation", list)
	}
	got, _ = discarded.handleMarketplaceListResult(list)
	recovered := got.(hubModel)
	down, _ = recovered.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, panelCmd = down.(launchconfig.PluginsPanel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("replacement read should leave the marketplace list selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != added.Name {
		t.Fatalf("panel row after replacement read = %q, want %q", remove.Name, added.Name)
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

func TestMarketplaceMutateResultStaleRemoveSnapshotCannotResurrect(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	dropped := appwire.MarketplaceEntry{Name: "dropped", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	removing := appwire.MarketplaceEntry{Name: "removing", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{}, errors.New("list read failed")
		})
	})
	defer cleanup()

	// The removal of "removing" is in flight (issued as generation 2); a
	// newer list read (generation 3) has applied, already reflecting
	// another client's removal of "dropped".
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, kept),
		marketplaceRemovePending:       removing.Name,
		marketplaceReconcileGeneration: 3,
		marketplaceListReadIssued:      3,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           1,
		marketplaceListApplied:         3,
		marketplaceListAppliedRows:     []appwire.MarketplaceEntry{kept},
	}

	// The delayed remove response: the hub read its snapshot at the
	// removal's landing, before the other client's change, so it still
	// carries the dropped row.
	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{dropped, kept}},
		Action:     "remove",
		Name:       removing.Name,
		Generation: 2,
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" {
		t.Fatalf("successful remove left the fence at %q, want cleared", after.marketplaceRemovePending)
	}
	if cmd == nil {
		t.Fatal("successful remove should schedule its replacement read")
	}

	// The scheduled replacement read fails: the stale snapshot's verdict
	// must not survive as the panel's state.
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		Err:                 errors.New("list read failed"),
		ReconcileGeneration: 4,
	})
	failed := got.(hubModel)
	updated, panelCmd := failed.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("a failed replacement should leave the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("stale remove snapshot resurrected marketplace %q; the newer applied list must survive", remove.Name)
	}
}

func TestMarketplaceMutateResultRemoveStillArmsTheBoundaryAfterSettlement(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	dropped := appwire.MarketplaceEntry{Name: "dropped", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{}, errors.New("list read failed")
		})
	})
	defer cleanup()

	// A removal's outcome settled and a newer list read applied; the
	// fence is clear, and the boundary stands at the last landing.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, kept),
		marketplaceRemovePending:       "",
		marketplaceReconcileGeneration: 3,
		marketplaceListReadIssued:      3,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           1,
		marketplaceListApplied:         3,
		marketplaceListAppliedRows:     []appwire.MarketplaceEntry{kept},
	}

	// A remove response arrives for a removal whose fence a list read
	// already settled. It must arm the boundary at the latest landing -
	// the fence being clear never disarms the read ordering - and its
	// stale snapshot must not resurrect what the applied read dropped.
	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{dropped, kept}},
		Action:     "remove",
		Name:       "settled",
		Generation: 2,
	})
	after := got.(hubModel)
	if after.marketplaceListFloor != m.marketplaceReconcileGeneration {
		t.Fatalf("remove response left the floor at %d, want it armed at the latest landing %d", after.marketplaceListFloor, m.marketplaceReconcileGeneration)
	}
	if cmd == nil {
		t.Fatal("a remove response past settlement should schedule its replacement read")
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("the settled panel should keep the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("remove response past settlement resurrected marketplace %q", remove.Name)
	}
}

func TestMarketplaceMutateResultFlooredRemoveSnapshotIsDiscarded(t *testing.T) {
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	dropped := appwire.MarketplaceEntry{Name: "dropped", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{}, errors.New("list read failed")
		})
	})
	defer cleanup()

	// A remove response whose issuance predates the latest landed removal
	// (generation 2 against a floor of 3): it is stale exactly like an
	// add or refresh of that vintage.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, kept),
		marketplaceRemovePending:       "",
		marketplaceReconcileGeneration: 4,
		marketplaceListReadIssued:      4,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           3,
		marketplaceListApplied:         4,
		marketplaceListAppliedRows:     []appwire.MarketplaceEntry{kept},
	}

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{dropped, kept}},
		Action:     "remove",
		Name:       "ancient",
		Generation: 2,
	})
	after := got.(hubModel)
	if cmd == nil {
		t.Fatal("a floored remove response should schedule a replacement read")
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("a discarded snapshot should leave the marketplace selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("floored remove snapshot resurrected marketplace %q", remove.Name)
	}
}

func TestMarketplaceRemoveSurfacesDifferentNameWhilePending(t *testing.T) {
	m := hubModel{marketplaceRemovePending: "first"}
	got, cmd := m.handleMarketplaceRemove(launchconfig.MarketplaceRemoveMsg{Name: "second"})
	after := got.(hubModel)
	if cmd != nil {
		t.Fatal("a remove issued while another is unconfirmed must not go out concurrently")
	}
	if after.marketplaceRemovePending != "first" {
		t.Fatalf("different-name remove clobbered the pending identity to %q", after.marketplaceRemovePending)
	}
	if after.err == nil {
		t.Fatal("a different-name remove during a pending removal was dropped without any feedback")
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
	// the removal's own snapshot - was issued as generation 2, before the
	// removal was issued as generation 3.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removing, kept),
		marketplaceRemovePending:       removing.Name,
		marketplaceReconcileGeneration: 3,
		marketplaceListReadIssued:      2,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           1,
	}

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
		Action:     "remove",
		Name:       removing.Name,
		Generation: 3,
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
	kept := appwire.MarketplaceEntry{Name: "kept", LastUpdated: 1, Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	removing := appwire.MarketplaceEntry{Name: "removing"}
	added := appwire.MarketplaceEntry{Name: "added"}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{added, kept}}, nil
		})
	})
	defer cleanup()

	// A remove is in flight on an ordered model, issued as generation 3
	// after a notification read tagged 2.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removing, kept),
		marketplaceRemovePending:       removing.Name,
		marketplaceReconcileGeneration: 3,
		marketplaceListReadIssued:      2,
		marketplaceListReadsOrdered:    true,
		marketplaceListFloor:           1,
	}
	err := marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
		Applied: appwire.MarketplaceListResponse{
			Marketplaces: []appwire.MarketplaceEntry{kept},
		},
	})

	got, cmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err, Action: "remove", Name: removing.Name, Generation: 3})
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

func TestMarketplaceStaleRefreshDuringReconciliationKeepsWarning(t *testing.T) {
	stale := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	confirmed := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{confirmed}}, nil
		})
	})
	defer cleanup()

	// A remove is in flight (issued as generation 1) and a refresh of
	// "kept" was issued after it (generation 2) when the removal's
	// unavailable outcome lands and arms the read boundary.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, stale),
		marketplaceRemovePending:       stale.Name,
		marketplaceReconcileGeneration: 1,
	}
	issued, delayedRefresh := m.handleMarketplaceRefresh(launchconfig.MarketplaceRefreshMsg{Name: confirmed.Name})
	m = issued.(hubModel)
	if delayedRefresh == nil {
		t.Fatal("refresh did not create a delayed mutation command")
	}
	got, reconcileCmd := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err:        marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains, AppliedUnavailable: true}),
		Action:     "remove",
		Name:       stale.Name,
		Generation: 1,
	})
	after := got.(hubModel)
	if reconcileCmd == nil || !after.marketplaceReconcilePending {
		t.Fatal("unavailable removal did not start reconciliation")
	}
	if after.marketplaceOutcomeWarning == nil {
		t.Fatal("unavailable removal did not leave its warning standing")
	}

	// The refresh was issued before the removal landed, so its snapshot
	// still carries the removed marketplace: the boundary must discard it
	// wholesale - without disturbing the fence, and without erasing the
	// warning, which no list read may erase either.
	got, replacement := after.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{stale}},
		Action:     "refresh",
		Name:       confirmed.Name,
		Generation: 2,
	})
	after = got.(hubModel)
	if replacement == nil {
		t.Fatal("discarded stale refresh should schedule the replacement read that lands its effect")
	}
	if after.marketplaceRemovePending != stale.Name || !after.marketplaceReconcilePending {
		t.Fatalf("stale refresh changed pending state = %q/%v, want fence preserved", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	if after.marketplaceOutcomeWarning == nil || !strings.Contains(after.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("stale refresh cleared the standing removal warning = %v", after.marketplaceOutcomeWarning)
	}
	updated, panelCmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale refresh snapshot discarded the pending marketplace row")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != stale.Name {
		t.Fatalf("stale refresh snapshot selected %q, want %q", remove.Name, stale.Name)
	}

	fresh := replacement().(launchconfig.MarketplaceListResultMsg)
	if fresh.Err != nil || fresh.ReconcileGeneration != after.marketplaceReconcileGeneration || fresh.List.Marketplaces[0].Name != confirmed.Name {
		t.Fatalf("replacement list = %+v, want the current confirmed snapshot", fresh)
	}
	got, _ = after.handleMarketplaceListResult(fresh)
	after = got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("replacement list left pending state = %q/%v, want cleared", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
}

func TestMarketplaceStaleListReadDuringReconciliationKeepsWarning(t *testing.T) {
	stale := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	confirmed := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{confirmed}}, nil
		})
	})
	defer cleanup()

	// The unavailable outcome lands and arms the read boundary (generation
	// 2) with its reconciliation read (generation 3) still outstanding.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, stale),
		marketplaceRemovePending:       stale.Name,
		marketplaceReconcileGeneration: 2,
	}
	got, reconcile := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err: marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
			EvenerErrorInfo:    appwire.ErrorMarketplaceUnregisteredCloneRemains,
			AppliedUnavailable: true,
		}),
		Action:     "remove",
		Name:       stale.Name,
		Generation: 2,
	})
	after := got.(hubModel)
	if reconcile == nil || !after.marketplaceReconcilePending || after.marketplaceOutcomeWarning == nil {
		t.Fatal("unavailable removal did not arm reconciliation with its warning")
	}

	// A read issued before the removal landed (generation 1, below the
	// floor) arrives while the reconciliation is pending: the boundary
	// discards it wholesale, and a discarded read is not the model's news
	// - exactly like a discarded mutation, it must not erase the warning
	// or settle the fence.
	got, _ = after.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{
		List:                appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{stale}},
		ReconcileGeneration: 1,
	})
	straggler := got.(hubModel)
	if straggler.marketplaceRemovePending != stale.Name || !straggler.marketplaceReconcilePending {
		t.Fatalf("stale read settled the fence to %q/%v, want preserved", straggler.marketplaceRemovePending, straggler.marketplaceReconcilePending)
	}
	if straggler.marketplaceOutcomeWarning == nil || !strings.Contains(straggler.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("stale read erased the standing removal warning = %v", straggler.marketplaceOutcomeWarning)
	}

	// The outstanding confirming read is the model's news: it settles the
	// fence and strips the warning's stale uncertainty while the
	// clone-remains fact keeps standing.
	fresh := reconcile().(launchconfig.MarketplaceListResultMsg)
	if fresh.Err != nil || fresh.ReconcileGeneration != straggler.marketplaceReconcileGeneration {
		t.Fatalf("confirming read = %+v, want the outstanding tagged generation", fresh)
	}
	got, _ = straggler.handleMarketplaceListResult(fresh)
	reconciled := got.(hubModel)
	if reconciled.marketplaceRemovePending != "" || reconciled.marketplaceReconcilePending {
		t.Fatalf("confirming read left pending state = %q/%v, want cleared", reconciled.marketplaceRemovePending, reconciled.marketplaceReconcilePending)
	}
	if reconciled.marketplaceOutcomeWarning == nil || strings.Contains(reconciled.marketplaceOutcomeWarning.Error(), "could not be confirmed") {
		t.Fatalf("confirming read left the warning = %v, want the clone-remains fact without the stale uncertainty", reconciled.marketplaceOutcomeWarning)
	}
}

func TestMarketplaceFreshMutationDuringReconciliationKeepsCloneRemainsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	added := appwire.MarketplaceEntry{Name: "added", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanup()

	// The unavailable outcome lands and arms the read boundary (generation
	// 1) with its reconciliation read (generation 2) still outstanding and
	// its warning standing.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removed),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcileGeneration: 1,
	}
	got, reconcile := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err: marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
			EvenerErrorInfo:    appwire.ErrorMarketplaceUnregisteredCloneRemains,
			AppliedUnavailable: true,
		}),
		Action:     "remove",
		Name:       removed.Name,
		Generation: 1,
	})
	after := got.(hubModel)
	if reconcile == nil || !after.marketplaceReconcilePending || after.marketplaceOutcomeWarning == nil {
		t.Fatal("unavailable removal did not arm reconciliation with its warning")
	}

	// A successful add - real news, issued after the removal landed - lands
	// as a MUTATE response while the reconciliation read is still
	// outstanding. It settles the fence exactly like a confirming read, so
	// it must settle the standing account the same way: the clone files the
	// outcome reported are still on disk, so only the stale uncertainty
	// strips - the fresh mutation must not erase the fact wholesale.
	got, cmd := after.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Action:     "add",
		Generation: 3,
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept, added}},
	})
	settled := got.(hubModel)
	if cmd != nil {
		t.Fatal("fresh add snapshot needs no replacement read")
	}
	if settled.marketplaceRemovePending != "" || settled.marketplaceReconcilePending {
		t.Fatalf("fresh add left the fence at %q/%v, want settled", settled.marketplaceRemovePending, settled.marketplaceReconcilePending)
	}
	if settled.marketplaceOutcomeWarning == nil || !strings.Contains(settled.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("fresh add erased the standing clone-remains warning = %v", settled.marketplaceOutcomeWarning)
	}
	if strings.Contains(settled.marketplaceOutcomeWarning.Error(), "could not be confirmed") {
		t.Fatalf("fresh add left the stale uncertainty in the warning: %v", settled.marketplaceOutcomeWarning)
	}
	updated, panelCmd := settled.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("fresh add should leave the marketplace list selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("panel row after fresh add = %q, want %q", remove.Name, kept.Name)
	}

	// The outstanding reconciliation read is superseded by the add's
	// applied response, so landing it later must change nothing: a
	// discarded read is not the model's news.
	straggler := reconcile().(launchconfig.MarketplaceListResultMsg)
	got, _ = settled.handleMarketplaceListResult(straggler)
	late := got.(hubModel)
	if late.marketplaceOutcomeWarning == nil || !strings.Contains(late.marketplaceOutcomeWarning.Error(), "clone files remain") || strings.Contains(late.marketplaceOutcomeWarning.Error(), "could not be confirmed") {
		t.Fatalf("superseded reconcile read changed the standing warning = %v", late.marketplaceOutcomeWarning)
	}
}

func TestMarketplaceFreshMutationRetryClearsOrdinaryFailureAndKeepsCloneRemainsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanup()

	// The unavailable outcome lands and arms the read boundary (generation
	// 1) with its reconciliation read (generation 2) still outstanding.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removed),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcileGeneration: 1,
	}
	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err: marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
			EvenerErrorInfo:    appwire.ErrorMarketplaceUnregisteredCloneRemains,
			AppliedUnavailable: true,
		}),
		Action:     "remove",
		Name:       removed.Name,
		Generation: 1,
	})
	after := got.(hubModel)
	if !after.marketplaceReconcilePending {
		t.Fatal("unavailable removal did not arm reconciliation")
	}

	// A refresh of "kept" (generation 3) fails: the failure becomes the
	// prominent transient error, the fence must keep standing, and the
	// standing outcome account must keep standing too - an ordinary
	// failure is news about the refresh, never about the removal.
	got, _ = after.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err:        errors.New("refresh failed"),
		Action:     "refresh",
		Name:       kept.Name,
		Generation: 3,
	})
	failed := got.(hubModel)
	if failed.err == nil || !strings.Contains(failed.err.Error(), "refresh failed") {
		t.Fatalf("failed refresh did not surface its failure = %v", failed.err)
	}
	if failed.marketplaceOutcomeWarning == nil || !strings.Contains(failed.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("failed refresh erased the standing clone-remains warning = %v", failed.marketplaceOutcomeWarning)
	}
	if failed.marketplaceRemovePending != removed.Name || !failed.marketplaceReconcilePending {
		t.Fatalf("failed refresh disturbed the fence = %q/%v, want preserved", failed.marketplaceRemovePending, failed.marketplaceReconcilePending)
	}

	// The retry (generation 4) succeeds: its fresh snapshot settles the
	// fence, the superseded ordinary failure clears, and the settle strips
	// the standing account's stale uncertainty - the clone-remains fact
	// itself is still true and keeps standing.
	got, cmd := failed.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Action:     "refresh",
		Name:       kept.Name,
		Generation: 4,
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
	})
	retried := got.(hubModel)
	if cmd != nil {
		t.Fatal("fresh retry snapshot needs no replacement read")
	}
	if retried.marketplaceRemovePending != "" || retried.marketplaceReconcilePending {
		t.Fatalf("successful retry left the fence at %q/%v, want settled", retried.marketplaceRemovePending, retried.marketplaceReconcilePending)
	}
	if retried.err != nil {
		t.Fatalf("successful retry left the superseded failure standing: %v", retried.err)
	}
	if retried.marketplaceOutcomeWarning == nil || !strings.Contains(retried.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("successful retry erased the standing clone-remains warning = %v", retried.marketplaceOutcomeWarning)
	}
	if strings.Contains(retried.marketplaceOutcomeWarning.Error(), "could not be confirmed") {
		t.Fatalf("successful retry left the stale uncertainty in the warning: %v", retried.marketplaceOutcomeWarning)
	}
	updated, panelCmd := retried.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("fresh retry should leave the marketplace list selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("panel row after fresh retry = %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceFreshMutationAfterAppliedSettlementKeepsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	kept := appwire.MarketplaceEntry{Name: "kept", LastUpdated: 1, Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	added := appwire.MarketplaceEntry{Name: "added", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanup()

	// The removal lands with an applied-with-litter outcome: the fence
	// clears immediately, the clone-remains warning stands on its own
	// account, and the replacement read (generation 2) is outstanding.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removed, kept),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcileGeneration: 1,
	}
	got, replacement := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err: marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
			EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
			Applied:         appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
		}),
		Action:     "remove",
		Name:       removed.Name,
		Generation: 1,
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("applied settlement state = %q/%v, want cleared fence", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	if after.marketplaceOutcomeWarning == nil || !strings.Contains(after.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("applied settlement did not leave its warning standing = %v", after.marketplaceOutcomeWarning)
	}

	// A later successful add is fresh news, but it does not supersede the
	// warning: the clone files the outcome reported are still on disk,
	// nothing re-derives that fact, and erasing the notice would leave the
	// litter unreported until the next removal happens to mention it.
	got, cmd := after.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Action:     "add",
		Generation: 3,
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept, added}},
	})
	settled := got.(hubModel)
	if cmd != nil {
		t.Fatal("fresh add snapshot needs no replacement read")
	}
	if settled.marketplaceRemovePending != "" || settled.marketplaceReconcilePending {
		t.Fatalf("fresh add disturbed the settled fence = %q/%v", settled.marketplaceRemovePending, settled.marketplaceReconcilePending)
	}
	if settled.marketplaceOutcomeWarning == nil || !strings.Contains(settled.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("fresh add erased the standing clone-remains warning = %v", settled.marketplaceOutcomeWarning)
	}
	updated, _ := settled.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, panelCmd := updated.(launchconfig.PluginsPanel).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if panelCmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("fresh add should leave the marketplace list selectable")
	}
	if remove := panelCmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != added.Name {
		t.Fatalf("panel row after fresh add = %q, want %q", remove.Name, added.Name)
	}

	// The settlement's own replacement read, superseded by the add's
	// applied response, must change nothing when it lands late.
	straggler := replacement().(launchconfig.MarketplaceListResultMsg)
	got, _ = settled.handleMarketplaceListResult(straggler)
	late := got.(hubModel)
	if late.marketplaceOutcomeWarning == nil || !strings.Contains(late.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("superseded replacement read changed the standing warning = %v", late.marketplaceOutcomeWarning)
	}
}

func TestMarketplaceFreshMutationAfterFreshReconciliationKeepsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	added := appwire.MarketplaceEntry{Name: "added", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanup()

	// The unavailable outcome lands, and its confirming read settles the
	// reconciliation: the fence clears and the warning keeps only the
	// still-true clone-remains fact, stripped of the stale uncertainty.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removed),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcileGeneration: 1,
	}
	got, reconcile := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err: marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
			EvenerErrorInfo:    appwire.ErrorMarketplaceUnregisteredCloneRemains,
			AppliedUnavailable: true,
		}),
		Action:     "remove",
		Name:       removed.Name,
		Generation: 1,
	})
	after := got.(hubModel)
	if reconcile == nil || !after.marketplaceReconcilePending {
		t.Fatal("unavailable removal did not arm reconciliation")
	}
	fresh := reconcile().(launchconfig.MarketplaceListResultMsg)
	got, _ = after.handleMarketplaceListResult(fresh)
	reconciled := got.(hubModel)
	if reconciled.marketplaceRemovePending != "" || reconciled.marketplaceReconcilePending {
		t.Fatalf("confirming read left the fence at %q/%v, want settled", reconciled.marketplaceRemovePending, reconciled.marketplaceReconcilePending)
	}
	if reconciled.marketplaceOutcomeWarning == nil || !strings.Contains(reconciled.marketplaceOutcomeWarning.Error(), "clone files remain") || strings.Contains(reconciled.marketplaceOutcomeWarning.Error(), "could not be confirmed") {
		t.Fatalf("confirming read left the warning = %v, want the clone-remains fact without the stale uncertainty", reconciled.marketplaceOutcomeWarning)
	}

	// A later successful add must not erase the settled clone-remains fact
	// either: the fence is down, but the clone files are still on disk.
	got, cmd := reconciled.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Action:     "add",
		Generation: 3,
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept, added}},
	})
	settled := got.(hubModel)
	if cmd != nil {
		t.Fatal("fresh add snapshot needs no replacement read")
	}
	if settled.marketplaceRemovePending != "" || settled.marketplaceReconcilePending {
		t.Fatalf("fresh add disturbed the settled fence = %q/%v", settled.marketplaceRemovePending, settled.marketplaceReconcilePending)
	}
	if settled.marketplaceOutcomeWarning == nil || !strings.Contains(settled.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("fresh add erased the settled clone-remains warning = %v", settled.marketplaceOutcomeWarning)
	}
	if strings.Contains(settled.marketplaceOutcomeWarning.Error(), "could not be confirmed") {
		t.Fatalf("fresh add restored the stale uncertainty = %v", settled.marketplaceOutcomeWarning)
	}
}

func TestMarketplaceFreshMutationSettlesRemovedOutcomeNotice(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	added := appwire.MarketplaceEntry{Name: "added", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanup()

	// The removed outcome lands: nothing was left on disk, so its warning
	// is only the refresh account, and the reconciliation is pending.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removed),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcileGeneration: 1,
	}
	err := appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: `marketplace "removed": removed, but the updated list could not be read`,
		Data: appwire.MarketplaceRemoveAppliedData{
			EvenerErrorInfo:    appwire.ErrorMarketplaceRemoveApplied,
			AppliedUnavailable: true,
		},
	}
	got, reconcile := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err:        err,
		Action:     "remove",
		Name:       removed.Name,
		Generation: 1,
	})
	after := got.(hubModel)
	if reconcile == nil || !after.marketplaceReconcilePending || after.marketplaceOutcomeWarning == nil {
		t.Fatal("removed outcome did not arm reconciliation with its account")
	}

	// A fresh add settles the reconciliation while it is pending, and the
	// settle clears the refresh account exactly like a confirming read:
	// nothing was left on disk, so no fact survives it.
	got, cmd := after.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Action:     "add",
		Generation: 3,
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept, added}},
	})
	settled := got.(hubModel)
	if cmd != nil {
		t.Fatal("fresh add snapshot needs no replacement read")
	}
	if settled.marketplaceRemovePending != "" || settled.marketplaceReconcilePending {
		t.Fatalf("fresh add left the fence at %q/%v, want settled", settled.marketplaceRemovePending, settled.marketplaceReconcilePending)
	}
	if settled.marketplaceOutcomeWarning != nil {
		t.Fatalf("fresh add left the removed outcome's account standing: %v", settled.marketplaceOutcomeWarning)
	}
}

func TestMarketplaceDelayedRefreshAfterAppliedSettlementKeepsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	kept := appwire.MarketplaceEntry{Name: "kept", LastUpdated: 1, Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceRefresh, func(context.Context, appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanup()

	// The remove (generation 1) and then a refresh of "kept" (generation 2)
	// are in flight when the removal lands with an applied snapshot.
	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removed, kept),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcileGeneration: 1,
	}
	issued, delayedRefresh := m.handleMarketplaceRefresh(launchconfig.MarketplaceRefreshMsg{Name: kept.Name})
	m = issued.(hubModel)
	if delayedRefresh == nil {
		t.Fatal("refresh did not create a delayed mutation command")
	}

	got, replacement := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err: marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
			EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
			Applied:         appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
		}),
		Action:     "remove",
		Name:       removed.Name,
		Generation: 1,
	})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("applied settlement state = %q/%v, want cleared fence", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	if after.marketplaceOutcomeWarning == nil {
		t.Fatal("applied settlement did not leave its warning standing")
	}
	if replacement == nil {
		t.Fatal("applied settlement should schedule its replacement read past the advancing floor")
	}

	// The refresh's response still carries the removed marketplace. Issued
	// below the floor the settlement just armed, it is discarded wholesale
	// - and a discarded response is not the model's news, so the
	// clone-remains warning must survive it exactly as it survives every
	// list read.
	delayed := delayedRefresh().(launchconfig.MarketplaceMutateResultMsg)
	if delayed.Generation != 2 {
		t.Fatalf("delayed refresh generation = %d, want request generation 2", delayed.Generation)
	}
	got, _ = after.handleMarketplaceMutateResult(delayed)
	after = got.(hubModel)
	if after.marketplaceOutcomeWarning == nil || !strings.Contains(after.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("stale refresh cleared the applied warning = %v", after.marketplaceOutcomeWarning)
	}
	updated, cmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale refresh replaced the applied marketplace panel")
	}
	if remove := cmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("stale refresh selected %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceDelayedRefreshAfterFreshReconciliationKeepsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	kept := appwire.MarketplaceEntry{Name: "kept", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceRefresh, func(context.Context, appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{removed, kept}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}}, nil
		})
	})
	defer cleanup()

	m := hubModel{
		client:                         client,
		pluginsPanel:                   marketplacePanelWithEntries(t, removed),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcileGeneration: 1,
	}
	issued, delayedRefresh := m.handleMarketplaceRefresh(launchconfig.MarketplaceRefreshMsg{Name: kept.Name})
	m = issued.(hubModel)
	if delayedRefresh == nil {
		t.Fatal("refresh did not create a delayed mutation command")
	}
	got, reconcile := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err:        marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains, AppliedUnavailable: true}),
		Action:     "remove",
		Name:       removed.Name,
		Generation: 1,
	})
	after := got.(hubModel)
	if reconcile == nil || !after.marketplaceReconcilePending {
		t.Fatal("unavailable removal did not start reconciliation")
	}
	fresh := reconcile().(launchconfig.MarketplaceListResultMsg)
	got, _ = after.handleMarketplaceListResult(fresh)
	after = got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("fresh reconciliation state = %q/%v, want cleared fence", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	if after.marketplaceOutcomeWarning == nil {
		t.Fatal("fresh reconciliation cleared the unavailable warning")
	}

	delayed := delayedRefresh().(launchconfig.MarketplaceMutateResultMsg)
	if delayed.Generation != 2 {
		t.Fatalf("delayed refresh generation = %d, want request generation 2", delayed.Generation)
	}
	got, _ = after.handleMarketplaceMutateResult(delayed)
	after = got.(hubModel)
	if after.marketplaceOutcomeWarning == nil || !strings.Contains(after.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("stale refresh cleared the unavailable warning = %v", after.marketplaceOutcomeWarning)
	}
	updated, cmd := after.pluginsPanel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil || updated.(launchconfig.PluginsPanel).Done() {
		t.Fatal("stale refresh replaced the reconciled marketplace panel")
	}
	if remove := cmd().(launchconfig.MarketplaceRemoveMsg); remove.Name != kept.Name {
		t.Fatalf("stale refresh resurrected %q, want %q", remove.Name, kept.Name)
	}
}

func TestMarketplaceOrdinaryFailureDoesNotOverwriteCloneRemainsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	kept := appwire.MarketplaceEntry{Name: "kept", LastUpdated: 1, Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	m := hubModel{
		pluginsPanel:                   marketplacePanelWithEntries(t, removed, kept),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcileGeneration: 1,
	}
	err := marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
		Applied:         appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
	})

	// The removal lands applied-with-litter: the fence clears and the
	// clone-remains warning stands on its own account.
	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err, Action: "remove", Name: removed.Name, Generation: 1})
	after := got.(hubModel)
	if after.marketplaceRemovePending != "" || after.marketplaceReconcilePending {
		t.Fatalf("applied settlement state = %q/%v, want cleared fence", after.marketplaceRemovePending, after.marketplaceReconcilePending)
	}
	if after.marketplaceOutcomeWarning == nil || !strings.Contains(after.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("applied settlement did not leave its warning standing = %v", after.marketplaceOutcomeWarning)
	}

	// A refresh of the surviving marketplace fails: the failure is the
	// fresh news and owns the transient account, but it is news about the
	// refresh, never about the removal. The standing clone-remains account
	// must survive it - both accounts are live and the panel co-renders
	// them - or the litter goes unreported until the next removal happens
	// to mention it.
	got, _ = after.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Err:        errors.New("refresh failed"),
		Action:     "refresh",
		Name:       kept.Name,
		Generation: 3,
	})
	failed := got.(hubModel)
	if failed.err == nil || !strings.Contains(failed.err.Error(), "refresh failed") {
		t.Fatalf("failed refresh did not surface its failure = %v", failed.err)
	}
	if failed.marketplaceOutcomeWarning == nil || !strings.Contains(failed.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("failed refresh erased the standing clone-remains warning = %v", failed.marketplaceOutcomeWarning)
	}
}

func TestMarketplaceSuccessfulRemoveKeepsEarlierCloneRemainsWarning(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	other := appwire.MarketplaceEntry{Name: "other", LastUpdated: 1, Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	m := hubModel{
		pluginsPanel:                   marketplacePanelWithEntries(t, removed, other),
		marketplaceRemovePending:       removed.Name,
		marketplaceReconcileGeneration: 1,
	}
	err := marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
		EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
		Applied:         appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{other}},
	})

	// Removal A lands applied-with-litter: the fence clears and A's
	// clone-remains warning stands on its own account.
	got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err, Action: "remove", Name: removed.Name, Generation: 1})
	after := got.(hubModel)
	if after.marketplaceOutcomeWarning == nil || !strings.Contains(after.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("applied settlement did not leave its warning standing = %v", after.marketplaceOutcomeWarning)
	}

	// Another client's successful remove of a different marketplace is
	// fresh post-floor news, but only the account it supersedes - the
	// transient one - is its to clear: A's clone files are still on disk,
	// and nothing about B's removal re-derives that fact.
	got, _ = after.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{
		Action:     "remove",
		Name:       other.Name,
		Generation: 3,
		List:       appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{}},
	})
	settled := got.(hubModel)
	if settled.marketplaceRemovePending != "" {
		t.Fatalf("successful remove left the fence at %q, want cleared", settled.marketplaceRemovePending)
	}
	if settled.err != nil {
		t.Fatalf("successful remove should clear only the transient account it supersedes: %v", settled.err)
	}
	if settled.marketplaceOutcomeWarning == nil || !strings.Contains(settled.marketplaceOutcomeWarning.Error(), "clone files remain") {
		t.Fatalf("successful remove of another marketplace erased removal A's standing warning = %v", settled.marketplaceOutcomeWarning)
	}
}

func TestMarketplaceOutcomeWarningSurvivesUnrelatedClears(t *testing.T) {
	removed := appwire.MarketplaceEntry{Name: "removed", Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	kept := appwire.MarketplaceEntry{Name: "kept", LastUpdated: 1, Source: appwire.MarketplaceSourceInput{Kind: "url"}}
	for name, apply := range map[string]func(hubModel) hubModel{
		// A plugin mutation's success clears the transient account its
		// failure would have raised - never the removal's standing one.
		"plugin mutation success": func(m hubModel) hubModel {
			got, _ := m.handlePluginMutateResult(launchconfig.PluginMutateResultMsg{})
			return got.(hubModel)
		},
		// A hub tree read's success clears the transient account its own
		// failure would have raised - the removal's account is not the
		// tree's to retire.
		"hub tree read success": func(m hubModel) hubModel {
			got, _ := m.Update(hubTreeMsg{tree: hubTreeResponse{}})
			return got.(hubModel)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := hubModel{
				pluginsPanel:                   marketplacePanelWithEntries(t, removed, kept),
				marketplaceRemovePending:       removed.Name,
				marketplaceReconcileGeneration: 1,
			}
			err := marketplaceCloneRemainsError(appwire.MarketplaceUnregisteredCloneRemainsData{
				EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
				Applied:         appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{kept}},
			})
			got, _ := m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err, Action: "remove", Name: removed.Name, Generation: 1})
			after := got.(hubModel)
			if after.marketplaceOutcomeWarning == nil || !strings.Contains(after.marketplaceOutcomeWarning.Error(), "clone files remain") {
				t.Fatalf("applied settlement did not leave its warning standing = %v", after.marketplaceOutcomeWarning)
			}

			settled := apply(after)
			if settled.marketplaceOutcomeWarning == nil || !strings.Contains(settled.marketplaceOutcomeWarning.Error(), "clone files remain") {
				t.Fatalf("%s erased the standing clone-remains warning = %v", name, settled.marketplaceOutcomeWarning)
			}
		})
	}
}

func TestProminentErrorsStacksTransientBeforeOutcomeWarning(t *testing.T) {
	transient := errors.New("boom")
	durable := errors.New("marketplace removed; clone files remain on disk")

	// Nil-safety: a model with no live account renders nothing.
	if errs := (hubModel{}).prominentErrors(); len(errs) != 0 {
		t.Fatalf("empty model prominentErrors = %v, want none", errs)
	}
	// Single-account degeneracy: each slot alone is the only account.
	if errs := (hubModel{err: transient}).prominentErrors(); len(errs) != 1 || !errors.Is(errs[0], transient) {
		t.Fatalf("transient-only prominentErrors = %v, want exactly the transient", errs)
	}
	if errs := (hubModel{marketplaceOutcomeWarning: durable}).prominentErrors(); len(errs) != 1 || !errors.Is(errs[0], durable) {
		t.Fatalf("durable-only prominentErrors = %v, want exactly the outcome warning", errs)
	}
	// Stacked order: transient first, then the durable warning.
	errs := (hubModel{err: transient, marketplaceOutcomeWarning: durable}).prominentErrors()
	if len(errs) != 2 || !errors.Is(errs[0], transient) || !errors.Is(errs[1], durable) {
		t.Fatalf("stacked prominentErrors = %v, want transient then outcome warning", errs)
	}

	// The status text degenerates to today's byte-identical returns with
	// one live account, and joins transient-then-durable when both live,
	// keeping the prominent-accounts-over-sessionStatusError preference.
	if got := (hubModel{err: transient, sessionStatusError: "stale"}).sessionStatusErrorText(); got != "boom" {
		t.Fatalf("transient-only status text = %q, want %q", got, "boom")
	}
	if got := (hubModel{marketplaceOutcomeWarning: durable}).sessionStatusErrorText(); got != durable.Error() {
		t.Fatalf("durable-only status text = %q, want %q", got, durable.Error())
	}
	if got := (hubModel{err: transient, marketplaceOutcomeWarning: durable, sessionStatusError: "stale"}).sessionStatusErrorText(); got != "boom; "+durable.Error() {
		t.Fatalf("stacked status text = %q, want transient then outcome warning", got)
	}
	if got := (hubModel{sessionStatusError: " stale "}).sessionStatusErrorText(); got != "stale" {
		t.Fatalf("no-account status text = %q, want the trimmed session status error", got)
	}

	// Single-account degeneracy at the render sites: with exactly one
	// live account the emitted bytes equal today's single-slot emission.
	width := 100
	wantDashboard := tuitext.TruncateText(fmt.Sprintf("error: %v", transient), width) + "\n\n"
	m := newHubModel(nil, "http://hub.test")
	m.width = width
	m.height = 24
	m.err = transient
	view := m.dashboardView()
	if !strings.Contains(view, wantDashboard) {
		t.Fatalf("single-account dashboard is not byte-identical to the single-slot emission; want %q:\n%s", wantDashboard, view)
	}
	if strings.Count(view, "error: boom") != 1 {
		t.Fatalf("single-account dashboard rendered the account more than once:\n%s", view)
	}
	if got := m.dashboardDetailsView(nil, width); got != renderDetailsPane(strings.Join([]string{
		"details",
		"Diagnostic",
		"Message:  boom",
		"Next:     refresh dashboard or check Hub health",
	}, "\n"), width) {
		t.Fatalf("single-account details pane is not byte-identical to the single-slot emission:\n%s", got)
	}

	// Stacked: two accounts render two error lines, transient first, and
	// the details pane composes both messages.
	m.marketplaceOutcomeWarning = durable
	view = m.dashboardView()
	wantStacked := tuitext.TruncateText(fmt.Sprintf("error: %v", transient), width) + "\n" +
		tuitext.TruncateText(fmt.Sprintf("error: %v", durable), width) + "\n\n"
	if !strings.Contains(view, wantStacked) {
		t.Fatalf("stacked dashboard did not render transient then outcome warning; want %q:\n%s", wantStacked, view)
	}
	if got, want := m.dashboardDetailsView(nil, width), renderDetailsPane(strings.Join([]string{
		"details",
		"Diagnostic",
		"Message:  boom\nMessage:  " + durable.Error(),
		"Next:     refresh dashboard or check Hub health",
	}, "\n"), width); got != want {
		t.Fatalf("stacked details pane = %q, want both accounts' messages composed:\n%s", got, want)
	}

	// The session and spawn views emit one stacked "error: %v" line per
	// account, in prominentErrors order.
	s := newSessionHubModel(nil)
	s.width = width
	s.height = 24
	s.err = transient
	s.marketplaceOutcomeWarning = durable
	sessionBody := s.renderSessionMainBody()
	if !strings.Contains(sessionBody, "\nerror: boom\n\nerror: "+durable.Error()+"\n") {
		t.Fatalf("session body did not stack transient then outcome warning:\n%s", sessionBody)
	}

	sp := newHubModel(nil, "http://hub.test")
	sp.openSpawnForm()
	sp.width = width
	sp.height = 30
	sp.err = transient
	sp.marketplaceOutcomeWarning = durable
	spawnView := sp.spawnView()
	if !strings.Contains(spawnView, "\nerror: boom\n\nerror: "+durable.Error()+"\n") {
		t.Fatalf("spawn view did not stack transient then outcome warning:\n%s", spawnView)
	}
}
